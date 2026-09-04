//! The desktop shell around the dashboard.
//!
//! It does four things the webview cannot do for itself:
//!
//!   1. reads the service's API token off disk and hands it to the page,
//!   2. keeps a tray icon showing up/down counts,
//!   3. hides the window on close instead of quitting, so monitoring feels
//!      continuous even though the *service* was never affected either way,
//!   4. refuses to run twice.
//!
//! It deliberately does not monitor anything. Every probe, alert and byte of
//! history belongs to the service; closing or crashing this window changes
//! nothing about what is being watched.

use std::path::PathBuf;

use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Manager, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};
use tauri_plugin_autostart::MacosLauncher;

/// Where the service keeps its data, and therefore its token.
///
/// Mirrors `internal/appdir`: ProgramData in production, `./.dev-data` when
/// the engine is run with `-dev`. Both are checked because a developer's
/// machine usually has the dev one and nothing else.
fn token_paths() -> Vec<PathBuf> {
    let mut paths = Vec::new();

    if let Ok(program_data) = std::env::var("ProgramData") {
        paths.push(
            PathBuf::from(program_data)
                .join("LocalMonitor")
                .join("api.token"),
        );
    }

    // The dev database sits next to the repo, so walk up from the executable
    // and from the working directory: `tauri dev` runs from ui/src-tauri.
    if let Ok(cwd) = std::env::current_dir() {
        for depth in 0..4 {
            let mut candidate = cwd.clone();
            for _ in 0..depth {
                candidate = match candidate.parent() {
                    Some(parent) => parent.to_path_buf(),
                    None => break,
                };
            }
            paths.push(candidate.join(".dev-data").join("api.token"));
        }
    }
    paths
}

/// Reads the first token file that exists.
///
/// A missing token is not fatal: the page falls back to its own token field on
/// the Service screen, and /api/health answers without one — so the window can
/// still tell the user what is wrong rather than showing a blank screen.
fn read_token() -> Option<String> {
    for path in token_paths() {
        if let Ok(contents) = std::fs::read_to_string(&path) {
            let token = contents.trim().to_string();
            if !token.is_empty() {
                return Some(token);
            }
        }
    }
    None
}

/// The script that hands the token to the page before any of its code runs.
///
/// An initialization script rather than an event: the client reads
/// `window.__MONITOR_TOKEN__` at module scope, so it has to be there before
/// the bundle evaluates, and a race would show a spurious "not authorised"
/// banner on every launch.
fn init_script(token: Option<String>) -> String {
    let literal = match token {
        // serde_json does the escaping, so a token can never break out of the
        // string it is injected into.
        Some(token) => serde_json::to_string(&token).unwrap_or_else(|_| "null".into()),
        None => "null".into(),
    };
    format!(
        "window.__MONITOR_TOKEN__ = {literal} ?? undefined; window.__MONITOR_SHELL__ = 'tauri';"
    )
}

/// Updates the tray tooltip with the current tallies.
///
/// Called by the page whenever health changes, rather than polled from Rust:
/// the webview is already subscribed to the service's event stream, and a
/// second poller here would double the load on the API to learn what the
/// window already knows.
#[tauri::command]
fn set_tray_status(app: tauri::AppHandle, up: u32, down: u32, paused: u32) -> Result<(), String> {
    let tooltip = tooltip_text(up, down, paused);

    if let Some(tray) = app.tray_by_id("main") {
        tray.set_tooltip(Some(&tooltip))
            .map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// The tray tooltip: what is wrong first, since that is why anyone hovers.
fn tooltip_text(up: u32, down: u32, paused: u32) -> String {
    let summary = if down > 0 {
        format!("Local Monitor — {down} down, {up} up")
    } else {
        format!("Local Monitor — all {up} up")
    };
    if paused > 0 {
        format!("{summary} ({paused} paused)")
    } else {
        summary
    }
}

/// Brings the window back, from the tray or from a second launch.
fn show_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        // A second launch raises the existing window instead of opening
        // another one. Two windows would mean two event streams and two sets
        // of notifications for one machine.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            show_window(app);
        }))
        .plugin(tauri_plugin_autostart::init(
            MacosLauncher::LaunchAgent,
            // No arguments: starting with the session should behave exactly
            // like starting from the Start menu.
            None,
        ))
        .invoke_handler(tauri::generate_handler![set_tray_status])
        .setup(|app| {
            let handle = app.handle().clone();

            // Built here rather than declared in tauri.conf.json: a static
            // window cannot carry an initialization script, and the token has
            // to be in place before the bundle evaluates.
            let window = WebviewWindowBuilder::new(app, "main", WebviewUrl::default())
                .title("Local Monitor")
                .inner_size(1280.0, 800.0)
                .min_inner_size(900.0, 600.0)
                .center()
                .initialization_script(init_script(read_token()))
                .build()?;

            let show = MenuItem::with_id(app, "show", "Show window", true, None::<&str>)?;
            let hide = MenuItem::with_id(app, "hide", "Hide window", true, None::<&str>)?;
            let quit = MenuItem::with_id(
                app,
                "quit",
                "Quit (monitoring continues)",
                true,
                None::<&str>,
            )?;
            let menu = Menu::with_items(app, &[&show, &hide, &quit])?;

            TrayIconBuilder::with_id("main")
                .icon(app.default_window_icon().cloned().ok_or("no window icon")?)
                .tooltip("Local Monitor")
                .menu(&menu)
                // The menu is for the right button; a left click is the
                // shortcut people actually use.
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id.as_ref() {
                    "show" => show_window(app),
                    "hide" => {
                        if let Some(window) = app.get_webview_window("main") {
                            let _ = window.hide();
                        }
                    }
                    // Quitting the GUI never touches the service: the tray
                    // label says so, because "quit" on a monitoring app
                    // otherwise reads as "stop monitoring".
                    "quit" => app.exit(0),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        show_window(tray.app_handle());
                    }
                })
                .build(app)?;

            // Closing the window hides it. The service keeps probing either
            // way; hiding just means the next Show is instant and the tray
            // tooltip keeps updating.
            let hide_handle = handle.clone();
            window.on_window_event(move |event| {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    if let Some(window) = hide_handle.get_webview_window("main") {
                        let _ = window.hide();
                    }
                }
            });

            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running the Local Monitor shell");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tooltip_leads_with_what_is_wrong() {
        assert_eq!(tooltip_text(8, 0, 0), "Local Monitor — all 8 up");
        assert_eq!(tooltip_text(6, 2, 0), "Local Monitor — 2 down, 6 up");
        assert_eq!(
            tooltip_text(6, 2, 1),
            "Local Monitor — 2 down, 6 up (1 paused)"
        );
        // A paused device is not a healthy one, so it is worth saying even when
        // nothing is down.
        assert_eq!(tooltip_text(7, 0, 1), "Local Monitor — all 7 up (1 paused)");
    }

    #[test]
    fn init_script_escapes_the_token() {
        // The token is hex today, but it is read from a file that anything on
        // the machine could have written. Injecting it unescaped into a script
        // would be a way to run code in the window.
        let script = init_script(Some(r#"a"; alert(1); //"#.to_string()));

        // The quote inside the token comes out backslash-escaped, so it cannot
        // close the string literal it is injected into...
        assert!(
            script.contains(r#"a\"; alert(1); //"#),
            "token was not escaped: {script}"
        );
        // ...and therefore never reaches the page as executable code.
        assert!(
            !script.contains(r#"= "a"; alert("#),
            "the token closed its own string literal: {script}"
        );
        assert!(script.contains("__MONITOR_SHELL__ = 'tauri'"));
    }

    #[test]
    fn missing_token_is_not_fatal() {
        // The page falls back to its own token field, and health answers
        // without a token — so the window can still say what is wrong.
        let script = init_script(None);
        assert!(script.contains("null ?? undefined"), "unexpected: {script}");
    }

    #[test]
    fn token_is_looked_for_where_the_service_puts_it() {
        let paths = token_paths();
        assert!(
            paths.iter().any(|p| p.ends_with("LocalMonitor/api.token")
                || p.to_string_lossy().contains("LocalMonitor")),
            "the production path is missing: {paths:?}"
        );
        assert!(
            paths
                .iter()
                .any(|p| p.to_string_lossy().contains(".dev-data")),
            "the dev path is missing: {paths:?}"
        );
    }
}
