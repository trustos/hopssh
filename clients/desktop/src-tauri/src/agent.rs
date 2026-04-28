//! Manages the bundled hop-agent child process.
//!
//! The agent prints a single magic line on startup:
//!   HOPSSH_LOCAL_API:<host:port>:<bearer-token>
//! We block-read stdout for that line and stash the parsed endpoint on
//! the global state. After that, all subsequent stdout/stderr is logged.

use std::io::{BufRead, BufReader};
use std::process::{ChildStdout, Command, Stdio};
use std::sync::Arc;

use tauri::{AppHandle, Emitter};

use crate::{resolve_agent_path, AppState, LocalAgentEndpoint};

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
