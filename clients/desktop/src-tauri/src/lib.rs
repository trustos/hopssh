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
///   3. **chown back to the console user**. CRITICAL: inside
///      `osascript ... with administrator privileges`, the shell runs
///      as root, so `$USER` would be "root" (wrong!). osascript sets
///      `$SUDO_USER` to the console user when escalating; that's the
///      correct env var. Without this fix the migrated configs are
///      root-owned 0600 and the user-level bundled child can't read
///      its own enrollments.json — visible symptom is "hopssh isn't
///      running" after toggling Run-in-the-background OFF.
///   4. Remove the stale mirror token + port files so the next launch's
///      try_attach_to_system_agent probe correctly falls through to
///      bundled spawn (instead of trying to TCP-connect to a dead
///      system-agent port).
fn build_revert_script(user_config: &std::path::Path) -> String {
    let user_config_str = user_config.display();
    format!(
        r#"do shell script "/usr/local/bin/hop-agent uninstall && mkdir -p '{0}' && (cd /etc/hop-agent 2>/dev/null && (cp -R . '{0}/' && rm -rf /etc/hop-agent/* /etc/hop-agent/.[!.]* 2>/dev/null) || true) && rm -rf /etc/hop-agent && chown -R \"$SUDO_USER\":staff '{0}' && rm -f '{0}/system-local-api-token' '{0}/system-local-api-port'" with administrator privileges"#,
        user_config_str
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

        let script = build_revert_script(&user_config);
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
            quit_app,
            convert_to_system_service,
            revert_to_bundled
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
            let _tray = TrayIconBuilder::with_id("main")
                .icon(boot_icon)
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
        let s = build_revert_script(user_config);
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

    /// Tripwire (locked in after the v0.10.55 broken-revert incident):
    /// the chown step inside the privileged shell MUST use $SUDO_USER,
    /// not $USER. Inside `osascript ... with administrator privileges`,
    /// the shell runs as root and `$USER` == "root" — chowning files
    /// to root:staff leaves the bundled child unable to read its own
    /// configs. $SUDO_USER is set by osascript to the console user
    /// who approved the admin prompt.
    #[test]
    fn revert_command_chowns_back_to_console_user() {
        let user_config = Path::new("/Users/alice/Library/Application Support/hopssh");
        let s = build_revert_script(user_config);
        assert!(
            s.contains("$SUDO_USER"),
            "chown must use $SUDO_USER (not $USER, which is root inside privileged shells): {s}"
        );
        assert!(
            !s.contains("'$USER'"),
            "chown must NOT use $USER — that's 'root' inside privileged shells: {s}"
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
        let s = build_revert_script(user_config);
        assert!(
            s.contains("system-local-api-token"),
            "must rm the stale mirror token: {s}"
        );
        assert!(
            s.contains("system-local-api-port"),
            "must rm the stale mirror port file: {s}"
        );
    }
}
