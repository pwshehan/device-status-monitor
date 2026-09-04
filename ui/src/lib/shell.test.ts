import { afterEach, describe, expect, it } from 'vitest'

import { isAutostartEnabled, isDesktopShell, setAutostart, setTrayStatus } from './shell'

afterEach(() => {
  delete window.__MONITOR_SHELL__
})

describe('shell detection', () => {
  it('is off in a plain browser', () => {
    expect(isDesktopShell()).toBe(false)
  })

  it('is on when the native shell has announced itself', () => {
    window.__MONITOR_SHELL__ = 'tauri'
    expect(isDesktopShell()).toBe(true)
  })
})

describe('shell calls outside the shell', () => {
  // The same bundle is served in a browser and loaded by the desktop app, so
  // every shell call has to be a no-op rather than a crash. A thrown error
  // here would take the dashboard down with it — in a browser, over a tray
  // tooltip.
  it('does nothing and reports nothing', async () => {
    await expect(setTrayStatus(3, 1, 0)).resolves.toBeUndefined()
    await expect(setAutostart(true)).resolves.toBeUndefined()
    await expect(isAutostartEnabled()).resolves.toBe(false)
  })

  it('swallows a failure from the shell rather than surfacing it', async () => {
    // Inside the shell but with no Tauri runtime to import: the dynamic import
    // rejects, and a tray tooltip is not worth an error banner.
    window.__MONITOR_SHELL__ = 'tauri'
    await expect(setTrayStatus(1, 0, 0)).resolves.toBeUndefined()
    await expect(isAutostartEnabled()).resolves.toBe(false)
  })
})
