//! Manages the bundled hop-agent child process.
//!
//! The agent prints a single magic line on startup:
//!   HOPSSH_LOCAL_API:<host:port>:<bearer-token>
//! We block-read stdout for that line and stash the parsed endpoint on
//! the global state. After that, all subsequent stdout/stderr is logged.

use std::io::{BufRead, BufReader};
use std::net::{SocketAddr, TcpStream};
use std::path::PathBuf;
use std::process::{ChildStdout, Command, Stdio};
use std::sync::Arc;
use std::time::Duration;

use tauri::{AppHandle, Emitter};

use crate::{resolve_agent_path, AppState, LocalAgentEndpoint};

/// system_mirror_files_exist returns true if both system-mode mirror
/// files are present on disk. This is the cheap "are we in system
/// mode?" predicate — separate from the TCP-connect probe so callers
/// can distinguish "we should be in system mode but the daemon
/// hasn't bound the port yet" from "we're definitely in bundled mode".
///
/// Pre-v0.10.87 this distinction wasn't made — a transient TCP-connect
/// failure caused `try_attach_to_system_agent` to return None and the
/// .app would fall through to spawning a bundled child, permanently
/// stuck since the bundled child can't start cleanly when the system
/// daemon already holds /etc/hop-agent + UDP ports.
pub(crate) fn system_mirror_files_exist() -> bool {
    #[cfg(not(target_os = "macos"))]
    {
        return false;
    }
    #[cfg(target_os = "macos")]
    {
        let Ok(home) = std::env::var("HOME") else {
            return false;
        };
        let mirror = PathBuf::from(home)
            .join("Library")
            .join("Application Support")
            .join("hopssh");
        mirror.join("system-local-api-token").exists()
            && mirror.join("system-local-api-port").exists()
    }
}

/// try_attach_to_system_agent probes for a `hop-agent install --migrate-from`
/// LaunchDaemon by reading its mirror token + port file out of the user's
/// `~/Library/Application Support/hopssh/`. Returns Some(endpoint) when
/// both files exist + the loopback port accepts a TCP connection. Returns
/// None on any failure (missing files, bad parse, port not bound).
///
/// **Retry budget: ~1 second** (10 attempts × 100ms TCP-connect timeouts,
/// 50ms sleeps between). Pre-v0.10.87 this was a single 500ms shot,
/// which lost the launch race against any transient (launchd respawning
/// the daemon, agent still binding the port, momentary high CPU).
/// When the probe lost the race, the .app committed to bundled mode
/// permanently — invisible from the user's perspective except as a
/// stuck "hopssh isn't running" screen across restarts.
///
/// File contracts (set by cmd/agent/migrate.go::writeSystemMirrorFiles):
///   - system-local-api-token  — bearer token, mode 0600, owned by user
///   - system-local-api-port   — decimal port number, mode 0644, owned by user
///
/// `pub(crate)` so the convert_to_system_service Tauri command can re-run
/// the probe inline after a successful migration without restarting the
/// .app. Without this, the .app's spawn_and_watch only runs ONCE at
/// launch, leaving the WebView stranded with no endpoint after the
/// bundled child got SIGKILLed.
pub(crate) fn try_attach_to_system_agent() -> Option<LocalAgentEndpoint> {
    #[cfg(not(target_os = "macos"))]
    {
        return None;
    }
    #[cfg(target_os = "macos")]
    {
        let home = std::env::var("HOME").ok()?;
        let mirror = PathBuf::from(home)
            .join("Library")
            .join("Application Support")
            .join("hopssh");
        let token_path = mirror.join("system-local-api-token");
        let port_path = mirror.join("system-local-api-port");
        if !token_path.exists() || !port_path.exists() {
            return None;
        }
        let token = std::fs::read_to_string(&token_path).ok()?.trim().to_string();
        let port_str = std::fs::read_to_string(&port_path).ok()?.trim().to_string();
        let port: u16 = port_str.parse().ok()?;
        let addr: SocketAddr = format!("127.0.0.1:{port}").parse().ok()?;

        // Retry budget — 10 × 100ms with 50ms sleep between = ~1.5s
        // worst case. The first attempt usually succeeds; the loop
        // exists to win launch-time races against the daemon binding
        // its loopback socket. This is the v0.10.87 fix for the
        // chronic "hopssh isn't running" false-positive.
        for attempt in 0..10 {
            if TcpStream::connect_timeout(&addr, Duration::from_millis(100)).is_ok() {
                if attempt > 0 {
                    log::info!(
                        "system-agent TCP probe succeeded on attempt {} (race won)",
                        attempt + 1
                    );
                }
                return Some(LocalAgentEndpoint {
                    host: format!("127.0.0.1:{port}"),
                    token,
                });
            }
            std::thread::sleep(Duration::from_millis(50));
        }
        log::warn!(
            "system-agent TCP probe FAILED after 10 attempts on 127.0.0.1:{port} \
             — daemon is slow to bind, still respawning, or wedged. \
             Watcher + periodic re-probe will retry in the background."
        );
        None
    }
}

const READY_PREFIX: &str = "HOPSSH_LOCAL_API:";

pub fn spawn_and_watch(app: &AppHandle, state: &Arc<AppState>) -> Result<(), String> {
    // Don't spawn if a system service / pre-existing agent is already
    // running and the user has set HOPSSH_AGENT_ENDPOINT + TOKEN env to
    // point at it. (Used by `hop-agent install` system-service mode.)
    if let (Ok(host), Ok(token)) = (
        std::env::var("HOPSSH_AGENT_ENDPOINT"),
        std::env::var("HOPSSH_AGENT_TOKEN"),
    ) {
        log::info!("attaching to externally-managed agent at {host}");
        *state.endpoint.lock() = Some(LocalAgentEndpoint { host, token });
        let _ = app.emit("agent-ready", ());
        return Ok(());
    }

    // Probe for a system-mode hop-agent installed via "Run in the
    // background" (the launchd LaunchDaemon writes a mirror of its
    // local-api token + listen port into our user-readable Application
    // Support dir on each (re)bind).
    //
    // v0.10.87 (Phase W): we now distinguish "mirror files exist" from
    // "TCP probe succeeded." Pre-fix, a single 500ms TCP probe at launch
    // decided whether to commit to system mode. A transient race (daemon
    // mid-respawn, port not yet bound, brief CPU pressure) caused the
    // probe to fail → fall through to bundled spawn → bundled child
    // can't bind the daemon's already-held UDP ports → state.endpoint
    // stays None forever → "hopssh isn't running" screen on every
    // restart. Now: if mirror files exist on disk, COMMIT to system
    // mode regardless of whether the immediate TCP probe wins.
    // watch_system_mirror's periodic re-probe will attach as soon as
    // the daemon is reachable.
    if system_mirror_files_exist() {
        match try_attach_to_system_agent() {
            Some(endpoint) => {
                log::info!("attached to system-mode hop-agent at {}", endpoint.host);
                *state.endpoint.lock() = Some(endpoint);
                let _ = app.emit("agent-ready", ());
            }
            None => {
                log::warn!(
                    "system-mode mirror files exist but TCP probe failed at launch — \
                     committing to system mode and waiting for watch_system_mirror's \
                     re-probe to attach. UI will show Connecting… until then."
                );
                // state.endpoint stays None for now; watch_system_mirror's
                // periodic re-probe (added in v0.10.87) will populate it
                // and emit agent-ready when the daemon becomes reachable.
            }
        }
        // CRITICAL: do not fall through to bundled spawn. Mirror files
        // mean the user is in system mode by intent; spawning a
        // bundled child here would conflict with the running daemon.
        return Ok(());
    }

    let agent_path = resolve_agent_path().ok_or_else(|| {
        "could not locate hop-agent binary (set HOPSSH_AGENT_BINARY or place it next to the .app)"
            .to_string()
    })?;

    // Strip com.apple.quarantine from the bundled hop-agent binary
    // before spawning. When the user downloads our .dmg via browser,
    // macOS attaches the quarantine xattr to EVERY file in the bundle
    // (Tauri's hop-agent sidecar included). The user's right-click ->
    // Open approves the .app itself, but macOS does NOT propagate that
    // approval to spawn-children — Launch Services performs a fresh
    // Gatekeeper check on hop-agent every time we Command::spawn it.
    // Without an Apple-notarized signature, that check fails and the
    // child process never starts (manifests as "agent unreachable" in
    // the UI). We can't ask Apple to notarize without a Dev ID, but
    // the .app can modify xattrs on its OWN bundle resources after
    // the user has approved it — so we strip quarantine here, just
    // before the spawn. Idempotent and silent on re-launch.
    #[cfg(target_os = "macos")]
    {
        let _ = std::process::Command::new("/usr/bin/xattr")
            .args(["-d", "com.apple.quarantine"])
            .arg(&agent_path)
            .output();
    }

    log::info!("spawning hop-agent: {}", agent_path.display());

    let mut cmd = Command::new(&agent_path);
    cmd.arg("serve");
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());

    // Inherit env. The agent picks up HOPSSH_PPROF_ADDR, etc. by itself.

    let mut child = cmd
        .spawn()
        .map_err(|e| format!("spawn hop-agent: {e}"))?;

    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| "no stdout on child".to_string())?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| "no stderr on child".to_string())?;

    *state.child.lock() = Some(child);

    // Pipe stderr to our log unconditionally.
    let app_for_stderr = app.clone();
    std::thread::spawn(move || {
        let reader = BufReader::new(stderr);
        for line in reader.lines().flatten() {
            log::info!("[agent] {line}");
            let _ = app_for_stderr.emit("agent-log", line);
        }
    });

    // Walk stdout looking for the ready line, then continue logging.
    let app_for_stdout = app.clone();
    let state_for_stdout = Arc::clone(state);
    std::thread::spawn(move || {
        watch_stdout(app_for_stdout, state_for_stdout, stdout);
    });

    Ok(())
}

fn watch_stdout(app: AppHandle, state: Arc<AppState>, stdout: ChildStdout) {
    let reader = BufReader::new(stdout);
    let mut announced = false;
    for line in reader.lines().flatten() {
        if !announced {
            if let Some(rest) = line.strip_prefix(READY_PREFIX) {
                // rest = "<host:port>:<token>"
                // host:port itself contains a colon (127.0.0.1:54321), so
                // we split on the LAST colon to peel the token off.
                if let Some(idx) = rest.rfind(':') {
                    let host = rest[..idx].to_string();
                    let token = rest[idx + 1..].to_string();
                    *state.endpoint.lock() = Some(LocalAgentEndpoint {
                        host: host.clone(),
                        token: token.clone(),
                    });
                    let _ = app.emit("agent-ready", ());
                    log::info!("local API ready at {host}");
                    announced = true;
                    continue;
                }
            }
        }
        log::info!("[agent] {line}");
        let _ = app.emit("agent-log", line);
    }
    log::warn!("hop-agent stdout closed");
    let _ = app.emit("agent-exited", ());
}

/// Phase Q: watch the system-mode mirror files for changes and
/// re-attach when an external `launchctl kickstart` (or any daemon
/// respawn — boot, crash, manual restart) rotates the LaunchDaemon's
/// local API port.
///
/// Without this watcher, the .app caches the endpoint at launch
/// (in `state.endpoint`) and never re-reads the mirror files. After
/// an external daemon respawn, the cached port points at a dead
/// listener and the WebView surfaces "agent unreachable" until the
/// user quits + relaunches the .app.
///
/// On change: re-run `try_attach_to_system_agent` to confirm the
/// new port is reachable, replace `state.endpoint`, emit
/// `agent-ready` so the JS layer's local-api.ts module-level
/// cache resets (Phase D infrastructure).
///
/// macOS-only because mirror files only exist there. The watcher
/// goroutine is spawned at app setup and lives until the .app
/// quits — no per-instance lifecycle needed.
#[cfg(target_os = "macos")]
pub fn watch_system_mirror(app: AppHandle, state: Arc<AppState>) {
    use notify::{Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher};

    // Spawn a periodic re-probe in addition to the file-change watcher.
    // v0.10.87 (Phase W): the file-change-only model leaves the .app
    // stuck when the launch-time TCP probe loses a race AND no file
    // events fire afterwards (because the daemon is stable, just was
    // briefly slow at launch). The periodic re-probe runs every 5s for
    // the first 60s after launch (catches launch races), then every
    // 30s indefinitely (catches daemon-flap recovery without restart).
    {
        let app = app.clone();
        let state = Arc::clone(&state);
        std::thread::spawn(move || {
            let launched_at = std::time::Instant::now();
            loop {
                let interval = if launched_at.elapsed() < std::time::Duration::from_secs(60) {
                    std::time::Duration::from_secs(5)
                } else {
                    std::time::Duration::from_secs(30)
                };
                std::thread::sleep(interval);

                // Skip the probe if we already have a working endpoint
                // — no need to thrash. The file-change watcher below
                // handles port rotations.
                if state.endpoint.lock().is_some() {
                    continue;
                }
                if !system_mirror_files_exist() {
                    continue;
                }
                if let Some(ep) = try_attach_to_system_agent() {
                    let host = ep.host.clone();
                    *state.endpoint.lock() = Some(ep);
                    let _ = app.emit("agent-ready", ());
                    log::info!(
                        "periodic re-probe attached to system agent at {host} \
                         (recovered from launch race or daemon flap)"
                    );
                }
            }
        });
    }

    std::thread::spawn(move || {
        let Ok(home) = std::env::var("HOME") else {
            log::warn!("HOME not set — system-mirror watcher disabled");
            return;
        };
        let mirror = PathBuf::from(home)
            .join("Library")
            .join("Application Support")
            .join("hopssh");

        // Channel for filesystem events; debounce in the consumer
        // because notify can deliver multiple events per single write
        // (rename + create + chmod for atomic-rename writes).
        let (tx, rx) = std::sync::mpsc::channel::<notify::Result<Event>>();
        let mut watcher: RecommendedWatcher = match notify::recommended_watcher(tx) {
            Ok(w) => w,
            Err(e) => {
                log::warn!("could not create mirror watcher: {e}");
                return;
            }
        };
        if let Err(e) = watcher.watch(&mirror, RecursiveMode::NonRecursive) {
            log::warn!("could not watch {}: {e}", mirror.display());
            return;
        }
        log::info!("system-mirror watcher started for {}", mirror.display());

        // Debounce: rapid events (within 500ms) coalesce into a single
        // re-attach attempt. notify can deliver 3-5 events per atomic
        // file write on macOS kqueue.
        let mut last_attempt = std::time::Instant::now()
            .checked_sub(std::time::Duration::from_secs(60))
            .unwrap_or_else(std::time::Instant::now);

        for ev in rx {
            let Ok(ev) = ev else { continue };
            // Filter to events on system-local-api-{port,token}. The
            // watcher fires on every file in the mirror dir; we only
            // care about port + token changes.
            let relevant = ev.paths.iter().any(|p| {
                p.file_name()
                    .and_then(|f| f.to_str())
                    .map(|n| n == "system-local-api-port" || n == "system-local-api-token")
                    .unwrap_or(false)
            });
            if !relevant {
                continue;
            }
            // Only react to writes / metadata changes — skip pure
            // access events.
            match ev.kind {
                EventKind::Create(_) | EventKind::Modify(_) | EventKind::Remove(_) => {}
                _ => continue,
            }
            if last_attempt.elapsed() < std::time::Duration::from_millis(500) {
                continue;
            }
            last_attempt = std::time::Instant::now();

            // Re-probe + replace endpoint + emit agent-ready.
            // try_attach_to_system_agent already does the TCP-connect
            // probe so we don't hand the JS a stale endpoint.
            match try_attach_to_system_agent() {
                Some(ep) => {
                    let host = ep.host.clone();
                    *state.endpoint.lock() = Some(ep);
                    let _ = app.emit("agent-ready", ());
                    log::info!("re-attached to system agent at {host} (mirror file changed)");
                }
                None => {
                    // Mirror files removed (revert flow) or daemon
                    // not yet up. Don't clobber a working endpoint —
                    // let the existing one stand until a successful
                    // re-attach happens. The JS layer's defensive
                    // resetCachedEndpoint on TypeError handles the
                    // case where the OLD endpoint has already gone
                    // dead.
                    log::info!("mirror file changed but try_attach failed — keeping existing endpoint");
                }
            }
        }
        log::warn!("system-mirror watcher loop exited");
    });
}

#[cfg(not(target_os = "macos"))]
pub fn watch_system_mirror(_app: AppHandle, _state: Arc<AppState>) {
    // Mirror files are macOS-only (system-mode = LaunchDaemon).
    // No-op on Linux/Windows.
}
