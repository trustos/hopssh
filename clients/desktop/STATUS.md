# Morning review: hopssh macOS desktop client

## TL;DR — what to look at first

Built and working: `clients/desktop/src-tauri/target/release/bundle/macos/hopssh.app`.

```bash
open clients/desktop/src-tauri/target/release/bundle/macos/hopssh.app
```

The app spawns a bundled `hop-agent` child, opens a small window, and shows
either an "Add a network" screen (if no enrollments exist on this Mac) or a
status dashboard for whichever networks are configured. A tray icon appears
in the menubar with a tooltip that updates live as status changes.

## What changed in the codebase

| Path | Status | Notes |
|---|---|---|
| `cmd/agent/local_api.go` | NEW | ~700 LOC. Loopback HTTP API + SSE + bearer-token auth + 127.0.0.1 enforcement. |
| `cmd/agent/main.go` | MOD | 2 small changes: starts `local_api` always, and softens the "no enrollments + no debug token" path from `log.Fatal` to `log.Printf` so the agent stays up for the GUI to drive enrollment. |
| `clients/desktop/` | NEW | Tauri 2 + Svelte 5 project. ~600 LOC of TS/Svelte + ~250 LOC of Rust. |
| `Makefile` | MOD | New targets: `desktop-build`, `desktop-app`, `desktop-dev`, `desktop-smoke`, `desktop-clean`. |
| `docs/clients/*.md` | (untouched — earlier plans remain authoritative) |

No changes to: `internal/*`, the existing dashboard at `frontend/`, vendor patches, deploy scripts.

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

1. Live `Connect` / `Disconnect` via instance registry (closes the "restart to apply" gap).
2. Phase 2A: tray icon state variants (3 PNGs: connected/relay/disconnected).
3. Phase 2C: optional kernel-TUN upgrade flow (Settings → "Install system component" → osascript admin prompt → `sudo hop-agent install`).
4. Phase 2D: optional CLI tools install (Settings → symlink `/usr/local/bin/hop`).
5. Phase 1G: universal binary CI step (`lipo` darwin/arm64 + darwin/amd64).
6. Phase 2E: code signing + notarization + DMG.
7. Phase 2F: Tauri auto-updater + agent self-update coordination (Phase 1D).
8. Phase 1A: `internal/client/` refactor — needed before iOS/Android gomobile work.
