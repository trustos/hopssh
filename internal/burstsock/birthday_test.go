package burstsock

import (
	"math"
	"testing"
)

func TestRandomPorts_NoDuplicates(t *testing.T) {
	out := RandomPorts(256)
	if len(out) != 256 {
		t.Fatalf("got %d ports, want 256", len(out))
	}
	seen := map[uint16]bool{}
	for _, p := range out {
		if p < dynamicPortMin || p > dynamicPortMax {
			t.Errorf("port %d outside dynamic range", p)
		}
		if seen[p] {
			t.Errorf("duplicate port %d", p)
		}
		seen[p] = true
	}
}

func TestRandomPorts_TruncatesAtRangeSize(t *testing.T) {
	rangeSize := dynamicPortMax - dynamicPortMin + 1
	out := RandomPorts(rangeSize + 100) // ask for more than possible
	if len(out) != rangeSize {
		t.Errorf("got %d, want %d", len(out), rangeSize)
	}
}

func TestHotPortsFirst_PrependsHot(t *testing.T) {
	hot := []uint16{50000, 50001, 50002}
	out := HotPortsFirst(hot, 10)
	if len(out) != 10 {
		t.Fatalf("got %d, want 10", len(out))
	}
	for i, want := range hot {
		if out[i] != want {
			t.Errorf("[%d] got %d want %d", i, out[i], want)
		}
	}
	// Remaining 7 must be unique random ports not overlapping hot list.
	for i := 3; i < 10; i++ {
		for _, h := range hot {
			if out[i] == h {
				t.Errorf("[%d] = %d duplicates hot port", i, out[i])
			}
		}
	}
}

func TestHotPortsFirst_DedupesHot(t *testing.T) {
	hot := []uint16{50000, 50000, 50001, 0, 50001} // dupes + zero
	out := HotPortsFirst(hot, 5)
	// First two should be 50000 and 50001 (zero filtered, dupes removed).
	if out[0] != 50000 || out[1] != 50001 {
		t.Errorf("dedup failed: got %v", out[:2])
	}
}

func TestHotPortsFirst_TruncatesIfHotExceedsTotal(t *testing.T) {
	hot := []uint16{50000, 50001, 50002, 50003, 50004}
	out := HotPortsFirst(hot, 3)
	if len(out) != 3 {
		t.Errorf("len = %d, want 3", len(out))
	}
	for i := 0; i < 3; i++ {
		if out[i] != hot[i] {
			t.Errorf("[%d] = %d", i, out[i])
		}
	}
}

func TestSuccessProbability_KnownValues(t *testing.T) {
	// Sanity: at N=K=256, expect ~63 % per cycle.
	p := SuccessProbability(256, 256)
	if p < 0.60 || p > 0.66 {
		t.Errorf("256x256 probability = %.2f, want ~0.63", p)
	}
	// Tiny case: 1x1 → 1/65536 ≈ 1.5e-5.
	q := SuccessProbability(1, 1)
	if math.Abs(q-1.0/65536.0) > 1e-9 {
		t.Errorf("1x1 probability = %g, want %g", q, 1.0/65536.0)
	}
	// Zero source/target → 0.
	if SuccessProbability(0, 100) != 0 || SuccessProbability(100, 0) != 0 {
		t.Errorf("zero size should give 0 probability")
	}
}

func TestSuccessProbability_MonotonicallyIncreases(t *testing.T) {
	prev := SuccessProbability(1, 1)
	for n := 2; n <= 512; n *= 2 {
		cur := SuccessProbability(n, n)
		if cur <= prev {
			t.Errorf("probability not monotonic: at N=%d got %g (prev=%g)", n, cur, prev)
		}
		prev = cur
	}
}
