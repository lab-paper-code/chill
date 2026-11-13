"use client"

// Time series on recharts: the Avara chart block draws {label, value} with numbers only,
// so it cannot leave gaps for null samples or use a time axis.
import * as React from "react"
import { CartesianGrid, Line, LineChart, ReferenceDot, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { formatKst, formatNumber, niceTicks, secondTicks, timeTicks } from "@/lib/format"
import { duration } from "@/lib/live-display"

export type SeriesPoint = { t: number } & Record<string, number | null>
export type Series = { key: string; label: string; /** 1 to 6 (chart colours). */ color?: 1 | 2 | 3 | 4 | 5 | 6 }

const tick = { fill: "var(--muted-foreground)", fontSize: 12 }
const colorOf = (item: Series, index: number) => `var(--chart-${item.color ?? (index % 6) + 1})`
const finite = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value)

/**
 * Lines over time. Null values leave a gap: the line is never drawn across missing samples,
 * and a sample with gaps on both sides is drawn as a dot. No entrance animation, so 1 s updates
 * do not replay anything. The y axis always includes zero and the reference line.
 */
export function TimeSeries({ data, series, unit, height = 220, domain, reference, digits, label, legend = false, x = "time", curve = "linear", summary }: {
  /** `t` is epoch ms, or seconds from the start with `x="seconds"`. */
  data: SeriesPoint[]
  series: Series[]
  /** Y axis unit, e.g. "W". */ unit?: string
  height?: number
  /** Fixed x window [from, to]; defaults to the data extent. */ domain?: [number, number]
  /** Horizontal reference line, e.g. a target or the decision interval. */ reference?: { value: number; label: string }
  /** Fraction digits in the tooltip; defaults to the axis precision (at least 1). */ digits?: number
  /** Accessible name of the chart. */ label: string
  /** Series names under the chart. */ legend?: boolean
  /** "seconds": numeric axis in seconds from the start, labelled "1분 30초". */ x?: "time" | "seconds"
  /** "step": each value holds until the next sample (load steps). */ curve?: "linear" | "step"
  /** Text alternative read by screen readers; defaults to the last value of each series. */ summary?: string
}) {
  const axes = React.useMemo(() => {
    let low = 0, high = reference?.value ?? 0, from = Infinity, to = -Infinity
    for (const point of data) {
      if (point.t < from) from = point.t
      if (point.t > to) to = point.t
      for (const item of series) { const value = point[item.key]; if (finite(value)) { low = Math.min(low, value); high = Math.max(high, value) } }
    }
    if (reference) low = Math.min(low, reference.value)
    const [start, end] = domain ?? (from <= to ? [from, to] : [0, 1])
    const y = niceTicks(low, high)
    const xs = x === "seconds" ? secondTicks(end - start) : timeTicks(start, end)
    const isolated = series.flatMap(item => data.flatMap((point, index) => {
      const value = point[item.key]
      return finite(value) && !finite(data[index - 1]?.[item.key]) && !finite(data[index + 1]?.[item.key]) ? [{ key: item.key, t: point.t, value }] : []
    }))
    return { y, xs, start, end, isolated }
  }, [data, series, reference, domain, x])
  const shown = digits ?? Math.max(1, axes.y.digits)
  const withUnit = (value: number) => `${formatNumber(value, shown)}${unit ? ` ${unit}` : ""}`
  const at = (value: number) => x === "seconds" ? duration(value) : `${formatKst(value)} KST`
  const text = summary ?? series.map(item => {
    const last = data.findLast(point => finite(point[item.key]))
    return last ? `${item.label} ${withUnit(last[item.key] as number)} (${at(last.t)})` : `${item.label} 값 없음`
  }).join(", ")
  return (
    <figure className="m-0 min-w-0">
      <figcaption className="sr-only">{label}. {text}</figcaption>
      <div style={{ height }} aria-hidden="true">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: 0 }} accessibilityLayer={false}>
            <CartesianGrid stroke="var(--border)" vertical={false} />
            <XAxis
              dataKey="t" type="number" scale={x === "time" ? "time" : "linear"} domain={[axes.start, axes.end]} allowDataOverflow
              ticks={axes.xs.ticks} tickFormatter={value => axes.xs.label(Number(value))} minTickGap={24}
              tick={tick} stroke="var(--border)" tickLine={false}
            />
            <YAxis
              width={52} domain={[axes.y.ticks[0], axes.y.ticks.at(-1)!]} ticks={axes.y.ticks} tickFormatter={value => formatNumber(value, axes.y.digits)}
              tick={tick} stroke="var(--border)" tickLine={false} axisLine={false}
              label={unit ? { value: unit, position: "insideTopLeft", offset: 0, dy: -4, fill: "var(--muted-foreground)", fontSize: 11 } : undefined}
            />
            <Tooltip
              isAnimationActive={false}
              cursor={{ stroke: "var(--muted-foreground)", strokeDasharray: "3 3" }}
              contentStyle={{ background: "var(--card)", border: "1px solid var(--border)", borderRadius: 8, fontSize: 12, color: "var(--foreground)" }}
              labelFormatter={value => at(Number(value))}
              formatter={(value, name) => [value == null ? "\u2014" : withUnit(Number(value)), String(name)]}
            />
            {reference && <ReferenceLine y={reference.value} stroke="var(--muted-foreground)" strokeDasharray="4 4" label={{ value: reference.label, position: "insideTopRight", fill: "var(--muted-foreground)", fontSize: 11 }} />}
            {series.map((item, index) => (
              <Line key={item.key} dataKey={item.key} name={item.label} type={curve === "step" ? "stepAfter" : "linear"} stroke={colorOf(item, index)} strokeWidth={1.75} dot={false} connectNulls={false} isAnimationActive={false} />
            ))}
            {axes.isolated.map(point => (
              <ReferenceDot key={`${point.key}-${point.t}`} x={point.t} y={point.value} r={2.5} stroke="none" fill={colorOf(series.find(item => item.key === point.key)!, series.findIndex(item => item.key === point.key))} />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </div>
      {legend && (
        <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {series.map((item, index) => (
            <li key={item.key} className="flex min-w-0 items-center gap-1.5"><span aria-hidden="true" className="size-2 shrink-0 rounded-full" style={{ background: colorOf(item, index) }} /><span className="truncate">{item.label}</span></li>
          ))}
        </ul>
      )}
    </figure>
  )
}

/** Tiny trend line for table cells; gaps stay gaps. */
export function Sparkline({ values, width = 72, height = 20, label }: { values: (number | null)[]; width?: number; height?: number; label: string }) {
  const finite = values.filter((value): value is number => value != null && Number.isFinite(value))
  const max = Math.max(...finite, 0), min = Math.min(...finite, 0)
  const span = max - min || 1
  const step = values.length > 1 ? width / (values.length - 1) : 0
  let path = "", open = false
  values.forEach((value, index) => {
    if (value == null || !Number.isFinite(value)) { open = false; return }
    const x = index * step, y = height - 1 - ((value - min) / span) * (height - 2)
    path += `${open ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`
    open = true
  })
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} className="shrink-0 overflow-visible">
      <path d={path} fill="none" stroke="var(--chart-1)" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}
