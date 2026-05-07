---
type: concept
title: Cert renewal lifecycle
status: current
last_compiled: 2026-05-03
sources:
  - cmd/agent/renew.go
  - cmd/agent/renew_sleep_test.go
  - internal/api/renew.go
  - docs/sleep-wake-plan.md (post-mortem section v0.10.33, v0.10.82)
---

# Cert renewal lifecycle

## TL;DR

24-hour Nebula certs, auto-renewed by the agent. **Phase S architecture (v0.10.82):** 60s wall-clock ticker checks `cert.NotAfter` each tick; renews when overdue. The renewal POST is **bearer-token authenticated**, NOT cert-authenticated, so kickstarting an agent with an expired cert immediately bootstraps a fresh one.

## Lifecycle

1. **Issuance** — at enrollment time, server issues a 24h cert via `internal/pki`, agent writes `node.crt` + `node.key` to `<configDir>/<network>/`.
2. **Polling** — `runCertRenewal` ticks every 60s (`renewalPollInterval` in `cmd/agent/renew.go`). Each tick:
   - Reads `<configDir>/<network>/node.crt` and parses it.
   - Computes `renewAt = (NotAfter - now) - safety_margin`.
   - If `renewAt <= 0` → call `renewNow` immediately.
3. **Renewal POST** — `POST https://<endpoint>/api/renew` with `{nodeId, agentToken}`. Server validates token via `Nodes.Get(nodeId)` + constant-time compare, issues a new cert, server-side stores updated `peer_state` and `agent_version`. Atomic write of new `node.crt` on the agent side.
4. **Hot reload** — `reloadNebula` swaps the running Nebula instance with the new cert. Phase v0.10.33 architecture: stop watcher → set `inst.svc=nil` → close OLD svc → wait for UDP port free (≤2s) → start NEW svc. Retry-with-backoff if start fails.

## Invariants

- **`time.After(longSleep)` is FORBIDDEN** at the outer renewal loop. Monotonic-anchored; freezes during macOS deep sleep. Phase S replaced it with `time.NewTicker(60s)`. Tripwire: `cmd/agent/renew_sleep_test.go::TestRenewLoop_UsesWallClockPolling` source-scans for the regression.
- **Renewal POST authenticated by token, not cert** — recovery from expired cert is always possible via `sudo launchctl kickstart -k system/com.hopssh.agent` (macOS) or `systemctl restart hop-agent` (Linux). Don't dead-end users on cert-expired by requiring cert-auth on the renewal endpoint.
- **`hop-agent status` reads from disk; running Nebula uses in-memory cert.** These can diverge silently across a botched reload (the v0.10.33 incident). The Phase P2 [[watchdog]] is the safety net.
- **Server must NOT push agent-owned values back via `/api/renew` response.** The v0.10.26 incident proved that: server pushed `listenPort` from a constant; agents on multi-enrollment hosts converged on the same port and second-instance bind failed. Server should only push values it owns (cert bytes); agent owns its own listen port, MTU, etc.

## Phase history

- **Phase P (v0.10.79)** — UI honesty fix + watchdog. `Connected = certValid && (peers > 0 || recentHeartbeat)`. Watchdog catches silent goroutine death. See [[concepts/watchdog]] for the watchdog architecture (Phase P shipped before the wiki bootstrap; details live in CLAUDE.md).
- **Phase P4 → R (v0.10.80 → v0.10.81)** — added then removed user-visible "Force renew" button. Engineering capability preserved at `POST /local/renew`; just no UI.
- **Phase Q (v0.10.80)** — FS watcher detects daemon respawn (e.g. `launchctl kickstart`) and re-attaches WebView to new endpoint. Sub-second.
- **Phase S (v0.10.82)** — 60s wall-clock ticker. Survives Clamshell Sleep cycles. See [[../phases/phase-s]].

## Backlinks

- [[sleep-wake]] — sleep-survival depends on this
- [[watchdog]] — backstop when this fails silently
- [[../phases/phase-s]]

## Long-form

- `docs/sleep-wake-plan.md` — v0.10.26 + v0.10.33 cascading-failure post-mortems
