# Results — hopssh vs Tailscale relay

Template. Fill in once benchmarks have run. See `README.md` for methodology.

## Environment

- Date: `____________`
- Peer A (Mac mini): `_____________ Mac mini, _____ network (Ethernet/WiFi), host _____________`
- Peer B (MacBook Pro): `_____________ MBP, _____ network (WiFi), host _____________`
- hopssh version: `_____________` (`hop-agent info | grep version`)
- Tailscale version: `_____________` (`tailscale version`)
- hopssh relay public IP/hostname: `_____________`
- Tailscale preferred DERP region + hostname: `_____________` (`tailscale netcheck`)
- Both peers confirmed relayed? `hopssh: ___ tailscale: ___`

## T1 — Path RTT (raw, no VPN)

| Path | p50 (ms) | p95 (ms) | p99 (ms) | max (ms) |
|---|---|---|---|---|
| Peer → hopssh relay | `___` | `___` | `___` | `___` |
| Peer → Tailscale DERP | `___` | `___` | `___` | `___` |
| **Gap (hopssh − tailscale, p50)** | | | | |

**Implication:** if the p50 gap is large (>50 ms), geographic RTT dominates. The VPN cannot be faster than 2× path RTT each way.

## T2 — TCP throughput through each relay

Upload (client → server):

| VPN | Mbps | Retransmits | Client CPU % | Server CPU % |
|---|---|---|---|---|
| hopssh | `___` | `___` | `___` | `___` |
| tailscale | `___` | `___` | `___` | `___` |

Download (reverse):

| VPN | Mbps | Retransmits | Client CPU % | Server CPU % |
|---|---|---|---|---|
| hopssh | `___` | `___` | `___` | `___` |
| tailscale | `___` | `___` | `___` | `___` |

**Implication:** if hopssh throughput is within 20% of tailscale, throughput is not the user's complaint. If hopssh is CPU-bound, check relay server CPU separately.

## T3 — TCP RTT under load (real interactive latency)

3 min probe @ 50 ms cadence, with 10 Mbps UDP background flow saturating the tunnel.

| VPN | p50 (ms) | p90 (ms) | p95 (ms) | p99 (ms) | max (ms) | timeouts |
|---|---|---|---|---|---|---|
| hopssh | `___` | `___` | `___` | `___` | `___` | `___` |
| tailscale | `___` | `___` | `___` | `___` | `___` | `___` |

**Implication:** this is the screen-sharing-relevant metric. Gap at p95/p99 means hopssh has jitter under load (queueing, drops, or scheduling) that Tailscale handles gracefully. A clean baseline (p50 reasonable, p99 blown out) points at queue management or flow control, not raw RTT.

## T5 — Real macOS Screen Sharing

Session pcap: `05-hopssh.pcap`, `05-tailscale.pcap`

### Subjective

| | Window drag smoothness | Mouse trails | Animation cadence | Click latency |
|---|---|---|---|---|
| hopssh | `smooth/janky` | `____ ms` | `____ fps` | `____ ms` |
| tailscale | `smooth/janky` | `____ ms` | `____ fps` | `____ ms` |

### Objective (from pcap)

| VPN | Bytes/s peak | TCP RTT p95 | Retransmit % | Out-of-order % |
|---|---|---|---|---|
| hopssh | `___` | `___` | `___` | `___` |
| tailscale | `___` | `___` | `___` | `___` |

Extraction commands:
```
tshark -r 05-hopssh.pcap -q -z io,stat,1
tshark -r 05-hopssh.pcap -q -z tcp,stat
tshark -r 05-hopssh.pcap -q -z conv,tcp
```

## Where the gap is

Pick ONE bucket (see plan file, Phase 3):

- [ ] **(1) Geographic RTT dominates.** Evidence: T1 gap ≥ 100 ms. Fix: ship regional relay. Est. fix cost: small.
- [ ] **(2) Relay capacity / queuing dominates.** Evidence: T3 p99 blown out while T1 is fine. Fix: profile relay; tune queue. Est. fix cost: medium.
- [ ] **(3) Per-packet overhead on macOS.** Evidence: T2 throughput gap ≥ 30% with low CPU headroom. Fix: additional batch I/O; uncertain. Est. fix cost: high.
- [ ] **(4) Transport-level (UDP vs TCP/HTTPS).** Evidence: T5 shows bursty TCP on hopssh, smooth on tailscale; pattern doesn't match 1-3. Fix: HTTPS/2-style relay. Est. fix cost: very high.
- [ ] **Other / multiple buckets:** explain below.

## Recommendation

One paragraph. What to ship first based on what the numbers show.
