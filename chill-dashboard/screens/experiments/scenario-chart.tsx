"use client"

import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { duration } from "@/lib/live-display"
import { formatNumber } from "@/lib/format"
import type { Spec } from "@/lib/evaluation-author"
import { scenarioSeconds, scenarioServices, scenarioSteps } from "@/lib/experiments-scenario"

const tick = { fill: "var(--muted-foreground)", fontSize: 12 }

/** Request rate per service as a step line over the scenario time (seconds from start). */
export function RateChart({ spec, height = 240 }: { spec: Spec; height?: number }) {
  const names = scenarioServices(spec), total = scenarioSeconds(spec)
  return (
    <figure className="m-0 min-w-0">
      <figcaption className="sr-only">서비스별 요청률 미리보기</figcaption>
      <div style={{ height }}><ResponsiveContainer width="100%" height="100%">
        <LineChart data={scenarioSteps(spec)} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="var(--border)" vertical={false} />
          <XAxis dataKey="t" type="number" domain={[0, total || 1]} tickFormatter={value => duration(value)} minTickGap={48} tick={tick} stroke="var(--border)" tickLine={false} />
          <YAxis width={52} tick={tick} stroke="var(--border)" tickLine={false} axisLine={false} tickFormatter={value => formatNumber(value, 0)}
            label={{ value: "req/s", position: "insideTopLeft", offset: 0, dy: -4, fill: "var(--muted-foreground)", fontSize: 11 }} />
          <Tooltip
            isAnimationActive={false} cursor={{ stroke: "var(--muted-foreground)", strokeDasharray: "3 3" }}
            contentStyle={{ background: "var(--card)", border: "1px solid var(--border)", borderRadius: 8, fontSize: 12, color: "var(--foreground)" }}
            labelFormatter={value => duration(Number(value))}
            formatter={(value, name) => [value == null ? "—" : `${formatNumber(Number(value), 2)} req/s`, String(name)]}
          />
          {names.map((name, index) => (
            <Line key={index} dataKey={`s${index}`} name={name || `서비스 ${index + 1}`} type="stepAfter" stroke={`var(--chart-${(index % 6) + 1})`} strokeWidth={1.75} dot={false} connectNulls={false} isAnimationActive={false} />
          ))}
        </LineChart>
      </ResponsiveContainer></div>
      {names.length > 1 && (
        <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground" aria-hidden="true">
          {names.map((name, index) => (
            <li key={index} className="flex items-center gap-1.5"><span className="size-2 rounded-full" style={{ background: `var(--chart-${(index % 6) + 1})` }} />{name}</li>
          ))}
        </ul>
      )}
    </figure>
  )
}
