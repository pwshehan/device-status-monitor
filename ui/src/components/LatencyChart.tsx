import { useEffect, useMemo, useRef } from 'react'
import uPlot from 'uplot'

import type { Incident, Sample } from '../api/types'

interface Props {
  samples: Sample[]
  /** Outages, drawn as shaded spans behind the line. */
  incidents: Incident[]
  height?: number
}

/**
 * The 24-hour latency line, with DOWN spans shaded.
 *
 * uPlot rather than a general charting library: 24 hours at a 30 s interval is
 * 2 880 points per device and uPlot draws that in about a millisecond from
 * 45 kB of code. It is imperative, so this component owns a ref and syncs
 * data into it rather than re-creating the plot on every render.
 */
export function LatencyChart({ samples, incidents, height = 240 }: Props) {
  const holder = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)

  const data = useMemo<uPlot.AlignedData>(() => {
    const t = new Float64Array(samples.length)
    const avg = new Float64Array(samples.length)
    const max = new Float64Array(samples.length)
    samples.forEach((s, i) => {
      t[i] = s.t
      // A bucket with no successful check has no average latency; NaN is how
      // uPlot draws a gap, which is more honest than joining across an outage.
      avg[i] = s.avg_latency_ms ?? NaN
      max[i] = s.max_latency_ms
    })
    return [t, avg, max]
  }, [samples])

  // An ongoing outage has no end yet, and it must not be given one here:
  // reading the clock during render is impure, and the value would be stale by
  // the time the canvas is painted anyway. `null` is resolved to "now" inside
  // the draw hook, which runs outside render.
  const spans = useMemo(
    () =>
      incidents.map((inc) => ({
        from: Date.parse(inc.started_at) / 1000,
        to: inc.resolved_at === null ? null : Date.parse(inc.resolved_at) / 1000,
      })),
    [incidents],
  )

  // Held in a ref so the draw hook sees the current spans without the plot
  // being rebuilt every time an incident resolves. Written in an effect, not
  // during render.
  const spansRef = useRef(spans)
  useEffect(() => {
    spansRef.current = spans
    plot.current?.redraw()
  }, [spans])

  useEffect(() => {
    const el = holder.current
    if (el === null) return

    const options: uPlot.Options = {
      width: el.clientWidth,
      height,
      padding: [12, 8, 0, 0],
      cursor: { drag: { x: true, y: false } },
      legend: { live: true },
      scales: { x: { time: true } },
      axes: [
        { stroke: 'currentColor', grid: { stroke: 'rgb(148 163 184 / 0.2)' } },
        {
          stroke: 'currentColor',
          grid: { stroke: 'rgb(148 163 184 / 0.2)' },
          values: (_u, ticks) => ticks.map((v) => `${v} ms`),
        },
      ],
      series: [
        { label: 'time' },
        {
          label: 'avg',
          stroke: 'oklch(58% 0.09 195)',
          width: 1.5,
          spanGaps: false,
          value: (_u, v) => (v === null ? '—' : `${Math.round(v)} ms`),
        },
        {
          label: 'peak',
          stroke: 'oklch(72% 0.15 80)',
          width: 1,
          dash: [4, 4],
          spanGaps: false,
          value: (_u, v) => (v === null ? '—' : `${Math.round(v)} ms`),
        },
      ],
      hooks: {
        // Shade outages behind the series, so a flat line during an outage
        // cannot be misread as a healthy one.
        drawClear: [
          (u) => {
            const { ctx } = u
            ctx.save()
            ctx.fillStyle = 'oklch(58% 0.19 25 / 0.14)'
            const now = Date.now() / 1000
            for (const span of spansRef.current) {
              const end = span.to ?? now
              const x0 = u.valToPos(Math.max(span.from, u.scales.x?.min ?? span.from), 'x', true)
              const x1 = u.valToPos(Math.min(end, u.scales.x?.max ?? end), 'x', true)
              if (x1 > x0) ctx.fillRect(x0, u.bbox.top, x1 - x0, u.bbox.height)
            }
            ctx.restore()
          },
        ],
      },
    }

    plot.current = new uPlot(options, data, el)

    const observer = new ResizeObserver(() => {
      plot.current?.setSize({ width: el.clientWidth, height })
    })
    observer.observe(el)

    return () => {
      observer.disconnect()
      plot.current?.destroy()
      plot.current = null
    }
    // Built once. Data changes go through setData below, which is the whole
    // point of using an imperative chart.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [height])

  useEffect(() => {
    plot.current?.setData(data)
  }, [data])

  return <div ref={holder} className="w-full text-slate-500 dark:text-slate-400" />
}
