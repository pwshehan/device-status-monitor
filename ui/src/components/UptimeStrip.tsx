import { uptimePct, uptimeTone } from '../lib/format'

export interface StripDay {
  day: string
  uptime_pct: number | null
  downtime_sec?: number
  /** Named in the tooltip for a group strip: the member that had the worst day. */
  worst_name?: string
  worst_pct?: number | null
  source?: string
}

interface Props {
  days: StripDay[]
  /** How many day slots to render; missing days show as no-data. */
  span?: number
  /** Tailwind height class for one block. */
  blockHeight?: string
  className?: string
}

const tones = {
  good: 'bg-up-500',
  warn: 'bg-warn-500',
  bad: 'bg-down-500',
  // No data is hatched rather than grey, so "nothing happened" cannot be
  // mistaken for "a perfect day" or for an outage.
  none: 'bg-[repeating-linear-gradient(45deg,var(--color-slate-200),var(--color-slate-200)_2px,transparent_2px,transparent_4px)] dark:bg-[repeating-linear-gradient(45deg,var(--color-slate-700),var(--color-slate-700)_2px,transparent_2px,transparent_4px)]',
}

/**
 * The 90-day availability strip: a CSS grid of coloured divs, not a chart.
 *
 * Ninety blocks is markup — a charting library for this would be a dependency
 * with a canvas, a tooltip layer and a resize observer to draw what a grid
 * does for free.
 */
export function UptimeStrip({ days, span = 90, blockHeight = 'h-8', className = '' }: Props) {
  const byDay = new Map(days.map((d) => [d.day, d]))
  const slots: StripDay[] = []

  // Walk back from today so the strip always ends at "now", whether or not the
  // service has data for every day.
  const today = new Date()
  for (let i = span - 1; i >= 0; i--) {
    const d = new Date(today)
    d.setDate(today.getDate() - i)
    const key = localDay(d)
    slots.push(byDay.get(key) ?? { day: key, uptime_pct: null })
  }

  return (
    <div
      className={`grid gap-[2px] ${className}`}
      style={{ gridTemplateColumns: `repeat(${span}, minmax(0, 1fr))` }}
      role="img"
      aria-label={`Availability for the last ${span} days`}
    >
      {slots.map((slot) => (
        <div
          key={slot.day}
          title={tooltip(slot)}
          className={`${blockHeight} rounded-[2px] ${tones[uptimeTone(slot.uptime_pct)]}`}
        />
      ))}
    </div>
  )
}

function tooltip(slot: StripDay): string {
  if (slot.uptime_pct === null) return `${slot.day}\nno data`
  const lines = [`${slot.day}`, `${uptimePct(slot.uptime_pct)} available`]
  if (slot.downtime_sec !== undefined && slot.downtime_sec > 0) {
    lines.push(`${Math.round(slot.downtime_sec / 60)} min down`)
  }
  if (slot.worst_name !== undefined && slot.worst_name !== '') {
    // A site of twenty devices where one was dead all day still reads 95%
    // aggregate, so the strip is coloured by the aggregate and the tooltip
    // names the member that actually had the bad day.
    lines.push(`worst: ${slot.worst_name} at ${uptimePct(slot.worst_pct ?? null)}`)
  }
  if (slot.source === 'raw') lines.push('(from raw checks, not yet rolled up)')
  return lines.join('\n')
}

/** Local calendar date, matching the service's rollup key. */
function localDay(d: Date): string {
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${month}-${day}`
}
