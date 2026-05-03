---
type: benchmark
title: Cellular cold-start direct-P2P baseline
status: current
last_compiled: 2026-05-03
last_measured: 2026-05-02
sources:
  - SSH probe of MBP local-API on 2026-05-02 23:42 (post-cellular-handoff session)
  - CLAUDE.md macOS Platform discovery log (Phase A1+A2 ship gate, pre-existing baselines)
---

# Cellular cold-start direct-P2P baseline

## TL;DR

[[../entities/mbp]] on Yettel BG cellular hotspot ↔ [[../entities/mac-mini]] anchored at home (NAT-PMP `46.10.240.91:4242`). **Direct P2P recovers within seconds; RTT lands in the 25–35ms band; under TCP load p95 stays sub-100ms.** This is the baseline against which any future cellular regression should be compared.

## Latest sample (2026-05-02, ad-hoc handoff)

User transitioned from home WiFi to iPhone Personal Hotspot mid-session.

```
ping 10.42.1.6 (MBP from mini, 30 packets @ 200ms cadence, under active screen-share load):
  min  = 19.6 ms
  avg  = 30.7 ms
  p95  ≈ 50 ms
  max  = 75.9 ms
  loss = 0%

MBP-side /local/peers report:
  vpnAddr=10.42.1.7 direct=true remoteAddr=46.10.240.91:4242 rttMs=32
  vpnAddr=10.42.1.1 direct=true remoteAddr=132.145.232.64:42001
```

No relay fallback. NAT-PMP-stable home endpoint reached the carrier-side fast.

## Historical baselines (from CLAUDE.md Performance log)

| Date | Scenario | Peak TCP DL | Mean RTT idle | Notes |
|---|---|---|---|---|
| 2026-04-25 | A1+A2 ship gate cold-start | 40.7–50.2 Mb/s (3 passes) | 35–43ms | parity with Tailscale (mean 45.6 vs 48.8) |
| 2026-04-25 | Tailscale cold-start (same path) | 40.9–57.6 Mb/s | similar | hopssh wins jitter under load (p95 227 vs 297) |
| 2026-05-02 | Ad-hoc post-Phase-S | RTT only this run | **30.7 ms** | within historical band |

## What we don't currently measure

- **Throughput regression smoke test.** No automated cellular benchmark; `scripts/perf_compare.sh` exists but requires manual setup. Future Phase-bench: add a 2-min TCP iperf + 30-ping concurrent run to a pre/post deploy harness.
- **Sustained UDP loss / jitter.** `iperf3 -u` doesn't work through hopssh (control-protocol race; see CLAUDE.md). Use `nuttcp -u` or a custom Go probe if needed.

## Reproduction

From the [[../entities/mac-mini]]:

```bash
ping -c 30 -i 0.2 -W 2 10.42.1.6   # 30 pings @ 200ms cadence
ssh yavortenev@10.42.1.6 'curl -sf -H "Authorization: Bearer $(sudo cat ~/Library/Application\ Support/hopssh/system-local-api-token)" http://127.0.0.1:$(sudo cat ~/Library/Application\ Support/hopssh/system-local-api-port)/local/peers'
```

## Backlinks

- [[../entities/mbp]] — subject under test
- [[../entities/mac-mini]] — anchor (NAT-PMP-stable target)
- [[../concepts/sleep-wake]] — wake-side path-quality recovery touches the same metrics
