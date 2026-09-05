import type { Status } from '../api/types'

interface Props {
  /** Check outcomes, oldest first. */
  checks: Status[]
  /** How many slots to draw; missing ones render as "no data". */
  slots?: number
  className?: string
}

/**
 * A row of check outcomes: green for a device that answered, red for one that
 * did not.
 *
 * This replaced a latency sparkline, which was the wrong question for a table
 * row. The latency of the last check is already in the column beside this one
 * and the full curve is on the device's page; what a row needs to answer at a
 * glance is "has this been steady?", and a run of red among green says that in
 * a way a line does not.
 *
 * Older checks are drawn dimmer, so the eye lands on what happened recently
 * without having to work out which end is now.
 */
export function StatusStrip({ checks, slots = 40, className = '' }: Props) {
  // Right-aligned: the most recent check is always the rightmost block,
  // whether there are three of them or forty.
  const padding = Math.max(0, slots - checks.length)
  const cells: (Status | null)[] = [
    ...Array<null>(padding).fill(null),
    ...checks.slice(-slots),
  ]

  const summary =
    checks.length === 0
      ? 'No checks recorded yet'
      : `${checks.filter((c) => c === 'UP').length} of the last ${checks.length} checks succeeded`

  return (
    <div
      className={`flex items-center gap-[1px] ${className}`}
      role="img"
      aria-label={summary}
      title={summary}
    >
      {cells.map((status, i) => {
        // Only the most recent quarter is at full strength.
        const recent = i >= cells.length - Math.ceil(slots / 4)
        return (
          <span
            key={i}
            className={[
              'h-4 w-[3px] shrink-0 rounded-[1px]',
              status === null
                ? 'bg-slate-200 dark:bg-slate-700'
                : status === 'UP'
                  ? recent
                    ? 'bg-up-500'
                    : 'bg-up-500/55'
                  : recent
                    ? 'bg-down-500'
                    : 'bg-down-500/60',
            ].join(' ')}
          />
        )
      })}
    </div>
  )
}
