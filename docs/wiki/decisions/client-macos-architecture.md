---
type: decision
title: macOS client architecture — Tauri shell + sidecar hop-agent
status: accepted
last_compiled: 2026-05-07
sources:
  - clients/desktop/src-tauri/src/lib.rs
  - clients/desktop/src-tauri/src/agent.rs
  - clients/desktop/src/App.svelte
  - cmd/agent/migrate.go (system-mode handoff)
shipped: v0.10.96
---

# macOS client architecture — Tauri shell + sidecar hop-agent

## Decision basis

The reasoning that produced this decision came from:

- **Code I read:** `cmd/agent/client.go` (existing userspace-mode entry point), `cmd/agent/service.go` (launchd installer that became the system-mode upgrade path), `cmd/agent/nebula.go` (sleep/wake handling that the Tauri shell did not need to reimplement).
- **External sources I fetched:** Tauri 2 sidecar docs (`externalBin`), Apple Developer notarization + hardened-runtime documentation, `SMJobBless` deprecation notes (drove choice of `osascript` + launchd over `SMJobBless`).
- **Prior-knowledge claims (with confidence):**
  - [HIGH] Tauri 2 sidecar pattern works on macOS — verified by shipping v0.10.85→v0.10.96.
  - [HIGH] Userspace gvisor netstack provides functional VPN without kernel TUN — long-running practice in `cmd/agent/`.
  - [HIGH] DMG + notarization is the standard non-App-Store macOS distribution path — verified against Tailscale's macOS distribution model.
  - [MEDIUM] Curl-pipeable installer bypasses Sequoia/Tahoe Gatekeeper because curl-downloaded files don't get the `com.apple.quarantine` xattr — confirmed empirically during Phase F (April 2026) and shipped as the canonical path.
  - [LOW] Apple's NEPacketTunnelProvider (Network Extension) would be the "correct" desktop-VPN architecture too — recalled from training data; deferred until iOS work needs it (carries to [[client-ios-architecture]]).

## Status — SHIPPED (Phase V → DD, v0.10.85 → v0.10.96)

This document was the original April 2026 implementation plan. It is now **shipped** in production on both Macs in the `dev-deploy` fleet (Mac mini + MBP). The architecture below was built almost verbatim — Tauri 2 main app spawns `hop-agent` as a sidecar in userspace mode by default, with optional one-time system-mode upgrade to a launchd daemon + kernel utun.

For the **shipped state inventory** (22 Tauri commands, 30 Rust tests, all 12 Phase V→DD capabilities), see [[../concepts/desktop-client]]. The sections below are preserved for the architectural reasoning that produced them; cross-reference [[../concepts/desktop-client]] for what's currently in `clients/desktop/`.

See [[../concepts/client-strategy]] for the overall Tauri-across-5-platforms strategy and shared substrate.

## Context

macOS is the team's primary dev platform (see the Discovery Log in CLAUDE.md — most empirical testing and the `sendmsg_x`/`recvmsg_x` batch-syscall optimizations target macOS), the first platform to dogfood, and the lowest-risk desktop target. Shipping macOS first lets us iterate the shared Tauri desktop scaffold against the platform we already know best, then extend to Windows and Linux with incremental deltas.

**Decisions (confirmed with user):**
- **Distribution**: DMG + notarization. No Mac App Store. Direct download from hopssh.com. Auto-update via the Tauri updater.
- **UI form**: Menu bar app with an optional main window. Connect/status in the menu bar dropdown; peer list + web terminal + management in the main window.
- **Privilege model**: Userspace Nebula (gvisor netstack) by default — zero admin prompts on first launch. Optional "Install system component" action upgrades to kernel TUN via launchd system daemon.

## Architecture on macOS

```
┌─────────────────────────────────────────────────────┐
│ hopssh.app (Tauri main app, LSUIElement=true)       │
│ ┌─────────────────────┐    ┌──────────────────────┐ │
│ │ Menu bar tray       │    │ Main window (hidden  │ │
│ │  - status icon      │    │  until "Open         │ │
│ │  - connect toggle   │    │  Dashboard" chosen)  │ │
│ │  - peer summary     │    │  - full dashboard    │ │
│ │  - "Open Dashboard" │    │  - xterm.js terminal │ │
│ │  - "Enroll…"        │    │  - peer list + DNS   │ │
│ └─────────┬───────────┘    └──────────┬───────────┘ │
│           │    Tauri Rust backend     │             │
│           │  (commands + state mgmt)  │             │
│           └────────────┬──────────────┘             │
└────────────────────────┼────────────────────────────┘
                         │ HTTP on 127.0.0.1
                         ▼
         ┌───────────────────────────────┐
         │ hop-agent (sidecar process)    │
         │                                │
         │ Default: userspace Nebula      │
         │ (gvisor netstack, no TUN)      │
         │                                │
         │ Elevated mode (optional):      │
         │ launchd daemon,                │
         │ kernel utun device             │
         └───────────────────────────────┘
```

The Tauri app itself runs as a normal user-space macOS app — no elevation at first launch. It spawns `hop-agent` as a sidecar child process in **userspace mode** (Nebula's gvisor netstack), which connects to the mesh without any OS-level TUN device. This mirrors the existing [cmd/agent/client.go](../cmd/agent/client.go) non-root default.

The **optional upgrade to kernel TUN** is a separate one-time flow that:
1. Prompts the user (from Settings in the main window) with a clear description of the tradeoff.
2. On consent, uses `SMJobBless` (or `osascript -e "do shell script … with administrator privileges"`) to install the agent as a launchd system daemon (`/Library/LaunchDaemons/com.hopssh.agent.plist`) using the existing [cmd/agent/service.go](../cmd/agent/service.go) installer.
3. After install, the Tauri app stops talking to the in-app sidecar and talks to the system-wide daemon via the same local HTTP API on `127.0.0.1`.
4. The mesh then stays up even when the user quits the app.

## Project structure (macOS-specific additions to the shared Tauri project)

Most of the Tauri desktop scaffold is shared across macOS/Windows/Linux. macOS specifically touches:

```
clients/desktop-mobile/
  src-tauri/
    tauri.conf.json
      bundle.macOS.minimumSystemVersion: "12.0"
      bundle.macOS.providerShortName: (dev cert team short name)
      bundle.macOS.signingIdentity: (Developer ID Application: …)
      bundle.macOS.entitlements: macos/Entitlements.plist
      bundle.macOS.dmg: license, background image, icon positioning
      app.macOSPrivateApi: false (default)
      plugins.updater.endpoints: ["https://releases.hopssh.com/macos/{{target}}/{{current_version}}"]
      plugins.updater.pubkey: (embedded signing key)
    macos/
      Info.plist.override            LSUIElement=1, LSApplicationCategoryType (Utilities)
      Entitlements.plist             hardened-runtime, network.client, network.server
      HelperTool-Install.sh          installs hop-agent as launchd daemon (kernel TUN upgrade)
      HelperTool-Uninstall.sh        removes daemon + plist (uninstall flow)
    src/
      tray.rs                        NEW — menu bar tray icon + menu + state
      mac_helper.rs                  NEW — SMJobBless / AuthorizationExec wrappers
      sidecar.rs                     Shared with Windows/Linux — process mgmt
  src/                               Svelte UI (shared across all 5 platforms)
    lib/screens/
      macOS-TrayMenu.svelte          NEW — rendered inside the tray popover window
  binaries/
    hop-agent-universal-apple-darwin NEW — universal binary (arm64 + x86_64 via lipo)
```

The universal binary is built in CI by compiling `hop-agent` for both `darwin/arm64` and `darwin/amd64` then `lipo`-joining them into one file that Tauri's `externalBin` references. This matches the existing macOS agent release pipeline.

## Menu bar UX details

**Icon states** (template images, black-on-transparent so macOS auto-inverts for light/dark menubar):
- Not enrolled: hollow hop icon
- Disconnected: outline icon
- Connecting: animated dots or pulsing icon
- Connected (P2P): filled icon
- Connected (relay): filled icon with subtle "R" badge
- Error: red dot

**Dropdown menu** (when user clicks the tray icon):
- Header: network name + your mesh IP (e.g. "home — 10.42.1.5")
- Toggle: Connect / Disconnect
- Peer count: "3 peers online" — submenu shows first 5, then "More…" opens main window
- Separator
- "Open Dashboard" → shows main window
- "Enroll in a Network…" (only visible if not enrolled yet)
- "Settings…"
- Separator
- "Quit hopssh"

**First-launch flow:**
1. User double-clicks the DMG, drags hopssh.app to /Applications.
2. First launch: macOS Gatekeeper check passes (notarized). App starts, tray icon appears.
3. Tray dropdown shows "Enroll in a Network…" — clicking opens the main window to a device-flow enrollment screen (QR code or short code + "Open browser to approve" button).
4. After approval, the agent sidecar starts userspace Nebula with the received cert. No admin prompt. Mesh works.
5. User can optionally visit Settings later to enable "Install system component" for kernel-TUN mode.

## Command Line Tools install (optional)

Power users want `hop status`, `hop peers`, `hop info` from Terminal. The same `hop-agent` binary bundled inside the .app already implements all these subcommands — we just expose it to `$PATH`.

**Flow:**
- Settings → "Install Command Line Tools" button.
- One-time admin prompt (`osascript -e 'do shell script … with administrator privileges'`).
- Creates symlink: `/usr/local/bin/hop` → `/Applications/hopssh.app/Contents/Resources/hop-agent-universal-apple-darwin`.
- Settings shows "Installed" state + "Uninstall" button afterward.

**Uninstall:**
- Settings → "Uninstall Command Line Tools".
- One-time admin prompt.
- Removes the symlink (only if it still points to our bundle — guards against overwriting a user's unrelated `hop` binary).

**Independence from kernel-TUN upgrade:**
- This is a *separate* opt-in from "Install system component" (kernel-TUN daemon).
- Users can have CLI tools installed without the system daemon, and vice versa.
- Matches Xcode's "Install Command Line Tools" convention as an independent action.

**Why symlink, not copy:**
- App auto-update replaces the binary in-place inside the bundle; the symlink continues resolving to the new version. A copy would require a privileged update step on every app update.
- Removing the .app bundle leaves a dangling symlink — the uninstall path in the app handles this cleanly when run before the user drags the app to Trash. For users who skip the uninstall step, document the manual fix in the README (`sudo rm /usr/local/bin/hop`).

## Critical files to read / reuse / modify

Reuse unchanged:
- [cmd/agent/client.go](../cmd/agent/client.go) — client join flow; agent runs in userspace mode by default.
- [cmd/agent/nebula.go](../cmd/agent/nebula.go) — Nebula startup; already supports `--userspace` mode.
- [cmd/agent/service.go](../cmd/agent/service.go) — launchd service install (already works on macOS).
- [cmd/agent/dns_darwin.go](../cmd/agent/dns_darwin.go) — macOS DNS registration via `scutil` and per-link DNS.
- `internal/nebulacfg/defaults.go` — Nebula config generation.

New, macOS-specific:
- Tauri tray icon + menu (planned: `tray.rs` under `src-tauri/`).
- Privilege escalation helper (planned: `mac_helper.rs` under `src-tauri/`) wrapping `AuthorizationExecuteWithPrivileges` or shelling to `osascript` for the one-time daemon install.
- DMG bundling config in `tauri.conf.json` — background image, license agreement, icon layout.
- CI pipeline additions:
  - Universal binary build (arm64 + amd64 → lipo).
  - Code signing: Developer ID Application cert in the macOS keychain on a macOS runner.
  - Notarization: `xcrun notarytool submit` + `xcrun stapler staple`.
  - Release feed: JSON manifest at `https://releases.hopssh.com/macos/aarch64-apple-darwin/latest` + signed `.sig` for Tauri updater.

Modify in existing codebase:
- Add a **local-only HTTP API** to `hop-agent` bound to `127.0.0.1:<random-port>` (port reported to stdout at startup for the Tauri parent to capture). Exposes: GET `/status`, GET `/peers`, POST `/connect`, POST `/disconnect`, POST `/enroll/device-flow`. ~150 LOC in `cmd/agent/`. See open question #1 in the strategy doc.
- Add `hop-agent --stdio-ready` flag: on startup, prints `HOPSSH_READY:127.0.0.1:<port>\n` to stdout and then blocks. Tauri parses this to learn the API port reliably.

## Distribution + signing + updates

**Signing:**
- Apple Developer Program account ($99/yr) — **prerequisite, confirm in place**.
- Developer ID Application certificate issued, installed on macOS CI runner.
- Hardened runtime enabled. Entitlements include `com.apple.security.network.client` and `com.apple.security.network.server` (the agent listens on 127.0.0.1).
- `hop-agent` binary is **separately signed** before being bundled into the .app, with its own hardened-runtime entitlements (network.client/server only).

**Notarization:**
- `xcrun notarytool submit hopssh.dmg --apple-id … --team-id … --password … --wait`.
- On success: `xcrun stapler staple hopssh.dmg`.
- Both steps run in GitHub Actions on a macOS runner.

**Auto-update:**
- Tauri updater plugin. At every app launch (and periodically), it fetches `https://releases.hopssh.com/macos/<target-triple>/<current-version>`.
- Response is a signed JSON with a URL to the new `.tar.gz` or `.app.tar.gz` + signature.
- The pubkey is embedded in the app at build time (`tauri signer generate` once, store the private key in CI secrets).
- Updater downloads in background, prompts user on next launch to relaunch, applies in-place. Agent daemon (if installed) is restarted as part of the flow via the existing `cmd/agent/update.go` self-update logic.

**Release pipeline (CI):**
1. Tag `v0.Y.Z` pushes → GitHub Actions on macOS-latest.
2. Build `hop-agent` universal binary.
3. Build Tauri app (universal) with the agent as `externalBin`.
4. Sign the app bundle + embedded agent.
5. Build DMG with custom background.
6. Submit for notarization, wait, staple.
7. Sign the `.tar.gz` update bundle with the updater private key.
8. Upload DMG + update bundle + updater manifest to `releases.hopssh.com` (Cloudflare R2 or similar).

## Implementation sequencing

Roughly 4-6 weeks elapsed for a usable macOS v1, shipped privately to internal testers first. Parallelizable with the shared Tauri scaffold work.

1. **Week 0-1 — Prerequisite refactor (shared with all platforms).** `internal/client/` extraction from `cmd/agent/*`. Add local HTTP API to `hop-agent`. See strategy doc.

2. **Week 1-2 — Tauri desktop scaffold.** New `clients/desktop-mobile/` project. Pull in the existing Svelte 5 frontend as a workspace. Wire up the sidecar lifecycle (spawn agent, parse `HOPSSH_READY`, kill on quit). Basic "hello world" main window showing agent status. **No macOS-specific work yet** — just proving the cross-platform scaffold works on macOS.

3. **Week 2-3 — Menu bar tray.** Add tray icon + dropdown menu. Implement connection state → icon mapping. Wire menu actions (connect, disconnect, quit, show window). LSUIElement=1 so the app does not appear in the Dock or the Cmd-Tab switcher.

4. **Week 3-4 — Enrollment flow + main window dashboard.** Device-flow enrollment UI (reuses existing Svelte components). Status + peer list screens. Embed the existing web terminal component (xterm.js) — should "just work" since it already runs in the dashboard.

5. **Week 4 — Settings screen + optional kernel-TUN upgrade.** "Install system component" flow. `osascript` prompt for admin, `launchctl load` the agent plist, app re-connects to the daemon API. Uninstall flow ("Remove system component") reverses it.

6. **Week 5 — CI: signing, notarization, DMG build, Tauri updater.** All the pipeline plumbing. First automated dogfood DMG on tag.

7. **Week 5-6 — Polish + internal dogfood.** Installed on both Macs in `dev-deploy` (Mac mini + laptop). Bugs triaged. README + basic user docs.

8. **Week 6+ — Public beta.** Publish to hopssh.com/download. Collect feedback. Iterate.

## Verification

End-to-end test plan on a **fresh macOS user account** (to catch any assumptions about pre-existing state):

**Fresh install:**
- Download DMG from a TestFlight-equivalent private URL.
- Open DMG, verify background image + license shown, drag app to Applications.
- First launch: Gatekeeper check passes silently (notarization stapled).
- Tray icon appears, tooltip shows "hopssh — not enrolled".

**Enrollment (userspace mode):**
- Click tray → "Enroll in a Network…" opens main window.
- Enter control-plane URL (self-hosted support), click "Enroll".
- App opens browser to device-flow approval URL; user approves.
- No admin prompt. Tray icon changes to "connected".
- `ifconfig` shows no new utun device (userspace mode, expected).
- From Terminal: curl to mesh hostname via the agent's HTTP proxy works.
- Web terminal in the main window: SSH to a peer via mesh works.

**Upgrade to kernel TUN:**
- Settings → "Install system component".
- Admin prompt appears (once).
- After install: `launchctl list | grep hopssh` shows daemon running.
- `ifconfig` shows new utun device with the mesh IP.
- Quit the hopssh app. Daemon keeps running; `ping` to mesh host still works.
- Relaunch app: tray shows connected state from daemon.

**CLI tools install (optional):**
- Settings → "Install Command Line Tools" → admin prompt appears once → succeeds.
- Terminal: `which hop` → `/usr/local/bin/hop`. `hop status` → returns current mesh state. `hop peers` → lists peers.
- Auto-update the app. `hop` still works and reports the new version (symlink resolves to the replaced bundle binary).
- Settings → "Uninstall Command Line Tools" → admin prompt once → symlink gone. `which hop` → empty.

**Network transitions:**
- Connect to WiFi A. Verify mesh OK.
- Switch to WiFi B. Within ~3 seconds, mesh reconnects (existing `watchNetworkChanges` behavior).
- Sleep laptop for 2 minutes. Wake. Mesh recovers within ~3 seconds.

**Auto-update:**
- Bump version, publish update feed.
- App detects update at next launch (or periodic check).
- Dialog: "hopssh 0.11.0 is available". Click Install.
- App restarts; version bumps; agent version bumps.

**Uninstall:**
- Quit app.
- If kernel-TUN installed: from app's Settings, "Remove system component" (prompts admin once).
- If CLI tools installed: from app's Settings, "Uninstall Command Line Tools" (prompts admin once).
- Drag app to Trash.
- Verify `/Library/LaunchDaemons/com.hopssh.agent.plist` is gone, `/usr/local/bin/hop` is gone, `~/Library/Application Support/hopssh/` can be wiped manually.

## macOS-specific risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Notarization rejection (hardened runtime + unsigned subcomponent) | Medium | High | Sign the `hop-agent` binary *before* bundling it into the .app. Add a local CI check that runs `codesign --verify --deep` and `spctl --assess --verbose`. Reject PRs that break this. |
| Tauri updater private key loss | Low | High | Generate in Week 5, back up in 1Password team vault + a second offline copy. Document recovery in a sealed envelope. |
| Menu-bar icon invisible on certain macOS appearance modes | Low | Low | Use `NSImage.isTemplate = true` (Tauri flag) so macOS handles inversion. Manually test in light, dark, and High Contrast modes. |
| DMG custom background broken on Apple Silicon | Low | Low | Build DMG on an arm64 runner matching the target; test on arm64 and amd64 machines. |
| Admin prompt UX for kernel-TUN install feels scary | Medium | Medium | Pre-flight screen that explains what's installed, why, and how to remove it. Link to docs page. Copy-review the dialog text. |
| WebKit-specific bugs in the embedded terminal | Low | Medium | Validated open — xterm.js works in WKWebView per xtermjs#3575 (resolved). Main remaining risk is keyboard/viewport bugs on iOS, not macOS. |

## Open questions

1. **Apple Developer account**: confirm this exists under the hopssh organization and who has admin access. The notarization pipeline needs an app-specific password in CI secrets — one-time setup.
2. **Release hosting**: where do the DMG + updater manifests live? Cloudflare R2? S3? The existing GitHub Releases path works for GitHub users but the Tauri updater wants a stable URL pattern. Recommend R2 behind `releases.hopssh.com`.
3. **Login at startup** behavior: should the app auto-launch at login by default, or ask the user on first launch? Recommend: ask once, remember choice (standard Mac UX).
4. **Crash reporting**: wire Sentry on macOS from day one? Recommend yes, via Tauri plugin + native Rust Sentry SDK (no iOS-specific gap here since this is macOS).
5. **Branding assets**: tray icon template (SVG → 16/32/44/64 pt), app icon (.icns, 1024×1024 master), DMG background image (540×380). Confirm these exist or need design.
6. **Path for CLI symlink**: `/usr/local/bin/hop` is universal but on modern macOS `/opt/homebrew/bin` is the default for arm64 Homebrew users. Recommend `/usr/local/bin/hop` for universality (exists in `$PATH` by default on all macOS versions); document that Homebrew-first users can manually symlink into their brew prefix if they prefer.
