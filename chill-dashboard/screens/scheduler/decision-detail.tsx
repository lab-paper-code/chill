"use client"

import * as React from "react"
import Link from "next/link"
import { useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { schedulerLabel } from "@/lib/scheduler-selection"
import { formatDurationNanos, formatKst, formatNumber } from "@/lib/format"
import { autonomyState, candidateStatus, decisionStatus, schedulerReason } from "@/lib/status-labels"
import { changedTarget, controlFor, decisionControlPath, decisionRecordPath, latencyRows, recordResult, tickPhases, type ControlObservation, type DecisionPage, type LatencyRow } from "@/lib/scheduler-decisions"
import type { SchedulerCandidate, SchedulerDecision, SchedulerNodePlan, SchedulerObservation, SchedulerObservationPage, SchedulerWorkloadState, WireInteger } from "@/lib/read-types"
import { buttonClassName, DataTable, EmptyState, Fields, Hint, IdText, QueryBoundary, Section, StatusBadge, StatusDot, type Column } from "@/components/kit"

const decoder = { decoder: parseSchedulerJSON }
type Names = (uid: string) => string | undefined

/** One reason code as its label; unknown codes show "—" with the code. */
function Reason({ code }: { code: string | null | undefined }) {
  if (!code) return null
  const reason = schedulerReason(code)
  return <Hint text={reason.code && `코드 ${reason.code}`} focusable={!!reason.code}>{reason.label}</Hint>
}

/** First reason and "+n"; the tooltip lists all, unknown codes as written. */
export function ReasonSummary({ codes }: { codes: string[] }) {
  const labels = codes.map(code => schedulerReason(code))
  const text = `${labels[0].label}${codes.length > 1 ? ` +${codes.length - 1}` : ""}`
  return <Hint text={codes.length > 1 || labels[0].code ? labels.map(item => item.code ?? item.label).join(", ") : undefined} focusable>{text}</Hint>
}

function Service({ uid, name }: { uid: string; name: Names }) {
  const label = name(uid)
  return label ? <>{label}</> : <IdText value={uid} />
}

/** Selected decision: full record by its exact time, plus the tick that produced it. */
export function DecisionDetail({ at, names, intervalNanos }: { at: string; names: Map<string, string>; intervalNanos: WireInteger | undefined }) {
  const path = decisionRecordPath(at)
  const record = useChillQuery<SchedulerObservationPage>(path ?? "", { enabled: !!path, ...decoder })
  if (!path) return <EmptyState title="판단 시각 오류" />
  return (
    <QueryBoundary query={record} skeleton="block" isEmpty={data => !data.events?.some(row => row.decision?.at === at)} empty={<EmptyState title="판단 기록 없음" />}>
      {data => <Detail event={data.events!.find(row => row.decision?.at === at)!} names={names} intervalNanos={intervalNanos} />}
    </QueryBoundary>
  )
}

function Detail({ event, names, intervalNanos }: { event: SchedulerObservation; names: Map<string, string>; intervalNanos: WireInteger | undefined }) {
  const decision = event.decision!
  // Names as recorded with the decision first; deleted services keep theirs.
  const name: Names = uid => event.snapshot?.workloads?.find(w => w?.uid === uid)?.name ?? names.get(uid)
  return (
    <div className="grid gap-6">
      <Summary decision={decision} />
      <Candidates decision={decision} name={name} />
      <Autonomy workloads={decision.workloads ?? []} name={name} />
      <Demand decision={decision} name={name} />
      <Capacity decision={decision} />
      <Timing at={decision.at} intervalNanos={intervalNanos} />
    </div>
  )
}

function Summary({ decision }: { decision: SchedulerDecision }) {
  const s = decision.search
  const reasons = decision.reasons ?? []
  return (
    <Section card title={formatKst(decision.at)} actions={<>
      <Link href={`/scheduler/actions?at=${encodeURIComponent(decision.at)}`} className={buttonClassName({ variant: "ghost", size: "sm" })}>작업 보기</Link>
      <StatusBadge status={decisionStatus(recordResult(decision))} />
    </>}>
      <Fields columns={3} items={[
        { label: "사유", value: reasons.length ? <ReasonSummary codes={reasons} /> : null, reason: "사유 없음" },
        { label: "입력 구간", value: s ? `${formatKst(s.windowStart).slice(11)} ~ ${formatKst(s.windowEnd).slice(11)}` : null, reason: "기록 없음" },
        { label: "판단 계산", value: decision.evaluationElapsedNanos == null ? null : formatDurationNanos(decision.evaluationElapsedNanos), reason: "기록 없음", info: "후보를 만들고 비교하는 데 걸린 시간" },
        { label: "받은 요청", value: s ? `${formatNumber(s.received)}건` : null, reason: "기록 없음" },
        { label: "접수 요청", value: s ? `${formatNumber(s.accepted)}건` : null, reason: "기록 없음" },
        { label: "검토한 후보", value: s ? `${formatNumber(s.examined)}개` : null, reason: "기록 없음" },
      ]} />
    </Section>
  )
}

type Plan = { label: string; plan: SchedulerCandidate; diff: number | null }
function Candidates({ decision, name }: { decision: SchedulerDecision; name: Names }) {
  const target = changedTarget(decision)
  const keepJ = decision.keep.expectedJoules
  const plans: Plan[] = [{ label: "유지안", plan: decision.keep, diff: null }]
  if (target) plans.push({ label: "변경안", plan: target, diff: target.expectedJoules != null && keepJ != null ? target.expectedJoules - keepJ : null })
  const latency = latencyRows(decision).sort((a, b) => (name(a.uid) ?? a.uid).localeCompare(name(b.uid) ?? b.uid))
  const latencyColumns: Column<LatencyRow>[] = [
    { key: "service", header: "서비스", width: "34%", cell: row => <Service uid={row.uid} name={name} /> },
    { key: "percentile", header: "분위수", cell: row => row.percentile == null ? null : `p${row.percentile}` },
    { key: "target", header: "목표", unit: "ms", numeric: true, cell: row => formatNumber(row.targetMs, 1) },
    { key: "keep", header: "유지안", unit: "ms", numeric: true, missingReason: "예측 불가", cell: row => row.keepMs == null ? null : formatNumber(row.keepMs, 1) },
    ...(target ? [{ key: "change", header: "변경안", unit: "ms", numeric: true, missingReason: "예측 불가", cell: (row: LatencyRow) => row.changeMs == null ? null : formatNumber(row.changeMs, 1) }] : []),
  ]
  return (
    <>
      <Section title="후보 비교">
        <DataTable label="후보별 예상 에너지" rows={plans} rowKey={row => row.label} pageSize={0} columns={[
          { key: "plan", header: "후보", cell: row => row.label },
          { key: "status", header: "판정", cell: row => <StatusDot status={candidateStatus(row.plan.status)} showLabel /> },
          { key: "energy", header: "예상 에너지", unit: "J", numeric: true, missingReason: "판정 불가", cell: row => row.plan.expectedJoules == null ? null : formatNumber(row.plan.expectedJoules, 1) },
          { key: "diff", header: "유지안 대비", unit: "J", numeric: true, missingReason: "해당 없음", cell: row => row.diff == null ? null : `${row.diff > 0 ? "+" : ""}${formatNumber(row.diff, 1)}` },
          { key: "actions", header: "작업", unit: "건", numeric: true, cell: row => formatNumber(row.plan.actions?.length ?? 0) },
        ]} />
      </Section>
      <Section title="서비스별 예상 지연">
        <DataTable label="서비스별 예상 지연" rows={latency} rowKey={row => row.uid} pageSize={0} columns={latencyColumns}
          empty={<EmptyState compact title="예상 지연 없음" />} />
      </Section>
    </>
  )
}

function Autonomy({ workloads, name }: { workloads: SchedulerWorkloadState[]; name: Names }) {
  const rows = [...workloads].sort((a, b) => (name(a.uid) ?? a.uid).localeCompare(name(b.uid) ?? b.uid))
  return (
    <Section title="서비스별 자동 제어">
      <DataTable label="서비스별 자동 제어" rows={rows} rowKey={row => row.uid} pageSize={0} empty={<EmptyState compact title="서비스 없음" />} columns={[
        { key: "service", header: "서비스", width: "40%", cell: row => <Service uid={row.uid} name={name} /> },
        { key: "state", header: "상태", cell: row => <StatusDot status={autonomyState(row.state)} showLabel /> },
        { key: "reason", header: "사유", missingReason: "사유 없음", cell: row => row.reason ? <Reason code={row.reason} /> : null },
      ]} />
    </Section>
  )
}

type DemandRow = { uid: string; received: number | null; accepted: number | null; inputs: number | null; planned: number | null }
function Demand({ decision, name }: { decision: SchedulerDecision; name: Names }) {
  const rows = new Map<string, DemandRow>()
  const row = (uid: string) => rows.get(uid) ?? rows.set(uid, { uid, received: null, accepted: null, inputs: null, planned: null }).get(uid)!
  for (const load of decision.search?.loads ?? []) Object.assign(row(load.uid), { received: load.received, accepted: load.accepted, inputs: load.inputsPerSecond })
  for (const workload of decision.capacity?.workloads ?? []) row(workload.uid).planned = workload.rate
  const list = [...rows.values()].sort((a, b) => (name(a.uid) ?? a.uid).localeCompare(name(b.uid) ?? b.uid))
  return (
    <Section title="입력 수요">
      <DataTable label="입력 수요" rows={list} rowKey={item => item.uid} pageSize={0} empty={<EmptyState compact title="입력 없음" />} columns={[
        { key: "service", header: "서비스", width: "32%", cell: item => <Service uid={item.uid} name={name} /> },
        { key: "received", header: "받은 요청", unit: "건", numeric: true, cell: item => item.received == null ? null : formatNumber(item.received) },
        { key: "accepted", header: "접수", unit: "건", numeric: true, cell: item => item.accepted == null ? null : formatNumber(item.accepted) },
        { key: "inputs", header: "입력률", unit: "/s", numeric: true, cell: item => item.inputs == null ? null : formatNumber(item.inputs, 2) },
        { key: "planned", header: "계획 수요", unit: "req/s", numeric: true, missingReason: "용량 계산 없음", cell: item => item.planned == null ? null : formatNumber(item.planned, 2) },
      ]} />
    </Section>
  )
}

function Capacity({ decision }: { decision: SchedulerDecision }) {
  const capacity = decision.capacity
  if (!capacity) return <Section title="용량 근거"><EmptyState compact title="용량 근거 없음" className="rounded-xl border border-dashed border-border" /></Section>
  const search = capacity.search
  return (
    <Section title="용량 근거">
      <div className="grid gap-3">
        <Section card>
          <Fields columns={3} items={[
            { label: "탐색 방식", value: search?.strategy ? schedulerLabel(search.strategy) : null, reason: "기록 없음" },
            { label: "평가한 구성", value: search ? `${formatNumber(search.evaluated)}개` : null, reason: "기록 없음" },
            { label: "탐색 시간", value: search ? formatDurationNanos(search.durationNanos) : null, reason: "기록 없음" },
            { label: "시간 마감 종료", value: search?.budgetCut == null ? null : search.budgetCut ? "예" : "아니요", reason: "기록 없음" },
            { label: "에너지 비교 기간", value: formatDurationNanos(capacity.periodNanos) },
            { label: "분배 계산 기간", value: formatDurationNanos(capacity.routeNanos) },
          ]} />
        </Section>
        <DataTable<SchedulerNodePlan> label="장치별 계획" rows={capacity.plans ?? []} rowKey={plan => `${plan.node.uid}/${plan.key}`} pageSize={8}
          empty={<EmptyState compact title="계획 없음" />} columns={[
            { key: "node", header: "장치", cell: plan => plan.node.name || null },
            { key: "eligible", header: "적격", cell: plan => <StatusDot status={plan.eligible ? { label: "적격", tone: "ok" } : { label: "제외", tone: "idle" }} showLabel /> },
            { key: "reason", header: "사유", missingReason: "사유 없음", cell: plan => plan.reason ? <Reason code={plan.reason} /> : null },
            { key: "joules", header: "예상 에너지", unit: "J", numeric: true, cell: plan => formatNumber(plan.joules, 1) },
          ]} />
      </div>
    </Section>
  )
}

function Timing({ at, intervalNanos }: { at: string; intervalNanos: WireInteger | undefined }) {
  const path = intervalNanos == null ? null : decisionControlPath(at, intervalNanos)
  const query = useChillQuery<DecisionPage>(path ?? "", { enabled: !!path, ...decoder })
  return (
    <Section title="소요 시간">
      <QueryBoundary query={query} skeleton="block" isEmpty={data => !controlFor(data, at)} empty={<EmptyState compact title="소요 기록 없음" className="rounded-xl border border-dashed border-border" />}>
        {data => <Phases control={controlFor(data, at)!} intervalNanos={intervalNanos!} />}
      </QueryBoundary>
    </Section>
  )
}

function Phases({ control, intervalNanos }: { control: ControlObservation; intervalNanos: WireInteger }) {
  const total = Number(control.elapsedNanos), interval = Number(intervalNanos)
  return (
    <Section card>
      <div className="grid gap-4">
        <Fields columns={3} items={[
          { label: "전체", value: formatDurationNanos(total) },
          { label: "판단 주기 대비", value: `${formatNumber((total / interval) * 100, 1)}%` },
          { label: "시작", value: formatKst(control.startedAt).slice(11) },
        ]} />
        <ul aria-label="단계별 소요" className="grid gap-1.5 text-sm">
          {tickPhases(control).map(phase => (
            <li key={phase.key} className="grid grid-cols-[minmax(0,9rem)_5.5rem_minmax(0,1fr)] items-center gap-3">
              <span className={phase.part ? "truncate pl-4 text-muted-foreground" : "truncate"}>{phase.label}</span>
              <span className="text-right tabular-nums">{formatDurationNanos(phase.nanos)}</span>
              <span className="h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                <span className={phase.part ? "block h-full bg-primary/45" : "block h-full bg-primary"} style={{ width: `${Math.min(phase.nanos / total, 1) * 100}%` }} />
              </span>
            </li>
          ))}
        </ul>
      </div>
    </Section>
  )
}
