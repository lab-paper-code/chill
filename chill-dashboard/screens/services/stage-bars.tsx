"use client"

import { EmptyState, MeterBar } from "@/components/kit"
import type { RequestRow } from "@/lib/read-types"
import { nanosToMs, requestMs, requestStages } from "@/lib/services-records"

/** Stage times of one request (요청 drawer, 테스트 result), each bar scaled to the request's total time. */
export function StageBars({ row, backendLabel }: { row: RequestRow; backendLabel?: string }) {
  const stages = requestStages(row, backendLabel)
  if (!stages) return <EmptyState compact title="단계 기록 없음" />
  const total = Math.max(nanosToMs(row.elapsedNanos) ?? 0, stages.reduce((sum, stage) => sum + stage.ms, 0))
  return (
    <dl aria-label="단계별 시간" className="grid gap-2.5">
      {stages.map(stage => (
        <div key={stage.key} className="grid grid-cols-[4.5rem_minmax(0,1fr)_5rem] items-center gap-3 text-sm">
          <dt className="text-muted-foreground">{stage.label}</dt>
          <dd><MeterBar value={stage.ms} max={total} width="full" label={`${stage.label} ${requestMs(stage.ms * 1e6)}`} /></dd>
          <dd className="text-right tabular-nums">{requestMs(stage.ms * 1e6)}</dd>
        </div>
      ))}
    </dl>
  )
}
