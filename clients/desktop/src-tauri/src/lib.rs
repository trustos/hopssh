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

#[tauri::command]
fn show_main_window(app: AppHandle) -> Result<(), String> {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
        Ok(())
    } else {
        Err("main window not found".into())
    }
}

#[tauri::command]
fn set_tray_tooltip(app: AppHandle, tooltip: String) -> Result<(), String> {
    if let Some(tray) = app.tray_by_id("main") {
        tray.set_tooltip(Some(&tooltip)).map_err(|e| e.to_string())?;
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
        .manage(app_state)
        .invoke_handler(tauri::generate_handler![
            local_api_endpoint,
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
            quit_app
        ])
        .setup(move |app| {
            // Build menubar tray menu.
            let show = MenuItem::with_id(app, "show", "Show hopssh", true, None::<&str>)?;
            let add = MenuItem::with_id(app, "add", "Add a network…", true, None::<&str>)?;
            let sep1 = PredefinedMenuItem::separator(app)?;
            let sep2 = PredefinedMenuItem::separator(app)?;
            let about = MenuItem::with_id(app, "about", "About hopssh", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit", "Quit hopssh", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show, &add, &sep1, &about, &sep2, &quit])?;

            let _tray = TrayIconBuilder::with_id("main")
                .icon(app.default_window_icon().cloned().unwrap())
                .icon_as_template(true)
                .menu(&menu)
                .show_menu_on_left_click(false)
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
                        "quit" => app.exit(0),
                        _ => {}
                    }
                })
                .on_tray_icon_event(|tray, event| {
                    use tauri::tray::TrayIconEvent;
                    if let TrayIconEvent::Click { button, .. } = event {
                        if matches!(button, tauri::tray::MouseButton::Left) {
                            let app = tray.app_handle();
                            if let Some(w) = app.get_webview_window("main") {
                                let _ = w.show();
                                let _ = w.set_focus();
                            }
                        }
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
}
