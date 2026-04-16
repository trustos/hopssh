# QUIC transport notes (Phase 0 research, archived)

> **Archived (2026-04-17): Phase 1 closed without implementation.** A 30-minute
> baseline test (`spike/nebula-baseline-evidence/`) showed the existing Nebula
> transport already survives a 30s `ifconfig en0 down/up` cycle with TCP
> connections intact and ~3s ICMP recovery, so the QUIC integration this
> document was scoping was deemed unnecessary for the headline benefit
> (mobile reliability). See the QUIC Connection Migration entry in
> `CLAUDE.md` §Discovery Log and the result section at the end of
> `~/.claude/plans/purring-chasing-babbage.md` for the full decision trail.
>
> Phases 2 and 3 below are stubbed but were never executed — the call-site
> map in Phase 0 and the `udp.Conn` contract analysis remain the single
> cleanest summary of how Nebula's Darwin send path and the patch-04/07/08/09
> interplay work, so the doc is preserved as reference for any future work.

Original working notes for the viability spike that wraps Nebula's `udp.Conn`
over a QUIC datagram session. Plan was at
[`/Users/tenevi/.claude/plans/nested-cooking-jellyfish.md`](../../.claude/plans/nested-cooking-jellyfish.md).

These notes cover Phase 0 (read-only study of how Nebula's send/recv path
interacts with the Darwin batch + priority-queue patches) and Phase 2
(MTU comparison — never measured).

---

## Phase 0 — Batch-syscall interplay with a QUIC `udp.Conn`

### What a `udp.Conn` implementor has to honor

From [`vendor/github.com/slackhq/nebula/udp/conn.go`](../vendor/github.com/slackhq/nebula/udp/conn.go) (patched), the interface is:

```go
type Conn interface {
    Rebind() error
    LocalAddr() (netip.AddrPort, error)
    ListenOut(r EncReader)
    WriteTo(b []byte, addr netip.AddrPort) error
    Flush() error                 // patch 08
    ReloadConfig(c *config.C)
    SupportsMultipleReaders() bool
    Close() error
}
```

The patch-08 contract says: _"Platforms without batch send support MUST implement this as a no-op."_ — Linux, generic, Windows-RIO, and the test stub all do exactly that. The Darwin `StdConn` is the only one with real queueing.

### Call sites that actually drive the Darwin queue

`Flush()` is called from exactly **three** places after patches 04, 07, 08, 09 land:

| Caller | File | Purpose |
|---|---|---|
| `listenIn` (post-TUN batch) | [`interface.go:341`](../vendor/github.com/slackhq/nebula/interface.go) | Drain any UDP sends produced while consuming a TUN `ReadBatch`. Only fires when the TUN reader implements `batchReader` (Darwin only — patch 06). |
| `ListenOut` (self-flush at end of each UDP recv batch) | [`udp_darwin.go:289`](../vendor/github.com/slackhq/nebula/udp/udp_darwin.go) | Drains sends produced by the read-side callback (handshake replies, hole-punch 1-byte replies, relay forwards). Without this, inbound-triggered sends would sit until the next TUN batch fired — which may be never if no local TUN traffic is flowing. |
| `Flush()` tests | [`udp_darwin_batch_test.go`](../vendor/github.com/slackhq/nebula/udp/udp_darwin_batch_test.go) | Not in the hot path. |

`WriteTo()` is called from:

| Caller | File | Conn field |
|---|---|---|
| Data send (main path) | [`inside.go:388,394`](../vendor/github.com/slackhq/nebula/inside.go) | `f.writers[q]` (routine-specific) |
| Relay send | [`inside.go:328`](../vendor/github.com/slackhq/nebula/inside.go) | `f.writers[0]` (always writer 0) |
| Handshake retry | [`handshake_manager.go:242`](../vendor/github.com/slackhq/nebula/handshake_manager.go) | `hm.outside` (no Flush afterward — relies on the next listenIn/ListenOut batch to drain) |
| Punchy keepalive | [`connection_manager.go:527,532`](../vendor/github.com/slackhq/nebula/connection_manager.go) | `cm.intf.outside` |
| Hole-punch reply | `outside.go` (inside recv callback) | `f.outside` |

### What the priority queue actually does (patch 09)

- Two in-process lanes in `StdConn`: `laneControl` (cap 32) and `laneData` (cap 96).
- `classify(b)` peeks the plaintext Nebula header byte (`b[0]&0x0f`). Message type = 1 → data lane. Anything else (handshake, lighthouse, test, close) → control lane.
- On flush, `sendmsg_x` processes control-lane entries **before** data-lane entries. Within each lane, FIFO.
- The explicit rationale in the patch comment is: splitting data packets by size reorders TCP segments within a flow → SACK fires → measured 70% throughput drop + 2× retransmits. Only safe priority is across orthogonal categories (control vs data) where reordering can't happen within a single TCP flow.

### Does a QUIC `udp.Conn` need to recreate any of this?

No. Reasoning, point by point:

1. **`Flush()` exists to amortize syscall cost.** `sendmsg_x` lets Darwin send 32-128 packets per syscall, vs per-packet `sendto`. The QUIC transport's `Session.SendDatagram(b)` hands the packet to quic-go, which has its own send loop and its own packet pacer. There is no userspace send queue we could batch into a single `sendto`, and QUIC datagrams don't benefit from `sendmsg_x` — they're wrapped in QUIC packets that quic-go writes through its own `net.PacketConn`. **QUIC `udp.Conn.Flush()` is a no-op.**

2. **Priority queue is a syscall-boundary trick.** The two-lane design keeps handshake/lighthouse frames from being stuck behind ~100 bulk packets in a `sendmsg_x` array. The QUIC transport has no such array — each `SendDatagram` is (conceptually) independent. quic-go's own scheduler may pace-delay datagrams under congestion, but not in a way a priority lane at our layer could fix; the pacing happens below us. **QUIC `udp.Conn` ignores the control/data distinction. `WriteTo` is immediate.** This is safe because:
   - Handshake retries rely on upstream timers in [`handshake_manager.go`](../vendor/github.com/slackhq/nebula/handshake_manager.go), not on `Flush()` cadence — they will retry regardless of send-path queueing behavior.
   - Punchy 1-byte keepalives are likewise scheduled by the connection manager's timer.
   - Data packets arrive at quic-go in the same order `inside.go` issues them; quic-go preserves datagram-send order within a connection (it doesn't reorder).

3. **Batch-reader integration (patch 07) is orthogonal.** `listenIn`'s post-batch `Flush()` call is a hint that _may_ be a no-op. The TUN `ReadBatch` + `consumeInsidePacket` path itself continues to work exactly the same — we're just signalling "I'm done writing for this batch, feel free to syscall now." For QUIC, we ignore the hint.

4. **`ListenOut`-driven flush:** The QUIC implementation's `ListenOut` is a goroutine reading from `Session.ReceiveDatagram` and invoking the Nebula-provided `EncReader` callback. Any `WriteTo` the callback makes (handshake reply, hole-punch ack) is immediate, so there's no queue to drain. No `Flush()` call needed inside the QUIC `ListenOut`.

### Will `Flush()` being a no-op break Nebula's retry / RTO logic?

No. Three specific paths to check:

- **Handshake retry loop** ([`handshake_manager.go:242`](../vendor/github.com/slackhq/nebula/handshake_manager.go)): retries are driven by `HandshakeConfig.tryInterval` and `retries`, not by `Flush()`. If a packet is lost on the wire, the retry fires after `tryInterval`. Immediate-send (our behavior) is strictly better for handshake latency than delayed-send (batched).
- **Connection-manager keepalives** ([`connection_manager.go:520-534`](../vendor/github.com/slackhq/nebula/connection_manager.go)): also timer-driven; `Flush()` cadence is irrelevant.
- **Nebula's inner test/closetunnel traffic** (not per-packet RTO): all scheduled on connection-manager ticks. Same conclusion.

### Three `udp.Conn` refs to wire

`InterfaceConfig.Outside` is an `udp.Conn`, but after `NewInterface` there are three references:

| Field | Set in | Used for |
|---|---|---|
| `f.outside` | `InterfaceConfig.Outside` at [`interface.go:~230`](../vendor/github.com/slackhq/nebula/interface.go) | routine-0 ListenOut, handshake replies, punchy |
| `f.writers[0..N-1]` | `main.go:262` (set from `udpConns` array) | per-routine data sends |
| `f.handshakeManager.outside` | `NewHandshakeManager` arg at main.go:219 | handshake retries |

With `routines = 1` (which the spike enforces via `SupportsMultipleReaders() bool { return false }`), all three resolve to the same `*QuicConn` instance. The spike does not need to handle the N>1 case.

### Concrete contract for `internal/quictransport/nebulaconn.go`

```
Rebind()                 → delegate to Session.Reconnect(context.Background()) on client;
                           on server (accept-side) this is a no-op — server has no
                           session identity to rebind.
LocalAddr()              → return the quic.Transport's local addr (stable across reconnects
                           because Session shares one Transport).
ListenOut(r EncReader)   → goroutine-loop:
                             conn, gen := session.Conn()
                             for {
                               b, err := conn.ReceiveDatagram(ctx)
                               if err != nil {
                                 wait for generation bump, then refresh conn
                                 continue
                               }
                               r(serverAddr, b)
                             }
WriteTo(b, _)            → session.SendDatagram(b)  (addr ignored; session is single-peer)
Flush()                  → nil
ReloadConfig(*config.C)  → nil (not meaningful for QUIC session)
SupportsMultipleReaders  → false
Close()                  → session.Close()
```

Notes:

- **The `addr` argument to `WriteTo` is ignored.** Nebula's world-model has many peers behind one UDP socket; the QUIC-session world-model has exactly one peer per session. For the spike this is fine — each Nebula instance sits over one QUIC session to its one peer. Note that Nebula's hostmap is keyed by overlay-IP, not UDP-addr, but `hostinfo.SetRemote` may overwrite the stored UDP addr if the synthesized one differs from prior; for single-peer spike this is benign, for production multi-peer it's where the `addr`-ignored-by-WriteTo design starts to leak.
- **Multi-peer fan-out (future phase).** One `quic.Transport` can host many `quic.Conn`s. The production mechanism: client-side maps `peer-addr → *Session` (one Session per peer); server-side maps `client connection-ID → *quic.Conn` (one accepted conn per client). The spike skips this: one Session, one peer, one `udp.Conn`.
- **`LocalAddr` returns the Transport's bound address**, which is the outer UDP socket that QUIC packets traverse, not the inner Nebula address. Nebula uses `LocalAddr` mainly for lighthouse registration — for the spike this is fine because the lighthouse isn't on the QUIC path (the spike is agent↔agent only; see Data plane vs control plane below).
- **`Rebind` on the accept-side is a no-op, but that's correct.** The server's `quic.Listener` accepts new client-initiated connections on the shared outer socket; if the client's underlying QUIC connection dies, the server's accepted `*quic.Conn` errors out on the next `ReceiveDatagram`, the wrapping `udp.Conn`'s `ListenOut` goroutine returns, and the Nebula tunnel tears down cleanly. The server then awaits a fresh accept from the (reconnected) client — no rebind needed, no server-side session identity to preserve. Nebula's existing `CloseAllTunnels(true)` logic in [`cmd/agent/nebula.go:165-169`](../cmd/agent/nebula.go) handles force-rehandshake on the client after Rebind, so client→server recovery is symmetric.

### MTU ceiling

`udp.MTU = 9001` in the Nebula code ([`udp/conn.go:9`](../vendor/github.com/slackhq/nebula/udp/conn.go)). A QUIC datagram's max payload is `~= min(1200, Conn.MaxDatagramSize())` on a real path. Phase 2 studies two strategies:

- **2a — Cap Nebula MTU at 1200 in the network_manager YAML.** `WriteTo` that exceeds `MaxDatagramSize()` returns an error; Nebula treats it as a transient send failure. Simple and safe. Throughput loss from smaller inner MTU is the cost.
- **2b — Fragment at the `QuicConn` layer.** 8-byte header `{uint32 frag_id, uint8 seq, uint8 total, uint16 _}`, LRU reassembly cache (128 entries × 500ms TTL). A dropped fragment is a dropped Nebula packet — handled by upper-layer retransmit.

Filled in after measurement.

### Operational details not covered by the contract

- **Data plane vs control plane split.** The spike runs Nebula's **data plane** (agent↔agent peer traffic) over QUIC; the **control plane** (lighthouse registration, peer discovery, hostmap queries) stays on raw UDP as it does today. Reason: peer discovery needs the lighthouse address to be statically known (in YAML config), and the lighthouse server is vanilla upstream Nebula — we're not building a QUIC-aware lighthouse for the spike. Agents discover each other via the UDP lighthouse, then their data-plane tunnels are QUIC. Future production work may move the lighthouse onto QUIC too (and add MASQUE fallback), but that's separable.
- **Double keepalive.** `quicConf.KeepAlivePeriod = 15s` (matches the probe's setting, reused in `nebulaconn.go`) and Nebula's punchy keepalive both fire on the same outer channel — two 1-byte heartbeats per peer per ~15s. Not a correctness issue, just wasteful. In QUIC mode we should set `punchy.punch = false` in the Nebula YAML built by [`internal/mesh/network_manager.go`](../internal/mesh/network_manager.go) (QUIC's keepalive and explicit reconnect handle both concerns punchy exists for). Document the default; leave for Phase 1 implementation.
- **Fewer routines.** `SupportsMultipleReaders() bool { return false }` pins `routines = 1`. Nebula's `f.writers[]` array collapses to a single entry (see [interface.go:227-246](../vendor/github.com/slackhq/nebula/interface.go)), and all three transport refs (`f.outside`, `f.writers[0]`, `f.handshakeManager.outside`) point to the same `*QuicConn`. This is the simplest topology for the spike; it matches hopssh's userspace-mode default today.

---

## Phase 2 — MTU comparison (pending measurement)

Methodology per plan:
- iperf3 TCP single-stream, 30 s, warm tunnel, 5 runs median-averaged.
- TCP retransmits via `netstat -s | grep retrans` diff pre/post.
- CPU % via `top -l 1 -pid <hop-agent-pid>` sampled 1 Hz during the run.
- QUIC pkts sent/recv from quic-go qlog (enable `QUICConfig.Tracer = qlog.DefaultConnectionTracer`).

| Variant | Throughput (Mbps) | TCP retrans | CPU client % | CPU server % | QUIC pkts |
|---|---|---|---|---|---|
| Raw UDP (baseline) | _pending_ | _pending_ | _pending_ | _pending_ | n/a |
| QUIC 2a (capped 1200) | _pending_ | _pending_ | _pending_ | _pending_ | _pending_ |
| QUIC 2b (fragmented) | _pending_ | _pending_ | _pending_ | _pending_ | _pending_ |

Fill in after the Mac mini ↔ laptop runs.

---

## Phase 3 — Reconnect validation (pending)

Test matrix:

| # | Scenario | Expected QUIC session | Expected iperf3 TCP |
|---|---|---|---|
| 1 | 5 min stable, no network change | no reconnect | no retrans spikes |
| 2 | Mid-flow WiFi → WiFi SSID switch | 1 reconnect, <5 s window | survives (spike in latency, no drop) |
| 3 | `ifconfig en0 down` 15 s → `up` | 1 reconnect after `up` | survives |
| 4 | `ifconfig en0 down` 65 s → `up` | 1 reconnect after `up` | drops (TCP timeout > 60s expected); fresh TCP succeeds after |

Test #2 mechanics: script the SSID switch with `networksetup -setairportnetwork en0 <SSID> <pwd>` — cleanest way to trigger a route change with a fresh public IP without human timing error. The iperf3 TCP connection in all four tests lives **inside** the Nebula tunnel (endpoints are the overlay IPs, e.g. `10.42.1.x`); the outer QUIC session is what reconnects under the covers. macOS's kernel TCP keepalive is 2h by default — way longer than any reconnect window we'd hit — so TCP inside the tunnel sees a reconnect as a latency spike, not a drop. Exception: test #4's 65s outage exceeds macOS's default TCP RTO give-up (~30-60s), so that iperf3 TCP does drop, but the Nebula tunnel identity (same overlay IP, same Nebula handshake) is preserved — a fresh iperf3 connects immediately post-recovery.

Fill in with observed reconnect windows + TCP retransmit counts.

---

## References

- `udp.Conn` interface: [`vendor/github.com/slackhq/nebula/udp/conn.go`](../vendor/github.com/slackhq/nebula/udp/conn.go)
- Session + reconnect: [`internal/quictransport/session.go`](../internal/quictransport/session.go)
- Migration probe (reference for network-change detection): [`internal/quictransport/probe.go`](../internal/quictransport/probe.go)
- Migration evidence from earlier investigation: `spike/migration-evidence/`
- CLAUDE.md's QUIC Connection Migration Discovery Log entry: [`CLAUDE.md`](../CLAUDE.md) §Discovery Log
