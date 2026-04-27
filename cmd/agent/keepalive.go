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
	"os"
	"path/filepath"
	"runtime/pprof"
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

	// watchdogStuckThreshold: number of consecutive all-failed cycles
	// (each cycle has >0 peers AND every probe failed) that trips the
	// auto-recovery. 3 cycles × 90 s = 4.5 minutes of confirmed
	// stuck-state before we self-heal. Below this we'd false-trigger
	// on transient network blips; above this we'd let the user
	// suffer too long.
	watchdogStuckThreshold = 3

	// watchdogRestartCooldown: minimum interval between auto-recovery
	// restarts on the same instance. Prevents restart-loop cycles if
	// the underlying problem is persistent (kernel-level firewall
	// rule, hardware issue, persistent CGNAT block).
	watchdogRestartCooldown = 5 * time.Minute
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
	var consecutiveStuck int
	for {
		probed, succeeded, skipped, peers := keepaliveOneCycle(inst)
		c := atomic.AddUint64(&cycle, 1)

		// Watchdog: a "stuck cycle" is one where the instance has
		// known peers in its hostmap AND every probe attempted to
		// reach them failed. Genuinely-no-peers states (peers == 0)
		// don't increment — fresh cold-start instances and
		// disconnected periods are not stuck-state.
		if peers > 0 && probed > 0 && succeeded == 0 {
			consecutiveStuck++
			log.Printf("[agent %s] mesh-keepalive: STUCK cycle %d (%d/%d peers all failed; consecutive: %d/%d)",
				inst.name(), c, probed, peers, consecutiveStuck, watchdogStuckThreshold)
			if consecutiveStuck >= watchdogStuckThreshold {
				watchdogTrip(inst, c, consecutiveStuck)
				consecutiveStuck = 0 // reset after recovery attempt
			}
		} else {
			consecutiveStuck = 0
			if c%keepaliveLogStride == 1 {
				log.Printf("[agent %s] mesh-keepalive: cycle %d (probed: %d ok: %d skipped: %d peers: %d)",
					inst.name(), c, probed, succeeded, skipped, peers)
			}
		}

		select {
		case <-ctx.Done():
			log.Printf("[agent %s] mesh-keepalive: stopped after %d cycles", inst.name(), c)
			return
		case <-t.C:
		}
	}
}

// watchdogTrip handles a confirmed stuck-state detection. Sequence:
//   1. Capture goroutine + heap dumps to disk for forensics — done
//      BEFORE restart so a future investigator can root-cause why
//      Nebula's data plane stalled (multi-instance contention?
//      vendored-runtime deadlock?).
//   2. Respect cooldown: if we already restarted this instance
//      within watchdogRestartCooldown, log only — don't restart-loop.
//   3. Invoke inst.restartFn (set by runServe's connectFn closure)
//      to bring the instance down + back up cleanly. The inst's
//      runCtx will be cancelled as part of the restart, ending THIS
//      goroutine — the new instance spawns a fresh keepalive.
func watchdogTrip(inst *meshInstance, cycle uint64, stuckCount int) {
	dumpPath := writeStuckStateDump(inst, cycle, stuckCount)
	if dumpPath != "" {
		log.Printf("[agent %s] WATCHDOG: data plane stuck after %d cycles — forensic dump at %s", inst.name(), stuckCount, dumpPath)
	} else {
		log.Printf("[agent %s] WATCHDOG: data plane stuck after %d cycles (forensic dump failed; see preceding logs)", inst.name(), stuckCount)
	}

	if !inst.lastWatchdogRestartAt.IsZero() && time.Since(inst.lastWatchdogRestartAt) < watchdogRestartCooldown {
		log.Printf("[agent %s] WATCHDOG: skipping auto-restart — last restart was %s ago, cooldown is %s",
			inst.name(), time.Since(inst.lastWatchdogRestartAt).Truncate(time.Second), watchdogRestartCooldown)
		return
	}

	if inst.restartFn == nil {
		log.Printf("[agent %s] WATCHDOG: cannot auto-recover — no restartFn wired (boot path?). Manual restart required.", inst.name())
		return
	}

	inst.lastWatchdogRestartAt = time.Now()
	log.Printf("[agent %s] WATCHDOG: invoking auto-restart", inst.name())
	if err := inst.restartFn(); err != nil {
		log.Printf("[agent %s] WATCHDOG: auto-restart returned error: %v (manual intervention may be needed)", inst.name(), err)
		return
	}
	log.Printf("[agent %s] WATCHDOG: auto-restart returned cleanly; new instance should be running", inst.name())
}

// writeStuckStateDump captures a goroutine snapshot to a file under
// the instance's config dir. Best-effort — on any error, logs and
// returns empty string. Includes the trip context (cycle number,
// stuck count) at the top so the file is self-explanatory weeks
// later when an operator finds it.
func writeStuckStateDump(inst *meshInstance, cycle uint64, stuckCount int) string {
	if inst == nil {
		return ""
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(inst.dir(), fmt.Sprintf("stuck-state-%s.txt", stamp))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		log.Printf("[agent %s] WATCHDOG: cannot create dump file %s: %v", inst.name(), path, err)
		return ""
	}
	defer f.Close()

	fmt.Fprintf(f, "# hopssh agent stuck-state forensic dump\n")
	fmt.Fprintf(f, "# enrollment: %s\n", inst.name())
	fmt.Fprintf(f, "# captured: %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(f, "# keepalive cycle: %d\n", cycle)
	fmt.Fprintf(f, "# consecutive stuck cycles: %d (threshold %d)\n", stuckCount, watchdogStuckThreshold)
	fmt.Fprintf(f, "# context: every keepalive probe failed for >%d cycles (~%.0fs); auto-restart was triggered.\n",
		watchdogStuckThreshold, float64(watchdogStuckThreshold)*keepaliveInterval.Seconds())
	fmt.Fprintf(f, "\n=== goroutine dump (debug=2 for full stacks) ===\n\n")

	prof := pprof.Lookup("goroutine")
	if prof == nil {
		fmt.Fprintf(f, "goroutine profile not available (pprof.Lookup returned nil)\n")
		return path
	}
	if err := prof.WriteTo(f, 2); err != nil {
		fmt.Fprintf(f, "WriteTo failed: %v\n", err)
	}
	return path
}

// keepaliveOneCycle fires one round of per-peer probes. Returns:
//   probed     — number of peers we attempted to dial this cycle
//   succeeded  — number whose dial returned nil (mesh flow IS alive)
//   skipped    — number skipped due to pathQualityRecent
//   peers      — number of peers in the hostmap (excluding skipped)
//
// The watchdog uses (peers > 0 && probed > 0 && succeeded == 0) as the
// "stuck cycle" signal. Pure no-peers states (peers == 0) do NOT
// count as stuck — common during cold-start or full-disconnect.
func keepaliveOneCycle(inst *meshInstance) (probed, succeeded, skipped, peers int) {
	ctrl := inst.control()
	if ctrl == nil {
		// Nebula not running (cold-start race, or instance is being
		// reloaded). Quietly skip; the next cycle will catch it.
		return 0, 0, 0, 0
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
		peers++
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
		target := fmt.Sprintf("%s:%d", vpn, agentAPIPort)
		if err := keepaliveDialFn(target); err == nil {
			succeeded++
		}
		probed++
	}
	return probed, succeeded, skipped, peers
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
