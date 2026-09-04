import type {
  ApiErrorBody,
  BulkRequest,
  CheckResult,
  DayUptime,
  Device,
  DeviceWrite,
  Group,
  GroupDayUptime,
  GroupWrite,
  Health,
  Incident,
  PauseRequest,
  Sample,
  Settings,
  Status,
  Summary,
} from './types'

/**
 * Where the API lives.
 *
 * In the browser dev loop this is a relative path and Vite proxies it, which
 * keeps everything same-origin and lets the proxy attach the bearer token
 * from ./.dev-data/api.token. The Tauri shell instead points this at the
 * service directly and supplies the token itself.
 */
const BASE = import.meta.env.VITE_API_BASE ?? '/api'

/** How the Tauri shell hands the token to the page it loads. */
declare global {
  interface Window {
    __MONITOR_TOKEN__?: string
  }
}

const TOKEN_KEY = 'monitor.api-token'

/**
 * The token, if this page has one.
 *
 * Three sources in order: one injected by the native shell, one the user
 * pasted (kept in localStorage), or none at all — which is the normal case
 * behind the dev proxy, since the proxy adds the header server-side.
 */
export function apiToken(): string | null {
  if (typeof window === 'undefined') return null
  if (window.__MONITOR_TOKEN__) return window.__MONITOR_TOKEN__
  try {
    return window.localStorage.getItem(TOKEN_KEY)
  } catch {
    // Private mode, or storage disabled. Not fatal: the proxy may be supplying
    // the header anyway.
    return null
  }
}

export function setApiToken(token: string | null): void {
  try {
    if (token === null || token.trim() === '') window.localStorage.removeItem(TOKEN_KEY)
    else window.localStorage.setItem(TOKEN_KEY, token.trim())
  } catch {
    /* ignore */
  }
}

/**
 * A failed request, carrying the service's error envelope.
 *
 * `field` is the whole reason the envelope has that shape: it lets a form mark
 * the offending control instead of showing a toast that says "422".
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly field: string | undefined

  constructor(status: number, code: string, message: string, field?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.field = field
  }

  /** True when the service is reachable but its database is not. */
  get isUnavailable(): boolean {
    return this.status === 503
  }

  /** True when the token is missing or stale, as opposed to the service being down. */
  get isUnauthorized(): boolean {
    return this.status === 401
  }
}

/** Raised when the service could not be reached at all. */
export class ServiceDownError extends Error {
  constructor(cause: unknown) {
    super('The monitoring service is not responding')
    this.name = 'ServiceDownError'
    this.cause = cause
  }
}

interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
}

export function authHeaders(): HeadersInit {
  const token = apiToken()
  return token === null ? {} : { Authorization: `Bearer ${token}` }
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal } = options

  const headers: Record<string, string> = { ...(authHeaders() as Record<string, string>) }
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  let res: Response
  try {
    res = await fetch(`${BASE}${path}`, {
      method,
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      ...(signal ? { signal } : {}),
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    // A network-level failure here means the service is not listening, which
    // is a different banner from any HTTP status.
    throw new ServiceDownError(cause)
  }

  if (res.status === 204) return undefined as T

  const text = await res.text()
  let parsed: unknown
  if (text !== '') {
    try {
      parsed = JSON.parse(text)
    } catch {
      if (!res.ok) throw new ApiError(res.status, 'unknown', text.slice(0, 200))
      throw new ApiError(res.status, 'unknown', 'the service returned a malformed response')
    }
  }

  if (!res.ok) {
    const envelope = parsed as ApiErrorBody | undefined
    const detail = envelope?.error
    throw new ApiError(
      res.status,
      detail?.code ?? 'unknown',
      detail?.message ?? `request failed with status ${res.status}`,
      detail?.field,
    )
  }
  return parsed as T
}

// --- endpoints ---------------------------------------------------------------

export interface DeviceFilter {
  groupId?: number | 'none'
  status?: Status
  tag?: string
  enabledOnly?: boolean
}

function deviceQuery(filter: DeviceFilter): string {
  const params = new URLSearchParams()
  if (filter.groupId !== undefined) params.set('group_id', String(filter.groupId))
  if (filter.status !== undefined) params.set('status', filter.status)
  if (filter.tag !== undefined && filter.tag !== '') params.set('tag', filter.tag)
  if (filter.enabledOnly === true) params.set('enabled', 'true')
  const q = params.toString()
  return q === '' ? '' : `?${q}`
}

export const api = {
  health: () => request<Health>('/health'),
  summary: () => request<Summary>('/summary'),

  devices: (filter: DeviceFilter = {}) =>
    request<{ devices: Device[] }>(`/devices${deviceQuery(filter)}`).then((r) => r.devices),

  device: (id: number) =>
    request<{ device: Device; open_incident: Incident | null }>(`/devices/${id}`),

  createDevice: (body: DeviceWrite) =>
    request<{ device: Device }>('/devices', { method: 'POST', body }).then((r) => r.device),

  updateDevice: (id: number, body: DeviceWrite) =>
    request<{ device: Device }>(`/devices/${id}`, { method: 'PATCH', body }).then((r) => r.device),

  deleteDevice: (id: number) => request<{ deleted: number }>(`/devices/${id}`, { method: 'DELETE' }),

  pauseDevice: (id: number, body: PauseRequest) =>
    request<{ device_id: number; paused_until: string | null }>(`/devices/${id}/pause`, {
      method: 'POST',
      body,
    }),

  checkDevice: (id: number) => request<CheckResult>(`/devices/${id}/check`, { method: 'POST' }),

  heartbeats: (id: number, fromUnix: number, toUnix: number, maxPoints = 1000) =>
    request<{ samples: Sample[] }>(
      `/devices/${id}/heartbeats?from=${fromUnix}&to=${toUnix}&max_points=${maxPoints}`,
    ).then((r) => r.samples),

  deviceUptime: (id: number, days = 90) =>
    request<{ uptime: DayUptime[] }>(`/devices/${id}/uptime?days=${days}`).then((r) => r.uptime),

  deviceIncidents: (id: number, limit = 50) =>
    request<{ incidents: Incident[] }>(`/devices/${id}/incidents?limit=${limit}`).then(
      (r) => r.incidents,
    ),

  bulk: (body: BulkRequest) =>
    request<{ op: string; affected: number; ids: number[] }>('/devices/bulk', {
      method: 'POST',
      body,
    }),

  groups: () => request<{ groups: Group[] }>('/groups').then((r) => r.groups),

  group: (id: number) => request<{ group: Group }>(`/groups/${id}`).then((r) => r.group),

  createGroup: (body: GroupWrite) =>
    request<{ group: Group }>('/groups', { method: 'POST', body }).then((r) => r.group),

  updateGroup: (id: number, body: GroupWrite) =>
    request<{ group: Group }>(`/groups/${id}`, { method: 'PATCH', body }).then((r) => r.group),

  deleteGroup: (id: number) =>
    request<{ deleted: number; devices_ungrouped: number }>(`/groups/${id}`, { method: 'DELETE' }),

  reorderGroups: (ids: number[]) =>
    request<{ reordered: number }>('/groups/reorder', { method: 'POST', body: { ids } }),

  pauseGroup: (id: number, body: PauseRequest) =>
    request<{ group_id: number; paused_until: string | null }>(`/groups/${id}/pause`, {
      method: 'POST',
      body,
    }),

  groupUptime: (id: number, days = 90) =>
    request<{ uptime: GroupDayUptime[] }>(`/groups/${id}/uptime?days=${days}`).then(
      (r) => r.uptime,
    ),

  settings: () => request<Settings>('/settings'),

  saveSettings: (body: unknown) => request<Settings>('/settings', { method: 'PUT', body }),

  testEmail: (to?: string[]) =>
    request<{ sent: boolean; to: string[] }>('/settings/test-email', {
      method: 'POST',
      ...(to === undefined ? {} : { body: { to } }),
    }),
}

/** The URL of the SSE stream, for the events hook. */
export function eventsUrl(types?: string[]): string {
  const q = types === undefined || types.length === 0 ? '' : `?types=${types.join(',')}`
  return `${BASE}/events${q}`
}
