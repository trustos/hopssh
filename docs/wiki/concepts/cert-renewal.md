---
type: concept
title: Cert renewal lifecycle
status: current
last_compiled: 2026-11-23
sources:
  - internal/client/renew.go (was cmd/agent/renew.go pre-Phase-NN)
  - internal/client/renew_sleep_test.go
  - internal/client/renew_watchdog_test.go
  - internal/api/renew.go
  - docs/sleep-wake-plan.md (post-mortem section v0.10.33, v0.10.82)
---

# Cert renewal lifecycle

## TL;DR

24-hour Nebula certs, auto-renewed by the agent. A 60 s wall-clock ticker (Phase S, v0.10.82) reads the cert and renews once a **fixed instant**, half of its life ± 10 %, has passed. The renewal POST is **bearer-token authenticated**, NOT cert-authenticated, so an expired cert can always be replaced.

> **v0.10.82 to v0.11.26 never renewed before expiry.** See the first invariant below and [[../incidents/2026-11-18-mbp-all-certs-expired]]. Not released yet: everything on this page about `renewalTime`, pre-start renewal, the recovery loop and `restartInstance`.

## Lifecycle

1. **Issuance** — at enrollment time, server issues a 24h cert via `internal/pki`, agent writes `node.crt` + `node.key` to `<configDir>/<network>/`.
2. **Polling** — `runCertRenewal` ticks every 60s (`renewalPollInterval` in `internal/client/renew.go`). Each tick:
   - Reads `<configDir>/<network>/node.crt` and parses it.
   - Computes `renewalTime(NotBefore, NotAfter, nodeID)`: half of the life, ± 10 % chosen by a hash so agents don't renew together.
   - If that instant has passed → `renewNow` (12 retries, 1 → 30 min backoff on wall-clock waits). At most one successful renewal per hour, in case the local clock is far ahead.
3. **Renewal POST** — `POST https://<endpoint>/api/renew` with `{nodeId, agentToken}`. Server validates token via `Nodes.Get(nodeId)` + constant-time compare (unreleased: if its database can't be read it answers 500; up to v0.11.26 it answered 401, which means "deleted"), issues a new cert, server-side stores updated `peer_state` and `agent_version`. On the agent side the new `node.crt` and `node.key` are written as one pair, under `startMu` — see the pair invariant under Invariants.
4. **Hot reload** — drops every tunnel of that network, about every 12 h (until v0.11.26: every 24 h). Reloading the cert in place would avoid it; see [[../open-questions]]. `reloadNebula` swaps the running Nebula instance with the new cert. Phase v0.10.33 architecture: stop watcher → set `inst.svc=nil` → close OLD svc → wait for UDP port free (≤2s) → start NEW svc. Retry-with-backoff if start fails.
5. **Pre-start renewal (Phase HH, unreleased)** — `connectLocked` calls `ensureUsableCert` *before* Nebula starts. If `node.crt` or `node.key` is expired, missing, unparseable, or they don't belong together, it renews via `fetchAndInstallCert`, which installs the cert without touching Nebula. Nebula refuses an expired cert, and the per-instance renewal goroutine dies with the instance's failed start, so this is the only renewal that can run for a dead enrollment.
6. **Recovery loop (Phase HH)** — if an enrollment that should be up fails to connect, `connectAndSupervise` → `scheduleRecovery` keeps retrying on its own. There is one loop per enrollment, owned by the `Client` and not by the instance. Backoff is 30 s → 10 m (capped, ±20 % jitter) on a wall-clock-polled wait. It stops on success, user Disconnect or Leave, Stop, or enrollment removal. After a 401 (node deleted or token revoked) it asks only once an hour: that answer is usually final, but servers up to v0.11.26 also gave it when their own database was busy.

## Invariants

- **The renewal deadline must be a fixed instant for a given cert.** Phase S recomputed "half of what remains" on every poll. That is in the future for as long as anything remains, so the POST was sent only after expiry (measured: 20 s after). It worked as long as nothing interrupted that moment. Tripwires: `renewal_time_test.go`, which checks that the real loop renews *before* expiry.
- **`time.After(longSleep)` is FORBIDDEN** at the outer renewal loop. Monotonic-anchored; freezes during macOS deep sleep. Phase S replaced it with `time.NewTicker(60s)`. Tripwire: `internal/client/renew_sleep_test.go::TestRenewLoop_UsesWallClockPolling` source-scans for the regression.
- **Renewal POST authenticated by token, not cert.** Don't dead-end users on cert-expired by requiring cert-auth on the renewal endpoint. Restarting the agent is enough to recover only with the Phase HH agent: v0.11.26 and earlier stay dead (experiment in the incident page).
- **`hop-agent status` reads from disk; running Nebula uses in-memory cert.** These can diverge silently across a botched reload (the v0.10.33 incident). The Phase P2 [[watchdog]] is the safety net.
- **`/api/renew` + `/api/heartbeat` must never share the anonymous `publicRL`** (10/min/IP). All agents behind one NAT share the bucket. Production answered single renewals with 429 on 2026-11-23: devices the server had deleted kept asking (resolved in [[../open-questions]]). They use the dedicated `agentRL`. Tripwire: `internal/api/agent_ratelimit_test.go`. See [[../incidents/2026-11-18-mbp-all-certs-expired]].
- **`node.crt` and `node.key` are written and read as one pair.** Nebula opens them one after the other (`vendor/github.com/slackhq/nebula/pki.go:307` and `:320`) and refuses the combination if they don't match (`ErrBadPrivKey`). The renewal loop is already running while `connect` holds `startMu` and Nebula reads, so a renewal landing in that window left the new cert next to the old key and the whole start failed: `Could not load client cert … private key is not a pair with public key in nebula cert`, network on the OS stack, Nebula only on the retry. That is not a corner case — it is every machine that was off for more than half a cert's life, 12 h of the 24 h we issue. `writeCertPair` (`internal/client/renew.go`) takes `startMu` for both writes; `certInstallMu` alone only ordered renewals against each other. Tripwire: `renew_pair_test.go::TestRenewalWaitsForTheStartToReadThePair` (fails 3 of 3 without the lock). A crash between the two writes is a different case and is still caught by `certStartProblem` ("mismatched") on the next start. One writer is still outside the lock: `ensureUsableCert` (`recovery.go`) writes the pair before a start, and holds nothing — in the normal case no start of that name is in flight then, but a user `Connect` racing the recovery loop for the same name is the same window. Closing it needs a lock keyed by name, not by instance, because the recovery path has no instance to lock.
- **Nothing may start Nebula on a closed instance.** A renewal answered after the teardown used to start an orphan that held the UDP port until the process ended. Tripwires: `closed_instance_test.go`, `TestE2E_DisconnectDuringRenewal`.
- **Only the owner deletes a node.** A deleted node gets 401 for good, so nothing on the agent can bring it back. Servers v0.10.51 to v0.11.26 delete nodes that were silent for 7 days; pruning is opt-in in the tree (`HOPSSH_NODE_PRUNE_AGE_HOURS`). Tripwires: `TestPrune_OnlyWhenConfigured`, `TestE2E_Prune`.
- **Recovery must not live inside the instance it recovers.** Tearing down a failed instance cancels its `runCtx` and every goroutine under it, the renewal loop included. Pre-start renewal and the recovery loop sit on the `Client`. Tripwire: `TestConnectLocked_RenewsBeforeStartingNebula`.
- **`restartFn` must actually restart.** It is `c.restartInstance(name, inst)`, which tears down that instance before reconnecting. A plain `connect(name)` short-circuits on "already up", and watchdogs only fire while Nebula is up. Tripwire: `TestConnectLocked_RestartFnIsRealRestart`.
- **Server must NOT push agent-owned values back via `/api/renew` response.** The v0.10.26 incident proved that: server pushed `listenPort` from a constant; agents on multi-enrollment hosts converged on the same port and second-instance bind failed. Server should only push values it owns (cert bytes); agent owns its own listen port, MTU, etc.

## Phase history

- **Phase P (v0.10.79)** — UI honesty fix + watchdog. `Connected = certValid && (peers > 0 || recentHeartbeat)`. Watchdog catches silent goroutine death. See [[concepts/watchdog]] for the watchdog architecture (Phase P shipped before the wiki bootstrap; details live in CLAUDE.md).
- **Phase P4 → R (v0.10.80 → v0.10.81)** — added then removed user-visible "Force renew" button. Engineering capability preserved at `POST /local/renew`; just no UI.
- **Phase Q (v0.10.80)** — FS watcher detects daemon respawn (e.g. `launchctl kickstart`) and re-attaches WebView to new endpoint. Sub-second.
- **Phase S (v0.10.82)** — 60s wall-clock ticker. Survives Clamshell Sleep cycles, but introduced the deadline that never arrives. See [[../phases/phase-s]].
- **Phase HH (v0.11.27, 2026-11-24)** — renewal at a fixed instant, a dedicated agent rate limiter, pre-start renewal, a per-enrollment recovery loop, a real `restartFn`, and no work on closed instances. Fixes "every network certificate expired, never recovers". See [[../incidents/2026-11-18-mbp-all-certs-expired]].

## Backlinks

- [[sleep-wake]] — sleep-survival depends on this
- [[watchdog]] — backstop when this fails silently
- [[../phases/phase-s]]

## Long-form

- `docs/sleep-wake-plan.md` — v0.10.26 + v0.10.33 cascading-failure post-mortems
