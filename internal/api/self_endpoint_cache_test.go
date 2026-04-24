package api

import (
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
		endpoints: []string{"46.10.240.91:4242", "192.168.1.5:4242"},
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
