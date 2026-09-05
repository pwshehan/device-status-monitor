import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { badgeOf, compareForTriage, duration, uptimePct, uptimeTone } from '../lib/format'
import { makeDevice } from '../test/server'
import { renderWithProviders } from '../test/render'
import { StatusStrip } from './StatusStrip'
import { StatusBadge } from './StatusBadge'
import { UptimeStrip } from './UptimeStrip'

describe('badgeOf', () => {
  it('shows PAUSED over the stored status, whichever it is', () => {
    // A device nobody is probing is not "UP". Showing a stale green badge
    // during a maintenance window is how people stop trusting a dashboard.
    const device = makeDevice({
      status: 'UP',
      effective: { ...makeDevice().effective, paused: true },
    })
    expect(badgeOf(device)).toBe('PAUSED')
  })

  it('shows DISABLED over a pause', () => {
    const device = makeDevice({
      enabled: false,
      effective: { ...makeDevice().effective, paused: true },
    })
    expect(badgeOf(device)).toBe('DISABLED')
  })

  it('separates a silent host from one that refused', () => {
    const timeout = makeDevice({ status: 'DOWN', last_error: 'TIMEOUT: i/o timeout' })
    const refused = makeDevice({ status: 'DOWN', last_error: 'REFUSED: connection refused' })
    expect(badgeOf(timeout)).toBe('TIMEOUT')
    expect(badgeOf(refused)).toBe('DOWN')
  })

  it('leaves an unchecked device UNKNOWN', () => {
    expect(badgeOf(makeDevice({ status: 'UNKNOWN' }))).toBe('UNKNOWN')
  })
})

describe('compareForTriage', () => {
  it('puts what is broken first and paused things last', () => {
    const devices = [
      makeDevice({ id: 1, name: 'zulu', status: 'UP' }),
      makeDevice({ id: 2, name: 'alpha', status: 'DOWN', last_error: 'REFUSED: no' }),
      makeDevice({
        id: 3,
        name: 'bravo',
        effective: { ...makeDevice().effective, paused: true },
      }),
      makeDevice({ id: 4, name: 'charlie', status: 'DOWN', last_error: 'TIMEOUT: silence' }),
      makeDevice({ id: 5, name: 'delta', status: 'UNKNOWN' }),
    ]
    expect([...devices].sort(compareForTriage).map((d) => d.name)).toEqual([
      'alpha', // DOWN
      'charlie', // TIMEOUT
      'delta', // UNKNOWN
      'zulu', // UP
      'bravo', // PAUSED
    ])
  })
})

describe('StatusBadge', () => {
  it('labels each state for a screen reader as well as by colour', () => {
    renderWithProviders(<StatusBadge badge="TIMEOUT" />)
    const badge = screen.getByText('TIMEOUT')
    expect(badge).toBeInTheDocument()
    expect(badge).toHaveAttribute('title', expect.stringContaining('timeout'))
  })
})

describe('uptime thresholds', () => {
  it('uses the §8 bands', () => {
    expect(uptimeTone(100)).toBe('good')
    expect(uptimeTone(99.9)).toBe('good')
    expect(uptimeTone(99.89)).toBe('warn')
    expect(uptimeTone(95)).toBe('warn')
    expect(uptimeTone(94.99)).toBe('bad')
    // No data is its own case: it must not render as a perfect day.
    expect(uptimeTone(null)).toBe('none')
  })

  it('keeps three decimals where they matter', () => {
    // 99.9% and 100% are eight hours of downtime a year apart.
    expect(uptimePct(99.9123)).toBe('99.912%')
    expect(uptimePct(100)).toBe('100%')
    expect(uptimePct(42.4242)).toBe('42.4%')
    expect(uptimePct(null)).toBe('—')
  })
})

describe('UptimeStrip', () => {
  it('renders one block per day in the span, filling gaps with no-data', () => {
    const today = new Date()
    const iso = (d: Date) =>
      `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(
        d.getDate(),
      ).padStart(2, '0')}`

    const yesterday = new Date(today)
    yesterday.setDate(today.getDate() - 1)

    const { container } = renderWithProviders(
      <UptimeStrip
        span={7}
        days={[
          { day: iso(yesterday), uptime_pct: 100 },
          { day: iso(today), uptime_pct: 42, downtime_sec: 600 },
        ]}
      />,
    )

    const blocks = container.querySelectorAll('div[title]')
    expect(blocks).toHaveLength(7)

    // The strip always ends at today, whatever data exists.
    const last = blocks[blocks.length - 1]
    expect(last?.getAttribute('title')).toContain(iso(today))
    expect(last?.getAttribute('title')).toContain('42.0%')
    expect(last?.getAttribute('title')).toContain('10 min down')

    // And days with nothing recorded say so rather than looking healthy.
    expect(blocks[0]?.getAttribute('title')).toContain('no data')
  })

  it('names the worst member in a group tooltip', () => {
    const today = new Date()
    const iso = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(
      today.getDate(),
    ).padStart(2, '0')}`

    const { container } = renderWithProviders(
      <UptimeStrip
        span={1}
        days={[{ day: iso, uptime_pct: 95, worst_name: 'Floor 2 switch', worst_pct: 0 }]}
      />,
    )
    // A site of twenty devices with one dead all day reads 95% in aggregate,
    // so the tooltip has to say which one it was.
    expect(container.querySelector('div[title]')?.getAttribute('title')).toContain(
      'worst: Floor 2 switch',
    )
  })
})

describe('StatusStrip', () => {
  it('draws one block per check, most recent last', () => {
    const { container } = renderWithProviders(
      <StatusStrip checks={['UP', 'UP', 'DOWN', 'UP']} slots={4} />,
    )
    const blocks = container.querySelectorAll('span')
    expect(blocks).toHaveLength(4)

    // Red where the check failed, green either side of it.
    expect(blocks[2]?.className).toContain('bg-down-500')
    expect(blocks[3]?.className).toContain('bg-up-500')
  })

  it('right-aligns a short history so "now" is always the last block', () => {
    // A device checked twice should not read as thirty-eight failures.
    const { container } = renderWithProviders(<StatusStrip checks={['UP', 'DOWN']} slots={6} />)
    const blocks = container.querySelectorAll('span')

    expect(blocks).toHaveLength(6)
    expect(blocks[0]?.className).toContain('bg-slate-200')
    expect(blocks[4]?.className).toContain('bg-up-500')
    expect(blocks[5]?.className).toContain('bg-down-500')
  })

  it('says what it is showing rather than only colouring it', () => {
    renderWithProviders(<StatusStrip checks={['UP', 'UP', 'DOWN']} />)
    // Colour alone is not a label, and red/green is the worst pair to rely on.
    expect(screen.getByRole('img')).toHaveAccessibleName('2 of the last 3 checks succeeded')
  })

  it('is honest about having no history yet', () => {
    renderWithProviders(<StatusStrip checks={[]} />)
    expect(screen.getByRole('img')).toHaveAccessibleName('No checks recorded yet')
  })
})

describe('duration', () => {
  it('reads at a glance at every scale', () => {
    expect(duration(0)).toBe('0s')
    expect(duration(45)).toBe('45s')
    expect(duration(134)).toBe('2m 14s')
    expect(duration(3720)).toBe('1h 02m')
    expect(duration(100_000)).toBe('1d 3h')
    expect(duration(null)).toBe('—')
  })
})
