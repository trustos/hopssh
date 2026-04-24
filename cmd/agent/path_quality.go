package main

// Per-peer path-quality observability. A periodic goroutine measures
// TCP-connect RTT to each direct peer's mesh API listener (:41820)
// and feeds the sample into a per-peer EWMA. Snapshot RTT lands in
// the per-peer heartbeat (PeerDetail.RTTms) so the dashboard can
// surface it; degradations trip a single log line for diagnostics.
//
// Phase B-lite: observability only. No auto-swap of CurrentRemote
// (Phase B's full proactive picker remains deferred). Risk surface
// is one TCP connect per direct peer per probeInterval.
//
// Cross-platform: pure stdlib net.Dial — works identically on
// Darwin/Linux/Windows. Same probe pattern is already used by
// warmPeersFromHeartbeat() so no new firewall surface.

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"github.com/slackhq/nebula"
)

const (
	// probeInterval is how often we re-measure each direct peer.
	// 10 s matches the existing connection-manager probe cadence
	// and keeps total network cost trivial (one TCP-SYN per peer
	// every 10 s).
	probeInterval = 10 * time.Second

	// probeTimeout caps a single dial. Anything beyond this is
	// recorded as a "down" sample (counts toward degradation, no
	// EWMA update with the timeout value).
	probeTimeout = 2 * time.Second

	// ewmaAlpha is the EWMA smoothing factor. 0.3 leans recent (so
	// real degradation surfaces within a few samples) without going
	// jumpy on single-sample outliers from WiFi MAC contention.
	ewmaAlpha = 0.3

	// degradationThresholdMs: a sample this many ms above the EWMA
	// is "concerning". degradationConsecutive consecutive concerning
	// samples trips the warning log line.
	degradationThresholdMs = 50
	degradationConsecutive = 3
)

// pathQualitySample is the persisted state per peer. ewmaMs is 0
// until the first successful sample lands; sampleCount distinguishes
// "no data yet" from "0 ms is the actual measurement".
type pathQualitySample struct {
	ewmaMs           float64 // exponentially-weighted moving average of TCP-connect RTT
	lastSampleMs     float64 // most recent sample (whether ABOVE or below ewma)
	sampleCount      int     // total successful samples seen for this peer
	consecutiveBad   int     // consecutive samples that exceeded ewma + threshold
	consecutiveDown  int     // consecutive timeouts (separate from "bad" RTT)
	lastDegradeLogAt time.Time
}

// pathQuality is one tracker per meshInstance. Concurrent reads from
// peerstate.go (heartbeat snapshot) coexist with the prober goroutine
// via the mutex.
type pathQuality struct {
	mu      sync.Mutex
	samples map[string]*pathQualitySample // keyed by peer VpnAddr (e.g. "10.42.1.7")
}

func newPathQuality() *pathQuality {
	return &pathQuality{samples: map[string]*pathQualitySample{}}
}

// snapshot returns a copy of the EWMA and sample count for vpnAddr,
// or (0, 0) if no data has been gathered yet. Safe to call from any
// goroutine.
func (p *pathQuality) snapshot(vpnAddr string) (rttMs int, sampleCount int) {
	if p == nil {
		return 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.samples[vpnAddr]
	if !ok || s.sampleCount == 0 {
		return 0, 0
	}
	rounded := int(s.ewmaMs + 0.5)
	if rounded < 0 {
		rounded = 0
	}
	return rounded, s.sampleCount
}

// recordSample folds a successful RTT measurement into the EWMA and
// returns true if degradation just tripped (caller logs once per
// trip, suppressed by lastDegradeLogAt).
func (p *pathQuality) recordSample(vpnAddr string, rttMs float64) (degraded bool, baselineMs float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.samples[vpnAddr]
	if !ok {
		s = &pathQualitySample{}
		p.samples[vpnAddr] = s
	}

	// Prime the EWMA on the very first sample so we don't compare
	// against zero (which would make every first measurement look
	// like an infinite degradation).
	if s.sampleCount == 0 {
		s.ewmaMs = rttMs
	} else {
		s.ewmaMs = ewmaAlpha*rttMs + (1-ewmaAlpha)*s.ewmaMs
	}
	s.lastSampleMs = rttMs
	s.sampleCount++
	s.consecutiveDown = 0

	// Compare against the EWMA BEFORE this sample was folded in
	// (else degradation would dampen its own detection signal).
	// To do that cheaply: re-derive prev = (ewma - alpha*sample)/(1-alpha).
	// For Phase B-lite we use the simpler "current ewma + threshold"
	// — this only mis-fires if a single huge sample yanks the EWMA
	// up enough to mask itself, which requires a >2× spike.
	if rttMs > s.ewmaMs+degradationThresholdMs {
		s.consecutiveBad++
	} else {
		s.consecutiveBad = 0
	}

	if s.consecutiveBad >= degradationConsecutive {
		// Trip; suppress further log lines for 30 s to avoid spam.
		now := time.Now()
		if now.Sub(s.lastDegradeLogAt) > 30*time.Second {
			s.lastDegradeLogAt = now
			return true, s.ewmaMs
		}
	}
	return false, s.ewmaMs
}

// recordTimeout folds an unreachable probe. Doesn't touch EWMA (so
// it doesn't poison the average if recovery happens), but counts
// against consecutiveDown which can also trip the warning.
func (p *pathQuality) recordTimeout(vpnAddr string) (downStreak int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.samples[vpnAddr]
	if !ok {
		s = &pathQualitySample{}
		p.samples[vpnAddr] = s
	}
	s.consecutiveDown++
	s.consecutiveBad = 0
	return s.consecutiveDown
}

// runPathQuality is the per-instance goroutine that drives sampling.
// Exits when ctx is cancelled (parentCtx is the meshInstance's
// lifecycle ctx — closes on agent shutdown or instance close).
func runPathQuality(ctx context.Context, inst *meshInstance) {
	if inst == nil {
		return
	}
	if inst.pathQuality == nil {
		inst.pathQuality = newPathQuality()
	}
	pq := inst.pathQuality

	t := time.NewTicker(probeInterval)
	defer t.Stop()

	// Stagger the first probe by a small offset so multi-instance
	// agents don't burst their probes simultaneously.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}

	for {
		probePeersOnce(inst, pq)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// probePeersOnce walks the current hostmap and fires one probe per
// direct peer. Probes run sequentially per peer (small fleets) but
// in parallel across peers via goroutines.
func probePeersOnce(inst *meshInstance, pq *pathQuality) {
	ctrl := inst.control()
	if ctrl == nil {
		return
	}
	hosts := ctrl.ListHostmapHosts(false)
	seen := map[string]struct{}{}

	var wg sync.WaitGroup
	for _, h := range hosts {
		if len(h.VpnAddrs) == 0 {
			continue
		}
		vpnAddr := h.VpnAddrs[0].String()
		if vpnAddr == "" {
			continue
		}
		if _, dup := seen[vpnAddr]; dup {
			continue // dedup in case Nebula has multiple HostInfos for one peer
		}
		seen[vpnAddr] = struct{}{}

		// Skip peers without a direct path — the TCP probe over the
		// mesh would still work via relay, but we want to measure
		// DIRECT-path quality. Relayed paths are slow by definition.
		if !h.CurrentRemote.IsValid() {
			continue
		}

		wg.Add(1)
		go func(addr string, h nebula.ControlHostInfo) {
			defer wg.Done()
			probeOnePeer(inst.name(), pq, addr)
		}(vpnAddr, h)
	}
	wg.Wait()
}

// probeOnePeer measures TCP-connect RTT to vpnAddr:agentAPIPort and
// records the result. agentAPIPort (41820) is the same listener used
// by warmPeers — no new firewall surface.
func probeOnePeer(instName string, pq *pathQuality, vpnAddr string) {
	t0 := time.Now()
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.Dial("tcp", net.JoinHostPort(vpnAddr, "41820"))
	if err != nil {
		streak := pq.recordTimeout(vpnAddr)
		if streak == degradationConsecutive {
			log.Printf("[path-quality %s %s] probe down for %d consecutive samples (TCP %s:41820: %v)",
				instName, vpnAddr, streak, vpnAddr, err)
		}
		return
	}
	rttMs := float64(time.Since(t0).Microseconds()) / 1000.0
	conn.Close()

	degraded, baseline := pq.recordSample(vpnAddr, rttMs)
	if degraded {
		log.Printf("[path-quality %s %s] degradation detected: latest=%.0fms baseline EWMA=%.0fms (over last %d samples)",
			instName, vpnAddr, rttMs, baseline, degradationConsecutive)
	}
}
