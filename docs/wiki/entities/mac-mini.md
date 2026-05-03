---
type: entity
title: tenevis-mac-mini-2 (the mini)
status: current
last_compiled: 2026-05-03
sources:
  - CLAUDE.md (macOS Platform, Performance discovery logs)
  - SSH probes
---

# tenevis-mac-mini-2

## TL;DR

Always-on macOS mini at home, ethernet to TP-Link router. **Stable home-network anchor**: NAT-PMP-mapped public UDP endpoint `46.10.240.91:4242` is the cellular ↔ home rendezvous point that lets the [[mbp]] reach it directly even from carrier CGNAT. Runs hopssh in system mode (LaunchDaemon, kernel utun).

## Mesh identity

| Field | home network |
|---|---|
| Mesh IP | 10.42.1.7/24 |
| Local LAN | 192.168.23.3 |
| Public via NAT-PMP | 46.10.240.91:4242 |
| TUN | kernel |

## Why this machine matters architecturally

- **Anchor for direct-P2P from cellular [[mbp]].** The home router's NAT-PMP service forwards `external:4242 → internal:4242` so the cellular peer (whose own outbound CGNAT mapping is random + symmetric) can hit a stable target. Phase G (HTTPS-distributed self-endpoints) keeps this in the lighthouse cache even when UDP-to-lighthouse is filtered by the carrier.
- **Stable testbed for [[../concepts/sleep-wake]].** Doesn't sleep, so it's the constant in any sleep/wake test — observes the wake-side recovery from the other end.

## Operational notes

- **Public IP `46.10.240.91`** has been stable for the entire research window (2026-04 → 2026-05). NAT-PMP allocation has rotated occasionally but `:4242` is preferred and usually held.
- **`hop-agent` runs as LaunchDaemon** like the MBP; same paths, same config layout.
- **Screen-share host.** The MBP frequently mounts the mini's screen for development. Subject to [[../concepts/sleep-wake#screen-sharing-app-doesnt-auto-reconnect]].

## Backlinks

- [[mbp]] — primary peer
- [[../concepts/sleep-wake]]
- [[../benchmarks/cellular-baseline]]
