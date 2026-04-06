package db

import (
	"context"
	"database/sql"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

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
	q := dbsqlc.New(WrapDB(s.wdb))
	return q.InsertAuditEntry(context.Background(), dbsqlc.InsertAuditEntryParams{
		ID:        id,
		UserID:    userID,
		NodeID:    nodeID,
		NetworkID: networkID,
		Action:    action,
		Details:   details,
	})
}

func (s *AuditStore) ListForNetwork(networkID string, limit int) ([]*AuditEntry, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListAuditForNetwork(context.Background(), dbsqlc.ListAuditForNetworkParams{
		NetworkID: &networkID,
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, err
	}

	entries := make([]*AuditEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, &AuditEntry{
			ID:        r.ID,
			UserID:    r.UserID,
			NodeID:    r.NodeID,
			NetworkID: r.NetworkID,
			Action:    r.Action,
			Details:   r.Details,
			CreatedAt: r.CreatedAt,
		})
	}
	return entries, nil
}

func (s *AuditStore) ListForUser(userID string, limit int) ([]*AuditEntry, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	rows, err := q.ListAuditForUser(context.Background(), dbsqlc.ListAuditForUserParams{
		UserID: userID,
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, err
	}

	entries := make([]*AuditEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, &AuditEntry{
			ID:        r.ID,
			UserID:    r.UserID,
			NodeID:    r.NodeID,
			NetworkID: r.NetworkID,
			Action:    r.Action,
			Details:   r.Details,
			CreatedAt: r.CreatedAt,
		})
	}
	return entries, nil
}
