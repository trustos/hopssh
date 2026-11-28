---
type: concept
title: End-to-end tests (real server, real agent)
status: current
last_compiled: 2026-11-28
sources:
  - internal/e2etest/server.go
  - internal/e2etest/peer.go
  - internal/api/e2e_test.go
  - internal/api/audit_e2e_test.go
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
| `internal/api` `TestE2E_*` (30) | server, SQLite, HTTP, DNS | who may do what on **every** route, what the audit log records of every change, firewall rules and rule sets, which rules each device's heartbeat answer carries and what it reported, IDs of another network, enrollment, device flow, renewal, limits, accounts, invites, DNS, bundles, prune, capabilities, history, network delete, load, proxy, clipboard, restart, a database left by an older server |
| `cmd/agent` `TestE2E_*` (11) | server, agent process, Nebula, mesh peers | the whole life of a device, off for half a year, server without its database, deleted node, member's shell, deleted peer, upgrade from a released version and back, a firewall rule saved in the dashboard and applied by the device |
| `internal/client` `TestE2E_*` (22) | client, Nebula; **fake** control plane | recovery, renewal during disconnect, keepalive, refused addresses, which paths a peer is sent on, the firewall blocks between two peers, a firewall policy applied in place from the heartbeat answer (open and new flows, refusals, no new handshake), reconnecting into a TIME_WAIT, and that every loop which keeps a network up runs exactly once (also after a renewal and a watchdog restart) and not at all once it is turned off |
| `internal/client` `TestKernelTunClose` (Linux, root) | kernel TUN device, Nebula | a closed system-wide network leaves neither its port nor its device behind, even with a program from its terminal still running |
| `internal/selfupdate` `TestE2E_Update` | a program that updates itself | every way the checksum can be wrong or missing |
| `internal/db` | SQLite | the settings really are in effect |
| `internal/mesh` | Nebula lighthouses, SQLite | an idle network's lighthouse is closed and comes back without ending the server |
| `frontend` `npm test` (11) | the Firewall tab's script, compiled by Svelte; **fake** API, no markup | an unsaved change survives attach, detach and rule-set changes and is saved against its own version, a stale version (412) reloads, a taken name (409) keeps what was typed, how an unknown device is named, what each device runs (applied, pending, refused, no report) |

`TestE2E_Access` walks the router (`chi.Walk`) and fails for any `/api` route that its table does not mention, so a new route cannot be added without deciding who may call it. `TestE2E_Audit` does the same for the audit log: every route is in exactly one of `auditedRoutes`, `notAudited` (with the reason) and `readOnly` (the GETs that change nothing), so a GET is not taken on trust either.

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
| what a device reports as its own addresses reaches its peers only if a peer can send to it, each once, at most 10 per address family, in its order, with its own lifetime | `TestE2E_SelfEndpoints`, `TestPeerCanSendTo`, `TestSelfEndpointHints_LifetimeStaysWithItsAddress`, `TestSelfEndpointHints_HugeLifetime` |
| a connection to a userspace node that meets its TIME_WAIT is answered (patch `gvisor-time-wait`) | `TestTimeWaitReopen` (also under `-race`), `TestE2E_ConnectAgainAfterTimeWait`, `TestE2E_ProxyBackToBack` |
| every change to a network, its nodes, its DNS and its people is in the audit log with who, where and what; a delete that deleted nothing, or lost a race, writes none; no entry holds a token or code | `TestE2E_Audit` |
| a network's firewall rules and its owner's rule sets: who may change or attach them, versions (428, 412), a set name already taken (409), strict reading and limits, their audit entries, what a network's delete removes; the agent's block for each preset; the tab keeps unsaved changes ([[../decisions/firewall-rule-sets]]) | `TestE2E_Firewall`, `TestE2E_Access`, `TestE2E_Audit`, `TestE2E_DeleteNetwork`, `TestE2E_FirewallPreset`, `TestFirewall_TodaysTemplates`, `frontend/tests/firewall-tab.test.mjs` |
| which rules each device of a managed network gets in its heartbeat, that it applies them in place (open and new flows, no new handshake), refuses what it must and says why, waits during a start or after its file changed, that a network from before migration 009 moves from TCP only to everything and back, and what the tab shows for each device ([[firewall-rules]]) | `TestE2E_FirewallDelivery`, `TestE2E_FirewallTCPNetworkToAll` (both in `internal/api` and `internal/client`), `TestManagedFirewall`, `TestRecordReport`, `TestE2E_FirewallApplied`, `frontend/tests/firewall-tab.test.mjs` |
| a device's switch "Block incoming": that it blocks and unblocks in place, keeps what hopssh needs, survives a restart and a start while it was down, keeps a policy that comes meanwhile for later, and that every heartbeat says so; what the dashboard shows as reaching a device ([[firewall-rules]], "On the device") | `TestE2E_BlockIncoming` (`internal/client`, `cmd/agent`), `TestE2E_FirewallReach`, `frontend/tests/firewall-reach.test.mjs`, `frontend/tests/firewall-tab.test.mjs` |

Improvements that are only proposed:

| Proposal | Pinned by |
|---|---|
| Shell and exec only for admins or by grant; log the command | `KnownGap` in `TestE2E_MemberShell` |
| Reload the cert in place, so a renewal ends no sessions | `KnownGap` in `TestE2E_Lifecycle` |
| Revoke a deleted device at once | `KnownGap` in `TestE2E_DeletedPeer` |
| Give proxied pages their own origin | `KnownGap` in `TestE2E_Proxy` |
| 404 for outsiders, invite uses, DNS of deleted nodes | `KnownGap` in `TestE2E_Access`, `TestE2E_History`, `TestE2E_Invites`, `TestE2E_DNS` |
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

**TIME_WAIT** (2026-11-27). A connection to a userspace node that met one of the node's TIME_WAITs hung until the server's 30 s timeout, about once in a few hundred requests (gVisor, see `patches/gvisor-time-wait.patch`). Three tests, red without the patch: `TestTimeWaitReopen` (two bare gVisor stacks, the second connection from the same port) every time, also under `-race`, and a half fix that lets only the resend after 1 s through; `TestE2E_ConnectAgainAfterTimeWait` (internal/client, real Nebula, 2000 random ports) 6 of 6; `TestE2E_ProxyBackToBack` (internal/api, real server, 2000 health checks) 5 of 5 on the Mac and 5 of 5 on Linux. Under `-race` a connection through Nebula's userspace stack takes about 0.5 s against well under 1 ms without it (profiled: the race detector's instrumentation and the scheduler, not our code), so the two through Nebula skip themselves there (`raceEnabled`), and a test that must run under `-race` goes to bare stacks, where it takes 0.1 to 0.4 s. A bar shorter than gVisor's 1 s resend over Nebula would fail on one lost packet on a loaded machine (found by review, not seen); the bare stacks hand packets over in memory, so their 500 ms bar is safe.

**A UDP probe through gonet** (2026-11-27). A read does not wake for the ICMP "port unreachable"; the next write returns it as "connection refused". With one deadline for two tries, an answer that came while the first read waited (half a second under `-race`) left the second write to fail on the deadline: `TestE2E_FirewallUDPBetweenAgents` failed 3 of 3 under `-race`. Each try now has its own deadline (`udpReaches`); a block that drops UDP is still caught, with and without `-race`.

**Audit log** (2026-11-27). `TestE2E_Audit` was red before the fix with 20 missing entries. 21 bugs planted one at a time, all caught, each by the line about it: an entry not written (member removal, DNS delete, shell, node delete), the creator of a node never stored, the wrong user or network on a machine's enrollment or a bundle download, the old name lost from a rename, a stopped forward without its node, an entry for a delete that deleted nothing or lost a race, an invite code, agent token or user code in the details (the secrets check), the server not handing the store to a handler, and a route left out of every list or a GET dropped from `readOnly` (the route gate). The racing delete was caught in 3 of 10 runs while each request opened its own connection, and in 10 of 10 once the 16 connections were opened first, so that the deletes reach the server together (`burst`).

**Firewall rules** (2026-11-27). 33 bugs planted in phase 1 of [[../decisions/firewall-rule-sets]], all caught, each by the line about it: a stale version accepted or answered 409, a version not raised in the database, the preset not stored, a node of another network or no node at all accepted (in the network's rules, in a set's change, on attach), a deleted node that blocks every later change, an attached set deleted, a repeated attach or a detach of nothing reported (and audited) as done, attachments that outlive their network, a plain member changing the rules, an admin attaching sets, the owner attaching another user's set, `If-Match` optional or weak, unknown fields, presets, names, ports, ranges, ICMP ports, counts and descriptions not checked, a rename to a taken name answered 500, a change not audited, an admin told they may attach. Four were first "caught" only because they did not compile: a build failure notices nothing, so they were rewritten until they compiled. Three showed only `TestE2E_Access`'s known-gap notes, which hid the line that failed. 10 bugs planted in the Firewall tab's script are all caught by `npm test`: an attach, a detach, or a rule set saved or deleted that drops an unsaved change; no unsaved change kept at all; a kept change saved against the new version; a refused save of the network that closes the rule-set editor; a stale rule set taken for a taken name, or not reopened; a device of another network called deleted. The bound on the ports a device's rules cover (`fwpolicy.MaxPorts`, the "wide" section of `TestE2E_Firewall`, `TestValidateRules_Ports`, `TestPorts`): 9 more bugs planted, all caught, each by the line about it — the network's rules, a set's change or an attach not counted, a set's old rules counted beside its new ones, the network's own rules left out of a set change, a range counted as one, the bound off by one, one list not checked, the refusal answered 500.

**Firewall rules on the devices** (2026-11-27, phase 2 of [[firewall-rules]]). 21 bugs planted, all caught, each by the line about it. In the server (9, `internal/api`'s `TestE2E_FirewallDelivery`, `TestRecordReport`): any report accepted, a reason not cut short, a policy sent for a network nobody manages, a device's own address unknown to the policy, a policy or the devices list without the attached sets, a repeated report that moves its time, reports that outlive their network, no report stored. Two first did not compile and were rewritten. In the agent (10, `internal/client`'s `TestE2E_FirewallDelivery`, `TestManagedFirewall`): no check for a start in progress, none for a changed `nebula.yaml`, no return of the old rules after a failed write, a failed write reported as applied, no look at the file after a failed backup, a refused policy tried again, a wait that changes the report, a refusal reported with the refused policy's fingerprint, a rule's source rendered as every node, no reload. The second try was missed at first: a report is what the device knew before its beat, so the beat that retried still sent the old one; the test now checks what runs. In the tab (2, `npm test`): a reload that keeps the old devices, a refusal shown over an applied policy. A third pointed at a guard for a device without an expected policy, which a managed network never has; the guard was removed.

**Firewall rules: the device switch** (2026-11-28, phase 3 of [[firewall-rules]]). 24 bugs planted. In the agent (10, `internal/client`'s `TestE2E_BlockIncoming`): the switch not read at a start, a policy that lifts it, the switch not saved, switching off that keeps the block, a report never blocked, a report from the file instead of what Nebula runs, no route, no heartbeat after the switch, the start-up heartbeat without a report, the status without the switch. Two of them passed at first: the test sent every heartbeat by hand, so one the switch failed to send went unseen, and the file and Nebula never disagreed. The test now waits for the switch's own heartbeat (the loop's next one is a minute away), and switches off while `nebula.yaml` has a rule Nebula refuses, where the file says off and Nebula still blocks. In the server (7, `internal/api`'s `TestE2E_FirewallReach`): "blocked" not stored, a heartbeat without a report that changes nothing or only the memory, the list with every rule of the network, without the sets, without what hopssh keeps, a device of another network answered. In the dashboard's script (7, `npm test`): a rule that always applies, one that applies from "managed", hopssh's own access shown as the network's, a port shown for ICMP, the set's name lost, the note "blocks incoming" taken from "managed", a device's state that ignores "blocked". All 24 caught, each by the line about it.

**Firewall rules: an existing network to everything** (2026-11-28, item 4 of [[firewall-rules]]). The tab already did it; `TestE2E_FirewallTCPNetworkToAll` was written after and passed at once, so 5 bugs were planted. In the server (2, `internal/api`): the save that does not store the preset, and a heartbeat answer that carries a policy only when it has rules. In the agent (3, `internal/client`, two real Nebula peers): the block rendered as TCP only whatever the preset, a policy without rules ignored, and TCP only rendered as everything (a network that never narrows back). All 5 caught, each by the line about it. `TestE2E_FirewallDelivery` caught 3 of them too, but not the answer without rules nor the one that never narrows: every policy it sends has a rule, and it never goes back to TCP only. A review found that nothing in the TCP-only step showed the tunnel still carried anything: a sixth plant, a reload that leaves b with no tunnel to a and sends no error back (so a keeps its own), passed every line but the one added for it: TCP to the agent's port, which every block lets in from a node, must still get through. **The API docs** (`TestE2E_APIDocs`, real server): with only the path names compared, a host named again and an operation that differs from `swagger/swagger.json` both passed. It now compares the whole document and wants no host; both caught.

**Deleted devices** (2026-11-28, [[deleted-devices]]). 31 bugs planted, 29 caught, 2 that can't fail. In the lighthouse (6, `internal/mesh`'s `TestLighthouseRefusesDeletedNodes`, real Nebula): the list left out of the YAML, a start without the list, a refresh that never reloads, a refresh that only adds entries, a closed network reloaded; 5 caught, each by the line about it. The sixth, the lighthouse's own cert not excluded, can't fail: that cert is never in `node_certs`, so the exclusion is defence only. In the database (4, `internal/db`): no bound on held certs, the bound off by one, expired certs counted against it, the oldest entries kept at the cut; all caught. In the server (3, `internal/api`'s `TestE2E_Blocklist`, real server): no `blocklist` in the heartbeat answer, an enrollment's cert not recorded, a renewal over the bound answered 500; all caught. In the agent (16, `internal/client`'s tests and, for the first, `cmd/agent`'s `TestE2E_DeletedPeer`): the heartbeat's list never applied, the saved list not given to Nebula at a start, the comparison with `nebula.yaml` seeing the list (so the second list never applies), the device's own cert not left out, the list not saved, the list not checked (an upper-case fingerprint reaches Nebula, which then blocks nothing), the warm-up's list not applied, the warm-up taking the start lock with TryLock (it is held, so a start never gets its list), `null` clearing the list, a firewall apply that drops it; and, from the review of the built change, the list taken as in force from the text Nebula loaded instead of its CA pool (caught by the step where the CA can't be read), no re-apply after a renewal's restart, none after a retried one, a second in-place reload while one is still running, and one built from older text taken after it. 15 caught, each by the line about it. The sixteenth, no check that the pool took the list after the reload, can't fail: it only changes the log line, and the next beat retries either way because the check before the reload looks at the pool. The warm-up step is only deterministic because the test's server answers the start's own heartbeat (it has no `agentVersion`) and no other. From the second review, for the switch "Block incoming" refused while a reload runs (`TestE2E_BlockIncoming_AfterBusyReload`): no retry from the heartbeat, and a retry that only ever switches on; both caught.

**Self-endpoints** (2026-11-28). `internal/api`'s `TestE2E_SelfEndpoints` (real server), `TestPeerCanSendTo`, `TestSelfEndpointHints_LifetimeStaysWithItsAddress` and `TestSelfEndpointHints_HugeLifetime`. 17 bugs planted, all caught, each by the line about it: no cap, one cap for both families; port 0, an address with a zone, unspecified, loopback, multicast, link-local, 0.0.0.0/8, 240.0.0.0/4 (the broadcast address with it) or the mesh range let through; an IPv4 address written as IPv6 judged as IPv6 by the filter, or not unmapped in what is kept; no dedup; the lifetimes taken by the index of what is kept instead of what was sent, a lifetime multiplied before it is compared (a huge one wraps and expires at once), and a report with nothing usable that leaves the old one. Two needed a second try. Without the multicast check 224.0.0.1 and ff02::1 were still refused, as link-local multicast, so it got through; the test has 239.1.2.3 and ff0e::1 now. The lifetime plant first did not compile. A duplicate kept does not show in the answer, which dedups again, but it takes one of the 10 places: that is how it is caught. The first version had one cap of 10 for both families and multiplied the lifetime first; a review found both, and the tests were made red for each before the fix. A second review found that a zone got through: `[2001:db8::7%eth0]` twice the same address, `[::%eth0]` not unspecified to `netip`, and a zone of any length, so ten entries could still carry a megabyte. And no test had a mapped address of a refused class, so the filter's `Unmap` could go. Both made red first.

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
- The desktop app, and the dashboard except the Firewall tab's script (its markup neither: the tab was tried by hand in a browser on 2026-11-27, as owner and admin, not as a plain member).
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
