"use client"

import Link from "next/link"
import { useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { schedulerLabel, schedulerOptions } from "@/lib/scheduler-selection"
import { formatDurationNanos, formatKst, formatNumber } from "@/lib/format"
import { autonomyState, candidateStatus, decisionStatus, schedulerReason } from "@/lib/status-labels"
import { actionKindLabel } from "@/lib/scheduler-actions"
import {
  changedTarget, controlFor, decisionControlPath, decisionHeadline, decisionRecordPath, latencyRows, recordResult, savingJoules, tickPhases,
  type ControlObservation, type DecisionPage, type LatencyRow, type PlannedAction,
} from "@/lib/scheduler-decisions"
import type { SchedulerCandidate, SchedulerDecision, SchedulerNodePlan, SchedulerObservation, SchedulerObservationPage, SchedulerWorkloadState, WireInteger } from "@/lib/read-types"
import { buttonClassName, DataTable, EmptyState, Feed, Fields, Hint, IdText, MeterBar, QueryBoundary, Section, StatusBadge, StatusDot, type Column } from "@/components/kit"

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

/** Reading order: what was decided and why, keep versus change, then the evidence. */
function Detail({ event, names, intervalNanos }: { event: SchedulerObservation; names: Map<string, string>; intervalNanos: WireInteger | undefined }) {
  const decision = event.decision!
  // Names as recorded with the decision first; deleted services keep theirs.
  const name: Names = uid => event.snapshot?.workloads?.find(w => w?.uid === uid)?.name ?? names.get(uid)
  return (
    <div className="grid gap-6">
      <Summary decision={decision} name={name} />
      <Latency decision={decision} name={name} />
      <Inputs decision={decision} name={name} />
      <Autonomy workloads={decision.workloads ?? []} name={name} />
      <Capacity decision={decision} />
      <Timing at={decision.at} intervalNanos={intervalNanos} />
    </div>
  )
}

/** The reason line: server reason labels (unknown codes folded into "기타 n건"), else the headline the record supports. */
function Headline({ decision }: { decision: SchedulerDecision }) {
  const headline = decisionHeadline(decision)
  if (!headline) return <p className="text-base text-muted-foreground">기록된 사유 없음</p>
  if ("text" in headline) return <p className="text-base font-semibold">{headline.text}</p>
  const labels = headline.reasons.map(code => schedulerReason(code))
  const known = [...new Set(labels.flatMap(item => (item.code ? [] : [item.label])))]
  const unknown = labels.flatMap(item => (item.code ? [item.code] : []))
  return (
    <p className="text-base font-semibold">
      {known.join(", ")}
      {unknown.length > 0 && <>{known.length > 0 && ", "}<Hint text={unknown.join(", ")} focusable>{`기타 ${unknown.length}건`}</Hint></>}
    </p>
  )
}

type Plan = { label: string; plan: SchedulerCandidate; diff: number | null }
function Summary({ decision, name }: { decision: SchedulerDecision; name: Names }) {
  const target = changedTarget(decision)
  const saving = savingJoules(decision)
  const plans: Plan[] = [{ label: "유지안", plan: decision.keep, diff: null }]
  if (target) plans.push({ label: "변경안", plan: target, diff: saving == null ? null : -saving })
  const actions = (target?.actions ?? []) as PlannedAction[]
  return (
    <Section card title={formatKst(decision.at)} actions={<>
      <Link href={`/scheduler/actions?at=${encodeURIComponent(decision.at)}`} className={buttonClassName({ variant: "ghost", size: "sm" })}>작업 보기</Link>
      <StatusBadge status={decisionStatus(recordResult(decision))} />
    </>}>
      <div className="grid gap-4">
        <Headline decision={decision} />
        <DataTable label="유지안과 변경안" rows={plans} rowKey={row => row.label} pageSize={0} density="compact" columns={[
          { key: "plan", header: "후보", cell: row => row.label },
          { key: "status", header: "판정", cell: row => <StatusDot status={candidateStatus(row.plan.status)} showLabel /> },
          { key: "energy", header: "예상 에너지", unit: "J", numeric: true, missingReason: "판정 불가", cell: row => row.plan.expectedJoules == null ? null : formatNumber(row.plan.expectedJoules, 1) },
          { key: "diff", header: "차이", unit: "J", numeric: true, missing: row => (row.label === "유지안" ? "기준" : "판정 불가"), cell: row => row.diff == null ? null : `${row.diff > 0 ? "+" : ""}${formatNumber(row.diff, 1)}` },
          {
            key: "over", header: "목표 초과", unit: "개", numeric: true, missingReason: "예측 없음",
            cell: row => row.plan.slo?.length ? formatNumber(row.plan.slo.filter(estimate => estimate.normalizedExceedance > 0).length) : null,
          },
        ]} />
        {target && (
          <Section title="변경 작업">
            <Feed label="변경 작업" empty="작업 없음" items={actions.map(action => ({
              key: action.id, title: actionKindLabel(action.kind) ?? "—", secondary: action.node.name || undefined,
              meta: action.workloadUID ? name(action.workloadUID) : undefined,
            }))} />
          </Section>
        )}
      </div>
    </Section>
  )
}

function Latency({ decision, name }: { decision: SchedulerDecision; name: Names }) {
  const target = changedTarget(decision)
  const latency = latencyRows(decision).sort((a, b) => (name(a.uid) ?? a.uid).localeCompare(name(b.uid) ?? b.uid))
  const columns: Column<LatencyRow>[] = [
    { key: "service", header: "서비스", width: "34%", cell: row => <Service uid={row.uid} name={name} /> },
    { key: "percentile", header: "분위수", cell: row => row.percentile == null ? null : `p${row.percentile}` },
    { key: "target", header: "목표", unit: "ms", numeric: true, cell: row => formatNumber(row.targetMs, 1) },
    { key: "keep", header: "유지안", unit: "ms", numeric: true, missingReason: "예측 불가", cell: row => row.keepMs == null ? null : formatNumber(row.keepMs, 1) },
    ...(target ? [{ key: "change", header: "변경안", unit: "ms", numeric: true, missingReason: "예측 불가", cell: (row: LatencyRow) => row.changeMs == null ? null : formatNumber(row.changeMs, 1) }] : []),
  ]
  return (
    <DataTable title="서비스별 예상 지연" label="서비스별 예상 지연" rows={latency} rowKey={row => row.uid} pageSize={0} columns={columns}
      empty={<EmptyState compact title="예상 지연 없음" />} />
  )
}

type DemandRow = { uid: string; received: number | null; accepted: number | null; inputs: number | null; planned: number | null }
function Inputs({ decision, name }: { decision: SchedulerDecision; name: Names }) {
  const s = decision.search
  const rows = new Map<string, DemandRow>()
  const row = (uid: string) => rows.get(uid) ?? rows.set(uid, { uid, received: null, accepted: null, inputs: null, planned: null }).get(uid)!
  for (const load of s?.loads ?? []) Object.assign(row(load.uid), { received: load.received, accepted: load.accepted, inputs: load.inputsPerSecond })
  for (const workload of decision.capacity?.workloads ?? []) row(workload.uid).planned = workload.rate
  const list = [...rows.values()].sort((a, b) => (name(a.uid) ?? a.uid).localeCompare(name(b.uid) ?? b.uid))
  return (
    <Section title="입력 수요">
      <div className="grid gap-3">
        <Section card>
          <Fields columns={3} items={[
            { label: "입력 구간", value: s ? `${formatKst(s.windowStart).slice(11)} ~ ${formatKst(s.windowEnd).slice(11)}` : null, reason: "기록 없음" },
            { label: "받은 요청", value: s ? `${formatNumber(s.received)}건` : null, reason: "기록 없음" },
            { label: "접수 요청", value: s ? `${formatNumber(s.accepted)}건` : null, reason: "기록 없음" },
          ]} />
        </Section>
        <DataTable label="서비스별 입력 수요" rows={list} rowKey={item => item.uid} pageSize={0} empty={<EmptyState compact title="입력 없음" />} columns={[
          { key: "service", header: "서비스", width: "32%", cell: item => <Service uid={item.uid} name={name} /> },
          { key: "received", header: "받은 요청", unit: "건", numeric: true, cell: item => item.received == null ? null : formatNumber(item.received) },
          { key: "accepted", header: "접수", unit: "건", numeric: true, cell: item => item.accepted == null ? null : formatNumber(item.accepted) },
          { key: "inputs", header: "입력률", unit: "개/s", numeric: true, cell: item => item.inputs == null ? null : formatNumber(item.inputs, 2) },
          { key: "planned", header: "계획 수요", unit: "req/s", numeric: true, missingReason: "용량 계산 없음", cell: item => item.planned == null ? null : formatNumber(item.planned, 2) },
        ]} />
      </div>
    </Section>
  )
}

function Autonomy({ workloads, name }: { workloads: SchedulerWorkloadState[]; name: Names }) {
  const rows = [...workloads].sort((a, b) => (name(a.uid) ?? a.uid).localeCompare(name(b.uid) ?? b.uid))
  return (
    <DataTable title="서비스별 자동 제어" label="서비스별 자동 제어" rows={rows} rowKey={row => row.uid} pageSize={0} empty={<EmptyState compact title="서비스 없음" />} columns={[
      { key: "service", header: "서비스", width: "40%", cell: row => <Service uid={row.uid} name={name} /> },
      { key: "state", header: "상태", cell: row => <StatusDot status={autonomyState(row.state)} showLabel /> },
      { key: "reason", header: "사유", missingReason: "사유 없음", cell: row => row.reason ? <Reason code={row.reason} /> : null },
    ]} />
  )
}

function Capacity({ decision }: { decision: SchedulerDecision }) {
  const capacity = decision.capacity
  if (!capacity) return <Section title="용량 근거"><EmptyState compact title="용량 근거 없음" className="rounded-xl border border-dashed border-border" /></Section>
  const search = capacity.search
  const strategy = search?.strategy
  const known = schedulerOptions.some(option => option.value === strategy)
  return (
    <Section title="용량 근거">
      <div className="grid gap-3">
        <Section card>
          <Fields columns={3} items={[
            { label: "스케줄러", value: known ? schedulerLabel(strategy) : null, reason: strategy ? `코드 ${strategy}` : "기록 없음" },
            { label: "평가한 구성", value: search ? `${formatNumber(search.evaluated)}개` : null, reason: "기록 없음" },
            { label: "탐색 시간", value: search ? formatDurationNanos(search.durationNanos) : null, reason: "기록 없음" },
            { label: "평가 예산 소진", value: search?.budgetCut == null ? null : search.budgetCut ? "예" : "아니요", reason: "기록 없음" },
            { label: "에너지 비교 기간", value: formatDurationNanos(capacity.periodNanos) },
            { label: "분배 계산 기간", value: formatDurationNanos(capacity.routeNanos) },
          ]} />
        </Section>
        <DataTable<SchedulerNodePlan> label="장치별 계획" rows={capacity.plans ?? []} rowKey={plan => `${plan.node.uid}/${plan.key}`} pageSize={0}
          empty={<EmptyState compact title="계획 없음" />} columns={[
            { key: "node", header: "장치", cell: plan => plan.node.name || null },
            { key: "eligible", header: "후보", cell: plan => <StatusDot status={plan.eligible ? { label: "포함", tone: "ok" } : { label: "빠짐", tone: "idle" }} showLabel /> },
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
              <MeterBar value={phase.nanos} max={total} width="full" tone={phase.part ? "idle" : "progress"} label={`${phase.label} ${formatDurationNanos(phase.nanos)}`} />
            </li>
          ))}
        </ul>
      </div>
    </Section>
  )
}
