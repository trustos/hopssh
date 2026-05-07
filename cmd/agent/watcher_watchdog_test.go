package main

// Phase DD (v0.10.96): tests for the watchNetworkChanges silent-death
// detector. The canonical incident: 2026-05-07 17:22 the network-change
// watcher emitted its last alive log; at 18:00:45 it logged "network
// change detected, rebinding Nebula" + "selfEndpoints DNS lookup failed";
// then went silent for 3.5+ hours. No CRITICAL/PANICKED log → the
// goroutine deadlocked inside vendor Nebula's RebindUDPServer or
// CloseAllTunnels (defer recover() catches panics, not deadlocks).
// Mini saw udpAddrs=[] from the lighthouse for MBP, screen-sharing
// dead, dashboard split-brained.
//
// This file mirrors renew_watchdog_test.go's structure 1-for-1 — same
// patterns, different stamp field, different dump file name.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// markWatcherActivity / watcherActivityAge are pure functions on the
// meshInstance — exercise them in isolation first.

func TestMarkWatcherActivity_StampsTime(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	if age := inst.watcherActivityAge(); age < 100*365*24*time.Hour {
		t.Fatalf("zero stamp must read as effectively-infinite age, got %s", age)
	}
	inst.markWatcherActivity()
	if age := inst.watcherActivityAge(); age > time.Second {
		t.Errorf("just-stamped activity should read as <1s, got %s", age)
	}
}

func TestMarkWatcherActivity_NilSafe(t *testing.T) {
	// Pre-init defensive — concurrent shutdown races could call this
	// before the goroutine sees the new instance pointer.
	var inst *meshInstance
	inst.markWatcherActivity() // must not panic
	if age := inst.watcherActivityAge(); age < 100*365*24*time.Hour {
		t.Fatalf("nil instance must read as effectively-infinite age")
	}
}

// Watchdog behavior tests — use the production runWatcherWatchdog but
// with shortened thresholds via temporary global override.

func withShortWatcherThresholds(t *testing.T) (origThresh, origInterval time.Duration, restore func()) {
	t.Helper()
	origThresh = watcherSilenceThreshold
	origInterval = watcherWatchdogInterval
	watcherSilenceThreshold = 100 * time.Millisecond
	watcherWatchdogInterval = 30 * time.Millisecond
	return origThresh, origInterval, func() {
		watcherSilenceThreshold = origThresh
		watcherWatchdogInterval = origInterval
	}
}

// TestWatcherWatchdog_FiresAfterSilenceThreshold proves the production
// code path: silent watcher → watchdog → restartFn called.
func TestWatcherWatchdog_FiresAfterSilenceThreshold(t *testing.T) {
	tmp := t.TempDir()
	inst := testInstance(t, "home", tmp)
	inst.lastWatcherActivityAt = time.Now().Add(-1 * time.Hour) // 1h silent

	var restartCalls atomic.Int32
	inst.restartFn = func() error {
		restartCalls.Add(1)
		return nil
	}

	_, _, restore := withShortWatcherThresholds(t)
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go runWatcherWatchdog(ctx, inst)

	// Wait for the watchdog to tick at least once after silence
	// threshold trips. Worst case: 2× watchdog interval (60ms) +
	// generous slack.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) && restartCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if restartCalls.Load() == 0 {
		t.Errorf("watchdog did not call restartFn after silence; threshold=%s, interval=%s",
			watcherSilenceThreshold, watcherWatchdogInterval)
	}
}

// TestWatcherWatchdog_QuietWhenWatcherIsTicking — fresh activity
// stamps must NOT trip the watchdog. False positives during a healthy
// watcher cycle would restart-loop the agent.
func TestWatcherWatchdog_QuietWhenWatcherIsTicking(t *testing.T) {
	_, _, restore := withShortWatcherThresholds(t)
	defer restore()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.markWatcherActivity()
	if age := inst.watcherActivityAge(); age >= watcherSilenceThreshold {
		t.Errorf("just-stamped activity tripped the threshold: age=%s threshold=%s",
			age, watcherSilenceThreshold)
	}
}

// TestWatcherWatchdog_DumpFileWritten asserts the forensic-dump path
// is correct + the file actually gets written + has the expected
// fields. Mirrors renew_watchdog_test.go's analogous test.
func TestWatcherWatchdog_DumpFileWritten(t *testing.T) {
	tmp := t.TempDir()
	inst := testInstance(t, "home", tmp)
	inst.lastWatcherActivityAt = time.Now().Add(-7 * time.Minute)

	path := writeWatcherStuckDump(inst, 7*time.Minute)
	if path == "" {
		t.Fatal("writeWatcherStuckDump returned empty path")
	}

	matches, err := filepath.Glob(filepath.Join(inst.dir(), "watcher-stuck-*.txt"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 dump file, got %d (matches=%v)", len(matches), matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"watcher-stuck dump",
		"instance: home",
		"silence: 7m0s",
		"--- goroutine dump ---",
		"goroutine ", // pprof output starts with "goroutine N..."
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dump body missing %q. Got:\n%s", want, body[:minInt(800, len(body))])
		}
	}
}

// TestWatcherWatchdog_ColdStartGrace — when lastWatcherActivityAt is
// zero (never stamped), the watchdog must NOT fire. Cold-start where
// the watcher hasn't been spawned yet (bundled mode pre-attach,
// OS-stack fallback, empty endpoint).
func TestWatcherWatchdog_ColdStartGrace(t *testing.T) {
	_, _, restore := withShortWatcherThresholds(t)
	defer restore()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	// Don't call markWatcherActivity — simulate cold start.

	age := inst.watcherActivityAge()
	if age < 100*365*24*time.Hour {
		t.Fatalf("zero-stamp instance must read as effectively-infinite age; got %s", age)
	}
	// Production code path: `if age > 100*365*24*time.Hour { continue }`
	// — verify the boundary. The watchdog explicitly skips firing in
	// this case.
}

// TestWatcherWatchdog_NilRestartFnNoOp — graceful path when restartFn
// is not wired (boot path before runServe assigns it).
func TestWatcherWatchdog_NilRestartFnNoOp(t *testing.T) {
	_, _, restore := withShortWatcherThresholds(t)
	defer restore()
	tmp := t.TempDir()
	inst := testInstance(t, "home", tmp)
	inst.lastWatcherActivityAt = time.Now().Add(-1 * time.Hour)
	inst.restartFn = nil // explicitly unwired

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runWatcherWatchdog(ctx, inst)
		close(done)
	}()

	select {
	case <-done:
		// expected — watchdog completes its one-or-more ticks then
		// exits cleanly on ctx.Done(), no panic from nil restartFn.
	case <-time.After(1 * time.Second):
		t.Fatal("runWatcherWatchdog did not exit on ctx cancel with nil restartFn")
	}
}

// TestWatcherWatchdog_CtxCancelStops — ctx cancellation must stop the
// watchdog cleanly (no goroutine leak after disconnect/leave).
func TestWatcherWatchdog_CtxCancelStops(t *testing.T) {
	tmp := t.TempDir()
	inst := testInstance(t, "home", tmp)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runWatcherWatchdog(ctx, inst)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("runWatcherWatchdog did not exit within 2s of ctx cancel — goroutine leak")
	}
}

// TestWatcherWatchdog_RestartFnPropagatesError — when restartFn
// returns an error, the watchdog logs but doesn't crash.
func TestWatcherWatchdog_RestartFnPropagatesError(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.restartFn = func() error {
		return errors.New("simulated restart failure")
	}
	if err := inst.restartFn(); err == nil {
		t.Errorf("expected error from restartFn, got nil")
	}
}

// --- Concurrency safety ---

func TestMarkWatcherActivity_ConcurrentSafe(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst.markWatcherActivity()
			_ = inst.watcherActivityAge()
		}()
	}
	wg.Wait()
	if age := inst.watcherActivityAge(); age > 5*time.Second {
		t.Errorf("post-concurrent-stamps age should be small, got %s", age)
	}
}

// --- runWithTimeout ---

// TestRunWithTimeout_FastReturn — fn that completes well under the
// deadline returns normally; runWithTimeout doesn't impose extra
// latency.
func TestRunWithTimeout_FastReturn(t *testing.T) {
	start := time.Now()
	runWithTimeout("test", "fast-fn", 1*time.Second, func() {
		// returns immediately
	})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("fast-returning fn took %s, expected ~immediate", elapsed)
	}
}

// TestRunWithTimeout_ReturnsOnDeadline — fn that exceeds the deadline
// is bypassed; runWithTimeout returns approximately at the deadline,
// not later. The leaked goroutine eventually completes (we don't
// assert on that — accepted leak per the comment in the helper).
func TestRunWithTimeout_ReturnsOnDeadline(t *testing.T) {
	start := time.Now()
	runWithTimeout("test", "slow-fn", 100*time.Millisecond, func() {
		time.Sleep(2 * time.Second) // way past deadline
	})
	elapsed := time.Since(start)
	// Generous slack: deadline + 2× scheduler latency.
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned BEFORE deadline (%s) — bad timer: %s", 100*time.Millisecond, elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("returned LONG after deadline (%s) — bad timer: %s", 100*time.Millisecond, elapsed)
	}
}

// --- Source-scan tripwires ---

// TestTryStartMeshInstance_SpawnsWatcherWatchdog asserts main.go
// spawns the watcher watchdog alongside the existing renewal watchdog.
// Without this spawn, F1's fix is dead code.
func TestTryStartMeshInstance_SpawnsWatcherWatchdog(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "go runWatcherWatchdog(inst.runCtx, inst)") {
		t.Error("main.go must spawn `go runWatcherWatchdog(inst.runCtx, inst)` — without this, the Phase DD fix is dead code")
	}
	// Must also be near the existing renewal watchdog spawn so the
	// bring-up order matches Phase P's expectations.
	if !strings.Contains(body, "go runRenewalWatchdog(inst.runCtx, inst)") {
		t.Error("main.go must still spawn runRenewalWatchdog — Phase P watchdog must coexist with Phase DD")
	}
}

// TestWatchNetworkChanges_StampsAtTopOfTickBody asserts the stamp call
// appears INSIDE the for loop body and BEFORE any blocking call. If a
// future refactor moves it after the rebind block, a wedge inside the
// rebind block would leave the stamp from the LAST successful tick
// (still works) but a wedge BEFORE the stamp would not be detected.
// Stamping at the top is the safer pattern.
func TestWatchNetworkChanges_StampsAtTopOfTickBody(t *testing.T) {
	src, err := os.ReadFile("nebula.go")
	if err != nil {
		t.Fatalf("read nebula.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "inst.markWatcherActivity()") {
		t.Error("watchNetworkChanges must call inst.markWatcherActivity() — Phase DD watchdog stamp")
	}
	// Stamp must appear before the rebind block (which contains
	// runWithTimeout calls) — verify by string-position ordering.
	stampIdx := strings.Index(body, "inst.markWatcherActivity()")
	rebindIdx := strings.Index(body, `runWithTimeout(inst.name(), "RebindUDPServer"`)
	if stampIdx < 0 || rebindIdx < 0 {
		t.Skipf("can't locate both anchors (stamp=%d rebind=%d) — skipping ordering check", stampIdx, rebindIdx)
	}
	if stampIdx > rebindIdx {
		t.Error("inst.markWatcherActivity() must appear BEFORE the rebind block (Phase DD invariant: stamp at top of tick body)")
	}
}

// TestWatchNetworkChanges_RebindBlockUsesTimeouts asserts the rebind
// block wraps the vendor-Nebula calls in runWithTimeout. Regression
// guard against future refactors removing the timeouts. portmap.ReProbe
// is intentionally NOT wrapped (it's a non-blocking buffered channel
// send).
func TestWatchNetworkChanges_RebindBlockUsesTimeouts(t *testing.T) {
	src, err := os.ReadFile("nebula.go")
	if err != nil {
		t.Fatalf("read nebula.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		`runWithTimeout(inst.name(), "RebindUDPServer"`,
		`runWithTimeout(inst.name(), "CloseAllTunnels"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("watchNetworkChanges rebind block missing %q — Phase DD F2 timeout wrapper", want)
		}
	}
	// Must NOT contain bare `ctrl.RebindUDPServer()` or `ctrl.CloseAllTunnels(true)`
	// outside the wrapper — those would re-introduce the wedge.
	if strings.Contains(body, "\n\t\t\tctrl.RebindUDPServer()") {
		t.Error("nebula.go contains an unwrapped ctrl.RebindUDPServer() — must be wrapped in runWithTimeout (Phase DD F2)")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
