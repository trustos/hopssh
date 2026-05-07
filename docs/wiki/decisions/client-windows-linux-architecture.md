# hopssh Windows + Linux client — desktop supplement plan

See [client-apps-plan.md](client-apps-plan.md) for the overall strategy. See [client-macos-plan.md](client-macos-plan.md) for the desktop architecture baseline. **This doc is a delta document** — it covers Windows and Linux as platform-specific additions on top of the macOS plan, not full re-specifications.

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

Shared:
11. **Download page layout at hopssh.com**: one "Download" button that sniffs UA and offers the right file, vs a table with all 5 platforms + architectures explicit. **Recommend** smart default + explicit table below for "other platforms". Matches Tailscale's pattern.
