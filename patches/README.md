# Nebula vendor patches

Applied on top of `slackhq/nebula` v1.10.3 via `make patch-vendor` (see Makefile), in number order.

> **Patch 12 does not apply, and `make patch-vendor` hides it** (`|| true`, then `*.rej` deleted). On Linux (GNU patch, where releases are built) the third hunk of **patch 19** is refused too, because it belongs to 12's function; the Mac's `patch` puts it in `listenIn` instead, where it repeats one harmless call. All other hunks apply with both, and the two trees are otherwise the same (checked 2026-11-24). Release and Docker builds use the same steps, so patch 12's code is in none of them, and its test (13) keeps `github.com/slackhq/nebula`'s tests from compiling. See [[open-questions]] in the wiki. The vendored UDP tests do run:
>
> ```bash
> go test -mod=vendor -race github.com/slackhq/nebula/udp
> ```

## Inventory

| # | File | What | Category |
|---|---|---|---|
| 01 | `graceful-shutdown.patch` | Adds `io.ErrClosedPipe` check to `interface.go` so userspace-mode shutdown doesn't `os.Exit(2)` the whole control plane. Upstream issue [#1031](https://github.com/slackhq/nebula/issues/1031), closed PR [#1375](https://github.com/slackhq/nebula/pull/1375); fixed upstream by #1661 in v1.11.0. **Not sufficient alone: see 27.** | Bug fix |
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
| 12 | `pipeline-listenin-darwin.patch` | **DOES NOT APPLY, in no clean build.** Splits `listenIn` into a reader goroutine (`recvmsg_x` on the TUN fd) and a worker-flusher goroutine (`consumeInsidePacket` + `sendmsg_x`), linked by a 2-slot channel pipeline. Overlaps the two blocking syscalls — profile showed 71% of CPU was in `Syscall6`. 2.7× WiFi LAN downlink improvement (137 → 343 Mb/s), now at Tailscale parity. Architecture doc: [`docs/macos-pipelined-listenin.md`](../docs/macos-pipelined-listenin.md). | Perf (Darwin) |
| 13 | `pipeline-listenin-test.patch` | Tests for patch 12 — channel-discipline invariants: init-doesn't-deadlock (regression-protects the channel-sizing bug we hit on first deploy), FIFO ordering through worker, drain-on-close shutdown, back-pressure blocks reader. Mutation-validated. | Test |
| 14 | `roam-suppress-window-darwin.patch` | Bumps `RoamingSuppressSeconds` from 2 → 10 in `hostmap.go`. The 2-second window failed to dampen 2-address alternation under hairpin-NAT (peer reachable via both LAN and NAT-PMP-reflected public addresses); the hostmap entry flipped every ~2s, dropping in-flight UDP and producing visibly choppy LAN screen-share. 10s blocks the typical alternation while still recovering quickly from real WiFi→cellular roams. | Bug fix (Darwin perf) |
| 15 | `roam-prefer-ranges-darwin.patch` | Adds preferred_ranges awareness to `handleHostRoaming` in `outside.go`. If the current remote is in preferred_ranges (LAN) and an inbound packet comes from a non-preferred address (NAT-reflected public), the roam is ignored entirely. Symmetric: a preferred candidate when on a non-preferred current immediately wins. Stops the hairpin-NAT flap at its source rather than relying on the time window from patch 14. **Had no effect from v0.9.1 to v0.11.27**: our config put `preferred_ranges` under `lighthouse`, where Nebula does not look, so the list was empty. Needs 35 to be safe. | Bug fix (Darwin perf) |
| 16 | `roam-stability-tests.patch` | Tests for patches 14 + 15: regression guard that `RoamingSuppressSeconds ≥ 5`, plus table-driven coverage of the `isAddrInRanges` helper used by the data-plane preferred_ranges check. | Test |
| 17 | `pipeline-listenout-darwin.patch` | Symmetric counterpart to patch 12 on the inbound (UDP→TUN) path. Splits `udp.StdConn.ListenOut` into a reader goroutine (`recvmsg_x` on the UDP fd) and a worker goroutine (callback × N + `Flush()`), linked by a 2-slot channel ring. Overlaps the reader's recvmsg_x block with the worker's decrypt+TUN-write+sendmsg_x work. Closes the v0.10.12 Tailscale-vs-hopssh uplink gap (346→target ≥440 Mb/s). | Perf (Darwin) |
| 18 | `pipeline-listenout-test.patch` | Tests for patch 17 — channel-discipline invariants mirroring patch 13: init-no-deadlock (mutation-validated, regression-protects the v0.10.10-dev1 channel-sizing bug recurring on this side), FIFO order, drain-on-close, back-pressure blocks reader, slot-count sanity. | Test |
| 19 | `qos-class-darwin.patch` | Sets macOS QoS class `USER_INTERACTIVE` on the four `LockOSThread`'d packet-processing OS threads (listenIn reader + worker, listenOut reader + worker). Empirically drops RTT mean from 30 → 25 ms (-17%) and stddev from 42 → 35 ms (-17%) on Mac mini Ethernet ↔ MBP WiFi (200-ping A/B/A test). Default-on for darwin; `HOPSSH_QOS=off` escape hatch. CGO; `qos_hint_darwin.go` (build-tag `darwin && cgo && !e2e_testing`) + `_other.go` stub (`!darwin || !cgo || e2e_testing`). The `cgo` build tag matters: a CGO_ENABLED=0 darwin cross-compile (e.g. CI without macos-latest) silently falls through to the stub instead of failing with "undefined: applyOSThreadHints". To put the gain into the released binary, `.github/workflows/release.yml` runs darwin builds on `macos-latest` with `CGO_ENABLED=1`. Mechanism: per the pprof profile, ~67% of CPU during sustained low-pps ICMP is in `runtime.findRunnable` / scheduler coordination — biasing the scheduler with `pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE)` reduces wakeup latency. See `docs/macos-latency-research.md`. | Perf (Darwin) |
| 20 | `static-hostmap-inject.patch` | Adds `Control.AddStaticHostMap(vpnAddr, addrs)` — the inbound counterpart to patch 11's `AddAdvertiseAddr`. Lets hop-agent inject peer UDP endpoints into the lighthouse cache at runtime, bypassing the normal UDP `HostQuery` flow. Used when the UDP lighthouse is unreachable upstream (e.g., carrier filters UDP to Oracle Cloud from iPhone Personal Hotspot) — the server pushes peer advertise_addrs via the HTTPS heartbeat response, agent calls `AddStaticHostMap`, direct UDP handshake to the peer's home-router public endpoint succeeds. Marks the entry as static so Nebula doesn't garbage-collect it; de-dupes via atomic CAS on `staticList`. | Feature (NAT) |
| 21 | `replace-static-hostmap.patch` | Adds `Control.ReplaceStaticHostMap(vpnAddr, addrs)` — replace-not-add cousin of patch 20. Fully replaces the prior `Reported` set instead of additively merging via `LearnRemote` (which only sets the singular `learned` slot). Eliminates stale NAT-PMP-mapped endpoints accumulating in the lighthouse cache after the upstream router rotates port allocations. Empirically dropped mini's invalid-cert rate 144→7/min (95% reduction). | Bug fix (CGNAT staleness) |
| 22 | `endpoint-probe.patch` | Adds `Control.SetInboundObserver(cb)` (called on every authenticated inbound packet) + `Control.ProbeEndpoint(vpnAddr, remoteAddr)` (sends `header.Test`/`TestRequest` via `Interface.sendTo` to a SPECIFIC remote, bypassing CurrentRemote selection). Infrastructure for per-endpoint active liveness probing in `cmd/agent/endpoint_probe.go`. Reap behavior is intentionally DISABLED in agent code (see Patch 22 details below). | Feature (Layer 4 infra) |
| 24 | `flush-error-propagation.patch` | Makes a failed macOS flush return its error instead of dropping it, so the handshake manager logs it (v0.10.29). | Bug fix (Darwin) |
| 25 | `cert-skew-tolerance.patch` | `cert.ClockSkewTolerance` (1h): accepts certs slightly outside their validity window. No upstream equivalent. | Feature |
| 26 | `relay-manager-warn-on-drop.patch` | Logs relay packets the lighthouse drops (v0.11.25). | Diagnostics |
| 27 | `stop-marks-closed-first.patch` | `Control.Stop()` marks the interface closed before cancelling its context. Otherwise closing an in-app network can reach `os.Exit(2)` and end the agent. Completes 01; upstream fixed it differently in v1.11.0. Tests: `internal/client/nebula_close_test.go`. | Bug fix |
| 28 | `darwin-send-outside-batch.patch` | macOS: writes outside `BeginBatch`/`EndBatch` are sent at once, and a batch is sent one destination at a time so a refused address loses only its own packets. The `interface.go` hunk is made against a tree **without** 12. See `docs/wiki/concepts/macos-udp-send.md`. | Bug fix (Darwin) |
| 29 | `service-close-netstack.patch` | Closing a userspace service closes its gvisor stack. Otherwise each close leaks a goroutine per CPU and ~1.26 MB, and reads on its connections block forever. Upstream v1.10.3 has the bug; newer versions were not checked. | Bug fix (leak) |
| 30 | `pki-no-redundant-global-write.patch` | Writes the package global `noiseEndianness` only when it changes: two networks starting together raced on it. A `chachapoly` network next to an `aes` one would still break both. | Bug fix (race) |
| 31 | `linux-close-releases-port-and-device.patch` | Linux: closing a network frees its UDP port and its TUN device at once. Without it both live on until the next packet arrives, and starting the network again fails with "address already in use" / "device or resource busy". Upstream fixed both (#1661 in v1.11.0, #1979 in v1.12.0). Tests: `nebula_close_test.go`, `nebula_close_linux_test.go` (root), `TestE2E_Lifecycle`, `TestE2E_DeleteNetwork`. | Bug fix (Linux) |
| 32 | `tun-close-on-exec.patch` | Linux and macOS: the TUN descriptor is close-on-exec. Without it a program left running from the web terminal (tmux, nohup) keeps the device after its network is closed, and can read and write the mesh's plain traffic. Checked by a test on Linux; on macOS only compiled. | Bug fix |
| 33 | `relayed-recv-error.patch` | A `recv_error` no longer closes a tunnel that runs over a relay. It is one unauthenticated packet, and nobody answers a relayed packet with one; what comes is the answer of another agent that a probe for a direct path has reached (same LAN address, or a port that changed hands). Without it a relayed session is torn down and rebuilt again and again. Upstream v1.10.3 has the bug. Test: `TestTwoPhones` (`internal/netlab`). | Bug fix |
| 34 | `received-counters.patch` | `ControlHostInfo.ReceivedDirect` / `ReceivedRelayed`: authenticated packets that came from the address we send to, and over relays. Used by 35 and by `internal/client/path_watch.go` to tell a path that works both ways from one that does not. Also: `ProbeEndpoint` with an empty address asks on the path in use, direct or relayed (patch 22 wanted an address). Touches `control.go`, which is tracked in git. | Feature |
| 35 | `ask-before-moving.patch` | A question about another path (from `TryPromoteBest` or `ProbeEndpoint`) is answered where it was asked and moves nobody; the answer moves whoever asked. Nebula moved first and answered then, so every question on a path that works in one direction only (a firewall, a strict router) cut the connection. For the same reason whoever was asked for a tunnel starts where it hears the first packet (before that packet is handed to the machine, which answers at once), instead of where the first copy of the handshake came from, and asks on a better path that a later copy came on instead of moving there. The question is an ordinary TestRequest with a payload, so agents without the patch answer it as before. Needed by 15 and `internal/client/path_watch.go`. Tests: `internal/client/path_question_test.go`, `TestOneWayLAN` and `TestOneWayDirect` (`internal/netlab`). | Bug fix |
| 36 | `new-tunnel-stays.patch` | The side with the lower VPN address does not make an older tunnel the one in use again while the peer is heard on the newer one. Nebula decided by what had come in on the old tunnel since its last check (up to 10 s), also from before the new tunnel, and so went back to the path that a new handshake had just repaired. Test: `TestE2E_NewTunnelStays`, `TestOneDirectionBreaks` (`internal/netlab`). | Bug fix |

23 was never used.

## Upstreamable patches

02, 03, 08, 29, 32 and 33 are clean, self-contained fixes/extensions that upstream
`slackhq/nebula` would benefit from. 01, 27 and (the TUN half of) 31 go away with v1.11.0, the rest of 31 with v1.12.0.
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

## Patch 21 — `Control.ReplaceStaticHostMap` (replace-not-add semantics)

**Shipped v0.10.27.** Adds `LightHouse.ReplaceStaticHostMap(vpnAddr, addrs)` and `Control.ReplaceStaticHostMap(...)` that fully replace the prior `Reported` set instead of additively merging. Patch 20's `AddStaticHostMap` routes through `LearnRemote`, which only sets the singular `learned` slot per peer — it cannot evict an older `Reported` entry that's no longer in the current set. Over time, stale NAT-PMP-mapped addresses accumulate in the lighthouse's `RemoteList.reported` slice, and Nebula's handshake manager probes all of them. When the upstream router reuses external port numbers across hosts (standard CGNAT behavior), packets sent to a stale address arrive at the WRONG host, generating "Invalid certificate from host" log spam and dead-tunnel cycles. Replace semantics fix this: each heartbeat's `peerEndpoints` is the authoritative current set; older entries vanish.

Caller: `cmd/agent/renew.go::injectPeerEndpoints` and `cmd/agent/peer_cache.go::injectCachedPeerEndpoints` switched from `AddStaticHostMap` to `ReplaceStaticHostMap`. Empirical impact: mini's invalid-cert rate dropped from 144/min to ~7/min (95% reduction). See `CLAUDE.md` "Stale NAT-PMP endpoints accumulate" entry for the full multi-layer fix story.

## Patch 22 — `Control.SetInboundObserver` + `Control.ProbeEndpoint`

**Shipped v0.10.27 (active probing infrastructure; reap behavior DISABLED in agent).** Adds two API surfaces:

1. **`Control.SetInboundObserver(cb)`** — registers a callback invoked on every authenticated inbound non-relayed packet, with the peer's `vpnAddr` and source UDP `remote`. Hook installed in `outside.go` after the existing `handleHostRoaming` + `connectionManager.In(hostinfo)` calls. Backed by an `atomic.Pointer[func(...)]` field on `Interface` (`inboundObserver`); when nil, hot-path cost is one atomic load + nil check (~2 ns).
2. **`Control.ProbeEndpoint(vpnAddr, remoteAddr)`** — sends a Nebula `header.Test`/`TestRequest` to the SPECIFIC remote address (not the HostInfo's CurrentRemote) via `Interface.sendTo`. Used to actively probe each candidate endpoint of each peer.

Caller: `cmd/agent/endpoint_probe.go` (new) — per-instance goroutine wires the observer, fires probes every 5s, tracks per-`(peer, endpoint)` `lastSeen` timestamps. **Reap behavior is intentionally a no-op (`WOULD-REAP (disabled)` log instead of `ReplaceStaticHostMap` call).** Reason: the inbound-observation liveness signal is fundamentally broken under asymmetric CGNAT — the peer's outbound REPLY has a DIFFERENT external port than the NAT-PMP-mapped INBOUND port we probed, so we never observe inbound from the probed source IP:port even when the endpoint is alive. Production outage 2026-04-26 confirmed this: ran overnight, progressively starved hostmap, screen-share collapsed at 09:16:46. Hotfix disabled the reap. The probes + observer remain as no-ops because (a) probes might warm cellular CGNAT flow state, (b) the observer counters give us telemetry, (c) the infrastructure is in place for a future Layer 4b nonce-correlated probing implementation that would fix the asymmetric-CGNAT issue properly.

Regression test: `cmd/agent/endpoint_probe_test.go::TestEndpointProbe_AsymmetricCGNAT_NoFalseReap` — simulates the failure mode and serves as a tripwire if the reap is ever re-enabled without per-probe nonce correlation.

## Dropped patches

| # | File | Why dropped |
|---|---|---|
| — | `sndbuf-env-knob.patch` | `HOPSSH_UDP_SNDBUF` env var for overriding macOS `SO_SNDBUF`. Tested 4KB → 512KB: all sizes produce identical p50/p95/p99 latency. The knob was never useful, defaulted off, and documented as "don't use this." Dropped to reduce maintenance surface. (The `11` slot is now used by `portmap-advertise-addr.patch`.) |
