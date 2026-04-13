package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/db/dbsqlc"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL,
			name TEXT NOT NULL,
			password_hash TEXT NOT NULL
		);
		CREATE TABLE nodes (
			id TEXT PRIMARY KEY,
			network_id TEXT NOT NULL,
			hostname TEXT NOT NULL
		);
		CREATE TABLE audit_log (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			node_id TEXT,
			network_id TEXT,
			action TEXT NOT NULL,
			details TEXT,
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAuditStore_BatchFlush(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	store := NewAuditStore(&DBPair{ReadDB: db, WriteDB: db})

	for i := 0; i < 10; i++ {
		store.Log("id-"+string(rune('a'+i)), "user-1", "test.action", nil, nil, nil)
	}

	// Nothing written yet — entries are buffered.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 entries before flush, got %d", count)
	}

	// Flush writes all in a single transaction.
	store.Flush()

	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 10 {
		t.Fatalf("expected 10 entries after flush, got %d", count)
	}
}

func TestAuditStore_FlushOnContextCancel(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	store := NewAuditStore(&DBPair{ReadDB: db, WriteDB: db})

	ctx, cancel := context.WithCancel(context.Background())
	store.StartFlusher(ctx)

	for i := 0; i < 5; i++ {
		store.Log("id-"+string(rune('a'+i)), "user-1", "test", nil, nil, nil)
	}

	// Cancel triggers final flush in the flusher goroutine.
	cancel()
	time.Sleep(100 * time.Millisecond)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 5 {
		t.Fatalf("expected 5 entries after cancel, got %d", count)
	}
}

func TestAuditStore_AutoFlushByTimer(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	store := NewAuditStore(&DBPair{ReadDB: db, WriteDB: db})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.StartFlusher(ctx)

	store.Log("id-1", "user-1", "test", nil, nil, nil)

	// Wait for the flush interval (2s) + margin.
	time.Sleep(3 * time.Second)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 entry after timer flush, got %d", count)
	}
}

func TestAuditStore_BufferFullDropsEntry(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	// Create store with tiny buffer to test overflow behavior.
	store := &AuditStore{
		rdb: db,
		wdb: db,
		buf: make(chan dbsqlc.InsertAuditEntryParams, 2),
	}

	store.Log("id-1", "user-1", "test", nil, nil, nil)
	store.Log("id-2", "user-1", "test", nil, nil, nil)
	// Third entry should be dropped (buffer full).
	store.Log("id-3", "user-1", "test", nil, nil, nil)

	store.Flush()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 entries (1 dropped), got %d", count)
	}
}

func TestAuditStore_MultipleFlushes(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	store := NewAuditStore(&DBPair{ReadDB: db, WriteDB: db})

	// First batch.
	for i := 0; i < 3; i++ {
		store.Log("batch1-"+string(rune('a'+i)), "user-1", "test", nil, nil, nil)
	}
	store.Flush()

	// Second batch.
	for i := 0; i < 4; i++ {
		store.Log("batch2-"+string(rune('a'+i)), "user-1", "test", nil, nil, nil)
	}
	store.Flush()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 7 {
		t.Fatalf("expected 7 entries across 2 flushes, got %d", count)
	}
}

func TestAuditStore_EmptyFlush(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	store := NewAuditStore(&DBPair{ReadDB: db, WriteDB: db})

	// Flush with nothing buffered — should not error or create empty transactions.
	store.Flush()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 entries, got %d", count)
	}
}
