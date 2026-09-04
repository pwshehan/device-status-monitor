import type { Badge } from '../lib/format'

const styles: Record<Badge, string> = {
  // DOWN is the only filled badge, so it reads as the exception even in
  // greyscale or to a red/green colour-blind eye.
  DOWN: 'bg-down-500 text-white ring-down-600',
  TIMEOUT: 'bg-warn-500 text-slate-900 ring-warn-600',
  UP: 'bg-up-100 text-up-600 ring-up-500/40 dark:bg-up-600/20 dark:text-up-100',
  PAUSED: 'bg-slate-200 text-slate-600 ring-slate-400/40 dark:bg-slate-700 dark:text-slate-300',
  DISABLED: 'bg-slate-100 text-slate-400 ring-slate-300/40 dark:bg-slate-800 dark:text-slate-500',
  UNKNOWN: 'bg-slate-100 text-slate-500 ring-slate-300/40 dark:bg-slate-800 dark:text-slate-400',
}

const titles: Record<Badge, string> = {
  UP: 'Answering',
  DOWN: 'Refused or unreachable',
  TIMEOUT: 'Down: no answer within the timeout',
  PAUSED: 'Paused — not being probed',
  DISABLED: 'Disabled — not being probed',
  UNKNOWN: 'Not checked yet',
}

interface Props {
  badge: Badge
  /** Shows a pulse for the first few seconds after a transition. */
  fresh?: boolean
}

export function StatusBadge({ badge, fresh = false }: Props) {
  return (
    <span
      title={titles[badge]}
      className={[
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
        styles[badge],
        fresh ? 'animate-pulse' : '',
      ].join(' ')}
    >
      <span aria-hidden="true" className="text-[0.6rem] leading-none">
        {badge === 'UP' ? '●' : badge === 'PAUSED' || badge === 'DISABLED' ? '❙❙' : '▲'}
      </span>
      {badge}
    </span>
  )
}
