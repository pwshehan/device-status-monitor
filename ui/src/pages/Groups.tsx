import { useState } from 'react'
import { Link } from 'react-router-dom'

import type { Group, PauseRequest } from '../api/types'
import { GroupFormModal } from '../components/GroupFormModal'
import { GroupUptimeStrip } from '../components/GroupUptimeStrip'
import { PauseDialog } from '../components/PauseDialog'
import { Button, Confirm, Empty, ErrorNote, Spinner } from '../components/ui'
import {
  useDeleteGroup,
  useGroups,
  usePauseGroup,
  useReorderGroups,
  useSettings,
} from '../hooks/queries'
import { uptimePct } from '../lib/format'

export function Groups() {
  const groups = useGroups()
  const settings = useSettings()
  const remove = useDeleteGroup()
  const pause = usePauseGroup()
  const reorder = useReorderGroups()

  const [editing, setEditing] = useState<Group | null>(null)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<Group | null>(null)
  const [pausing, setPausing] = useState<Group | null>(null)

  // The Ungrouped bucket is a dashboard section, not a row that can be
  // renamed, reordered or deleted.
  const real = (groups.data ?? []).filter((g) => g.id !== null)

  const move = (index: number, delta: number) => {
    const next = [...real]
    const target = index + delta
    const a = next[index]
    const b = next[target]
    if (a === undefined || b === undefined) return
    next[index] = b
    next[target] = a
    reorder.mutate(next.map((g) => g.id as number))
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Groups</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">
            A group is a device's structural home: it sets probe defaults, routes alerts and can be
            paused as one for maintenance.
          </p>
        </div>
        <Button tone="primary" onClick={() => setAdding(true)}>
          New group
        </Button>
      </div>

      <ErrorNote error={groups.error ?? remove.error ?? reorder.error} />

      {groups.isPending ? (
        <Spinner label="Loading groups" />
      ) : real.length === 0 ? (
        <Empty>
          No groups yet. Devices work fine without them, but a group is what gives you site-level
          alert routing, shared probe settings and one maintenance window for everything on a site.
        </Empty>
      ) : (
        <ul className="space-y-3">
          {real.map((group, index) => (
            <li
              key={group.id}
              className="rounded-lg bg-white p-4 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700"
            >
              <div className="flex flex-wrap items-start gap-3 sm:flex-nowrap">
                <span
                  aria-hidden="true"
                  className="mt-1 size-3 shrink-0 rounded-full"
                  style={{ backgroundColor: group.color === '' ? '#94a3b8' : group.color }}
                />
                <div className="min-w-0">
                  <Link
                    to={`/groups/${group.id}`}
                    className="font-semibold hover:text-accent-600 hover:underline"
                  >
                    {group.name}
                  </Link>
                  {group.description !== '' && (
                    <p className="text-sm text-slate-500 dark:text-slate-400">
                      {group.description}
                    </p>
                  )}
                  <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
                    {group.stats?.members ?? 0} devices
                    {group.stats !== undefined && ` · ${group.stats.up} up, ${group.stats.down} down`}
                    {group.stats?.uptime_today_pct != null &&
                      ` · today ${uptimePct(group.stats.uptime_today_pct)}`}
                  </p>
                  <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
                    {group.check_interval_sec === null
                      ? 'Interval inherited from the global default'
                      : `Interval ${group.check_interval_sec}s for members that do not override it`}
                    {' · '}
                    {group.recipients === null
                      ? 'alerts to the global list'
                      : `alerts to ${group.recipients}`}
                    {!group.notify && ' · alerts silenced'}
                  </p>
                </div>

                <div className="ml-auto flex shrink-0 flex-col items-end gap-2">
                  <div className="flex gap-1">
                    <Button
                      size="sm"
                      tone="ghost"
                      onClick={() => move(index, -1)}
                      disabled={index === 0 || reorder.isPending}
                      aria-label={`Move ${group.name} up`}
                    >
                      ↑
                    </Button>
                    <Button
                      size="sm"
                      tone="ghost"
                      onClick={() => move(index, 1)}
                      disabled={index === real.length - 1 || reorder.isPending}
                      aria-label={`Move ${group.name} down`}
                    >
                      ↓
                    </Button>
                    <Button size="sm" onClick={() => setPausing(group)}>
                      {group.paused ? 'Resume' : 'Pause'}
                    </Button>
                    <Button size="sm" onClick={() => setEditing(group)}>
                      Edit
                    </Button>
                    <Button size="sm" tone="danger" onClick={() => setDeleting(group)}>
                      Delete
                    </Button>
                  </div>
                  {group.id !== null && (
                    <GroupUptimeStrip groupId={group.id} className="w-64" blockHeight="h-2.5" />
                  )}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}

      {(adding || editing !== null) && (
        <GroupFormModal
          group={editing}
          defaults={settings.data?.defaults}
          globalRecipients={settings.data?.alerts.recipients ?? ''}
          onClose={() => {
            setAdding(false)
            setEditing(null)
          }}
        />
      )}

      {deleting !== null && (
        <Confirm
          title={`Delete the ${deleting.name} group?`}
          message={
            <>
              Its {deleting.stats?.members ?? 0} device
              {(deleting.stats?.members ?? 0) === 1 ? '' : 's'} will <strong>not</strong> be
              deleted — they move to <strong>Ungrouped</strong> and keep all of their history.
              They will fall back to the global probe defaults and the global alert recipients.
            </>
          }
          confirmLabel="Delete group"
          busy={remove.isPending}
          onCancel={() => setDeleting(null)}
          onConfirm={() =>
            remove.mutate(deleting.id as number, { onSuccess: () => setDeleting(null) })
          }
        />
      )}

      {pausing !== null && (
        <PauseDialog
          what={`the ${pausing.name} group`}
          pausedUntil={pausing.paused_until}
          busy={pause.isPending}
          error={pause.error}
          onClose={() => setPausing(null)}
          onSubmit={(body: PauseRequest) =>
            pause.mutate(
              { id: pausing.id as number, body },
              { onSuccess: () => setPausing(null) },
            )
          }
        />
      )}
    </div>
  )
}
