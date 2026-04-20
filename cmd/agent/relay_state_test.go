package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRelayState_LoadMissingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	got, err := loadRelayState(inst)
	if err != nil {
		t.Fatalf("loadRelayState on missing file: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}

func TestRelayState_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	if err := saveRelayState(inst, true, []string{"10.42.1.7", "10.42.1.11"}); err != nil {
		t.Fatalf("saveRelayState: %v", err)
	}
	got, err := loadRelayState(inst)
	if err != nil || got == nil {
		t.Fatalf("loadRelayState: %v / %+v", err, got)
	}
	if !got.AmRelay {
		t.Errorf("AmRelay = false, want true")
	}
	want := []string{"10.42.1.11", "10.42.1.7"} // sorted
	if !stringSliceEqual(got.Relays, want) {
		t.Errorf("Relays = %v, want %v", got.Relays, want)
	}
}

func TestRelayState_SaveSkipsWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	if err := saveRelayState(inst, false, []string{"10.42.1.7"}); err != nil {
		t.Fatal(err)
	}
	stat1, err := os.Stat(relayStatePath(inst))
	if err != nil {
		t.Fatal(err)
	}

	// Same content, even with order shuffled and dupes — should be no-op.
	if err := saveRelayState(inst, false, []string{"10.42.1.7", "10.42.1.7"}); err != nil {
		t.Fatal(err)
	}
	stat2, err := os.Stat(relayStatePath(inst))
	if err != nil {
		t.Fatal(err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file rewritten despite no semantic change")
	}
}

func TestRelayState_SaveRewritesOnChange(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	_ = saveRelayState(inst, false, []string{"10.42.1.7"})
	if err := saveRelayState(inst, true, []string{"10.42.1.7"}); err != nil {
		t.Fatal(err)
	}
	got, _ := loadRelayState(inst)
	if !got.AmRelay {
		t.Errorf("AmRelay change not persisted")
	}
}

func TestRelayState_NormalizeDedupesAndSorts(t *testing.T) {
	in := []string{"10.42.1.7", "", "10.42.1.7", "10.42.1.11"}
	out := normalizeRelays(in)
	want := []string{"10.42.1.11", "10.42.1.7"}
	if !stringSliceEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

func TestMergeRelayList_PreservesLighthousePlusPeers(t *testing.T) {
	existing := []any{"10.42.1.1"} // lighthouse
	peerRelays := []string{"10.42.1.7", "10.42.1.11"}

	got := mergeRelayList(existing, peerRelays)
	want := []any{"10.42.1.1", "10.42.1.7", "10.42.1.11"}

	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %v want %v", i, got[i], want[i])
		}
	}
}

func TestMergeRelayList_DedupesPeerAlreadyInExisting(t *testing.T) {
	existing := []any{"10.42.1.1", "10.42.1.7"}
	peerRelays := []string{"10.42.1.7"} // already present

	got := mergeRelayList(existing, peerRelays)
	if len(got) != 2 {
		t.Errorf("expected dedup: got %d items, want 2", len(got))
	}
}

func TestMergeRelayList_HandlesStringSliceVariant(t *testing.T) {
	// yaml.Unmarshal can produce []any OR []string depending on
	// the input. We accept both.
	existing := []string{"10.42.1.1"}
	got := mergeRelayList(existing, []string{"10.42.1.7"})
	if len(got) != 2 {
		t.Errorf("got %d, want 2", len(got))
	}
}

func TestRelayListEqual(t *testing.T) {
	a := []any{"10.42.1.1", "10.42.1.7"}
	b := []any{"10.42.1.1", "10.42.1.7"}
	c := []any{"10.42.1.1"}
	d := []string{"10.42.1.1", "10.42.1.7"}

	if !relayListEqual(a, b) {
		t.Error("a != b")
	}
	if relayListEqual(a, c) {
		t.Error("a == c (different lengths)")
	}
	if !relayListEqual(a, d) {
		t.Error("a != d (mixed types)")
	}
}

// TestRelayState_FileLocation confirms the cache lives next to nebula.yaml
// in the per-enrollment subdir — important so multi-network agents don't
// confuse home and work relay state.
func TestRelayState_FileLocation(t *testing.T) {
	dir := t.TempDir()
	inst := newMeshInstance(&Enrollment{Name: "home"})
	inst.customDir = dir

	if err := saveRelayState(inst, true, nil); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, relayStateFile)
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s: %v", want, err)
	}
}
