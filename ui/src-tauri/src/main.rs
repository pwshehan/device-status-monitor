// Windows: no console window behind the app in release builds. Kept in debug
// so `tauri dev` can still print panics and logs.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    local_monitor_gui_lib::run()
}
