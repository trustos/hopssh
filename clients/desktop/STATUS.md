# hopssh macOS desktop client — current state (v0.10.36 era)

## TL;DR

Built and working: `clients/desktop/src-tauri/target/release/bundle/macos/hopssh.app`.

```bash
open clients/desktop/src-tauri/target/release/bundle/macos/hopssh.app
```

The app spawns a bundled `hop-agent` child, opens a small window, and shows
either an "Add a network" screen (if no enrollments exist on this Mac) or a
status dashboard for whichever networks are configured. A tray icon appears
in the menubar with a tooltip that updates live as status changes.

The app inherits all the agent-side reliability work shipped today: live
runtime add/remove, CGNAT-aware mesh keepalive, and the stuck-data-plane
watchdog with auto-recovery. So a user who enrolls a new network sees it
come up in seconds; an idle session survives CGNAT timeouts; and a
hypothetical Nebula vendored-runtime stuck-state recovers within ~5 min.

## Agent improvements that landed today (all on prod fleet)

| Tag | Headline | What it gives the GUI app |
|---|---|---|
| **v0.10.33** | cert-reload close-old-then-start-new | overnight cert renewals don't strand the agent on an expired cert |
| **v0.10.34** | live runtime add/remove of mesh instances + local HTTP API | the GUI can connect / disconnect / leave a network without ever asking for an agent restart |
| **v0.10.35** | CGNAT-aware mesh keepalive | first click after long idle (e.g., Screen Sharing after 5+ min) doesn't show a 30s black screen — flow stays warm |
| **v0.10.36** | stuck-data-plane watchdog + auto-recovery + forensic dump | if Nebula's vendored runtime ever silently stalls, the agent self-heals within ~5 min and writes a goroutine dump for root-cause forensics |

## What changed in the codebase (cumulative)

| Path | Status | Tag | Notes |
|---|---|---|---|
| `cmd/agent/renew.go` | MOD | v0.10.33 | close-old-then-start-new + `waitForUDPPortFree` + retryReloadBackoff atomic.Pointer |
| `cmd/agent/local_api.go` | NEW | v0.10.34 | ~1000 LOC loopback HTTP API + bearer-token + SSE + auto-connect-after-enroll |
| `cmd/agent/instance.go` | MOD | v0.10.34, v0.10.36 | runCtx + restartFn fields; v0.10.34 lifecycle invariant |
| `cmd/agent/main.go` | MOD | v0.10.34, v0.10.36 | tryStartMeshInstance refactor; var connectFn forward-decl; restartFn wiring |
| `cmd/agent/serverset.go` | MOD | v0.10.34 | shutdownInstance helper |
| `cmd/agent/keepalive.go` | NEW | v0.10.35, v0.10.36 | runMeshKeepalive every 90s + watchdog + auto-pprof |
| `cmd/agent/nebula.go` | MOD | v0.10.33 | meshService.Close contract documented |
| `clients/desktop/` | NEW | (initial) | Tauri 2 + Svelte 5 project |
| `Makefile` | MOD | (initial) | desktop-{build,app,dev,smoke,clean} targets |

Test coverage cumulative: 4 keepalive + 6 watchdog + 6 lifecycle + 5
reload invariants + 3 port-release + 1 source-scan tripwire for each
of v0.10.33/v0.10.34/v0.10.36 — **30+ new tests**, all passing under `-race`.

No changes to: `internal/*`, the existing dashboard at `frontend/`, vendor patches.

## End-to-end verified

- ✅ `go test ./cmd/agent` — all passing (no regressions from the local_api wiring).
- ✅ Smoke test: `make desktop-smoke` — bearer + 127.0.0.1 gates work, all read endpoints respond.
- ✅ `.app` launches; agent child spawns; clean shutdown on SIGTERM/SIGINT/Cmd-Q (no leaks).
- ✅ Device-flow enrollment kickoff against real `https://hopssh.com` returns user code + verification URL; first poll returns `pending` (correct).
- ✅ TypeScript / Svelte type check — 0 errors, 0 warnings.

## Known limitations (the things to discuss in the morning)

1. ~~No live "Connect" / "Disconnect"~~ ✅ **Shipped v0.10.34.** `POST /local/connect` and `/local/disconnect` are now wired through `tryStartMeshInstance` and `instances.remove + inst.close()`. Auto-connect fires after a successful device-flow or token enrollment so the user sees the mesh come up immediately; per-network Connect/Disconnect button in the UI lets the user toggle a network on demand. Leave disconnects the live instance before deleting cert files. No more "restart the agent" copy anywhere.
2. **No code signing / notarization yet** — the .app is built with an ad-hoc signature. Launching from Finder will show "unidentified developer" the first time. Workaround: Cmd-click → Open. Real fix is Phase 2E in the plan (Apple Developer cert + xcrun notarytool in CI).
3. **No DMG / installer yet** — currently the .app is just a bundle. Phase 2E will package it.
4. **Tray icon doesn't change with state** — the tooltip updates live but the icon is static. Phase 2A polish: separate icon variants for connected/disconnected/error.
5. **No "Open Dashboard" iframe yet** — current "Open dashboard" button opens the control plane in the system browser. Embedded iframe is out of scope for v1.
6. **Userspace TUN by default** — per the plan. Per the discovery log this means Screen Sharing HP cold-path can be flaky. Settings copy points users at `sudo hop-agent install` for kernel TUN.

## How the architecture works

```
┌──────────────────────────────────────────────┐
│ hopssh.app                                    │
│   ├── Svelte UI (WebView)                     │
│   ├── Rust shell                              │
│   │   - spawns hop-agent child                │
│   │   - parses HOPSSH_LOCAL_API:host:token    │
│   │   - tray icon + commands                  │
│   │   - SIGTERM/Cmd-Q → child cleanup          │
│   └── hop-agent (existing Go binary)          │
│        running with --config-dir=             │
│        ~/Library/Application Support/hopssh/  │
│        + new local_api on 127.0.0.1:0         │
└──────────────────────────────────────────────┘
```

The same architecture extends to Windows (.exe / .msi) and Linux (.AppImage / .deb / .rpm) with no changes to the Svelte UI — only the Rust shell needs platform-specific tray + service code, and the Go agent already has all the platform-specific TUN/DNS/service logic.

## Quick commands

```bash
# Rebuild .app from scratch (host arch only, ~30s)
make desktop-app

# Live dev (vite + tauri reload on Svelte changes)
make desktop-dev

# Smoke-test the API surface
make desktop-smoke

# Wipe build artifacts (keeps node_modules / cargo registry cache)
make desktop-clean
```

## Pending follow-ups (priority order)

1. ~~Live `Connect` / `Disconnect`~~ ✅ shipped v0.10.34.
2. **Tray icon state variants** (3 PNGs: connected/relay/disconnected). Tooltip is dynamic; only the icon glyph is static today.
3. **Optional kernel-TUN upgrade flow** (Settings → "Install system component" → osascript admin prompt → `sudo hop-agent install`).
4. **Optional CLI tools install** (Settings → symlink `/usr/local/bin/hop`).
5. **Code signing + notarization + DMG packaging** — Apple Developer cert + xcrun notarytool in CI. Required before any public download.
6. **Tauri auto-update** + agent self-update coordination (lock file pattern).
7. **Surface watchdog events in UI** — when the agent's watchdog auto-recovers an instance, surface as a transient banner so the user knows recovery happened.
4. Phase 2D: optional CLI tools install (Settings → symlink `/usr/local/bin/hop`).
5. Phase 1G: universal binary CI step (`lipo` darwin/arm64 + darwin/amd64).
6. Phase 2E: code signing + notarization + DMG.
7. Phase 2F: Tauri auto-updater + agent self-update coordination (Phase 1D).
8. Phase 1A: `internal/client/` refactor — needed before iOS/Android gomobile work.
