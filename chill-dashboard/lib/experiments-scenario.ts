import { object, records, type Spec } from "./evaluation-author.ts"

export type ScenarioPart = { id: string; seconds: number | null; rates: Record<string, number | null> }

const finite = (value: unknown): number | null => (typeof value === "number" && Number.isFinite(value) ? value : null)

export const scenarioServices = (spec: Spec): string[] => records(spec.workloads).map(workload => String(workload.name ?? ""))

export function scenarioParts(spec: Spec): ScenarioPart[] {
  return records(spec.parts).map(part => ({
    id: String(part.id ?? ""),
    seconds: finite(part.durationSeconds),
    rates: Object.fromEntries(Object.entries(object(part.rates)).map(([name, rate]) => [name, finite(rate)])),
  }))
}

/** Sum of the part lengths (a sum of server values). */
export const scenarioSeconds = (spec: Spec): number => scenarioParts(spec).reduce((sum, part) => sum + Math.max(0, part.seconds ?? 0), 0)

/** One row per part start plus a closing row at the end, so a step line holds each rate until the next part. */
export function scenarioSteps(spec: Spec): { t: number; [service: string]: number | null }[] {
  const names = scenarioServices(spec), parts = scenarioParts(spec)
  const rows: { t: number; [service: string]: number | null }[] = []
  let at = 0
  for (const part of parts) {
    rows.push({ t: at, ...Object.fromEntries(names.map((name, index) => [`s${index}`, part.rates[name] ?? null])) })
    at += Math.max(0, part.seconds ?? 0)
  }
  const last = rows.at(-1)
  if (last) rows.push({ ...last, t: at })
  return rows
}

const labelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const seedMax = 18446744073709551615n

/** Short Korean problems that block the preflight; the server validates the same rules again. */
export function scenarioProblems(name: string, spec: Spec, editing: boolean): string[] {
  const problems: string[] = []
  if (!editing && !name) problems.push("이름 필요")
  else if (!editing && (name.length > 253 || !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(name))) problems.push("이름 형식 오류")
  const services = scenarioServices(spec), parts = scenarioParts(spec)
  if (!services.length) problems.push("서비스 필요")
  if (!parts.length) problems.push("구간 필요")
  const ids = parts.map(part => part.id)
  if (ids.some(id => !labelPattern.test(id) || id.length > 63)) problems.push("구간 이름 형식 오류")
  if (new Set(ids).size !== ids.length) problems.push("구간 이름 중복")
  if (parts.some(part => part.seconds == null || part.seconds <= 0)) problems.push("구간 길이 필요")
  if (parts.some(part => services.some(service => { const rate = part.rates[service]; return rate == null || rate < 0 }))) problems.push("요청률 필요")
  const seeds = object(spec.seeds), datasets = object(spec.datasetRefs), arrivals = object(spec.arrivalDistribution)
  if (services.some(service => { const seed = String(seeds[service] ?? ""); return !/^[0-9]{1,20}$/.test(seed) || BigInt(seed) > seedMax })) problems.push("난수 씨앗 필요")
  if (services.some(service => !object(datasets[service]).uid)) problems.push("데이터셋 필요")
  if (services.some(service => !arrivals[service])) problems.push("도착 분포 필요")
  return problems
}

/** Server preflight check ids ("workload:NAME", "dataset:NAME", "evidence:KEY", "name", "spec") as short labels. */
export function loadCheckLabel(id: string): string {
  const [kind, ...rest] = id.split(":")
  const target = rest.join(":")
  if (kind === "name") return "이름"
  if (kind === "spec") return "정의"
  if (kind === "workload") return `서비스 ${target}`
  if (kind === "dataset") return `데이터셋 ${target}`
  if (kind === "evidence") return `기록 입력 ${target}`
  return id
}
