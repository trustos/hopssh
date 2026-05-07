---
type: concept
title: Desktop client (Tauri 2 + Svelte 5) — shipped state + forward gaps
status: current
last_compiled: 2026-05-07
sources:
  - clients/desktop/src/App.svelte
  - clients/desktop/src/lib/Connected.svelte
  - clients/desktop/src/lib/Settings.svelte
  - clients/desktop/src/lib/Onboarding.svelte
  - clients/desktop/src-tauri/src/lib.rs
  - clients/desktop/src-tauri/src/agent.rs
  - clients/desktop/src-tauri/Cargo.toml
---

# Desktop client — shipped state

## TL;DR

macOS-only Tauri 2 + Svelte 5 menubar app. Production-ready as of v0.10.96 (2026-05-07). 22 Tauri commands all wired, 30 Rust tests in `lib.rs`, zero TODO/FIXME/HACK in product code. Shipping cycles V → DD landed 12 versions in a single session covering enrollment, system-mode lifecycle, autostart, UX copy, update flow, and three independent watchdog classes (renewal / data-plane / watcher). No architectural debt; remaining gaps are forward-looking (cross-platform builds, CI test enforcement, defensive UX polish).

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                  hopssh.app (Tauri 2 bundle)                  │
│                                                               │
│  ┌─────────────────────┐         ┌───────────────────────┐  │
│  │  Svelte 5 WebView   │         │  Tauri Rust shell     │  │
│  │  (src/)             │ ◄─────► │  (src-tauri/src/)     │  │
│  │                     │  IPC    │                       │  │
│  │  - App.svelte       │         │  - lib.rs (2158 LOC)  │  │
│  │  - Onboarding       │         │  - agent.rs (child   │  │
│  │  - Connected        │         │    process mgmt)      │  │
│  │  - Settings         │         │  - 22 commands        │  │
│  │  - Disconnected     │         │  - tray icon          │  │
│  │  - SystemModeCTA    │         │  - autostart plugin   │  │
│  │  - lib/local-api.ts │         │  - update plugin      │  │
│  └─────────────────────┘         └───────────────────────┘  │
│           │                                  │                │
│           │ HTTP loopback                    │ spawn / kill   │
│           │ (token-authed)                   │                │
│           ▼                                  ▼                │
│  ┌─────────────────────────────────────────────────────────┐│
│  │  hop-agent (bundled or system-mode LaunchDaemon)         ││
│  │  - Bundled: child of .app, gvisor netstack, no root      ││
│  │  - System: /usr/local/bin/hop-agent, kernel utun, root   ││
│  └─────────────────────────────────────────────────────────┘│
└──────────────────────────────────────────────────────────────┘
```

System-mode handoff uses **mirror files** at `~/Library/Application Support/hopssh/system-local-api-{port,token}` — see [[macos-system-mode]].

## Shipped capabilities (v0.10.85 → v0.10.96)

| Capability | Phase | Version | Surface |
|---|---|---|---|
| Member-role enrollment + orphan-node prevention | V | v0.10.85 | Onboarding.svelte, server-side authz |
| Path-quality probe excludes lighthouse VPN addrs | (perf hardening) | v0.10.88 | `cmd/agent/path_quality.go` |
| Launch-race "hopssh isn't running" false-positive | W | v0.10.87 | `agent.rs::try_attach_system_agent` retry loop |
| Stale state.endpoint post-LaunchDaemon-restart | X | v0.10.89 | `lib.rs::retry_attach_system_agent` + `agent.rs::watch_system_mirror` (TCP probe) |
| Member-role network-detail "network not found" | X | v0.10.89 | `internal/api/dns.go` (CanView) + `frontend/.../[id]/+page.svelte` (.catch) |
| Autostart on macOS login | Y | v0.10.90 | `tauri-plugin-autostart`, Settings toggle, `--start-minimized` arg |
| System-mode mirror file chown self-heal | Z | v0.10.91 | `cmd/agent/migrate.go::chownMirrorFiles` + `runMirrorChownSelfHeal` goroutine |
| Cross-platform `syscall.Stat_t` split | (CI fix) | v0.10.94 | `migrate_chown_{unix,windows}.go` |
| Uninstall removes Phase Y autostart LaunchAgent | AA | v0.10.92 | `cmd/agent/uninstall.go::uninstallTargetsDarwin` + `lib.rs::uninstall_hopssh_full` |
| UX copy audit (28 strings) + Show-in-Dock affirmative | BB | v0.10.93 | Settings/Onboarding/Connected/Disconnected.svelte + `user_facing_copy_has_no_protocol_jargon` test |
| `/version` reports `max(current, fetched)` | CC | v0.10.95 | `internal/api/distribution.go::pickNewerVersion` |
| watchNetworkChanges wedge detector + Nebula timeouts | DD | v0.10.96 | `cmd/agent/watcher_watchdog.go` + `nebula.go` rebind block |

Pre-Phase-V history (Phases A → U) covered baseline UX: tray icon, dock visibility toggle, in-app update flow with osascript Terminal handoff, system-mode conversion + revert, clipboard sync per-network toggle, two-stage destructive-action confirmations (Reset / Uninstall), bgprompt for admin operations, transient banners, status bar with peer counts.

## Tauri command surface (22 total, all wired)

**Lifecycle**: `convert_to_system_service`, `revert_to_bundled`, `quit_app`
**System-mode attach**: `local_api_endpoint`, `retry_attach_system_agent`, `install_status`, `install_system_service`, `uninstall_system_service`
**CLI integration**: `install_cli_symlink`, `uninstall_cli_symlink`
**Window/tray**: `show_main_window`, `set_tray_tooltip`, `set_tray_state`
**Preferences**: `get_hide_from_dock`, `set_hide_from_dock`, `get_start_at_login`, `set_start_at_login`
**Update**: `check_remote_version`, `install_update_mac`
**Destructive**: `reset_hopssh`, `uninstall_hopssh_full`

All commands are registered in `tauri::Builder.invoke_handler!` and exercised by either UI flows or source-scan tripwires in `lib.rs::tests`.

## Test coverage

- ~30 `#[test]` functions in `lib.rs` — covers prefs round-trip, command registration tripwires, copy-jargon source scans, ordering invariants (e.g., `uninstall_full_disables_autostart_first`).
- One `panic!` at `lib.rs:2056` — inside the user-facing-copy fixture loop (`for (name, path) in targets { read_to_string(path).unwrap_or panic! }`). Appropriate use; the fixture must exist for the test to be meaningful.
- **Not enforced in CI today.** Tests pass locally per `cargo test` but `release-desktop.yml` doesn't gate releases on them. See [[#known-gaps]].

## Known gaps (forward-looking)

None of these block the current macOS user base. Listed in priority order by user-visible value, with rough complexity estimates.

| # | Gap | Complexity | Value | Notes |
|---|---|---|---|---|
| 1 | CI `cargo test` enforcement | low (30 min) | med | Add `cargo test` step to `.github/workflows/release-desktop.yml`. Tests already exist; just unrun in pipeline. Phase FF candidate. |
| 2 | Desktop CHANGELOG.md | low (30 min) | low-med | Curate Phase A → DD into a user-facing changelog. Useful for community. Phase GG candidate. |
| 3 | UI disabled-state when agent offline | low (30 min) | low-med | `App.svelte` already disables top-level tabs (`Add network`, `Settings`); per-network toggles in `Connected.svelte` should follow same pattern. Phase II candidate. |
| 4 | System-mode 30s health-probe loop | low (1 h) | low (defensive) | Phase X's TCP probe is one-shot at attach + on `notify` file events. macOS `notify` can miss atomic renames. Adds a 30s probe goroutine as belt-and-braces. Phase HH candidate. |
| 5 | Cross-platform builds (Windows + Linux) | high (1-2 days) | med-high | Tauri bundle targets are macOS-only (`["app", "dmg"]`). `notify` uses `macos_kqueue` features. `#[cfg(target_os = "macos")]` guards lib.rs activation policy / osascript / mirror-file paths / desktop-prefs path. Phase BB HIG audit is macOS-only. Phase EE candidate. |

The cross-platform gap is intentional: today's user base is Mac-only, and Phase BB landed macOS HIG-compliant copy that doesn't yet have Windows/Linux equivalents. Builds that ship without per-platform UX review would land worse than no build.

## Known design decisions worth knowing

- **Bundled vs system mode is a per-install choice surfaced via `SystemModeCTA.svelte`**, with a 7-day re-ask cadence after a "not now" dismissal. Bundled is the default on first launch (no admin prompt); system mode is opt-in.
- **Window stays visible at launch** (Tauri config `visible: true`), with a `RunEvent::Reopen` handler so Dock-icon clicks re-surface a hidden window. Tray-only apps that hide on close use this pattern. See `lib.rs::run_event_reopen_shows_main_window` test.
- **Tray is built programmatically in `setup()`, NOT declaratively in Tauri config** — Tauri 2 doesn't dedupe across the two paths and would register two NSStatusItems. Tripwire test `tauri_conf_has_no_declarative_tray_icon` enforces this.
- **Template-mode tray icons re-apply `set_icon_as_template(true)` after every `set_icon()` call** — the flag lives on the NSImage, not the NSStatusItem, and gets dropped when the underlying image is replaced. Tripwire in `set_tray_state` source-scan.
- **Ad-hoc-signed `.app` distribution via `curl install-mac.sh`** rather than DMG. Bypasses Sequoia/Tahoe Gatekeeper because curl-downloaded files don't get the `com.apple.quarantine` xattr. See [[macos-system-mode]] § Gatekeeper.

## Backlinks

- [[macos-system-mode]] — bundled-vs-system architecture + mirror-file handoff
- [[watchdog]] — three-watchdog architecture this client surfaces via the `watcherAlive` axis
- [[../incidents/2026-05-07-mbp-watcher-wedge]] — Phase DD motivating incident
- [[cert-renewal]] — cert lifecycle the client's status indicators depend on
- [[../entities/mbp]], [[../entities/mac-mini]] — primary fleet for desktop client testing
