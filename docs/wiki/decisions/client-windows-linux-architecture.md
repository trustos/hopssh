---
type: decision
title: Windows + Linux desktop client architecture (delta on top of macOS)
status: foundation-shipped
last_compiled: 2026-05-09
sources:
  - cmd/agent/wintun
  - cmd/agent/service_windows.go
  - cmd/agent/dnsproxy_windows.go
  - cmd/agent/dns_linux.go
  - cmd/agent/service.go
---

# Windows + Linux desktop client architecture (delta on top of macOS)

## Decision basis

The reasoning that produced this decision came from:

- **Code I read:** `cmd/agent/wintun/` (Windows kernel TUN integration, already shipped), `cmd/agent/service_windows.go` (SCM service, shipped v0.9.9), `cmd/agent/dnsproxy_windows.go` (NRPT-bypass DNS forwarder, shipped), `cmd/agent/dns_linux.go` (systemd-resolved drop-in fallback, shipped), `internal/selfupdate/selfupdate.go` (Windows rename-swap trick).
- **Wiki pages I consulted:** [[client-macos-architecture]] (the inherited desktop baseline), [[../concepts/desktop-client]] (shipped state from which lessons are derived).
- **External sources I fetched:** Tauri 2 docs for NSIS/MSI/AppImage/.deb/.rpm bundling, Microsoft Authenticode + EV cert documentation, freedesktop.org autostart spec, Ubuntu/Fedora/Debian package signing conventions.
- **Prior-knowledge claims (with confidence):**
  - [HIGH] WinTun + SCM service work in production — shipped agent-side since v0.9.9.
  - [HIGH] systemd-resolved per-link DNS with non-53 ports is broken across Ubuntu LTS line — verified empirically (CLAUDE.md Discovery Log entry).
  - [HIGH] GNOME 3.26+ removed legacy tray icons; AppIndicator extension required — verified against GNOME release notes.
  - [MEDIUM] Standard Authenticode reputation builds in ~30 days / few thousand downloads — recalled from Microsoft SmartScreen documentation.
  - [LOW] Snap VPN permissions model is "awkward" — recalled from training data; not directly verified in 2026.

## Status

**Foundation shipped (Stages 5+6, v0.11.11).** Agent-side platform plumbing (WinTun, SCM, NRPT-bypass DNS proxy on Windows; systemd, dnsproxy on Linux) was already shipped via the existing `internal/client/` codebase — see `internal/client/service_windows.go`, `internal/client/dnsproxy_windows.go`, `internal/client/dns_linux.go` (post-Phase-NN paths). The Tauri shell now also has:

- **`tauri.conf.json` cross-platform bundle config** — `bundle.windows` (NSIS + MSI/WiX), `bundle.linux` (.deb + .rpm + AppImage with the right runtime-deps list).
- **CI build jobs** — `.github/workflows/release-desktop.yml` has a `build-windows` job (windows-latest runner, cross-compiles `hop-agent.exe`, runs `tauri build --bundles msi,nsis`) and a `build-linux` job (ubuntu-22.04 runner, installs WebKit2GTK + GTK3 + AppIndicator dev deps, runs `tauri build --bundles deb,rpm,appimage`). Artifacts attached to GitHub release on tag push.
- **Windows Rust analogues for the load-bearing flows** — `install_system_service` / `uninstall_system_service` in `lib.rs` now use a `run_windows_elevated` helper that invokes PowerShell `Start-Process -Verb RunAs` to UAC-elevate `hop-agent.exe install` / `uninstall`. Other macOS-only commands (osascript-driven Terminal handoff, /etc/resolver, /Applications-paths) keep returning errors on non-macOS — they're macOS-specific UX papercut features that have non-load-bearing fallbacks.

**What's verified:** `cargo check` + the 41 cargo unit tests pass on macOS post-changes (no regression). CI builds will surface any cross-compile errors on the next release tag.

**What's NOT yet verified:** the resulting `.msi` / `.AppImage` actually installs and runs on a real Windows / Linux machine. The user-facing UX (system-tray rendering, autostart-on-login, window decorations, system-mode install dialog) needs hardware-in-the-loop testing before Windows + Linux can be marked production-ready. Tracking those gaps:

- **Windows:** verify NSIS installer enrols the agent + spawns it on first launch + tray icon renders + UAC dialog appears on system-mode upgrade. Autostart via the registry's `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` key is NOT yet wired in the Rust shell — Phase Y autostart on macOS uses `tauri-plugin-autostart`, which DOES support Windows registry autostart out of the box, but I haven't confirmed the existing call site in `setup()` works through to Windows on a real machine.
- **Linux:** verify .deb/.rpm/AppImage on Ubuntu 24.04 + Fedora 40 (the existing agent-side test matrix). DBus-based autostart (`~/.config/autostart/hopssh.desktop` file) needs writing — `tauri-plugin-autostart` supports Linux too but has caveats around `.desktop` file location depending on distro.

Implementation can start immediately after [[client-macos-architecture]]'s shell stabilizes (already shipped at v0.10.96); the delta here describes only what changes vs the macOS ADR, NOT a full re-specification.

See [[../concepts/client-strategy]] for the overall 5-platform strategy and [[client-macos-architecture]] for the inherited desktop baseline.

**This doc is a delta document** — it covers Windows and Linux as platform-specific additions on top of the macOS plan.

All of the following are inherited from the macOS plan unchanged:
- Sidecar-based architecture (Tauri app spawns `hop-agent` child process; agent ↔ UI talk over a local HTTP API on `127.0.0.1:<random-port>` with a `HOPSSH_READY:` stdout handshake).
- Userspace Nebula (gvisor netstack) by default — zero elevation at first launch.
- Optional one-time "Install system component" upgrade to kernel TUN via the OS's service manager.
- Optional "Install Command Line Tools" flow (symlinks/PATH entry to `hop`).
- Menu bar / system tray primary UX; main window for dashboard + terminal.
- Auto-update via Tauri updater.
- Tauri 2 + Svelte 5 frontend, shared with iOS and Android.
- Enrollment: device-flow + QR (QR useful here too when admins paste a link into Signal/Slack and the user opens it on the same machine).

Where Windows or Linux behaves differently, this doc spells out the delta.

---

## Part 1 — Windows

### Architecture deltas

- **TUN driver**: WinTun (Go-accessed via the existing [cmd/agent/wintun/](../cmd/agent/wintun) package — already shipped). No user-space alternative on Windows matches kernel TUN performance; we still default to userspace Nebula first to avoid the elevation prompt, but the kernel-TUN upgrade is the more common path on Windows because WinTun offers a meaningfully better experience than gvisor netstack there.
- **Service model**: Windows Service via SCM. The existing [cmd/agent/service_windows.go](../cmd/agent/service_windows.go) has been SCM-compatible since v0.9.9 (per the Discovery Log in CLAUDE.md). Service runs as `LocalSystem` (needed for WinTun). Auto-restart recovery already configured (5s/5s/30s).
- **DNS**: Windows NRPT strips port suffixes silently (per CLAUDE.md Discovery Log). Mitigated by the existing [cmd/agent/dnsproxy_windows.go](../cmd/agent/dnsproxy_windows.go) — a miekg/dns-based local DNS forwarder on `127.53.0.1:53`. NRPT registers the loopback. This is **already shipped** — no new code.
- **Self-update**: Windows cannot overwrite a running `.exe`; the existing rename-swap trick in [internal/selfupdate/selfupdate.go](../internal/selfupdate/selfupdate.go) (rename `<exe>` → `<exe>.old`, place new binary, `sc.exe stop/start`) is already shipped. Tauri updater hooks into this flow for the desktop app wrapper itself.
- **WebView**: Microsoft Edge WebView2 (Chromium). Tauri requires WebView2 Runtime on the target machine. Modern Windows 11 ships it; Windows 10 users may need to install. **Bundle the Evergreen WebView2 Bootstrapper in the NSIS/MSI installer** (Tauri supports this via `bundle.windows.webviewInstallMode = "embedBootstrapper"` or `"downloadBootstrapper"`).
- **Privilege escalation (kernel-TUN upgrade)**: UAC prompt via `ShellExecuteW` with the `"runas"` verb, invoking the agent binary with `hop-agent install-service`. Replaces the macOS `osascript` helper.
- **System tray**: Windows system tray via Tauri. Supports balloon notifications; use for connection-state change feedback.

### Distribution + signing

- **NSIS** (`.exe` installer) and **MSI** (Windows Installer) — both produced by Tauri from the same codebase. NSIS is the default download; MSI is linked in the download page under "Enterprise / MSI".
- **Target triples**: `x86_64-pc-windows-msvc` (standard) and `aarch64-pc-windows-msvc` (Windows on ARM — growing segment thanks to Snapdragon X laptops). Ship both from day 1.
- **Code signing**:
  - **Standard Authenticode cert** (~$200-400/yr from DigiCert, SSL.com, etc.) is the v1 target. Downside: SmartScreen reputation must build over ~30 days / few thousand downloads before the "Windows protected your PC" dialog goes away for new installers.
  - **EV (Extended Validation) cert** (~$300-700/yr, requires HSM/YubiKey) gives immediate SmartScreen trust at the cost of harder CI integration and more expensive ceremony. **Recommend standard for v1**, upgrade to EV if the unsigned-reputation UX hurts adoption.
  - Sign both the installer AND the embedded `hop-agent.exe`. Sign the app EXE as well.
- **Auto-update**: Tauri updater works with NSIS artifact URLs the same way it does with DMG / AppImage. The updater downloads the new NSIS installer and runs it with the existing app's uninstaller in silent mode, then relaunches. Agent self-update (the daemon, if installed as a service) uses the rename-swap trick.
- **Release pipeline**: GitHub Actions on `windows-latest`. Build x64 + ARM64 binaries, sign via `signtool` with the cert imported into the CI Windows keychain, produce NSIS + MSI, upload to the same `releases.hopssh.com` bucket as macOS.

### Windows-specific project additions

Under `clients/desktop-mobile/src-tauri/`:

```
windows/
  installer-nsis.nsi             Tauri template; customize install paths,
                                 shortcut creation, WebView2 bootstrap
  installer-wix.wxs              MSI template
  agent-service.ps1              PowerShell helper invoked from the Tauri app
                                 to install/uninstall the hop-agent SCM service
                                 (wraps the existing `hop-agent install` subcommand)
  webview2-bootstrap.exe         Evergreen bootstrapper bundled for offline install
src/
  win_helper.rs                  NEW — ShellExecute runas for UAC prompt
  tray.rs                        Shared — Windows tray rendered from same code
```

### Windows-specific risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| SmartScreen "Unknown publisher" warnings deter adoption (pre-reputation) | High | Medium | Standard Authenticode cert + visible "Verified Publisher: HopSSH" on the UAC dialog. Document the "More info → Run anyway" workaround on the download page. Plan EV upgrade if install-rate suffers. |
| WebView2 Runtime missing on older Windows 10 | Medium | Low | Bundle the bootstrapper (~100 KB) or fall back to `downloadBootstrapper` for online installs. |
| UAC prompt fatigue | Medium | Low | Only prompt once per kernel-TUN upgrade OR CLI-install OR auto-update cycle. Never on every launch. |
| ARM64 Windows niche bugs (QEMU tests show some limitations per CLAUDE.md) | Low | Medium | Test on a real Snapdragon X device before shipping ARM64 builds. Keep x64 as default download; ARM64 as explicit opt-in. |
| WinTun driver install requires reboot on some older Windows 10 builds | Low | Medium | Existing `cmd/agent/wintun/` handles this — document it in the kernel-TUN upgrade dialog ("A restart may be required"). |
| Code-signing cert key compromise | Low | High | HSM for EV (enforced); YubiKey for standard recommended. Rotate annually. |

---

## Part 2 — Linux

### Architecture deltas

- **TUN driver**: Linux kernel TUN (rock-solid, standard). Requires `CAP_NET_ADMIN` (or running as root / via systemd with `AmbientCapabilities=CAP_NET_ADMIN`). Userspace Nebula (gvisor) remains the first-launch default.
- **Service model**: systemd. The existing [cmd/agent/service.go](../cmd/agent/service.go) handles systemd unit install. Two flavors:
  - **User unit** (`~/.config/systemd/user/hopssh.service`) — userspace mode, no root needed. Not dependent on kernel TUN.
  - **System unit** (`/etc/systemd/system/hopssh.service`) — kernel TUN mode, installed during "Install system component" flow via `pkexec` (polkit) or the classic `sudo` fallback.
- **DNS**: systemd-resolved's per-link DNS with non-53 ports is broken across Ubuntu LTS releases (verified in CLAUDE.md 2026-04-17 + 2026-04-18). Mitigated by the existing [cmd/agent/dns_linux.go](../cmd/agent/dns_linux.go) — probes the stub, falls back to a drop-in `/etc/systemd/resolved.conf.d/hopssh.conf` with `DNS=<ip>:<port> Domains=~<domain>` + `systemctl reload-or-restart systemd-resolved`. **Already shipped** — no new code.
- **WebView**: WebKitGTK (`libwebkit2gtk-4.1`). Less polished than WebView2 / WKWebView — known differences:
  - **Video decoding** may need `gst-plugins-bad` + `gst-plugins-ugly` for H.264. We don't need video decoding; noted for awareness.
  - **Theme follow** can be inconsistent between GNOME's preferred color scheme and GTK3 vs GTK4. Tauri uses GTK3 WebKitGTK by default; acceptable for our monochrome tray aesthetic.
  - **Hardware acceleration** can be quirky on Wayland; most users won't notice for a VPN tray app. Disable HW accel if we see rendering bugs (one Tauri setting).
- **System tray**: This is the single most annoying desktop-environment issue on Linux.
  - **GNOME 3.26+** removed legacy tray icons. Users need the [AppIndicator extension](https://extensions.gnome.org/extension/615/appindicator-support/) to see Tauri's tray. Document this prominently.
  - **KDE, XFCE, Cinnamon, MATE, Budgie** all have native tray support via StatusNotifier.
  - Tauri uses `libappindicator3` / `libayatana-appindicator3`. Add as an explicit dependency in the `.deb` / `.rpm` control files.
  - If the tray is missing (GNOME without the extension), fall back to a standalone main window as the primary UX — don't leave users without a way to open the app.

### Distribution + signing

- **AppImage** (universal, self-contained) — primary for direct download. Tauri updater works with AppImage URLs. Bundles libraries internally; no distro-specific dependencies.
- **.deb** (Debian, Ubuntu, Mint, Pop!_OS, Linux Mint, elementary OS) — integrates with apt. Signed with a GPG key; users add our signing key + a repo line, or install the standalone .deb. v1: ship the standalone .deb; a proper apt repo is a v1.1 nice-to-have.
- **.rpm** (Fedora, RHEL, openSUSE, Rocky, AlmaLinux) — integrates with dnf/yum/zypper. Signed with the same GPG key. Same shape as .deb for v1 (standalone).
- **Target architectures**:
  - `x86_64-unknown-linux-gnu` (standard desktop Linux)
  - `aarch64-unknown-linux-gnu` (Raspberry Pi 4+, Apple Silicon Linux VMs, Ampere servers — high value for selfhoster audience)
- **Signing model**:
  - AppImage: optional `--sign` via the AppImage tool; weak trust model (no CA). Use primarily as an integrity check alongside SHA-256 checksums published on hopssh.com.
  - .deb: `dpkg-sig -k <key-id>` signing. Publish GPG pubkey at `https://hopssh.com/gpg-key.asc`.
  - .rpm: `rpm --addsign` with the same GPG key.
- **Auto-update**:
  - AppImage: Tauri updater downloads new AppImage + signature, replaces the current file, prompts relaunch.
  - .deb / .rpm: **no in-app auto-update**. System package manager handles updates. Document this clearly — users who installed via apt/dnf update the normal way.

### Linux-specific project additions

Under `clients/desktop-mobile/src-tauri/`:

```
linux/
  deb-postinst.sh                adds hopssh-agent user, sets up directories,
                                 optionally enables systemd service
  deb-prerm.sh                   stops services on uninstall
  rpm-spec.in                    RPM spec template
  appimage-recipe.yml            AppImage build config
  hopssh.desktop                 .desktop file for app launcher menus
  hopssh.service.template        systemd user unit template
  hopssh-system.service.template systemd system unit template (kernel TUN mode)
src/
  linux_helper.rs                NEW — pkexec wrapper for kernel-TUN upgrade
  tray.rs                        Shared — Linux tray rendered from same code
```

### Linux-specific risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| GNOME users have no tray icon without the AppIndicator extension | High | Medium | In-app onboarding detects GNOME + missing extension; shows a one-time banner linking to the extensions.gnome.org page. Fallback UX: the main window stays open on launch for GNOME-without-extension users. |
| Wayland-specific WebKitGTK rendering quirks | Low | Low | Test on Fedora (Wayland default) and Ubuntu 22.04+ (Wayland default). Fall back to X11 backend via `GDK_BACKEND=x11` if needed. |
| systemd-resolved DNS bug on Ubuntu LTS (already known) | High | High | **Mitigated** — existing `cmd/agent/dns_linux.go` has the drop-in config fallback. Add a self-diagnostic log + user-visible "DNS fallback active" indicator in the dashboard. |
| .deb/.rpm signing key compromise | Low | High | Store signing key in GPG-encrypted form in CI secrets; private key only on an air-gapped Yubikey. Rotate every 2 years per Debian convention. |
| AppImage integration missing on some distros (no AppImageLauncher) | Medium | Low | Document: chmod +x + run. Recommend AppImageLauncher. |
| Legacy distros lacking `libayatana-appindicator3` | Medium | Low | Bundle the library in AppImage; declare as a .deb/.rpm dependency. For fallback, open the main window if tray registration fails. |
| Pre-release Linux kernels breaking TUN via some systemd-networkd change | Low | Medium | Match CI matrix to Ubuntu LTS + latest + Fedora stable; test before every release. |

---

## Part 3 — Shared desktop CI additions

Extend the existing CI pipeline (`.github/workflows/`) beyond the macOS-only additions in [client-macos-plan.md](client-macos-plan.md):

```
release-desktop.yml
  macos-latest runner          builds darwin/universal, DMG, notarize (existing)
  windows-latest runner        NEW — builds x64 + ARM64 hop-agent,
                                     signs via signtool + YubiKey,
                                     Tauri build → NSIS + MSI,
                                     signs the installers,
                                     uploads to releases.hopssh.com
  ubuntu-latest runner         NEW — builds linux/amd64 + linux/arm64 hop-agent
                                     (cross-compiled to arm64 via docker buildx),
                                     Tauri build → AppImage + .deb + .rpm,
                                     GPG-signs the .deb and .rpm,
                                     uploads to releases.hopssh.com
```

Updater manifest per platform. A single Tauri updater endpoint pattern:
`https://releases.hopssh.com/<platform>/<target>/<current-version>` → JSON with update URL + signature. Tauri's updater figures out the right target at build time.

---

## Part 4 — Combined verification checklist

Beyond the macOS verification already in `client-macos-plan.md`:

**Windows:**
- Install from NSIS on Windows 11 Home, Windows 11 Pro, Windows 10 22H2.
- SmartScreen dialog: "More info → Run anyway" works. Verified Publisher shows as "HopSSH" or the cert CN.
- Userspace enrollment on fresh user with no admin rights — works without elevation.
- Kernel-TUN upgrade → UAC prompt → SCM service `hopssh-agent` appears in `services.msc`.
- Run `ipconfig /all` — see the WinTun adapter with mesh IP and mesh DNS domain.
- `nslookup <host>.<domain>` — resolves via the local DNS forwarder on 127.53.0.1.
- Suspend / resume laptop — mesh reconnects within ~3s.
- Uninstall via Settings → Apps. Verify service removed, WinTun adapter gone, no stray registry keys.
- Separate test: MSI installation via `msiexec /i hopssh.msi /quiet` — silent group-policy-style install works.
- ARM64 build on a Snapdragon X laptop — end-to-end works.

**Linux:**
- Install AppImage on Ubuntu 24.04 (Wayland), Fedora 41 (Wayland), Arch (X11), Debian 12 (X11).
- GNOME without AppIndicator extension: main window opens as primary UX fallback.
- KDE / XFCE / Cinnamon: tray icon visible.
- Userspace enrollment, no sudo — works.
- Kernel-TUN upgrade → pkexec prompt → systemd system unit installed. `systemctl status hopssh` shows Active.
- DNS verification: `resolvectl status` shows the hopssh per-link DNS. `dig @<mesh-dns-ip> -p <port> <host>.<domain>` resolves. If per-link is silently broken, self-diagnostic logs the fallback to drop-in config.
- .deb install on Ubuntu via `sudo apt install ./hopssh_*.deb` — enrolls, mesh up.
- .rpm install on Fedora via `sudo dnf install ./hopssh-*.rpm` — enrolls, mesh up.
- Suspend (`systemctl suspend`) and resume — mesh recovers within ~3s.
- ARM64 AppImage on a Raspberry Pi 4 — end-to-end works.
- Uninstall: apt remove / dnf remove / delete AppImage — clean.

---

## Part 5 — Open questions

Windows:
1. **Code-signing cert provider + type**: standard Authenticode (DigiCert, SSL.com, Sectigo) vs EV. **Recommend** standard for v1; upgrade to EV if SmartScreen friction hurts install-rate. Confirm a cert is ordered by Week 3.
2. **ARM64 Windows priority**: ship from v1 or defer? **Recommend** ship from v1 — Snapdragon X laptops are a growing selfhoster segment, and the CI addition is incremental.
3. **Bundle vs download WebView2**: bundled (larger installer, offline install works) vs downloaded (smaller, needs internet). **Recommend** `downloadBootstrapper` for standard NSIS; `embedBootstrapper` for MSI (enterprise / offline-friendly).
4. **Store presence (Microsoft Store)**: out of scope for v1. Can revisit — Store publishing has different signing rules and adds reach.

Linux:
5. **apt/dnf repository hosting**: standalone .deb/.rpm in v1; proper repo (`deb https://packages.hopssh.com/debian stable main`) in v1.1. Confirm infrastructure plan.
6. **Flatpak**: v1.1 candidate. Flathub review + sandbox negotiation for VPN access adds ~2-3 weeks. Not a blocker.
7. **AUR (Arch User Repository)**: community contributors usually handle this — ship the AppImage + .deb first and let an AUR package emerge organically.
8. **NixOS flake**: single file, low effort if we have a Nix user on the team. Ship as a community contribution opportunity.
9. **Snap packages**: explicitly skip. Snap's VPN permissions model is awkward and the Snap ecosystem is Canonical-centric; AppImage + .deb cover Ubuntu users adequately.
10. **tray fallback on GNOME**: is the in-app banner + "install extension" link sufficient, or ship with a bundled wrapper that pokes at the GNOME Shell extension API? **Recommend** banner only — most GNOME-savvy users already have the extension.

---

## Lessons from macOS Phase V→DD (2026-04-28 → 2026-05-07)

The macOS desktop client shipped via Phases V through DD (12 versions, v0.10.85 → v0.10.96). Six production-discovered patterns belong in this plan when implementation begins for Windows + Linux:

### 1. State-endpoint TCP probe (Phase X)
After daemon restart (dev-deploy, manual `sc.exe restart` / `systemctl restart`, crash + relaunch), the cached attach endpoint may point at a now-dead loopback port. macOS shipped a single-shot TCP-connect with retry-loop that validates before reuse + clears stale endpoint to None before re-attaching.

**Apply to Windows + Linux:** the `agent.rs::watch_system_mirror` periodic re-probe pattern carries over identically. The mirror file path differs: Windows uses `%LOCALAPPDATA%\hopssh\system-local-api-{port,token}`, Linux uses `~/.config/hopssh/system-local-api-{port,token}`.

See [[../incidents/2026-05-07-mbp-watcher-wedge]] for related daemon-restart concerns.

### 2. Autostart-on-login per OS (Phase Y)
macOS uses `tauri-plugin-autostart` writing `~/Library/LaunchAgents/com.hopssh.desktop.plist`. Default OFF on fresh install; auto-flip ON when user converts to system mode (with `start_at_login_explicit` sentinel so user-overrides win forever after).

**Windows mapping:** `tauri-plugin-autostart` writes to `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`. Add `--start-minimized` arg to the autostart entry. Don't ALSO use Task Scheduler — pick one mechanism.

**Linux mapping:** `tauri-plugin-autostart` writes a freedesktop.org `.desktop` file at `~/.config/autostart/hopssh-desktop.desktop`. systemd user units are an alternative for system-mode-with-Tauri-shell; prefer the autostart .desktop for consistency with other tray apps.

### 3. Privileged daemon writing user-readable files (Phase Z)
macOS hit a boot-before-login race where mirror files at `~/Library/Application Support/hopssh/` were left root-owned because `/dev/console` was root-owned at boot, so `resolveConsoleUser()` returned error and the chown was skipped. Self-heal goroutine (`runMirrorChownSelfHeal`) re-chowns every 30s using mirror-DIR owner as fallback target.

**Apply to Linux** if running system-mode systemd unit that writes to `/var/run/hopssh/` or similar — derive ownership from a stable user-controlled path (the dir's owner), not from "who's logged in right now". Boot-before-login is a real OS state on Linux too.

**Windows is exempt** — SCM service runs as LocalSystem; mirror files for the user-side Tauri app go to `%LOCALAPPDATA%` which is per-user from the start. No cross-process ownership race.

### 4. Uninstall hygiene tripwire (Phase AA)
Phase Y added an autostart LaunchAgent. Phase AA discovered the uninstall sweep didn't include it — left a stale launchd entry post-uninstall pointing at a deleted .app. Fixed by adding the path to `uninstallTargetsDarwin()` AND making `uninstall_hopssh_full` call `app.autolaunch().disable()` BEFORE the privileged uninstall script.

**Apply to Windows:** uninstall sweep must include registry Run keys, scheduled tasks (if used), the SCM service entry, and `%LOCALAPPDATA%\hopssh\`.

**Apply to Linux:** uninstall sweep must include `~/.config/autostart/hopssh-desktop.desktop`, systemd user unit, systemd system unit (if installed), `~/.config/hopssh/` data dir.

**General rule (codify as tripwire test in each ADR-to-implementation):** every artifact-creating feature must update the uninstall sweep + a tripwire test that locks it in.

### 5. UX copy: forbidden-jargon source-scan tripwire + affirmative phrasing (Phase BB)
macOS landed `lib.rs::tests::user_facing_copy_has_no_protocol_jargon` source-scanning every Svelte file's body (post-`</script>` strip) for banned terms: `mesh`, `data-plane`, `hop-agent` (binary name), `local agent didn't respond`, `Hide hopssh from the Dock`, etc. Replaced with `network`, `background service`, `command-line tool`, `Show hopssh in Dock`. Affirmative toggles ("Show in Dock") beat negative ("Hide hopssh from the Dock") per Apple HIG; invert at UI layer, not storage layer.

**Apply universally:** the tripwire test must run against the same Svelte source on every platform — the UI is shared. Add per-platform copy variants (Windows: "Open hopssh on sign-in" instead of "Open hopssh on login"; Linux: same as macOS) without losing the jargon-source-scan invariant.

### 6. Three independent watchdogs (Phase DD)
Stamp + threshold + cooldown + restartFn for each long-running goroutine doing load-bearing work. macOS now has three watchdogs covering renewal-goroutine death (Phase P), data-plane stuck (v0.10.36), and watcher-goroutine wedge (Phase DD). Hard timeouts on vendor-Nebula calls (`RebindUDPServer`, `CloseAllTunnels`) — `defer recover()` only catches panics, not deadlocks.

**Apply universally:** the agent code is identical across desktop platforms — the three watchdogs already work on Windows + Linux. The only Windows/Linux delta: forensic dump path conventions. macOS writes to `<configDir>/<network>/<class>-stuck-<ts>.txt`; Windows uses `%PROGRAMDATA%\hopssh\<network>\` for system-mode service, Linux uses `/var/lib/hopssh/<network>/`. Existing `inst.dir()` already abstracts this.

See [[../concepts/watchdog]] for the three-watchdog architecture, [[../incidents/2026-05-07-mbp-watcher-wedge]] for the motivating incident, and [[../concepts/desktop-client]] for the macOS shipped state these lessons came from.

## Lessons from macOS Phase EE → II.4 (2026-05-08 → 2026-05-09)

These shipped after the previous "Lessons" section was last compiled. They build on the Phase V→DD foundations rather than replacing any of them. Patterns are listed as cross-platform applicability tables — most are universal because the Svelte UI is shared; only platform-specific machinery differs.

| Phase | Pattern | Cross-platform applicability |
|---|---|---|
| EE F1 | `desktop-prefs.json` corruption logging instead of silent fallback | All — Tauri prefs file path differs per OS but the corruption-logged-not-eaten pattern is universal |
| EE F2 | Account identity affordance in Connected.svelte | All — `os.Hostname()` works everywhere; the Svelte component is shared |
| EE F3 | Disabled-button tooltips with reason copy | All — native HTML `title=` works in WebView2 (Windows) and WebKitGTK (Linux) too |
| EE F4 | Onboarding error specificity (preserve agent error message) | All — same Svelte component + same agent error returns |
| EE F5 | Post-uninstall blocking overlay + state cleanup | All; **Windows** uses `MsiExec.exe /x` not `osascript`, **Linux** uses `apt remove` / `dnf remove` / AppImage delete, but the UX pattern (auto-redirect to a blocking quit overlay + clear in-memory state) is uniform |
| FF | In-app Activity view (SSE event ring buffer) | All — Svelte component + agent SSE stream are platform-independent |
| GG | Diagnostics: View agent logs + Copy diagnostic info | **Per-platform**: macOS=Console.app via `osascript`; Windows=Event Viewer or `wevtutil qe` to a temp file then open Notepad; Linux=`journalctl --user-unit=hopssh -e` piped to a viewer or open `/var/log/hop-agent.log` in the user's `$EDITOR`. The "Copy diagnostic info" assemble-and-copy flow is universal. |
| HH | Read-only DNS records section + "Manage in dashboard" link | All — the DNS data is in peer-info regardless of platform |
| II.3 | In-app Terminal via Tauri webview pointed at dashboard's `/terminal/` route | All — Tauri's webview API is cross-platform; cookie storage is shared across webviews on every desktop platform |
| II.4 | OS brand-mark icons + tooltips on peer rows | All — inline SVG renders identically; `title=` tooltip on desktop, shadcn `<Tooltip>` on dashboard |
| KK | Karpathy behavioral guidelines vendored into CLAUDE.md + skill | Process-only; not a code feature |

**Items that DO NOT cross-port directly:**
- **Phase EE F5's macOS-specific osascript-driven uninstall** stays macOS-only. Windows uses `MsiExec.exe /x{ProductGUID}` (or the registry-driven Add/Remove Programs flow); Linux uses the package manager that installed the app. The uninstall *flow* (disable autostart first, then run privileged uninstall, then clear UI state) is universal.
- **Phase II.3's dashboard-webview cookie share** works on all desktop platforms (cookies are scoped to the bundled WebView's profile dir on each), but uses different underlying webviews (WKWebView on macOS, WebView2 on Windows, WebKitGTK on Linux). Tauri abstracts this; verify cookie persistence on each platform during the cross-platform-build phase.
- **Phase GG diagnostic-tools** explicitly need per-platform implementations as listed in the table above. The Svelte component dispatches to a platform-keyed Tauri command.

Shared:
11. **Download page layout at hopssh.com**: one "Download" button that sniffs UA and offers the right file, vs a table with all 5 platforms + architectures explicit. **Recommend** smart default + explicit table below for "other platforms". Matches Tailscale's pattern.
