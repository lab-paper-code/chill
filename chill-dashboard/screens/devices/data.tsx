"use client"

import * as React from "react"
import { TIME_PRESETS, TimeSeries, useNow, type Series, type TimeRangeValue } from "@/components/kit"
import { useChillQuery } from "@/lib/client"
import { chartRows } from "@/lib/devices-view"
import { mergePowerSeries, parsePowerSeriesJSON, powerSeriesPath, type PowerPoint } from "@/lib/power-series"
import type { OperatingPower, OperatingPowerSeries, PoolsView, SchedulerSettings } from "@/lib/read-types"
import { deviceRows } from "@/lib/resources-data"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { useLive } from "@/lib/use-live"
import { useCoverageInventory } from "@/hooks/use-measurement-data"

/** Last defined value, so a refetch under a new key does not blank the region. */
function useKept<T>(value: T | undefined): T | undefined {
  const [kept, setKept] = React.useState(value)
  if (value !== undefined && value !== kept) setKept(value)
  return value ?? kept
}

/** Devices joined by UID from inventory, pools and the live stream; exclusion from scheduler settings. */
export function useDevices() {
  const { state } = useLive()
  const inventory = useCoverageInventory()
  const pools = useChillQuery<PoolsView>("v1/pools", { poll: true })
  const [etag, setEtag] = React.useState<string | null>(null)
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON, onResponse: response => setEtag(response.etag ?? null) })
  // Rows only seen in power metrics have no name to show or link to.
  const rows = React.useMemo(() => deviceRows(state, inventory.data, pools.data).filter(row => row.name), [state, inventory.data, pools.data])
  const excluded = React.useMemo(() => new Set(settings.data?.excludedNodes ?? []), [settings.data])
  return { state, inventory, pools, settings, etag, rows, excluded }
}

const LIVE_REACH_MS = 60_000

/**
 * Power over a range: server buckets merged with the live per-second watts.
 * A recent range re-reads the server every minute so the history always meets the 10-minute live buffer.
 * `window` is the server's own total for the same range (energy, mean power, gaps).
 */
export function usePower(range: TimeRangeValue) {
  const { state } = useLive()
  const now = useNow()
  const [anchor, setAnchor] = React.useState(0)
  const recent = "preset" in range
  if (recent && now && now - anchor >= LIVE_REACH_MS) setAnchor(now)
  const span = recent ? TIME_PRESETS[range.preset].ms : range.to - range.from
  const end = recent ? anchor : range.to
  const from = new Date(end - span).toISOString(), to = new Date(end).toISOString()
  const seriesPath = end ? powerSeriesPath(from, to, span <= 900_000 ? "5s" : "30s") : null
  const series = useChillQuery<OperatingPowerSeries>(seriesPath ?? "v1/observations/power", { enabled: !!seriesPath, decoder: parsePowerSeriesJSON })
  const window = useChillQuery<OperatingPower>(`v1/observations/power?${new URLSearchParams({ from, to })}`, { enabled: !!end, decoder: parseSchedulerJSON })
  const seed = useKept(series.data)
  const total = useKept(window.data)
  // A recent range follows the clock; a chosen range stays put.
  const shownEnd = recent ? now : range.to
  const points = React.useMemo(() => shownEnd ? mergePowerSeries(seed, state.metrics, shownEnd, span) : [], [seed, state.metrics, shownEnd, span])
  return { points, domain: [shownEnd - span, shownEnd] as [number, number], series: { ...series, data: seed, isPending: !seed && series.isPending }, window: { ...window, data: total } }
}

export function PowerChart({ points, domain, series, values, label, height }: {
  points: PowerPoint[]; domain: [number, number]; series: Series[]
  values: (point: PowerPoint) => Record<string, number | null>; label: string; height?: number
}) {
  const data = React.useMemo(() => chartRows(points, values), [points, values])
  return (
    <div className="grid gap-2">
      {series.length > 1 && (
        <ul className="flex items-center gap-4 text-xs text-muted-foreground" aria-hidden="true">
          {series.map((item, index) => (
            <li key={item.key} className="inline-flex items-center gap-1.5">
              <span className="h-0.5 w-3 rounded-full" style={{ background: `var(--chart-${item.color ?? index + 1})` }} />{item.label}
            </li>
          ))}
        </ul>
      )}
      <TimeSeries data={data} series={series} unit="W" domain={domain} label={label} height={height} />
    </div>
  )
}
