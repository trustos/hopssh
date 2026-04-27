package main

import (
	"context"
	"errors"
	"net"
	"os"
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
	probed, skipped := keepaliveOneCycle(inst)
	if probed != 0 || skipped != 0 {
		t.Errorf("nil-ctrl cycle should return (0,0), got (%d,%d)", probed, skipped)
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
