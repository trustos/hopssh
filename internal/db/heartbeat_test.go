package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newHeartbeatTestDB(t *testing.T) (*NodeStore, *DBPair) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlDB.Exec(`
		CREATE TABLE nodes (
			id TEXT PRIMARY KEY,
			network_id TEXT NOT NULL,
			hostname TEXT NOT NULL,
			os TEXT NOT NULL DEFAULT '',
			arch TEXT NOT NULL DEFAULT '',
			nebula_cert BLOB,
			nebula_key BLOB,
			nebula_ip TEXT,
			agent_token TEXT NOT NULL DEFAULT '',
			enrollment_token TEXT,
			enrollment_expires_at INTEGER,
			agent_real_ip TEXT,
			node_type TEXT NOT NULL DEFAULT 'server',
			exposed_ports TEXT,
			dns_name TEXT,
			capabilities TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			last_seen_at INTEGER,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			peers_direct INTEGER,
			peers_relayed INTEGER,
			peers_reported_at INTEGER,
			peer_state TEXT,
			agent_version TEXT
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Exec(`INSERT INTO nodes (id, network_id, hostname, status) VALUES ('node-1', 'net-1', 'test-host', 'online')`)
	sqlDB.Exec(`INSERT INTO nodes (id, network_id, hostname, status) VALUES ('node-2', 'net-1', 'test-host-2', 'online')`)
	pair := &DBPair{ReadDB: sqlDB, WriteDB: sqlDB}
	store := NewNodeStore(pair, nil)
	return store, pair
}

func TestRecordHeartbeat_BatchFlush(t *testing.T) {
	store, pair := newHeartbeatTestDB(t)
	defer pair.ReadDB.Close()

	store.RecordHeartbeat("node-1", "1.2.3.4", nil, nil, nil, nil)
	store.RecordHeartbeat("node-2", "5.6.7.8", nil, nil, nil, nil)

	// Nothing written yet.
	var count int
	pair.ReadDB.QueryRow("SELECT COUNT(*) FROM nodes WHERE agent_real_ip IS NOT NULL").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 nodes with IP before flush, got %d", count)
	}

	store.FlushHeartbeats()

	// Verify both nodes updated.
	var ip1, ip2 string
	pair.ReadDB.QueryRow("SELECT agent_real_ip FROM nodes WHERE id = 'node-1'").Scan(&ip1)
	pair.ReadDB.QueryRow("SELECT agent_real_ip FROM nodes WHERE id = 'node-2'").Scan(&ip2)
	if ip1 != "1.2.3.4" {
		t.Errorf("node-1 IP: got %q, want 1.2.3.4", ip1)
	}
	if ip2 != "5.6.7.8" {
		t.Errorf("node-2 IP: got %q, want 5.6.7.8", ip2)
	}

	// Verify last_seen_at and status updated.
	var status string
	var lastSeen int64
	pair.ReadDB.QueryRow("SELECT status, last_seen_at FROM nodes WHERE id = 'node-1'").Scan(&status, &lastSeen)
	if status != "online" {
		t.Errorf("status: got %q, want online", status)
	}
	if lastSeen == 0 {
		t.Error("last_seen_at should be set")
	}
}

func TestRecordHeartbeat_Coalesces(t *testing.T) {
	store, pair := newHeartbeatTestDB(t)
	defer pair.ReadDB.Close()

	// Same node, multiple heartbeats — only latest IP should be kept.
	store.RecordHeartbeat("node-1", "1.1.1.1", nil, nil, nil, nil)
	store.RecordHeartbeat("node-1", "2.2.2.2", nil, nil, nil, nil)
	store.RecordHeartbeat("node-1", "3.3.3.3", nil, nil, nil, nil)

	store.FlushHeartbeats()

	var ip string
	pair.ReadDB.QueryRow("SELECT agent_real_ip FROM nodes WHERE id = 'node-1'").Scan(&ip)
	if ip != "3.3.3.3" {
		t.Errorf("expected coalesced IP 3.3.3.3, got %q", ip)
	}
}

func TestRecordHeartbeat_EmptyIPPreservesExisting(t *testing.T) {
	store, pair := newHeartbeatTestDB(t)
	defer pair.ReadDB.Close()

	// Set an initial IP.
	pair.WriteDB.Exec("UPDATE nodes SET agent_real_ip = '10.0.0.1' WHERE id = 'node-1'")

	// Heartbeat with empty IP (health check path).
	store.RecordHeartbeat("node-1", "", nil, nil, nil, nil)
	store.FlushHeartbeats()

	var ip string
	pair.ReadDB.QueryRow("SELECT agent_real_ip FROM nodes WHERE id = 'node-1'").Scan(&ip)
	if ip != "10.0.0.1" {
		t.Errorf("empty IP should preserve existing, got %q", ip)
	}
}

func TestRecordHeartbeat_EmptyFlush(t *testing.T) {
	store, pair := newHeartbeatTestDB(t)
	defer pair.ReadDB.Close()

	// Should not panic or error.
	store.FlushHeartbeats()
}

func TestRecordHeartbeat_FlushOnContextCancel(t *testing.T) {
	store, pair := newHeartbeatTestDB(t)
	defer pair.ReadDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	store.StartHeartbeatFlusher(ctx)

	store.RecordHeartbeat("node-1", "9.9.9.9", nil, nil, nil, nil)
	cancel()
	time.Sleep(100 * time.Millisecond)

	var ip string
	pair.ReadDB.QueryRow("SELECT agent_real_ip FROM nodes WHERE id = 'node-1'").Scan(&ip)
	if ip != "9.9.9.9" {
		t.Errorf("expected flush on cancel, got IP %q", ip)
	}
}

func TestRecordHeartbeat_MultipleFlushes(t *testing.T) {
	store, pair := newHeartbeatTestDB(t)
	defer pair.ReadDB.Close()

	store.RecordHeartbeat("node-1", "1.0.0.1", nil, nil, nil, nil)
	store.FlushHeartbeats()

	store.RecordHeartbeat("node-1", "2.0.0.2", nil, nil, nil, nil)
	store.FlushHeartbeats()

	var ip string
	pair.ReadDB.QueryRow("SELECT agent_real_ip FROM nodes WHERE id = 'node-1'").Scan(&ip)
	if ip != "2.0.0.2" {
		t.Errorf("second flush should update IP, got %q", ip)
	}
}
