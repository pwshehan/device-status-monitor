import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'

import type { Device, Group, Health, Settings, Summary } from '../api/types'

/** A device with the fields a test does not care about filled in. */
export function makeDevice(overrides: Partial<Device> = {}): Device {
  return {
    id: 1,
    group_id: null,
    group_name: '',
    name: 'Core switch',
    ip_address: '10.0.0.1',
    port: 22,
    check_interval_sec: null,
    timeout_sec: null,
    failure_threshold: null,
    recovery_threshold: null,
    enabled: true,
    notify: true,
    paused_until: null,
    tags: [],
    status: 'UP',
    consecutive_failures: 0,
    consecutive_successes: 3,
    first_failure_at: null,
    last_check_at: new Date().toISOString(),
    last_latency_ms: 4,
    last_error: '',
    last_status_change_at: new Date().toISOString(),
    effective: {
      check_interval_sec: 30,
      timeout_sec: 3,
      failure_threshold: 3,
      recovery_threshold: 1,
      notify: true,
      recipients: ['ops@example.com'],
      paused_until: null,
      paused: false,
      source: {
        interval: 'global',
        timeout: 'global',
        failure_threshold: 'global',
        recovery_threshold: 'global',
        recipients: 'global',
      },
      ...overrides.effective,
    },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  }
}

export function makeGroup(overrides: Partial<Group> = {}): Group {
  return {
    id: 1,
    ungrouped: false,
    name: 'Head Office',
    description: '',
    color: '#0d6a73',
    sort_order: 0,
    check_interval_sec: null,
    timeout_sec: null,
    failure_threshold: null,
    recovery_threshold: null,
    notify: true,
    recipients: null,
    paused_until: null,
    paused: false,
    stats: {
      members: 1,
      up: 1,
      down: 0,
      unknown: 0,
      paused: 0,
      disabled: 0,
      checks_today: 100,
      uptime_today_pct: 100,
      ...overrides.stats,
    },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  }
}

export const defaultHealth: Health = {
  ok: true,
  version: 'test',
  uptime_sec: 120,
  db_ok: true,
  db_size_bytes: 77_824,
  schema_version: 1,
  devices: 1,
  up: 1,
  down: 0,
  unknown: 0,
  paused: 0,
  monitored: 1,
  open_incidents: 0,
  pending_alerts: 0,
  scheduler_lag_ms: 0,
  heartbeats: 1440,
  rollups: 90,
  maintenance: {
    at: new Date().toISOString(),
    rolled_up: 1,
    heartbeats_pruned: 5760,
    rollups_pruned: 0,
    vacuumed: false,
  },
  event_clients: 1,
  events_dropped: 0,
  data_dir: 'C:\\ProgramData\\LocalMonitor',
  log_dir: 'C:\\ProgramData\\LocalMonitor\\logs',
}

export const defaultSummary: Summary = {
  generated_at: new Date().toISOString(),
  counts: { devices: 1, up: 1, down: 0, unknown: 0, paused: 0, disabled: 0, groups: 1 },
  monitored: 1,
  groups: [makeGroup()],
  worst_offenders: [],
  open_incidents: [],
}

export const defaultSettings: Settings = {
  smtp: {
    host: 'smtp.example.com',
    port: 587,
    security: 'starttls',
    username: 'monitor',
    from: 'monitor@example.com',
    has_password: true,
    password: '***',
  },
  alerts: { recipients: 'ops@example.com', reminder_sec: 0, collapse_sec: 15, max_per_hour: 20 },
  defaults: {
    check_interval_sec: 30,
    timeout_sec: 3,
    failure_threshold: 3,
    recovery_threshold: 1,
  },
  retention: { raw_days: 14, rollup_days: 400 },
}

/**
 * The default handlers: one healthy device in one group.
 *
 * Tests override what they care about with server.use(...).
 */
export const handlers = [
  http.get('/api/health', () => HttpResponse.json(defaultHealth)),
  http.get('/api/summary', () => HttpResponse.json(defaultSummary)),
  http.get('/api/devices', () => HttpResponse.json({ devices: [makeDevice()] })),
  http.get('/api/groups', () => HttpResponse.json({ groups: [makeGroup()] })),
  http.get('/api/settings', () => HttpResponse.json(defaultSettings)),
  http.get('/api/groups/:id/uptime', () => HttpResponse.json({ uptime: [] })),
  http.get('/api/devices/:id/incidents', () => HttpResponse.json({ incidents: [] })),
  // The stream: an empty body, so the app's reader completes rather than
  // hanging a test open.
  http.get('/api/events', () => new HttpResponse(null, { status: 200 })),
]

export const server = setupServer(...handlers)
