---
type: concept
title: End-to-end tests (real server, real agent)
status: current
last_compiled: 2026-11-24
sources:
  - internal/e2etest/server.go
  - internal/e2etest/peer.go
  - internal/api/e2e_test.go
  - cmd/agent/e2e_test.go
  - internal/client/recovery_e2e_test.go
  - internal/client/nebula_close_linux_test.go
  - internal/selfupdate/selfupdate_test.go
  - internal/db/db_test.go
  - internal/mesh/network_manager_test.go
---

# End-to-end tests

**TL;DR.** `internal/e2etest` builds the real `hop-server` from the tree and runs it as its own process on a temporary data directory. The agent under test is the real `main()`, run as a helper process. A test talks to both exactly as a browser, an agent or a peer does. Real Nebula, real SQLite, real HTTP; no root.

## Running them

```bash
go test -mod=vendor ./internal/api ./cmd/agent -run TestE2E -v
```

- They are skipped with `-short`.
- Every network's lighthouse has a fixed UDP port (42001 and up), so **one server runs at a time**. Test processes queue on `127.0.0.1:41999`. If UDP 42001 is taken by something else, they skip locally and fail when `CI` is set.
- `-race` also builds the server with the race detector.
- `HOP_E2E_COVER=<dir>` makes the servers write coverage data there (`go tool covdata percent -i=<dir>`).
- `HOP_E2E_OLD=<dir>` runs `TestE2E_Upgrade` against a released version: the directory holds its `hop-server` and its `hop-agent`. Without it that test is skipped. Build them from a worktree of the tag, after `go mod vendor` and `make patch-vendor`; set `PortmapEnabled = false` in its `internal/nebulacfg/defaults.go` first, or the old agent asks your router for a port.
- The kernel-TUN test needs root and a network namespace: `go test -mod=vendor -c -o /tmp/client.test ./internal/client`, then `sudo unshare -n sh -c 'ip link set lo up && /tmp/client.test -test.run KernelTun -test.v'`. CI does that; `make test` skips it.
- They never ask the router for a port mapping (`PortmapEnabled = false` in `TestMain`) and never call `enroll`, which would install a service.

## What is where

| Tests | What is real | What they check |
|---|---|---|
| `internal/api` `TestE2E_*` (18) | server, SQLite, HTTP, DNS | who may do what on **every** route, IDs of another network, enrollment, device flow, renewal, limits, accounts, invites, DNS, bundles, prune, capabilities, history, network delete, load, proxy, clipboard, restart, a database left by an older server |
| `cmd/agent` `TestE2E_*` (7) | server, agent process, Nebula, mesh peers | the whole life of a device, off for half a year, server without its database, deleted node, member's shell, deleted peer, upgrade from a released version and back |
| `internal/client` `TestE2E_*` (5) | client, Nebula; **fake** control plane | recovery, renewal during disconnect, keepalive, refused addresses, and that every loop which keeps a network up runs exactly once (also after a renewal and a watchdog restart) and not at all once it is turned off |
| `internal/client` `TestKernelTunClose` (Linux, root) | kernel TUN device, Nebula | a closed system-wide network leaves neither its port nor its device behind, even with a program from its terminal still running |
| `internal/selfupdate` `TestE2E_Update` | a program that updates itself | every way the checksum can be wrong or missing |
| `internal/db` | SQLite | the settings really are in effect |
| `internal/mesh` | Nebula lighthouses, SQLite | an idle network's lighthouse is closed and comes back without ending the server |

`TestE2E_Access` walks the router (`chi.Walk`) and fails for any `/api` route that its table does not mention, so a new route cannot be added without deciding who may call it.

## What covers what

Fixes in the tree (letters as in [[../incidents/2026-11-18-mbp-all-certs-expired]] and [[../open-questions]]). At least one test of each row fails when its fix is taken out.

| Fix | Test |
|---|---|
| E certs were renewed only after they expired | `renewal_time_test.go`; `TestE2E_Lifecycle` (a renewal on request) |
| B, C, D, F an expired or wedged network comes back | `TestE2E_OffForHalfAYear`, `TestE2E_LongOffMachineRecovers`, `TestE2E_DisconnectDuringRenewal`, `recovery_test.go`, `watchdog_restart_test.go`, `closed_instance_test.go` |
| A renewals don't share the login rate limit | `TestE2E_RateLimits` |
| a a server that can't read its database says 500, and the agent keeps trying | `TestE2E_RenewAndHeartbeat`, `TestE2E_ServerCannotReadItsDatabase` |
| L silent machines are not deleted | `TestPrune_OnlyWhenConfigured`, `TestE2E_Prune` |
| N the database settings are in effect | `TestOpen_AppliesSettings`, `TestE2E_ManyAgents`, `TestE2E_DeleteNetwork`, `TestE2E_OldDatabase` |
| M an ID of another network is refused | `TestE2E_IDsOfAnotherNetwork` |
| Q, S, T the proxy keeps the dashboard's session to itself and leaves no connections open | `TestE2E_Proxy` |
| O the port probe sees Nebula's port | `enrollments_test.go`, `renew_reload_port_release_test.go` |
| G, I, J, K vendor patches 27 to 29 | `nebula_close_test.go`, `TestReapIdleInstances` (G in the server), `TestE2E_KeepaliveOnUserspaceNetwork`, `TestE2E_RefusedAddressFirst`, vendored `udp` tests (I, J: macOS only) |
| an update without a matching checksum is refused | `TestE2E_Update` |
| U no data race in the agent | `TestE2E_Lifecycle` with `-race` (two of its three fields: by reading only) |
| V a closed network frees its UDP port (Linux, patch 31) | `TestUserspaceClose_DoesNotExit`, `TestReapIdleInstances`, `TestE2E_Lifecycle`, `TestE2E_LongOffMachineRecovers`, `TestE2E_DeleteNetwork`, run on Linux |
| W, X a closed system-wide network frees its device, and programs don't inherit it (patches 31, 32) | `TestKernelTunClose_ReleasesDeviceAndPort` (Linux as root; X on macOS: nothing) |
| a node's OS and CPU are corrected by its heartbeat, not only by enrollment | `TestSendHeartbeat_ReportsItsOwnMachine`, `TestE2E_HeartbeatCorrectsPlatform` |

Improvements that are only proposed:

| Proposal | Pinned by |
|---|---|
| Shell and exec only for admins or by grant; log the command | `KnownGap` in `TestE2E_MemberShell` |
| Reload the cert in place, so a renewal ends no sessions | `KnownGap` in `TestE2E_Lifecycle` |
| Revoke a deleted device at once | `KnownGap` in `TestE2E_DeletedPeer` |
| Give proxied pages their own origin | `KnownGap` in `TestE2E_Proxy` |
| 404 for outsiders, audit of removals, invite uses, DNS of deleted nodes | `KnownGap` in `TestE2E_Access`, `TestE2E_History`, `TestE2E_Invites`, `TestE2E_DNS` |
| Nebula v1.11 | nothing of its own; all of the above must still pass |
| Signed updates, `hop ssh`, resumable sessions, recording, TCP/443 | nothing, they don't exist yet |

## Known gaps

`e2etest.KnownGap(t, why, check)` states the *right* behaviour and expects it to fail today. When somebody fixes the gap the test turns red with "remove the marker". List: [[../open-questions]], "API gaps pinned by the E2E tests".

## Do they catch anything?

They found eleven product bugs nobody had seen: L, M, N, O, Q, S, T on their first runs, U under the race detector, V on the first run on Linux, W and X through the kernel-TUN test ([[../incidents/2026-11-18-mbp-all-certs-expired]], [[../open-questions]]).

**Planted bugs.** On 2026-11-23, 81 bugs were planted one at a time (13 in code the tests run in their own process, 68 in the server or the agent they start) and the test that should notice was run:

- 81 of 81 are caught now: 28 undo a fix of this round, 28 let the wrong people use a route, 13 let a token or a session work twice, late or without proof, 12 lift a rate limit.
- 4 were missed at first: an unreachable checksum server, a renewal and a heartbeat answered with 401 while the database fails, and enrollment without a rate limit. Each now has a check.
- 1 was "caught" for the wrong reason: the test failed on a flaky check of its own, not on the bug. Found by reading why each one failed. The check is fixed, and the bug is caught by the line that is about it.

**Clipboard and WebSocket** (2026-11-24). 16 bugs in the clipboard relay and 3 in the agent's WebSocket proxy, all caught. Before that, the clipboard's three rate limits were missed, because the table in `TestE2E_RateLimits` did not have those routes, and an expired clip was "caught" by a flaky 2 MB upload. Taking out only one of the two size checks changes nothing, so those two are not counted.

**Idle lighthouse** (2026-11-24). `TestReapIdleInstances` crashes the test process in 40 of 40 runs without patch 27 and in none of 40 with it; 7 bugs planted in the reaper are all caught. On Linux without patch 31 it fails too, because the port of the closed lighthouse stays taken (V). Its first version passed 3 of 3 without the patch: the race has to be widened (several goroutines that hold Nebula's lock), so a test for a race must be run against the bug often enough to know its odds.

**Wiring.** Another 14 bugs were planted in how the agent starts and stops its background work (a loop not started, started twice, or tied to the agent instead of to the network; a watchdog restart that only reconnects). Without `TestE2E_BackgroundWork` the package missed **11 of 14**, with it none. Four source-scan tests had been meant to notice; they were switched off with `t.Skip("… reimplemented in M3")` when the code moved to `internal/client` in May 2026 (`a37656f`), and M3 never came. `TestE2E_BackgroundWork` replaces three, the fourth is in `cmd/agent/main_test.go`.

**Coverage** (2026-11-24, macOS; statements run by any test, counting the server and the agent the tests start). The eight server-side packages went from 22.7 % before these tests to **72.9 %** (3158 of 4330); without the E2E tests it would be 23.9 %.

| Package | Without E2E | With E2E |
|---|---|---|
| `internal/api` | 23.9 % | 78.6 % |
| `internal/auth` | 52.3 % | 84.6 % |
| `internal/authz` | 92.3 % | 92.3 % |
| `internal/db` | 31.6 % | 72.8 % |
| `internal/mesh` | 1.1 % | 70.6 % |
| `internal/pki` | 0 % | 76.6 % |
| `internal/selfupdate` | 0 % | 0 % (`TestE2E_Update` runs in a copy of the test binary, which counts nothing) |
| `cmd/agent` | 41.3 % | 68.6 % |
| `internal/client` (not one of the eight) | 49.4 % | another 40.4 % seen from the agent process, overlapping |

A process that is killed writes no coverage. The numbers above are from a run in which the tests that crash a process on purpose sent it SIGTERM instead; measured as written it is 70.0 %. A line that ran is not a line that was checked: the planted bugs are the better number.

## Not covered

- Windows (never run; the client's tests don't compile there, `renew_sleep_test.go` uses `syscall.SIGSTOP`).
- Kernel TUN on macOS. On Linux the agent runs as root with one in [[netlab]], which is not in CI; `make test` covers only closing it.
- Linux ran in a local arm64 VM (2026-11-24: 604 tests in 10 packages, plus the kernel-TUN test as root 3 times, all passed) and in CI on amd64 (2026-11-24, 10 packages and the kernel-TUN test, passed). CI does not use `-race`. The client package as **root** on Linux was first run on 2026-11-27, and five path tests failed there and nowhere else — see [[../log]] "the client's tests as root on Linux". The same day `internal/api` and `cmd/agent` ran there as root with a real TUN device: as root the agent under test is in kernel TUN mode, and `TestE2E_Lifecycle` expects that (`agentTunMode`) — see [[../log]] "v0.11.31, and the agent's end-to-end tests as root".
- Real carriers, real sleep. NAT traversal, relays, roaming, outages and a frozen agent are in [[netlab]], against simulated routers.
- The desktop app and the dashboard.
- Old and new versions together, in CI: `TestE2E_Upgrade` needs `HOP_E2E_OLD`, so somebody has to run it before a release. On 2026-11-24 it passed against v0.11.26 in both directions, on the tree of v0.11.27. On macOS it checks only HTTP for the old agent, because v0.11.26 cannot be reached over the mesh there (I); with patch 28 added to that release the mesh part passes too.
- A release on a real fleet.
- Replacing the program on disk and restarting its service (`selfupdate.Apply`, `restartService`); asking GitHub for a newer version.
- Port mapping against a real router (NAT-PMP, PCP, UPnP). [[netlab]] has a simulated one that does NAT-PMP.
- The agent's `/upload` (no server route leads there), the QUIC probe (`hop-agent migration`), GitHub sign-in beyond what `auth_oauth_e2e_test.go` fakes.
- The clipboard on a real pasteboard: `TestE2E_Clipboard` checks the server's relay.

## Rules

1. A test that needs the server uses the server. A fake answers what its author expects.
2. A bug found by hand gets an E2E test that is red first.
3. A behaviour we know is wrong is pinned with `KnownGap`, not left out.
4. A planted bug counts as caught only after reading *why* the test failed.
5. Mutating **server** code under these tests needs `GOFLAGS=-overlay=<json>`: the server is built by a child process, which `go test -overlay` does not reach.
