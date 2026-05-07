---
type: incident
title: 2026-05-07 — MBP home-network watcher wedge in vendor Nebula call
status: resolved
last_compiled: 2026-05-07
sources:
  - cmd/agent/nebula.go
  - cmd/agent/watcher_watchdog.go
  - cmd/agent/instance.go
  - cmd/agent/local_api.go
shipped_in: v0.10.96 (Phase DD)
---

# 2026-05-07 — MBP home-network watcher wedge in vendor Nebula call

**TL;DR.** MBP's `watchNetworkChanges` goroutine for the home enrollment deadlocked at 18:00:45 during a network-change handler — most likely inside vendor Nebula's `RebindUDPServer` or `CloseAllTunnels`, both called sequentially in the rebind block. No CRITICAL/PANICKED log fired (deadlocks aren't panics; the deferred `recover()` couldn't catch this). The wedge stranded the lighthouse selfEndpoint refresh: peers (mac mini) saw `udpAddrs=[]` from the lighthouse for MBP for 3.5+ hours, screen-sharing dead, dashboard split-brain (mini visible, MBP missing from home network roster). Phase DD (v0.10.96) ships a third independent watchdog scoped to `watchNetworkChanges`, plus 5s timeouts wrapping the two vendor-Nebula calls.

## Timeline

| Time (local, 2026-05-07) | Event | Source |
|---|---|---|
| 17:18:56 | `[agent home] closed 1 tunnels to force re-handshake on new network` — earlier network change handled cleanly | agent log |
| 17:22:22 | `[agent home] watcher alive (ticks: 1595, iface: en0, lastGap: 5s)` — last successful watcher tick | agent log |
| 18:00:45 | `[agent home] network change detected (iface: en0→), rebinding Nebula` — entered rebind block | agent log |
| 18:00:45 | `[agent home] selfEndpoints: physical-interface detect failed: ... lookup hopssh.com: no such host (falling back to all interfaces)` — heartbeat-side selfEndpoints log; this is from a separate goroutine but fires on the same `signalHeartbeat()` from the watcher | agent log |
| 18:00:54 | `[heartbeat work] wake/network-change triggered out-of-cycle heartbeat` — work enrollment's signalHeartbeat fired | agent log |
| 18:00:45 → 20:50:09 | NO `[agent home] watcher alive` entries for ~3h — watcher silent | agent log |
| 20:50:09 | `[renew home] cert OK, renewal due in 9h17m31s` — renewal goroutine still healthy (separate goroutine, Phase P watchdog catches its silence) | agent log |
| 20:50:09 | Mini's `/local/peers?enrollment=home` reports peers=1 (only the lighthouse 10.42.1.1); handshakes to `vpnAddrs=[10.42.1.11]` time out with `udpAddrs=[]` from lighthouse | mini local-API |
| 20:53 | User reports "MBP says connected to home, but mini unreachable; dashboard shows only mini" | session log |
| 21:00 | Diagnosis confirmed; Phase DD planned + implemented | this session |

## Root cause

Three cooperating facts:

1. **The deferred `recover()` in `watchNetworkChanges` (cmd/agent/nebula.go:197-201) only catches panics, not deadlocks.** A blocked goroutine waiting on a Nebula internal mutex (HostMap, listener) leaves the recover deferred but never reachable. No CRITICAL line ever fires.

2. **The renewal watchdog (Phase P) and keepalive watchdog (v0.10.36) cannot detect this class of failure.** Phase P watches `lastRenewalActivityAt`, which is stamped by the still-healthy renew goroutine. The keepalive watchdog requires `peers > 0 && probed > 0 && succeeded == 0` — but with the lighthouse-filter from Phase V, peers=0 for the mini, so the predicate is false (probed=0).

3. **The local-API status endpoint serializes per-enrollment state behind a lock that the watcher also holds.** When the watcher is wedged, `/local/status?enrollment=home` hangs. The desktop UI then falls back to the heartbeat-only "connected" heuristic and shows GREEN — UI lies.

## What broke as a consequence

- **Lighthouse selfEndpoint refresh stops** for the wedged enrollment. Peers querying the lighthouse for 10.42.1.11 receive `udpAddrs=[]`. Handshakes time out.
- **Sleep/wake recovery dies** for that enrollment. Next sleep/wake gap > 15s would normally trip `sleptAndWoke = true`, but the goroutine never returns from the prior tick's body.
- **Local-API status hangs** for the wedged enrollment. Desktop UI's "connected" indicator stays GREEN (heartbeat goroutine is fine on its own).
- **Mesh-keepalive becomes a no-op.** With peers=0, the keepalive cycle skips probing. Dashboard's RTT column goes empty for that enrollment.

## Phase DD fix (v0.10.96)

Three layers, mirroring the Phase P + v0.10.36 architecture as a third independent watchdog/timeout pair:

### F1 — `lastWatcherActivityAt` stamp + `runWatcherWatchdog` goroutine

- `cmd/agent/instance.go` adds `lastWatcherActivityAt time.Time` field (guarded by existing `activityMu`) plus `markWatcherActivity()` / `watcherActivityAge()` accessors. Mirrors the Phase P field exactly.
- `cmd/agent/nebula.go` calls `inst.markWatcherActivity()` at the TOP of every tick body in `watchNetworkChanges`.
- `cmd/agent/watcher_watchdog.go` (new) hosts `runWatcherWatchdog(ctx, inst)` — checks `inst.watcherActivityAge() > watcherSilenceThreshold` (= 3 min) every 30s. On trip: writes goroutine pprof dump to `<configDir>/<name>/watcher-stuck-<ts>.txt`, logs CRITICAL, invokes `inst.restartFn` (the v0.10.36 lifecycle infrastructure). 30-min cooldown.
- `cmd/agent/main.go::tryStartMeshInstance` spawns `go runWatcherWatchdog(inst.runCtx, inst)` alongside the existing renewal watchdog.

### F2 — Hard timeouts on vendor Nebula calls

`cmd/agent/watcher_watchdog.go` defines `runWithTimeout(name, label, deadline, fn)`. `cmd/agent/nebula.go:266-293` wraps `ctrl.RebindUDPServer()` and `ctrl.CloseAllTunnels(true)` with 5s deadlines. On timeout, the helper logs WARN and returns; the leaked goroutine is the accepted cost (alternative is the entire watcher wedging for hours). `inst.portmap.ReProbe()` is intentionally NOT wrapped — it's a non-blocking buffered channel send.

### F3 — UI honesty

`cmd/agent/local_api.go:484-487` adds a `watcherAlive` axis to the `Connected` derivation:
```go
watcherAge := inst.watcherActivityAge()
watcherAlive := watcherAge < heartbeatGrace || watcherAge > 100*365*24*time.Hour  // grace
es.Connected = certValid && watcherAlive && (activeFlow || recentHeartbeat)
```
Cold-start grace handles bundled mode + OS-stack fallback (where the watcher is never spawned).

### F4 — Tests

`cmd/agent/watcher_watchdog_test.go` (new) — 14 tests mirroring `renew_watchdog_test.go` 1-for-1 plus source-scan tripwires for the spawn site and the timeout wrappers. All pass.

## Architectural lesson

> **Any long-running goroutine that does load-bearing work in a per-iteration loop body MUST stamp activity at the TOP of each iteration; pair with a watchdog that asserts on stamp age. Blocking calls inside the loop body MUST have hard timeouts — `defer recover()` only catches panics, not deadlocks.**

The hopssh agent now has three independent watchdogs covering three classes of silent failure:

| Watchdog | What it watches | Trigger condition | Shipped |
|---|---|---|---|
| Renewal watchdog | `lastRenewalActivityAt` (renew goroutine) | silence > 6h on 24h cert | Phase P (v0.10.79) |
| Keepalive watchdog | TCP-probe success rate | 3 consecutive cycles all fail (with peers > 0) | v0.10.36 |
| **Watcher watchdog** | **`lastWatcherActivityAt` (watchNetworkChanges)** | **silence > 3 min** | **Phase DD (v0.10.96)** |

All three share the same primitive: stamp + age threshold + cooldown + `restartFn` via the `connectFn` closure in runServe.

## Workaround (no longer needed post-v0.10.96)

`sudo launchctl kickstart -k system/com.hopssh.agent` on the wedged Mac fully restored the home mesh within ~10s. With Phase DD shipped, the agent now self-heals within ~3 min of the wedge with no operator intervention.

## Open questions

- **Which vendor Nebula call wedged?** Couldn't determine from logs alone. The forensic goroutine dump from F1 will reveal this on next occurrence — likely either `RebindUDPServer` (if a recv loop is blocked) or `CloseAllTunnels` (if a handshake-mid-flight contends with a HostMap lock). Knowing the specific lock contention would inform whether to file an upstream Nebula PR or extend our vendor patches.
- **Why did the DNS resolution fail at exactly 18:00:45?** `dial udp4: lookup hopssh.com: no such host` for an interval of seconds. Could be Go runtime's `cgo_resolver` cache invalidation during interface flap, or macOS DNSExtensionConfig transitioning. Unrelated to the wedge itself (DNS error is propagated cleanly), but worth noting if it correlates with the wedge timing.

## Cross-references

- [[concepts/watchdog]] — the broader watchdog architecture this fix slots into
- [[phases/phase-s]] — Phase S 60s ticker for cert renewal, similar "stamp + threshold" pattern
- [[entities/mbp]] — affected machine
- [[runbooks/mesh-dead-kickstart]] — the kickstart workaround used pre-fix
