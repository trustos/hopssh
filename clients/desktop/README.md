# hopssh desktop client

Tauri 2 + Svelte 5 native macOS client for hopssh. The app bundles the existing
`hop-agent` binary as a sidecar — no networking logic is reimplemented in Rust
or TypeScript. The UI is a thin shell around the agent, talking to it over a
loopback HTTP API on `127.0.0.1:<random>` with a fresh bearer token per launch.

This is the **desktop** project; an iOS/Android variant on the same Svelte UI
will land on top of this once `internal/client/` is extracted (Phase 1A in the
plan).

## Status (2026-04-27, agent at v0.10.36)

Working end-to-end:
- ✅ `.app` builds and launches
- ✅ Bundled `hop-agent` spawned as a child process
- ✅ Loopback API + bearer-token auth + 127.0.0.1 enforcement
- ✅ SSE event stream (status + enrollment lifecycle)
- ✅ Device-flow enrollment kickoff against `https://hopssh.com`
- ✅ Live connect / disconnect / leave (v0.10.34) — no agent restart needed
- ✅ Auto-connect after a successful enrollment
- ✅ Cert-status / peer-list display from live agent state
- ✅ Token-flow enrollment + leave endpoints
- ✅ Child agent cleanup on Cmd-Q / SIGTERM / SIGINT (no leaks)

Agent reliability inherited automatically:
- ✅ Cert renewals never strand the agent on an expired cert (v0.10.33).
- ✅ CGNAT idle timeouts can't kill an idle tunnel — every 90 s the agent fires a TCP-connect to each peer to refresh flow state (v0.10.35). User-visible: clicking Screen Sharing after long idle no longer triggers the 30 s black-screen.
- ✅ If Nebula's data plane ever silently stalls (multi-instance vendored-runtime stuck-state), a watchdog detects within ~5 min, writes a goroutine pprof dump for forensics, then auto-recovers the instance (v0.10.36).

Pending:
- Menu bar tray icon state variants (PNGs for connected / relay / disconnected — tooltip is already dynamic)
- Optional kernel-TUN upgrade flow (Settings → "Install system component")
- Optional CLI tools install (Settings → symlink `/usr/local/bin/hop`)
- Code signing + notarization + DMG packaging
- Tauri auto-update + agent self-update coordination
- Surface agent watchdog events in the UI as transient banners

## Quick start

From the repo root:

```bash
# First time: vendor + apply patches.
make setup

# Build the .app (releases the host-arch agent + the Tauri shell).
make desktop-app

# Launch:
open clients/desktop/src-tauri/target/release/bundle/macos/hopssh.app
```

You'll see the "Add a network" screen if no enrollment exists. Type your
control-plane URL (e.g. `https://hopssh.com`), click "Continue with browser",
approve the device code, and the app will register the enrollment.

⚠️ For the mesh to actually come up, the agent has to **restart** so that
the new enrollment is picked up by `runServe`. This is a known v1
limitation; Phase 2 will add live add/remove via the instance registry.
For now: kill the .app, relaunch.

## Dev workflow

```bash
# Live reload Svelte UI + Tauri shell.
make desktop-dev
```

Or run the agent + a manual vite server (no Tauri):

```bash
# Terminal 1: build + run the agent in an isolated config dir
mkdir -p /tmp/hopssh-dev
clients/desktop/src-tauri/binaries/hop-agent serve --config-dir /tmp/hopssh-dev

# Terminal 2: scrape token, run vite
cd clients/desktop
TOKEN=$(cat /tmp/hopssh-dev/local-api-token)
PORT=$(grep -oE '127.0.0.1:[0-9]+' /tmp/hopssh-dev/agent.log | head -1 || echo "127.0.0.1:0")
echo "VITE_HOPSSH_API=http://$PORT" > .env.local
echo "VITE_HOPSSH_TOKEN=$TOKEN" >> .env.local
npm run dev
```

## Smoke test

```bash
make desktop-smoke
```

Spins up a fresh agent in a temp dir, exercises every read endpoint, and
verifies that requests without a token return 401 and that off-loopback
requests return 403.

## Architecture

```
┌──────────────────────────────────────────────┐
│ hopssh.app (Tauri 2 release)                  │
│ ┌────────────────────────────────────────┐   │
│ │ Svelte 5 UI (WebView)                   │   │
│ │  - Onboarding (device-flow QR-less)     │   │
│ │  - Connected (status, peers)            │   │
│ │  - Settings (leave network)             │   │
│ │  - StatusBar (online dot, peer count)   │   │
│ └─────────────────┬──────────────────────┘   │
│                   │ fetch + SSE              │
│ ┌─────────────────┴──────────────────────┐   │
│ │ Tauri Rust shell                        │   │
│ │  - spawn hop-agent child                │   │
│ │  - parse HOPSSH_LOCAL_API:host:token    │   │
│ │  - expose local_api_endpoint() command  │   │
│ │  - tray icon + menu                     │   │
│ │  - SIGTERM/Drop -> child cleanup        │   │
│ └─────────────────┬──────────────────────┘   │
│                   │ stdin/stdout              │
│ ┌─────────────────┴──────────────────────┐   │
│ │ hop-agent (existing Go binary, unchanged)│  │
│ │   serve subcommand + new local_api.go   │   │
│ │   listening on 127.0.0.1:0              │   │
│ └─────────────────────────────────────────┘   │
└──────────────────────────────────────────────┘
```

Config dir on macOS: `~/Library/Application Support/hopssh/`.
Bearer token: `<configDir>/local-api-token` (mode 0600).

## Key files

- `src/lib/local-api.ts` — TypeScript client for the agent's `/local/*` API.
- `src/lib/stores.svelte.ts` — Svelte 5 reactive state (status, online, events).
- `src/App.svelte` — top-level shell; routes between Onboarding / Connected /
  Settings views.
- `src/lib/Onboarding.svelte` — device-flow enrollment UI.
- `src/lib/Connected.svelte` — status + peer list per enrollment.
- `src/lib/Settings.svelte` — agent info + per-enrollment leave button.
- `src-tauri/src/lib.rs` — Tauri shell entry point: tray, commands, lifecycle.
- `src-tauri/src/agent.rs` — child agent spawn + stdout watcher.
- `../../cmd/agent/local_api.go` — agent-side local HTTP API.
