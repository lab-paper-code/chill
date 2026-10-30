import type { NodeRef, SchedulerAllocation, SchedulerDefinition, SchedulerSettings, WireInteger } from "./read-types.ts"
import { explain } from "./labels.ts"

export const capacityFields = [
  {key:"completionMarginMS",label:"완료 여유",unit:"ms"},
  {key:"capacityWindowNanos",label:"용량 계산 창 길이",unit:"ns"},
  {key:"capacityWindows",label:"용량 계산 창 수",unit:"개"},
  {key:"capacityPassingWindows",label:"필요한 합격 창 수",unit:"개"},
  {key:"capacityMinimumRequests",label:"창당 최소 요청 수",unit:"건/창"},
  {key:"capacityGridSteps",label:"격자 분할 수",unit:"개"},
  {key:"capacityBisectionWidth",label:"μ 대비 이분탐색 폭 비율",unit:"(무차원)"},
  {key:"capacityTailSamples",label:"백분위 위 꼬리 목표 표본 수",unit:"개"},
] as const
export type CapacitySettings = Pick<SchedulerSettings, typeof capacityFields[number]["key"]>
// internal/serving/capacity.go and scheduler.go: GET /scheduler/capacity is current rows, not history.
export type CapacityInput = CapacitySettings & {
  workloadUID:string;slo:SchedulerDefinition["slo"];node:NodeRef;powerMode:string;profileID:string
  allocation:SchedulerAllocation;maxBatch:WireInteger
  dispatchOccupancy?:{node:NodeRef;powerMode:string;runtimeImage:string;model:string;perRequestNanos:WireInteger;source:string}
}
export type CapacityJudgment = {rate:number;requests:WireInteger;passed:WireInteger;met:boolean;percentileMS:number[]|null}
export type CapacityRow = {
  key:string;input:CapacityInput;status:string;reason?:string;mu:number;kappa?:number
  minimumServiceNanos?:number;grid?:CapacityJudgment[];bisection?:CapacityJudgment[]
  builtAt:string;elapsedNanos?:WireInteger;previous?:boolean
}
export type CapacityView = {rows:CapacityRow[]|null}
export const capacityPath=(workload="",row="")=>{
  const query=new URLSearchParams()
  if(workload)query.set("workload",workload)
  if(row)query.set("row",row)
  return `/scheduler/capacity${query.size?`?${query}`:""}`
}
export const capacityRows=(rows:CapacityRow[],workload:string)=>workload?rows.filter(row=>row.input.workloadUID===workload):rows
export const selectedCapacityRow=(rows:CapacityRow[],key:string)=>rows.find(row=>row.key===key)
export const capacityPolling=(rows:CapacityRow[])=>rows.some(row=>row.status==="CapacityPending"||row.previous)
export const capacityRate=(value:number|undefined)=>value==null?"용량 값이 없어요":`${value} req/s`
export const capacityStatus=(status:string)=>({Built:"용량 계산이 끝났어요",CapacityPending:"용량을 계산하고 있어요",CapacityRejected:"이 조건의 용량을 사용할 수 없어요"}[status]??"용량 계산 상태를 확인할 수 없어요")
export const capacityReason=(reason?:string)=>!reason?"사유 제공 없음":({
  MissingDispatchOccupancy:"분배 점유시간 근거가 없어요",
  NonMonotone:"요청률을 높였는데 다시 통과한 구간이 있어 용량을 정하지 않았어요",
  BatchServiceExceedsSLO:"가장 짧은 배치 실행에도 완료 여유를 더하면 목표 시간을 넘어요",
}[reason]??explain(reason).join(" "))
export const capacityPercentile=(value:number)=>value===-1?"이 창의 지연값 없음(미충족)":`${value} ms`
export function capacityComparison(input:CapacitySettings,current:CapacitySettings) {
  return capacityFields.map(field=>({...field,used:input[field.key],current:current[field.key],same:String(input[field.key])===String(current[field.key])}))
}
