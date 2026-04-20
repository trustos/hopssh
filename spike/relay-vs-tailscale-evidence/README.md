# Relay performance: hopssh vs Tailscale

Empirical answer to: "why does Tailscale screen-sharing over DERP relay feel dramatically better than hopssh over our Nebula lighthouse relay?"

Target workload: **macOS Screen Sharing (RFB over TCP) over a relayed tunnel** between two peers on the same home network in Bulgaria, vs over a US-East Oracle Cloud hopssh relay.

## Hypothesis (pre-test)

From exploration in the plan file, the most likely cause is **geographic RTT**: our single-region relay in US is ~200 ms RTT from Bulgaria; Tailscale auto-selects a nearby DERP (Frankfurt or Warsaw, ~30-50 ms). A naive 150 ms RTT delta on an RFB/VNC session is enough to make screen sharing feel laggy regardless of throughput.

Other candidates: relay CPU capacity, UDP queue overflow under load, per-packet overhead on macOS. These tests aim to disambiguate.

## Topology

- **Peer A**: Mac mini (wired to router). hopssh IP `10.42.x.x`, Tailscale IP `100.x.x.x`.
- **Peer B**: MacBook Pro (WiFi). hopssh IP `10.42.y.y`, Tailscale IP `100.x.x.y`.
- **hopssh relay**: single Oracle Cloud instance (likely US-East or EU; verify via `dig hopssh.com` or dashboard).
- **Tailscale DERP**: auto-selected; verify via `tailscale netcheck`.

Both VPNs active at the same time so tests can alternate without reconfiguration.

## Forcing relay mode

hopssh and Tailscale both default to P2P when available. On a flat home LAN, both will prefer direct P2P, which is NOT what we want to measure.

### Option 1: pfctl firewall rule (macOS, preferred, no code changes)

On both peers, block direct UDP between the peers' physical-interface IPs. Direct handshakes fail → VPN falls back to relay.

Use `scripts/force-relay-on.sh <peer-ip>` to install the rule, `force-relay-off.sh` to remove it. The rule only blocks the peer's physical IP on the VPN's UDP port, so all other traffic is untouched.

After enabling: verify on the hopssh dashboard that the peer shows `relayed=true`, and verify Tailscale shows `via DERP <region>` in `tailscale status`.

### Option 2: natural CGNAT (requires cellular)

Switch one peer to iPhone hotspot (cellular CGNAT blocks direct P2P). Works for both VPNs, but traffic now goes over cellular, which confounds measurements.

### Option 3: (future) `--force-relay` flag in hopssh agent

Not implemented yet. Would require agent-side logic to omit direct endpoints from static_host_map and disable hole punching. Deferred — pfctl option is sufficient for the benchmark.

## Tests

Run in this order. Each produces numbered artifacts in this directory.

### T1 — Raw path RTT (no VPN)

```
./scripts/path-rtt.sh
```

Pings (ICMP) the hopssh relay public IP and the nearest Tailscale DERP for 60 s each. Output: `01-path-rtt.log` with p50/p95/p99. **Ceiling for each VPN: the VPN can never be faster than path RTT × 2.**

### T2 — Clean TCP throughput through each tunnel

With force-relay enabled on both peers:

```
./scripts/iperf-tunnel.sh hopssh <peer-hopssh-ip>
./scripts/iperf-tunnel.sh tailscale <peer-tailscale-ip>
```

Runs `iperf3 -t 30 -P 4` (4 parallel streams, 30 s). Captures throughput Mbps, retransmits, CPU %. Output: `02-iperf-hopssh.log`, `02-iperf-tailscale.log`.

### T3 — TCP RTT distribution under sustained load

With force-relay enabled:

```
./scripts/tcp-rtt-load.sh hopssh <peer-hopssh-ip>
./scripts/tcp-rtt-load.sh tailscale <peer-tailscale-ip>
```

Runs `tools/tcp-rtt-probe` (Go): opens a TCP connection to the peer's port 1 (closed, gets RST) every 50 ms for 3 min, measures `connect()` time. Meanwhile, `iperf3` runs a 10 Mbps background flow (`-b 10M`) to simulate the relay being actively used. Output: `03-tcp-rtt-hopssh.log` + `03-tcp-rtt-tailscale.log` with p50/p95/p99/max distribution.

This is the **screen-sharing-relevant** metric: interactive latency when the tunnel is busy doing real work. Per CLAUDE.md engineering lessons, ICMP ping is the wrong benchmark for RFB experience.

### T4 — (deferred)

A synthetic RFB-replay test was in the original plan but dropped. T5 (real Screen Sharing) captures the same workload reality more directly, and a replay tool adds more variables (timing precision, TCP flow control interaction) without new information. If T1-T3 don't explain the gap, revisit.

### T5 — Real Screen Sharing A/B

The ground-truth test. On Peer B, with force-relay enabled:

1. Open Screen Sharing to Peer A's hopssh IP. Drive a deterministic visual workload (e.g. open a clock animation page; drag a window in circles for 60 s). Screen-record Peer B for the full minute.
2. Disconnect. Open Screen Sharing to Peer A's Tailscale IP. Repeat the same workload. Screen-record.
3. Run `tcpdump -i utunN -w 05-hopssh.pcap` (hopssh utun) and the equivalent for Tailscale during their sessions.

Compare:
- Subjective: does the hopssh recording have visible jank/stutter the Tailscale recording doesn't?
- Objective: `tshark -r 05-hopssh.pcap -z io,stat,1` shows per-second bytes + packet rate over time; extract TCP RTT via `-z rpc,rtt` if VNC uses one TCP stream.

Output: `05-screenshare-hopssh.mp4`, `05-screenshare-tailscale.mp4`, `05-hopssh.pcap`, `05-tailscale.pcap`, `05-comparison.md`.

## Results

Final write-up: `RESULTS.md`. Structure:

| Test | Tailscale | hopssh | Gap | Implication |
|---|---|---|---|---|
| T1 path RTT p50 | `x ms` | `y ms` | `y-x ms` | Geographic gap |
| T2 throughput | `x Mbps` | `y Mbps` | `ratio` | Throughput-bound? |
| T3 TCP RTT p99 | `x ms` | `y ms` | `diff` | Jitter under load |
| T4 RFB replay delay p95 | `x ms` | `y ms` | `diff` | Workload-realistic |
| T5 subjective | `smooth/janky` | `smooth/janky` | — | Ground truth |

Then a 1-paragraph summary per test on what the number implies, and a "where the gap is" section picking one of the four root-cause buckets from the plan file.

## Reproducibility

### Prerequisites

- macOS Sequoia on both peers.
- `iperf3` (`brew install iperf3`) — **server side must be running `iperf3 -s`** before T2/T3.
- `tshark` / Wireshark (`brew install wireshark`) — optional, for richer pcap analysis.
- `go` 1.22+ (to compile the TCP-RTT probe).
- `tcpdump` (built-in) and `pfctl` (built-in) — both need sudo.
- macOS Screen Sharing enabled on the receiving peer (System Settings → General → Sharing → Screen Sharing ON).
- Both VPNs authenticated and up on both peers.

### Ordering

Neither VPN should be touched during a test series — same tunnel version, same config, same physical interface. Household WiFi should not be changed (router restarts, other devices transferring, etc.) during data collection.

### Recommended execution

1. On the **remote peer** (receiver): run `iperf3 -s` (leave it running).
2. On the **local peer** (tester): run `sudo scripts/force-relay-on.sh <remote-physical-ip>`. On the remote peer: run `sudo scripts/force-relay-on.sh <local-physical-ip>`. Wait 15 s for both VPNs to drop to relay. Verify on both dashboards.
3. Run `scripts/run-all.sh <peer-hopssh-ip> <peer-tailscale-ip>` — runs T1-T3 back-to-back.
4. Run T5 manually (see above).
5. On both peers: `sudo scripts/force-relay-off.sh`.
6. Fill in `RESULTS.md`.
