---
type: concept
title: Stuck-data-plane watchdog (Phase P2)
status: current
last_compiled: 2026-05-03
sources:
  - cmd/agent/keepalive.go
  - cmd/agent/watchdog_test.go
  - cmd/agent/instance.go (restartFn closure)
---

# Stuck-data-plane watchdog

## TL;DR

Per-mesh-instance liveness loop. Every 90s, TCP-connect to each peer's mesh API listener (`<peerVPN>:41820`). When `peers > 0 && probed > 0 && succeeded == 0` for 3 consecutive cycles (~270s), fire `restartFn`: dump goroutine pprof to `<configDir>/<network>/stuck-state-<ts>.txt` then close + reconnect the Nebula instance. Cooldown 5 min between restarts.

## Why it exists

The data plane can hang silently in ways that don't crash the process:

- Goroutine deadlock somewhere in Nebula's vendored code
- Hung syscall (rare on macOS, possible on Linux under network reset)
- Swallowed panic in deferred recover (e.g. cert-renewal goroutine going silent — the May 1 2026 incident that motivated Phase P)
- Server-side outage long enough that all peers go silent (e.g. control-plane deploy bouncing the lighthouse for 7 min — verified 2026-05-02 v0.10.84 deploy)

The watchdog asserts on the *symptom* (no peer reachable for ~5 min) regardless of *cause*. Restart is the universal recovery primitive.

## Operation

| Stage | Action | File |
|---|---|---|
| Probe | TCP-connect to `<peerVPN>:41820` with 2s timeout, every 90s | `cmd/agent/keepalive.go::runMeshKeepalive` |
| Track | Increment `consecutiveStuckCycles` per failed cycle | same |
| Trip | When ≥3 consecutive: `writeStuckStateDump` then `restartFn()` | `cmd/agent/keepalive.go::watchdogTrip` |
| Cooldown | 5 min before next trip eligible | `cmd/agent/watchdog.go` |

## Forensic dump format

`<configDir>/<network>/stuck-state-<YYYYMMDDTHHMMSSZ>.txt`:

```
# hopssh agent stuck-state forensic dump
# enrollment: <network-name>
# captured: <ISO-8601 UTC>
# keepalive cycle: <cycle-number>
# consecutive stuck cycles: <count> (threshold 3)
# context: every keepalive probe failed for >3 cycles (~270s); auto-restart was triggered.

=== goroutine dump (debug=2 for full stacks) ===
<full pprof goroutine dump>
```

The dump is the artifact for post-mortem investigation. **Don't delete these files** — they're small (~50–200KB) and load-bearing the next time the same class of failure recurs.

## Known interactions

- **Control-plane deploy bounce** — when [[../entities/hopssh-cloud]] swaps containers, lighthouse UDP becomes unreachable for ~5–7 min. All peers fail probes simultaneously → watchdog fires within 4–5 min. Auto-recovers when new container comes up. **Functionally correct but noisy** (forensic dump on every deploy).
  - Optional polish (NOT shipped): suppress the stuck counter when a recent heartbeat returned HTTP 4xx/5xx. Tradeoff documented in [[../phases/phase-t]] postscript.
- **Sleep/wake** — the watchdog is suppressed inside its 5min cooldown so a wake event doesn't trigger an immediate restart-on-restart. Phase S [[cert-renewal]] handles cert-side recovery; the watchdog is the *peer-reachability* safety net.

## Test coverage

- `cmd/agent/watchdog_test.go` — 6 tests: dump-file written + format checked, cooldown respected, nil-restartFn graceful no-op, restartFn-called success path, source-scan for the load-bearing predicate, source-scan for `inst.restartFn` wired in BOTH boot path AND runtime connectFn.

## Backlinks

- [[../entities/hopssh-cloud]] — primary trigger source (deploy bounces)
- [[cert-renewal]] — sister architecture for renewal-side silent death
- [[sleep-wake]] — interacts during wake recovery
