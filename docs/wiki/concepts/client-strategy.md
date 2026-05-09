---
type: concept
title: Client app strategy — unified Tauri 2 delivery across 5 platforms
status: current
last_compiled: 2026-05-07
sources:
  - clients/desktop/src-tauri/src/lib.rs
  - clients/desktop/src/App.svelte
  - cmd/agent/main.go
  - frontend/src/lib
---

# Client app strategy — unified Tauri 2 delivery across 5 platforms

## TL;DR

One Tauri 2 + Svelte 5 UI codebase across iOS, Android, macOS, Windows, Linux. Desktop spawns the existing `hop-agent` binary as a sidecar; mobile uses native VPN provider extensions (NEPacketTunnelProvider on iOS, VpnService on Android) linking a gomobile-compiled shared Go core. Web terminal works in the WebView on every platform.

**Status (2026-05-09):** macOS shipped (Phase V→DD); Windows and Linux desktop blocked only on Tauri-shell + packaging work (agent-side already complete); the `internal/client/` shared-substrate landed in Phase NN — iOS and Android are now substrate-ready. Remaining mobile work is the gomobile binding (NN+1) and the platform-specific VPN-extension scaffolding (NN+2 iOS, NN+3 Android).

## Status snapshot

| Platform | Architecture | Substrate | UI shell | Status | Shipped at |
|---|---|---|---|---|---|
| **macOS desktop** | Tauri + sidecar `hop-agent` | `cmd/agent/` (post-Phase-NN: thin shell that imports `internal/client/`) | Tauri 2 + Svelte 5 | Production | v0.10.85 → v0.10.96 |
| **Windows desktop** | Tauri + sidecar + WinTun + WebView2 | Same `cmd/agent/` thin shell (WinTun, SCM, NRPT all shipped via `internal/client/`) | Tauri 2 + Svelte 5 | Plan ready | — |
| **Linux desktop** | Tauri + sidecar + AppImage / .deb / .rpm | Same `cmd/agent/` thin shell (systemd, dnsproxy shipped via `internal/client/`) | Tauri 2 + Svelte 5 | Plan ready | — |
| **iOS** | Tauri UI + NEPacketTunnelProvider + gomobile xcframework | ✅ `internal/client/` (Phase NN); ❌ gomobile binding (NN+1) | Tauri 2 + Svelte 5 | Substrate ready | — |
| **Android** | Tauri UI + VpnService + gomobile aar | ✅ `internal/client/` (Phase NN); ❌ gomobile binding (NN+1) | Tauri 2 + Svelte 5 | Substrate ready | — |

**The `internal/client/` substrate landed in Phase NN (2026-05-09).** ~22k LOC + 89 files extracted from `cmd/agent/` (which was `package main`); see [[../phases/phase-nn]] for the extraction post-mortem. The package exposes a gomobile + Rust-FFI compatible public API (`Client`, `Config`, `EnrollOptions`, `Snapshot`, `EnrollmentSummary`, `PeerInfo`, `Event`, `EventCallback`, `SubscriptionID`, `InstanceHTTPHook`) with constraint tripwires in `internal/client/api_constraints_test.go`.

## Per-platform decisions (ADRs)

- [[../decisions/client-macos-architecture]] — Sidecar Tauri + userspace Nebula default + system-mode upgrade. **Shipped v0.10.96.**
- [[../decisions/client-windows-linux-architecture]] — Sidecar Tauri delta on top of macOS for Windows + Linux desktop. Plan ready.
- [[../decisions/client-ios-architecture]] — Tauri main app + Network Extension target + gomobile xcframework. Substrate built (Phase NN, v0.11.5); gomobile binding (NN+1) + iOS scaffolding (NN+2) are the remaining open work.
- [[../decisions/client-android-architecture]] — Tauri + VpnService foreground + gomobile aar. Substrate built (Phase NN, v0.11.5); gomobile binding (NN+1) + Android scaffolding (NN+3) are the remaining open work.

## Context

hopssh today ships only a Go CLI agent (`hop-agent`) and a Svelte 5 web dashboard embedded in the control plane. Roadmap items #22 (desktop tray app) and #23 (mobile apps) flag both as table-stakes — every competitor (Defined Networking, Tailscale, ZeroTier, NetBird) ships native iOS, Android, and desktop tray apps, and "no mobile apps" is one of the largest remaining gaps in the [competitive analysis](/Users/tenevi/Projects/Github.Trustos/hopssh/docs/competitive-analysis.md).

The user's goal is to ship clients on **all 5 platforms** with the absolute minimum number of UI codebases, full feature parity (including the differentiating web terminal), and proper background VPN behavior on mobile (NEPacketTunnelProvider on iOS, VpnService on Android).

**Decision: One UI codebase (Tauri 2 + the existing Svelte 5 frontend) across all 5 platforms, with platform-specific native VPN provider extensions binding to a shared Go core (`mobilehop`) compiled via gomobile.**

This is a deliberate bet on Tauri 2 mobile (released Oct 2024, no production VPN app shipped on it yet as of April 2026) for the upside of literally a single UI codebase. The data plane risk is contained because the Nebula tunnel itself runs in native Swift/Kotlin extensions calling battle-tested Go code — the same Nebula library DefinedNet's `mobile_nebula` ships in the App Store today.

---

## Architecture at a glance

```
                ┌──────────────────────────────────────────────┐
                │         Svelte 5 UI (existing, reused)        │
                │   (web dashboard + new connect/status views)  │
                └──────────────────────────────────────────────┘
                                       │
                ┌──────────────────────────────────────────────┐
                │              Tauri 2 shell (Rust)             │
                │     One project, 5 platform build targets     │
                └─┬──────────────────┬────────────────────────┬─┘
                  │                  │                        │
        ┌─────────┴───────┐  ┌───────┴────────┐  ┌────────────┴───────────┐
        │ Desktop sidecar │  │ iOS NE plugin  │  │ Android VpnService     │
        │ (mac/win/linux) │  │ (Swift + .xcfw)│  │ plugin (Kotlin + .aar) │
        │                 │  │                │  │                        │
        │ spawns existing │  │ NEPacketTunnel │  │ android.net.VpnService │
        │ hop-agent bin   │  │ Provider       │  │ foreground service     │
        │ as sidecar +    │  │       │        │  │           │            │
        │ talks to local  │  │       └─► MobileHop ◄──────────┘           │
        │ HTTP API        │  │     (gomobile binding of Go Nebula core)   │
        └────────┬────────┘  └────────────────────────────────────────────┘
                 │
        ┌────────┴─────────┐
        │  hop-agent       │
        │  (existing Go    │
        │   binary, reused │
        │   verbatim)      │
        └──────────────────┘
```

The "real" code reuse: **the Go core runs unchanged on every platform**. Desktop runs the existing `hop-agent` binary. Mobile runs the same Nebula client logic compiled via gomobile and called from a thin native VPN extension.

---

## Project structure

Paths under `/Users/tenevi/Projects/Github.Trustos/hopssh/`:

```
internal/
  client/                          BUILT (Phase NN, 2026-05-09) — 89 files extracted from cmd/agent/{client,enroll,nebula,renew,…}.go
    api.go                         FFI-clean public surface: Client, Config, EnrollOptions, Snapshot, etc.
    api_constraints_test.go        gomobile-compat tripwires (no chan/func/interface{} fields)
    client.go                      Client.connect / Client.startInstance — lifted from runServe's connectFn
    enroll.go                      device flow + token + bundle enrollment
    nebula.go                      Nebula start/stop/rebind, watchNetworkChanges
    renew.go                       cert renewal + heartbeat loops
    instance.go, keepalive.go, watcher_watchdog.go, peerstate.go, peer_cache.go, …
    local_api.go                   loopback HTTP API (also lives here post-Phase-NN — desktop sidecar consumes via StartLocalAPI)

clients/
  mobile-go/                       NEW (Phase NN+1) — gomobile binding module
    mobilehop/
      mobilehop.go                 exported API: NewClient(tunFd, configJSON), Start, Stop, Rebind, Status, Peers
      control.go                   lifecycle + GC tuning (debug.SetGCPercent(20))
    go.mod                         separate module, replace-directive to ../../
    Makefile                       targets: ios-xcframework, android-aar, all
    README.md                      build instructions

  desktop-mobile/                  NEW — single Tauri 2 project, 5 platform targets
    package.json                   svelte/vite/tauri-cli
    src-tauri/
      Cargo.toml
      tauri.conf.json              externalBin: bundles hop-agent per-triple (desktop only)
      tauri.ios.conf.json          iOS-specific config (Network Extension target)
      tauri.android.conf.json      Android-specific config (VpnService permissions)
      src/
        main.rs                    Tauri app entry, command registration
        sidecar.rs                 desktop: spawn + supervise hop-agent
        ipc.rs                     mobile: bridge UI commands to native plugin
      gen/
        apple/
          project.yml              xcodegen overrides: adds hopssh-tunnel NE target
          hopssh-tunnel/           Swift NEPacketTunnelProvider source
            PacketTunnelProvider.swift
            Info.plist
        android/
          app/src/main/java/com/hopssh/
            HopsshVpnService.kt   VpnService implementation
            MobileHopBridge.kt    JNI glue to mobilehop.aar
      plugins/
        hopssh-tunnel/             Tauri plugin exposing native VPN APIs to JS
          ios/
            Sources/Tunnel.swift  NETunnelProviderManager wrapper
          android/
            src/main/Tunnel.kt    VpnService.prepare() + control APIs
          src/lib.rs              Tauri plugin command definitions
    src/                           Svelte 5 UI
      lib/
        api/                       wraps existing frontend API client
        platform/                  conditional: web → http API, tauri → invoke()
        screens/
          Connect.svelte           connect/disconnect toggle, status
          NetworkPicker.svelte     enroll into a network (device flow QR/code)
          PeerList.svelte          P2P vs relay badges
          Terminal.svelte          xterm.js wrapper (reuses existing dashboard component)
        shared/                    imported from ../../../frontend/src/lib (symlink or workspace)
      App.svelte
      main.ts
```

What gets reused vs newly written:

| Component | Status | Notes |
|---|---|---|
| Nebula data plane (vendored library + patches/) | **Reuse 100%** | Same Go module, same patch (`patches/nebula-1031-graceful-shutdown.patch`) applied to gomobile builds |
| Client logic: enroll, join, renew, heartbeat, watchNetworkChanges | **Refactor + reuse** | Extract from `cmd/agent/{client,enroll,nebula,renew}.go` into `internal/client/` so both `cmd/agent` and `clients/mobile-go/mobilehop/` import it |
| `cmd/agent/dns_*.go`, `dnsproxy_windows.go`, `wintun/`, `service_*.go` | **Reuse on desktop** via sidecar | Desktop Tauri shell ships the hop-agent binary unchanged; all platform integration code keeps working |
| Svelte 5 frontend + shadcn-svelte components | **Reuse** | Existing components (already mobile-responsive at 768px) serve all 5 platforms. Add a small `platform/` shim that switches API target (web → control plane HTTPS, Tauri → local sidecar / native bridge) |
| xterm.js web terminal | **Reuse** | Works in the Tauri WebView the same as in a browser; the mesh tunnel routes the WebSocket through the native VPN extension |
| Native iOS NEPacketTunnelProvider | **NEW** | ~600-1000 LOC Swift; closely models [DefinedNet/mobile_nebula](https://github.com/DefinedNet/mobile_nebula) |
| Native Android VpnService | **NEW** | ~600-1000 LOC Kotlin; foreground service with persistent notification |
| gomobile binding (`mobilehop`) | **NEW** | ~400-800 LOC Go; exports a small surface (NewClient/Start/Stop/Rebind/Status/Peers) that wraps `internal/client/` |
| Tauri shell + per-platform plugin | **NEW** | ~1000-2000 LOC Rust + Swift + Kotlin glue |

---

## Per-platform integration details

### Desktop (macOS, Windows, Linux)

- Tauri ships a per-target-triple `hop-agent` binary as a Tauri [externalBin sidecar](https://v2.tauri.app/develop/sidecar/).
- First launch (with elevation): the Tauri shell installs `hop-agent` as a system service via the existing `cmd/agent/service_windows.go` (Windows SCM), `cmd/agent/service.go` (launchd / systemd) installers — already implemented.
- The Svelte UI calls a thin Tauri Rust command layer that proxies to `http://127.0.0.1:<agent-local-port>` (a small new local HTTP API on the agent — see "Open question 1" below).
- DNS, TUN, network change detection, sleep/wake recovery, certificate renewal: all already handled by `hop-agent` — zero new platform-specific desktop code required.
- System tray: Tauri 2 has first-class tray support; reuse the existing dashboard's `Connect.svelte` + `PeerList.svelte` views.

### iOS

- Tauri 2 main app target hosts the WebView running the Svelte UI.
- A separate iOS app extension target (`hopssh-tunnel`) implements `NEPacketTunnelProvider`. Added via xcodegen `project.yml` override (workaround for [tauri#10074](https://github.com/tauri-apps/tauri/issues/10074); pattern proven in [tauri#14332](https://github.com/tauri-apps/tauri/issues/14332)).
- The extension links `MobileHop.xcframework` (built from `clients/mobile-go/`) and calls `MobileHop.NewClient(tunFd:, configJSON:)` with the TUN file descriptor handed in by `NEPacketTunnelProvider.startTunnel()`.
- `debug.SetGCPercent(20)` in the gomobile shim keeps the extension comfortably under the 50 MiB iOS 15+ extension memory cap (raised from the original 15 MiB; verified via [Apple Developer Forums thread](https://developer.apple.com/forums/thread/73148)).
- Cert + key + agent token storage: shared App Group keychain (so the extension and the Tauri main app see the same credentials).
- Web terminal: WebSocket from xterm.js routes through the Tauri WebView → over the iOS Network Extension's tunnel → to the control plane's `/api/networks/{id}/events` and shell endpoints, exactly like the browser path.
- Auto-reconnect: configured via `NEOnDemandRule` (default on; settings-togglable). Mirrors what users expect from every other VPN app.

### Android

- Tauri 2 main app target hosts the Svelte UI.
- `HopsshVpnService` extends `android.net.VpnService`; runs as a foreground service with a persistent notification (Android 8+ requirement).
- Loads `mobilehop.aar` and calls into the same Go binding as iOS via JNI.
- VPN profile installed via standard `VpnService.prepare()` → user consent dialog. Always-on enabled via the standard "Always-on VPN" toggle in Android system settings (the app surfaces a deep link to it).
- Cert storage: Android Keystore.

### Shared (all platforms)

- Svelte UI is written once. A `platform/` module branches API access:
  - In a browser tab against the live control plane: directly call HTTPS endpoints (existing behavior).
  - In Tauri desktop: call `invoke("agent_request", ...)` which proxies to the local sidecar.
  - In Tauri mobile: call `invoke("tunnel_command", ...)` which routes through the native plugin to the VPN extension.
- All UI strings, layouts, theming, and components: identical across platforms. Mobile-friendly because the dashboard is already responsive (existing 768px breakpoint via `frontend/src/lib/hooks/is-mobile.svelte.ts`).

---

## Critical files to read / reuse

When implementing:

- [cmd/agent/client.go](Projects/Github.Trustos/hopssh/cmd/agent/client.go) — laptop/phone "client join" flow; the model for `mobilehop.NewClient`.
- `cmd/agent/nebula.go` — `startNebula`, `watchNetworkChanges` (5s poll for sleep/wake / interface changes, calls `RebindUDPServer()` + `CloseAllTunnels(true)`). Will move to the new shared client package's nebula module (planned, not yet on disk).
- [cmd/agent/enroll.go](Projects/Github.Trustos/hopssh/cmd/agent/enroll.go) — 4 enrollment modes (device flow, token-stdin, direct token, bundle). Mobile uses device flow; desktop will support all four.
- [cmd/agent/renew.go](Projects/Github.Trustos/hopssh/cmd/agent/renew.go) — 12-hour cert renewal + 60s heartbeat loops. Must run inside the Network Extension / VpnService on mobile.
- [cmd/agent/service_windows.go](Projects/Github.Trustos/hopssh/cmd/agent/service_windows.go), [cmd/agent/service.go](Projects/Github.Trustos/hopssh/cmd/agent/service.go) — desktop service install (reused as-is via sidecar).
- [patches/nebula-1031-graceful-shutdown.patch](Projects/Github.Trustos/hopssh/patches/nebula-1031-graceful-shutdown.patch) — vendor patch; the `Makefile` for `clients/mobile-go/` must apply it before gomobile builds.
- [frontend/src/lib/](Projects/Github.Trustos/hopssh/frontend/src/lib/) — existing Svelte components, API client, terminal wrapper. Imported into the new Tauri project as a workspace dependency or symlink.
- [swagger/swagger.yaml](Projects/Github.Trustos/hopssh/swagger/swagger.yaml) — server API contract; reference for the API client shim.

References (external):

- [DefinedNet/mobile_nebula](https://github.com/DefinedNet/mobile_nebula) — MIT-licensed reference for the gomobile + iOS NE + Android VpnService stack. We use the same Go binding pattern (different UI framework).
- [tauri-apps/tauri PR #10431](https://github.com/tauri-apps/tauri/pull/10431) — fix that unblocked iOS extension targets in Tauri.
- [Apple NEPacketTunnelProvider docs](https://developer.apple.com/documentation/networkextension/nepackettunnelprovider).

---

## Implementation sequencing

Roughly 12-16 weeks elapsed for v1 across all 5 platforms with focused work.

1. **Week 0-1 — Refactor `cmd/agent/{client,enroll,nebula,renew}.go` → `internal/client/`.** Pure refactor, no behavior change. Verify by running existing agent tests + a manual `hop-agent client join`. This unblocks both desktop sidecar (existing agent imports the new package) and mobile gomobile shim.

2. **Week 1-3 — Build `clients/mobile-go/mobilehop/`.** Define the small exported API surface (`NewClient`, `Start`, `Stop`, `Rebind`, `Status`, `Peers`). Add a Makefile that vendors + patches Nebula and runs `gomobile bind` for both iOS (xcframework) and Android (aar). Validate with a tiny Swift CLI test harness (no UI yet) that imports the framework and starts a Nebula session against a dev control plane.

3. **Week 2-5 (parallel with #2) — Tauri 2 desktop scaffold.** New `clients/desktop-mobile/` project. Pull in the existing Svelte 5 frontend as a workspace dependency. Wire up the sidecar: bundled `hop-agent` per target triple (mac arm64, mac amd64, win amd64, win arm64, linux amd64, linux arm64), local HTTP API on `127.0.0.1`, system tray, basic Connect/Disconnect/Status views. Ship dogfood builds for macOS first (the team's primary platform), then Windows and Linux.

4. **Week 4-9 — iOS app + Network Extension.** Tauri iOS target builds; add the `hopssh-tunnel` NE extension via xcodegen `project.yml` override. Implement `PacketTunnelProvider.swift` linking the xcframework. Implement Tauri plugin (`plugins/hopssh-tunnel/ios/`) exposing `NETunnelProviderManager` operations to JS. Wire UI: device-flow enrollment, connect/disconnect, peer list, web terminal (xterm.js inside the Tauri WebView).

5. **Week 4-9 (parallel with #4) — Android app + VpnService.** Tauri Android target builds; implement `HopsshVpnService.kt` and the JNI bridge to `mobilehop.aar`. Implement the Android side of the Tauri tunnel plugin. Same UI as iOS.

6. **Week 9-14 — Beta + store submission.** TestFlight (iOS) + Play Internal Testing (Android) for ~2-4 weeks of real-device testing. Submit to App Store + Play Store. Network Extension entitlement (separate Apple review beyond App Store review) and Google Play VPN policy review can each take 1-2 weeks; budget elapsed time for both.

---

## Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Tauri 2 mobile is unproven for VPN — no production VPN ships on it as of April 2026 | High | High | The VPN code lives in native Swift/Kotlin extensions, not in Tauri. Tauri only hosts the UI WebView. If Tauri mobile turns out to be unworkable, the `mobilehop` binding + extension code stays valid; only the UI layer would need to be rebuilt in SwiftUI/Compose. Cost of wrong bet is bounded to ~3-4 weeks of UI work. |
| Adding iOS Network Extension target to Tauri requires xcodegen override hack | Medium | Medium | Pattern documented in [tauri#14332](https://github.com/tauri-apps/tauri/issues/14332) and proven workable. Allocate a dedicated 3-5 day spike in week 4 to validate end-to-end before committing the rest of the iOS schedule. |
| iOS Network Extension memory cap (50 MiB iOS 15+) | Medium | Medium | Apply `debug.SetGCPercent(20)` in `mobilehop`'s init (DefinedNet's pattern). Nebula's hot path is already low-allocation per the discovery log. Run the extension under Xcode Instruments early (week 5) to confirm memory behavior under realistic load. |
| App Store / Play VPN policy review delays | Medium | Medium | Submit to TestFlight Internal Testing in week 9 (well before requesting public release); Network Extension entitlement request in week 6. These reviews run async — start them as early as the binaries can prove themselves on real devices. |
| Web terminal performance inside Tauri WebView on mobile | Low | Low | xterm.js works in any WebKit/WebView. Already the dashboard's terminal works in mobile Safari (the responsive UI uses it). The added WebSocket through-the-VPN hop is no different from the existing browser → control plane → Nebula → agent path. |
| `gomobile` build pipeline complexity (must apply the Nebula patch on every CI build) | Low | Low | The existing `Makefile` already encapsulates `make patch-vendor`. The mobile Makefile reuses the same helper. CI runs on macOS for the iOS xcframework target. |
| Refactor of `cmd/agent/*` into `internal/client/` introduces regressions | Low | Medium | Pure code-motion refactor; existing agent tests cover the behavior. Land in a single PR, verify all platforms with `make build` + a manual `hop-agent client join` smoke test before starting downstream work. |

---

## Verification

End-to-end test plan once each platform v1 is built:

**Desktop (per OS):**
- Install Tauri app fresh on a clean VM (or a fresh user account) on macOS / Windows / Linux.
- Start the app → grants admin / sudo for first-time service install → confirm `hop-agent` runs as a system service (launchd / SCM / systemd).
- Run device-flow enrollment from the UI; approve in browser; verify a node appears in the dashboard.
- Verify mesh connectivity: `ping`, `curl`, and SSH to a peer via mesh hostname (e.g. `host.zero`).
- Open the web terminal from the Tauri UI; confirm xterm.js streams correctly.
- Suspend / resume the machine; verify the agent re-establishes the mesh within ~10 seconds (existing `watchNetworkChanges` behavior).
- Stop the app; verify the system service keeps the mesh up; restart the app and confirm UI reflects current state.

**iOS:**
- Install via TestFlight on a real device (simulator does not support NEPacketTunnelProvider).
- Enroll via device flow; verify VPN profile installation prompt; approve.
- Confirm tunnel established (status icon, system VPN indicator visible).
- Switch WiFi → cellular → WiFi; verify reconnection (uses the existing `RebindUDPServer` + `CloseAllTunnels(true)` recovery path inside `mobilehop`).
- Lock the device for 5+ minutes; unlock; verify the mesh is still up (if always-on is enabled) or reconnects within ~3 seconds.
- Open the web terminal; SSH to a peer; verify keystrokes flow.
- Check Xcode Instruments: extension memory under 35 MiB sustained.

**Android:**
- Install via Play Internal Testing on a real device.
- Same flows as iOS: enroll, connect, network switch, lock/unlock, terminal.
- Verify foreground service notification visible while connected.
- Test always-on VPN toggle in Android system settings.

**Cross-platform regression:**
- Existing `hop-agent` CLI continues to work after the `internal/client/` refactor — run the existing test suite (`make test`) plus a manual `hop-agent client join` against the dev control plane.
- `make dev-deploy` continues to push the Mac mini + laptop binaries unchanged.

---

## Open questions to resolve during week 0-1

These don't change the plan shape but need answers before relevant tasks start:

1. **Local sidecar API on hop-agent**: today the agent only exposes endpoints over the mesh (port 8089 via Nebula). Desktop UI needs a local-only HTTP API on `127.0.0.1:<port>` that exposes status, peer list, and connect/disconnect. Either (a) add a small new local server on `hop-agent serve`, or (b) talk over a local Unix socket / Windows named pipe. Recommend (a) for cross-platform simplicity. ~100-200 LOC addition to `cmd/agent/`.
2. **Self-hosted endpoint UI**: the mobile + desktop apps must support custom control-plane URLs (`--endpoint`) on the enrollment screen so self-hosters get first-class support. Confirm the field is on the first-run flow.
3. **Always-on default on mobile**: recommend default ON, settings-togglable. Matches Tailscale / WireGuard / DefinedNet competitor behavior.
4. **Mobile app icons + branding assets**: need source files (1024×1024 master) — likely worth commissioning early if not already done.
5. **Distribution accounts**: Apple Developer Program ($99/yr) and Google Play Console ($25 one-time) are prerequisites. Confirm these exist or budget for them in week 1.
