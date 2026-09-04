import { Link } from 'react-router-dom'

import type { Device } from '../api/types'
import { addr, badgeOf, latency, since } from '../lib/format'
import { Sparkline } from './Sparkline'
import { StatusBadge } from './StatusBadge'
import { Button } from './ui'

export interface RowHistory {
  values: (number | null)[]
  downs: boolean[]
}

interface Props {
  devices: Device[]
  selected: Set<number>
  onToggle: (id: number, selected: boolean) => void
  onToggleAll: (ids: number[], selected: boolean) => void
  history: Map<number, RowHistory>
  recentlyChanged: Set<number>
  onEdit: (device: Device) => void
  onDelete: (device: Device) => void
  onPause: (device: Device) => void
  onCheck: (device: Device) => void
  checking: number | null
}

export function DeviceTable({
  devices,
  selected,
  onToggle,
  onToggleAll,
  history,
  recentlyChanged,
  onEdit,
  onDelete,
  onPause,
  onCheck,
  checking,
}: Props) {
  const ids = devices.map((d) => d.id)
  const allSelected = ids.length > 0 && ids.every((id) => selected.has(id))

  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="border-b border-slate-200 text-left text-xs uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:text-slate-400">
          <th className="w-8 px-2 py-2">
            <input
              type="checkbox"
              aria-label="Select all in this section"
              checked={allSelected}
              onChange={(e) => onToggleAll(ids, e.target.checked)}
              className="size-4 rounded border-slate-300 accent-accent-600"
            />
          </th>
          <th className="px-2 py-2 font-medium">Device</th>
          <th className="px-2 py-2 font-medium">Address</th>
          <th className="px-2 py-2 font-medium">Status</th>
          {/* The two columns that go first when space is short: a sparkline
              and a relative time are context, where status and address are
              the answer. */}
          <th className="hidden px-2 py-2 font-medium md:table-cell">Last check</th>
          <th className="px-2 py-2 font-medium">Latency</th>
          <th className="hidden px-2 py-2 font-medium lg:table-cell">24 h</th>
          <th className="px-2 py-2 text-right font-medium">Actions</th>
        </tr>
      </thead>
      <tbody>
        {devices.map((device) => {
          const rows = history.get(device.id)
          return (
            <tr
              key={device.id}
              className="border-b border-slate-100 last:border-0 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50"
            >
              <td className="px-2 py-2">
                <input
                  type="checkbox"
                  aria-label={`Select ${device.name}`}
                  checked={selected.has(device.id)}
                  onChange={(e) => onToggle(device.id, e.target.checked)}
                  className="size-4 rounded border-slate-300 accent-accent-600"
                />
              </td>
              <td className="px-2 py-2">
                <Link
                  to={`/devices/${device.id}`}
                  className="font-medium text-slate-900 hover:text-accent-600 hover:underline dark:text-slate-100 dark:hover:text-accent-500"
                >
                  {device.name}
                </Link>
                {device.tags.length > 0 && (
                  <span className="ml-2 space-x-1">
                    {device.tags.map((tag) => (
                      <span
                        key={tag}
                        className="rounded bg-slate-100 px-1.5 py-0.5 text-[0.65rem] text-slate-500 dark:bg-slate-800 dark:text-slate-400"
                      >
                        {tag}
                      </span>
                    ))}
                  </span>
                )}
                {device.last_error !== '' && badgeOf(device) !== 'UP' && (
                  <div
                    className="truncate text-xs text-slate-500 dark:text-slate-400"
                    title={device.last_error}
                  >
                    {device.last_error}
                  </div>
                )}
              </td>
              <td className="px-2 py-2 font-mono text-xs text-slate-600 dark:text-slate-300">
                {addr(device)}
              </td>
              <td className="px-2 py-2">
                <StatusBadge badge={badgeOf(device)} fresh={recentlyChanged.has(device.id)} />
              </td>
              <td
                className="hidden px-2 py-2 text-slate-600 md:table-cell dark:text-slate-300"
                title={device.last_check_at ?? 'never'}
              >
                {since(device.last_check_at)}
              </td>
              <td className="px-2 py-2 tabular-nums text-slate-600 dark:text-slate-300">
                {latency(device.last_latency_ms)}
              </td>
              <td className="hidden px-2 py-2 lg:table-cell">
                <Sparkline values={rows?.values ?? []} {...(rows ? { downs: rows.downs } : {})} />
              </td>
              <td className="px-2 py-2">
                <div className="flex justify-end gap-1">
                  <Button
                    size="sm"
                    tone="ghost"
                    onClick={() => onCheck(device)}
                    disabled={checking === device.id}
                    title="Probe now, without touching stored state"
                  >
                    {checking === device.id ? '…' : 'Check'}
                  </Button>
                  <Button size="sm" tone="ghost" onClick={() => onPause(device)}>
                    {device.effective.paused ? 'Resume' : 'Pause'}
                  </Button>
                  <Button size="sm" tone="ghost" onClick={() => onEdit(device)}>
                    Edit
                  </Button>
                  <Button size="sm" tone="ghost" onClick={() => onDelete(device)}>
                    Delete
                  </Button>
                </div>
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
