package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slackhq/nebula/cert"
)

// Enrollment self-healing (Phase HH, incident 2026-11-18).
//
// Nebula refuses to start on an expired cert, and connect tears down an
// instance whose Nebula failed to start, including the renewal goroutine that
// would have fixed the cert. Up to v0.11.26 such an enrollment never came
// back: not on reboot, not on Connect. (Why certs ran out in the first place
// is a separate bug: see renewalTime in renew.go.)
//
// Two layers:
//
//   - ensureUsableCert: before starting Nebula, if node.crt or node.key is
//     expired, missing, unparseable, or they don't belong together, renew
//     first. Renewal is bearer-token authenticated, so an expired cert is
//     never a dead end.
//   - recovery loop: if an enrollment that should be up fails to connect
//     (renewal 429/offline, port busy, ...), keep retrying in the
//     background with capped backoff on a wall-clock-safe timer until it
//     connects, the user disconnects/leaves it, or the client stops. After a
//     401 (node deleted or token revoked) it asks only once an hour: that
//     answer is usually final, but servers up to v0.11.26 also gave it when
//     their own database was busy.

// certStartProblem reports why the on-disk cert/key in dir can't be used to
// start Nebula. problem is "" when the cert is usable. blocking reports
// whether Nebula would refuse to start anyway: false only for a cert that
// expired less than cert.ClockSkewTolerance ago (vendor patch 25 lets
// Nebula load it), so a failed renewal in that window must not prevent the
// start attempt.
func certStartProblem(dir string, now time.Time) (problem string, blocking bool) {
	keyPEM, err := os.ReadFile(filepath.Join(dir, "node.key"))
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", true
		}
		return "unreadable", true
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, "node.crt"))
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", true
		}
		return "unreadable", true
	}
	c, _, err := cert.UnmarshalCertificateFromPEM(certPEM)
	if err != nil {
		return "unreadable", true
	}
	// A crash between the two writes of a renewal leaves a new cert next to
	// the old key. Nebula refuses the pair, and the cert alone looks fine
	// for another day.
	key, _, curve, err := cert.UnmarshalPrivateKeyFromPEM(keyPEM)
	if err != nil {
		return "unreadable", true
	}
	if c.VerifyPrivateKey(curve, key) != nil {
		return "mismatched", true
	}
	notAfter := c.NotAfter()
	if now.Before(notAfter) {
		return "", false
	}
	return "expired", now.After(notAfter.Add(cert.ClockSkewTolerance))
}

// certRenewalError is returned by connect when the cert is unusable and the
// pre-start renewal failed. Unwraps to the renewal error so callers can
// match errNodeRevoked / errRateLimited.
type certRenewalError struct {
	problem string
	err     error
}

func (e *certRenewalError) Error() string {
	return fmt.Sprintf("certificate %s and renewal failed: %v", e.problem, e.err)
}

func (e *certRenewalError) Unwrap() error { return e.err }

// preStartRenewFn is the renewal used before Nebula starts. A var so tests
// can stub the control-plane POST.
var preStartRenewFn = fetchAndInstallCert

// ensureUsableCert renews the enrollment's cert before Nebula starts when
// the on-disk cert can't be used. Returns a *certRenewalError when renewal
// failed and Nebula would refuse the cert anyway.
func (c *Client) ensureUsableCert(e *Enrollment) error {
	inst := c.newInstance(e)
	inst.parentCtx = c.runCtx // so Stop aborts the POST
	problem, blocking := certStartProblem(inst.dir(), time.Now())
	if problem == "" {
		return nil
	}
	if e.Endpoint == "" || e.NodeID == "" {
		// Nothing to renew against; let Nebula surface the real error.
		return nil
	}
	log.Printf("[client] %q: certificate %s — renewing before starting Nebula", e.Name, problem)
	err := preStartRenewFn(inst)
	if err == nil {
		log.Printf("[client] %q: certificate renewed before start", e.Name)
		return nil
	}
	if !blocking {
		log.Printf("[client] %q: pre-start renewal failed (%v); cert is within clock-skew tolerance, starting anyway", e.Name, err)
		return nil
	}
	return &certRenewalError{problem: problem, err: err}
}

// defaultRecoveryBackoff is the wait before each background reconnect
// attempt; the last entry repeats. The first retry is fast enough that a
// transient failure (port still held by a just-closed instance, DNS not up
// yet after wake) heals quickly, the cap keeps a permanently failing
// enrollment from hammering the control plane's rate limiter.
var defaultRecoveryBackoff = []time.Duration{
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
}

// revokedRetryDelay is the wait after a 401. Var so tests can shorten it.
var revokedRetryDelay = time.Hour

// recoveryWallClockTick bounds how long a recovery wait can overshoot after
// laptop sleep: Go timers run on the monotonic clock, which stops during
// macOS deep sleep (Phase S lesson), so waits poll the wall clock instead.
const recoveryWallClockTick = 15 * time.Second

// enrollmentRecovery is the state of one background reconnect loop.
type enrollmentRecovery struct {
	cancel context.CancelFunc

	mu       sync.Mutex
	lastErr  string
	attempts int
	nextAt   time.Time
}

func (r *enrollmentRecovery) setLastErr(err error) {
	r.mu.Lock()
	r.lastErr = err.Error()
	r.mu.Unlock()
}

// recoveryStatus is a point-in-time copy of a recovery loop's state for the
// local status API.
type recoveryStatus struct {
	LastErr  string
	Attempts int
	NextAt   time.Time
}

// connectLock serializes connect/disconnect per enrollment. Without it a
// background recovery attempt and a user's Connect/Disconnect click could
// interleave and double-start (or resurrect) the same instance.
func (c *Client) connectLock(name string) *sync.Mutex {
	v, _ := c.connectLocks.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// connectAndSupervise brings an enrollment up and, if that fails, keeps
// retrying in the background. Every "this enrollment should be up" caller
// goes through here: boot (Start), the Connect button, auto-connect after
// enrollment (userInitiated=true) and watchdog restarts (false).
// userInitiated clears an earlier user Disconnect; a watchdog restart must
// not override one.
func (c *Client) connectAndSupervise(name string, userInitiated bool) error {
	if userInitiated {
		c.setUserDisconnected(name, false)
	} else if c.isUserDisconnected(name) {
		return nil // the user turned it off; a watchdog must not turn it back on
	}
	err := c.connect(name)
	if err != nil {
		c.scheduleRecovery(name, err)
	} else {
		// Up now (e.g. the user clicked Connect while a loop was
		// waiting): the loop has nothing left to do, and leaving it in
		// place would keep /local/status saying "retrying".
		c.cancelRecovery(name)
		c.watchdogRestartDone(name)
	}
	return err
}

// restartInstance is every watchdog's restartFn: tear down the wedged
// instance and bring the enrollment back up.
//
// Pre-Phase-HH restartFn was plain connect(name), whose "already up"
// short-circuit returned nil whenever the instance still had a Nebula
// control — which it always does when a watchdog fires (a stuck data plane,
// a silent renewal loop and a wedged watcher all leave Nebula running). So
// since v0.10.36 every watchdog "auto-restart" was a silent no-op that still
// reported success: a dead renewal loop was never restarted and the certs
// ran out.
//
// Only the instance the watchdog belongs to is torn down: if it was already
// replaced (a user reconnect, an earlier restart), the current instance is
// left alone and this is just an idempotent connect.
//
// If the teardown doesn't release the UDP port or TUN device (a wedged
// Nebula), no reconnect can succeed in this process: exit, so the service
// supervisor starts a new one. When the teardown did finish and the port is
// busy all the same, the recovery loop counts first (Phase EE).
func (c *Client) restartInstance(name string, stale *meshInstance) error {
	if c.isUserDisconnected(name) {
		return nil
	}
	mu := c.connectLock(name)
	mu.Lock()
	wedged := false
	if stale != nil && c.instances.get(name) == stale {
		c.instances.remove(name)
		c.noteWatchdogRestart(name)
		if err := closeInstance(stale); err != nil {
			log.Printf("[client] %q: %v", name, err)
			wedged = true
		}
		c.servers.shutdownInstance(name)
	}
	mu.Unlock()
	err := c.connectAndSupervise(name, false)
	// The old instance never finished closing and still holds the port or the
	// device. Waiting does not free them (2026-11-26: three more rounds of the
	// recovery loop would have been five more minutes of outage).
	if wedged && err != nil && isResourceBusy(err) {
		log.Printf("[client] %q: did not shut down and still holds its port or device — exiting (%d) so the service supervisor starts a new agent", name, escalationExitCode)
		osExitFn(escalationExitCode)
	}
	return err
}

// instanceCloseTimeout bounds a teardown. A wedged Nebula can block Stop
// forever, and the caller holds connectLock, which Connect, Disconnect and
// every recovery attempt need. Var so tests can shorten it.
var instanceCloseTimeout = 20 * time.Second

// closeInstance closes inst, but stops waiting after instanceCloseTimeout.
func closeInstance(inst *meshInstance) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.close()
	}()
	select {
	case <-done:
		return nil
	case <-time.After(instanceCloseTimeout):
		return fmt.Errorf("network %q did not shut down within %s", inst.name(), instanceCloseTimeout)
	}
}

// watchdogRestartState tracks watchdog restarts of one enrollment across the
// instances they replace.
type watchdogRestartState struct {
	last   time.Time
	streak int
	// pending is set from the teardown until the enrollment is up again.
	pending bool
}

// watchdogRestartMaxCooldown caps the wait between watchdog restarts of one
// enrollment; twice that without a restart starts over.
const watchdogRestartMaxCooldown = 6 * time.Hour

// noteWatchdogRestart records that a watchdog is replacing name's instance.
func (c *Client) noteWatchdogRestart(name string) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.watchdogRestarts == nil {
		c.watchdogRestarts = map[string]watchdogRestartState{}
	}
	st := c.watchdogRestarts[name]
	if time.Since(st.last) > 2*watchdogRestartMaxCooldown {
		st.streak = 0
	}
	st.streak++
	st.last = time.Now()
	st.pending = true
	c.watchdogRestarts[name] = st
}

// watchdogRestartNotBefore is when a watchdog may restart name again. The
// wait doubles with every restart: when restarting doesn't clear the
// condition (a peer that firewalls the keepalive port looks exactly like a
// stuck data plane), each further restart only drops working connections.
func (c *Client) watchdogRestartNotBefore(name string) time.Time {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	st := c.watchdogRestarts[name]
	if st.streak == 0 {
		return time.Time{}
	}
	d := watchdogRestartCooldown
	for n := 1; n < st.streak && d < watchdogRestartMaxCooldown; n++ {
		d *= 2
	}
	if d > watchdogRestartMaxCooldown {
		d = watchdogRestartMaxCooldown
	}
	return st.last.Add(d)
}

// watchdogRestartPending reports whether a watchdog tore name down and it
// has not come back yet.
func (c *Client) watchdogRestartPending(name string) bool {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	return c.watchdogRestarts[name].pending
}

// watchdogRestartDone clears pending once name is up again. Reports whether
// it was set.
func (c *Client) watchdogRestartDone(name string) bool {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	st := c.watchdogRestarts[name]
	if !st.pending {
		return false
	}
	st.pending = false
	c.watchdogRestarts[name] = st
	return true
}

// isResourceBusy reports whether err means the UDP port or TUN device is
// still held.
func isResourceBusy(err error) bool {
	es := err.Error()
	return strings.Contains(es, "address already in use") || strings.Contains(es, "device or resource busy")
}

func (c *Client) isUserDisconnected(name string) bool {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	return c.userDisconnected[name]
}

func (c *Client) setUserDisconnected(name string, v bool) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if !v {
		delete(c.userDisconnected, name)
		return
	}
	if c.userDisconnected == nil {
		c.userDisconnected = map[string]bool{}
	}
	c.userDisconnected[name] = true
}

// scheduleRecovery starts the background reconnect loop for name unless one
// is already running, the user disconnected it, it no longer exists, or the
// client isn't running.
func (c *Client) scheduleRecovery(name string, cause error) {
	if cause == nil {
		return
	}
	c.mu.Lock()
	parent := c.runCtx
	c.mu.Unlock()
	if parent == nil || parent.Err() != nil || c.enrolls.Get(name) == nil {
		return
	}

	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.userDisconnected[name] {
		return
	}
	if r, ok := c.recoveries[name]; ok {
		r.setLastErr(cause)
		return
	}
	if c.recoveries == nil {
		c.recoveries = map[string]*enrollmentRecovery{}
	}
	ctx, cancel := context.WithCancel(parent)
	r := &enrollmentRecovery{cancel: cancel, lastErr: cause.Error()}
	c.recoveries[name] = r
	log.Printf("[recover %s] connect failed (%v) — retrying in the background", name, cause)
	go c.runRecovery(ctx, name, r, cause)
}

// cancelRecovery stops the background loop for name, if any.
func (c *Client) cancelRecovery(name string) {
	c.recoveryMu.Lock()
	r := c.recoveries[name]
	delete(c.recoveries, name)
	c.recoveryMu.Unlock()
	if r != nil {
		r.cancel()
	}
}

// recoveryInfo returns the state of name's recovery loop, ok=false when
// none is running.
func (c *Client) recoveryInfo(name string) (recoveryStatus, bool) {
	c.recoveryMu.Lock()
	r := c.recoveries[name]
	c.recoveryMu.Unlock()
	if r == nil {
		return recoveryStatus{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return recoveryStatus{LastErr: r.lastErr, Attempts: r.attempts, NextAt: r.nextAt}, true
}

// recoveryDelay is the wait before the given attempt, after the last one
// failed with cause.
func (c *Client) recoveryDelay(attempt int, cause error) time.Duration {
	sched := c.recoveryBackoff
	if len(sched) == 0 {
		sched = defaultRecoveryBackoff
	}
	d := sched[len(sched)-1]
	if attempt < len(sched) {
		d = sched[attempt]
	}
	if errors.Is(cause, errNodeRevoked) {
		d = revokedRetryDelay
	}
	// ±20% jitter so enrollments (and agents behind one NAT) don't retry
	// in lockstep.
	if span := int64(d) * 2 / 5; span > 0 {
		d += time.Duration(rand.Int63n(span)) - time.Duration(span/2)
	}
	return d
}

// runRecovery retries connect until the enrollment is up or the loop has no
// reason to continue. It calls c.connect (not connectAndSupervise) so a
// failed attempt doesn't try to schedule a second loop.
func (c *Client) runRecovery(ctx context.Context, name string, r *enrollmentRecovery, cause error) {
	defer func() {
		c.recoveryMu.Lock()
		if c.recoveries[name] == r {
			delete(c.recoveries, name)
		}
		c.recoveryMu.Unlock()
		r.cancel()
	}()

	var busy int // consecutive resource-busy failures after a watchdog teardown
	for attempt := 0; ; attempt++ {
		delay := c.recoveryDelay(attempt, cause)
		r.mu.Lock()
		r.nextAt = time.Now().Add(delay)
		r.mu.Unlock()
		if !waitWallClock(ctx, delay) {
			log.Printf("[recover %s] stopped (cancelled)", name)
			return
		}
		if c.enrolls.Get(name) == nil {
			log.Printf("[recover %s] stopped (enrollment removed)", name)
			return
		}
		if inst := c.instances.get(name); inst != nil && inst.control() != nil {
			log.Printf("[recover %s] stopped (already connected)", name)
			return
		}

		r.mu.Lock()
		r.attempts++
		n := r.attempts
		r.mu.Unlock()

		err := c.connect(name)
		cause = err
		if ctx.Err() != nil {
			// Disconnect/Leave/Stop raced with the attempt; their teardown
			// (serialized by connectLock) wins.
			return
		}
		if err == nil {
			c.clearBootError(name)
			if c.watchdogRestartDone(name) {
				publishToEventBus(localEvent{
					Time: time.Now(),
					Type: "instance.watchdog-recovered",
					Data: map[string]any{"name": name},
				})
			}
			log.Printf("[recover %s] connected after %d background attempt(s)", name, n)
			return
		}
		c.recordBootError(name, err)
		r.setLastErr(err)
		log.Printf("[recover %s] attempt %d failed: %v", name, n, err)

		// The watchdog that tore the instance down died with it, so the
		// Phase EE exit-and-respawn escalation has to happen here. Only
		// after a watchdog teardown: a port held by another program on a
		// normal connect must not take the whole agent down.
		if isResourceBusy(err) && c.watchdogRestartPending(name) {
			if recordRestartFailure("recover", name, &busy) {
				return
			}
		} else {
			busy = 0
		}
	}
}

// waitWallClock waits d of wall-clock time. Returns false if ctx ends first.
// Go timers are monotonic and stop during macOS deep sleep, so a 10-minute
// time.After can take hours on a sleeping laptop; polling the wall clock on
// a short ticker bounds the overshoot to one tick after wake.
func waitWallClock(ctx context.Context, d time.Duration) bool {
	// Round(0) strips the monotonic reading so comparisons use wall time.
	deadline := time.Now().Round(0).Add(d)
	tick := d
	if tick > recoveryWallClockTick {
		tick = recoveryWallClockTick
	}
	if tick <= 0 {
		tick = time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		if !time.Now().Round(0).Before(deadline) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}
