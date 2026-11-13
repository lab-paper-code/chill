// Evaluation run records (v1/evaluations, v1/evaluations/<name>) shaped for the run screens. Pure.
import type { EvaluationProgressEvent } from "./evaluation-active.ts"
import type { EvaluationLiveTarget, EvaluationSpec } from "./evaluation-request.ts"
import { object, records } from "./evaluation-author.ts"
import { formatDurationNanos } from "./format.ts"
import { settingControls, settingFields } from "./scheduler-settings.ts"
import { schedulerLabel } from "./scheduler-selection.ts"
import { patchKeyLabel } from "./experiments-run-editor.ts"
import { evaluationCleanup, evaluationPool, evaluationReason, type StatusInfo } from "./status-labels.ts"

export type RunCondition = { type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }
export type RunProcedure = { step: string; startedAt: string | null; finishedAt: string | null; reason?: string; measurementVerdict?: string }
export type LoadJob = { name: string; uid: string; status: string; startedAt?: string; completedAt?: string; reason?: string }
export type RunStage = {
  id: string; name: string; uid: string; currentProcedure?: string
  conditions?: RunCondition[] | null; procedures?: RunProcedure[] | null
  startedAt?: string | null; finishedAt?: string | null; goAt?: string | null
  measurementVerdict?: string; progress?: EvaluationProgressEvent[] | null; lastProgressAt?: string | null
  loadJobs?: LoadJob[] | null; provisionalD1?: Record<string, unknown> | null
}
export type Run = {
  name: string; uid: string; spec: EvaluationSpec; phase: string
  liveTarget?: EvaluationLiveTarget[] | null; stopRequested?: { at: string; by: string } | null
  conditions?: RunCondition[] | null; currentStage?: unknown; currentProcedure?: string
  procedures?: RunProcedure[] | null; stages?: RunStage[] | null
  startedAt?: string | null; finishedAt?: string | null; publicationID?: string
}

export const runPath = (name: string) => `v1/evaluations/${encodeURIComponent(name)}`
export const runHref = (run: { name: string; uid: string }) => `/experiments/runs/${encodeURIComponent(run.name)}?uid=${encodeURIComponent(run.uid)}`
export const condition = (list: RunCondition[] | null | undefined, type: string) => list?.find(item => item.type === type)
export const isFinished = (run: Pick<Run, "phase" | "finishedAt">) => run.phase === "Done" || !!run.finishedAt
/** Planned stage ids in run order. */
export const specStages = (spec: EvaluationSpec) => records(spec.stages).flatMap(stage => typeof stage.id === "string" ? [stage.id] : [])

/** Position of the current stage in the plan ("2/3"); 0 when the run ended before its first stage; null before the start. */
export function stageProgress(run: Pick<Run, "spec" | "currentStage" | "startedAt">): { index: number; total: number; id: string | null } | null {
  if (!run.startedAt) return null
  const ids = specStages(run.spec), id = typeof run.currentStage === "string" ? run.currentStage : null
  return { index: id ? ids.indexOf(id) + 1 : 0, total: ids.length, id }
}

/** Cleanup state of a run in one word: the pool while held, then residual cleanup, then how the pool came back. */
export function cleanupState(run: Pick<Run, "conditions">): { status: StatusInfo; reason?: string } | null {
  const pool = condition(run.conditions, "PoolAcquired"), cleanup = condition(run.conditions, "CleanupComplete")
  if (pool?.status !== "True" && cleanup && cleanup.status !== "True") return { status: evaluationCleanup(cleanup), reason: reasonText(cleanup.reason) }
  return pool ? { status: evaluationPool(pool), reason: reasonText(pool.reason) } : null
}
/** Tooltip text of a reason code: its label, or the code when the label is unknown. */
export function reasonText(code: string | null | undefined): string | undefined {
  if (!code) return undefined
  const reason = evaluationReason(code)
  return reason.code ? `코드 ${reason.code}` : reason.label
}

export const injectionActions: Record<string, string> = { "delete-serving-pod": "서비스 파드 삭제", "cordon-delete": "노드 차단 후 파드 삭제", uncordon: "노드 차단 해제" }

// Step strip ---------------------------------------------------------------

export type StepState = "done" | "failed" | "running" | "stalled" | "waiting"
export type Step = { key: string; label: string; state: StepState; at: string | null; reason?: string }
const okReasons = new Set(["ProcedureComplete", "Complete", "NoControlEffects", "MeasurementComplete"])
function step(key: string, label: string, start: string | null | undefined, end: string | null | undefined, ended: boolean, reason: string | undefined, active: boolean): Step {
  const state: StepState = ended ? (reason && !okReasons.has(reason) ? "failed" : "done") : start ? (active ? "running" : "stalled") : "waiting"
  return { key, label, state, at: start ?? end ?? null, reason }
}
const last = (list: RunProcedure[] | null | undefined, name: string) => list?.findLast(item => item.step === name)
const procedureStep = (list: RunProcedure[] | null | undefined, key: string, label: string, active: boolean) => {
  const p = last(list, key)
  return step(key, label, p?.startedAt, p?.finishedAt, !!p?.finishedAt, p?.reason, active)
}

/** Run procedure: 준비, every planned stage, 정리, 게시. */
export function runSteps(run: Run): Step[] {
  const active = !isFinished(run)
  const stages = specStages(run.spec).map(id => {
    const stage = run.stages?.find(item => item.id === id)
    const failed = stage?.procedures?.find(p => p.reason && !okReasons.has(p.reason))
    return step(`stage:${id}`, id, stage?.startedAt, stage?.finishedAt, !!stage?.finishedAt, failed?.reason, active)
  })
  return [procedureStep(run.procedures, "prepare", "준비", active), ...stages, procedureStep(run.procedures, "finish", "정리", active), procedureStep(run.procedures, "publish", "게시", active)]
}

/** One stage: its procedures with the recorded load boundaries (부하, 배출, 기록, 수집) between them. */
export function stageSteps(stage: RunStage | undefined, active: boolean): Step[] {
  const events = stage?.progress ?? []
  const at = (phase: EvaluationProgressEvent["phase"]) => events.findLast(event => event.phase === phase)?.at ?? null
  const load = last(stage?.procedures, "stage")
  // A boundary without its own end is over once the enclosing load procedure has finished.
  const over = !!load?.finishedAt
  const loadFailed = over && load?.reason && !okReasons.has(load.reason) ? load.reason : undefined
  const bounds: [string, string, string | null, string | null][] = [
    ["load", "부하", at("loadStarted") ?? load?.startedAt ?? null, at("loadEnded")],
    ["drain", "배출", at("drainStarted"), at("drainEnded")],
    ["flush", "기록", at("flushStarted"), at("collectStarted")],
    ["collect", "수집", at("collectStarted"), at("collectEnded")],
  ]
  const boundarySteps = bounds.map(([key, label, start, end]) => step(key, label, start, end, !!end || (over && !!start), end ? undefined : loadFailed, active))
  const judge = last(stage?.procedures, "judge")
  return [
    procedureStep(stage?.procedures, "factor", "조건 적용", active),
    ...boundarySteps,
    step("judge", "판정", at("judgeStarted") ?? judge?.startedAt, at("judgeEnded") ?? judge?.finishedAt, !!(at("judgeEnded") ?? judge?.finishedAt), judge?.reason, active),
    procedureStep(stage?.procedures, "cleanup-jobs", "작업 정리", active),
  ]
}

// Provisional per-service results -------------------------------------------

export type ProvisionalRow = { name: string; successRate: number | null; goodput: number | null; latencyMs: number | null; targetMs: number | null; percentile: number | null; offered: number | null }
const num = (value: unknown): number | null => typeof value === "number" && Number.isFinite(value) ? value : typeof value === "bigint" ? Number(value) : null
/** Server values of `provisionalD1.workloads`; latency at the service's own SLO percentile. */
export function provisionalRows(provisional: unknown): ProvisionalRow[] {
  return Object.entries(object(object(provisional).workloads)).map(([name, value]) => {
    const row = object(value), slo = object(row.slo), percentile = num(slo.percentile)
    const latency = percentile === null ? null : num(object(row.completedLatencyNS)[`p${percentile}`])
    const target = num(slo.targetLatencyNanos)
    return { name, successRate: num(row.onTimeSuccessRate), goodput: num(row.goodputRequestsPerSecond), latencyMs: latency === null ? null : latency / 1e6, targetMs: target === null ? null : target / 1e6, percentile, offered: num(row.offered) }
  }).sort((a, b) => a.name.localeCompare(b.name))
}

// Plan ---------------------------------------------------------------------

export type ScenarioSource = { name: string; uid: string; spec: Record<string, unknown> }
export type PlanRow = { key: string; stage: string; scenario: { name: string; uid: string } | null; part: string | null; seconds: number | null; rates: Record<string, number> }
/** One row per stage and load part; a stage whose scenario is not found keeps one row without parts. */
export function planRows(spec: EvaluationSpec, scenarios: readonly ScenarioSource[]): { rows: PlanRow[]; services: string[] } {
  const rows = records(spec.stages).flatMap((stage, index): PlanRow[] => {
    const id = typeof stage.id === "string" ? stage.id : `${index + 1}`
    const ref = object(stage.loadScenarioRef)
    const found = scenarios.find(item => item.uid === ref.uid)
    const scenario = typeof ref.name === "string" && typeof ref.uid === "string" ? { name: ref.name, uid: ref.uid } : null
    const parts = found ? records(found.spec.parts) : []
    if (!parts.length) return [{ key: `${id}`, stage: id, scenario, part: null, seconds: null, rates: {} }]
    return parts.map((part, partIndex) => ({
      key: `${id}/${partIndex}`, stage: id, scenario, part: typeof part.id === "string" ? part.id : null, seconds: num(part.durationSeconds),
      rates: Object.fromEntries(Object.entries(object(part.rates)).flatMap(([service, rate]) => num(rate) === null ? [] : [[service, num(rate)!]])),
    }))
  })
  return { rows, services: [...new Set(rows.flatMap(row => Object.keys(row.rates)))].sort() }
}

/** Service names by UID from the scenarios' workload snapshots (settings patches store UIDs). */
export function serviceNames(scenarios: readonly ScenarioSource[], extra: readonly { name: string; uid: string }[] = []): Map<string, string> {
  const pairs = [...scenarios.flatMap(item => records(item.spec.workloads)), ...extra]
  return new Map(pairs.flatMap(item => typeof item.uid === "string" && typeof item.name === "string" ? [[item.uid, item.name] as [string, string]] : []))
}

export type SettingRow = { key: string; label: string | null; values: (string | null)[] }
const onOff = (value: unknown) => value === true ? "켜짐" : value === false ? "꺼짐" : String(value)
const plain = (value: unknown) => typeof value === "number" || typeof value === "bigint" ? Number(value).toLocaleString("ko-KR", { maximumFractionDigits: 6 }) : typeof value === "string" ? value : JSON.stringify(value)
function settingEntries(patch: Record<string, unknown>, name: (uid: string) => string): [string, string | null, string][] {
  const list = (value: unknown, map = (item: string) => item) => Array.isArray(value) && value.length ? value.map(item => map(String(item))).join(", ") : "없음"
  return Object.entries(patch).flatMap(([key, value]): [string, string | null, string][] => {
    if (key === "controls") {
      const controls = Object.entries(object(value))
      // Every switch set the same way reads as one row.
      if (controls.length === settingControls.length && controls.every(([, on]) => on === controls[0][1])) return [["controls", "자동 조정 항목", `모두 ${onOff(controls[0][1])}`]]
      return controls.map(([control, on]) => [`controls.${control}`, settingControls.find(item => item.key === control)?.label ?? null, onOff(on)])
    }
    if (key === "minimumDemand") return Object.entries(object(value)).map(([uid, rate]) => [`minimumDemand.${uid}`, `${patchKeyLabel(key)} ${name(uid)}`, `${plain(rate)} req/s`])
    if (key === "autonomousWorkloads" || key === "workloadPriority") return [[key, patchKeyLabel(key), list(value, name)]]
    if (key === "excludedNodes") return [[key, patchKeyLabel(key), list(value)]]
    if (key === "sloPriorityStrategy") return [[key, patchKeyLabel(key), value === "workload" ? "서비스 순위" : "목표 초과 정도"]]
    const field = settingFields.find(item => item.key === key)
    if (!field) return [[key, null, plain(value)]]
    const text = typeof value === "boolean" ? onOff(value) : key === "planSearchStrategy" && typeof value === "string" ? schedulerLabel(value) : key.endsWith("Nanos") ? formatDurationNanos(value as number | bigint) : `${plain(value)}${field.unit ? ` ${field.unit}` : ""}`
    return [[key, field.label, text]]
  })
}
/** One row per changed setting, one value per scope (run-wide patch, then each stage); null where a scope leaves it unset. */
export function settingRows(scopes: readonly (Record<string, unknown> | null | undefined)[], name: (uid: string) => string): SettingRow[] {
  const rows = new Map<string, SettingRow>()
  scopes.forEach((patch, index) => {
    for (const [key, label, text] of settingEntries(object(patch), name)) {
      const row = rows.get(key) ?? { key, label, values: scopes.map(() => null) }
      row.values[index] = text
      rows.set(key, row)
    }
  })
  return [...rows.values()]
}
