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
        .manage(app_state)
        .invoke_handler(tauri::generate_handler![
            local_api_endpoint,
            show_main_window,
            set_tray_tooltip
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
