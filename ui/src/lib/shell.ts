/**
 * The bits of the app that only exist inside the native shell.
 *
 * Every call is a no-op in a plain browser, so the same bundle runs in both
 * places and no screen has to be written twice. The Tauri modules are imported
 * dynamically, which keeps them out of the browser build's main chunk.
 */

declare global {
  interface Window {
    /** Set by the shell's initialization script. */
    __MONITOR_SHELL__?: 'tauri'
  }
}

export const isDesktopShell = (): boolean =>
  typeof window !== 'undefined' && window.__MONITOR_SHELL__ === 'tauri'

/**
 * Puts the current tallies in the tray tooltip.
 *
 * Pushed from here rather than polled by Rust: this window is already
 * subscribed to the service's event stream, so a second poller in the shell
 * would double the load on the API to learn something the page already knows.
 */
export async function setTrayStatus(up: number, down: number, paused: number): Promise<void> {
  if (!isDesktopShell()) return
  try {
    const { invoke } = await import('@tauri-apps/api/core')
    await invoke('set_tray_status', { up, down, paused })
  } catch {
    // A tooltip is not worth an error banner.
  }
}

export async function isAutostartEnabled(): Promise<boolean> {
  if (!isDesktopShell()) return false
  try {
    const { isEnabled } = await import('@tauri-apps/plugin-autostart')
    return await isEnabled()
  } catch {
    return false
  }
}

/**
 * Turns "start with Windows" on or off for the *window*.
 *
 * Worth being precise about in the UI: the service already starts with the
 * machine, whether or not anyone is logged in. This only decides whether the
 * dashboard opens with the session.
 */
export async function setAutostart(on: boolean): Promise<void> {
  if (!isDesktopShell()) return
  const { enable, disable } = await import('@tauri-apps/plugin-autostart')
  if (on) await enable()
  else await disable()
}
