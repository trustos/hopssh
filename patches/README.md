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
| 11 | `portmap-advertise-addr.patch` | Lets `internal/portmap/` inject the public `IP:port` from NAT-PMP/UPnP/PCP into the lighthouse's advertise_addrs set at runtime. Enables direct P2P across asymmetric carrier NAT (home router + cellular peer). | Feature (NAT) |
| 12 | `pipeline-listenin-darwin.patch` | Splits `listenIn` into a reader goroutine (`recvmsg_x` on the TUN fd) and a worker-flusher goroutine (`consumeInsidePacket` + `sendmsg_x`), linked by a 2-slot channel pipeline. Overlaps the two blocking syscalls — profile showed 71% of CPU was in `Syscall6`. 2.7× WiFi LAN downlink improvement (137 → 343 Mb/s), now at Tailscale parity. Architecture doc: [`docs/macos-pipelined-listenin.md`](../docs/macos-pipelined-listenin.md). | Perf (Darwin) |
| 13 | `pipeline-listenin-test.patch` | Tests for patch 12 — channel-discipline invariants: init-doesn't-deadlock (regression-protects the channel-sizing bug we hit on first deploy), FIFO ordering through worker, drain-on-close shutdown, back-pressure blocks reader. Mutation-validated. | Test |
| 14 | `roam-suppress-window-darwin.patch` | Bumps `RoamingSuppressSeconds` from 2 → 10 in `hostmap.go`. The 2-second window failed to dampen 2-address alternation under hairpin-NAT (peer reachable via both LAN and NAT-PMP-reflected public addresses); the hostmap entry flipped every ~2s, dropping in-flight UDP and producing visibly choppy LAN screen-share. 10s blocks the typical alternation while still recovering quickly from real WiFi→cellular roams. | Bug fix (Darwin perf) |
| 15 | `roam-prefer-ranges-darwin.patch` | Adds preferred_ranges awareness to `handleHostRoaming` in `outside.go`. If the current remote is in preferred_ranges (LAN) and an inbound packet comes from a non-preferred address (NAT-reflected public), the roam is ignored entirely. Symmetric: a preferred candidate when on a non-preferred current immediately wins. Stops the hairpin-NAT flap at its source rather than relying on the time window from patch 14. | Bug fix (Darwin perf) |
| 16 | `roam-stability-tests.patch` | Tests for patches 14 + 15: regression guard that `RoamingSuppressSeconds ≥ 5`, plus table-driven coverage of the `isAddrInRanges` helper used by the data-plane preferred_ranges check. | Test |
| 17 | `pipeline-listenout-darwin.patch` | Symmetric counterpart to patch 12 on the inbound (UDP→TUN) path. Splits `udp.StdConn.ListenOut` into a reader goroutine (`recvmsg_x` on the UDP fd) and a worker goroutine (callback × N + `Flush()`), linked by a 2-slot channel ring. Overlaps the reader's recvmsg_x block with the worker's decrypt+TUN-write+sendmsg_x work. Closes the v0.10.12 Tailscale-vs-hopssh uplink gap (346→target ≥440 Mb/s). | Perf (Darwin) |
| 18 | `pipeline-listenout-test.patch` | Tests for patch 17 — channel-discipline invariants mirroring patch 13: init-no-deadlock (mutation-validated, regression-protects the v0.10.10-dev1 channel-sizing bug recurring on this side), FIFO order, drain-on-close, back-pressure blocks reader, slot-count sanity. | Test |
| 19 | `qos-class-darwin.patch` | Sets macOS QoS class `USER_INTERACTIVE` on the four `LockOSThread`'d packet-processing OS threads (listenIn reader + worker, listenOut reader + worker). Empirically drops RTT mean from 30 → 25 ms (-17%) and stddev from 42 → 35 ms (-17%) on Mac mini Ethernet ↔ MBP WiFi (200-ping A/B/A test). Default-on for darwin; `HOPSSH_QOS=off` escape hatch. CGO; `qos_hint_darwin.go` (build-tag `darwin && cgo && !e2e_testing`) + `_other.go` stub (`!darwin || !cgo || e2e_testing`). The `cgo` build tag matters: a CGO_ENABLED=0 darwin cross-compile (e.g. CI without macos-latest) silently falls through to the stub instead of failing with "undefined: applyOSThreadHints". To put the gain into the released binary, `.github/workflows/release.yml` runs darwin builds on `macos-latest` with `CGO_ENABLED=1`. Mechanism: per the pprof profile, ~67% of CPU during sustained low-pps ICMP is in `runtime.findRunnable` / scheduler coordination — biasing the scheduler with `pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE)` reduces wakeup latency. See `docs/macos-latency-research.md`. | Perf (Darwin) |

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
| — | `sndbuf-env-knob.patch` | `HOPSSH_UDP_SNDBUF` env var for overriding macOS `SO_SNDBUF`. Tested 4KB → 512KB: all sizes produce identical p50/p95/p99 latency. The knob was never useful, defaulted off, and documented as "don't use this." Dropped to reduce maintenance surface. (The `11` slot is now used by `portmap-advertise-addr.patch`.) |
