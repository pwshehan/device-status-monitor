import { useQueries } from '@tanstack/react-query'
import { useMemo } from 'react'
import { Link, useParams } from 'react-router-dom'

import { api } from '../api/client'
import type { Device } from '../api/types'

import { StatusBadge } from '../components/StatusBadge'
import { UptimeStrip } from '../components/UptimeStrip'
import { Empty, ErrorNote, Spinner, Tile } from '../components/ui'
import { keys, useDevices, useGroup, useGroupUptime } from '../hooks/queries'
import { addr, badgeOf, compareForTriage, duration, latency, localTime, since, uptimePct } from '../lib/format'

export function GroupDetail() {
  const params = useParams<{ id: string }>()
  const id = Number(params.id)

  const group = useGroup(id)
  const members = useDevices({ groupId: id })
  const uptime = useGroupUptime(id, 90)

  const sorted = useMemo(() => [...(members.data ?? [])].sort(compareForTriage), [members.data])

  if (group.isPending) return <Spinner label="Loading group" />
  if (group.error !== null) return <ErrorNote error={group.error} />
  if (group.data === undefined) return null

  const stats = group.data.stats
  const today = uptime.data?.at(-1)

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start gap-3">
        <span
          aria-hidden="true"
          className="mt-1.5 size-4 shrink-0 rounded-full"
          style={{ backgroundColor: group.data.color === '' ? '#94a3b8' : group.data.color }}
        />
        <div className="min-w-0">
          <h1 className="text-xl font-semibold">{group.data.name}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">
            {group.data.description === '' ? 'No description' : group.data.description}
            {group.data.paused && ' · in a maintenance window'}
          </p>
        </div>
        <Link
          to="/groups"
          className="ml-auto text-sm text-accent-600 hover:underline dark:text-accent-500"
        >
          Manage groups
        </Link>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Tile label="Devices" value={stats?.members ?? sorted.length} />
        <Tile label="Up" value={stats?.up ?? '—'} tone="up" />
        <Tile
          label="Down"
          value={stats?.down ?? '—'}
          tone={(stats?.down ?? 0) > 0 ? 'down' : 'default'}
        />
        <Tile
          label="Today"
          value={uptimePct(today?.uptime_pct ?? stats?.uptime_today_pct ?? null)}
          hint="Share of all member checks that succeeded today"
        />
      </div>

      <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="mb-3 text-sm font-semibold">Site availability, last 90 days</h2>
        {uptime.isPending ? (
          <Spinner label="Loading history" />
        ) : (uptime.data ?? []).length === 0 ? (
          <Empty>No history yet for this group's members.</Empty>
        ) : (
          <>
            <UptimeStrip
              days={(uptime.data ?? []).map((d) => ({
                day: d.day,
                uptime_pct: d.uptime_pct,
                downtime_sec: d.downtime_sec,
                worst_name: d.worst_device_name,
                worst_pct: d.worst_device_pct,
                source: d.source,
              }))}
            />
            <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
              Coloured by aggregate availability across every member. A site of twenty devices with
              one dead all day still reads 95% — hover a day to see which member had the worst of
              it.
            </p>
          </>
        )}
      </section>

      <section className="overflow-hidden rounded-lg bg-white shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
        <h2 className="border-b border-slate-200 px-4 py-2 text-sm font-semibold dark:border-slate-700">
          Members
        </h2>
        {sorted.length === 0 ? (
          <div className="p-4">
            <Empty>No devices in this group. Move some in from the dashboard.</Empty>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:text-slate-400">
                <th className="px-4 py-2 font-medium">Device</th>
                <th className="px-2 py-2 font-medium">Address</th>
                <th className="px-2 py-2 font-medium">Status</th>
                <th className="px-2 py-2 font-medium">Last check</th>
                <th className="px-2 py-2 font-medium">Latency</th>
                <th className="px-2 py-2 font-medium">Interval</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((device) => (
                <tr key={device.id} className="border-b border-slate-100 last:border-0 dark:border-slate-800">
                  <td className="px-4 py-2">
                    <Link
                      to={`/devices/${device.id}`}
                      className="font-medium hover:text-accent-600 hover:underline"
                    >
                      {device.name}
                    </Link>
                  </td>
                  <td className="px-2 py-2 font-mono text-xs text-slate-600 dark:text-slate-300">
                    {addr(device)}
                  </td>
                  <td className="px-2 py-2">
                    <StatusBadge badge={badgeOf(device)} />
                  </td>
                  <td className="px-2 py-2 text-slate-600 dark:text-slate-300">
                    {since(device.last_check_at)}
                  </td>
                  <td className="px-2 py-2 tabular-nums text-slate-600 dark:text-slate-300">
                    {latency(device.last_latency_ms)}
                  </td>
                  <td className="px-2 py-2 text-xs text-slate-500 dark:text-slate-400">
                    {device.effective.check_interval_sec}s
                    {device.check_interval_sec === null ? ' (inherited)' : ' (own)'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <GroupIncidents devices={sorted} />
    </div>
  )
}

/**
 * The site's combined incident log.
 *
 * Merged from each member's log rather than served by a group endpoint:
 * incidents belong to devices, and a group-level query would have to decide
 * what to do about a device that has since moved. Merging over the current
 * members keeps "the log for this site" meaning exactly that.
 */
function GroupIncidents({ devices }: { devices: Device[] }) {
  const names = useMemo(
    () => new Map(devices.map((d) => [d.id, d.name])),
    [devices],
  )

  // useQueries, because the number of members is not known at compile time and
  // hooks cannot be called in a loop.
  const results = useQueries({
    queries: devices.map((device) => ({
      queryKey: keys.incidents(device.id),
      queryFn: () => api.deviceIncidents(device.id, 10),
    })),
  })

  // Not memoised: `results` is a fresh array on every render, so any
  // dependency list would be a lie. Sorting a few dozen incidents costs less
  // than the bookkeeping to avoid it.
  const merged = results
    .flatMap((r) => r.data ?? [])
    .sort((x, y) => Date.parse(y.started_at) - Date.parse(x.started_at))
    .slice(0, 25)

  if (devices.length === 0) return null

  return (
    <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
      <h2 className="mb-3 text-sm font-semibold">Recent incidents on this site</h2>
      {merged.length === 0 ? (
        <Empty>No outages recorded.</Empty>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400">
              <th className="py-1 font-medium">Device</th>
              <th className="py-1 font-medium">Started</th>
              <th className="py-1 font-medium">Duration</th>
              <th className="py-1 font-medium">Cause</th>
            </tr>
          </thead>
          <tbody>
            {merged.map((inc) => (
              <tr key={inc.id} className="border-t border-slate-100 dark:border-slate-800">
                <td className="py-1.5">
                  <Link to={`/devices/${inc.device_id}`} className="hover:underline">
                    {names.get(inc.device_id) ?? `Device ${inc.device_id}`}
                  </Link>
                </td>
                <td className="py-1.5 whitespace-nowrap">{localTime(inc.started_at)}</td>
                <td className="py-1.5 tabular-nums">
                  {inc.ongoing ? (
                    <span className="text-down-600 dark:text-down-500">ongoing</span>
                  ) : (
                    duration(inc.duration_sec)
                  )}
                </td>
                <td className="max-w-48 truncate py-1.5 text-xs" title={inc.cause}>
                  {inc.cause}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}
