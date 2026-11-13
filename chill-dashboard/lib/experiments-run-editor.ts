// Experiment run editor: labels and pure checks over the evaluation spec tree (lib/evaluation-author.ts).
import type { SchedulerSettings } from "./read-types.ts"
import { settingControls, settingFields, settingTargets, type SettingField } from "./scheduler-settings.ts"
import { evaluationSameSpec } from "./evaluation-request.ts"
import { object, records, type Spec } from "./evaluation-author.ts"

type FieldKey = SettingField["key"]
/** Patch keys with a dedicated input beyond the scalar scheduler fields. */
export const targetKeys = ["autonomousWorkloads", "minimumDemand", "excludedNodes", "controls", "sloPriorityStrategy", "workloadPriority"] as const
export type TargetKey = typeof targetKeys[number]
export type PatchKey = FieldKey | TargetKey
// The same words as the scheduler settings screen (lib/scheduler-settings.ts and its 우선순위 group).
const targetLabels: Record<TargetKey, string> = {
  ...Object.fromEntries(settingTargets.map(target => [target.key, target.label])) as Record<(typeof settingTargets)[number]["key"], string>,
  controls: "자동 조정 항목", sloPriorityStrategy: "우선순위 기준", workloadPriority: "서비스 순위",
}
export const patchKeys: PatchKey[] = [...targetKeys, ...settingFields.map(field => field.key)]
export const isPatchKey = (key: string): key is PatchKey => (patchKeys as string[]).includes(key)
export function patchKeyLabel(key: string): string {
  if (Object.hasOwn(targetLabels, key)) return targetLabels[key as TargetKey]
  return settingFields.find(field => field.key === key)?.label ?? key
}

/** The value a newly added patch key starts from: the current scheduler setting, else a neutral default. */
export function patchDefault(key: PatchKey, current: SchedulerSettings | undefined): unknown {
  const stored = current?.[key as keyof SchedulerSettings]
  if (stored !== undefined && stored !== null) return structuredClone(stored)
  switch (key) {
    case "controls": return Object.fromEntries(settingControls.map(control => [control.key, true]))
    case "autonomousWorkloads": case "excludedNodes": case "workloadPriority": return []
    case "minimumDemand": return {}
    case "sloPriorityStrategy": return "excess"
  }
  const field = settingFields.find(item => item.key === key)!
  return field.defaultValue ?? (field.kind === "boolean" ? false : field.kind === "strategy" ? "v2" : undefined)
}

const checkNames: Record<string, string> = {
  spec: "정의 형식", name: "실험 이름", evidenceStore: "증거 저장소", evaluationConfig: "실험 구성",
  pool: "장치 풀", profiling: "진행 중인 측정",
}
const stageCheckNames: Record<string, string> = { loadScenario: "부하 시나리오", coverage: "서비스 포함", settingsPatch: "설정 변경" }
/** Korean label of a preflight check id (`pool`, `loadScenario/<stage>`, `settingsPatch/run`, ...); unknown ids keep their code. */
export function checkLabel(id: string): { label: string; code?: string } {
  if (Object.hasOwn(checkNames, id)) return { label: checkNames[id] }
  const slash = id.indexOf("/"), head = id.slice(0, slash), stage = id.slice(slash + 1)
  if (head === "settingsPatch" && (stage === "run" || isPatchKey(stage))) return { label: stage === "run" ? "공통 설정 변경" : `설정 변경 ${patchKeyLabel(stage)}` }
  if (slash > 0 && Object.hasOwn(stageCheckNames, head)) return { label: `${stageCheckNames[head]} ${stage}` }
  return { label: "기타 점검", code: id }
}
const knownReasons: Record<string, string> = {
  "another EvaluationRun occupies the pool": "다른 실험이 사용 중", "profiling measurement is active": "측정 진행 중",
  "name must be a DNS subdomain": "소문자, 숫자, -, .만", "LoadScenario not found": "시나리오 없음", "LoadScenario UID must match": "같은 이름으로 다시 등록된 시나리오",
}
/** A failed check's reason: known server sentences in Korean, others as sent. */
export const checkReason = (reason: string | undefined) => reason ? knownReasons[reason] ?? (reason.startsWith("evidenceStore must be ") ? "지정 저장소만 가능" : reason) : ""

/** Kubernetes object name rule the server's `name` check applies (DNS-1123 subdomain). */
export function runNameError(name: string, origin?: string): string {
  if (!name) return "이름 필요"
  if (name.length > 253 || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/.test(name)) return "소문자, 숫자, -, .만"
  if (origin && name === origin) return "원본과 다른 이름 필요"
  return ""
}

/** Sum of a load scenario's part lengths in seconds (server values). */
export const scenarioSeconds = (spec: unknown) => records(object(spec).parts).reduce((sum, part) => sum + (typeof part.durationSeconds === "number" ? part.durationSeconds : 0), 0)

const specLabels: Record<string, string> = {
  question: "질문", loadGenerator: "부하 발생 장치", settingsPatch: "공통 설정 변경", stages: "단계", comparisons: "비교",
  judgment: "판정 기준", target: "대상 구성요소", harness: "실행기", powerMeasurement: "전력 계측", evidenceStore: "증거 저장소",
}
function summary(key: string, value: unknown): string {
  if (value === undefined || value === null) return "없음"
  if (key === "question" && typeof value === "string") return value
  if (key === "loadGenerator") return String(object(value).nodeName ?? "—")
  if (Array.isArray(value)) return `${value.length}개`
  if (key === "settingsPatch") return `${Object.keys(object(value)).length}개 항목`
  return "있음"
}
/** Before/after rows of the top-level spec fields that differ. */
export function specChanges(before: Spec, after: Spec): { label: string; before: string; after: string }[] {
  return [...new Set([...Object.keys(before), ...Object.keys(after)])].filter(key => !evaluationSameSpec(before[key], after[key])).map(key => {
    const was = summary(key, before[key]), now = summary(key, after[key])
    return { label: specLabels[key] ?? key, before: was, after: was === now ? "변경됨" : now }
  })
}
