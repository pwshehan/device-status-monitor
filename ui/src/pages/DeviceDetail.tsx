import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import type { PauseRequest } from '../api/types'
import { DeviceFormModal } from '../components/DeviceFormModal'
import { LatencyChart } from '../components/LatencyChart'
import { PauseDialog } from '../components/PauseDialog'
import { StatusBadge } from '../components/StatusBadge'
import { UptimeStrip } from '../components/UptimeStrip'
import { Button, Confirm, Empty, ErrorNote, Spinner, Tile } from '../components/ui'
import {
  useCheckDevice,
  useDeleteDevice,
  useDeviceUptime,
  useDevice,
  useGroups,
  useHeartbeats,
  useIncidents,
  usePauseDevice,
  useSettings,
} from '../hooks/queries'
import {
  addr,
  badgeOf,
  duration,
  inheritedFrom,
  latency,
  localTime,
  since,
  uptimePct,
  type SourceKey,
} from '../lib/format'

const WINDOWS = [
  { label: '1 h', hours: 1 },
  { label: '6 h', hours: 6 },
  { label: '24 h', hours: 24 },
  { label: '7 d', hours: 168 },
]

const inheritedRows: { key: SourceKey; label: string }[] = [
  { key: 'interval', label: 'Check interval' },
  { key: 'timeout', label: 'Timeout' },
  { key: 'failure_threshold', label: 'Failures before DOWN' },
  { key: 'recovery_threshold', label: 'Successes before UP' },
]

export function DeviceDetail() {
  const params = useParams<{ id: string }>()
  const id = Number(params.id)
  const navigate = useNavigate()

  const [hours, setHours] = useState(24)
  const [editing, setEditing] = useState(false)
  const [pausing, setPausing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const detail = useDevice(id)
  const groups = useGroups()
  const settings = useSettings()
  const samples = useHeartbeats(id, hours)
  const uptime = useDeviceUptime(id, 90)
  const incidents = useIncidents(id)

  const check = useCheckDevice()
  const pause = usePauseDevice()
  const remove = useDeleteDevice()

  if (detail.isPending) return <Spinner label="Loading device" />
  if (detail.error !== null) return <ErrorNote error={detail.error} />
  if (detail.data === undefined) return null

  const { device, open_incident: open } = detail.data
  const today = uptime.data?.at(-1)

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h1 className="truncate text-xl font-semibold">{device.name}</h1>
            <StatusBadge badge={badgeOf(device)} />
          </div>
          <p className="mt-0.5 text-sm text-slate-500 dark:text-slate-400">
            <span className="font-mono">{addr(device)}</span>
            {device.group_id !== null && (
              <>
                {' · '}
                <Link to={`/groups/${device.group_id}`} className="hover:underline">
                  {device.group_name}
                </Link>
              </>
            )}
            {device.tags.length > 0 && ` · ${device.tags.join(', ')}`}
          </p>
        </div>

        <div className="ml-auto flex flex-wrap gap-2">
          <Button onClick={() => check.mutate(id)} disabled={check.isPending}>
            {check.isPending ? 'Checking…' : 'Check now'}
          </Button>
          <Button onClick={() => setPausing(true)}>
            {device.effective.paused ? 'Resume' : 'Pause'}
          </Button>
          <Button onClick={() => setEditing(true)}>Edit</Button>
          <Button tone="danger" onClick={() => setDeleting(true)}>
            Delete
          </Button>
        </div>
      </div>

      {check.data !== undefined && (
        <p
          className={`rounded-md px-3 py-2 text-sm ${
            check.data.ok
              ? 'bg-up-100 text-up-600 dark:bg-up-600/15 dark:text-up-100'
              : 'bg-down-100 text-down-600 dark:bg-down-600/15 dark:text-down-100'
          }`}
        >
          {check.data.ok
            ? `Answered in ${check.data.latency_ms} ms at ${localTime(check.data.checked_at)}.`
            : `No answer — ${check.data.error === '' ? check.data.class : check.data.error}`}
          <span className="ml-2 text-xs opacity-70">
            (a manual check does not affect stored history)
          </span>
        </p>
      )}
      <ErrorNote error={check.error} />

      {open !== null && (
        <div className="rounded-lg bg-down-100 px-4 py-3 text-sm text-down-600 dark:bg-down-600/15 dark:text-down-100">
          <strong>Down for {duration(open.duration_sec)}</strong> — since{' '}
          {localTime(open.started_at)}. {open.cause}
          {!open.alert_sent && (
            <span className="ml-1 opacity-80">
              No alert was sent: this device had not been seen working before it failed.
            </span>
          )}
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Tile label="Last check" value={since(device.last_check_at)} hint={device.last_check_at ?? ''} />
        <Tile label="Last latency" value={latency(device.last_latency_ms)} />
        <Tile
          label="Today"
          value={uptimePct(today?.uptime_pct ?? null)}
          tone={today !== undefined && today.uptime_pct < 99.9 ? 'warn' : 'up'}
          hint={today === undefined ? '' : `${today.checks_up} of ${today.checks_total} checks`}
        />
        <Tile
          label="Status since"
          value={since(device.last_status_change_at)}
          hint={device.last_status_change_at ?? ''}
        />
      </div>

      <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-semibold">Latency</h2>
          <div className="flex rounded-md bg-slate-200 p-0.5 dark:bg-slate-800">
            {WINDOWS.map((w) => (
              <button
                key={w.hours}
                type="button"
                onClick={() => setHours(w.hours)}
                className={`rounded px-2 py-0.5 text-xs ${
                  hours === w.hours
                    ? 'bg-white shadow-sm dark:bg-slate-700'
                    : 'text-slate-600 dark:text-slate-300'
                }`}
              >
                {w.label}
              </button>
            ))}
          </div>
        </div>
        {samples.isPending ? (
          <Spinner label="Loading series" />
        ) : (samples.data ?? []).length === 0 ? (
          <Empty>No checks recorded in this window.</Empty>
        ) : (
          <LatencyChart samples={samples.data ?? []} incidents={incidents.data ?? []} />
        )}
      </section>

      <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="mb-3 text-sm font-semibold">Availability, last 90 days</h2>
        <UptimeStrip
          days={(uptime.data ?? []).map((d) => ({
            day: d.day,
            uptime_pct: d.uptime_pct,
            downtime_sec: d.downtime_sec,
            source: d.source,
          }))}
        />
        <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
          Green ≥ 99.9%, amber ≥ 95%, red below. Hatched days have no data.
        </p>
      </section>

      <div className="grid gap-4 lg:grid-cols-2">
        <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
          <h2 className="mb-3 text-sm font-semibold">Effective settings</h2>
          <dl className="space-y-1.5 text-sm">
            {inheritedRows.map((row) => (
              <div key={row.key} className="flex justify-between gap-4">
                <dt className="text-slate-500 dark:text-slate-400">{row.label}</dt>
                <dd className="tabular-nums">{inheritedFrom(device.effective, row.key)}</dd>
              </div>
            ))}
            <div className="flex justify-between gap-4">
              <dt className="text-slate-500 dark:text-slate-400">Alerts</dt>
              <dd className="text-right">
                {device.effective.notify ? 'on' : 'off'}
                {device.effective.recipients.length > 0 && (
                  <div className="text-xs text-slate-500 dark:text-slate-400">
                    {device.effective.recipients.join(', ')} (
                    {device.effective.source.recipients ?? 'global'})
                  </div>
                )}
              </dd>
            </div>
          </dl>
        </section>

        <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
          <h2 className="mb-3 text-sm font-semibold">Incidents</h2>
          {(incidents.data ?? []).length === 0 ? (
            <Empty>No outages recorded.</Empty>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400">
                  <th className="py-1 font-medium">Started</th>
                  <th className="py-1 font-medium">Duration</th>
                  <th className="py-1 font-medium">Cause</th>
                </tr>
              </thead>
              <tbody>
                {(incidents.data ?? []).map((inc) => (
                  <tr key={inc.id} className="border-t border-slate-100 dark:border-slate-800">
                    <td className="py-1.5 whitespace-nowrap">{localTime(inc.started_at)}</td>
                    <td className="py-1.5 tabular-nums">
                      {inc.ongoing ? (
                        <span className="text-down-600 dark:text-down-500">ongoing</span>
                      ) : (
                        duration(inc.duration_sec)
                      )}
                    </td>
                    <td className="max-w-40 truncate py-1.5 text-xs" title={inc.cause}>
                      {inc.cause}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>

      {editing && (
        <DeviceFormModal
          device={device}
          groups={groups.data ?? []}
          defaults={settings.data?.defaults}
          onClose={() => setEditing(false)}
        />
      )}

      {pausing && (
        <PauseDialog
          what={device.name}
          pausedUntil={device.paused_until}
          busy={pause.isPending}
          error={pause.error}
          onClose={() => setPausing(false)}
          onSubmit={(body: PauseRequest) =>
            pause.mutate({ id, body }, { onSuccess: () => setPausing(false) })
          }
        />
      )}

      {deleting && (
        <Confirm
          title={`Delete ${device.name}?`}
          message="This removes the device and its entire history. It cannot be undone."
          confirmLabel="Delete device"
          busy={remove.isPending}
          onCancel={() => setDeleting(false)}
          onConfirm={() => remove.mutate(id, { onSuccess: () => void navigate('/') })}
        />
      )}
    </div>
  )
}
