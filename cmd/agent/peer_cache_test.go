package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPeerCache_LoadMissingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	got, err := loadPeerCache(inst)
	if err != nil {
		t.Fatalf("loadPeerCache on missing file: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}

func TestPeerCache_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	in := map[string][]string{
		"10.42.1.7": {"203.0.113.10:4242", "192.168.0.3:4242"},
		"10.42.2.3": {"203.0.113.10:4243"},
	}
	if err := savePeerCache(inst, in); err != nil {
		t.Fatalf("savePeerCache: %v", err)
	}
	got, err := loadPeerCache(inst)
	if err != nil || got == nil {
		t.Fatalf("loadPeerCache: %v / %+v", err, got)
	}
	if got.SchemaVersion != peerCacheSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, peerCacheSchemaVersion)
	}
	if len(got.Peers) != 2 {
		t.Fatalf("Peers count = %d, want 2", len(got.Peers))
	}
	wantP1 := []string{"192.168.0.3:4242", "203.0.113.10:4242"} // sorted
	if !stringSliceEqual(got.Peers["10.42.1.7"].Endpoints, wantP1) {
		t.Errorf("10.42.1.7 endpoints = %v, want %v", got.Peers["10.42.1.7"].Endpoints, wantP1)
	}
	if got.Peers["10.42.1.7"].SeenAt == 0 {
		t.Error("SeenAt was not set")
	}
}

func TestPeerCache_SaveSkipsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	in := map[string][]string{"10.42.1.7": {"203.0.113.10:4242"}}
	if err := savePeerCache(inst, in); err != nil {
		t.Fatal(err)
	}
	stat1, err := os.Stat(peerCachePath(inst))
	if err != nil {
		t.Fatal(err)
	}

	// Second save with the same content but the SeenAt bump alone would
	// still rewrite. To keep this test deterministic we first re-write
	// the file with a SeenAt slightly in the past, then save the same
	// endpoints — the merge should detect the SeenAt update and rewrite.
	// Instead: assert that bytewise-equal save (no endpoint changes,
	// same SeenAt second) is a no-op when the second call lands inside
	// the same 1-second wall-clock window.
	time.Sleep(10 * time.Millisecond)
	if err := savePeerCache(inst, in); err != nil {
		t.Fatal(err)
	}
	stat2, err := os.Stat(peerCachePath(inst))
	if err != nil {
		t.Fatal(err)
	}
	// SeenAt updates rewrite the file once per second, so back-to-back
	// saves within the same wall-clock second must NOT rewrite.
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file rewritten despite no semantic change (mod1=%s, mod2=%s)",
			stat1.ModTime(), stat2.ModTime())
	}
}

func TestPeerCache_SaveRewritesOnEndpointChange(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	_ = savePeerCache(inst, map[string][]string{"10.42.1.7": {"203.0.113.10:4242"}})
	if err := savePeerCache(inst, map[string][]string{"10.42.1.7": {"203.0.113.10:4242", "1.2.3.4:5678"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := loadPeerCache(inst)
	if got == nil || len(got.Peers["10.42.1.7"].Endpoints) != 2 {
		t.Errorf("endpoint addition not persisted: %+v", got)
	}
}

func TestPeerCache_MergeAcrossSnapshots(t *testing.T) {
	// Heartbeat N reports peer A; heartbeat N+1 reports peer B only.
	// Cache must retain BOTH (so a restart can attempt direct to either).
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	_ = savePeerCache(inst, map[string][]string{"10.42.1.7": {"203.0.113.10:4242"}})
	_ = savePeerCache(inst, map[string][]string{"10.42.2.3": {"203.0.113.10:4243"}})

	got, _ := loadPeerCache(inst)
	if got == nil {
		t.Fatal("nil cache after second save")
	}
	if len(got.Peers) != 2 {
		t.Errorf("expected 2 peers retained, got %d (%+v)", len(got.Peers), got.Peers)
	}
}

func TestPeerCache_TTLFiltersStaleEntries(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	stale := time.Now().Add(-2 * peerCacheTTL).Unix()
	fresh := time.Now().Unix()
	c := peerCache{
		SchemaVersion: peerCacheSchemaVersion,
		UpdatedAt:     fresh,
		Peers: map[string]peerCacheEntry{
			"10.42.1.7": {Endpoints: []string{"203.0.113.10:4242"}, SeenAt: stale},
			"10.42.2.3": {Endpoints: []string{"203.0.113.10:4243"}, SeenAt: fresh},
		},
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(peerCachePath(inst), data, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := loadPeerCache(inst)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("nil cache despite one fresh entry")
	}
	if _, ok := got.Peers["10.42.1.7"]; ok {
		t.Errorf("stale entry not filtered: %+v", got.Peers["10.42.1.7"])
	}
	if _, ok := got.Peers["10.42.2.3"]; !ok {
		t.Errorf("fresh entry filtered out: %+v", got.Peers)
	}
}

func TestPeerCache_RejectsUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	bogus := []byte(`{"schemaVersion": 999, "updatedAt": 0, "peers": {}}`)
	if err := os.WriteFile(peerCachePath(inst), bogus, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := loadPeerCacheRaw(inst)
	if err == nil {
		t.Error("expected schema-version error, got nil")
	}
}

func TestPeerCache_DropsInvalidVPNIP(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	in := map[string][]string{
		"not-an-ip": {"203.0.113.10:4242"},
		"10.42.1.7": {"203.0.113.10:4242"},
	}
	if err := savePeerCache(inst, in); err != nil {
		t.Fatal(err)
	}
	got, _ := loadPeerCache(inst)
	if got == nil {
		t.Fatal("nil cache")
	}
	if _, ok := got.Peers["not-an-ip"]; ok {
		t.Error("invalid VPN IP persisted")
	}
	if _, ok := got.Peers["10.42.1.7"]; !ok {
		t.Error("valid VPN IP dropped")
	}
}

func TestPeerCache_NormalizeEndpointsDropsInvalidAndDedupes(t *testing.T) {
	in := []string{
		"203.0.113.10:4242",
		"",
		"203.0.113.10:4242",
		"not-an-addrport",
		"1.2.3.4:5678",
	}
	out := normalizeEndpoints(in)
	want := []string{"1.2.3.4:5678", "203.0.113.10:4242"}
	if !stringSliceEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestPeerCache_FileLocation(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	if err := savePeerCache(inst, map[string][]string{"10.42.1.7": {"203.0.113.10:4242"}}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, peerCacheFile)
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s: %v", want, err)
	}
}

func TestPeerCache_InjectWithoutControlIsZero(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	// Prime the cache.
	if err := savePeerCache(inst, map[string][]string{"10.42.1.7": {"203.0.113.10:4242"}}); err != nil {
		t.Fatal(err)
	}
	// inst.svc is nil → control() returns nil → injection is a no-op.
	if got := injectCachedPeerEndpoints(inst); got != 0 {
		t.Errorf("expected 0 injections without Nebula control, got %d", got)
	}
}

func TestPeerCache_InjectMissingCacheIsZero(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	if got := injectCachedPeerEndpoints(inst); got != 0 {
		t.Errorf("expected 0 injections without cache file, got %d", got)
	}
}

// TestWarmEndpointPath_DeliversProbe verifies that warmEndpointPath
// actually transmits a UDP packet to each endpoint. We bind a local
// UDP listener and confirm the byte arrives. Cross-platform — same
// behavior on Darwin, Linux, Windows.
func TestWarmEndpointPath_DeliversProbe(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	ap, err := netip.ParseAddrPort(pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("parse local addr: %v", err)
	}

	warmEndpointPath([]netip.AddrPort{ap})

	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("did not receive warm probe: %v", err)
	}
	if n == 0 {
		t.Errorf("warm probe was empty, want >=1 byte")
	}
}

func TestWarmEndpointPath_NoEndpointsIsNoop(t *testing.T) {
	// Just verify it doesn't panic / hang.
	warmEndpointPath(nil)
	warmEndpointPath([]netip.AddrPort{})
}
