package db

import (
	"database/sql"
	"fmt"

	"github.com/trustos/hopssh/internal/crypto"
)

type Network struct {
	ID           string
	UserID       string
	Name         string
	Slug         string
	NebulaCACert []byte // PEM, plaintext
	NebulaCAKey  []byte // PEM, plaintext (encrypted on disk)
	NebulaSubnet string // e.g. "10.42.1.0/24"
	ServerCert   []byte // PEM, plaintext
	ServerKey    []byte // PEM, plaintext (encrypted on disk)
	CreatedAt    int64
}

type NetworkStore struct {
	rdb *sql.DB
	wdb *sql.DB
	enc *crypto.Encryptor
}

func NewNetworkStore(p *DBPair, enc *crypto.Encryptor) *NetworkStore {
	return &NetworkStore{rdb: p.ReadDB, wdb: p.WriteDB, enc: enc}
}

func (s *NetworkStore) Create(n *Network) error {
	encCAKey, err := s.enc.EncryptBytes(n.NebulaCAKey)
	if err != nil {
		return fmt.Errorf("encrypt CA key: %w", err)
	}
	encServerKey, err := s.enc.EncryptBytes(n.ServerKey)
	if err != nil {
		return fmt.Errorf("encrypt server key: %w", err)
	}

	_, err = s.wdb.Exec(`
		INSERT INTO networks (id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, n.ID, n.UserID, n.Name, n.Slug, n.NebulaCACert, encCAKey, n.NebulaSubnet, n.ServerCert, encServerKey)
	return err
}

func (s *NetworkStore) Get(id string) (*Network, error) {
	var n Network
	var encCAKey, encServerKey []byte
	err := s.rdb.QueryRow(`
		SELECT id, user_id, name, slug, nebula_ca_cert, nebula_ca_key, nebula_subnet, server_cert, server_key, created_at
		FROM networks WHERE id = ?
	`, id).Scan(&n.ID, &n.UserID, &n.Name, &n.Slug, &n.NebulaCACert, &encCAKey, &n.NebulaSubnet, &n.ServerCert, &encServerKey, &n.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get network: %w", err)
	}
	if len(encCAKey) > 0 {
		n.NebulaCAKey, err = s.enc.DecryptBytes(encCAKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt CA key: %w", err)
		}
	}
	if len(encServerKey) > 0 {
		n.ServerKey, err = s.enc.DecryptBytes(encServerKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt server key: %w", err)
		}
	}
	return &n, nil
}

func (s *NetworkStore) ListForUser(userID string) ([]*Network, error) {
	rows, err := s.rdb.Query(`
		SELECT id, user_id, name, slug, nebula_subnet, created_at
		FROM networks WHERE user_id = ? ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var networks []*Network
	for rows.Next() {
		var n Network
		if err := rows.Scan(&n.ID, &n.UserID, &n.Name, &n.Slug, &n.NebulaSubnet, &n.CreatedAt); err != nil {
			return nil, err
		}
		networks = append(networks, &n)
	}
	return networks, nil
}

func (s *NetworkStore) Delete(id string) error {
	_, err := s.wdb.Exec(`DELETE FROM networks WHERE id = ?`, id)
	return err
}

// AllocateSubnet returns the next available /24 subnet in the 10.42.0.0/8 range.
func (s *NetworkStore) AllocateSubnet() (string, error) {
	var count int
	if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM networks`).Scan(&count); err != nil {
		return "", err
	}
	octet := count + 1 // 10.42.1.0/24, 10.42.2.0/24, ...
	if octet > 254 {
		return "", fmt.Errorf("subnet space exhausted")
	}
	return fmt.Sprintf("10.42.%d.0/24", octet), nil
}
