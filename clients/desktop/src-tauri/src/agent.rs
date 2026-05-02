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

/// try_attach_to_system_agent probes for a `hop-agent install --migrate-from`
/// LaunchDaemon by reading its mirror token + port file out of the user's
/// `~/Library/Application Support/hopssh/`. Returns Some(endpoint) when
/// both files exist + the loopback port accepts a TCP connection. Returns
/// None on any failure (missing files, bad parse, port not bound) so the
/// caller falls through to spawning the bundled child. Defense in depth.
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
        // TCP-connect probe — confirms the launchd-spawned agent is
        // actually up before we hand the .app's UI a stale endpoint.
        let addr: SocketAddr = format!("127.0.0.1:{port}").parse().ok()?;
        TcpStream::connect_timeout(&addr, Duration::from_millis(500)).ok()?;
        Some(LocalAgentEndpoint {
            host: format!("127.0.0.1:{port}"),
            token,
        })
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
    // Support dir on each (re)bind). If both files exist + the port is
    // currently bound + a TCP connect succeeds, attach to the system
    // agent and skip spawning a bundled child. Falls through to bundled
    // spawn on any failure — defense in depth.
    if let Some(endpoint) = try_attach_to_system_agent() {
        log::info!("attached to system-mode hop-agent at {}", endpoint.host);
        *state.endpoint.lock() = Some(endpoint);
        let _ = app.emit("agent-ready", ());
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
