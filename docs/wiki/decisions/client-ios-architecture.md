---
type: decision
title: iOS client architecture — Tauri main app + Network Extension + gomobile xcframework
status: proposed
last_compiled: 2026-05-09
sources:
  - cmd/agent/client.go (model for mobilehop.NewClient)
  - cmd/agent/nebula.go (model for in-extension network-change handling)
  - cmd/agent/renew.go (model for in-extension cert renewal + heartbeat)
  - patches/nebula-1031-graceful-shutdown.patch (must apply to gomobile builds)
---

# iOS client architecture — Tauri main app + Network Extension + gomobile xcframework

## Decision basis

The reasoning that produced this decision came from:

- **Code I read:** `cmd/agent/client.go` (model for the `mobilehop.NewClient` API surface), `cmd/agent/nebula.go` (the network-change recovery path that runs unchanged inside the extension), `cmd/agent/renew.go` (cert renewal + heartbeat that runs in-extension), `patches/nebula-1031-graceful-shutdown.patch` (must apply to gomobile builds).
- **Wiki pages I consulted:** [[../concepts/client-strategy]] (master strategy), [[client-macos-architecture]] (sibling desktop ADR), [[../concepts/watchdog]] (three-watchdog architecture the gomobile core inherits).
- **External sources I fetched:** [DefinedNet/mobile_nebula](https://github.com/DefinedNet/mobile_nebula) (MIT-licensed reference for the gomobile + iOS NE stack), Apple's `NEPacketTunnelProvider` documentation, Tauri issues #14371 / #10074 / #14332 / #9907 / #10631 (iOS-on-Tauri readiness), Apple Developer Forums thread on iOS 15+ NE memory cap.
- **Prior-knowledge claims (with confidence):**
  - [HIGH] iOS NE memory cap is 50 MiB on iOS 15+ (was 15 MiB earlier) — verified against DefinedNet's published `debug.SetGCPercent(20)` workaround.
  - [HIGH] `NEPacketTunnelProvider` runs in a separate OS-managed process — verified against Apple docs.
  - [HIGH] App Group Keychain sharing is the standard pattern for main-app + extension credential exchange — verified against multiple WWDC sessions on app extensions.
  - [MEDIUM] `socket.fileDescriptor` KVC hack on `packetFlow` is the standard tunFd extraction — recalled from DefinedNet `mobile_nebula` source.
  - [MEDIUM] xcodegen `project.yml` override is the durable way to add NE targets to Tauri iOS — verified against tauri#14332 thread but pattern may shift if Tauri ships first-class extension support.
  - [LOW] Network Extension entitlement is "routinely granted to legitimate VPN apps" — recalled from Apple developer-program lore; should be verified by submitting the request.

## Status

**Proposed. Substrate-blocked.** Re-verified 2026-05-09: `internal/client/` directory still does not exist; no `.xcframework` artifacts on disk; no `clients/mobile-go/` tree. iOS work cannot start until the substrate is built.

Required substrate (none of these exist as of 2026-05-09):
- `internal/client/` — shared package extracted from `cmd/agent/{client,enroll,nebula,renew}.go`. The same package will back the Android client and any future mobile/embedded targets.
- `clients/mobile-go/mobilehop/` — gomobile binding compiling the shared package to `MobileHop.xcframework`.
- iOS Network Extension scaffolding inside `clients/desktop/src-tauri/` (PacketTunnelProvider target + entitlements + App Group config).

External prerequisites:
- Apple Developer Program account ($99/yr) — see [`docs/wiki/runbooks/notarization-pipeline.md`](../runbooks/notarization-pipeline.md) for the same enrollment that gates macOS notarization.
- Network Extension entitlement — Apple grants this on a per-team basis after a 1-2 week review separate from App Store review.

See [[../concepts/client-strategy]] for the overall 5-platform strategy and [[client-macos-architecture]] for the desktop peer.

## Context

iOS is the first **mobile** target. Unlike desktop (where `hop-agent` runs as a sidecar process), iOS architecture is fundamentally different: the VPN tunnel runs inside a separate OS-managed Network Extension process, with the Go Nebula library compiled via gomobile into an `.xcframework` and linked directly into the extension. Tauri hosts the UI only; it never runs the tunnel.

**Verified Tauri iOS state (April 2026):**
- [tauri#14371](https://github.com/tauri-apps/tauri/issues/14371) (blank-on-resume) — **fixed** via PR #14523.
- [tauri#10074](https://github.com/tauri-apps/tauri/issues/10074) (simulator extension-build) — **fixed** via PR #10431 (merged Aug 2024).
- [tauri#14332](https://github.com/tauri-apps/tauri/issues/14332) — confirms the xcodegen-override pattern works for iOS Network Extensions.
- [tauri#9907](https://github.com/tauri-apps/tauri/issues/9907) + [#10631](https://github.com/tauri-apps/tauri/issues/10631) (keyboard/viewport) — **open** but CSS-workaroundable; Week-5 spike dedicated to this.

**Decisions (confirmed with user):**
- Minimum iOS version: **15.0** (covers ~99% of active devices, required for 50 MiB NE extension memory cap — below iOS 15 it was 15 MiB, too tight for a Go runtime).
- **Universal app** — same binary runs on iPhone and iPad from v1. Responsive Svelte UI already adapts at 768px.
- Enrollment: **QR code scan** (primary) + **device flow** (fallback).
- Always-on VPN: **default ON** via `NEOnDemandRule`.

## Architecture on iOS

```
┌─────────────────────────────────────────────────────┐
│ hopssh.app (Tauri main app, WKWebView + Svelte UI)   │
│                                                      │
│  ┌─────────────────────────────────────────────┐    │
│  │ Svelte UI                                    │    │
│  │  - Enrollment (QR scan + device flow)        │    │
│  │  - Connect / disconnect toggle               │    │
│  │  - Peer list + mesh IP + DNS                 │    │
│  │  - xterm.js terminal                         │    │
│  └─────────────────────────────────────────────┘    │
│                       │                              │
│  ┌────────────────────┴────────────────────────┐    │
│  │ Tauri plugin "hopssh-tunnel" (Swift + Rust)  │    │
│  │  - NETunnelProviderManager.installProfile    │    │
│  │  - session.startVPNTunnel / stopVPNTunnel    │    │
│  │  - setOnDemandRules (always-on)              │    │
│  │  - observeStatus (status → JS via emit)      │    │
│  └────────────────────┬────────────────────────┘    │
└───────────────────────┼──────────────────────────────┘
                        │ App Group: group.com.hopssh.app
                        │ Keychain access group: <TEAM_ID>.com.hopssh.app
                        ▼
      ┌──────────────────────────────────────────────┐
      │ hopssh-tunnel  (Network Extension target)     │
      │ Separate OS-managed process                   │
      │                                               │
      │  PacketTunnelProvider.swift                   │
      │    startTunnel() →                            │
      │      MobileHop.NewClient(tunFd, cfg)          │
      │      MobileHop.Start()                        │
      │      NWPathMonitor → MobileHop.Rebind()       │
      │                                               │
      │  MobileHop.xcframework                        │
      │    Go gomobile: Nebula + cert renewal +       │
      │    heartbeat + watchNetworkChanges            │
      │    debug.SetGCPercent(20)                     │
      └──────────────────────────────────────────────┘
```

Key properties:
- **OS keeps the extension alive** independent of the main app. User kills the Tauri app → VPN stays up. Phone reboots → on-demand rules restart it.
- **Packet forwarding, keepalives, rekeys, heartbeat, and network-change detection all live in the extension** — the exact same Go logic as `cmd/agent/nebula.go` and `cmd/agent/renew.go`, just compiled via gomobile.
- **Main-app background work is minimal**: silent-push-driven reconnect + status polling. Handled by the Tauri push plugin + the Swift plugin.
- **Memory**: 50 MiB cap applies only to the extension process. `debug.SetGCPercent(20)` in `mobilehop` init (matches DefinedNet `mobile_nebula` pattern). Main app WebView is unbounded by normal iOS app memory.

## Project structure (iOS-specific additions to the single Tauri project)

```
clients/desktop-mobile/
  src-tauri/
    tauri.conf.json
      bundle.iOS.minimumSystemVersion: "15.0"
      bundle.iOS.developmentTeam: "<TEAM_ID>"
      bundle.identifier: com.hopssh.app
      plugins.deep-link.schemes: ["hopssh"]
      plugins.biometric: enabled (Face ID / Touch ID for optional app lock)

    gen/apple/
      project.yml                         xcodegen — Tauri's default template
      project.yml.patch                   our override: adds hopssh-tunnel NE target,
                                          wires entitlements, App Group,
                                          hooks MobileHop.xcframework to extension

      hopssh/                             main app target (Swift shell for Tauri)
        Info.plist
          NSCameraUsageDescription        "Used to scan network join codes"
          NSFaceIDUsageDescription        "Unlock the hopssh app"  (optional, v1.1)
        hopssh.entitlements
          com.apple.security.application-groups: [group.com.hopssh.app]
          keychain-access-groups: [<TEAM_ID>.com.hopssh.app]
          com.apple.developer.networking.networkextension: [packet-tunnel-provider]

      hopssh-tunnel/                      NE extension target (NEW)
        PacketTunnelProvider.swift        ~150 LOC: startTunnel, stopTunnel,
                                          handleAppMessage, NWPathMonitor → Rebind
        Info.plist                        NSExtension dict:
                                            NSExtensionPointIdentifier:
                                              com.apple.networkextension.packet-tunnel
                                            NSExtensionPrincipalClass:
                                              $(PRODUCT_MODULE_NAME).PacketTunnelProvider
        hopssh-tunnel.entitlements        same App Group + Keychain group,
                                          NE packet-tunnel entitlement

      MobileHop.xcframework               built from clients/mobile-go/ in CI;
                                          committed as CI artifact or stored in an
                                          Artifactory-equivalent per macOS runner

    plugins/hopssh-tunnel/                custom Tauri plugin (NEW)
      ios/Sources/Tunnel.swift            NETunnelProviderManager wrapper:
                                            installProfile(config)
                                            start() / stop()
                                            observeStatus() → emit events
                                            setOnDemandRules(enabled)
      ios/Sources/AppGroup.swift          ~60 LOC: Keychain + shared-file helpers
                                          (roll our own — avoid community-plugin
                                          bus-factor risk)
      ios/Sources/QRScanner.swift         AVCaptureSession wrapper (Tauri plugin
                                          command invoke("scan_qr") → returns URL)
      src/lib.rs                          Tauri command stubs (invoke bridge)

  src/                                    Svelte UI (shared across all 5 platforms)
    lib/screens/
      EnrollmentQR.svelte                 NEW — full-screen camera view + manual-code
                                          fallback + deep-link-received path
      MobileConnect.svelte                NEW — large connect toggle, mesh IP hero,
                                          peer count, terminal shortcut
      MobileSettings.svelte               NEW — always-on toggle, unenroll, about,
                                          optional Face ID lock
      Terminal.svelte                     REUSE — existing xterm.js component,
                                          accepts new `scaleHint` prop for DPI fix
```

What is new vs. reused:
- **Reuse**: Svelte dashboard components (nav, peer list, DNS view, terminal wrapper), the entire existing `cmd/agent/` client logic via gomobile.
- **New**: `PacketTunnelProvider.swift`, Tauri plugin (`Tunnel.swift`, `AppGroup.swift`, `QRScanner.swift`), xcodegen override, entitlements files, mobile-specific Svelte screens.

## Enrollment UX

**Primary path — QR code scan:**
1. Admin opens the web dashboard, picks a network, clicks "Add device".
2. Dashboard generates a short-lived join token (server-side: minor addition — wrap the existing `/api/networks/{id}/enroll-token` endpoint with a convenience path that returns a deep-link URL).
3. Dashboard renders the URL `hopssh://enroll?token=<token>&endpoint=https://hopssh.example.com` as a QR code.
4. Phone user opens the hopssh app → taps "Scan QR" → camera permission prompt → scan.
5. App parses URL, POSTs to `{endpoint}/api/networks/{id}/join` with `{token, hostname: deviceName, os: "ios", arch: "arm64"}`.
6. Server returns cert + key + agent token + lighthouse addr + DNS domain.
7. App writes credentials to the App Group Keychain; config JSON to App Group shared file.
8. App calls `NETunnelProviderManager.installProfile(…)` → iOS shows "Add VPN Configuration" system sheet → user approves.
9. Tunnel auto-starts (always-on ON by default). Status view shows connected.

**Fallback — manual device flow:**
1. User taps "Enter code" on the enrollment screen.
2. Input control-plane URL → phone POSTs to `/api/device/code` → gets short user code.
3. UI shows: "Open `{endpoint}/activate` on any device and enter `ABCD-1234`".
4. Phone polls `/api/device/poll` every 5s until approved.
5. Same post-approval path as QR (credentials → keychain → VPN profile install → connect).

**Deep link path** (user received the QR URL via iMessage/email and taps it directly on the phone):
- Tauri deep-link plugin receives `hopssh://enroll?token=…&endpoint=…`.
- Skips camera step, goes straight to the POST-to-`/join` flow.

**Camera permissions:**
- `NSCameraUsageDescription` string: "hopssh uses the camera only to scan network join codes. No images are stored or transmitted."
- First-time permission denial → fall back gracefully to the manual device flow path.

## Tunnel lifecycle + always-on

- **Install profile**: `NETunnelProviderManager.loadAllFromPreferences` → if none, create one with `NETunnelProviderProtocol.providerBundleIdentifier = "com.hopssh.app.tunnel"`. Pass `providerConfiguration` dict containing the lighthouse addr, network name, DNS domain. Certificate material lives in the shared Keychain — the extension reads it with the shared access group at startup.
- **Start**: `session.startVPNTunnel()`. `PacketTunnelProvider.startTunnel()` fires in the extension:
  1. Read cert/key/token from Keychain Access Group.
  2. Construct `NEPacketTunnelNetworkSettings` with the mesh IP, DNS server (the mesh DNS at `10.x.x.1`), match domain (e.g., `home`).
  3. `setTunnelNetworkSettings()` → OS brings up utun.
  4. Get `tunFd: Int32 = self.packetFlow.value(forKey: "socket.fileDescriptor") as! Int32` (private API but stable; standard for packet-tunnel providers).
  5. Call `MobileHop.NewClient(tunFd: tunFd, configJSON: …).Start()`.
  6. Extension enters its run loop; Go goroutines take over for Nebula, heartbeat, renewal.
- **Stop**: `session.stopVPNTunnel()` → `PacketTunnelProvider.stopTunnel(with reason:)` → `MobileHop.Stop()` → Go shuts down cleanly (the vendored `patches/nebula-1031-graceful-shutdown.patch` fix is active).
- **Always-on**: attach an `NEOnDemandRule` with `.connect` action + `.anyInterface` match. Settings toggle flips this. Default: ON at first enroll.
- **Network changes** (WiFi ↔ cellular, WiFi disconnect): extension runs its own `NWPathMonitor` observing path updates; calls `MobileHop.Rebind()` on change. Internally this maps to the existing recovery path in [cmd/agent/nebula.go](../cmd/agent/nebula.go) (`RebindUDPServer()` + `CloseAllTunnels(true)`).
- **Screen lock / sleep**: iOS does NOT suspend the extension during screen-off — only during hardware power-down or airplane mode. Path changes (WiFi drop when device locks a few minutes later) are handled like any other network change.
- **Silent push reconnect (v1.1)**: if we ever need the server to trigger a reconnect (e.g., cert revocation, emergency re-enroll), a silent push wakes the *main* app for ~30s, which calls `session.startVPNTunnel()` to force the extension to restart. Not required for v1 (on-demand rules cover the common cases).

## Web terminal specifics (WKWebView workarounds)

xterm.js works in WKWebView ([xtermjs#3575](https://github.com/xtermjs/xterm.js/issues/3575) resolved), but two iOS-specific gotchas remain:

**DPI fix** (maps to [wails#5111](https://github.com/wailsapp/wails/issues/5111) pattern):
- Custom URL schemes (which Tauri uses on iOS) cause WKWebView to report `devicePixelRatio = 1` on Retina. Result: blurry xterm canvas rendering.
- Fix: expose a Tauri command `invoke("get_display_scale")` that returns `UIScreen.main.scale` from Swift. On app init, multiply the xterm `fontSize` by that scale and set `CanvasRenderingContext2D.setTransform` explicitly.
- ~20 LOC across Swift + the Svelte Terminal wrapper.
- Add a `scaleHint` prop to the existing `Terminal.svelte` so desktop (where `devicePixelRatio` works normally) is unaffected.

**Keyboard / viewport fix** ([tauri#9907](https://github.com/tauri-apps/tauri/issues/9907) + [#10631](https://github.com/tauri-apps/tauri/issues/10631) open):
- Symptom: on-screen keyboard appears → webview scrolls out-of-bounds, `visualViewport.height` misreports.
- CSS workarounds:
  - `<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content">`.
  - JS listener on `window.visualViewport.onresize` → dynamically set `body` `padding-bottom` to the difference between window inner height and viewport height.
  - Scroll-padding on the active input so it stays visible.
- Budget 2-4 days of iteration in Week 5 (dedicated spike).
- If CSS doesn't fully fix it → fallback option: upstream a patch to Tauri's WKWebView webview config; worst case, SwiftUI'ify the enrollment input (terminal can stay in WebView since scroll-into-view behavior is acceptable there).

**Column count**:
- Cap at 120 cols on iPhone, 160 cols on iPad — safely below the [xtermjs#4597](https://github.com/xtermjs/xterm.js/issues/4597) 200-col slowness threshold on Android WebView (iOS has no equivalent documented limit but staying conservative avoids surprises).

**Copy/paste**:
- xterm.js built-in selection + iOS "Copy" callout works.
- For paste: iOS requires explicit permission gate on `navigator.clipboard.readText()` — doesn't work reliably in WebView contexts. Use a Tauri plugin command `invoke("paste_from_clipboard")` that reads `UIPasteboard.general.string` in Swift and returns to JS. ~15 LOC.

## App Groups + Keychain sharing

Two processes (main app + extension) need to share:
- Certificate material (CA cert, node cert, node private key)
- Agent bearer token
- Network config (lighthouse addr, name, DNS domain)
- User preferences (always-on flag)

**Setup:**
- App Group ID: `group.com.hopssh.app`.
- Keychain Access Group: `<TEAM_ID>.com.hopssh.app`.
- Both targets declare both entitlements.

**Storage layout:**
| Item | Where | Why |
|---|---|---|
| `ca-cert` (PEM) | App Group shared `config.json` | Public, large enough to skip Keychain |
| `node-cert` (PEM) | App Group shared `config.json` | Public |
| `node-private-key` | Keychain (`kSecAttrAccessibleAfterFirstUnlock`) | Secret |
| `agent-token` | Keychain (`kSecAttrAccessibleAfterFirstUnlock`) | Secret |
| `lighthouse-host`, `network-name`, `dns-domain` | App Group shared `config.json` | Not secret |
| `always-on-enabled` | App Group `UserDefaults(suiteName: "group.com.hopssh.app")` | Simple toggle |

**Helper**: `AppGroup.swift` (~60 LOC) — read/write convenience wrappers. **Roll our own** rather than depend on a community plugin (per research note on bus-factor risk for `tauri-plugin-keychain`).

**Keychain survival across reinstalls**: default iOS behavior — as long as Team ID is unchanged, Keychain items survive app reinstall. Accept this default (re-enrollment is annoying; users appreciate it "just working" after reinstall).

## Distribution + Apple reviews

**Apple Developer Program**: prerequisite ($99/yr).

**Network Extension entitlement**:
- `com.apple.developer.networking.networkextension` with value `[packet-tunnel-provider]`.
- Requested via the Apple Developer portal — a separate review from App Store review.
- Typical turnaround: **1-2 weeks**. Routinely granted to legitimate VPN apps.
- Submit as soon as the first extension builds + archives (Week 6), **not** at App Store submission time.
- Use-case description for the request: "Mesh VPN client for self-hosted infrastructure (hopssh.com). Establishes encrypted P2P tunnels between user-owned devices via the Nebula protocol. No third-party traffic inspection or ad-blocking."

**App Store review**:
- Category: VPN.
- Privacy manifest (iOS 17+ requirement): declare IP address collection (control plane heartbeat), device name (hostname). No tracking, no third-party SDKs in v1.
- Reviewer notes: emphasize the substantial native code (NEPacketTunnelProvider extension, Tauri plugin, AppGroup helpers) to pre-empt any "just a WebView wrapper" concern (Apple 4.2).

**TestFlight**:
- Internal Testing enabled Week 8. Up to 100 internal testers across the team.
- External Testing (up to 10,000 testers) enabled Week 9-10 for broader beta.

**Distribution channels**:
- App Store only for v1.
- No EU alternative marketplaces in scope for v1 (can revisit post-launch).

## Implementation sequencing

Builds on top of the shared Tauri scaffold (Week 0-3 per strategy doc: refactor + gomobile + scaffold). iOS-specific work starts Week 4.

1. **Week 4 — iOS target + NE extension spike (GO/NO-GO GATE).**
   - Scaffold iOS target via `cargo tauri ios init`.
   - Add `hopssh-tunnel` extension target via xcodegen `project.yml` override.
   - Empty extension that immediately `completionHandler(nil)` — prove archive + TestFlight upload works.
   - Configure entitlements, App Group, Keychain access group.
   - If this takes >5 days → **pause** and re-evaluate Tauri-mobile per the fallback hybrid in [client-apps-plan.md](client-apps-plan.md).

2. **Week 5 — MobileHop.xcframework integration.**
   - Wire `MobileHop.NewClient(…).Start()` from the extension.
   - Hardcode a test config (dev control plane, hardcoded cert).
   - Verify tunnel connects to a peer on the mesh.
   - Run under Xcode Instruments: memory <35 MiB sustained.

3. **Week 5 — WKWebView DPI + keyboard spike (parallel).**
   - Tauri debug page with xterm.js.
   - Apply DPI fix (scale hint from Swift).
   - Apply keyboard CSS workarounds.
   - Physical-device testing on iPhone + iPad.
   - Go/no-go: if xterm fidelity is unacceptable → fallback to SwiftTerm for mobile only.

4. **Week 6 — Enrollment flow (QR + device flow).**
   - Tauri plugin: QR scanner via AVCaptureSession.
   - Deep-link handler for `hopssh://enroll?…`.
   - Server-side: minor addition for the wrapped enrollment URL endpoint.
   - Wire to the existing `/api/networks/{id}/join` path.
   - **Submit Network Extension entitlement request** to Apple in parallel (don't wait for UI completion — the review takes 1-2 weeks).

5. **Week 7 — Connect UX, peer list, terminal, always-on.**
   - `MobileConnect.svelte`: hero toggle, mesh IP, peer count.
   - `NEOnDemandRule` wiring in Swift plugin.
   - Terminal with DPI/keyboard workarounds applied.
   - Settings screen (always-on toggle, unenroll, about).

6. **Week 8 — Polish + TestFlight Internal Testing.**
   - Dogfood on team's iPhones + iPads.
   - Bug triage. Performance profiling under real workloads.
   - Crash reporting: wire a separate Swift Sentry SDK in the main app (sentry-tauri doesn't cover iOS minidumps).

7. **Week 9-10 — External beta + App Store submission.**
   - TestFlight External Testing.
   - Finalize privacy manifest, app description, screenshots, reviewer notes.
   - Submit to App Store. Parallel: finalize NE entitlement approval (should be in-hand by now).

## Verification

On a real iPhone AND a real iPad (simulator does not support NEPacketTunnelProvider):

**Fresh install + enrollment (QR):**
- Install via TestFlight.
- Open app → "Scan QR" → camera permission prompt.
- Scan QR from web dashboard → "Add VPN Configuration" iOS system sheet → approve.
- Tunnel connects within ~3 seconds (status view reflects `.connected`).
- In-app terminal: SSH to a peer via mesh hostname → works.

**Fresh install + enrollment (device-flow fallback):**
- Deny camera permission intentionally.
- Enter control-plane URL manually → app shows short code → approve on a laptop browser.
- Same post-approval flow succeeds.

**Deep-link enrollment:**
- Email the `hopssh://enroll?…` URL to the phone; tap in Mail app.
- App opens straight into the post-approval flow without camera.

**Tunnel lifecycle:**
- Kill Tauri app from the iOS app switcher. VPN stays up (visible in iOS Settings → VPN, and `ping` from another peer to this device's mesh IP still works).
- Relaunch app: status view correctly shows `.connected` state (read from `NETunnelProviderSession.status`).

**Network transitions:**
- WiFi → cellular: mesh reconnects within ~3 seconds.
- Cellular → WiFi: same.
- Lock for 5+ minutes, unlock: still connected (always-on `.connect` rule re-triggers).
- Airplane mode ON for 2 minutes, OFF: mesh recovers within ~5 seconds.

**Memory (Xcode Instruments):**
- Extension process sustained <35 MiB under continuous traffic (10 peers, 1 MB/s bidirectional).
- Spike test: high-rate Nebula handshake burst — peak <45 MiB, never OOM.

**Terminal:**
- xterm.js renders sharply (no blurriness) on iPhone Retina + iPad.
- Soft keyboard slides in, terminal content stays visible above the keyboard (no scroll-out-of-bounds).
- Paste works via toolbar.

**Uninstall:**
- Delete app from home screen.
- iOS automatically removes the VPN profile (system behavior).
- Verify: iOS Settings → VPN → profile gone.
- On-demand rules don't accidentally re-enable a phantom tunnel.

## iOS-specific risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| NE entitlement not approved by Apple | Low | Critical | Request it Week 6 as soon as the first archive builds. Routinely granted for legitimate VPN apps. Prepare the use-case description in advance. |
| Keyboard/viewport bugs affect enrollment + terminal | Medium | Medium | Week-5 dedicated spike. CSS workarounds documented. Fallback: SwiftUI enrollment screen. |
| App Store "just a WebView wrapper" rejection (Apple 4.2) | Low | Medium | Not applicable — substantial native code. Emphasize in reviewer notes. |
| xcodegen override drifts across Tauri CLI releases | Medium | Medium | Pin Tauri CLI version. Test upgrades in a dedicated PR. Track [tauri#14332](https://github.com/tauri-apps/tauri/issues/14332) for official extension support. |
| 50 MiB NE memory cap still insufficient | Low | High | `debug.SetGCPercent(20)` in `mobilehop`. Week 5 Instruments profiling. Fallback: call `runtime.GC()` after handshake bursts; cap max-peers per session. |
| Private API `socket.fileDescriptor` KVC hack for tunFd | Low | Medium | This is the standard workaround used by every Nebula-on-iOS implementation including DefinedNet's `mobile_nebula`. Apple has not rejected apps for this. Watch for a public API in future iOS releases. |
| Silent-push reconnect needed but unavailable without server infra | Low | Low | v1 doesn't need it. On-demand rules cover 95% of cases. If needed later, APNs is straightforward to add. |
| Community plugin (push, keychain) goes unmaintained | Medium | Low | Roll our own Swift helpers (`AppGroup.swift`, ~60 LOC). No runtime dependency on community plugins for critical paths. |

## Open questions

1. **Apple Developer account ownership**: who on the team owns the hopssh developer account, and can they add a second admin? Needed for signing cert issuance + NE entitlement request. Confirm Week 0.
2. **Team ID + bundle ID convention**: `com.hopssh.app` (main) + `com.hopssh.app.tunnel` (extension), or pick different? Affects Keychain Access Group naming — can't change later without user re-enrollment. **Recommend** current scheme; lock Week 1.
3. **QR payload format**: `hopssh://enroll?token=…&endpoint=…` URL form vs. compact base64 blob. **Recommend** URL form — serves as deep link too, no ambiguity.
4. **Face ID / Touch ID app lock**: v1 or v1.1? **Recommend** v1.1 — not critical for the first release; biometric-gated app open is a nice-to-have.
5. **Branding assets**: app icon (1024×1024 master), launch screen storyboard, in-app status icons. Reuse/adapt from macOS branding. Commission Week 2 if not already in hand.
6. **Server-side `/api/networks/{id}/enroll-token` endpoint**: does it exist with the wrapper URL shape, or is this a small new endpoint? Confirm by reading existing `internal/api/enroll.go`. If a small addition, factor in 2-3 days of server-side work.
7. **QR code display in dashboard**: need a Svelte QR component (`qrcode` npm package, ~5kb). Standard addition to the existing admin dashboard.

---

## Lessons from macOS Phase V→DD (2026-04-28 → 2026-05-07)

The macOS desktop client shipped 12 versions in a tight one-week iteration ([[client-macos-architecture]] is now status: accepted, shipped v0.10.96). Six production-discovered patterns affect the iOS plan when implementation begins:

### 1. Daemon/extension-restart recovery (Phase X)
On macOS the cached attach endpoint went stale after `launchctl kickstart`. iOS's exact analogue: **the Network Extension process gets killed routinely by the OS** (memory pressure, on-demand rule re-arm, system update). When the extension respawns it gets a NEW process state — any cached references in the main Tauri app become stale.

**Apply to iOS:** the main app's status observer on `NETunnelProviderSession.status` already gives us "extension is up/down" but doesn't surface "extension restarted with fresh state". Add an explicit `observeStatus` handler that on `.connected → .disconnected → .connected` transitions invalidates JS-side endpoint caches AND triggers `agent.refresh()` in the Svelte layer. Mirror Phase X's `clients/desktop/src-tauri/src/agent.rs::endpoint_alive` TCP-probe pattern, but for the App Group keychain handle instead of a loopback port.

### 2. Autostart-on-login (Phase Y)
macOS plan was: `tauri-plugin-autostart` writes `~/Library/LaunchAgents/com.hopssh.desktop.plist`, default OFF on fresh install, auto-flip ON on first system-mode conversion (with `start_at_login_explicit` sentinel preserving user override).

**iOS analogue is `NEOnDemandRule`** (already in this plan). The `start_at_login_explicit` sentinel pattern still applies: don't auto-toggle the user's Always-on preference once they've explicitly touched it. Default ON for fresh installs (matches the macOS first-system-mode-conversion behavior + matches Tailscale/WireGuard/DefinedNet user expectation on iOS).

### 3. Boot-before-login race (Phase Z)
macOS hit this for system-mode mirror files at `~/Library/Application Support/hopssh/` left root-owned because `/dev/console` was root-owned at boot. **iOS has an analogous but different race:** App Group container access requires the user to have unlocked the device at least once after boot (kSecAttrAccessibleAfterFirstUnlock). On first boot the extension may try to start before the user unlocks, fail to read keychain, and end up in a "started but credential-less" state.

**Apply to iOS:** the `PacketTunnelProvider.startTunnel()` path must handle "keychain access denied because device not yet unlocked" cleanly — log + sleep 5s + retry until keychain becomes accessible, OR fail and let `NEOnDemandRule` retry on next path change. Don't crash the extension; iOS will exponentially back off on extension crashes.

### 4. Uninstall hygiene (Phase AA)
On macOS, Phase Y's autostart LaunchAgent was missed by the uninstall sweep — left a stale launchd entry post-uninstall.

**On iOS, "uninstall" = the user deletes the app**, which iOS handles atomically: removes the VPN profile, removes the App Group, wipes the Keychain Access Group. **No code-side uninstall sweep needed** — the OS handles it. This is structurally simpler than desktop AA. **But:** verify in QA that `kSecAttrAccessibleAfterFirstUnlock` keychain items DO get wiped on app delete (they should, but historically there were edge cases where re-installing on the same Team ID kept old keychain items by design — confirm whether this is desirable for hopssh or whether we want explicit pre-uninstall key removal).

### 5. UX copy: forbidden-jargon source-scan tripwire (Phase BB)
macOS landed `lib.rs::tests::user_facing_copy_has_no_protocol_jargon` — banned `mesh`, `hop-agent`, `data-plane`, `Hide hopssh from the Dock`, etc. Replaced with `network`, `background service`, `Show hopssh in Dock`. Affirmative phrasing per Apple HIG.

**Apply to iOS:** the Svelte UI is shared, so the same tripwire test runs against the same source files automatically. iOS-specific copy additions (Settings → Always-on toggle subtitle, NSCameraUsageDescription string, App Store screenshots) need the same forbidden-token review at PR time. The `NSCameraUsageDescription` already says "scan network join codes" (not "scan mesh codes") — good. Audit the rest of the Info.plist and App Store metadata.

### 6. Three independent watchdogs (Phase DD)
The mobile gomobile core inherits the renewal + watcher watchdogs through `internal/client/`. **`debug.SetGCPercent(20)` in mobilehop init must coexist with the watchdog goroutines** — verify under Xcode Instruments that the three watchdog goroutines each cost <1 MiB sustained (3 × 1 MiB = under the 50 MiB extension cap with comfortable headroom). The three-watchdog architecture is documented at [[../concepts/watchdog]]; the motivating incident is at [[../incidents/2026-05-07-mbp-watcher-wedge]].

**Hard timeouts on vendor-Nebula calls:** Phase DD added `runWithTimeout` wrappers around `RebindUDPServer` + `CloseAllTunnels` because deadlocks aren't panics and `defer recover()` doesn't catch them. The same wrappers carry over to `mobilehop.Rebind()` callers. The Network Extension's NWPathMonitor → Rebind path MUST use the timeout-wrapped version to prevent a wedged Rebind from killing the entire extension's path-change handling.

See [[../concepts/desktop-client]] for the macOS shipped state these lessons came from.

## Lessons from macOS Phase EE → II.4 (2026-05-08 → 2026-05-09)

Polish + diagnostics + UI features that shipped after the previous "Lessons" section was last compiled. The Svelte UI is shared, so most patterns port automatically; the iOS-specific deltas are in the table below.

| Phase | Pattern | iOS applicability |
|---|---|---|
| EE F1 | desktop-prefs.json corruption logging | All — but iOS uses `UserDefaults` (suite name keyed to App Group), not a JSON file; same "log warning, fall back to defaults" discipline applies |
| EE F2 | Account identity in Connected.svelte | All — iOS reads device name via `UIDevice.current.name` (or shows the App Group user identity); component is shared |
| EE F3 | Disabled-button tooltips | **iOS-specific delta**: native HTML `title=` doesn't surface on iOS Safari/WKWebView taps. Replace with iOS-native popover (`UIPopoverPresentationController` shim called from JS via Tauri) OR a tap-to-toast pattern. The intent ("user always knows why a control is disabled") is universal; the mechanism is platform-keyed. |
| EE F4 | Onboarding error specificity | All — same Svelte component + same error returns (just travel through `mobilehop.Enroll()` instead of HTTP) |
| EE F5 | Post-uninstall blocking overlay | **N/A on iOS** — uninstall = user deletes the app from Home Screen; iOS handles atomically. No app-side cleanup flow needed. |
| FF | In-app Activity view (SSE event ring buffer) | All — but **iOS-specific delta**: SSE doesn't work over a Network Extension's containment boundary. Activity events bridge from extension → app via `IPCConnection` (Apple's recommended NEMachServiceName) or shared App Group container file. Ring buffer + Svelte component unchanged. |
| GG | Diagnostics (View logs + Copy info) | **iOS-specific delta**: no Console.app analogue. View logs = render shared App Group file in an in-app text viewer; ⤴ Share sheet to email / messages / AirDrop for support tickets. "Copy diagnostic info" works identically. |
| HH | Read-only DNS records + Manage in dashboard | All — opens the dashboard URL in `SFSafariViewController` (or system browser) for write operations, since the .app's WKWebView already authenticates via cookie share for the Terminal flow |
| II.3 | In-app Terminal via webview pointed at dashboard's `/terminal/` route | All — `WKWebView` + Tauri's iOS support handles cookie share automatically. Verify on iPad screen sizes (xterm.js viewport keyboard interactions are the largest QA risk — already noted in §iOS-specific risks above) |
| II.4 | OS brand-mark icons + tooltips | All — inline SVG renders in WKWebView identically; replace tooltip mechanism per EE F3 row above |
| KK | Karpathy behavioral guidelines | Process-only; no code change |

**Substrate-not-built reminder.** All of the above iOS items are paper-blocked on `internal/client/` + `MobileHop.xcframework` not existing. Do not start Phase EE→II.4 iOS port work until the substrate ships. Re-verify with `ls internal/client/ clients/mobile-go/` before starting.
