---
type: concept
title: Loss on a limited line (what the retransmits of a short upload are, and why we don't prevent them)
status: current
last_compiled: 2026-11-26
sources:
  - internal/netlab/limited_linux_test.go
  - internal/netlab/wire_linux_test.go
---

# Loss on a limited line

Between two Macs 1 to 13 % of a short `iperf3` upload through the tunnel was sent again. It looked like a fault of ours. It is not: it is how a TCP sender finds the speed of a line, and a connection without the tunnel does the same on the same line.

**Read this before trying to make the tunnel lose fewer packets.**

## What happens

1. A TCP sender that starts doubles its speed every round trip until something is lost.
2. A line holds what it cannot send yet in a queue: about 100 ms of it on the Wi-Fi line measured, about 240 ms on a phone's uplink.
3. The sender fills that queue and goes on. When it is full, everything above the line's speed is lost at once: hundreds to thousands of packets.
4. The sender falls back to what the line takes. From then on nothing more is lost.

Per second of an 8 s upload, MBP to mini (Mbit/s / retransmits / TCP window in kB):

| run | 1 | 2 | 3 | 4 | 5 to 8 |
|---|---|---|---|---|---|
| hop, Wi-Fi | 220 / 2376 / 2060 | 141 / 2867 / 1474 | 176 / 1279 / 540 | 153 / 72 / 414 | 0 to 49 retransmits a second |
| hop, Wi-Fi | 200 / 1754 / 2945 | 173 / 1614 / 740 | 184 / 273 / 557 | 160 / 0 / 606 | 0 |
| hop, phone's hotspot | 38 / 615 / 1129 | 15 / 406 / 800 | 16 / 249 / 396 | 18 / 0 / 430 | 0 to 47 |
| ZeroTier, Wi-Fi | 146 / 280 / 357 | 161 / 0 / 461 | 165 / 5 / 409 | 158 / 3 / 329 | 0 to 9 |

ZeroTier loses less because it is itself the slowest part (its packets are not lost on the line at all, it drops them itself), so the line's queue never fills. It is also slower.

## Where the packets are lost

One upload on the hotspot, counted at every layer (2026-11-26):

- MBP: 22434 UDP packets sent, 22456 packets left its Wi-Fi interface, none dropped in the interface's queue.
- Mini: 21722 packets arrived at its interface, 21615 at UDP.

So 3.3 % were lost between the two interfaces, on the line. Not in the agent, not in either Mac.

## In the lab

`TestLimitedLine` (`internal/netlab/limited_linux_test.go`): a line of 20 Mbit/s (10 for the mobile one) with a queue of 5, 20, 100 or 200 ms, a sender that doubles until something is lost (Reno), 15 s through the tunnel and 15 s beside it, twice.

| queue | through the tunnel | beside it |
|---|---|---|
| 200 ms | 18.2 Mbit/s, 397 segments sent again (1.55 %) | 19.1 Mbit/s, 364 (1.44 %) |
| 100 ms | 18.2, 229 (0.90 %) | 19.1, 201 (0.80 %) |
| 20 ms | 17.4, 63 (0.26 %) | 19.0, 102 (0.41 %) |
| 5 ms | 13.3, 20 (0.11 %) | 15.5, 76 (0.38 %) |
| mobile, 50 ms | 8.5, 132 and 43 | 9.1, 75 and 108 |

The same with and without the tunnel, and more with a deeper queue. With Linux's usual sender (CUBIC with HyStart, which stops doubling when the round trip gets longer) it is 0.05 to 0.9 % everywhere.

The test fails when the tunnel has more than twice the retransmits of the connection beside it plus 50, or less than 70 % of its speed. Checked with a planted fault (the tunnel drops every 100th packet): 3.3 against 19.1 Mbit/s, all five lines fail.

The lab's line got a queue depth for this: `Wire.Queue`, 200 ms if not set. `Cable.full` counts what did not fit.

## What was tried against it, and why it is not in

**Idea:** the queue is on the line, so it cannot be seen. But what waits in it comes back later. After every 32 packets to a host a test request goes out with the time in it; the other side sends it back, as it does with any test request (so it needs nothing new there). The time it took, less the shortest seen, is how long packets wait. When that is above 20 ms, one TCP packet with data is not sent, and the sender slows down a few hundred packets earlier than it would have.

**In the lab:**

| variant | queue 200 ms | queue 100 ms | speed |
|---|---|---|---|
| none | 397 lost on the line | 229 | 18.2 Mbit/s |
| drop after the wait was above 20 ms for 100 ms (as CoDel) | 411 and 404 | 234 and 238 | 16 to 17 |
| drop at once | 0 and 63 | 189 and 231 | 16, mobile 6.2 instead of 8.6 |

A packet that is not sent is noticed by the sender only after its place has passed the line's queue. So the sign is always late by the length of the queue, and it helps only where the queue is about twice as long as the round trip.

**On the two Macs, Wi-Fi, drop at once:**

| | speed | retransmits |
|---|---|---|
| before | about 180 Mbit/s | 2458 to 5582 |
| prototype, upload | 69, 37, 50 Mbit/s | 54, 21, 20 |
| prototype, download | 3.9 Mbit/s | 300 |
| after it was removed | 147 Mbit/s | 3939 |

The Wi-Fi holds packets for about 100 ms now and then, with no load of ours (the prototype's log: waits of 91 to 106 ms against a shortest of 4 ms, on both Macs). Each one looks like a full queue. Waiting for the delay to last filters these out, and is then too late for the start, as the lab shows.

**So:** a sign that comes from the delay is either too late or wrong on a radio link. This is the same reason HyStart is unreliable on Wi-Fi.

## What would work, and what it costs

Being the slowest part ourselves: measure what the line delivers (the answers to the test requests give that too, by how far apart they come back), and send no faster. Then the queue is ours, and what we drop from it is noticed at once. This is a congestion control of its own inside the tunnel, as BBR is one: a queue and a pacer per peer in the packet path, an estimate that must be right on a line whose speed changes, and a way up again when the estimate is too low. Timers in the send path have cost TCP half its speed here before (CLAUDE.md, "Timer-based send batching breaks TCP").

Not built. What it would remove is a burst of retransmits in the first two or three seconds of a large transfer, which TCP repairs by itself and which the same transfer has without any VPN. An SSH session, a terminal, a screen share never send enough at once to see it.

## How to measure it again

- Per second: `iperf3 -c <mesh address> -t 8 -J`, then `intervals[].streams[0]` (`retransmits`, `snd_cwnd`).
- Where packets are lost: before and after a run on both ends, `netstat -s -p udp` (datagrams output / received), `netstat -ibdn -I en0` (packets out / in), `netstat -qq -I en0` (dropped in the interface's queue).
- Before a run on a phone's hotspot: `route -n get default` (is it really the hotspot), `nettop -P -L 1` (is anything else sending).
- `networkQuality` does not show in `netstat -s -p tcp`: it does not use the kernel's TCP.
- TCP between the Macs does not use ECN (`netstat -s -p tcp | grep -i ecn`: all 0), so marking instead of dropping is not an option there.
