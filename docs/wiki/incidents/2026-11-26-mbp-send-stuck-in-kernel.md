---
type: incident
title: 2026-11-26 — MBP: one UDP send did not return for four minutes, the network was dead and could not be closed
status: fixed on the branch fix/followups-v0.11.29, not released; the send limit is not shown against a real stuck send
last_compiled: 2026-11-26
sources:
  - patches/darwin-udp.patch
  - vendor/github.com/slackhq/nebula/udp/udp_darwin.go
  - vendor/github.com/slackhq/nebula/udp/udp_darwin_batch_test.go
  - internal/client/recovery.go
  - internal/client/watchdog_restart_test.go
---

# 2026-11-26 — MBP: one UDP send did not return for four minutes

## What happened

MBP, agent v0.11.29, during an 8 s `iperf3` download over the network `home` (11:27:44). The network went silent; `work`, in the same process, went on.

- Mini: "Handshake timed out" for the MBP from 11:27:44, also over the relay; tunnel dead at 11:28:22.
- MBP: socket of `home` with 1.78 MB waiting to be read (`netstat -anv`), agent at 2 % CPU.
- 11:31:04 the keepalive watchdog fired (3 cycles of 90 s) and wrote `stuck-state-20261126T093104Z.txt`.
- 11:31:24 `network "home" did not shut down within 20s`; then four reconnects with `address already in use`.
- 11:31:46 I ended the agent by hand (SIGQUIT); launchd started a new one and the network was back in 10 s.

Outage: 4 minutes. Left alone, the agent would have ended itself after three more failed rounds of the recovery loop, about 5 minutes later.

## Cause

The dump shows one goroutine `[syscall, 4 minutes]` in `sendmsg_x` (`udp.(*StdConn).sendRun` ← `flushLocked` ← `EndBatch` ← `listenIn`) and three waiting for `sendMu`: the reader's worker, the connection manager's test packet, and one more.

1. The macOS socket is a blocking one (it always was, since old patch 04), so that `recvmsg_x` can block in the reader. A send on it can wait in the kernel without limit.
2. Sends hold `sendMu`. With the lock held nothing else is sent, not even a handshake, and `Close` needs the same lock: the network could not be closed, so its port stayed taken.

Why the kernel did not return is not established. The MBP runs two content filters (Check Point, Defender) and two more VPN extensions; with a filter, held data counts against the socket's send space and the sender waits. That is a guess. The mini, which has none, has not shown it.

## Fix

- **A send may wait one second** (`SO_SNDTIMEO` in `NewListener`, `patches/darwin-udp.patch`). After that the packets of that flush are dropped, as when the buffer is full. Not `MSG_DONTWAIT`: on a Mac with a filter waiting is the normal back-pressure, and every wait would become a drop.
- **A network that did not shut down and whose port is still taken ends the agent at once** (`restartInstance`), so launchd, systemd or the SCM start a new one. Before, the recovery loop first failed three more times.

## Tests

- `TestListenerSendHasTimeLimit` (vendored `udp`, macOS): the listener's socket has a send limit of at most 5 s. A UDP send cannot be made to block in a test, so the test checks the limit itself. Red without the fix.
- `TestRestartInstance_WedgedAndBusyExitsAtOnce`: exit only when both hold (close timed out, port busy). Red without the fix.
- CI now runs the vendored macOS tests on a Mac (`race` job); before, they ran nowhere but on a developer's machine.

## Measured after the fix

Test build on both Macs, 8 `iperf3` runs of 8 s over `home`: no freeze, no send timed out (0 "Flush failed" lines), 123 to 178 Mbit/s.

## Open

- The limit has not been seen to end a real stuck send: the freeze came once in about 20 runs and not again.
- `Close` still waits for `sendMu`. With the limit that is at most a second per send.
