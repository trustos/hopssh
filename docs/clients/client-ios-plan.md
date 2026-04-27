# hopssh iOS client — implementation plan

See [client-apps-plan.md](client-apps-plan.md) for the overall Tauri-across-5-platforms strategy and shared substrate (gomobile core, `internal/client/` refactor, Svelte UI). See [client-macos-plan.md](client-macos-plan.md) for the macOS peer. This doc covers the iOS-specific layer of the single Tauri project.

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
