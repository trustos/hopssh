package main

import (
	"reflect"
	"sort"
	"testing"
)

// peerInfoEntry + updatePeerInfoCache + enrichPeersWithInfo together
// implement the peer-name + DNS plumbing surfaced via /local/peers
// (commit 1892725). The contract is: server pushes peer-name + DNS map
// in the heartbeat response, agent caches it keyed by mesh IP, and the
// local API enriches each PeerDetail with the cached entry. These
// tests pin the cache lifecycle (replace + evict) and the enrichment
// behavior (cache-hit and cache-miss).

func TestUpdatePeerInfoCache_StoresFreshEntries(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	updatePeerInfoCache(inst, map[string]peerInfoEntry{
		"10.42.1.5": {Name: "mac-mini", DnsHostname: "mac-mini.home"},
		"10.42.1.6": {Name: "macbook-pro", DnsHostname: "macbook-pro.home", CustomDnsNames: []string{"laptop.home"}},
	})

	v, ok := inst.peerInfoCache.Load("10.42.1.5")
	if !ok {
		t.Fatalf("expected 10.42.1.5 to be in cache")
	}
	got := v.(peerInfoEntry)
	if got.Name != "mac-mini" || got.DnsHostname != "mac-mini.home" {
		t.Errorf("10.42.1.5: got %+v", got)
	}

	v, ok = inst.peerInfoCache.Load("10.42.1.6")
	if !ok {
		t.Fatalf("expected 10.42.1.6 to be in cache")
	}
	got = v.(peerInfoEntry)
	if !reflect.DeepEqual(got.CustomDnsNames, []string{"laptop.home"}) {
		t.Errorf("10.42.1.6 CustomDnsNames: got %v", got.CustomDnsNames)
	}
}

func TestUpdatePeerInfoCache_EvictsRemovedPeers(t *testing.T) {
	// Tripwire: when the server stops reporting a peer (because the
	// node was removed from the network), the agent's cache must drop
	// the stale entry. Otherwise /local/peers continues to surface a
	// hostname for a peer that no longer exists.
	inst := newMeshInstance(&Enrollment{Name: "home"})
	updatePeerInfoCache(inst, map[string]peerInfoEntry{
		"10.42.1.5": {Name: "mac-mini"},
		"10.42.1.6": {Name: "macbook-pro"},
	})
	// Second update: laptop removed from network.
	updatePeerInfoCache(inst, map[string]peerInfoEntry{
		"10.42.1.5": {Name: "mac-mini"},
	})

	if _, ok := inst.peerInfoCache.Load("10.42.1.6"); ok {
		t.Errorf("10.42.1.6 should have been evicted after second update")
	}
	if _, ok := inst.peerInfoCache.Load("10.42.1.5"); !ok {
		t.Errorf("10.42.1.5 should still be present after second update")
	}
}

func TestUpdatePeerInfoCache_NilInstanceNoop(t *testing.T) {
	// Defensive: race between heartbeat response and instance shutdown
	// (close()-then-leave) can null out inst. Must not panic.
	updatePeerInfoCache(nil, map[string]peerInfoEntry{
		"10.42.1.5": {Name: "x"},
	})
}

func TestEnrichPeersWithInfo_AddsCachedHostnameAndDNS(t *testing.T) {
	inst := newMeshInstance(&Enrollment{Name: "home"})
	updatePeerInfoCache(inst, map[string]peerInfoEntry{
		"10.42.1.5": {Name: "mac-mini", DnsHostname: "mac-mini.home", CustomDnsNames: []string{"jellyfin.home"}},
	})
	out := enrichPeersWithInfo(
		[]PeerDetail{{VpnAddr: "10.42.1.5", Direct: true}},
		inst,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 enriched peer, got %d", len(out))
	}
	if out[0].Name != "mac-mini" {
		t.Errorf("Name: got %q", out[0].Name)
	}
	if out[0].DnsHostname != "mac-mini.home" {
		t.Errorf("DnsHostname: got %q", out[0].DnsHostname)
	}
	if !reflect.DeepEqual(out[0].CustomDnsNames, []string{"jellyfin.home"}) {
		t.Errorf("CustomDnsNames: got %v", out[0].CustomDnsNames)
	}
	if !out[0].Direct {
		t.Errorf("Direct flag must round-trip from PeerDetail")
	}
}

func TestEnrichPeersWithInfo_CacheMissLeavesEmptyFields(t *testing.T) {
	// Critical for fresh enrollments before the first heartbeat
	// response arrives — the peers list must render with IP-only
	// rather than panicking or omitting the row.
	inst := newMeshInstance(&Enrollment{Name: "home"})
	out := enrichPeersWithInfo(
		[]PeerDetail{{VpnAddr: "10.42.1.5", Direct: true}},
		inst,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 peer even on cache miss, got %d", len(out))
	}
	if out[0].Name != "" || out[0].DnsHostname != "" || out[0].CustomDnsNames != nil {
		t.Errorf("cache-miss peer should have empty info fields, got %+v", out[0])
	}
	// PeerDetail itself must round-trip unchanged.
	if out[0].VpnAddr != "10.42.1.5" {
		t.Errorf("VpnAddr lost: got %q", out[0].VpnAddr)
	}
}

func TestEnrichPeersWithInfo_PreservesOrder(t *testing.T) {
	// /local/peers callers expect peers in the order returned by
	// collectPeerState. Enrichment must not reorder.
	inst := newMeshInstance(&Enrollment{Name: "home"})
	updatePeerInfoCache(inst, map[string]peerInfoEntry{
		"10.42.1.5": {Name: "mac-mini"},
		"10.42.1.6": {Name: "macbook"},
	})
	out := enrichPeersWithInfo(
		[]PeerDetail{
			{VpnAddr: "10.42.1.6"},
			{VpnAddr: "10.42.1.5"},
		},
		inst,
	)
	got := []string{out[0].VpnAddr, out[1].VpnAddr}
	want := []string{"10.42.1.6", "10.42.1.5"}
	if !reflect.DeepEqual(got, want) {
		// Sort both ways to confirm it's an order issue not a content issue.
		sortedGot := append([]string{}, got...)
		sort.Strings(sortedGot)
		t.Errorf("enrichment changed order: got %v, want %v (content match: %v)",
			got, want, reflect.DeepEqual(sortedGot, []string{"10.42.1.5", "10.42.1.6"}))
	}
}
