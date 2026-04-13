package db

import (
	"context"
	"database/sql"
	"log"
	"time"

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

	// Enrichment from LEFT JOINs (nil if user/node deleted).
	UserEmail    *string
	UserName     *string
	NodeHostname *string
}

// auditFlushInterval is how often buffered audit entries are flushed to SQLite.
const auditFlushInterval = 2 * time.Second

// auditFlushSize is the max buffer size before an immediate flush.
const auditFlushSize = 100

// auditBufCap is the channel capacity. If the channel is full, Log drops
// the entry rather than blocking the request goroutine.
const auditBufCap = 1000

type AuditStore struct {
	rdb *sql.DB
	wdb *sql.DB
	buf chan dbsqlc.InsertAuditEntryParams
}

func NewAuditStore(p *DBPair) *AuditStore {
	return &AuditStore{
		rdb: p.ReadDB,
		wdb: p.WriteDB,
		buf: make(chan dbsqlc.InsertAuditEntryParams, auditBufCap),
	}
}

// StartFlusher starts the background goroutine that batches audit writes.
// Call Flush() on shutdown to drain remaining entries.
func (s *AuditStore) StartFlusher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(auditFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.flush() // drain on shutdown
				return
			case <-ticker.C:
				s.flush()
			}
		}
	}()
}

// Flush drains all buffered entries to the database. Safe to call from
// the shutdown path after the flusher context is canceled.
func (s *AuditStore) Flush() {
	s.flush()
}

// Log buffers an audit entry for batch insertion. Non-blocking: if the
// buffer is full (>1000 pending entries), the entry is dropped with a log
// warning rather than blocking the caller.
func (s *AuditStore) Log(id, userID, action string, networkID, nodeID, details *string) error {
	entry := dbsqlc.InsertAuditEntryParams{
		ID:        id,
		UserID:    userID,
		NodeID:    nodeID,
		NetworkID: networkID,
		Action:    action,
		Details:   details,
	}
	select {
	case s.buf <- entry:
	default:
		log.Printf("[audit] buffer full, dropping entry: %s %s", action, userID)
	}
	return nil
}

func (s *AuditStore) flush() {
	// Drain the channel into a local slice.
	var batch []dbsqlc.InsertAuditEntryParams
	for {
		select {
		case entry := <-s.buf:
			batch = append(batch, entry)
			if len(batch) >= auditFlushSize {
				s.writeBatch(batch)
				batch = nil
			}
		default:
			if len(batch) > 0 {
				s.writeBatch(batch)
			}
			return
		}
	}
}

func (s *AuditStore) writeBatch(batch []dbsqlc.InsertAuditEntryParams) {
	tx, err := s.wdb.Begin()
	if err != nil {
		log.Printf("[audit] begin tx: %v (dropping %d entries)", err, len(batch))
		return
	}
	q := dbsqlc.New(WrapTx(tx))
	for _, entry := range batch {
		if err := q.InsertAuditEntry(context.Background(), entry); err != nil {
			log.Printf("[audit] insert: %v", err)
			tx.Rollback()
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[audit] commit: %v (dropping %d entries)", err, len(batch))
	}
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
			ID:           r.ID,
			UserID:       r.UserID,
			NodeID:       r.NodeID,
			NetworkID:    r.NetworkID,
			Action:       r.Action,
			Details:      r.Details,
			CreatedAt:    r.CreatedAt,
			UserEmail:    r.UserEmail,
			UserName:     r.UserName,
			NodeHostname: r.NodeHostname,
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
			ID:           r.ID,
			UserID:       r.UserID,
			NodeID:       r.NodeID,
			NetworkID:    r.NetworkID,
			Action:       r.Action,
			Details:      r.Details,
			CreatedAt:    r.CreatedAt,
			UserEmail:    r.UserEmail,
			UserName:     r.UserName,
			NodeHostname: r.NodeHostname,
		})
	}
	return entries, nil
}
