package burstsock

// Port-candidate generation for birthday-paradox NAT traversal.
//
// Two strategies, picked based on what we know about the peer's NAT:
//
//   Random:   uniform random across the dynamic-port range (49152-65535).
//             Lowest assumption — works against truly random-port CGNAT
//             but pays the full sqrt(65536) cost.
//
//   HotPorts: probe a list of "recently observed" ports first, falling
//             back to random for the rest. Usable when the lighthouse
//             has seen the peer's source port from prior flows; many
//             carriers reuse from a small pool, so the hot-port list
//             often hits in the first few probes.

import (
	"math/rand/v2"
)

const (
	dynamicPortMin = 49152
	dynamicPortMax = 65535
)

// RandomPorts returns count unique random ports in the dynamic range.
// Uses math/rand/v2 — security is not a concern for port probes.
// If count exceeds the dynamic-port range size, repeats are truncated
// (caller never gets duplicates).
func RandomPorts(count int) []uint16 {
	rangeSize := dynamicPortMax - dynamicPortMin + 1
	if count > rangeSize {
		count = rangeSize
	}
	seen := make(map[uint16]bool, count)
	out := make([]uint16, 0, count)
	for len(out) < count {
		p := uint16(dynamicPortMin + rand.IntN(rangeSize))
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// HotPortsFirst prepends the given hot-port list (deduplicated) to a
// random-port set, capped at total entries. Use this when you have
// a list of ports recently observed for the peer — those go first
// because they're the most likely to still be allocated.
func HotPortsFirst(hot []uint16, total int) []uint16 {
	if total <= 0 {
		return nil
	}
	seen := make(map[uint16]bool, total)
	out := make([]uint16, 0, total)
	for _, p := range hot {
		if p == 0 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) == total {
			return out
		}
	}
	for _, p := range RandomPorts(total - len(out)) {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// SuccessProbability returns the approximate probability of at least
// one collision per cycle when N source ports each probe K target
// ports against a uniform-random NAT mapping. Approximation valid
// when NK << 65536^2; good enough for sizing decisions.
//
// Math: 1 - (1 - 1/65536)^(N*K) ≈ NK/65536 for small NK; the more
// exact form is 1 - (1 - 1/65536)^(NK).
func SuccessProbability(sourcePorts, targetPorts int) float64 {
	const universe = 65536.0
	if sourcePorts <= 0 || targetPorts <= 0 {
		return 0
	}
	pAttemptMisses := 1.0 - 1.0/universe
	allMiss := 1.0
	for i := 0; i < sourcePorts*targetPorts; i++ {
		allMiss *= pAttemptMisses
		if allMiss < 1e-12 {
			break // numerical floor; further multiplications change nothing
		}
	}
	return 1 - allMiss
}
