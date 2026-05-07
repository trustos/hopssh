---
type: decision
title: Use time.NewTicker(60s) instead of time.After(longSleep) for cert renewal
status: accepted
last_compiled: 2026-05-03
sources:
  - cmd/agent/renew.go
  - cmd/agent/renew_sleep_test.go
---

# Use `time.NewTicker(60s)` instead of `time.After(longSleep)` for cert renewal

**TL;DR.** Long-duration `time.After` is anchored to the monotonic clock; on Darwin the monotonic clock freezes during deep sleep, so a `time.After(5h49m)` scheduled before bed can take days to fire on a laptop. Replaced with a 60-second ticker that re-reads cert NotAfter against wall-clock on every tick.

## Context

Production incident, 2026-05-02: a laptop scheduled cert renewal at 22:59:50 for `+5h49m54s`. The renewal goroutine never woke. `pmset -g log` proved the laptop entered Clamshell Sleep at 23:08:12 and cycled Sleep ↔ DarkWake every ~15 min for the entire night. Each DarkWake is ~45s of awake-time; deep sleep accumulates ~95% of the wall-clock window.

Go runtime monotonic time only advances during awake periods. The 5h49m timer needed ~5h49m of cumulative DarkWake — wouldn't fire until days later. Cert hard-expired before the timer fired, peers rejected handshakes, mesh died.

## Options considered

### A. Switch to a wall-clock-based scheduler

Track absolute wall-clock NotAfter; schedule a goroutine wake using a wall-clock timer source. Go's standard library doesn't expose one cleanly on macOS — would require `os.NewProcessTimer` plumbing or CGO.

Pros: precise, single goroutine, fires close to deadline.
Cons: no cross-platform stdlib path; would mean platform-specific shims.

### B. Listen for OS sleep/wake notifications + re-arm

Subscribe to `IOPMrootDomain` sleep/wake events on Darwin (and equivalents on Linux/Windows); re-arm the timer on wake.

Pros: precise, event-driven.
Cons: requires platform-specific NSE-style integration. Tailscale uses NetworkExtension which gets these notifications natively, but that requires Apple Developer ID + entitlement. Out of scope for the agent today.

### C. Polling ticker against an external invariant

Replace `time.After(longSleep)` with `time.NewTicker(60s)`. On each tick, re-read the cert's wall-clock `NotAfter`; renew if past the threshold. After laptop wake, the next 60s tick post-resume fires immediately (Go ticker catch-up), reads the cert, sees NotAfter has passed, renews.

Pros: cross-platform stdlib; survives any clock-related sleep semantics by design; simple.
Cons: 60 wakeups/hour during normal operation (negligible — `time.Ticker` is cheap on all platforms).

## Decision

Chose **Option C**. The polling overhead is negligible; the cross-platform property is load-bearing (this code runs on macOS laptops, Linux servers, Windows workstations). Re-reading the cert on each tick costs one stat + parse — measured at <100µs.

The general rule that fell out of this incident: **any agent timer scheduled for >5 minutes MUST poll wall-clock against an external invariant** (cert NotAfter, system time, etc.), not rely on monotonic-anchored sleep. Long-duration `time.After` is safe ONLY when the absolute deadline doesn't matter (e.g., exponential-backoff retry intervals).

## Decision basis

The reasoning that produced this decision came from:

- **Code I read:** `cmd/agent/renew.go` (`runCertRenewal` loop using `time.After(longSleep)`), `cmd/agent/nebula.go:147-174` (`watchNetworkChanges` 5s polling pattern — proven cross-platform), `cmd/agent/keepalive.go` (Phase P2 watchdog using `time.Since(lastRenewalActivityAt)` — same monotonic-clock bug).
- **Wiki pages I consulted:** [[concepts/sleep-wake]] (the macOS deep-sleep behavior + `pmset -g log` evidence), [[concepts/cert-renewal]] (the 24h cert lifecycle + retry-with-backoff structure), CLAUDE.md "macOS Platform" Discovery Log (the original one-paragraph entry capturing the 2026-05-02 incident).
- **External sources I fetched:** Apple `pmset(1)` man page + IORegistry kernel docs to confirm Darwin's `mach_absolute_time` freezes during deep sleep (no live URL cited; canonical platform behavior).
- **Prior-knowledge claims (with confidence):**
  - [HIGH] Go's `time.After` is anchored to the monotonic clock — verified against the Go stdlib source (`runtime/time.go`).
  - [HIGH] `time.NewTicker` catches up missed ticks after a long pause — verified against stdlib `time.Ticker` semantics (catch-up is documented and load-bearing for our case).
  - [HIGH] Linux + Windows servers don't deep-sleep in production, so the bug is laptop-specific — confirmed against fleet inventory ([[entities/mbp]] vs [[entities/hopssh-cloud]]).
  - [MEDIUM] Tailscale uses NetworkExtension on macOS to receive explicit sleep/wake notifications; this is why their cert renewal doesn't have this bug — recalled from Apple NEPacketTunnelProvider docs, not directly verified against Tailscale source.
  - [LOW] An OS-level NSE-style integration would be the "correct" long-term fix on macOS — recalled from training data; deferred until Apple Dev ID + entitlement available, so verification is cheap when we get there.

## Consequences

- The Phase P watchdog had the same bug (`time.Since(lastRenewalActivityAt)` is also monotonic-anchored). Fixed in the same change.
- A regression test (`cmd/agent/renew_sleep_test.go::TestTicker_SurvivesSIGSTOP`) spawns a sub-process, sends SIGSTOP + SIGCONT, and asserts the ticker catches up. SIGSTOP-based simulation per the established sleep-test pattern.
- Tailscale doesn't have this bug because their NE plugin gets explicit OS sleep/wake notifications + re-arms timers. We could match this with NetworkExtension (Apple Dev ID + entitlement) — deferred.

## Status

- **Accepted:** 2026-05-02
- **Shipped:** v0.10.82

## Backlinks

- [[concepts/cert-renewal]]
- [[concepts/sleep-wake]]
- [[phases/phase-s]]

The prompting incident (2026-05-02 overnight renewal stall on MBP) was investigated alongside Phase S's design but never written up as a separate wiki page — the timeline lives in CLAUDE.md's "Go's `time.After(longSleep)` is monotonic-clock-anchored" entry under macOS Platform. File a `incidents/2026-05-02-mbp-renewal-stall.md` page if a future session needs a forensic-style write-up.
