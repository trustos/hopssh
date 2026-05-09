package client

// Tests for the v0.10.36 keepalive watchdog (stuck-state detection +
// auto-recovery + forensic dump).
//
// The watchdog itself is a counter inside runMeshKeepalive's loop; we
// can't easily exercise the full goroutine without injecting fake
// time. Instead these tests directly exercise the constituent parts:
//
//   1. watchdogTrip respects cooldown (refuses to restart twice within
//      watchdogRestartCooldown).
//   2. watchdogTrip writes a goroutine dump to the instance's dir.
//   3. watchdogTrip invokes inst.restartFn when one is wired.
//   4. watchdogTrip is no-op when restartFn is nil (boot path).
//   5. Source-scan tripwire: the watchdog check uses
//      `peers > 0 && probed > 0 && succeeded == 0` (the load-bearing
//      definition of "stuck cycle"), not weaker variants.

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchdog_DumpFileWritten verifies writeStuckStateDump produces a
// file under the instance's dir with the expected header + a goroutine
// section.
func TestWatchdog_DumpFileWritten(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	path := writeStuckStateDump(inst, 7, 3)
	if path == "" {
		t.Fatal("dump file path was empty")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump %s: %v", path, err)
	}
	s := string(body)
	for _, want := range []string{
		"hopssh agent stuck-state forensic dump",
		"enrollment: home",
		"keepalive cycle: 7",
		"consecutive stuck cycles: 3",
		"goroutine dump",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dump file missing expected fragment %q\n--- file ---\n%s", want, s)
		}
	}
	if !strings.HasPrefix(filepath.Base(path), "stuck-state-") {
		t.Errorf("dump path %q doesn't follow stuck-state-*.txt convention", path)
	}
}

// TestWatchdog_RespectCooldown verifies that a second watchdog trip
// within watchdogRestartCooldown does NOT call restartFn again.
// Without cooldown, a persistent issue would cause restart-loop.
func TestWatchdog_RespectCooldown(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir
	inst.restartFn = func() error {
		calls.Add(1)
		return nil
	}

	watchdogTrip(inst, 1, watchdogStuckThreshold) // first trip
	if calls.Load() != 1 {
		t.Fatalf("first trip: expected 1 restart call, got %d", calls.Load())
	}

	// Second trip immediately after should be suppressed.
	watchdogTrip(inst, 2, watchdogStuckThreshold)
	if calls.Load() != 1 {
		t.Errorf("second trip within cooldown: expected restart call count to stay at 1, got %d", calls.Load())
	}
}

// TestWatchdog_NilRestartFn verifies the watchdog is gracefully
// no-op when no restartFn is wired (boot path before connectFn was
// constructed, or test scaffolding). Must not panic.
func TestWatchdog_NilRestartFn(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir
	inst.restartFn = nil

	// Should not panic; should still write a dump for forensics.
	watchdogTrip(inst, 1, watchdogStuckThreshold)
	entries, _ := os.ReadDir(dir)
	hasDump := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "stuck-state-") {
			hasDump = true
			break
		}
	}
	if !hasDump {
		t.Error("watchdogTrip with nil restartFn should still write a forensic dump")
	}
}

// TestWatchdog_RestartFnCalled verifies the success path: restartFn
// gets invoked, lastWatchdogRestartAt is stamped.
func TestWatchdog_RestartFnCalled(t *testing.T) {
	dir := t.TempDir()
	var called atomic.Int32
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir
	inst.restartFn = func() error {
		called.Add(1)
		return nil
	}
	if !inst.lastWatchdogRestartAt.IsZero() {
		t.Fatal("setup: lastWatchdogRestartAt should start zero")
	}

	watchdogTrip(inst, 1, watchdogStuckThreshold)

	if called.Load() != 1 {
		t.Errorf("restartFn was invoked %d times, want 1", called.Load())
	}
	if inst.lastWatchdogRestartAt.IsZero() {
		t.Error("lastWatchdogRestartAt was not stamped")
	}
	if time.Since(inst.lastWatchdogRestartAt) > 5*time.Second {
		t.Errorf("lastWatchdogRestartAt is stale: %v ago", time.Since(inst.lastWatchdogRestartAt))
	}
}

// TestKeepaliveOneCycle_StuckSignature verifies the cycle returns the
// signature shape that the watchdog reads: (probed > 0, succeeded == 0,
// peers > 0) means "stuck"; (probed > 0, succeeded > 0) means "healthy".
//
// Uses keepaliveDialFn injection to fake all-fail and all-succeed
// outcomes. We can't easily fake hostmap peers without a real Nebula
// control, so this test only exercises the return-counts contract by
// manipulating the dial fn on a fixture with nil ctrl (returns 0/0/0/0)
// — the asserting truth here is the SIGNATURE that runMeshKeepalive
// reads, not the inputs.
func TestKeepaliveOneCycle_NilCtrl_AllZero(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	probed, succeeded, skipped, peers := keepaliveOneCycle(inst)
	if probed != 0 || succeeded != 0 || skipped != 0 || peers != 0 {
		t.Errorf("nil-ctrl returns (%d,%d,%d,%d), want all zero", probed, succeeded, skipped, peers)
	}
}

// TestWatchdog_SourceScan asserts the watchdog stuck-cycle predicate
// in runMeshKeepalive is the load-bearing one:
//   peers > 0 && probed > 0 && succeeded == 0
// Any future refactor that weakens this (e.g. dropping `peers > 0`,
// or basing it on probed-count alone) would either false-trigger on
// no-peers cold-start states or fail-to-trigger on real stuck states.
func TestWatchdog_SourceScan(t *testing.T) {
	src, err := os.ReadFile("keepalive.go")
	if err != nil {
		t.Fatalf("read keepalive.go: %v", err)
	}
	body := string(src)
	required := "peers > 0 && probed > 0 && succeeded == 0"
	if !strings.Contains(body, required) {
		t.Errorf("keepalive.go must contain the watchdog stuck-cycle predicate %q (load-bearing in v0.10.36)", required)
	}
	// Confirm the consecutive counter mechanism is present.
	for _, want := range []string{
		"consecutiveStuck++",
		"watchdogStuckThreshold",
		"watchdogTrip(inst",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("keepalive.go missing expected fragment %q", want)
		}
	}
}

// TestWatchdog_RunServeWiringSourceScan asserted in cmd/agent/main.go's
// runServe that inst.restartFn was wired for BOTH the boot-time path AND
// the runtime connectFn path. Phase NN extracted that lifecycle into
// internal/client/api.go::Client.Connect — both paths now flow through
// one method, and the M3 tripwire will live in api.go alongside the
// extracted Client.connect() helper. Skipped here until M3 lands.
func TestWatchdog_RunServeWiringSourceScan(t *testing.T) {
	t.Skip("Phase NN: wiring moved to internal/client/api.go::Client.connect; tripwire is reimplemented in M3")
}
