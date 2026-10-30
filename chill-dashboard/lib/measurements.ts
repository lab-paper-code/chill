import type { MeasurementEvidenceLocation, OperationCondition, OperationCostMeasurement, OperationStepStatus, WireInteger } from "./read-types.ts"

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
export const occupancyNanos = (value?:WireInteger) => value === undefined ? "결과 없음" : `${value} ns/request`
export const occupancyThroughput = (value?:string) => value ? `${value} req/s` : "결과 없음"
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
