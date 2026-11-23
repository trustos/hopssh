package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A closed instance must never end up with Nebula running. Closing only
// cancels a context, and work already past its last ctx check (a renewal
// whose POST has returned, a Nebula start under way, a retry that just woke)
// used to start Nebula anyway: an orphan holding the UDP port that nothing
// could stop, so every later connect failed with "address already in use".

// blockingStart is a Nebula start that waits to be released.
type blockingStart struct {
	entered, release chan struct{}
	svc              *fakeMeshService
	calls            atomic.Int64
}

func newBlockingStart() *blockingStart {
	return &blockingStart{entered: make(chan struct{}, 8), release: make(chan struct{}), svc: newFakeMeshService("late")}
}

func (b *blockingStart) start(string, string) (meshService, error) {
	b.calls.Add(1)
	b.entered <- struct{}{}
	<-b.release
	return b.svc, nil
}

func (b *blockingStart) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Nebula start was never called")
	}
}

func substituteStartMesh(t *testing.T, fn func(string, string) (meshService, error)) {
	t.Helper()
	orig := startMeshFn
	startMeshFn = fn
	t.Cleanup(func() { startMeshFn = orig })
}

func waitDone(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// assertStopped checks that the late Nebula was stopped and not installed.
func assertStopped(t *testing.T, inst *meshInstance, svc *fakeMeshService) {
	t.Helper()
	if !svc.wasClosed() {
		t.Error("ORPHAN: the Nebula that finished starting after close() was left running")
	}
	if inst.currentSvc() != nil {
		t.Error("a closed instance must not hold a mesh service")
	}
}

func TestSetSvc_RefusedAfterClose(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	if !inst.setSvc(newFakeMeshService("a")) {
		t.Fatal("setSvc on an open instance must succeed")
	}
	inst.close()
	if inst.setSvc(newFakeMeshService("b")) || inst.currentSvc() != nil {
		t.Error("setSvc on a closed instance must be refused")
	}
	if !inst.isClosed() {
		t.Error("isClosed = false after close")
	}
}

func TestReloadNebula_ClosedBefore(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	start := newBlockingStart()
	close(start.release)
	defer withSubstitutes(start.start, func(int, time.Duration) {}, nil)()
	substituteStartMesh(t, start.start)

	inst.close()
	reloadNebula(inst)
	if n := start.calls.Load(); n != 0 {
		t.Errorf("Nebula started %d times on a closed instance", n)
	}
}

func TestReloadNebula_ClosedDuringColdStart(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	start := newBlockingStart()
	substituteStartMesh(t, start.start)
	restarted := false
	inst.onRestart = func(meshService) { restarted = true }

	done := make(chan struct{})
	go func() { defer close(done); reloadNebula(inst) }()
	start.waitEntered(t)
	inst.close()
	close(start.release)
	waitDone(t, "reload", done)

	assertStopped(t, inst, start.svc)
	if restarted {
		t.Error("onRestart ran for a closed instance")
	}
}

func TestReloadNebula_ClosedDuringHotRestart(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	old := newFakeMeshService("old")
	inst.setSvc(old)
	start := newBlockingStart()
	defer withSubstitutes(start.start, func(int, time.Duration) {}, nil)()
	restarted := false
	inst.onRestart = func(meshService) { restarted = true }

	done := make(chan struct{})
	go func() { defer close(done); reloadNebula(inst) }()
	start.waitEntered(t)
	inst.close()
	close(start.release)
	waitDone(t, "reload", done)

	assertStopped(t, inst, start.svc)
	if !old.wasClosed() {
		t.Error("the old Nebula was left running")
	}
	if restarted {
		t.Error("onRestart ran for a closed instance")
	}
}

func TestRetryReload_StopsOnClose(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	inst.runCtx, inst.runCancel = context.WithCancel(inst.parentCtx)
	var starts atomic.Int64
	defer withSubstitutes(
		func(string, string) (meshService, error) { starts.Add(1); return nil, errors.New("busy") },
		func(int, time.Duration) {},
		[]time.Duration{20 * time.Millisecond, 20 * time.Millisecond, time.Millisecond, time.Millisecond},
	)()

	scheduleRetryReload(inst, "nebula.yaml", "userspace")
	inst.close()
	time.Sleep(150 * time.Millisecond)
	if n := starts.Load(); n != 0 {
		t.Errorf("retry started Nebula %d times after the instance was closed", n)
	}
}

func TestRetryReload_ClosedDuringStart(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	start := newBlockingStart()
	defer withSubstitutes(start.start, func(int, time.Duration) {}, []time.Duration{time.Millisecond})()

	scheduleRetryReload(inst, "nebula.yaml", "userspace")
	start.waitEntered(t)
	inst.close()
	close(start.release)
	eventually(t, "the late Nebula to be stopped", start.svc.wasClosed)
	assertStopped(t, inst, start.svc)
}

func newStartableInstance(t *testing.T) *meshInstance {
	t.Helper()
	inst := withTestInstance(t, "userspace", 0)
	if err := os.WriteFile(filepath.Join(inst.dir(), "token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestStartInstance_ClosedWhileStarting(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	inst := newStartableInstance(t)
	start := newBlockingStart()
	substituteStartMesh(t, start.start)

	errc := make(chan error, 1)
	go func() { errc <- c.startInstance(c.runCtx, inst) }()
	start.waitEntered(t)
	inst.close()
	close(start.release)
	if err := <-errc; !errors.Is(err, errInstanceClosed) {
		t.Errorf("err = %v, want errInstanceClosed", err)
	}
	assertStopped(t, inst, start.svc)
}

// TestStartInstance_AfterStop: Stop cancels, then closes what is registered.
// An instance registered a moment later must not come up.
func TestStartInstance_AfterStop(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	ctx := c.runCtx
	_ = c.Stop()
	start := newBlockingStart()
	close(start.release)
	substituteStartMesh(t, start.start)

	if err := c.startInstance(ctx, newStartableInstance(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := start.calls.Load(); n != 0 {
		t.Errorf("Nebula started %d times after Stop", n)
	}
}

// TestStarts_DoNotOverlap: a renewal that finishes while the instance is
// still starting must wait, not start a second Nebula next to the first.
func TestStarts_DoNotOverlap(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	var running, overlaps, starts atomic.Int64
	slow := func(string, string) (meshService, error) {
		starts.Add(1)
		if running.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return newFakeMeshService("x"), nil
	}
	defer withSubstitutes(slow, func(int, time.Duration) {}, nil)()
	substituteStartMesh(t, slow)

	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() { reloadNebula(inst); done <- struct{}{} }()
	}
	for i := 0; i < 4; i++ {
		waitDone(t, "reload", done)
	}
	if overlaps.Load() != 0 || starts.Load() != 4 {
		t.Errorf("starts = %d, overlapping = %d; want 4 and 0", starts.Load(), overlaps.Load())
	}
	inst.close()
}

func TestLifetimeCtx(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	if inst.lifetimeCtx() == nil {
		t.Fatal("must never be nil")
	}
	inst.parentCtx = context.WithValue(context.Background(), struct{}{}, 1)
	if inst.lifetimeCtx() != inst.parentCtx {
		t.Error("without runCtx, use parentCtx")
	}
	inst.runCtx, inst.runCancel = context.WithCancel(inst.parentCtx)
	inst.close()
	if inst.lifetimeCtx().Err() == nil {
		t.Error("work started after close (watcher, retry, POST) must get an ended context")
	}
}

// TestRenewalPOST_AbortedByClose: disconnecting during a slow renewal must
// not wait for it, and must leave the old cert in place.
func TestRenewalPOST_AbortedByClose(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	inst := newHTTPTestInstance(t, srv.URL)
	inst.runCtx, inst.runCancel = context.WithCancel(context.Background())
	crt := filepath.Join(inst.dir(), "node.crt")
	os.WriteFile(crt, []byte("OLD"), 0o644)

	errc := make(chan error, 1)
	go func() { errc <- fetchAndInstallCert(inst) }()
	<-entered
	inst.close()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the renewal POST outlived its instance")
	}
	if b, _ := os.ReadFile(crt); string(b) != "OLD" {
		t.Errorf("cert = %q, want it untouched", b)
	}
}

// TestRebindMesh_LosesRaceWithShutdown: a reload that finishes just as the
// network is shut down used to call Shutdown on a nil server and panic.
func TestRebindMesh_LosesRaceWithShutdown(t *testing.T) {
	s := newServerSet()
	s.servers["home"] = &instanceServer{}
	inst := newMeshInstance(&Enrollment{Name: "home"})
	if err := s.rebindMesh(inst, http.NotFoundHandler(), &listeningFakeSvc{}); err == nil {
		t.Error("want an error, not a server on a closing network")
	}
}

type listeningFakeSvc struct{ fakeMeshService }

func (*listeningFakeSvc) Listen(string, string) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// TestEnsureUsableCert_MismatchedPairIsRenewed: a crash between writing
// node.crt and node.key leaves a pair Nebula refuses, with a cert that looks
// valid for another day.
func TestEnsureUsableCert_MismatchedPairIsRenewed(t *testing.T) {
	c, dir := newTestClientForBootResilience(t)
	e := addEnrollment(t, c, "home")
	now := time.Now()
	home := filepath.Join(dir, "home")
	writeNodeCert(t, home, now, now.Add(24*time.Hour))
	crt, _ := os.ReadFile(filepath.Join(home, "node.crt"))
	writeNodeCert(t, home, now, now.Add(24*time.Hour)) // second pair
	os.WriteFile(filepath.Join(home, "node.crt"), crt, 0o644)

	if problem, blocking := certStartProblem(home, now); problem != "mismatched" || !blocking {
		t.Fatalf("certStartProblem = %q, %v", problem, blocking)
	}
	renewed := 0
	stubPreStartRenew(t, func(*meshInstance) error { renewed++; return nil })
	if err := c.ensureUsableCert(e); err != nil || renewed != 1 {
		t.Errorf("err = %v, renewals = %d; want nil, 1", err, renewed)
	}
}

// TestNebulaYAML_Empty: a write cut short by a crash leaves an empty file.
// Every writer used to panic on it, so the agent crashed on every boot and
// took its healthy networks down too.
func TestNebulaYAML_Empty(t *testing.T) {
	for _, content := range []string{"", "\n", "# only a comment\n", "null\n", "---\n"} {
		if cfg, err := parseNebulaYAML([]byte(content)); err == nil || cfg != nil {
			t.Errorf("parseNebulaYAML(%q) = %v, %v; want an error", content, cfg, err)
		}
		inst := withTestInstance(t, "userspace", 4242)
		if err := os.WriteFile(filepath.Join(inst.dir(), "nebula.yaml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		ensureP2PConfig(inst)
		if err := rewriteNebulaUnsafeRoutes(inst, []string{"10.0.0.0/8"}); err == nil {
			t.Error("rewriteNebulaUnsafeRoutes on an empty file: want an error")
		}
		relays := true
		if _, _, err := mergeNebulaConfig([]byte(content), &nebulaConfigUpdate{UseRelays: &relays}); err == nil {
			t.Error("mergeNebulaConfig on an empty file: want an error")
		}
	}
	if _, err := parseNebulaYAML([]byte("pki: {ca: x}")); err != nil {
		t.Errorf("a normal file: %v", err)
	}
}
