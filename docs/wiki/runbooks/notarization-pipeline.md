---
type: runbook
title: macOS notarization pipeline
status: planning — blocked on Apple Developer Program enrollment
last_compiled: 2026-05-09
sources:
  - Apple notarytool docs (developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)
  - tauri.conf.json
  - .github/workflows/release-desktop.yml
---

# macOS notarization pipeline

Operational runbook for moving the macOS desktop bundle from ad-hoc-signed (current) to Developer-ID-signed + notarized + stapled (target for public 1.0). The curl-pipe install path (`curl https://hopssh.com/install-mac.sh | bash`) stays as the install-script alternative — notarization unblocks the DMG drag-to-Applications path that Sequoia/Tahoe Gatekeeper currently friction-gates.

## Status

**Blocked on Apple Developer Program enrollment.** Every other step is mechanical.

## Prerequisites (numbered, in order)

1. **Apple Developer Program account** ($99/year). Enroll at https://developer.apple.com/programs/. Required to issue Developer ID Application certificates.
2. **Developer ID Application certificate.** Generate via Apple Developer portal: Certificates → Production → Developer ID Application. Download `.cer`, install into the macOS CI runner's keychain via `security import` (CI) or by double-clicking (local).
3. **App-specific password for notarytool.** Apple ID → Sign-In and Security → App-Specific Passwords. Distinct from the developer account password; required by `notarytool submit`.
4. **CI secrets populated.** GitHub Actions repo settings → Secrets:
   - `APPLE_ID` — the Apple ID email used for notarization.
   - `APPLE_TEAM_ID` — 10-character team ID from Apple Developer portal.
   - `APPLE_APP_PASSWORD` — the app-specific password from step 3.
   - `APPLE_CERTIFICATE_BASE64` — `base64 -i DeveloperID.p12` output of the exported certificate.
   - `APPLE_CERTIFICATE_PASSWORD` — the password protecting the `.p12` export.
5. **Hardened runtime + entitlements** in the desktop `tauri.conf.json` (under `clients/desktop/src-tauri/`) — set `bundle.macOS`:
   - `"hardenedRuntime": true`
   - `"entitlements": "entitlements.plist"` referencing a file with at minimum `com.apple.security.network.client` and `com.apple.security.network.server` (the .app makes outbound connections to the agent's loopback API + opens dashboard webviews).

## Pipeline

Once prerequisites are met, the CI flow becomes:

```yaml
# .github/workflows/release-desktop.yml — pseudocode for the notarization stage
- name: Import signing certificate
  run: |
    echo "$APPLE_CERTIFICATE_BASE64" | base64 -d > cert.p12
    security create-keychain -p "$KEYCHAIN_PASSWORD" build.keychain
    security default-keychain -s build.keychain
    security unlock-keychain -p "$KEYCHAIN_PASSWORD" build.keychain
    security import cert.p12 -k build.keychain -P "$APPLE_CERTIFICATE_PASSWORD" -T /usr/bin/codesign
    security set-key-partition-list -S apple-tool:,apple: -s -k "$KEYCHAIN_PASSWORD" build.keychain

- name: Build + sign the .app
  run: cd clients/desktop && npm run tauri build
  # Tauri reads bundle.macOS.signingIdentity from tauri.conf.json (set to the Developer ID Application identity name).

- name: Notarize the DMG
  run: |
    xcrun notarytool submit \
      "clients/desktop/src-tauri/target/release/bundle/dmg/hopssh.dmg" \
      --apple-id "$APPLE_ID" \
      --team-id "$APPLE_TEAM_ID" \
      --password "$APPLE_APP_PASSWORD" \
      --wait

- name: Staple the ticket
  run: xcrun stapler staple "clients/desktop/src-tauri/target/release/bundle/dmg/hopssh.dmg"
```

`--wait` blocks until Apple's notarization service completes (typically 1–5 minutes). On failure, `notarytool log <submission-id>` returns the rejection reason — common causes are missing entitlements, hardened runtime disabled, or an embedded sidecar binary that itself isn't signed.

## Sidecar binary signing

The desktop bundle includes `hopssh.app/Contents/Resources/binaries/hop-agent`. This sidecar must be Developer-ID-signed too — Apple notarization rejects bundles containing unsigned executables, even helpers. Tauri's `bundle.externalBin` config handles this when `signingIdentity` is set.

## What stays unchanged

- The `curl https://hopssh.com/install-mac.sh | bash` install-script path. Curl-downloaded files have no `com.apple.quarantine` xattr (only browsers set it via WebKit), so Gatekeeper has no quarantine to gate against. This path keeps working with or without notarization.
- The `make dev-deploy-desktop` flow. Dev builds remain ad-hoc-signed for fast iteration; only release builds go through the pipeline.

## Verification

After a notarized release:

1. Download the DMG via Safari (which sets the quarantine xattr).
2. Drag the .app to /Applications.
3. Double-click. Expected: the app opens without any "developer cannot be verified" dialog. The first-launch dialog should say something benign like "hopssh is an app downloaded from the Internet. Are you sure you want to open it?" with an "Open" button (not greyed out).
4. `spctl --assess --verbose=4 /Applications/hopssh.app` — expected output: `accepted` and `source=Notarized Developer ID`.
5. `xcrun stapler validate /Applications/hopssh.app` — expected: `The validate action worked!`.

## Cost / cadence

- $99/year Apple Developer Program.
- ~1–5 min per notarization submission. Roll into the existing `release-desktop.yml` workflow; no separate manual step.
- Apple's notarization service has had multi-hour outages a handful of times per year. If a release is gating on notarization, ship the curl-pipe path first and let the DMG follow when notarytool returns.

## What this runbook does NOT cover

- iOS / iPadOS / macOS Catalyst signing (separate provisioning profiles + App Store Connect flow).
- Windows code signing (DigiCert / Sectigo certificate, separate `signtool.exe` flow — different CA ecosystem entirely).
- Linux package signing (rpm-sign / debsig-verify — distro-specific).

## Backlinks

- [`docs/pre-release-checklist.md`](../../pre-release-checklist.md) — Distribution + signing section that gates on this runbook.
- [`docs/wiki/decisions/client-macos-architecture.md`](../decisions/client-macos-architecture.md) — overall macOS bundle strategy.
