import type { CoverageOccupancy, DispatchOccupancyMeasurement, MeasurementCoverageCell, OccupancyMutation, OccupancyRequest } from "./read-types.ts"
import { occupancyReason } from "./measurements.ts"

export const occupancyStates={none:"없음",measuring:"측정 중",failed:"실패",present:"있음"} as const
export const occupancyRunning=(data:DispatchOccupancyMeasurement)=>!data.phase||data.phase==="Measuring"
export const occupancyDirect=(data:DispatchOccupancyMeasurement)=>data.schemaVersion==="chill.dacs.io/dispatch-occupancy-measurement.v1alpha2"||data.estimate!=null||(data.minimumPairs??0)>0
export function occupancyDisplayReason(data:DispatchOccupancyMeasurement) {
  if(occupancyDirect(data)) {
    const label=(reason:string)=>occupancyReason({status:{reason}})
    const recorded=data.reason||data.status.reason
    const reasons=[
      recorded?label(recorded):"",
      ...(data.assessment?.reasons??[]).map(({code,message})=>{
        const summary=label(code||message)
        return message&&message!==code&&message!==summary?`${summary}, ${message}`:summary
      }),
      ...(data.assessment?.evidence?.reasons??[]).map(label),
    ].filter(Boolean)
    if(reasons.length)return [...new Set(reasons)].join(", ")
    return data.phase==="Failed"?"측정 실패":data.phase==="Insufficient"?"근거 부족":data.assessment?.displayStatus==="problem"?"상세 사유 미제공":data.estimate?"해당 없음":"결과 없음"
  }
  const result=data.d
  return occupancyReason({assessment:data.assessment,status:{reason:data.reason??data.status.reason,perRequestNanos:result?.perRequestNanos??undefined,throughput:result?.throughput==null?undefined:String(result.throughput)}})
}
export function occupancyReasonLabel(reason:string) {
  return reason==="ProfileSuperseded"?"이전 프로파일 측정":reason==="EvidenceInvalid"?"점유시간 근거 검증 실패":reason?occupancyReason({status:{reason}}):"해당 없음"
}
export function occupancyRequestHref(namespace:string,selection:{model?:string;deviceClass?:string;path?:string;powerMode?:string;node?:string;nodeUID?:string;batch?:number|string;measurementID?:string;profileID?:string;workloadUID?:string;workload?:string;batchEnergyOptimization?:string}={}) {
  const params=new URLSearchParams({namespace,kind:"dispatch-occupancy"})
  for(const [key,value] of Object.entries(selection))if(value!==undefined&&value!=="")params.set(key,String(value))
  return `/measurements/requests/new?${params}`
}
export function coverageOccupancyRequestHref(cell:MeasurementCoverageCell,row:CoverageOccupancy,namespace:string) {
  return occupancyRequestHref(namespace,{model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,node:row.node.name,nodeUID:row.node.uid,batch:row.batch,measurementID:cell.currentMeasurement?.id})
}
export function occupancyBody(input:OccupancyRequest):OccupancyRequest {
  return {...(input.name?{name:input.name}:{}),node:input.node,batchEnergyOptimization:input.batchEnergyOptimization,workload:input.workload,batch:input.batch}
}
export function occupancyChecked(inputKey:string,checkedKey:string|null,response:OccupancyMutation|undefined) {
  return inputKey===checkedKey&&response?.request==="NOT_SUBMITTED"&&!!response.task?.metadata.name&&!!response.task.spec
}
export function occupancyResponseState(code?:string,request?:string) {
  return code==="OccupancyInfeasible"?"측정 조건 불충족":code==="OccupancyPolicyUnavailable"?"측정 정책 사용 불가":request==="NOT_SUBMITTED"?"미제출":request==="Accepted"?"접수됨":"요청 실패"
}
export function occupancyResultHref(namespace:string,name?:string,uid?:string) {
  return `/measurements/results?${new URLSearchParams({kind:"dispatch-occupancy",namespace,...(name?{measurement:name}:{}),...(uid?{uid}:{})})}`
}
