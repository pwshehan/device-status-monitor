/**
 * The wire types of the local API, mirroring internal/api/dto.go.
 *
 * Two conventions carried over from the service:
 *
 *  - Record timestamps are RFC3339 UTC strings; series timestamps are unix
 *    seconds, because that is what uPlot wants.
 *  - A nullable probe setting is `null` when the row inherits. The resolved
 *    value and where it came from live under `effective`, which is what lets a
 *    form show "30 (from Warehouse)" as placeholder text.
 */

export type Status = 'UP' | 'DOWN' | 'UNKNOWN'

/** Where a resolved value came from: "device", "group:Warehouse" or "global". */
export type SourceMap = Record<string, string>

export interface Effective {
  check_interval_sec: number
  timeout_sec: number
  failure_threshold: number
  recovery_threshold: number
  notify: boolean
  recipients: string[]
  paused_until: string | null
  paused: boolean
  source: SourceMap
}

export interface Device {
  id: number
  group_id: number | null
  group_name: string
  name: string
  ip_address: string
  port: number

  check_interval_sec: number | null
  timeout_sec: number | null
  failure_threshold: number | null
  recovery_threshold: number | null
  enabled: boolean
  notify: boolean
  paused_until: string | null
  tags: string[]

  status: Status
  consecutive_failures: number
  consecutive_successes: number
  first_failure_at: string | null
  last_check_at: string | null
  last_latency_ms: number | null
  last_error: string
  last_status_change_at: string | null

  effective: Effective

  /**
   * The last checks, oldest first, one character each: "U" up, "D" down.
   * Compact because it ships for every device on every refresh.
   */
  recent_checks: string

  created_at: string
  updated_at: string
}

export interface GroupStats {
  members: number
  up: number
  down: number
  unknown: number
  paused: number
  disabled: number
  checks_today: number
  uptime_today_pct: number | null
}

export interface Group {
  /** null for the Ungrouped bucket, which is a section but not a row. */
  id: number | null
  ungrouped: boolean
  name: string
  description: string
  color: string
  sort_order: number

  check_interval_sec: number | null
  timeout_sec: number | null
  failure_threshold: number | null
  recovery_threshold: number | null
  notify: boolean
  recipients: string | null
  paused_until: string | null
  paused: boolean

  stats?: GroupStats

  created_at: string | null
  updated_at: string | null
}

export interface Incident {
  id: number
  device_id: number
  device_name?: string
  group_id?: number | null
  group_name?: string
  started_at: string
  detected_at: string
  resolved_at: string | null
  duration_sec: number | null
  ongoing: boolean
  cause: string
  alert_sent: boolean
  recovery_sent: boolean
}

/** One bucket of the decimated latency series. `t` is unix seconds. */
export interface Sample {
  t: number
  avg_latency_ms: number | null
  max_latency_ms: number
  checks: number
  downs: number
}

export type DaySource = 'rollup' | 'raw' | 'mixed'

export interface DayUptime {
  day: string
  checks_total: number
  checks_up: number
  uptime_pct: number
  avg_latency_ms: number | null
  p95_latency_ms: number | null
  downtime_sec: number
  source: DaySource
}

export interface GroupDayUptime {
  day: string
  devices: number
  checks_total: number
  checks_up: number
  uptime_pct: number
  worst_device_pct: number | null
  worst_device_name: string
  downtime_sec: number
  source: DaySource
}

export interface Offender {
  device_id: number
  name: string
  group_name: string
  status: Status
  checks: number
  failures: number
  uptime_pct: number
  last_error: string
}

export interface Counts {
  devices: number
  up: number
  down: number
  unknown: number
  paused: number
  disabled: number
  groups: number
}

export interface Summary {
  generated_at: string
  counts: Counts
  monitored: number
  groups: Group[]
  worst_offenders: Offender[]
  open_incidents: Incident[]
}

export interface Health {
  ok: boolean
  version: string
  uptime_sec: number
  db_ok: boolean
  db_error?: string
  db_size_bytes: number
  schema_version: number
  devices: number
  up: number
  down: number
  unknown: number
  paused: number
  monitored: number
  open_incidents: number
  pending_alerts: number
  scheduler_lag_ms: number
  heartbeats: number
  rollups: number
  /** Null until the janitor's first pass, which happens at startup. */
  maintenance: {
    at: string
    rolled_up: number
    heartbeats_pruned: number
    rollups_pruned: number
    vacuumed: boolean
    error?: string
  } | null
  event_clients: number
  events_dropped: number
  data_dir: string
  log_dir: string
}

export interface Settings {
  smtp: {
    host: string
    port: number
    security: 'starttls' | 'tls' | 'none'
    username: string
    from: string
    has_password: boolean
    /** Always "***" or "" — the real password never leaves the service. */
    password: string
  }
  alerts: {
    recipients: string
    reminder_sec: number
    /** How long an alert waits for company before going out. 0 = off. */
    collapse_sec: number
    /** Ceiling on outbound mail per hour. 0 = no cap. */
    max_per_hour: number
  }
  defaults: {
    check_interval_sec: number
    timeout_sec: number
    failure_threshold: number
    recovery_threshold: number
  }
  retention: {
    raw_days: number
    rollup_days: number
  }
}

/** The error envelope every endpoint uses on failure. */
export interface ApiErrorBody {
  error: {
    code: string
    message: string
    field?: string
  }
}

// --- request bodies ----------------------------------------------------------
//
// `undefined` means "leave this alone" and `null` means "clear the override
// back to inherited". JSON.stringify drops undefined keys, which is exactly
// the distinction the service's Opt[T] reads on the other end.

export interface DeviceWrite {
  group_id?: number | null
  name?: string
  ip_address?: string
  port?: number
  check_interval_sec?: number | null
  timeout_sec?: number | null
  failure_threshold?: number | null
  recovery_threshold?: number | null
  enabled?: boolean
  notify?: boolean
  tags?: string[]
}

export interface GroupWrite {
  name?: string
  description?: string
  color?: string
  sort_order?: number
  check_interval_sec?: number | null
  timeout_sec?: number | null
  failure_threshold?: number | null
  recovery_threshold?: number | null
  notify?: boolean
  recipients?: string | null
}

export type BulkOp = 'move' | 'pause' | 'resume' | 'delete'

export interface BulkRequest {
  ids: number[]
  op: BulkOp
  group_id?: number | null
  minutes?: number | null
  until?: string | null
}

export interface PauseRequest {
  minutes?: number | null
  until?: string | null
}

export interface CheckResult {
  device_id: number
  ok: boolean
  status: Status
  latency_ms: number
  class: string
  error: string
  checked_at: string
}
