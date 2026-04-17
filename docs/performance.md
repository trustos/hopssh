# Performance Engineering

## Current State (v0.9.3)

### Benchmarks

**LAN (WiFi, Apple Silicon):**

| Metric | Raw LAN | ZeroTier | Nebula | Gap |
|--------|---------|----------|--------|-----|
| Avg latency | 11.5ms | 14.2ms | 16.6ms | +2.4ms vs ZT |
| Stddev | 19.2ms | 21.2ms | 24.1ms | WiFi-dominated |
| Max | 92ms | 92ms | 96ms | WiFi spikes |

Periodic 60-90ms spikes are WiFi power management — identical across all three paths.
Nebula's actual overhead above raw LAN is ~3ms per packet.

**WAN (2026-04-15):**

| Scenario | Nebula | ZeroTier | Winner |
|----------|--------|----------|--------|
| LAN P2P (WiFi) | 14ms avg, 0% loss | 17ms avg | **Nebula** |
| WAN P2P (mobile hotspot) | 106ms avg, 0% loss | 222ms avg | **Nebula** |
| WAN relay (symmetric NAT) | 125ms avg | ~200ms | **Nebula** |
| P2P on carrier NAT | Fails (symmetric NAT) | Usually succeeds | ZeroTier |
| Network roam (WiFi↔cellular) | Auto, <5 seconds | Auto | Tie |

Nebula is faster when P2P works but fails to establish P2P on carrier-grade NAT (symmetric NAT). Relay overhead is only 9ms (125ms relay vs 106ms P2P) — the bottleneck is network path, not processing.

**Throughput (2026-04-15, iperf3, Mac mini ↔ MacBook, WiFi LAN, Apple Silicon):**

| Test | Throughput | Latency (avg) | Latency (max) | Packet loss |
|------|-----------|---------------|----------------|-------------|
| Raw LAN (no tunnel) | 735 Mbits/sec | 14.5ms | 93ms | 0% |
| Nebula tunnel (single stream) | 217 Mbits/sec | 20.1ms | 202ms | 0% |
| Nebula tunnel (4 streams) | 148 Mbits/sec | — | — | 0% |

Tunnel overhead: 70% throughput reduction, +5.6ms latency. The throughput gap is entirely syscall overhead (71.6% CPU in sendto/recvfrom — see profile below). The 202ms max latency is a WiFi power management spike (raw LAN also has 93ms spikes). Multi-stream is lower than single-stream due to TUN fd contention and Nebula's single-writer architecture on macOS.

**Competitive position:** 217 Mbps is 2-4x what ZeroTier users typically report (50-100 Mbps). Sufficient for all selfhoster use cases (SSH, web UIs, Jellyfin, NAS, Screen Sharing). The remaining gap to raw LAN is a macOS kernel limitation (no sendmmsg/recvmmsg) — not fixable in userspace.

### CPU Profile Under Screen Sharing Load (30s, pprof)

| CPU% | Function | Category |
|------|----------|----------|
| 62.9% | `syscall.syscall6` (sendto) | UDP send kernel transition |
| 9.9% | `runtime.kevent` | I/O polling |
| 8.7% | `runtime.pthread_cond_wait` | Goroutine sleeping |
| 8.7% | `syscall.syscall` (TUN r/w) | TUN kernel transition |
| 7.2% | `runtime.pthread_cond_signal` | Goroutine waking |
| 0.8% | `AES-GCM Seal` | Encryption |

**The bottleneck is syscall overhead (71.6% of CPU), not crypto (0.8%).**

Each packet requires two kernel transitions: TUN read (userspace←kernel) and
UDP sendto (userspace→kernel). These context switches dominate CPU time.

### Sleep/Wake Recovery (v0.9.7, measured 2026-04-17)

Tested: Mac mini (peer, 10.42.1.7, Ethernet) ↔ MacBook Pro (subject,
10.42.1.6, WiFi), same LAN, kernel-TUN mode. Six-test battery T1–T6.
Full evidence: [`spike/sleep-wake-evidence/RESULTS.md`](../spike/sleep-wake-evidence/RESULTS.md).

| Scenario | Measured | Notes |
|---|---|---|
| 9s soft sleep | No tunnel break | Sub-15s tick-gap threshold correctly ignored |
| 2-min soft sleep | **3s peer-view recovery** | Rebind fires via `addrChanged` branch in `watchNetworkChanges`; two WiFi-stabilization flaps in the first 12s |
| ~13-min soft sleep | <1s after each actual wake event | `pmset schedule wake` unreliable for 10-min sleeps on Apple Silicon; WOL is the reliable fallback |
| Hibernate (`hibernatemode 25`, 2-min) | **191s** (+65s vs soft-sleep for disk restore) | `utun0` and mesh IP `10.42.1.6` survive the cycle |
| DNS on wake | mesh query answers at **T+5s**; `/etc/resolver/<domain>` byte-identical pre/post | Public DNS queries never blocked — split-DNS isolation holds |
| Peer-side black-hole window | **<3s** across all tests | Peer recovers as soon as subject re-handshakes |

**Cellular hotspot variant** (same MacBook switched to iPhone hotspot,
172.20.10.0/28, public IP via carrier CGNAT, ~80ms mesh RTT via relay
`132.145.232.64:42001`):

| Scenario | Cellular | vs LAN |
|---|---|---|
| 2-min sleep peer-view recovery | **3s** | **identical** |
| DNS first success post-wake | **T+6s** | +1s (cellular DNS hop) |
| Rebind path | addrChanged branch fires, relay wins | same code, relay vs P2P |
| Resolver file diff | empty | same |
| Mesh DNS latency p50 | ~125ms | +20ms (cellular RTT) |

So **recovery performance is indistinguishable from LAN** on cellular despite
~15× higher baseline RTT and CGNAT-forced relay-routing. Nebula's relay
fallback is seamless when direct P2P fails.

**Linux VM (Ubuntu 25.10 aarch64 in UTM, 2026-04-17)** — sleep code path validated functionally, but hit a separate DNS bug:

| Signal | Linux VM (SIGSTOP-based test) |
|---|---|
| Tick-gap detection fires on time-jump | **✅ confirmed** — log line `"sleep/wake detected (tick gap 2m14s) detected (iface: enp0s1→enp0s1), rebinding Nebula"` appeared (first time observed across any run — macOS's `addrChanged` branch had masked it) |
| Rebind + tunnels closed + tunnel recovery | ✅ <1s post-resume, same PID, no restart |
| Mesh IP connectivity (ping, SSH, arbitrary TCP) | ✅ works end-to-end |
| **Mesh DNS via systemd-resolved stub** | ✅ **FIXED in v0.9.8-dev** — agent now probes the stub after per-link registration and falls back to a drop-in config (`/etc/systemd/resolved.conf.d/hopssh.conf`) on affected systemd-resolved versions. Post-fix: 39ms per query (was 3s timeout). See `spike/sleep-wake-evidence/linux-dns-fix-validation.txt`. |

**Scope caveat for the Linux run:** real OS-level suspend-resume is NOT testable on QEMU ARM in UTM — `systemctl suspend` cold-reboots the VM on "wake," `rtcwake -m mem` has no RTC alarm support, `freeze` hangs. Testing used SIGSTOP/SIGCONT on the agent process, which exercises only the agent's time-jump detection (not interface down/up or kernel sleep). Full Linux validation needs bare-metal hardware.

Scope this covers: macOS 15.x on Apple Silicon (same-LAN WiFi + iPhone hotspot cellular CGNAT, two-node topology, kernel-TUN mode) + Linux VM sleep-simulation via SIGSTOP. Behavior on **bare-metal Linux suspend, Windows, SSID-roam mid-sleep (WiFi↔cellular switch on wake), multi-hop, mobile battery** is **not yet measured** — those are the dimensions where competitor VPNs have long-open issues (Tailscale #17736, #10688, #1554; ZeroTier #2026, #2545; NetBird #2454), and we have no data either way.

Observed design quirks worth knowing:
- The `"sleep/wake detected (tick gap Ns)"` log string from
  `cmd/agent/nebula.go:187` rarely appears in practice — the `addrChanged`
  branch wins the condition because WiFi re-associates during sleep.
  Functionally equivalent (rebind + tunnel-close still fire); diagnostically
  confusing post-hoc.
- Every soft-sleep recovery produces two rebind events ~6s apart during
  WiFi stabilization. Total convergence under 12s.

## Applied Optimizations

### GOGC=400 (v0.6.50)
Reduces GC frequency 4x. Eliminated 100ms GC pause spikes.
**Location:** `cmd/agent/main.go`

### TUN Read Buffer Reuse (v0.6.51)
Vendor patch eliminates per-packet heap allocation in `tun_darwin.go:Read()`.
**Patch:** `patches/03-tun-darwin-read-buffer.patch`

### Faster Handshake + Blocking Tunnel Warmup (v0.6.54-v0.6.58)
`handshakes.try_interval: 20ms` (down from 100ms). Agent pre-warms all mesh
subnet peer tunnels on startup, blocking until handshakes complete. Fixes
Screen Sharing "High Performance not supported" on first connection.

### MTU 1440 (v0.6.58)
True maximum without IP fragmentation: 1500 - 60 bytes overhead = 1440.
Previous 1420 was 20 bytes too conservative. MTU 2800 was tested and rejected —
IP fragmentation doubled sendto syscalls instead of halving them.

### pprof Endpoint (v0.6.50)
```bash
curl -H "Authorization: Bearer <token>" \
  "http://<mesh-ip>:41820/debug/pprof/profile?seconds=30" > cpu.prof
go tool pprof cpu.prof
```

## What We Tried and Reverted

| Change | Result | Why |
|--------|--------|-----|
| ChaCha20-Poly1305 cipher | 7ms avg vs 4ms with AES | Apple Silicon has hardware AES; ChaCha uses NEON (slower) |
| GOMEMLIMIT 128MB | Excessive GC under burst traffic | Too tight for sustained packet load |
| 2MB UDP socket buffers | 50-293ms bufferbloat spikes | macOS reads one packet at a time; large buffers queue stale packets |
| MTU 2800 | 39ms avg (2.4x worse) | IP fragmentation doubled sendto syscalls per packet |
| FEC (async parity) | +57% latency on cellular (300ms vs 191ms) | Extra parity causes congestion on bandwidth-constrained paths |
| FEC (original buffered) | 10-second Screen Sharing freezes | Buffered K packets before sending |
| Port prediction (±50 ports) | EAGAIN socket errors | Carrier uses random port assignment; 100 punch packets flood socket |
| sendmmsg egress batching | 408ms vs 125ms relay | Batch-flush holds packets; needs per-packet flush to be useful |
| Full /24 subnet warmup | EAGAIN socket flood, dead tunnels | 254 simultaneous handshakes overwhelm the handshake manager |
| Multi-reader UDP (SO_REUSEPORT) | 35% packet loss | Created orphaned sockets |
| TUN Read buffer reuse | crypto/cipher buffer overlap panic | Unsafe buffer sharing |

## Platform Constraints

### macOS
- No public `sendmmsg`/`recvmmsg` — BUT see **`sendmsg_x`/`recvmsg_x` discovery** below
- No UDP GSO/GRO (Linux 4.18+)
- No TUN multiqueue (`IFF_MULTI_QUEUE` is Linux-only; utun is single-fd)
- No `SIOCSIFTXQLEN` for TUN transmit queue
- utun hard cap: 4096 bytes per packet (`UTUN_IF_MAX_SLOT_SIZE` in XNU, max MTU = 4064)
- Single-packet-at-a-time I/O for TUN (UDP can be batched — see below)

### macOS `sendmsg_x` / `recvmsg_x` — Undiscovered Batch UDP (Research: 2026-04-15)

macOS has **private batch UDP syscalls** that no VPN project uses. Found in XNU kernel
source (`/bsd/sys/socket.h`, behind `#ifdef PRIVATE`):

```c
ssize_t sendmsg_x(int s, const struct msghdr_x *msgp, u_int cnt, int flags);  // syscall #481
ssize_t recvmsg_x(int s, const struct msghdr_x *msgp, u_int cnt, int flags);  // syscall #480
```

**Key facts:**
- Batch up to **1024 sends** and **100 receives** per syscall
- Available since macOS 10.11 (2015) — stable for 11 years
- Used internally by Apple for Network.framework and MPTCP
- Connected-socket fast path: `connect()` the UDP socket → `sendmsg_x` uses `sosend_list()`
  which builds one mbuf chain for all messages (single kernel traversal)
- `msg_name`/`msg_control` must be zero — must use `connect()` for the send fast path
- Not in Go stdlib, `x/sys/unix`, or `x/net` — requires CGO or raw syscall wrappers

**No VPN project uses these:**
- WireGuard-Go: `BatchSize=1` on macOS (`conn/bind_std.go`)
- Go `x/net/ipv4.PacketConn.ReadBatch/WriteBatch`: falls back to batch size 1 on non-Linux
- Tailscale: all throughput work is Linux-only, no macOS optimizations
- ZeroTier: uses C++ with feth interfaces — different approach

**Projected impact:** Our 71.6% CPU in syscalls could drop to single digits. Theoretical
throughput on macOS: 217 Mbps → 600-800+ Mbps. Would make hopssh the first VPN on macOS
to use batch UDP syscalls.

**Risk:** Private API (`#ifdef PRIVATE`). But these are syscalls (kernel ABI is stable),
not library calls. They've been unchanged since 2015 and Apple uses them for their own
networking stack.

**Spike 1 results (2026-04-16, recv-only):**
- Pure Go implementation (no CGO) — struct layouts match XNU, verified with `unsafe.Sizeof`/`Offsetof`
- `recvmsg_x` batch receive: working, 64 packets per syscall, integrated into ListenOut
- `sendmsg_x` batch send with 500μs timer: **hurts TCP** — adds jitter that congestion control
  interprets as loss. 63 Mbps (batch=64) vs 154 Mbps (batch=1). Timer-based send batching
  is fundamentally incompatible with the serial TUN read → encrypt → send architecture.

**Spike 2 results (2026-04-16, full send+recv):**
- Discovery: `recvmsg_x` works on the utun fd directly (utun is `SOCK_DGRAM`/`AF_SYSTEM`).
  Batches TUN reads via the same syscall used for UDP receives.
- Architecture: opportunistic batching. listenIn does blocking first read via Go netpoller,
  then non-blocking drain via recvmsg_x. Encrypted packets queue in UDP send queue.
  After processing the whole batch, a single sendmsg_x flushes everything.
- **No timer.** Caller-driven flush (listenIn after TUN batch, ListenOut after UDP batch).
  Mutex on the send queue (handshake manager + lighthouse + listenIn + listenOut all produce).
- iperf3 results (WiFi raw 377 Mbps today): 134 Mbps single stream, 202 Mbps 4-stream.
  Tunnel efficiency improved from 17.4% (recv-only) to 35-53% (full batch).

**Status:** Both shipped. Patches 04-08 in `patches/`. Adds `Flush() error` to Nebula's
`Conn` interface (no-op stubs on non-Darwin platforms).

**Spike 3: Inline packet prioritization (final: 2-lane control-only)**

Initial design was a 3-lane queue splitting data packets by size (interactive <200B,
realtime 200-1200B, bulk ≥1200B). Synthetic latency benchmarks looked good (~25% RTT
reduction under load). But user reported screen sharing felt LAGGIER, not better.

Investigation with proper TCP metrics confirmed the user's instinct:

| Metric | No PQ | 3-Lane size-split |
|--------|-------|-------------------|
| TCP retransmits (avg of 3 runs) | 168 | **383 (+128%)** |
| Bulk throughput under mixed load | 320 Mbps | **96 Mbps (-70%)** |
| TCP-RTT p99 under load | 100ms | 155ms |

**Root cause:** splitting data packets by size reorders TCP segments within a single flow.
TCP receivers see out-of-order arrivals, trigger SACK, mark as congestion, and back off.
A naive priority queue inside a VPN is actively harmful for TCP throughput.

**Fix: 2-lane control-only.** Only Nebula control packets (Handshake, LightHouse, Test,
CloseTunnel, Control — type != Message) get priority. ALL data packets (type == Message)
share one lane in FIFO order, preserving within-flow TCP ordering.

| Metric | No PQ | 3-Lane | 2-Lane (shipped) |
|--------|-------|--------|------------------|
| Bulk under load | 170 Mbps | 96 Mbps | **307 Mbps** ✓ |
| Throughput regression | — | -43% | **none** |
| Control packet latency (handshake, lighthouse) | n/a | best | best |

Classification is one read (`b[0]&0x0f`), zero crypto cost. Lane caps: 32 control / 96 data.
Within `sendmsg_x` flush: control lane drains first (kernel processes msghdrX array in order),
then data lane in FIFO.

**Honest assessment of latency win:** Synthetic ping-under-load showed inconsistent
improvement vs baseline once we held WiFi conditions equal. The control-only PQ does NOT
materially reduce p50/p95 RTT for typical TCP flows under load — that latency is dominated
by WiFi MAC contention, not VPN queueing. What we DO get: tunnel handshakes, lighthouse
queries, and keepalives never wait behind bulk transfers, which keeps the mesh responsive.

To meaningfully reduce TCP latency under load further, the candidates are: per-flow fair
queueing (fq_codel-style) inside the VPN, or smaller OS UDP send buffers. **NOT included:
"pacing on bulk to reduce WiFi airtime contention"** — research
([arxiv.org/html/2512.18259v1](https://arxiv.org/html/2512.18259v1)) confirms WiFi airtime
contention happens at the MAC layer, below IP, and is unaffected by anything the VPN does
at the userspace socket boundary. BBR-style pacing parameters are documented as
"suboptimal for WiFi's variable OFDMA scheduling." This matches our own discovery-log
entry: "Screen sharing latency floor is the wireless medium, not the VPN." Userspace
pacing is a dead end; leave it off the roadmap.

**Status:** Shipped in patches 09 (control-only logic) and 10 (tests). The size-based 3-lane
variant lives in commit history as a documented dead-end.

**Spike 4: SO_SNDBUF tuning investigation (negative result)**

User reported screen sharing still felt laggy after the 2-lane PQ shipped. We did a proper
A/B with continuous TCP-RTT probes (50ms interval, 3 minutes) during real screen sharing
on both PQ-enabled and PQ-disabled builds. Result: **identical distributions**.

| Metric | PQ ON | NO PQ |
|--------|-------|-------|
| p50 | 6.2ms | 6.3ms |
| p90 | 50.0ms | 49.0ms |
| p95 | 60.4ms | 62.0ms |
| p99 | 94.0ms | 96.6ms |
| % in 40-160ms band | 12.9% | 12.9% |

The PQ change is invisible at the user level, confirmed by direct measurement.

Next, hypothesized that kernel UDP send buffer was bufferbloating. Discovered the macOS
default `SO_SNDBUF` is **9216 bytes** (~6 packets at MTU) — already very small. Tested
overrides at 4KB / 32KB / 128KB / 512KB across 60s probes during real screen sharing:

| SO_SNDBUF | p50 | p95 | p99 |
|-----------|-----|-----|-----|
| 4 KB | 6.1ms | 65ms | 124ms |
| 32 KB | 6.1ms | 92ms | 121ms |
| 128 KB | 6.1ms | 92ms | 122ms |
| 512 KB | 6.2ms | 92ms | 119ms |

**No effect at any size.** The kernel UDP buffer is not where the latency tail comes from.

By process of elimination, the tail latency in the user's setup (Mac mini wired → router →
laptop on WiFi) comes from:
1. **WiFi MAC contention** between router and laptop — the only wireless leg in the chain
2. **Apple Screen Sharing (RFB) protocol bunching** — full-frame TCP delivery is inherently bursty
3. Possibly **TCP socket buffer on the laptop's receive side** — out of our reach

None of these are fixable inside the VPN layer.

**Status:** Shipped a `HOPSSH_UDP_SNDBUF` env-var knob in patch 11 as an opt-in tuning
mechanism for edge cases (high pps, custom kernel tuning). Defaults to system default.
Do NOT recommend it as a "performance fix" — we proved it doesn't help in the typical case.

### Linux
- Full `sendmmsg`/`recvmmsg` support (batch 64 packets per syscall)
- TUN multiqueue with `IFF_MULTI_QUEUE`
- UDP GSO/GRO (kernel 4.18+/5.0+)
- `SIOCSIFTXQLEN` for transmit queue tuning
- `SO_RCVBUFFORCE`/`SO_SNDBUFFORCE` for privileged buffer sizing

### Windows
- No `sendmmsg`/`recvmmsg`
- No TUN multiqueue (WinTun is single-queue)
- WinTun ring buffer provides efficient single-queue I/O

## How Competitors Solve This

### WireGuard-Go (Tailscale)
- **Linux:** `sendmmsg`/`recvmmsg` with 128-packet batches, UDP GSO/GRO,
  TUN vectorized I/O (kernel 6.2+), per-core encrypt/decrypt goroutine pools.
  Achieves 11.3 Gbps.
- **macOS:** Falls back to single-packet I/O. No sticky sockets. Same
  fundamental constraints as Nebula.
- **Key insight:** wireguard-go's performance on macOS is NOT significantly
  better than Nebula. The 11.3 Gbps numbers are Linux-only.

### ZeroTier
- Uses `feth` (fake Ethernet) interfaces on macOS — classified as
  `nw_interface_type_wired` by Network.framework. Reports 10Gbase-T link speed.
- MTU 2800 with application-layer fragmentation (splits before UDP send).
- C++ implementation — no GC pauses, lower per-packet overhead than Go.
- **Key advantage on macOS:** app-layer fragmentation avoids IP fragmentation
  cost. The C++ runtime avoids goroutine scheduling overhead.

### WireGuard Kernel Module
- All crypto in kernel space. Zero userspace transitions per packet.
- Fastest possible data plane. Used by Tailscale on Linux.
- Not available as a library; requires kernel module installation.

---

# Performance Roadmap

## Phase 1: Packet Coalescing (Highest Impact)

**Problem:** 62.9% of CPU is in `sendto` syscalls. Each encrypted packet
requires its own kernel transition.

**Solution:** Batch multiple encrypted Nebula packets into a single UDP
datagram. The receiver decoalesces them back to individual packets.

### Architecture

```
CURRENT (per-packet):
  TUN read → encrypt → sendto     (one syscall per packet)
  TUN read → encrypt → sendto
  TUN read → encrypt → sendto

PROPOSED (coalesced):
  TUN read → encrypt → buffer
  TUN read → encrypt → buffer
  TUN read → encrypt → buffer
  flush timer (1ms) → sendto       (one syscall for N packets)
```

### Wire Format

Each coalesced UDP datagram contains multiple Nebula packets with
length-prefix framing:

```
┌──────────────────────────────────────────────────────┐
│ Outer UDP datagram                                    │
│ ┌────────┬──────────────┬────────┬──────────────┬──┐ │
│ │ len(2) │ nebula pkt 1 │ len(2) │ nebula pkt 2 │..│ │
│ └────────┴──────────────┴────────┴──────────────┴──┘ │
└──────────────────────────────────────────────────────┘
```

- 2-byte big-endian length prefix per inner packet
- Maximum coalesced datagram size: 1440 bytes (avoids IP fragmentation)
- Flush trigger: buffer full OR 1ms timer (whichever comes first)
- Backward compatible: single-packet datagrams look identical to existing
  Nebula packets (detected by checking if first 4 bits match Nebula header
  version, vs length prefix which will have different bit pattern)

### Implementation

**New package:** `internal/coalesce/`

```go
type Coalescer struct {
    buf       []byte         // accumulation buffer (1440 bytes)
    offset    int            // current write position
    flushFn   func([]byte)   // callback to send the coalesced datagram
    timer     *time.Timer    // 1ms flush deadline
    mu        sync.Mutex
}

func (c *Coalescer) Add(packet []byte)   // add encrypted packet to buffer
func (c *Coalescer) Flush()              // send buffer contents via flushFn
```

```go
type Decoalescer struct{}

func (d *Decoalescer) Split(data []byte) [][]byte  // split coalesced datagram
```

**Integration points (vendor patches):**

1. `inside.go` — after `EncryptDanger()`, pass to `Coalescer.Add()` instead
   of direct `WriteTo()`
2. `outside.go` — before `readOutsidePackets()`, pass through
   `Decoalescer.Split()` to handle both coalesced and single-packet datagrams
3. `interface.go` — create one `Coalescer` per writer, with flush timer

**Platform behavior:**
- All platforms benefit from coalescing (fewer syscalls everywhere)
- Linux additionally benefits from existing `sendmmsg` for the coalesced sends
- macOS and Windows see the largest relative improvement (no kernel batching)

### Expected Impact

With average 4 packets per coalesced datagram:
- sendto syscalls reduced by 75%
- CPU in sendto drops from 62.9% to ~16%
- Overall latency improvement: ~2-3ms reduction in steady-state

## Phase 2: Per-Core Crypto Goroutine Pools

**Problem:** Nebula uses 1 goroutine for the outbound path (TUN→encrypt→send).
Encryption is only 0.8% CPU now, but with coalescing reducing syscall overhead,
crypto becomes a larger fraction. More importantly, the single goroutine
serializes ALL outbound processing.

**Solution:** Adopt wireguard-go's architecture: per-core encrypt/decrypt pools.

### Architecture

```
CURRENT:
  listenIn goroutine:
    TUN read → encrypt → send (serial, one goroutine)

PROPOSED:
  TUN reader goroutine:
    TUN read → dispatch to encrypt pool (round-robin)

  encrypt pool (GOMAXPROCS goroutines):
    receive plaintext → encrypt → pass to send coalescer

  send coalescer:
    buffer encrypted packets → flush via sendto
```

### Implementation

**New package:** `internal/pipeline/`

```go
type Pipeline struct {
    encryptPool  chan *work    // buffered channel, GOMAXPROCS workers
    decryptPool  chan *work    // buffered channel, GOMAXPROCS workers
    coalescer    *coalesce.Coalescer
}
```

**Key difference from wireguard-go:** wireguard-go passes slices of packets
("vectors") through channels instead of individual packets. This reduced
`runtime.chanrecv()` from 20% to negligible. We should do the same — batch
8-16 packets per channel send.

### Expected Impact

- Distributes encrypt/decrypt across all CPU cores
- Reduces channel overhead by 20% (vector-based dispatch)
- Combined with coalescing: estimated 40-60% reduction in per-packet latency

## Phase 3: Platform-Specific I/O Acceleration

### Linux: sendmmsg/recvmmsg Integration

Nebula already uses `recvmmsg` for UDP reads on Linux (`udp_linux.go:174`).
Add `sendmmsg` for writes — batch the coalesced output through the kernel's
multi-message send path.

```go
// In coalescer flush, on Linux:
func (c *Coalescer) flushLinux(msgs []rawMessage) {
    unix.Syscall6(unix.SYS_SENDMMSG, ...)  // batch send
}
```

### Linux: UDP GSO (Generic Segmentation Offload)

Set `UDP_SEGMENT` socket option to let the kernel handle segmentation of
large UDP datagrams. This allows sending one large buffer and having the
kernel split it into MTU-sized UDP packets — zero-copy from userspace.

```go
// setsockopt(fd, SOL_UDP, UDP_SEGMENT, segmentSize)
unix.SetsockoptInt(fd, unix.SOL_UDP, unix.UDP_SEGMENT, 1440)
```

Combined with coalescing, this eliminates per-packet syscall overhead entirely
on Linux 4.18+.

### Linux: UDP GRO (Generic Receive Offload)

Set `UDP_GRO` socket option to receive coalesced datagrams from the kernel.
The kernel batches incoming UDP packets before delivering to userspace.

```go
unix.SetsockoptInt(fd, unix.SOL_UDP, unix.UDP_GRO, 1)
```

### macOS: Network.framework Evaluation

Apple claims Network.framework provides "way beyond sockets" performance for
UDP. Worth benchmarking as an alternative to raw sockets, though likely
limited by the same per-packet constraints.

### Windows: WinTun Ring Buffer

WinTun provides an efficient ring buffer for TUN I/O. The coalescing layer
should integrate with this for batch reads/writes.

## Phase 4: Adaptive MTU via DPLPMTUD (RFC 8899)

**Problem:** Static MTU forces a choice: 1440 (safe for internet, slow for
LAN) or 4400 (fast for LAN, breaks on internet). Users shouldn't have to
know or care — the agent should discover the optimal MTU per peer.

**Opportunity:** No mesh VPN has working adaptive MTU. ZeroTier has had an
open request since 2016. Tailscale's is experimental and broken. WireGuard
refuses to implement it. This is a genuine competitive advantage for hopssh.

### Algorithm (RFC 8899 — Datagram Packetization Layer Path MTU Discovery)

Binary search using Nebula's existing Test message type (header type 4,
`outside.go:152-173`). No ICMP needed, no new wire protocol, works through
NATs and firewalls.

```
1. Start at BASE_MTU (1440 — safe for all internet paths)
2. Send TestRequest probe with padding = midpoint of search range
3. If TestReply received within 2s → path supports that size → raise floor
4. If no reply → path doesn't support that size → lower ceiling
5. Binary search converges in 5-7 probes (~10-14 seconds)
6. Set TUN MTU to discovered value via SIOCSIFMTU ioctl
7. Re-probe every 5 minutes to detect path changes (WiFi roaming)
```

### Architecture

```
┌──────────────────────────────────────────┐
│           internal/pmtud/                │
│                                          │
│  Prober (per-peer state machine)         │
│  ├─ floor: lowest confirmed MTU          │
│  ├─ ceiling: highest failed MTU          │
│  ├─ SendProbe(peer, size) → TestRequest  │
│  ├─ OnReply(peer, size) → raise floor    │
│  └─ OnTimeout(peer) → lower ceiling      │
│                                          │
│  Manager (background goroutine)          │
│  ├─ probes all active peers periodically │
│  ├─ TUN MTU = min(all peer MTUs)         │
│  └─ calls SetMTU on TUN device           │
└──────────────────────────────────────────┘
```

### Why Nebula Test Messages Are Perfect

Nebula already has `header.Test` with `TestRequest`/`TestReply` subtypes.
When a TestRequest arrives, Nebula automatically sends a TestReply with
the same payload (`outside.go:166-170`). The payload size determines the
outer packet size. If the reply comes back, the path supports that MTU.

No new message types, no protocol changes, no vendor patches to the
packet format. The probing is pure application-layer logic.

### Dynamic TUN MTU

`tun_darwin.go` already uses `SIOCSIFMTU` ioctl to set MTU (line 189-193).
Add a `SetMTU(int) error` method to the `Device` interface and implement
per-platform (Darwin, Linux, Windows). The manager calls this when the
discovered MTU changes.

### Per-Peer vs Per-Interface

TUN MTU is per-interface. Different peers may have different path MTUs.
Strategy: TUN MTU = min(all discovered peer MTUs). This is safe — large
packets to high-MTU peers waste some capacity but work correctly. Small
packets to low-MTU peers aren't fragmented.

### Expected Behavior

```
Agent boots → MTU 1440 (safe default)
  ↓ PMTUD probes run in background
Discovers LAN peer supports 4400 → MTU rises to 4400 (10-14 seconds)
  ↓ User connects Screen Sharing → High Performance mode, fast frames
Laptop moves to coffee shop WiFi → re-probe detects 1440 limit
  ↓ MTU drops to 1440 automatically
Returns home → re-probe → MTU rises to 4400 again
```

### Implementation Scope

| Component | Location | Effort |
|-----------|----------|--------|
| Prober state machine | `internal/pmtud/pmtud.go` | ~200 lines |
| Prober tests | `internal/pmtud/pmtud_test.go` | ~150 lines |
| SetMTU vendor patch | `overlay/tun_darwin.go`, `tun_linux.go`, `tun_windows.go` | ~30 lines each |
| Device interface update | `overlay/device.go` | 1 line |
| Agent integration | `cmd/agent/main.go` | ~50 lines |
| Total | | ~500 lines |

### References

- [RFC 8899 — DPLPMTUD](https://datatracker.ietf.org/doc/html/rfc8899)
- [quic-go DPLPMTUD implementation (MIT)](https://github.com/quic-go/quic-go/pull/3520)
- Nebula Test message handling: `vendor/github.com/slackhq/nebula/outside.go:152-173`
- Nebula MTU ioctl: `vendor/github.com/slackhq/nebula/overlay/tun_darwin.go:189-193`

## Implementation Strategy: Shim Layer

All performance optimizations live in `internal/perf/` as a shim layer between
the TUN device and Nebula's packet processing. This isolates our changes from
vendor code, making Nebula upgrades safe.

```
┌─────────────────────────────────────────────────┐
│                  TUN Device                      │
└──────────────┬──────────────────┬────────────────┘
               │ read             │ write
┌──────────────▼──────────────────▼────────────────┐
│           internal/perf/ (our shim)              │
│                                                   │
│  ┌─────────────┐  ┌──────────┐  ┌─────────────┐ │
│  │ Fragmenter  │  │ Crypto   │  │ Coalescer   │ │
│  │ (split/     │  │ Pool     │  │ (batch/     │ │
│  │  reassemble)│  │ (per-CPU)│  │  flush)     │ │
│  └─────────────┘  └──────────┘  └─────────────┘ │
└──────────────┬──────────────────┬────────────────┘
               │ send             │ receive
┌──────────────▼──────────────────▼────────────────┐
│              Nebula Core (vendor)                 │
└──────────────────────────────────────────────────┘
```

**Key principle:** Nebula's vendor code stays untouched for phases 1-4. The
shim wraps the TUN device and UDP connections, intercepting packets before
and after Nebula processes them.

**Exception:** The existing vendor patches (graceful shutdown, TUN buffer reuse,
multi-reader UDP, TUN write mutex) remain as patches because they modify
Nebula internals that can't be shimmed.

## Priority Order

The original Phase 1-4 structure (design docs below) organizes work by technique. For
strategic priority across all platforms, use this tier list instead. It incorporates the
2026-04-17 competitive audit that corrected several previously-overstated claims.

| Tier | Item | Truly novel? | Effort | Notes |
|------|------|--------------|--------|-------|
| 1 | **DPLPMTUD (build it)** | **✅ Would be first for mesh VPN** — no competitor ships this in production. Design already drafted (Phase 4 below). | 2-3 weeks | All platforms. Biggest "first-in-class" win we have realistic access to. |
| 1 | **Linux GSO/GRO + checksum unwind + crypto vector** | ❌ Catch-up to Tailscale, not novel. But necessary. | 3-4 weeks | Gap is multi-Gbps on modern hardware (not 900 Mbps — that figure is specific to DN's c6i-class bench). See `linux-throughput-plan.md` for the full MVP + ship-gated Step 4 plan. |
| 2 | **Windows RIO (Registered I/O)** | ⚠️ First for *userspace* VPNs only — kernel VPNs (WireGuardNT) went WSK instead, so RIO is irrelevant to the kernel class. Narrower positioning than "unique across all VPNs." | 6-8 weeks incl. Windows CI/CD setup | Requires CGO or syscall wrappers. Real win but scope-heavier than initial "3 weeks" claim. |
| 2 | **Sleep/wake resilience** | ⚠️ macOS LAN + cellular: all PASS. Linux VM: sleep code path PASS (SIGSTOP-simulated). Linux DNS bug found + **fixed in v0.9.8-dev** (`cmd/agent/dns_linux.go` — per-link probe + drop-in fallback). Windows, bare-metal Linux suspend, SSID-roam-mid-sleep still unmeasured. Evidence: `spike/sleep-wake-evidence/RESULTS.md`. | macOS + Linux DNS done; Windows VM ~1 day; bare-metal Linux needs hardware | See `#sleep-wake-recovery` section above. |
| 3 | **Cross-platform vectorized crypto pipeline (batch)** | ⚠️ Incremental. wireguard-go's per-core pool is already cross-platform; the *batch* optimization that needs PR #75's vector channels is Linux-gated because it consumes the GSO/GRO packet vectors. On macOS/Windows, crypto parallelism from per-core pools is available but batching requires equivalent platform I/O. | 3-4 weeks | Most meaningful *after* Linux GSO/GRO lands (to produce the vectors). |
| 4 | macOS Network.framework eval | Research only — may not help given `sendmsg_x` lead | 1 week | Phase 3 below |
| Drop | **Smart pacing / BBR for WiFi airtime** | ❌ Dead end. Research ([arxiv.org/html/2512.18259v1](https://arxiv.org/html/2512.18259v1)) + our own Discovery Log confirm: WiFi airtime contention is MAC-layer, below IP. Userspace pacing cannot help. | — | Not pursued. |
| Drop | **Multipath bonding as "novel differentiator"** | ❌ Not novel. [Speedify](https://speedify.com/) ships real WiFi+cellular bonding since 2014 across all platforms. ZeroTier has protocol-level multipath (active-backup, flow-based LB). If we ever want multipath, frame it as "catching up to Speedify," not first-in-class. | 3-6 months if pursued | Not on near-term roadmap. |

**Legacy phase reference** (original design docs, below this table): Phase 1 Coalescing
is **not yet built** (`internal/coalesce/` doesn't exist — the confusion with the
shipped `sendmsg_x` batching has been corrected). Phase 2 Crypto pools is a
cross-platform pattern borrowed from wireguard-go — valuable but the *batching* that
amplifies it is platform-I/O-gated, per Tier 3 above. Phase 3 Platform I/O has shipped
the macOS half (patches 04-10); Linux half is Tier 1 above. Phase 4 Adaptive MTU is
Tier 1 above.

| Phase | Status |
|-------|--------|
| 1. Coalescing | ⏳ Planned (not yet built; `internal/coalesce/` does not exist) |
| 2. Crypto pools | ⏳ Planned — per-core pool is cross-platform; batching benefit gated on platform I/O (Tier 3) |
| 3. Platform I/O | ✅ macOS shipped (patches 04-10); ⏳ Linux Tier 1, Windows RIO Tier 2 |
| 4. Adaptive MTU (DPLPMTUD) | ⏳ Planned (Tier 1). No mesh VPN ships this today. |

## Profiling Methodology

Always profile before and after changes:

```bash
# CPU profile during Screen Sharing
curl -H "Authorization: Bearer <token>" \
  "http://<mesh-ip>:41820/debug/pprof/profile?seconds=30" > before.prof

# Compare
go tool pprof -top before.prof
go tool pprof -top after.prof

# Latency
ping -c 100 <mesh-ip>   # during Screen Sharing

# Throughput
iperf3 -s               # on one machine
iperf3 -c <mesh-ip>     # on the other
```

## Lessons Learned

- **Profile first, optimize what the profile shows.** We assumed crypto was
  the bottleneck; profiling revealed it was 0.8% CPU. Syscalls were 71%.
- **macOS UDP socket buffers cause bufferbloat.** Large SO_RCVBUF on
  single-packet-read platforms queues stale packets instead of processing
  fast.
- **IP fragmentation is worse than more packets.** MTU 2800 with IP
  fragmentation doubled syscalls. App-layer fragmentation is required for
  large effective MTU.
- **AES-GCM is faster than ChaCha20 on Apple Silicon.** Hardware AES
  instructions beat NEON vector operations.
- **WiFi spikes dominate latency.** 60-90ms periodic spikes are WiFi power
  management, not VPN overhead. All VPNs see them equally.
- **Screen Sharing High Performance checks connection quality at startup.**
  Cold tunnels fail the probe. Pre-warming with blocking handshakes fixes it.
- **macOS Screen Sharing checks TUN interface MTU.** Rejects High Performance
  mode if MTU is below a threshold (experimentally ~1500). MTU 2800+ always
  passes. Adaptive MTU (DPLPMTUD) will solve this automatically.
- **Coalescing buffer size is a latency/throughput tradeoff (measured in aborted coalescing spike, never shipped).** 8KB buffer
  cut sendto by 59% but amplified WiFi spikes to 423ms (5-6 IP fragments).
  3200 bytes (2 packets per batch) was the sweet spot in that experiment.
- **Higher MTU = fewer TUN reads per frame.** MTU 4400 (3 IP fragments)
  reduces TUN reads from 35 to 12 per 50KB keyframe — 33% less encrypt+send
  time. But static high MTU breaks internet paths.
