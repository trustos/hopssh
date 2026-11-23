package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustos/hopssh/internal/crypto"
)

func openTestDB(t *testing.T) (*DBPair, *crypto.Encryptor) {
	t.Helper()
	p, err := Open(filepath.Join(t.TempDir(), "hopssh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if err := Migrate(p.WriteDB); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(strings.Repeat("00", 32))
	if err != nil {
		t.Fatal(err)
	}
	return p, enc
}

// addTestNetwork inserts a network (and its owner) the foreign keys accept.
func addTestNetwork(t *testing.T, p *DBPair, enc *crypto.Encryptor, id string) {
	t.Helper()
	if _, err := p.WriteDB.Exec(`INSERT OR IGNORE INTO users (id, email, name, password_hash) VALUES ('owner', 'owner@test', 'owner', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := NewNetworkStore(p, enc).Create(&Network{ID: id, UserID: "owner", Name: id, Slug: id, NebulaSubnet: id}); err != nil {
		t.Fatal(err)
	}
}

// TestOpen_AppliesSettings: the driver ignores DSN parameters it does not know,
// so a misspelled setting leaves SQLite at its defaults without an error. Up to
// v0.11.26 that was all of them: no waiting on locks, no WAL, no foreign keys.
func TestOpen_AppliesSettings(t *testing.T) {
	dir := t.TempDir()
	for name, dsn := range map[string]struct{ path, journal string }{
		"file":            {filepath.Join(dir, "a.db"), "wal"},
		"file with query": {"file:" + filepath.Join(dir, "b.db") + "?mode=rwc", "wal"},
		"memory":          {":memory:", "memory"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Open(dsn.path)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			want := map[string]string{
				"journal_mode": dsn.journal, "busy_timeout": "10000", "foreign_keys": "1", "synchronous": "1",
				"cache_size": "-32000", "temp_store": "2", "journal_size_limit": "200000000",
			}
			// Every connection of a pool has to have them, so hold several at once.
			for i := 0; i < 4; i++ {
				pool := p.ReadDB
				if i == 0 {
					pool = p.WriteDB
				}
				conn, err := pool.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				for pragma, value := range want {
					var got string
					if err := conn.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if got != value {
						t.Errorf("connection %d: %s = %s, want %s", i, pragma, got, value)
					}
				}
			}
		})
	}
}

// TestReadsSucceedWhileWriting: a read that meets a commit must wait or read
// the previous state, not fail. /api/renew answered such a read with "unknown
// node".
func TestReadsSucceedWhileWriting(t *testing.T) {
	p, enc := openTestDB(t)
	addTestNetwork(t, p, enc, "net")
	nodes := NewNodeStore(p, enc)
	if err := nodes.Create(&Node{ID: "node", NetworkID: "net", AgentToken: "tok"}); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	var reads, writes atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ; ctx.Err() == nil; writes.Add(1) {
			tx, err := p.WriteDB.Begin()
			if err != nil {
				t.Errorf("begin: %v", err)
				return
			}
			tx.Exec(`UPDATE nodes SET hostname = ? WHERE id = 'node'`, fmt.Sprint(writes.Load()))
			if err := tx.Commit(); err != nil {
				t.Errorf("commit: %v", err)
				return
			}
		}
	}()
	var failed atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ; ctx.Err() == nil; reads.Add(1) {
				// Straight at the pool: NodeStore.Get retries once, which hides most of it.
				var id string
				if err := p.ReadDB.QueryRow(`SELECT id FROM nodes WHERE id = 'node'`).Scan(&id); err != nil {
					if failed.Add(1) == 1 {
						t.Errorf("read: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()
	t.Logf("%d reads (%d failed) during %d writes", reads.Load(), failed.Load(), writes.Load())
	if reads.Load() < 100 || writes.Load() < 10 {
		t.Error("too little happened to prove anything")
	}
}

// TestDeletingANetworkDeletesItsRows: the schema relies on ON DELETE CASCADE.
func TestDeletingANetworkDeletesItsRows(t *testing.T) {
	p, enc := openTestDB(t)
	addTestNetwork(t, p, enc, "gone")
	addTestNetwork(t, p, enc, "stays")
	for _, net := range []string{"gone", "stays"} {
		for _, q := range []string{
			`INSERT INTO nodes (id, network_id, hostname, agent_token) VALUES ('node-' || ?1, ?1, 'h', 't')`,
			`INSERT INTO enrollment_bundles (id, node_id, download_token, expires_at) VALUES (?1, 'node-' || ?1, ?1, 0)`,
			`INSERT INTO dns_records (id, network_id, name, nebula_ip) VALUES (?1, ?1, 'n', '10.0.0.1')`,
			`INSERT INTO network_members (id, network_id, user_id) VALUES (?1, ?1, 'owner')`,
			`INSERT INTO network_invites (id, network_id, created_by, code) VALUES (?1, ?1, 'owner', ?1)`,
			`INSERT INTO network_events (network_id, event_type, created_at) VALUES (?1, 'x', 0)`,
		} {
			if _, err := p.WriteDB.Exec(q, net); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	if err := NewNetworkStore(p, enc).Delete("gone"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nodes", "enrollment_bundles", "dns_records", "network_members", "network_invites", "network_events"} {
		var n int
		if err := p.ReadDB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s: %d rows left, want 1 (the other network's)", table, n)
		}
	}
}

// TestNetworkEvents_DeletedNetworkDoesNotLoseTheBatch: events are written up
// to two seconds late, so one can arrive for a network deleted meanwhile. The
// other events of the batch still have to be stored.
func TestNetworkEvents_DeletedNetworkDoesNotLoseTheBatch(t *testing.T) {
	p, enc := openTestDB(t)
	addTestNetwork(t, p, enc, "net")
	s := NewNetworkEventStore(p)
	s.Record("net", "before", nil, nil, nil)
	s.Record("deleted", "orphan", nil, nil, nil)
	s.Record("net", "after", nil, nil, nil)
	s.Flush()

	events, err := s.ListForNetwork("net", 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.EventType)
	}
	if len(got) != 2 {
		t.Errorf("stored events %v, want before and after", got)
	}
}

// TestPrune_OnlyWhenConfigured: a deleted node can never come back with the
// identity it has on disk, so a machine that was off for a while must not be
// deleted unless the operator asked for it.
func TestPrune_OnlyWhenConfigured(t *testing.T) {
	for env, want := range map[string]time.Duration{
		"": 0, "0": 0, "-5": 0, "junk": 0, "24": 24 * time.Hour,
	} {
		t.Setenv("HOPSSH_NODE_PRUNE_AGE_HOURS", env)
		if got := pruneAgeDuration(); max(got, 0) != want {
			t.Errorf("HOPSSH_NODE_PRUNE_AGE_HOURS=%q: prune after %v, want %v", env, got, want)
		}
	}

	store := newTestNodeStore(t)
	now := time.Now().Unix()
	insertTestNode(t, store, "net", "half-a-year", "offline", now-180*86400)
	insertTestNode(t, store, "net", "yesterday", "offline", now-86400)
	store.runPruneSweep(30 * 24 * time.Hour)
	if left := remainingHostnames(t, store, "net"); left["half-a-year"] || !left["yesterday"] {
		t.Errorf("after pruning at 30 days: %v", left)
	}
}
