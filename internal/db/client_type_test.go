package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// SetClientType + ClientTypesForNetwork together implement the
// "Client" column in the dashboard's Nodes tab (Phase C, commit
// e5da5d7) — the agent reports its build-baked client type
// ("desktop" / "cli") in the heartbeat body, and the server persists
// it to nodes.client_type. These tests pin the lifecycle: write,
// retrieve, scope, idempotent-rewrite, empty-ignore.

func TestSetClientType_PersistsAndRetrieves(t *testing.T) {
	store := newClientTypeTestStore(t)
	insertClientTypeTestNode(t, store, "net-a", "n1", "")
	insertClientTypeTestNode(t, store, "net-a", "n2", "")

	if err := store.SetClientType("n1", "desktop"); err != nil {
		t.Fatalf("SetClientType: %v", err)
	}
	if err := store.SetClientType("n2", "cli"); err != nil {
		t.Fatalf("SetClientType: %v", err)
	}

	got := store.ClientTypesForNetwork("net-a")
	if got["n1"] != "desktop" {
		t.Errorf("n1: got %q, want desktop", got["n1"])
	}
	if got["n2"] != "cli" {
		t.Errorf("n2: got %q, want cli", got["n2"])
	}
}

func TestClientTypesForNetwork_OmitsNullAndEmpty(t *testing.T) {
	// Tripwire: ClientTypesForNetwork must filter out NULL and empty
	// values. The dashboard pill renderer maps "desktop" → 🖥, "cli"
	// → ⌨, anything else → "—". Returning "" for legacy nodes that
	// never reported a clientType would render as a literal empty
	// pill — visually wrong.
	store := newClientTypeTestStore(t)
	insertClientTypeTestNode(t, store, "net-a", "legacy-null", "")  // NULL
	insertClientTypeTestNodeEmpty(t, store, "net-a", "legacy-empty") // ''
	insertClientTypeTestNode(t, store, "net-a", "modern-desktop", "desktop")

	got := store.ClientTypesForNetwork("net-a")
	if _, ok := got["legacy-null"]; ok {
		t.Errorf("legacy-null should be omitted (NULL client_type)")
	}
	if _, ok := got["legacy-empty"]; ok {
		t.Errorf("legacy-empty should be omitted ('' client_type)")
	}
	if got["modern-desktop"] != "desktop" {
		t.Errorf("modern-desktop: got %q, want desktop", got["modern-desktop"])
	}
}

func TestClientTypesForNetwork_ScopedToNetwork(t *testing.T) {
	// Tripwire: a node's client_type must NOT leak across networks.
	// When the dashboard renders Nodes for net-a, it must not see the
	// client_type assigned to nodes in net-b.
	store := newClientTypeTestStore(t)
	insertClientTypeTestNode(t, store, "net-a", "n-a", "desktop")
	insertClientTypeTestNode(t, store, "net-b", "n-b", "cli")

	gotA := store.ClientTypesForNetwork("net-a")
	if _, ok := gotA["n-b"]; ok {
		t.Errorf("net-a query leaked net-b's node")
	}
	if gotA["n-a"] != "desktop" {
		t.Errorf("net-a: missing own node n-a")
	}

	gotB := store.ClientTypesForNetwork("net-b")
	if _, ok := gotB["n-a"]; ok {
		t.Errorf("net-b query leaked net-a's node")
	}
	if gotB["n-b"] != "cli" {
		t.Errorf("net-b: missing own node n-b")
	}
}

func TestSetClientType_IgnoresEmpty(t *testing.T) {
	// Idempotent + safe: empty client_type from a legacy/incomplete
	// heartbeat must NOT clobber a previously-set value. We've seen
	// build pipelines forget the ldflag; without this guard, dropping
	// the ldflag once would silently NULL out the desktop pill for
	// every existing user.
	store := newClientTypeTestStore(t)
	insertClientTypeTestNode(t, store, "net-a", "n1", "desktop")

	if err := store.SetClientType("n1", ""); err != nil {
		t.Fatalf("SetClientType('' should not error): %v", err)
	}

	got := store.ClientTypesForNetwork("net-a")
	if got["n1"] != "desktop" {
		t.Errorf("desktop pill clobbered by empty SetClientType: got %q", got["n1"])
	}
}

func TestSetClientType_Idempotent(t *testing.T) {
	// Heartbeats fire every 30s. Repeated SetClientType("desktop")
	// must not error and must not change the value.
	store := newClientTypeTestStore(t)
	insertClientTypeTestNode(t, store, "net-a", "n1", "")

	for i := 0; i < 5; i++ {
		if err := store.SetClientType("n1", "desktop"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	got := store.ClientTypesForNetwork("net-a")
	if got["n1"] != "desktop" {
		t.Errorf("after 5 idempotent writes: got %q, want desktop", got["n1"])
	}
}

// --- Test fixtures ---
//
// Separate from prune_test.go's helpers because we need the
// client_type column in the schema; extending the shared helper would
// touch unrelated tests.

func newClientTypeTestStore(t *testing.T) *NodeStore {
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
			status TEXT NOT NULL DEFAULT 'pending',
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			client_type TEXT
		)`); err != nil {
		t.Fatal(err)
	}
	return &NodeStore{wdb: wdb, rdb: wdb}
}

// insertClientTypeTestNode inserts with a NULL client_type when ct is
// "" — matches a legacy node that never reported its build-baked type.
func insertClientTypeTestNode(t *testing.T, store *NodeStore, networkID, id, ct string) {
	t.Helper()
	if ct == "" {
		_, err := store.wdb.Exec(
			`INSERT INTO nodes (id, network_id, hostname, status) VALUES (?, ?, ?, 'online')`,
			id, networkID, id,
		)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	_, err := store.wdb.Exec(
		`INSERT INTO nodes (id, network_id, hostname, status, client_type) VALUES (?, ?, ?, 'online', ?)`,
		id, networkID, id, ct,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// insertClientTypeTestNodeEmpty inserts with explicit '' (not NULL)
// in client_type so we can prove the filter rejects both forms.
func insertClientTypeTestNodeEmpty(t *testing.T, store *NodeStore, networkID, id string) {
	t.Helper()
	_, err := store.wdb.Exec(
		`INSERT INTO nodes (id, network_id, hostname, status, client_type) VALUES (?, ?, ?, 'online', '')`,
		id, networkID, id,
	)
	if err != nil {
		t.Fatal(err)
	}
}
