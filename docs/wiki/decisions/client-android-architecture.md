---
type: decision
title: Android client architecture — Tauri + VpnService foreground + gomobile aar
status: proposed
last_compiled: 2026-05-07
sources:
  - cmd/agent/client.go
  - cmd/agent/nebula.go
  - cmd/agent/renew.go
  - patches/nebula-1031-graceful-shutdown.patch
---

# Android client architecture — Tauri + VpnService foreground + gomobile aar

## Decision basis

The reasoning that produced this decision came from:

- **Code I read:** `cmd/agent/client.go`, `cmd/agent/nebula.go`, `cmd/agent/renew.go` (same Go core that gomobile compiles into the aar), `patches/nebula-1031-graceful-shutdown.patch`.
- **Wiki pages I consulted:** [[../concepts/client-strategy]] (master strategy), [[client-ios-architecture]] (mobile sibling — Android shares everything in-process where iOS needed App Groups + Keychain sharing), [[../concepts/watchdog]] (inherited watchdogs).
- **External sources I fetched:** [DefinedNet/mobile_nebula](https://github.com/DefinedNet/mobile_nebula) (Android `VpnService` reference), Android documentation for `VpnService`, foreground service `specialUse` type (API 28+), `EncryptedSharedPreferences` + Jetpack Security `MasterKey`, Google Play VPN policy.
- **Prior-knowledge claims (with confidence):**
  - [HIGH] `VpnService` foreground service runs in the same process as the main activity by default — verified against Android docs.
  - [HIGH] `START_STICKY` causes Android to auto-restart the service after low-memory kill with a null intent — Android lifecycle docs.
  - [HIGH] OEM battery killers (Xiaomi, OPPO, Huawei) aggressively kill background apps including foreground services with VPN — community reports + dontkillmyapp.com.
  - [HIGH] Play Store first-submission VPN review takes 7-14 days — recalled from past Play submissions and developer community reports.
  - [MEDIUM] `EncryptedSharedPreferences` MasterKey unwrap requires after-first-unlock — verified against Jetpack Security docs but interaction with `BootReceiver` timing is empirically tested at Phase Z analogue (see Lessons from macOS Phase V→DD § 3).
  - [MEDIUM] ML Kit barcode scanning adds ~5 MB to APK; ZXing is lighter but less accurate — recalled from past mobile work.
  - [LOW] `BIND_VPN_SERVICE` permission is system-declared and not user-grantable — recalled from Android security model.

## Status

**Proposed.** Substrate-blocked: requires `internal/client/` refactor + a new `clients/mobile-go/mobilehop/` gomobile binding compiling to `mobilehop.aar`. Verified 2026-05-07: `internal/client/` does not exist yet; no `.aar` artifacts on disk.

Google Play Console ($25 one-time) + Play Store VPN policy review (1-3 days typical, 7-14 days for first submission) are external prerequisites.

See [[../concepts/client-strategy]] for the overall 5-platform strategy and [[client-ios-architecture]] for the mobile peer (iOS architecture is more complex — App Groups + Keychain sharing — Android shares everything in-process).

## Context

Android is the second mobile target, paralleling iOS in the Week 4-10 shared plan. Architecture is fundamentally simpler than iOS:

- On iOS the tunnel code lives in a separate OS-managed Network Extension process (App Groups + Keychain sharing required).
- On Android a `VpnService` foreground service runs **in the same process** as the main app by default. Same Kotlin code, same JVM, direct access to the `mobilehop.aar` gomobile binding — no IPC, no shared-storage dance.

The foreground service (with a persistent notification, required on API 26+) keeps the tunnel alive when the UI activity is destroyed, killed, or backgrounded. Same-process does not mean fate-sharing: Android keeps the service alive independently of the activity as long as the foreground notification is active.

**Decisions (confirmed with user):**
- **Minimum SDK: API 26 / Android 8** (covers ~97% of active devices, required for Notification Channels).
- **Distribution: Google Play only** for v1. No F-Droid or direct APK (can revisit post-launch).
- Enrollment: **QR code scan + device flow fallback** (same pattern as iOS).
- Always-on VPN: **default ON** at the app level; deep-link to the OS-level always-on setting for users who want the strongest reconnect-on-boot guarantees.
- **Target SDK**: follow Play Store requirements (currently API 34+; check at release time).

## Architecture on Android

```
┌──────────────────────────────────────────────────────────┐
│ hopssh app (single process)                               │
│                                                           │
│  ┌────────────────────────────────────────────────────┐  │
│  │ MainActivity (Tauri Android host)                   │  │
│  │                                                     │  │
│  │  ┌────────────────────────────────────────────┐    │  │
│  │  │ WebView (Chromium) — Svelte UI              │    │  │
│  │  │  - Enrollment (QR scan + device flow)       │    │  │
│  │  │  - Connect / disconnect toggle              │    │  │
│  │  │  - Peer list + mesh IP + DNS                │    │  │
│  │  │  - xterm.js terminal                        │    │  │
│  │  └────────────────────────────────────────────┘    │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                 │
│  ┌──────────────────────┴──────────────────────────────┐  │
│  │ Tauri plugin "hopssh-tunnel" (Kotlin + Rust)        │  │
│  │   @Command start, stop, observeStatus, scanQR       │  │
│  │   wraps HopsshVpnService lifecycle                  │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │ Intent + Binder                 │
│  ┌──────────────────────┴──────────────────────────────┐  │
│  │ HopsshVpnService (foreground service)               │  │
│  │   extends android.net.VpnService                    │  │
│  │   - persistent notification (required API 26+)      │  │
│  │   - startForeground(NOTIFICATION_ID, …)             │  │
│  │   - START_STICKY: OS restarts if killed             │  │
│  │                                                     │  │
│  │   onStartCommand() →                                │  │
│  │     VpnService.Builder → establish() → tunFd        │  │
│  │     MobileHop.NewClient(tunFd, cfg) + Start()       │  │
│  │     ConnectivityManager.registerNetworkCallback →   │  │
│  │       MobileHop.Rebind() on path change             │  │
│  │                                                     │  │
│  │   (mobilehop.aar — same Go core as iOS)             │  │
│  └─────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────┘
```

Key properties:
- **Foreground service + persistent notification** keeps the VPN alive independent of the Activity lifecycle. User kills the app → service stays up (unless the user force-stops the whole app).
- **Same process as UI** → no IPC required, zero marshaling cost between UI actions and tunnel control.
- **START_STICKY** → if Android kills the service under memory pressure, the OS restarts it automatically with a null intent; we check enrollment state and reconnect.
- **BOOT_COMPLETED receiver** → restart the service on device reboot if always-on was enabled.
- **No memory cap equivalent to iOS NE 50 MiB** — normal app memory budget (hundreds of MB). The `debug.SetGCPercent(20)` in `mobilehop` is harmless here and kept for consistency.

## Project structure (Android-specific additions to the single Tauri project)

```
clients/desktop-mobile/
  src-tauri/
    tauri.conf.json
      bundle.android.minSdkVersion: 26
      bundle.android.targetSdkVersion: 34   # bump to current Play requirement at release
      bundle.identifier: com.hopssh.app
      plugins.deep-link.schemes: ["hopssh"]

    gen/android/
      app/
        build.gradle.kts                   applies minSdk=26, targetSdk=34,
                                           compileSdk=34, kotlin jvmTarget=17
        src/main/
          AndroidManifest.xml              (see permissions + service declaration below)
          java/com/hopssh/
            MainActivity.kt                Tauri's generated host activity (unchanged)
            HopsshVpnService.kt            NEW — VpnService foreground service (~200 LOC)
            BootReceiver.kt                NEW — BOOT_COMPLETED → restart service if always-on
            CredentialStore.kt             NEW — Android Keystore wrapper (~80 LOC)
            NotificationHelper.kt          NEW — notification channel + ongoing notification
          res/
            values/strings.xml             notification text, permissions rationale
            drawable/                      ongoing notification icon (monochrome)
            xml/
              network_security_config.xml  allow localhost HTTP (for device-flow polling)
        libs/
          mobilehop.aar                    built from clients/mobile-go/ in CI

    plugins/hopssh-tunnel/                 custom Tauri plugin (shared with iOS)
      android/src/main/kotlin/com/hopssh/tunnel/
        TunnelPlugin.kt                    @Command start, stop, observeStatus,
                                           scanQR, deepLinkHandler (~150 LOC)
        QRScanActivity.kt                  CameraX + ML Kit barcode scanning (~100 LOC)
      src/lib.rs                           Tauri command stubs (shared iOS/Android)

  src/                                     Svelte UI (shared across all 5 platforms)
    lib/screens/
      EnrollmentQR.svelte                  REUSE — same component as iOS
      MobileConnect.svelte                 REUSE — same component as iOS
      MobileSettings.svelte                REUSE + Android-specific deep-links
                                           (battery optimization, always-on VPN settings)
      Terminal.svelte                      REUSE — no DPI hack needed on Android
```

What is new vs. reused:
- **Reuse**: Svelte dashboard components, `mobilehop.aar` (same Go binding as iOS), Tauri plugin Rust side, cross-platform enrollment flow.
- **New (Android-only)**: `HopsshVpnService.kt`, `BootReceiver.kt`, `CredentialStore.kt`, `NotificationHelper.kt`, `TunnelPlugin.kt` Android side, `QRScanActivity.kt`, network security config.

## AndroidManifest + permissions

```xml
<manifest>
  <!-- Camera for QR scan -->
  <uses-permission android:name="android.permission.CAMERA"/>

  <!-- Foreground service + specialized VPN type (API 28+) -->
  <uses-permission android:name="android.permission.FOREGROUND_SERVICE"/>
  <uses-permission android:name="android.permission.FOREGROUND_SERVICE_SPECIAL_USE"/>

  <!-- Notifications (runtime on API 33+) -->
  <uses-permission android:name="android.permission.POST_NOTIFICATIONS"/>

  <!-- Boot receiver for reconnect-on-boot -->
  <uses-permission android:name="android.permission.RECEIVE_BOOT_COMPLETED"/>

  <!-- Network state observation -->
  <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE"/>

  <!-- Internet (always needed) -->
  <uses-permission android:name="android.permission.INTERNET"/>

  <application>
    <activity android:name=".MainActivity" android:launchMode="singleTask"
              android:exported="true">
      <intent-filter>
        <action android:name="android.intent.action.MAIN"/>
        <category android:name="android.intent.category.LAUNCHER"/>
      </intent-filter>
      <!-- Deep link for hopssh://enroll?… -->
      <intent-filter android:autoVerify="false">
        <action android:name="android.intent.action.VIEW"/>
        <category android:name="android.intent.category.DEFAULT"/>
        <category android:name="android.intent.category.BROWSABLE"/>
        <data android:scheme="hopssh"/>
      </intent-filter>
    </activity>

    <service android:name=".HopsshVpnService"
             android:permission="android.permission.BIND_VPN_SERVICE"
             android:foregroundServiceType="specialUse"
             android:exported="true">
      <intent-filter>
        <action android:name="android.net.VpnService"/>
      </intent-filter>
      <property android:name="android.app.PROPERTY_SPECIAL_USE_FGS_SUBTYPE"
                android:value="vpn"/>
    </service>

    <receiver android:name=".BootReceiver"
              android:exported="true"
              android:enabled="true">
      <intent-filter>
        <action android:name="android.intent.action.BOOT_COMPLETED"/>
      </intent-filter>
    </receiver>
  </application>
</manifest>
```

**Runtime permission handling:**
- `CAMERA` — requested when user taps "Scan QR". Deny path → fallback to manual device flow.
- `POST_NOTIFICATIONS` (API 33+) — requested before starting the foreground service. Deny path → service still runs but without visible notification, which Android counts against our foreground-service compliance; prompt user once with a clear explanation.
- `BIND_VPN_SERVICE` — system-declared, not user-grantable. Declared in `<service>` tag so Android knows to route VPN intents here.
- **VpnService consent dialog** — `VpnService.prepare(ctx)` returns an Intent if the user has never granted VPN consent; we show the system dialog once. Subsequent starts skip this.

## Enrollment UX

Same overall flow as iOS (spec'd in [client-ios-plan.md](client-ios-plan.md)); Android-specific details:

- **QR scanning**: CameraX + ML Kit Barcode Scanning library. `QRScanActivity.kt` launches as a plugin command result, returns the decoded URL. Bundle size impact: ~4-6 MB for ML Kit — acceptable for the UX win.
- **Deep link**: `hopssh://enroll?token=…&endpoint=…` registered in the manifest intent-filter. Works from any Android app that renders URLs (Gmail, Messages, browser).
- **VpnService consent prompt**: after the server returns credentials, we call `VpnService.prepare(ctx)`. First time: the system VPN-consent dialog appears. User approves once. Subsequent reconnects don't prompt.
- **Post-approval**: start `HopsshVpnService` via `ctx.startForegroundService(…)`. The service reads credentials from `CredentialStore` (Android Keystore + EncryptedSharedPreferences), builds the VPN interface, starts the Go tunnel.
- **Fallback — device flow**: identical to iOS — enter control-plane URL, phone shows short code, user approves on a browser.

## Tunnel lifecycle + always-on

**Starting the tunnel** (`HopsshVpnService.onStartCommand`):
1. Read cert/key/agent token/config from `CredentialStore`.
2. Promote to foreground: `startForeground(NOTIFICATION_ID, buildOngoingNotification())`. Notification is persistent, non-dismissible, with "Connected — 3 peers" text.
3. `VpnService.Builder`:
   - `.setSession("hopssh")` (shown in iOS-equivalent VPN system UI)
   - `.addAddress(meshIP, 24)` → the device's mesh IP
   - `.addRoute(meshSubnet, subnetBits)` → route mesh traffic here
   - `.addDnsServer(meshDnsServer)` → user-defined mesh DNS
   - `.addSearchDomain(meshDomain)` → e.g. `home`, `prod`
   - `.setMtu(1300)` → Nebula MTU minus headroom
4. `.establish()` → returns `ParcelFileDescriptor`; `tunFd = parcelFd.detachFd()`.
5. `MobileHop.NewClient(tunFd, configJSON).Start()`.
6. Register `ConnectivityManager.NetworkCallback` → on `onCapabilitiesChanged` / `onLinkPropertiesChanged`, call `MobileHop.Rebind()` (mapped to the same Nebula `RebindUDPServer()` + `CloseAllTunnels(true)` recovery path from [cmd/agent/nebula.go](../cmd/agent/nebula.go)).
7. Return `START_STICKY` — OS auto-restarts the service if killed.

**Stopping** (user disconnect or uninstall):
- `MobileHop.Stop()` (clean shutdown — the vendored `patches/nebula-1031-graceful-shutdown.patch` is active).
- `stopForeground(STOP_FOREGROUND_REMOVE)`.
- `stopSelf()`.

**Always-on VPN — two layers:**

1. **App-level always-on** (default ON): a preference in `EncryptedSharedPreferences`. Controls:
   - `BootReceiver` behavior: if ON → start `HopsshVpnService` on `BOOT_COMPLETED`.
   - `START_STICKY` — already set, so even if the app is killed the service restarts. Combined with the app-level preference, the service respects the user's "always on" choice across restarts.

2. **OS-level always-on VPN** (opt-in via system settings): stronger guarantee — Android blocks network access until our VPN is up. Configured in Settings → Network → Advanced → VPN → hopssh → Always-on. We cannot toggle this programmatically; we can only deep-link to the settings page with `Intent(Settings.ACTION_VPN_SETTINGS)` and explain in an in-app tip.

Recommended UX: app-level always-on ON by default (no user action). In Settings, a tip: "For maximum reliability, enable OS-level always-on VPN → opens Android Settings".

**Battery optimization exemption:**
- Android's battery optimizer can kill background apps. Foreground services with `specialUse` type are exempt from Doze, but some OEMs (Xiaomi, OPPO, Huawei) have aggressive custom killers.
- Prompt users in Settings: "On some devices, battery optimization may disconnect the VPN. Tap here to whitelist hopssh." Deep-link to `Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` with our package.

## Web terminal specifics (Android WebView)

Android WebView uses Chromium (since Android 5) and is generally well-behaved — none of the WKWebView custom-scheme DPI bugs.

**Known gotcha:** [xtermjs#4597](https://github.com/xtermjs/xterm.js/issues/4597) — Android WebView becomes sluggish at column widths >200. Cap at 160 cols (same as iPad) for safety. No issue at normal phone column counts (80-120).

**Keyboard handling:**
- Android's soft keyboard behavior is governed by `windowSoftInputMode` on the Activity. Set to `adjustResize` (default for Tauri's generated MainActivity). This pushes the WebView content up when the keyboard appears — no scroll-out-of-bounds bugs equivalent to [tauri#9907](https://github.com/tauri-apps/tauri/issues/9907) on iOS.
- The `visualViewport` API works correctly in Android WebView → no CSS workarounds needed.

**Copy/paste:**
- Standard WebView clipboard. `navigator.clipboard.readText()` works on API 26+ with a slight permission dialog quirk on first use.
- Fallback plugin command `invoke("paste_from_clipboard")` (shared with iOS) for consistency.

## Credential storage

Single-process means no shared-keychain complexity. Storage:

| Item | Where |
|---|---|
| `ca-cert`, `node-cert`, `network-config-json` | `EncryptedSharedPreferences` (Jetpack Security `MasterKey`) |
| `node-private-key`, `agent-token` | Same, but with hardware-backed Keystore key |
| `always-on-enabled` | Plain `SharedPreferences` (non-secret) |

**Helper**: `CredentialStore.kt` (~80 LOC) — Jetpack Security `MasterKey` + `EncryptedSharedPreferences`. AES-256-GCM encryption via Android Keystore-backed key. Hardware-backed on most modern devices (StrongBox on Pixel 3+, Samsung Knox).

**On uninstall**: Android automatically wipes app data including encrypted prefs. No cleanup needed.

**On data restore (Google Backup)**: include encrypted prefs in the backup (default). If the user restores on a new device with the same Google account, enrollment state survives. This matches iOS's Keychain-survives-reinstall behavior.

## Distribution + Play Store reviews

**Google Play Console account**: prerequisite ($25 one-time fee).

**Play App Signing**:
- Google manages the app signing key (we upload an Android App Bundle signed with our upload key).
- Play generates and signs per-device APKs from the bundle.
- We keep the upload key in CI secrets; Google handles the signing key (can't be leaked from our side).

**VPN category + content rating**:
- Category: Tools (Play has no dedicated VPN category; Tools is standard for VPN apps per Play's own guidance).
- Google Play's **VPN policy** applies: must be the primary functionality, must not host ads that sell VPN services, must declare data collection in the Data Safety form.

**Data Safety form**:
- Declare: IP address (collected, not shared, used for service functionality — control plane heartbeat).
- Device name (collected, not shared, service functionality — node identification).
- No encryption for data in transit? We encrypt (Nebula Noise/ChaCha20-Poly1305 E2E). Mark as encrypted.

**Privacy manifest** (Play-equivalent): link to hopssh.com privacy policy; mention the self-hosted option where the user runs everything.

**Play Review**:
- Typical turnaround: 1-3 days for a new app. First submission often takes longer (7-14 days) due to VPN policy scrutiny.
- Pre-launch report: Play runs automated tests on a range of devices; fix any crashes surfaced.

**Target SDK compliance**:
- Play Store requires target SDK within 1 year of current (currently API 34 at April 2026; API 35 likely required by Aug 2026). Plan to bump targetSdk annually.

**Internal Testing**:
- Enable Week 8. Up to 100 internal testers.
- Closed Testing (private beta) Week 9-10 for broader pre-release.
- Production release after Closed Testing feedback.

## Implementation sequencing

Builds on top of the shared Tauri scaffold (Week 0-3 per strategy doc). Android-specific work starts Week 4 and runs in parallel with iOS.

1. **Week 4 — Android target scaffold (GO/NO-GO GATE).**
   - `cargo tauri android init`.
   - Configure `minSdkVersion=26`, `targetSdkVersion=34`, compile + run on a physical device.
   - Empty app that shows the Svelte UI. Prove AAB build + upload to Play Internal Testing.
   - If blocked by Tauri Android issues → pause and re-evaluate per strategy doc fallback.

2. **Week 5 — HopsshVpnService + mobilehop.aar integration.**
   - Implement `HopsshVpnService.kt` with foreground service lifecycle.
   - Wire `mobilehop.aar` (built from `clients/mobile-go/`, shared with iOS).
   - Hardcoded test config: verify tunnel connects to a peer.
   - Verify service survives activity destruction, low-memory kill, BOOT_COMPLETED.

3. **Week 5 — Tauri Android plugin (parallel).**
   - `TunnelPlugin.kt` exposes start/stop/observeStatus to JS via Tauri's `@Command` system.
   - `QRScanActivity.kt` with CameraX + ML Kit.
   - Deep-link handler integrated with `MainActivity` (singleTask launch mode).

4. **Week 6 — Enrollment flow.**
   - Reuse `EnrollmentQR.svelte` from iOS work; no changes.
   - Wire camera plugin + deep-link handler.
   - Flow: scan QR → VpnService consent prompt → service starts → mesh up.

5. **Week 7 — Connect UX, peer list, terminal, always-on.**
   - `MobileConnect.svelte` and `MobileSettings.svelte` (shared with iOS).
   - Battery optimization whitelist prompt.
   - Deep-link to OS-level always-on VPN settings.
   - Terminal (no DPI hack needed; just col cap at 160).

6. **Week 8 — Polish + Play Internal Testing + upload to Closed Testing track.**
   - Dogfood on team Androids.
   - Crash reporting: Firebase Crashlytics or Sentry Android SDK (wire natively in Kotlin; no Tauri-plugin dependency needed).
   - Bug triage, performance profiling (Android Studio Profiler).

7. **Week 9-10 — Closed Testing + Production submission.**
   - Expand beta. Finalize Data Safety form, privacy policy, screenshots, Play listing.
   - Submit to Production.

## Verification

On real devices spanning at least:
- A stock Android phone (Pixel) — baseline behavior.
- A Samsung or Xiaomi device — aggressive battery killer ecosystem.
- A tablet — responsive layout check.

**Fresh install + enrollment (QR):**
- Install via Play Internal Testing.
- Open app → "Scan QR" → camera permission prompt → grant.
- Scan QR → VPN consent dialog appears ("Connection request… hopssh wants to set up a VPN connection") → approve.
- Notification permission prompt (API 33+) → grant.
- Persistent notification appears with connected status.
- Tunnel connects within ~3 seconds; mesh IP visible in status view.

**Enrollment (device-flow fallback):**
- Deny camera permission intentionally.
- Manual control-plane URL entry → short code → approve on laptop.
- Same post-approval path succeeds.

**Deep-link enrollment:**
- Send `hopssh://enroll?…` via Messages, tap in the app.
- App opens straight into post-approval flow.

**Tunnel lifecycle:**
- Close the app (swipe away from Recents). VPN stays up (check `ping` from another peer to this device's mesh IP, or view in Android Settings → Network → VPN).
- Reopen app: status view correctly reflects still-connected state.
- Force-stop the app (Settings → Apps → hopssh → Force stop). Service stops. Reopen the app — it re-enters disconnected state. (Force-stop is the nuclear option; regular swipe-away shouldn't kill the service.)
- Reboot device. With app-level always-on ON: `BootReceiver` starts the service within ~30s of boot.

**Network transitions:**
- WiFi ↔ cellular: mesh reconnects within ~3s (ConnectivityManager callback → `Rebind()`).
- Airplane mode ON for 2 min, OFF: mesh recovers within ~5s.
- Lock for 5+ min, unlock: still connected.
- Doze mode (`adb shell dumpsys deviceidle force-idle` + wait): foreground service with VPN specialUse is exempt; mesh stays up.

**Always-on OS-level setting:**
- Enable in Settings → Network → VPN → hopssh → Always-on. Also enable "Block connections without VPN".
- Force-stop the app. Observe that all network access is blocked (as intended) until the app/service restarts.

**Memory + battery:**
- Android Studio Profiler: service process <120 MiB sustained under continuous traffic.
- Battery Historian: foreground service shows as active; no wakelock surprises.

**Terminal:**
- xterm.js renders at correct scale (no Android-specific DPI fixes needed).
- Soft keyboard push-up works; content stays visible.
- Paste works via toolbar.

**Uninstall:**
- Uninstall from Play / Settings → Apps.
- Android automatically removes the VPN profile (visible in Settings → VPN).
- Encrypted prefs wiped. Reinstall: fresh enrollment required.

## Android-specific risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Aggressive OEM battery killer (Xiaomi, OPPO, Huawei) kills the service | Medium | Medium | Deep-link to battery-optimization whitelist in Settings. Document in the FAQ. Encourage OS-level always-on VPN (Android respects this even under aggressive killers). |
| Play Store VPN policy rejection | Low | Critical | First submission can take 7-14 days due to extra scrutiny. Data Safety form must be accurate; privacy policy URL must be live. Submit early (Week 8). |
| Notification-permission denied on API 33+ → foreground service non-compliant | Medium | Medium | Clear rationale prompt. Degraded mode: service runs but with a generic system notification (worse UX but not broken). |
| Target SDK annual bump | Low | Low | Known compliance treadmill. Budget ~1 engineer-week per year to bump compile + target SDK, address deprecated APIs. |
| mobilehop.aar ProGuard / R8 obfuscation breaks JNI | Low | High | Add keep rules for `com.hopssh.mobilehop.**` (standard gomobile pattern). Test release builds early. |
| Android auto-revoke permissions (API 30+) strips CAMERA if unused | Low | Low | User can re-grant in Settings. App detects revoked camera permission and re-prompts on next QR scan. Not a big issue — users open the app occasionally anyway. |
| VpnService.prepare() consent dialog re-appears unexpectedly | Low | Medium | Android sometimes re-triggers consent after system updates. Handle the case: if `prepare()` returns non-null, show the consent intent again; don't crash. |
| Play app signing key compromise | Low | Critical | Google manages it — not a key-hygiene concern for us. Upload key stays in CI secrets only; rotate annually. |
| Kotlin → Go (mobilehop) call crashes under high-throughput | Low | High | gomobile is battle-tested by DefinedNet's `mobile_nebula` + many other projects. Profile in Week 5 with sustained 10 MB/s traffic. |

## Open questions

1. **Play Console ownership**: who owns the hopssh developer account? Can multiple team members have access? Confirm Week 0.
2. **Firebase vs Sentry for crash reporting**: Firebase Crashlytics is free and Android-native; Sentry gives cross-platform consistency with the Swift Sentry SDK on iOS. **Recommend** Sentry for consistency (one dashboard across all platforms).
3. **ML Kit dependency size**: barcode scanning adds ~5 MB to the APK. Alternative: ZXing (lighter, less accurate on dim/tilted QR). **Recommend** ML Kit — the UX win on poor-light scanning justifies the size.
4. **Should the notification show peer count?** E.g., "hopssh — Connected, 3 peers". Requires wiring the peer count from the Go tunnel to the Kotlin notification. Minor addition, good feedback loop for users. **Recommend** yes.
5. **In-app VPN kill switch** (block non-VPN traffic at app level, separate from Android's OS-level setting): competitors don't do this (they rely on the OS toggle). **Recommend** skip for v1; rely on OS always-on.
6. **Android Auto / Wear OS / ChromeOS compat**: out of scope for v1. Android app on ChromeOS works by default through Android Runtime — no extra work. Auto/Wear not applicable to a VPN app.
7. **Tablet-specific UX**: responsive Svelte UI handles layout automatically. No separate tablet manifest entry needed. Confirm at Week 7 polish.

---

## Lessons from macOS Phase V→DD (2026-04-28 → 2026-05-07)

The macOS desktop client shipped 12 versions in a tight one-week iteration ([[client-macos-architecture]] is now status: accepted, shipped v0.10.96). Six production-discovered patterns affect the Android plan when implementation begins:

### 1. Service-restart recovery (Phase X)
On macOS the cached attach endpoint went stale after `launchctl kickstart`. Android's analogue: **`HopsshVpnService` can be killed under memory pressure** and restarted via `START_STICKY` with a null intent. When that happens the service comes up but UI-side cached state may be stale.

**Apply to Android:** the `TunnelPlugin.kt::observeStatus` callback fires on service state transitions. On `STARTED → null intent → STARTED` (the START_STICKY recovery path), invalidate JS-side caches AND re-fetch credentials from `CredentialStore`. The pattern mirrors Phase X's `try_attach_system_agent` retry-loop in macOS.

### 2. Boot-on-completion + always-on (Phase Y)
macOS: `tauri-plugin-autostart` LaunchAgent, default OFF on fresh install, auto-flip ON on first system-mode conversion (with `start_at_login_explicit` sentinel preserving user override).

**Android already has the right primitive: `BootReceiver` + always-on preference (already in this plan).** Apply the macOS pattern: default OFF on fresh install, auto-flip ON when user first enrolls successfully (Android has no system-mode-vs-bundled distinction; enrollment IS the commitment). Once user has explicitly toggled, never auto-override.

### 3. Process-crash + battery-killer race (Phase Z analogue)
macOS hit a boot-before-login race for system-mode mirror files. Android has its own race: `BootReceiver` fires on `BOOT_COMPLETED`, BUT `EncryptedSharedPreferences` requires the device to be unlocked at least once before the AES key can be unwrapped (the `MasterKey` is hardware-keystore-backed and unwrapped after-first-unlock).

**Apply to Android:** `BootReceiver.onReceive()` should NOT immediately try to start the foreground service if `CredentialStore.isAccessible()` returns false — schedule a `JobScheduler` job that retries every 30s until the keystore unwraps successfully. Same self-heal pattern as Phase Z's `runMirrorChownSelfHeal` goroutine. Aggressive OEM battery killers (Xiaomi/OPPO/Huawei) make this even more important — the service may need multiple restart attempts.

### 4. Uninstall hygiene (Phase AA)
On macOS, Phase Y's autostart LaunchAgent was missed by the uninstall sweep — left a stale launchd entry post-uninstall. **Android handles this atomically — uninstall wipes app data including `EncryptedSharedPreferences`, removes the VPN profile, removes scheduled jobs.** Same as iOS, structurally simpler.

**One Android-specific gotcha:** if the user has enabled **OS-level always-on VPN** for hopssh in Android Settings, that setting persists across uninstall+reinstall (it's keyed by package name, not app-data-bundled). On reinstall the user gets reconnected automatically without re-enrolling — UX-wise this MIGHT be desirable (hopssh "just works" again) or unexpected (user thought uninstall meant fresh start). Document the behavior; tripwire test: source-scan that `MobileSettings.svelte` mentions OS-level always-on persistence.

### 5. UX copy: forbidden-jargon source-scan tripwire (Phase BB)
macOS landed `lib.rs::tests::user_facing_copy_has_no_protocol_jargon` source-scanning Svelte source for banned terms.

**Apply to Android:** the Svelte UI is shared, so the same tripwire test runs automatically. Android-specific copy additions: notification text ("Connected — 3 peers"), QR scanner permission rationale, battery-optimization-prompt copy, Always-on tip in Settings. Material Design encourages plain-English ("Connect to network", not "Bring up mesh") same way Apple HIG does. Affirmative toggles ("Show notifications" not "Hide notifications") apply equally.

### 6. Three independent watchdogs (Phase DD)
The mobile gomobile core inherits the renewal + watcher watchdogs through `internal/client/`. **`debug.SetGCPercent(20)` in mobilehop is harmless on Android** (no 50 MiB cap; standard app memory budget is hundreds of MB). Three watchdog goroutines + their forensic dump infrastructure cost <5 MiB sustained — negligible on Android's heap.

**Hard timeouts on vendor-Nebula calls:** Phase DD added `runWithTimeout` wrappers because deadlocks aren't panics. The same wrappers carry to `mobilehop.Rebind()` callers in `HopsshVpnService.kt`. The `ConnectivityManager.NetworkCallback → Rebind()` path MUST use the timeout-wrapped version — a wedged Rebind during a WiFi/cellular handoff would freeze the service's path-change handling indefinitely.

**Forensic dump path:** `<filesDir>/<network>/<class>-stuck-<ts>.txt` (Android's app-private files dir). Existing `inst.dir()` already abstracts this — the gomobile core works without modification.

See [[../concepts/watchdog]] for the three-watchdog architecture, [[../incidents/2026-05-07-mbp-watcher-wedge]] for the motivating incident, and [[../concepts/desktop-client]] for the macOS shipped state these lessons came from.
