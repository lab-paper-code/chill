import type { MeasurementEvidenceLocation, OperationCondition, OperationCostMeasurement, OperationStepStatus, ProfilingRun } from "./read-types.ts"
import { codeText } from "./labels.ts"
import { nanosToMilliseconds } from "./scheduler.ts"

export type ProjectionMeasurementKind = "operation-cost-measurements" | "dispatch-occupancy-measurements"
export const isProjectionMeasurementKind = (kind:string):kind is ProjectionMeasurementKind => kind === "operation-cost-measurements" || kind === "dispatch-occupancy-measurements"
export const measurementPath = (kind:ProjectionMeasurementKind, namespace:string, name?:string) => `v1/namespaces/${encodeURIComponent(namespace)}/${kind}${name === undefined ? "" : `/${encodeURIComponent(name)}`}`
export const measurementListHref = (kind:ProjectionMeasurementKind, namespace:string) => `/measurements/${kind}?namespace=${encodeURIComponent(namespace)}`
export const measurementHref = (kind:ProjectionMeasurementKind, metadata:{namespace:string;name:string;uid:string}) => `/measurements/${kind}/${encodeURIComponent(metadata.namespace)}/${encodeURIComponent(metadata.name)}?uid=${encodeURIComponent(metadata.uid)}`
export const measurementUIDMismatch = (uid:string, expectedUID:string|null) => !!expectedUID && uid !== expectedUID
export function measurementState(phase?:string):{label:string;tone:"info"|"ok"|"warn"|"fail"|"idle"} {
  switch (phase) {
    case "Measuring": return {label:"측정 중이에요",tone:"info"}
    case "Complete": return {label:"측정이 끝났어요",tone:"ok"}
    case "Insufficient": return {label:"판단할 근거가 부족해요",tone:"warn"}
    case "Failed": return {label:"측정을 끝내지 못했어요",tone:"fail"}
    default: return {label:"측정 상태를 확인할 수 없어요",tone:"idle"}
  }
}
export const measurementReason = (reason:string) => codeText[reason] ? `${codeText[reason].join(" ")} (${reason})` : reason
export function occupancyDuration(value?:unknown) {
  if (value === undefined) return "결과 없음"
  if ((typeof value !== "number" && typeof value !== "bigint") || (typeof value === "number" && (!Number.isFinite(value) || !Number.isInteger(value)))) return `${String(value)} · 품질 경고·사용 확인 필요`
  return `${Number(nanosToMilliseconds(value)).toLocaleString("ko-KR",{maximumFractionDigits:2})}ms/request${value < 0 ? " · 품질 경고·사용 확인 필요" : ""}`
}
export const occupancyThroughput = (value?:string) => !value ? "결과 없음" : `${value} req/s${!/^[+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value) || !Number.isFinite(Number(value)) || Number(value) < 0 ? " · 품질 경고·사용 확인 필요" : ""}`
export const significant = (value:number) => Number.isFinite(value) ? Number(value.toPrecision(4)).toLocaleString("ko-KR",{maximumSignificantDigits:4}) : `${String(value)} · 품질 경고`
export function safeMeasurementReturn(value:string|null, fallback="/measurements") {
  if (!value || !value.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(value)) return fallback
  try {
    const url = new URL(value,"https://dashboard.invalid")
    return url.origin === "https://dashboard.invalid" && (/^\/measurements(?:\/|$)/.test(url.pathname) || url.pathname === "/scheduler/profiles" || /^\/experiments(?:\/|$)/.test(url.pathname)) ? value : fallback
  } catch { return fallback }
}
export const exactRun = (rows:ProfilingRun[],namespace:string,name:string,uid:string|null) => rows.find(run=>run.namespace===namespace&&run.name===name&&(!uid||run.uid===uid))
export const occupancySupply = (value?:string) => value === "closed-loop" ? "고정 동시 요청" : value === "poisson" ? "포아송 공급" : value || "기록되지 않았어요"
export function evidenceLocations(evidence:MeasurementEvidenceLocation[], result?:MeasurementEvidenceLocation) {
  return [...evidence,...(result ? [result] : [])].filter((location,index,all) => all.findIndex(other => other.store === location.store && other.key === location.key) === index)
}
export type OperationStep = {name:string;condition:OperationCondition|undefined;results:OperationStepStatus[]}
// Preserve condition order and every recorded result, including unmatched status-only steps.
export function operationSteps(data:OperationCostMeasurement):OperationStep[] {
  const recorded = data.status.steps ?? [], planned = data.conditions.steps
  return [
    ...planned.map(condition => ({name:condition.name,condition,results:recorded.filter(result => result.name === condition.name)})),
    ...recorded.filter(result => !planned.some(condition => condition.name === result.name)).map(result => ({name:result.name,condition:undefined,results:[result]})),
  ]
}
