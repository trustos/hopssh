package api

import (
	"net/netip"
	"testing"
	"time"
)

// Phase G — verify the self-endpoint cache mechanism on RenewHandler:
// store, expire-by-TTL, overwrite, delete. The full HTTP-handler path
// is exercised by integration testing, but the cache primitives need
// fast unit coverage so a regression in TTL math doesn't silently
// serve stale endpoints to peers.
//
// Layer 1 (v0.10.27) adds per-endpoint expiry via NAT-PMP lease
// lifetimes. Tests now construct endpointHint values directly with
// the desired expiry, instead of relying on a single updatedAt + TTL.

// hintFromAddrs is a test helper that wraps a list of address strings
// into endpointHint values, all with the same expiry.
func hintFromAddrs(addrs []string, expiresAt time.Time) *selfEndpointHint {
	out := make([]endpointHint, len(addrs))
	for i, a := range addrs {
		out[i] = endpointHint{addr: a, expiresAt: expiresAt}
	}
	return &selfEndpointHint{endpoints: out}
}

func TestSelfEndpointCache_StoreAndRead(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-1", hintFromAddrs(
		[]string{"203.0.113.10:4242", "192.168.0.5:4242"},
		time.Now().Add(15*time.Minute),
	))

	v, ok := h.selfEndpointCache.Load("node-1")
	if !ok {
		t.Fatal("entry not found after store")
	}
	hint, ok := v.(*selfEndpointHint)
	if !ok {
		t.Fatalf("wrong type: %T", v)
	}
	if len(hint.endpoints) != 2 {
		t.Errorf("got %d endpoints, want 2", len(hint.endpoints))
	}
}

func TestSelfEndpointCache_Overwrite(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-1", hintFromAddrs(
		[]string{"1.1.1.1:4242"},
		time.Now().Add(time.Hour),
	))
	h.selfEndpointCache.Store("node-1", hintFromAddrs(
		[]string{"2.2.2.2:4242"},
		time.Now().Add(time.Hour),
	))
	v, _ := h.selfEndpointCache.Load("node-1")
	hint := v.(*selfEndpointHint)
	if hint.endpoints[0].addr != "2.2.2.2:4242" {
		t.Errorf("overwrite did not take effect: %v", hint.endpoints)
	}
}

func TestSelfEndpointCache_Delete(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-1", hintFromAddrs(
		[]string{"1.1.1.1:4242"},
		time.Now().Add(time.Hour),
	))
	h.selfEndpointCache.Delete("node-1")
	if _, ok := h.selfEndpointCache.Load("node-1"); ok {
		t.Error("entry still present after Delete")
	}
}

// TTL fallback constant must be > heartbeat interval (5 min default) so a
// single missed heartbeat doesn't immediately drop a peer's endpoint
// hints from peerEndpoints responses to OTHERS. 15 min is the chosen
// floor (3× heartbeat interval). Per-endpoint NAT-PMP lifetimes can be
// SHORTER than this — they apply on top of the fallback as the actual
// expiry, never longer than the fallback.
func TestSelfEndpointHintTTL_AbovesHeartbeatInterval(t *testing.T) {
	if selfEndpointHintTTL < 5*time.Minute {
		t.Errorf("selfEndpointHintTTL=%v, must be at least one heartbeat interval (5m)", selfEndpointHintTTL)
	}
}

// --- Phase G regression tests for mergePeerEndpoints ---

// REGRESSION TEST FOR THE OBSERVED CELLULAR-IDLE BUG
// (2026-04-25 incident: MBP cellular ↔ mini home went silent after
// ~16 min idle until manual restart):
//
// Without Phase G, peer B's heartbeat response had NO endpoint info for
// peer A → mini's hostmap stayed stale → mesh went silent. With Phase G:
// peer A's HTTPS heartbeat includes selfEndpoints, server caches them,
// and B's heartbeat response gets them via the merge.
func TestMergePeerEndpoints_CellularIdleScenario(t *testing.T) {
	var lighthouse []netip.AddrPort
	now := time.Now()
	hint := hintFromAddrs(
		[]string{"203.0.113.10:4242", "192.168.0.3:4242"},
		now.Add(15*time.Minute),
	)

	got := mergePeerEndpoints(lighthouse, hint, now)
	if len(got) != 2 {
		t.Fatalf("got %d endpoints, want 2 — Phase G fix REGRESSED, cellular-idle bug returns: %v", len(got), got)
	}
	want := map[string]bool{"203.0.113.10:4242": true, "192.168.0.3:4242": true}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected endpoint %q", s)
		}
	}
}

func TestMergePeerEndpoints_LighthouseOnly(t *testing.T) {
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	got := mergePeerEndpoints(lighthouse, nil, time.Now())
	if len(got) != 1 || got[0] != "203.0.113.10:4242" {
		t.Errorf("got %v, want [203.0.113.10:4242]", got)
	}
}

func TestMergePeerEndpoints_BothSourcesUnion(t *testing.T) {
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	now := time.Now()
	hint := hintFromAddrs([]string{"192.168.0.3:4242"}, now.Add(15*time.Minute))
	got := mergePeerEndpoints(lighthouse, hint, now)
	if len(got) != 2 {
		t.Errorf("union should have 2 entries, got %d: %v", len(got), got)
	}
	if got[0] != "203.0.113.10:4242" {
		t.Errorf("lighthouse entry not first: %v", got)
	}
}

func TestMergePeerEndpoints_DedupOverlap(t *testing.T) {
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	now := time.Now()
	hint := hintFromAddrs(
		[]string{"203.0.113.10:4242", "192.168.0.3:4242"},
		now.Add(15*time.Minute),
	)
	got := mergePeerEndpoints(lighthouse, hint, now)
	if len(got) != 2 {
		t.Errorf("dedup failed, got %d entries: %v", len(got), got)
	}
}

func TestMergePeerEndpoints_ExpiredHintIgnored(t *testing.T) {
	// Layer 1: hint is past its per-endpoint expiry — must NOT be merged.
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	now := time.Now()
	hint := hintFromAddrs([]string{"1.2.3.4:4242"}, now.Add(-time.Minute))
	got := mergePeerEndpoints(lighthouse, hint, now)
	if len(got) != 1 || got[0] != "203.0.113.10:4242" {
		t.Errorf("expired hint leaked through: %v", got)
	}
}

func TestMergePeerEndpoints_InvalidHintStringSkipped(t *testing.T) {
	now := time.Now()
	hint := hintFromAddrs(
		[]string{"not-an-addrport", "203.0.113.10:4242", ""},
		now.Add(15*time.Minute),
	)
	got := mergePeerEndpoints(nil, hint, now)
	if len(got) != 1 || got[0] != "203.0.113.10:4242" {
		t.Errorf("invalid entries leaked or valid one dropped: %v", got)
	}
}

func TestMergePeerEndpoints_BothEmpty(t *testing.T) {
	now := time.Now()
	got := mergePeerEndpoints(nil, nil, now)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	got = mergePeerEndpoints(nil, &selfEndpointHint{}, now)
	if len(got) != 0 {
		t.Errorf("empty hint returned %v, want empty", got)
	}
}

// Layer 1 regression: per-endpoint expiry. Some endpoints in the hint
// are still valid; others have expired. Only the valid ones should be
// returned.
//
// This is the load-bearing scenario: an agent reported [public-NAT-PMP,
// LAN] with NAT-PMP lifetime 60s and LAN no-lifetime (15-min fallback).
// 2 minutes later, the NAT-PMP entry has expired (router may have
// reassigned the port to another host) but the LAN entry is still good.
// mergePeerEndpoints must drop the public address but keep the LAN one,
// preventing peers from sending packets to the router's reassigned host
// and causing cross-host packet rejection.
func TestMergePeerEndpoints_PerEndpointExpiry(t *testing.T) {
	now := time.Now()
	hint := &selfEndpointHint{
		endpoints: []endpointHint{
			// NAT-PMP-mapped public address with a 60s lease — expired 1 min ago.
			{addr: "46.10.240.91:4243", expiresAt: now.Add(-1 * time.Minute)},
			// LAN address with the 15-min fallback — still good.
			{addr: "192.168.23.232:4242", expiresAt: now.Add(13 * time.Minute)},
		},
	}
	got := mergePeerEndpoints(nil, hint, now)
	if len(got) != 1 || got[0] != "192.168.23.232:4242" {
		t.Errorf("expired NAT-PMP address leaked or LAN address dropped: %v", got)
	}
}

// Layer 1 regression: ALL endpoints expired → empty merge result.
// Ensures we don't accidentally keep returning stale data when a peer
// has gone silent past its lease lifetimes.
func TestMergePeerEndpoints_AllExpired(t *testing.T) {
	now := time.Now()
	hint := hintFromAddrs(
		[]string{"46.10.240.91:4243", "192.168.23.232:4242"},
		now.Add(-time.Second),
	)
	got := mergePeerEndpoints(nil, hint, now)
	if len(got) != 0 {
		t.Errorf("got %v, want empty (all expired)", got)
	}
}

// Layer 1 regression: zero-value expiresAt means "no expiry" (defensive
// — lets us construct test hints without computing now+TTL every time,
// and tolerates any future migration that loses the timestamp). NEVER
// hits in production because the cache write always sets expiresAt to
// at least the fallback.
func TestMergePeerEndpoints_ZeroExpiryTreatedAsValid(t *testing.T) {
	hint := &selfEndpointHint{
		endpoints: []endpointHint{
			{addr: "192.168.23.232:4242"}, // expiresAt zero-value
		},
	}
	got := mergePeerEndpoints(nil, hint, time.Now())
	if len(got) != 1 || got[0] != "192.168.23.232:4242" {
		t.Errorf("zero-expiry endpoint dropped: %v", got)
	}
}
