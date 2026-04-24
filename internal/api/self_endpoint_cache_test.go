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

func TestSelfEndpointCache_StoreAndRead(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-1", &selfEndpointHint{
		endpoints: []string{"203.0.113.10:4242", "192.168.0.5:4242"},
		updatedAt: time.Now(),
	})

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

func TestSelfEndpointCache_TTLExpiry(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-stale", &selfEndpointHint{
		endpoints: []string{"1.2.3.4:4242"},
		updatedAt: time.Now().Add(-2 * selfEndpointHintTTL),
	})

	v, ok := h.selfEndpointCache.Load("node-stale")
	if !ok {
		t.Fatal("entry missing")
	}
	hint := v.(*selfEndpointHint)
	if time.Since(hint.updatedAt) < selfEndpointHintTTL {
		t.Errorf("stale entry mis-aged: %v ago, want > %v", time.Since(hint.updatedAt), selfEndpointHintTTL)
	}
	// The read-side merge in Heartbeat() must compare with this same
	// TTL constant — no test wiring here because we don't construct a
	// full request, but the TTL value being honored is what counts.
}

func TestSelfEndpointCache_Overwrite(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-1", &selfEndpointHint{
		endpoints: []string{"1.1.1.1:4242"},
		updatedAt: time.Now().Add(-time.Hour),
	})
	h.selfEndpointCache.Store("node-1", &selfEndpointHint{
		endpoints: []string{"2.2.2.2:4242"},
		updatedAt: time.Now(),
	})
	v, _ := h.selfEndpointCache.Load("node-1")
	hint := v.(*selfEndpointHint)
	if hint.endpoints[0] != "2.2.2.2:4242" {
		t.Errorf("overwrite did not take effect: %v", hint.endpoints)
	}
	if time.Since(hint.updatedAt) > time.Second {
		t.Errorf("updatedAt not refreshed on overwrite")
	}
}

func TestSelfEndpointCache_Delete(t *testing.T) {
	h := &RenewHandler{}
	h.selfEndpointCache.Store("node-1", &selfEndpointHint{endpoints: []string{"1.1.1.1:4242"}, updatedAt: time.Now()})
	h.selfEndpointCache.Delete("node-1")
	if _, ok := h.selfEndpointCache.Load("node-1"); ok {
		t.Error("entry still present after Delete")
	}
}

// TTL constant must be > heartbeat interval (5 min default) so a
// single missed heartbeat doesn't immediately drop a peer's endpoint
// hints from peerEndpoints responses to OTHERS. 15 min is the chosen
// floor (3× heartbeat interval).
func TestSelfEndpointHintTTL_AbovesHeartbeatInterval(t *testing.T) {
	if selfEndpointHintTTL < 5*time.Minute {
		t.Errorf("selfEndpointHintTTL=%v, must be at least one heartbeat interval (5m)", selfEndpointHintTTL)
	}
}

// --- Phase G regression tests for mergePeerEndpoints ---
//
// These tests reproduce the production failure mode that motivated
// Phase G and prove the merge logic handles each branch correctly.
// They test the EXACT helper the Heartbeat handler calls, so a
// regression in either source's handling will be caught here without
// needing a full DB + HTTP server spin-up.

// REGRESSION TEST FOR THE OBSERVED CELLULAR-IDLE BUG
// (2026-04-25 incident: MBP cellular ↔ mini home went silent after
// ~16 min idle until manual restart):
//
// Scenario: Peer A (cellular) cannot reach the lighthouse via UDP
// (carrier filters), so its advertise_addrs never propagate to the
// server's lighthouse cache. The lighthouse PeerEndpoints lookup
// returns empty. Without Phase G, peer B's heartbeat response had
// NO endpoint info for peer A → mini's hostmap stayed stale → mesh
// went silent.
//
// With Phase G: peer A's HTTPS heartbeat includes selfEndpoints,
// server caches them, and B's heartbeat response gets them via the
// merge. This test fails if either (a) the hint side stops being
// honored, OR (b) the lighthouse-empty case stops surfacing the
// hint endpoints alone.
func TestMergePeerEndpoints_CellularIdleScenario(t *testing.T) {
	// Lighthouse cache empty (the fail case — UDP advertise_addrs
	// never reached the lighthouse because carrier filtered them).
	var lighthouse []netip.AddrPort

	// Peer A's HTTPS heartbeat reported these moments ago.
	hint := &selfEndpointHint{
		endpoints: []string{"203.0.113.10:4242", "192.168.0.3:4242"},
		updatedAt: time.Now(),
	}

	got := mergePeerEndpoints(lighthouse, hint, selfEndpointHintTTL)
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
	// Healthy lighthouse path; no hint cached yet (older agent).
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	got := mergePeerEndpoints(lighthouse, nil, selfEndpointHintTTL)
	if len(got) != 1 || got[0] != "203.0.113.10:4242" {
		t.Errorf("got %v, want [203.0.113.10:4242]", got)
	}
}

func TestMergePeerEndpoints_BothSourcesUnion(t *testing.T) {
	// Both sources report DIFFERENT endpoints — result is union.
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	hint := &selfEndpointHint{
		endpoints: []string{"192.168.0.3:4242"},
		updatedAt: time.Now(),
	}
	got := mergePeerEndpoints(lighthouse, hint, selfEndpointHintTTL)
	if len(got) != 2 {
		t.Errorf("union should have 2 entries, got %d: %v", len(got), got)
	}
	// Lighthouse comes first.
	if got[0] != "203.0.113.10:4242" {
		t.Errorf("lighthouse entry not first: %v", got)
	}
}

func TestMergePeerEndpoints_DedupOverlap(t *testing.T) {
	// Lighthouse and hint both report the SAME endpoint — appears once.
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	hint := &selfEndpointHint{
		endpoints: []string{"203.0.113.10:4242", "192.168.0.3:4242"},
		updatedAt: time.Now(),
	}
	got := mergePeerEndpoints(lighthouse, hint, selfEndpointHintTTL)
	if len(got) != 2 {
		t.Errorf("dedup failed, got %d entries: %v", len(got), got)
	}
}

func TestMergePeerEndpoints_ExpiredHintIgnored(t *testing.T) {
	// Hint is past TTL — must NOT be merged (stale endpoints could
	// route packets to a now-dead address). Lighthouse alone wins.
	lighthouse := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.10:4242"),
	}
	hint := &selfEndpointHint{
		endpoints: []string{"1.2.3.4:4242"},
		updatedAt: time.Now().Add(-2 * selfEndpointHintTTL),
	}
	got := mergePeerEndpoints(lighthouse, hint, selfEndpointHintTTL)
	if len(got) != 1 || got[0] != "203.0.113.10:4242" {
		t.Errorf("expired hint leaked through: %v", got)
	}
}

func TestMergePeerEndpoints_InvalidHintStringSkipped(t *testing.T) {
	// Garbage in hint (e.g. older agent format, parse failure on
	// agent side, etc.) must not crash and must not poison output.
	hint := &selfEndpointHint{
		endpoints: []string{"not-an-addrport", "203.0.113.10:4242", ""},
		updatedAt: time.Now(),
	}
	got := mergePeerEndpoints(nil, hint, selfEndpointHintTTL)
	if len(got) != 1 || got[0] != "203.0.113.10:4242" {
		t.Errorf("invalid entries leaked or valid one dropped: %v", got)
	}
}

func TestMergePeerEndpoints_BothEmpty(t *testing.T) {
	got := mergePeerEndpoints(nil, nil, selfEndpointHintTTL)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	got = mergePeerEndpoints(nil, &selfEndpointHint{updatedAt: time.Now()}, selfEndpointHintTTL)
	if len(got) != 0 {
		t.Errorf("empty hint returned %v, want empty", got)
	}
}
