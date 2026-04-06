package db

import "database/sql"

type AuditEntry struct {
	ID        string
	UserID    string
	NodeID    *string
	NetworkID *string
	Action    string
	Details   *string
	CreatedAt int64
}

type AuditStore struct {
	rdb *sql.DB
	wdb *sql.DB
}

func NewAuditStore(p *DBPair) *AuditStore {
	return &AuditStore{rdb: p.ReadDB, wdb: p.WriteDB}
}

func (s *AuditStore) Log(id, userID, action string, networkID, nodeID, details *string) error {
	_, err := s.wdb.Exec(`
		INSERT INTO audit_log (id, user_id, node_id, network_id, action, details)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, userID, nodeID, networkID, action, details)
	return err
}

func (s *AuditStore) ListForNetwork(networkID string, limit int) ([]*AuditEntry, error) {
	rows, err := s.rdb.Query(`
		SELECT id, user_id, node_id, network_id, action, details, created_at
		FROM audit_log WHERE network_id = ? ORDER BY created_at DESC LIMIT ?
	`, networkID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.NodeID, &e.NetworkID, &e.Action, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, nil
}
