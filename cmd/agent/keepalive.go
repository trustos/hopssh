package main

// CGNAT-aware mesh keepalive.
//
// Why: CGNAT operators silently expire UDP flow-state at 60-300 s of
// idle. Nebula's connection_manager TestRequest is traffic-reactive
// (vendor/github.com/slackhq/nebula/connection_manager.go::makeTrafficDecision)
// — it sends probes only AFTER inactivity-timeout fires, which means
// truly-idle tunnels can lose CGNAT flow state before the next probe
// fires, especially since TestRequest packets may not refresh CGNAT
// reliably (small, batched). When a user's app finally sends data
// (e.g. clicks Screen Sharing), the first packet hits a dead flow
// and Nebula re-handshakes — but the application protocol already
// has its own retry timeout (RFB ~30 s), causing the user to see a
// long stall before the mesh recovers.
//
// runMeshKeepalive periodically TCP-dials each direct peer's mesh
// API listener on :41820. The dial enters the kernel's utun route
// for the mesh subnet, Nebula encrypts and sends via *:<listenPort>,
// which refreshes our own CGNAT flow state on the wire. Reuses the
// same pattern as runPathQuality but with a longer cadence and
// peer-coverage that includes BOTH direct AND relayed peers (we
// want flow refresh regardless of path classification).
//
// Cross-platform: pure stdlib net.Dial. Same code as pathQuality's
// probe — works identically on Darwin/Linux/Windows.

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

const (
	// keepaliveInterval is how often we refresh per-peer flow state.
	// 90 s is below the typical 120 s CGNAT idle floor (Yettel BG, AWS
	// NAT Gateway, most Tier-1 carrier-grade NATs) and well above
	// Tailscale's 25 s disco cadence — we don't need to be that
	// aggressive because we only fire when no recent app traffic.
	keepaliveInterval = 90 * time.Second

	// keepaliveDialTimeout is the per-peer TCP-connect deadline. Short
	// enough that a dead peer doesn't hold up the keepalive loop.
	keepaliveDialTimeout = 2 * time.Second

	// keepaliveSkipIfRecentSec: if pathQuality probed this peer in the
	// last N seconds, skip the keepalive (recent traffic already
	// refreshed CGNAT state — no work needed).
	keepaliveSkipIfRecentSec = 60

	// keepaliveLogStride: how many keepalive cycles between summary
	// log lines. Prevents log spam — one line every 30 minutes
	// (90 s × 20 cycles) of healthy operation. Anomalies log
	// unconditionally.
	keepaliveLogStride = 20
)

// keepaliveDialFn is the indirection used by runMeshKeepalive's per-peer
// dial. Tests substitute this with a fake to validate the goroutine's
// peer-selection + skip-on-recent logic without binding real TCP sockets.
var keepaliveDialFn = keepaliveTCPDial

// keepaliveTCPDial is the production TCP-dial with the standard
// 2-second timeout. Returns the net.Conn (so the caller can close it)
// or err.
func keepaliveTCPDial(target string) error {
	d := net.Dialer{Timeout: keepaliveDialTimeout}
	conn, err := d.Dial("tcp", target)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// runMeshKeepalive runs the per-instance keepalive goroutine.
//
// Lifecycle: spawned from tryStartMeshInstance against inst.runCtx
// (the per-instance ctx that's cancelled on disconnect/leave/agent-
// shutdown). Exits cleanly when ctx is cancelled.
//
// One probe per direct OR relayed peer per cycle. Skips peers seen
// recently by pathQuality (within keepaliveSkipIfRecentSec). Probe
// errors don't fail the loop — a peer being temporarily unreachable
// is normal and self-recovers.
func runMeshKeepalive(ctx context.Context, inst *meshInstance) {
	if inst == nil {
		return
	}

	// Stagger the first probe so multi-instance agents (home + work)
	// don't burst their keepalives simultaneously.
	select {
	case <-ctx.Done():
		return
	case <-time.After(addJitter(keepaliveInterval / 2)):
	}

	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()

	var cycle uint64
	for {
		probed, skipped := keepaliveOneCycle(inst)
		c := atomic.AddUint64(&cycle, 1)
		if c%keepaliveLogStride == 1 {
			log.Printf("[agent %s] mesh-keepalive: cycle %d (probed: %d, skipped recent: %d)",
				inst.name(), c, probed, skipped)
		}

		select {
		case <-ctx.Done():
			log.Printf("[agent %s] mesh-keepalive: stopped after %d cycles", inst.name(), c)
			return
		case <-t.C:
		}
	}
}

// keepaliveOneCycle fires one round of per-peer probes. Returns counts
// (probed, skipped) for diagnostic logging.
func keepaliveOneCycle(inst *meshInstance) (probed, skipped int) {
	ctrl := inst.control()
	if ctrl == nil {
		// Nebula not running (cold-start race, or instance is being
		// reloaded). Quietly skip; the next cycle will catch it.
		return 0, 0
	}
	hosts := ctrl.ListHostmapHosts(false)
	now := time.Now().Unix()
	for _, h := range hosts {
		if len(h.VpnAddrs) == 0 {
			continue
		}
		vpn := h.VpnAddrs[0].String()
		if vpn == "" {
			continue
		}
		// Skip-on-recent: pathQuality probed this peer within the
		// last keepaliveSkipIfRecentSec. The mesh flow is already
		// being kept warm by the user's actual traffic OR by
		// pathQuality. Don't pile on with synthetic probes.
		if pq := inst.pathQuality; pq != nil {
			rttMs, sampleCount := pq.snapshot(vpn)
			_ = rttMs
			if sampleCount > 0 && pathQualityRecent(pq, vpn, now) {
				skipped++
				continue
			}
		}
		// Best-effort: the dial result doesn't matter for keepalive
		// purposes — the act of dialing fires UDP through Nebula's
		// encrypt path, which refreshes our outbound CGNAT state.
		target := fmt.Sprintf("%s:%d", vpn, agentAPIPort)
		if err := keepaliveDialFn(target); err != nil {
			// Down-peer is normal (peer offline / firewalled);
			// don't spam the log. Could lift to a counter later.
		}
		probed++
	}
	return probed, skipped
}

// pathQualityRecent returns true if pathQuality has a sample for vpn
// from within the last keepaliveSkipIfRecentSec. Lightweight check —
// we don't expose lastSampleAt on pathQualitySample today, so we
// approximate by sampleCount changes between cycles. For v1 of the
// keepalive, conservative behavior is "always probe" — over time we
// can tighten this once pathQuality exposes a timestamp.
//
// TODO: add lastSampleAt to pathQualitySample so this can short-
// circuit accurately. For now, assume "recent" is unknowable and
// always probe (returns false). Cost is negligible: 1 TCP-dial per
// peer per 90 s.
func pathQualityRecent(_ *pathQuality, _ string, _ int64) bool {
	return false
}
