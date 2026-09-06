import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query'

import { api, type DeviceFilter } from '../api/client'
import type {
  BulkRequest,
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
  Summary,
  UpdateStatus,
} from '../api/types'

/** Query keys, in one place so the event stream can invalidate by prefix. */
export const keys = {
  health: ['health'] as const,
  summary: ['summary'] as const,
  devices: ['devices'] as const,
  deviceList: (filter: DeviceFilter) => ['devices', 'list', filter] as const,
  device: (id: number) => ['devices', id] as const,
  heartbeats: (id: number, hours: number) => ['devices', id, 'heartbeats', hours] as const,
  uptime: (id: number, days: number) => ['devices', id, 'uptime', days] as const,
  incidents: (id: number) => ['devices', id, 'incidents'] as const,
  groups: ['groups'] as const,
  group: (id: number) => ['groups', id] as const,
  groupUptime: (id: number, days: number) => ['groups', id, 'uptime', days] as const,
  settings: ['settings'] as const,
  update: ['update'] as const,
}

/**
 * Health is polled as well as pushed.
 *
 * Everything else relies on the event stream for freshness, but health is the
 * one thing that has to keep answering when the stream is *not* connected —
 * that is precisely the state it exists to detect.
 */
export function useHealth(): UseQueryResult<Health> {
  return useQuery({
    queryKey: keys.health,
    queryFn: api.health,
    refetchInterval: 10_000,
    retry: false,
    // A failure here is the service-down banner, so it must not be masked by
    // a stale cached value.
    gcTime: 0,
  })
}

export function useSummary(): UseQueryResult<Summary> {
  return useQuery({ queryKey: keys.summary, queryFn: api.summary })
}

export function useDevices(filter: DeviceFilter = {}): UseQueryResult<Device[]> {
  return useQuery({
    queryKey: keys.deviceList(filter),
    queryFn: () => api.devices(filter),
  })
}

export function useDevice(
  id: number,
): UseQueryResult<{ device: Device; open_incident: Incident | null }> {
  return useQuery({ queryKey: keys.device(id), queryFn: () => api.device(id) })
}

export function useHeartbeats(id: number, hours: number): UseQueryResult<Sample[]> {
  return useQuery({
    queryKey: keys.heartbeats(id, hours),
    queryFn: () => {
      const to = Math.floor(Date.now() / 1000)
      return api.heartbeats(id, to - hours * 3600, to, 1000)
    },
    // The window is relative to now, so a stale copy is a chart that has
    // quietly stopped moving.
    refetchInterval: 60_000,
  })
}

export function useDeviceUptime(id: number, days = 90): UseQueryResult<DayUptime[]> {
  return useQuery({ queryKey: keys.uptime(id, days), queryFn: () => api.deviceUptime(id, days) })
}

export function useIncidents(id: number, limit = 50): UseQueryResult<Incident[]> {
  return useQuery({
    queryKey: keys.incidents(id),
    queryFn: () => api.deviceIncidents(id, limit),
  })
}

export function useGroups(): UseQueryResult<Group[]> {
  return useQuery({ queryKey: keys.groups, queryFn: api.groups })
}

export function useGroup(id: number): UseQueryResult<Group> {
  return useQuery({ queryKey: keys.group(id), queryFn: () => api.group(id) })
}

export function useGroupUptime(id: number, days = 90): UseQueryResult<GroupDayUptime[]> {
  return useQuery({
    queryKey: keys.groupUptime(id, days),
    queryFn: () => api.groupUptime(id, days),
  })
}

export function useSettings(): UseQueryResult<Settings> {
  return useQuery({ queryKey: keys.settings, queryFn: api.settings })
}

/**
 * Update state is polled slowly.
 *
 * It changes on the service's own schedule — a check every six hours, or a
 * download that takes a while — so there is nothing to push and nothing to
 * gain from asking often. The exception is a download in progress, where the
 * byte count is the only sign that anything is happening.
 */
export function useUpdate(): UseQueryResult<UpdateStatus> {
  return useQuery({
    queryKey: keys.update,
    queryFn: api.update,
    refetchInterval: (query) => (query.state.data?.state === 'downloading' ? 1_000 : 60_000),
    // A service that has gone away is the health banner's job to report, not
    // this section's, and retrying would only delay that banner.
    retry: false,
  })
}

// --- mutations ---------------------------------------------------------------
//
// Every write invalidates rather than patching the cache by hand. The service
// is the authority on resolved values: a device edit can change what its
// `effective` block says, and a group edit changes it for every member, so
// guessing the new state locally would be wrong more often than not.

function useInvalidateAll(): () => void {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: keys.devices })
    void queryClient.invalidateQueries({ queryKey: keys.groups })
    void queryClient.invalidateQueries({ queryKey: keys.summary })
    void queryClient.invalidateQueries({ queryKey: keys.health })
  }
}

export function useCreateDevice(): UseMutationResult<Device, Error, DeviceWrite> {
  const invalidate = useInvalidateAll()
  return useMutation({ mutationFn: api.createDevice, onSuccess: invalidate })
}

export function useUpdateDevice(): UseMutationResult<
  Device,
  Error,
  { id: number; body: DeviceWrite }
> {
  const invalidate = useInvalidateAll()
  return useMutation({
    mutationFn: ({ id, body }) => api.updateDevice(id, body),
    onSuccess: invalidate,
  })
}

export function useDeleteDevice(): UseMutationResult<unknown, Error, number> {
  const invalidate = useInvalidateAll()
  return useMutation({ mutationFn: api.deleteDevice, onSuccess: invalidate })
}

export function usePauseDevice(): UseMutationResult<
  unknown,
  Error,
  { id: number; body: PauseRequest }
> {
  const invalidate = useInvalidateAll()
  return useMutation({
    mutationFn: ({ id, body }) => api.pauseDevice(id, body),
    onSuccess: invalidate,
  })
}

export function useCheckDevice(): UseMutationResult<
  Awaited<ReturnType<typeof api.checkDevice>>,
  Error,
  number
> {
  // No invalidation: a manual check deliberately does not touch stored state,
  // so nothing in the cache has changed.
  return useMutation({ mutationFn: api.checkDevice })
}

export function useBulk(): UseMutationResult<unknown, Error, BulkRequest> {
  const invalidate = useInvalidateAll()
  return useMutation({ mutationFn: api.bulk, onSuccess: invalidate })
}

export function useCreateGroup(): UseMutationResult<Group, Error, GroupWrite> {
  const invalidate = useInvalidateAll()
  return useMutation({ mutationFn: api.createGroup, onSuccess: invalidate })
}

export function useUpdateGroup(): UseMutationResult<
  Group,
  Error,
  { id: number; body: GroupWrite }
> {
  const invalidate = useInvalidateAll()
  return useMutation({
    mutationFn: ({ id, body }) => api.updateGroup(id, body),
    onSuccess: invalidate,
  })
}

export function useDeleteGroup(): UseMutationResult<
  { deleted: number; devices_ungrouped: number },
  Error,
  number
> {
  const invalidate = useInvalidateAll()
  return useMutation({ mutationFn: api.deleteGroup, onSuccess: invalidate })
}

export function usePauseGroup(): UseMutationResult<
  unknown,
  Error,
  { id: number; body: PauseRequest }
> {
  const invalidate = useInvalidateAll()
  return useMutation({
    mutationFn: ({ id, body }) => api.pauseGroup(id, body),
    onSuccess: invalidate,
  })
}

export function useReorderGroups(): UseMutationResult<unknown, Error, number[]> {
  const invalidate = useInvalidateAll()
  return useMutation({ mutationFn: api.reorderGroups, onSuccess: invalidate })
}

export function useSaveSettings(): UseMutationResult<Settings, Error, unknown> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: api.saveSettings,
    onSuccess: (settings) => {
      queryClient.setQueryData(keys.settings, settings)
      // The default.* tier is the bottom of the inheritance chain, so every
      // device's resolved values may have moved.
      void queryClient.invalidateQueries({ queryKey: keys.devices })
    },
  })
}

/**
 * The three update actions. Each answers with the new state, which is written
 * straight into the cache: the service is the authority on what it has staged,
 * and guessing locally is how a button ends up disagreeing with the text above
 * it.
 */
function useUpdateAction(
  fn: () => Promise<UpdateStatus>,
): UseMutationResult<UpdateStatus, Error, void> {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: (status) => queryClient.setQueryData(keys.update, status),
    // A failure leaves the cache alone and surfaces through the mutation's own
    // error, so the section can say what went wrong without losing what it
    // already knew.
  })
}

export function useCheckUpdate(): UseMutationResult<UpdateStatus, Error, void> {
  return useUpdateAction(api.checkUpdate)
}

export function useDownloadUpdate(): UseMutationResult<UpdateStatus, Error, void> {
  return useUpdateAction(api.downloadUpdate)
}

export function useInstallUpdate(): UseMutationResult<UpdateStatus, Error, void> {
  return useUpdateAction(api.installUpdate)
}

export function useTestEmail(): UseMutationResult<
  { sent: boolean; to: string[] },
  Error,
  string[] | undefined
> {
  return useMutation({ mutationFn: (to) => api.testEmail(to) })
}
