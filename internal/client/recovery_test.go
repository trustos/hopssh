package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"
	"golang.org/x/crypto/curve25519"
)

// Phase HH tripwires — enrollment self-healing after the 2026-11-18 incident
// (every enrollment on a MBP showed "certificate expired" + "no live
// instance" and never recovered). See docs/wiki/incidents/ for the write-up.

// --- fixtures -------------------------------------------------------------

// writeNodeCert writes a CA-signed node.crt + node.key into dir with the
// given validity window. notBefore/notAfter may be in the past so tests can
// produce genuinely expired certs relative to the real clock.
func writeNodeCert(t *testing.T, dir string, notBefore, notAfter time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTBS := &cert.TBSCertificate{
		Version:   cert.Version1,
		Name:      "test-ca",
		NotBefore: notBefore.Add(-24 * time.Hour),
		NotAfter:  notAfter.Add(365 * 24 * time.Hour),
		PublicKey: caPub,
		IsCA:      true,
		Curve:     cert.Curve_CURVE25519,
	}
	ca, err := caTBS.Sign(nil, cert.Curve_CURVE25519, caPriv)
	if err != nil {
		t.Fatalf("sign CA: %v", err)
	}
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	nodeTBS := &cert.TBSCertificate{
		Version:   cert.Version1,
		Name:      "test-node",
		Networks:  []netip.Prefix{netip.MustParsePrefix("10.42.1.6/24")},
		NotBefore: notBefore,
		NotAfter:  notAfter,
		PublicKey: pub,
		Curve:     cert.Curve_CURVE25519,
	}
	node, err := nodeTBS.Sign(ca, cert.Curve_CURVE25519, caPriv)
	if err != nil {
		t.Fatalf("sign node: %v", err)
	}
	certPEM, err := node.MarshalPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.crt"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	keyPEM := cert.MarshalPrivateKeyToPEM(cert.Curve_CURVE25519, priv)
	if err := os.WriteFile(filepath.Join(dir, "node.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubPreStartRenew replaces the pre-start renewal for one test.
func stubPreStartRenew(t *testing.T, fn func(*meshInstance) error) {
	t.Helper()
	orig := preStartRenewFn
	preStartRenewFn = fn
	t.Cleanup(func() { preStartRenewFn = orig })
}

// newRecoveryTestClient is a started-looking Client (runCtx set, no real
// mesh) with a fast or slow recovery schedule.
func newRecoveryTestClient(t *testing.T, backoff time.Duration) (*Client, context.CancelFunc) {
	t.Helper()
	c, _ := newTestClientForBootResilience(t)
	c.recoveryBackoff = []time.Duration{backoff}
	ctx, cancel := context.WithCancel(context.Background())
	c.runCtx, c.runCancel = ctx, cancel
	c.started.Store(true)
	t.Cleanup(func() { _ = c.Stop() })
	return c, cancel
}

func addEnrollment(t *testing.T, c *Client, name string) *Enrollment {
	t.Helper()
	e := &Enrollment{Name: name, NodeID: "node-" + name, Endpoint: "https://cp.invalid"}
	if err := c.enrolls.Add(e); err != nil {
		t.Fatal(err)
	}
	return e
}

// eventually polls cond until it is true or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// --- certStartProblem -----------------------------------------------------

func TestCertStartProblem(t *testing.T) {
	notBefore := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	notAfter := notBefore.Add(24 * time.Hour)
	tol := cert.ClockSkewTolerance
	if tol <= 0 {
		t.Fatalf("cert.ClockSkewTolerance = %v; vendor patch 25 should set a positive tolerance", tol)
	}

	dir := t.TempDir()
	writeNodeCert(t, dir, notBefore, notAfter)

	cases := []struct {
		name         string
		now          time.Time
		wantProblem  string
		wantBlocking bool
	}{
		{"valid mid-life", notBefore.Add(12 * time.Hour), "", false},
		{"valid one second before expiry", notAfter.Add(-time.Second), "", false},
		{"exactly at NotAfter", notAfter, "expired", false},
		{"expired within clock-skew tolerance", notAfter.Add(tol / 2), "expired", false},
		{"expired at tolerance edge", notAfter.Add(tol), "expired", false},
		{"expired beyond tolerance", notAfter.Add(tol + time.Second), "expired", true},
		{"expired for months (the incident)", notAfter.Add(180 * 24 * time.Hour), "expired", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, b := certStartProblem(dir, tc.now)
			if p != tc.wantProblem || b != tc.wantBlocking {
				t.Errorf("certStartProblem = (%q, %v), want (%q, %v)", p, b, tc.wantProblem, tc.wantBlocking)
			}
		})
	}
}

func TestCertStartProblem_MissingAndCorruptFiles(t *testing.T) {
	now := time.Now()
	t.Run("missing node.crt", func(t *testing.T) {
		dir := t.TempDir()
		writeNodeCert(t, dir, now.Add(-time.Hour), now.Add(time.Hour))
		os.Remove(filepath.Join(dir, "node.crt"))
		if p, b := certStartProblem(dir, now); p != "missing" || !b {
			t.Errorf("got (%q, %v), want (missing, true)", p, b)
		}
	})
	t.Run("missing node.key", func(t *testing.T) {
		dir := t.TempDir()
		writeNodeCert(t, dir, now.Add(-time.Hour), now.Add(time.Hour))
		os.Remove(filepath.Join(dir, "node.key"))
		if p, b := certStartProblem(dir, now); p != "missing" || !b {
			t.Errorf("got (%q, %v), want (missing, true)", p, b)
		}
	})
	t.Run("empty dir", func(t *testing.T) {
		if p, b := certStartProblem(t.TempDir(), now); p != "missing" || !b {
			t.Errorf("got (%q, %v), want (missing, true)", p, b)
		}
	})
	t.Run("corrupt node.crt", func(t *testing.T) {
		dir := t.TempDir()
		writeNodeCert(t, dir, now.Add(-time.Hour), now.Add(time.Hour))
		os.WriteFile(filepath.Join(dir, "node.crt"), []byte("not a pem"), 0o644)
		if p, b := certStartProblem(dir, now); p != "unreadable" || !b {
			t.Errorf("got (%q, %v), want (unreadable, true)", p, b)
		}
	})
	t.Run("truncated node.crt (partial write)", func(t *testing.T) {
		dir := t.TempDir()
		writeNodeCert(t, dir, now.Add(-time.Hour), now.Add(time.Hour))
		full, _ := os.ReadFile(filepath.Join(dir, "node.crt"))
		os.WriteFile(filepath.Join(dir, "node.crt"), full[:len(full)/2], 0o644)
		if p, b := certStartProblem(dir, now); p != "unreadable" || !b {
			t.Errorf("got (%q, %v), want (unreadable, true)", p, b)
		}
	})
}

// --- ensureUsableCert (renew before Nebula starts) -------------------------

func TestEnsureUsableCert(t *testing.T) {
	now := time.Now()
	tol := cert.ClockSkewTolerance
	renewErr := errors.New("control plane unreachable")

	cases := []struct {
		name       string
		notAfter   time.Time
		noEndpoint bool
		renew      error
		wantCalls  int
		wantErr    bool
	}{
		{"valid cert: no renewal", now.Add(12 * time.Hour), false, nil, 0, false},
		{"expired: renewed, start proceeds", now.Add(-48 * time.Hour), false, nil, 1, false},
		{"expired: renewal fails, start blocked", now.Add(-48 * time.Hour), false, renewErr, 1, true},
		{"expired within tolerance: renewal fails, start proceeds", now.Add(-tol / 4), false, renewErr, 1, false},
		{"expired, no endpoint: nothing to renew against", now.Add(-48 * time.Hour), true, nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClientForBootResilience(t)
			e := &Enrollment{Name: "home", NodeID: "n1", Endpoint: "https://cp.invalid"}
			if tc.noEndpoint {
				e.Endpoint = ""
			}
			dir := enrollmentDir(c.cfg.ConfigDir, e.Name)
			writeNodeCert(t, dir, tc.notAfter.Add(-24*time.Hour), tc.notAfter)

			var calls int
			stubPreStartRenew(t, func(inst *meshInstance) error {
				calls++
				if got := inst.dir(); got != dir {
					t.Errorf("renewal targeted dir %q, want enrollment dir %q", got, dir)
				}
				if inst.endpoint() != e.Endpoint || inst.nodeID() != e.NodeID {
					t.Errorf("renewal got endpoint=%q nodeID=%q", inst.endpoint(), inst.nodeID())
				}
				return tc.renew
			})

			err := c.ensureUsableCert(e)
			if calls != tc.wantCalls {
				t.Errorf("renewal calls = %d, want %d", calls, tc.wantCalls)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				var cre *certRenewalError
				if !errors.As(err, &cre) {
					t.Fatalf("error %T is not *certRenewalError", err)
				}
				if !errors.Is(err, renewErr) {
					t.Error("certRenewalError must unwrap to the renewal error")
				}
				if !strings.HasPrefix(err.Error(), "certificate expired") {
					t.Errorf("error %q must start with \"certificate expired\" (UI + local API key on it)", err)
				}
			}
		})
	}
}

func TestEnsureUsableCert_MissingKeyIsRenewed(t *testing.T) {
	c, _ := newTestClientForBootResilience(t)
	e := &Enrollment{Name: "work", NodeID: "n2", Endpoint: "https://cp.invalid"}
	dir := enrollmentDir(c.cfg.ConfigDir, e.Name)
	writeNodeCert(t, dir, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	os.Remove(filepath.Join(dir, "node.key"))

	calls := 0
	stubPreStartRenew(t, func(*meshInstance) error { calls++; return nil })
	if err := c.ensureUsableCert(e); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("missing node.key must trigger a renewal (renewal returns a fresh key); calls = %d", calls)
	}
}

func TestEnsureUsableCert_RevokedNodeSurfacesSentinel(t *testing.T) {
	c, _ := newTestClientForBootResilience(t)
	e := &Enrollment{Name: "home", NodeID: "n1", Endpoint: "https://cp.invalid"}
	writeNodeCert(t, enrollmentDir(c.cfg.ConfigDir, e.Name), time.Now().Add(-72*time.Hour), time.Now().Add(-48*time.Hour))
	stubPreStartRenew(t, func(*meshInstance) error { return fmt.Errorf("%w for enrollment %q", errNodeRevoked, "home") })

	err := c.ensureUsableCert(e)
	if !errors.Is(err, errNodeRevoked) {
		t.Fatalf("err = %v; must unwrap to errNodeRevoked so recovery slows down", err)
	}
}

// TestConnect_ExpiredCertRenewalFailure_DoesNotStartNebula: when the cert is
// expired and renewal fails, connect returns the renewal error without
// creating an instance (no Nebula start that is bound to fail).
func TestConnect_ExpiredCertRenewalFailure_DoesNotStartNebula(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	e := addEnrollment(t, c, "home")
	writeNodeCert(t, enrollmentDir(c.cfg.ConfigDir, e.Name), time.Now().Add(-72*time.Hour), time.Now().Add(-48*time.Hour))
	stubPreStartRenew(t, func(*meshInstance) error { return errRateLimited })

	err := c.connect("home")
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("connect err = %v, want wrapped errRateLimited", err)
	}
	if c.instances.get("home") != nil {
		t.Error("connect must not register an instance when the cert can't be made usable")
	}
}

// TestConnectLocked_RenewsBeforeStartingNebula is the ordering tripwire:
// the pre-start renewal must run before any instance is created/started.
// Moving it after startInstance re-creates the incident (Nebula refuses the
// expired cert, the instance is torn down, renewal never runs).
func TestConnectLocked_RenewsBeforeStartingNebula(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "func (c *Client) connectLocked(")
	iRenew := strings.Index(body, "c.ensureUsableCert(e)")
	iStart := strings.Index(body, "c.startInstance(")
	iNew := strings.Index(body, "c.newInstance(e)")
	if iRenew < 0 {
		t.Fatal("REGRESSION: connectLocked no longer calls c.ensureUsableCert(e) — expired certs can never recover (incident 2026-11-18)")
	}
	if iStart < 0 || iNew < 0 {
		t.Fatal("connectLocked shape changed; re-check this tripwire")
	}
	if iRenew > iStart || iRenew > iNew {
		t.Error("REGRESSION: ensureUsableCert must run before the instance is created and Nebula started")
	}
}

// funcBody returns the source of the function starting at sig, through its
// matching closing brace.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%s not found", sig)
	}
	depth, started := 0, false
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return src[i : j+1]
			}
		}
	}
	t.Fatalf("unterminated %s", sig)
	return ""
}

// --- background recovery loop ---------------------------------------------

func TestRecovery_RetriesUntilConnected(t *testing.T) {
	c, _ := newTestClientForBootResilience(t)
	c.recoveryBackoff = []time.Duration{time.Millisecond}
	addEnrollment(t, c, "home")
	var calls atomic.Int32
	c.connectOverride = func(string) error {
		if calls.Add(1) <= 3 {
			return errRateLimited
		}
		return nil
	}
	t.Cleanup(func() { _ = c.Stop() })

	if err := c.Start(context.Background()); err == nil {
		t.Fatal("Start should report the single enrollment's boot failure")
	}
	if c.LastBootError("home") == "" {
		t.Fatal("boot failure must be recorded")
	}
	eventually(t, "recovery loop to connect and exit", func() bool {
		_, running := c.recoveryInfo("home")
		return !running && calls.Load() == 4
	})
	if got := c.LastBootError("home"); got != "" {
		t.Errorf("boot error should be cleared after background recovery, got %q", got)
	}
	time.Sleep(20 * time.Millisecond)
	if n := calls.Load(); n != 4 {
		t.Errorf("connect called %d times; the loop must stop after success", n)
	}
}

func TestRecovery_OneLoopPerEnrollment(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	c.scheduleRecovery("home", errors.New("first"))
	c.scheduleRecovery("home", errors.New("second"))
	c.scheduleRecovery("home", errors.New("third"))

	c.recoveryMu.Lock()
	n := len(c.recoveries)
	c.recoveryMu.Unlock()
	if n != 1 {
		t.Fatalf("recoveries = %d, want 1 (deduped per enrollment)", n)
	}
	rec, ok := c.recoveryInfo("home")
	if !ok || rec.LastErr != "third" {
		t.Errorf("recoveryInfo = %+v, %v; want the latest cause recorded", rec, ok)
	}
}

func TestRecovery_IndependentPerEnrollment(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	addEnrollment(t, c, "work")
	c.scheduleRecovery("home", errors.New("x"))
	c.scheduleRecovery("work", errors.New("y"))
	c.cancelRecovery("home")
	if _, ok := c.recoveryInfo("home"); ok {
		t.Error("home loop should be cancelled")
	}
	if _, ok := c.recoveryInfo("work"); !ok {
		t.Error("cancelling home must not touch work's loop")
	}
}

func TestRecovery_NotScheduled(t *testing.T) {
	t.Run("client not started", func(t *testing.T) {
		c, _ := newTestClientForBootResilience(t)
		addEnrollment(t, c, "home")
		c.scheduleRecovery("home", errors.New("x"))
		if _, ok := c.recoveryInfo("home"); ok {
			t.Error("no loop may start before Client.Start (runCtx nil)")
		}
	})
	t.Run("client stopped", func(t *testing.T) {
		c, cancel := newRecoveryTestClient(t, time.Hour)
		addEnrollment(t, c, "home")
		cancel()
		c.scheduleRecovery("home", errors.New("x"))
		if _, ok := c.recoveryInfo("home"); ok {
			t.Error("no loop may start after the client context is cancelled")
		}
	})
	t.Run("unknown enrollment", func(t *testing.T) {
		c, _ := newRecoveryTestClient(t, time.Hour)
		c.scheduleRecovery("ghost", errors.New("x"))
		if _, ok := c.recoveryInfo("ghost"); ok {
			t.Error("no loop for an enrollment that doesn't exist")
		}
	})
	t.Run("nil error", func(t *testing.T) {
		c, _ := newRecoveryTestClient(t, time.Hour)
		addEnrollment(t, c, "home")
		c.scheduleRecovery("home", nil)
		if _, ok := c.recoveryInfo("home"); ok {
			t.Error("nothing to recover from a nil error")
		}
	})
	t.Run("node revoked (401)", func(t *testing.T) {
		c, _ := newRecoveryTestClient(t, time.Hour)
		addEnrollment(t, c, "home")
		c.scheduleRecovery("home", &certRenewalError{problem: "expired", err: fmt.Errorf("%w", errNodeRevoked)})
		eventually(t, "the retry to be planned", func() bool {
			r, _ := c.recoveryInfo("home")
			return !r.NextAt.IsZero()
		})
		if r, _ := c.recoveryInfo("home"); time.Until(r.NextAt) < 40*time.Minute || time.Until(r.NextAt) > 80*time.Minute {
			t.Errorf("a deleted node is asked for again in %v, want about an hour", time.Until(r.NextAt))
		}
	})
	t.Run("user disconnected", func(t *testing.T) {
		c, _ := newRecoveryTestClient(t, time.Hour)
		addEnrollment(t, c, "home")
		c.setUserDisconnected("home", true)
		c.scheduleRecovery("home", errors.New("x"))
		if _, ok := c.recoveryInfo("home"); ok {
			t.Error("must not reconnect an enrollment the user disconnected")
		}
	})
}

// A 401 is usually final, but servers up to v0.11.26 also answered it when
// their own database was busy. So: ask again, but rarely.
func TestRecovery_SlowsDownAfter401(t *testing.T) {
	old := revokedRetryDelay
	revokedRetryDelay = 500 * time.Millisecond
	t.Cleanup(func() { revokedRetryDelay = old })
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	var calls atomic.Int32
	c.connectOverride = func(string) error {
		switch calls.Add(1) {
		case 1:
			return errRateLimited
		case 2:
			return fmt.Errorf("renew: %w", errNodeRevoked)
		}
		return nil
	}
	c.scheduleRecovery("home", errRateLimited)
	eventually(t, "the 401", func() bool { return calls.Load() == 2 })
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Errorf("%d attempts right after the 401", n-2)
	}
	if !strings.Contains(c.LastBootError("home"), "401") {
		t.Errorf("the 401 must stay visible in LastBootError; got %q", c.LastBootError("home"))
	}
	eventually(t, "the next attempt, which succeeds", func() bool {
		_, running := c.recoveryInfo("home")
		return !running && calls.Load() == 3
	})
}

func TestRecovery_StopsWhenEnrollmentRemoved(t *testing.T) {
	c, _ := newRecoveryTestClient(t, 5*time.Millisecond)
	addEnrollment(t, c, "home")
	var calls atomic.Int32
	c.connectOverride = func(string) error { calls.Add(1); return errRateLimited }
	c.scheduleRecovery("home", errRateLimited)
	eventually(t, "first background attempt", func() bool { return calls.Load() >= 1 })
	if err := c.enrolls.Remove("home"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "loop to exit after removal", func() bool {
		_, running := c.recoveryInfo("home")
		return !running
	})
	n := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() > n+1 {
		t.Error("loop kept connecting after the enrollment was removed")
	}
}

func TestRecovery_StopsOnClientStop(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	c.scheduleRecovery("home", errors.New("x"))
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "loop to exit on Stop", func() bool {
		_, running := c.recoveryInfo("home")
		return !running
	})
}

func TestRecovery_StopsOnDisconnect_AndUserConnectReEnables(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	c.connectOverride = func(string) error { return errRateLimited }

	if err := c.connectAndSupervise("home", true); err == nil {
		t.Fatal("expected failure")
	}
	if _, ok := c.recoveryInfo("home"); !ok {
		t.Fatal("failed connect should start a recovery loop")
	}
	if err := c.disconnect("home"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.recoveryInfo("home"); ok {
		t.Fatal("disconnect must stop the recovery loop even with no live instance")
	}
	// A watchdog-style (non-user) reconnect must not resurrect it.
	calls := 0
	c.connectOverride = func(string) error { calls++; return nil }
	if err := c.connectAndSupervise("home", false); err != nil || calls != 0 {
		t.Fatalf("non-user reconnect after a user disconnect: err=%v calls=%d, want nil/0", err, calls)
	}
	// The user clicking Connect clears the intent and connects.
	if err := c.connectAndSupervise("home", true); err != nil || calls != 1 {
		t.Fatalf("user reconnect: err=%v calls=%d, want nil/1", err, calls)
	}
	if c.isUserDisconnected("home") {
		t.Error("user Connect must clear the disconnected intent")
	}
}

func TestRecovery_SuccessfulConnectCancelsLoop(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	c.scheduleRecovery("home", errors.New("x"))
	c.connectOverride = func(string) error { return nil }
	if err := c.Connect(context.Background(), "home"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.recoveryInfo("home"); ok {
		t.Error("a successful Connect must cancel the pending loop (else status keeps saying \"retrying\")")
	}
}

// TestRecovery_DisconnectWaitsForInFlightAttempt: a background attempt that
// is mid-connect when the user disconnects must not leave the enrollment up
// — disconnect serializes behind it via connectLock and wins.
func TestRecovery_DisconnectWaitsForInFlightAttempt(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Millisecond)
	addEnrollment(t, c, "home")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	c.connectOverride = func(string) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	}
	c.scheduleRecovery("home", errors.New("x"))
	<-entered

	disconnected := make(chan struct{})
	go func() {
		_ = c.disconnect("home")
		close(disconnected)
	}()
	select {
	case <-disconnected:
		t.Fatal("disconnect returned while a connect attempt held the per-enrollment lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect never completed")
	}
	if !c.isUserDisconnected("home") {
		t.Error("user intent lost")
	}
	eventually(t, "loop to exit", func() bool {
		_, running := c.recoveryInfo("home")
		return !running
	})
}

// --- watchdog restarts ----------------------------------------------------

func newStaleInstance(t *testing.T, c *Client, name string) (*meshInstance, context.Context) {
	t.Helper()
	inst := newMeshInstance(c.enrolls.Get(name))
	ctx, cancel := context.WithCancel(context.Background())
	inst.runCtx, inst.runCancel = ctx, cancel
	c.instances.add(inst)
	return inst, ctx
}

// TestRestartInstance_TearsDownWedgedInstance is the core regression test
// for the watchdog no-op: pre-HH restartFn was connect(name), which returned
// nil immediately because the wedged instance was still registered with a
// live Nebula control. restartInstance must close it first so connect
// actually brings up a fresh one.
func TestRestartInstance_TearsDownWedgedInstance(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	stale, staleCtx := newStaleInstance(t, c, "home")

	var sawStaleRegistered bool
	calls := 0
	c.connectOverride = func(name string) error {
		calls++
		sawStaleRegistered = c.instances.get(name) == stale
		return nil
	}
	if err := c.restartInstance("home", stale); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("connect calls = %d, want 1", calls)
	}
	if sawStaleRegistered {
		t.Error("REGRESSION: the wedged instance was still registered when connect ran — connect's \"already up\" short-circuit makes the restart a no-op")
	}
	if staleCtx.Err() == nil {
		t.Error("the wedged instance's goroutines (renewal, heartbeat, watchdogs) must be cancelled")
	}
}

func TestRestartInstance_LeavesReplacementAlone(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	stale := newMeshInstance(c.enrolls.Get("home"))
	current, currentCtx := newStaleInstance(t, c, "home")
	c.connectOverride = func(string) error { return nil }

	if err := c.restartInstance("home", stale); err != nil {
		t.Fatal(err)
	}
	if c.instances.get("home") != current || currentCtx.Err() != nil {
		t.Error("a late watchdog from an already-replaced instance must not tear down the current one")
	}
}

func TestRestartInstance_RespectsUserDisconnect(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	c.setUserDisconnected("home", true)
	calls := 0
	c.connectOverride = func(string) error { calls++; return nil }
	if err := c.restartInstance("home", stale); err != nil || calls != 0 {
		t.Errorf("restart after user disconnect: err=%v calls=%d, want nil/0", err, calls)
	}
}

func TestRestartInstance_FailureSchedulesRecovery(t *testing.T) {
	c, _ := newRecoveryTestClient(t, time.Hour)
	addEnrollment(t, c, "home")
	stale, _ := newStaleInstance(t, c, "home")
	c.connectOverride = func(string) error { return errRateLimited }
	if err := c.restartInstance("home", stale); !errors.Is(err, errRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := c.recoveryInfo("home"); !ok {
		t.Error("a restart that can't bring the instance back must keep retrying in the background")
	}
}

// TestConnectLocked_RestartFnIsRealRestart: source tripwire on the wiring.
func TestConnectLocked_RestartFnIsRealRestart(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "func (c *Client) connectLocked(")
	if !strings.Contains(body, "inst.restartFn = func() error { return c.restartInstance(instName, inst) }") {
		t.Error("REGRESSION: restartFn must call c.restartInstance(instName, inst). Plain connect(name) short-circuits on \"already up\" and makes every watchdog restart a silent no-op (v0.10.36–v0.11.26)")
	}
}

// --- timing helpers -------------------------------------------------------

func TestWaitWallClock(t *testing.T) {
	t.Run("elapses", func(t *testing.T) {
		start := time.Now()
		if !waitWallClock(context.Background(), 30*time.Millisecond) {
			t.Fatal("want true")
		}
		if el := time.Since(start); el < 30*time.Millisecond {
			t.Errorf("returned after %v, before the deadline", el)
		}
	})
	t.Run("zero duration", func(t *testing.T) {
		if !waitWallClock(context.Background(), 0) {
			t.Fatal("want true")
		}
	})
	t.Run("pre-cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if waitWallClock(ctx, time.Hour) {
			t.Fatal("want false")
		}
	})
	t.Run("cancelled while waiting returns promptly", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		start := time.Now()
		if waitWallClock(ctx, time.Hour) {
			t.Fatal("want false")
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("took %v to notice cancellation", el)
		}
	})
}

// TestWaitWallClock_PollsWallClock: the wait must not be a single long
// monotonic timer (those stall during macOS deep sleep — Phase S).
func TestWaitWallClock_PollsWallClock(t *testing.T) {
	if recoveryWallClockTick > time.Minute {
		t.Errorf("recoveryWallClockTick = %v; keep it short so a post-wake overshoot stays small", recoveryWallClockTick)
	}
	src, err := os.ReadFile("recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "func waitWallClock(")
	if !strings.Contains(body, "Round(0)") || !strings.Contains(body, "NewTicker") {
		t.Error("waitWallClock must compare wall-clock time (Round(0)) on a short ticker")
	}
	if strings.Contains(body, "time.After(d)") || strings.Contains(body, "NewTimer(d)") {
		t.Error("waitWallClock must not sleep on a single monotonic timer for the whole duration")
	}
}

func TestRecoveryDelay(t *testing.T) {
	c := &Client{}
	sched := defaultRecoveryBackoff
	for attempt := 0; attempt < len(sched)+5; attempt++ {
		base := sched[len(sched)-1]
		if attempt < len(sched) {
			base = sched[attempt]
		}
		lo, hi := base-base/5, base+base/5
		for i := 0; i < 50; i++ {
			if d := c.recoveryDelay(attempt, nil); d < lo || d > hi {
				t.Fatalf("attempt %d: delay %v outside [%v, %v]", attempt, d, lo, hi)
			}
		}
	}
	if sched[0] > time.Minute {
		t.Errorf("first retry %v is too slow for transient failures", sched[0])
	}
	if last := sched[len(sched)-1]; last > 15*time.Minute || last < time.Minute {
		t.Errorf("backoff cap %v: must stay well under the 24h cert lifetime but not hammer the control plane", last)
	}
	c.recoveryBackoff = []time.Duration{time.Second}
	if d := c.recoveryDelay(7, nil); d < 800*time.Millisecond || d > 1200*time.Millisecond {
		t.Errorf("override schedule ignored: %v", d)
	}
}

func TestNextHeartbeatRetry(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		err      error
		want     time.Duration
	}{
		{"success keeps interval", 5 * time.Second, nil, 5 * time.Second},
		{"other error keeps interval", 5 * time.Second, errors.New("HTTP 500"), 5 * time.Second},
		{"429 raises short retry", 5 * time.Second, errRateLimited, rateLimitedMinRetry},
		{"wrapped 429 raises short retry", 5 * time.Second, fmt.Errorf("x: %w", errRateLimited), rateLimitedMinRetry},
		{"429 keeps longer interval", 5 * time.Minute, errRateLimited, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextHeartbeatRetry(tc.interval, tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- control-plane status mapping -------------------------------------------

func newHTTPTestInstance(t *testing.T, endpoint string) *meshInstance {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("agent-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := newMeshInstance(&Enrollment{Name: "home", NodeID: "node-1", Endpoint: endpoint})
	inst.customDir = dir
	return inst
}

func TestFetchAndInstallCert_StatusMapping(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
	}{
		{http.StatusUnauthorized, errNodeRevoked},
		{http.StatusTooManyRequests, errRateLimited},
		{http.StatusInternalServerError, nil},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			err := fetchAndInstallCert(newHTTPTestInstance(t, srv.URL))
			if err == nil {
				t.Fatal("want error")
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want %v", err, tc.sentinel)
			}
			if tc.sentinel == nil && (errors.Is(err, errNodeRevoked) || errors.Is(err, errRateLimited)) {
				t.Errorf("a 5xx must not be classified as revoked/rate-limited: %v", err)
			}
		})
	}
}

func TestFetchAndInstallCert_InstallsWithoutNebula(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodeCert":"NEW-CERT","nodeKey":"NEW-KEY"}`))
	}))
	defer srv.Close()
	inst := newHTTPTestInstance(t, srv.URL)

	if err := fetchAndInstallCert(inst); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer agent-token" {
		t.Errorf("renewal must authenticate with the bearer token (never the cert); got %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"nodeId":"node-1"`) {
		t.Errorf("body = %q", gotBody)
	}
	crt, _ := os.ReadFile(filepath.Join(inst.dir(), "node.crt"))
	key, _ := os.ReadFile(filepath.Join(inst.dir(), "node.key"))
	if string(crt) != "NEW-CERT" || string(key) != "NEW-KEY" {
		t.Errorf("installed crt=%q key=%q", crt, key)
	}
	if fi, err := os.Stat(filepath.Join(inst.dir(), "node.key")); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("node.key mode = %v, want 0600", fi.Mode().Perm())
	}
	if inst.control() != nil {
		t.Error("fetchAndInstallCert must not start Nebula")
	}
}

func TestFetchAndInstallCert_EmptyResponseLeavesOldCert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nodeCert":"","nodeKey":""}`))
	}))
	defer srv.Close()
	inst := newHTTPTestInstance(t, srv.URL)
	os.WriteFile(filepath.Join(inst.dir(), "node.crt"), []byte("OLD"), 0o644)
	if err := fetchAndInstallCert(inst); err == nil {
		t.Fatal("empty cert must be an error")
	}
	if b, _ := os.ReadFile(filepath.Join(inst.dir(), "node.crt")); string(b) != "OLD" {
		t.Error("an empty response must not clobber the existing cert")
	}
}

func TestSendHeartbeat_StatusMapping(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
	}{
		{http.StatusUnauthorized, errNodeRevoked},
		{http.StatusTooManyRequests, errRateLimited},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			if err := sendHeartbeat(newHTTPTestInstance(t, srv.URL)); !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want %v", err, tc.sentinel)
			}
		})
	}
	t.Run("204 is success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		if err := sendHeartbeat(newHTTPTestInstance(t, srv.URL)); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}

// TestSendHeartbeat_ReportsItsOwnMachine: the node row keeps whatever
// enrollment guessed, and an enrollment guesses — scripts/install.sh says
// "linux" whatever it runs on. The heartbeat is the only place the
// machine's real OS and CPU ever arrive, so a Windows node enrolled by
// that script stays listed as Linux forever if this field is dropped.
func TestSendHeartbeat_ReportsItsOwnMachine(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
	}))
	defer srv.Close()
	if err := sendHeartbeat(newHTTPTestInstance(t, srv.URL)); err != nil {
		t.Fatal(err)
	}
	if body["os"] != runtime.GOOS || body["arch"] != detectArch() {
		t.Errorf("heartbeat reports %v/%v, this machine is %s/%s", body["os"], body["arch"], runtime.GOOS, detectArch())
	}
}

// --- status surface ---------------------------------------------------------

func TestRecoveryMessage(t *testing.T) {
	cases := []struct {
		prev, lastErr, want string
	}{
		{"certificate expired", "certificate expired and renewal failed: HTTP 429: rate limited by control plane",
			"certificate expired — renewing automatically (last attempt: renewal failed: HTTP 429: rate limited by control plane)"},
		{"", "connect failed after 4 attempts: port busy",
			"not connected — retrying automatically (last attempt: connect failed after 4 attempts: port busy)"},
		{"certificate expired", "", "certificate expired — renewing automatically"},
		{"certificate expired", `certificate expired and renewal failed: 401: node deleted or token revoked for enrollment "home"`,
			"certificate expired — the server says this device was removed from the network. Leave the network, then join it again"},
		{"", `certificate missing and renewal failed: 401: node deleted or token revoked for enrollment "home"`,
			"not connected — the server says this device was removed from the network. Leave the network, then join it again"},
	}
	for _, tc := range cases {
		if got := recoveryMessage(tc.prev, recoveryStatus{LastErr: tc.lastErr}); got != tc.want {
			t.Errorf("recoveryMessage(%q, %q)\n got %q\nwant %q", tc.prev, tc.lastErr, got, tc.want)
		}
	}
	// The local API's certValid derivation and existing UI consumers key on
	// this prefix; keep it.
	if got := recoveryMessage("certificate expired", recoveryStatus{}); !strings.HasPrefix(got, "certificate expired") {
		t.Errorf("lost the \"certificate expired\" prefix: %q", got)
	}
}

// TestLocalAPI_StatusShowsRecoveryAndDisconnectStopsIt drives the local API
// end to end: a failed /local/connect starts a loop that /local/status
// reports; /local/disconnect with no live instance (the incident state)
// still stops it.
func TestLocalAPI_StatusShowsRecoveryAndDisconnectStopsIt(t *testing.T) {
	srv, h, _ := newTestLocalAPIServer(t, func(string) error { return errRateLimited }, nil)
	c := srv.client
	c.recoveryBackoff = []time.Duration{time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.runCtx, c.runCancel = ctx, cancel
	addEnrollment(t, c, "home")

	rec, _ := authedReq(t, h, "POST", "/local/connect?enrollment=home", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("connect should fail, got 200")
	}
	if _, ok := c.recoveryInfo("home"); !ok {
		t.Fatal("failed /local/connect must start background recovery")
	}

	lastError := func() string {
		st := srv.buildStatus()
		for _, e := range st.Enrollments {
			if e.Name == "home" {
				return e.LastError
			}
		}
		t.Fatal("home missing from status")
		return ""
	}
	if le := lastError(); !strings.Contains(le, "retrying automatically") || !strings.Contains(le, "429") {
		t.Errorf("status lastError = %q; want it to say it is retrying and why", le)
	}

	rec, body := authedReq(t, h, "POST", "/local/disconnect?enrollment=home", nil)
	if rec.Code != http.StatusOK || body["status"] != "already-disconnected" {
		t.Fatalf("disconnect: %d %v", rec.Code, body)
	}
	if _, ok := c.recoveryInfo("home"); ok {
		t.Error("/local/disconnect must stop the loop even when no instance is live")
	}
	if le := lastError(); strings.Contains(le, "retrying automatically") {
		t.Errorf("status still claims to be retrying after disconnect: %q", le)
	}
}
