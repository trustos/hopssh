package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/trustos/hopssh/internal/crypto"
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
	NebulaCert          []byte // PEM, plaintext
	NebulaKey           []byte // PEM, plaintext (encrypted on disk)
	NebulaIP            string // e.g. "10.42.1.2/24"
	AgentToken          string
	EnrollmentToken     *string // one-time, nulled after use (SHA-256 hashed at rest)
	EnrollmentExpiresAt *int64  // TTL for enrollment token
	AgentRealIP         *string
	Status              string // pending, online, offline
	LastSeenAt          *int64
	CreatedAt           int64
}

type NodeStore struct {
	rdb *sql.DB
	wdb *sql.DB
	enc *crypto.Encryptor
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

	// Hash enrollment token before storage (one-time use, only need to compare).
	var enrollHash *string
	if n.EnrollmentToken != nil {
		h := hashToken(*n.EnrollmentToken)
		enrollHash = &h
	}

	_, err = s.wdb.Exec(`
		INSERT INTO nodes (id, network_id, hostname, os, arch, nebula_cert, nebula_key, nebula_ip, agent_token, enrollment_token, enrollment_expires_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, n.ID, n.NetworkID, n.Hostname, n.OS, n.Arch, n.NebulaCert, encKey, n.NebulaIP, encToken, enrollHash, n.EnrollmentExpiresAt, n.Status)
	return err
}

func (s *NodeStore) Get(id string) (*Node, error) {
	var n Node
	var encKey, encToken []byte
	err := s.rdb.QueryRow(`
		SELECT id, network_id, hostname, os, arch, nebula_cert, nebula_key, nebula_ip,
		       agent_token, enrollment_token, agent_real_ip, status, last_seen_at, created_at
		FROM nodes WHERE id = ?
	`, id).Scan(&n.ID, &n.NetworkID, &n.Hostname, &n.OS, &n.Arch, &n.NebulaCert, &encKey,
		&n.NebulaIP, &encToken, &n.EnrollmentToken, &n.AgentRealIP, &n.Status, &n.LastSeenAt, &n.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	if len(encKey) > 0 {
		n.NebulaKey, err = s.enc.DecryptBytes(encKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt node key: %w", err)
		}
	}
	if len(encToken) > 0 {
		n.AgentToken, err = s.enc.Decrypt(encToken)
		if err != nil {
			return nil, fmt.Errorf("decrypt agent token: %w", err)
		}
	}
	return &n, nil
}

// ClaimEnrollmentToken atomically looks up a node by enrollment token hash,
// NULLs the token (consuming it), and returns the node. This prevents two
// agents from enrolling with the same token concurrently.
func (s *NodeStore) ClaimEnrollmentToken(token string) (*Node, error) {
	h := hashToken(token)

	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var n Node
	var encToken []byte
	err = tx.QueryRow(`
		SELECT id, network_id, hostname, os, arch, nebula_ip, agent_token, status
		FROM nodes WHERE enrollment_token = ?
		  AND (enrollment_expires_at IS NULL OR enrollment_expires_at > ?)
	`, h, time.Now().Unix()).Scan(&n.ID, &n.NetworkID, &n.Hostname, &n.OS, &n.Arch, &n.NebulaIP, &encToken, &n.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node by enrollment token: %w", err)
	}

	// Consume the token atomically within the same transaction.
	res, err := tx.Exec(`UPDATE nodes SET enrollment_token = NULL, enrollment_expires_at = NULL WHERE id = ? AND enrollment_token = ?`, n.ID, h)
	if err != nil {
		return nil, fmt.Errorf("consume enrollment token: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Another request consumed it between SELECT and UPDATE (race).
		return nil, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	if len(encToken) > 0 {
		n.AgentToken, err = s.enc.Decrypt(encToken)
		if err != nil {
			return nil, fmt.Errorf("decrypt agent token: %w", err)
		}
	}
	return &n, nil
}

func (s *NodeStore) ListForNetwork(networkID string) ([]*Node, error) {
	rows, err := s.rdb.Query(`
		SELECT id, network_id, hostname, os, arch, nebula_ip, agent_real_ip, status, last_seen_at, created_at
		FROM nodes WHERE network_id = ? ORDER BY created_at ASC
	`, networkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []*Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.NetworkID, &n.Hostname, &n.OS, &n.Arch, &n.NebulaIP,
			&n.AgentRealIP, &n.Status, &n.LastSeenAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (s *NodeStore) CountForNetwork(networkID string) (int, error) {
	var count int
	err := s.rdb.QueryRow(`SELECT COUNT(*) FROM nodes WHERE network_id = ?`, networkID).Scan(&count)
	return count, err
}

// NextNodeIndex returns the next available host index for a network's subnet.
// Uses MAX(last_octet) to avoid IP collisions when nodes are deleted.
// Returns 0 if no nodes exist (which maps to .2 via NodeAddress).
func (s *NodeStore) NextNodeIndex(networkID string) (int, error) {
	rows, err := s.rdb.Query(`SELECT nebula_ip FROM nodes WHERE network_id = ? AND nebula_ip IS NOT NULL`, networkID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	maxOctet := 1 // server is .1, nodes start at .2
	found := false
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			continue
		}
		found = true
		octet := parseLastOctet(ip)
		if octet > maxOctet {
			maxOctet = octet
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	// maxOctet is the last octet (e.g. 3 for .3), NodeAddress(subnet, idx) → .idx+2
	// So next index = maxOctet - 2 + 1 = maxOctet - 1
	return maxOctet - 1, nil
}

// parseLastOctet extracts the last octet from a CIDR like "10.42.1.3/24" → 3.
func parseLastOctet(cidr string) int {
	// Strip mask if present.
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
// The enrollment token is already consumed by ClaimEnrollmentToken.
func (s *NodeStore) CompleteEnrollment(id string, cert, key []byte, hostname, os, arch string) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	_, err = s.wdb.Exec(`
		UPDATE nodes SET nebula_cert = ?, nebula_key = ?, hostname = ?, os = ?, arch = ?,
		status = 'pending'
		WHERE id = ?
	`, cert, encKey, hostname, os, arch, id)
	return err
}

// UpdateCert replaces the node's Nebula certificate and key (for cert rotation).
func (s *NodeStore) UpdateCert(id string, cert, key []byte) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	_, err = s.wdb.Exec(`UPDATE nodes SET nebula_cert = ?, nebula_key = ? WHERE id = ?`, cert, encKey, id)
	return err
}

func (s *NodeStore) UpdateStatus(id, status string) error {
	_, err := s.wdb.Exec(`UPDATE nodes SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *NodeStore) UpdateLastSeen(id string) error {
	_, err := s.wdb.Exec(`UPDATE nodes SET last_seen_at = unixepoch(), status = 'online' WHERE id = ?`, id)
	return err
}

func (s *NodeStore) UpdateAgentRealIP(id, ip string) error {
	_, err := s.wdb.Exec(`UPDATE nodes SET agent_real_ip = ? WHERE id = ?`, ip, id)
	return err
}

func (s *NodeStore) Delete(id string) error {
	_, err := s.wdb.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

func (s *NodeStore) DeleteForNetwork(networkID string) error {
	_, err := s.wdb.Exec(`DELETE FROM nodes WHERE network_id = ?`, networkID)
	return err
}
