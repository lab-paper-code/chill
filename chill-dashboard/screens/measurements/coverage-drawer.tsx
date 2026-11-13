"use client"

import Link from "next/link"
import { formatNumber, msNumber } from "@/lib/format"
import { coverageBatchState, coverageMeasurementStatus, coverageProgress, coverageRequestHref, coverageVariant } from "@/lib/measurement-coverage"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { measurementSchedulerUsed } from "@/lib/measurement-assessment"
import { coverageOccupancyRequestHref, occupancyReasonLabel, occupancyRequestHref, occupancyStates } from "@/lib/dispatch-occupancy"
import { measurementStatus, type StatusInfo } from "@/lib/status-labels"
import type { MeasurementCoverageCell as Cell, MeasurementCoverageResponse, ResultRow } from "@/lib/read-types"
import { Button, DataTable, DetailDrawer, Feed, Fields, Hint, Section, StatusDot, TimeText, buttonClassName, type Column } from "@/components/kit"
import { cellMeasured, cellReasons } from "@/lib/measurements-coverage-matrix"

const BATCH_STATES: Record<string, StatusInfo> = {
  measured: { label: "측정됨", tone: "ok" },
  running: { label: "측정 중", tone: "progress" },
  missing: { label: "미측정", tone: "idle" },
  unknown: { label: "미확인", tone: "idle" },
  outside: { label: "목표 밖", tone: "idle" },
}
type BatchRow = { batch: number; state: string; result: ResultRow | undefined }
const missingBatch = (r: BatchRow) => r.state === "measured" ? "결과 기록 없음" : BATCH_STATES[r.state].label
const batchColumns: Column<BatchRow>[] = [
  { key: "batch", header: "배치", unit: "건", cell: r => <Hint text={BATCH_STATES[r.state].label}><StatusDot status={BATCH_STATES[r.state]} label={String(r.batch)} showLabel /></Hint> },
  { key: "latency", header: "지연 p95", unit: "ms", numeric: true, cell: r => r.result?.p95Milliseconds == null ? null : msNumber(r.result.p95Milliseconds), missing: missingBatch },
  { key: "energy", header: "건당 에너지", unit: "J", numeric: true, cell: r => r.result?.incrementalJoulesPerItem == null ? null : formatNumber(r.result.incrementalJoulesPerItem, 3), missing: missingBatch },
]

export function CellDrawer({ cell, namespace, returnTo, onClose }: { cell: Cell; namespace: string; returnTo: string; onClose: () => void }) {
  const code = coverageMeasurementStatus(cell)
  const variant = coverageVariant(cell), progress = coverageProgress(cell)
  const batches = [...new Set([...(progress?.target ?? []), ...(progress?.measured ?? []), ...(progress?.inProgress ?? []), ...(variant?.resultRows.map(row => row.batch) ?? [])])].sort((a, b) => a - b)
  const rows: BatchRow[] = batches.map(batch => ({
    batch, state: coverageBatchState(progress, batch),
    result: variant?.resultRows.find(row => row.batch === batch && row.rowKind === "direct") ?? variant?.resultRows.find(row => row.batch === batch),
  }))
  const used = measurementSchedulerUsed(cell.assessment)
  const occupancy = cell.occupancy ?? []
  const reasons = cellReasons(cell)
  const measuredAt = cell.currentMeasurement?.measuredAt ?? variant?.measuredAt
  const occupancyHref = occupancy[0] ? coverageOccupancyRequestHref(cell, occupancy[0], namespace)
    : occupancyRequestHref(namespace, { model: cell.model.name, deviceClass: cell.deviceClass.name, path: cell.executionPath, powerMode: cell.powerMode, measurementID: cell.currentMeasurement?.id })
  const history = `/measurements/requests?${new URLSearchParams({ model: cell.model.name, deviceClass: cell.deviceClass.name, path: cell.executionPath, powerMode: cell.powerMode })}`
  return (
    <DetailDrawer
      open onOpenChange={open => { if (!open) onClose() }}
      title={`${deviceClassLabel(cell.deviceClass.name)} ${powerModeLabel(cell.powerMode)}`}
      status={measurementStatus(code)}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <Link href={history} className={buttonClassName({ variant: "ghost" })}>요청 기록</Link>
          {cell.currentMeasurement
            ? <Link href={occupancyHref} className={buttonClassName({ variant: "outline" })}>분배 점유시간 측정 요청</Link>
            : <Button variant="outline" disabled disabledReason="측정 결과 필요">분배 점유시간 측정 요청</Button>}
          {code !== "unsupported" && <Link href={coverageRequestHref(cell, namespace, returnTo)} className={buttonClassName({ variant: "default" })}>이 조건으로 측정 요청</Link>}
        </div>
      }
    >
      {/* The answer first: why this status, how many target batches, whether the scheduler uses it. */}
      {reasons.length > 0 && <Fields columns={1} items={[{ label: "사유", value: reasons.join(", ") }]} />}
      <Fields columns={2} items={[
        { label: "측정 배치", value: cellMeasured(cell) || null, reason: "측정 없음", info: "목표 배치 중 측정된 수" },
        { label: "사용 여부", value: used === true ? "사용 중" : used === false ? "미사용" : null, reason: "확인 불가", info: "스케줄러가 지금 이 측정을 쓰는지" },
      ]} />
      <Fields columns={2} items={[
        { label: "모델", value: cell.model.name },
        { label: "실행 경로", value: <Hint text={cell.executionPath}>{executionPathLabel(cell.executionPath)}</Hint> },
        { label: "측정 시각", value: measuredAt && <TimeText at={measuredAt} mode="detail" />, reason: "측정 없음" },
        { label: "분배 점유시간", value: occupancy.length ? <Hint text={[...new Set(occupancy.map(row => occupancyReasonLabel(row.reason)))].join(", ")}>{[...new Set(occupancy.map(row => occupancyStates[row.state]))].join(", ")}</Hint> : null, reason: "기록 없음" },
      ]} />
      {rows.length > 0 && (
        <Section title="배치별 결과">
          <DataTable label="배치별 결과" density="compact" pageSize={0} rows={rows} rowKey={r => String(r.batch)} columns={batchColumns} />
        </Section>
      )}
    </DetailDrawer>
  )
}

type NextAction = MeasurementCoverageResponse["nextActions"][number]
const ACTION_KINDS: Record<string, StatusInfo> = {
  failure: { label: "실패 확인", tone: "fail" },
  evidence: { label: "근거 확인", tone: "warn" },
  "missing-batches": { label: "배치 부족", tone: "warn" },
}

export function NextActionsDrawer({ actions, cells, hrefOf, onClose }: { actions: NextAction[]; cells: Cell[]; hrefOf: (cell: Cell) => string; onClose: () => void }) {
  const byId = new Map(cells.map(cell => [cell.id, cell]))
  // One row per action kind and cell: the server lists a failure once per failed request.
  const rows = new Map<string, { key: string; kind: string; cell: Cell }>()
  for (const action of actions) {
    const cell = byId.get(action.cellId), key = `${action.kind}/${action.cellId}`
    if (cell && !rows.has(key)) rows.set(key, { key, kind: action.kind, cell })
  }
  const models = [...new Set([...rows.values()].map(row => row.cell.model.name))].sort()
  return (
    <DetailDrawer open onOpenChange={open => { if (!open) onClose() }} title="다음 조치">
      {models.map(model => (
        <Section key={model} title={model}>
          <Feed label={model} items={[...rows.values()].filter(row => row.cell.model.name === model).map(row => ({
            key: row.key,
            status: ACTION_KINDS[row.kind] ?? { label: "—", tone: "idle", code: row.kind },
            title: `${deviceClassLabel(row.cell.deviceClass.name)} ${powerModeLabel(row.cell.powerMode)}`,
            secondary: executionPathLabel(row.cell.executionPath),
            meta: (ACTION_KINDS[row.kind] ?? { label: "—" }).label,
            href: hrefOf(row.cell),
          }))} />
        </Section>
      ))}
    </DetailDrawer>
  )
}
