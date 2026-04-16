# Linux throughput plan — Phase 3 (match or surpass kernel WireGuard)

## Context

hopssh is already the fastest mesh VPN on macOS (unique `sendmsg_x`/`recvmsg_x` batch
syscalls). The one front where we are demonstrably behind
is **Linux throughput**: Defined Networking's own 10 Gbps benchmark
([blog](https://www.defined.net/blog/nebula-is-not-the-fastest-mesh-vpn/)) shows Nebula
at ~9 Gbps transmit / **7.8 Gbps receive**, vs Tailscale at **8.8 Gbps receive** — a
~900 Mbps gap that exists purely because Tailscale implemented UDP GSO/GRO and a
vectorized crypto pipeline in userspace wireguard-go, and Nebula has not.

Tailscale's own engineering writeups ([throughput improvements](https://tailscale.com/blog/throughput-improvements),
[10 Gb/s](https://tailscale.com/blog/more-throughput)) document the techniques:
**UDP Generic Segmentation Offload (GSO), UDP Generic Receive Offload (GRO),
checksum loop unwinding, and packet-vector channels.** All are public, all are
standard Linux kernel interfaces (no custom drivers), and the reference
implementation in [wireguard-go PR #75](https://github.com/WireGuard/wireguard-go/pull/75)
is MIT-licensed and readable.

This plan adapts those four techniques to hopssh's Nebula fork. The target outcome
is that a Linux-to-Linux iperf3 benchmark shows hopssh **at parity with Tailscale
on receive and transmit, and within 10%** of the kernel WireGuard module on the
same hardware. The existing macOS lead (batch `sendmsg_x`/`recvmsg_x`, patches
04–11) stays intact; none of the Linux work touches Darwin code paths.

---

## Step 0 — Establish the Linux baseline before any code

**Goal:** answer "where are we actually starting?" with real numbers, because
today's `performance.md` has *zero* Linux iperf3 measurements and the claimed
~7.8 Gbps Nebula receive figure comes from Defined Networking's benchmark, not
ours. If our baseline is worse than DN's (older kernel, different NIC, different
CPU), the headroom for each step below scales accordingly.

### Hardware setup

Two throwaway Linux VMs in the same OCI AZ, private subnet, arm64 (matches our
production worker pool). Shape `VM.Standard.A1.Flex`, 4 OCPU / 24 GB each,
~$0.02/hour. Destroy after the baseline run so we don't drift ops costs.

Alternative: two x86_64 VMs if we want to report both architectures. Prefer arm64
primary since that's what the cloud lighthouse runs on.

### What to measure

| Config | Transport | Notes |
|---|---|---|
| Raw LAN (no VPN) | TCP direct | Ceiling — shows the NIC + kernel limit |
| hopssh baseline | Nebula over UDP (today's code) | Starting point |
| hopssh + patch 12 | + sendmmsg batch send | Step 1 |
| hopssh + patches 12–13 | + UDP_SEGMENT (GSO send) | Step 2 |
| hopssh + patches 12–14 | + UDP_GRO (GRO recv) | Step 3 |
| hopssh + patches 12–15 | + vectorized pipeline | Step 4 (vendor patch) |
| hopssh + patches 12–16 | + checksum unwinding | Step 5 |
| wireguard-go (userspace) | — | Tailscale's transport |
| kernel WireGuard | — | The theoretical ceiling |
| ZeroTier | — | Market comparison |

Metrics per config:
- iperf3 TCP single-stream throughput (Mbps)
- iperf3 TCP 4-stream throughput
- iperf3 UDP 1 Gbps target — measured Mbps + packet loss
- TCP retransmits over 30s (`ss -ti` diff)
- CPU % per side (`top -b -n 30 -d 1 | grep <pid>` averaged)
- Syscall count over 30s (`perf stat -e 'syscalls:sys_enter_sendmsg*,syscalls:sys_enter_recvmsg*' -p <pid> sleep 30`)

### Test script (drop on one VM, peer's IP as arg)

```bash
#!/usr/bin/env bash
# /usr/local/bin/hopssh-bench — run on Linux VM as the iperf3 client.
# Usage: ./hopssh-bench <peer-overlay-ip> <label>
set -eu
PEER="${1:?peer ip}"; LABEL="${2:-run}"; OUT="/tmp/bench-${LABEL}"
mkdir -p "${OUT}"

echo "== warmup tunnel =="
ping -c 10 -i 0.2 -W 1 "${PEER}" | tail -3 | tee "${OUT}/warmup.txt"

echo "== TCP single-stream, 30s =="
iperf3 -c "${PEER}" -t 30 -i 5 -O 2 -J > "${OUT}/tcp-1.json"

echo "== TCP 4-stream, 30s =="
iperf3 -c "${PEER}" -t 30 -i 5 -O 2 -P 4 -J > "${OUT}/tcp-4.json"

echo "== UDP 1Gbps target, 30s =="
iperf3 -c "${PEER}" -u -b 1G -t 30 -i 5 -O 2 -J > "${OUT}/udp-1g.json"

echo "== CPU + syscall profile (TCP single-stream background) =="
iperf3 -c "${PEER}" -t 35 -O 2 > "${OUT}/tcp-1-retry.txt" &
IPERF_PID=$!
sleep 2
HA_PID=$(pgrep -f 'hop-agent serve' | head -1)
top -b -n 30 -d 1 -p "${HA_PID}" > "${OUT}/cpu.txt" &
perf stat -e 'syscalls:sys_enter_sendmsg,syscalls:sys_enter_recvmsg,syscalls:sys_enter_sendmmsg,syscalls:sys_enter_recvmmsg,syscalls:sys_enter_sendto,syscalls:sys_enter_recvfrom' \
  -p "${HA_PID}" -- sleep 30 2> "${OUT}/syscalls.txt"
wait ${IPERF_PID} 2>/dev/null || true

echo "== TCP retransmits =="
ss -ti dst "${PEER}" | head -20 > "${OUT}/ss-ti.txt" 2>&1 || true

echo "== done: ${OUT} =="
ls -la "${OUT}"
```

Run matrix: each of the 9 configs above, same peer, same time window (within 1
hour, before ambient network changes). The results land in
`spike/linux-throughput-evidence/baseline/` as a committed baseline.

### Deliverable

A single section appended to this doc titled **"Baseline (YYYY-MM-DD)"** with
one row per config and columns: TCP-1, TCP-4, UDP-1G loss, CPU%,
sendto-count/30s, retrans/30s. **No code changes begin until this table
exists.**

---

## Scope and staging

Five discrete patches, numbered continuing from existing `patches/`:

| # | Patch | Scope | Depends on | Can ship alone? |
|---|---|---|---|---|
| 12 | `linux-sendmmsg-batch.patch` | udp/udp_linux.go — add send queue + sendmmsg | — | Yes |
| 13 | `linux-udp-gso.patch` | udp/udp_linux.go — UDP_SEGMENT sockopt + GSO send path | 12 | Yes (with #12) |
| 14 | `linux-udp-gro.patch` | udp/udp_linux.go — UDP_GRO sockopt + GRO recv split | — | Yes |
| 15 | `vectorized-pipeline.patch` | interface.go — channel of []*packet instead of *packet | optional | Large scope; ships as a later gate |
| 16 | `checksum-unwind.patch` | internal/crypto — unroll the AEAD-adjacent checksum loop | — | Yes, anytime |

**Ship-gates:**

- **MVP** = 12 + 13 + 14 + 16. No vendor patch to `interface.go`. Self-contained in
  `vendor/github.com/slackhq/nebula/udp/` plus a small crypto file. Ship this
  first, measure, decide on #15.
- **Full parity** = MVP + 15. Only taken if Step 4 measurement shows Tailscale
  still ahead of us after MVP (their 20%→0% `chanrecv` reduction is the hardest
  win to land; the vendor-patch maintenance cost is real).

---

## Step 1 — sendmmsg batch send (patch 12)

### Current state

[`vendor/github.com/slackhq/nebula/udp/udp_linux.go`](../vendor/github.com/slackhq/nebula/udp/udp_linux.go)
uses `recvmmsg` on receive (ReadMulti, line 174) but **per-packet `sendto`** on
send (writeTo4 line 226, writeTo6 line 201). `Flush()` at line 255 is a no-op —
there's no queue to drain. We need to introduce the queue.

### What this patch adds

Mirror the Darwin batch-queue pattern from patch 04 (`udp_darwin.go`) but using
Linux syscall `SYS_SENDMMSG`:

```go
// New fields on StdConn:
sendMu    sync.Mutex
sendQueue []sendEntry    // up to batchSize entries
sendMsgs  []mmsghdr      // pre-allocated, parallel to sendQueue
sendIovs  []iovec

// Batch cap — start at 32 (Linux kernel limit on sendmmsg is 1024, but
// latency vs throughput tradeoff says don't go too large). Tune in Step 0.
const linuxSendBatch = 32
```

`WriteTo` enqueues instead of syscalling:

```go
func (u *StdConn) WriteTo(b []byte, ip netip.AddrPort) error {
    u.sendMu.Lock()
    if len(u.sendQueue) >= cap(u.sendQueue) {
        if err := u.flushLocked(); err != nil {
            u.sendMu.Unlock()
            return err
        }
    }
    // copy b into entry.data, build sockaddr into entry.name
    // append to u.sendQueue
    u.sendMu.Unlock()
    return nil
}
```

`Flush` does the actual syscall:

```go
func (u *StdConn) Flush() error {
    u.sendMu.Lock()
    defer u.sendMu.Unlock()
    return u.flushLocked()
}

func (u *StdConn) flushLocked() error {
    // populate u.sendMsgs[] from u.sendQueue[]
    // unix.Syscall6(SYS_SENDMMSG, fd, &msgs[0], len(msgs), 0, 0, 0)
    // reset queue
}
```

### Who calls Flush() on Linux?

Patch 07 (`batch-listenin.patch`) already inserts `f.writers[i].Flush()` at
[`interface.go:341`](../vendor/github.com/slackhq/nebula/interface.go) after each
TUN `ReadBatch` iteration. **But that only fires when the TUN reader implements
`batchReader`**, and on Linux the TUN reader is single-packet (`tun_linux.go`
uses plain `Read`). Options:

1. **Flush on a short timer** (e.g., 200 µs). Simple, but risk: the hopssh
   Darwin experience showed any timer-driven flush hurts TCP when not paired with
   pacing (performance.md §"Spike 2: timer-based send batching hurts TCP"). Skip.
2. **Add `batchReader` to `tun_linux.go`** via a separate small patch that uses
   multiple `Read` calls in a tight loop up to batch size, EAGAIN or batch-cap
   signals flush. Cleanest: mirrors Darwin's structure exactly.
3. **Caller-driven flush from the existing `listenIn`'s single-packet path** —
   modify the else branch at `interface.go:347` to also call
   `f.writers[i].Flush()` after every TUN read. This forces per-packet flush,
   negating the batch. Skip.

**Recommend option 2.** Small Linux-side `tun_linux.go` companion patch that
adds a `batchReader` interface impl, reading up to N packets by looping `Read`
with `SOCK_NONBLOCK` between the first blocking read and EAGAIN. The existing
patch 07 then does the right thing automatically on Linux too.

### Expected gain

Linux's `sendmmsg` amortizes one syscall over N packets. For a 300 Mbps iperf3
stream at MTU 1440, that's ~26 kpps. Batch=32 means ~800 syscalls/sec instead of
26000 — a **30× syscall reduction**, dropping sendto CPU from ~60% to ~2-5%.
Throughput ceiling scales with remaining bottlenecks (TUN read, crypto).
Realistic: +40-80% single-stream throughput over the baseline.

### Verification

Re-run `hopssh-bench` with patch 12 applied. Compare:

- TCP-1 Mbps (primary)
- `syscalls:sys_enter_sendto` count should drop to near zero
- `syscalls:sys_enter_sendmsg` count should rise (each sendmmsg counts as one)
- CPU% on hop-agent should drop substantially

Landing bar: **TCP-1 throughput improves ≥ 30%** and syscall count drops ≥ 20×.
Anything less, something is misconfigured (batch size, flush site).

---

## Step 2 — UDP GSO (patch 13)

### What UDP_SEGMENT does

`setsockopt(fd, SOL_UDP, UDP_SEGMENT, segment_size)` tells the kernel: "when I
send a large buffer, split it into UDP packets of `segment_size` each for wire
transmission." The userspace caller gets to issue one large sendmsg for
`segment_size × N` bytes, and the kernel produces N packets on the wire, with
all the per-packet UDP header / IP header work done once at GSO layer. This is
the same trick TCP uses (TSO) but for UDP — kernel 4.18+.

### How it combines with patch 12

Patch 12 already batches up to N encrypted packets via sendmmsg. Patch 13
changes the packing:

**Before (patch 12 only):** N separate `mmsghdr` entries, each pointing to its
own encrypted packet buffer. One `sendmmsg` syscall; kernel processes N
independent UDP sends.

**After (patch 13):** **One** `msghdr` for up to N contiguous packets of the
*same size*, with `UDP_SEGMENT` cmsg indicating segment size. One `sendmsg`
syscall; kernel emits N wire packets, one GSO context, minimal stack traversal.

We keep `sendmmsg` as fallback for mixed-size batches (handshake + data
interleaved). GSO path is the fast lane.

### Same-size batching

Nebula encrypted packet size = inner IP packet size + Noise overhead (~32
bytes). Bulk TCP transfers have inner packets at MSS (typically
`tun.mtu - 40 = 1400`), so encrypted size is uniform at `1400 + 32 = 1432`
bytes — perfect for GSO. Control traffic (handshakes, lighthouse, punchy) is
mixed size — falls back to non-GSO sendmmsg path.

Strategy in `flushLocked`:

```go
func (u *StdConn) flushLocked() error {
    if len(u.sendQueue) == 0 { return nil }
    // Group by destination + size. Same-{dst,size} entries go in a GSO batch.
    // Mixed batches go through sendmmsg.
    // In the common case (single bulk flow), one GSO send fires.
}
```

Kernel version check: `setsockopt(fd, SOL_UDP, UDP_SEGMENT, 1200)` at socket
creation. If EOPNOTSUPP or ENOPROTOOPT: kernel is pre-4.18, skip GSO, sendmmsg
fallback still works.

### Expected gain

Tailscale measured **4× UDP throughput gain on bare metal Linux** from GSO
alone. Tempered for our Nebula path (single-flow-dominated, already batching in
userspace): realistic **+60-100% TCP-1 Mbps on top of patch 12**.

### Verification

Add to `hopssh-bench`:

```bash
echo "== GSO sent segments (from nstat) =="
nstat -z | grep -iE 'udp|gso' | tee "${OUT}/nstat.txt"
```

- `UdpOutDatagrams` should rise.
- `UdpOutSegs` (kernel 6.x+) confirms GSO path active.
- TCP-1 throughput step-change vs Step 1.

Landing bar: **TCP-1 ≥ 2× Step 1 baseline, or within 15% of wireguard-go
single-stream on same hardware.**

---

## Step 3 — UDP GRO (patch 14)

### What UDP_GRO does

`setsockopt(fd, SOL_UDP, UDP_GRO, 1)` tells the kernel: "when multiple UDP
packets arrive from the same 5-tuple in a short window, coalesce them into one
`recvmsg` delivery." Kernel 5.0+. Receiver reads one large buffer; a control
message (`SCM_UDP_GRO`) carries the segment size so we can split.

### Receive path change

Current `ReadMulti` (line 174) uses recvmmsg and returns N buffers, one per wire
packet. With GRO enabled, each of those N buffers may itself contain M
coalesced packets. We add a split step:

```go
// After ReadMulti returns, for each msg[i]:
segSize := extractGROSegmentSize(msg[i].Hdr.Control)  // from cmsg
if segSize == 0 || segSize >= msg[i].Len {
    // No GRO merge; one packet in buffer
    r(addr, buffers[i][:msg[i].Len])
} else {
    // GRO merged; split into segSize chunks
    for off := 0; off < int(msg[i].Len); off += segSize {
        end := off + segSize
        if end > int(msg[i].Len) { end = int(msg[i].Len) }
        r(addr, buffers[i][off:end])
    }
}
```

The Nebula cert/MAC check happens per-packet after the split, unchanged — GRO
only affects the kernel→userspace handoff.

### Buffer size increase

Per-message buffer needs to hold up to `65535` bytes (max UDP payload) because
GRO can coalesce many packets. Currently buffers are `MTU = 9001` bytes. Bump
the GRO-enabled path to 64 KB per entry. Allocation cost is one-time at socket
setup; zero ongoing cost.

### Expected gain

On the receive side, roughly mirrors GSO's send-side win — fewer
`recvmsg`/`recvmmsg` syscalls per Gbps. This is the patch that closes the
900 Mbps "Nebula receive deficit" vs Tailscale that DN measured. **Estimated
+40-60% TCP-1 receive-side throughput**, which shows up as higher iperf3 with
the Linux box as the *server*.

### Verification

Run iperf3 with the Linux VM as the **server** (`iperf3 -s`) and the peer as
client. Pre-patch receive throughput sets the ceiling; post-patch should
approach transmit throughput (symmetric).

Landing bar: **iperf3 reverse direction (TCP-1 with -R) ≥ 2× pre-patch.**

---

## Step 4 — Vectorized packet pipeline (patch 15) — ship gate

### What this is

Today, `interface.go` passes individual packets through the outbound goroutine
chain (`listenIn` → `consumeInsidePacket` → encrypt → writer's `WriteTo`). One
goroutine per routine, serial per-packet. Every channel-borne packet incurs
~20% CPU overhead in `runtime.chanrecv` per Tailscale's pre-optimization
profile.

The vectorized pattern (wireguard-go PR #75): **channels carry `[]*packet`
slices**, not `*packet`. One channel op amortizes over K packets. Tailscale
measured `chanrecv` dropping from 20% to negligible after this change, and
worker goroutines per CPU core scale crypto linearly.

### Scope

This is the first patch in the Phase 3 plan that's a **vendor patch**
(`vendor/github.com/slackhq/nebula/interface.go`). That's a material
maintenance burden — every Nebula upstream version bump requires reapplying
and testing. Consider the ship-gate below carefully.

Rough scope: ~400 lines of changes across `interface.go`, maybe `inside.go`
and `outside.go`. Adds a new internal `packetBatch` type, rewires
`consumeInsidePacket` and `readOutsidePackets` to accept batches, spawns a
GOMAXPROCS-sized encrypt/decrypt worker pool.

### Ship gate

**Measure MVP (patches 12+13+14+16) first.** If TCP-1 and TCP-4 are within 10%
of wireguard-go on the same hardware, **skip Step 4**. The 900 Mbps gap DN
measured was attributable primarily to GRO absence, and MVP ships that.

If MVP lands us more than 15% below wireguard-go or more than 25% below kernel
WireGuard on TCP-1, Step 4 is justified. Otherwise it's a vendor-patch tax for
diminishing returns.

### Expected gain (conditional on ship gate)

If we get here: **+30-60% TCP-4 multi-stream throughput** (primary beneficiary
is multi-flow workloads), **+10-20% TCP-1**, **better multi-core scaling on
concentrators (lighthouse/relay)**. Secondary: latency under load improves
because encrypt work now parallelizes per core.

### Verification

TCP-4 iperf3 with 8+ streams. CPU% per core in `top`. The pre-patch profile
should show one CPU near 100% (the serial encrypt goroutine); post-patch, load
should distribute evenly across cores.

Landing bar: **TCP-4 throughput ≥ 1.5× MVP baseline AND matches or exceeds
Tailscale on the same hardware.**

---

## Step 5 — Checksum loop unwinding (patch 16)

### What this is

Nebula's crypto path invokes an AEAD-adjacent checksum loop (either in Go
runtime or in `crypto/cipher`). Tailscale unwound loops to process 64–128 byte
chunks at a time instead of byte-at-a-time, measured **57% checksum CPU
reduction, 10% overall throughput on older CPUs, 5% on newer**. This is free
performance if the code is still in a hot path.

Scope: small. Likely one file under `internal/crypto/` or inside a thin wrapper
around `crypto/cipher.AEAD.Seal`. Can be written, tested, and shipped in a
single afternoon. Not path-dependent on patches 12–15; ship any time.

### Expected gain

5-10% throughput, more on older x86 / arm32 hardware. Low cost, high PR value
for the "ships optimizations other mesh VPNs don't" narrative.

### Verification

Same `hopssh-bench`. Primary metric: CPU% delta at same throughput. Secondary:
max achievable TCP-1 Mbps.

---

## Verification matrix (cumulative)

| Stage | TCP-1 Mbps | TCP-4 Mbps | UDP-1G loss | sendto/30s | CPU% (client) | Pass bar |
|---|---|---|---|---|---|---|
| Baseline (measured) | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | — |
| + patch 12 (sendmmsg) | | | | ≤ 5% of baseline | ≤ 50% of baseline | TCP-1 +30% |
| + patch 13 (GSO) | | | | ≤ 1% of baseline | ≤ 30% of baseline | TCP-1 2× step 1 |
| + patch 14 (GRO) | | | | | | TCP-1 -R ≥ 2× pre-14 |
| + patch 16 (checksum) | | | | | | TCP-1 +5% |
| **MVP total** | **≥ Tailscale userspace** | | | | | within 10% of kernel WG |
| + patch 15 (vector) | | | | | distributed across cores | TCP-4 +50% MVP |

Target column on the right: hopssh at MVP should be **within 10% of kernel
WireGuard** on TCP-1 and **≥ Tailscale** on TCP-1 and TCP-4. Full parity (with
patch 15) should **meet or beat Tailscale on TCP-4**.

---

## Risks and rollback

### Risk: UDP_SEGMENT on older kernels

Ubuntu 18.04 LTS ships 4.15; UDP_SEGMENT needs 4.18. On EOL / frozen kernels
the sockopt returns ENOPROTOOPT. Mitigation: runtime feature-detect at socket
setup; record capability on `StdConn`; sendmmsg fallback path always
available.

### Risk: UDP_GRO on older kernels

Needs 5.0+. RHEL 7 / CentOS 7 / old Debian. Same detection pattern; fallback to
non-GRO recvmmsg.

### Risk: Packet reordering

GSO produces packets in submission order — no reordering hazard. GRO coalesces
adjacent arrivals — we split back to original order. Neither introduces
within-flow reordering. (Per the hardened lesson from the macOS priority queue:
never reorder within a TCP flow.)

### Risk: GSO batch padding waste

If encrypted packets are mixed size, GSO batching doesn't apply and we fall
back to non-GSO sendmmsg. Worst case: same as Step 1 throughput. No regression.

### Risk: Vendor-patch maintenance (patch 15)

If we ship Step 4, we carry an `interface.go` patch forever. Nebula upstream
moves slowly but not never. Mitigation: ship Step 4 only if MVP measurement
shows it's necessary; document the patch rationale inline so a future maintainer
knows why; include patch in `scripts/check-nebula-patch.sh` so we notice
upstream drift.

### Rollback per stage

All five patches land as separate files in `patches/`. Each is independently
revertable via `git revert` on the patch-adding commit. If a production
regression appears after a specific patch lands, delete the patch file, run
`make patch-vendor`, rebuild, redeploy. No data format changes, no wire
incompatibility — Nebula's on-wire packets look identical before and after
these patches; the changes are entirely in how Linux queues, segments, and
delivers them at the socket boundary.

---

## Critical files

**Read before starting:**
- [`vendor/github.com/slackhq/nebula/udp/udp_linux.go`](../vendor/github.com/slackhq/nebula/udp/udp_linux.go) — today's Linux UDP path (`StdConn`, line 20)
- [`vendor/github.com/slackhq/nebula/udp/udp_darwin.go`](../vendor/github.com/slackhq/nebula/udp/udp_darwin.go) — the Darwin batch pattern to mirror (patches 04–11 already landed)
- [`vendor/github.com/slackhq/nebula/interface.go`](../vendor/github.com/slackhq/nebula/interface.go) — `listenIn`/`listenOut` at line 266-345, patch 07's `batchReader` glue
- [`patches/04-batch-udp-darwin.patch`](../patches/04-batch-udp-darwin.patch) — reference for the queue+flush pattern

**Create:**
- `patches/12-linux-sendmmsg-batch.patch`
- `patches/13-linux-udp-gso.patch`
- `patches/14-linux-udp-gro.patch`
- `patches/15-vectorized-pipeline.patch` (conditional on ship-gate)
- `patches/16-checksum-unwind.patch`

**Test infrastructure:**
- `scripts/hopssh-bench.sh` — the per-VM benchmark runner from Step 0
- `spike/linux-throughput-evidence/` — baseline + per-step result dirs (one per
  config listed in Step 0)

**Update:**
- `docs/performance.md` — append measured numbers per step to the Roadmap Phase
  3 section (mark it "✅ Done" or "In progress"), reconcile the Phase 1
  coalescing-vs-sendmsg_x labelling inconsistency noted in the strategic audit.
- `CLAUDE.md` — one Discovery Log entry per shipped patch, terse, with numbers.

---

## References

- [Tailscale — Improving Tailscale Performance: Enhancing Userspace with Kernel Interfaces](https://tailscale.com/blog/throughput-improvements)
- [Tailscale — Surpassing 10Gb/s with Tailscale: Performance Gains on Linux](https://tailscale.com/blog/more-throughput)
- [Tailscale — Enhance UDP Throughput for QUIC and HTTP/3 on Linux](https://tailscale.com/blog/quic-udp-throughput)
- [Defined Networking — Nebula is not the fastest mesh VPN](https://www.defined.net/blog/nebula-is-not-the-fastest-mesh-vpn/) — the benchmark that quantifies the 900 Mbps Nebula receive gap we're closing
- [WireGuard/wireguard-go PR #75 — UDP GSO/GRO, checksum optimizations, vectorized crypto](https://github.com/WireGuard/wireguard-go/pull/75) — MIT-licensed reference implementation
- [Linux `UDP_SEGMENT` kernel patch (v4.18)](https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/commit/?id=bec1f6f697362) — original GSO merge commit for kernel version context
- [Linux `UDP_GRO` kernel patch (v5.0)](https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/commit/?id=e20cf8d3f1f7) — original GRO merge commit

---

## Timeline

- **Step 0 baseline**: 1 day (spin up 2 OCI VMs, run matrix, collect numbers, commit).
- **Step 1 + Step 5**: 1 week (sendmmsg batching + checksum unwind).
- **Step 2 + Step 3**: 1 week (GSO + GRO).
- **MVP measurement + ship gate decision for Step 4**: 2 days.
- **Step 4 (if taken)**: 2-3 weeks (vendor patch; higher risk).

Total: **2-3 weeks for MVP**, 4-6 weeks including the optional Step 4. This
closes the only remaining Linux throughput gap and establishes hopssh as the
performance leader across every platform and transport mode except kernel-WG
single-binary DIY, which is a different product category.
