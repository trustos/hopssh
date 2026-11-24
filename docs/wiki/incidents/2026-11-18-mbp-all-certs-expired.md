---
type: incident
title: 2026-11-18 — MBP: every network "certificate expired" + "no live instance", never recovers
status: fixed in v0.11.27; the server runs it since 2026-11-24, no device does yet
last_compiled: 2026-11-24
sources:
  - internal/client/renew.go
  - internal/client/renewal_time_test.go
  - internal/client/recovery.go
  - internal/client/recovery_test.go
  - internal/client/recovery_e2e_test.go
  - internal/client/closed_instance_test.go
  - internal/client/watchdog_restart_test.go
  - internal/client/nebula_close_test.go
  - internal/client/client.go
  - cmd/agent/e2e_test.go
  - internal/api/e2e_test.go
  - internal/db/db.go
  - internal/db/status_sweeper.go
  - internal/api/router.go
  - internal/api/agent_ratelimit_test.go
  - internal/mesh/network_manager_test.go
  - patches/27-stop-marks-closed-first.patch
  - patches/28-darwin-send-outside-batch.patch
  - patches/29-service-close-netstack.patch
shipped_in: v0.11.27 (2026-11-24)
---

# 2026-11-18 — MBP: every network "certificate expired" + "no live instance", never recovers

**TL;DR.** After about 6 months, yavors-macbook-pro showed both enrollments the same way: `home` (10.42.1.6, in-app, :4242) and `work` (10.42.2.2, system-wide, :4243). Each had 0 peers, cert "—", a `certificate expired` banner and `no live instance "<name>"`. Reboot and Connect did not help.

- **Why the certs expired (E):** since v0.10.82 the agent has never renewed a cert *before* it expired. Every cert ran out first and was renewed seconds later. Any interruption longer than Nebula's 1 h tolerance around that moment left the cert expired.
- **Why it never recovered (B, C, D):** an agent that starts, or reconnects, with an expired cert kills the only goroutine that could renew it. The watchdog restarts were no-ops.
- **What can trigger it (A and others):** being off, asleep or offline past expiry, or the server answering 429. Which one hit this MBP is **not known**; its logs have not been seen.
- **And then the server deletes the machine (L):** a control plane on v0.10.51 to v0.11.26 removes every node that has been silent for 7 days. After that no agent can renew; the machine has to be enrolled again.

**This page replaces the 2026-11-18 version, which missed E, gave a wrong mechanism for B, and stated an unproven explanation for A as fact.** E was found on 2026-11-23 by running the released code. L, N and O were found later that day, by the first end-to-end tests against the real server; until then this page said that installing the fixed agent is enough, which is wrong for a machine that was silent for more than a week.

## Evidence

Released code (HEAD `bac0c68` = v0.11.26), real userspace Nebula, 179-day-expired cert, fake control plane:

| Control plane at boot | v0.11.26 | with the fix |
|---|---|---|
| answers in 80 ms | dead; an orphan Nebula holds the UDP port | live in 6 s |
| answers in 1.2 s | dead | live in 7 s |
| answers in 6 s | dead | live in 12 s |
| 502 for 45 s | dead, cert stays expired, POSTs stop at 4 | live in 46 s |
| 429 for 45 s | dead, same | live in 91 s |

The error v0.11.26 shows in the first three rows, "another hop-agent on this device is using the network port", is misleading: the port is held by the agent's own orphan.

## Defects

### E — the agent never renews before expiry (main cause)

`timeUntilRenewal` returned `remaining/2 + jitter(±remaining/10)` and the loop renewed when that was `<= 0`. Phase S (commit `0c52943`, v0.10.82, 2026-05-02) changed the loop from *sleep once for that long* to *recompute it every 60 s*. Half of what remains is never zero or less while anything remains, so the loop waited for expiry. Measured on HEAD: 0 of 1000 calls were due at any of 24 h … 5 s remaining, and in the real loop the first renewal POST arrived **20 s after the certificate expired**.

In normal running this is hidden: the POST succeeds and Nebula restarts with the new cert. Without it, there is no margin at all. It also explains why both networks fail together.

**Fix:** `renewalTime(notBefore, notAfter, seed)`, a fixed instant: half of the cert's life ±10 % (per-enrollment, stable). On a 24 h cert that leaves about 12 h to retry.

### B — an expired enrollment never recovers

Nebula refuses an expired cert (1 h tolerance from vendor patch 25). Since Phase NN, `Client.connect` tears down an instance whose Nebula failed to start, which cancels the renewal goroutine that `startInstance` spawned. Two outcomes, both seen in the experiment:

- the POST is cancelled, or fails, and nobody tries again;
- the POST completes after the teardown and the goroutine starts a Nebula on the closed instance: an **orphan** that holds the UDP port until the process ends, so every reconnect fails.

**Fix:** `ensureUsableCert` renews before Nebula starts, and a per-enrollment recovery loop on the `Client` retries (30 s → 10 min, wall-clock waits) until it connects, the user disconnects or leaves, or the client stops. After a 401 it asks once an hour: that answer usually means "deleted", but servers up to v0.11.26 also gave it when their database was busy (N).

### C — every watchdog restart was a no-op since v0.10.36

`restartFn` was `connect(name)`, whose "already up" check returns nil whenever the instance has a Nebula. Watchdogs only fire while it has one. **Fix:** `restartInstance` tears that instance down first.

### D — work on a closed instance

An in-flight renewal or `scheduleRetryReload` could start Nebula on an instance that had been closed (the orphan in B, and a resurrected network after Disconnect). **Fix:** `closed` + `startMu` on the instance, `setSvc` refuses on a closed one, and the POST is bound to the instance's lifetime.

### F — in the 2026-11-18 fix itself (never released)

Making restarts real exposed four problems: cooldown state lived on the instance being replaced; Phase EE's exit-after-3 could no longer fire; the keepalive of a userspace network dialled through the OS stack, so it always failed and would have restarted the network about every 5 min; a wedged teardown could block forever while holding `connectLock`. **Fix:** restart backoff per enrollment (5 min doubling to 6 h, reset after 12 h), escalation from the recovery loop, keepalive through the netstack, a 20 s teardown bound.

### A — renewals share the anonymous login limiter

`/api/renew` and `/api/heartbeat` were behind `publicRL` (10/min, burst 20, per IP). Every device behind one NAT shares a bucket. **Seen live on production on 2026-11-23 18:34–18:39 local: single requests to `/api/renew` and `/api/auth/status` got 429.** What drained it was found in the server's log on 2026-11-24: devices that the server had deleted (L). A released agent gives up on a 401 and is started again, and every start sends 3 renewals and 3 heartbeats, about every 14 s, for weeks. **Fix:** a separate limiter for the two token-authenticated endpoints (600/min), and the fixed agent asks once an hour after a 401.

### G — closing an in-app network could end the whole agent

`Control.Stop()` cancelled the context before marking the interface closed, so the TUN reader saw EOF first and called `os.Exit(2)`. Released code: 1 of 10 runs of 20 closes, and 6 of 6 runs once the hostmap was busy. On 2026-11-23 it also happened by itself in the upgrade test: a released agent, asked to renew, logged `Error while reading outbound packet … EOF` and was gone. Every Disconnect, watchdog restart and renewal of an in-app network takes this path. So does the server: its idle reaper closes a lighthouse after 2 h without a device. Production's server went down 8 times that way between 10 and 16 June 2026, each time about 16 s before the restart in the log. `TestReapIdleInstances` crashes in 40 of 40 runs without the patch. **Fix:** vendor patch 27 (upstream: PR #1661, v1.11.0).

### H — an empty `nebula.yaml` panicked the agent on every boot

**Fix:** `parseNebulaYAML` returns an error. Nothing regenerates a lost file yet (open question).

### I, J — macOS held back or dropped outbound packets

Found while testing F. See [[../concepts/macos-udp-send]]. **Fix:** vendor patch 28. In the upgrade test an unmodified v0.11.26 agent with an in-app network was not reachable over the mesh within 2 minutes on macOS; the same release plus patch 28 was reachable in seconds.

### K — every closed in-app network leaked its netstack

`service.Close()` never closed the gvisor stack: 18 goroutines (one per CPU) and about 1.26 MB per close, and reads on its connections blocked forever. Each renewal closes one. **Fix:** vendor patch 29.

### L — the server deletes machines that are off for a week

`internal/db/status_sweeper.go` hard-deleted every node offline for more than 7 days, hourly, since v0.10.51 (`7052a44`, 2026-04-28; `HOPSSH_NODE_PRUNE_AGE_HOURS`, 0 = off). It was meant for zombies in the topology view. Production set no override until 2026-11-24. By then no device was left: the last two were deleted on 2026-08-07. A laptop in a drawer for a week loses its identity: `/api/renew` answers 401 for good, and its mesh IP can be given to another machine. **Fix:** automatic pruning is opt-in; the dashboard's "Clean up" button stays, and its confirmation says what it does.

### N — none of the database settings were in effect

`db.Open` passed `_busy_timeout=…&_journal_mode=WAL&_foreign_keys=on…`. The driver (`modernc.org/sqlite`) reads only `_pragma=…` and ignores the rest without a word. In effect were `journal_mode=delete`, `busy_timeout=0`, `foreign_keys=0`, `synchronous=2`. So a read that met a commit failed at once with "database is locked", which the handlers turned into 401, 403 or 404, and `ON DELETE CASCADE` never ran. Under load 353 of 53,532 requests failed that way. **Fix:** `_pragma=`; Renew and Heartbeat answer 500 when the database fails (a); `network_events` skips an event of a deleted network instead of losing the batch.

### O — the port probe did not see Nebula on macOS

`net.ListenUDP("udp", 0.0.0.0:port)` is a dual-stack bind in Go, and macOS allows it next to Nebula's IPv4 socket. So `udpPortAvailable` and `waitForUDPPortFree` always said "free" there: a second agent picked 4242 again, and a renewal never waited for the port. The tests held the port the same wrong way. **Fix:** `"udp4"`.

### U — fields written while other goroutines read them

`runPathQuality` created `inst.pathQuality` while the heartbeat and the local API read it; `startPortmap` set `inst.portmap` after the heartbeat and the watcher had started; `inst.onRestart` was set after the renewal goroutine had started. Present at v0.11.26. Found by running `TestE2E_Lifecycle` with `-race` (the first one; the other two are the same mistake, hidden there because the tests turn the port mapping off). No symptom is known. **Fix:** the first is created with the instance, the second is an `atomic.Pointer`, the third is set before the goroutines start.

### V — on Linux, closing a network did not release its UDP port

`close(2)` on a UDP socket does not wake a thread that is blocked in `recvmmsg`, and the kernel keeps the socket, and so the port, until that thread returns: until the next packet arrives. Nebula never waits for its reader. So on Linux every renewal of a quiet network ("address already in use", then the retry schedule), Disconnect followed by Connect, and deleting a network and creating one again could fail. In the last case the API still answers 201 while the lighthouse is not running. Released versions have it, and production runs on Linux: after an idle close its log has networks whose lighthouse was not started again for 39 minutes to 2 hours, and one that never was (fits V; the error itself is not logged, see [[../open-questions]]). Found by the first run of the E2E tests on Linux (6 tests failed). **Fix:** vendor patch 31: shut the socket down, wait up to 1 s for the reader, then close it.

### W — on Linux, closing a system-wide network did not remove its device

Same shape: a thread blocked in a read of `/dev/net/tun` keeps the device. The next start failed with "could not set tun device name … device or resource busy". A root agent on Linux is system-wide by default, so each renewal of a quiet network could end there. Upstream fixed W in v1.11.0 and V in v1.12.0. **Fix:** vendor patch 31 opens the device non-blocking, so Go's poller ends the read.

### X — programs started from the web terminal inherited the network device

The TUN descriptor was opened without close-on-exec (Linux and macOS). A program that outlives the shell (`tmux`, `nohup`) kept the device, so the network could not start again after the next renewal, and it could read and write the mesh's unencrypted traffic. Verified on Linux; on macOS the same code, not run. **Fix:** vendor patch 32.

## Tests

Server and agent together, as real processes: see [[../concepts/e2e-tests]]. `cmd/agent` `TestE2E_OffForHalfAYear` is this incident with the real server.

- `renewal_time_test.go` (E), `closed_instance_test.go` (D), `watchdog_restart_test.go` (F), `recovery_test.go` (B, C), `nebula_close_test.go` (G, K).
- `recovery_e2e_test.go`: a real `Client` with real userspace Nebula against a fake control plane. `TestE2E_LongOffMachineRecovers` is this incident. Skipped with `-short`.
- `internal/api/agent_ratelimit_test.go` (A).
- `nebula_close_test.go`, `TestE2E_Lifecycle` and `TestE2E_DeleteNetwork` on Linux (V); `nebula_close_linux_test.go`, as root in a network namespace (W, X).
- Mutation testing: 23 of 23 agent mutations and 5 of 5 patch 28 mutations caught; the G, I, J and K tests fail without their patch. I and J only show on macOS, and CI is ubuntu-only.

## Recovering a machine in this state

1. **Is the machine still listed in the dashboard?** If it was silent for more than 7 days it is probably gone (L). Then leave each network on the machine and enroll it again. Nothing else works.
2. If it is listed: install the fixed agent. It renews before starting Nebula and keeps retrying.

**One restart of a v0.11.26 agent does not help** (first three rows above). Two restarts at least 15 s apart may, because the orphan's renewal does write a fresh cert, but that is inferred, not tested.

UI strings: `certificate expired` is the on-disk cert as read by `enrollmentStatus`. `no live instance` is a 404 from a local-API handler. "in-app"/"system-wide" is the TUN mode (`Connected.svelte`).

## Lessons

> **A deadline must be a fixed instant.** "Half of what remains", recomputed on every poll, never arrives. Test that the thing happens *before* the deadline, not only that it happens.

> **Recovery must not live inside the thing that failed.** It belongs one level up, with its own lifecycle.

> **Test a restart primitive in the state it is called in.** Watchdogs fire while things are running.

> **Run the released code before explaining it.** The first analysis of this incident read the code, found three real bugs and missed the main one; the experiment found it in minutes.

> **Authenticated machine traffic must not share an anonymous per-IP limiter.**

> **A test against a fake server cannot find a bug in the server.** L and N, the two that decide whether this machine can come back, were invisible until the tests ran the real one.

> **Run the tests on the system you ship for.** Everything passed on the Mac. The first run on Linux, where the server runs, failed in six places.

> **Read a setting back.** N stood for the life of the project because nothing ever asked SQLite what it was using.

> **Never delete an identity because it is quiet.** Being off is normal for a laptop.

## Open

See [[../open-questions]], 2026-11-23 entries.

## Cross-references

- [[../concepts/cert-renewal]], [[../concepts/watchdog]], [[../concepts/macos-udp-send]]
- [[../entities/mbp]], [[../runbooks/mesh-dead-kickstart]], [[2026-05-07-mbp-watcher-wedge]]
