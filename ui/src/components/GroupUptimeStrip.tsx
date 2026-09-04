import { useGroupUptime } from '../hooks/queries'
import { UptimeStrip } from './UptimeStrip'

interface Props {
  groupId: number
  days?: number
  blockHeight?: string
  className?: string
}

/**
 * A group's derived availability strip.
 *
 * Fetches its own data so a section header can show it without the dashboard
 * having to gather uptime for every group up front. There is no group rollup
 * table — group history is derived from current members on each request (§3.3)
 * — so this is a real query, but 90 days across a handful of members is a few
 * hundred rows and it is cached until an event invalidates it.
 */
export function GroupUptimeStrip({
  groupId,
  days = 90,
  blockHeight = 'h-3',
  className = '',
}: Props) {
  const uptime = useGroupUptime(groupId, days)

  if (uptime.data === undefined) {
    return <div className={`${blockHeight} ${className}`} aria-hidden="true" />
  }

  return (
    <UptimeStrip
      span={days}
      blockHeight={blockHeight}
      className={className}
      days={uptime.data.map((d) => ({
        day: d.day,
        uptime_pct: d.uptime_pct,
        downtime_sec: d.downtime_sec,
        worst_name: d.worst_device_name,
        worst_pct: d.worst_device_pct,
        source: d.source,
      }))}
    />
  )
}
