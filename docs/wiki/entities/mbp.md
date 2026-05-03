---
type: entity
title: yavors-macbook-pro (the MBP)
status: current
last_compiled: 2026-05-03
sources:
  - CLAUDE.md (macOS Platform discovery log)
  - docs/sleep-wake-plan.md
  - SSH probes on 10.42.1.6
---

# yavors-macbook-pro

## TL;DR

Apple Silicon MacBook Pro. Primary sleep/wake / cellular-handoff test subject. Runs hopssh in **system mode** (root LaunchDaemon, kernel utun for both networks). Two enrollments: `home` (10.42.1.6/24) and `work` (10.42.2.2/24). Default home network is via WiFi (Yavor's home LAN, en0 to TP-Link router with public 46.10.240.91 via UPnP/NAT-PMP). Frequently switches to **Yettel BG cellular hotspot** for mobile testing.

## Mesh identity

| Field | home network | work network |
|---|---|---|
| Mesh IP | 10.42.1.6/24 | 10.42.2.2/24 |
| Node ID | `6e5206df-2e8b-4f9e-94e2-d77446131ade` | `fe7c3a5a-d676-4c20-938e-8fe201afe278` |
| DNS suffix | `.home` | `.work` |
| Listen port | 4242 | 4243 |
| TUN | kernel (post Phase J upgrade) | kernel |
| Cert validity | 24h, auto-renewed via Phase S 60s ticker | same |

## Operational notes

- **Sleeps via Clamshell**. `pmset -g log` regularly shows Sleep ↔ DarkWake ↔ Maintenance Sleep cycles every ~15 min through the night. Real wake on lid-open registered as `Wake from Deep Idle [CDNVA]: lid SMC.OutboxNotEmpty RTP.multi-touch/HID Activity`.
- **Carrier identity** (when on hotspot): Yettel BG, public CGNAT IP rotates. Symmetric NAT with random outbound ports (so NAT-PMP doesn't help here; relies on the mini's home-router NAT-PMP-stable public endpoint via [[../concepts/cellular-perf|cellular perf]]).
- **System mode active since v0.10.57** convert flow. LaunchDaemon at `/Library/LaunchDaemons/com.hopssh.agent.plist`, agent at `/usr/local/bin/hop-agent`, configs at `/etc/hop-agent/`, mirror token + port at `~/Library/Application Support/hopssh/system-local-api-{token,port}`.
- **Logs at** `/var/log/hop-agent.log` (rotated by macOS).

## SSH access (mesh-only)

The mini and the MBP share the home network. From the mini: `ssh yavortenev@10.42.1.6`. Used heavily for forensic SSH probes (no public SSH; mesh-only).

## Notable incidents

- **2026-05-02 23:09–23:14** — Clamshell Sleep with multiple DarkWake/Maintenance Sleep cycles. [[../concepts/sleep-wake]] survived correctly per Phase S; [[../phases/phase-s]] verification.
- **2026-05-02 23:30–23:40** — work network temporarily stuck during the v0.10.84 control-plane deploy bounce; [[../concepts/watchdog]] auto-recovered. Forensic dump at `/etc/hop-agent/work/stuck-state-20260502T204038Z.txt`.
- **2026-05-02 19:36-19:42** — wake-from-sleep tunnel re-handshake; mesh recovered <30s but **Screen Sharing.app showed black frame** (application-layer limitation, see [[../concepts/sleep-wake#screen-sharing-app-doesnt-auto-reconnect]]).

## Backlinks

- [[mac-mini]] — primary peer
- [[hopssh-cloud]] — heartbeats target this
- [[../concepts/sleep-wake]]
- [[../phases/phase-s]]
- [[../phases/phase-t]] (this is where dashboard tests run)
