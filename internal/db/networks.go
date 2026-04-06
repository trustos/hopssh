package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/trustos/hopssh/internal/crypto"
	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

type Network struct {
	ID             string
	UserID         string
	Name           string
	Slug           string
	NebulaCACert   []byte // PEM, plaintext
	NebulaCAKey    []byte // PEM, plaintext (encrypted on disk)
	NebulaSubnet   string // e.g. "10.42.1.0/24"
	ServerCert     []byte // PEM, plaintext
	ServerKey      []byte // PEM, plaintext (encrypted on disk)
	LighthousePort *int64 // UDP port for this network's Nebula lighthouse
	DNSDomain      string // user-defined DNS domain (e.g., "zero", "prod")
	CreatedAt      int64
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

	q := dbsqlc.New(WrapDB(s.wdb))
	return q.InsertNetwork(context.Background(), dbsqlc.InsertNetworkParams{
		ID:             n.ID,
		UserID:         n.UserID,
		Name:           n.Name,
		Slug:           n.Slug,
		NebulaCaCert:   n.NebulaCACert,
		NebulaCaKey:    encCAKey,
		NebulaSubnet:   &n.NebulaSubnet,
		ServerCert:     n.ServerCert,
		ServerKey:      encServerKey,
		LighthousePort: n.LighthousePort,
		DnsDomain:      n.DNSDomain,
	})
}

func (s *NetworkStore) Get(id string) (*Network, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	row, err := q.GetNetworkByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get network: %w", err)
	}

	n := &Network{
		ID:             row.ID,
		UserID:         row.UserID,
		Name:           row.Name,
		Slug:           row.Slug,
		NebulaCACert:   row.NebulaCaCert,
		ServerCert:     row.ServerCert,
		LighthousePort: row.LighthousePort,
		DNSDomain:      row.DnsDomain,
		CreatedAt:      row.CreatedAt,
	}
	if row.NebulaSubnet != nil {
		n.NebulaSubnet = *row.NebulaSubnet
	}

	if len(row.NebulaCaKey) > 0 {
		n.NebulaCAKey, err = s.enc.DecryptBytes(row.NebulaCaKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt CA key: %w", err)
		}
	}
	if len(row.ServerKey) > 0 {
		n.ServerKey, err = s.enc.DecryptBytes(row.ServerKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt server key: %w", err)
		}
	}
	return n, nil
}

func (s *NetworkStore) ListForUser(userID string) ([]*Network, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListNetworksForUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}

	networks := make([]*Network, 0, len(rows))
	for _, r := range rows {
		n := &Network{
			ID:             r.ID,
			UserID:         r.UserID,
			Name:           r.Name,
			Slug:           r.Slug,
			LighthousePort: r.LighthousePort,
			DNSDomain:      r.DnsDomain,
			CreatedAt:      r.CreatedAt,
		}
		if r.NebulaSubnet != nil {
			n.NebulaSubnet = *r.NebulaSubnet
		}
		networks = append(networks, n)
	}
	return networks, nil
}

// ListAll returns all networks (for NetworkManager startup).
func (s *NetworkStore) ListAll() ([]*Network, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListAllNetworks(context.Background())
	if err != nil {
		return nil, err
	}
	networks := make([]*Network, 0, len(rows))
	for _, r := range rows {
		n := &Network{
			ID:             r.ID,
			UserID:         r.UserID,
			Name:           r.Name,
			Slug:           r.Slug,
			NebulaCACert:   r.NebulaCaCert,
			ServerCert:     r.ServerCert,
			LighthousePort: r.LighthousePort,
			DNSDomain:      r.DnsDomain,
			CreatedAt:      r.CreatedAt,
		}
		if r.NebulaSubnet != nil {
			n.NebulaSubnet = *r.NebulaSubnet
		}
		if len(r.NebulaCaKey) > 0 {
			n.NebulaCAKey, err = s.enc.DecryptBytes(r.NebulaCaKey)
			if err != nil {
				return nil, fmt.Errorf("decrypt CA key for network %s: %w", r.ID, err)
			}
		}
		if len(r.ServerKey) > 0 {
			n.ServerKey, err = s.enc.DecryptBytes(r.ServerKey)
			if err != nil {
				return nil, fmt.Errorf("decrypt server key for network %s: %w", r.ID, err)
			}
		}
		networks = append(networks, n)
	}
	return networks, nil
}

// MaxLighthousePort returns the highest allocated lighthouse port, or 0 if none.
func (s *NetworkStore) MaxLighthousePort() (int, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	result, err := q.MaxLighthousePort(context.Background())
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	switch v := result.(type) {
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	}
	return 0, nil
}

// SlugExists checks if a network slug is already taken.
func (s *NetworkStore) SlugExists(slug string) bool {
	q := dbsqlc.New(WrapDB(s.rdb))
	count, err := q.NetworkSlugExists(context.Background(), slug)
	return err == nil && count > 0
}

func (s *NetworkStore) Delete(id string) error {
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.DeleteNetwork(context.Background(), id)
}

// IsUniqueViolation checks if an error is a SQLite unique constraint violation.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// AllocateSubnet returns the next available /24 subnet in the 10.42.0.0/8 range.
// Uses MAX to avoid collisions when networks are deleted.
func (s *NetworkStore) AllocateSubnet() (string, error) {
	q := dbsqlc.New(WrapDB(s.wdb))
	result, err := q.MaxSubnetOctet(context.Background())
	if err != nil {
		return "", fmt.Errorf("query max subnet: %w", err)
	}
	octet := 1
	if result != nil {
		// sqlc returns interface{} for MAX(CAST(...)). SQLite returns int64.
		switch v := result.(type) {
		case int64:
			octet = int(v) + 1
		case float64:
			octet = int(v) + 1
		}
	}
	if octet > 254 {
		return "", fmt.Errorf("subnet space exhausted")
	}
	return fmt.Sprintf("10.42.%d.0/24", octet), nil
}
