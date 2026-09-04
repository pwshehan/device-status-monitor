import { describe, expect, it } from 'vitest'

import { parseEventStream, type MonitorEvent } from './events'

/** Feeds the parser a fixed set of chunks, as a network would. */
function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
      controller.close()
    },
  })
}

async function collect(chunks: string[]): Promise<MonitorEvent[]> {
  const out: MonitorEvent[] = []
  for await (const event of parseEventStream(streamOf(chunks))) out.push(event)
  return out
}

describe('parseEventStream', () => {
  it('reads events and skips the service comment lines', async () => {
    const events = await collect([
      ': connected 2026-09-03T08:33:28Z\n\n',
      'event: device_status\ndata: {"device_id":7,"status":"DOWN","previous_status":"UP"}\n\n',
      ': keep-alive\n\n',
      'event: incident\ndata: {"id":3,"device_id":7,"state":"opened"}\n\n',
    ])

    expect(events).toHaveLength(2)
    expect(events[0]?.type).toBe('device_status')
    expect(events[1]?.type).toBe('incident')
    if (events[0]?.type === 'device_status') {
      expect(events[0].data.device_id).toBe(7)
      expect(events[0].data.previous_status).toBe('UP')
    }
  })

  it('holds a frame split across chunks instead of parsing half of it', async () => {
    // A chunk boundary lands mid-JSON, which is the case that turns into
    // "Unexpected end of JSON input" if frames are parsed per chunk.
    const events = await collect([
      'event: heartbeat\ndata: {"device_id":1,"stat',
      'us":"UP","latency_ms":4,"t":1}\n\n',
    ])

    expect(events).toHaveLength(1)
    if (events[0]?.type === 'heartbeat') {
      expect(events[0].data.latency_ms).toBe(4)
    }
  })

  it('drops a malformed frame without ending the stream', async () => {
    const events = await collect([
      'event: heartbeat\ndata: {oh no\n\n',
      'event: heartbeat\ndata: {"device_id":2,"status":"UP","latency_ms":9,"t":2}\n\n',
    ])

    // One bad frame must not cost the connection: everything after it still
    // arrives.
    expect(events).toHaveLength(1)
    if (events[0]?.type === 'heartbeat') expect(events[0].data.device_id).toBe(2)
  })

  it('tolerates CRLF line endings', async () => {
    const events = await collect([
      'event: settings\r\ndata: {"changed_keys":["smtp.host"]}\r\n\r\n',
    ])
    expect(events[0]?.type).toBe('settings')
  })
})
