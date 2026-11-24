---
type: concept
title: macOS UDP send path (batches, and what must never wait for one)
status: fixed in v0.11.27 (2026-11-24), not yet run on a real Mac fleet
last_compiled: 2026-11-23
sources:
  - patches/04-batch-udp-darwin.patch
  - patches/09-priority-queue-darwin.patch
  - patches/17-pipeline-listenout-darwin.patch
  - patches/28-darwin-send-outside-batch.patch
  - vendor/github.com/slackhq/nebula/udp/udp_darwin_batch_test.go (applied by patch 28)
  - internal/client/recovery_e2e_test.go
---

# macOS UDP send path

**TL;DR.** Our macOS patches queue outgoing packets and send many with one `sendmsg_x`. From patch 04 (v0.7.x) to v0.11.26, *every* write was queued and only some callers flushed. Anything else waited for an unrelated packet. One refused destination also dropped every packet queued behind it. Patch 28 fixes both. Linux and Windows never had this path.

## What went wrong

**I: packets held back.** Flushes happened in three places: the kernel-TUN read loop, `ListenOut`, and the handshake manager. These writers had none and waited until someone else flushed:

- hole punches and lighthouse updates;
- tunnel tests and close-tunnel messages;
- relay requests;
- *all* data of an in-app (userspace) network.

Measured on HEAD with two in-app nodes on one machine:

| | v0.11.26 | with patch 28 |
|---|---|---|
| first mesh connection | never, in 20 s | after 1 ms |
| 60 s idle | two bursts, both set off by inbound packets | a lighthouse update every 10 s and a punch about every 10 s, both ways |

**J: one bad address dropped the rest.** `sendmsg_x` stops at the first destination the kernel refuses (`no route to host`) and the rest of the queue was discarded: 100 of 100 good packets lost behind one dead LAN address. Nebula sends handshakes to all known addresses in one batch and sorts LAN addresses first, so a peer that left the LAN takes the good addresses down with it.

## The rules

1. **A write outside `BeginBatch`/`EndBatch` is sent immediately.** Only the two packet loops open a batch, and both always end it.
2. **One `sendmsg_x` per run of packets to the same destination.** A refused destination loses its own packets only.
3. **A write after `Close` returns `net.ErrClosed`.**

Tests: `TestWriteToOutsideBatchSendsNow`, `TestRefusedDestinationDoesNotDropOthers`, `TestWriteToAfterClose` (vendored; run with `go test -mod=vendor github.com/slackhq/nebula/udp`), `TestE2E_KeepaliveOnUserspaceNetwork` and `TestE2E_RefusedAddressFirst`. **None of them run in CI**: it is ubuntu-only and `./...` skips `vendor/`.

## Cost

Throughput was not re-measured. Bulk data to one peer is still one `sendmsg_x` per batch. Traffic to several peers interleaved in one batch now takes one call per run. Compare with `results/perf-20260425-003810/` before release.

## Unproven

Earlier symptoms would fit: tunnels dying when idle, Phase G ("cellular peer goes silent"), the v0.10.35 keepalive, the 2026-04-26 post-sleep "handshake sent" without a packet (patch 24), and the 2026-05-14 same-CGNAT relay stall. None of them was re-tested. Do that before adding more workarounds, and consider removing those that turn out to be one.

## Backlinks

- [[../incidents/2026-11-18-mbp-all-certs-expired]], [[../open-questions]]
