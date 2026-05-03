---
type: phase
title: Phase S — renewal loop survives macOS deep-sleep
status: shipped
last_compiled: 2026-05-03
ship_version: v0.10.82
ship_date: 2026-05-02
sources:
  - cmd/agent/renew.go
  - cmd/agent/renew_sleep_test.go
  - docs/sleep-wake-plan.md (2026-05-02 update section)
  - CLAUDE.md macOS Platform discovery log
---

# Phase S — renewal loop survives macOS deep-sleep (v0.10.82)

## TL;DR

Replaced `time.After(longSleep)` in the cert-renewal outer loop with `time.NewTicker(60s)` + a wall-clock `cert.NotAfter` re-read each tick. Survives Darwin's `mach_absolute_time` freeze during Clamshell Sleep / DarkWake. Empirically validated 2026-05-02 on [[../entities/mbp]] across multiple real Sleep/DarkWake cycles.

## What broke

[[../entities/mbp]] cert renewal scheduled at `2026-05-01 22:59:50` for `+5h49m54s` never fired. `pmset -g log` proved Clamshell Sleep at `23:08:12 May 1` then Sleep ↔ DarkWake every ~15 min through the night. Cert hard-expired at `2026-05-02 06:14:27 UTC`. Mac mini rejected every handshake with `error="certificate is expired"`. Mesh dead until manual `launchctl kickstart`.

Root cause: `mach_absolute_time` (Darwin's monotonic clock) freezes during deep sleep. Go's `time.After(longSleep)` reads it, so a 5h49m timer needed ~5h49m of cumulative awake-time, which a sleeping laptop never accumulates. CLI agents (servers — never sleep) don't see this.

## Fix

`cmd/agent/renew.go::runCertRenewal`:

```go
const renewalPollInterval = 60 * time.Second

ticker := time.NewTicker(renewalPollInterval)
defer ticker.Stop()
for {
    renewAt, err := timeUntilRenewal(inst)
    inst.markRenewalActivity()
    if err == nil && renewAt <= 0 {
        renewNow()
    }
    select {
    case <-ctx.Done():
        return
    case <-ticker.C:
        // catch-up tick fires immediately post-wake
    }
}
```

Post-wake, the next 60s tick fires immediately (Go's `time.Ticker` semantics: missed ticks are coalesced, so the first post-resume `<-ticker.C` returns instantly), reads the cert, sees NotAfter has passed, renews via the existing retry-with-backoff `renewNow` closure.

## Test coverage

`cmd/agent/renew_sleep_test.go`:

| Test | Purpose |
|---|---|
| `TestRenewLoop_UsesWallClockPolling` | source-scan: ticker present, `time.After(renewAt)` absent. Tripwire for refactor regressions. |
| `TestRenewalPollInterval_ShortEnough` | bounds: 30s ≤ interval ≤ 2m |
| `TestRenewLoop_FiresOnWallClockExpiry` | source-scan: `renewAt <= 0` branch + `renewNow := func()` closure exist |
| `TestRenewLoop_RespondsToCtxCancelBetweenTicks` | runtime: ctx cancellation respected between ticks |
| `TestTicker_SurvivesSIGSTOP` | empirical: spawns sub-process with `HOPSSH_RENEW_SLEEP_TEST_CHILD=1`, SIGSTOPs 2s, SIGCONTs, asserts ticker fires post-resume. The canonical sleep simulation per the SIGSTOP pattern in CLAUDE.md (QEMU ARM and macOS pmset don't permit programmatic sleep on dev machines). |

## Verification (post-deploy)

- `pmset -g log` confirmed real Clamshell Sleep on [[../entities/mbp]] at 23:09:10, DarkWake/Maintenance Sleep cycles 23:10–23:14, real lid-open Wake at 23:14:48.
- Agent log shows `[renew home] cert OK ... next poll in 1m0s` every 60s through entire sleep window.
- 60s tick cadence proves the ticker survived sleep+wake.

## Architectural rule (added to CLAUDE.md)

> Any agent timer scheduled for >5 minutes MUST poll wall-clock against an external invariant (cert NotAfter, system time, etc.), not rely on monotonic-anchored sleep. Long-duration `time.After` is safe ONLY when the absolute deadline doesn't matter (e.g. exponential-backoff retry intervals).

## Backlinks

- [[../concepts/cert-renewal]]
- [[../concepts/sleep-wake]]
- [[../entities/mbp]]
