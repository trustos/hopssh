package client

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Renewal timing. From v0.10.82 to v0.11.26 the deadline was "half of the
// time remaining", recomputed on every poll, so it never arrived and no agent
// renewed before its cert had already expired.

// issueCert writes a node cert of the given age and lifetime into inst's dir.
func issueCert(t *testing.T, inst *meshInstance, age, life time.Duration) {
	t.Helper()
	nb := time.Now().Add(-age)
	writeNodeCert(t, inst.dir(), nb, nb.Add(life))
}

func TestTimeUntilRenewal(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		name      string
		age, life time.Duration
		due       bool
	}{
		{"fresh", 0, day, false},
		{"clock behind the server (cert not valid yet)", -30 * time.Minute, day, false},
		{"before the earliest renewal", 9*time.Hour + 30*time.Minute, day, false},
		{"after the latest renewal", 14*time.Hour + 30*time.Minute, day, true},
		{"6h left", 18 * time.Hour, day, true},
		{"1h left", 23 * time.Hour, day, true},
		{"5s left", day - 5*time.Second, day, true},
		{"just expired", day + time.Second, day, true},
		{"expired within clock-skew tolerance", day + 30*time.Minute, day, true},
		{"expired months ago", 180 * day, day, true},
		{"short-lived cert, early", 20 * time.Minute, time.Hour, false},
		{"short-lived cert, late", 40 * time.Minute, time.Hour, true},
		{"long-lived cert, early", 100 * day, 365 * day, false},
		{"long-lived cert, late", 250 * day, 365 * day, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := newHTTPTestInstance(t, "")
			issueCert(t, inst, tc.age, tc.life)
			d, err := timeUntilRenewal(inst)
			if err != nil {
				t.Fatal(err)
			}
			if due := d <= 0; due != tc.due {
				t.Errorf("due = %v (in %s), want %v", due, d, tc.due)
			}
		})
	}
}

func TestTimeUntilRenewal_BadCertFile(t *testing.T) {
	inst := newHTTPTestInstance(t, "")
	if _, err := timeUntilRenewal(inst); err == nil {
		t.Error("missing cert: want error")
	}
	os.WriteFile(filepath.Join(inst.dir(), "node.crt"), []byte("garbage"), 0o644)
	if _, err := timeUntilRenewal(inst); err == nil {
		t.Error("corrupt cert: want error")
	}
}

// TestTimeUntilRenewal_SameDeadlineEveryPoll: the jitter belongs to the cert.
// The old code drew a new one on each poll.
func TestTimeUntilRenewal_SameDeadlineEveryPoll(t *testing.T) {
	inst := newHTTPTestInstance(t, "")
	issueCert(t, inst, 0, 24*time.Hour)
	first, err := timeUntilRenewal(inst)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(first)
	for i := 0; i < 500; i++ {
		d, _ := timeUntilRenewal(inst)
		if drift := time.Now().Add(d).Sub(deadline).Abs(); drift > time.Second {
			t.Fatalf("poll %d: the deadline moved by %s; it must be a fixed instant for a given cert", i, drift)
		}
	}
}

func TestRenewalTime_BoundsAndSpread(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5000; i++ {
		nb := base.Add(time.Duration(rng.Int63n(int64(1000 * 24 * time.Hour))))
		life := time.Minute + time.Duration(rng.Int63n(int64(400*24*time.Hour)))
		na := nb.Add(life)
		at := renewalTime(nb, na, fmt.Sprint(i))
		if lo, hi := nb.Add(life*4/10), nb.Add(life*6/10+time.Second); at.Before(lo) || at.After(hi) {
			t.Fatalf("life %s: renewal at %s of life, want 40–60%%", life, at.Sub(nb))
		}
		if !at.Equal(renewalTime(nb, na, fmt.Sprint(i))) {
			t.Fatal("same cert, different renewal time")
		}
	}

	// Agents enrolled in the same second must not renew in the same minute.
	nb, na := base, base.Add(24*time.Hour)
	minutes := map[int64]bool{}
	for i := 0; i < 200; i++ {
		minutes[renewalTime(nb, na, fmt.Sprintf("node-%d", i)).Unix()/60] = true
	}
	if len(minutes) < 100 {
		t.Errorf("200 agents renew in only %d distinct minutes", len(minutes))
	}
	if renewalTime(nb, na, "").IsZero() {
		t.Error("an empty node ID must still give a renewal time")
	}
}

// renewServer counts /api/renew POSTs and answers with certs of the given
// age and life.
func renewServer(t *testing.T, posts *atomic.Int64, age, life time.Duration) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		dir := t.TempDir()
		nb := time.Now().Add(-age)
		writeNodeCert(t, dir, nb, nb.Add(life))
		crt, _ := os.ReadFile(filepath.Join(dir, "node.crt"))
		key, _ := os.ReadFile(filepath.Join(dir, "node.key"))
		json.NewEncoder(w).Encode(map[string]string{"nodeCert": string(crt), "nodeKey": string(key)})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// runRenewalLoop runs the real loop against inst for d, polling every 10ms.
func runRenewalLoop(t *testing.T, inst *meshInstance, d time.Duration) {
	t.Helper()
	origPoll, origStart := renewalPollInterval, startMeshFn
	renewalPollInterval = 10 * time.Millisecond
	startMeshFn = func(string, string) (meshService, error) { return nil, fmt.Errorf("no Nebula in this test") }
	defer func() { renewalPollInterval, startMeshFn = origPoll, origStart }()

	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runCertRenewal(ctx, inst) }()
	select {
	case <-done:
	case <-time.After(d + 10*time.Second):
		t.Fatal("renewal loop did not stop with its context")
	}
}

// TestRunCertRenewal_RenewsWhileStillValid runs the real loop on a cert with
// 9h left. v0.11.26 sends nothing here and first renews after expiry.
func TestRunCertRenewal_RenewsWhileStillValid(t *testing.T) {
	var posts atomic.Int64
	inst := newHTTPTestInstance(t, renewServer(t, &posts, 0, 24*time.Hour))
	issueCert(t, inst, 15*time.Hour, 24*time.Hour)

	runRenewalLoop(t, inst, 400*time.Millisecond)

	if n := posts.Load(); n != 1 {
		t.Fatalf("renewal POSTs = %d, want exactly 1 (9h before expiry, then quiet on the fresh cert)", n)
	}
	if d, err := timeUntilRenewal(inst); err != nil || d < 9*time.Hour {
		t.Errorf("after renewing, next renewal in %s (err %v)", d, err)
	}
}

func TestRunCertRenewal_LeavesFreshCertAlone(t *testing.T) {
	var posts atomic.Int64
	inst := newHTTPTestInstance(t, renewServer(t, &posts, 0, 24*time.Hour))
	issueCert(t, inst, time.Hour, 24*time.Hour)

	runRenewalLoop(t, inst, 300*time.Millisecond)

	if n := posts.Load(); n != 0 {
		t.Errorf("renewed a fresh cert %d times", n)
	}
}

// TestRunCertRenewal_ClockFarAhead: every cert the server issues already
// looks due. Renewing on each poll would restart the tunnel every minute.
func TestRunCertRenewal_ClockFarAhead(t *testing.T) {
	for name, age := range map[string]time.Duration{"looks old": 20 * time.Hour, "looks expired": 48 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int64
			inst := newHTTPTestInstance(t, renewServer(t, &posts, age, 24*time.Hour))
			issueCert(t, inst, 30*time.Hour, 24*time.Hour)

			runRenewalLoop(t, inst, 400*time.Millisecond)

			if n := posts.Load(); n != 1 {
				t.Errorf("renewal POSTs = %d, want 1 (then wait %s)", n, minRenewalInterval)
			}
		})
	}
}

// TestRunCertRenewal_MissingCertKeepsPolling: an unreadable cert must not end
// the loop.
func TestRunCertRenewal_MissingCertKeepsPolling(t *testing.T) {
	var posts atomic.Int64
	inst := newHTTPTestInstance(t, renewServer(t, &posts, 0, 24*time.Hour))
	go func() {
		time.Sleep(150 * time.Millisecond)
		nb := time.Now().Add(-20 * time.Hour)
		writeNodeCert(t, inst.dir(), nb, nb.Add(24*time.Hour))
	}()

	runRenewalLoop(t, inst, 600*time.Millisecond)

	if n := posts.Load(); n != 1 {
		t.Errorf("renewal POSTs = %d, want 1 once the cert is readable again", n)
	}
}
