# Next Steps — Performance & Architecture

## Current State (v0.9.0)

### What's Shipping
- 3 vendor patches to Nebula v1.10.3 (~15 lines total):
  1. **Graceful shutdown** — `io.ErrClosedPipe` + `io.EOF` checks in `listenIn`
  2. **TestReply panic fix** — copy buffer before encrypt (Go 1.25+ FIPS)
  3. **WrapWriters** — generic hook for wrapping UDP connections
- **MTU 2800** — optimally fills 2 IP fragments, halves packet count vs 1440
- **routines=1** — safe single-reader (macOS SO_REUSEPORT causes packet loss with >1)
- **GOGC=400** — reduces GC pause frequency 4x
- **Faster handshake** — 20ms try_interval (down from 100ms default)
- **Tunnel warmup** — blocks until peer handshakes complete on startup
- **pprof endpoint** — built-in profiling at `/debug/pprof/*`

### Performance
- **16.1ms avg** latency during Screen Sharing (0% packet loss)
- **ZeroTier comparison**: 17.0ms avg (we're slightly faster)
- **WiFi spikes**: ~60-90ms every ~11 seconds (WiFi power management, affects all VPNs equally)

### Dev Workflow
- `make dev-deploy` — builds + deploys to both Macs in ~12 seconds
- SSH access: Mac mini (tenevi@192.168.23.3), laptop (yavortenev@192.168.23.18)
- Both have NOPASSWD sudo for hop-agent operations

---

## Priority 1: FEC Redesign (Async Parity)

### Problem
The current FEC implementation (`internal/fec/`) buffers K data packets before generating parity. This adds latency to every packet — 1-50ms depending on timeout. Real-time streams (Screen Sharing) freeze because data is held waiting for a group to fill.

### Solution: Async Parity
Send each data packet **immediately** (zero added latency). Generate parity packets asynchronously from a sliding window of recent packets. Send parity after the data.

```
CURRENT (broken for real-time):
  packet1 → buffer
  packet2 → buffer
  ...
  packetK → buffer full → encode parity → send K+M packets
  (K packets delayed by up to GroupTimeout)

PROPOSED (async parity):
  packet1 → send immediately
  packet2 → send immediately
  ...
  packetK → send immediately
  → async: compute parity from packets 1..K → send M parity packets
  (zero delay on data, parity arrives right after)
```

### How Receiver Works
- Data packets arrive immediately → pass to Nebula → processed normally
- Parity packets arrive shortly after → stored in recovery buffer
- If a data packet is missing → use parity to reconstruct → pass to Nebula
- If no packets are missing → parity is discarded (no overhead)

### Key Insight
FEC should be a **safety net**, not a **pipeline stage**. Data always flows at full speed. Parity is an optional backup that only matters when loss occurs.

### Implementation Notes
- Sender maintains a circular buffer of last K sent packets per peer
- After every K packets, compute M parity shards from the buffer
- Each parity shard has a FEC header identifying the data range it covers
- Receiver tracks received packets by sequence number
- When a gap is detected, check if parity covers the gap → reconstruct
- 2-byte length prefix per shard (already implemented) handles variable packet sizes

### Files
- `internal/fec/fec.go` — redesign sender to immediate-send + async parity
- `internal/fec/fec_test.go` — update tests for async model
- Tests should simulate: no loss (parity unused), single loss (recovered), burst loss

---

## Priority 2: P2P Reliability (Same-NAT)

### Problem
When both Macs are behind the same NAT, P2P sometimes takes 30-60 seconds to establish. During this time, traffic goes through the lighthouse relay at ~110ms. Screen Sharing fails to connect or shows High Performance warning.

### Root Cause
Nebula's hole punching sends packets to the peer's public IP. When both peers share the same public IP (same NAT), the router may drop these hairpin NAT packets. The `preferred_ranges` config tells Nebula to prefer local IPs, but the discovery takes time.

### Potential Fix
- Detect same-NAT peers (same public IP) and immediately try local IPs
- Skip the relay phase entirely for same-subnet peers
- Could be done in the warmup function — if peer's public IP matches ours, connect via LAN directly

---

## Priority 3: Control Plane Docker Image

### Problem
The Nomad-deployed control plane container needs to be updated when vendor patches change. Currently requires CI to build and push to ghcr.io.

### Solution
Add Docker image building to `make dev-deploy` or a separate `make dev-deploy-server` target:
```bash
# Build linux/arm64 server
GOOS=linux GOARCH=arm64 make build-linux
# Build + push Docker image
docker buildx build --platform linux/arm64 -t ghcr.io/trustos/hopssh:dev --push .
# Update Nomad job
ssh server "nomad job run ..."
```

---

## Priority 4: Screen Sharing High Performance Mode

### Problem
macOS Screen Sharing sometimes rejects High Performance mode over Nebula. We confirmed this is partly MTU-related (UTun interface with MTU < ~1500 gets rejected) and partly tunnel warmth (first connection after restart fails).

### Current Mitigations
- MTU 2800 (above threshold)
- Tunnel warmup (handshakes complete before user connects)
- Still inconsistent — sometimes works, sometimes doesn't

### Investigation Needed
- Binary search for exact MTU threshold on macOS Sequoia
- Check if there's a timing window where the quality probe runs before warmup completes
- Test if the `preferred_ranges` detection speeds up HP mode acceptance

---

## What We Tried and Removed

These were implemented, tested, and removed because they didn't measurably improve performance:

| Feature | Why Removed |
|---------|-------------|
| Multi-reader UDP (SO_REUSEPORT) | Created orphaned sockets → 35% packet loss |
| Packet coalescing (CoalescingConn) | No benefit at MTU 1440 (packets too large for buffer) |
| TUN Read buffer reuse | Caused crypto/cipher buffer overlap panic |
| TUN Write mutex | Only needed for multi-reader (removed) |
| PMTUD (adaptive MTU) | Correctly discovers 1440 but can't improve beyond fragmentation-free limit |
| ChaCha20-Poly1305 cipher | Slower than AES-GCM on Apple Silicon |
| GOMEMLIMIT | Too tight, caused excessive GC |
| 2MB UDP socket buffers | Caused bufferbloat (50-293ms spikes) |
| Nebula fork | 3 vendor patches are simpler than maintaining a fork |

## Architecture Notes

### Vendor Patches
- Located in `patches/` directory
- Applied via `make patch-vendor` (called by `make setup`)
- Each patch is a standard unified diff
- When Nebula updates: re-vendor, re-apply patches, fix conflicts

### FEC Library
- `internal/fec/` — Reed-Solomon implementation using klauspost/reedsolomon
- FEC header: `[0xFE][group_id 2B][index 1B][k 1B][m 1B]` (6 bytes)
- Backward compatible: raw Nebula packets (0x1X) pass through unchanged
- Tests pass for no-loss, single-loss, double-loss recovery
- Benchmark: 824ns/packet encode on Apple M1
- NOT wired into agent (disabled pending async parity redesign)
