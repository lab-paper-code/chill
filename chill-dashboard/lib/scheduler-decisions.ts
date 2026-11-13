// Scheduler decision screen helpers: record paths, tick timing rows and the two-decision comparison.
import type { SchedulerCandidate, SchedulerDecision, SchedulerWorkloadState, WireInteger } from "./read-types.ts"
import { decisionRange } from "./scheduler-history.ts"
import { formatKst } from "./format.ts"

/** internal/serving/observation SchedulerControlObservation, as served in every view. */
export type ControlObservation = {
  scope: string; tickAt?: string; decisionAt: string; startedAt: string; finishedAt: string; elapsedNanos: WireInteger; phase: string
  status?: string; targetID?: string; fallback?: string; error?: string
  lockWaitNanos: WireInteger; captureNanos: WireInteger; windowNanos: WireInteger; refreshListNanos: WireInteger; sourceProbeNanos: WireInteger; restNanos: WireInteger
  evaluateNanos: WireInteger; publishNanos: WireInteger; recordNanos: WireInteger; launchNanos: WireInteger
  evaluation?: { preparationNanos: WireInteger; searchNanos: WireInteger; paybackNanos: WireInteger; ratesNanos: WireInteger; projectionNanos: WireInteger }
}
/** A view=summary row. `reasons` and `targetKeep` are absent on older servers. */
export type DecisionRow = {
  at: string; offset?: WireInteger; control?: ControlObservation
  decision?: { at: string; status: string; workloads?: SchedulerWorkloadState[] | null; reasons?: string[] | null; targetKeep?: boolean }
}
export type DecisionPage = { from: string; to: string; events: DecisionRow[] | null; nextCursor?: string; gapUntil?: string }

const query = (params: Record<string, string>) => new URLSearchParams(params).toString()

/** One page of the decision list, newest first; `changes` selects decision-change. */
export function decisionListPath(window: { from: number; to: number }, changes: boolean, uid: string, limit: number, cursor: number): string {
  return `v1/observations/scheduler?${query({
    view: "summary", filter: changes ? "decision-change" : "decision", order: "desc",
    from: new Date(window.from).toISOString(), to: new Date(window.to).toISOString(), limit: String(limit),
    ...(uid ? { uid } : {}), ...(cursor ? { cursor: String(cursor) } : {}),
  })}`
}

/** The full (compact) record of one decision; null for a malformed time. */
export function decisionRecordPath(at: string): string | null {
  const range = decisionRange(at)
  return range && `v1/observations/scheduler?${query({ ...range, filter: "decision", limit: "5" })}`
}

/** Control rows of the tick that made a decision: it is recorded when the tick finishes, within one interval. */
export function decisionControlPath(at: string, intervalNanos: WireInteger): string | null {
  const range = decisionRange(at), start = Date.parse(at)
  if (!range || !Number.isFinite(start)) return null
  return `v1/observations/scheduler?${query({ view: "summary", filter: "other", order: "asc", from: range.from, to: new Date(start + Number(intervalNanos) / 1e6 + 1000).toISOString(), limit: "6" })}`
}

/** Recent control rows; `to` runs one hour past the mount so polling sees new ticks under one query key. */
export function latestControlPath(mountedAt: number): string {
  return `v1/observations/scheduler?${query({ view: "summary", filter: "other", order: "desc", from: new Date(mountedAt - 300_000).toISOString(), to: new Date(mountedAt + 3_600_000).toISOString(), limit: "10" })}`
}

export const controlFor = (page: DecisionPage | undefined, at: string): ControlObservation | undefined =>
  page?.events?.find(row => row.control?.decisionAt === at)?.control

/** The newest tick that produced a decision. */
export const latestTick = (page: DecisionPage | undefined): ControlObservation | undefined =>
  page?.events?.find(row => row.control && row.control.scope !== "round" && !row.control.decisionAt.startsWith("0001-"))?.control

export type Phase = { key: string; label: string; nanos: number; part: boolean }
/** Tick phases in order, each followed by its parts. Zero rows are dropped. */
export function tickPhases(control: ControlObservation): Phase[] {
  const e = control.evaluation
  const rows: [string, string, WireInteger | undefined][] = [
    ["lock", "잠금 대기", control.lockWaitNanos],
    ["capture", "상태 수집", control.captureNanos],
    ["capture.refresh", "목록 갱신", control.refreshListNanos],
    ["capture.window", "관측 구간 읽기", control.windowNanos],
    ["capture.probe", "출처 확인", control.sourceProbeNanos],
    ["capture.rest", "기타", control.restNanos],
    ["evaluate", "판단", control.evaluateNanos],
    ["evaluate.preparation", "준비", e?.preparationNanos],
    ["evaluate.search", "구성 탐색", e?.searchNanos],
    ["evaluate.payback", "전환 비용", e?.paybackNanos],
    ["evaluate.rates", "요청률 검사", e?.ratesNanos],
    ["evaluate.projection", "지연 예측", e?.projectionNanos],
    ["publish", "게시", control.publishNanos],
    ["record", "기록", control.recordNanos],
    ["launch", "작업 시작", control.launchNanos],
  ]
  return rows.map(([key, label, value]) => ({ key, label, nanos: value == null ? 0 : Number(value), part: key.includes(".") })).filter(row => row.nanos > 0)
}

/** The plan the decision moves to; null when it keeps the current one. */
export const changedTarget = (decision: SchedulerDecision): SchedulerCandidate | null =>
  decision.target && !decision.target.keep && decision.target.id !== decision.keep.id ? decision.target : null

export type LatencyRow = { uid: string; percentile: number | null; targetMs: number | null; keepMs: number | null; changeMs: number | null }
/** Expected latency per service under the keep plan and the changed plan, in ms. */
export function latencyRows(decision: SchedulerDecision): LatencyRow[] {
  const rows = new Map<string, LatencyRow>()
  for (const [plan, key] of [[decision.keep, "keepMs"], [changedTarget(decision), "changeMs"]] as const) {
    for (const estimate of plan?.slo ?? []) {
      const row = rows.get(estimate.uid) ?? { uid: estimate.uid, percentile: null, targetMs: null, keepMs: null, changeMs: null }
      row.percentile = estimate.percentile
      row.targetMs = Number(estimate.targetNanos) / 1e6
      row[key] = estimate.expectedNanos == null ? null : estimate.expectedNanos / 1e6
      rows.set(estimate.uid, row)
    }
  }
  return [...rows.values()]
}

/** Status code of a decision's result: a changed target reads as Feasible (변경), the keep target as Keep (유지). */
export const decisionResult = (status: string | undefined, targetKeep: boolean | undefined): string | undefined =>
  targetKeep === false ? "Feasible" : targetKeep === true ? "Keep" : status
/** decisionResult of a full record, where the target itself is present. */
export const recordResult = (d: SchedulerDecision): string | undefined => decisionResult(d.status, d.target ? !changedTarget(d) : undefined)

export type CompareRow = { key: string; label: string; unit?: string; digits?: number; a: number | string | null; b: number | string | null }
/** Field-by-field rows for two decisions; numbers are server values (the screen adds B minus A). */
export function compareRows(a: SchedulerDecision, b: SchedulerDecision, name: (uid: string) => string, status: (code: string | undefined) => string): CompareRow[] {
  const window = (d: SchedulerDecision) => d.search ? `${formatKst(d.search.windowStart).slice(11)} ~ ${formatKst(d.search.windowEnd).slice(11)}` : null
  const rows: CompareRow[] = [
    { key: "status", label: "결과", a: status(recordResult(a)), b: status(recordResult(b)) },
    { key: "window", label: "입력 구간", a: window(a), b: window(b) },
    { key: "received", label: "받은 요청", unit: "건", a: a.search?.received ?? null, b: b.search?.received ?? null },
    { key: "accepted", label: "접수 요청", unit: "건", a: a.search?.accepted ?? null, b: b.search?.accepted ?? null },
    { key: "keepJ", label: "유지 예상 에너지", unit: "J", digits: 1, a: a.keep.expectedJoules, b: b.keep.expectedJoules },
    { key: "changeJ", label: "변경 예상 에너지", unit: "J", digits: 1, a: changedTarget(a)?.expectedJoules ?? null, b: changedTarget(b)?.expectedJoules ?? null },
  ]
  // The latency of the plan each decision selected.
  const latency = (d: SchedulerDecision) => new Map(latencyRows(d).map(row => [row.uid, changedTarget(d) ? row.changeMs : row.keepMs]))
  const la = latency(a), lb = latency(b)
  for (const uid of [...new Set([...la.keys(), ...lb.keys()])].sort((x, y) => name(x).localeCompare(name(y))))
    rows.push({ key: `latency:${uid}`, label: `${name(uid)} 예상 지연`, unit: "ms", digits: 1, a: la.get(uid) ?? null, b: lb.get(uid) ?? null })
  return rows
}
