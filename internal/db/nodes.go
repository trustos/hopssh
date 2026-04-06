package db

import (
	"database/sql"
	"fmt"

	"github.com/trustos/hopssh/internal/crypto"
)

type Node struct {
	ID              string
	NetworkID       string
	Hostname        string
	OS              string
	Arch            string
	NebulaCert      []byte // PEM, plaintext
	NebulaKey       []byte // PEM, plaintext (encrypted on disk)
	NebulaIP        string // e.g. "10.42.1.2/24"
	AgentToken      string
	EnrollmentToken *string // one-time, nulled after use
	AgentRealIP     *string
	Status          string // pending, online, offline
	LastSeenAt      *int64
	CreatedAt       int64
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

	_, err := s.wdb.Exec(`
		INSERT INTO nodes (id, network_id, hostname, os, arch, nebula_cert, nebula_key, nebula_ip, agent_token, enrollment_token, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, n.ID, n.NetworkID, n.Hostname, n.OS, n.Arch, n.NebulaCert, encKey, n.NebulaIP, n.AgentToken, n.EnrollmentToken, n.Status)
	return err
}

func (s *NodeStore) Get(id string) (*Node, error) {
	var n Node
	var encKey []byte
	err := s.rdb.QueryRow(`
		SELECT id, network_id, hostname, os, arch, nebula_cert, nebula_key, nebula_ip,
		       agent_token, enrollment_token, agent_real_ip, status, last_seen_at, created_at
		FROM nodes WHERE id = ?
	`, id).Scan(&n.ID, &n.NetworkID, &n.Hostname, &n.OS, &n.Arch, &n.NebulaCert, &encKey,
		&n.NebulaIP, &n.AgentToken, &n.EnrollmentToken, &n.AgentRealIP, &n.Status, &n.LastSeenAt, &n.CreatedAt)
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
	return &n, nil
}

func (s *NodeStore) GetByEnrollmentToken(token string) (*Node, error) {
	var n Node
	err := s.rdb.QueryRow(`
		SELECT id, network_id, hostname, os, arch, nebula_ip, agent_token, status
		FROM nodes WHERE enrollment_token = ?
	`, token).Scan(&n.ID, &n.NetworkID, &n.Hostname, &n.OS, &n.Arch, &n.NebulaIP, &n.AgentToken, &n.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get node by enrollment token: %w", err)
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
	return nodes, nil
}

func (s *NodeStore) CountForNetwork(networkID string) (int, error) {
	var count int
	err := s.rdb.QueryRow(`SELECT COUNT(*) FROM nodes WHERE network_id = ?`, networkID).Scan(&count)
	return count, err
}

// CompleteEnrollment sets the node's cert/key and consumes the enrollment token.
func (s *NodeStore) CompleteEnrollment(id string, cert, key []byte, hostname, os, arch string) error {
	encKey, err := s.enc.EncryptBytes(key)
	if err != nil {
		return fmt.Errorf("encrypt node key: %w", err)
	}
	_, err = s.wdb.Exec(`
		UPDATE nodes SET nebula_cert = ?, nebula_key = ?, hostname = ?, os = ?, arch = ?,
		enrollment_token = NULL, status = 'pending'
		WHERE id = ?
	`, cert, encKey, hostname, os, arch, id)
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
