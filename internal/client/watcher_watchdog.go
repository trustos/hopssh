package client

// Phase DD (v0.10.96): watcher silent-death detection + auto-recovery.
//
// Catches the third class of silent goroutine failure in the agent:
// the watchNetworkChanges goroutine deadlocking inside a vendor Nebula
// call (RebindUDPServer / CloseAllTunnels). Phase P covers renewal
// silent-death; v0.10.36 covers data-plane stuck. Without this, a
// wedged watcher silently disables: lighthouse selfEndpoint refresh
// after network changes, sleep/wake recovery, and the local-API status
// serializer for the wedged enrollment — UI lies "connected".
//
// Production evidence (2026-05-07): MBP's home enrollment watcher
// went silent at 17:22 after a network-change handler at 18:00:45,
// peer (mini) saw udpAddrs=[] from the lighthouse for 3.5+ hours.
// No CRITICAL/PANICKED log → not crashed, genuinely blocked. The
// fix combines:
//   - F1: this watchdog goroutine, detects silence > 3 min, restarts
//     the instance via the existing v0.10.36 restartFn pipeline.
//   - F2: hard timeouts on the vendor Nebula calls (runWithTimeout)
//     so the watcher self-recovers in <5s in the common case without
//     needing this watchdog to fire.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"
)

// watcherSilenceThreshold is how long inst.lastWatcherActivityAt may
// sit idle before we declare the watchNetworkChanges goroutine dead.
// 3 minutes = 36× the 5s tick cadence + 3× the 60s alive-log cadence —
// generous enough to absorb GC pauses, App Nap, and rebind block
// duration in the common case; tight enough to limit user-visible
// outage to the watchdog interval after the wedge.
//
// Variable so tests can shorten it.
var watcherSilenceThreshold = 3 * time.Minute

// watcherWatchdogInterval is how often runWatcherWatchdog wakes to
// check the stamp. 30s matches the v0.10.36 keepalive watchdog cadence
// and keeps detection latency bounded to ~30s after the silence
// threshold is crossed.
var watcherWatchdogInterval = 30 * time.Second

// runWatcherWatchdog asserts on inst.lastWatcherActivityAt updates from
// watchNetworkChanges. When silence exceeds watcherSilenceThreshold,
// it (a) writes a forensic goroutine dump to
// <configDir>/<name>/watcher-stuck-<ts>.txt, (b) logs CRITICAL with the
// silence duration, and (c) invokes inst.restartFn — the v0.10.36
// lifecycle infrastructure that the stuck-data-plane watchdog uses for
// auto-recovery.
//
// Mirrors runRenewalWatchdog (cmd/agent/renew.go) and watchdogTrip
// (cmd/agent/keepalive.go) — same primitive, different trigger
// condition.
//
// Cooldown matches Phase P's 30 min: a persistent underlying issue
// shouldn't restart-loop. If the wedge recurs after restart, that's
// a deeper bug worth manual investigation.
func runWatcherWatchdog(ctx context.Context, inst *meshInstance) {
	const cooldown = 30 * time.Minute
	var lastTripAt time.Time

	t := time.NewTicker(watcherWatchdogInterval)
	defer t.Stop()
	log.Printf("[watcher-watchdog %s] started (threshold=%s, check-interval=%s)",
		inst.name(), watcherSilenceThreshold, watcherWatchdogInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		age := inst.watcherActivityAge()
		if age < watcherSilenceThreshold {
			continue
		}
		// Cold-start grace: if lastWatcherActivityAt was never stamped
		// (i.e. age == math.MaxInt64-equivalent), don't fire — the
		// goroutine may simply not have started yet on bundled-mode
		// boot or a slow Nebula bring-up.
		if age > 100*365*24*time.Hour {
			continue
		}
		// Cooldown: don't restart-loop on persistent issues.
		if !lastTripAt.IsZero() && time.Since(lastTripAt) < cooldown {
			continue
		}
		lastTripAt = time.Now()

		log.Printf("[watcher-watchdog %s] CRITICAL: network-change watcher silent for %s (threshold %s) — capturing forensic dump and triggering auto-restart",
			inst.name(), age.Truncate(time.Second), watcherSilenceThreshold)

		writeWatcherStuckDump(inst, age)

		if inst.restartFn != nil {
			if err := inst.restartFn(); err != nil {
				log.Printf("[watcher-watchdog %s] auto-restart failed: %v (next attempt in %s)",
					inst.name(), err, cooldown)
			} else {
				log.Printf("[watcher-watchdog %s] auto-restart triggered", inst.name())
			}
		} else {
			log.Printf("[watcher-watchdog %s] no restartFn wired — agent will not self-recover. Manual `launchctl kickstart` needed.",
				inst.name())
		}
	}
}

// writeWatcherStuckDump writes a goroutine pprof + diagnostic snapshot
// to <configDir>/<name>/watcher-stuck-<ts>.txt. Mirrors the v0.10.36
// stuck-data-plane dump pattern. Returns the path written, or empty
// string on failure (with the failure logged).
func writeWatcherStuckDump(inst *meshInstance, silenceAge time.Duration) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(inst.dir(), fmt.Sprintf("watcher-stuck-%s.txt", ts))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("[watcher-watchdog %s] failed to open dump file %s: %v", inst.name(), path, err)
		return ""
	}
	defer f.Close()

	fmt.Fprintf(f, "watcher-stuck dump — %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(f, "instance: %s\n", inst.name())
	fmt.Fprintf(f, "silence: %s (threshold %s)\n", silenceAge, watcherSilenceThreshold)
	fmt.Fprintf(f, "endpoint: %s\n", inst.endpoint())
	fmt.Fprintf(f, "node-id: %s\n", inst.nodeID())
	fmt.Fprintf(f, "\n--- goroutine dump ---\n")

	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		fmt.Fprintf(f, "goroutine dump failed: %v\n", err)
	}
	log.Printf("[watcher-watchdog %s] forensic dump: %s", inst.name(), path)
	return path
}

// runWithTimeout runs fn in a goroutine and waits up to deadline for
// it to return. If fn returns within the deadline, runWithTimeout
// returns normally. If it doesn't, runWithTimeout logs a WARN and
// returns; fn's goroutine is leaked (it will eventually complete OR
// the leak persists until the next instance restart, which is the
// accepted cost — the alternative is the entire watchNetworkChanges
// goroutine wedging for hours).
//
// instName + label appear in the log line so multi-enrollment hosts
// can attribute a timeout to its enrollment.
//
// Variable so tests can stub for fast assertion.
var runWithTimeout = func(instName, label string, deadline time.Duration, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return
	case <-time.After(deadline):
		log.Printf("[agent %s] WARN: %s exceeded %v deadline, continuing without it (Phase DD timeout — likely Nebula vendor deadlock; goroutine dump on next watchdog trip)",
			instName, label, deadline)
	}
}
