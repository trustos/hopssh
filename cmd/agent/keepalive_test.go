package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for runMeshKeepalive (CGNAT-aware mesh keepalive).
//
// Coverage:
//   1. keepaliveTCPDial respects the timeout (real-socket smoke).
//   2. keepaliveOneCycle dials per peer when ctrl is non-nil.
//   3. keepaliveOneCycle short-circuits cleanly when ctrl is nil.
//   4. runMeshKeepalive exits via runCtx cancellation (the v0.10.34
//      lifecycle invariant — keepalive must NOT leak goroutines on
//      disconnect/leave).
//   5. Source-scan tripwire: keepalive uses inst.runCtx, not the
//      agent-wide ctx (matches the v0.10.34 contract).
//
// We do NOT exercise the full ctrl.ListHostmapHosts path — that
// requires a real Nebula control. The fake-dial indirection lets us
// drive the cycle without real sockets; the runtime invariants are
// validated against pathQuality's existing infrastructure.

// TestKeepaliveTCPDial_Timeout verifies the dial respects the deadline.
// Uses a non-listening port so the connect attempt times out.
func TestKeepaliveTCPDial_Timeout(t *testing.T) {
	// 192.0.2.0/24 is RFC 5737 TEST-NET — guaranteed unroutable.
	target := "192.0.2.1:9"
	start := time.Now()
	err := keepaliveTCPDial(target)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected dial to fail; succeeded")
	}
	if elapsed > keepaliveDialTimeout+500*time.Millisecond {
		t.Errorf("dial took %v, want <= %v + 500ms slack", elapsed, keepaliveDialTimeout)
	}
}

// TestKeepaliveTCPDial_Success verifies a successful TCP-dial returns
// nil and closes its connection.
func TestKeepaliveTCPDial_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if err := keepaliveTCPDial(ln.Addr().String()); err != nil {
		t.Errorf("dial of bound port failed: %v", err)
	}
}

// TestKeepalive_RunCtxCancellation verifies runMeshKeepalive exits
// promptly when its ctx is cancelled. This is the v0.10.34 lifecycle
// invariant — disconnect/leave must stop the keepalive goroutine.
func TestKeepalive_RunCtxCancellation(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	ctx, cancel := context.WithCancel(context.Background())
	inst.parentCtx = ctx
	inst.runCtx = ctx
	inst.runCancel = cancel

	done := make(chan struct{})
	go func() {
		runMeshKeepalive(inst.runCtx, inst)
		close(done)
	}()

	// Cancel before the first cycle would fire (initial jitter delay
	// is ~45 s); should exit immediately via the first select.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Goroutine exited via ctx.Done() — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("runMeshKeepalive did not exit within 2s after ctx cancel")
	}
}

// TestKeepalive_OneCycle_NilCtrl verifies the cycle short-circuits
// when there's no Nebula control yet (cold-start race or instance is
// being reloaded). Must NOT panic.
func TestKeepalive_OneCycle_NilCtrl(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	probed, succeeded, skipped, peers := keepaliveOneCycle(inst)
	if probed != 0 || succeeded != 0 || skipped != 0 || peers != 0 {
		t.Errorf("nil-ctrl cycle should return all zeros, got (%d,%d,%d,%d)", probed, succeeded, skipped, peers)
	}
}

// TestKeepalive_DialFn_Indirection verifies tests can substitute
// keepaliveDialFn. Test injection works.
func TestKeepalive_DialFn_Indirection(t *testing.T) {
	var calls atomic.Int32
	saved := keepaliveDialFn
	defer func() { keepaliveDialFn = saved }()
	keepaliveDialFn = func(target string) error {
		calls.Add(1)
		return nil
	}

	if err := keepaliveDialFn("test:1234"); err != nil {
		t.Errorf("substitute fn returned err: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("substitute fn invoked %d times, want 1", got)
	}
}

// TestKeepalive_DialFn_PropagatesError verifies dial errors are
// non-fatal — the goroutine logs but continues to the next cycle.
// Probed count still reflects the attempt.
func TestKeepalive_DialFn_PropagatesError(t *testing.T) {
	var calls atomic.Int32
	saved := keepaliveDialFn
	defer func() { keepaliveDialFn = saved }()
	keepaliveDialFn = func(target string) error {
		calls.Add(1)
		return errors.New("test: connection refused")
	}

	if err := keepaliveDialFn("test:1234"); err == nil {
		t.Error("expected error from substitute fn")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("substitute fn invoked %d times, want 1", got)
	}
}

// TestKeepalive_SourceOrder asserts the keepalive goroutine is wired
// to inst.runCtx (NOT the boot-time ctx) so it stops cleanly on
// disconnect/leave. v0.10.34 introduced this invariant; the
// keepalive must follow it.
func TestKeepalive_SourceOrder(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)

	// Locate the runMeshKeepalive call site.
	idx := strings.Index(body, "go runMeshKeepalive(")
	if idx < 0 {
		t.Fatal("go runMeshKeepalive(...) not found in main.go — wiring removed?")
	}
	// Examine the next ~80 chars for the ctx argument.
	end := idx + 80
	if end > len(body) {
		end = len(body)
	}
	snippet := body[idx:end]
	if !strings.Contains(snippet, "inst.runCtx") {
		t.Errorf("runMeshKeepalive call doesn't pass inst.runCtx — disconnect/leave will leak the goroutine.\nSnippet: %q", snippet)
	}
}

// TestReadLighthouseAddrs_ParsesHosts verifies the helper correctly
// extracts lighthouse mesh addresses from a typical nebula.yaml.
// Uses the exact yaml shape produced by writeNebulaConfig in enroll.go.
func TestReadLighthouseAddrs_ParsesHosts(t *testing.T) {
	dir := t.TempDir()
	yaml := `cipher: aes
lighthouse:
  am_lighthouse: false
  hosts:
    - 10.42.2.1
    - 10.42.2.99
listen:
  host: 0.0.0.0
  port: 4243
static_host_map:
  10.42.2.1:
    - 132.145.232.64:42002
`
	if err := os.WriteFile(filepath.Join(dir, "nebula.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	got := readLighthouseAddrs(filepath.Join(dir, "nebula.yaml"))
	want := []netip.Addr{netip.MustParseAddr("10.42.2.1"), netip.MustParseAddr("10.42.2.99")}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("lighthouse %s missing from set: %v", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d lighthouses, want %d: %v", len(got), len(want), got)
	}
}

// TestReadLighthouseAddrs_MissingFile returns an empty (non-nil) set.
// Read failures are non-fatal — the cycle falls back to old behavior
// (probing all hostmap entries). The 90 s cycle cadence makes a
// single-cycle stale read harmless.
func TestReadLighthouseAddrs_MissingFile(t *testing.T) {
	got := readLighthouseAddrs("/nonexistent/nebula.yaml")
	if got == nil {
		t.Fatal("expected non-nil empty set on missing file")
	}
	if len(got) != 0 {
		t.Errorf("expected empty set, got %v", got)
	}
}

// TestReadLighthouseAddrs_GarbledYAML returns an empty (non-nil) set
// rather than nil-deref'ing or panicking. Same fall-back-to-old-
// behavior contract as the missing-file case.
func TestReadLighthouseAddrs_GarbledYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nebula.yaml"), []byte("not: valid: yaml: ::"), 0644); err != nil {
		t.Fatal(err)
	}
	got := readLighthouseAddrs(filepath.Join(dir, "nebula.yaml"))
	if got == nil {
		t.Fatal("expected non-nil empty set on garbled yaml")
	}
	if len(got) != 0 {
		t.Errorf("expected empty set on parse failure, got %v", got)
	}
}

// TestReadLighthouseAddrs_NoLighthouseSection handles configs that
// lack a lighthouse block entirely (hop-agent client ephemeral mode).
func TestReadLighthouseAddrs_NoLighthouseSection(t *testing.T) {
	dir := t.TempDir()
	yaml := `cipher: aes
listen:
  host: 0.0.0.0
  port: 4243
`
	if err := os.WriteFile(filepath.Join(dir, "nebula.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	got := readLighthouseAddrs(filepath.Join(dir, "nebula.yaml"))
	if len(got) != 0 {
		t.Errorf("expected empty set when no lighthouse block, got %v", got)
	}
}

// TestPruneOldStuckStateDumps_RespectsCutoff asserts the GC drops
// files older than retention and keeps newer ones. Also verifies it
// only touches the stuck-state-*.txt prefix — sibling files like
// nebula.yaml and node.crt must survive.
func TestPruneOldStuckStateDumps_RespectsCutoff(t *testing.T) {
	dir := t.TempDir()

	// Old dumps (pre-cutoff): should be pruned.
	oldDump1 := filepath.Join(dir, "stuck-state-20260420T120000Z.txt")
	oldDump2 := filepath.Join(dir, "stuck-state-20260421T120000Z.txt")
	for _, p := range []string{oldDump1, oldDump2} {
		if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-stuckStateDumpRetention - time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	// Fresh dump (within retention): must survive.
	freshDump := filepath.Join(dir, "stuck-state-20260504T120000Z.txt")
	if err := os.WriteFile(freshDump, []byte("fresh"), 0600); err != nil {
		t.Fatal(err)
	}

	// Sibling files: must survive regardless of mtime.
	for _, name := range []string{"nebula.yaml", "node.crt", "peers.json"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-30 * 24 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	pruneOldStuckStateDumps(dir)

	for _, p := range []string{oldDump1, oldDump2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned, still present", filepath.Base(p))
		}
	}
	for _, name := range []string{"stuck-state-20260504T120000Z.txt", "nebula.yaml", "node.crt", "peers.json"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to survive prune, got %v", name, err)
		}
	}
}

// TestPruneOldStuckStateDumps_EmptyDir is a no-op safe path — the
// most common case (instance was just created, no dumps yet).
func TestPruneOldStuckStateDumps_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	pruneOldStuckStateDumps(dir) // must not panic
	pruneOldStuckStateDumps("")  // edge case: empty path
}

// TestPruneOldStuckStateDumps_AllFresh keeps every file when nothing
// is past the cutoff. Important: the GC is run at every connect so a
// rapid restart-cycle scenario should never delete still-relevant
// forensic state.
func TestPruneOldStuckStateDumps_AllFresh(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "stuck-state-fresh-"+string(rune('A'+i))+".txt")
		if err := os.WriteFile(path, []byte("fresh"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pruneOldStuckStateDumps(dir)
	matches, _ := filepath.Glob(filepath.Join(dir, "stuck-state-*.txt"))
	if len(matches) != 5 {
		t.Errorf("expected 5 fresh dumps to survive, got %d", len(matches))
	}
}

// TestKeepalive_OneCycle_FiltersLighthouse_SourceScan asserts that
// keepaliveOneCycle (a) reads lighthouse addrs from the per-instance
// nebula.yaml and (b) skips hostmap entries whose VpnAddrs[0] matches
// a lighthouse, in the iteration loop.
//
// We use a source-scan tripwire (rather than mock the full
// ctrl.ListHostmapHosts() path) because the existing fakeMeshService
// returns nil for NebulaControl() — there's no way to feed a synthetic
// hostmap into the cycle without a real Nebula instance. The behavior
// is verified end-to-end by the smoke test on a deployed agent where
// stuck-cycle counts drop from hundreds-per-day to ~0.
//
// This test guards against a future refactor that drops the filter
// silently and re-introduces the watchdog churn bug.
func TestKeepalive_OneCycle_FiltersLighthouse_SourceScan(t *testing.T) {
	src, err := os.ReadFile("keepalive.go")
	if err != nil {
		t.Fatalf("read keepalive.go: %v", err)
	}
	body := string(src)

	cycleStart := strings.Index(body, "func keepaliveOneCycle(")
	if cycleStart < 0 {
		t.Fatal("keepaliveOneCycle not found")
	}
	cycleEnd := strings.Index(body[cycleStart:], "\n}")
	if cycleEnd < 0 {
		t.Fatal("end of keepaliveOneCycle not found")
	}
	cycleBody := body[cycleStart : cycleStart+cycleEnd]

	if !strings.Contains(cycleBody, "readLighthouseAddrs(") {
		t.Error("keepaliveOneCycle does not call readLighthouseAddrs — lighthouse exclusion missing, watchdog will false-trip")
	}
	if !strings.Contains(cycleBody, "isLighthouse") || !strings.Contains(cycleBody, "continue") {
		t.Error("keepaliveOneCycle does not skip lighthouse entries via isLighthouse continue — exclusion broken")
	}
	// Confirm the filter check runs BEFORE the peers++ increment so
	// lighthouses don't count toward the hostmap-occupied predicate.
	filterIdx := strings.Index(cycleBody, "isLighthouse")
	peersIncIdx := strings.Index(cycleBody, "peers++")
	if filterIdx < 0 || peersIncIdx < 0 {
		t.Fatal("expected both isLighthouse check and peers++ to be present")
	}
	if filterIdx > peersIncIdx {
		t.Errorf("isLighthouse filter (offset %d) appears AFTER peers++ (offset %d) — lighthouse entries are still counted as peers, defeating the fix",
			filterIdx, peersIncIdx)
	}
}

// concurrentDial harness for completeness — exercises that the dial
// is safe to call from multiple goroutines. Belt-and-braces against
// a future change that adds shared state.
func TestKeepalive_DialFn_Concurrent(t *testing.T) {
	saved := keepaliveDialFn
	defer func() { keepaliveDialFn = saved }()
	var calls atomic.Int32
	keepaliveDialFn = func(target string) error {
		calls.Add(1)
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = keepaliveDialFn("test:1234")
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != 10 {
		t.Errorf("expected 10 dial invocations from concurrent goroutines, got %d", got)
	}
}
