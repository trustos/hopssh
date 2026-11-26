---
type: concept
title: The network lab (real server and root agents behind simulated routers)
status: current
last_compiled: 2026-11-26
sources:
  - internal/netlab
  - scripts/netlab.sh
  - internal/client/path_watch.go
  - patches/paths.patch
  - patches/control-api.patch
---

# The network lab

**TL;DR.** `make netlab` runs the real `hop-server` and real `hop-agent`s, as root with a kernel TUN device, on simulated networks: home routers, carrier NAT, relays, port mapping, packet loss, sleep, outages. One command, one machine, about 9 minutes, nothing left behind; not part of CI. Run it before and after touching paths, relays, roaming, the Nebula patches or `nebulacfg`.

## How to run it

```bash
make netlab                                            # everything
make netlab ARGS="-test.v -test.run TestTwoHomes"      # one test
HOP_NETLAB_BIN=<dir> make netlab                       # the hop-server and hop-agent in <dir>: an old release, a planted bug
```

- On Linux it asks for `sudo`. On a Mac it uses the running podman VM: the programs are copied to `/var/tmp/hop-netlab`, run there as root, and removed.
- Needs `ip`, `nft` and `/dev/net/tun` there. No download.
- **One run at a time** on one machine (the directory is fixed).
- At the end it prints a report: per test the paths taken, the longest silence of a session, transfer speeds. Read it also when everything passes; a limit in a test is looser than what is normal.
- `go test ./...` skips it.

## What it is

Everything lives in one private tree of Linux namespaces (network, mounts, processes). When the test process ends, the kernel removes it all.

| Piece | What it does |
|---|---|
| `Lab` | one test's own world: an internet (203.0.113.0/24), a server with its lighthouse and relay, an owner |
| `Node` | a machine with its own network, hostname, `/etc`, `/run` |
| `Agent` | a machine that enrolled with a token and runs `hop-agent serve` as root |
| `Router` | NAT with netfilter: `Home` (keeps the port, every Linux-based home router) or `Carrier` (a new random port per destination: mobile networks, hotels). Optional: NAT-PMP, hairpin, firewall rules, short NAT memory, a new address, behind another router |
| `Cable` | a line with delay, jitter, loss and a rate, which can be cut |
| `Session` | a connection in use: a short message every 100 ms and back; measures the longest silence, and counts the new tunnels |

Two lines of a new test: `a, b, ra, rb := l.twoHomes(Home, Carrier)` and `s := openDirect(a, b)`. `want(a, b, "direct", d)` waits for a path, `bulk` sends megabytes, `s.close("what happened", limit)` checks the silence; `s.closeSteady`, where nothing changes, also that nobody made a new tunnel.

## What it found

"Before" is v0.11.27, "now" the same commit with the changes below (2026-11-24, Linux arm64).

| Test | Before (v0.11.27) | Now |
|---|---|---|
| `TestTwoHomes` | stays on the relay in about 1 run of 3 | direct at once, 20 MB at 44 to 70 Mbit/s |
| `TestServerRestart`, relayed connection | silent 72 to 84 s | silent 20.3 to 20.9 s: 10 s after the server is back |
| `TestNewPublicAddress` | silent 60.7 s | 7.6 s |
| `TestRoamingSameAddress` | silent 60.6 s | 7.6 s |
| `TestRelayBecomesDirect` | still on the relay after 2 min | direct after 2 s |
| `TestLeaveTheLAN` | does not get back to the LAN | back on the LAN, silent 6.6 s |
| `TestTwoPhones` | 800 ms silent, 6 needless new tunnels in 2 min | 250 ms, none |
| `TestOneWayLAN` | first answer after 42 s | 0.3 s |
| `TestOneWayDirect` | silent 2 min 18 s, transfers fail | 0.16 s, 24 to 68 Mbit/s |
| `TestOneDirectionBreaks` | not measured | 7.5 to 15.7 s (see 11) |
| `TestRenewalOnTheRelay` (new 2026-11-25) | not measured | 0.7 to 0.85 s, for one renewal and for both ends at once |
| `TestLimitedLine` | a line with a speed limit and a queue: the tunnel loses no more than a connection beside it, and is not much slower. [[concepts/loss-on-a-limited-line]] | |
| `TestRoaming`, `TestRouterReboot`, `TestRelayedPeerMoves`, `TestRenewalDuringSession`, `TestBadLine`, `TestHairpin`, `TestPortMappingStrict`, `TestSleep`, `TestSameProvider` | as good as now | |

`TestRoaming`, home to mobile, is either about 3.8 to 4.4 s or about 7.6 to 7.8 s (13 runs, also with an older state of the path loop). 2 s of them are without any network. `watchNetworkChanges` looks every 5 s: when that look came 3 s after the cable was out, the session was silent for 3.75 s, when it came after 4 s, for 7.6 s. *Likely:* TCP doubles its waits, so its retry at 3.75 s just finds the new path or just misses it. A message from the kernel instead of a look every 5 s would end that (not built).

**On Nebula v1.11.2** (2026-11-26, branch `upgrade/nebula-1.11.2`, one full run): 30 of 30 pass, and every silence is in the range of the table: `TestServerRestart` relayed 20.8 s, `TestNewPublicAddress` 7.5 s, `TestRoamingSameAddress` 7.5 s, `TestLeaveTheLAN` 6.6 s, `TestTwoPhones` 240 ms and no new tunnel, `TestOneWayLAN` 110 ms, `TestOneWayDirect` 160 ms, `TestOneDirectionBreaks` 7.5 and 15.3 s, `TestRenewalOnTheRelay` 0.83 and 0.70 s, `TestRoaming` 3.7 and 3.0 s, `TestRouterReboot` 30.5 s. The patch numbers below are those against v1.10.3; `patches/README.md` says in which file each is now.

### Causes

1. **The relay sticks.** When two devices meet, the handshake over the relay often overtakes the direct one, and Nebula looks for a direct path again only after 1000 packets. Fixed by `path_watch.go` (`relayed`).
2. **A restart of the server** silenced every relayed connection until Nebula's dead-tunnel timer (about 40 s, and once more for the relay). Fixed by `path_watch.go` (`silence`).
3. **`preferred_ranges` was never in effect** since v0.9.1: our config put it under `lighthouse`. Fixed in `nebulacfg` / `renew.go`: an old config's own list is moved to the top level when the agent starts, and one that has none gets ours. Patch 15 works for the first time.
4. **A new public address** was 60 s of silence: same timer, same fix.
5. **One stray packet closed a relayed tunnel** (an unauthenticated RecvError). Fixed by patch 33.
6. **Roaming to a network that gives out the same address** is not seen by `watchNetworkChanges`. Covered by `silence`.
7. **A path that works in one direction** was kept as long as packets arrived. Fixed by patches 34 and 35.
8. **A LAN that works in one direction** went dead once (3) was fixed, because patch 15 then preferred it. Fixed by patch 35: nobody moves to another path before an answer came back on it.
9. **A path that takes less than 1480 bytes** and does not say so loses big packets. Pinned, see below.
10. **Machines that met at their router's own LAN address** stay there: that address is in the range of the LAN, so Nebula thinks it is on the LAN already. Fixed by `path_watch.go` (`lan`).
11. **After a repair Nebula went back to the broken tunnel** for up to 10 s, because it remembered packets from before the repair. Fixed by patch 36 (`TestE2E_NewTunnelStays`: red 3 of 3 without it). What is left of `TestOneDirectionBreaks` is detection: the deaf side notices only when it has sent something itself, at worst 8 s after the break, and then TCP's next retry decides between 7.5 and 15.4 s.
12. **The first answer took the old path now and then** (in patch 35 itself, found by one run too many: `TestOneWayLAN` failed once in about 20 tries, with 1.13 s instead of 0.3 s). Nebula handed the first packet to the network card and only then looked where it came from. The kernel answers within microseconds, and if it was quicker, its answer went into the path that does not work, and TCP tried again after 1 s. Patch 35 moves before it hands over the packet now. Proof, `TestE2E_StartsWhereBothHear` in the VM: as it was 40 of 40 pass with 43 to 77 ms; with 3 ms planted before the move 19 of 20 fail with 1.02 s; with the fix and the same 3 ms 10 of 10 pass with 43 to 70 ms. `TestE2E_MovesBeforeItAnswers` sees it without luck: red 10 of 10 without the fix on the Mac and in the VM (one of the 20 for another reason, a port in use), green 10 of 10 with it on both.

Nebula's own end-to-end tests (`e2e/`, on the Mac, v1.10.3 and v1.11.2) with all our patches: all pass but one. `TestUncleanShutdownRaceLoser` waits for a dead tunnel to go away that is gone before it starts to wait (patch 35 sends one more packet, whose answer gets a RecvError); not a fault. They are not part of any script.

## Pinned gaps

Known to be wrong, checked by a test that will say when it is no longer so (`KnownGap`):

- **UDP between devices does not get through.** Our own mesh firewall allows TCP and ICMP only (`TestUDPBetweenDevices`). mosh, QUIC, WireGuard and Screen Sharing's media need it. Whether to allow it is a policy decision.
- **A network without UDP has no way out** (`TestNoUDP`). Tailscale has a relay on TCP 443 for that.
- **Big packets get lost** on a line that takes less than 1480 bytes and does not say so (`TestSmallPackets/told_false`). Small ones get through, so a login works and a listing hangs. Our TUN MTU of 1420 needs 1480 on the way; 1280 broke Screen Sharing (see CLAUDE.md, "TunMTU"). Looking for the MTU per path (DPLPMTUD) was never built.

## What it does not show

- macOS and Windows: the batched UDP and TUN code (patches 03–10, 17–19, 24, 28) does not run here.
- Real carriers. `Carrier` is the strictest kind netfilter can do; nobody's actual equipment is imitated.
- IPv6, and networks whose agents run inside the program (userspace).
- Loss on a mobile line; the web terminal and `exec` through the server while a path changes.
- Anything that takes hours: certificates, a device that is away for a week.

## Rules

1. A path bug found by hand gets a lab test that is red first. Run it on the old state with `HOP_NETLAB_BIN` and write down both numbers.
2. Two things the path loop (`internal/client/path_watch.go`) must never do: close a tunnel, and look for another path to a peer whose direct path works. The second made paths flap in v0.10.28. The one exception is a peer that *says* it has an address in one of our own subnets and that we send to elsewhere: only that address is tried.
3. A limit that a change breaks is not raised, it is a finding. `TestLeaveTheLAN` wanted the LAN within 30 s; the first version of patch 35 made it fail 3 of 3, and for a few hours the test sent 10 MB first to get it green. The cause was real: the two machines had met at the router's own address, 192.168.1.1, which is in the range of the LAN, so Nebula thought that it was on the LAN already. Before patch 35 a handshake that happened for another reason had hidden that.
4. Never test a path with what arrives. What comes over the relay says nothing about the direct path, and what comes from an address says nothing about the way back to it.
5. A wide limit hides a bimodal result. `TestOneDirectionBreaks` passed with 15.4 s and would have passed for ever; the second run said 30.9 s. Read the numbers, run a new scenario at least three times, and explain the spread before moving on.
6. After the Mac slept, compare `date` in the VM with the Mac before a run. The VM's clock was 53 minutes behind and was set forward in one step 13 s into a run, which was thrown away.
7. `signal: killed` at the end of a run is the VM's memory (8 GB), not a result. Whatever ran beside it has to be repeated. A run that logs without end is itself a finding.

## Planted bugs

47 faults were planted in the first version of this change, one at a time (2026-11-24). **39 were noticed**, and for each of them the message of the failing test was read and names the fault:

- 29 by the fast tests (`path_watch_test.go`, `path_question_test.go`, `ensurep2p_test.go`, `recovery_e2e_test.go` in `internal/client`; those in `path_question_test.go` need a second loopback address, so some are skipped on a Mac).
- 10 by the lab only: a server that does not relay, no port mapping, no cache of endpoints, no endpoints in the heartbeat, patch 33 left out, direct or relayed packets not counted, Nebula's own search for a better path that moves before the answer, and the two about a tunnel that starts on a relay.

Not noticed by anything, and why:

| Fault | Why nothing shows it (from reading the code, not measured) |
|---|---|
| no rebind when the network changes | `path_watch.go` repairs the same thing on Linux within 8 s. The rebind is for macOS, which the lab can't run |
| no punching in the config | Nebula's lighthouse makes the other side punch anyway |
| what comes from any address counts as direct | whoever hears nothing repairs the path, and that is the other side |
| the side that asked for a tunnel may also start elsewhere | comes right with the next answer |
| a tunnel may start elsewhere again after its first packet | the same; the check matters when packets are not counted (then the two ask each other without end) |
| no question after starting elsewhere | `path_watch.go` asks within a second |
| the question before the answer to the handshake | only another order |
| no hello after a handshake | the first real packet does the same |

So these lines could go without a test noticing. They are kept because they act at once where the loop needs a second or more; do not take them for proven.

25 more were planted in what the first review of the pull request led to: the watchdog, the time limit and the panics of the path loop (17), the moved `preferred_ranges` (4), the lighthouse as a peer (1), whole ticks (2), and a needless new tunnel every 30 s, which all seven tests that use `closeSteady` or count tunnels notice (1). All were noticed, two only after their tests were sharpened: a time limit of an hour, and a watchdog that does not forget its first look.

After the second review, 4 in the log lines of the path loop: three noticed, the fourth delays a line by a second and changes nothing else.

Four of the 47 were done again: one had been planted so that it changed nothing (the next line still protected), and three had run beside one that filled the VM's memory. Three tests were sharpened because a fault got through: `TestPathWatch_SilentPeerOnTheRelay` is new, `TestE2E_MeetOnTheLAN` and `TestE2E_StartsWhereBothHear` no longer depend on which copy of a handshake arrives first, and `TestOneWayLAN` / `TestOneWayDirect` want the first answer within 0.8 s and no silence over 1 s.

## Backlinks

- [[e2e-tests]], [[macos-udp-send]], [[../open-questions]]

## The host must be idle

The lab VM shares this Mac's cores. With a load average of 31 on 18 cores (a game and two other VMs) `TestLimitedLine/5ms` failed in every run, with the binaries of the commit that added it too: tunnel 4 to 9 Mbit/s against 13 to 16 at a load of 5. Check `uptime` before a run and note it with the numbers.
