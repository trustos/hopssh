package client

// Tripwire: heartbeat + renewCert MUST disable HTTP keep-alives.
//
// Why this matters (root cause confirmed 2026-04-26 incident, validated by
// reading sendHeartbeat / renewCert source):
//
// Each call constructs a fresh &http.Client{} with no explicit Transport.
// Without Transport set, Go resolves to http.DefaultTransport — a process-
// wide singleton with a per-host idle-connection pool. After a physical
// network change (sleep/wake, hotspot toggle, DHCP renew to a new gateway,
// UTM bridge transition between subnets), TCP sockets in that pool are
// kernel-level dead but Go still tries to reuse them. Each reuse hangs
// for the request Timeout (10-30s), failing the heartbeat. Until the pool
// drains naturally (or the process restarts), the dashboard shows the
// node offline for minutes.
//
// Fix: every client constructor on the control-plane path sets
// Transport: &http.Transport{DisableKeepAlives: true}. Each request opens
// a fresh TCP+TLS connection; nothing pools; network changes can never
// poison anything. Cost: ~80-150ms TLS handshake per heartbeat (heartbeat
// every 30s = ~0.3-0.5% overhead). Eliminates an entire class of
// "post-sleep dashboard offline" reports.
//
// These tests scan the relevant source files for the literal client
// constructor pattern and assert DisableKeepAlives appears in the same
// short window. If a future refactor strips the option, the test fires
// and tells the reviewer exactly which call site regressed.

import (
	"os"
	"strings"
	"testing"
)

// hotPathClients catalogues every &http.Client{} constructor on the
// post-enroll control-plane path that's invoked repeatedly during the
// agent's lifetime. Each MUST disable keep-alives.
//
// Excluded by design:
//   - cmd/agent/clock_check.go — already sets DisableKeepAlives (verified
//     by reading the file; correct from inception)
//   - cmd/agent/client.go::join — one-shot CLI command, process exits
//     immediately after; no pool survival problem possible
type hotPathClient struct {
	file string
	// label appears in the failure message so the maintainer knows which
	// constructor regressed.
	label string
}

var hotPathHTTPClients = []hotPathClient{
	{"renew.go", "sendHeartbeat (POST /api/heartbeat, every 30s)"},
	{"renew.go", "renewCert (POST /api/renew, every ~12h)"},
	// main.go's self-update probe client moved to cmd/agent in Phase NN
	// (it's a CLI-side concern, not part of the client substrate). The
	// cmd/agent test file owns that half of the tripwire.
}

func TestHTTPClients_DisableKeepAlives(t *testing.T) {
	files := map[string]string{}
	for _, c := range hotPathHTTPClients {
		if _, ok := files[c.file]; ok {
			continue
		}
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		files[c.file] = string(b)
	}

	for _, file := range []string{"renew.go"} {
		src := files[file]
		// Count constructors and DisableKeepAlives uses; they must match.
		ctors := strings.Count(src, "&http.Client{")
		nokeep := strings.Count(src, "DisableKeepAlives: true")
		if ctors == 0 {
			t.Errorf("%s: expected at least one &http.Client{} constructor; found 0 — has the file been refactored?", file)
			continue
		}
		if nokeep < ctors {
			t.Errorf("%s: %d &http.Client{} constructors but only %d DisableKeepAlives:true — at least one client is back to the default-pool behavior that caused the 2026-04-26 post-sleep heartbeat outage", file, ctors, nokeep)
		}
	}
}

// TestClockCheck_DisableKeepAlives confirms clock_check.go still sets the
// flag — important because EnsureClockSane runs on every agent boot and
// is the first HTTP call the agent makes. A regression here would re-
// introduce the "post-wake first probe hangs on stale conn" failure mode.
func TestClockCheck_DisableKeepAlives(t *testing.T) {
	src, err := os.ReadFile("clock_check.go")
	if err != nil {
		t.Fatalf("read clock_check.go: %v", err)
	}
	if !strings.Contains(string(src), "DisableKeepAlives") {
		t.Error("clock_check.go: DisableKeepAlives missing — the boot-time clock probe is back to the default pool, which on agent restart after sleep WILL pick a stale conn and time out")
	}
}
