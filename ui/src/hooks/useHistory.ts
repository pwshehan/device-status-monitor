import { useCallback, useRef, useState } from 'react'

import type { HeartbeatEvent, MonitorEvent } from '../api/events'
import type { Device, Status } from '../api/types'

/** One live check, with the time it happened. */
export interface LiveCheck {
  /** Unix seconds, as the event carries it. */
  t: number
  status: Status
}

export type RowHistory = LiveCheck[]

/** How many live checks a row keeps. Matches the API's RecentCheckCount. */
const WINDOW = 40

/** How long a row pulses after a transition. */
const FRESH_MS = 4000

/**
 * Keeps a short in-memory history per device, fed by the heartbeat stream.
 *
 * This is the one place the UI renders straight from events instead of from a
 * query. Heartbeats arrive several times a second across 200 devices, and
 * refetching the device list on each would be absurd; nothing here is
 * authoritative, and it is discarded on reload — which is why each row also
 * gets a baseline from the server (see mergeChecks).
 */
export function useHistory() {
  const [rows, setRows] = useState<Map<number, RowHistory>>(new Map())
  const [recentlyChanged, setRecentlyChanged] = useState<Set<number>>(new Set())
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>())

  const push = useCallback((hb: HeartbeatEvent) => {
    setRows((prev) => {
      const next = new Map(prev)
      const current = next.get(hb.device_id) ?? []
      next.set(hb.device_id, [...current, { t: hb.t, status: hb.status }].slice(-WINDOW))
      return next
    })
  }, [])

  const markChanged = useCallback((deviceId: number) => {
    setRecentlyChanged((prev) => new Set(prev).add(deviceId))

    const existing = timers.current.get(deviceId)
    if (existing !== undefined) clearTimeout(existing)
    timers.current.set(
      deviceId,
      setTimeout(() => {
        timers.current.delete(deviceId)
        setRecentlyChanged((prev) => {
          const next = new Set(prev)
          next.delete(deviceId)
          return next
        })
      }, FRESH_MS),
    )
  }, [])

  const onEvent = useCallback(
    (event: MonitorEvent) => {
      if (event.type === 'heartbeat') push(event.data)
      else if (event.type === 'device_status') markChanged(event.data.device_id)
    },
    [push, markChanged],
  )

  return { rows, recentlyChanged, onEvent }
}

/**
 * Combines the server's recent checks with whatever has arrived live since.
 *
 * The baseline means a row's strip is populated the moment the page loads
 * rather than after a probe interval of watching, and the live part means it
 * keeps moving between refetches. Live checks are filtered by the device's own
 * `last_check_at` — the timestamp of the newest check the server included — so
 * a refetch that already contains them does not draw them twice.
 */
export function mergeChecks(device: Device, live: RowHistory | undefined): Status[] {
  const baseline: Status[] = []
  for (const c of device.recent_checks) baseline.push(c === 'D' ? 'DOWN' : 'UP')

  if (live === undefined || live.length === 0) return baseline

  const knownUpTo =
    device.last_check_at === null ? 0 : Math.floor(Date.parse(device.last_check_at) / 1000)

  const newer = live.filter((c) => c.t > knownUpTo).map((c) => c.status)
  return [...baseline, ...newer].slice(-WINDOW)
}
