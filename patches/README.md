# Nebula vendor patches

Applied on top of `slackhq/nebula` via `make patch-vendor` (see Makefile).
Each patch is numbered for ordering; they apply cleanly in sequence.

## Inventory

| # | File | What | Category |
|---|---|---|---|
| 01 | `graceful-shutdown.patch` | Adds `io.ErrClosedPipe` check to `interface.go` so userspace-mode shutdown doesn't `os.Exit(2)` the whole control plane. Upstream issue [#1031](https://github.com/slackhq/nebula/issues/1031), fix PR [#1375](https://github.com/slackhq/nebula/pull/1375) (not yet merged). | Bug fix |
| 02 | `testreply-panic-fix.patch` | Fixes a nil-pointer panic in the handshake test-reply path. | Bug fix |
| 03 | `tun-darwin-read-buffer.patch` | Caches the TUN read buffer (`make([]byte, len+4)` → pre-allocated), eliminating ~9KB/packet allocation churn on macOS. | Perf (alloc) |
| 04 | `batch-udp-darwin.patch` | `sendmsg_x` / `recvmsg_x` batch syscalls for UDP on macOS. Pure Go, no CGO. Tunnel efficiency went from 17% → 35–53% of raw WiFi throughput. | Perf (Darwin) |
| 05 | `batch-udp-darwin-test.patch` | Tests for patch 04. | Test |
| 06 | `batch-tun-darwin.patch` | `recvmsg_x` on `utun` for TUN batch reads on macOS. Companion to 04 that unlocks batch-level efficiency for inbound. | Perf (Darwin) |
| 07 | `batch-listenin.patch` | `batchReader` interface + `Flush()` call in `listenIn` after each TUN-read batch. Glue that activates 04 + 06. | Perf (Darwin) |
| 08 | `conn-flush-interface.patch` | Adds `Flush() error` to `udp.Conn` interface. Clean API extension; platforms without batch support implement it as a no-op. | API |
| 09 | `priority-queue-darwin.patch` | 2-lane control/data priority queue in the `sendmsg_x` send path. Control packets (handshakes, lighthouse, test, close) jump ahead of data packets; data lane preserves within-flow FIFO ordering. | Perf (Darwin) |
| 10 | `priority-queue-tests.patch` | Tests for patch 09. | Test |

## Upstreamable patches

01, 02, 03, and 08 are clean, self-contained fixes/extensions that upstream
`slackhq/nebula` would benefit from. 01 already has an upstream PR (#1375).
The others can be filed as individual PRs when bandwidth allows. Reducing
the vendor-maintained set is a long-term goal.

## Patches retained despite marginal measured benefit (09, 10)

The priority queue (09/10) showed **no measurable user-visible improvement**
under our test conditions (single Mac mini ↔ laptop pair on WiFi): "throughput
preserved (no regression), but ping-under-load improvement is within WiFi
noise" (see `CLAUDE.md` §Discovery Log).

It is retained because:

1. **The test harness doesn't exercise the failure mode it defends against.**
   Our benchmarks are 1-on-1 LAN with light control traffic. The patch
   prevents control-lane starvation under bulk load — a failure mode that
   would manifest during mass-enrollment events, saturating transfers with
   concurrent lighthouse keepalives, or tunnel-test packets competing with
   bulk data. These scenarios haven't been stress-tested.

2. **The cost is bounded.** ~250 lines across 09 + 10. Hot-path overhead is
   one byte read (`b[0] & 0x0f`) per packet, negligible at any realistic pps.
   Rebase risk is real but low since we don't bump Nebula frequently.

3. **Dropping working defensive code based on a null result from a test that
   doesn't exercise the failure mode it defends against is an engineering
   anti-pattern** — see the "null result on defensive code" lesson in
   `CLAUDE.md` §Engineering Lessons.

**Revisit on next major Nebula version bump** if rebase friction is high,
or drop opportunistically when we ship a Linux send-path that has its own
priority story.

## Dropped patches

| # | File | Why dropped |
|---|---|---|
| 11 | `sndbuf-env-knob.patch` | `HOPSSH_UDP_SNDBUF` env var for overriding macOS `SO_SNDBUF`. Tested 4KB → 512KB: all sizes produce identical p50/p95/p99 latency. The knob was never useful, defaulted off, and documented as "don't use this." Dropped to reduce maintenance surface. |
