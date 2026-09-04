/// <reference types="vite/client" />

/**
 * Only one environment variable, and it exists for the Tauri shell: the
 * browser dev loop leaves it unset and talks to the Vite proxy at /api.
 */
interface ImportMetaEnv {
  readonly VITE_API_BASE?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
