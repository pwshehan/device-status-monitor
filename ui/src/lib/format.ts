import type { Device, Effective, Status } from '../api/types'

/** The badge a row shows, which is not quite the stored status. */
export type Badge = 'UP' | 'DOWN' | 'TIMEOUT' | 'PAUSED' | 'DISABLED' | 'UNKNOWN'

/**
 * Chooses the badge for a device.
 *
 * Paused and disabled win over the stored status: a device nobody is probing
 * is not "UP", it is not being watched, and showing a stale green badge during
 * a maintenance window is how people come to distrust a dashboard.
 *
 * TIMEOUT is a DOWN device whose last failure was silence rather than a
 * refusal — worth separating, because an unplugged cable and a stopped service
 * are different jobs.
 */
export function badgeOf(device: Device): Badge {
  if (!device.enabled) return 'DISABLED'
  if (device.effective.paused) return 'PAUSED'
  if (device.status === 'DOWN') {
    return device.last_error.startsWith('TIMEOUT') ? 'TIMEOUT' : 'DOWN'
  }
  return device.status
}

export function addr(device: Pick<Device, 'ip_address' | 'port'>): string {
  return `${device.ip_address}:${device.port}`
}

/** "2 m 14 s", "3 h 02 m", "6 d 4 h" — short enough for a table cell. */
export function duration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return '—'
  const s = Math.max(0, Math.floor(seconds))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
  if (s < 86_400) {
    return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
  }
  return `${Math.floor(s / 86_400)}d ${Math.floor((s % 86_400) / 3600)}h`
}

/** "12s ago", "4m ago", "—" when never. */
export function since(timestamp: string | null): string {
  if (timestamp === null) return '—'
  const then = Date.parse(timestamp)
  if (Number.isNaN(then)) return '—'
  const secs = Math.round((Date.now() - then) / 1000)
  if (secs < 0) return 'just now'
  if (secs < 5) return 'just now'
  return `${duration(secs)} ago`
}

export function localTime(timestamp: string | null): string {
  if (timestamp === null) return '—'
  const d = new Date(timestamp)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
}

export function latency(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return '—'
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

export function uptimePct(pct: number | null | undefined): string {
  if (pct === null || pct === undefined) return '—'
  // Three decimals matter at the top of the range: 99.9% and 100% are eight
  // hours of downtime a year apart.
  if (pct >= 99.9 && pct < 100) return `${pct.toFixed(3)}%`
  if (pct === 100) return '100%'
  return `${pct.toFixed(1)}%`
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}

/**
 * Renders an inherited value the way the form's placeholder should read:
 * "30 (from Warehouse)" or "30 (global default)".
 *
 * This is the single most likely support question about inheritance — "why is
 * this device checking every 60 seconds?" — so the answer is on screen rather
 * than in a doc.
 */
export function inheritedFrom(effective: Effective, key: SourceKey): string {
  const source = effective.source[key] ?? 'global'
  const value = resolvedValue(effective, key)
  if (source === 'device') return String(value)
  if (source.startsWith('group:')) return `${value} (from ${source.slice(6)})`
  return `${value} (global default)`
}

export type SourceKey = 'interval' | 'timeout' | 'failure_threshold' | 'recovery_threshold'

export function resolvedValue(effective: Effective, key: SourceKey): number {
  switch (key) {
    case 'interval':
      return effective.check_interval_sec
    case 'timeout':
      return effective.timeout_sec
    case 'failure_threshold':
      return effective.failure_threshold
    case 'recovery_threshold':
      return effective.recovery_threshold
  }
}

/** Sort order for triage: anything broken first, then by name. */
export function compareForTriage(a: Device, b: Device): number {
  const rank: Record<Badge, number> = {
    DOWN: 0,
    TIMEOUT: 1,
    UNKNOWN: 2,
    UP: 3,
    PAUSED: 4,
    DISABLED: 5,
  }
  const byStatus = rank[badgeOf(a)] - rank[badgeOf(b)]
  return byStatus !== 0 ? byStatus : a.name.localeCompare(b.name)
}

/** Colour for an uptime percentage: the thresholds from §8. */
export function uptimeTone(pct: number | null): 'none' | 'good' | 'warn' | 'bad' {
  if (pct === null) return 'none'
  if (pct >= 99.9) return 'good'
  if (pct >= 95) return 'warn'
  return 'bad'
}

export function statusLabel(status: Status): string {
  return status.charAt(0) + status.slice(1).toLowerCase()
}
