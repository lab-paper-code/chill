import type { SchedulerActionObservation, SchedulerObservationSummary } from "./read-types.ts"
import { actionStatus, type StatusInfo } from "./status-labels.ts"
import { formatDurationNanos } from "./format.ts"
import { liveReason, incidentDiagnosis } from "./live-display.ts"
import type { LiveCluster } from "./live.ts"

const kindLabels: Record<string, string> = {
  prepare: "실행 준비", route: "요청 분배 변경", unroute: "요청 분배 해제", release: "자원 예약 해제", reset: "서비스 초기화",
  stop: "요청 접수 중지", drain: "기존 요청 마무리", remove: "인스턴스 제거", power: "전력모드 변경", resize: "인스턴스 교체",
  "manual-static-routing": "설정한 요청 분배 적용",
  "measurement-post": "측정 요청", "measurement-delete": "측정 요청 삭제", "measurement-handback": "측정 장치 반환", "measurement-preflight": "측정 사전 검사",
}
/** Unknown kinds show "—" (null); the screen puts the code in the tooltip. */
export const actionKindLabel = (kind: string): string | null => kindLabels[kind] ?? null

const phases: Record<string, StatusInfo> = {
  Accepted: { label: "접수됨", tone: "progress" },
  validated: { label: "검증됨", tone: "ok" },
  NOT_SUBMITTED: { label: "미제출", tone: "idle" },
}
/** Scheduler action records carry the live vocabulary plus the measurement request phases. */
export const phaseStatus = (code: string): StatusInfo => phases[code] ?? actionStatus(code)

export type ActionRow = { id: string; kind: string; node: { uid: string; name: string }; startedAt: string; at: string; phase: string; event: SchedulerObservationSummary }
const finished = new Set(["complete", "completed", "failed", "partial", "cancel", "canceled"])

/** One row per action id: the newest record of the loaded page (order is newest first). */
export function groupActions(events: SchedulerObservationSummary[]): ActionRow[] {
  const seen = new Set<string>(), rows: ActionRow[] = []
  for (const event of events) {
    const a = event.action
    if (!a || seen.has(a.id)) continue
    seen.add(a.id)
    rows.push({ id: a.id, kind: a.kind, node: a.node, startedAt: a.startedAt, at: event.at, phase: a.phase, event })
  }
  return rows
}

/** Nanoseconds from start to the finishing record; null while the action is not finished. */
export const actionElapsedNanos = (row: Pick<ActionRow, "phase" | "startedAt" | "at">): number | null =>
  finished.has(row.phase) ? Math.max(0, Date.parse(row.at) - Date.parse(row.startedAt)) * 1e6 : null

type Obj = Record<string, unknown>
const obj = (v: unknown): Obj | undefined => (v !== null && typeof v === "object" && !Array.isArray(v) ? v as Obj : undefined)

/** Plan versus observed result of a full action record; empty when the record carries neither. */
export function planVsActual(action: SchedulerActionObservation): { label: string; plan: string | null; actual: string | null }[] {
  const op = obj(action.expected), result = obj(action.actual)
  const plan = (Array.isArray(obj(op?.target)?.actions) ? obj(op?.target)?.actions as unknown[] : []).map(obj).find(a => a?.id === action.id)
  const rows: { label: string; plan: string | null; actual: string | null }[] = []
  const planned = typeof plan?.elapsedNanos === "number" || typeof plan?.elapsedNanos === "bigint" ? formatDurationNanos(Number(plan.elapsedNanos)) : null
  const ended = typeof result?.endedAt === "string" ? result.endedAt : action.completedAt
  const took = ended && !ended.startsWith("0001-") ? formatDurationNanos(Math.max(0, Date.parse(ended) - Date.parse(action.startedAt)) * 1e6) : null
  if (planned || took) rows.push({ label: "소요", plan: planned, actual: took })
  if (action.kind === "power") {
    const after = obj(result?.actual)?.powerMode
    const to = typeof plan?.to === "string" ? plan.to : null
    if (to || typeof after === "string") rows.push({ label: "전력모드", plan: to, actual: typeof after === "string" ? after : null })
  }
  return rows
}

/** Cluster event row: kind, target and content in display words. */
export function clusterRow(c: LiveCluster): { kind: string; target: string | null; content: string } {
  const target = c.pod?.name ?? c.source ?? null
  if (c.kind === "issue") return { kind: "문제", target, content: `${liveReason(c.event)}${c.reason ? `: ${incidentDiagnosis(c.reason).reason}` : ""}` }
  return { kind: "파드", target, content: `${liveReason(c.event)}${c.pod?.phase ? `: ${liveReason(c.pod.phase)}` : ""}` }
}
