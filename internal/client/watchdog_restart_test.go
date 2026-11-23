package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Watchdog restarts became real with restartInstance (recovery.go). These
// tests cover what that brings with it: a restart drops working connections,
// so a watchdog that keeps firing must back off, and one that can't tear a
// wedged Nebula down must still end in a process restart.

// captureEvents records event-bus events for one test.
func captureEvents(t *testing.T) (count func(typ string) int) {
	t.Helper()
	prev := globalEventBus.Load()
	hub := newLocalEventHub()
	globalEventBus.Store(hub)
	ch := hub.subscribe()
	var mu sync.Mutex
	seen := map[string]int{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			mu.Lock()
			seen[ev.Type]++
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		globalEventBus.Store(prev)
		hub.unsubscribe(ch)
		<-done
	})
	return func(typ string) int {
		mu.Lock()
		defer mu.Unlock()
		return seen[typ]
	}
}

// --- backoff ---------------------------------------------------------------

func TestWatchdogRestartBackoff(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	if !c.watchdogRestartNotBefore("home").IsZero() {
		t.Error("the first restart must not have to wait")
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute,
		80 * time.Minute, 160 * time.Minute, 320 * time.Minute, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		c.noteWatchdogRestart("home")
		if got := time.Until(c.watchdogRestartNotBefore("home")); got > w || got < w-time.Second {
			t.Errorf("after restart %d the next may come in %s, want %s", i+1, got, w)
		}
	}
	if !c.watchdogRestartNotBefore("work").IsZero() {
		t.Error("one network's restarts must not hold up another's")
	}

	// Half a day without a restart starts over.
	st := c.watchdogRestarts["home"]
	st.last = time.Now().Add(-2*watchdogRestartMaxCooldown - time.Minute)
	c.watchdogRestarts["home"] = st
	c.noteWatchdogRestart("home")
	if got := time.Until(c.watchdogRestartNotBefore("home")); got > 5*time.Minute {
		t.Errorf("after a quiet half day the next restart waits %s, want 5m again", got)
	}
}

// TestWatchdogRestartBackoff_SurvivesTheRestart: the cooldown used to live on
// the instance the restart throws away, so every new instance started at
// zero.
func TestWatchdogRestartBackoff_SurvivesTheRestart(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	for i := 0; i < 3; i++ {
		stale, _ := newStaleInstance(t, c, "home")
		c.connectOverride = func(string) error { return nil }
		if err := c.restartInstance("home", stale); err != nil {
			t.Fatal(err)
		}
	}
	if got := time.Until(c.watchdogRestartNotBefore("home")); got < 19*time.Minute {
		t.Errorf("after three restarts the fourth may come in %s, want 20m", got)
	}

	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(funcBody(t, string(src), "func (c *Client) connectLocked("), "inst.restartNotBefore = c.watchdogRestartNotBefore(name)") {
		t.Error("connectLocked must hand the cooldown to the new instance")
	}
}

func TestWatchdogTrip_RespectsCooldown(t *testing.T) {
	events := captureEvents(t)
	inst := withTestInstance(t, "userspace", 0)
	restarts := 0
	inst.restartFn = func() error { restarts++; return nil }

	inst.restartNotBefore = time.Now().Add(time.Hour)
	watchdogTrip(inst, 1, watchdogStuckThreshold)
	if restarts != 0 {
		t.Error("restarted during the cooldown")
	}
	eventually(t, "the cooldown event", func() bool { return events("instance.watchdog-cooldown") == 1 })

	inst.restartNotBefore = time.Now().Add(-time.Second)
	watchdogTrip(inst, 2, watchdogStuckThreshold)
	if restarts != 1 {
		t.Error("did not restart after the cooldown")
	}
}

// TestWatchdogs_AllCheckCooldown: the other two watchdogs run on multi-minute
// tickers, so check their source.
func TestWatchdogs_AllCheckCooldown(t *testing.T) {
	for file, sig := range map[string]string{
		"keepalive.go":        "func watchdogTrip(",
		"renew.go":            "func runRenewalWatchdog(",
		"watcher_watchdog.go": "func runWatcherWatchdog(",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body := funcBody(t, string(src), sig)
		check, restart := strings.Index(body, "inst.restartCoolingDown()"), strings.Index(body, "inst.restartFn()")
		if check < 0 || restart < 0 || check > restart {
			t.Errorf("%s: %s must check restartCoolingDown before restartFn", file, sig)
		}
	}
}

// --- teardown of a wedged Nebula -------------------------------------------

// TestRestartInstance_WedgedClose: Nebula's Stop can block forever. The
// restart must give up on it and must not keep connectLock.
func TestRestartInstance_WedgedClose(t *testing.T) {
	orig := instanceCloseTimeout
	instanceCloseTimeout = 50 * time.Millisecond
	defer func() { instanceCloseTimeout = orig }()

	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	release := make(chan struct{})
	wedged := newFakeMeshService("wedged")
	wedged.releaseHook = func() { <-release }
	stale.setSvc(wedged)
	defer close(release)
	var connects atomic.Int64
	c.connectOverride = func(string) error { connects.Add(1); return nil }

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := c.restartInstance("home", stale); err != nil {
			t.Error(err)
		}
		if err := c.disconnect("home"); err != nil { // needs connectLock
			t.Error(err)
		}
	}()
	waitDone(t, "restart of a wedged network", done)
	if connects.Load() != 1 {
		t.Error("the restart must still try to connect")
	}
}

func TestCloseInstance_ReportsTimeout(t *testing.T) {
	orig := instanceCloseTimeout
	instanceCloseTimeout = 20 * time.Millisecond
	defer func() { instanceCloseTimeout = orig }()

	inst := newMeshInstance(&Enrollment{Name: "home"})
	release := make(chan struct{})
	defer close(release)
	svc := newFakeMeshService("wedged")
	svc.releaseHook = func() { <-release }
	inst.setSvc(svc)
	if err := closeInstance(inst); err == nil || !strings.Contains(err.Error(), "home") {
		t.Errorf("err = %v, want a timeout naming the network", err)
	}
	if err := closeInstance(newMeshInstance(&Enrollment{Name: "work"})); err != nil {
		t.Errorf("a normal close: %v", err)
	}
}

// --- escalation to process exit --------------------------------------------

func stubExit(t *testing.T) *atomic.Int64 {
	t.Helper()
	var exits atomic.Int64
	orig := osExitFn
	osExitFn = func(int) { exits.Add(1) }
	t.Cleanup(func() { osExitFn = orig })
	return &exits
}

var errPortBusy = errors.New("connect failed: listen udp :4242: bind: address already in use")

// TestRecovery_EscalatesAfterWatchdogTeardown: the 2026-05-11 wedge held the
// UDP port for 24h. Only a new process frees it, and the watchdog that used
// to count the failures is gone with the instance, so the recovery loop must.
func TestRecovery_EscalatesAfterWatchdogTeardown(t *testing.T) {
	exits := stubExit(t)
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	var attempts atomic.Int64
	c.connectOverride = func(string) error { attempts.Add(1); return errPortBusy }

	if err := c.restartInstance("home", stale); err == nil {
		t.Fatal("want the connect error")
	}
	eventually(t, "process exit", func() bool { return exits.Load() == 1 })
	if n := attempts.Load(); n != int64(escalateAfterN)+1 {
		t.Errorf("exited after %d connect attempts, want 1 + %d", n, escalateAfterN)
	}
}

// TestRecovery_NoEscalationOnPlainConnect: a port held by another program
// must not take the agent (and its other networks) down.
func TestRecovery_NoEscalationOnPlainConnect(t *testing.T) {
	exits := stubExit(t)
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	var attempts atomic.Int64
	c.connectOverride = func(string) error { attempts.Add(1); return errPortBusy }

	_ = c.connectAndSupervise("home", true)
	eventually(t, "many attempts", func() bool { return attempts.Load() > int64(escalateAfterN)*5 })
	if exits.Load() != 0 {
		t.Error("a busy port on a normal connect must never exit the process")
	}
}

// TestRecovery_OtherFailuresResetEscalation: being offline is not a wedge.
func TestRecovery_OtherFailuresResetEscalation(t *testing.T) {
	exits := stubExit(t)
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	var attempts atomic.Int64
	c.connectOverride = func(string) error {
		if attempts.Add(1)%3 == 0 {
			return errors.New("dial tcp: no such host")
		}
		return errPortBusy
	}

	_ = c.restartInstance("home", stale)
	eventually(t, "many attempts", func() bool { return attempts.Load() > 30 })
	if exits.Load() != 0 {
		t.Error("only consecutive busy failures may exit the process")
	}
}

// TestRecovery_AfterWatchdogPublishesRecovered: the UI shows "recovering"
// until this event arrives.
func TestRecovery_AfterWatchdogPublishesRecovered(t *testing.T) {
	events := captureEvents(t)
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	var attempts atomic.Int64
	c.connectOverride = func(string) error {
		if attempts.Add(1) < 3 {
			return errRateLimited
		}
		return nil
	}
	_ = c.restartInstance("home", stale)
	eventually(t, "recovered event", func() bool { return events("instance.watchdog-recovered") == 1 })
	if c.watchdogRestartPending("home") {
		t.Error("still pending after recovery")
	}
}

// TestRecovery_PlainConnectPublishesNoRecovered: nothing was torn down.
func TestRecovery_PlainConnectPublishesNoRecovered(t *testing.T) {
	events := captureEvents(t)
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	var attempts atomic.Int64
	c.connectOverride = func(string) error {
		if attempts.Add(1) < 3 {
			return errRateLimited
		}
		return nil
	}
	_ = c.connectAndSupervise("home", true)
	eventually(t, "loop to end", func() bool { _, ok := c.recoveryInfo("home"); return !ok })
	if events("instance.watchdog-recovered") != 0 {
		t.Error("reported a watchdog recovery after a plain connect")
	}
}

// TestRestart_SuccessClearsPending: otherwise a busy port weeks later would
// count as a wedge and exit the process.
func TestRestart_SuccessClearsPending(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	c.connectOverride = func(string) error { return nil }
	if err := c.restartInstance("home", stale); err != nil {
		t.Fatal(err)
	}
	if c.watchdogRestartPending("home") {
		t.Error("still pending after a successful restart")
	}
}

func TestIsResourceBusy(t *testing.T) {
	for msg, want := range map[string]bool{
		"listen udp :4242: bind: address already in use": true,
		"ioctl TUNSETIFF: device or resource busy":       true,
		"certificate expired and renewal failed":         false,
		"no such host":                                   false,
	} {
		if got := isResourceBusy(fmt.Errorf("wrapped: %w", errors.New(msg))); got != want {
			t.Errorf("isResourceBusy(%q) = %v", msg, got)
		}
	}
}

// --- keepalive probe --------------------------------------------------------

// dialingFakeSvc is a userspace-style service: peers are only reachable
// through it, not through the OS.
type dialingFakeSvc struct {
	fakeMeshService
	dial func(ctx context.Context) (net.Conn, error)
}

func (d *dialingFakeSvc) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return d.dial(ctx)
}

// TestKeepaliveProbe_UsesTheMesh: in userspace mode the OS has no route to a
// peer. Dialling through the OS made every probe fail on "in-app" networks,
// so the data-plane watchdog saw a healthy network as stuck.
func TestKeepaliveProbe_UsesTheMesh(t *testing.T) {
	orig := keepaliveDialFn
	defer func() { keepaliveDialFn = orig }()
	osDials := 0
	keepaliveDialFn = func(string) error { osDials++; return errors.New("no route to host") }

	probe := func(svc meshService) error {
		inst := newMeshInstance(&Enrollment{Name: "home"})
		inst.setSvc(svc)
		return keepaliveProbe(inst, "10.42.1.7:41820")
	}
	answers := func(err error) meshService {
		return &dialingFakeSvc{dial: func(context.Context) (net.Conn, error) {
			if err != nil {
				return nil, err
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		}}
	}

	if err := probe(answers(nil)); err != nil {
		t.Errorf("peer answered: %v", err)
	}
	if err := probe(answers(errors.New("connect tcp 10.42.1.7:41820: connection was refused"))); err != nil {
		t.Errorf("a peer that refuses the port is alive: %v", err)
	}
	if err := probe(answers(errors.New("i/o timeout"))); err == nil {
		t.Error("a peer that does not answer must count as failed")
	}
	if osDials != 0 {
		t.Errorf("%d probes went through the OS in userspace mode", osDials)
	}

	if err := probe(newFakeMeshService("kernel")); err == nil || osDials != 1 {
		t.Errorf("kernel TUN must dial through the OS (err %v, dials %d)", err, osDials)
	}
	keepaliveDialFn = func(string) error {
		return errors.New("dial tcp 10.42.1.7:41820: connectex: No connection could be made because the target machine actively refused it.")
	}
	if err := probe(newFakeMeshService("kernel")); err != nil {
		t.Errorf("Windows wording of a refused connection: %v", err)
	}
}

// TestKeepaliveProbe_EndsWithTheInstance: a probe must not outlive close.
func TestKeepaliveProbe_EndsWithTheInstance(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.runCtx, inst.runCancel = context.WithCancel(context.Background())
	entered := make(chan struct{})
	inst.setSvc(&dialingFakeSvc{dial: func(ctx context.Context) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); _ = keepaliveProbe(inst, "10.42.1.7:41820") }()
	<-entered
	inst.close()
	waitDone(t, "probe", done)
	if time.Since(start) > keepaliveDialTimeout {
		t.Error("the probe waited for its full timeout after close")
	}
}

func TestKeepaliveCycle_UsesProbe(t *testing.T) {
	src, err := os.ReadFile("keepalive.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "func keepaliveOneCycle(")
	if !strings.Contains(body, "keepaliveProbe(inst, target)") || strings.Contains(body, "keepaliveDialFn(") {
		t.Error("keepaliveOneCycle must probe with keepaliveProbe, which knows the TUN mode")
	}
}
