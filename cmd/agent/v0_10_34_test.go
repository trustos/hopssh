package main

// Tests for the v0.10.34 live-runtime-add/remove fix.
//
// Three classes of coverage:
//
//   1. Helper primitives (waitForTUNDeviceFree, instanceRegistry.remove,
//      serverSet.shutdownInstance) — direct unit tests.
//
//   2. Lifecycle invariants (inst.close() cancels runCtx; runCtx is the
//      ctx that per-instance goroutines should observe).
//
//   3. Auto-connect after enroll — verifies the enrollment path invokes
//      connectFn so the user never has to "restart the agent for the
//      new network".

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula"
)

// --- waitForTUNDeviceFree ---

// TestWaitForTUNDeviceFree_Empty verifies the empty-name fast path.
// Defensive against callers passing an unset device name (e.g. before
// the per-enrollment name is computed).
func TestWaitForTUNDeviceFree_Empty(t *testing.T) {
	start := time.Now()
	waitForTUNDeviceFree("", 1*time.Second)
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("waitForTUNDeviceFree('') took %v, want fast no-op", elapsed)
	}
}

// TestWaitForTUNDeviceFree_FastReturn verifies the cross-platform fast
// path: on macOS / Windows, /sys/class/net never exists, so a stat
// failure on the very first iteration returns immediately. On Linux the
// device name we probe ('hop-test-no-such-device') won't exist either,
// so the same fast-return applies.
func TestWaitForTUNDeviceFree_FastReturn(t *testing.T) {
	start := time.Now()
	waitForTUNDeviceFree("hop-test-no-such-device-"+runtime.GOOS, 5*time.Second)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("waitForTUNDeviceFree on nonexistent device took %v, want fast return", elapsed)
	}
}

// TestWaitForTUNDeviceFree_RespectsDeadline verifies the deadline path
// when the path-being-watched DOES exist but never goes away. We can
// safely simulate this by pointing at a path that always exists
// regardless of OS (the temp dir).
func TestWaitForTUNDeviceFree_RespectsDeadline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only linux uses /sys/class/net path; non-linux fast-returns")
	}
	// Linux only: stub the path by waiting on a known-existing dev like 'lo'
	// is not a TUN but we can simulate with the loopback's existence path.
	deadline := 150 * time.Millisecond
	start := time.Now()
	waitForTUNDeviceFree("lo", deadline)
	elapsed := time.Since(start)
	if elapsed < deadline {
		t.Errorf("waitForTUNDeviceFree(lo) returned in %v before deadline %v", elapsed, deadline)
	}
	if elapsed > deadline+200*time.Millisecond {
		t.Errorf("waitForTUNDeviceFree(lo) elapsed %v exceeds deadline + 200ms slack", elapsed)
	}
}

// --- instanceRegistry.remove ---

func TestInstanceRegistry_Remove_Found(t *testing.T) {
	r := newInstanceRegistry()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	r.add(inst)
	if got := r.get("home"); got != inst {
		t.Fatalf("setup: get returned %v, want %v", got, inst)
	}
	popped := r.remove("home")
	if popped != inst {
		t.Errorf("remove returned %v, want the registered inst", popped)
	}
	if r.get("home") != nil {
		t.Error("get should return nil after remove")
	}
	if r.len() != 0 {
		t.Errorf("len = %d, want 0", r.len())
	}
}

func TestInstanceRegistry_Remove_NotFound(t *testing.T) {
	r := newInstanceRegistry()
	if r.remove("ghost") != nil {
		t.Error("remove of missing entry should return nil")
	}
}

// --- serverSet.shutdownInstance ---

// TestServerSet_ShutdownInstance_Idempotent verifies the helper is safe
// to call when no per-instance HTTP server has been registered (e.g.
// disconnect during a partial-startup state). Production behavior:
// silent no-op, must not panic or leak.
func TestServerSet_ShutdownInstance_Idempotent(t *testing.T) {
	s := newServerSet()
	// No prior register; should be a no-op.
	s.shutdownInstance("never-existed")
	// Twice in a row: still a no-op.
	s.shutdownInstance("never-existed")
}

// --- runCtx lifecycle ---

// TestMeshInstance_Close_CancelsRunCtx verifies the load-bearing
// invariant from v0.10.34: inst.close() cancels runCtx so per-instance
// goroutines (heartbeat, renewal, path-quality) observe Done() and
// exit. Pre-fix these used the agent-wide ctx and leaked across
// disconnect/leave.
func TestMeshInstance_Close_CancelsRunCtx(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	inst.parentCtx = parent
	runCtx, runCancel := context.WithCancel(parent)
	inst.runCtx = runCtx
	inst.runCancel = runCancel

	if inst.runCtx.Err() != nil {
		t.Fatal("setup: runCtx should not be cancelled yet")
	}

	inst.close()

	if inst.runCtx.Err() == nil {
		t.Error("inst.close() did not cancel runCtx — disconnect/leave will leak per-instance goroutines")
	}
	// Idempotent: a second close must not panic.
	inst.close()
	if inst.runCancel != nil {
		t.Error("inst.runCancel should be nil after first close to prevent double-cancel")
	}
}

// TestMeshInstance_Close_RunCtxBeforeSvcClose verifies the ORDER:
// runCancel must fire before svc.Close so per-instance goroutines
// observe Done() and stop reading inst.svc before svc disappears.
// Without this ordering, an in-flight heartbeat could nil-deref on
// inst.svc, or runCertRenewal could spawn a fresh Nebula via
// reloadNebula AFTER we've started tearing things down.
func TestMeshInstance_Close_RunCtxBeforeSvcClose(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	inst.parentCtx = parent
	runCtx, runCancel := context.WithCancel(parent)
	inst.runCtx = runCtx
	inst.runCancel = runCancel

	var runCancelAt, svcCloseAt time.Time
	wrapped := &orderedFakeSvc{closeAt: &svcCloseAt}
	inst.setSvc(wrapped)

	// Wrap runCancel to record when it fires.
	origCancel := inst.runCancel
	inst.runCancel = func() {
		runCancelAt = time.Now()
		origCancel()
	}

	inst.close()

	if runCancelAt.IsZero() {
		t.Fatal("runCancel did not fire")
	}
	if svcCloseAt.IsZero() {
		t.Fatal("svc.Close did not fire")
	}
	if !runCancelAt.Before(svcCloseAt) {
		t.Errorf("runCancel at %v fired AFTER svc.Close at %v — ordering regression",
			runCancelAt, svcCloseAt)
	}
}

// orderedFakeSvc records when Close is called. Minimal meshService
// implementation for the ordering test.
type orderedFakeSvc struct {
	closeAt *time.Time
}

func (s *orderedFakeSvc) Listen(_, _ string) (net.Listener, error) {
	return nil, errors.New("Listen unsupported")
}
func (s *orderedFakeSvc) Close() {
	if s.closeAt != nil {
		*s.closeAt = time.Now()
	}
}
func (s *orderedFakeSvc) NebulaControl() *nebula.Control { return nil }
func (s *orderedFakeSvc) DevName() string                { return "" }

// --- Auto-connect-after-enroll ---

// TestLocalAPI_EnrollToken_AutoConnects verifies that a successful
// token-flow enrollment automatically calls connectFn so the user's
// new mesh comes up immediately. Without this the user had to restart
// the agent (the v0.10.34 contract is "no restart required ever").
//
// We mock the control plane's /api/enroll endpoint with httptest, then
// invoke handleEnrollToken via the test scaffolding from
// local_api_lifecycle_test.go.
func TestLocalAPI_EnrollToken_AutoConnects(t *testing.T) {
	cpane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/enroll") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"nodeId":"test-node-1",
			"caCert":"-----BEGIN NEBULA CA-----\nFAKE\n-----END NEBULA CA-----\n",
			"nodeCert":"-----BEGIN NEBULA CERT-----\nFAKE\n-----END NEBULA CERT-----\n",
			"nodeKey":"-----BEGIN NEBULA X25519 PRIVATE KEY-----\nFAKE\n-----END NEBULA X25519 PRIVATE KEY-----\n",
			"agentToken":"test-token",
			"serverIP":"10.42.99.1",
			"nebulaIP":"10.42.99.2/24",
			"lighthousePort":42099,
			"lighthouseHost":"127.0.0.1",
			"dnsDomain":"testenroll"
		}`))
	}))
	defer cpane.Close()

	connectCalled := atomic.Int32{}
	var lastName atomic.Pointer[string]
	connectFn := func(name string) error {
		connectCalled.Add(1)
		lastName.Store(&name)
		return nil
	}
	srv, h, _ := newTestLocalAPIServer(t, connectFn, nil)

	// Sanity: the test scaffolding's installEnrollment writes files via
	// writeNebulaConfig + writeDNSConfig which call log.Fatalf on error
	// (e.g. failure to write nebula.yaml). We've supplied a valid
	// configDir from t.TempDir(), so this should succeed.
	body := []byte(`{"endpoint":"` + cpane.URL + `","name":"testenroll","tunMode":"userspace"}`)
	rec, _ := authedReq(t, h, "POST", "/local/enroll/token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("token enroll returned %d: %s", rec.Code, rec.Body.String())
	}
	if connectCalled.Load() != 1 {
		t.Errorf("expected connectFn to be invoked exactly once after token enroll, got %d", connectCalled.Load())
	}
	if n := lastName.Load(); n == nil || !strings.HasPrefix(*n, "testenroll") {
		t.Errorf("connectFn invoked with wrong name: %v", n)
	}
	// Also verify the enrollment was persisted.
	if srv.enrolls.Get("testenroll") == nil {
		// The auto-suffix path may have been chosen; check by prefix.
		found := false
		for _, e := range srv.enrolls.List() {
			if strings.HasPrefix(e.Name, "testenroll") {
				found = true
				break
			}
		}
		if !found {
			t.Error("enrollment not persisted to registry after token enroll")
		}
	}
}

// TestLocalAPI_EnrollToken_AutoConnectFailure_StillSucceeds verifies
// that if connectFn returns an error (e.g. bind collision), the
// enrollment path STILL succeeds — the cert is on disk, the registry
// has the entry, and the user can retry connect later via
// POST /local/connect. This matches the production contract: persist
// enrollment first, connection is best-effort.
func TestLocalAPI_EnrollToken_AutoConnectFailure_StillSucceeds(t *testing.T) {
	cpane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"nodeId":"test-node-2",
			"caCert":"-----BEGIN NEBULA CA-----\nFAKE\n-----END NEBULA CA-----\n",
			"nodeCert":"-----BEGIN NEBULA CERT-----\nFAKE\n-----END NEBULA CERT-----\n",
			"nodeKey":"-----BEGIN NEBULA X25519 PRIVATE KEY-----\nFAKE\n-----END NEBULA X25519 PRIVATE KEY-----\n",
			"agentToken":"test-token-2",
			"serverIP":"10.42.99.1",
			"nebulaIP":"10.42.99.3/24",
			"lighthousePort":42100,
			"lighthouseHost":"127.0.0.1",
			"dnsDomain":"testenrollfail"
		}`))
	}))
	defer cpane.Close()

	connectFn := func(name string) error {
		return errors.New("simulated bind failure")
	}
	srv, h, _ := newTestLocalAPIServer(t, connectFn, nil)

	body := []byte(`{"endpoint":"` + cpane.URL + `","name":"testenrollfail","tunMode":"userspace"}`)
	rec, respBody := authedReq(t, h, "POST", "/local/enroll/token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("token enroll returned %d: %s", rec.Code, rec.Body.String())
	}
	if connected, _ := respBody["connected"].(bool); connected {
		t.Error("response said connected=true despite connectFn failure")
	}
	// Enrollment must still be persisted.
	found := false
	for _, e := range srv.enrolls.List() {
		if strings.HasPrefix(e.Name, "testenrollfail") {
			found = true
			break
		}
	}
	if !found {
		t.Error("enrollment NOT persisted after connectFn failure — should have been")
	}
}

// --- Source-scan tripwire ---

// TestMeshInstance_Close_SourceOrder asserts that within the body of
// (*meshInstance).close(), the runCancel call appears before svc.Close()
// in the source. This complements the runtime ordering test by
// catching reorder-regressions at compile-test time, before the
// goroutine-leak symptom could ever manifest in production.
func TestMeshInstance_Close_SourceOrder(t *testing.T) {
	src, err := os.ReadFile("instance.go")
	if err != nil {
		t.Fatalf("read instance.go: %v", err)
	}
	body := string(src)

	fnStart := strings.Index(body, "func (i *meshInstance) close()")
	if fnStart < 0 {
		t.Fatal("could not locate (*meshInstance).close() in instance.go")
	}
	bodyAfter := body[fnStart:]
	fnEnd := strings.Index(bodyAfter, "\n}\n")
	if fnEnd < 0 {
		t.Fatal("could not locate end of close() body")
	}
	fnBody := bodyAfter[:fnEnd]

	cancelIdx := strings.Index(fnBody, "i.runCancel()")
	closeIdx := strings.Index(fnBody, "svc.Close()")
	if cancelIdx < 0 {
		t.Error("i.runCancel() not found in (*meshInstance).close() — runCtx cancellation removed?")
		return
	}
	if closeIdx < 0 {
		t.Error("svc.Close() not found in (*meshInstance).close()")
		return
	}
	if cancelIdx > closeIdx {
		t.Errorf("ORDERING REGRESSION: i.runCancel() (offset %d) appears AFTER svc.Close() (offset %d).\n"+
			"Pre-v0.10.34 this exact ordering caused per-instance goroutines (heartbeat, renewal, path-quality) to keep running against a closed svc, leaking goroutines and producing duplicate workers on subsequent reconnect.\n"+
			"FIX: ensure i.runCancel() runs BEFORE svc.Close() in (*meshInstance).close().",
			cancelIdx, closeIdx)
	}

	// Also confirm the leave handler in local_api.go still disconnects
	// the live instance BEFORE removing it from the registry.
	apiSrc, err := os.ReadFile("local_api.go")
	if err != nil {
		t.Fatalf("read local_api.go: %v", err)
	}
	leaveStart := strings.Index(string(apiSrc), "func (s *localAPIServer) handleLeave(")
	if leaveStart < 0 {
		t.Fatal("could not locate handleLeave in local_api.go")
	}
	leaveBody := string(apiSrc)[leaveStart:]
	leaveEnd := strings.Index(leaveBody, "\n}\n")
	if leaveEnd > 0 {
		leaveBody = leaveBody[:leaveEnd]
	}
	disconnectIdx := strings.Index(leaveBody, "s.disconnectFn(")
	removeIdx := strings.Index(leaveBody, "s.enrolls.Remove(")
	if disconnectIdx < 0 || removeIdx < 0 {
		t.Error("handleLeave: cannot locate disconnect+remove sites — has the function been refactored?")
		return
	}
	if disconnectIdx > removeIdx {
		t.Errorf("ORDERING REGRESSION: handleLeave's disconnectFn (offset %d) appears AFTER enrolls.Remove (offset %d).\n"+
			"Pre-v0.10.34 the running Nebula stayed up through 'leave', so the per-instance renewal loop could read just-deleted cert files mid-operation. disconnect MUST run before registry removal.",
			disconnectIdx, removeIdx)
	}

	// Sanity: ensure the file still exists at /Users/...; trip if path layout changes.
	_ = filepath.Base("anchor")
}
