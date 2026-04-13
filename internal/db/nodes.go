package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

// hashToken returns the hex-encoded SHA-256 hash of a token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

type Node struct {
	ID                  string
	NetworkID           string
	Hostname            string
	OS                  string
	Arch                string
	NebulaCert          []byte  // PEM, plaintext
	NebulaKey           []byte  // PEM, plaintext (encrypted on disk)
	NebulaIP            string  // e.g. "10.42.1.2/24"
	AgentToken          string
	EnrollmentToken     *string // one-time, nulled after use (SHA-256 hashed at rest)
	EnrollmentExpiresAt *int64  // TTL for enrollment token
	AgentRealIP         *string
	NodeType            string   // "node" or "lighthouse"
	ExposedPorts        *string  // JSON: [{"port":8096,"proto":"tcp","name":"Jellyfin"}]
	DNSName             *string  // custom DNS hostname
	Capabilities        string   // JSON: ["terminal","health","forward"]
	Status              string   // pending, enrolled, online, offline
	LastSeenAt          *int64
	CreatedAt           int64
}

// HasCapability checks if a node has a specific capability enabled.
func (n *Node) HasCapability(cap string) bool {
	// Fast path for common defaults.
	if n.Capabilities == "" || n.Capabilities == "null" {
		return false
	}
	var caps []string
	if err := json.Unmarshal([]byte(n.Capabilities), &caps); err != nil {
		return false
	}
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// heartbeatFlushInterval is how often buffered heartbeats are flushed to SQLite.
const heartbeatFlushInterval = 5 * time.Second

type NodeStore struct {
	rdb *sql.DB
	wdb *sql.DB
	enc *crypto.Encryptor

	// heartbeats coalesces pending UpdateLastSeen + UpdateAgentRealIP.
	// Key: nodeID (string), Value: string (latest agent IP, "" = no IP update).
	// sync.Map for lock-free concurrent writes from heartbeat handlers.
	heartbeats sync.Map
}

func NewNodeStore(p *DBPair, enc *crypto.Encryptor) *NodeStore {
	return &NodeStore{rdb: p.ReadDB, wdb: p.WriteDB, enc: enc}
}

func (s *NodeStore) Create(n *Node) error {
	var encKey []byte
	if len(n.NebulaKey) > 0 {
		var err error
		encKey, err = s.enc.EncryptBytes(n.NebulaKey)
		if err != nil {
			return fmt.Errorf("encrypt node key: %w", err)
		}
	}

	encToken, err := s.enc.Encrypt(n.AgentToken)
	if err != nil {
		return fmt.Errorf("encrypt agent token: %w", err)
	}

	var enrollHash *string
	if n.EnrollmentToken != nil {
		h := hashToken(*n.EnrollmentToken)
		enrollHash = &h
	}

	var nebulaIP *string
	if n.NebulaIP != "" {
		nebulaIP = &n.NebulaIP
	}

	nodeType := n.NodeType
	if nodeType == "" {
		nodeType = "agent"
	}

	q := dbsqlc.New(WrapDB(s.wdb))
	return q.InsertNode(context.Background(), dbsqlc.InsertNodeParams{
		ID:                  n.ID,
		NetworkID:           n.NetworkID,
		Hostname:            n.Hostname,
		Os:                  n.OS,
		Arch:                n.Arch,
		NebulaCert:          n.NebulaCert,
		NebulaKey:           encKey,
		NebulaIp:            nebulaIP,
		AgentToken:          string(encToken),
		EnrollmentToken:     enrollHash,
		EnrollmentExpiresAt: n.EnrollmentExpiresAt,
		NodeType:            nodeType,
		DnsName:             n.DNSName,
		Status:              n.Status,
	})
}

func (s *NodeStore) Get(id string) (*Node, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	row, err := q.GetNodeByID(context.Background(), id)
	if isLockError(err) {
		// Retry once — see NetworkStore.Get for rationale.
		time.Sleep(100 * time.Millisecond)
		row, err = q.GetNodeByID(context.Background(), id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	return s.mapNodeRow(row)
}

func (s *NodeStore) mapNodeRow(row dbsqlc.GetNodeByIDRow) (*Node, error) {
	n := &Node{
		ID:              row.ID,
		NetworkID:       row.NetworkID,
		Hostname:        row.Hostname,
		OS:              row.Os,
		Arch:            row.Arch,
		NebulaCert:      row.NebulaCert,
		EnrollmentToken: row.EnrollmentToken,
		AgentRealIP:     row.AgentRealIp,
		NodeType:        row.NodeType,
		ExposedPorts:    row.ExposedPorts,
		DNSName:         row.DnsName,
		Capabilities:    row.Capabilities,
		Status:          row.Status,
		LastSeenAt:      row.LastSeenAt,
		CreatedAt:       row.CreatedAt,
	}
	if row.NebulaIp != nil {
		n.NebulaIP = *row.NebulaIp
	}

	if len(row.NebulaKey) > 0 {
		var err error
		n.NebulaKey, err = s.enc.DecryptBytes(row.NebulaKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt node key: %w", err)
		}
	}
	if row.AgentToken != "" {
		decrypted, err := s.enc.Decrypt([]byte(row.AgentToken))
		if err != nil {
			return nil, fmt.Errorf("decrypt agent token: %w", err)
		}
		n.AgentToken = decrypted
	}
	return n, nil
}

// ClaimEnrollmentToken atomically looks up a node by enrollment token hash,
// NULLs the token (consuming it), and returns the node.
func (s *NodeStore) ClaimEnrollmentToken(token string) (*Node, error) {
	h := hashToken(token)
	ctx := context.Background()

	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	q := dbsqlc.New(WrapTx(tx))
	row, err := q.GetNodeByEnrollmentToken(ctx, dbsqlc.GetNodeByEnrollmentTokenParams{
		EnrollmentToken: &h,
		EnrollmentExpiresAt: func() *int64 { t := time.Now().Unix(); return &t }(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node by enrollment token: %w", err)
	}

	result, err := q.ConsumeEnrollmentToken(ctx, dbsqlc.ConsumeEnrollmentTokenParams{
		ID:              row.ID,
		EnrollmentToken: &h,
	})
	if err != nil {
		return nil, fmt.Errorf("consume enrollment token: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	n := &Node{
		ID:        row.ID,
		NetworkID: row.NetworkID,
		Hostname:  row.Hostname,
		OS:        row.Os,
		Arch:      row.Arch,
		Status:    row.Status,
	}
	if row.NebulaIp != nil {
		n.NebulaIP = *row.NebulaIp
	}
	if row.AgentToken != "" {
		n.AgentToken, err = s.enc.Decrypt([]byte(row.AgentToken))
		if err != nil {
			return nil, fmt.Errorf("decrypt agent token: %w", err)
		}
	}
	return n, nil
}

func (s *NodeStore) ListForNetwork(networkID string) ([]*Node, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListNodesForNetwork(context.Background(), networkID)
	if err != nil {
		return nil, err
	}

	nodes := make([]*Node, 0, len(rows))
	for _, r := range rows {
		n := &Node{
			ID:           r.ID,
			NetworkID:    r.NetworkID,
			Hostname:     r.Hostname,
			OS:           r.Os,
			Arch:         r.Arch,
			AgentRealIP:  r.AgentRealIp,
			NodeType:     r.NodeType,
			ExposedPorts: r.ExposedPorts,
			DNSName:      r.DnsName,
			Capabilities: r.Capabilities,
			Status:       r.Status,
			LastSeenAt:   r.LastSeenAt,
			CreatedAt:    r.CreatedAt,
		}
		if r.NebulaIp != nil {
			n.NebulaIP = *r.NebulaIp
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (s *NodeStore) CountForNetwork(networkID string) (int, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	count, err := q.CountNodesForNetwork(context.Background(), networkID)
	return int(count), err
}

// MaxLastSeenForNetwork returns the most recent last_seen_at for non-pending nodes, or nil if none.
func (s *NodeStore) MaxLastSeenForNetwork(networkID string) (*int64, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	result, err := q.MaxLastSeenForNetwork(context.Background(), networkID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	switch v := result.(type) {
	case int64:
		return &v, nil
	case float64:
		iv := int64(v)
		return &iv, nil
	}
	return nil, nil
}

// CountNonPendingForNetwork returns the count of non-pending nodes.
func (s *NodeStore) CountNonPendingForNetwork(networkID string) (int, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	count, err := q.CountNonPendingNodesForNetwork(context.Background(), networkID)
	return int(count), err
}

// NextNodeIndex returns the next available host index for a network's subnet.
// Uses the write DB to serialize with concurrent node creation (prevents IP collisions).
func (s *NodeStore) NextNodeIndex(networkID string) (int, error) {
	q := dbsqlc.New(WrapDB(s.wdb))
	rows, err := q.ListNodeIPsForNetwork(context.Background(), networkID)
	if err != nil {
		return 0, err
	}

	if len(rows) == 0 {
		return 0, nil
	}

	maxOctet := 1
	for _, ip := range rows {
		if ip != nil {
			octet := parseLastOctet(*ip)
			if octet > maxOctet {
				maxOctet = octet
			}
		}
	}
	return maxOctet - 1, nil
}

// parseLastOctet extracts the last octet from a CIDR like "10.42.1.3/24" → 3.
func parseLastOctet(cidr string) int {
	ip := cidr
	if idx := strings.Index(cidr, "/"); idx >= 0 {
		ip = cidr[:idx]
	}
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[3])
	return n
}

// CompleteEnrollment sets the node's cert/key after enrollment.
func (s *NodeStore) CompleteEnrollment(id string, cert, key []byte, hostname, os, arch string) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.CompleteEnrollment(context.Background(), dbsqlc.CompleteEnrollmentParams{
		NebulaCert: cert,
		NebulaKey:  encKey,
		Hostname:   hostname,
		Os:         os,
		Arch:       arch,
		ID:         id,
	})
}

// UpdateCert replaces the node's Nebula certificate and key (for cert rotation).
func (s *NodeStore) UpdateCert(id string, cert, key []byte) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.UpdateNodeCert(context.Background(), dbsqlc.UpdateNodeCertParams{
		NebulaCert: cert,
		NebulaKey:  encKey,
		ID:         id,
	})
}

func (s *NodeStore) UpdateStatus(id, status string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.UpdateNodeStatus(context.Background(), dbsqlc.UpdateNodeStatusParams{
		Status: status,
		ID:     id,
	})
}

func (s *NodeStore) UpdateLastSeen(id string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.UpdateNodeLastSeen(context.Background(), id)
}

func (s *NodeStore) UpdateAgentRealIP(id, ip string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.UpdateNodeAgentRealIP(context.Background(), dbsqlc.UpdateNodeAgentRealIPParams{
		AgentRealIp: &ip,
		ID:          id,
	})
}

func (s *NodeStore) UpdateDNSName(id, dnsName string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.UpdateNodeDNSName(context.Background(), dbsqlc.UpdateNodeDNSNameParams{
		DnsName: &dnsName,
		ID:      id,
	})
}

func (s *NodeStore) UpdateCapabilities(id string, caps []string) error {
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		return err
	}
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.UpdateNodeCapabilities(context.Background(), dbsqlc.UpdateNodeCapabilitiesParams{
		Capabilities: string(capsJSON),
		ID:           id,
	})
}

func (s *NodeStore) Rename(id, hostname, dnsName string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.RenameNode(context.Background(), dbsqlc.RenameNodeParams{
		DnsName:  &dnsName,
		Hostname: hostname,
		ID:       id,
	})
}

func (s *NodeStore) Delete(id string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNode(context.Background(), id)
}

func (s *NodeStore) DeleteForNetwork(networkID string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNodesForNetwork(context.Background(), networkID)
}

// RecordHeartbeat buffers a heartbeat for batch writing. Non-blocking.
// If ip is empty, only last_seen_at is updated (agent_real_ip preserved).
// Coalesces: if the same node heartbeats multiple times between flushes,
// only the latest IP is kept.
func (s *NodeStore) RecordHeartbeat(nodeID, ip string) {
	s.heartbeats.Store(nodeID, ip)
}

// StartHeartbeatFlusher starts periodic batch flushing of heartbeats.
func (s *NodeStore) StartHeartbeatFlusher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(heartbeatFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.FlushHeartbeats()
				return
			case <-ticker.C:
				s.FlushHeartbeats()
			}
		}
	}()
}

// FlushHeartbeats drains all pending heartbeats to the database in a
// single transaction. Safe to call from the shutdown path.
func (s *NodeStore) FlushHeartbeats() {
	type entry struct {
		id string
		ip string
	}
	var batch []entry
	s.heartbeats.Range(func(key, value any) bool {
		batch = append(batch, entry{id: key.(string), ip: value.(string)})
		s.heartbeats.Delete(key)
		return true
	})
	if len(batch) == 0 {
		return
	}

	tx, err := s.wdb.Begin()
	if err != nil {
		log.Printf("[heartbeat] begin tx: %v (dropping %d entries)", err, len(batch))
		return
	}
	q := dbsqlc.New(WrapTx(tx))
	for _, e := range batch {
		if err := q.HeartbeatNode(context.Background(), dbsqlc.HeartbeatNodeParams{
			AgentRealIp: e.ip,
			ID:          e.id,
		}); err != nil {
			log.Printf("[heartbeat] update %s: %v", e.id, err)
			tx.Rollback()
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[heartbeat] commit: %v (dropping %d entries)", err, len(batch))
	}
}
