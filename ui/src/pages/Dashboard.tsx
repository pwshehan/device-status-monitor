import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import type { Device, Group, PauseRequest, Status } from '../api/types'
import { DeviceFormModal } from '../components/DeviceFormModal'
import { DeviceTable } from '../components/DeviceTable'
import { GroupUptimeStrip } from '../components/GroupUptimeStrip'
import { PauseDialog } from '../components/PauseDialog'
import { Button, Confirm, Empty, ErrorNote, Spinner, Tile, inputClass } from '../components/ui'
import {
  useBulk,
  useCheckDevice,
  useDeleteDevice,
  useDevices,
  useGroups,
  usePauseDevice,
  usePauseGroup,
  useSettings,
  useSummary,
} from '../hooks/queries'
import { useStream } from '../app/StreamContext'
import { compareForTriage, uptimePct } from '../lib/format'

type Mode = 'grouped' | 'flat'

const MODE_KEY = 'monitor.dashboard-mode'
const COLLAPSED_KEY = 'monitor.collapsed-groups'

/** The client-side stand-in for the service's Ungrouped bucket. */
const ungroupedSection: Group = {
  id: null,
  ungrouped: true,
  name: 'Ungrouped',
  description: '',
  color: '',
  sort_order: 1 << 30,
  check_interval_sec: null,
  timeout_sec: null,
  failure_threshold: null,
  recovery_threshold: null,
  notify: true,
  recipients: null,
  paused_until: null,
  paused: false,
  created_at: null,
  updated_at: null,
}

function loadCollapsed(): Set<string> {
  try {
    const raw = window.localStorage.getItem(COLLAPSED_KEY)
    return new Set(raw === null ? [] : (JSON.parse(raw) as string[]))
  } catch {
    return new Set()
  }
}

export function Dashboard() {
  const [mode, setMode] = useState<Mode>(
    () => (window.localStorage.getItem(MODE_KEY) as Mode | null) ?? 'grouped',
  )
  const [collapsed, setCollapsed] = useState<Set<string>>(loadCollapsed)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [filterTag, setFilterTag] = useState('')
  const [filterStatus, setFilterStatus] = useState<Status | ''>('')
  const [search, setSearch] = useState('')

  const [editing, setEditing] = useState<Device | null>(null)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<Device | null>(null)
  const [pausingDevice, setPausingDevice] = useState<Device | null>(null)
  const [pausingGroup, setPausingGroup] = useState<Group | null>(null)
  const [bulkMoveTo, setBulkMoveTo] = useState<string>('')
  const [bulkDeleting, setBulkDeleting] = useState(false)

  const summary = useSummary()
  const groups = useGroups()
  const settings = useSettings()
  const devices = useDevices({
    ...(filterStatus === '' ? {} : { status: filterStatus }),
    ...(filterTag === '' ? {} : { tag: filterTag }),
  })

  const stream = useStream()
  const deleteDevice = useDeleteDevice()
  const pauseDevice = usePauseDevice()
  const pauseGroup = usePauseGroup()
  const checkDevice = useCheckDevice()
  const bulk = useBulk()

  const persistMode = (next: Mode) => {
    setMode(next)
    try {
      window.localStorage.setItem(MODE_KEY, next)
    } catch {
      /* ignore */
    }
  }

  const toggleCollapsed = (key: string) => {
    const next = new Set(collapsed)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setCollapsed(next)
    try {
      window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...next]))
    } catch {
      /* ignore */
    }
  }

  const rows = useMemo(() => {
    const all = devices.data ?? []
    const needle = search.trim().toLowerCase()
    if (needle === '') return all
    return all.filter(
      (d) =>
        d.name.toLowerCase().includes(needle) ||
        d.ip_address.includes(needle) ||
        d.tags.some((t) => t.toLowerCase().includes(needle)),
    )
  }, [devices.data, search])

  const allTags = useMemo(() => {
    const set = new Set<string>()
    for (const d of devices.data ?? []) for (const t of d.tags) set.add(t)
    return [...set].sort()
  }, [devices.data])

  /**
   * Group sections, ordered so anything broken floats to the top.
   *
   * A group with a member down sorts above the rest and gets a red header:
   * when something is wrong, the answer should be at the top of the page, not
   * wherever sort_order happens to put it.
   */
  const sections = useMemo(() => {
    const byGroup = new Map<string, Device[]>()
    for (const d of rows) {
      const key = d.group_id === null ? 'none' : String(d.group_id)
      const list = byGroup.get(key)
      if (list === undefined) byGroup.set(key, [d])
      else list.push(d)
    }

    const list = (groups.data ?? []).map((group) => {
      const key = group.id === null ? 'none' : String(group.id)
      return { group, devices: (byGroup.get(key) ?? []).sort(compareForTriage) }
    })

    // Anything whose group is not in the list still has to appear.
    //
    // The service sends an Ungrouped bucket when one is needed, but a device
    // can also point at a group this list has not caught up with — the moment
    // after a group is deleted, say. Dropping it would make a device silently
    // vanish from the dashboard, which is the one thing a monitor must never
    // do: an invisible device reads exactly like a healthy one.
    const covered = new Set(list.map((s) => (s.group.id === null ? 'none' : String(s.group.id))))
    const orphans = [...byGroup.entries()]
      .filter(([key]) => !covered.has(key))
      .flatMap(([, devices]) => devices)

    if (orphans.length > 0) {
      list.push({ group: ungroupedSection, devices: orphans.sort(compareForTriage) })
    }

    return list
      .filter((s) => s.devices.length > 0 || (s.group.id !== null && search === '' && filterTag === '' && filterStatus === ''))
      .sort((a, b) => {
        const aDown = a.devices.some((d) => d.status === 'DOWN') ? 0 : 1
        const bDown = b.devices.some((d) => d.status === 'DOWN') ? 0 : 1
        if (aDown !== bDown) return aDown - bDown
        // Ungrouped always renders last.
        if (a.group.ungrouped !== b.group.ungrouped) return a.group.ungrouped ? 1 : -1
        return a.group.sort_order - b.group.sort_order
      })
  }, [rows, groups.data, search, filterTag, filterStatus])

  const toggle = (id: number, on: boolean) => {
    const next = new Set(selected)
    if (on) next.add(id)
    else next.delete(id)
    setSelected(next)
  }

  const toggleAll = (ids: number[], on: boolean) => {
    const next = new Set(selected)
    for (const id of ids) {
      if (on) next.add(id)
      else next.delete(id)
    }
    setSelected(next)
  }

  const runBulk = (body: Parameters<typeof bulk.mutate>[0]) => {
    bulk.mutate(body, {
      onSuccess: () => {
        setSelected(new Set())
        setBulkMoveTo('')
        setBulkDeleting(false)
      },
    })
  }

  const counts = summary.data?.counts
  const tableProps = {
    selected,
    onToggle: toggle,
    onToggleAll: toggleAll,
    history: stream.rows,
    recentlyChanged: stream.recentlyChanged,
    onEdit: setEditing,
    onDelete: setDeleting,
    onPause: setPausingDevice,
    onCheck: (device: Device) => checkDevice.mutate(device.id),
    checking: checkDevice.isPending ? (checkDevice.variables ?? null) : null,
  }

  return (
    <div className="space-y-6">
      <div className="grid gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Tile label="Devices" value={counts?.devices ?? '—'} />
        <Tile label="Up" value={counts?.up ?? '—'} tone="up" />
        <Tile label="Down" value={counts?.down ?? '—'} tone={counts && counts.down > 0 ? 'down' : 'default'} />
        <Tile label="Unknown" value={counts?.unknown ?? '—'} />
        <Tile label="Paused" value={counts?.paused ?? '—'} />
        <Tile
          label="Open incidents"
          value={summary.data?.open_incidents.length ?? '—'}
          tone={(summary.data?.open_incidents.length ?? 0) > 0 ? 'down' : 'default'}
        />
      </div>

      {summary.data !== undefined && summary.data.open_incidents.length > 0 && (
        <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
          <h2 className="mb-2 text-sm font-semibold">Open incidents</h2>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {summary.data.open_incidents.map((inc) => (
              <li key={inc.id} className="flex items-baseline justify-between gap-4 py-1.5">
                <Link
                  to={`/devices/${inc.device_id}`}
                  className="font-medium hover:text-accent-600 hover:underline"
                >
                  {inc.device_name}
                </Link>
                <span className="truncate text-xs text-slate-500 dark:text-slate-400">
                  {inc.cause}
                </span>
                <span className="shrink-0 tabular-nums text-down-600 dark:text-down-500">
                  down {Math.round((inc.duration_sec ?? 0) / 60)} min
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex rounded-md bg-slate-200 p-0.5 dark:bg-slate-800">
          {(['grouped', 'flat'] as Mode[]).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => persistMode(m)}
              className={`rounded px-3 py-1 text-sm capitalize ${
                mode === m
                  ? 'bg-white shadow-sm dark:bg-slate-700'
                  : 'text-slate-600 dark:text-slate-300'
              }`}
            >
              {m}
            </button>
          ))}
        </div>

        <input
          className={`${inputClass} w-48`}
          placeholder="Search name, IP, tag"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Search devices"
        />
        <select
          className={`${inputClass} w-36`}
          value={filterStatus}
          onChange={(e) => setFilterStatus(e.target.value as Status | '')}
          aria-label="Filter by status"
        >
          <option value="">Any status</option>
          <option value="UP">UP</option>
          <option value="DOWN">DOWN</option>
          <option value="UNKNOWN">UNKNOWN</option>
        </select>
        <select
          className={`${inputClass} w-36`}
          value={filterTag}
          onChange={(e) => setFilterTag(e.target.value)}
          aria-label="Filter by tag"
        >
          <option value="">Any tag</option>
          {allTags.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>

        <div className="ml-auto flex gap-2">
          <Button tone="primary" onClick={() => setAdding(true)}>
            Add device
          </Button>
        </div>
      </div>

      {selected.size > 0 && (
        <div className="sticky top-2 z-10 flex flex-wrap items-center gap-2 rounded-lg bg-accent-50 px-3 py-2 text-sm ring-1 ring-accent-500/30 dark:bg-accent-700/20">
          <span className="font-medium">{selected.size} selected</span>

          <select
            className={`${inputClass} w-44`}
            value={bulkMoveTo}
            aria-label="Move to group"
            onChange={(e) => {
              setBulkMoveTo(e.target.value)
              if (e.target.value !== '') {
                runBulk({
                  ids: [...selected],
                  op: 'move',
                  group_id: e.target.value === 'none' ? null : Number(e.target.value),
                })
              }
            }}
          >
            <option value="">Move to group…</option>
            <option value="none">Ungrouped</option>
            {(groups.data ?? [])
              .filter((g) => g.id !== null)
              .map((g) => (
                <option key={g.id} value={String(g.id)}>
                  {g.name}
                </option>
              ))}
          </select>

          <Button
            size="sm"
            onClick={() => runBulk({ ids: [...selected], op: 'pause', minutes: 60 })}
          >
            Pause 1 h
          </Button>
          <Button size="sm" onClick={() => runBulk({ ids: [...selected], op: 'resume' })}>
            Resume
          </Button>
          <Button size="sm" tone="danger" onClick={() => setBulkDeleting(true)}>
            Delete
          </Button>
          <Button size="sm" tone="ghost" onClick={() => setSelected(new Set())}>
            Clear
          </Button>
          {bulk.isPending && <span className="text-xs text-slate-500">Working…</span>}
        </div>
      )}

      <ErrorNote error={devices.error ?? bulk.error ?? deleteDevice.error} />

      {devices.isPending ? (
        <Spinner label="Loading devices" />
      ) : rows.length === 0 ? (
        <Empty>
          {(devices.data ?? []).length === 0
            ? 'No devices yet. Add one to start monitoring.'
            : 'No devices match these filters.'}
        </Empty>
      ) : mode === 'flat' ? (
        <div className="overflow-x-auto rounded-lg bg-white shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
          <DeviceTable devices={[...rows].sort(compareForTriage)} {...tableProps} />
        </div>
      ) : (
        <div className="space-y-4">
          {sections.map(({ group, devices: members }) => {
            const key = group.id === null ? 'none' : String(group.id)
            const isCollapsed = collapsed.has(key)
            const anyDown = members.some((d) => d.status === 'DOWN')
            const stats = group.stats

            return (
              <section
                key={key}
                className={`overflow-hidden rounded-lg bg-white shadow-sm ring-1 dark:bg-slate-900 ${
                  anyDown
                    ? 'ring-down-500/40 dark:ring-down-500/40'
                    : 'ring-slate-200 dark:ring-slate-700'
                }`}
              >
                <header
                  className={`flex flex-wrap items-center gap-3 px-3 py-2 ${
                    anyDown ? 'bg-down-100 dark:bg-down-600/15' : 'bg-slate-50 dark:bg-slate-800/60'
                  }`}
                >
                  <button
                    type="button"
                    onClick={() => toggleCollapsed(key)}
                    aria-expanded={!isCollapsed}
                    className="text-xs text-slate-500 hover:text-slate-900 dark:hover:text-white"
                  >
                    {isCollapsed ? '▸' : '▾'}
                  </button>

                  {group.color !== '' && (
                    <span
                      aria-hidden="true"
                      className="size-3 shrink-0 rounded-full"
                      style={{ backgroundColor: group.color }}
                    />
                  )}

                  {group.id === null ? (
                    <span className="font-semibold">{group.name}</span>
                  ) : (
                    <Link
                      to={`/groups/${group.id}`}
                      className="font-semibold hover:text-accent-600 hover:underline"
                    >
                      {group.name}
                    </Link>
                  )}

                  <span className="text-xs tabular-nums text-slate-500 dark:text-slate-400">
                    {stats === undefined
                      ? `${members.length} devices`
                      : `${stats.up}/${stats.members} up`}
                    {stats !== undefined && stats.paused > 0 && ` · ${stats.paused} paused`}
                  </span>

                  {stats?.uptime_today_pct != null && (
                    <span
                      className="text-xs tabular-nums text-slate-500 dark:text-slate-400"
                      title="Share of all member checks that succeeded today"
                    >
                      today {uptimePct(stats.uptime_today_pct)}
                    </span>
                  )}

                  {group.paused && (
                    <span className="rounded bg-slate-200 px-1.5 py-0.5 text-[0.65rem] uppercase text-slate-600 dark:bg-slate-700 dark:text-slate-300">
                      maintenance
                    </span>
                  )}

                  {group.id !== null && (
                    <>
                      <GroupUptimeStrip
                        groupId={group.id}
                        className="hidden w-56 lg:ml-auto lg:grid"
                      />
                      <div className="ml-auto flex gap-1 lg:ml-3">
                        <Button size="sm" tone="ghost" onClick={() => setPausingGroup(group)}>
                          {group.paused ? 'Resume group' : 'Pause group'}
                        </Button>
                      </div>
                    </>
                  )}
                </header>

                {!isCollapsed &&
                  (members.length === 0 ? (
                    <p className="px-3 py-4 text-sm text-slate-500 dark:text-slate-400">
                      No devices in this group yet.
                    </p>
                  ) : (
                    <div className="overflow-x-auto">
                      <DeviceTable devices={members} {...tableProps} />
                    </div>
                  ))}
              </section>
            )
          })}
        </div>
      )}

      {summary.data !== undefined && summary.data.worst_offenders.length > 0 && (
        <section className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700">
          <h2 className="mb-2 text-sm font-semibold">Worst 24 hours</h2>
          <ul className="space-y-1 text-sm">
            {summary.data.worst_offenders.map((o) => (
              <li key={o.device_id} className="flex items-baseline justify-between gap-3">
                <Link to={`/devices/${o.device_id}`} className="hover:underline">
                  {o.name}
                  {o.group_name !== '' && (
                    <span className="ml-1 text-xs text-slate-400">{o.group_name}</span>
                  )}
                </Link>
                <span className="tabular-nums">
                  {uptimePct(o.uptime_pct)}{' '}
                  <span className="text-xs text-slate-400">
                    ({o.failures} of {o.checks} failed)
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {(adding || editing !== null) && (
        <DeviceFormModal
          device={editing}
          groups={groups.data ?? []}
          defaults={settings.data?.defaults}
          onClose={() => {
            setAdding(false)
            setEditing(null)
          }}
        />
      )}

      {deleting !== null && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          message={
            <>
              This removes the device and its entire history — heartbeats, incidents and uptime.
              It cannot be undone.
            </>
          }
          confirmLabel="Delete device"
          busy={deleteDevice.isPending}
          onCancel={() => setDeleting(null)}
          onConfirm={() =>
            deleteDevice.mutate(deleting.id, { onSuccess: () => setDeleting(null) })
          }
        />
      )}

      {bulkDeleting && (
        <Confirm
          title={`Delete ${selected.size} devices?`}
          message="This removes every selected device and all of its history. It cannot be undone."
          confirmLabel="Delete them"
          busy={bulk.isPending}
          onCancel={() => setBulkDeleting(false)}
          onConfirm={() => runBulk({ ids: [...selected], op: 'delete' })}
        />
      )}

      {pausingDevice !== null && (
        <PauseDialog
          what={pausingDevice.name}
          pausedUntil={pausingDevice.paused_until}
          busy={pauseDevice.isPending}
          error={pauseDevice.error}
          onClose={() => setPausingDevice(null)}
          onSubmit={(body: PauseRequest) =>
            pauseDevice.mutate(
              { id: pausingDevice.id, body },
              { onSuccess: () => setPausingDevice(null) },
            )
          }
        />
      )}

      {pausingGroup !== null && pausingGroup.id !== null && (
        <PauseDialog
          what={`the ${pausingGroup.name} group`}
          pausedUntil={pausingGroup.paused_until}
          busy={pauseGroup.isPending}
          error={pauseGroup.error}
          onClose={() => setPausingGroup(null)}
          onSubmit={(body: PauseRequest) =>
            pauseGroup.mutate(
              { id: pausingGroup.id as number, body },
              { onSuccess: () => setPausingGroup(null) },
            )
          }
        />
      )}
    </div>
  )
}
