import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import type { PluginOption, ProxyOptions } from 'vite'
import { defineConfig } from 'vitest/config'

/**
 * The dev server proxies /api to the Go service and injects the bearer token
 * for us.
 *
 * The token lives in a file only the local machine can read, and a browser
 * cannot read files — so without this, developing the UI would mean pasting a
 * token into a form after every `rotate-token`. Proxying also makes every
 * request same-origin, so the dev loop does not depend on CORS at all.
 *
 * The token is read per request rather than once at startup, so rotating it
 * does not require restarting Vite.
 */
const TOKEN_FILE = resolve(import.meta.dirname, '..', '.dev-data', 'api.token')

function readDevToken(): string | null {
  try {
    const token = readFileSync(TOKEN_FILE, 'utf8').trim()
    return token === '' ? null : token
  } catch {
    // The service has not run yet, or is running against ProgramData rather
    // than ./.dev-data. Either way the UI's own token handling takes over.
    return null
  }
}

/**
 * Where the dev server forwards /api.
 *
 * Overridable because the installed service owns 49215 on a machine that has
 * one, so developing against a second instance means running it on another
 * port: `monitor-service -dev -api-addr 127.0.0.1:49216` with
 * `MONITOR_API=http://127.0.0.1:49216 npm run dev`.
 */
const API_TARGET = process.env.MONITOR_API ?? 'http://127.0.0.1:49215'

const apiProxy: ProxyOptions = {
  target: API_TARGET,
  changeOrigin: true,
  // Server-Sent Events must not be buffered or the dashboard goes quiet.
  ws: false,
  configure(proxy) {
    proxy.on('proxyReq', (proxyReq) => {
      if (proxyReq.getHeader('authorization') === undefined) {
        const token = readDevToken()
        if (token !== null) proxyReq.setHeader('authorization', `Bearer ${token}`)
      }
    })
  },
}

/**
 * Warns once at startup if the token file is missing, since the symptom
 * otherwise is a wall of 401s with no explanation.
 */
function devTokenNotice(): PluginOption {
  return {
    name: 'dev-token-notice',
    apply: 'serve',
    configureServer(server) {
      if (readDevToken() === null) {
        server.config.logger.warn(
          `\n  No API token at ${TOKEN_FILE}\n` +
            '  Start the service first:  ./dist/monitor-service.exe -dev -seed\n',
        )
      }
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), devTokenNotice()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: { '/api': apiProxy },
    // Nothing under src-tauri is frontend source, and target/ is where cargo
    // rewrites a multi-megabyte DLL every time the shell rebuilds. Watching it
    // does not just waste effort: `tauri dev` rebuilds while the dev server is
    // running, and the watcher dies with EBUSY on the half-written file, taking
    // the dev server down with it.
    watch: { ignored: ['**/src-tauri/**'] },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
  },
})
