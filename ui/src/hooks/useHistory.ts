import { useCallback, useRef, useState } from 'react'

import type { HeartbeatEvent, MonitorEvent } from '../api/events'
import type { RowHistory } from '../components/DeviceTable'

/** How many points a row sparkline keeps. */
const WINDOW = 40

/** How long a row pulses after a transition. */
const FRESH_MS = 4000

/**
 * Keeps a short in-memory latency history per device, fed by the heartbeat
 * stream.
 *
 * This is the one place the UI renders straight from events instead of from a
 * query. Heartbeats arrive several times a second across 200 devices, and
 * refetching a series per row on each would be absurd; nothing here is
 * authoritative, and it is discarded on reload.
 */
export function useHistory() {
  const [rows, setRows] = useState<Map<number, RowHistory>>(new Map())
  const [recentlyChanged, setRecentlyChanged] = useState<Set<number>>(new Set())
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>())

  const push = useCallback((hb: HeartbeatEvent) => {
    setRows((prev) => {
      const next = new Map(prev)
      const current = next.get(hb.device_id) ?? { values: [], downs: [] }
      const values = [...current.values, hb.status === 'UP' ? hb.latency_ms : null].slice(-WINDOW)
      const downs = [...current.downs, hb.status === 'DOWN'].slice(-WINDOW)
      next.set(hb.device_id, { values, downs })
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
