# Next Steps — Performance & Architecture

## Current State (v0.9.2)

### What's Shipping
- 3 vendor patches to Nebula v1.10.3 (~15 lines total):
  1. **Graceful shutdown** — `io.ErrClosedPipe` + `io.EOF` checks in `listenIn`
  2. **TestReply panic fix** — copy buffer before encrypt (Go 1.25+ FIPS)
  3. **utun read buffer cache** — eliminates per-packet `make([]byte, 9005)` allocation on macOS
- **preferred_ranges** — RFC 1918 ranges for same-NAT P2P discovery
- **Network change detection** — auto-rebind + tunnel close on WiFi↔cellular switch
- **Peer warmup from heartbeat** — server returns online peer IPs, agent pre-warms tunnels
- **GOGC=400** — on both agent and server, reduces GC pause frequency 4x
- **MTU 2800** — optimally fills 2 IP fragments, halves packet count vs 1440
- **routines=1** — safe single-reader (macOS SO_REUSEPORT causes packet loss with >1)
- **Faster handshake** — 20ms try_interval (down from 100ms default)
- **Synchronous lighthouse warmup** — blocks until lighthouse handshake completes (30-40ms)
- **pprof endpoint** — built-in profiling at `/debug/pprof/*`
- **dev-deploy-server** — Docker build + push + Nomad update in one command

### Performance (2026-04-15)
| Scenario | Nebula | ZeroTier | Winner |
|----------|--------|----------|--------|
| LAN P2P (WiFi) | 14ms avg, 0% loss | 17ms avg | **Nebula** |
| WAN P2P (mobile hotspot) | 106ms avg, 0% loss | 222ms avg | **Nebula** |
| WAN relay (symmetric NAT) | 125ms avg | ~200ms | **Nebula** |
| P2P on carrier NAT | Fails (symmetric NAT) | Usually succeeds | ZeroTier |
| Network roam (WiFi↔cellular) | Auto, <5 seconds | Auto | Tie |

### Key Findings
- Nebula is **faster when P2P works** but **fails to establish P2P on carrier-grade NAT** (symmetric NAT)
- Relay overhead is only **9ms** (125ms relay vs 106ms P2P) — the bottleneck is network path, not processing
- Screen Sharing HP mode warning is a **macOS limitation**, not a tunnel issue (see Known Issues)

### Dev Workflow
- `make dev-deploy` — builds + deploys agent to both Macs in ~12 seconds
- `make dev-deploy-server` — builds Docker image, pushes to ghcr.io, updates Nomad job
- Can deploy agent over mesh: `scp hop-agent yavortenev@10.42.1.6:/tmp/`
- Both Macs have NOPASSWD sudo for hop-agent operations

---

## Known Issues

### Screen Sharing High Performance Mode Warning (macOS)

**Symptom**: First 2-3 Screen Sharing HP connection attempts show a "High Performance" warning and fail. Subsequent attempts succeed. Once connected, HP works fine.

**Root Cause**: macOS's network quality framework (`NWPathEvaluator`) tests each interface by reaching Apple's CDN (`mensura.cdn-apple.com`). Private VPN tunnels that don't route internet traffic are marked as `unsatisfied (No network route)`. Screen Sharing trusts this assessment and rejects HP mode.

**Verified**: `networkquality -I utun10` returns `"The Internet connection appears to be offline"` even with a fully warm, 5ms P2P tunnel.

**Impact**: Affects ALL private mesh VPNs (Tailscale, ZeroTier, WireGuard) that don't route internet traffic. Not specific to hopssh.

**Workaround**: Retry 2-3 times. Once a connection succeeds, Screen Sharing caches the result.

**Potential fixes** (not yet attempted):
- Route a subset of Apple CDN traffic through the tunnel (hacky, fragile)
- Use `defaults write` to disable the quality check (no known key exists)
- Implement a custom Screen Sharing client that skips the check

---

## Priority 1: P2P on Symmetric NAT

### Problem
Symmetric NAT (most mobile carriers, CGNAT) assigns a different external port per destination. The port learned by the lighthouse is wrong for peer-to-peer traffic. Hole punching fails → relay fallback.

Nebula has no STUN, no port prediction, no NAT type detection. Port prediction was attempted (±50 ports) but the carrier uses random port assignment, making prediction ineffective.

### Status: No Known Solution
- Port prediction tried and reverted — carrier uses random ports
- The fundamental issue: symmetric NAT assigns different ports per destination, and both sides see the wrong port for each other
- This is an unsolved problem in the industry for random-port symmetric NAT

### Mitigation
- Relay works (125ms avg, 9ms overhead) — the focus should be on making relay fast rather than trying to establish impossible P2P
- TCP/443 relay fallback (Priority 2) ensures connectivity even when UDP is blocked

---

## Priority 2: TCP/443 Relay Fallback

### Problem
Some networks block UDP entirely (corporate firewalls, hotel WiFi). Both P2P and UDP relay fail. Tailscale solves this with DERP (TCP/443 relay).

### Approach
WebSocket relay through the control plane's HTTPS port (9473, already open). Agent detects UDP relay failure → connects via WebSocket. No firewall changes needed.

- Large effort (~500-1000 lines)
- Product differentiator — universal connectivity through any network

---

## Priority 3: Adaptive Connection Quality

Detect P2P vs relay, measure RTT, expose to dashboard. Auto-tune keepalive intervals based on measured path quality. Show users whether they're on P2P or relay.

---

## Transport Layer Analysis (2026-04-15)

### Nebula's Hot Path
The data path is clean — **no goroutine handoffs, no channels, zero per-packet allocations** (after utun fix):
- Outbound: TUN read → firewall → encrypt → UDP `sendto()` (all in one goroutine)
- Inbound: UDP `recvfrom()` → header parse → decrypt → firewall → TUN write (all in one goroutine)

### Relay Path
- Relay overhead: only 9ms (2 AEAD operations + 2 syscalls at lighthouse)
- The lighthouse does NOT decrypt relay traffic — it verifies authentication and forwards opaquely
- sendmmsg batching was tried for egress but HURT performance (408ms vs 125ms) by holding packets during batch processing
- The bottleneck is network path, not lighthouse processing

### Per-Packet Overhead
| Component | macOS | Linux |
|-----------|-------|-------|
| TUN read syscall | 1 | 1 |
| TUN write syscall | 1 | 1 |
| UDP send syscall | 1 (`sendto`) | 1 (`sendto`) |
| UDP recv syscall | 1 | 1/N (`recvmmsg` batch) |
| Memory copies | 1 (utun header, cached) | 0 |

### What's NOT the Bottleneck
- Crypto (AES-GCM hardware accelerated, nanoseconds)
- Memory allocation (buffers pre-allocated, utun buffer cached)
- Goroutine scheduling (no handoffs on hot path)
- Relay processing (only 9ms overhead)

---

## What We Tried and Removed

| Feature | Why Removed |
|---------|-------------|
| **FEC (async parity)** | Zero-latency design worked on LAN (13.5ms vs 14.2ms). On cellular, +57% latency (300ms vs 191ms) from congestion. FEC helps random loss but hurts congestion-induced loss. |
| **FEC (original buffered)** | Buffered K packets → 10-second Screen Sharing freezes. |
| **WrapWriters vendor patch** | Added for FEC. Must wrap `f.outside` + `f.handshakeManager.outside` + `f.writers[]`. Removed with FEC. |
| **Port prediction (±50 ports)** | Carrier uses random port assignment. Flooding 100 punch packets caused EAGAIN socket errors. |
| **sendmmsg egress batching** | Held packets during batch processing → 408ms vs 125ms relay. Needs per-packet flush architecture to be useful. |
| **Full /24 subnet warmup** | Scanning 254 IPs triggers 254 Nebula handshakes simultaneously → EAGAIN socket flood, tunnels go dead. |
| Multi-reader UDP (SO_REUSEPORT) | Created orphaned sockets → 35% packet loss |
| Packet coalescing | No benefit at MTU 1440 |
| TUN Read buffer reuse | Caused crypto/cipher buffer overlap panic |
| PMTUD | Correctly discovers 1440, can't improve beyond it |
| ChaCha20-Poly1305 | Slower than AES-GCM on Apple Silicon |
| GOMEMLIMIT | Too tight, caused excessive GC |
| 2MB UDP socket buffers | Caused bufferbloat (50-293ms spikes) |

### Key Lessons
1. **FEC hurts on bandwidth-constrained paths** — extra parity causes congestion
2. **sendmmsg needs per-packet flush** — batch-flush holds packets and adds latency
3. **Subnet scanning floods Nebula** — each dead IP triggers a 1.3s handshake timeout
4. **Symmetric NAT with random ports is unsolvable** — no port prediction works
5. **macOS marks VPN interfaces as offline** — Screen Sharing HP mode always warns initially
6. **Relay overhead is trivially small (9ms)** — optimizing relay processing won't help; the bottleneck is network path
7. **Network change detection + auto-rebind is essential** — without it, roaming from relay to P2P takes minutes

## Architecture Notes

### Vendor Patches
- Located in `patches/` directory
- Applied via `make patch-vendor` (called by `make setup`)
- Each patch is a standard unified diff
- When Nebula updates: re-vendor, re-apply patches, fix conflicts
