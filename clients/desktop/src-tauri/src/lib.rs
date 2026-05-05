//! hopssh desktop client (Tauri 2 shell).
//!
//! Responsibilities of this layer:
//!   1. Spawn the bundled `hop-agent` binary as a child process.
//!   2. Parse the `HOPSSH_LOCAL_API:<host:port>:<token>` line from stdout
//!      so the WebView knows where to talk.
//!   3. Expose a `local_api_endpoint` Tauri command so the JS layer can
//!      ask for the agent endpoint after window load.
//!   4. Build a system tray with a connect/disconnect-aware icon.
//!   5. Cleanly shut down the child on app quit.
//!
//! What this layer DOES NOT do: any networking logic. The agent owns
//! all mesh state; this is a thin UI host.

use std::path::PathBuf;
use std::process::Child;
use std::sync::Arc;

use parking_lot::Mutex;
use serde::Serialize;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Emitter, Manager, State};

mod agent;

/// Endpoint published to the JS layer once the agent prints
/// HOPSSH_LOCAL_API. Stored on the global app state so commands can read
/// it later.
#[derive(Clone, Serialize)]
pub struct LocalAgentEndpoint {
    pub host: String,  // "127.0.0.1:54321"
    pub token: String, // hex bearer token
}

#[derive(Default)]
pub struct AppState {
    pub endpoint: Mutex<Option<LocalAgentEndpoint>>,
    pub child: Mutex<Option<Child>>,
}

impl AppState {
    /// Kill the child agent process, waiting briefly for clean exit.
    /// Idempotent — safe to call repeatedly.
    pub fn shutdown_agent(&self) {
        let mut guard = self.child.lock();
        if let Some(mut child) = guard.take() {
            log::info!("shutting down hop-agent child (pid {})", child.id());
            // Try a graceful kill; on Unix this is SIGKILL via Rust's
            // std::process::Child::kill. The agent doesn't currently
            // ship a SIGTERM-then-SIGKILL window from us, but the OS
            // sends signals in process group on shutdown and the agent
            // already handles that; this is the explicit fallback.
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

impl Drop for AppState {
    fn drop(&mut self) {
        self.shutdown_agent();
    }
}

#[tauri::command]
fn local_api_endpoint(state: State<'_, Arc<AppState>>) -> Result<LocalAgentEndpoint, String> {
    state
        .endpoint
        .lock()
        .clone()
        .ok_or_else(|| "agent endpoint not yet available; agent may still be starting".into())
}

/// Force-retry the system-agent probe. Wired to the WebView's "Retry"
/// button on the Disconnected screen so a user staring at "hopssh
/// isn't running" can manually kick the .app out of the stuck state
/// instead of having to quit + relaunch.
///
/// v0.10.87 (Phase W). Pre-fix the Retry button only re-ran
/// `agent.refresh()` against the same broken state.endpoint=None,
/// which was guaranteed to keep failing.
///
/// v0.10.89 (Phase X). The "is_some" early-return was itself a bug:
/// when state.endpoint pointed at a DEAD port (post-LaunchDaemon-
/// restart), the Retry button silently no-op'd and the user was
/// stuck again. Now we TCP-probe the cached endpoint via
/// `endpoint_alive`; if it fails the predicate, we clear and
/// re-attach — exactly what the user expects when they click Retry.
/// Also called by the JS SSE-failure-recovery path so the .app
/// self-heals on daemon restart without user action (see local-api.ts
/// subscribeEvents catch handler).
#[tauri::command]
fn retry_attach_system_agent(
    app: AppHandle,
    state: State<'_, Arc<AppState>>,
) -> Result<(), String> {
    let stale = match state.endpoint.lock().as_ref() {
        None => true,
        Some(ep) => !crate::agent::endpoint_alive(ep),
    };
    if !stale {
        // Truly attached — no-op is correct.
        return Ok(());
    }
    if !crate::agent::system_mirror_files_exist() {
        return Err(
            "no system-mode mirror files found — this Mac doesn't appear to be in system mode"
                .into(),
        );
    }
    // Clear stale endpoint BEFORE re-attach. This makes the agent-ready
    // event a real transition signal for the JS listener (which resets
    // the module-level endpoint cache) and keeps the log line below
    // honest.
    *state.endpoint.lock() = None;
    match crate::agent::try_attach_to_system_agent() {
        Some(ep) => {
            let host = ep.host.clone();
            *state.endpoint.lock() = Some(ep);
            let _ = app.emit("agent-ready", ());
            log::info!("retry_attach_system_agent: attached to {host}");
            Ok(())
        }
        None => Err("system-mode daemon is not reachable yet — try again in a few seconds".into()),
    }
}

#[tauri::command]
fn show_main_window(app: AppHandle) -> Result<(), String> {
    // Phase N: when "Hide from Dock" is enabled, the user's CMD-W /
    // red-dot close demoted us to .Accessory and removed the Dock
    // icon. We MUST set the policy back to .Regular BEFORE show()
    // + set_focus(); otherwise the window appears but the Dock
    // icon stays gone and Cmd-Tab still doesn't list us — the OS
    // honors policy at the moment we activate.
    #[cfg(target_os = "macos")]
    if hide_from_dock_enabled(&app) {
        let _ = app.set_activation_policy(tauri::ActivationPolicy::Regular);
    }
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        // set_focus() in Tauri 2 calls NSApp.activate(ignoringOtherApps:true)
        // on macOS, which is required to bring the Dock icon to the
        // foreground after the policy flip above.
        let _ = w.set_focus();
        Ok(())
    } else {
        Err("main window not found".into())
    }
}

/// Phase N: simple file-backed preference for the "Hide hopssh from
/// the Dock" toggle. Persisted at
/// `<HOME>/Library/Application Support/hopssh/desktop-prefs.json`
/// so the choice survives across .app updates and reinstalls (we
/// take care to NOT wipe this file in install-mac.sh's reinstall
/// path).
///
/// Single-key JSON keeps the structure trivial; future per-user
/// preferences can land here without versioning gymnastics.
#[derive(serde::Deserialize, serde::Serialize, Default)]
struct DesktopPrefs {
    #[serde(default)]
    hide_from_dock: bool,

    // Phase Y (v0.10.90): autostart the .app on macOS login. Defaults
    // to OFF on a fresh install — the user explicitly opts in, never
    // surprised by a LaunchAgent file appearing behind their back. The
    // first time the user converts to "Run in the background" (system
    // mode) we auto-flip this to ON, since opting into an always-on
    // daemon implies wanting the tray on login too. After the user
    // touches the toggle in Settings (start_at_login_explicit=true)
    // their choice always wins, regardless of mode transitions.
    #[serde(default)]
    start_at_login: bool,

    // True once the user has actively flipped the start_at_login
    // toggle in Settings. Used by convert_to_system_service to decide
    // whether to auto-enable autostart (only auto-enables on first
    // convert IF the user hasn't expressed an explicit choice yet).
    #[serde(default)]
    start_at_login_explicit: bool,
}

fn desktop_prefs_path() -> Option<PathBuf> {
    let home = std::env::var("HOME").ok()?;
    Some(PathBuf::from(home).join("Library/Application Support/hopssh/desktop-prefs.json"))
}

fn read_desktop_prefs() -> DesktopPrefs {
    let Some(p) = desktop_prefs_path() else {
        return DesktopPrefs::default();
    };
    let Ok(data) = std::fs::read(&p) else {
        return DesktopPrefs::default();
    };
    serde_json::from_slice(&data).unwrap_or_default()
}

fn write_desktop_prefs(prefs: &DesktopPrefs) -> Result<(), String> {
    let p = desktop_prefs_path().ok_or("HOME not set")?;
    if let Some(parent) = p.parent() {
        std::fs::create_dir_all(parent).map_err(|e| e.to_string())?;
    }
    let data = serde_json::to_vec_pretty(prefs).map_err(|e| e.to_string())?;
    std::fs::write(&p, data).map_err(|e| e.to_string())
}

fn hide_from_dock_enabled(_app: &AppHandle) -> bool {
    read_desktop_prefs().hide_from_dock
}

#[tauri::command]
fn get_hide_from_dock() -> bool {
    read_desktop_prefs().hide_from_dock
}

/// Run the macOS one-line installer in a fresh Terminal window so
/// the user sees progress + can enter their admin password. The
/// install-mac.sh script (since v0.10.73) does sudo-validate
/// upfront, refreshes /usr/local/bin/hop-agent for system-mode
/// users, and SIGKILL+relaunches hopssh.app at the end — so this
/// IS a one-click in-app updater. Replaces the old "Open install
/// instructions" button which sent users to hopssh.com homepage.
///
/// Why osascript+Terminal instead of running the script directly
/// from this process: the script kills the running hopssh-desktop
/// (us!) mid-execution. If we ran it as a child of this process,
/// our SIGKILL would also kill the install. Decoupling via Terminal
/// means the install survives our own death.
#[cfg(target_os = "macos")]
#[tauri::command]
fn install_update_mac() -> Result<(), String> {
    // Order matters here. `activate` is what brings Terminal.app to the
    // front — and if Terminal isn't already running, it launches it
    // with a default empty window. If we then call `do script`, that
    // opens a SECOND window for the actual command, leaving the user
    // staring at two Terminal windows when they expected one.
    //
    // Putting `do script` FIRST means Terminal launches with the
    // command-running window as its initial window. The trailing
    // `activate` then just brings the existing window to the front
    // without spawning a duplicate.
    let cmd = r#"tell application "Terminal"
    do script "curl -fsSL https://hopssh.com/install-mac.sh | bash"
    activate
end tell"#;
    let output = std::process::Command::new("osascript")
        .arg("-e")
        .arg(cmd)
        .output()
        .map_err(|e| format!("osascript spawn failed: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "osascript failed: {}",
            String::from_utf8_lossy(&output.stderr)
        ));
    }
    Ok(())
}

#[cfg(not(target_os = "macos"))]
#[tauri::command]
fn install_update_mac() -> Result<(), String> {
    Err("install_update_mac is macOS-only".into())
}

/// Fetch `<endpoint>/version` from a Rust HTTP client so the WebView's
/// CSP / CORS doesn't gate the manual update check. The control plane
/// at hopssh.com responds 200 to a direct `curl` but doesn't include
/// `Access-Control-Allow-Origin: tauri://localhost`, so the WebView
/// blocks the response. Routing through Rust bypasses both layers.
#[tauri::command]
async fn check_remote_version(endpoint: String) -> Result<String, String> {
    let url = format!("{}/version", endpoint.trim_end_matches('/'));
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(10))
        .build()
        .map_err(|e| e.to_string())?;
    let resp = client.get(&url).send().await.map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("HTTP {}", resp.status()));
    }
    let body = resp.text().await.map_err(|e| e.to_string())?;
    // Server returns {"version": "vX.Y.Z", "current": "vX.Y.Z"}.
    // Parse the "version" field — that's the LATEST AVAILABLE per
    // distribution.go::Version.
    #[derive(serde::Deserialize)]
    struct VersionResp {
        version: String,
    }
    let parsed: VersionResp = serde_json::from_str(&body).map_err(|e| e.to_string())?;
    Ok(parsed.version)
}

/// Tauri command: persist the "Hide from Dock" preference and apply
/// it immediately. When enabling: if the window is currently visible
/// the Dock icon stays until window-close (we don't want to disorient
/// the user mid-interaction). When disabling: reverts to .Regular
/// immediately, brings the Dock icon back, and focuses the window so
/// the user sees the change took effect.
#[tauri::command]
fn set_hide_from_dock(app: AppHandle, enabled: bool) -> Result<(), String> {
    let mut prefs = read_desktop_prefs();
    prefs.hide_from_dock = enabled;
    write_desktop_prefs(&prefs)?;

    #[cfg(target_os = "macos")]
    {
        if !enabled {
            // Toggling OFF: restore the regular activation policy
            // immediately so the user sees the Dock icon return.
            let _ = app.set_activation_policy(tauri::ActivationPolicy::Regular);
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.set_focus();
            }
        }
        // Toggling ON: defer the policy switch until the user closes
        // the window. Demoting to .Accessory while the window is on
        // screen leaves a confused state where the window remains
        // visible but Cmd-Tab no longer lists us. Better to have the
        // user finish what they're doing, close the window normally,
        // and have the close-handler perform the demotion.
    }
    Ok(())
}

#[tauri::command]
fn set_tray_tooltip(app: AppHandle, tooltip: String) -> Result<(), String> {
    if let Some(tray) = app.tray_by_id("main") {
        tray.set_tooltip(Some(&tooltip)).map_err(|e| e.to_string())?;
    }
    Ok(())
}

// ============================================================
// Phase Y (v0.10.90): "Open hopssh on login" plumbing.
//
// `get_start_at_login` returns the ACTUAL on-disk state via the
// tauri-plugin-autostart plugin (`is_enabled()` reads
// ~/Library/LaunchAgents/<bundle-id>.plist existence on macOS).
// This is the source of truth — if the user manually deletes the
// LaunchAgent or removes hopssh from System Settings → Login Items,
// the UI reflects reality the next time it queries.
//
// `set_start_at_login` (a) persists the user's intent in
// desktop-prefs.json, (b) sets `start_at_login_explicit=true` so
// future convert_to_system_service calls don't override the user's
// choice, and (c) drives the plugin to write or remove the
// LaunchAgent file so the actual on-disk state matches.
//
// `sync_autostart_with_pref` is the helper used by setup() (F4) and
// convert_to_system_service (F5) to bring the plugin into agreement
// with the persisted pref. It's idempotent — only acts when the
// states differ.
// ============================================================

#[tauri::command]
fn get_start_at_login(app: AppHandle) -> bool {
    use tauri_plugin_autostart::ManagerExt;
    // Source of truth: actual LaunchAgent file presence on disk.
    // The pref file holds intent; if they ever drift (manual removal
    // via System Settings, etc.) the UI shows reality.
    app.autolaunch().is_enabled().unwrap_or(false)
}

#[tauri::command]
fn set_start_at_login(app: AppHandle, enabled: bool) -> Result<(), String> {
    let mut prefs = read_desktop_prefs();
    prefs.start_at_login = enabled;
    prefs.start_at_login_explicit = true;
    write_desktop_prefs(&prefs)?;
    sync_autostart_with_pref(&app, enabled)
}

/// Bring the tauri-plugin-autostart plugin's actual LaunchAgent state
/// into agreement with the requested pref. Idempotent — only mutates
/// when the states differ.
///
/// Used by:
/// - setup() (F4): on every .app launch, sync the persisted pref
///   to the actual LaunchAgent state. Self-healing layer for cases
///   where the user removed the LaunchAgent via System Settings.
/// - convert_to_system_service (F5): the first time the user opts
///   into "Run in the background", auto-enable autostart so the
///   tray appears on next login (the user's mental model: "always-on
///   hopssh" includes the GUI).
/// - set_start_at_login (F3): direct user toggle from Settings.
fn sync_autostart_with_pref(app: &AppHandle, want_enabled: bool) -> Result<(), String> {
    use tauri_plugin_autostart::ManagerExt;
    let mgr = app.autolaunch();
    let is_enabled = mgr.is_enabled().map_err(|e| e.to_string())?;
    if want_enabled && !is_enabled {
        mgr.enable().map_err(|e| e.to_string())?;
        log::info!("autostart: enabled (LaunchAgent registered)");
    } else if !want_enabled && is_enabled {
        mgr.disable().map_err(|e| e.to_string())?;
        log::info!("autostart: disabled (LaunchAgent removed)");
    }
    Ok(())
}

#[derive(Serialize)]
pub struct InstallStatus {
    pub system_service: bool, // /Library/LaunchDaemons/com.hopssh.agent.plist exists
    pub cli_symlink: bool,    // /usr/local/bin/hop exists and points at our bundle
}

/// One-shot probe of admin-installed integrations. JS uses this to
/// decide whether to render the Settings → Install / Uninstall buttons.
#[tauri::command]
fn install_status() -> InstallStatus {
    InstallStatus {
        system_service: std::path::Path::new("/Library/LaunchDaemons/com.hopssh.agent.plist").exists(),
        cli_symlink: std::path::Path::new("/usr/local/bin/hop").exists(),
    }
}

/// Install hop-agent as a launchd system daemon. Triggers a single
/// admin-prompt via osascript; on consent, the bundled hop-agent runs
/// `install` (which writes the plist + bootstraps it). After this
/// the agent runs as root and survives across user logouts.
///
/// macOS only — no-op on other platforms.
#[tauri::command]
fn install_system_service(_app: AppHandle) -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        return Err("install_system_service: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        // Quote-safe: osascript "do shell script" requires single-line
        // quoted POSIX path. We trust resolve_agent_path's output (no
        // user input).
        let script = format!(
            r#"do shell script "'{}' install" with administrator privileges"#,
            agent.display()
        );
        run_osascript(&script).map(|out| {
            if out.trim().is_empty() {
                "system service installed".to_string()
            } else {
                out
            }
        })
    }
}

/// Uninstall the launchd daemon (mirror of install_system_service).
#[tauri::command]
fn uninstall_system_service(_app: AppHandle) -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        return Err("uninstall_system_service: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        let script = format!(
            r#"do shell script "'{}' uninstall" with administrator privileges"#,
            agent.display()
        );
        run_osascript(&script).map(|out| {
            if out.trim().is_empty() {
                "system service removed".to_string()
            } else {
                out
            }
        })
    }
}

/// Symlink /usr/local/bin/hop to the bundled hop-agent binary so
/// power users can invoke it from Terminal. Idempotent — overwrites
/// any existing symlink that already points at our bundle, refuses
/// to overwrite a stranger's binary.
#[tauri::command]
fn install_cli_symlink(_app: AppHandle) -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        return Err("install_cli_symlink: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        // Refuse if /usr/local/bin/hop exists and is NOT a symlink
        // pointing at our bundle. Avoids clobbering a user's
        // unrelated `hop` binary.
        let target = std::path::Path::new("/usr/local/bin/hop");
        if target.exists() {
            if let Ok(link) = std::fs::read_link(target) {
                if link != agent {
                    return Err(format!(
                        "/usr/local/bin/hop already exists and points at {} (not our bundle); manual cleanup required",
                        link.display()
                    ));
                }
            } else {
                return Err("/usr/local/bin/hop already exists as a regular file (not a symlink); manual cleanup required".to_string());
            }
        }
        let script = format!(
            r#"do shell script "mkdir -p /usr/local/bin && ln -sf '{}' /usr/local/bin/hop" with administrator privileges"#,
            agent.display()
        );
        run_osascript(&script).map(|_| "/usr/local/bin/hop installed".to_string())
    }
}

/// Build the osascript command for the bundled-→-system "convert"
/// flow. Run with administrator privileges from
/// `convert_to_system_service`. Two steps in one privileged shell so
/// the user sees ONE admin prompt:
///
///   1. cp <bundled hop-agent> /usr/local/bin/hop-agent — gives
///      the launchd plist a stable binary path to point at, decoupled
///      from the .app's bundle.
///   2. /usr/local/bin/hop-agent install --migrate-from <user-config>
///      — moves enrollments user-mode → /etc/hop-agent, installs the
///      LaunchDaemon, writes the mirror-token file the .app reads.
///
/// Caller (`convert_to_system_service`) MUST call AppState::shutdown_agent()
/// BEFORE this script runs — otherwise the bundled child still holds
/// UDP :4242 and the new system agent fails to bind. See tripwire test
/// `convert_command_stops_bundled_first_then_admin_prompt`.
fn build_convert_script(agent_path: &std::path::Path, user_config: &std::path::Path) -> String {
    format!(
        r#"do shell script "cp '{0}' /usr/local/bin/hop-agent && /usr/local/bin/hop-agent install --migrate-from '{1}'" with administrator privileges"#,
        agent_path.display(),
        user_config.display()
    )
}

/// Build the osascript command for the system-→-bundled "revert" flow.
/// Mirror of build_convert_script:
///
///   1. /usr/local/bin/hop-agent uninstall (no --purge — keep configs)
///      stops + unloads the LaunchDaemon, removes the plist + binary.
///   2. Move /etc/hop-agent/enrollments.json + per-enrollment subdirs
///      back into the user's configDir.
///   3. **chown back to the console user**. The username is BAKED IN
///      at command-build time from the calling Tauri process's $USER
///      env var (which IS the console user — Tauri runs as the user).
///      We CANNOT defer to a runtime $SUDO_USER inside the privileged
///      shell because `osascript ... with administrator privileges`
///      uses Apple's SecurityAuthorization framework, NOT sudo —
///      $SUDO_USER is empty there, and $USER is "root". Verified the
///      hard way in v0.10.56: a source-scan tripwire passed, but the
///      runtime chown was a no-op and configs stayed root-owned.
///   4. Remove the stale mirror token + port files so the next launch's
///      try_attach_to_system_agent probe correctly falls through to
///      bundled spawn (instead of trying to TCP-connect to a dead
///      system-agent port).
fn build_revert_script(user_config: &std::path::Path, console_user: &str) -> String {
    let user_config_str = user_config.display();
    format!(
        r#"do shell script "/usr/local/bin/hop-agent uninstall && mkdir -p '{0}' && (cd /etc/hop-agent 2>/dev/null && (cp -R . '{0}/' && rm -rf /etc/hop-agent/* /etc/hop-agent/.[!.]* 2>/dev/null) || true) && rm -rf /etc/hop-agent && chown -R '{1}':staff '{0}' && rm -f '{0}/system-local-api-token' '{0}/system-local-api-port'" with administrator privileges"#,
        user_config_str,
        console_user
    )
}

/// Build the osascript command for `hop-agent uninstall` with the
/// given flag set. Extracted from the Tauri commands so it's
/// directly testable — the flag combos are load-bearing (Reset path
/// MUST keep the binary; Uninstall path MUST remove it; both MUST
/// pass --yes to skip the agent's stdin confirmation prompt, which
/// would hang the osascript-spawned shell). See tripwire tests at
/// the bottom of this file.
fn build_uninstall_script(agent_path: &std::path::Path, remove_binary: bool) -> String {
    let binary_flag = if remove_binary {
        "--remove-binary"
    } else {
        "--remove-binary=false"
    };
    format!(
        r#"do shell script "'{}' uninstall --purge {} --yes" with administrator privileges"#,
        agent_path.display(),
        binary_flag
    )
}

/// Reset hopssh — remove enrollments + certs but leave the binary +
/// system service installed. Equivalent of:
///
///   hop-agent uninstall --purge --remove-binary=false --yes
///
/// One admin prompt; agent restarts back to fresh-install state and
/// can re-enroll immediately. Wires through osascript so the
/// privileged CLI invocation has the same admin-prompt UX as the
/// existing install_system_service / uninstall_cli_symlink commands.
#[tauri::command]
fn reset_hopssh() -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        return Err("reset_hopssh: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        let script = build_uninstall_script(&agent, false);
        run_osascript(&script).map(|out| {
            if out.trim().is_empty() {
                "hopssh reset to fresh-install state".to_string()
            } else {
                out
            }
        })
    }
}

/// Uninstall hopssh — full removal of agent integrations + binary.
/// Equivalent of:
///
///   hop-agent uninstall --purge --remove-binary --yes
///
/// One admin prompt. Cannot remove the running .app from inside
/// itself, so the success message instructs the user to drag
/// /Applications/hopssh.app to the Trash. The UI shows that message
/// in a banner along with a Quit button (calls `quit_app`).
#[tauri::command]
fn uninstall_hopssh_full() -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        return Err("uninstall_hopssh_full: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        let script = build_uninstall_script(&agent, true);
        run_osascript(&script).map(|_| {
            "Uninstall complete. Quit hopssh and drag /Applications/hopssh.app to the Trash to finish.".to_string()
        })
    }
}

/// Quit the desktop app. Used by the post-uninstall banner's "Quit"
/// button so the user can complete the macOS uninstall flow (drag .app
/// to Trash) without hunting for the Apple-menu Quit item. Drops AppState,
/// which kills the child hop-agent process via ctrlc handler / RunEvent::Exit.
#[tauri::command]
fn quit_app(app: AppHandle) {
    app.exit(0);
}

/// Convert from "bundled" mode (the .app spawns its own hop-agent
/// child) to "system" mode (a launchd LaunchDaemon runs the agent as
/// root). User-facing equivalent of "Run in the background".
///
/// Sequence (load-bearing — see Plan A3 + the C1 inline-attach fix):
///   1. Stop the bundled child via AppState::shutdown_agent(). This
///      releases UDP :4242 BEFORE the new system agent tries to bind.
///   2. Single osascript admin prompt that copies the binary into
///      /usr/local/bin/hop-agent and runs `hop-agent install
///      --migrate-from <user-config>`. The migrate step moves
///      enrollments + writes the mirror token file the .app will
///      read on its next refresh.
///   3. Invalidate the cached endpoint AND inline-probe for the
///      system agent's newly-written mirror token + port. Without
///      this step the .app's spawn_and_watch only ran ONCE at launch
///      and won't re-discover the system agent — the WebView would
///      show "agent unreachable" until the user manually relaunches.
///   4. Emit `agent-ready` so the JS layer's agent.refresh() fires.
///
/// On error (admin prompt cancelled, migration failed): the bundled
/// child is dead but no system agent → return Err so the UI surfaces
/// it instead of silently leaving the .app in unreachable state.
#[tauri::command]
fn convert_to_system_service(app: AppHandle, state: State<'_, Arc<AppState>>) -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (app, state);
        return Err("convert_to_system_service: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        // Step 1 — stop bundled FIRST. Any other ordering racey-fails
        // at the kernel UDP-bind stage.
        state.shutdown_agent();

        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        let home = std::env::var("HOME")
            .map_err(|_| "HOME env var not set".to_string())?;
        let user_config = std::path::PathBuf::from(home)
            .join("Library")
            .join("Application Support")
            .join("hopssh");

        // Step 2 — privileged migration. Single admin prompt.
        let script = build_convert_script(&agent, &user_config);
        run_osascript(&script)?;

        // Step 3 — invalidate cached endpoint, then inline-probe for
        // the new system agent. launchd takes ~200-500ms to spawn the
        // daemon + write the mirror-token + port file, so we retry
        // with backoff for up to 5s.
        state.endpoint.lock().take();
        let endpoint = wait_for_system_agent(std::time::Duration::from_secs(5))
            .ok_or_else(|| {
                "Migration succeeded but the system agent didn't come up within 5s. \
                 Try toggling Run in the background again, or check Console.app for \
                 com.hopssh.agent errors."
                    .to_string()
            })?;
        log::info!("post-convert attached to system agent at {}", endpoint.host);
        *state.endpoint.lock() = Some(endpoint);

        // Step 4 — tell the JS layer the endpoint is fresh; it'll
        // re-fetch /local/status on the next refresh.
        let _ = app.emit("agent-ready", ());

        // Phase Y (F5, v0.10.90): the user just opted into "Run in
        // the background" — system mode means hopssh is meant to be
        // always-on. The natural mental-model extension: the tray
        // icon should appear on login too. Auto-enable autostart,
        // BUT only if the user hasn't already explicitly toggled it
        // (start_at_login_explicit=false). If they toggled it OFF
        // before, respect that choice — the user wins.
        let prefs = read_desktop_prefs();
        if !prefs.start_at_login_explicit && !prefs.start_at_login {
            let mut new_prefs = prefs;
            new_prefs.start_at_login = true;
            // start_at_login_explicit stays false here — this is an
            // auto-default, not a user-explicit choice. If the user
            // later flips the toggle in Settings, set_start_at_login
            // sets the explicit flag and locks in their preference.
            if let Err(e) = write_desktop_prefs(&new_prefs) {
                log::warn!("failed to persist auto-enabled start_at_login: {e}");
            } else if let Err(e) = sync_autostart_with_pref(&app, true) {
                log::warn!("failed to register LaunchAgent for auto-enabled start_at_login: {e}");
            } else {
                log::info!("autostart auto-enabled on first system-mode opt-in");
            }
        }

        Ok("hopssh now runs in the background".to_string())
    }
}

/// wait_for_system_agent retries try_attach_to_system_agent every 200ms
/// until it succeeds or the budget runs out. Used by the convert flow
/// to bridge the gap between launchctl bootstrap returning and launchd
/// actually spawning the daemon + the daemon writing its mirror files.
#[cfg(target_os = "macos")]
fn wait_for_system_agent(budget: std::time::Duration) -> Option<LocalAgentEndpoint> {
    use std::time::Instant;
    let deadline = Instant::now() + budget;
    while Instant::now() < deadline {
        if let Some(ep) = agent::try_attach_to_system_agent() {
            return Some(ep);
        }
        std::thread::sleep(std::time::Duration::from_millis(200));
    }
    // One last try after the deadline elapsed.
    agent::try_attach_to_system_agent()
}

/// Revert from "system" mode back to "bundled" mode. Mirror of
/// convert_to_system_service.
///
/// Sequence:
///   1. Single osascript admin prompt that runs `hop-agent uninstall`
///      (no --purge) to stop + unload the LaunchDaemon, then moves
///      /etc/hop-agent/* back into the user's configDir.
///   2. Invalidate cached endpoint, then inline-spawn a fresh bundled
///      child via agent::spawn_and_watch and emit agent-ready. Same
///      reasoning as convert_to_system_service: don't leave the .app
///      stranded waiting for an event that may never fire.
#[tauri::command]
fn revert_to_bundled(app: AppHandle, state: State<'_, Arc<AppState>>) -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (app, state);
        return Err("revert_to_bundled: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let home = std::env::var("HOME")
            .map_err(|_| "HOME env var not set".to_string())?;
        let user_config = std::path::PathBuf::from(home)
            .join("Library")
            .join("Application Support")
            .join("hopssh");

        // Resolve the console user from the calling Tauri process's
        // env. The Tauri shell runs AS the console user, so $USER is
        // exactly what we need to bake into the privileged osascript
        // (where $USER would be "root" and $SUDO_USER would be empty).
        let console_user = std::env::var("USER")
            .map_err(|_| "USER env var not set; cannot resolve console user for chown".to_string())?;
        if console_user == "root" || console_user.is_empty() {
            return Err(format!(
                "refusing to revert: $USER is {:?} (expected console user, not root)",
                console_user
            ));
        }

        let script = build_revert_script(&user_config, &console_user);
        run_osascript(&script)?;

        // Invalidate so the next attach probe sees no system agent.
        state.endpoint.lock().take();

        // Re-run spawn_and_watch — the system mirror files are now
        // gone (the script's `hop-agent uninstall` removed them via
        // the launchd uninstall path), so spawn_and_watch's probe
        // misses the system path and falls through to spawning a
        // fresh bundled child against the now-restored user
        // configDir.
        let state_clone = Arc::clone(&*state);
        if let Err(e) = agent::spawn_and_watch(&app, &state_clone) {
            return Err(format!("uninstalled system service but failed to restart bundled agent: {e}"));
        }

        Ok("hopssh now runs only while the .app is open".to_string())
    }
}

/// Remove /usr/local/bin/hop only if it's a symlink to our bundle —
/// won't touch any unrelated `hop` binary the user installed.
#[tauri::command]
fn uninstall_cli_symlink(_app: AppHandle) -> Result<String, String> {
    #[cfg(not(target_os = "macos"))]
    {
        return Err("uninstall_cli_symlink: only supported on macOS".to_string());
    }
    #[cfg(target_os = "macos")]
    {
        let agent = resolve_agent_path()
            .ok_or_else(|| "could not locate bundled hop-agent binary".to_string())?;
        let target = std::path::Path::new("/usr/local/bin/hop");
        if !target.exists() {
            return Ok("/usr/local/bin/hop not present; nothing to do".to_string());
        }
        match std::fs::read_link(target) {
            Ok(link) if link == agent => {
                let script = r#"do shell script "rm /usr/local/bin/hop" with administrator privileges"#;
                run_osascript(script).map(|_| "/usr/local/bin/hop removed".to_string())
            }
            Ok(link) => Err(format!(
                "/usr/local/bin/hop points at {} (not our bundle); refusing to remove",
                link.display()
            )),
            Err(_) => Err("/usr/local/bin/hop is not a symlink; refusing to remove".to_string()),
        }
    }
}

/// Run an osascript with the given AppleScript source. Returns stdout
/// on success, stderr-prefixed on failure. macOS-only; protected by
/// caller's #[cfg(target_os = "macos")] guard.
#[cfg(target_os = "macos")]
fn run_osascript(script: &str) -> Result<String, String> {
    let out = std::process::Command::new("/usr/bin/osascript")
        .arg("-e")
        .arg(script)
        .output()
        .map_err(|e| format!("osascript spawn failed: {e}"))?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
        // Cancel-on-prompt → exit code 1 with this stderr.
        if stderr.contains("User canceled") {
            return Err("admin prompt cancelled by user".to_string());
        }
        return Err(format!("osascript failed: {stderr}"));
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

/// JS calls this when the aggregate connection state changes. Valid
/// values: "connected", "relay", "disconnected". Anything else falls
/// back to "disconnected" (safer than panicking on bad input).
#[tauri::command]
fn set_tray_state(app: AppHandle, state: String) -> Result<(), String> {
    let bytes: &[u8] = match state.as_str() {
        "connected" => include_bytes!("../icons/tray/tray-connected@2x.png"),
        "relay" => include_bytes!("../icons/tray/tray-relay@2x.png"),
        _ => include_bytes!("../icons/tray/tray-disconnected@2x.png"),
    };
    let img = tauri::image::Image::from_bytes(bytes).map_err(|e| e.to_string())?;
    if let Some(tray) = app.tray_by_id("main") {
        tray.set_icon(Some(img)).map_err(|e| e.to_string())?;
        // set_icon replaces the NSImage; the template flag lives on the
        // image, not on the tray, so it must be re-applied after every
        // swap. Without this the icon renders as opaque dark RGBA on a
        // dark menubar instead of macOS-auto-flipping with the bg.
        tray.set_icon_as_template(true).map_err(|e| e.to_string())?;
    }
    Ok(())
}

pub fn run() {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info")).init();

    let app_state = Arc::new(AppState::default());
    let state_for_setup = Arc::clone(&app_state);

    // SIGINT/SIGTERM handler: kill the child agent and exit cleanly. Without
    // this, kill <pid> on the parent leaves the agent running. Drop's not
    // invoked under signal-driven exit.
    let state_for_signal = Arc::clone(&app_state);
    if let Err(e) = ctrlc::set_handler(move || {
        log::info!("received termination signal, shutting down");
        state_for_signal.shutdown_agent();
        std::process::exit(0);
    }) {
        log::warn!("could not install signal handler: {e}");
    }

    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        // Phase Y (v0.10.90): autostart on macOS login. The plugin
        // manages ~/Library/LaunchAgents/<bundle-id>.plist when the
        // user enables autostart via Settings → "Open hopssh on
        // login". The `--start-minimized` arg is passed to the .app
        // when launchd auto-launches it; we detect the arg in
        // setup() and hide the main window so the user only sees
        // the menubar icon, not a popped-up window. See F7 + F4 in
        // the Phase Y plan.
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            Some(vec!["--start-minimized"]),
        ))
        .manage(app_state)
        .invoke_handler(tauri::generate_handler![
            local_api_endpoint,
            retry_attach_system_agent,
            get_start_at_login,
            set_start_at_login,
            show_main_window,
            set_tray_tooltip,
            set_tray_state,
            install_status,
            install_system_service,
            uninstall_system_service,
            install_cli_symlink,
            uninstall_cli_symlink,
            reset_hopssh,
            uninstall_hopssh_full,
            quit_app,
            convert_to_system_service,
            revert_to_bundled,
            get_hide_from_dock,
            set_hide_from_dock,
            check_remote_version,
            install_update_mac
        ])
        .setup(move |app| {
            // Phase N: re-apply the persisted "Hide from Dock"
            // preference at launch. If the user toggled the option
            // ON in a previous session and the .app is starting
            // again, demote the activation policy to .Accessory
            // BEFORE the window appears. Since tauri.conf.json sets
            // visible:true, we'd flash the Dock icon for one frame
            // before the policy change otherwise — order matters.
            #[cfg(target_os = "macos")]
            if read_desktop_prefs().hide_from_dock {
                let _ = app.set_activation_policy(tauri::ActivationPolicy::Accessory);
            }

            // Phase Y (F7, v0.10.90): if launchd auto-launched us with
            // `--start-minimized`, hide the main window so the user
            // only sees the menubar icon. The tauri-plugin-autostart
            // passes this arg when the LaunchAgent fires on login;
            // double-clicking the .app from /Applications never
            // includes the arg. Without this, the user gets a window
            // popping up unprompted on every login — wrong UX for an
            // always-on tray utility.
            //
            // Order: this runs AFTER the hide_from_dock policy switch
            // so we don't fight policy + visibility at the same time.
            // tauri.conf.json's visible:true creates the window; we
            // hide it here BEFORE the WebView paints anything visible.
            let started_minimized = std::env::args().any(|a| a == "--start-minimized");
            if started_minimized {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.hide();
                }
                log::info!("autostart launch detected (--start-minimized); window hidden, tray only");
            }

            // Phase Y (F4, v0.10.90): self-heal autostart state.
            // The persisted pref is the user's intent; the LaunchAgent
            // file on disk is the actual state. They should match —
            // sync_autostart_with_pref reconciles them. This handles
            // cases where the user manually deleted the LaunchAgent
            // via System Settings, or the .app upgraded across the
            // boundary that introduced this plugin (Phase Y itself).
            let prefs = read_desktop_prefs();
            if let Err(e) = sync_autostart_with_pref(&app.handle(), prefs.start_at_login) {
                // Non-fatal — log and continue. The user can fix from
                // Settings → "Open hopssh on login".
                log::warn!("autostart sync at startup failed: {e}");
            }

            // Build menubar tray menu.
            let show = MenuItem::with_id(app, "show", "Show hopssh", true, None::<&str>)?;
            let add = MenuItem::with_id(app, "add", "Add a network…", true, None::<&str>)?;
            let sep1 = PredefinedMenuItem::separator(app)?;
            let check_update = MenuItem::with_id(
                app,
                "checkUpdate",
                "Check for updates…",
                true,
                None::<&str>,
            )?;
            let sep2 = PredefinedMenuItem::separator(app)?;
            let about = MenuItem::with_id(app, "about", "About hopssh", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit", "Quit hopssh", true, None::<&str>)?;
            let menu = Menu::with_items(
                app,
                &[&show, &add, &sep1, &check_update, &sep2, &about, &quit],
            )?;

            // Use the four-dots template icon at boot, NOT
            // app.default_window_icon() — that's the colorful 128px
            // app icon and lights up with a background blob in the
            // menubar. The template variant is monochrome + alpha so
            // macOS auto-flips it for dark/light menubar without a
            // background. State updates after the agent connects swap
            // to connected/relay variants via set_tray_state.
            let boot_icon_bytes: &[u8] = include_bytes!("../icons/tray/tray-disconnected@2x.png");
            let boot_icon = tauri::image::Image::from_bytes(boot_icon_bytes)
                .expect("tray-disconnected@2x.png must be valid PNG");
            // Tray click (left or right) opens the menu — the tray is
            // never used to show/hide the window directly. The window
            // appears (a) on app launch via tauri.conf.json's
            // "visible": true, (b) via the "Show hopssh" menu item, or
            // (c) on RunEvent::Reopen when the user clicks the Dock
            // icon while the app is already running with the window
            // hidden (see app.run handler below).
            //
            // show_menu_on_left_click(true) tells muda/NSStatusItem to
            // pop the menu on every primary-button click — same as
            // right click. No on_tray_icon_event handler needed; the
            // menu's on_menu_event below handles all user actions.
            let _tray = TrayIconBuilder::with_id("main")
                .icon(boot_icon)
                .icon_as_template(true)
                .menu(&menu)
                .show_menu_on_left_click(true)
                .on_menu_event(|app, event| {
                    let show_window = || {
                        if let Some(w) = app.get_webview_window("main") {
                            let _ = w.show();
                            let _ = w.unminimize();
                            let _ = w.set_focus();
                        }
                    };
                    match event.id.as_ref() {
                        "show" | "about" => show_window(),
                        "add" => {
                            show_window();
                            let _ = app.emit("tray-action", "add");
                        }
                        "checkUpdate" => {
                            show_window();
                            let _ = app.emit("tray-action", "checkUpdate");
                        }
                        "quit" => app.exit(0),
                        _ => {}
                    }
                })
                .build(app)?;

            // Spawn the agent and parse its stdout.
            let app_handle = app.handle().clone();
            let state_for_agent = Arc::clone(&state_for_setup);
            std::thread::spawn(move || {
                if let Err(e) = agent::spawn_and_watch(&app_handle, &state_for_agent) {
                    log::error!("agent spawn watcher exited with error: {e}");
                }
            });

            // Phase Q: watch system-local-api-{port,token} for
            // external daemon respawns (launchctl kickstart, system
            // boot, crash recovery). On mirror-file change, re-probe
            // + replace endpoint + emit agent-ready so the JS layer
            // resets its cached endpoint via the existing Phase D
            // infrastructure.
            agent::watch_system_mirror(app.handle().clone(), Arc::clone(&state_for_setup));

            Ok(())
        })
        .on_window_event(|window, event| {
            // On macOS the window-close button hides the window instead of
            // exiting, matching menubar-app conventions. Use Cmd-Q (or the
            // tray's Quit menu item) for a real exit.
            #[cfg(target_os = "macos")]
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
                // Phase N: when the user has opted in to hiding
                // hopssh from the Dock, demote the activation
                // policy on close. The Dock icon disappears on the
                // next runloop tick; Cmd-Tab no longer lists us;
                // the menubar tray icon stays. Reverse on the next
                // show_main_window / RunEvent::Reopen.
                if read_desktop_prefs().hide_from_dock {
                    let app_handle = window.app_handle().clone();
                    let _ = app_handle.set_activation_policy(tauri::ActivationPolicy::Accessory);
                }
            }
            #[cfg(not(target_os = "macos"))]
            let _ = window;
            #[cfg(not(target_os = "macos"))]
            let _ = event;
        })
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(move |app_handle, event| match event {
            tauri::RunEvent::Exit => {
                if let Some(state) = app_handle.try_state::<Arc<AppState>>() {
                    state.shutdown_agent();
                }
            }
            tauri::RunEvent::ExitRequested { .. } => {
                if let Some(state) = app_handle.try_state::<Arc<AppState>>() {
                    state.shutdown_agent();
                }
            }
            // macOS-only: fires when the user clicks the .app icon in
            // the Dock or double-clicks /Applications/hopssh.app while
            // the app is already running. Without this handler, the
            // click is a no-op when the window is hidden — making the
            // .app feel "broken" to anyone who closes the window via
            // the red dot (which hides on macOS, see on_window_event
            // above).
            #[cfg(target_os = "macos")]
            tauri::RunEvent::Reopen { .. } => {
                // Phase N: if the user previously hid us from the
                // Dock and is now Dock-clicking the .app to bring
                // the window back, restore .Regular FIRST so the
                // Dock icon and Cmd-Tab entry come back, THEN show
                // + focus the window. Skipping the policy flip
                // would leave the window visible without a Dock
                // icon — confusing.
                if read_desktop_prefs().hide_from_dock {
                    let _ = app_handle.set_activation_policy(tauri::ActivationPolicy::Regular);
                }
                if let Some(w) = app_handle.get_webview_window("main") {
                    let _ = w.show();
                    let _ = w.unminimize();
                    let _ = w.set_focus();
                }
            }
            _ => {}
        });
}

/// Resolve the path to the bundled hop-agent binary.
///
/// Resolution order:
///   1. `HOPSSH_AGENT_BINARY` env var (highest priority, for dev override).
///   2. `<bundle>/Contents/Resources/hop-agent` — production macOS bundle.
///   3. Sibling of the exe — for some dev/test layouts.
///   4. Walk up from the exe and look in `src-tauri/binaries/hop-agent` —
///      `cargo tauri dev` resolves the exe to target/debug/<bin>, so this
///      finds the dev-built sidecar without env config.
pub fn resolve_agent_path() -> Option<PathBuf> {
    if let Ok(p) = std::env::var("HOPSSH_AGENT_BINARY") {
        let p = PathBuf::from(p);
        if p.exists() {
            return Some(p);
        }
    }

    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            // Production macOS: <bundle>/Contents/MacOS/<bin>
            //                   + Contents/Resources/(binaries/)hop-agent
            // Tauri preserves the relative path of bundle resources, so
            // when bundle.resources lists "binaries/hop-agent" the file
            // ends up at Resources/binaries/hop-agent.
            let candidates = [
                parent.join("../Resources/binaries/hop-agent"),
                parent.join("../Resources/hop-agent"),
                parent.join("hop-agent"),
            ];
            for c in candidates {
                if c.exists() {
                    return Some(c);
                }
            }
        }
        // Walk up to find src-tauri/binaries/hop-agent — used by cargo
        // tauri dev when the binary lives at the project root.
        let mut p = exe.clone();
        for _ in 0..7 {
            if !p.pop() {
                break;
            }
            let candidate = p.join("src-tauri/binaries/hop-agent");
            if candidate.exists() {
                return Some(candidate);
            }
            let candidate2 = p.join("hop-agent");
            if candidate2.exists() {
                return Some(candidate2);
            }
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    /// Tripwire: the Reset path MUST keep the binary AND pass --yes.
    /// Without --yes the agent waits for stdin which the
    /// osascript-spawned shell can't supply, hanging the UI.
    #[test]
    fn reset_command_keeps_binary_and_skips_prompt() {
        let path = Path::new("/usr/local/bin/hop-agent");
        let s = build_uninstall_script(path, false);
        assert!(s.contains("--purge"), "Reset must include --purge: {s}");
        assert!(
            s.contains("--remove-binary=false"),
            "Reset MUST keep the binary (--remove-binary=false): {s}"
        );
        assert!(
            !s.contains("--remove-binary "),
            "Reset must NOT use bare --remove-binary (which would delete it): {s}"
        );
        assert!(
            s.contains("--yes"),
            "Reset must pass --yes (osascript shell has no stdin): {s}"
        );
        assert!(
            !s.contains("--remove-logs"),
            "Reset must NOT remove logs (forensic value preserved): {s}"
        );
    }

    /// Tripwire: the Uninstall path MUST remove the binary AND pass --yes.
    #[test]
    fn uninstall_full_command_removes_binary_and_skips_prompt() {
        let path = Path::new("/usr/local/bin/hop-agent");
        let s = build_uninstall_script(path, true);
        assert!(s.contains("--purge"), "Uninstall must include --purge: {s}");
        assert!(
            s.contains("--remove-binary "),
            "Uninstall must include bare --remove-binary (the variant that removes): {s}"
        );
        assert!(
            !s.contains("--remove-binary=false"),
            "Uninstall must NOT use --remove-binary=false: {s}"
        );
        assert!(
            s.contains("--yes"),
            "Uninstall must pass --yes (osascript shell has no stdin): {s}"
        );
    }

    /// Tripwire: the AppleScript wrapping uses single-quoted POSIX paths
    /// to survive paths with spaces, and runs with administrator
    /// privileges so the agent can remove root-owned files like the
    /// LaunchDaemon plist.
    #[test]
    fn uninstall_command_runs_privileged() {
        let path = Path::new("/Applications/hopssh.app/Contents/Resources/binaries/hop-agent");
        let s = build_uninstall_script(path, true);
        assert!(
            s.contains("with administrator privileges"),
            "must request admin to remove root-owned files: {s}"
        );
        assert!(s.contains("'/Applications/hopssh.app/"));
    }

    /// Tripwire: the convert-to-system script must include both the
    /// binary copy AND the `--migrate-from` invocation in a SINGLE
    /// shell so the user sees ONE admin prompt. Splitting them across
    /// two osascript calls would prompt twice — usability regression.
    #[test]
    fn convert_command_single_admin_prompt() {
        let agent = Path::new("/Applications/hopssh.app/Contents/Resources/binaries/hop-agent");
        let user_config = Path::new("/Users/alice/Library/Application Support/hopssh");
        let s = build_convert_script(agent, user_config);
        assert!(s.contains("cp '"), "must copy the bundled binary: {s}");
        assert!(s.contains("/usr/local/bin/hop-agent"), "must target /usr/local/bin: {s}");
        assert!(s.contains("install --migrate-from"), "must invoke migrate-from: {s}");
        assert!(s.contains("'/Users/alice/"), "must quote the user config path: {s}");
        assert!(
            s.contains("with administrator privileges"),
            "must request admin in a single prompt: {s}"
        );
        // The two steps should be chained with `&&`, not separate
        // osascript calls. A second `do shell script` would mean two
        // admin prompts.
        assert_eq!(
            s.matches("do shell script").count(),
            1,
            "convert script must use exactly one privileged shell: {s}"
        );
    }

    /// Tripwire: the revert-to-bundled script must (a) `hop-agent
    /// uninstall` to stop the LaunchDaemon AND (b) move the configs
    /// back to the user's dir, in a single admin prompt.
    #[test]
    fn revert_command_uninstall_then_migrate_back() {
        let user_config = Path::new("/Users/alice/Library/Application Support/hopssh");
        let s = build_revert_script(user_config, "alice");
        assert!(s.contains("uninstall"), "must run hop-agent uninstall: {s}");
        assert!(s.contains("/etc/hop-agent"), "must reference system configDir: {s}");
        assert!(s.contains("'/Users/alice/"), "must reference user config dest: {s}");
        assert!(
            s.contains("with administrator privileges"),
            "must request admin: {s}"
        );
        assert_eq!(
            s.matches("do shell script").count(),
            1,
            "revert script must use exactly one privileged shell: {s}"
        );
    }

    /// Tripwire (locked in after the v0.10.56 broken-revert incident):
    /// the username for chown MUST be baked into the script string at
    /// command-build time. We CANNOT defer to a runtime $SUDO_USER or
    /// $USER inside `osascript ... with administrator privileges`:
    /// Apple's SecurityAuthorization-driven privileged execution does
    /// NOT set $SUDO_USER (only sudo does), and $USER inside the root
    /// shell is "root". Both would chown to wrong user, leaving the
    /// migrated configs unreadable to the bundled hop-agent child.
    ///
    /// This is a BEHAVIOR test: it asserts the LITERAL username is in
    /// the script and the unexpanded-shell-var forms are NOT. The
    /// previous v0.10.56 test was a source-scan that asserted the
    /// presence of the (broken) "$SUDO_USER" string — passed but the
    /// runtime chown was a silent no-op.
    #[test]
    fn revert_command_bakes_console_user_at_build_time() {
        let user_config = Path::new("/Users/alice/Library/Application Support/hopssh");
        let s = build_revert_script(user_config, "alice");
        assert!(
            s.contains("'alice':staff"),
            "username must be baked in literally: {s}"
        );
        assert!(
            !s.contains("$SUDO_USER"),
            "must NOT defer to runtime $SUDO_USER (osascript admin doesn't set it): {s}"
        );
        assert!(
            !s.contains("$USER"),
            "must NOT defer to runtime $USER (= 'root' inside admin shell): {s}"
        );
        assert!(s.contains("chown"), "must chown the migrated configs: {s}");
    }

    /// Tripwire: revert must also remove the stale mirror token + port
    /// files. Otherwise the next launch's try_attach_to_system_agent
    /// probe reads them, attempts a TCP-connect to the dead system
    /// agent's port, and falls through anyway — but the visible
    /// effect is a brief "Connecting..." stutter on every relaunch.
    /// Cleaning them at revert time keeps the user's home dir tidy.
    #[test]
    fn revert_command_removes_stale_mirror_files() {
        let user_config = Path::new("/Users/alice/Library/Application Support/hopssh");
        let s = build_revert_script(user_config, "alice");
        assert!(
            s.contains("system-local-api-token"),
            "must rm the stale mirror token: {s}"
        );
        assert!(
            s.contains("system-local-api-port"),
            "must rm the stale mirror port file: {s}"
        );
    }

    /// Tripwire (locked in after dark-menubar tray icon rendered opaque
    /// dark instead of auto-flipping white): set_tray_state MUST call
    /// set_icon_as_template(true) after every set_icon() swap. The
    /// template flag lives on the NSImage, not on the tray; replacing
    /// the image drops the flag.
    #[test]
    fn set_tray_state_reapplies_template_after_set_icon() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        let cmd = src
            .split("fn set_tray_state(")
            .nth(1)
            .expect("set_tray_state command must exist");
        let body = cmd
            .split("\n}\n")
            .next()
            .expect("set_tray_state body must end with closing brace");
        assert!(
            body.contains("set_icon(Some(img))"),
            "set_tray_state must call set_icon — body: {body}"
        );
        assert!(
            body.contains("set_icon_as_template(true)"),
            "set_tray_state MUST call set_icon_as_template(true) after \
             set_icon — without it macOS renders the new NSImage as \
             plain RGBA, not template-flipped. Body: {body}"
        );
    }

    /// Tripwire: pure menubar-app behavior — left click on the tray
    /// icon must open the menu (same as right click), not show the
    /// window. The window appears only via "Show hopssh" or App.svelte
    /// programmatic show. Source-scan because a real Tauri runtime is
    /// out of reach for unit tests.
    #[test]
    fn tray_left_click_opens_menu() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        // Find the TrayIconBuilder::with_id("main") block.
        let block = src
            .split("TrayIconBuilder::with_id(\"main\")")
            .nth(1)
            .expect("tray builder must exist");
        // Truncate at the .build(app)?; site that ends the chain.
        let block = block
            .split(".build(app)?")
            .next()
            .expect("tray builder must call .build(app)");
        assert!(
            block.contains("show_menu_on_left_click(true)"),
            "tray must call .show_menu_on_left_click(true) — left click must open the menu, not the window. Block: {block}"
        );
        assert!(
            !block.contains("on_tray_icon_event"),
            "tray must NOT define an on_tray_icon_event handler — clicks are handled by show_menu_on_left_click(true) + on_menu_event. A handler here would race with the native menu pop. Block: {block}"
        );
    }

    /// Phase Y (v0.10.90) — Tripwire: Cargo.toml MUST list
    /// tauri-plugin-autostart. Without it, the .app has no
    /// mechanism to autostart on macOS login and we regress to the
    /// pre-Phase-Y bug where the agent runs but the GUI doesn't.
    #[test]
    fn cargo_toml_includes_tauri_plugin_autostart() {
        let toml_path = Path::new(env!("CARGO_MANIFEST_DIR")).join("Cargo.toml");
        let toml = std::fs::read_to_string(&toml_path)
            .expect("Cargo.toml must be readable");
        assert!(
            toml.contains("tauri-plugin-autostart"),
            "Cargo.toml must depend on tauri-plugin-autostart for Phase Y autostart"
        );
    }

    /// Phase Y — Tripwire: get_start_at_login + set_start_at_login
    /// Tauri commands MUST be registered in invoke_handler. Otherwise
    /// the JS Settings panel gets "command not found" and the toggle
    /// silently fails.
    #[test]
    fn start_at_login_commands_in_invoke_handler() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        // Locate the invoke_handler array.
        let start = src
            .find("invoke_handler(tauri::generate_handler![")
            .expect("invoke_handler must exist");
        let end = src[start..]
            .find("])")
            .expect("invoke_handler must terminate with `])`");
        let handler_block = &src[start..start + end];
        assert!(
            handler_block.contains("get_start_at_login"),
            "get_start_at_login must be registered in invoke_handler. Block: {handler_block}"
        );
        assert!(
            handler_block.contains("set_start_at_login"),
            "set_start_at_login must be registered in invoke_handler. Block: {handler_block}"
        );
    }

    /// Phase Y — Tripwire: setup() MUST call sync_autostart_with_pref
    /// at startup. This is the self-healing layer — if the pref says
    /// ON but the LaunchAgent file is missing (or vice versa), we
    /// reconcile. Without this, drift between pref and disk is silent.
    #[test]
    fn setup_syncs_autostart_with_pref() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        let setup_marker = ".setup(move |app|";
        let start = src
            .find(setup_marker)
            .expect("setup() block must exist");
        // Take ~6000 chars after the marker to cover the full setup body.
        let end = (start + 6000).min(src.len());
        let setup_body = &src[start..end];
        assert!(
            setup_body.contains("sync_autostart_with_pref"),
            "setup() must call sync_autostart_with_pref so the LaunchAgent state matches the persisted pref at every launch"
        );
    }

    /// Phase Y — Tripwire: setup() MUST detect the --start-minimized
    /// arg and hide the main window when present. This is the
    /// autostart-launch UX: tray icon visible, window hidden until
    /// user clicks. Without this the user sees a window pop up on
    /// every login — wrong UX for an always-on tray utility.
    #[test]
    fn setup_handles_start_minimized_arg() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        let setup_marker = ".setup(move |app|";
        let start = src
            .find(setup_marker)
            .expect("setup() block must exist");
        let end = (start + 6000).min(src.len());
        let setup_body = &src[start..end];
        assert!(
            setup_body.contains("--start-minimized"),
            "setup() must detect the --start-minimized arg passed by tauri-plugin-autostart on login launches"
        );
        // The arg-detection MUST run BEFORE any window.show() in
        // setup() — otherwise the window flashes visible for one
        // frame before being hidden.
        let minimized_idx = setup_body
            .find("--start-minimized")
            .expect("anchor must exist");
        if let Some(show_idx) = setup_body.find(".show()") {
            assert!(
                minimized_idx < show_idx,
                "ORDERING REGRESSION: --start-minimized detection (offset {minimized_idx}) must come BEFORE any window.show() (offset {show_idx}) in setup() — otherwise the window flashes visible on autostart launches"
            );
        }
    }

    /// Phase Y — Tripwire: convert_to_system_service auto-enables
    /// autostart on first opt-in. The user's mental model is
    /// "always-on hopssh = tray on login too" — this auto-default
    /// matches that. The auto-enable respects start_at_login_explicit
    /// so it only fires when the user hasn't already touched the toggle.
    #[test]
    fn convert_auto_enables_autostart_on_first_opt_in() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        let fn_marker = "fn convert_to_system_service(";
        let start = src
            .find(fn_marker)
            .expect("convert_to_system_service must exist");
        // Find the next standalone "fn " or "/// " block start that
        // marks the end of this function.
        let after = &src[start..];
        let body_end = after
            .find("\n/// wait_for_system_agent")
            .or_else(|| after.find("\n/// Revert from"))
            .unwrap_or(after.len());
        let body = &after[..body_end];

        assert!(
            body.contains("start_at_login_explicit"),
            "convert_to_system_service must check start_at_login_explicit so it only auto-enables when the user hasn't already touched the toggle"
        );
        assert!(
            body.contains("sync_autostart_with_pref"),
            "convert_to_system_service must call sync_autostart_with_pref to register the LaunchAgent on first opt-in"
        );
    }

    /// Phase Y — Behavioral: writing start_at_login=true to the prefs
    /// file and reading it back round-trips. Belt-and-braces against
    /// a future refactor that breaks the serde derive on DesktopPrefs.
    #[test]
    fn start_at_login_pref_round_trip() {
        // Use a tempdir + override HOME via env so write_desktop_prefs
        // doesn't pollute the real ~/Library/Application Support/hopssh.
        let tmp = tempdir_lite();
        let saved_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", &tmp);

        let prefs = DesktopPrefs {
            hide_from_dock: false,
            start_at_login: true,
            start_at_login_explicit: true,
        };
        write_desktop_prefs(&prefs).expect("write_desktop_prefs must succeed");
        let loaded = read_desktop_prefs();
        assert_eq!(loaded.start_at_login, true);
        assert_eq!(loaded.start_at_login_explicit, true);
        assert_eq!(loaded.hide_from_dock, false);

        // Restore HOME.
        if let Some(h) = saved_home {
            std::env::set_var("HOME", h);
        } else {
            std::env::remove_var("HOME");
        }

        // Cleanup.
        let _ = std::fs::remove_dir_all(&tmp);
    }

    /// Helper: create a unique tempdir-style path. Avoids pulling in
    /// the `tempfile` crate just for one test.
    fn tempdir_lite() -> std::path::PathBuf {
        let pid = std::process::id();
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.subsec_nanos())
            .unwrap_or(0);
        let dir = std::env::temp_dir().join(format!("hopssh-phasey-test-{pid}-{nanos}"));
        std::fs::create_dir_all(&dir).expect("tempdir must be creatable");
        dir
    }

    /// v0.10.89 (Phase X) — behavioral test for endpoint_alive.
    /// Bind a local TCP listener, probe → ok. Close listener, probe →
    /// false. Loopback connect-refused returns instantly (no flakes).
    #[test]
    fn endpoint_alive_handles_dead_port() {
        use crate::LocalAgentEndpoint;
        use std::net::TcpListener;

        let ln = TcpListener::bind("127.0.0.1:0").expect("bind loopback");
        let host = ln.local_addr().expect("local_addr").to_string();
        let ep = LocalAgentEndpoint {
            host: host.clone(),
            token: "test-token".into(),
        };
        assert!(
            crate::agent::endpoint_alive(&ep),
            "endpoint_alive must return true for a bound port (host={host})"
        );

        // Close the listener; the port becomes unbound (kernel rejects
        // SYN with RST → connect_refused, returned instantly).
        drop(ln);

        // Tiny grace for the kernel to fully release the bind. macOS
        // is fast here; 50ms is plenty.
        std::thread::sleep(std::time::Duration::from_millis(50));
        assert!(
            !crate::agent::endpoint_alive(&ep),
            "endpoint_alive must return false after the listener is dropped (host={host})"
        );
    }

    /// v0.10.89 (Phase X) — endpoint_alive must reject malformed host
    /// strings without panicking. Defense against future regressions
    /// where state.endpoint.host gets a non-SocketAddr value.
    #[test]
    fn endpoint_alive_rejects_malformed_host() {
        use crate::LocalAgentEndpoint;
        let ep = LocalAgentEndpoint {
            host: "not-a-socket-addr".into(),
            token: "test".into(),
        };
        assert!(
            !crate::agent::endpoint_alive(&ep),
            "endpoint_alive must return false for unparseable host"
        );
    }

    /// v0.10.89 (Phase X) — Tripwire: retry_attach_system_agent MUST
    /// use endpoint_alive (not bare is_some) when deciding whether to
    /// re-probe. Without this, the Retry button no-ops against a
    /// stale endpoint pointing at a dead daemon port — the exact
    /// regression Phase X exists to prevent.
    #[test]
    fn retry_attach_system_agent_uses_endpoint_alive() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        let fn_marker = "fn retry_attach_system_agent(";
        let start = src
            .find(fn_marker)
            .expect("retry_attach_system_agent must exist");
        // Body ends at the next `#[tauri::command]` or `#[cfg(target_os` block,
        // whichever comes first. Approximate by scanning for the next "\n#["
        // marker.
        let after = &src[start..];
        let body_end = after.find("\n#[").unwrap_or(after.len());
        let body = &after[..body_end];

        assert!(
            body.contains("endpoint_alive"),
            "retry_attach_system_agent must call endpoint_alive — \
             bare is_some leaves the .app stuck against a dead-port endpoint. \
             Body: {body}"
        );
        // The clear-before-reattach invariant: state.endpoint.lock() = None
        // must run between the stale check and try_attach_to_system_agent.
        let stale_idx = body.find("endpoint_alive").unwrap();
        let clear_idx = body
            .find("state.endpoint.lock() = None")
            .expect("retry_attach_system_agent must clear state.endpoint before re-attach");
        let attach_idx = body
            .find("try_attach_to_system_agent(")
            .expect("retry_attach_system_agent must call try_attach_to_system_agent");
        assert!(
            stale_idx < clear_idx && clear_idx < attach_idx,
            "ORDERING REGRESSION: expected endpoint_alive (offset {stale_idx}) → \
             clear (offset {clear_idx}) → try_attach (offset {attach_idx}). \
             Re-ordering breaks the Phase X invariant."
        );
    }

    /// v0.10.89 (Phase X) — Tripwire: watch_system_mirror's periodic
    /// re-probe uses endpoint_alive, not bare is_some. Same rationale
    /// as retry_attach_system_agent's tripwire.
    #[test]
    fn watch_system_mirror_periodic_reprobe_uses_endpoint_alive() {
        let src = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("src/agent.rs"),
        )
        .expect("must be able to read agent.rs");
        // Locate the periodic re-probe block. Anchor on the unique
        // comment phrase we wrote at the top of that block (NOT the
        // generic "Phase X)" marker, which also appears in
        // endpoint_alive's docstring elsewhere in the file).
        let anchor = "Phase X): predicate flipped";
        let start = src
            .find(anchor)
            .expect("Phase X periodic-reprobe anchor must exist in agent.rs");
        // Take a window of ~1500 chars after the anchor — covers the loop body.
        let end = (start + 1500).min(src.len());
        let block = &src[start..end];

        assert!(
            block.contains("endpoint_alive"),
            "periodic re-probe must call endpoint_alive — bare is_some leaves \
             the .app stuck against a dead-port endpoint"
        );
        assert!(
            block.contains("state.endpoint.lock() = None"),
            "periodic re-probe must clear stale state.endpoint before re-attach"
        );
    }

    /// Tripwire: try_attach_to_system_agent MUST retry the TCP-connect
    /// probe multiple times, not commit to a single 500ms shot. v0.10.87
    /// fixes the chronic "hopssh isn't running" false-positive caused by
    /// launch-time races between the .app and the system daemon. A
    /// regression to single-shot probing immediately re-introduces the
    /// stuck-state-after-restart bug.
    #[test]
    fn try_attach_to_system_agent_retries_probe() {
        let src = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("src/agent.rs"),
        )
        .expect("must be able to read agent.rs");
        let fn_marker = "pub(crate) fn try_attach_to_system_agent()";
        let start = src
            .find(fn_marker)
            .expect("try_attach_to_system_agent must exist");
        let body_end = src[start..].find("\npub").unwrap_or(src.len() - start);
        let body = &src[start..start + body_end];
        assert!(
            body.contains("for attempt in 0..10"),
            "try_attach_to_system_agent must retry the TCP-connect probe \
             (look for `for attempt in 0..10`). Single-shot regressed → \
             chronic 'hopssh isn't running' false-positive returns."
        );
        assert!(
            body.contains("connect_timeout"),
            "try_attach_to_system_agent must use connect_timeout"
        );
    }

    /// Tripwire: spawn_and_watch MUST commit to system mode when mirror
    /// files exist, NOT fall through to bundled spawn. Falling through
    /// when the daemon is just slow at launch produces a permanently
    /// stuck .app — the bundled child can't bind ports the daemon
    /// already holds, state.endpoint stays None, UI shows "isn't
    /// running" until quit + relaunch.
    #[test]
    fn spawn_and_watch_commits_to_system_mode_when_mirror_exists() {
        let src = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("src/agent.rs"),
        )
        .expect("must be able to read agent.rs");
        let fn_marker = "pub fn spawn_and_watch(";
        let start = src.find(fn_marker).expect("spawn_and_watch must exist");
        // Body ends at the next "pub fn" or "fn watch_stdout" marker.
        let body_end = src[start..]
            .find("\nfn watch_stdout")
            .or_else(|| src[start..].find("\npub fn watch_system_mirror"))
            .unwrap_or(src.len() - start);
        let body = &src[start..start + body_end];

        assert!(
            body.contains("system_mirror_files_exist()"),
            "spawn_and_watch must check system_mirror_files_exist() before \
             falling through to bundled spawn"
        );
        // The commit point: when mirror files exist, we must `return Ok(())`
        // BEFORE the agent_path / Command::spawn block.
        let mirror_check = body
            .find("system_mirror_files_exist()")
            .expect("system_mirror_files_exist() expected");
        let spawn_call = body
            .find("Command::new(&agent_path)")
            .expect("Command::new(&agent_path) expected");
        assert!(
            mirror_check < spawn_call,
            "spawn_and_watch ORDERING REGRESSION: mirror check (offset {mirror_check}) \
             must come BEFORE bundled Command::spawn (offset {spawn_call})"
        );

        // Confirm there's a return Ok(()) between the mirror check and
        // the bundled spawn — otherwise we fall through.
        let between = &body[mirror_check..spawn_call];
        assert!(
            between.contains("return Ok(())"),
            "spawn_and_watch must `return Ok(())` after handling the \
             mirror-files-exist branch, NOT fall through to bundled spawn. \
             v0.10.87 invariant. Body between checks: {between}"
        );
    }

    /// Tripwire: the install_update_mac AppleScript must run `do script`
    /// BEFORE `activate`. Reverse order causes a chronic two-window
    /// glitch: when Terminal isn't already running, `activate` first
    /// launches Terminal with a default empty window, then `do script`
    /// opens a second window for the actual command. User-visible bug,
    /// reported on v0.10.85 desktop client. Reordering means the
    /// command runs in Terminal's initial window and `activate` just
    /// brings it forward.
    #[test]
    fn install_update_mac_do_script_before_activate() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        // Find the AppleScript heredoc inside install_update_mac. The
        // raw-string opener is unique enough to anchor on.
        let heredoc_open = "r#\"tell application \"Terminal\"";
        let start = src.find(heredoc_open).expect(
            "expected AppleScript heredoc (r#\"tell application \"Terminal\") in install_update_mac"
        );
        // Heredoc closes with the matching r#"..."# delimiter.
        let after = &src[start..];
        let end = after.find("\"#").expect("heredoc end (\"#) not found");
        let script = &after[..end];

        let do_idx = script.find("do script").expect(
            "install_update_mac AppleScript must call `do script`"
        );
        let act_idx = script.find("activate").expect(
            "install_update_mac AppleScript must call `activate`"
        );
        assert!(
            do_idx < act_idx,
            "install_update_mac ORDERING REGRESSION: `do script` (offset {do_idx}) must come BEFORE `activate` (offset {act_idx}) — reverse order opens TWO Terminal windows when Terminal isn't already running. Script: {script}"
        );
    }

    /// Tripwire: the WebView CSP `connect-src` must include
    /// https://hopssh.com so the Updates panel's manualCheck() fetch
    /// to /version succeeds. Without this, the WebView's CSP blocks
    /// the fetch and the UI shows "Load failed" with no diagnostic.
    /// User-visible enough to guard with a tripwire.
    #[test]
    fn tauri_conf_csp_allows_hopssh_com() {
        let conf_path =
            Path::new(env!("CARGO_MANIFEST_DIR")).join("tauri.conf.json");
        let conf = std::fs::read_to_string(&conf_path)
            .expect("tauri.conf.json must exist next to Cargo.toml");
        // Find the CSP string. Must include https://hopssh.com in the
        // connect-src directive (otherwise fetch from the WebView is
        // blocked and the Updates panel's manualCheck shows "Load
        // failed").
        assert!(
            conf.contains("https://hopssh.com"),
            "tauri.conf.json CSP must allow https://hopssh.com so the Updates panel's fetch to /version works. WebView CSP blocks fetches that aren't in connect-src."
        );
    }

    /// Tripwire: the main window must show on .app launch. The tray
    /// is for the menu only; the .app icon click should open the
    /// window like any standard macOS app. We tried "visible": false
    /// (pure menubar) once and immediately got user reports of "I
    /// open the .app and nothing happens" — the window was hidden
    /// and the only path to surface it was the tray's "Show hopssh".
    /// Don't go back to that.
    #[test]
    fn tauri_conf_main_window_visible_on_launch() {
        let conf_path =
            Path::new(env!("CARGO_MANIFEST_DIR")).join("tauri.conf.json");
        let conf = std::fs::read_to_string(&conf_path)
            .expect("tauri.conf.json must exist next to Cargo.toml");
        assert!(
            conf.contains("\"visible\": true"),
            "main window must have \"visible\": true — clicking the .app icon should open the window"
        );
        assert!(
            !conf.contains("\"visible\": false"),
            "main window must NOT have \"visible\": false"
        );
    }

    /// Tripwire (Phase N): the "Hide from Dock" toggle wires the
    /// activation policy at three lifecycle points. Missing any one
    /// leaves the user in a broken state (e.g. window-close with
    /// the toggle on but no policy demotion = Dock icon stays
    /// despite the user opting out). All three pairs of sentinel
    /// tokens must coexist in the source. Slicing the file by
    /// CloseRequested / Reopen blocks is brittle because of nested
    /// `}` braces in inner if-blocks; whole-file token checks are
    /// good enough — the deletion regressions we're guarding against
    /// would remove the tokens entirely.
    #[test]
    fn hide_from_dock_wired_at_all_lifecycle_points() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        // Toggle plumbing
        assert!(
            src.contains("set_hide_from_dock") && src.contains("get_hide_from_dock"),
            "Tauri commands set_hide_from_dock + get_hide_from_dock must be defined"
        );
        // Persistence
        assert!(
            src.contains("desktop-prefs.json") && src.contains("read_desktop_prefs"),
            "Persisted-pref helpers must reference the prefs file + reader"
        );
        // Both policy variants must be referenced — Accessory for
        // demotion, Regular for restoration.
        assert!(
            src.contains("ActivationPolicy::Accessory"),
            "Demotion to .Accessory missing — close-handler can't hide Dock icon"
        );
        assert!(
            src.contains("ActivationPolicy::Regular"),
            "Restoration to .Regular missing — Reopen + show_main_window can't bring Dock icon back"
        );
        // Lifecycle hooks: setup, CloseRequested, Reopen, show_main_window
        // must all exist (verified in earlier tripwires) and
        // hide_from_dock must be referenced from at least 3 distinct
        // call sites.
        let occurrences = src.matches("hide_from_dock").count();
        assert!(
            occurrences >= 3,
            "hide_from_dock should be referenced at >=3 call sites (setup + close + reopen). Got: {occurrences}"
        );
    }

    /// Tripwire (Phase O slice O1 — jargon rewrite): default-rendered
    /// copy in the user-facing Svelte components must NOT contain
    /// the protocol vocabulary that fails NN/g's jargon test for
    /// novice VPN users. Each token below has an industry-tested
    /// plain-English replacement landed in slice O1; a regression
    /// adding any of them back to default copy fails this test.
    /// Power-user details may live behind a `<details>` disclosure
    /// or in About; this scan is intentionally limited to the four
    /// top-of-mind components.
    #[test]
    fn user_facing_copy_has_no_protocol_jargon() {
        // Resolve frontend dir relative to src-tauri/Cargo.toml.
        let manifest = Path::new(env!("CARGO_MANIFEST_DIR"));
        let lib_dir = manifest.join("..").join("src").join("lib");
        let app_svelte = manifest.join("..").join("src").join("App.svelte");

        let targets = [
            ("App.svelte", app_svelte),
            ("Connected.svelte", lib_dir.join("Connected.svelte")),
            ("Onboarding.svelte", lib_dir.join("Onboarding.svelte")),
            ("Settings.svelte", lib_dir.join("Settings.svelte")),
            ("SystemModeCTA.svelte", lib_dir.join("SystemModeCTA.svelte")),
        ];

        // Forbidden tokens. Substring-match — these strings should
        // not appear in default-rendered copy. Comments inside
        // <script> blocks DO contain them legitimately (architecture
        // notes), so we strip script blocks before scanning.
        let forbidden = [
            "P2P",        // status badge — replaced by plain "connected"
            "TUN:",       // hero stat label — replaced by "Networking:"
            "data-plane", // banner title — humanized in BannerStrip
        ];
        // Forbidden ONLY in onboarding (the flow that exposes them):
        let onboarding_forbidden = ["Control plane"];

        for (name, path) in targets.iter() {
            let src = match std::fs::read_to_string(path) {
                Ok(s) => s,
                Err(_) => panic!("could not read {}", path.display()),
            };
            // Strip <script>…</script> so architectural comments
            // (which legitimately reference protocol terms) don't
            // trigger the scan. Cheap split: take everything after
            // the first </script> tag.
            let body = src.split("</script>").nth(1).unwrap_or(&src);
            for needle in &forbidden {
                assert!(
                    !body.contains(needle),
                    "{}: forbidden jargon token {:?} appears in default-rendered copy. Use a plain-English replacement (see Phase O slice O1 plan).",
                    name, needle
                );
            }
            if *name == "Onboarding.svelte" {
                for needle in &onboarding_forbidden {
                    assert!(
                        !body.contains(needle),
                        "{}: forbidden jargon token {:?} in default-rendered copy. Onboarding's `Control plane` legend was renamed to `Where do you want to connect?`.",
                        name, needle
                    );
                }
            }
        }
    }

    /// Tripwire: macOS RunEvent::Reopen must show + focus the main
    /// window. Without this, clicking the Dock icon (while the app is
    /// running with the window hidden via Cmd-W) is a no-op. The
    /// red-dot Cmd-W close handler hides the window (see
    /// on_window_event), so Reopen is the only path back.
    #[test]
    fn run_event_reopen_shows_main_window() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        // Locate the .run(...) closure body.
        let block = src
            .split(".run(move |app_handle, event| match event")
            .nth(1)
            .expect("app.run handler must exist");
        // Truncate at the closing `});` of the run callback.
        let block = block
            .split("\n        });")
            .next()
            .unwrap_or(block);
        assert!(
            block.contains("RunEvent::Reopen"),
            "app.run must handle RunEvent::Reopen so Dock-icon clicks show the window. Block: {block}"
        );
        assert!(
            block.contains("get_webview_window(\"main\")"),
            "Reopen handler must look up the \"main\" window. Block: {block}"
        );
        assert!(
            block.contains(".show()"),
            "Reopen handler must call .show() on the main window. Block: {block}"
        );
    }

    /// Tripwire: the tray menu must include a "Check for updates"
    /// item. Users repeatedly missed the corresponding Settings tab,
    /// so making it tray-discoverable is the user-driven design.
    #[test]
    fn tray_menu_has_check_for_updates() {
        let src = std::fs::read_to_string(file!())
            .expect("must be able to read lib.rs source for self-scan");
        assert!(
            src.contains("\"checkUpdate\""),
            "tray menu must include a checkUpdate menu item"
        );
        assert!(
            src.contains("Check for updates"),
            "tray menu must include a 'Check for updates' label"
        );
        assert!(
            src.contains("emit(\"tray-action\", \"checkUpdate\")"),
            "checkUpdate menu click must emit a tray-action event so the JS layer can route to Settings"
        );
    }

    /// Tripwire (locked in after the duplicate-tray-icon incident):
    /// tauri.conf.json MUST NOT declare a `trayIcon` block. Tauri 2
    /// does not dedupe trays by id — declarative + programmatic both
    /// register separate NSStatusItems on macOS, producing two icons
    /// in the menubar. The programmatic TrayIconBuilder in lib.rs::run
    /// is the single source of truth (loads the correct template,
    /// wires menu + click handlers, supports dynamic state swap).
    ///
    /// Reference: https://github.com/tauri-apps/tauri/issues/8982
    #[test]
    fn tauri_conf_has_no_declarative_tray_icon() {
        let conf_path =
            Path::new(env!("CARGO_MANIFEST_DIR")).join("tauri.conf.json");
        let conf = std::fs::read_to_string(&conf_path)
            .expect("tauri.conf.json must exist next to Cargo.toml");
        assert!(
            !conf.contains("\"trayIcon\""),
            "tauri.conf.json must NOT declare a trayIcon block — it produces \
             a duplicate NSStatusItem on macOS alongside the programmatic \
             TrayIconBuilder. See https://github.com/tauri-apps/tauri/issues/8982"
        );
    }
}
