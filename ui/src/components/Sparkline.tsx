import { useMemo } from 'react'

interface Props {
  /** Latency in ms, oldest first. Nulls are gaps. */
  values: (number | null)[]
  /** Indices that were DOWN, drawn as ticks rather than points on the line. */
  downs?: boolean[]
  width?: number
  height?: number
  className?: string
}

/**
 * An inline latency sparkline as a single SVG path.
 *
 * Hand-drawn rather than uPlot: there is one of these per table row, and 200
 * uPlot instances on a dashboard would each bring a canvas, a resize observer
 * and a legend. uPlot earns its place on the detail page, where there is one
 * chart with 2 880 points and interaction.
 */
export function Sparkline({ values, downs, width = 96, height = 24, className = '' }: Props) {
  const { path, marks, max } = useMemo(() => {
    const present = values.filter((v): v is number => v !== null)
    const peak = present.length === 0 ? 1 : Math.max(...present, 1)
    const step = values.length > 1 ? width / (values.length - 1) : width

    let d = ''
    let pen = false
    values.forEach((v, i) => {
      if (v === null) {
        pen = false
        return
      }
      const x = i * step
      // Leave a pixel of headroom top and bottom so the line is never clipped.
      const y = height - 1 - (v / peak) * (height - 2)
      d += `${pen ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)} `
      pen = true
    })

    const ticks =
      downs === undefined
        ? []
        : downs.flatMap((isDown, i) => (isDown ? [{ x: i * step }] : []))

    return { path: d.trim(), marks: ticks, max: peak }
  }, [values, downs, width, height])

  if (values.length === 0) {
    return <span className={`inline-block text-xs text-slate-400 ${className}`}>—</span>
  }

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className={className}
      role="img"
      aria-label={`Latency, peak ${Math.round(max)} ms`}
      preserveAspectRatio="none"
    >
      {marks.map((m, i) => (
        <line
          key={i}
          x1={m.x}
          x2={m.x}
          y1={0}
          y2={height}
          className="stroke-down-500/40"
          strokeWidth={1.5}
        />
      ))}
      {path !== '' && (
        <path d={path} fill="none" className="stroke-accent-500" strokeWidth={1.25} />
      )}
    </svg>
  )
}
