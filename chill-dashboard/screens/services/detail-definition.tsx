"use client"

import * as React from "react"
import Link from "next/link"
import { DataTable, EmptyState, Fields, QueryBoundary, Section, StatusBadge, TimeText, type Column } from "@/components/kit"
import { useChillQuery, useSession } from "@/lib/client"
import type { WorkloadView } from "@/lib/domain"
import { formatDurationNanos, formatNumber } from "@/lib/format"
import type { ManagementEvent, ObservationResult, SchedulerDefinition } from "@/lib/read-types"
import { managementKind, managementPhase, managementResult } from "@/lib/services-records"
import { parseSchedulerJSON } from "@/lib/scheduler"

export type ServiceTabProps = { name: string; uid: string | undefined; workload: WorkloadView | undefined }

const HOUR = 3_600_000
const catalogLink = (kind: "models" | "preprocessors" | "datasets", name: string | undefined) =>
  name ? <Link href={`/catalog/${kind}/${encodeURIComponent(name)}`} className="text-primary hover:underline">{name}</Link> : null

export function ServiceDefinitionTab({ uid, workload }: ServiceTabProps) {
  const { config } = useSession()
  // One fixed window per mount keeps the query key stable; the server returns all management events inside it.
  const [now] = React.useState(() => Date.now())
  const params = new URLSearchParams({ from: new Date(now - config.maxWindowHours * HOUR).toISOString(), to: new Date(now).toISOString(), limit: "1" })
  const query = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid ?? "-")}?${params}`, { enabled: !!uid, decoder: parseSchedulerJSON })
  // A deleted service has no live spec; the last definition the server recorded stands in for it.
  const last = query.data?.definitions.at(-1)?.definition as RecordedDefinition | undefined
  const definition = workload ? fromWorkload(workload) : last ? fromRecorded(last) : undefined
  if (!definition) {
    return query.isPending && uid ? <QueryBoundary query={query} skeleton="block">{() => null}</QueryBoundary> : <EmptyState title="정의 없음" />
  }
  return (
    <div className="grid min-w-0 gap-4">
      <Section title="정의" card actions={workload ? <Applied workload={workload} /> : undefined}>
        <DefinitionFields definition={definition} />
      </Section>
      <Section title="변경 이력">
        <QueryBoundary query={query} skeleton="table" isEmpty={result => !result.management?.length} empty={<EmptyState compact title="변경 이력 없음" />}>
          {result => <History events={result.management ?? []} />}
        </QueryBoundary>
      </Section>
    </div>
  )
}

function Applied({ workload }: { workload: WorkloadView }) {
  const applied = workload.appliedDefinition
  if (!applied) return <StatusBadge tone="warn" label="미적용" />
  const current = applied.generation === workload.desired.metadata.generation
  return (
    <>
      <span className="text-xs text-muted-foreground tabular-nums">적용 세대 {applied.generation}</span>
      <StatusBadge tone={current ? "ok" : "warn"} label={current ? "적용됨" : "미적용"} reason={current ? undefined : `수정 세대 ${workload.desired.metadata.generation}`} />
    </>
  )
}

const policyLabel = { Reject: "거절", Delay: "지연 허용" } as const

type Definition = {
  model: string; preprocessor: string; dataset?: string; percentile: number; target: string
  policy: keyof typeof policyLabel; maxQueued: number; lifetime?: string
}

function DefinitionFields({ definition }: { definition: Definition }) {
  return (
    <Fields columns={3} items={[
      { label: "모델", value: catalogLink("models", definition.model) },
      { label: "전처리기", value: catalogLink("preprocessors", definition.preprocessor) },
      { label: "데이터셋", value: catalogLink("datasets", definition.dataset), reason: "지정하지 않음" },
      { label: "목표", value: `${definition.percentile}% 요청을 ${definition.target} 안에` },
      { label: "과부하 정책", value: policyLabel[definition.policy] },
      { label: "대기 상한", value: formatNumber(definition.maxQueued) },
      { label: "최종 기한", value: definition.lifetime, reason: "지정하지 않음" },
    ]} />
  )
}

const fromWorkload = ({ desired: { spec } }: WorkloadView): Definition => ({
  model: spec.modelRef.name, preprocessor: spec.preprocessorRef.name, dataset: spec.datasetRef?.name, percentile: spec.slo.percentile, target: spec.slo.targetLatency,
  policy: spec.admission.policy, maxQueued: spec.admission.maxQueuedRequests, lifetime: spec.requestLifetime,
})

// The wire definition carries more than SchedulerDefinition declares.
type RecordedDefinition = SchedulerDefinition & {
  preprocessor: { name: string }; dataset?: { name: string }; admission: { policy: keyof typeof policyLabel; maxQueuedRequests: number }; requestLifetime?: number
}
const fromRecorded = (recorded: RecordedDefinition): Definition => ({
  model: recorded.model.name, preprocessor: recorded.preprocessor.name, dataset: recorded.dataset?.name, percentile: recorded.slo.percentile, target: formatDurationNanos(recorded.slo.targetLatencyNanos),
  policy: recorded.admission.policy, maxQueued: recorded.admission.maxQueuedRequests, lifetime: recorded.requestLifetime === undefined ? undefined : formatDurationNanos(recorded.requestLifetime),
})

const columns: Column<ManagementEvent>[] = [
  { key: "at", header: "시각", cell: event => <TimeText at={event.at} mode="absolute" />, csv: event => event.at },
  { key: "kind", header: "종류", cell: event => managementKind(event.kind), csv: event => managementKind(event.kind) },
  { key: "phase", header: "단계", cell: event => managementPhase(event.phase), csv: event => managementPhase(event.phase) },
  { key: "result", header: "결과", cell: event => <StatusBadge status={managementResult(event)} reason={event.error} />, csv: event => managementResult(event).label },
]

function History({ events }: { events: ManagementEvent[] }) {
  // Newest first; a stable sort key keeps the order when the list refetches.
  const rows = React.useMemo(() => [...events].reverse(), [events])
  return <DataTable label="변경 이력" rows={rows} rowKey={event => `${event.at}/${event.kind}/${event.phase}`} columns={columns} pageSize={10} download={{ filename: "management-history" }} />
}
