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
	PeersDirect         *int64 // last-reported count of direct (P2P) peers; nil if never reported
	PeersRelayed        *int64 // last-reported count of relay-routed peers; nil if never reported
	PeersReportedAt     *int64 // unix seconds of the last heartbeat that carried peer state
	PeerState           *string // JSON array: [{vpnAddr, direct, remoteAddr, rttMs}, ...]; nil if never reported
	AgentVersion        *string // self-reported hop-agent build (e.g. "v0.9.15"); nil for pre-v0.9.15 agents
	ClientType          *string // "desktop" | "cli" | nil; build-baked at the agent, written once via SetClientType
	CreatedBy           string  // user who brought the node in; written by Create, read by CreatedBy
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

	// lastProxyHeartbeat tracks the last time RecordProxyActivity fired
	// for a given nodeID. Used to throttle the proxy-liveness signal so
	// that a high-frequency proxy path (e.g., many TCP connections
	// through a port-forward) doesn't churn the heartbeats map at
	// connection rate. Key: nodeID, Value: time.Time of last fire.
	lastProxyHeartbeat sync.Map

	// lastLoggedStatus tracks the last "online"|"offline" state we
	// persisted to network_events for a node, so that the caller can
	// suppress redundant logs when a heartbeat lands on an already-
	// online node. Populated lazily — the first heartbeat after server
	// start is always logged as a transition. Key: nodeID, Value: string.
	lastLoggedStatus sync.Map
}

// StatusTransition reports whether the given heartbeat constitutes a
// state change relative to the last logged status for this node, and
// updates the internal tracker if so. Returns (true, "online") when
// we should log an online transition; (false, "") when the status is
// unchanged. Callers use this to decide whether to persist a
// node.status event alongside the WS publish.
func (s *NodeStore) StatusTransition(nodeID, newStatus string) bool {
	prev, loaded := s.lastLoggedStatus.LoadOrStore(nodeID, newStatus)
	if !loaded {
		return true
	}
	if prev.(string) == newStatus {
		return false
	}
	s.lastLoggedStatus.Store(nodeID, newStatus)
	return true
}

// MarkOfflineOnStale transitions a currently-online node to offline in
// the database when its last_seen_at is older than the stale threshold.
// Called by the sweeper for each row returned from ScanStaleOnlineNodes.
// Returns true if this was a real online→offline transition (so the
// caller should also log it).
func (s *NodeStore) MarkOfflineOnStale(nodeID string) bool {
	q := dbsqlc.New(WrapDB(s.wdb))
	if err := q.UpdateNodeStatus(context.Background(), dbsqlc.UpdateNodeStatusParams{
		Status: "offline",
		ID:     nodeID,
	}); err != nil {
		log.Printf("[sweep] mark offline %s: %v", nodeID, err)
		return false
	}
	return s.StatusTransition(nodeID, "offline")
}

// StaleOnlineNodes returns nodes still marked "online" in the DB whose
// last_seen_at is older than cutoff (unix seconds). Used by the
// transition sweeper to flip stale rows to "offline" + emit a log
// event. Lighthouses are excluded (they heartbeat via the mesh, not
// the control-plane heartbeat endpoint).
type StaleNode struct {
	ID         string
	NetworkID  string
	Hostname   string
	LastSeenAt *int64
}

func (s *NodeStore) StaleOnlineNodes(cutoff int64) ([]StaleNode, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ScanStaleOnlineNodes(context.Background(), &cutoff)
	if err != nil {
		return nil, err
	}
	out := make([]StaleNode, 0, len(rows))
	for _, r := range rows {
		out = append(out, StaleNode{
			ID:         r.ID,
			NetworkID:  r.NetworkID,
			Hostname:   r.Hostname,
			LastSeenAt: r.LastSeenAt,
		})
	}
	return out, nil
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

	tx, err := s.wdb.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	q := dbsqlc.New(WrapTx(tx))
	err = q.InsertNode(context.Background(), dbsqlc.InsertNodeParams{
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
	if err != nil {
		return err
	}
	if n.CreatedBy != "" {
		if _, err := tx.Exec(`UPDATE nodes SET created_by = ? WHERE id = ?`, n.CreatedBy, n.ID); err != nil {
			return fmt.Errorf("record creator: %w", err)
		}
	}
	if len(n.NebulaCert) > 0 {
		if err := recordCert(tx, n.ID, n.NebulaCert, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CreatedBy returns the user who brought the node in, "" if unknown.
func (s *NodeStore) CreatedBy(id string) (string, error) {
	var userID sql.NullString
	err := s.rdb.QueryRow(`SELECT created_by FROM nodes WHERE id = ?`, id).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return userID.String, err
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
		AgentVersion:    row.AgentVersion,
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
			ID:              r.ID,
			NetworkID:       r.NetworkID,
			Hostname:        r.Hostname,
			OS:              r.Os,
			Arch:            r.Arch,
			AgentRealIP:     r.AgentRealIp,
			NodeType:        r.NodeType,
			ExposedPorts:    r.ExposedPorts,
			DNSName:         r.DnsName,
			Capabilities:    r.Capabilities,
			Status:          r.Status,
			LastSeenAt:      r.LastSeenAt,
			CreatedAt:       r.CreatedAt,
			PeersDirect:     r.PeersDirect,
			PeersRelayed:    r.PeersRelayed,
			PeersReportedAt: r.PeersReportedAt,
			PeerState:       r.PeerState,
			AgentVersion:    r.AgentVersion,
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

// releasedIPQuarantine is how long a deleted node's address stays out of use
// once the network is full: twice the 24 h a node certificate lives, for
// clocks that are off.
const releasedIPQuarantine = 48 * time.Hour

// ErrNetworkFull: every address of the network's /24 is in use, or was
// released too recently to give out again.
var ErrNetworkFull = errors.New("no free mesh address in this network")

// maxNodeOctet is the last address of a /24 a node can have (.1 is the
// lighthouse, .255 the broadcast address).
const maxNodeOctet = 254

// NextNodeIndex returns the next available host index for a network's subnet
// (pki.NodeAddress adds 2). It never goes below the highest address ever
// used, deleted nodes included (released_node_ips): a deleted node's
// certificate stays valid until it expires. Once the /24 is full it gives the
// lowest address that is free and was released longer ago than
// releasedIPQuarantine.
// Uses the write DB to serialize with concurrent node creation (prevents IP collisions).
func (s *NodeStore) NextNodeIndex(networkID string) (int, error) {
	ctx := context.Background()
	q := dbsqlc.New(WrapDB(s.wdb))
	live, err := q.ListNodeIPsForNetwork(ctx, networkID)
	if err != nil {
		return 0, err
	}
	held := map[int]bool{}
	highest := 1
	for _, ip := range live {
		if ip != nil {
			octet := parseLastOctet(*ip)
			held[octet] = true
			highest = max(highest, octet)
		}
	}

	rows, err := s.wdb.QueryContext(ctx,
		`SELECT nebula_ip, released_at FROM released_node_ips WHERE network_id = ?`, networkID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	recent := time.Now().Add(-releasedIPQuarantine).Unix()
	for rows.Next() {
		var ip string
		var at int64
		if err := rows.Scan(&ip, &at); err != nil {
			return 0, err
		}
		octet := parseLastOctet(ip)
		highest = max(highest, octet)
		if at > recent {
			held[octet] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if highest < maxNodeOctet {
		return highest - 1, nil
	}
	for octet := 2; octet <= maxNodeOctet; octet++ {
		if !held[octet] {
			return octet - 2, nil
		}
	}
	return 0, ErrNetworkFull
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

// CompleteEnrollment sets the node's cert/key after enrollment and records
// the cert (node_certs), in one transaction. ErrNodeGone when the node was
// deleted meanwhile: the cert must not be handed out then.
func (s *NodeStore) CompleteEnrollment(id string, cert, key []byte, hostname, os, arch string) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	return s.writeCert(id, cert, `UPDATE nodes SET nebula_cert = ?, nebula_key = ?, hostname = ?, os = ?, arch = ?,
		status = 'enrolled' WHERE id = ?`, cert, encKey, hostname, os, arch, id)
}

// UpdateCert replaces the node's Nebula certificate and key (for cert
// rotation) and records the cert, in one transaction. ErrNodeGone when the
// node was deleted meanwhile, ErrTooManyCerts when it holds MaxHeldCerts:
// the cert must not be handed out then.
func (s *NodeStore) UpdateCert(id string, cert, key []byte) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	return s.writeCert(id, cert, `UPDATE nodes SET nebula_cert = ?, nebula_key = ? WHERE id = ?`, cert, encKey, id)
}

// writeCert runs update, which stores cert on node id, and records cert.
// A cert the blocklist would not know is never stored: if the node is gone,
// holds MaxHeldCerts, or the cert cannot be read, nothing is written.
func (s *NodeStore) writeCert(id string, cert []byte, update string, args ...any) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(update, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNodeGone
	}
	now := time.Now()
	if held, err := heldCerts(tx, id, now); err != nil {
		return err
	} else if held >= MaxHeldCerts {
		return ErrTooManyCerts
	}
	if err := recordCert(tx, id, cert, now); err != nil {
		return err
	}
	return tx.Commit()
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

// NodeRoute is one entry in a node's `routes` JSON column. Roadmap #5
// (subnet routing): the node will route mesh traffic to this CIDR.
// Schema is shaped as an array-of-objects to leave room for extra
// per-route knobs (metric, mtu) without another migration.
type NodeRoute struct {
	Route string `json:"route"`
}

// UpdateRoutes sets the per-node route list. routes==nil clears the
// column (NULL) — equivalent to "this node is no longer a gateway."
// Caller must validate CIDRs before passing in.
func (s *NodeStore) UpdateRoutes(id string, routes []NodeRoute) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	if len(routes) == 0 {
		return q.UpdateNodeRoutes(context.Background(), dbsqlc.UpdateNodeRoutesParams{
			Routes: nil,
			ID:     id,
		})
	}
	b, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	js := string(b)
	return q.UpdateNodeRoutes(context.Background(), dbsqlc.UpdateNodeRoutesParams{
		Routes: &js,
		ID:     id,
	})
}

// GetRoutes returns the per-node route list. Returns (nil, nil) when
// the node has no routes configured.
func (s *NodeStore) GetRoutes(id string) ([]NodeRoute, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	raw, err := q.GetNodeRoutes(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("get node routes: %w", err)
	}
	if raw == nil || *raw == "" {
		return nil, nil
	}
	var routes []NodeRoute
	if err := json.Unmarshal([]byte(*raw), &routes); err != nil {
		return nil, fmt.Errorf("decode node routes: %w", err)
	}
	return routes, nil
}

func (s *NodeStore) Rename(id, hostname, dnsName string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.RenameNode(context.Background(), dbsqlc.RenameNodeParams{
		DnsName:  &dnsName,
		Hostname: hostname,
		ID:       id,
	})
}

// ClientTypesForNetwork returns a map from node ID to client_type for
// every node in the network where client_type is non-null. Used by
// GetNetwork to inject the field into NodeResponse without breaking
// the sqlc abstraction (the existing ListForNetwork doesn't yet
// SELECT client_type because regenerating sqlc adds friction). Cheap
// — a single network has < 100 nodes typically.
func (s *NodeStore) ClientTypesForNetwork(networkID string) map[string]string {
	rows, err := s.rdb.Query(
		`SELECT id, client_type FROM nodes WHERE network_id = ? AND client_type IS NOT NULL AND client_type != ''`,
		networkID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, ct string
		if err := rows.Scan(&id, &ct); err != nil {
			continue
		}
		out[id] = ct
	}
	return out
}

// SetClientType updates the client_type column for a node. Called from
// the heartbeat handler when the agent reports its build-baked client
// type ("desktop" or "cli"). Cheap UPDATE-by-id; bypasses the buffered
// heartbeat path because client_type is build-baked and rarely changes
// — the cost is one write per heartbeat which is fine at our scale.
// Idempotent (re-writes the same value most of the time).
func (s *NodeStore) SetClientType(nodeID, clientType string) error {
	if clientType == "" {
		return nil
	}
	_, err := s.wdb.Exec(
		`UPDATE nodes SET client_type = ? WHERE id = ?`,
		clientType, nodeID,
	)
	return err
}

// SetPlatform records the OS and CPU the agent reports about its own
// machine. Enrollment only guesses — the token body said "linux" for
// every machine until 72f2165, and the device flow falls back to the
// server's own OS — so the heartbeat is where the truth arrives, and
// a node that enrolled wrong is corrected by its next beat. An empty
// value leaves that column alone: an old agent omits the field, and "no
// opinion" must not erase what a newer one reported.
func (s *NodeStore) SetPlatform(nodeID, os, arch string) error {
	if os == "" && arch == "" {
		return nil
	}
	_, err := s.wdb.Exec(
		`UPDATE nodes SET os = COALESCE(NULLIF(?, ''), os), arch = COALESCE(NULLIF(?, ''), arch) WHERE id = ?`,
		os, arch, nodeID,
	)
	return err
}

func (s *NodeStore) Delete(id string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNode(context.Background(), id)
}

// PruneOfflineSince hard-deletes offline nodes whose last_seen_at is
// older than cutoff (unix seconds), scoped to a single network. Used
// by the dashboard's "Clean up offline nodes" admin button + the
// hourly sweeper goroutine to garbage-collect zombie rows from
// re-enrollments. Returns the number of rows deleted.
//
// The schema has no unique constraint on (network_id, hostname), so
// each `hop-agent enroll` creates a fresh UUID node row. Without
// pruning, a Mac that's been re-enrolled five times leaves five
// rows on the server forever — they show up as gray ghosts in the
// topology graph and inflate the Nodes-tab counter.
//
// Hard-delete (consistent with NodeStore.Delete) — soft-delete adds
// API surface without value here. Callers expect the row to actually
// disappear from subsequent ListForNetwork responses.
func (s *NodeStore) PruneOfflineSince(networkID string, cutoff int64) (int64, error) {
	res, err := s.wdb.Exec(
		`DELETE FROM nodes WHERE network_id = ? AND status = 'offline' AND last_seen_at < ?`,
		networkID, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("prune offline nodes: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneOfflineSinceAllNetworks is the cross-network variant called by
// the periodic sweeper. Cheap on a single SQLite write since the
// WHERE has no joins. Audit trail isn't emitted here — the sweep is
// background maintenance, not a user action; per-network pruning
// triggered from the dashboard goes through the audit-logging API
// handler.
func (s *NodeStore) PruneOfflineSinceAllNetworks(cutoff int64) (int64, error) {
	res, err := s.wdb.Exec(
		`DELETE FROM nodes WHERE status = 'offline' AND last_seen_at < ?`,
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("prune offline nodes (all): %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *NodeStore) DeleteForNetwork(networkID string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNodesForNetwork(context.Background(), networkID)
}

// heartbeatEntry is the buffered row written on each flush. agent_real_ip
// merges via COALESCE(NULLIF(...)), so empty ip preserves the prior value.
// peersDirect / peersRelayed / peerState are nullable: heartbeats that
// don't carry peer state (e.g., userspace agents without Nebula Control
// access, or agents mid-startup) leave the prior value in place via
// COALESCE server-side.
type heartbeatEntry struct {
	ip           string
	peersDirect  *int64
	peersRelayed *int64
	peerState    *string // JSON blob: [{vpnAddr, direct, remoteAddr, ...}, ...]
	agentVersion *string // e.g. "v0.9.15"; nil when caller has no version to report
}

// RecordHeartbeat buffers a heartbeat for batch writing. Non-blocking.
// If ip is empty, only last_seen_at is updated (agent_real_ip preserved).
// peersDirect / peersRelayed / peerState / agentVersion are optional;
// pass nil when the caller has no value to report this round. Coalesces:
// if the same node heartbeats multiple times between flushes, only the
// latest values are kept.
func (s *NodeStore) RecordHeartbeat(nodeID, ip string, peersDirect, peersRelayed *int64, peerState *string, agentVersion *string) {
	s.heartbeats.Store(nodeID, heartbeatEntry{
		ip:           ip,
		peersDirect:  peersDirect,
		peersRelayed: peersRelayed,
		peerState:    peerState,
		agentVersion: agentVersion,
	})
}

// proxyHeartbeatThrottle caps the rate at which proxy-traffic events
// (shell / exec / port-forward / health) can refresh a node's
// last_seen_at. The underlying `heartbeats` map already coalesces and
// the 5s flush bounds DB writes, so this is a CPU optimization for
// high-frequency proxy paths (e.g., a busy port-forward accepting
// many TCP connections) — not a correctness mechanism. 30s is well
// below the 180s stale threshold, so long-running interactive sessions
// still keep nodes marked online.
const proxyHeartbeatThrottle = 30 * time.Second

// RecordProxyActivity records a liveness beat triggered by a successful
// mesh-proxy interaction (shell opened, exec completed, port-forward
// established, health probe returned 2xx). Unlike the agent's own
// heartbeat, this fires from the control-plane side whenever we see
// the agent respond — so an agent whose outbound heartbeat is broken
// (blocked firewall, TLS glitch) but still serves proxy traffic stays
// correctly marked "online" in the dashboard.
//
// Throttled per nodeID so a high-throughput proxy (many short TCP
// connections through a port-forward) doesn't churn the sync.Map at
// connection rate.
func (s *NodeStore) RecordProxyActivity(nodeID string) {
	now := time.Now()
	if last, ok := s.lastProxyHeartbeat.Load(nodeID); ok {
		if now.Sub(last.(time.Time)) < proxyHeartbeatThrottle {
			return
		}
	}
	s.lastProxyHeartbeat.Store(nodeID, now)
	s.RecordHeartbeat(nodeID, "", nil, nil, nil, nil)
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
		id           string
		ip           string
		peersDirect  *int64
		peersRelayed *int64
		peerState    *string
		agentVersion *string
	}
	var batch []entry
	s.heartbeats.Range(func(key, value any) bool {
		e := value.(heartbeatEntry)
		batch = append(batch, entry{
			id:           key.(string),
			ip:           e.ip,
			peersDirect:  e.peersDirect,
			peersRelayed: e.peersRelayed,
			peerState:    e.peerState,
			agentVersion: e.agentVersion,
		})
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
			AgentRealIp:  e.ip,
			PeersDirect:  e.peersDirect,
			PeersRelayed: e.peersRelayed,
			PeerState:    e.peerState,
			AgentVersion: e.agentVersion,
			ID:           e.id,
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
