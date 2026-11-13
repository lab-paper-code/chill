"use client"

import * as React from "react"
import { Button, DataTable, DetailDrawer, EmptyState, Fields, FilterBar, Missing, QueryBoundary, RawDialog, StatusBadge, TimeText, type Column, type Segment } from "@/components/kit"
import { useChillQuery } from "@/lib/client"
import { formatDurationNanos, formatNumber } from "@/lib/format"
import { measurementPath, measurementReasonSummary, operationSteps, type OperationStep } from "@/lib/measurements"
import { operationStageLabels } from "@/lib/operation-cost"
import type { MeasurementProjectionList, OperationCostMeasurement } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { measurementStatus } from "@/lib/status-labels"
import { distinct, filterChoice, useUrlState } from "./results-shared"

type Operation = "prepare" | "remove" | "power"
const operations: Operation[] = ["prepare", "remove", "power"]
type Result = OperationStep["results"][number]
type Row = { key: string; measurement: OperationCostMeasurement; operation: Operation; model: string | null; steps: OperationStep[]; results: Result[] }

const durationOf = (result: Result) => result.durationNanos == null ? undefined : Number(result.durationNanos)
const energyOf = (result: Result) => result.verdict === "Accepted" && typeof result.wallJoules === "number" ? result.wallJoules : undefined
const defined = (values: (number | undefined)[]) => values.filter((value): value is number => value !== undefined)
// Largest recorded value: a pick of server values, no statistic.
const largest = (values: number[]) => values.length ? Math.max(...values) : undefined

const verdicts: Record<string, { label: string }> = { Accepted: { label: "채택" }, Failed: { label: "실패" }, Insufficient: { label: "근거 부족" } }

/** A removal result carries no model; its profile reference ties it to the preparation that does. */
function modelLookup(steps: OperationStep[]) {
  const byProfile = new Map<string, string>()
  for (const step of steps) {
    const model = step.results.find(result => result.model)?.model
    if (model && step.condition?.batchEnergyOptimization) byProfile.set(step.condition.batchEnergyOptimization, model)
  }
  return (step: OperationStep, result?: Result) => result?.model ?? (step.condition?.batchEnergyOptimization ? byProfile.get(step.condition.batchEnergyOptimization) : undefined) ?? null
}

function rowsOf(items: OperationCostMeasurement[]): Row[] {
  return items.flatMap(measurement => {
    const steps = operationSteps(measurement), modelOf = modelLookup(steps)
    return operations.flatMap(operation => {
      const groups = new Map<string | null, OperationStep[]>()
      for (const step of steps.filter(step => step.condition?.kind === operation)) {
        const model = modelOf(step, step.results[0])
        groups.set(model, [...(groups.get(model) ?? []), step])
      }
      return [...groups].map(([model, own]) => toRow(measurement, operation, model, own))
    })
  })
}
const toRow = (measurement: OperationCostMeasurement, operation: Operation, model: string | null, steps: OperationStep[]): Row =>
  ({ key: `${measurement.metadata.uid}/${operation}/${model ?? ""}`, measurement, operation, model, steps, results: steps.flatMap(step => step.results) })

const columns: Column<Row>[] = [
  { width: "22%", key: "model", header: "모델", cell: row => row.model, missingReason: "모델 없는 동작", sortValue: row => row.model },
  { width: "12%", key: "node", header: "장치", cell: row => row.measurement.node, sortValue: row => row.measurement.node },
  { width: "12%", key: "operation", header: "동작", cell: row => operationStageLabels[row.operation] },
  { width: "14%", key: "duration", header: "최대 시간", numeric: true, cell: row => { const v = largest(defined(row.results.map(durationOf))); return v === undefined ? null : formatDurationNanos(v) }, sortValue: row => largest(defined(row.results.map(durationOf))), missingReason: "실측 시간 없음" },
  { width: "15%", key: "energy", header: "최대 에너지", unit: "J", numeric: true, cell: row => { const v = largest(defined(row.results.map(energyOf))); return v === undefined ? null : formatNumber(v, 1) }, sortValue: row => largest(defined(row.results.map(energyOf))), missingReason: "채택된 에너지 없음" },
  { width: "11%", key: "samples", header: "표본", unit: "회", numeric: true, cell: row => row.results.length, sortValue: row => row.results.length },
  { key: "status", header: "상태", cell: row => <StatusBadge status={measurementStatus(row.measurement.assessment?.displayStatus)} /> },
]

type StepLine = { key: string; step: OperationStep; result: Result | undefined }
const stepColumns: Column<StepLine>[] = [
  { width: "46%", key: "step", header: "단계", cell: line => line.step.name.replace(/^(prepare|remove)-/, "") },
  { key: "duration", header: "시간", numeric: true, cell: line => line.result?.durationNanos == null ? null : formatDurationNanos(line.result.durationNanos), missingReason: "실측 시간 없음" },
  {
    key: "energy", header: "에너지", unit: "J", numeric: true,
    cell: line => { const v = line.result && energyOf(line.result); return v === undefined ? <Missing reason={failureReason(line.result)} /> : formatNumber(v, 1) },
  },
]
function failureReason(result: Result | undefined) {
  if (!result) return "측정 기록 없음"
  const known = verdicts[result.verdict]
  if (result.verdict === "Accepted") return "에너지 기록 없음"
  return [known?.label ?? `코드 ${result.verdict}`, result.reason && measurementReasonSummary(result.reason)].filter(Boolean).join(": ")
}

export function MeasurementCostResults({ lead, namespace }: { lead: Segment; namespace: string }) {
  const { search, set } = useUrlState()
  const list = useChillQuery<MeasurementProjectionList>(measurementPath("operation-cost-measurements", namespace), { decoder: parseSchedulerJSON })
  const [raw, setRaw] = React.useState(false)
  const all = React.useMemo(() => rowsOf((list.data?.items ?? []).filter((item): item is OperationCostMeasurement => item.kind === "OperationCostMeasurement")), [list.data])
  const model = search.get("model"), node = search.get("node"), status = search.get("status")
  const rows = all.filter(row => (!model || row.model === model) && (!node || row.measurement.node === node) && (!status || row.measurement.assessment?.displayStatus === status))
  const selectedKey = search.get("op") ? `${search.get("uid")}/${search.get("op")}` : null
  const selected = all.find(row => row.key === selectedKey)
  const reset = { uid: null, op: null }
  return (
    <>
      <FilterBar
        segments={[lead]}
        selects={[
          filterChoice("모델", model, distinct(all.map(row => row.model)), value => set({ model: value, ...reset })),
          filterChoice("장치", node, distinct(all.map(row => row.measurement.node)), value => set({ node: value, ...reset })),
          filterChoice("상태", status, distinct(all.map(row => row.measurement.assessment?.displayStatus)), value => set({ status: value, ...reset }), code => measurementStatus(code).label),
        ]}
      />
      <QueryBoundary query={list} isEmpty={() => !all.length} empty={<EmptyState title="전환 비용 결과 없음" />}>
        {() => (
          <DataTable
            label="전환 비용"
            rows={rows}
            rowKey={row => row.key}
            columns={columns}
            onRowClick={row => set({ uid: row.measurement.metadata.uid, op: `${row.operation}/${row.model ?? ""}` })}
            selectedKey={selectedKey}
            busy={list.isFetching}
            download={{ filename: "operation-cost" }}
            empty={<EmptyState compact title="조건에 맞는 결과 없음" />}
          />
        )}
      </QueryBoundary>
      <DetailDrawer
        open={!!selectedKey}
        onOpenChange={open => { if (!open) set(reset) }}
        title={selected ? operationStageLabels[selected.operation] : "전환 비용"}
        status={selected ? measurementStatus(selected.measurement.assessment?.displayStatus) : undefined}
        actions={selected && <Button size="sm" variant="ghost" onClick={() => setRaw(true)}>원본 보기</Button>}
      >
        {selected ? (
          <>
            <Fields columns={2} items={[
              { label: "모델", value: selected.model, reason: "모델 없는 동작" },
              { label: "측정", value: selected.measurement.metadata.name },
              { label: "장치", value: selected.measurement.node },
              { label: "등록", value: <TimeText at={selected.measurement.metadata.creationTimestamp} mode="detail" /> },
            ]} />
            <DataTable
              label="단계별 실측값"
              rows={selected.steps.flatMap(step => (step.results.length ? step.results : [undefined]).map((result, index): StepLine => ({ key: `${step.name}/${index}`, step, result })))}
              rowKey={line => line.key}
              columns={stepColumns}
              pageSize={0}
            />
            <RawDialog open={raw} onOpenChange={setRaw} value={selected.measurement} filename={`${selected.measurement.metadata.name}.json`} title="전환 비용 원본" />
          </>
        ) : <EmptyState compact title="목록에 없음" />}
      </DetailDrawer>
    </>
  )
}
