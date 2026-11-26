---
type: concept
title: hop's download to the MBP is slow on one office Wi-Fi (cause not established; Defender's filter is the suspect)
status: open
last_compiled: 2026-11-26
sources: []
---

# hop's download to the MBP is slow on one office Wi-Fi

On the MBP a download through hop reached 25 to 68 Mbit/s on 2026-11-26 from 14:02 on, while Tailscale did 157 and ZeroTier 137 to 189 in the same minutes and hop's upload stayed at 180. Between 11:36 and 12:16 the same day it was 146 to 185.

**The cause is not established.** Two things are known:

1. The MBP rejoined Wi-Fi at 13:54 after a test on the phone's hotspot (agent log: "network change detected"), and the downloads are slow since then. The office has more than one Wi-Fi; it may be on another one than at noon (the user, 2026-11-26). Now: gateway 172.17.132.1, 802.11ax, channel 48, 40 MHz, -53 dBm.
2. Microsoft Defender's network extension (`netext`, `com.microsoft.wdav.netext`) uses half a core during hop's downloads. In two of three comparisons it was not among the busiest processes during ZeroTier's; in the third it was at 69 % and ZeroTier still did 142 Mbit/s. So it is a suspect, not the cause.

**Read this before looking for a slow receive path in the agent on a managed Mac.**

## The measurement

CPU on the MBP (`top`, 5 s) during 8 s downloads from the mini:

| | speed | `netext` | agent |
|---|---|---|---|
| idle | | 11 % | 5 % |
| hop | 43 and 57 Mbit/s | 56 % | 21 to 29 % |
| ZeroTier | 189 and 184 Mbit/s | not among the first seven | (`zerotier-one` 60 %) |
| hop, an hour later | 59 Mbit/s | 55 % | 38 % |
| ZeroTier, an hour later | 142 Mbit/s | 69 % | (`zerotier-one` 27 %) |
| hop, upload | 178 Mbit/s | 96 % | 47 % |


## What it is not

- Not the path: direct both ways (handshakes from the public addresses, no relay).
- Not the packet size: tunnel MTU 1280, 1340, 1380, 1400 and 1420 on the MBP, all 32 to 64 Mbit/s. No fragments at either end; pings of 1392 bytes pass.
- Not the line: with the sender capped, 10 and 30 Mbit/s pass clean (2 retransmits), 60 and 100 give 37 to 39.
- Not Tailscale on the mini: the same with it on and off.
- Not the mini: nothing in its log, the agent is not among its busiest processes, and it sends 180 Mbit/s to the MBP over ZeroTier.

## The other office Wi-Fi (20:14)

The user switched the MBP between the office's two Wi-Fi names during the day; which one it was on at noon is not certain. After a switch at 20:14 (agent log: network change): the same gateway, subnet and channel, so the two names are one network. hop 38 and 36 Mbit/s with `netext` at 54 and 50 %; ZeroTier 180 and 186 with `netext` not among the first seven; hop upload 185 with `netext` at 104 %.

So the Wi-Fi name is not it. `netext` was absent in four of five ZeroTier runs and present in every hop run, which leaves the filter as the likelier of the two suspects. Still not explained: the fast downloads at noon. The MBP had been restarted at about 11:15; a filter that gets its full rules some time after a start would fit, and is a guess.

## What is not known, and the test that decides

- Whether it is this Wi-Fi (something on it that slows unknown UDP towards the MBP: Tailscale's and ZeroTier's ports are well known, ours are not) or the filters on the MBP (their rules may depend on the network).
- **The test:** the MBP on the other office Wi-Fi, or at home: one hop download, one ZeroTier download, `top` beside both. Fast there with `netext` as busy: it is the network. Fast with `netext` quiet: it is the filter's rules.
- The MBP's TCP counters (`netstat -s -p tcp`) stay at 0 on macOS 26, so loss cannot be told from reordering there.

The filters are the Mac owner's company's; nothing was changed on them.

## How to see it

- `top -l 2 -s 5 -n 7 -o cpu -stats command,cpu` on the receiver during a download: `netext` (Defender), `com.checkpoint.fw.filter` (Check Point).
- `systemextensionsctl list` shows which filters a Mac has.
- The kernel's "full socket buffers" counter (`netstat -s -p udp`) rises with every packet on such a Mac and says nothing.
- `iperf3 -M` does not work on macOS; to send smaller packets lower the tunnel's MTU on the receiver (`ifconfig utunN mtu 1280`) and set it back.
