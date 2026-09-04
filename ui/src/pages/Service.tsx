import { useState } from 'react'

import { apiToken, setApiToken } from '../api/client'
import { useStream } from '../app/StreamContext'
import { Button, ErrorNote, Field, Spinner, Tile, inputClass } from '../components/ui'
import { useHealth } from '../hooks/queries'
import { bytes, duration } from '../lib/format'

/**
 * Service status: what the engine is doing, where its files are, and the token
 * this window is using.
 *
 * The scheduler-lag figure is the one number here worth watching. It is the
 * most overdue device's probe, and a value that climbs and stays up means
 * probes are queueing behind the concurrency limit — something no count of
 * devices or heartbeats would reveal.
 */
export function Service() {
  const health = useHealth()
  const stream = useStream()
  const [token, setToken] = useState(apiToken() ?? '')
  const [tokenSaved, setTokenSaved] = useState(false)

  const h = health.data
  const lagTone = h === undefined ? 'default' : h.scheduler_lag_ms > 30_000 ? 'down' : h.scheduler_lag_ms > 5_000 ? 'warn' : 'up'

  return (
    <div className="max-w-3xl space-y-6">
      <div>
        <h1 className="text-xl font-semibold">Service</h1>
        <p className="text-sm text-slate-500 dark:text-slate-400">
          The engine runs as a Windows service and keeps monitoring whether or not this window is
          open.
        </p>
      </div>

      <ErrorNote error={health.error} />

      {health.isPending ? (
        <Spinner label="Asking the service" />
      ) : h === undefined ? null : (
        <>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Tile label="Version" value={h.version} />
            <Tile label="Running for" value={duration(h.uptime_sec)} />
            <Tile
              label="Monitoring"
              value={`${h.monitored}/${h.devices}`}
              hint="Devices with a live probe loop, out of every device configured"
            />
            <Tile
              label="Scheduler lag"
              value={`${h.scheduler_lag_ms} ms`}
              tone={lagTone}
              hint="How overdue the most overdue probe is"
            />
            <Tile label="Database" value={bytes(h.db_size_bytes)} hint={h.data_dir} />
            <Tile
              label="Queued alerts"
              value={h.pending_alerts}
              tone={h.pending_alerts > 0 ? 'warn' : 'default'}
              hint="Mail waiting to be delivered or retried"
            />
            <Tile
              label="Open incidents"
              value={h.open_incidents}
              tone={h.open_incidents > 0 ? 'down' : 'default'}
            />
            <Tile
              label="Live updates"
              value={stream.state === 'open' ? 'connected' : stream.state}
              tone={stream.state === 'open' ? 'up' : 'warn'}
              hint={`${h.event_clients} client(s), ${h.events_dropped} event(s) dropped`}
            />
          </div>

          <section className="space-y-2 rounded-lg bg-white p-4 text-sm shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
            <h2 className="text-sm font-semibold">Files</h2>
            <dl className="space-y-1">
              <div className="flex flex-wrap justify-between gap-2">
                <dt className="text-slate-500 dark:text-slate-400">Data</dt>
                <dd className="font-mono text-xs">{h.data_dir}</dd>
              </div>
              <div className="flex flex-wrap justify-between gap-2">
                <dt className="text-slate-500 dark:text-slate-400">Logs</dt>
                <dd className="font-mono text-xs">{h.log_dir}</dd>
              </div>
              <div className="flex flex-wrap justify-between gap-2">
                <dt className="text-slate-500 dark:text-slate-400">Schema</dt>
                <dd className="tabular-nums">version {h.schema_version}</dd>
              </div>
            </dl>
          </section>
        </>
      )}

      <section className="space-y-3 rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="text-sm font-semibold">API token</h2>
        <p className="text-sm text-slate-500 dark:text-slate-400">
          Every request except the health check needs the service's token. The desktop app reads it
          from <code className="font-mono text-xs">api.token</code> in the data folder; a plain
          browser needs it pasted here, unless it is going through the dev proxy, which supplies it
          for you.
        </p>
        <Field
          label="Token"
          hint="Rotate it with: monitor-service rotate-token (then restart the service)."
        >
          {(id) => (
            <input
              id={id}
              className={`${inputClass} font-mono text-xs`}
              value={token}
              type="password"
              autoComplete="off"
              onChange={(e) => {
                setToken(e.target.value)
                setTokenSaved(false)
              }}
            />
          )}
        </Field>
        <div className="flex items-center gap-3">
          <Button
            onClick={() => {
              setApiToken(token)
              setTokenSaved(true)
              void health.refetch()
            }}
          >
            Use this token
          </Button>
          <Button
            tone="ghost"
            onClick={() => {
              setApiToken(null)
              setToken('')
              setTokenSaved(false)
            }}
          >
            Forget it
          </Button>
          {tokenSaved && <span className="text-sm text-up-600 dark:text-up-500">Stored.</span>}
        </div>
      </section>
    </div>
  )
}
