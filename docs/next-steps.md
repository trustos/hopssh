# Next Steps — Performance & Architecture

## Current State (v0.9.1)

### What's Shipping
- 3 vendor patches to Nebula v1.10.3 (~15 lines total):
  1. **Graceful shutdown** — `io.ErrClosedPipe` + `io.EOF` checks in `listenIn`
  2. **TestReply panic fix** — copy buffer before encrypt (Go 1.25+ FIPS)
  3. **utun read buffer cache** — eliminates per-packet `make([]byte, 9005)` allocation on macOS
- **preferred_ranges** — RFC 1918 ranges for same-NAT P2P discovery
- **MTU 2800** — optimally fills 2 IP fragments, halves packet count vs 1440
- **routines=1** — safe single-reader (macOS SO_REUSEPORT causes packet loss with >1)
- **GOGC=400** — reduces GC pause frequency 4x
- **Faster handshake** — 20ms try_interval (down from 100ms default)
- **Tunnel warmup** — blocks until peer handshakes complete on startup
- **pprof endpoint** — built-in profiling at `/debug/pprof/*`

### Performance (2026-04-15)
| Scenario | Nebula | ZeroTier | Winner |
|----------|--------|----------|--------|
| LAN (WiFi) | 14ms avg, 0% loss | 17ms avg | **Nebula** |
| WAN P2P (mobile hotspot) | 106ms avg, 0% loss | 222ms avg | **Nebula** |
| WAN relay (symmetric NAT) | 105ms, poor Screen Sharing | ~200ms, usable | ZeroTier |
| P2P success on carrier NAT | Fails (symmetric NAT) | Usually succeeds | ZeroTier |

### Key Finding
Nebula is **faster when P2P works** but **fails to establish P2P on carrier-grade NAT** (symmetric NAT). When P2P fails, traffic relays through the lighthouse — adding latency and making the lighthouse a throughput bottleneck for Screen Sharing.

### Dev Workflow
- `make dev-deploy` — builds + deploys to both Macs in ~12 seconds
- SSH access: Mac mini (tenevi@192.168.23.3), laptop (yavortenev@192.168.23.18)
- Can also deploy over mesh: `scp hop-agent yavortenev@10.42.1.6:/tmp/`
- Both have NOPASSWD sudo for hop-agent operations

---

## Priority 1: P2P on Symmetric NAT

### Problem
Symmetric NAT (most mobile carriers, CGNAT) assigns a different external port per destination. Nebula learns port X (from lighthouse traffic) but the peer needs port Y (assigned for peer-to-peer). Hole punching fails → relay fallback → poor Screen Sharing.

Nebula has no STUN, no port prediction, no NAT type detection.

### Approach: Birthday Paradox Port Prediction
Send punch packets to a range of ports around the learned port. With sequential/near-sequential port assignment (common in CGNAT), punching ±50 ports gives a high hit probability.

- Vendor patch to `lighthouse.go` punch handler
- When punching, send N packets to port range instead of 1
- Only activate when initial punch fails (don't waste bandwidth on easy NATs)
- ~100 lines, medium effort

### Alternative: NAT Type Detection (STUN)
Lightweight probe: agent sends to lighthouse from two source ports, compares external ports. If different → symmetric NAT → skip P2P, go straight to relay (skip 30-60s failed punch phase).

Could combine: detect symmetric NAT → try port prediction → fall back to relay.

---

## Priority 2: TCP/443 Relay Fallback

### Problem
Some networks block UDP entirely (corporate firewalls, hotel WiFi). Both P2P and UDP relay fail. Tailscale solves this with DERP (TCP/443 relay).

### Approach
WebSocket relay through the control plane's HTTPS port (9473, already open). Agent detects UDP relay failure → connects via WebSocket. No firewall changes needed.

- Large effort (~500-1000 lines)
- Product differentiator — universal connectivity through any network

---

## Priority 3: Relay Throughput Optimization

### Problem
Lighthouse relays every packet through Nebula's full packet handler (decrypt relay header → re-encrypt to forward). For Screen Sharing (~5-15 Mbps), this limits throughput.

### Approach
Zero-copy relay: parse only the relay header, forward inner payload without touching it. Inner payload is already E2E encrypted.

---

## Priority 4: Adaptive Connection Quality

Detect P2P vs relay, measure RTT, expose to dashboard. Auto-tune keepalive intervals based on measured path quality.

---

## Priority 5: sendmmsg Egress Batching (Linux)

Add `sendmmsg()` to `udp_linux.go` for outgoing packets (Nebula already has `recvmmsg` for reads). Benefits the lighthouse/relay server most — 10-64x fewer send syscalls.

---

## Transport Layer Analysis (2026-04-15)

### Nebula's Hot Path
The data path is clean — **no goroutine handoffs, no channels, zero per-packet allocations** (after our utun fix):
- Outbound: TUN read → firewall → encrypt → UDP `sendto()` (all in one goroutine)
- Inbound: UDP `recvfrom()` → header parse → decrypt → firewall → TUN write (all in one goroutine)

### Per-Packet Overhead
| Component | macOS | Linux |
|-----------|-------|-------|
| TUN read syscall | 1 | 1 |
| TUN write syscall | 1 | 1 |
| UDP send syscall | 1 (`sendto`) | 1 (`sendto`, no batch) |
| UDP recv syscall | 1 | 1/N (`recvmmsg` batch) |
| Memory copies | 1 (utun header) | 0 |
| Encrypt mutex | Conditional (GoBoring only) | Same |

### What's NOT the Bottleneck
- Crypto (AES-GCM hardware accelerated, nanoseconds)
- Memory allocation (buffers pre-allocated per routine)
- Goroutine scheduling (no handoffs on hot path)
- Channel contention (no channels in data path)

---

## What We Tried and Removed

| Feature | Why Removed |
|---------|-------------|
| **FEC (async parity)** | Zero-latency design worked on LAN (13.5ms vs 14.2ms baseline). But on cellular, 20% extra parity packets caused congestion: 300ms vs 191ms without. FEC helps random loss but hurts congestion-induced loss. |
| **FEC (original buffered)** | Buffered K packets → 10-second Screen Sharing freezes. Redesigned to async, then removed entirely. |
| **WrapWriters vendor patch** | Added for FEC. Discovered: must wrap `f.outside` + `f.handshakeManager.outside` + `f.writers[]` for full coverage. Removed with FEC. |
| Multi-reader UDP (SO_REUSEPORT) | Created orphaned sockets → 35% packet loss |
| Packet coalescing | No benefit at MTU 1440 |
| TUN Read buffer reuse | Caused crypto/cipher buffer overlap panic |
| PMTUD | Correctly discovers 1440, can't improve beyond it |
| ChaCha20-Poly1305 | Slower than AES-GCM on Apple Silicon |
| GOMEMLIMIT | Too tight, caused excessive GC |
| 2MB UDP socket buffers | Caused bufferbloat (50-293ms spikes) |
| Nebula fork | Vendor patches are simpler |

### FEC Lessons (2026-04-15)
1. FEC only helps **random** loss. On bandwidth-constrained paths, extra parity causes **congestion-induced** loss — the opposite of what it fixes.
2. The async-parity design (send data immediately, compute parity in background) achieves zero added latency. The architecture is sound if FEC is ever needed.
3. WrapWriters at the UDP layer intercepts ALL traffic. Need lighthouse exclusion (by IP) to avoid breaking handshakes.

## Architecture Notes

### Vendor Patches
- Located in `patches/` directory
- Applied via `make patch-vendor` (called by `make setup`)
- Each patch is a standard unified diff
- When Nebula updates: re-vendor, re-apply patches, fix conflicts
