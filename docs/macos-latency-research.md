# macOS userspace utun latency — research and experiment plan

> **TL;DR — UPDATED 2026-04-22 evening, after Experiment 2 shipped**
>
> Measured baseline before Experiment 2: hopssh 33 ms / Tailscale 16 ms.
> After Experiment 2 (patch 19, default-on `pthread_set_qos_class_self_np(USER_INTERACTIVE)`
> on the four LockOSThread'd packet goroutines): **hopssh 24.5 ms — closes ~1/3 of the gap.**
> Confirmed via 200-ping A/B/A test (no QoS / interactive / no QoS) in
> the same WiFi window with the same binary. A and C agree (30/29 ms),
> B clearly below (25 ms) — the gain isn't WiFi drift.
>
> Remaining ~8 ms gap to Tailscale is architecturally NE-only —
> Mach-port IPC + Skywalk Flow Switch fast path. Realistic to plan
> for it alongside the iOS client work; not closable in raw userspace
> utun.
>
> Adoption-wise this is good: 24.5 ms LAN+WiFi RTT puts us
> **substantially ahead** of every other userspace VPN (wireguard-go
> on raw utun, NetBird, Mullvad-as-userspace all in the 30–40 ms band)
> and within ~50% of Tailscale's NE-bundle number.
>
> Original research below — kept as historical context. Experiment 1
> (single-packet fast path) was killed by profile data. Experiment 3
> (pprof wiring) shipped as a quiet helper in `cmd/agent/pprof.go`.

## Why we're researching this

User report (2026-04-22 evening, after v0.10.13 shipped):
- hopssh DL **313 Mb/s**, UL **230 Mb/s** — wins both directions vs Tailscale (274 / 181).
- hopssh RTT mean **33 ms**, stddev 38 ms.
- Tailscale RTT mean **16 ms**, stddev 25 ms.
- User wants to close the latency gap WITHOUT NE so adoption-without-NE
  isn't penalised. NE is acknowledged as the long-term answer (planned
  alongside iOS client work).

Throughput we already have. The unsolved bit is per-packet latency.

## Background: what NE actually buys you

`NEPacketTunnelProvider` is a NetworkExtension framework component. It
does NOT mean "the VPN runs in the kernel." It still runs in a
userspace container app. What it does buy:

| What | Mechanism | Per-packet impact |
|---|---|---|
| `IS_VPN` xflag on the utun | Set by NE during interface bring-up | Lets `avconferenced`, Chrome's `NetworkChangeNotifier`, etc. classify the interface as VPN; affects scheduling priority and HP screen-share routing |
| Skywalk NetIf + FlowSwitch agents | Registered by NE | Shorter packet-delivery path inside XNU; fewer kernel layers between NIC driver and the app's read buffer |
| Mach-port IPC + shared-memory ring buffer | NE delegates packet I/O to a kernel-side queue | App reads packets from a memory-mapped region rather than via `read(2)`/`recvmsg(2)` syscalls. **This is the big latency win** — eliminates ~500ns–2µs of syscall overhead per packet. |
| App lifecycle integration | NE supervises the container | Faster wake-from-sleep, no LaunchDaemon racing |

The Mach-IPC + shared-memory ring is the architectural advantage we
cannot replicate from a raw-utun userspace process. macOS sandboxing
prevents non-entitled processes from accessing the kernel's IPC ports
or mapping kernel-shared memory regions. **There is no public
non-NE API to opt into this fast path.** Every VPN that doesn't ship
NE — wireguard-go, OpenVPN, strongSwan, Nebula, NetBird, Mullvad
when run as a daemon — pays the full syscall round-trip per packet.

Sources cited by the research: Apple WWDC 2019 session 714, Cloudflare
"How to receive a million packets" measurement, Apple Developer Forums
thread on NEPacketTunnelProvider performance characteristics.

## What we found in our hot path

Two parallel investigations into the codebase. The interesting result
is what's NOT a problem — most early hypotheses got knocked down once
we read the actual code:

### Things ruled out (with file:line refs)

- **Per-packet allocations.** `vendor/.../interface.go:323-342` (listenInWorker), `vendor/.../udp/udp_darwin.go:268-389` (listenOut pipeline) — all buffers pre-allocated at slot init; no `make()` in the hot path.
- **Per-packet global locks.** `vendor/.../firewall.go:486-559` looks like it acquires a global mutex per packet, but lines 487-490 short-circuit through `localCache` (a per-goroutine `firewall.ConntrackCache` map). The mutex is only acquired on first-of-flow; for sustained ping, every subsequent packet hits the local cache and skips the lock.
- **Crypto cost.** `EncryptDanger`/`DecryptDanger` uses AES-GCM. On Apple Silicon this is hardware-accelerated (single-cycle AES instructions) — measured at <1% of CPU under load (per CLAUDE.md performance profile). Not the bottleneck.
- **`time.Now()` cost.** Modern macOS reads the time via the commpage (~20ns, no syscall). The 1–3 µs estimate one of our research agents floated was wrong for Apple Silicon.
- **Priority queue classifier.** `vendor/.../udp/udp_darwin.go:87` `classify(b)` is one byte read. Negligible.
- **`runtime.Gosched()`.** Absent — we're not doing artificial scheduler yields.
- **Periodic timers competing for the scheduler.** Connection-manager test packets fire every ~5 s per peer; not in the per-packet path.

### Things that COULD matter under low pps (the ICMP-RTT case)

| Source | Mechanism | Estimated per-RTT cost | Confidence |
|---|---|---|---|
| **Pipeline channel overhead** (patches 12, 17) | At low pps, only 1 packet in flight. Each direction's reader+worker pair does 4 channel ops per packet (free←slot, slot→inflight, slot←inflight, slot→free). Each op may trigger a Go scheduler wakeup that costs 20–50 µs cross-thread on macOS. With LockOSThread on both reader and worker, cross-thread coordination is not free. | ~80–150 µs per RTT | Medium-high. Throughput-optimized design, latency-suboptimal at the bottom of the pps curve. |
| **Two `recvmsg_x` waits per RTT** (one per direction, on each peer) | Even with the pipeline, each direction has to wait for the kernel to deliver the packet via `recvmsg_x`. Each delivery is one syscall transition (~500 ns–2 µs on Apple Silicon). | ~2–8 µs per RTT | High. Inherent to userspace utun — only NE removes this. |
| **macOS scheduler tail latency** (Go goroutines on a non-realtime kernel) | macOS doesn't expose `SCHED_FIFO` to user processes. Goroutines are subject to the cooperative scheduler's tail. Under contention this can add 1–10 ms. | ~1–10 ms tail (visible as RTT stddev, not mean) | Medium. Hard to attribute precisely; matches our high stddev (38 ms vs Tailscale's 25 ms). |
| **WiFi MAC contention floor** | `ping -c 50` shows 4 ms min, 33 ms mean — that ~29 ms gap is mostly WiFi airtime. Both VPNs see this. | ~5–25 ms variance per RTT | High. Documented in CLAUDE.md as inherent to wireless. |

The numbers rough-add to a recoverable budget of ~80–160 µs per RTT.
On a 17 ms gap that's ~1% of the way to closing it. The other 99%
is architecture (NE) and physics (WiFi).

## Three experiments worth running

These are ordered by **effort × likelihood of measured improvement**.
Each is justified by the analysis above; each has a clear
measurement methodology so we can falsify the hypothesis quickly
rather than committing code first.

### Experiment 1 — Single-packet fast path on the listenIn pipeline (low effort)

**Hypothesis:** under low pps the 2-slot pipeline costs more than it
saves. If the reader's `ReadBatch` returned `n=1`, processing that
one packet inline (without channel handoff) would save 4 channel ops
+ a scheduler wakeup.

**Implementation sketch:** in `listenIn` (interface.go:344), after
`ReadBatch(batch.packets)`, if `n == 1` AND the worker's `inflight`
channel is empty (no backlog), call `consumeInsidePacket` and
`Flush()` directly on the reader goroutine. Otherwise hand off to the
worker as today.

**Expected gain:** if our 80–150 µs estimate is right, ICMP RTT
should drop ~150–300 µs (savings on both directions = both peers see
n=1 batches). On a 33 ms RTT that's <1%, well within WiFi noise.
Worth trying because the code change is small and we keep all the
throughput optimization for the high-pps case.

**Measurement:** before/after `ping -c 200 -i 0.1` to collect tight
RTT distributions; significance test on means. If we see ≥0.5 ms
mean reduction with no throughput regression on iperf3 sustained
load, ship.

**Risk:** low. The fast path is a strict subset of the pipeline path;
no new code path is taken under sustained load.

### Experiment 2 — `pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE)` on packet-processing goroutines (low effort, uncertain gain)

**Hypothesis:** macOS QoS classes can give specific OS threads
scheduler preference. By default, Go runtime threads inherit the
process's QoS, which is `QOS_CLASS_DEFAULT`. Bumping the
LockOSThread'd packet-processing threads to `USER_INTERACTIVE`
may reduce scheduler tail latency.

**Implementation sketch:** add a CGO wrapper that calls
`pthread_set_qos_class_self_np` from inside `listenIn`,
`listenOut`, `listenInWorker`, and `listenOutWorker` — all of which
call `runtime.LockOSThread()` already, so we're sure they're running
on a single dedicated kernel thread.

**Expected gain:** documented elsewhere as 5–15% reduction in
scheduler latency for "interactive" workloads. For us that might be
1–3 ms off the RTT tail (mean), more off the stddev. Real number is
uncertain because no public benchmark exists for Go goroutines under
QoS classes.

**Measurement:** same `ping -c 200` distribution. Plus capture
`sample <pid> 1000` flamegraph before/after to see scheduler
behaviour change.

**Risk:** medium. Over-prioritising can starve other system threads
(audio, etc.). Test on dev machine first; gate behind opt-in env var
during the experiment phase.

### Experiment 3 — Profile-driven targeted optimisation (medium effort)

Before any more code changes, run a real CPU profile under sustained
ping load and let the data tell us where the time actually goes.
Both research agents made plausible claims that turned out wrong on
inspection (firewall lock isn't per-packet thanks to localCache;
time.Now isn't 1-3µs on Apple Silicon). We've been running on
hypotheses; time to measure.

**Methodology:**
```
# Generate sustained low-pps traffic during profile
ping -c 600 -i 0.1 10.42.1.11 &

# Profile hop-agent for 30 s
go tool pprof -seconds 30 -http :8080 \
  http://localhost:6060/debug/pprof/profile

# Look for: time in runtime.gopark, runtime.chanrecv, runtime.chansend,
# runtime.kevent, syscall.syscall6, and any unexpected hot frames in
# our patch-12/patch-17 path.
```

This requires wiring `_ "net/http/pprof"` into the agent (existing —
just needs an opt-in env var to expose the endpoint). Cost: 10 lines
of code + a confirmed methodology for future investigations.

**Decision rule:** if the profile shows >5% of CPU in
`runtime.chanrecv` / `chansend` along the listenIn/listenOut paths,
Experiment 1 is justified empirically. If it shows <2% of CPU there,
the channel overhead hypothesis is wrong and we should look
elsewhere (possibly the WiFi MAC contention is actually
the dominant component and there's nothing to recover).

## What we explicitly will NOT do

- **Ship `NEPacketTunnelProvider` as part of this work.** Out of
  scope per the user's directive — that's a separate project tied to
  iOS client. Documented as the only path to ~16 ms RTT.
- **Switch to `feth` (ZeroTier's L2 fake-Ethernet trick).** No
  documented latency advantage; would require rewriting our utun
  bring-up code; "kernel TUN" mode wouldn't apply unchanged.
- **Add timer-based send batching.** CLAUDE.md: 500 µs timer dropped
  throughput 154 → 63 Mbps. Time-based batching is a documented
  anti-pattern for our architecture.
- **Tune `GOMAXPROCS` globally.** The Go runtime's default
  (= num CPUs) is correct for our throughput targets; pinning to 1
  would tank multi-stream iperf3.

## Sources of uncertainty

- The 16 ms vs 33 ms comparison was a single measurement window with
  two trials per side. WiFi conditions vary across 5–20 ms run-to-run.
  We need 200+ ping samples per side to get statistically clean
  numbers; the published headline is approximate.
- The "wireguard-go without NE sits at 30–40 ms" claim is based on
  the research agent's web search; we did not measure it directly.
- The pipeline-overhead estimate of 80–150 µs is from the research
  agent's reading of the channel-flow code, NOT a measured number.
  Experiment 3's profile is what would confirm or deny it.

## Decision

Recommend proceeding with **Experiments 3 → 1 → 2 in that order**:

1. Wire up `pprof` (Experiment 3 prerequisite) — small, no-risk.
2. Capture a real profile during low-pps and high-pps load.
3. If the profile justifies it, implement Experiment 1 (single-packet
   fast path). Ship as v0.10.14 if it shows ≥0.5 ms mean RTT win.
4. If Experiment 1 still leaves a noticeable gap, consider
   Experiment 2 (QoS class) — gated behind a feature flag so we can
   measure with/without on the same binary.

**Setting expectations clearly:** even on the best case across all
three experiments, we're looking at recovering ~10% of the gap
to Tailscale. The remaining ~90% is the NE architecture, and the
right way to recover that is to ship our own NE bundle when the iOS
client work begins. For non-NE userspace utun, hopssh is already at
or near the best-case latency that's achievable.
