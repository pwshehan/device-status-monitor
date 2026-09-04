import type { Status } from './types'

/** Event payloads, mirroring the exported types in internal/api/dto.go. */

export interface DeviceStatusEvent {
  device_id: number
  name: string
  group_id: number | null
  group_name: string
  status: Status
  previous_status: Status
  at: string
  latency_ms: number
  class: string
  error?: string
  incident_id?: number
}

export interface HeartbeatEvent {
  device_id: number
  status: Status
  latency_ms: number
  t: number
  class: string
  error?: string
}

export interface IncidentEvent {
  id: number
  device_id: number
  device_name: string
  group_id: number | null
  state: 'opened' | 'resolved'
  started_at: string
  resolved_at: string | null
  duration_sec: number | null
  cause: string
}

export interface GroupStatusEvent {
  group_id: number | null
  name: string
  members: number
  up: number
  down: number
  unknown: number
  paused: number
  uptime_today_pct: number | null
}

export interface SettingsEvent {
  changed_keys: string[]
}

export type MonitorEvent =
  | { type: 'device_status'; data: DeviceStatusEvent }
  | { type: 'heartbeat'; data: HeartbeatEvent }
  | { type: 'incident'; data: IncidentEvent }
  | { type: 'group_status'; data: GroupStatusEvent }
  | { type: 'settings'; data: SettingsEvent }

export type EventType = MonitorEvent['type']

/**
 * Parses an SSE stream into events.
 *
 * Hand-rolled rather than using EventSource because the stream needs an
 * Authorization header and EventSource cannot set one; the alternative would
 * be a token in the query string, which ends up in logs and history.
 *
 * Comment lines (the service's connect notice and its keep-alives) are
 * skipped. A frame is terminated by a blank line, so a chunk boundary in the
 * middle of one is held over rather than parsed as truncated JSON.
 */
export async function* parseEventStream(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): AsyncGenerator<MonitorEvent> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    while (true) {
      if (signal?.aborted === true) return
      const { done, value } = await reader.read()
      if (done) return

      buffer += decoder.decode(value, { stream: true })

      // A frame ends at a blank line, which is CRLF from some peers and LF
      // from others — matching only '\n\n' would hold every frame for ever
      // against a CRLF one.
      for (;;) {
        const boundary = /\r?\n\r?\n/.exec(buffer)
        if (boundary === null) break
        const frame = buffer.slice(0, boundary.index)
        buffer = buffer.slice(boundary.index + boundary[0].length)
        const event = parseFrame(frame)
        if (event !== null) yield event
      }
    }
  } finally {
    reader.releaseLock()
  }
}

function parseFrame(frame: string): MonitorEvent | null {
  let type: string | null = null
  let data: string | null = null

  for (const raw of frame.split('\n')) {
    const line = raw.replace(/\r$/, '')
    if (line === '' || line.startsWith(':')) continue
    if (line.startsWith('event:')) type = line.slice(6).trim()
    else if (line.startsWith('data:')) data = line.slice(5).trim()
  }
  if (type === null || data === null) return null

  try {
    const payload: unknown = JSON.parse(data)
    return { type, data: payload } as MonitorEvent
  } catch {
    // A malformed frame is not worth tearing the stream down for.
    return null
  }
}
