package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateEnrollmentsToSystem_MovesFiles verifies the happy path:
// a source dir with one valid enrollment + the registry file is fully
// moved to the destination, and the source ends up empty (so the
// parallel-install detector won't relight the warning banner).
func TestMigrateEnrollmentsToSystem_MovesFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Stage a minimally-valid enrollment in src.
	mustSeedEnrollment(t, src, "home")

	if err := migrateEnrollmentsToSystem(src, dst); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Source's enrollments.json + subdir should be gone.
	if _, err := os.Stat(filepath.Join(src, "enrollments.json")); !os.IsNotExist(err) {
		t.Errorf("source registry not removed: stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "home")); !os.IsNotExist(err) {
		t.Errorf("source enrollment subdir not removed: stat err = %v", err)
	}

	// Dest should have everything.
	for _, p := range []string{
		filepath.Join(dst, "enrollments.json"),
		filepath.Join(dst, "home", "node.crt"),
		filepath.Join(dst, "home", "node.key"),
		filepath.Join(dst, "home", "ca.crt"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dest missing %s: %v", p, err)
		}
	}
}

// TestMigrateEnrollmentsToSystem_RejectsMalformed asserts that if any
// enrollment in source is missing a required cert file, the migration
// aborts WITHOUT moving anything. This is the load-bearing safety
// guarantee — a half-migrated state would leave the user with no
// working install on either side.
func TestMigrateEnrollmentsToSystem_RejectsMalformed(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	mustSeedEnrollment(t, src, "home")
	// Corrupt: remove node.key from the home enrollment.
	if err := os.Remove(filepath.Join(src, "home", "node.key")); err != nil {
		t.Fatal(err)
	}

	err := migrateEnrollmentsToSystem(src, dst)
	if err == nil {
		t.Fatal("expected migration to fail with missing node.key")
	}

	// Source should be untouched.
	if _, err := os.Stat(filepath.Join(src, "enrollments.json")); err != nil {
		t.Errorf("source registry got moved despite migration failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "home", "node.crt")); err != nil {
		t.Errorf("source enrollment subdir got moved despite migration failure: %v", err)
	}
	// Dest should still be empty (no enrollments.json).
	if _, err := os.Stat(filepath.Join(dst, "enrollments.json")); !os.IsNotExist(err) {
		t.Errorf("dest got partial migration: %v", err)
	}
}

// TestMigrateEnrollmentsToSystem_EmptySourceIsNoOp asserts that an
// empty source (no enrollments.json) is a successful no-op. Used by
// the desktop's "convert to system service" flow when the user
// enables background mode BEFORE enrolling — the migrate step needs
// to succeed cleanly so the LaunchDaemon install proceeds.
func TestMigrateEnrollmentsToSystem_EmptySourceIsNoOp(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	// Don't seed anything.
	if err := migrateEnrollmentsToSystem(src, dst); err != nil {
		t.Fatalf("empty-source migrate should succeed; got: %v", err)
	}
}

// TestMigrateEnrollmentsToSystem_RefusesNonEmptyDest asserts that we
// don't silently merge two registries when the dest already has
// enrollments. Operator must explicitly clean dest first.
func TestMigrateEnrollmentsToSystem_RefusesNonEmptyDest(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	mustSeedEnrollment(t, src, "home")
	mustSeedEnrollment(t, dst, "work") // different name, but dest is non-empty

	err := migrateEnrollmentsToSystem(src, dst)
	if err == nil {
		t.Fatal("expected migration to refuse non-empty dest")
	}
}

// TestDeriveRunMode covers the configDir → runMode mapping that the
// desktop "Run in the background" toggle binds to.
func TestDeriveRunMode(t *testing.T) {
	// On non-darwin we always report "system" (no toggle exposed).
	// On darwin: only /etc/hop-agent counts as system mode.
	cases := []struct {
		dir  string
		want string
	}{
		// These cases are darwin-meaningful; on other OSes deriveRunMode
		// short-circuits to "system" regardless. Test runs on whatever
		// platform we're on; both branches are correct.
		{dir: "/etc/hop-agent", want: "system"},
	}
	for _, c := range cases {
		got := deriveRunMode(c.dir)
		if got != c.want {
			t.Errorf("deriveRunMode(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

// mustSeedEnrollment writes a minimally-valid enrollment into the
// given configDir so migrateEnrollmentsToSystem accepts it.
func mustSeedEnrollment(t *testing.T, dir, name string) {
	t.Helper()
	subdir := filepath.Join(dir, name)
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write the three required files. Contents don't matter for the
	// migration's structural check.
	for _, f := range []string{"node.crt", "node.key", "ca.crt"} {
		if err := os.WriteFile(filepath.Join(subdir, f), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Add the enrollment to the registry. Use the registry's Add API
	// so the persisted file matches the production format.
	prevConfigDir := configDir
	configDir = dir
	t.Cleanup(func() { configDir = prevConfigDir })

	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{
		Name:     name,
		NodeID:   "test-" + name,
		Endpoint: "https://hopssh.test",
		TunMode:  "userspace",
	}); err != nil {
		t.Fatal(err)
	}
}
