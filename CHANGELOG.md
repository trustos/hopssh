# Changelog

All notable changes to hopssh are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). For the exhaustive per-phase record see [`docs/wiki/phases/phase-ledger.md`](docs/wiki/phases/phase-ledger.md); architectural lessons live in [`CLAUDE.md` § Discovery Log](CLAUDE.md#discovery-log).

## [Unreleased]

## [v0.11.4] — 2026-05-09

### Added
- Peer rows on both the desktop client and the dashboard show OS-brand icons (Apple, Tux, Windows pane) with tooltips that surface "macOS" / "Linux" / "Windows" on hover. Native HTML `title=` on desktop, shadcn `<Tooltip>` on the dashboard. (Phase II.4)

## [v0.11.3] — 2026-05-08

### Added
- In-app Terminal via Tauri webview: clicking "Terminal" on a peer in the desktop client opens a new window pointed at the dashboard's `/terminal/{networkId}/{nodeId}` page. Cookie storage is shared with the main window — first launch may prompt for dashboard login, subsequent opens are seamless. (Phase II.3)
- Per-peer `nodeId` propagated end-to-end through the heartbeat → enrollment → local-API → desktop UI flow.

### Removed
- `open_ssh_to_peer` Tauri command and the osascript-based SSH stopgap shipped in v0.11.1. The in-app Terminal supersedes it.

### Changed
- Peer-row OS rendering switched from inline SVG icons (v0.11.2) to plain text labels for dashboard parity. Restored as icons + tooltips in v0.11.4.

## [v0.11.2] — 2026-05-08

### Added
- Peer rows in the desktop client surface each peer's OS, with the lighthouse rendered as a special-case row (no SSH/Terminal action). (Phase II.2)

## [v0.11.1] — 2026-05-08

### Added
- "SSH to peer" button on Connected.svelte's peer rows opens Terminal.app at the peer's mesh IP via osascript. Stopgap; superseded by Phase II.3's in-app Terminal in v0.11.3. (Phase II)

## [v0.11.0] — 2026-05-08

### Added
- Read-only DNS records section in the desktop client's Connected view, sourced from existing peer-info data. "Manage in dashboard" link routes write operations (create/delete) to the web UI, where they remain owner-only. (Phase HH)

## [v0.10.99] — 2026-05-08

### Added
- Settings → About → "View agent logs" opens Console.app filtered to the hopssh process via osascript. Native macOS tool, zero new UI surface.
- Settings → About → "Copy diagnostic info" assembles version + commit + cert NotAfter + recent error count for support tickets. (Phase GG)

## [v0.10.98] — 2026-05-08

### Added
- In-app Activity view in the desktop client. Subscribes to the agent's existing SSE event stream; ring-buffered last ~100 events with timestamp, type, network, and message. Filter by network. Session-scoped (not durable — see `docs/wiki/concepts/desktop-client.md` for the diagnostics-log split). (Phase FF)

## [v0.10.97] — 2026-05-08

### Changed (Phase EE polish bundle)
- `read_desktop_prefs()` now logs a warning when the prefs file fails to parse instead of silently falling back to defaults. Corruption is recoverable but no longer invisible.
- Account identity surfaced in Connected.svelte: signed-in hostname/user shown near the network-name hero so multi-tenant users can verify the active account at a glance.
- Disabled buttons across Settings.svelte and Connected.svelte now carry `title=` tooltips explaining the disabled reason (mesh error, in-flight reload, peer not enrolled, etc.).
- Onboarding error messages preserve the agent's specific `enrollDeviceFlowPoll` error (`certificate signed by unknown CA`, `endpoint unreachable`, `rate limited`) instead of collapsing to a generic "Couldn't connect to this network".
- Post-uninstall flow: Settings auto-redirects to a blocking "Uninstalled, please quit" overlay and clears in-memory enrollment state so stale Settings rows no longer render.

## [v0.10.96] — 2026-05-07

### Fixed
- Three-watchdog reliability completed with the `watchNetworkChanges` goroutine wedge detector. New `lastWatcherActivityAt` stamp + 3-min threshold + 5-min restart cooldown. Tripping the watchdog writes a goroutine pprof dump to `<configDir>/<name>/watcher-stuck-<ts>.txt` for forensics.
- Hard 5s timeouts wrap the vendor Nebula `RebindUDPServer` and `CloseAllTunnels` calls inside the watcher's rebind block. The watcher now self-recovers in <5s in the common case, eliminating the 3.5h "connected but unreachable" silent-failure mode that motivated this phase.
- Desktop UI's `enrollmentStatus.Connected` now requires `watcherAlive` in addition to `certValid && (activeFlow || recentHeartbeat)`. UI flips to "agent connection issue — recovering automatically" within ~30s of watcher silence instead of staying GREEN indefinitely. (Phase DD)

## [v0.10.95] — 2026-05-05

### Fixed
- `/version` endpoint returns `max(current, fetchedLatest)` semver-aware. Eliminates the ~5–8 minute window after each tag bump where the desktop's Settings → Updates panel showed "Latest available: vN-1" while the user was running vN. Server cache TTL reduced 5 min → 60 s. (Phase CC)

## [v0.10.94] — 2026-05-05

### Fixed
- Cross-platform `syscall.Stat_t` build fix for Phase Z chown helpers (`migrate_chown_unix.go` + `migrate_chown_windows.go`).

## [v0.10.93] — 2026-05-05

### Changed
- UX copy audit: 28 user-facing strings rewritten to remove protocol jargon (`mesh`, `data-plane`, `hop-agent`, `local agent`, `TUN`, `port collision`) in favor of plain language ("network", "background service", "connection issue — recovering automatically").
- "Hide hopssh from the Dock" toggle renamed to "Show hopssh in Dock" (affirmative phrasing, Apple HIG-aligned). Underlying `hide_from_dock` pref schema unchanged for backward compatibility — UI inverts via a `showInDock` `$derived`.
- Onboarding "Allow" button on the bgprompt accept dialog renamed to "Turn on" (consistent with Settings toggles). (Phase BB)

## [v0.10.92] — 2026-05-05

### Fixed
- Settings → Danger zone → Uninstall now removes Phase Y's autostart LaunchAgent (`~/Library/LaunchAgents/com.hopssh.desktop.plist`). The .app calls `tauri-plugin-autostart::disable()` BEFORE running the privileged uninstall script — the plugin removes the file AND unloads it from launchd in one operation. (Phase AA)

## [v0.10.91] — 2026-05-05

### Fixed
- System-mode mirror files (`~/Library/Application Support/hopssh/system-local-api-{port,token}`) no longer remain root-owned across boot-before-login. New `chownMirrorFiles` two-layer fallback: (1) `resolveConsoleUser` if `/dev/console` is owned by a real user, (2) stat the mirror dir's owner. New `runMirrorChownSelfHeal` 30s-tick goroutine re-runs the chown if files revert to root-owned. (Phase Z)

## [v0.10.90] — 2026-05-05

### Added
- Desktop `.app` autostarts on macOS login via tauri-plugin-autostart. Writes `~/Library/LaunchAgents/com.hopssh.desktop.plist` and launches the .app with `--start-minimized` so only the tray icon appears (window opens on tray-click).
- New "Open hopssh on login" toggle in Settings → Preferences (third toggle alongside "Run in the background" and "Show hopssh in Dock"). Default OFF on fresh install; auto-enabled on first opt-in to system mode unless the user has explicitly toggled it before. (Phase Y)

## [v0.10.89] — 2026-05-05

### Fixed
- Desktop "hopssh isn't running" recurrence after LaunchDaemon restart: cached `state.endpoint` was stale-but-not-None, so Phase W's recovery hooks skipped the re-attach. New `endpoint_alive(ep)` TCP-probe (200ms) validates the cached endpoint before treating it as healthy. Both the periodic re-probe and the Retry button now correctly detect stale endpoints and re-attach.
- Member-role users no longer see "network not found" on the dashboard's network-detail page when they were invited but not the network owner. `ListDNSRecords` relaxed from owner-only `CanAccessNetwork` to membership-aware `CheckAccess(...).CanView()`. Network-detail page's `dnsApi.list()` call now also self-degrades via `.catch(() => [])` so a single sub-fetch failure can't 404 the whole render. (Phase X)

## [v0.10.88] — 2026-05-04

### Fixed
- Path-quality probe loop now excludes the lighthouse VPN address. Pre-fix, the probe occasionally targeted the lighthouse instead of a real peer, polluting RTT metrics.

## [v0.10.87] — 2026-05-04

### Fixed
- Desktop "hopssh isn't running" launch-race false-positive on system-mode Macs: the single-shot 500ms TCP probe was losing the race when the LaunchDaemon was mid-respawn. Now retries 10× × 100ms with 50ms sleep (~1.5s total budget). New periodic re-probe goroutine catches missed notify events. New `retry_attach_system_agent` Tauri command wired to the Retry button. (Phase W)

## [v0.10.86] — 2026-05-04

### Fixed
- Desktop "Install update" no longer opens two Terminal windows.

## [v0.10.85] — 2026-05-04

### Added
- Member-role users can now enroll nodes into networks they were invited to. Three server endpoints relaxed from owner-only `CanAccessNetwork` to membership-aware `CanEnrollNode`: `/api/enroll`, `/api/device/authorize`, `/api/networks/{id}/join`.
- Same-network duplicate-enrollment prevention: `existingEnrollmentForNetwork(reg, endpoint, caFingerprint)` blocks a second enroll into the same network even if the user picks a different local name. (Phase V)

### Fixed
- Mesh keepalive watchdog now filters lighthouse hostmap entries before applying the `peers > 0 && probed > 0 && succeeded == 0` predicate. Pre-fix, small networks that pruned to lighthouse-only during quiet periods triggered the watchdog every ~5 min (1,072 spurious forensic dumps observed across mini + MBP over 5 days).
- Stuck-state forensic dumps older than 7 days are now garbage-collected at every connect.

## [v0.10.84] — 2026-05-02

### Fixed
- Dashboard: contained page-level horizontal scroll on the network-detail Nodes table.

## [v0.10.83] — 2026-05-02

### Added
- Nodes table column toggles (Hostname / OS / IP / RTT / Last seen / etc.) — user can hide columns they don't care about.
- Per-peer EWMA RTT replaces the dead "Handshake (ms)" column.

## [v0.10.82] — 2026-05-02

### Fixed
- Cert renewal loop now survives macOS deep-sleep. Pre-fix, `time.After(longSleep)` was monotonic-clock-anchored and `mach_absolute_time` freezes during sleep — a 5h49m timer could take days to fire on a laptop. Replaced with a `time.NewTicker(60s)` that re-reads the cert's wall-clock NotAfter on each tick. After laptop wake, the next tick post-resume fires immediately and renews if NotAfter has passed. (Phase S)

## [v0.10.81] — 2026-05-02

### Removed
- Settings → Danger zone → "Force renew" button. Engineering capability preserved at the local-API level (`POST /local/renew`); the user-visible button caused more confusion than it solved. (Phase R)

## [v0.10.80] — 2026-05-02

### Added
- Auto-recover on cert renewal failure (Phase Q). Renewal failures now trigger a watchdog-driven restart instead of leaving the agent silently stuck.
- Manual escape hatch: Settings → Danger zone → "Force renew" button (removed in v0.10.81 as Phase R).

## [v0.10.79] — 2026-05-02

### Fixed
- Renewal-goroutine silent-death watchdog (Phase P). New `lastRenewalActivityAt` stamp on `meshInstance` + new `runRenewalWatchdog` goroutine that fires CRITICAL + writes a forensic dump (`<configDir>/<name>/renewal-stuck-<ts>.txt`) + invokes `inst.restartFn` when the stamp goes idle past `expectedCertValidity / 4` (= 6h on 24h certs). Catches the silent-failure mode where a cert hard-expired 5h49m after a CGNAT-path-flap-induced 4-reload cascade went unobserved.
- UI honesty: `enrollmentStatus.Connected` is no longer set unconditionally on `ctrl != nil`. New derivation requires `certValid && (peers > 0 || lastHeartbeatSuccessAge < 3min)` so the green pill cannot paint while the mesh is dead.

## Pre-v0.10.79 — 2026-04-30 → 2026-05-01

Foundational shipping window covering multi-network agent (Phases A–E, April 19), the desktop-client baseline (Phases A–D desktop, April 28–29), the post-launch fixes (Phase J system-mode refresh, Phase L clipboard sync), and the early UX polish (Phase N Hide-from-Dock, Phase O jargon rewrite). Tags were not bumped per phase in this window; phases shipped via dev-deploy + later folded into the v0.10.79 release.

Highlighted ships:
- **Multi-network per agent** (Phase A → E, April 19) — single agent process can join 2+ networks simultaneously with per-network Nebula CA, listen port, DNS domain, kernel TUN device name. On-disk layout shifted to per-enrollment subdirs at `<configDir>/<name>/`.
- **macOS desktop client** (Phase A → D desktop, April 28–29) — Tauri 2 + Svelte 5 menubar app. Auto-convert to system service (Tailscale-style invisible daemon), topology dedup + zombie-node prune, JS endpoint cache invalidation on `agent-ready`.
- **install-mac.sh + system-mode refresh** (Phase J, May 1) — silent-failure fixes to the curl-pipe install path; `/usr/local/bin/hop-agent` now refreshes from the `.app` bundle when the .app updates.
- **Clipboard sync** (Phase L, April 30 – May 1) — server-side relay + agent opt-in flag + Settings toggle; off-by-default.
- **"Hide hopssh from the Dock" toggle** (Phase N, May 1) — menubar-only mode for users who want zero Dock presence. Renamed to affirmative "Show hopssh in Dock" in v0.10.93.
- **Jargon rewrite slice 1** (Phase O, May 1) — initial sweep of "mesh"/"hop-agent"/"local agent" in user-facing copy. Extended in v0.10.93.

## Pre-v0.10.0 — 2026-04-25 and earlier

Initial release window: control plane single binary, agent enrollment (token + device flow + bundle), web terminal (xterm.js + WebSocket proxy), per-network DNS, port forwarding, teams + invites, audit log, real-time events, self-update, Docker. See `git log --until=2026-04-25` for the per-commit history.
