// Scheduler settings screen: the editable form, its validation and the change rows of a review,
// a conflict and a history record. Wire values stay exact (bigint); durations are seconds in the UI, ns on the wire.
import type { SchedulerSettings } from "./read-types.ts"
import { settingControls, settingFields, type SettingField } from "./scheduler-settings.ts"
import { marginMilliseconds, stringifySchedulerJSON, wireInteger } from "./scheduler.ts"
import { formatDurationNanos } from "./format.ts"
import { schedulerLabel, schedulerOptions } from "./scheduler-selection.ts"

export type FieldKey = SettingField["key"]
export type SettingsGroup = "targets" | "cycle" | "limit" | "controls" | "capacity" | "search"
export const settingsGroups: { value: SettingsGroup; label: string }[] = [
  { value: "targets", label: "대상" }, { value: "cycle", label: "판단 주기" }, { value: "limit", label: "자원 한도" },
  { value: "controls", label: "자동 조정 항목" }, { value: "capacity", label: "용량 계산" }, { value: "search", label: "탐색" },
]

/** Editable scalar fields with their shared labels (`settingFields`). */
export const fields = settingFields

const isNanos = (key: FieldKey) => key.endsWith("Nanos")
const MIB = 1048576n, INT64 = 9223372036854775807n, UINT64 = 18446744073709551615n, SECOND = 1_000_000_000n
/** Input unit suffix of a field. */
export function fieldUnit(key: FieldKey): string | undefined {
  if (isNanos(key)) return "초"
  if (key === "completionMarginMS") return "ms"
  if (key === "servingMemoryBytes") return "MiB"
  return settingFields.find(field => field.key === key)?.unit
}

/** The text (or switch value) an input shows for a stored value; absent optional values show their default. */
export function fieldInput(key: FieldKey, settings: SchedulerSettings): string | boolean {
  const field = settingFields.find(item => item.key === key)!
  const value = settings[key] ?? field.defaultValue
  if (field.kind === "boolean") return Boolean(value)
  if (value === undefined) return ""
  if (isNanos(key)) return nanosToSeconds(BigInt(value as number | bigint))
  if (key === "servingMemoryBytes") return mib(BigInt(value as number | bigint))
  return String(value)
}
/** Exact seconds text of a nanosecond value: 10000000000 → "10", 500000000 → "0.5". */
function nanosToSeconds(nanos: bigint): string {
  const fraction = (nanos % SECOND).toString().padStart(9, "0").replace(/0+$/, "")
  return `${nanos / SECOND}${fraction ? `.${fraction}` : ""}`
}
function secondsToNanos(text: string): bigint {
  const match = /^(\d+)(?:\.(\d{1,9}))?$/.exec(text)
  if (!match) throw new Error("form")
  const nanos = BigInt(match[1]) * SECOND + BigInt((match[2] ?? "").padEnd(9, "0"))
  if (nanos < 1n || nanos > INT64) throw new Error("form")
  return nanos
}
function mib(bytes: bigint): string {
  if (bytes % MIB === 0n) return String(bytes / MIB)
  // MiB is 2^20, so the fraction terminates within 20 digits.
  let rest = bytes % MIB, digits = ""
  while (rest && digits.length < 20) { rest *= 10n; digits += String(rest / MIB); rest %= MIB }
  return `${bytes / MIB}.${digits}`
}
function mibBytes(text: string): bigint {
  const match = /^(\d+)(?:\.(\d{1,20}))?$/.exec(text)
  if (!match) throw new Error("숫자로 입력")
  const scale = 10n ** BigInt((match[2] ?? "").length), scaled = BigInt(match[1] + (match[2] ?? "")) * MIB
  if (scaled % scale) throw new Error("바이트 단위로 나누어떨어지지 않음")
  const bytes = scaled / scale
  if (bytes > UINT64) throw new Error("범위 초과")
  return bytes
}
function fraction(text: string, open: boolean): number {
  const value = Number(text)
  if (!/^\d+(?:\.\d+)?$/.test(text) || !Number.isFinite(value) || value > 1 || (open ? value <= 0 : value < 0)) throw new Error(open ? "0 초과 1 이하" : "0 이상 1 이하")
  return value
}
/** Wire value of one input, or an Error with a short label. */
export function parseField(key: FieldKey, text: string): bigint | number | string {
  const value = text.trim()
  if (!value) throw new Error("값 필요")
  try {
    if (isNanos(key)) return secondsToNanos(value)
    if (key === "completionMarginMS") return marginMilliseconds(value)
    if (key === "servingMemoryBytes") return mibBytes(value)
    if (key === "transitionReserveFraction" || key === "scaleDownHeadroomFraction") return fraction(value, false)
    if (key === "capacityBisectionWidth") return fraction(value, true)
    if (key === "planSearchStrategy") { if (!schedulerOptions.some(option => option.value === value)) throw new Error("알 수 없는 값"); return value }
    return wireInteger(value, key === "planPolishActive" ? 0n : 1n, INT64)
  } catch (error) {
    if (error instanceof Error && ["0 초과 1 이하", "0 이상 1 이하", "알 수 없는 값", "바이트 단위로 나누어떨어지지 않음", "범위 초과", "숫자로 입력"].includes(error.message)) throw error
    throw new Error(isNanos(key) ? "0 초과, 소수 9자리까지" : key === "completionMarginMS" ? "0 이상 숫자" : key === "planPolishActive" ? "0 이상 정수" : "1 이상 정수")
  }
}

export type SettingsForm = {
  fields: Record<FieldKey, string | boolean>
  controls: SchedulerSettings["controls"]
  autonomous: string[]
  /** Minimum demand text per workload UID; "" is unset. */
  demand: Record<string, string>
  excluded: string[]
  priorityStrategy: "excess" | "workload"
  priority: string[]
}
export function formOf(settings: SchedulerSettings): SettingsForm {
  return {
    fields: Object.fromEntries(settingFields.map(field => [field.key, fieldInput(field.key, settings)])) as Record<FieldKey, string | boolean>,
    controls: { ...settings.controls },
    autonomous: [...settings.autonomousWorkloads ?? []],
    demand: Object.fromEntries(Object.entries(settings.minimumDemand ?? {}).map(([uid, rate]) => [uid, String(rate)])),
    excluded: [...settings.excludedNodes ?? []],
    priorityStrategy: settings.sloPriorityStrategy ?? "excess",
    priority: [...settings.workloadPriority ?? []],
  }
}

/** Settings the form describes on top of `base`; untouched inputs keep the stored value exactly. */
export function settingsOf(base: SchedulerSettings, form: SettingsForm): { settings: SchedulerSettings; errors: Record<string, string> } {
  const shown = formOf(base), next: SchedulerSettings = { ...base }, errors: Record<string, string> = {}
  const record = next as Record<string, unknown>
  for (const field of settingFields) {
    const input = form.fields[field.key]
    if (input === shown.fields[field.key]) continue
    if (typeof input === "boolean") { record[field.key] = input; continue }
    try { record[field.key] = parseField(field.key, input) } catch (error) { errors[field.key] = (error as Error).message }
  }
  if (!errors.capacityPassingWindows && !errors.capacityWindows && BigInt(next.capacityPassingWindows) > BigInt(next.capacityWindows)) errors.capacityPassingWindows = "창 수 이하"
  next.controls = { ...form.controls }
  if (!sameSet(form.autonomous, base.autonomousWorkloads ?? [])) next.autonomousWorkloads = form.autonomous
  if (!sameSet(form.excluded, base.excludedNodes ?? [])) next.excludedNodes = form.excluded
  const demand: Record<string, number> = {}
  for (const [uid, text] of Object.entries(form.demand)) {
    if (!text.trim()) continue
    if (text === shown.demand[uid]) { demand[uid] = base.minimumDemand![uid]; continue }
    const rate = Number(text)
    if (!/^\d+(?:\.\d+)?$/.test(text.trim()) || !Number.isFinite(rate)) errors[`demand:${uid}`] = "0 이상 숫자"
    else demand[uid] = rate
  }
  if (base.minimumDemand !== undefined || Object.keys(demand).length) next.minimumDemand = demand
  if (form.priorityStrategy !== shown.priorityStrategy) next.sloPriorityStrategy = form.priorityStrategy
  if (form.priority.join() !== shown.priority.join()) next.workloadPriority = form.priority
  return { settings: next, errors }
}
const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every(item => b.includes(item))

/** Display of a stored field value with its unit. */
export function fieldText(key: FieldKey, settings: SchedulerSettings): string {
  const input = fieldInput(key, settings)
  if (typeof input === "boolean") return input ? "켜짐" : "꺼짐"
  if (key === "planSearchStrategy") return schedulerLabel(input || undefined)
  if (!input) return "—"
  if (isNanos(key)) return formatDurationNanos((settings[key] ?? settingFields.find(field => field.key === key)!.defaultValue) as number | bigint)
  const [whole, part] = input.split(".")
  const grouped = /^\d+$/.test(whole) ? BigInt(whole).toLocaleString("ko-KR") : whole
  const unit = fieldUnit(key)
  return `${grouped}${part ? `.${part}` : ""}${unit === "ms" || unit === "MiB" ? ` ${unit}` : ""}`
}

export type ChangeRow = { id: string; group: SettingsGroup; label: string; before: string; after: string; apply: (settings: SchedulerSettings) => SchedulerSettings }
const onOff = (on: boolean) => on ? "켜짐" : "꺼짐"
const rate = (value: number | undefined) => value === undefined ? "미설정" : `${value} req/s`
const strategy = (value: SchedulerSettings["sloPriorityStrategy"]) => value === "workload" ? "서비스 순위" : "목표 초과 정도"

/** One row per changed value (per service, device and switch), each able to re-apply itself on other settings. */
export function changeRows(before: SchedulerSettings, after: SchedulerSettings, name: (uid: string) => string): ChangeRow[] {
  const rows: ChangeRow[] = []
  for (const uid of union(before.autonomousWorkloads, after.autonomousWorkloads)) {
    const on = !!after.autonomousWorkloads?.includes(uid)
    if (!!before.autonomousWorkloads?.includes(uid) !== on) rows.push({ id: `auto:${uid}`, group: "targets", label: `자동 제어 ${name(uid)}`, before: onOff(!on), after: onOff(on),
      apply: s => ({ ...s, autonomousWorkloads: toggle(s.autonomousWorkloads, uid, on) }) })
  }
  for (const uid of union(Object.keys(before.minimumDemand ?? {}), Object.keys(after.minimumDemand ?? {}))) {
    const was = before.minimumDemand?.[uid], now = after.minimumDemand?.[uid]
    if (was !== now) rows.push({ id: `demand:${uid}`, group: "targets", label: `최소 수요 ${name(uid)}`, before: rate(was), after: rate(now),
      apply: s => { const demand = { ...s.minimumDemand }; if (now === undefined) delete demand[uid]; else demand[uid] = now; return { ...s, minimumDemand: demand } } })
  }
  for (const node of union(before.excludedNodes, after.excludedNodes)) {
    const out = !!after.excludedNodes?.includes(node)
    if (!!before.excludedNodes?.includes(node) !== out) rows.push({ id: `excluded:${node}`, group: "targets", label: `제외 장치 ${node}`, before: out ? "포함" : "제외", after: out ? "제외" : "포함",
      apply: s => ({ ...s, excludedNodes: toggle(s.excludedNodes, node, out) }) })
  }
  if ((before.sloPriorityStrategy ?? "excess") !== (after.sloPriorityStrategy ?? "excess")) rows.push({ id: "sloPriorityStrategy", group: "targets", label: "우선순위 기준",
    before: strategy(before.sloPriorityStrategy), after: strategy(after.sloPriorityStrategy), apply: s => ({ ...s, sloPriorityStrategy: after.sloPriorityStrategy }) })
  if ((before.workloadPriority ?? []).join() !== (after.workloadPriority ?? []).join()) {
    const order = (list: string[] | undefined) => list?.length ? list.map(name).join(", ") : "없음"
    rows.push({ id: "workloadPriority", group: "targets", label: "서비스 순위", before: order(before.workloadPriority), after: order(after.workloadPriority), apply: s => ({ ...s, workloadPriority: after.workloadPriority }) })
  }
  for (const field of fields) if (stringifySchedulerJSON(before[field.key]) !== stringifySchedulerJSON(after[field.key])) rows.push({ id: field.key, group: field.group, label: field.label,
    before: fieldText(field.key, before), after: fieldText(field.key, after), apply: s => ({ ...s, [field.key]: after[field.key] }) })
  for (const control of settingControls) if (before.controls[control.key] !== after.controls[control.key]) rows.push({ id: `controls:${control.key}`, group: "controls", label: control.label,
    before: onOff(before.controls[control.key]), after: onOff(after.controls[control.key]), apply: s => ({ ...s, controls: { ...s.controls, [control.key]: after.controls[control.key] } }) })
  return rows
}
const union = (a: string[] | null | undefined, b: string[] | null | undefined) => [...new Set([...a ?? [], ...b ?? []])]
const toggle = (list: string[] | null | undefined, item: string, on: boolean) => on ? [...new Set([...list ?? [], item])] : (list ?? []).filter(value => value !== item)

/** My changes since `base`, applied on top of the latest stored settings. */
export function rebase(latest: SchedulerSettings, base: SchedulerSettings, mine: SchedulerSettings): SchedulerSettings {
  return changeRows(base, mine, uid => uid).reduce((settings, row) => row.apply(settings), latest)
}
