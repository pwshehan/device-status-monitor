import { useState } from 'react'

import { ApiError, ServiceDownError } from '../api/client'
import type { Health } from '../api/types'
import { Button } from './ui'

interface Props {
  error: unknown
  health: Health | undefined
}

/**
 * The hard-fail banner.
 *
 * Three failures look identical from a user's chair and need completely
 * different advice, which is why /api/health is unauthenticated — it is the
 * only way to tell them apart:
 *
 *  - nothing listening    → the service is not running: start it
 *  - 401                  → the service is fine, this window's token is stale
 *  - db_ok false          → the service is running but its database is not
 */
export function ServiceDownBanner({ error, health }: Props) {
  const [copied, setCopied] = useState(false)

  const dbBroken = health !== undefined && !health.db_ok
  if (error === null && !dbBroken) return null

  const unauthorized = error instanceof ApiError && error.isUnauthorized
  const down = error instanceof ServiceDownError || (error !== null && !unauthorized && !dbBroken)

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      /* clipboard blocked; the command is on screen to type */
    }
  }

  return (
    <div
      role="alert"
      className="border-b border-down-500/30 bg-down-100 px-4 py-3 text-sm text-down-600 dark:bg-down-600/15 dark:text-down-100"
    >
      {down && (
        <div className="space-y-2">
          <p className="font-semibold">The monitoring service is not running.</p>
          <p>
            Nothing is answering on the local API, so no devices are being checked and no alerts
            will be sent.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <code className="rounded bg-white/70 px-2 py-1 font-mono text-xs text-slate-800 dark:bg-slate-900/60 dark:text-slate-100">
              sc start LocalMonitorSvc
            </code>
            <Button size="sm" onClick={() => void copy('sc start LocalMonitorSvc')}>
              {copied ? 'Copied' : 'Copy'}
            </Button>
            <span className="text-xs opacity-80">
              In development, run <code className="font-mono">monitor-service -dev</code> instead.
            </span>
          </div>
        </div>
      )}

      {unauthorized && (
        <div className="space-y-1">
          <p className="font-semibold">This window is not authorised.</p>
          <p>
            The service is running, but the API token this window is using is not valid — most
            likely it was rotated. Restart the app, or paste the current token on the Service
            page.
          </p>
        </div>
      )}

      {dbBroken && !down && (
        <div className="space-y-1">
          <p className="font-semibold">The service cannot reach its database.</p>
          <p>
            Monitoring is running but nothing can be read or written.
            {health?.db_error !== undefined && health.db_error !== '' ? (
              <>
                {' '}
                The database said: <span className="font-mono text-xs">{health.db_error}</span>
              </>
            ) : null}
          </p>
        </div>
      )}
    </div>
  )
}
