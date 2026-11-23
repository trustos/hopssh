package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Phase HH — incident 2026-11-18: /api/renew + /api/heartbeat shared the
// anonymous 10/min/IP publicRL with login/register, and production answered
// renewals with 429.

// TestRouter_AgentEndpointsNotOnPublicLimiter is the source tripwire.
func TestRouter_AgentEndpointsNotOnPublicLimiter(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		for _, path := range []string{`"/api/renew"`, `"/api/heartbeat"`} {
			if !strings.Contains(line, path) {
				continue
			}
			found[path] = true
			if strings.Contains(line, "publicRL") {
				t.Errorf("REGRESSION: %s is on publicRL (10/min/IP) — agents behind one NAT starve each other's cert renewals:\n%s", path, strings.TrimSpace(line))
			}
			if !strings.Contains(line, "agentRL.Limit") {
				t.Errorf("%s must use the dedicated agentRL limiter:\n%s", path, strings.TrimSpace(line))
			}
		}
	}
	for _, path := range []string{`"/api/renew"`, `"/api/heartbeat"`} {
		if !found[path] {
			t.Errorf("route %s not found in router.go — re-check this tripwire", path)
		}
	}
}

// TestRouter_AgentTrafficFromOneIPNotThrottled exercises the real router:
// a household behind one NAT (several devices × several networks, plus
// renewals) must never be 429'd, while login stays strictly limited.
func TestRouter_AgentTrafficFromOneIPNotThrottled(t *testing.T) {
	// Handlers with no stores: requests without a bearer token / with a
	// malformed body return 401/400 before any store is touched, which is
	// all we need — the limiter runs first.
	r := NewRouter(nil, nil, &AuthHandler{}, nil, nil, nil, nil, nil, nil,
		&RenewHandler{}, nil, nil, nil, nil, nil, nil, nil, nil)

	send := func(path, body string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.RemoteAddr = "203.0.113.7:40000" // one shared public IP
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	// ~10 minutes of a 3-device × 3-network household at a 60s heartbeat,
	// plus a renewal burst after a shared wake-from-sleep.
	for i := 0; i < 90; i++ {
		if code := send("/api/heartbeat", "{}"); code == http.StatusTooManyRequests {
			t.Fatalf("heartbeat %d from one IP was rate limited", i+1)
		}
	}
	for i := 0; i < 30; i++ {
		if code := send("/api/renew", "{}"); code == http.StatusTooManyRequests {
			t.Fatalf("renewal %d from one IP was rate limited — certs would expire", i+1)
		}
	}

	// Anonymous auth endpoints keep the strict anti-abuse limit.
	saw429 := false
	for i := 0; i < 40; i++ {
		if send("/api/auth/login", "not-json") == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Error("login must stay on the strict publicRL")
	}
}
