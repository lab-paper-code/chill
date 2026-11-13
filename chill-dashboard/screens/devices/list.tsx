"use client"

import { DataTable, EmptyState, Page, PageHeader, QueryBoundary, Section, StatRow, StatTile, StatusDot, TimeRange, useTimeRangeParam, type Column } from "@/components/kit"
import { deviceRole, energyParts, readiness } from "@/lib/devices-view"
import { formatNumber, formatWatts } from "@/lib/format"
import type { PowerPoint } from "@/lib/power-series"
import { devicePowerPercent, devicePowerScale, type DeviceRow } from "@/lib/resources-data"
import { PowerChart, useDevices, usePower } from "./data"

const HOUR = 3_600_000
const poolSeries = [{ key: "whole", label: "전체 풀", color: 1 as const }, { key: "active", label: "활성 풀", color: 2 as const }]
const poolValues = (point: PowerPoint) => ({ whole: point.wholeWatts, active: point.activeWatts })

export function DeviceListScreen() {
  const { state, inventory, pools, excluded, rows } = useDevices()
  const [range, setRange] = useTimeRangeParam(["15m", "1h"], "15m", HOUR)
  const power = usePower(range)
  const pool = state.metrics.at(-1)?.pool
  const total = power.window.data
  const energy = energyParts(total?.wholePoolJoules)
  const scale = devicePowerScale(rows, state.powerScale)
  const waiting = state.metrics.length ? "관측 없음" : "실시간 수신 대기"

  const columns: Column<DeviceRow>[] = [
    { key: "name", header: "장치", cell: row => row.name, sortValue: row => row.name, width: "13%" },
    { key: "class", header: "종류", cell: row => row.classes[0]?.name, sortValue: row => row.classes[0]?.name, missingReason: "장비 사양 없음", width: "19%" },
    {
      key: "role", header: "역할",
      cell: row => { const role = deviceRole(row.roles, excluded.has(row.name)); return role && <StatusDot status={role} showLabel /> },
      sortValue: row => deviceRole(row.roles, excluded.has(row.name))?.label, missingReason: pools.error ? "배치 조회 실패" : "역할 없음",
    },
    { key: "ready", header: "준비", cell: row => { const ready = readiness(row.ready); return ready && <StatusDot status={ready} showLabel /> }, sortValue: row => readiness(row.ready)?.label, missingReason: "준비 상태 미확인" },
    { key: "mode", header: "전력 모드", cell: row => row.powerMode || null, sortValue: row => row.powerMode, missingReason: "전력 모드 미확인" },
    {
      key: "watts", header: "전력", unit: "W", numeric: true, width: "9rem",
      cell: row => row.watts == null ? null : (
        <span className="inline-flex items-center gap-2">
          <span className="relative h-1.5 w-12 overflow-hidden rounded-full bg-muted" aria-hidden="true">
            <span className="absolute inset-y-0 left-0 rounded-full bg-primary" style={{ width: `${devicePowerPercent(row, scale) ?? 0}%` }} />
          </span>
          {formatNumber(row.watts, 1)}
        </span>
      ),
      sortValue: row => row.watts, missingReason: "전력 미관측",
    },
    {
      key: "services", header: "서비스", width: "20%",
      cell: row => row.services.length ? `${row.services[0].name}${row.services.length > 1 ? ` +${row.services.length - 1}` : ""}` : null,
      missingReason: "실행 중인 서비스 없음",
    },
  ]

  return (
    <Page>
      <PageHeader title="장치" />
      <StatRow className="grid-cols-2">
        <StatTile label="전체 풀" value={pool?.wholeWatts == null ? null : formatNumber(pool.wholeWatts, 1)} unit="W" reason={waiting} />
        <StatTile label="활성 풀" value={pool?.activeWatts == null ? null : formatNumber(pool.activeWatts, 1)} unit="W" reason={waiting} />
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
            label="장치" rows={rows} rowKey={row => row.uid} columns={columns}
            rowHref={row => `/devices/${encodeURIComponent(row.name)}`}
            defaultSort={{ key: "name", desc: false }} pageSize={0}
          />
        )}
      </QueryBoundary>
    </Page>
  )
}
