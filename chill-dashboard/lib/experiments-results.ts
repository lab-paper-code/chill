// Read model of one published experiment for the results screens. Server values only:
// unit conversion, differences and ratios of two published values, and sums of durations.
import { finite, object, type Experiment, type ExperimentStage, type Published } from "./experiments.ts"
import { comparisonValidity, definitionComparisons, durationMismatch, experimentRows, experimentServiceResults, publishedEnergy, resultMetric, stageSeconds } from "./experiment-comparison.ts"

export const resultName = (run: Experiment): string => run.definition?.run.name ?? run.facts?.name ?? run.id

/** Fraction digits for a millisecond value: 2 below 10 ms, 1 below 100 ms, else 0. */
export const latencyDigits = (value: number): number => Math.abs(value) < 10 ? 2 : Math.abs(value) < 100 ? 1 : 0
export const rejectionReasons: Record<string, string> = { QueueFull: "대기열 가득", DeadlineInfeasible: "기한 내 처리 불가" }
export const waitLocations: Record<string, string> = { gateway: "게이트웨이", executor: "실행기" }
export const energyScopes: Record<string, string> = { "declared-node-meters": "선언된 장치 계측기" }

const ms = (value: unknown, nanos: boolean): number | null => {
  const n = typeof value === "bigint" ? Number(value) : finite(value)
  return n === null ? null : nanos ? n / 1e6 : n
}
export type Quantiles = { p50: number | null; p95: number | null; p99: number | null }
const quantiles = (value: unknown, nanos = true): Quantiles => {
  const q = object(value)
  return { p50: ms(q.p50, nanos), p95: ms(q.p95, nanos), p99: ms(q.p99, nanos) }
}

export type ServiceSummary = {
  name: string
  verdict: string | null
  success: number | null
  goodput: number | null
  latency: Quantiles
  target: { percentile: number; ms: number } | null
  /** The published quantile that matches the target percentile. */
  targetQuantile: number | null
}
export function serviceSummaries(stage: ExperimentStage, run: Experiment): ServiceSummary[] {
  return experimentServiceResults(stage, run).map(({ row, verdict, appliedSLO, declaredSLO }) => {
    const request = row.source.startsWith("kpis.requests.")
    const latency = quantiles(row.original[request ? "completedLatencyNS" : "latencyMS"], request)
    const slo = appliedSLO ?? declaredSLO ?? object(row.original.slo)
    const percentile = finite(slo.percentile), target = ms(slo.targetLatencyNanos, true)
    const key = percentile === 50 ? "p50" : percentile === 95 ? "p95" : percentile === 99 ? "p99" : null
    return {
      name: row.name,
      verdict: typeof verdict === "string" ? verdict : null,
      success: finite(resultMetric(row, "success").value),
      goodput: finite(row.original.goodputRequestsPerSecond),
      latency,
      target: percentile !== null && target !== null ? { percentile, ms: target } : null,
      targetQuantile: key ? latency[key] : null,
    }
  })
}

/** Services whose published verdict is FAIL. */
export function failedServices(stage: ExperimentStage): string[] {
  const verdicts = object(object(object(stage.kpis.components).slo).workloads)
  return experimentRows(stage).filter(row => (verdicts[row.name] ?? row.original.D1) === "FAIL").map(row => row.name)
}

export type StageEnergy = { joules: number | null; partial: boolean; status: string | null; watts: number | null; drainComplete: boolean | null }
export function stageEnergy(stage: ExperimentStage): StageEnergy {
  const power = object(stage.kpis.power), { joules, partial } = publishedEnergy(power, stage.kpis.wholePoolJoules), seconds = stageSeconds(stage)
  const drain = object(object(stage.kpis.components).drain)
  return {
    joules, partial,
    status: typeof power.energyStatus === "string" ? power.energyStatus : null,
    watts: joules !== null && seconds ? joules / seconds : null,
    drainComplete: typeof drain.complete === "boolean" ? drain.complete : null,
  }
}

/** Candidate minus baseline whole-pool energy; partial measurements are not compared. */
export function energyDelta(stage: ExperimentStage, base: ExperimentStage): { joules: number; percent: number | null } | null {
  const a = stageEnergy(stage), b = stageEnergy(base)
  if (a.partial || b.partial || a.joules === null || b.joules === null) return null
  return { joules: a.joules - b.joules, percent: b.joules > 0 ? (100 * (a.joules - b.joules)) / b.joules : null }
}

export type StageRef = { run: Experiment; stage: ExperimentStage }
export const stageKey = (ref: StageRef): string => `${ref.run.id}/${ref.stage.id}`

/** Baselines a stage can be compared with: other stages of this run, then published stages the definition names. */
export function baselineChoices(run: Experiment, runs: Experiment[], stage: ExperimentStage): (StageRef & { label: string })[] {
  const own = run.stages.filter(other => other !== stage).map(other => ({ run, stage: other, label: other.id }))
  const historical = definitionComparisons(run, runs).filter(pair => pair.rightRun !== run).map(pair => ({ run: pair.rightRun, stage: pair.control, label: `${resultName(pair.rightRun)} / ${pair.control.id}` }))
  return [...own, ...historical].filter((item, index, all) => all.findIndex(other => stageKey(other) === stageKey(item)) === index)
}

/** The baseline the definition pairs with this stage, if it recorded one. */
export function recordedBaseline(run: Experiment, runs: Experiment[], stage: ExperimentStage): StageRef | null {
  const pair = definitionComparisons(run, runs).find(item => item.chill === stage)
  return pair ? { run: pair.rightRun, stage: pair.control } : null
}

export type ConditionWord = "같음" | "다름" | "미확인" | "미기록"
export type Conditions = { load: ConditionWord; identity: ConditionWord; rules: ConditionWord; duration: ConditionWord; limited: boolean }
export function comparisonConditions(candidate: StageRef, base: StageRef): Conditions {
  const validity = comparisonValidity(candidate.stage, base.stage, candidate.run, base.run)
  const a = stageSeconds(candidate.stage), b = stageSeconds(base.stage)
  return {
    ...(validity.conditions as Record<"load" | "identity" | "rules", ConditionWord>),
    duration: a === null || b === null ? "미확인" : durationMismatch(candidate.stage, base.stage) ? "다름" : "같음",
    limited: validity.limited,
  }
}

export type PartRow = {
  key: string; stage: string; part: string; from: number; to: number
  rate: number | null; p95: number | null; target: number | null; success: number | null; joules: number | null
}
/** One row per stage and load part for one service; a stage without parts is one row. Energy is published per stage. */
export function partRows(run: Experiment, service: string): PartRow[] {
  return [...run.stages].sort((a, b) => Date.parse(a.from) - Date.parse(b.from)).flatMap(stage => {
    const row = experimentRows(stage).find(item => item.name === service)
    if (!row) return []
    const summary = serviceSummaries(stage, run).find(item => item.name === service)
    const request = row.source.startsWith("kpis.requests.")
    const parts = Array.isArray(row.original.substages) ? row.original.substages.map(object) : []
    const energy = stageEnergy(stage).partial ? null : stageEnergy(stage).joules
    const start = Date.parse(stage.from), end = Date.parse(stage.to)
    if (!parts.length) return [{ key: stage.id, stage: stage.id, part: "", from: start, to: end, rate: null, p95: summary?.latency.p95 ?? null, target: summary?.target?.ms ?? null, success: summary?.success ?? null, joules: energy }]
    let offset = 0
    return parts.map((part, index) => {
      const from = start + offset * 1000
      offset += finite(part.durationSeconds) ?? 0
      return {
        key: `${stage.id}/${String(part.id ?? index)}`, stage: stage.id, part: String(part.id ?? ""), from, to: Math.min(start + offset * 1000, end),
        rate: finite(part.rate), p95: quantiles(part[request ? "completedLatencyNS" : "latencyMS"], request).p95, target: summary?.target?.ms ?? null,
        success: finite(part.onTimeSuccessRate ?? part.deadlineSuccessFraction), joules: parts.length === 1 ? energy : null,
      }
    })
  })
}

export const OUTCOME_CLASSES = [["onTime", "기한 내 성공"], ["late", "늦은 완료"], ["rejected", "거부"], ["timeout", "시간 초과"], ["failed", "실패"], ["incomplete", "미완료"]] as const
export type RequestRow = { name: string; outcomes: Published | null; gateway: Quantiles; executor: Quantiles }
export function requestRows(stage: ExperimentStage): RequestRow[] {
  return experimentRows(stage).map(row => ({
    name: row.name,
    outcomes: row.original.outcomeClasses ? object(row.original.outcomeClasses) : null,
    gateway: quantiles(row.original.gatewayWaitNS),
    executor: quantiles(row.original.executorWaitNS),
  }))
}

export type RejectionRow = { key: string; service: string; code: string; at: string; count: number | null; gateway: number | null; executor: number | null }
export function rejectionRows(stage: ExperimentStage): RejectionRow[] {
  return experimentRows(stage).flatMap(row => (Array.isArray(row.original.rejections) ? row.original.rejections.map(object) : []).map((item, index) => ({
    key: `${row.name}/${index}`, service: row.name, code: String(item.code ?? ""), at: String(item.stage ?? ""),
    count: finite(item.count), gateway: quantiles(item.gatewayWaitNS).p95, executor: quantiles(item.executorWaitNS).p95,
  })))
}

export type NodeEnergy = { name: string; joules: number | null; watts: number | null; gapFraction: number | null; maxGapSeconds: number | null; readFailures: number | null; complete: boolean | null; policyMet: boolean | null }
export function nodeEnergyRows(stage: ExperimentStage): NodeEnergy[] {
  const nodes = object(stage.kpis.power).nodes, seconds = stageSeconds(stage)
  return (Array.isArray(nodes) ? nodes.map(object) : []).map(node => {
    const joules = finite(node.reportedMeterJoules ?? node.meterJoules ?? node.joules)
    return {
      name: String(object(node.node).name ?? node.meter ?? ""), joules, watts: joules !== null && seconds ? joules / seconds : null,
      gapFraction: finite(node.gapFraction), maxGapSeconds: finite(node.maximumSampleGapSeconds), readFailures: finite(node.readFailures),
      complete: typeof node.meterComplete === "boolean" ? node.meterComplete : null, policyMet: typeof node.policyMet === "boolean" ? node.policyMet : null,
    }
  })
}
