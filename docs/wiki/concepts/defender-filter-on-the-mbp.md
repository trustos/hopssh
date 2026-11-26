---
type: concept
title: A content filter that inspects our packets (Microsoft Defender on the MBP caps hop's download)
status: current
last_compiled: 2026-11-26
sources: []
---

# A content filter that inspects our packets

On the MBP a download through hop reached 34 to 68 Mbit/s on 2026-11-26 from 14:02 on, while Tailscale did 157 and ZeroTier 137 to 189 in the same minutes and hop's upload stayed at 180. The cause is on the MBP and is not ours: **Microsoft Defender's network extension (`netext`, `com.microsoft.wdav.netext`) looks at every packet of the agent's UDP socket and at none of ZeroTier's.**

**Read this before looking for a slow receive path in the agent on a managed Mac.**

## The measurement

CPU on the MBP (`top`, 5 s) during 8 s downloads from the mini:

| | speed | `netext` | agent |
|---|---|---|---|
| idle | | 11 % | 5 % |
| hop | 43 and 57 Mbit/s | 56 % | 21 to 29 % |
| ZeroTier | 189 and 184 Mbit/s | not among the first seven | (`zerotier-one` 60 %) |
| hop, upload | 178 Mbit/s | 96 % | 47 % |

About 4500 packets a second arriving cost the filter half a core; it has one. Sending costs it a third of that per packet, so the upload still fits.

## What it is not

- Not the path: direct both ways (handshakes from the public addresses, no relay).
- Not the packet size: tunnel MTU 1280, 1340, 1380, 1400 and 1420 on the MBP, all 32 to 64 Mbit/s. No fragments at either end; pings of 1392 bytes pass.
- Not the line: with the sender capped, 10 and 30 Mbit/s pass clean (2 retransmits), 60 and 100 give 37 to 39.
- Not Tailscale on the mini: the same with it on and off.
- Not the mini: nothing in its log, the agent is not among its busiest processes, and it sends 180 Mbit/s to the MBP over ZeroTier.

## What is not known

- Why hop's downloads were 146 to 185 Mbit/s between 11:36 and 12:16 the same day. `netext` had been running since about 11:19. The MBP changed Wi-Fi in between (gateway 172.17.132.1 since 14:02 at the latest); a filter policy that depends on the network would fit, and is a guess.
- Why ZeroTier and Tailscale are not inspected. Tailscale is a network extension of its own; ZeroTier is a plain daemon like ours.
- Whether a signed agent, or the planned network extension, is treated differently.

The filter is the Mac owner's company's; nothing was changed on it.

## How to see it

- `top -l 2 -s 5 -n 7 -o cpu -stats command,cpu` on the receiver during a download: `netext` (Defender), `com.checkpoint.fw.filter` (Check Point).
- `systemextensionsctl list` shows which filters a Mac has.
- The kernel's "full socket buffers" counter (`netstat -s -p udp`) rises with every packet on such a Mac and says nothing.
- `iperf3 -M` does not work on macOS; to send smaller packets lower the tunnel's MTU on the receiver (`ifconfig utunN mtu 1280`) and set it back.
