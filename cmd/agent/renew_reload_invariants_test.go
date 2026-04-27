package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula"
)

// Tripwire tests for v0.10.33 reloadNebula invariants.
//
// These lock in the load-bearing properties of the cert-reload sequencing
// fix. Any future refactor that breaks the invariants will fail these
// tests deterministically — without these tests in place, the regression
// could ship silently because the symptom (peer rejecting expired certs)
// only surfaces 24+ hours after the buggy code lands, by which time the
// commit is hard to bisect.
//
// Invariants under test:
//
//   I1. Hot-restart calls oldSvc.Close() BEFORE startNebulaByModeFn.
//       Pre-v0.10.33 the order was reversed (try-then-swap), which
//       always failed because Nebula's UDP listen socket can't share
//       port. The fix reverses the order; this test asserts it.
//
//   I2. On startNebulaByModeFn failure, reloadNebula sets inst.svc = nil
//       BEFORE returning, so the scheduled retry's "haveSvc" check
//       correctly identifies the post-failure state.
//
//   I3. scheduleRetryReload SKIPS its retry attempt when inst.svc is
//       non-nil (e.g. another path restored the agent). This is the
//       "manual launchctl bootstrap already fixed it" branch.
//
//   I4. scheduleRetryReload PROCEEDS with the retry attempt when
//       inst.svc is nil. This is the post-v0.10.33 state where
//       reloadNebula nilled svc before scheduling. Pre-v0.10.33 the
//       leftover OLD svc tripped the "skip" branch falsely.
//
// All four are tested at the unit level using fakes for meshService and
// startNebulaByModeFn — no real Nebula process is started.

// fakeMeshService is a controllable meshService for unit testing the
// reload sequencing. It records when Close() is called and supports a
// release-at-Close hook for the port-release-wait integration check.
type fakeMeshService struct {
	mu           sync.Mutex
	closed       bool
	closeAt      time.Time
	releaseHook  func()
	closeCallID  int64
	identityName string
}

var fakeServiceCloseSeq int64 // monotonic ID across all fake Close() calls

func newFakeMeshService(name string) *fakeMeshService {
	return &fakeMeshService{identityName: name}
}

func (f *fakeMeshService) Listen(_, _ string) (net.Listener, error) {
	return nil, errors.New("fakeMeshService.Listen unsupported in unit test")
}
func (f *fakeMeshService) NebulaControl() *nebula.Control { return nil }
func (f *fakeMeshService) DevName() string                { return "" }
func (f *fakeMeshService) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	f.closeAt = time.Now()
	f.closeCallID = atomic.AddInt64(&fakeServiceCloseSeq, 1)
	if f.releaseHook != nil {
		f.releaseHook()
	}
}
func (f *fakeMeshService) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// withTestInstance constructs a meshInstance backed by a temp dir that has
// just enough state on disk to satisfy reloadNebula's path reads
// (nebula.yaml + tun-mode). The instance carries a cancellable
// parentCtx so callers can drain any retry goroutine cleanly via
// inst.parentCtx.(context.Context).Done(). The cancel function is
// installed into a sentinel field so withSubstitutes can sequence
// "cancel ctx → wait for goroutines → restore vars" correctly.
func withTestInstance(t *testing.T, tunMode string, listenPort int) *meshInstance {
	t.Helper()
	dir := t.TempDir()
	if err := writeFileForTest(filepath.Join(dir, "tun-mode"), tunMode); err != nil {
		t.Fatalf("write tun-mode: %v", err)
	}
	if err := writeFileForTest(filepath.Join(dir, "nebula.yaml"), "# stub"); err != nil {
		t.Fatalf("write nebula.yaml: %v", err)
	}
	inst := newMeshInstance(&Enrollment{
		Name:       "test",
		ListenPort: listenPort,
	})
	inst.customDir = dir
	ctx, cancel := context.WithCancel(context.Background())
	inst.parentCtx = ctx
	testCancelByInst.Store(inst, cancel)
	return inst
}

// testCancelByInst maps each test instance to its parentCtx cancel, so
// withSubstitutes' cleanup can find the right cancel without weaving an
// extra parameter through every test signature.
var testCancelByInst sync.Map

func writeFileForTest(path, content string) error {
	return writeFileForTestImpl(path, []byte(content))
}

func writeFileForTestImpl(path string, data []byte) error {
	return atomicWrite(path, data, 0644)
}

// withSubstitutes saves and replaces the test-injectable package vars
// (start fn, wait fn, retry schedule). The returned cleanup function
// cancels any test parentCtx so retry goroutines exit, briefly waits
// for them to drain, then restores the originals.
//
// retryReloadBackoff uses atomic.Pointer for proper happens-before
// edges; the start/wait fns are plain vars but tests run sequentially
// so the inter-goroutine reads have completed by the time cleanup
// touches them (the cancel + sleep sequence + atomic schedule swap
// gives Go's memory model the synchronization it needs).
func withSubstitutes(start func(string, string) (meshService, error), wait func(int, time.Duration), schedule []time.Duration) func() {
	prevStart := startNebulaByModeFn
	prevWait := waitForUDPPortFreeFn
	prevSchedule := retryReloadBackoffSchedule()
	if start != nil {
		startNebulaByModeFn = start
	}
	if wait != nil {
		waitForUDPPortFreeFn = wait
	}
	if schedule != nil {
		s := schedule
		retryReloadBackoff.Store(&s)
	}
	return func() {
		// Cancel every test-managed context so retry goroutines exit
		// via their <-parentCtx.Done() branch.
		testCancelByInst.Range(func(_, v any) bool {
			if cancel, ok := v.(context.CancelFunc); ok {
				cancel()
			}
			return true
		})
		// Wait for the goroutine's select to observe Done(). Atomic
		// schedule swap below establishes the synchronization edge for
		// any race-detector concerns about retryReloadBackoff reads.
		time.Sleep(80 * time.Millisecond)

		startNebulaByModeFn = prevStart
		waitForUDPPortFreeFn = prevWait
		s := prevSchedule
		retryReloadBackoff.Store(&s)

		testCancelByInst.Range(func(k, _ any) bool {
			testCancelByInst.Delete(k)
			return true
		})
	}
}

// I1: Close-before-Start ordering. Records the time the old svc was
// Close()d and the time the new svc start was attempted; asserts the
// close happened first.
func TestReloadNebula_I1_ClosesOldBeforeStartingNew(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	old := newFakeMeshService("old")
	inst.setSvc(old)

	var startAt time.Time
	var startCalled bool
	new := newFakeMeshService("new")
	cleanup := withSubstitutes(
		func(_, _ string) (meshService, error) {
			startAt = time.Now()
			startCalled = true
			return new, nil
		},
		func(_ int, _ time.Duration) {},
		nil,
	)
	defer cleanup()

	reloadNebula(inst)

	if !old.wasClosed() {
		t.Fatal("old svc was not closed")
	}
	if !startCalled {
		t.Fatal("startNebulaByModeFn was not called")
	}
	old.mu.Lock()
	closeAt := old.closeAt
	old.mu.Unlock()
	if !closeAt.Before(startAt) {
		t.Errorf("old.Close() at %v happened AFTER start at %v — bug regressed!", closeAt, startAt)
	}
	if got := inst.currentSvc(); got != new {
		t.Errorf("inst.svc = %T, want the new svc", got)
	}
}

// I2: On start failure, reloadNebula sets inst.svc = nil and triggers
// scheduleRetryReload. The retry's "haveSvc" check must see nil.
func TestReloadNebula_I2_NilsSvcOnStartFailure(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	old := newFakeMeshService("old")
	inst.setSvc(old)

	// Substitute: start always fails, retry schedule is empty so the
	// goroutine exhausts immediately without firing real retries.
	cleanup := withSubstitutes(
		func(_, _ string) (meshService, error) {
			return nil, errors.New("fake start failure")
		},
		func(_ int, _ time.Duration) {},
		[]time.Duration{}, // empty: retry goroutine exits immediately
	)
	defer cleanup()

	reloadNebula(inst)

	// Assert: post-reload, inst.svc must be nil so the retry-loop's
	// haveSvc check sees a clean slate.
	if got := inst.currentSvc(); got != nil {
		t.Errorf("inst.svc = %T after start failure, want nil (would trip false-positive haveSvc)", got)
	}
	if !old.wasClosed() {
		t.Error("old svc was not closed before start attempt")
	}
}

// I3: scheduleRetryReload SKIPS the retry when inst.svc is non-nil —
// e.g. a manual restart already restored the agent.
func TestScheduleRetryReload_I3_SkipsWhenSvcRestored(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)

	var startCalled int32
	cleanup := withSubstitutes(
		func(_, _ string) (meshService, error) {
			atomic.AddInt32(&startCalled, 1)
			return newFakeMeshService("retry-new"), nil
		},
		func(_ int, _ time.Duration) {},
		[]time.Duration{20 * time.Millisecond}, // very short retry delay
	)
	defer cleanup()

	// Simulate the "manual restart already restored" state by setting
	// inst.svc to a fresh fake BEFORE scheduling the retry. With the
	// invariant intact, the retry must observe haveSvc=true and skip.
	restored := newFakeMeshService("restored-out-of-band")
	inst.setSvc(restored)

	scheduleRetryReload(inst, "/dev/null", "userspace")

	// Wait long enough for one retry tick (>20ms) and then verify
	// the retry did NOT call startNebulaByModeFn.
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&startCalled); got != 0 {
		t.Errorf("retry called startNebulaByModeFn %d times when svc was already restored — should have skipped", got)
	}
	if got := inst.currentSvc(); got != restored {
		t.Errorf("inst.svc was modified to %T, want the out-of-band restored svc", got)
	}
}

// I4: scheduleRetryReload PROCEEDS with the retry when inst.svc is nil
// (the post-v0.10.33-fix state). Pre-fix this branch never executed
// because the OLD svc lingered and falsely tripped the skip.
func TestScheduleRetryReload_I4_ProceedsWhenSvcIsNil(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)

	var startCalled int32
	new := newFakeMeshService("retry-new")
	cleanup := withSubstitutes(
		func(_, _ string) (meshService, error) {
			atomic.AddInt32(&startCalled, 1)
			return new, nil
		},
		func(_ int, _ time.Duration) {},
		[]time.Duration{20 * time.Millisecond},
	)
	defer cleanup()

	// Precondition: inst.svc is nil — what reloadNebula now leaves
	// behind on a failed start.
	if got := inst.currentSvc(); got != nil {
		t.Fatalf("test setup: expected nil svc, got %T", got)
	}

	scheduleRetryReload(inst, "/dev/null", "userspace")

	// Allow one retry tick + slack.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&startCalled) > 0 && inst.currentSvc() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&startCalled); got == 0 {
		t.Error("retry never called startNebulaByModeFn — invariant I4 broken (svc-nil should proceed)")
	}
	if got := inst.currentSvc(); got != new {
		t.Errorf("inst.svc = %T after successful retry, want the new svc", got)
	}
}

// I5 (cross-cutting): full integration — reloadNebula on a failed start
// schedules the retry AND the retry then succeeds when start works on
// the second attempt. This is the realistic recovery path.
func TestReloadNebula_I5_FailsThenRetryRecovers(t *testing.T) {
	inst := withTestInstance(t, "userspace", 0)
	old := newFakeMeshService("old")
	inst.setSvc(old)

	var attempts int32
	new := newFakeMeshService("new")
	cleanup := withSubstitutes(
		func(_, _ string) (meshService, error) {
			n := atomic.AddInt32(&attempts, 1)
			if n == 1 {
				return nil, errors.New("address already in use")
			}
			return new, nil
		},
		func(_ int, _ time.Duration) {},
		[]time.Duration{20 * time.Millisecond},
	)
	defer cleanup()

	reloadNebula(inst)

	// First attempt failed → svc nil → retry should fire and succeed.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if inst.currentSvc() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("startNebulaByModeFn called %d times, want 2 (one fail, one success)", got)
	}
	if got := inst.currentSvc(); got != new {
		t.Errorf("inst.svc = %T after retry recovery, want the new svc", got)
	}
	if !old.wasClosed() {
		t.Error("old svc was not closed during reload sequence")
	}
}
