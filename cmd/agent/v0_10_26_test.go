package main

// Tests for v0.10.26 fixes — port-clobber prevention, reload resilience,
// watcher observability, self-loop filter, duplicate-port self-heal.
//
// Each test names the Fix it covers (B/C/D/E/F per the v0.10.26 plan;
// Fix A is server-side and tested in internal/api/).

import (
	"path/filepath"
	"testing"
	"time"
)

// --- Fix B — agent rejects mismatched server-pushed listenPort ---

func TestFixB_ApplyNebulaConfigUpdate_RejectsMismatchedListenPort(t *testing.T) {
	dir := withTempConfigDir(t)
	enrollDir := filepath.Join(dir, "home")
	writeFakeNebulaYAML(t, enrollDir, 4243) // on-disk = enrollment's correct port

	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4243})

	// Server pushes 4242 — should be REJECTED (does not match enrollment's 4243).
	bogus := 4242
	update := &nebulaConfigUpdate{ListenPort: &bogus}
	if err := applyNebulaConfigUpdate(inst, update); err != nil {
		t.Fatalf("applyNebulaConfigUpdate: %v", err)
	}

	// On-disk port must still be 4243 — server's 4242 was rejected.
	if got := readListenPortYAML(t, enrollDir); got != 4243 {
		t.Errorf("Fix B FAILED: yaml port = %d, want 4243 (server clobbered local port)", got)
	}
}

func TestFixB_ApplyNebulaConfigUpdate_AcceptsMatchingListenPort(t *testing.T) {
	dir := withTempConfigDir(t)
	enrollDir := filepath.Join(dir, "home")
	writeFakeNebulaYAML(t, enrollDir, 4243)

	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4243})

	// Server pushes 4243 — matches enrollment, harmless to apply.
	matching := 4243
	update := &nebulaConfigUpdate{ListenPort: &matching}
	if err := applyNebulaConfigUpdate(inst, update); err != nil {
		t.Fatalf("applyNebulaConfigUpdate: %v", err)
	}
	if got := readListenPortYAML(t, enrollDir); got != 4243 {
		t.Errorf("matching port lost: got %d want 4243", got)
	}
}

func TestFixB_ApplyNebulaConfigUpdate_AbsentListenPortIsHonored(t *testing.T) {
	// Fix A behavior: server now omits listenPort entirely. Agent
	// should keep on-disk value as-is.
	dir := withTempConfigDir(t)
	enrollDir := filepath.Join(dir, "home")
	writeFakeNebulaYAML(t, enrollDir, 4243)

	inst := newMeshInstance(&Enrollment{Name: "home", ListenPort: 4243})

	update := &nebulaConfigUpdate{ListenPort: nil}
	if err := applyNebulaConfigUpdate(inst, update); err != nil {
		t.Fatalf("applyNebulaConfigUpdate: %v", err)
	}
	if got := readListenPortYAML(t, enrollDir); got != 4243 {
		t.Errorf("yaml port = %d, want 4243 (no server push, no change)", got)
	}
}

// --- Fix F — boot-time duplicate-port self-heal ---

func TestFixF_HealDuplicateListenPorts_ReassignsNewer(t *testing.T) {
	dir := t.TempDir()
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}

	older := time.Date(2026, 4, 19, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 19, 8, 1, 0, 0, time.UTC)
	if err := reg.Add(&Enrollment{Name: "work", ListenPort: 4242, EnrolledAt: older}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "home", ListenPort: 4242, EnrolledAt: newer}); err != nil {
		t.Fatal(err)
	}

	renumbered, err := reg.HealDuplicateListenPorts(4242)
	if err != nil {
		t.Fatalf("HealDuplicateListenPorts: %v", err)
	}
	if len(renumbered) != 1 {
		t.Fatalf("got %d renamed, want 1: %v", len(renumbered), renumbered)
	}
	if renumbered[0] != "home" {
		t.Errorf("renumbered = %s, want 'home' (newer of the two)", renumbered[0])
	}
	work := reg.Get("work")
	home := reg.Get("home")
	if work.ListenPort != 4242 {
		t.Errorf("older enrollment lost its port: work=%d, want 4242", work.ListenPort)
	}
	if home.ListenPort == 4242 {
		t.Errorf("newer enrollment kept colliding port: home=4242")
	}
	if home.ListenPort < 4242 {
		t.Errorf("home reassigned below base: %d", home.ListenPort)
	}
}

func TestFixF_HealDuplicateListenPorts_NoOpOnHealthyRegistry(t *testing.T) {
	dir := t.TempDir()
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "work", ListenPort: 4242}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&Enrollment{Name: "home", ListenPort: 4243}); err != nil {
		t.Fatal(err)
	}
	renumbered, err := reg.HealDuplicateListenPorts(4242)
	if err != nil {
		t.Fatalf("HealDuplicateListenPorts: %v", err)
	}
	if len(renumbered) != 0 {
		t.Errorf("healthy registry triggered rename: %v", renumbered)
	}
}

func TestFixF_HealDuplicateListenPorts_PersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	reg, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	older := time.Date(2026, 4, 19, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 19, 8, 1, 0, 0, time.UTC)
	_ = reg.Add(&Enrollment{Name: "work", ListenPort: 4242, EnrolledAt: older})
	_ = reg.Add(&Enrollment{Name: "home", ListenPort: 4242, EnrolledAt: newer})

	if _, err := reg.HealDuplicateListenPorts(4242); err != nil {
		t.Fatal(err)
	}

	// Reload from disk: confirm the heal was persisted.
	reg2, err := loadEnrollmentRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	work := reg2.Get("work")
	home := reg2.Get("home")
	if work == nil || home == nil {
		t.Fatalf("enrollments lost on reload")
	}
	if work.ListenPort != 4242 || home.ListenPort == 4242 {
		t.Errorf("reload state wrong: work=%d, home=%d (want work=4242, home!=4242)",
			work.ListenPort, home.ListenPort)
	}
}

func TestFixF_HealDuplicateListenPorts_ThreeWayCollision(t *testing.T) {
	dir := t.TempDir()
	reg, _ := loadEnrollmentRegistry(dir)
	t1 := time.Date(2026, 4, 19, 8, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 19, 8, 1, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 19, 8, 2, 0, 0, time.UTC)
	_ = reg.Add(&Enrollment{Name: "first", ListenPort: 4242, EnrolledAt: t1})
	_ = reg.Add(&Enrollment{Name: "second", ListenPort: 4242, EnrolledAt: t2})
	_ = reg.Add(&Enrollment{Name: "third", ListenPort: 4242, EnrolledAt: t3})

	renumbered, err := reg.HealDuplicateListenPorts(4242)
	if err != nil {
		t.Fatal(err)
	}
	if len(renumbered) != 2 {
		t.Errorf("3-way collision should rename 2, got %d: %v", len(renumbered), renumbered)
	}
	first := reg.Get("first")
	if first.ListenPort != 4242 {
		t.Errorf("oldest enrollment lost port: first=%d", first.ListenPort)
	}
	second := reg.Get("second").ListenPort
	third := reg.Get("third").ListenPort
	if second == 4242 || third == 4242 || second == third {
		t.Errorf("ports not unique after heal: first=4242, second=%d, third=%d",
			second, third)
	}
}

// --- Fix E — self-VPN-IP filter (peer cache + injection paths) ---
//
// The runtime injection path needs a real Nebula control to actually
// inject; that's hard in a unit test. We test the filter LOGIC by
// exercising the helper inst.meshIP() which underpins both injection
// sites' filtering decision.

func TestFixE_MeshIP_NilSafe(t *testing.T) {
	var inst *meshInstance
	if got := inst.meshIP(); got != "" {
		t.Errorf("nil meshInstance.meshIP() = %q, want empty", got)
	}
}

func TestFixE_MeshIP_EmptyOnMissingCert(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir
	// No cert on disk → should return "" without crashing.
	if got := inst.meshIP(); got != "" {
		t.Errorf("missing cert: meshIP() = %q, want empty", got)
	}
}

// --- Fix C — reloadNebula resilience helpers ---

func TestFixC_RetryReloadBackoff_HasMonotonicSchedule(t *testing.T) {
	if len(retryReloadBackoff) < 5 {
		t.Errorf("retry schedule too short: %d entries", len(retryReloadBackoff))
	}
	// Monotonically non-decreasing.
	for i := 1; i < len(retryReloadBackoff); i++ {
		if retryReloadBackoff[i] < retryReloadBackoff[i-1] {
			t.Errorf("schedule not monotonic at index %d: %v < %v",
				i, retryReloadBackoff[i], retryReloadBackoff[i-1])
		}
	}
	// First attempt should be reasonably fast (<1 minute) so transient
	// failures recover quickly.
	if retryReloadBackoff[0] > time.Minute {
		t.Errorf("first retry too slow: %v", retryReloadBackoff[0])
	}
	// Last attempt should be capped (<= 1 hour).
	last := retryReloadBackoff[len(retryReloadBackoff)-1]
	if last > time.Hour {
		t.Errorf("retry cap too high: %v", last)
	}
}

// --- Fix D — watcher observability (smoke test for the constants) ---

func TestFixD_WatcherStartupGraceTicks_Sane(t *testing.T) {
	// watcherStartupGraceTicks should be a small but non-zero number
	// (between 1 and 100). A regression to 0 would make the
	// TUN-down-detection fire spuriously on cold start.
	if watcherStartupGraceTicks < 1 || watcherStartupGraceTicks > 100 {
		t.Errorf("watcherStartupGraceTicks = %d, want 1..100", watcherStartupGraceTicks)
	}
}
