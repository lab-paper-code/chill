"use client"

import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { DataTable, EmptyState, FilterBar, MeterBar, Page, PageHeader, QueryBoundary, Section, StatRow, StatTile, StatusDot, TimeRange, useTimeRangeParam, type Column } from "@/components/kit"
import { useSession } from "@/lib/client"
import { deviceRole, energyParts, readiness } from "@/lib/devices-view"
import { formatNumber, formatWatts, wattsNumber } from "@/lib/format"
import { deviceClassLabel, powerModeLabel } from "@/lib/display-labels"
import { requestHref } from "@/lib/measurement-request"
import type { PowerPoint } from "@/lib/power-series"
import { devicePowerScale, type DeviceRow } from "@/lib/resources-data"
import { PowerChart, useDevices, usePower } from "./data"

const HOUR = 3_600_000
const poolSeries = [{ key: "whole", label: "전체 풀", color: 1 as const }, { key: "active", label: "활성 풀", color: 2 as const }]
const poolValues = (point: PowerPoint) => ({ whole: point.wholeWatts, active: point.activeWatts })
// `?role=` values and the role words they select.
const ROLES = [{ value: "serving", label: "서비스" }, { value: "measuring", label: "측정" }, { value: "available", label: "가용" }, { value: "excluded", label: "제외" }]
const link = "truncate text-foreground hover:underline"

export function DeviceListScreen() {
  const { state, inventory, pools, excluded, rows, holds, runs } = useDevices()
  const { config } = useSession()
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const [range, setRange] = useTimeRangeParam(["15m", "1h"], "15m", HOUR)
  const power = usePower(range)
  const pool = state.metrics.at(-1)?.pool
  const total = power.window.data
  const energy = energyParts(total?.wholePoolJoules)
  const scale = devicePowerScale(rows, state.powerScale)
  const waiting = state.metrics.length ? "관측 없음" : "실시간 수신 대기"
  const roleOf = (row: DeviceRow) => deviceRole(row.roles, excluded.has(row.name))
  const role = ROLES.find(item => item.value === search.get("role"))
  const shown = role ? rows.filter(row => roleOf(row)?.label === role.label) : rows
  const setRole = (value: string) => {
    const params = new URLSearchParams(search.toString())
    if (value === "all") params.delete("role"); else params.set("role", value)
    const text = params.toString()
    router.replace(`${pathname}${text ? `?${text}` : ""}`, { scroll: false })
  }

  const columns: Column<DeviceRow>[] = [
    { key: "name", header: "장치", cell: row => row.name, sortValue: row => row.name, width: "12%" },
    { key: "class", header: "종류", cell: row => row.classes[0] && deviceClassLabel(row.classes[0].name), sortValue: row => row.classes[0]?.name, missingReason: "장비 사양 없음", width: "17%" },
    {
      key: "role", header: "역할",
      cell: row => { const value = roleOf(row); return value && <StatusDot status={value} showLabel /> },
      sortValue: row => roleOf(row)?.label, missingReason: pools.error ? "배치 조회 실패" : "역할 없음",
    },
    { key: "ready", header: "준비", cell: row => { const ready = readiness(row.ready); return ready && <StatusDot status={ready} showLabel /> }, sortValue: row => readiness(row.ready)?.label, missingReason: "준비 상태 미확인" },
    { key: "mode", header: "전력 모드", width: "10rem", cell: row => row.powerMode ? powerModeLabel(row.powerMode) : null, sortValue: row => row.powerMode, missingReason: "전력 모드 미확인" },
    {
      key: "watts", header: "전력", unit: "W", numeric: true, width: "8.5rem",
      cell: row => row.watts == null ? null : (
        <span className="inline-flex items-center gap-2">
          <MeterBar value={row.watts} max={scale} label={`전력 ${formatWatts(row.watts)}`} className="w-12" />
          {wattsNumber(row.watts)}
        </span>
      ),
      sortValue: row => row.watts, missingReason: "전력 미관측",
    },
    {
      key: "running", header: "실행 중", width: "21%",
      cell: row => {
        const request = holds.get(row.name)?.request
        if (request) return <Link href={requestHref(config.namespace, request.name, request.uid)} className={link}>{request.name}</Link>
        const first = row.services[0]
        if (!first) return null
        return (
          <span className="flex min-w-0 items-baseline gap-1">
            <Link href={`/services/${encodeURIComponent(first.name)}?uid=${encodeURIComponent(first.uid)}`} className={link}>{first.name}</Link>
            {row.services.length > 1 && <span className="shrink-0 text-muted-foreground">+{row.services.length - 1}</span>}
          </span>
        )
      },
      csv: row => holds.get(row.name)?.request?.name ?? row.services.map(service => service.name).join(" "),
      missing: row => holds.has(row.name) ? (runs.error ? "측정 회차 조회 실패" : "측정 요청 확인 불가") : "실행 중인 서비스 없음",
    },
  ]

  return (
    <Page>
      <PageHeader title="장치" />
      <StatRow columns={4}>
        <StatTile label="전체 풀" value={pool?.wholeWatts == null ? null : wattsNumber(pool.wholeWatts)} unit="W" reason={waiting} />
        <StatTile label="활성 풀" value={pool?.activeWatts == null ? null : wattsNumber(pool.activeWatts)} unit="W" reason={waiting} />
        <StatTile
          label="구간 에너지" value={energy.value == null ? null : formatNumber(energy.value, 1)} unit={energy.unit}
          sub={total?.wholePoolMeanWatts == null ? undefined : `평균 ${formatWatts(total.wholePoolMeanWatts)}`}
          reason={power.window.error ? "조회 실패" : total && !total.complete ? "구간 계측 불완전" : "조회 중"}
          info="선택 구간에서 서버가 적분한 전체 풀 에너지"
        />
        <StatTile
          label="준비된 장치" value={inventory.data ? String(rows.filter(row => row.ready).length) : null} unit={`/ ${rows.length}`}
          reason={inventory.error ? "조회 실패" : "조회 중"}
        />
      </StatRow>
      <Section title="전체 전력" card actions={<TimeRange value={range} onChange={setRange} presets={["15m", "1h"]} custom={{ maxSpanMs: HOUR }} />}>
        <QueryBoundary query={power.series} skeleton="block">
          {() => <PowerChart points={power.points} domain={power.domain} series={poolSeries} values={poolValues} label="전체 풀과 활성 풀 전력" />}
        </QueryBoundary>
      </Section>
      <QueryBoundary query={inventory} isEmpty={() => rows.length === 0} empty={<EmptyState title="장치 없음" />}>
        {() => (
          <DataTable
            label="장치" rows={shown} rowKey={row => row.uid} columns={columns}
            rowHref={row => `/devices/${encodeURIComponent(row.name)}`}
            defaultSort={{ key: "name", desc: false }} pageSize={0}
            toolbar={<FilterBar segments={[{
              label: "역할", value: role?.value ?? "all", onChange: setRole,
              options: [{ value: "all", label: "전체", count: rows.length }, ...ROLES.map(item => ({ ...item, count: rows.filter(row => roleOf(row)?.label === item.label).length }))],
            }]} />}
            empty={<EmptyState compact title={`${role?.label ?? ""} 장치 없음`} />}
          />
        )}
      </QueryBoundary>
    </Page>
  )
}
