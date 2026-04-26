package main

// Layer 4 — per-endpoint active liveness probing + stale-endpoint reaping.
//
// Why: Layers 1-3 (TTL on lighthouse, ReplaceStaticHostMap, subnet filter)
// fix the AUTHORITATIVE side of endpoint distribution. But peers' Nebula
// hostmaps cache endpoints LOCALLY from prior lighthouse queries. Those
// local caches don't expire — the only signal Nebula uses to drop them
// is connection_manager's per-HostInfo dead-mark, which only checks the
// CurrentRemote. Stale candidate endpoints in the same HostInfo's
// remote-list never get tested.
//
// Layer 4 fills the gap: we actively probe EVERY candidate endpoint via
// patch 22's Control.ProbeEndpoint (sends a Nebula TestRequest to a
// specific remote, bypassing CurrentRemote selection). Patch 22 also
// installs an InboundObserver hook so we observe which endpoints respond.
// Endpoints with no inbound traffic in the staleness window get pruned
// via Layer 2's ReplaceStaticHostMap with the live-only set.
//
// Resulting cadence:
//   - endpointProbeInterval (default 5s): fire one TestRequest per known endpoint
//   - inboundObserver: record (vpnAddr, remote, time.Now()) on every
//     authenticated inbound packet (any subtype — handshake, message,
//     test reply, lighthouse — all signal the endpoint is alive)
//   - endpointReapInterval (default 10s): scan; endpoints with lastSeen older
//     than endpointStaleWindow (default 30s) get dropped
//   - On reap, call ReplaceStaticHostMap(vpnAddr, liveEndpoints) to
//     update the lighthouse cache atomically
//
// Hot-path overhead: probe sends are ~100B per endpoint per 5s; the
// inbound observer is one atomic load + nil check + map insert per
// packet. Negligible at fleet scale.

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/slackhq/nebula"
)

const (
	// endpointProbeInterval is the cadence at which we send a TestRequest
	// to each known endpoint. 5s = 6× faster than connection_manager's
	// 30s dead-mark cycle so we detect failure before tunnel-flap visible
	// to the user. Tested OK on cellular CGNAT — the probes share the
	// existing tunnel's cipher state, no per-probe handshake overhead.
	endpointProbeInterval = 5 * time.Second

	// endpointReapInterval is the cadence at which we scan for stale
	// endpoints. Slower than endpointProbeInterval to give probes time
	// to elicit replies before we declare an endpoint dead.
	endpointReapInterval = 10 * time.Second

	// endpointStaleWindow is how long we wait without ANY inbound traffic
	// from an endpoint before we declare it dead. 30s covers 5 missed
	// probes (endpointProbeInterval * 6) plus jitter — past this, the
	// upstream router/CGNAT has likely reassigned the external port.
	//
	// Per RFC 8445 (ICE) §10: STUN keepalives at 15s, liveness via
	// successful exchange. Our 30s window is 2× that — biased toward
	// false-negative (keep alive a bit too long) over false-positive
	// (kill a live endpoint and disrupt traffic).
	endpointStaleWindow = 30 * time.Second
)

// endpointProbe tracks per-(peer, endpoint) liveness state. One
// instance per meshInstance (each enrollment has its own probe state).
type endpointProbe struct {
	mu sync.Mutex
	// lastSeen[vpnAddr][remote] = time of last observed inbound packet
	// from that endpoint for that peer. Used to decide reap eligibility.
	lastSeen map[netip.Addr]map[netip.AddrPort]time.Time

	// Diagnostic counters (Layer 4 debug). Help confirm probe firing,
	// observer wiring, and reap correctness in production logs.
	probesSent     uint64
	observations   uint64
	reapsFired     uint64
	reapsSkipped   uint64 // skipped because live==current set
}

func newEndpointProbe() *endpointProbe {
	return &endpointProbe{
		lastSeen: make(map[netip.Addr]map[netip.AddrPort]time.Time),
	}
}

// observe records an inbound packet from (vpnAddr, remote). Called from
// the Nebula vendor patch 22 inboundObserver callback on every
// authenticated inbound packet. Lock-protected — outside callers
// shouldn't read this map directly.
func (p *endpointProbe) observe(vpnAddr netip.Addr, remote netip.AddrPort) {
	if !vpnAddr.IsValid() || !remote.IsValid() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := p.lastSeen[vpnAddr]
	if !ok {
		m = make(map[netip.AddrPort]time.Time)
		p.lastSeen[vpnAddr] = m
	}
	m[remote] = time.Now()
	p.observations++
}

// liveEndpoints returns the set of remotes for vpnAddr that have shown
// inbound traffic within the staleness window. Used by reaper goroutine
// to compute what to keep.
func (p *endpointProbe) liveEndpoints(vpnAddr netip.Addr, window time.Duration) []netip.AddrPort {
	cutoff := time.Now().Add(-window)
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := p.lastSeen[vpnAddr]
	if !ok {
		return nil
	}
	out := make([]netip.AddrPort, 0, len(m))
	for ep, t := range m {
		if t.After(cutoff) {
			out = append(out, ep)
		}
	}
	return out
}

// pruneStale drops entries past the staleness window from the local map
// so the map doesn't grow indefinitely if peers come and go.
func (p *endpointProbe) pruneStale(window time.Duration) {
	cutoff := time.Now().Add(-window)
	p.mu.Lock()
	defer p.mu.Unlock()
	for vpn, m := range p.lastSeen {
		for ep, t := range m {
			if t.Before(cutoff) {
				delete(m, ep)
			}
		}
		if len(m) == 0 {
			delete(p.lastSeen, vpn)
		}
	}
}

// runEndpointProbe is the per-instance goroutine that:
//   1. Wires the inbound observer into Nebula via Control.SetInboundObserver
//   2. Periodically probes every candidate endpoint per peer
//   3. Periodically reaps stale endpoints by calling ReplaceStaticHostMap
//      with the live-only set
//
// Exits when ctx is done (parent context tracks instance lifecycle).
func runEndpointProbe(ctx context.Context, inst *meshInstance) {
	if inst == nil {
		return
	}
	probe := newEndpointProbe()

	// Wait briefly for Nebula to start before installing the observer —
	// avoid a race where Control isn't ready yet.
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}

	ctrl := inst.control()
	if ctrl == nil {
		log.Printf("[endpoint-probe %s] no Nebula control yet, exiting (will retry on next instance start)", inst.name())
		return
	}
	ctrl.SetInboundObserver(probe.observe)
	defer ctrl.SetInboundObserver(nil)
	log.Printf("[endpoint-probe %s] started (probe interval=%s, reap interval=%s, staleness=%s)",
		inst.name(), endpointProbeInterval, endpointReapInterval, endpointStaleWindow)

	probeTicker := time.NewTicker(endpointProbeInterval)
	reapTicker := time.NewTicker(endpointReapInterval)
	statsTicker := time.NewTicker(60 * time.Second)
	defer probeTicker.Stop()
	defer reapTicker.Stop()
	defer statsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[endpoint-probe %s] exiting", inst.name())
			return
		case <-probeTicker.C:
			probeAllEndpoints(inst, probe)
		case <-reapTicker.C:
			reapStaleEndpoints(inst, probe)
			probe.pruneStale(endpointStaleWindow)
		case <-statsTicker.C:
			probe.mu.Lock()
			ps, obs, rf, rs := probe.probesSent, probe.observations, probe.reapsFired, probe.reapsSkipped
			knownVpns := len(probe.lastSeen)
			liveTotal := 0
			for _, m := range probe.lastSeen {
				liveTotal += len(m)
			}
			probe.mu.Unlock()
			log.Printf("[endpoint-probe %s] stats: probesSent=%d observations=%d reapsFired=%d reapsSkipped=%d knownPeers=%d liveEndpoints=%d",
				inst.name(), ps, obs, rf, rs, knownVpns, liveTotal)
		}
	}
}

// probeAllEndpoints iterates every peer in the hostmap and fires a
// TestRequest to EVERY candidate endpoint (lighthouse-cached + learned
// via inbound) for that peer. The TestRequest goes through Nebula's
// normal encrypted send path; the receiver's outside.go will TestReply,
// which our inbound observer captures.
//
// Best-effort: any individual probe failure is silently ignored. The
// next reap cycle will catch endpoints that consistently fail to
// respond.
func probeAllEndpoints(inst *meshInstance, probe *endpointProbe) {
	ctrl := inst.control()
	if ctrl == nil {
		return
	}
	hosts := ctrl.ListHostmapHosts(false)
	selfIP := inst.meshIP()
	sent := uint64(0)
	for _, h := range hosts {
		if len(h.VpnAddrs) == 0 {
			continue
		}
		vpnAddr := h.VpnAddrs[0]
		if selfIP != "" && vpnAddr.String() == selfIP {
			continue
		}
		for _, ra := range h.RemoteAddrs {
			if ctrl.ProbeEndpoint(vpnAddr, ra) {
				sent++
			}
		}
	}
	if sent > 0 {
		probe.mu.Lock()
		probe.probesSent += sent
		probe.mu.Unlock()
	}
}

// reapStaleEndpoints scans every peer's known candidate endpoints and
// removes those without recent inbound activity by calling
// ReplaceStaticHostMap with ONLY the still-live set.
//
// Important: we only reap when there's at least one live endpoint for
// the peer. If ALL endpoints look stale (e.g. peer is offline, mid-
// handshake, or all probes lost), we skip — the goal is to remove dead
// candidates while preserving the live ones, not to nuke the entire
// hostmap entry.
func reapStaleEndpoints(inst *meshInstance, probe *endpointProbe) {
	ctrl := inst.control()
	if ctrl == nil {
		return
	}
	subnet := inst.meshSubnet()
	hosts := ctrl.ListHostmapHosts(false)
	for _, h := range hosts {
		if len(h.VpnAddrs) == 0 || len(h.RemoteAddrs) == 0 {
			continue
		}
		vpnAddr := h.VpnAddrs[0]
		// Defense-in-depth: skip cross-network peers (matches Layer 3
		// filter in injectPeerEndpoints).
		if subnet.IsValid() && !subnet.Contains(vpnAddr) {
			continue
		}
		live := probe.liveEndpoints(vpnAddr, endpointStaleWindow)
		if len(live) == 0 {
			// No endpoint has shown life — peer is offline or all
			// candidates are dead. Don't replace with empty set.
			continue
		}
		// If the live set equals the current set, nothing to reap.
		if sameRemotes(h.RemoteAddrs, live) {
			continue
		}
		liveSet := make(map[netip.AddrPort]struct{}, len(live))
		for _, ep := range live {
			liveSet[ep] = struct{}{}
		}
		var dropped []netip.AddrPort
		for _, ep := range h.RemoteAddrs {
			if _, ok := liveSet[ep]; !ok {
				dropped = append(dropped, ep)
			}
		}
		if len(dropped) == 0 {
			probe.mu.Lock()
			probe.reapsSkipped++
			probe.mu.Unlock()
			continue
		}
		// Layer 4 reap DISABLED (v0.10.27 hotfix) — the inbound-observation
		// liveness signal is fundamentally broken under asymmetric CGNAT.
		// CGNAT translates the peer's outbound REPLY source to a DIFFERENT
		// external port than the NAT-PMP-mapped INBOUND port. So we
		// observe inbound from `46.10.240.91:4246` but never from the
		// probed `46.10.240.91:9266`, even though `:9266` is genuinely
		// alive (NAT-PMP forwards external:9266 → internal:4242 fine).
		// The reap then removes the live endpoint, starving the hostmap.
		// Observed in production overnight: MBP→mini tunnel collapsed
		// after ~9 h of progressive endpoint reaping; screen-share failed.
		//
		// Logging only — what we WOULD reap if liveness detection worked.
		// Keeping the probe + observer for future iteration: nonce-
		// correlated TestRequest/TestReply (Layer 4b, deferred) would
		// give us a per-endpoint liveness signal that's CGNAT-asymmetry
		// safe. Until then, rely on Layers 1-3 + Nebula's connection_manager
		// dead-mark for endpoint cleanup.
		log.Printf("[endpoint-probe %s] WOULD-REAP (disabled) %d stale endpoint(s) for %s: %v (keeping %d live: %v)",
			inst.name(), len(dropped), vpnAddr, dropped, len(live), live)
		probe.mu.Lock()
		probe.reapsSkipped++
		probe.mu.Unlock()
	}
}

// sameRemotes returns true if the two address-port slices contain the
// same set of values, regardless of order. Used to skip ReplaceStaticHostMap
// calls when nothing's changed.
func sameRemotes(a, b []netip.AddrPort) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[netip.AddrPort]struct{}, len(a))
	for _, ap := range a {
		seen[ap] = struct{}{}
	}
	for _, ap := range b {
		if _, ok := seen[ap]; !ok {
			return false
		}
	}
	return true
}

// Compile-time verification that ProbeEndpoint signature matches what
// we're calling in tests/integration. Using nebula.Control here makes
// sure the import is used and lets the linter catch any vendor patch
// signature drift on rebuild.
var _ = (*nebula.Control)(nil)
