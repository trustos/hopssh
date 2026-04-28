package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestPruneOfflineSince covers the cutoff semantics of the new
// hard-delete prune: only offline nodes older than cutoff get wiped;
// online nodes (regardless of last_seen_at) are always kept; offline
// nodes inside the protected window are kept.
//
// This is the storage-layer half of Phase B's auto-prune feature.
// The dashboard-side admin button calls PruneOfflineSince(networkID,
// now()) to wipe ALL offline; the hourly sweeper calls
// PruneOfflineSinceAllNetworks(now() - 7d) for the long-tail cleanup.
func TestPruneOfflineSince(t *testing.T) {
	store := newTestNodeStore(t)
	netID := "net-test"

	now := time.Now().Unix()
	// Three rows in the same network:
	//   1. offline, last seen 8 days ago — must be deleted
	//   2. offline, last seen 5 days ago — kept (inside protect window)
	//   3. online, last seen 2 hours ago — kept (online never deleted)
	insertTestNode(t, store, netID, "old-zombie", "offline", now-8*86400)
	insertTestNode(t, store, netID, "recent-offline", "offline", now-5*86400)
	insertTestNode(t, store, netID, "live", "online", now-2*3600)

	cutoff := now - 7*86400 // 7 days
	deleted, err := store.PruneOfflineSince(netID, cutoff)
	if err != nil {
		t.Fatalf("PruneOfflineSince: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	names := remainingHostnames(t, store, netID)
	if names["old-zombie"] {
		t.Error("old-zombie still present after prune")
	}
	if !names["recent-offline"] {
		t.Error("recent-offline removed but should be inside protected window")
	}
	if !names["live"] {
		t.Error("live online node removed by prune (must NEVER happen)")
	}
}

// TestPruneOfflineSince_ScopedToNetwork asserts the prune only touches
// the requested network. Two networks with one offline-zombie each;
// pruning network A must NOT delete network B's zombie.
func TestPruneOfflineSince_ScopedToNetwork(t *testing.T) {
	store := newTestNodeStore(t)
	now := time.Now().Unix()
	insertTestNode(t, store, "net-a", "zombie-a", "offline", now-30*86400)
	insertTestNode(t, store, "net-b", "zombie-b", "offline", now-30*86400)

	deleted, err := store.PruneOfflineSince("net-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if names := remainingHostnames(t, store, "net-b"); !names["zombie-b"] {
		t.Errorf("net-b lost zombie-b — prune must scope to network")
	}
	if names := remainingHostnames(t, store, "net-a"); names["zombie-a"] {
		t.Errorf("net-a still has zombie-a after prune")
	}
}

// TestPruneOfflineSinceAllNetworks asserts the cross-network variant
// used by the hourly sweeper.
func TestPruneOfflineSinceAllNetworks(t *testing.T) {
	store := newTestNodeStore(t)
	now := time.Now().Unix()
	insertTestNode(t, store, "net-a", "old", "offline", now-30*86400)
	insertTestNode(t, store, "net-b", "old", "offline", now-30*86400)
	insertTestNode(t, store, "net-c", "live", "online", now-3600)

	deleted, err := store.PruneOfflineSinceAllNetworks(now - 7*86400)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
}

// --- Test fixtures ---

func newTestNodeStore(t *testing.T) *NodeStore {
	t.Helper()
	wdb, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wdb.Close() })

	if _, err := wdb.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			network_id TEXT NOT NULL,
			hostname TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT '',
			arch TEXT NOT NULL DEFAULT '',
			nebula_ip TEXT NOT NULL DEFAULT '',
			node_type TEXT NOT NULL DEFAULT 'node',
			status TEXT NOT NULL DEFAULT 'pending',
			last_seen_at INTEGER,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			agent_real_ip TEXT,
			exposed_ports TEXT,
			dns_name TEXT,
			capabilities TEXT,
			peers_direct INTEGER,
			peers_relayed INTEGER,
			peers_reported_at INTEGER,
			agent_version TEXT,
			peer_state TEXT
		)`); err != nil {
		t.Fatal(err)
	}

	return &NodeStore{wdb: wdb, rdb: wdb}
}

// remainingHostnames returns the set of hostnames still in the nodes
// table for the given network. Bypasses NodeStore.ListForNetwork
// because the test schema is intentionally minimal and ListForNetwork
// scans nullable columns the test schema doesn't fully populate.
func remainingHostnames(t *testing.T, store *NodeStore, networkID string) map[string]bool {
	t.Helper()
	rows, err := store.wdb.Query("SELECT hostname FROM nodes WHERE network_id = ?", networkID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var hn string
		if err := rows.Scan(&hn); err != nil {
			t.Fatal(err)
		}
		out[hn] = true
	}
	return out
}

func insertTestNode(t *testing.T, store *NodeStore, networkID, hostname, status string, lastSeenAt int64) {
	t.Helper()
	_, err := store.wdb.ExecContext(context.Background(),
		`INSERT INTO nodes (id, network_id, hostname, status, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		networkID+"-"+hostname, networkID, hostname, status, lastSeenAt,
	)
	if err != nil {
		t.Fatal(err)
	}
}
