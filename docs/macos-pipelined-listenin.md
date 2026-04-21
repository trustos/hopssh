# macOS pipelined listenIn (vendor patch 12)

> **TL;DR** — Splitting Nebula's `listenIn` (outbound TUN→UDP path) into
> a reader goroutine and a worker-flusher goroutine, linked by a 2-slot
> channel ring, lets `recvmsg_x` and `sendmsg_x` overlap. WiFi LAN
> downlink jumped from 137 → **343 Mb/s** on Apple Silicon (Mac mini ↔
> MacBook Pro, 4-stream iperf3, direct-P2P), at parity with Tailscale's
> NEPacketTunnelProvider-backed 346–411 Mb/s. No NEPacketTunnelProvider
> required, no Apple Developer ID, no extra threads beyond one Go routine.
>
> Status: shipped in v0.10.10. Darwin-only — see [Cross-OS applicability](#cross-os-applicability).

## Table of contents

1. [Background — what patches 04–08 already gave us](#background)
2. [Why the single-goroutine batch loop hit a wall](#why-the-single-goroutine-batch-loop-hit-a-wall)
3. [The pipeline design](#the-pipeline-design)
4. [Channel sizing and the init deadlock](#channel-sizing-and-the-init-deadlock)
5. [Back-pressure and ordering invariants](#back-pressure-and-ordering-invariants)
6. [Shutdown discipline](#shutdown-discipline)
7. [Benchmark methodology and results](#benchmark-methodology-and-results)
8. [Cross-OS applicability](#cross-os-applicability)
9. [What's next — listenOut symmetric split + userspace TSO](#whats-next)
10. [Vendor patch reference](#vendor-patch-reference)

## Background

Nebula on macOS userspace utun has, prior to v0.10.10, the following
outbound-packet hot path inside `interface.go::listenIn`:

```
for {
    n := tun.ReadBatch(packets[:32])              // recvmsg_x on utun fd (patch 06)
    for j := 0; j < n; j++ {
        f.consumeInsidePacket(packets[j], ...)     // firewall + crypt + queue UDP send
    }
    f.writers[i].Flush()                           // sendmsg_x for whole batch (patches 04+08)
}
```

This already amortizes both TUN-read and UDP-send across N packets via
XNU's private batch syscalls. From the v0.9.x baseline (one syscall per
packet), patches 04–08 brought tunnel efficiency from ~17% of raw WiFi
to ~50%. That's where v0.10.9 sat.

## Why the single-goroutine batch loop hit a wall

At ~140 Mb/s on WiFi LAN with the patch-07 path, a CPU profile of
`hop-agent` under sustained iperf3 looked like:

| % CPU | Function | What |
|---|---|---|
| **71%** | `runtime.Syscall6` | dominated by `sendmsg_x` inside `Flush()` |
| 9% | `runtime.kevent` | netpoller waking the goroutine for the next ReadBatch |
| 9% | `runtime.pthread_cond_*` | scheduler wake/park |
| 8% | other syscall (TUN read) | `recvmsg_x` |
| <1% | AES-GCM Seal | crypto barely registers |

Crypto is irrelevant. The cost is being parked in the kernel waiting
for one of two batch syscalls to return. And critically: **the same
goroutine alternates between them**. While we're stuck inside
`sendmsg_x`, no one is reading from the TUN; the kernel TUN-side
buffer fills. While we're stuck inside `recvmsg_x` waiting for the
next burst, no one is writing to UDP.

That serialization is the bottleneck. The two syscalls each take real
time in the kernel, and we're doing them strictly back-to-back.

## The pipeline design

Patch 12 splits `listenIn` into two goroutines linked by channels:

```
                ┌─────────────────────────────────┐
                │  reader (was listenIn)          │
                │                                 │
                │   for {                         │
                │     batch := <-free             │
                │     n := tun.ReadBatch(batch)   │
                │     batch.n = n                 │
                │     inflight <- batch           │
                │   }                             │
                └────────────┬────────────────────┘
                             │
                  inflight (size 2, FIFO)
                             │
                             ▼
                ┌─────────────────────────────────┐
                │  worker (listenInWorker)        │
                │                                 │
                │   for batch := range inflight { │
                │     for j := 0; j < batch.n;    │
                │       consumeInsidePacket(...)  │
                │     writers[i].Flush()          │
                │     free <- batch               │
                │   }                             │
                └────────────┬────────────────────┘
                             │
                   free (size 2, FIFO)
                             │
                             └────► back to reader
```

Two pre-allocated `inBatch` slots (each holding 32 `mtu`-sized buffers)
circulate forever between the two goroutines. At steady state:

- One slot is sitting in `inflight` (filled by reader, waiting for worker).
- The other slot is being processed by worker, OR being filled by reader.

While the worker is parked in `sendmsg_x`, the reader can already be
parked in `recvmsg_x` filling the next slot. The two blocking syscalls
**overlap** instead of serializing — pure latency hiding, no extra
goroutines beyond one Go routine.

The non-batch path (kernel TUN, FreeBSD, etc.) is preserved unchanged
behind a `useBatch` interface check, so this only takes effect on
macOS where `recvmsg_x` is available on the utun fd.

## Channel sizing and the init deadlock

The channels are sized **equal to the number of slots** (`numSlots = 2`),
not 1. This matters during initialization:

```go
const numSlots = 2
inflight := make(chan *inBatch, numSlots)
free     := make(chan *inBatch, numSlots)

for k := 0; k < numSlots; k++ {
    slot := &inBatch{packets: makeBuffers(listenInBatchSize)}
    free <- slot   // ← would deadlock on iteration k=1 if cap(free) < numSlots
}
go f.listenInWorker(inflight, free, i)
```

The init loop pushes both slots onto `free` **before** the worker
goroutine has been started. If `free` were sized 1, the second `free <- slot`
would block forever (no reader). This was an actual bug we hit on the
first deploy: the agent's TUN-side reader silently stalled, no traffic
flowed, and the only symptom was missing log lines after `Nebula interface is active`.

The general invariant: **the buffered capacity of `free` must be ≥
the number of slots** (and same for `inflight` if anything ever pushes
multiple slots to it concurrently before a reader exists). Sizing
both channels to `numSlots` keeps the rule trivial regardless of how
many slots we add later.

## Back-pressure and ordering invariants

**Back-pressure.** With both channels sized `numSlots`, at most
`numSlots` slots can sit in either channel at a time. When the
worker is slow (e.g. blocked in `sendmsg_x`), `inflight` fills up and
the reader's next `inflight <- batch` blocks. When `free` is empty
(no slot returned yet), the reader's `<-free` blocks. Either way,
the reader cannot run ahead of the worker by more than `numSlots`
batches. No unbounded queueing, no memory growth.

**Ordering.** This is non-negotiable: TCP segments within a single
flow must arrive at the peer in the order they were sent. The
priority-queue patch (09) discovery is the canonical lesson —
splitting packets by size reordered TCP segments → SACK fired → TCP
treated it as congestion → throughput collapsed (320 → 96 Mb/s).
Patch 12 preserves order on two axes:

1. **Within a batch:** the worker iterates `for j := 0; j < batch.n;`
   in array order, calling `consumeInsidePacket` strictly in the order
   the reader filled the slot.
2. **Across batches:** `inflight` is a FIFO Go channel — slots come
   out in the order they went in. With only one reader and one worker,
   batch N is fully processed before batch N+1 starts.

Combined, every packet read from TUN reaches `Flush()` in the exact
order it was read. No reordering is introduced.

## Shutdown discipline

When the reader sees `ErrClosed`/`ErrClosedPipe`/`io.EOF` (which is
how Nebula signals "the agent is shutting down, stop reading"), it
calls `close(inflight)` and returns. The worker's `for batch := range
inflight` loop then drains any remaining queued slots, processes
them, and exits naturally when the channel is closed and empty.

The `free` channel is intentionally not closed — only the reader ever
sends to it (when retrying on `EAGAIN`), and once both goroutines have
exited, the channel and its slots are GC'd as a unit.

This matches Nebula's broader expectation that interface tear-down
goes through `f.closed.Store(true)` then `tun.Close()`, which causes
the next `ReadBatch` to return one of the recognized "closing" errors.
The shutdown propagates from the TUN side outward.

## Benchmark methodology and results

**Setup:**
- Mac mini M-series (router-side, Ethernet → home WiFi router)
- MacBook Pro M-series (client, on the same home WiFi)
- Both on the `home` Nebula network, direct-P2P verified via `Send handshake from <peer-LAN-IP>:4243`
- iperf3 4-stream, 15 second runs, both directions

**Procedure (reproducible):**
```bash
# On MBP:
iperf3 -s -B 10.42.1.11 -p 5202 -D

# On Mac mini, downlink (mini ← MBP):
iperf3 -c 10.42.1.11 -p 5202 -t 15 -P 4 -R

# Uplink (mini → MBP):
iperf3 -c 10.42.1.11 -p 5202 -t 15 -P 4
```

**Numbers (2026-04-21):**

| Direction | v0.10.9 (single-goroutine) | v0.10.10 (pipelined) | Tailscale (NEPacketTunnelProvider) |
|---|---|---|---|
| Downlink (Mac mini ← MBP) | 126–137 Mb/s | **343 Mb/s** | 346–411 Mb/s |
| Uplink (Mac mini → MBP) | 172 Mb/s | 174 Mb/s | 199 Mb/s |
| Downlink retransmits (15s, 4 streams) | high (varied) | 3 196 | n/a |
| Direct-P2P RTT | 4–5 ms | 4–5 ms | 4–5 ms |

Downlink: **2.7× improvement, at Tailscale parity.** Uplink: unchanged
— the remaining delta to Tailscale is on the inbound `listenOut`
(UDP→TUN) path which is **not** pipelined yet (see [What's next](#whats-next)).

**Non-regressions verified:**
- Cellular DL still 49 Mb/s on MTU 1420 (no change from v0.10.9 measurement).
- macOS Screen Sharing HP mode still works with the documented retry-once pattern.
- Direct-P2P establishment still successful from cold start (peer reached via 192.168.23.18:4243).
- `go test ./...` green across all hopssh-side packages.

## Cross-OS applicability

**The pipeline is Darwin-only by design** — the `useBatch` branch is
only entered when the TUN reader implements `batchReader` (i.e.
`recvmsg_x` is available on the utun fd, currently true only on macOS
via patch 06). Other platforms fall through to the unchanged single-
goroutine `Read` loop.

This is intentional, not a porting gap:

| OS | Why this pipeline doesn't help (or doesn't apply) |
|---|---|
| **Linux (kernel TUN)** | Profile bottleneck differs. Tailscale's Linux throughput advantage comes from TSO/GSO + per-flow segment coalescing, not from a goroutine pipeline. CLAUDE.md records: "Linux `sendmmsg` with batch-flush HURTS single-stream performance" — applying this pattern over unbatched Linux sends would do nothing useful and could regress single-stream. The Linux roadmap is the userspace TSO step. |
| **Windows (WinTun)** | Different driver model — userspace ring buffers, no `sendmsg_x` equivalent, no batch syscall to overlap. Bottleneck shape unstudied; no profile data justifies copying this pattern over. |
| **FreeBSD / kernel-TUN-on-Linux fallback** | Falls through `if !useBatch` → original per-packet `Read` loop. Pipeline literally does nothing here. |

The Darwin-only scoping is what makes patch 12 small (~120 lines) and
self-contained. A future cross-platform pipeline would be a different
patch, with its own profile-driven justification.

## What's next

The pipeline only covers the **outbound** TUN→UDP direction (`listenIn`).
The **inbound** UDP→TUN direction (`listenOut` → `readOutsidePackets`
→ TUN write) is still single-goroutine. The uplink benchmark shows
the bottleneck has now moved there: when the Mac mini is the SENDER,
its outbound is fast (343 Mb/s would be the symmetric peak), but the
peer's INBOUND processing — which IS the bottleneck for the peer's
receive direction — is still the un-pipelined path. We measured that
ceiling at ~174 Mb/s.

Symmetric pipelining of `listenOut` is the natural next step. Same
pattern: reader (`recvmsg_x` on UDP) + worker (`readOutsidePackets`
+ TUN write batch). Estimated win: another 1.5–2× on uplink, putting
us at full Tailscale parity in both directions.

The sequel after that — if needed — is userspace TCP segment
coalescing (TSO-equivalent at the encrypt boundary). Documented in
the `we-have-previously-ran-rustling-hopcroft.md` plan as Step 3,
but probably unnecessary if the listenOut split closes the uplink gap.

## Vendor patch reference

| Item | Path |
|---|---|
| Patch file | [`patches/12-pipeline-listenin-darwin.patch`](../patches/12-pipeline-listenin-darwin.patch) |
| Patch series inventory | [`patches/README.md`](../patches/README.md) |
| Touched vendor file | `vendor/github.com/slackhq/nebula/interface.go` (added `inBatch`, `listenInWorker`; rewrote `listenIn` batch path) |
| Test file | `vendor/github.com/slackhq/nebula/interface_pipeline_test.go` (added in patch 13) |
| Test patch file | [`patches/13-pipeline-listenin-test.patch`](../patches/13-pipeline-listenin-test.patch) |

To re-apply after re-vendoring: `make patch-vendor` (or `make vendor`,
which runs both steps). The patch is checked for clean application by
`scripts/check-nebula-patch.sh` along with the bug-fix patches.
