package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"
	"github.com/trustos/hopssh/internal/nebulacfg"
)

// dateHeaderHandler returns a stub that sets the Date header to a fixed
// timestamp, so the test controls "server time" deterministically.
func dateHeaderHandler(serverTime time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", serverTime.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}
}

func TestProbeClockSkew_Synchronized(t *testing.T) {
	// Server reports "now" — local skew should be near zero.
	srv := httptest.NewServer(dateHeaderHandler(time.Now()))
	defer srv.Close()

	skew, err := probeClockSkew(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if skew > 5*time.Second || skew < -5*time.Second {
		t.Errorf("expected skew within 5s of zero, got %v", skew)
	}
}

func TestProbeClockSkew_LocalAhead(t *testing.T) {
	// Server reports "1 hour ago" — local clock is 1h AHEAD.
	pastTime := time.Now().Add(-1 * time.Hour)
	srv := httptest.NewServer(dateHeaderHandler(pastTime))
	defer srv.Close()

	skew, err := probeClockSkew(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	// HTTP Date precision is 1 second, so allow ±5s tolerance on the hour.
	if skew < 55*time.Minute || skew > 65*time.Minute {
		t.Errorf("expected ~+1h skew, got %v", skew)
	}
}

func TestProbeClockSkew_LocalBehind(t *testing.T) {
	// Server reports "1 hour from now" — local clock is 1h BEHIND.
	// This is the exact UTM-suspended-VM failure pattern.
	futureTime := time.Now().Add(1 * time.Hour)
	srv := httptest.NewServer(dateHeaderHandler(futureTime))
	defer srv.Close()

	skew, err := probeClockSkew(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if skew > -55*time.Minute || skew < -65*time.Minute {
		t.Errorf("expected ~-1h skew, got %v", skew)
	}
}

func TestProbeClockSkew_MissingDateHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip the auto-Date header that net/http inserts by default.
		w.Header()["Date"] = nil
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := probeClockSkew(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error on missing Date header, got nil")
	}
}

func TestProbeClockSkew_BadEndpoint(t *testing.T) {
	// Closed listener address — connection refused.
	_, err := probeClockSkew(context.Background(), "http://127.0.0.1:1/")
	if err == nil {
		t.Fatal("expected error on unreachable endpoint, got nil")
	}
}

func TestEnsureClockSane_EmptyEndpoint(t *testing.T) {
	// Defensive: empty endpoint must not panic; returns 0.
	if got := EnsureClockSane(context.Background(), ""); got != 0 {
		t.Errorf("EnsureClockSane(\"\") = %v, want 0", got)
	}
}

func TestEnsureClockSane_HealthyClock(t *testing.T) {
	// Within skewWarn (60s) — should silently return the small skew.
	srv := httptest.NewServer(dateHeaderHandler(time.Now()))
	defer srv.Close()

	got := EnsureClockSane(context.Background(), srv.URL)
	if got > 5*time.Second || got < -5*time.Second {
		t.Errorf("expected ~0 skew on healthy clock, got %v", got)
	}
}

// TestClockSkewTolerance_WiredFromConfig is a tripwire: if anyone removes
// the init() block, refactors the cert package, or sets nebulacfg.ClockSkewTolerance
// to zero by accident, the agent loses the cert-validity grace window and
// every UTM-suspended-VM resume will re-break with "certificate is expired".
// Patch 25 (vendor) is meaningless without this wire-up.
func TestClockSkewTolerance_WiredFromConfig(t *testing.T) {
	if cert.ClockSkewTolerance != nebulacfg.ClockSkewTolerance {
		t.Errorf("cert.ClockSkewTolerance = %v, nebulacfg.ClockSkewTolerance = %v — init() wiring broken",
			cert.ClockSkewTolerance, nebulacfg.ClockSkewTolerance)
	}
	if cert.ClockSkewTolerance < 30*time.Second {
		t.Errorf("cert.ClockSkewTolerance = %v is too small to be useful (config bug)", cert.ClockSkewTolerance)
	}
	if cert.ClockSkewTolerance > 24*time.Hour {
		t.Errorf("cert.ClockSkewTolerance = %v is dangerously large (>= cert lifetime)", cert.ClockSkewTolerance)
	}
}

func TestEnsureClockSane_BadEndpoint_DoesNotBlock(t *testing.T) {
	// If the endpoint is unreachable, EnsureClockSane must not block boot.
	// 10s probe timeout + a defensive 12s test bound proves it returns
	// promptly even when the endpoint is dead.
	done := make(chan struct{})
	go func() {
		EnsureClockSane(context.Background(), "http://127.0.0.1:1/")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("EnsureClockSane did not return within 12s on dead endpoint")
	}
}
