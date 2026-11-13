// Overview screen rows and side lists from the live state. Only sums and counts of server values.
import { isLiveActionActive, type LiveEvent, type LiveMetric, type LiveMetrics, type LiveSLOWindow, type LiveSnapshot } from "./live.ts"
import { belongsToPool, isActionFailure } from "./live-display.ts"
import { actionKindLabel } from "./scheduler-actions.ts"
import { decisionStatus, serviceReadiness, type StatusTone } from "./status-labels.ts"
import { DASH } from "./format.ts"
import type { ActiveEvaluation } from "./evaluation-active.ts"
import type { SchedulerObservationSummary } from "./read-types.ts"

export type ServiceState = "attention" | "active" | "idle"
export const serviceStates: Record<ServiceState, string> = { attention: "확인 필요", active: "가동", idle: "유휴" }
export const TREND_SECONDS = 60
export const FAILURE_SECONDS = 10

export type OverviewRow = {
  uid: string; name: string; state: ServiceState; tone: StatusTone; reason: string | null
  /** Newest bucket's received count (req/s); null when that bucket lacks the service. */
  received: number | null
  /** Received per second over the last TREND_SECONDS buckets; absent seconds are null. */
  trend: (number | null)[]
  slo: LiveSLOWindow | undefined
  /** completed / (completed + failed) over the trend window; null when nothing finished. */
  success: number | null
  devices: string[]
}

/** The last `seconds` per-second buckets ending at the newest one; missing seconds stay null. */
export function lastBuckets(metrics: LiveMetrics[], seconds: number): (LiveMetrics | null)[] {
  const last = metrics.at(-1)
  if (!last) return []
  const second = (at: string) => Math.floor(Date.parse(at) / 1000)
  const end = second(last.bucketAt), bySecond = new Map(metrics.map(row => [second(row.bucketAt), row]))
  return Array.from({ length: seconds }, (_, index) => bySecond.get(end - seconds + 1 + index) ?? null)
}

const total = (rows: (LiveMetric | null)[], key: "received" | "completed" | "failed") => rows.reduce((sum, row) => sum + (row?.[key] ?? 0), 0)

/** One row per service in name order, so live updates never reorder the table. */
export function overviewRows(snapshot: LiveSnapshot, metrics: LiveMetrics[]): OverviewRow[] {
  const buckets = lastBuckets(metrics, TREND_SECONDS), latest = metrics.at(-1)
  return snapshot.workloads.map((workload): OverviewRow => {
    const series = buckets.map(bucket => bucket?.workloads[workload.uid] ?? null)
    const completed = total(series, "completed"), failed = total(series, "failed")
    const recentFailed = total(series.slice(-FAILURE_SECONDS), "failed")
    const slo = latest?.workloads[workload.uid]?.slo
    const known = serviceReadiness(workload.readiness.reason), readiness = known.label === DASH ? "준비 안 됨" : known.label
    const changing = snapshot.actions.some(action => action.workloadUID === workload.uid && isLiveActionActive(action))
    const [reason, tone]: [string | null, StatusTone] =
      slo?.met === false ? ["목표 초과", "fail"]
      : recentFailed > 0 ? [`최근 ${FAILURE_SECONDS}초 실패 ${recentFailed}건`, "fail"]
      : workload.admission && !workload.readiness.ready ? [readiness, "warn"]
      : changing ? ["변경 진행 중", "warn"]
      : [null, "ok"]
    const serving = snapshot.instances.some(instance => !instance.stopped && belongsToPool(instance, workload.uid))
    const state: ServiceState = reason ? "attention" : serving || total(series, "received") > 0 ? "active" : "idle"
    const devices = [...new Set(snapshot.instances.filter(instance => !instance.stopped && belongsToPool(instance, workload.uid)).map(instance => instance.node.name))].sort()
    return {
      uid: workload.uid, name: workload.name, state, tone: state === "idle" ? "idle" : tone, reason,
      received: latest?.workloads[workload.uid]?.received ?? null,
      trend: series.map(row => row?.received ?? null),
      slo, success: completed + failed > 0 ? completed / (completed + failed) : null, devices,
    }
  }).sort((a, b) => a.name.localeCompare(b.name))
}

/** A line of the side column; `at` shows as relative time, `meta` as a short value. */
export type FeedItem = { key: string; tone: StatusTone; label: string; target?: string; at?: string; meta?: string; href: string }

const actionHref = (action: { id: string; startedAt: string }) =>
  `/scheduler/actions?${new URLSearchParams({ kind: "actions", at: action.startedAt, action: action.id })}`

/** Work in progress, grouped by kind in a fixed order. */
export function progressItems(snapshot: LiveSnapshot, evaluations: ActiveEvaluation[]): FeedItem[] {
  const measuring = snapshot.nodes.filter(node => node.measurement).sort((a, b) => a.name.localeCompare(b.name))
  const leased = new Set(measuring.map(node => node.uid))
  const names = new Map(snapshot.workloads.map(row => [row.uid, row.name]))
  return [
    ...snapshot.actions.filter(isLiveActionActive).map(action => ({
      key: `action:${action.id}`, tone: "progress" as const, label: actionKindLabel(action.kind) ?? "작업",
      target: names.get(action.workloadUID) ? `${action.node.name}, ${names.get(action.workloadUID)}` : action.node.name, at: action.startedAt, href: actionHref(action),
    })),
    // A measurement lease is the device row below; list only the other reservations.
    ...snapshot.reservations.filter(row => !row.nodes.length || row.nodes.some(node => !leased.has(node.uid))).map(row => ({
      key: `reservation:${row.owner}`, tone: "progress" as const, label: "자원 예약",
      target: row.nodes.map(node => node.name).join(", ") || undefined, href: "/scheduler/actions?kind=reservations",
    })),
    ...measuring.map(node => ({ key: `measurement:${node.uid}`, tone: "progress" as const, label: "측정 중", target: node.name, href: `/devices/${encodeURIComponent(node.name)}` })),
    ...evaluations.map(run => ({
      key: `evaluation:${run.uid}`, tone: "progress" as const, label: "실험", target: run.name, meta: `단계 ${run.stageIndex}/${run.stagesTotal}`,
      href: `/experiments/runs/${encodeURIComponent(run.name)}`,
    })),
  ]
}

/** Changed decisions, failed actions and settings changes, newest first; cluster events only as a count. */
export function changeItems(events: LiveEvent[], settings: SchedulerObservationSummary[], limit = 8): { items: FeedItem[]; cluster: number } {
  const items: FeedItem[] = []
  for (const event of events) {
    if (event.kind === "decision" && event.decision.state !== "Keep" && event.decision.state !== "Pending") {
      const status = decisionStatus(event.decision.state)
      items.push({ key: `decision:${event.at}`, tone: status.tone, label: status.code ? "판단 변경" : `판단 ${status.label}`, at: event.at, href: `/scheduler/decisions?${new URLSearchParams({ at: event.decision.at })}` })
    } else if (event.kind === "action" && isActionFailure(event.action)) {
      items.push({ key: `action:${event.action.id}`, tone: "fail", label: `${actionKindLabel(event.action.kind) ?? "작업"} 실패`, target: event.action.node.name, at: event.at, href: actionHref(event.action) })
    }
  }
  for (const row of settings) if (row.settings) {
    items.push({ key: `settings:${row.at}`, tone: row.settings.error ? "fail" : "progress", label: row.settings.error ? "설정 변경 실패" : "설정 변경", target: row.settings.actorUsername || undefined, at: row.at, href: `/scheduler/settings?${new URLSearchParams({ history: row.at })}` })
  }
  items.sort((a, b) => Date.parse(b.at!) - Date.parse(a.at!))
  return { items: items.slice(0, limit), cluster: events.filter(event => event.kind === "cluster").length }
}
