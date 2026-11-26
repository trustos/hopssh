package client

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slackhq/nebula/cert"
)

// TestE2E_RenewalFailsPastExpiry: a network is up and every renewal is refused,
// as when the server is down for a day. The certificate runs out, the peer drops
// the tunnel, and then the server answers again. The agent has to come back by
// itself, and the peer has to accept it again, which it does only if the running
// Nebula uses the new certificate and not just the file on disk.
func TestE2E_RenewalFailsPastExpiry(t *testing.T) {
	m := newE2EMesh(t)
	// A minute in the agent, both: the wait between tries and, after 12 of them, the
	// time until the loop looks again.
	origWait, origPoll := renewRetryFirstWait, renewalPollInterval
	renewRetryFirstWait, renewalPollInterval = 300*time.Millisecond, time.Second
	t.Cleanup(func() { renewRetryFirstWait, renewalPollInterval = origWait, origPoll })

	portA, portB := freeUDPPort(t), freeUDPPort(t)
	ipA, ipB := e2eSubnet+"6", e2eSubnet+"7"
	// Nebula takes a's first certificate for another minute: it ended an hour ago
	// less a minute, and patch 25 allows an hour.
	const left = 60 * time.Second
	age := 24*time.Hour - time.Minute + cert.ClockSkewTolerance - left
	refusedUntil := time.Now().Add(left)
	m.enroll("a", ipA, portA, age, map[string][]string{ipB: {localUDP(portB)}})
	m.enroll("b", ipB, portB, time.Hour, map[string][]string{ipA: {localUDP(portA)}})

	var refused, allowed atomic.Int64
	var serverUp atomic.Bool
	answer := func(nodeID string) int {
		if nodeID != "node-a" || serverUp.Load() {
			allowed.Add(1)
			return http.StatusOK
		}
		refused.Add(1)
		return http.StatusServiceUnavailable
	}
	m.onRenew.Store(&answer)

	if err := m.c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	reachable := func() bool {
		return keepaliveProbe(m.c.instances.get("a"), fmt.Sprintf("%s:%d", ipB, agentAPIPort)) == nil
	}
	// The same from b, whose own certificate is fine all the time: what the
	// owner of a healthy machine sees of one whose certificate ran out.
	fromB := func() bool {
		return keepaliveProbe(m.c.instances.get("b"), fmt.Sprintf("%s:%d", ipA, agentAPIPort)) == nil
	}
	within(t, 25*time.Second, "a to reach b while its certificate is still taken", reachable)
	within(t, 10*time.Second, "b to reach a while a's certificate is still taken", fromB)

	time.Sleep(time.Until(refusedUntil))
	within(t, 40*time.Second, "b to drop a: its certificate has run out", func() bool { return !reachable() })
	if fromB() {
		t.Error("b still reaches a, whose certificate has run out")
	}
	if !m.live("a") {
		t.Fatal("a's network was taken down; nothing is left that could renew")
	}
	if n := refused.Load(); n < 3 {
		t.Errorf("a asked for a new certificate %d times while it was refused, want it to keep asking", n)
	}
	if m.certValid("a") {
		t.Fatal("a has a valid certificate although every renewal was refused")
	}

	// The network is down until a renewal works, so the wait between tries must
	// not grow: 300 ms here, a minute in production (it grew to 30 minutes).
	// 50 tries in 15 s; a wait that doubles from 300 ms gets to 5. CI's Mac with
	// the race detector did 4 in 5 s, so the window is long and the bar low.
	before := refused.Load()
	time.Sleep(15 * time.Second)
	if n := refused.Load() - before; n < 8 {
		t.Errorf("a asked %d times in 15 s without a valid certificate, want one try every 300 ms", n)
	}

	serverUp.Store(true)
	back := time.Now()
	// 300 ms between tries now that the certificate has run out, plus the handshake.
	// b asks first. (a may have started the new tunnel by then: its own
	// keepalive is not held back.)
	within(t, 10*time.Second, "b to reach a again after the server is back", fromB)
	within(t, 10*time.Second, "a to reach b again after the server is back", reachable)
	t.Logf("back %s after the server answered again, %d renewals refused before", time.Since(back).Round(100*time.Millisecond), refused.Load())
	if !m.certValid("a") {
		t.Error("a is reachable but the certificate on disk is not valid")
	}
}
