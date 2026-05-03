---
type: concept
title: macOS system mode vs bundled mode
status: current
last_compiled: 2026-05-03
sources:
  - clients/desktop/src-tauri/src/lib.rs (convert_to_system_service, revert_to_bundled)
  - cmd/agent/main.go (LaunchDaemon detection)
  - CLAUDE.md macOS Platform discovery log (Phase A1, Phase F, Phase Q)
---

# macOS system mode vs bundled mode

## TL;DR

The macOS desktop app runs the agent in one of two modes:

- **Bundled** — gvisor netstack inside the .app process. No admin prompt. `ping 10.42.1.x` from Terminal does NOT work because there's no kernel route. Default for first-run.
- **System** — root LaunchDaemon (`com.hopssh.agent`) at `/usr/local/bin/hop-agent`, kernel utun. Requires one-time admin prompt via `osascript … with administrator privileges`. `ping` works from any app.

Conversion + revert are user-triggered via Settings → "Run in the background". We do not have Apple's NetworkExtension entitlement (Phase K, deferred) so the admin prompt is unavoidable.

## Why this exists

Tailscale always uses kernel utun on macOS, but they get there via a signed `NEPacketTunnelProvider` (NetworkExtension framework, $99/yr Apple Developer ID, signed bundle). We don't have that, so our only path to kernel utun is `osascript with administrator privileges` to install a LaunchDaemon — equivalent to `sudo tailscaled install-system-daemon` for Tailscale standalone.

## State synchronization

Three files mediate the .app ↔ LaunchDaemon handshake (Phase A1):

- `~/Library/Application Support/hopssh/system-local-api-token` — bearer token written by the daemon, read by the .app for local-API auth
- `~/Library/Application Support/hopssh/system-local-api-port` — loopback port the daemon listens on
- `/var/log/hop-agent.log` — daemon stdout

The .app's Tauri Rust shell scrapes these files at launch, falling back to `try_attach_to_system_agent()` retry-with-backoff if they're not yet written.

## Phase Q: FS watcher for daemon respawn

When `launchctl kickstart -k system/com.hopssh.agent` runs (or the daemon crashes + relaunches), the mirror token + port files get rewritten. Phase Q wires a `notify-rs` FS watcher on the mirror dir; on change, emits `agent-ready` Tauri event, JS layer resets the cached `local-api.ts` endpoint, refetches status. Sub-second.

Without Phase Q, users had to quit + reopen the .app after kickstart to see the new endpoint.

## Phase F: console-user resolution

`osascript with administrator privileges` does NOT set `$SUDO_USER` (or any caller-user variable) in the privileged shell. Apple's `SecurityAuthorization` framework is not `sudo`. Inside the privileged shell, `$SUDO_USER` is empty AND `$USER` is `root`.

Any chown that uses either variable in the privileged shell silently no-ops, leaving configs root-owned and unreadable to user-mode child processes.

**Rule:** resolve the console user in the calling Tauri Rust process (which IS the console user) via `std::env::var("USER")`, reject `root`/empty, and bake the literal username into the script string at command-build time.

## Convert flow (bundled → system)

1. User toggles "Run in the background" in Settings.
2. `convert_to_system_service` Tauri command runs `osascript ... with administrator privileges` to: install hop-agent at `/usr/local/bin`, write LaunchDaemon plist, migrate `<bundled-config-dir>` to `/etc/hop-agent/`.
3. SIGKILL the bundled child agent.
4. Wait briefly for launchd to spawn the daemon + write mirror token + mirror port (retry-with-backoff up to 5s).
5. Re-attach via `try_attach_to_system_agent()`.
6. Emit `agent-ready` to the JS layer.
7. JS layer calls `resetCachedEndpoint()` (defensive — also auto-cleared on `TypeError` in `fetch`).

## Revert flow (system → bundled)

Same shape but in reverse. Phase F fix applies — the chown-back-to-console-user step uses the username baked into the script at build time.

## Detection at runtime

`/local/status` reports `runMode: "system"` when:
- `os.Getppid() == 1` (launchd is parent on macOS)
- AND `<configDir>` is `/etc/hop-agent`

## Known limitation: Phase J (deferred)

The bundled `hop-agent` binary inside the .app gets updated when the .app updates, but `/usr/local/bin/hop-agent` does NOT auto-refresh. Manual refresh:

```
sudo cp /Applications/hopssh.app/Contents/Resources/binaries/hop-agent /usr/local/bin/hop-agent
sudo launchctl kickstart -k system/com.hopssh.agent
```

## Backlinks

- [[../entities/mbp]] — currently in system mode
- [[../entities/mac-mini]] — currently in system mode
- [[sleep-wake]] — system mode TUN survives sleep more cleanly than bundled gvisor
