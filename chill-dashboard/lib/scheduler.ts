import type { SchedulerSettings } from "./read-types.ts"

type NativeJSON = { rawJSON?: (text: string) => unknown; parse: (text: string, reviver?: (key: string, value: unknown, context?: {source?: string}) => unknown) => unknown }
const native = JSON as NativeJSON
export function preciseJSONSupported(): boolean {
  if (!native.rawJSON) return false
  let source = ""
  native.parse("9007199254740993", (_key, value, context) => { source = context?.source ?? ""; return value })
  return source === "9007199254740993"
}
export function parseSchedulerJSON(text: string): unknown {
  if (!preciseJSONSupported()) throw new Error("정밀 정수 JSON을 지원하는 최신 Chrome, Edge 또는 Firefox를 사용해 주세요.")
  return restoreEvidence(native.parse(text, (_key, value, context) => typeof value === "number" && !Number.isSafeInteger(value) && /^-?\d+$/.test(context?.source ?? "") ? BigInt(context!.source!) : value))
}
// internal/serving/entrypoint/scheduler_presentation.go: view=compact replaces only instance `profile` and
// `completionMeasurement` with {"$ref":key} into two top-level dictionaries. Restoring yields the full response.
function restoreEvidence(root: unknown): unknown {
  if (!root || typeof root !== "object" || !("profiles" in root) || !("completionMeasurements" in root)) return root
  const {profiles, completionMeasurements, ...full} = root as Record<string, unknown>
  const dictionaries: Record<string, Record<string, unknown>> = {profile: profiles as Record<string, unknown>, completionMeasurement: completionMeasurements as Record<string, unknown>}
  const walk = (value: unknown): void => {
    if (!value || typeof value !== "object") return
    if (Array.isArray(value)) { value.forEach(walk); return }
    const object = value as Record<string, unknown>
    for (const key of Object.keys(object)) {
      const item = object[key] as {$ref?: unknown} | null
      if (key in dictionaries && item && typeof item === "object" && typeof item.$ref === "string") {
        if (!Object.hasOwn(dictionaries[key], item.$ref)) throw new Error(`응답의 ${key} 참조 ${item.$ref}를 찾지 못했어요.`)
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
    if (!native.rawJSON) throw new Error("정밀 정수 JSON 지원 환경이 필요합니다.")
    return native.rawJSON(item.toString())
  }, space)
}
export function millisecondsToNanos(text: string): bigint {
  const match = /^(\d+)(?:\.(\d{1,6}))?$/.exec(text)
  if (!match) throw new Error("ms는 양수이며 소수점 아래 6자리까지 입력합니다.")
  const value = BigInt(match[1]) * 1000000n + BigInt((match[2] ?? "").padEnd(6, "0"))
  if (value <= 0n || value > 9223372036854775807n) throw new Error("시간은 1~9223372036854775807 ns 범위입니다.")
  return value
}
export function nanosToMilliseconds(value: number | bigint): string {
  const nanos = BigInt(value)
  const fraction = (nanos % 1000000n).toString().padStart(6, "0").replace(/0+$/, "")
  return `${nanos / 1000000n}${fraction ? `.${fraction}` : ""}`
}
export function wireInteger(text: string, minimum: bigint, maximum: bigint): bigint {
  if (!/^-?\d+$/.test(text)) throw new Error("정수로 입력합니다.")
  const value = BigInt(text)
  if (value < minimum || value > maximum) throw new Error(`${minimum}~${maximum} 범위로 입력합니다.`)
  return value
}

export function settingsEqual(a: SchedulerSettings, b: SchedulerSettings): boolean {
  return (Object.keys(a) as (keyof SchedulerSettings)[]).every(key => key === "controls"
    ? (Object.keys(a.controls) as (keyof SchedulerSettings["controls"])[]).every(control => a.controls[control] === b.controls[control])
    : String(a[key]) === String(b[key]))
}
