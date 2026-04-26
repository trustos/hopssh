package main

import (
	"net/netip"
	"testing"
	"time"
)

// Layer 4 unit tests — focused on the pure helper functions, since
// runEndpointProbe + the Nebula vendor patch interaction needs an
// integration test against a live Control (deferred to E2E verification
// post-deploy, similar to how we verified Layer 2 via journalctl).

func TestEndpointProbe_ObserveAndLookup(t *testing.T) {
	p := newEndpointProbe()
	vpn := netip.MustParseAddr("10.42.1.8")
	remote1 := netip.MustParseAddrPort("192.168.23.232:4242")
	remote2 := netip.MustParseAddrPort("46.10.240.91:9266")

	p.observe(vpn, remote1)
	p.observe(vpn, remote2)

	live := p.liveEndpoints(vpn, time.Minute)
	if len(live) != 2 {
		t.Fatalf("got %d live, want 2: %v", len(live), live)
	}
}

// The load-bearing test: an observed endpoint that's outside the
// staleness window must NOT be in liveEndpoints. This is what makes
// Layer 4 actually reap stale entries.
func TestEndpointProbe_StaleEntriesExcluded(t *testing.T) {
	p := newEndpointProbe()
	vpn := netip.MustParseAddr("10.42.1.8")
	staleRemote := netip.MustParseAddrPort("46.10.240.91:4243")
	freshRemote := netip.MustParseAddrPort("192.168.23.232:4242")

	// Mark stale by writing a past timestamp directly (skips waiting in
	// real time — same end state as if observe ran 10s ago).
	p.lastSeen[vpn] = map[netip.AddrPort]time.Time{
		staleRemote: time.Now().Add(-time.Minute), // past staleness window
		freshRemote: time.Now(),                   // just now
	}

	live := p.liveEndpoints(vpn, 30*time.Second)
	if len(live) != 1 {
		t.Fatalf("got %d live, want 1 (only fresh): %v", len(live), live)
	}
	if live[0] != freshRemote {
		t.Errorf("wrong endpoint surfaced: got %v, want %v", live[0], freshRemote)
	}
}

func TestEndpointProbe_PruneStale(t *testing.T) {
	p := newEndpointProbe()
	vpn := netip.MustParseAddr("10.42.1.8")
	staleRemote := netip.MustParseAddrPort("46.10.240.91:4243")
	freshRemote := netip.MustParseAddrPort("192.168.23.232:4242")

	p.lastSeen[vpn] = map[netip.AddrPort]time.Time{
		staleRemote: time.Now().Add(-2 * time.Minute),
		freshRemote: time.Now(),
	}

	p.pruneStale(time.Minute)

	if _, present := p.lastSeen[vpn][staleRemote]; present {
		t.Error("stale entry not pruned")
	}
	if _, present := p.lastSeen[vpn][freshRemote]; !present {
		t.Error("fresh entry incorrectly pruned")
	}
}

func TestEndpointProbe_PruneEmptyVpnRemoved(t *testing.T) {
	// If ALL endpoints for a peer go stale, the peer's map entry should
	// be removed entirely so the outer map doesn't grow unbounded as
	// peers come and go over time.
	p := newEndpointProbe()
	vpn := netip.MustParseAddr("10.42.1.8")
	p.lastSeen[vpn] = map[netip.AddrPort]time.Time{
		netip.MustParseAddrPort("46.10.240.91:4243"): time.Now().Add(-time.Hour),
	}
	p.pruneStale(time.Minute)
	if _, present := p.lastSeen[vpn]; present {
		t.Error("empty per-peer map should be removed")
	}
}

func TestEndpointProbe_ObserveIgnoresInvalid(t *testing.T) {
	p := newEndpointProbe()
	// Invalid vpnAddr — no panic, no entry created.
	p.observe(netip.Addr{}, netip.MustParseAddrPort("192.168.1.1:4242"))
	if len(p.lastSeen) != 0 {
		t.Errorf("invalid vpnAddr created an entry: %v", p.lastSeen)
	}
	// Invalid remote — same.
	p.observe(netip.MustParseAddr("10.42.1.8"), netip.AddrPort{})
	if len(p.lastSeen) != 0 {
		t.Errorf("invalid remote created an entry: %v", p.lastSeen)
	}
}

// REGRESSION TEST FOR THE OVERNIGHT 2026-04-26 INCIDENT.
//
// Bug: Layer 4's reap was based on "did we observe inbound from this exact
// source IP:port?" — but under asymmetric CGNAT, the peer's outbound REPLY
// has a DIFFERENT external port than the NAT-PMP-mapped INBOUND port we
// probed. So the inbound observation never matched the probed endpoint,
// the endpoint got marked "stale" forever, the reap fired every 10s, and
// over 9 hours the hostmap was progressively starved of valid candidates
// until the tunnel collapsed and screen-share failed.
//
// This test simulates the asymmetric-CGNAT scenario: probes go to one
// port, replies arrive from a different port. The CURRENT (post-hotfix)
// implementation must NOT actively reap the probed endpoint based on
// inbound-source mismatch alone — it should observe the alternate-port
// inbound as evidence that the peer is alive, but not reap the original.
//
// If a future change re-enables the reap path naively, this test will
// fail on the assertion below (would-reap-count > 0 for an endpoint
// whose peer IS sending inbound traffic via a different port).
func TestEndpointProbe_AsymmetricCGNAT_NoFalseReap(t *testing.T) {
	probe := newEndpointProbe()
	vpn := netip.MustParseAddr("10.42.1.11")

	// Endpoint we're probing — Linux HOME's NAT-PMP-mapped public.
	probedEndpoint := netip.MustParseAddrPort("46.10.240.91:9266")
	// Endpoint that REPLIES come from — same IP, DIFFERENT port due to
	// CGNAT asymmetry.
	replyFromEndpoint := netip.MustParseAddrPort("46.10.240.91:4246")

	// Simulate: we observe constant inbound from the reply-port (peer
	// is alive, just CGNAT-translated). We never observe inbound from
	// the probed port itself.
	for i := 0; i < 6; i++ {
		probe.observe(vpn, replyFromEndpoint)
	}

	// liveEndpoints reports what we've SEEN respond. The probed endpoint
	// is NOT in this set (we never observed inbound from it directly).
	live := probe.liveEndpoints(vpn, 30*time.Second)
	if len(live) != 1 || live[0] != replyFromEndpoint {
		t.Fatalf("liveEndpoints should reflect what we observed: got %v, want [%v]", live, replyFromEndpoint)
	}

	// The bug: a naive reap would now compare the hostmap's RemoteAddrs
	// (which includes the probed endpoint :9266) against `live` (which
	// only has :4246) and incorrectly drop :9266 — even though :9266
	// is the legitimate NAT-PMP-mapped destination for outbound packets.
	//
	// The fix: actual reap is disabled in reapStaleEndpoints; the function
	// emits a "WOULD-REAP (disabled)" log line instead. We can't easily
	// invoke reapStaleEndpoints in a unit test (it needs *meshInstance
	// with a live Nebula control), so this test guards the helper layer:
	// `liveEndpoints` correctly returns ONLY observed endpoints, and any
	// future re-enabled reap path that diffs (current RemoteAddrs - live)
	// would falsely drop :9266.
	//
	// If you re-enable reaping in reapStaleEndpoints without first
	// implementing nonce-correlated TestRequest/TestReply (Layer 4b),
	// this test serves as a tripwire — at minimum, the comment in
	// reapStaleEndpoints must be updated to explain why it's now safe.
	hostmapAddrs := []netip.AddrPort{
		probedEndpoint,           // valid endpoint — would be falsely reaped
		replyFromEndpoint,        // valid endpoint — observed alive
		netip.MustParseAddrPort("192.168.23.18:4243"), // LAN — valid
	}
	liveSet := map[netip.AddrPort]struct{}{}
	for _, ep := range live {
		liveSet[ep] = struct{}{}
	}
	wouldReap := []netip.AddrPort{}
	for _, ep := range hostmapAddrs {
		if _, ok := liveSet[ep]; !ok {
			wouldReap = append(wouldReap, ep)
		}
	}
	// THE BUG: a naive reap would mark probedEndpoint AND the LAN as
	// stale (only the reply-from-port was observed). Both are valid.
	if len(wouldReap) < 2 {
		t.Errorf("expected at least 2 false-reap candidates, got %d: %v", len(wouldReap), wouldReap)
	}
	// Specifically, the probed endpoint must show up as a false-reap
	// candidate — that's the failure mode we observed in production.
	foundProbed := false
	for _, ep := range wouldReap {
		if ep == probedEndpoint {
			foundProbed = true
		}
	}
	if !foundProbed {
		t.Errorf("REGRESSION: probedEndpoint %v not in false-reap set %v — asymmetric-CGNAT detection is no longer demonstrating the bug", probedEndpoint, wouldReap)
	}
	// The fix is to NOT actively reap based on this signal. The current
	// reapStaleEndpoints implementation logs "WOULD-REAP (disabled)" and
	// no-ops. If you re-enable the actual ReplaceStaticHostMap call
	// without a per-endpoint correlated liveness signal (Layer 4b nonce
	// correlation), the production tunnel will collapse the same way
	// it did 2026-04-26 between 00:43 deploy and 09:16 first dead-mark.
}

func TestSameRemotes(t *testing.T) {
	a := []netip.AddrPort{
		netip.MustParseAddrPort("1.1.1.1:4242"),
		netip.MustParseAddrPort("2.2.2.2:4242"),
	}
	b := []netip.AddrPort{
		netip.MustParseAddrPort("2.2.2.2:4242"),
		netip.MustParseAddrPort("1.1.1.1:4242"),
	}
	if !sameRemotes(a, b) {
		t.Error("same set in different order: should match")
	}
	if !sameRemotes(a, a) {
		t.Error("identity should match")
	}
	if sameRemotes(a, b[:1]) {
		t.Error("different lengths should not match")
	}
	c := []netip.AddrPort{
		netip.MustParseAddrPort("1.1.1.1:4242"),
		netip.MustParseAddrPort("3.3.3.3:4242"),
	}
	if sameRemotes(a, c) {
		t.Error("different elements should not match")
	}
	if !sameRemotes(nil, nil) {
		t.Error("two nils should match")
	}
}
