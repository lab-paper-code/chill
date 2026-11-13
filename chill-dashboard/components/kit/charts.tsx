"use client"

// Time series on recharts: the Avara chart block draws {label, value} with numbers only,
// so it cannot leave gaps for null samples or use a time axis.
import * as React from "react"
import { CartesianGrid, Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { formatKst, formatNumber } from "@/lib/format"

export type SeriesPoint = { t: number } & Record<string, number | null>
export type Series = { key: string; label: string; /** 1 to 6 (chart colours). */ color?: 1 | 2 | 3 | 4 | 5 | 6 }

const kstTime = new Intl.DateTimeFormat("ko-KR", { timeZone: "Asia/Seoul", hour: "2-digit", minute: "2-digit", hourCycle: "h23" })

/**
 * Lines over time. Null values leave a gap: the line is never drawn across missing samples.
 * No entrance animation, so 1 s updates do not replay anything.
 */
export function TimeSeries({ data, series, unit, height = 220, domain, reference, digits = 1, label }: {
  data: SeriesPoint[]
  series: Series[]
  /** Y axis unit, e.g. "W". */ unit?: string
  height?: number
  /** Fixed time window [from, to] in epoch ms; defaults to the data extent. */ domain?: [number, number]
  /** Horizontal reference line, e.g. a target or the decision interval. */ reference?: { value: number; label: string }
  digits?: number
  /** Accessible description of the chart. */ label: string
}) {
  return (
    <figure className="m-0 min-w-0" style={{ height }}>
      <figcaption className="sr-only">{label}</figcaption>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="var(--border)" vertical={false} />
          <XAxis
            dataKey="t" type="number" scale="time" domain={domain ?? ["dataMin", "dataMax"]} allowDataOverflow
            tickFormatter={value => kstTime.format(new Date(value))} minTickGap={48}
            tick={{ fill: "var(--muted-foreground)", fontSize: 12 }} stroke="var(--border)" tickLine={false}
          />
          <YAxis
            width={52} tickFormatter={value => formatNumber(value, 0)} tick={{ fill: "var(--muted-foreground)", fontSize: 12 }}
            stroke="var(--border)" tickLine={false} axisLine={false}
            label={unit ? { value: unit, position: "insideTopLeft", offset: 0, dy: -4, fill: "var(--muted-foreground)", fontSize: 11 } : undefined}
          />
          <Tooltip
            isAnimationActive={false}
            cursor={{ stroke: "var(--muted-foreground)", strokeDasharray: "3 3" }}
            contentStyle={{ background: "var(--card)", border: "1px solid var(--border)", borderRadius: 8, fontSize: 12, color: "var(--foreground)" }}
            labelFormatter={value => `${formatKst(Number(value))} KST`}
            formatter={(value, name) => [value == null ? "—" : `${formatNumber(Number(value), digits)}${unit ? ` ${unit}` : ""}`, String(name)]}
          />
          {reference && <ReferenceLine y={reference.value} stroke="var(--muted-foreground)" strokeDasharray="4 4" label={{ value: reference.label, position: "insideTopRight", fill: "var(--muted-foreground)", fontSize: 11 }} />}
          {series.map((item, index) => (
            <Line key={item.key} dataKey={item.key} name={item.label} type="linear" stroke={`var(--chart-${item.color ?? index + 1})`} strokeWidth={1.75} dot={false} connectNulls={false} isAnimationActive={false} />
          ))}
        </LineChart>
      </ResponsiveContainer>
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
