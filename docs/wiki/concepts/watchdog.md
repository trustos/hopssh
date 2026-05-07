---
type: concept
title: Agent watchdog architecture (3 independent watchdogs)
status: current
last_compiled: 2026-05-07
sources:
  - cmd/agent/keepalive.go
  - cmd/agent/renew.go (runRenewalWatchdog)
  - cmd/agent/watcher_watchdog.go
  - cmd/agent/instance.go (markX / activityAge accessors, restartFn closure)
  - cmd/agent/watchdog_test.go
  - cmd/agent/renew_watchdog_test.go
  - cmd/agent/watcher_watchdog_test.go
---

# Agent watchdog architecture

## TL;DR

The agent has **three independent watchdogs**, each scoped to one class of silent failure that the others can't catch. All three share the same primitive: stamp activity → assert on stamp age → on trip, write goroutine pprof dump to disk, then invoke `restartFn` (the v0.10.36 lifecycle infrastructure that closes the running svc and reconnects). Cooldowns prevent restart-loops on persistent issues. None of the three is sufficient alone.

| Watchdog | Watches | Trip condition | Threshold | Shipped |
|---|---|---|---|---|
| Renewal | `lastRenewalActivityAt` (stamped by `runCertRenewal`) | silence > `renewalSilenceThreshold` (= cert validity / 4 = **6h** on 24h certs) | 5 min check interval, 30 min cooldown | Phase P (v0.10.79 / v0.10.82) |
| Data plane | TCP-probe success rate against peer mesh API listeners | `peers > 0 && probed > 0 && all-fail` for **3 consecutive 90s cycles** (~270s) | 90s probe cadence, 5 min cooldown | v0.10.36 |
| Watcher | `lastWatcherActivityAt` (stamped at top of `watchNetworkChanges` tick body) | silence > `watcherSilenceThreshold` (= **3 min**) | 30s check interval, 30 min cooldown | Phase DD (v0.10.96) |

## Why three, not one

Each watchdog catches a different failure mode that the others structurally cannot detect:

- **Renewal watchdog** — guards `runCertRenewal`. Motivating incident (May 1 2026): renewal goroutine emitted its startup banner then went silent for 9+ hours; cert hard-expired; mesh died but the data plane and watcher kept running, so the data-plane watchdog never tripped. The other two can't see renewal silence because they don't read `lastRenewalActivityAt`.
- **Data-plane watchdog** — guards peer reachability. Catches Nebula vendored-runtime stuck states (e.g., May 2 2026 v0.10.84 deploy bounce, multi-instance contention, hung syscall under network reset). Doesn't fire on ZERO peers (`probed > 0` predicate), so it's blind when peers go uninstalled or lighthouse-only.
- **Watcher watchdog** — guards `watchNetworkChanges`. Motivating incident (May 7 2026): goroutine deadlocked inside vendor Nebula's `RebindUDPServer` or `CloseAllTunnels` during a network-change handler, no `CRITICAL/PANICKED` log because deadlocks aren't panics. Lighthouse selfEndpoint refresh died for 3.5+ hours; peers saw `udpAddrs=[]`; mesh stayed dead. The other two missed it: renewal goroutine kept stamping fine; data-plane watchdog had `peers=0` (after Phase V's lighthouse-filter), so its predicate `peers > 0` was false.

## Common pattern

All three share the same shape:

```go
// 1. The watched goroutine stamps activity at observable points.
func runX(ctx context.Context, inst *meshInstance) {
    for {
        inst.markXActivity()       // <-- stamp at TOP of loop body
        // ... do work, possibly blocking ...
    }
}

// 2. The watchdog goroutine checks stamp age every N seconds.
func runXWatchdog(ctx context.Context, inst *meshInstance) {
    var lastTripAt time.Time
    t := time.NewTicker(checkInterval)
    for {
        <-t.C
        age := inst.xActivityAge()
        if age < silenceThreshold {
            continue                                // healthy
        }
        if age > 100*365*24*time.Hour {
            continue                                // cold start (never stamped) — grace
        }
        if !lastTripAt.IsZero() && time.Since(lastTripAt) < cooldown {
            continue                                // recent trip — wait it out
        }
        lastTripAt = time.Now()
        log.Printf("[X-watchdog] CRITICAL: silent for %s — capturing dump and triggering auto-restart", age)
        writeXStuckDump(inst, age)                  // <configDir>/<name>/X-stuck-<ts>.txt
        if inst.restartFn != nil {
            _ = inst.restartFn()
        }
    }
}
```

Variants per watchdog:

- **Renewal**: stamp points are `runCertRenewal` entry, pre-sleep, post-wake, pre-POST, post-POST, retry-attempts. Field: `lastRenewalActivityAt`. Method: `markRenewalActivity()` / `renewalActivityAge()`. Dump file: `renewal-stuck-<ts>.txt`.
- **Data plane**: stamp surrogate is the keepalive cycle's all-fail counter (no `lastDataPlaneActivityAt` field — instead, consecutive cycle count). The "stamp" is implicit in the ticker advancing. Dump file: `stuck-state-<ts>.txt`.
- **Watcher**: stamp point is the TOP of every tick body in `watchNetworkChanges`. Field: `lastWatcherActivityAt`. Method: `markWatcherActivity()` / `watcherActivityAge()`. Dump file: `watcher-stuck-<ts>.txt`.

The renewal and watcher watchdogs share the `activityMu sync.Mutex` and stamp pattern in `cmd/agent/instance.go`. The data-plane watchdog uses cycle-counter logic in `cmd/agent/keepalive.go`.

## Defense in depth: per-watchdog companion timeouts

Phase DD added a fourth idea on top of the watcher watchdog: **hard timeouts on the load-bearing blocking calls inside the watched loop body.** `runWithTimeout(name, label, deadline, fn)` in `cmd/agent/watcher_watchdog.go` wraps `ctrl.RebindUDPServer()` and `ctrl.CloseAllTunnels(true)` with 5s deadlines. On timeout, the helper logs WARN and returns; the leaked goroutine is the accepted cost (alternative is the entire watcher wedging for hours).

This is **defense in depth, not an alternative** to the watchdog. The watchdog is the safety net for any wedge the timeouts don't catch (different deadlock site, missed deadline, non-blocking-but-still-stuck logic). Common case: timeouts fire, watcher continues normally, no user-visible disruption. Edge case: timeouts miss → watchdog catches within 3.5 min.

The pattern generalizes: any long-running goroutine doing load-bearing work in a per-iteration loop body should have BOTH a stamp + watchdog AND hard timeouts on the iteration's blocking calls.

Why didn't the deferred `recover()` in `watchNetworkChanges` catch the May 7 wedge? Because `defer recover()` only catches **panics**, not deadlocks. A blocked goroutine waiting on a Nebula internal mutex leaves the recover deferred but never reachable. Tripwire test `TestWatchNetworkChanges_RebindBlockUsesTimeouts` source-scans `nebula.go` to lock in the timeout wrappers.

## Forensic dump format

All three dumps share the same shape: header lines + full pprof goroutine stack dump:

```
<class>-stuck dump — 2026-05-07T18:30:00Z
instance: home
silence: 3m12s (threshold 3m0s)
endpoint: https://hopssh.com
node-id: 6e5206df-...

--- goroutine dump ---
goroutine 1 [running]: ...
goroutine 47 [select, 1 minutes]: ...
[full pprof goroutine output, debug=2]
```

Files live at `<configDir>/<network>/<class>-stuck-<YYYYMMDDTHHMMSSZ>.txt`. Pruned automatically after 7 days by `pruneOldStuckStateDumps()` (called from `tryStartMeshInstance` on every connect).

## Known interactions

- **Control-plane deploy bounce** — when [[../entities/hopssh-cloud]] swaps containers, lighthouse UDP becomes unreachable for ~5–7 min. All peers fail probes simultaneously → data-plane watchdog fires within 4–5 min. Auto-recovers when new container comes up. **Functionally correct but noisy** (forensic dump on every deploy). See [[../incidents/2026-05-02-mbp-watchdog-deploy-bounce]].
- **Sleep/wake** — both the renewal and watcher watchdogs are suppressed inside their cooldown windows so a wake event doesn't trigger an immediate restart-on-restart. Phase S [[cert-renewal]] handles cert-side recovery via the 60s wall-clock ticker; the data-plane watchdog is the peer-reachability safety net.
- **Multi-enrollment hosts** — each enrollment runs its own meshInstance with its own three watchdogs, so a wedge on `home` doesn't affect `work`. Verified May 7: MBP's `work` watcher stayed alive throughout the 3.5h `home` wedge.

## Test coverage

- `cmd/agent/watchdog_test.go` — 6 tests for the data-plane keepalive watchdog (dump format, cooldown, nil-restartFn graceful no-op, restartFn success, source-scan tripwires).
- `cmd/agent/renew_watchdog_test.go` — 8 tests for the renewal watchdog (stamp/age accessors, fires after silence, quiet when ticking, cold-start grace, ctx-cancel exit, restart error propagation, concurrent stamp safety).
- `cmd/agent/watcher_watchdog_test.go` — 14 tests for the watcher watchdog including 3 source-scan tripwires (`TestTryStartMeshInstance_SpawnsWatcherWatchdog`, `TestWatchNetworkChanges_StampsAtTopOfTickBody`, `TestWatchNetworkChanges_RebindBlockUsesTimeouts`).

## Architectural lesson

> Any long-running goroutine that does load-bearing work in a per-iteration loop body MUST stamp activity at the TOP of each iteration; pair with a watchdog that asserts on stamp age. Blocking calls inside the loop body MUST have hard timeouts — `defer recover()` only catches panics, not deadlocks.

If the agent ever gains a fourth long-running per-iteration goroutine doing load-bearing work, the answer is "add a fourth watchdog with the same shape," not "extend an existing one." Each watchdog's predicate is structurally specific to its goroutine's notion of liveness; sharing them couples failure modes that should be independent.

## Backlinks

- [[../incidents/2026-05-02-mbp-watchdog-deploy-bounce]] — data-plane watchdog tripped during v0.10.84 control-plane rollout
- [[../incidents/2026-05-07-mbp-watcher-wedge]] — watcher watchdog motivating incident (Phase DD)
- [[../entities/hopssh-cloud]] — primary trigger source for data-plane watchdog (deploy bounces)
- [[cert-renewal]] — sister architecture for renewal-side silent death
- [[sleep-wake]] — interacts during wake recovery
