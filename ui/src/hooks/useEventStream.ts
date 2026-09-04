import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'

import { authHeaders, eventsUrl } from '../api/client'
import { parseEventStream, type EventType, type MonitorEvent } from '../api/events'
import { keys } from './queries'

export type StreamState = 'connecting' | 'open' | 'closed'

/** Backoff for reconnecting, in milliseconds. Capped, and jittered on use. */
const BACKOFF_MS = [500, 1000, 2000, 5000, 10_000, 15_000]

interface Options {
  types?: EventType[]
  /** Called for every event, after query invalidation. */
  onEvent?: (event: MonitorEvent) => void
  enabled?: boolean
}

/**
 * Holds one SSE connection open and turns events into cache invalidations.
 *
 * The division of labour is deliberate: the stream decides *when* data is
 * stale and TanStack Query decides *what* to refetch. Rendering straight from
 * event payloads would mean two sources of truth for every row and a dashboard
 * that drifts from the database after a single missed event.
 *
 * Heartbeats are the exception — they arrive several times a second and
 * invalidating on each would defeat the point — so they are only handed to
 * `onEvent`, where the sparklines consume them in place.
 */
export function useEventStream(options: Options = {}): StreamState {
  const { types, onEvent, enabled = true } = options
  const queryClient = useQueryClient()
  const [state, setState] = useState<StreamState>('connecting')

  // Held in a ref so a new callback identity does not tear down the
  // connection, and written in an effect rather than during render.
  const onEventRef = useRef(onEvent)
  useEffect(() => {
    onEventRef.current = onEvent
  }, [onEvent])

  const typesKey = types === undefined ? '' : types.join(',')

  useEffect(() => {
    if (!enabled) return

    const controller = new AbortController()
    let attempt = 0
    let stopped = false
    let retryTimer: ReturnType<typeof setTimeout> | undefined

    const run = async (): Promise<void> => {
      while (!stopped) {
        try {
          const res = await fetch(eventsUrl(typesKey === '' ? undefined : typesKey.split(',')), {
            headers: { ...(authHeaders() as Record<string, string>), Accept: 'text/event-stream' },
            signal: controller.signal,
          })
          if (!res.ok || res.body === null) throw new Error(`stream failed: ${res.status}`)

          attempt = 0
          if (!stopped) setState('open')

          for await (const event of parseEventStream(res.body, controller.signal)) {
            if (stopped) break
            invalidate(queryClient, event)
            onEventRef.current?.(event)
          }
        } catch {
          if (controller.signal.aborted || stopped) return
        }

        if (stopped) return
        setState('closed')

        // The service may simply be restarting, so reconnect rather than
        // making the user reload. Jitter keeps several open windows from
        // retrying in lockstep.
        const base = BACKOFF_MS[Math.min(attempt, BACKOFF_MS.length - 1)] ?? 15_000
        attempt += 1
        await new Promise<void>((resolve) => {
          retryTimer = setTimeout(resolve, base + Math.random() * base * 0.3)
        })
        if (!stopped) setState('connecting')
      }
    }

    void run()

    return () => {
      stopped = true
      if (retryTimer !== undefined) clearTimeout(retryTimer)
      controller.abort()
    }
  }, [enabled, typesKey, queryClient])

  return enabled ? state : 'closed'
}

/** Maps one event onto the queries it makes stale. */
function invalidate(queryClient: QueryClient, event: MonitorEvent): void {
  switch (event.type) {
    case 'device_status':
      void queryClient.invalidateQueries({ queryKey: keys.devices })
      void queryClient.invalidateQueries({ queryKey: keys.summary })
      void queryClient.invalidateQueries({ queryKey: keys.health })
      break
    case 'incident':
      void queryClient.invalidateQueries({ queryKey: keys.summary })
      void queryClient.invalidateQueries({ queryKey: keys.device(event.data.device_id) })
      void queryClient.invalidateQueries({ queryKey: keys.incidents(event.data.device_id) })
      break
    case 'group_status':
      void queryClient.invalidateQueries({ queryKey: keys.groups })
      void queryClient.invalidateQueries({ queryKey: keys.summary })
      break
    case 'settings':
      void queryClient.invalidateQueries({ queryKey: keys.settings })
      void queryClient.invalidateQueries({ queryKey: keys.devices })
      break
    case 'heartbeat':
      // Deliberately nothing: seven a second at 200 devices, and the
      // sparklines consume them directly.
      break
  }
}
