package main

import (
	"net"
	"testing"
	"time"
)

// Tests for the cert-reload port-bind fix (v0.10.33).
//
// Background: pre-v0.10.33 reloadNebula tried to start a NEW Nebula svc
// on the same UDP port the OLD svc was still holding (try-then-swap),
// which always failed with "address already in use" because Nebula's
// listen socket can't share-port. The retry-with-backoff goroutine then
// short-circuited via a `inst.svc != nil` check that fired falsely on
// the leftover OLD svc, never actually retrying. Result: agent kept
// using the OLD (eventually-expired) cert; peers rejected every
// handshake. Verified in production 2026-04-27.
//
// The fix: close the old svc FIRST, wait for the kernel to release the
// UDP port, then start the new svc. These tests exercise the
// `waitForUDPPortFree` primitive that the fix relies on.

// TestWaitForUDPPortFree_BoundFast verifies the fast path: a port that
// is already free returns from waitForUDPPortFree immediately (well
// under the deadline).
func TestWaitForUDPPortFree_BoundFast(t *testing.T) {
	// Bind+release to pick a port we know is currently free.
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	port := sock.LocalAddr().(*net.UDPAddr).Port
	sock.Close()

	start := time.Now()
	waitForUDPPortFree(port, 2*time.Second)
	elapsed := time.Since(start)

	// Should return well under the deadline — first probe succeeds.
	if elapsed > 200*time.Millisecond {
		t.Errorf("waitForUDPPortFree on free port took %v, want < 200ms", elapsed)
	}
}

// TestWaitForUDPPortFree_RespectsDeadline verifies the negative path:
// when the port stays held for the full deadline, waitForUDPPortFree
// returns at the deadline (NOT forever-loops).
func TestWaitForUDPPortFree_RespectsDeadline(t *testing.T) {
	held, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer held.Close()
	port := held.LocalAddr().(*net.UDPAddr).Port

	deadline := 150 * time.Millisecond
	start := time.Now()
	waitForUDPPortFree(port, deadline)
	elapsed := time.Since(start)

	// Should respect the deadline within a small slack budget.
	if elapsed < deadline {
		t.Errorf("waitForUDPPortFree returned in %v before deadline %v", elapsed, deadline)
	}
	if elapsed > deadline+200*time.Millisecond {
		t.Errorf("waitForUDPPortFree elapsed %v exceeds deadline %v + 200ms slack", elapsed, deadline)
	}
}

// TestWaitForUDPPortFree_DetectsRelease is the regression test for the
// actual production scenario: the port is held when reloadNebula calls
// in, then released mid-wait, and waitForUDPPortFree must detect the
// release and return promptly so the new bind can proceed.
func TestWaitForUDPPortFree_DetectsRelease(t *testing.T) {
	held, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	port := held.LocalAddr().(*net.UDPAddr).Port

	releaseAfter := 80 * time.Millisecond
	go func() {
		time.Sleep(releaseAfter)
		held.Close()
	}()

	start := time.Now()
	waitForUDPPortFree(port, 2*time.Second)
	elapsed := time.Since(start)

	// Should detect the release within one polling tick (20ms) of when
	// it happened, well before the deadline.
	if elapsed < releaseAfter {
		t.Errorf("waitForUDPPortFree returned in %v before release at %v", elapsed, releaseAfter)
	}
	if elapsed > releaseAfter+150*time.Millisecond {
		t.Errorf("waitForUDPPortFree took %v to detect release scheduled for %v (poll interval miss?)",
			elapsed, releaseAfter)
	}

	// Sanity: now that the port is free, a fresh bind should succeed.
	verify, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		t.Fatalf("post-release bind: %v", err)
	}
	verify.Close()
}

// TestWaitForUDPPortFreeFn_Indirected ensures the package-level var
// indirection used by reloadNebula points at the real function. Tests
// can rebind this var to substitute behavior; production code must
// resolve to the real implementation.
func TestWaitForUDPPortFreeFn_Indirected(t *testing.T) {
	if waitForUDPPortFreeFn == nil {
		t.Fatal("waitForUDPPortFreeFn is nil")
	}
	// Smoke: invoking via the indirection on a known-free port should
	// be a fast no-op.
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	port := sock.LocalAddr().(*net.UDPAddr).Port
	sock.Close()

	start := time.Now()
	waitForUDPPortFreeFn(port, 2*time.Second)
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("waitForUDPPortFreeFn slow on a free port")
	}
}
