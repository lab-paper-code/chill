import type { SchedulerSettings } from "./read-types.ts"
import { writableSettingKeys } from "./scheduler-settings.ts"

type NativeJSON = { rawJSON?: (text: string) => unknown; parse: (text: string, reviver?: (key: string, value: unknown, context?: {source?: string}) => unknown) => unknown }
const native = JSON as NativeJSON
export function preciseJSONSupported(): boolean {
  if (!native.rawJSON) return false
  let source = ""
  native.parse("9007199254740993", (_key, value, context) => { source = context?.source ?? ""; return value })
  return source === "9007199254740993"
}
export function parseSchedulerJSON(text: string): unknown {
  if (!preciseJSONSupported()) throw new Error("정밀 정수 JSON 미지원 브라우저, 최신 Chrome, Edge, Firefox 필요")
  return restoreEvidence(native.parse(text, (_key, value, context) => typeof value === "number" && !Number.isSafeInteger(value) && /^-?\d+$/.test(context?.source ?? "") ? BigInt(context!.source!) : value))
}
// internal/serving/httpapi/scheduler_presentation.go: view=compact replaces only instance `profile`
// with {"$ref":key} into the top-level `profiles` dictionary. Restoring yields the full response.
function restoreEvidence(root: unknown): unknown {
  if (!root || typeof root !== "object" || !("profiles" in root)) return root
  const {profiles, ...full} = root as Record<string, unknown>
  // Catalog views carry a `profiles` array; only the compact dictionary is an object.
  if (!profiles || typeof profiles !== "object" || Array.isArray(profiles)) return root
  const dictionaries: Record<string, Record<string, unknown>> = {profile: profiles as Record<string, unknown>}
  const walk = (value: unknown): void => {
    if (!value || typeof value !== "object") return
    if (Array.isArray(value)) { value.forEach(walk); return }
    const object = value as Record<string, unknown>
    for (const key of Object.keys(object)) {
      const item = object[key] as {$ref?: unknown} | null
      if (key in dictionaries && item && typeof item === "object" && typeof item.$ref === "string") {
        if (!Object.hasOwn(dictionaries[key], item.$ref)) throw new Error(`응답의 ${key} 참조 ${item.$ref} 없음`)
        object[key] = dictionaries[key][item.$ref]
      } else walk(item)
    }
  }
  walk(full)
  return full
}
export function stringifySchedulerJSON(value: unknown, space?: number): string {
  return JSON.stringify(value, (_key, item) => {
    if (typeof item !== "bigint") return item
    if (!native.rawJSON) throw new Error("정밀 정수 JSON 미지원 환경")
    return native.rawJSON(item.toString())
  }, space)
}
export function millisecondsToNanos(text: string, minimum = 1n): bigint {
  const match = /^(\d+)(?:\.(\d{1,6}))?$/.exec(text)
  if (!match) throw new Error("0 이상 ms, 소수 6자리까지")
  const value = BigInt(match[1]) * 1000000n + BigInt((match[2] ?? "").padEnd(6, "0"))
  if (value < minimum || value > 9223372036854775807n) throw new Error(`시간 범위 ${minimum}~9223372036854775807 ns`)
  return value
}
export function nanosToMilliseconds(value: number | bigint): string {
  const raw = BigInt(value), nanos = raw < 0n ? -raw : raw
  const fraction = (nanos % 1000000n).toString().padStart(6, "0").replace(/0+$/, "")
  return `${raw < 0n ? "-" : ""}${nanos / 1000000n}${fraction ? `.${fraction}` : ""}`
}
export function wireInteger(text: string, minimum: bigint, maximum: bigint): bigint {
  if (!/^-?\d+$/.test(text)) throw new Error("정수 필요")
  const value = BigInt(text)
  if (value < minimum || value > maximum) throw new Error(`범위 ${minimum}~${maximum}`)
  return value
}
// settings.completionMarginMS: finite nonnegative milliseconds, as SchedulerSettings.Validate accepts.
export function marginMilliseconds(text: string): number {
  const value = Number(text)
  if (!/^\d+(?:\.\d+)?$/.test(text) || !Number.isFinite(value)) throw new Error("0 이상 ms")
  return value
}

export function settingsEqual(a: SchedulerSettings, b: SchedulerSettings): boolean {
  return writableSettingKeys.every(key => {
    if(key==="controls")return [...new Set([...Object.keys(a.controls),...Object.keys(b.controls)])].every(control=>a.controls[control as keyof SchedulerSettings["controls"]]===b.controls[control as keyof SchedulerSettings["controls"]])
    if(key==="minimumDemand")return [...new Set([...Object.keys(a.minimumDemand??{}),...Object.keys(b.minimumDemand??{})])].every(uid=>a.minimumDemand?.[uid]===b.minimumDemand?.[uid])
    if(key==="autonomousWorkloads"||key==="excludedNodes"){const left=new Set(a[key]??[]),right=new Set(b[key]??[]);return left.size===right.size&&[...left].every(value=>right.has(value))}
    return stringifySchedulerJSON(a[key])===stringifySchedulerJSON(b[key])
  })
}
