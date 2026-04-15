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

## Applied Optimizations

### GOGC=400 (v0.6.50)
Reduces GC frequency 4x. Eliminated 100ms GC pause spikes.
**Location:** `cmd/agent/main.go`

### TUN Read Buffer Reuse (v0.6.51)
Vendor patch eliminates per-packet heap allocation in `tun_darwin.go:Read()`.
**Patch:** `patches/nebula-darwin-perf.patch`

### Decoupled Multi-Reader UDP (v0.6.56)
Vendor patch to `interface.go` separates `tunRoutines` from `routines`. macOS gets
4 parallel UDP reader goroutines (SO_REUSEPORT) sharing a mutex-protected TUN writer.
**Patch:** `patches/nebula-darwin-multithread.patch`

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

**Spike results (2026-04-16):**
- Pure Go implementation (no CGO) — struct layouts match XNU, verified with `unsafe.Sizeof`/`Offsetof`
- `recvmsg_x` batch receive: working, 64 packets per syscall, integrated into ListenOut
- `sendmsg_x` batch send with 500μs timer: **hurts TCP** — adds jitter that congestion control
  interprets as loss. 63 Mbps (batch=64) vs 154 Mbps (batch=1). Timer-based send batching
  is fundamentally incompatible with the serial TUN read → encrypt → send architecture.
- **Shipped: receive-only batching** (recvmsg_x). Send path uses direct sendto.
- Next: send batching needs architectural change — non-blocking TUN reads or connected sockets

**Status:** Receive batching shipped in `patches/04-batch-udp-darwin.patch`. Send batching
requires deeper integration (non-blocking TUN read loop or per-peer connected sockets).

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

| Phase | Impact | Effort | Platforms | Status |
|-------|--------|--------|-----------|--------|
| 1. Coalescing | High (59% sendto reduction) | Medium | All | ✅ Done (v0.6.59) |
| 2. Crypto pools | Medium (parallelism) | Medium | All | Planned |
| 3. Platform I/O | High on Linux, low on macOS | Medium | Linux, macOS, Windows | Planned |
| 4. Adaptive MTU (DPLPMTUD) | High (auto-optimize per path) | Medium | All | ✅ Done (v0.7.3) — first mesh VPN |

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
- **Coalescing buffer size is a latency/throughput tradeoff.** 8KB buffer
  cut sendto by 59% but amplified WiFi spikes to 423ms (5-6 IP fragments).
  3200 bytes (2 packets per batch) is the sweet spot.
- **Higher MTU = fewer TUN reads per frame.** MTU 4400 (3 IP fragments)
  reduces TUN reads from 35 to 12 per 50KB keyframe — 33% less encrypt+send
  time. But static high MTU breaks internet paths.
