---
type: concept
title: Sleep/wake architecture (macOS focus)
status: current
last_compiled: 2026-05-03
sources:
  - docs/sleep-wake-plan.md
  - cmd/agent/renew.go (Phase S 60s ticker)
  - cmd/agent/nebula.go (watchNetworkChanges, RebindUDPServer)
  - cmd/agent/keepalive.go (Phase P2 watchdog)
  - CLAUDE.md macOS Platform discovery log
---

# Sleep/wake architecture

## TL;DR

Three layers cover the sleep/wake transition on macOS as of 2026-05-03:

1. **Cert-renewal survival** — Phase S 60s wall-clock ticker (NOT `time.After`). Survives Darwin's `mach_absolute_time` freeze during Clamshell Sleep / DarkWake.
2. **Tunnel rebind** — `watchNetworkChanges` (5s polling) detects tick-gap >15s OR address fingerprint change → `RebindUDPServer` + `CloseAllTunnels(true)`. Forces immediate handshake on new path.
3. **Stuck-state recovery** — Phase P2 keepalive watchdog: if 3 consecutive 90s probe cycles fail, auto-restart the mesh instance + dump goroutine pprof to `<configDir>/<network>/stuck-state-<ts>.txt`.

What the architecture does NOT cover (by design): application-layer reconnection. macOS Screen Sharing.app does not auto-resume RFB after socket drop; that's not a hopssh bug.

## What survives sleep

| Layer | Mechanism | File |
|---|---|---|
| Cert renewal | 60s ticker + wall-clock NotAfter check | `cmd/agent/renew.go::runCertRenewal` |
| UDP tunnel | `watchNetworkChanges` rebind on tick-gap or addr-change | `cmd/agent/nebula.go` |
| Mesh DNS (kernel mode, Linux) | systemd-resolved drop-in fallback | `cmd/agent/dns_linux.go` |
| Mesh DNS (userspace, all OS) | gvisor netstack-internal resolver | `internal/mesh/` |
| Heartbeat HTTPS | `DisableKeepAlives:true` on every client (v0.10.31) | `cmd/agent/renew.go` |
| Per-network watchdog | keepalive cycles + restartFn | `cmd/agent/keepalive.go` |

## What does NOT survive sleep (limitations)

### Screen-Sharing.app doesn't auto-reconnect

Verified 2026-05-02 on [[../entities/mbp]] post-Clamshell Sleep at 19:36-19:42:
- Mesh tunnel re-handshaked within 30s of wake (logged: `wake/network-change triggered out-of-cycle heartbeat` → `Tunnel status: dead [10.42.1.7]` → re-handshake completed).
- **Screen Sharing.app showed black frame indefinitely** because RFB protocol does not initiate a new session after its TCP socket dies.
- Reproducible without VPN on direct LAN — Apple's built-in Screen Sharing simply lacks transparent reconnect for RFB. Citadel, Jump Desktop, RealVNC, RDP all handle it.
- **Workaround:** ⌘W on the Screen Sharing window then reconnect via `vnc://10.42.1.7` from Finder. Mesh is already up, sub-second reconnect.
- **Why we won't fix in hopssh:** would require a reconnecting TCP proxy in front of every long-lived app socket. Wrong layer.

### Phase J: bundled `hop-agent` binary lags behind `.app` updates

The system-mode LaunchDaemon at `/usr/local/bin/hop-agent` is NOT refreshed automatically when the `.app` updates. Manual refresh:
```
sudo cp /Applications/hopssh.app/Contents/Resources/binaries/hop-agent /usr/local/bin/hop-agent
sudo launchctl kickstart -k system/com.hopssh.agent
```
Phase J (auto-refresh on `.app` self-update) is deferred per CLAUDE.md.

## Empirical evidence

- **Phase S validation 2026-05-02** ([[../phases/phase-s]]): real Clamshell Sleep on [[../entities/mbp]] with `pmset -g log` showing Sleep ↔ DarkWake cycles. Agent log confirms `[renew home] cert OK ... next poll in 1m0s` every 60s through entire sleep window.
- **Tunnel survival baseline 2026-04-16**: `nc` keep-alive over Nebula tunnel survived 30s `ifconfig en0 down` with **0 application-data bytes lost**. Evidence at `spike/nebula-baseline-evidence/`.

## Anti-patterns to avoid (rules learned)

- **Don't use `time.After(longSleep)` for any timer >5 min on macOS.** Anchored to monotonic clock; freezes during deep sleep. Use ticker + wall-clock invariant. (Phase S rule.)
- **Don't trust `/dev/console` USER inside `osascript ... with administrator privileges`** — it's empty/`root`, not the calling user. Resolve console user in the calling Tauri Rust process. (Phase F rule.)
- **Don't share `http.DefaultTransport` across network-change events.** Idle conns in the pool are dead post-wake; reuse hangs for full Timeout. `DisableKeepAlives:true` everywhere or own transport + `CloseIdleConnections()` on rebind.

## Backlinks

- [[../entities/mbp]] — primary test subject
- [[../entities/mac-mini]] — observer
- [[cert-renewal]]
- [[watchdog]]
- [[../phases/phase-s]]

## Long-form

- `docs/sleep-wake-plan.md` — full test plan, failure-mode matrix, S1-S7 remediations, post-mortems
