# Performance Engineering

## Current State (v0.6.58)

### Benchmarks

Nebula P2P between two Apple Silicon Macs on same LAN (WiFi + Gigabit Ethernet).

| Metric | Raw LAN | ZeroTier | Nebula | Gap |
|--------|---------|----------|--------|-----|
| Avg latency | 11.5ms | 14.2ms | 16.6ms | +2.4ms vs ZT |
| Stddev | 19.2ms | 21.2ms | 24.1ms | WiFi-dominated |
| Max | 92ms | 92ms | 96ms | WiFi spikes |

Periodic 60-90ms spikes are WiFi power management — identical across all three paths.
Nebula's actual overhead above raw LAN is ~3ms per packet.

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

## Platform Constraints

### macOS
- No `sendmmsg`/`recvmmsg` (Linux-only batch syscalls)
- No UDP GSO/GRO (Linux 4.18+)
- No TUN multiqueue (`IFF_MULTI_QUEUE` is Linux-only; utun is single-fd)
- No `SIOCSIFTXQLEN` for TUN transmit queue
- Single-packet-at-a-time I/O for both TUN and UDP

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

## Phase 4: Application-Layer Fragmentation

**Problem:** MTU 2800 would halve per-frame packet count for Screen Sharing,
but IP fragmentation doubles sendto syscalls. ZeroTier solves this with
app-layer fragmentation.

**Solution:** Fragment large TUN packets before encryption, reassemble after
decryption. Each fragment fits in 1440 bytes (no IP fragmentation).

### Wire Format

```
Fragment header (4 bytes):
  ┌──────────┬──────────┬──────────┬──────────────┐
  │ flags(1) │ frag_id  │ frag_idx │ frag_total   │
  │ bit 0:   │ (1 byte) │ (1 byte) │ (1 byte)     │
  │ is_frag  │          │          │              │
  └──────────┴──────────┴──────────┴──────────────┘
```

- Fragment before encrypt: split TUN packet into 1380-byte chunks
- Encrypt each fragment independently (each gets its own AEAD tag)
- Reassemble after decrypt on receiver (keyed by frag_id + source addr)
- Timeout stale fragments after 500ms

### Expected Impact

With TUN MTU 2800 and app-layer fragmentation:
- Each 2800-byte TUN packet → 2 fragments of ~1400 bytes each
- Each fragment encrypted independently → 2 UDP sends of ~1440 bytes
- Same syscall count as MTU 1440 single packet, but 2x payload per frame
- Screen Sharing: ~50% fewer TUN reads per screen update

Combined with coalescing: fragments from the same TUN packet can be
coalesced into a single UDP datagram, reducing syscalls to 1 per original
TUN packet regardless of size.

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

| Phase | Impact | Effort | Platforms |
|-------|--------|--------|-----------|
| 1. Coalescing | High (75% syscall reduction) | Medium | All |
| 2. Crypto pools | Medium (parallelism) | Medium | All |
| 3. Platform I/O | High on Linux, low on macOS | Medium | Linux, macOS, Windows |
| 4. App-layer frag | Medium (2x payload/frame) | High | All |

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
