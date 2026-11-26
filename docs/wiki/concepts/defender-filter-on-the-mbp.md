---
type: concept
title: hop's download to the MBP was slow for six hours (Defender's filter looked at every packet of ours; why it started and stopped is not known)
status: open
last_compiled: 2026-11-26
sources:
  - vendor/github.com/slackhq/nebula/udp/udp_darwin.go
---

# hop's download to the MBP was slow for six hours

On 2026-11-26 a download through hop to the MBP reached 25 to 68 Mbit/s from 14:02 to about 20:20, while Tailscale did 157 and ZeroTier 137 to 189 in the same minutes. Before (11:36 to 12:16) and after (from 20:29) it was 145 to 185, as fast as the other two.

It came back for five minutes at 21:37.

**What is established:** in the slow hours Microsoft Defender's network extension (`netext`, `com.microsoft.wdav.netext`, a socket filter) handled every packet of hop's sockets; in the fast hours it did not. **What is not:** why it began and why it ended. Nothing of ours changed at either moment.

**Read this before looking for a slow receive path in the agent on a managed Mac, and before raising the socket's send buffer.**

## The two states

CPU on the MBP (`top`, 5 s) during 8 s runs from and to the mini:

| | slow state (14:02 to 20:20) | fast state (from 20:29) |
|---|---|---|
| hop download | 36 to 59 Mbit/s, `netext` 50 to 57 % | 145 to 179, `netext` 3 to 9 % |
| hop upload | 178 to 185 Mbit/s, `netext` 96 to 104 % | 123 to 188, `netext` 3 to 7 % |
| ZeroTier download | 142 to 189 Mbit/s, `netext` absent in 4 runs of 5, once 69 % | 143 to 190, `netext` 4 to 8 % |
| hop download, sender capped at 20 and 40 Mbit/s | 20.0 and 39.7, `netext` 5 to 6 % | |
| idle | `netext` 10 % | |

A sample of `netext` in the slow state shows it in `NEFilterDataExtensionProviderContext handleData:…forFlow:` and `resumeFlow:withVerdict:`: it is asked for a verdict on the data of a flow again and again, where a filter that has allowed a flow is not asked any more.

## When it began and ended

| time | what happened on the MBP | hop download |
|---|---|---|
| about 11:15 | restart | |
| 11:36 to 12:16 | | 146 to 185 |
| 13:20 | on the phone's hotspot | 54 (the hotspot's speed) |
| 13:54 | back on the office Wi-Fi | |
| 14:02 to 20:20 | several restarts of the agent in between | 25 to 68 |
| 20:14 | switched to the office's other Wi-Fi name (same gateway, subnet and channel: one network) | |
| 20:1x to 20:2x | 106 "Flush failed … resource temporarily unavailable" lines, tunnels flapping | 36 to 38, then failing |
| from 20:29 | | 145 to 179 |

Both changes follow a change of network on the MBP by some minutes. No restart of the agent changed anything, and none of the filter's processes restarted (all run since 11:06). The filter writes nothing to the system log.

## What it is not

- Not the path: direct both ways in both states.
- Not the packet size: tunnel MTU 1280 to 1420 on the MBP, all 32 to 64 Mbit/s in the slow state.
- Not the line: capped at 10 to 40 Mbit/s the slow state passes clean.
- Not Tailscale on the mini, not the mini.
- Not how the socket is used. A test program sending 6000 to 16000 packets of 1400 bytes a second to the MBP over ZeroTier's interface, received on a wildcard socket, a bound one, a connected one, with `recvmsg_x` and `sendmsg_x`, as root: `netext` stayed idle in every variant. (Over hop itself the test cannot run: hop's firewall blocks UDP between devices.)
- Not the missing signature as such: `iperf3` is signed ad hoc like the agent.

## The send buffer: ruled out

The agent's sockets have the system's send buffer of 9216 bytes, ZeroTier's have 1 MB. With a filter, what waits for its verdict counts against the send buffer, so a small one could mean that the sender stops for the filter every six packets. A test agent set 1 MB on the network `work` only (`port == 4242` in `NewListener`) and left `home` at 9216. Both did 156 to 176 Mbit/s for 50 minutes; then the slow state came back (21:37) and both were measured in the same minutes:

| | `home`, 9216 bytes | `work`, 1 MB | ZeroTier |
|---|---|---|---|
| download | 36, 38, 42, 43 Mbit/s | 10, 10, 10, 9 | 129 to 166 |
| upload | 174, 174, 170 | 60, 87, 55 | 165 to 191 |
| `netext` | 51 to 112 % | 56 to 80 % | 0 to 9 % |

**The big buffer is worse by a factor of three to four.** With it nothing tells the agent to wait, and its packets queue behind the filter instead. The small buffer is the back-pressure that keeps the slow state at 40 Mbit/s. Do not raise it.

## The second episode (21:37 to 21:42)

Fast at 21:27, slow at 21:37, fast again at 21:42 right after a restart of the agent (135 and 159 Mbit/s, `netext` not among the first eight). Looked at as triggers, and not it:

- **The agent's rebind** ("Local network changed, rebinding the udp listener", logged at 21:35:37): provoked six times with a temporary address on `lo0`; 147 to 170 Mbit/s after each, `netext` idle. There was also one at 21:13 with fast runs after it.
- **A screen-sharing session to the MBP** ended at 21:35:25. The day's sessions (13:20 to 13:47, 20:14 to 21:35) do not line up with the slow hours.
- **A restart of the agent** was followed by the fast state once (21:42). In the afternoon the agent was restarted at 15:56, 15:58 and 16:39 and the runs after stayed slow, so it is not a cure.

## The send limit works

The 106 "Flush failed" lines at 20:1x to 20:2x are the send limit of one second ([[incidents/2026-11-26-mbp-send-stuck-in-kernel]]) ending sends that the kernel did not take. Without it each of them could have been this morning's four minutes.

## What is left to us

The filters are the Mac owner's company's; nothing was changed on them and an exclusion is not to be had. In the slow state the filter does not hold anything up as long as the sender does not burst (20 and 40 Mbit/s pass with `netext` idle), so an SSH session, a terminal or a screen share do not see it; a large download does.

## How to see it

- `top -l 2 -s 5 -n 8 -o cpu -stats command,cpu` on the MBP during a download: `netext` above 30 % is the slow state.
- `sample <pid of netext> 3` shows `handleData:` per packet.
- `systemextensionsctl list` shows which filters a Mac has.
- `netstat -anv -p udp | grep hop-agent`: columns 8 and 9 are the receive and send buffer.
- The kernel's "full socket buffers" counter (`netstat -s -p udp`) rises with every packet on such a Mac and says nothing. `netstat -s -p tcp` stays at 0 on macOS 26.
- `iperf3 -M` does not work on macOS; to send smaller packets lower the tunnel's MTU on the receiver (`ifconfig utunN mtu 1280`) and set it back.
