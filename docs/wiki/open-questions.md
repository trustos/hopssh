# hopssh wiki — open questions

Cross-cutting unresolved items. Acts as a handoff state machine across sessions: when a session ends with "next session should do X", file it here, not in chat.

Per-incident open items live on the incident page itself (`## Open` section). This file is for items that span multiple pages or that don't have a natural home yet.

## 2026-11-28 — deleted devices blocked with `pki.blocklist` (item 14, not released): what is left

- **Agents of v0.11.31 and older, and `hop client join` devices, get no list.** They accept a deleted device until its cert expires; only the lighthouse refuses it for them. Nothing to do but update the agents (`sudo hop-agent update`).
- **The server's hourly prune does not refresh DNS** (`internal/db/status_sweeper.go:84`, only when `HOPSSH_NODE_PRUNE_AGE_HOURS` is set). The lighthouse catches the prune within 30 s, but a pruned node's name resolves until the next DNS refresh. The sweeper lives in `db` and can't call the network manager; it needs a callback.
- **Certs of devices deleted before migration 013 are not known**, nor certs a node held before its current one at the time of the upgrade. Both are gone within 25 hours of the upgrade.
- **A server whose clock runs ahead drops entries early**, by as much as it runs ahead. Devices check `until` against their own clock too.
- **The agent bounds nothing in `peerEndpoints`** (`internal/client/renew.go`'s `injectPeerEndpoints`, `peer_cache.go`): it trusts its control plane. Since 2026-11-28 the server keeps at most 10 self-endpoints of a device per address family, each one a peer can send to (log); the lighthouse's part is bounded by Nebula. The peer cache sorts them as text, so after a restart the 10 per address family that Nebula takes are the lowest, not the server's order with the NAT-PMP mapping first. Low.
- **Self-endpoints a device no longer has stay with its peers until they expire** (15 min, or half the NAT-PMP lease). The server clears them on a heartbeat with an empty or useless `selfEndpoints`, but the agent leaves the field out when it has nothing to report (`internal/client/renew.go`, `sendHeartbeat`), so a real agent never clears them. Low.
- **`peerCanSendTo` lets a directed broadcast (192.168.1.255) and a NAT64 form (`64:ff9b::/96`) through**: it can't know a LAN's prefix, and a NAT64 address can be a real path. The agent sends neither. Low.
- **A peer gets a device's self-endpoints after what the lighthouse knows of it** (`mergePeerEndpoints`, `internal/api/renew.go`), and Nebula keeps the first 10 per family of the whole list. With 9 or 10 addresses of the device at the lighthouse, a NAT-PMP mapping the lighthouse does not know yet (UDP to it blocked, or the mapping just changed) is not dialled; the device's own updates list the mapping first, so in the steady state it is. As before 2026-11-28. Putting the self-endpoints first, or capping the merged list, would be a change of behaviour to decide. Low.
- **A device can make the server log a line on every heartbeat**: "self-endpoints dropped" and "firewall report" (`internal/api/renew.go`), at most 600 a minute by the agents' limiter, about 150 MB a day. Real agents trigger neither. Low.
- **The DNS server of each network listens on `0.0.0.0`** (`internal/mesh/dns.go:88`) and answers anyone who reaches its port (15300-15400/udp is published in the Docker example): the names and mesh addresses of a network's devices. Bind it to the lighthouse's mesh address, or answer only queries from the mesh.
- **The real-server tests skip without a word outside CI when UDP 42001 is in use** (`internal/e2etest/server.go`). Seen once, in the first root run in the VM for P3 (log 2026-11-28); a second run with `-v` had no skip, and what held the port is not known. A gate has to count skips (`-v`), not only read `ok`.
- **A stolen cert of a device that was not deleted can't be revoked.** That needs a "revoke this certificate" action; a product decision of its own.
- **An in-place reload that never returns blocks every later one until a restart**, on the device (firewall, "Block incoming", the list: `reloadInPlace`) as on the lighthouse. The price of one reload at a time (found in review: without it each beat queued one more reload behind the stuck one). No watchdog restarts on it; a renewal's restart does. On the lighthouse, `Close` waits for a refresh only up to its 10 s limit, so a reload that outlives it can still be running when the network closes.
- **What has no test.** Taking the service only once the start lock is held (`takeBlocklist`): the race it closes is a restart between two lines, and nothing can time it. The check after the reload that the pool took the list only changes the log line ("refused" or "waits"): its plant can't fail; the beat after it retries either way, because the check before the reload looks at the pool. The lighthouse's guard for a reload still running, its 10 s limit and the 30 s refresher loop. The e2e check that b is refused counts any datagram that does not arrive.
- **A lighthouse that starts reads the list before it registers its network** (`internal/mesh/network_manager.go`: the list at the start, the network in the map later), so a node deleted in between is let in until the next refresh, at most 30 s.
- **A network with 4096 entries logs the cut and sends all of them on every heartbeat** (`internal/db/node_certs.go`, `internal/api/renew.go`): about 420 KB per beat per device. Not reached by anything real; a list that only grows when devices are deleted.
- **The refresher reloads networks one after the other**, each under its `blockMu` and up to 10 s, so a hung reload delays the others by that much every 30 s.
- **A firewall apply takes the service before the start lock** (`applyFirewall`, `internal/client/firewall_apply.go`), so a restart in between leaves it working on the old one: one heartbeat reports a policy as applied that the new Nebula does not run yet. The next answer with a policy applies it to the new one. Older than item 14; `takeBlocklist` takes the service under the lock, `applyFirewall` does not. Found in the review of the fixes, low.
- **The lighthouse does not check that Nebula's CA pool took the list** (`internal/mesh/network_manager.go`, unlike the device's `CABlocklist` check). Only a reload that can't load the CA leaves the pool behind, and the lighthouse's CA is inline text that never changes, so that happens only once the CA expired, when the pool refuses every cert anyway. A log line that says more than it should, nothing else.
- **A real-server test waits for every other package's real-server tests** when packages run together (`takeTurn`, `internal/e2etest/server.go`: one hop-server per machine, and whoever has the turn mostly keeps it). That was `TestE2E_Audit`'s 580 s as root, and 680 s as user on 2026-11-28: it waited from 06:33:50 to 06:45:09 (UTC), the moment `cmd/agent` (678 s) ended, then ran in a second; on the Mac `TestE2E_Lifecycle` waited instead (255 s). A package's time is therefore not its own. The wait gave up after 25 min whatever `go test -timeout` allowed; since 2026-11-28 it waits until a minute before that timeout ends the test binary (`turnDeadline`, `TestTurnDeadline`), so it fails only where the run would fail anyway. A run of all packages needs a `-timeout` longer than the sum of their real-server tests (about 25 min in the VM).
- **Not run on Windows**, like P2 and P3.

## 2026-11-28 — after the device switch "Block incoming" (phase 3 of firewall rules, not released)

- **No report means "not blocked".** The server takes a heartbeat without a firewall report to mean the switch is off (`FirewallStore.NoReport`), so that an agent of v0.11.31 or older, which never blocks, never shows as blocked. The other side of this: any new code that sends a heartbeat must carry `firewallAck()`, or the dashboard says "not blocked" for that beat (it happened with the start-up heartbeat, fixed 2026-11-28). Only two senders exist today (`sendHeartbeat`, `warmPeersFromHeartbeat`).
- **The local API's start-up log line shows the first 8 hex characters of its token** (`internal/client/local_api.go:139`). For the system service that log is `/var/log/hop-agent.log`, which everyone can read. That is 32 of its 256 bits. It is not enough to use the token, but nothing needs it either. Drop it, or log the file's path only.
- **The desktop toggle has no test of its own.** `user_facing_copy_has_no_protocol_jargon` reads its text, and the agent side is tested, but nothing runs Settings.svelte. The desktop app has no component runner.
- **Not run on Windows**, like P2.

## 2026-11-26 — lab: `TestLimitedLine/5ms` fails in the evening, passed in the afternoon

**2026-11-27 01:10: not a change of ours.** The agent and server built from `bf557ea` (the commit that added the test; the lab's code is unchanged since) fail the same way now: tunnel 3.9 to 5.9, beside 9.3 to 15.7 Mbit/s, six of six. The current code in the same half hour: 5.4 to 8.7 against 13.0 to 16.0. What was different: the Mac that hosts the lab VM had a load average of 31 on 18 cores (a game, two UTM VMs); inside the lab VM the load was 0.5. A queue of 5 ms holds about eight packets, so a tunnel whose process is held up for a moment and then sends what has gathered overflows it; the kernel's own connection beside it is not held up. **Resolved 01:55:** with the game closed (it had also run the evening before, when the test first failed) and the load at 5 it passes three of three: tunnel 13.0 to 14.0, beside 16.1 to 16.3 Mbit/s. Run `scripts/netlab.sh -test.run 'TestLimitedLine/5ms' -test.count 3` with `uptime` below 5 before reading anything into this test, and note the load with every lab number.

The whole lab on the branch's tip (22:12): 38 tests pass, this one case fails. A line of 20 Mbit/s with a queue of 5 ms (about 8 packets): through the tunnel 3.4 to 7.0 Mbit/s, beside it 9.3 to 13.9, in 8 runs of 8; the bar is 70 %. At 16:00 it was 13.3 against 15.5, twice. Between the two only one loop was added to the agent (`runSendWatch`), and with it switched off the case fails the same (4.2 against 10.6, 5.0 against 13.4). So it is not that change, and not known what it is: the VM, or a case that passed by luck. The other four queues pass. **Next:** run the case 10 times on the commit `bf557ea` that added it; if it fails there too, the tunnel on a line with almost no queue is a finding of its own (batches of ours into 8 places).

## 2026-11-26 — hop's download to the MBP was slow for six hours

25 to 68 Mbit/s from 14:02 to 20:20, 145 to 185 before and after, where Tailscale and ZeroTier did 137 to 190 throughout. In the slow hours Defender's network filter on the MBP handled every packet of ours (50 to 100 % CPU) and not ZeroTier's; why it began and ended is not known, both followed a change of network on the MBP by some minutes. Ruled out: path, packet size, line, the mini, how the socket is used. It came back from 21:37 to 21:42. A 1 MB send buffer (ZeroTier has one, we have 9 kB) is ruled out: measured beside ours in the slow state it was three to four times slower. Not the trigger: the agent's rebind, a screen-sharing session. [[concepts/defender-filter-on-the-mbp]]

## 2026-11-26 — from the cellular and reboot test on v0.11.29

- **Retransmits, partly explained (2026-11-26, Wi-Fi, mini and MBP at different sites):** they follow two counters. (1) Drops on the sender's own tunnel interface (`netstat -ibdn`, last column): the tunnel reader waits in the UDP send and the tunnel's queue overflows; 240 to 1150 per 8 s run while the MBP was busy after its reboot, 0 to 140 later. (2) hop's UDP packets lost on the way, 2 to 8 % in some runs, while ZeroTier's packets on the same line lose next to none. **Explained the same day: it is the line's limit.** With the sender capped (`iperf3 -b`) the loss is 0 at 60 Mbit/s, 0.1 to 0.5 % at 100 and 1.3 % at 140; uncapped hop reaches 173 to 185 Mbit/s and loses 2 to 3 %, ZeroTier stops by itself at 122 to 167 and loses none. The bursts are not it (tunnel reader's batch at 32, 4 and 1, two rounds each, alternating: 1.5 to 5.8 %, 0.07 to 7.8 %, 1.3 to 3.5 %; a batch of 1 brings tunnel drops back), and nothing is fragmented (0 fragments at both ends). So TCP in hop runs into the line and pays with retransmits; pacing the sender would be the cure, and nothing shows that it is worth it at these speeds. The old idea that the macOS sender drops on a full buffer is wrong: no such line in either log. Cellular upload is the same thing (measured cleanly later that day: uncapped 17 to 27 Mbit/s with 5 to 13 % lost, capped at 5 and 10 Mbit/s none; ZeroTier 11 Mbit/s, none). **Closed 2026-11-26:** the loss is the start of each connection (TCP doubles until the line's queue is full; nearly all retransmits fall in seconds 1 to 3, then none), a connection without the tunnel loses the same on the same line (`TestLimitedLine`), and counted at both interfaces it is lost between the Macs. A prototype that held one packet back when the delay rose cut the retransmits from thousands to 20 to 50 on the Macs and the speed from 180 to 37 to 69 Mbit/s, because the Wi-Fi delays packets by 100 ms by itself. Not built; what would work (our own rate control in the tunnel) and why not now: [[concepts/loss-on-a-limited-line]].
- **"Dropped due to full socket buffers" counts nearly every datagram for hop-agent on a Mac with a content filter**, although they arrive. Do not read that counter on such a Mac.
- **Silences of 1.5 to 2 s** on both networks at once with nothing in the logs. Not explained. The recorder of 2026-11-26 (12:19 to 20:32, a ping every 0.5 s over both networks and ZeroTier, stopped now) saw them at 14:02, 15:57, ten times 16:11 to 16:53 (while a prototype of ours ran on both Macs), 20:11, 20:14, 20:16. Only 20:14 has refused sends in the log (17 lines), so the content filter holding sends is not the general answer. ZeroTier had three of its own (13:15, 15:51, 18:03) at other times. None in the three quiet hours 17:00 to 20:09.
- **FileVault:** after a reboot nothing runs until the disk is unlocked, so "mesh before login" does not exist on such a Mac. Worth a sentence in the user docs.
- **20 s without connection on hotspot to Wi-Fi (13:54), explained:** it is the change itself. The agent's sends failed with "can't assign requested address" (the Mac had no address yet), the rebind came at 13:54:08, the tunnels after it. ZeroTier was silent 47 s for the same change, and 30.5 s at 14:31 where hop was silent 7 s.
- **A Mac that is off for more than 24 h** has not been tried on a device; only `TestE2E_OffForHalfAYear` shows it.

Source / context: `docs/wiki/log.md` 2026-11-26

## 2026-11-26 — after the Nebula upgrade: what is left

- **v1.12.0** needs Go 1.27.3 and a much newer gvisor. There `linux-udp-close.patch` can go (upstream waits for the reader); `linux-tun.patch` has to be shown unnecessary with the kernel-TUN test first.
- **Log level.** Nebula v1.11 no longer sets the level of the logger it is given. Before, `nebula.Main` did, so the agent's "warn" and `HOPSSH_LIGHTHOUSE_LOG_LEVEL` never had an effect. Now `nebulacfg.Logger` applies the config's `logging.level` and the server variable works for the first time (default info). Anything that reads the logs by pattern has to expect `level=INFO msg="…" key=value`.
- **CI job `check-patches`** calls `make check-patches`, which does not exist; `continue-on-error` hides it. Remove the job or write the target.
- **`TestUncleanShutdownRaceLoser`** of upstream's e2e does not end with `paths.patch` (explained in `patches/README.md`); upstream's e2e are still in no script.
- **Windows** ran once (2026-11-26/27, Windows 11 ARM64 in UTM, against a private server on the host, with an Ubuntu 26.10 VM as second device). Works: enrollment by token, the service, the tunnel, name lookup of the mesh domain, a restart (tunnel back 8 s after the start), the direct path to the Linux VM, health, exec and the terminal (ConPTY) through the server, a forced renewal (9 s, same port). Three faults found and fixed (see log). **Two networks at once, run 2026-11-27:** both are up (interfaces `hop-hop` 10.42.1.3 and `hop-lab` 10.42.2.2, listeners 4242 and 4243), each with its own DNS forwarder on its own loopback (`127.53.0.1:53` → 15300, `127.53.0.2:53` → 15301), so a query sent to the wrong one is refused — that is not a fault. Traffic passes both ways on both networks, and a restart of the service brings both back in 6 s. The same on the Linux VM found a real fault: `installAgentSystemd` ran `systemctl start`, which does nothing to a running unit, so a second enrollment stayed down until a reboot (fixed with `serviceStartCmd`, `TestServiceStartCmd_*`). **Run there 2026-11-27:** a change of network (an address change on the adapter: rebind within 2 s on both networks, the tunnel answering 4 s later, about 5 s of pings lost) and sleep (`NtSuspendProcess` for 40.4 s: `sleep/wake detected (tick gap 44s)`, rebind and an out-of-cycle heartbeat on both networks, 0 % loss after the resume). In that guest never use `netsh interface set interface "Ethernet" admin=disable`: the adapter never returns and the guest agent dies with it, and only a stop and start of the VM recovers. **Run there 2026-11-27:** self-update (`update` on a v0.11.28 agent: download, SHA-256, rename to `.old`, swap, `sc stop` + `sc start`, `version` v0.11.30, both meshes back at 0 % loss). The `<exe>.old` survives the update itself — `cleanupOldBinary` runs at the next start of `runServe`, and the SCM can still hold the image of a service it just stopped; its error is discarded, so a failure is silent. The next start removes it. One warning seen once, not explained: `[dns hop] WARNING: failed to cleanup DNS for .hop: remove NRPT rule: exit status 0xc0000409` — the PowerShell that removes the rules died; the next start re-registers them, so nothing was seen by the user. The Linux VM ran in userspace mode and then as a root service (kernel TUN, split-DNS through the resolved drop-in, restart, forced renewal, 40 s stopped: the peer answered within 2 s each time). Explained and fixed: the line `Failed to write to log, write /dev/stderr: The handle is invalid.`, once per start. It is logrus's own complaint: the port mapper logs with logrus, which took `os.Stderr` when the process started, and a Windows service has no console handles. So every `portmap:` line was lost and the log held only the complaint. Now the standard logrus logger writes to wherever the standard log writes at the moment of the line (`routeStandardLog`, `TestStandardLogrus_*`); in the VM the `portmap: no protocol available` line appears in `C:\ProgramData\hopssh\hop-agent.log` and the complaint is gone. Starting PowerShell took 13 to 107 s in that VM. Also: the Windows agent's IPv6 addresses reach the other device, whose listener is IPv4, so each handshake logs two `Failed to send handshake message … listener is IPv4, but writing to IPv6 remote` at ERROR. Harmless, the IPv4 address is tried too.
- **Nebula now watches the network itself on macOS** (`listen.rebind_on_network_change`, on by default): a message on the routing socket, one second to settle, then the same rebind our `watchNetworkChanges` does every 5 s, without closing tunnels. On the Macs it fired once at start (another network's utun appeared) and did no harm. Not looked at: whether our 5 s look is still needed on macOS, and what the two do together when the Wi-Fi changes.
- **From the review of the pull request (2026-11-26), all three older than the upgrade:**
  - *macOS: closing the UDP socket does not wake its reader* (`udp_darwin.go`, `Close`). The reader sits in `recvmsg_x`, so the port may stay taken until the next packet arrives: the macOS twin of what `linux-udp-close.patch` fixes. Read from the code, not measured. Renewals on the Macs reload on the same port and were clean, and `waitForUDPPortFree` waits; a test like the Linux close tests would settle it.
  - *`ProbeEndpoint` without an address* (the path loop's question to a silent tunnel) sends an ordinary test packet, so the receiver may move to us before an answer came. "Ask before moving" does not cover it.
  - *`Control.Wait`* (new in v1.11) could replace our own close-then-wait-for-the-port in `reloadNebula`. Not tried.

Source / context: `patches/README.md`, `docs/wiki/raw/nebula-upgrade/`

Format:

```
## YYYY-MM-DD — short title

What's open. Why it matters. Where to start.

Source / context: link to wiki page or external URL.
```

Move closed items to a "Resolved" section at the bottom rather than deleting — the resolution rationale is itself useful.

---

## Open

## 2026-11-27 — firewall rules, phase 1: what is left

- **(Done 2026-11-27 in phase 2, not released.)** ~~Devices don't apply the rules yet.~~ A managed network's devices apply them at their next heartbeat ([[concepts/firewall-rules]]).
- **A preset reaches only devices with an agent newer than v0.11.31.** Released agents ignore it and keep the block they wrote at enrollment (`tcp`). Since phase 2 a newer agent in a managed network applies the preset and the rules; in a network nobody managed, the preset still reaches only devices that join.
- **A rule set that names a device works only in that device's network**, so such a set can be attached to one network only (an attach elsewhere is refused with 400). The editor offers only the devices of the network it was opened from.
- **The tab was not tried as a plain member** in a browser; `TestE2E_Firewall` checks what a member may do.
- **`swagger/` has not been regenerated since April 2026** (no Makefile target, `swag` not installed): it lacks the firewall routes and much else.

Source / context: `docs/wiki/log.md` 2026-11-27 20:07 to 20:55.

## 2026-11-27 — firewall rules, phase 2: found in the design

Read in vendored Nebula v1.11.2 for phase 2 (design and review: [raw/firewall-p2/design-review-2026-11-27.md](raw/firewall-p2/design-review-2026-11-27.md)).

- **Some firewall text crashes Nebula instead of being refused.** `groups: []`, `groups:` with nothing after it, and a group that is not a string panic in `convertRule` (`vendor/.../firewall.go:979-990`). So do `conntrack.udp_timeout: 0s`, or any other timeout of 0 or less (`NewTimerWheel`, `timeout.go:83`). A reload of such text would end the agent. The agent writes only its own template, so it never sends this text. It still has to build the candidate under `recover`. Upstream v1.12.0 fixes the groups crash (#1912) and the timeout crash (#1984), according to the survey (not verified).
- **Nebula accepts some wrong rules without a word.** It takes port 0 as "any", never checks groups, host or CA names against anything, and ignores a port on an ICMP rule. The agent's own check is the gate, not Nebula.
- **A SIGHUP reloads every network from its file.** Each `nebula.Main` calls `CatchHUP` (`vendor/.../main.go:121`). A SIGHUP therefore no longer ends a foreground agent, and every instance re-reads its `nebula.yaml`. This is harmless while the file is what was loaded. Phase 2 keeps it that way: the file is written only after a successful reload, and the old text goes back on any failure. Nothing in hopssh sends a SIGHUP. Not changed.
- **Subnet routes and the router's firewall.** A rule without `local_cidr` covers only the node's own mesh address while `default_local_cidr_any` is false. Traffic to a subnet a node routes (its cert's unsafe networks) may then be dropped by that node's own firewall. Not checked. Phase 2 renders the same block as today plus the rules, so it changes nothing here.
- **Upstream v1.12.0 changes the signature of `RegisterReloadCallback`** (survey). Phase 2 registers no callback: it compares `Control.FirewallRuleHash` after the reload with what `CheckFirewall` built, so the upgrade does not touch it.
- **The config's getters take no lock** (`config.go:304`); `ReloadConfigString` replaces `c.Settings` under `reloadLock` only (`config.go:175,381`). The callbacks run under that lock, so they are safe. A goroutine that reads the config outside a callback while a reload runs would race. None was found by reading; the two-peer E2E test of phase 2 runs under `-race` with reloads to show it.
- **With more than one routine the conntrack cache skips the recheck.** `firewall.conntrack.routine_cache_timeout` defaults to 100 ms when `routines > 1` (0, off, with one routine, which is what hopssh runs). A cached flow is not checked against a new rules version until its entry ages out, so a removed rule takes up to 100 ms longer there. Nothing to do while hopssh runs one routine.
- **Patch `firewall-swap` done 2026-11-27** (the plain pointer write of finding 6 in [[concepts/firewall-rules]]): an `atomic.Pointer`, plus `Control.CheckFirewall` and `Control.FirewallRuleHash`.

## 2026-11-27 — firewall rules, phase 2: what is left

Built and tested ([[concepts/firewall-rules]], "Built"); not released.

- **After a restart the agent does not know which policy its file holds** until it applies one: a policy it refuses first is reported with fingerprint "" (the enrollment block), although the file may hold an earlier managed one. The server sends the same policy on every beat, so the next accepted one fixes the report. Storing the fingerprint beside `nebula.yaml` would close it.
- **Every device reaches every other's agent API on TCP 41820, whatever the policy.** The keepalive needs it (a refused dial counts as alive), and the bearer token is the only gate on what is served there. A probe that needs no open port would let a policy close it.
- **A reload runs every callback, the PKI one too.** The agent refuses when `nebula.yaml` changed since the last load, but it does not compare the cert files: a reload between a renewal's write of the cert and its restart of Nebula loads the new cert in place (same network and curve, so Nebula accepts it). Thought harmless, not tested.
- **The devices table was not tried in a browser**, only its script (`npm test`). Not run on Windows.
- **`internal/e2etest/peer.go` still allows all.** The peer of the process tests runs no firewall of its own, so `cmd/agent`'s `TestE2E_FirewallApplied` (real server, real agent process) checks the device's file, its report and that it stays reachable, but no traffic that a rule opens or closes; the two-peer test in `internal/client` does that.
- **A refused policy is not tried again on the same Nebula**, a write that failed for a moment included (a full disk). The report says why; a change of the policy or a restart of the network tries it again. Trying every beat would reload twice a minute, and each reload stops flows the new rules don't allow.

## 2026-11-27 — audit log: what is left

- **A removed member's credentials keep working for minutes.** An enrollment token (10 min), a bundle URL (15 min) or a device code they approved still brings a node into the network after they are removed, and the entry names them. `Enroll`, `Poll` and `DownloadBundle` check no membership. Proposed: refuse them when the creator is no longer the owner or a member. Needs a yes; it changes what a token does.
- **Failed logins write no entry.** Only the rate limiter sees them.
- **Every member reads the network's audit log** (`CanView`), with the address a machine enrolled or fetched its bundle from. That address is `X-Forwarded-For` only with `--trusted-proxy`; whether production behind Traefik records the client's address was not checked.
- **Two deletes of one network at once can both write `network.delete`** (`Networks.Delete` reports nothing). Owner only; a nit.
- `internal/api/members.go` keeps a branch for `sql.ErrNoRows` from `Remove`, which no longer returns it.

Source / context: `docs/wiki/raw/audit/coverage-2026-11-27.md`, `docs/wiki/log.md` 2026-11-27.

## 2026-11-27 — loose ends seen while fixing the API and the debug listener

- **A full network uses up the device code.** The device flow takes the address in `Poll`, after `ClaimAuthorized` has consumed the code (`internal/api/device.go`). The 409 now says the network is full, but the user has to start the device flow again. Same shape as "`Enroll` and device `Poll` use up the token before the 409 check" in the API gaps below.
- **The CLI's device flow hides every error.** `internal/client/enroll.go:309-348` keeps polling on anything but 200 and 403 until the code expires, so `hop-agent enroll` shows "network is full" (or any 409/500) only as a timeout ten minutes later. The desktop app and the dashboard show the message.
- **The debug listener stays up after an enrollment.** `startUnscopedOSListener` keeps `s.bare` until shutdown. Since 2026-11-27 it is on 127.0.0.1 and starts only with `--token`/`--token-file`, which no installer passes, so it no longer blocks a mesh listener on `<mesh IP>:41820`. Shutting it down when the first network comes up is the clean end.
- **`Config.UserAgent` is read by nothing** (`internal/client/api.go:34`), like `HeartbeatInterval`. Set by `cmd/agent`; not removed because the mobile binding may set it.

Source / context: `docs/wiki/log.md` 2026-11-27, `concepts/firewall-rules.md` "Found on the way".

## 2026-11-27 — `hop client join` gives the device no way to use the mesh

Read in the code while fixing the keepalive, not run. `runClientJoin` (`internal/client/cli_join.go`) starts Nebula with `tun: user: true` and nothing else: no listener, no SOCKS proxy, no port forward, no DNS. The device's programs can't reach the mesh, so the hint it prints ("Try: ping <hostname>") can't work. Every run also enrolls afresh: a new node and certificate each time, and the old node stays on the server. **Decide:** build it out as a client (kernel TUN as an installed service does, or a local proxy), or drop the command in favour of `hop-agent enroll`.

Also: existing networks keep the `tcp` preset; there is no way yet to move a network, or the devices already in it, to `all` (item 4 of the plan, later; [[concepts/firewall-rules]] "Decide").

## 2026-11-27 — `TestE2E_Proxy`: a request hangs for 30 s (resolved: TIME_WAIT in gVisor)

**Resolved 17:55, not the two tunnels; the hypothesis below was wrong.** gVisor lets a new SYN reopen a connection in TIME_WAIT only when a listening socket is there (`handleTimeWaitSegments`). Nebula's userspace service accepts through gVisor's forwarder and has none, so the SYN and every retry (after 1, 3, 7, 15 s) were dropped until the TIME_WAIT ended 60 s later, and the server's 30 s route timeout answered 503. The server's proxy opens a connection per request (`DisableKeepAlives`) from a random port; the agent closes first and keeps the TIME_WAIT, so a run of requests meets one after a few hundred. One tunnel is enough. Shown 17:21 (load 3.8) with two peers in one process: when the listener closed first, 3 stalls in 776 connections and 3 in 364; when the dialer closed first, none in 25,760 and 26,469. Fixed by `patches/gvisor-time-wait.patch`, which hands such a SYN to the forwarder. `TestTimeWaitReopen` (internal/client, two bare gVisor stacks, the second connection from the same port) fails every time without it, with and without `-race`, and catches a half fix that lets only the resend after 1 s through. `TestE2E_ConnectAgainAfterTimeWait` (internal/client, through Nebula) failed 3 of 3 without it, at connections 251, 190 and 210, and again 3 of 3 at 55, 237 and 319 once its 800 ms bar was gone; it passed every run with it. `TestE2E_ProxyBackToBack` (internal/api, real server, 2000 health checks) failed 5 of 5 without it on the Mac, at checks 769 to 894 (503 after 30.0 s), and 5 of 5 in the Linux VM, at checks 169 to 1426; with it, it passed every run on both. Its first failures come later than the client test's odds say (about 280 on average), and on the Mac in a narrow band: not explained. In use, this hit the nodes whose listener is userspace: the desktop app's bundled agent and any agent in userspace TUN mode. With kernel TUN the agent's own OS stack takes such a SYN. The load of the full run was not needed: the server picks each port among 49,536, and `TestE2E_Proxy` opens about 65 connections to the node before the check that hung; that one of them meets an earlier one's TIME_WAIT has a chance of up to 4 % (65·64/2/49,536), which fits one failure in the 47 runs seen.

**Review of the patch (17:56 to 18:03, 11 findings, each checked by a skeptic and then by me in the code).** Changed: a SYN+ACK no longer takes the new path, where it ended the TIME_WAIT for nothing (the forwarder refuses it); the bare-stack test above, so the patched path runs under `-race` (`make test-race`); the 800 ms bar of the Nebula test went, as one lost packet on a loaded machine made it fail, and the bare-stack test pins the first answer instead; corrected docs: where nothing listens any more the SYN gets an RST, the odds in the test's comment hold for a loop of 60 s. Left: a second copy of the SYN queued behind the first, which needs the stack about a second behind, gets an RST, as upstream answers any SYN queued at a closing endpoint (`patches/README.md`). **For the firewall work (item 17):** read in the code by the review, not run. Nebula's service listens without reserving its ports in gVisor, so about one outbound dial in 49,536 from a userspace agent uses local port 41820. The conntrack entry of that dial then lets the dialed peer open a connection back to the agent API from that port, whatever the inbound rules say, until conntrack forgets it (12 min). The bearer token still guards it, and today every preset lets nodes reach 41820 anyway. Once rules can shut a peer out, that is a way past them to an unauthenticated HTTP request.

In one of two full runs of `go test ./...` on the Mac (14:14), the 30th health check of `TestE2E_Proxy` (`internal/api/e2e_test.go:1548`) got the route timeout (503 after 30 s); the 29 before it took about 1 ms. Alone it passed 45 of 45 (5, then 40 in a loop); in 35 of those 40 both ends had started a tunnel at once and none had a dead tunnel, so two tunnels alone do not cause it — the full run's load (three packages of end-to-end tests at once) is part of it. Neither fix of the day is on its path (fake agent, first node, nothing deleted).

What the log shows: the fake agent started a tunnel to the lighthouse (agent index 3151938886, server 2348978550) and, 14 ms later, the control plane started one to the agent (server 2411564706, agent 2394334941), so both ends held two tunnels. 21 s after the hang began, both ends marked the agent-started tunnel dead in the same second (`tunnelCheck="map[method:active state:dead]"`); the control plane's tunnel lived until the test closed it. **Hypothesis (wrong, see above):** one end kept sending on the tunnel the other end no longer answered, and the request's packets went there.

Seen in the same log, not a fault of the test: the real agents of the Linux and Windows VMs (192.168.64.6, .5) sent handshakes to the test lighthouse, because their own server had been on this Mac's port 42001.

Source / context: `docs/wiki/log.md` 2026-11-27

## 2026-11-24 — v0.11.27: prove it on devices

The server runs it, no device does. Open until then: a device renews at half its certificate's life and stays online for 24 h (E); an in-app network on a Mac and a system-wide one; a machine that was off for a week comes back (B, L). Steps 7 and 8 of [[runbooks/release-v0.11.27]].

## 2026-11-24 — the database file relies on its directory's mode

`hop-server` creates a missing data directory with 0700, which keeps other local users out. An existing directory (a container volume made by something else) keeps its mode, and `hopssh.db`, `-wal` and `-shm` are created with 0644; only `encryption.key` is 0600. Secrets in the database are encrypted or hashed, the rest (users, networks, audit log) is not. **Action:** create the database with 0600 and tighten existing files at start, so that it doesn't depend on the directory.

## 2026-11-24 — dashboard: no security headers, and a dead link

`hopssh.com` sends no `Strict-Transport-Security`, no `Content-Security-Policy` and no `X-Frame-Options`. `SameSite=Lax` keeps the session out of frames of other sites, which limits clickjacking, but HSTS and `frame-ancestors` are missing. And "Download the DMG" on the login page leads back to the login page when signed out (`routes/+layout.svelte` sends `/download` there). Both older than v0.11.27.

## 2026-11-24 — releases are not gated by the tests

`release.yml` and `release-desktop.yml` start on any `v*` tag and run no test; `make release` tags whatever is checked out and pushes only the tag, so `main` can stay behind. The Docker image is built for the first time there. **Action:** a `test` job that the build jobs need. Until then: push `main`, wait for CI, then tag ([[runbooks/release-v0.11.27]]).

## 2026-11-24 — a lighthouse that fails to come back is silent

The idle reaper closes a network's lighthouse after 2 h without a device; the next heartbeat starts it again through `GetInstance`. `renew.go` throws that error away (`netInst, _ =`), so a lighthouse that cannot start (V did that in production, see [[incidents/2026-11-18-mbp-all-certs-expired]]) leaves no line in the log, and the network has no relay until a later heartbeat succeeds. The reaper's own line prints a pointer (`last activity: 0x…`). Also: a device that *enrolls* into a reaped network finds no lighthouse until its first heartbeat. **Action:** log the error; consider starting the lighthouse at enrollment.

## 2026-11-24 — five type errors in the dashboard

`npm run check` in `frontend/`: three in `network-topology.svelte`, two in `networks/[id]/+page.svelte` (`role` as a plain string; `network` possibly null). Known since May 2026. A sixth was a real bug: the "Clean up" button called a function that does not exist, so it deleted the devices and then showed an error (fixed for v0.11.27). **Action:** fix them and run the check in CI.

## 2026-11-23 — a proxied page runs as the dashboard (R)

`NodeProxy` serves a node's web service under the dashboard's own origin and strips its CSP. A script on any node's page can call `/api/…` with the viewer's login. The session cookie no longer reaches the node (Q), and a node can no longer set it (T), but same-origin requests still work. **Action:** serve proxied pages from a separate host name (best), or with `Content-Security-Policy: sandbox` without `allow-same-origin` (breaks apps that need storage). Pinned by `TestE2E_Proxy`.

## 2026-11-23 — API gaps pinned by the E2E tests

Each has a `KnownGap` in the test named, which fails once the gap is closed, so the marker can't be forgotten. Not fixed because each is a product decision or a change of its own.

| Gap | Test |
|---|---|
| *(closed 2026-11-28)* A deleted device could join the mesh until its cert expired (≤ 24 h): nothing distributed a blocklist. Now the lighthouse and every device refuse its certs ([[concepts/deleted-devices]]) | `TestE2E_DeletedPeer` (normal test now) |
| *(closed 2026-11-28)* A deleted node's name kept resolving: `DeleteNode` didn't call `RefreshDNS`. Now a delete and the API's prune do | `TestE2E_DNS` (normal check now) |
| An outsider gets 403 instead of 404 on admin routes, so they can tell which network IDs exist (9 routes) | `TestE2E_Access` |
| A member who opens an invite uses it up for the person it was meant for | `TestE2E_Invites` |
| *(closed 2026-11-27)* Removing a member left no audit trace, nor did 18 other routes (networks, enrollment, device approval, bundles, rename, capabilities, routes, stopped forwards, DNS, invites). Now each is logged with who, where and what, and a machine's enrollment or bundle download names the user who made its token or bundle (`nodes.created_by`, migration 010) | `TestE2E_Audit` (normal test now) |
| Every renewal ends the open sessions of an in-app network (not a system install: with kernel TUN the session lives on, asserted when the test runs as root) | `TestE2E_Lifecycle` |
| Any member runs commands on every node; the audit log doesn't say which command | `TestE2E_MemberShell` |
| What a device copies (up to 8 KB) is sent to every open dashboard of the network, where nothing uses it | `TestE2E_Clipboard` |
| A clip of 198 to 256 KB is refused: the limit for the request body forgets that the content is base64 | `TestE2E_Clipboard` |

Seen while writing the tests, not pinned: any member can start port forwards (they listen on 127.0.0.1 of the control-plane host only) and stop anyone's; CORS allow-methods lacks PUT and PATCH; an invite accepts `maxUses: -1`; a DNS record may point outside the network; `Enroll` and device `Poll` use up the token before the 409 check; `NetworkManager.StopNetwork` during a start in flight is untested.

Clipboard, seen while writing `TestE2E_Clipboard`, not pinned: `since` has seconds, so the second of two clips in one second is skipped for a device that polled in between; the limit is 60 requests a minute per address and every device polls 30 times a minute, so three devices behind one router run into it after about four minutes; a database error answers 401 (as renew and heartbeat did, a).

## 2026-11-24 — UDP between devices is blocked by our own firewall: allow it? (P)

**Decided and built 2026-11-27 (not released):** networks created from migration 009 on allow every protocol between agents; older networks keep TCP only, and moving them is a later decision. Clients now also accept TCP 41820 from `node`, so the keepalive of agents reaches them. How, and the tests: [[concepts/firewall-rules]] ("Today", item 8). The text below is the question as it stood.

Proven in [[concepts/netlab]] (`TestUDPBetweenDevices`, pinned), and read off the live config on the mini (2026-11-27): the agent allows inbound **TCP on every port** from the `node` group, TCP 41820 from `admin`, and ICMP — no UDP (`internal/client/enroll.go:654`). `hop client join` allows inbound ICMP only, so a client accepts nothing at all from the mesh (`internal/client/cli_join.go:206`). The control plane's own lighthouse allows everything (`internal/mesh/network_manager.go:538`).

So the UDP line buys nothing that the TCP line does not already give away: any node can already reach any TCP port on any other. It costs mosh, QUIC, WireGuard and Screen Sharing's media, and there is no user-facing control to open it again — firewall rules are roadmap #8, not built. How they could be set (dashboard tab, heartbeat, in-place reload) and what the others do: [[concepts/firewall-rules]] (proposed 2026-11-27). Tailscale's default ACL permits every protocol between nodes. Nothing in the code or the docs says who chose this; it is unchanged since the first agent commit (2026-06-05).

**Decide:** allow UDP for all, per network, or with the firewall rules of the roadmap. The change itself is one line; existing agents need their `nebula.yaml` rewritten (`ensureP2PConfig` rewrites what it owns on every boot, so it can heal the rest).

**Proposed 2026-11-27, waiting for the owner's yes:** new networks allow everything between agents, UDP included, as Tailscale, NetBird, Headscale and ZeroTier (rules off) do; clients stay ICMP-only inbound; the agent keeps TCP 41820 from `admin` and `node`, ICMP and `inbound_action: drop` whatever else changes. Shared rules later as an account-level library that networks attach (Defined's model), combined as a union of allows, not inheritance with override, which none of the products read has ([[concepts/firewall-rules]], "Decide").

**What the others do** (read 2026-11-27 from their own docs):

| | Default between nodes | How a rule is written |
|---|---|---|
| Tailscale | everything: "in the absence of an `acls` section … Tailscale applies the default allow all policy" ([acls](https://tailscale.com/kb/1018/acls)) | `src` → `dst:port`; "Without a protocol, the access rule applies to all TCP and UDP traffic", and ICMP is allowed whenever any traffic is ([policy file](https://tailscale.com/docs/reference/syntax/policy-file)) |
| ZeroTier | everything: "By default, flow rules are disabled and all traffic is allowed across the network"; only non-IP Ethernet types are dropped ([rules](https://docs.zerotier.com/rules)) | flow rules, can match `ipprotocol udp` |
| Nebula / Defined Networking | nothing: "deny all for all inbound and outbound traffic" until a rule allows it ([firewall](https://nebula.defined.net/docs/config/firewall/)); their Managed Nebula: "Inbound traffic is denied by default; rules allow by role and tag" ([defined.net](https://www.defined.net/)) | `port`, `proto` (`any`, `tcp`, `udp`, `icmp`), groups/host/cidr |

Defined Networking was founded in 2020 by Nebula's creators (Ryan Huber, Nate Brown) "to focus on Nebula development"; Slack "continues to be a primary sponsor" ([nebula docs](https://nebula.defined.net/)). Not confirmed from a page: whether Tailscale's userspace SOCKS5 proxy carries UDP.

Two facts of ours that bound the decision: (1) in userspace mode only the agent's own API listens on the mesh (a non-root agent), or nothing at all (`hop client join` starts no listener), so no host program is reachable over TCP *or* UDP; the rule matters for system installs (kernel TUN) only. (2) Nebula's conntrack lets a UDP *reply* in, so an app whose two ends both send first can half-work today; what is blocked is UDP that the other side starts (a mosh server, a DNS server, a game server, QUIC to a node).

## 2026-11-24 — what Tailscale has and the lab shows that we don't

1. **A way out of a network without UDP** (`TestNoUDP`, pinned): a relay on TCP 443. Hotels, offices, some mobile networks.
2. **The right packet size per path** (`TestSmallPackets/told_false`, pinned): on a line that takes less than 1480 bytes and does not say so, big packets vanish. A login works, a listing hangs. It needs DPLPMTUD (never built) or MSS clamping.

## 2026-11-24 — path fixes: what is still unproven

Patches 33 to 36 and `path_watch.go` are on the branch `lab/network`, not released. The whole lab passes (37 tests), and 39 of 47 planted bugs were noticed; the 8 others are listed in [[concepts/netlab]] with the reason, and so are 25 more, all noticed. **Not done:** real devices (macOS and Windows agents, a real phone carrier). The rebind after a network change is the one of the 8 that matters: on macOS only a real laptop can show whether it still works. An agent of v0.11.27 or older on the other end does not answer patch 35's question, so a path that works in one direction only can still be interrupted until both ends are updated.

`ownNets` (`path_watch.go`) counts the networks of every interface, also of another VPN (all three reviews of the pull request say so). **Harm not shown:** only an address that the peer itself reports is tried, the peer's `local_allow_list` and `selfEndpoints` hold back those of its other interfaces, and Nebula prefers every private address anyway since `preferred_ranges` works. What is left is a peer that can't tell its physical interface and then reports all of them. Two machines that are in one ZeroTier network would show it; the lab has none.

## 2026-11-24 — data races inside Nebula v1.10.3

`go test -race ./internal/client` fails in `TestE2E_QuestionMovesNobody` (8 of 8 runs) and sometimes in `TestE2E_NewTunnelStays` (2 of 8); in the one run of everything the rest was clean, and so is `-race` on the vendored `udp` package. Four pairs, all between lines that are the same in upstream v1.10.3, none in our patches or in `internal/client`:

1. `HostInfo.remote`: written by `SetRemote` (UDP thread), read by `sendNoMetrics` (TUN thread, two places) and by `copyHostInfo` (`ListHostmapHosts`, `GetHostInfoByVpnAddr`) without a lock. A torn read can mix an old address with a new port, so one packet goes astray or the dashboard shows a wrong address for one second. Our `path_watch.go` reads it once a second and at worst asks one question too many.
2. `RemoteList.relays`: sorted in place under its lock, its length read without it by `handleOutbound`. For a moment it looks empty, then one handshake attempt doesn't try the relays; the next one, a second later, does.

**Fixed 2026-11-25** (branch `fix/peer-address-race`): patch 37 makes the address an atomic value, patch 38 gives the handshake code a copy of the relay list taken under the lock. Each has a test in the vendored package for `-race`: the one of 37 is red without the patch, the one of 38 is red when the lock is taken out of `copyRelays` (without the patch it does not compile). Upstream v1.11.2 has the same fix as patch 37 (seen in its source), so 37 goes on an upgrade; the code of patch 38 was moved there (`relayManager.StartRelays`) and has to be looked at. **Still open:** CI doesn't use `-race`; both races were invisible to `go test ./...`.

## 2026-11-25 — certificates and paths: cases no test covers yet

From an audit of the tests against the ways a certificate or a path can fail. Covered: a fixed renewal instant, a start with an expired certificate, a deleted node (401), a renewal goroutine that dies, a lost path (lab), and since today a running agent whose renewals fail until after expiry (`TestE2E_RenewalFailsPastExpiry`: the peer drops it, and it is back 0.1 to 0.2 s after the server answers again). **Not covered:**

1. *(done 2026-11-25)* The peer's view of a machine whose certificate ran out, and that the peer gets through again by itself once it renewed: now part of `TestE2E_RenewalFailsPastExpiry`. In the lab it can't be shown; the server gives out 24-hour certificates only.
2. *(done 2026-11-25)* A renewal on the relay, of one and of both in the same moment: lab `TestRenewalOnTheRelay`, silent 0.7 to 0.85 s in 3 runs.
3. A new CA, or changed groups or address, arriving with a renewal.
4. A disk that is full or read-only when the new certificate is written.
5. A sleep longer than the life of the certificate, as one test from sleep to answer.
6. An open TCP session across a reload: pinned as a known gap, it is cut.

## 2026-11-25 — two short silences on both networks at once (field recorder)

`hop-field` (pull request 3) ran 4 hours between the two Macs: always connected, always direct, 599 of 600 probe answers per minute. Twice the probe was silent on `home` and `work` in the same instant (1.0 s at 17:38:30, 3.4 s at 18:04:11), no session lost, nothing in either agent's log. *Likely* the line under the mesh; not proven. A probe that runs beside it outside the mesh would tell.
**Partly answered 2026-11-27, still open.** It is the line under the mesh, and only at the office: home to home 0.007 to 0.014 % of pings lost, the phone's hotspot 0 of 1200, the office 0.15 to 0.81 %, in bursts. Nebula logged no dead tunnel, no roam and no failed handshake in those minutes, and the path stayed direct. **But it is not "the office network" as such:** in the worst hour `home` lost 9 % in both directions while `work` between the same two machines lost nothing, and the split holds over the whole 40 hours (0.59 / 0.63 % against 0.16 / 0.09 %). Round trip is the same in lossy and clean minutes, so nothing was queued. The lab with two networks on the same two VMs is clean (1000 of 1000 on each), and 2400 pings from the office the next morning were clean too. **Open:** which layer picks on one flow — the office edge or our own per-instance path. What would settle it is a wire count during a lossy minute, which `hop-field` cannot do today. Numbers: [[concepts/loss-on-a-limited-line]], "Idle loss, by place".

## 2026-11-25 — a router that comes back is sometimes noticed late (`TestRouterReboot`, 1 of 12)

**Why open:** In the full lab run before v0.11.28, `TestRouterReboot` failed once: the session was silent for 60.9 s where the router was away for 30 s (limit 45 s). The other 11 runs of that day gave 30.6 to 31.0 s, among them 4 with this branch and 4 with main, alternating, right after. From the log of the slow run: the tunnel was declared dead 20 s into the outage, and Nebula then asks for a new one at growing gaps (`try_interval` times the number of the try: 1, 4, 8, 12 s). The router was back at :32, the next try came at :34, TCP's own retry had fallen at 30.5 s after the cut, just before, and its next one is at 61 s. Not shown: why only this run declared the tunnel dead early enough to matter (it was the only one inside a full run, so load is a candidate), and whether main does it too (0 of 4, too few to say).
**Closed 2026-11-26:** it is TCP, not the path. It came again in a full run (60.9 s); neither agent had declared the tunnel dead and no handshake was needed at 61 s. The open connection tries again about 30.4 s and 61 s after the cut, and the first try falls 0.4 s after the router is back, or before it when the lab is busy. The test now asks with a new connection: it answers 0.1 to 1.2 s after the router is back (4 runs, limit 5 s), while the open session in those same runs was silent for 30.7, 30.9, 38.7 and 61.7 s. Its limit is 65 s now. An open SSH session can so stay frozen for up to a minute after a 30 s outage although the path works; that is the same on any VPN.

## 2026-11-24 — the network lab is not in CI

It takes about 9 minutes on an arm64 VM with 8 cores and needs root, `nft` and `/dev/net/tun`. GitHub's ubuntu runners have all three (the kernel-TUN test already uses `sudo unshare`). Not tried there; a busy runner may break the time limits. Start with `workflow_dispatch` and look at ten runs before it can block anything.

## 2026-11-23 — kernel TUN: what is still unproven

`TestKernelTunClose_ReleasesDeviceAndPort` (root, Linux) covers V, W and X. CI runs it in its own network namespace (`.github/workflows/ci.yml`); that step ran in CI for the first time on 2026-11-24 and passed; before that only the same commands in a local VM. **Not run:** the macOS half of patch 32 (needs root), and throughput of a Linux system-wide network with the device opened non-blocking (patch 31; the same thing wireguard-go does). **Action:** `iperf3` through a Linux system-wide network before and after, at the next dev deploy.

## 2026-11-23 — creating a network answers 201 although its lighthouse did not start

`CreateNetwork` only logs `[networks] failed to start lighthouse for …`. Seen on Linux because of V, which is fixed; any other cause (a port in use) still looks like success. **Action:** answer 500 and roll back, or show the network as "not running".

## 2026-11-23 — logs wanted from the MBP (the original trigger is not proven)

Why the certs ran out in that machine's case is inferred from released code, not from its logs. **Action:** before reinstalling, save `/var/log/hop-agent.log`, `/etc/hop-agent/*/{renewal-stuck,stuck-state,watcher-stuck}-*.txt`, `hop-agent status`, and `pmset -g log`.

## 2026-11-23 — reload the certificate in place instead of restarting Nebula

Every renewal (about every 12 h since the fix, 24 h before) closes the network and starts it again, which drops all tunnels, in-app connections and the web terminal's path. Nebula can swap certs through its config reload callbacks; we can't use it because `startNebula*` keeps `config.C` local. **Action:** keep the `config.C`, write the new cert, call `ReloadConfig`. Fall back to the restart if the reload is rejected. The test exists: `TestE2E_Lifecycle/force_renewal` pins today's behaviour with an open shell.

Source / context: `internal/client/renew.go::reloadNebula`, `internal/client/nebula.go`

## 2026-11-23 — do the macOS send bugs (I, J) explain earlier symptoms?

Idle tunnels dying, Phase G, the v0.10.35 keepalive, post-sleep "handshake sent" without a packet (patch 24), the 2026-05-14 same-CGNAT relay stall. All fit; none was re-tested. **Action:** re-test with patch 28 on two Macs before working on any of them; consider removing workarounds that turn out to be for this.

Source / context: [[concepts/macos-udp-send]]

## 2026-11-23 — macOS code is never tested in CI

CI is ubuntu-only, so all Darwin Nebula patches and their tests, the macOS part of the agent, and the 44 desktop `#[test]`s never run. `go test ./...` also skips vendored tests. **Action:** add a macos-latest job running `go test -mod=vendor -race ./internal/client ./internal/api github.com/slackhq/nebula/udp` and `cargo test`. Add govulncheck and `-race` on ubuntu. Fix first: `TestPeerCache_SaveSkipsWhenUnchanged` (flaky, about 1 in 40). Two more flakes fixed 2026-11-27 (`fix/windows-vm-findings`): tests that asked the kernel for a UDP port could get one another package's Nebula already holds, because Nebula binds with `SO_REUSEPORT` on Linux and `go test ./...` runs packages in parallel — now one helper takes ports from 20000–29999 and never hands one out twice in a run; and two assertions that checked a port, or a live instance, at a single instant now wait and report how long they waited. Open from the same runs: `TestE2E_DisconnectDuringRenewal` once saw `Connect` return no error and the instance not live 100 ms later; the mechanism is not found (15 runs on Linux and 8 under `-race` since, no reproduction). The only mechanism found that could do it is a reload closing the running Nebula before starting the new one (the v0.10.33 ordering, pinned by `TestReloadNebula_I1`), which leaves `control()` nil for up to 2 s of port-free wait plus the start — but nothing in that test fires a renewal within 100 ms of `Connect` (the loop's first check is one 60 s tick in), so it stays a guess. The test now waits out a swap of that size (live within 5 s) and then checks it is still live 3 s later, so the one-off cannot come back as a silent pass of a dead reconnect. What that run was **not**: the same test failing 9 times in 10 on the Mac the next morning, which was the test's own teardown — `t.TempDir` removed the instance dir while a renewal still in flight wrote `node.crt` into it, so the failure text was "directory not empty" and not the assertion. The harness now stops the client and waits for the renewal loop to leave before that removal (`recovery_e2e_test.go`), and the assertion checks `live` at a single instant again, so a real reconnect that comes back wrong still fails. On Windows the client's tests don't even compile: `renew_sleep_test.go` uses `syscall.SIGSTOP` without a build tag.

## 2026-11-23 — authorization: every network member gets a shell on every node

`requireNode` (`internal/api/proxy.go:68`) only needs `CanView`, so any member can open a terminal and run commands, as root on an installed service. Capabilities default to all (`terminal,health,forward`) and the agent never checks them. `NodeExec` audits no command. The mesh firewall is flat. Pinned by `TestE2E_MemberShell`. **Action:** terminal and exec need admin or an explicit per-node grant; check the capability in the agent too; audit the command.

Source / context: `docs/wiki/raw/evaluation/2026-11-23-code-and-ssh-review.md`

## 2026-11-23 — updates are unsigned; the install scripts still fail open

The agent's own update now refuses when the checksums can't be fetched, read or don't list the file (`TestE2E_Update`). Still open: the install scripts skip an empty checksum (`internal/api/distribution.go:502`, `scripts/install.sh:83`: `… SHA256SUMS … || true`); the checksums come from the same place as the binary; nothing is signed; `tauri.conf.json:42` ships `PLACEHOLDER_REPLACE_AT_RELEASE_TIME`. Whoever controls the control plane's downloads gets root on every node. **Action:** make the scripts fail closed (small), then sign with minisign or cosign and pin the key in the agent.

## 2026-11-23 — hardening notes from the evaluation

- Browser shell uses a predictable `$TMPDIR/hop-shell` (`cmd/agent/shell_unix.go`).
- Mesh DNS binds `0.0.0.0` (`internal/mesh/dns.go:88`).
- `encryption.key` sits beside the database (`cmd/server/main.go:107`).
- `features.md:36` says "end-to-end encrypted"; the browser terminal is plaintext in the control plane's memory.

## 2026-11-23 — agent loose ends

- In-app mode: `warmTunnel`, `warmPeersFromHeartbeat` and `probePeersOnce` dial through the OS, so they never reach the mesh.
- A lost `nebula.yaml` is never regenerated, and some writers use a non-atomic `os.WriteFile`. (2026-11-27: every writer now locks the file and writes it atomically, and a missing or damaged file is restored from `nebula.yaml.bak` before the network starts. Still open: with no whole backup either, nothing regenerates it and the network stays down until the device enrolls again.)
- `renew.go` (heartbeat `networkId` path) loads and saves its own copy of the enrollment registry: a concurrent save can be lost.
- An agent started with a cert past half its life restarts Nebula right after starting it.
- The error "another hop-agent on this device is using the network port" is shown for our own orphans too.

## 2026-11-18 — `renewNow` retry loop can mask a stuck renewal from the Phase P watchdog

`renewNow` retries up to 12 times with backoff reaching 30 min, stamping `lastRenewalActivityAt` on each attempt. While it is retrying (for example on 429s), the watchdog sees "activity" even though no renewal succeeds, and can't tell busy-failing from healthy. Renewals now have about 12 h of margin and the recovery loop covers the expired-cert end state, but a watchdog predicate on *last successful renewal age* versus cert NotAfter would catch it hours earlier. **Action:** consider stamping success separately, and alarm when `NotAfter - now < 2h` with no success in the last N attempts.

Source / context: `internal/client/renew.go::renewNow`; [[concepts/watchdog]]

## 2026-11-18 — `/local/renew` (force renew) returns 503 when there is no live instance

`handleForceRenew` needs an instance, so it can't help exactly when it's most needed (an enrollment down because of its cert). The Phase HH recovery loop makes this mostly moot, but it is a cheap improvement to fall back to `fetchAndInstallCert` + `connectAndSupervise` when there is no instance.

Source / context: `internal/client/local_api.go::handleForceRenew`

## 2026-11-18 — npm dev-tooling vulnerabilities need major upgrades

After `npm audit fix`, the dashboard frontend's **production** dependencies are clean, but its dev/build tooling still has 13 issues (8 high, 5 low) in @sveltejs/kit, vite, vite-plugin-svelte, svelte-check, picomatch, fdir, tinyglobby, cookie and bits-ui. The desktop client has 7 high (appdmg, image-size, vite, svelte-check, ...). All are build-time only, but they need `npm audit fix --force`-class **major** upgrades (SvelteKit / vite majors), which should be a dedicated, tested change. Cargo: 0 vulnerabilities; unmaintained/unsound warnings only (glib 0.18 via Tauri's gtk-rs, chrono, proc-macro-error, zbus/zvariant), which clear when Tauri moves its gtk stack.

## 2026-05-14 — Nebula relay path silently fails for same-CGNAT peers across different private subnets

Mini + MBP on home network both behind same Yettel BG CGNAT (`46.10.240.91`), but on DIFFERENT private LANs (mini `192.168.23.x`, MBP `172.17.132.x`). Direct P2P fails due to hairpin NAT (well-documented in CLAUDE.md Discovery Log `preferred_ranges`). Relay fallback through the lighthouse (`use_relays: true, relays: [10.42.1.1]`) SHOULD bridge them — but in this incident relay also timed out: MBP's log shows continuous `Attempt to relay through hosts relays=[10.42.1.1]` + `CreateRelayRequest` followed by `Handshake timed out` for vpnAddr 10.42.1.7. Manual `launchctl kickstart` on BOTH agents was the only recovery — kickstarting one alone wasn't enough. **Three hypotheses to test next session**: (1) Lighthouse's UDP source-addr cache for the mini was stale because the Nebula-UDP flow timed out on CGNAT while the HTTPS heartbeat channel stayed warm. Trace lighthouse logs on hopssh.com around 09:39 (2026-05-14) to see what destination addr it used when forwarding relay packets. (2) Yettel CGNAT hairpin restriction applies to UDP-from-lighthouse-public-IP-back-to-Yettel-internal-host too (single-NAT-but-three-hop relay). Compare with a peer on a different ISP. (3) Nebula's relay implementation has a stale-endpoint retry bug — read `vendor/github.com/slackhq/nebula/relay.go` flow. **Action**: when this recurs, capture the lighthouse-side log first (server-side `journalctl -u hopssh` filtered for `relayFrom=10.42.1.6 relayTo=10.42.1.7` or vice versa). The agent-side artifacts (kickstart logs + nebula.yaml + lighthouse: replaced static host map entry lines) are preserved in this session's transcript for reference.

**2026-11-24:** the same layout works in [[concepts/netlab]] (`TestSameProvider`: one provider that has both homes behind one address, forgets after 30 s and does not let them meet there; the relay answers within 0.4 s, also after two quiet minutes). So the layout is not the cause. *Likely*, not proven: what was found there since — after the server restarts, a relayed connection was dead for 72 to 84 s and both sides went on sending; and one packet from a stranger could close a relayed tunnel (patch 33). Both are fixed on the branch that has the lab. Look again if it happens with a release that has them.

Source / context: plan-of-record (`~/.claude/plans/we-re-starting-phase-valiant-wave.md` from this session); Discovery Log `preferred_ranges` note; v0.10.22 Phase G (HTTPS-distributed self-endpoints); v0.10.27.1 Layer 4 endpoint-probe disabled.

## 2026-05-07 — which vendor Nebula call wedged in the May 7 watcher incident

The watchNetworkChanges goroutine deadlocked at `cmd/agent/nebula.go:272-273` — either inside `ctrl.RebindUDPServer()` or `ctrl.CloseAllTunnels(true)`. We couldn't determine which from logs alone (no CRITICAL/PANICKED, no goroutine dump pre-fix). Phase DD's watchdog will produce a forensic goroutine dump on the next occurrence (`<configDir>/<name>/watcher-stuck-<ts>.txt`) — that dump will pinpoint the lock contention and inform whether to file an upstream Nebula issue or extend our vendor patches. **Action**: when the next watcher-stuck dump appears in the wild, walk the goroutine stack and identify the held mutex.

Source / context: [[incidents/2026-05-07-mbp-watcher-wedge]]

## 2026-05-07 — DNS resolution flap during MBP network changes

`dial udp4: lookup hopssh.com: no such host` fires intermittently during network-change events on MBP (observed at 17:18:56, 18:00:45 today; also in earlier sessions on 2026-05-05, 2026-05-06). Heartbeat goroutine recovers within seconds (separate goroutine, separate DNS resolution). Could be Go runtime cgo_resolver cache invalidation during interface flap, or macOS DNSExtensionConfig transitioning. Unrelated to the wedge itself (DNS error returns cleanly), but worth understanding if it correlates with the May 7 wedge timing or recurs as a primary failure mode. **Action**: low priority; investigate if a DNS-flap-driven incident appears.

Source / context: [[incidents/2026-05-07-mbp-watcher-wedge]] § "Open questions"

---

## Resolved

## 2026-11-18 — upgrade vendored Nebula v1.10.3 → v1.11.2 (resolved 2026-11-26, not released)

Upstream releases since our pin are v1.11.0, v1.11.1, v1.11.2 and v1.12.0 (v1.12.0 needs Go 1.27.3 and a gvisor jump of about 2.5 years, so it is a separate project). v1.11.2 brings:

- two firewall bypass fixes with no GHSA (IPv6 extension-header parsing #1789; SCTP/GRE classified as TCP/UDP #1840), plus nonce-reuse and relay replay-window hardening;
- a relay re-establishment fix (#1805), the same symptom class as the 2026-05-14 relay stall above;
- hostmap/roam race fixes (#1752, #1755, #1773, #1788, #1790);
- `Control.Wait` (#1794).

Patch impact:

- **Drop:** 01 (fixed upstream by PR #1661) and 03.
- **Probably drop:** 02, pending a test.
- **Rewrite:** 08 against upstream's `ListenOut` flush callback.
- **Real rework:** the macOS perf stack (04, 06, 07, 09, 12, 17, 19, 24), the control API (11, 20–22, which have no upstream equivalent), 15 and 26.
- **Keep as-is:** 14 and 25 (upstream `Expired()` still has no clock-skew tolerance).

Known advisory GHSA-69x3-g4r3-p962 is already patched in our v1.10.3 (and only affects P256; we use Curve25519).

**Action:** make `patch-vendor` fail loudly → bump → re-port patches one by one → run the full test suite plus the Phase E multi-host smoke test (mini + MBP + Linux + Windows), with cellular and WiFi perf A/B against `results/perf-20260425-003810/`.

Source / context: `docs/wiki/raw/nebula-upgrade/2026-11-18-upstream-survey.md`

**Resolved 2026-11-26** (branch `upgrade/nebula-1.11.2`, not merged, not released). The 34 patches became 10 files, one theme each (`patches/README.md`). Dropped: 01, 02, 03, 12, 13, 27, 30, 37. **Not** dropped although upstream looked the same: 31. Upstream's close wakes the reader and returns; on the Mac all was green, in the Linux VM the port or the device was still taken (kernel-TUN close test 4 of 5 red, userspace close every time). With waiting restored: 10 of 10. Tested: suite, `-race` on the Nebula packages and `internal/client`, `internal/mesh`, upstream's own e2e, `TestE2E_Upgrade` from v0.11.28, the lab (see `log.md`). Not done: Windows on a device, cellular.

## 2026-11-18 — vendor patch 12 never applies (silently, even in CI) (resolved 2026-11-26)

**2026-11-23: its code is in no clean build.** `listenInWorker` and `inBatch` exist only in the test patch 13 adds, so `go vet github.com/slackhq/nebula` fails, and release and Docker builds use the same steps. Published macOS binaries very likely never had the pipelined `listenIn` (not checked on a binary), and the 2.7× downlink numbers in `docs/performance.md` and `docs/macos-pipelined-listenin.md` describe dev builds. **Decide:** re-measure with and without it on the current tree, then port it properly or drop 12 + 13. Patch 28's `interface.go` hunk is made against a tree without 12.

Patch 12 fails on a blank-line context mismatch and `make patch-vendor` carries on: the Makefile runs each `patch` with `|| true` and then deletes `*.rej`, so **any** failed patch is silently ignored, in CI too. Either refresh patch 12 against vendored Nebula v1.10.3 or fold it into the Nebula upgrade below. Make `patch-vendor` **fail** on any patch that doesn't apply, and do that *before* the upgrade.

**2026-11-24: the Mac and the release build get different trees.** GNU patch also refuses hunk 3 of patch 19, which adds `applyOSThreadHints()` to the function patch 12 would have added. Apple's `patch` accepts it in the wrong place, `listenIn`, which then makes the same call twice. Nothing else differs and the call does nothing off macOS, so v0.11.27 is what was tested. But a test on a Mac does not prove that a patch applies on Linux, and "it applied" does not prove it went to the right place: check with `patch` of both kinds, with `--fuzz=0` where possible (2, 19 and 26 apply only with fuzz).

Source / context: `patches/`, `Makefile` `patch-vendor` target. `scripts/check-nebula-patch.sh` does not exist despite older docs.

**Resolved 2026-11-26** (branch `upgrade/nebula-1.11.2`): `make patch-vendor` stops at a patch that does not apply, and says "already applied" for one that is. Patches 12 and 13 are gone: the pipelined `listenIn` was in no build, so what was measured and shipped is the batch read of patch 07, which is kept (`read-loop.patch`). One thing learned on the way: Apple's `patch -R --dry-run` succeeds on a patch that is *not* applied, so the forward try has to come first.

## 2026-11-23 — before releasing the server: three checks (resolved 2026-11-24)

1. The MBP is **not** listed. No device is: the sweep (L) deleted the last two on 2026-08-07, seven days after they went quiet. Every device has to leave and enroll again. `HOPSSH_NODE_PRUNE_AGE_HOURS=0` is set in production since 2026-11-24 (log: `auto-prune disabled`), so nothing more is deleted before the new server runs.
2. The data volume is a local ext4 disk on the one worker, not NFS.
3. The database was copied with SQLite's online backup (`integrity_check: ok`; 3 orphan rows; `journal_mode: delete`, which confirms N on production).

## 2026-11-23 — what drains the production login rate limiter? (resolved 2026-11-24)

Devices that the server itself had deleted (L). A released agent that gets 401 gives up and is started again; one such device sent 3 renewals and 3 heartbeats about every 14 s. Between 2026-11-06 and 2026-11-23 the server answered 105,260 heartbeats and 102,721 renewals with 401 and 347,149 more with 429. Other devices behind the same address share that bucket. That it was this address is inferred, not shown. The fixed agent asks once an hour after a 401, and renewals have their own limiter.

## 2026-11-23 — the E2E tests had not run on Linux (resolved 2026-11-23)

Run in a local Fedora VM (arm64, kernel 6.12). Six tests failed because of one product bug (V, the UDP port outlives the network); a new root test for kernel TUN found two more (W, X). Fixed by vendor patches 31 and 32; everything passes there since. See [[incidents/2026-11-18-mbp-all-certs-expired]] V, W, X.

## 2026-11-18 — `TestTicker_SurvivesSIGSTOP` has a data race (resolved 2026-11-23)

Its tick counter is an atomic now; `internal/client` and `cmd/agent` pass with `-race`. Running the E2E tests with `-race` found one in the product (U).

## 2026-11-18 — does production pass `--trusted-proxy` behind Traefik? (resolved 2026-11-18: yes)

`oci_nomad_cluster/jobs/hopssh.nomad.hcl` sets `HOPSSH_TRUSTED_PROXY = "true"`, so rate-limit buckets are per the address in `X-Forwarded-For`. **Corrected 2026-11-23:** this does not show that buckets are per household, nor what exhausted one; see "what drains the production login rate limiter?" above. Caveat: `internal/auth/ratelimit.go::clientIP` takes the **leftmost** `X-Forwarded-For` entry. That is safe only while Traefik replaces client-supplied `X-Forwarded-For` (its default for untrusted sources). If a CDN or another proxy is ever put in front, revisit this, because leftmost-XFF becomes client-spoofable and the limiter can be bypassed.

## 2026-05-09 — Linux desktop client stuck onboarding + "Load failed" on Connect (resolved 2026-05-10)

Resolved in v0.11.19 (commit 10ff0c9). Root cause: `cmd/agent/main.go` skipped `c.Start` on the no-enrollments branch (fresh installs), but `client.StartLocalAPI` was always called — the local API's auto-connect handler then panicked on nil `c.runCtx` during `context.WithCancel`. Phase NN regression: pre-extraction the equivalent closure used the always-live `renewCtx`. Fix is two-layer (always Start before StartLocalAPI; defensive nil guard in connect). Live-verified on the aarch64 Ubuntu VM. See [[incidents/2026-05-09-linux-desktop-stuck-onboarding]] § Resolution for the captured panic + lessons.
