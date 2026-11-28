package db

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/trustos/hopssh/internal/crypto"
)

// TestMigration009_FirewallPreset: a network that existed before migration
// 009 keeps the "tcp" block its devices have; one created after it gets
// "all". NetworkStore.Create does not name the column, so the default is what
// a new network gets.
func TestMigration009_FirewallPreset(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "hopssh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// The database as a server before 009 left it.
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if _, err := p.WriteDB.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		version := filepath.Base(f)
		if version >= "009" {
			break
		}
		sqlBytes, err := migrationsFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.WriteDB.Exec(string(sqlBytes)); err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if _, err := p.WriteDB.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			t.Fatal(err)
		}
	}
	enc, err := crypto.NewEncryptor(strings.Repeat("00", 32))
	if err != nil {
		t.Fatal(err)
	}
	addTestNetwork(t, p, enc, "old")

	if err := Migrate(p.WriteDB); err != nil {
		t.Fatal(err)
	}
	addTestNetwork(t, p, enc, "new")

	store := NewNetworkStore(p, enc)
	for id, want := range map[string]string{"old": "tcp", "new": "all"} {
		got, err := store.FirewallPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("network %q has firewall preset %q, want %q", id, got, want)
		}
	}
	if _, err := store.FirewallPreset("missing"); err == nil {
		t.Error("FirewallPreset of a missing network: no error")
	}
}
