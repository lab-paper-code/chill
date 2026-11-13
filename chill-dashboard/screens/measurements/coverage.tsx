"use client"

import * as React from "react"
import Link from "next/link"
import { sectionTabs } from "@/lib/routes"
import { useSession } from "@/lib/client"
import { formatNumber } from "@/lib/format"
import { deviceClassLabel } from "@/lib/display-labels"
import { measurementStatus } from "@/lib/status-labels"
import type { MeasurementCoverageSummaryResponse as Coverage } from "@/lib/read-types"
import { useMeasurementCoverage } from "@/hooks/use-measurement-data"
import {
  Button, EmptyState, FilterBar, Page, PageHeader, PageTabs, QueryBoundary, StatRow, StatTile, StatusBadge, TimeText, buttonClassName,
} from "@/components/kit"
import { filterChoice, useUrlState } from "./results-shared"
import { matrixRows } from "@/lib/measurements-coverage-matrix"
import { CoverageMatrix, STATUSES } from "./coverage-matrix"
import { CellDrawer, NextActionsDrawer } from "./coverage-drawer"

const count = (value: number | null | undefined) => (value == null ? null : formatNumber(value))

export function CoverageScreen() {
  const { config } = useSession()
  const query = useMeasurementCoverage(config.namespace)
  const { search, set } = useUrlState()
  const data = query.data
  return (
    <Page>
      <PageHeader
        title="측정"
        badges={data && <Basis data={data} />}
        actions={<>
          {data && data.nextActions.length > 0 && <Button variant="outline" onClick={() => set({ actions: "1", cell: null })}>다음 조치 {data.nextActions.length}</Button>}
          <Link href="/measurements/requests/new" className={buttonClassName({ variant: "default" })}>새 측정 요청</Link>
        </>}
      />
      <PageTabs tabs={sectionTabs.measurements} />
      <QueryBoundary query={query} isEmpty={d => d.cells.length === 0} empty={<EmptyState title="측정 대상 없음" />}>
        {d => <CoverageBody data={d} search={search} set={set} namespace={config.namespace} />}
      </QueryBoundary>
    </Page>
  )
}

function Basis({ data }: { data: Coverage }) {
  const failed = data.sources.filter(source => source.error || !source.complete).map(source => source.kind)
  return (
    <>
      <span className="text-sm text-muted-foreground">기준 <TimeText at={data.dataAsOf} mode="relative" /></span>
      {data.freshness === "stale" && <StatusBadge tone="warn" label="오래된 관측" />}
      {(data.completeness === "partial" || failed.length > 0) && <StatusBadge tone="warn" label="일부 조회 실패" reason={failed.join(", ") || undefined} />}
    </>
  )
}

function CoverageBody({ data, search, set, namespace }: {
  data: Coverage; search: URLSearchParams; namespace: string
  set: (patch: Record<string, string | null | undefined>) => void
}) {
  const summary = data.summary
  const models = React.useMemo(() => [...new Set(data.cells.map(cell => cell.model.name))].sort(), [data])
  const model = models.includes(search.get("model") ?? "") ? search.get("model")! : models[0]
  const modelCells = React.useMemo(() => data.cells.filter(cell => cell.model.name === model), [data, model])
  const classes = [...new Set(modelCells.map(cell => cell.deviceClass.name))].sort()
  const deviceClass = classes.includes(search.get("deviceClass") ?? "") ? search.get("deviceClass") : null
  const status = STATUSES.includes(search.get("status") ?? "") ? search.get("status") : null
  const rows = React.useMemo(() => matrixRows(modelCells, deviceClass, status), [modelCells, deviceClass, status])
  const cellId = search.get("cell")
  const cell = cellId ? data.cells.find(item => item.id === cellId) : undefined
  const returnTo = `/measurements/coverage?${search}`
  const pickModel = (value: string) => set({ model: value, deviceClass: null, cell: null })
  const filters = [
    filterChoice("장치 종류", deviceClass, classes, value => set({ deviceClass: value }), deviceClassLabel),
    filterChoice("상태", status, STATUSES, value => set({ status: value }), code => measurementStatus(code).label),
  ]
  return (
    <div className="flex flex-col gap-6">
      <StatRow>
        <StatTile label="지원 조합" value={count(summary.supportedCellCount)} reason="서버 미제공" />
        <StatTile label="측정 완료" value={count(summary.completeCellCount)} reason="서버 미제공"
          sub={summary.countsByDisplayStatus.running ? `측정 중 ${formatNumber(summary.countsByDisplayStatus.running)}` : undefined} />
        <StatTile label="기준 충족" value={count(summary.criteriaMetCellCount)} reason="서버 미제공" />
        <StatTile label="실패" value={count(summary.failedCellCount)} reason="서버 미제공" />
      </StatRow>
      {/* The model names do not fit a segmented row on phones; there the model is a select. */}
      <FilterBar className="hidden md:flex" segments={[{ label: "모델", value: model, options: models.map(name => ({ value: name, label: name })), onChange: pickModel }]} selects={filters} />
      <FilterBar className="md:hidden" selects={[{ label: "모델", value: model, options: models.map(name => ({ value: name, label: name })), onChange: pickModel }, ...filters]} />
      <CoverageMatrix
        rows={rows}
        cells={modelCells}
        status={status}
        onOpen={id => set({ cell: id, actions: null })}
        empty={<EmptyState compact title="조건에 맞는 조합 없음" action={<Button size="sm" variant="outline" onClick={() => set({ deviceClass: null, status: null })}>필터 해제</Button>} />}
      />
      {cell && <CellDrawer cell={cell} namespace={namespace} returnTo={returnTo} onClose={() => set({ cell: null })} />}
      {!cell && search.get("actions") && (
        <NextActionsDrawer actions={data.nextActions} cells={data.cells} onClose={() => set({ actions: null })}
          hrefOf={target => `/measurements/coverage?${new URLSearchParams({ model: target.model.name, cell: target.id })}`} />
      )}
    </div>
  )
}
