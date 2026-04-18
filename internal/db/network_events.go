package db

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
)

// NetworkEvent is the domain model for a persisted control-plane event.
type NetworkEvent struct {
	ID             int64
	NetworkID      string
	EventType      string
	TargetID       *string
	Status         *string
	Details        *string
	CreatedAt      int64
	TargetHostname *string
}

// networkEventFlushInterval is how often buffered events are flushed.
// Mirrors audit.go's 2s cadence since the volume profile is similar.
const networkEventFlushInterval = 2 * time.Second

// networkEventFlushSize is the max buffer size before an immediate flush.
const networkEventFlushSize = 100

// networkEventBufCap is the channel capacity. If the channel is full,
// Record drops the entry rather than blocking the caller (same policy
// as audit).
const networkEventBufCap = 1000

// NetworkEventStore buffers + flushes network-event writes on a timer,
// same pattern as AuditStore. Separate table because "events" are
// server-side signals (node came online / offline / got renamed)
// while audit_log tracks user-driven actions.
type NetworkEventStore struct {
	rdb *sql.DB
	wdb *sql.DB
	buf chan dbsqlc.InsertNetworkEventParams
}

func NewNetworkEventStore(p *DBPair) *NetworkEventStore {
	return &NetworkEventStore{
		rdb: p.ReadDB,
		wdb: p.WriteDB,
		buf: make(chan dbsqlc.InsertNetworkEventParams, networkEventBufCap),
	}
}

// StartFlusher starts the background goroutine that batches event writes.
// Call Flush() on shutdown to drain remaining entries.
func (s *NetworkEventStore) StartFlusher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(networkEventFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.flush()
				return
			case <-ticker.C:
				s.flush()
			}
		}
	}()
}

// Flush drains all buffered entries to the database.
func (s *NetworkEventStore) Flush() {
	s.flush()
}

// Record buffers an event for batch insertion. Non-blocking; drops on
// overflow. targetID is the node ID for node-scoped events; nil for
// network-scoped events (dns.changed, member.changed). status is set
// for node.status events ("online" / "offline"); nil otherwise.
// details is an optional compact JSON payload; nil when the event has
// no extra data.
func (s *NetworkEventStore) Record(networkID, eventType string, targetID, status, details *string) {
	entry := dbsqlc.InsertNetworkEventParams{
		NetworkID: networkID,
		EventType: eventType,
		TargetID:  targetID,
		Status:    status,
		Details:   details,
		CreatedAt: time.Now().Unix(),
	}
	select {
	case s.buf <- entry:
	default:
		log.Printf("[events] buffer full, dropping entry: %s %s", eventType, networkID)
	}
}

func (s *NetworkEventStore) flush() {
	var batch []dbsqlc.InsertNetworkEventParams
	for {
		select {
		case entry := <-s.buf:
			batch = append(batch, entry)
			if len(batch) >= networkEventFlushSize {
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

func (s *NetworkEventStore) writeBatch(batch []dbsqlc.InsertNetworkEventParams) {
	tx, err := s.wdb.Begin()
	if err != nil {
		log.Printf("[events] begin tx: %v (dropping %d entries)", err, len(batch))
		return
	}
	q := dbsqlc.New(WrapTx(tx))
	for _, entry := range batch {
		if err := q.InsertNetworkEvent(context.Background(), entry); err != nil {
			log.Printf("[events] insert: %v", err)
			tx.Rollback()
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[events] commit: %v (dropping %d entries)", err, len(batch))
	}
}

// ListForNetwork returns the most recent events for a network, optionally
// filtered by event type and time range. since is a unix-seconds cutoff
// (events at or after since are returned). eventType empty matches all
// types. limit caps the result count.
func (s *NetworkEventStore) ListForNetwork(networkID string, since int64, eventType string, limit int) ([]*NetworkEvent, error) {
	q := dbsqlc.New(WrapDB(s.rdb))
	var typeArg interface{}
	if eventType != "" {
		typeArg = eventType
	}
	rows, err := q.ListNetworkEvents(context.Background(), dbsqlc.ListNetworkEventsParams{
		NetworkID: networkID,
		Since:     since,
		EventType: typeArg,
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, err
	}
	events := make([]*NetworkEvent, 0, len(rows))
	for _, r := range rows {
		events = append(events, &NetworkEvent{
			ID:             r.ID,
			NetworkID:      r.NetworkID,
			EventType:      r.EventType,
			TargetID:       r.TargetID,
			Status:         r.Status,
			Details:        r.Details,
			CreatedAt:      r.CreatedAt,
			TargetHostname: r.TargetHostname,
		})
	}
	return events, nil
}
