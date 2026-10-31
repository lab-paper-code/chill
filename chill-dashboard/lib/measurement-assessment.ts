import type { MeasurementAssessment, ResultRow } from "./read-types.ts"
import { measurementCheckText, measurementReasonSummary } from "./measurements.ts"

export function measurementUsage(assessment?:MeasurementAssessment|null) {
  return assessment?.scope?.schedulerUsed===true?"스케줄러 사용 중":assessment?.scope?.schedulerUsed===false?"스케줄러 미사용":"스케줄러 사용 여부 미확인"
}

export type MeasurementEvidenceCheck={label:string;value:string|number;unit:string}
export function measurementEvidenceChecks(assessment?:MeasurementAssessment|null,rows:ResultRow[]=[]) {
  const checks=assessment?.checklist?.checks??[],row=rows.find(row=>row.batch===1)??rows[0]
  const measured=!!row||checks.some(check=>["IncrementalConfidence","PowerSource","FrozenPowerWindowPolicy"].includes(check.code))
  const notes:MeasurementEvidenceCheck[]=[]
  if(measured) {
    const protocol=row?.condition.activeProtocol as {dataset?:{name?:string}}|undefined,ci=row?.confidence,mean=row?.incrementalJoulesPerItem
    const valid=ci&&Number.isFinite(ci.halfWidth)&&ci.halfWidth>=0&&ci.level>0&&ci.level<1
    const relative=typeof mean==="number"&&Number.isFinite(mean)&&mean!==0
    notes.push({label:"오차 반폭",value:valid?(relative?100*ci.halfWidth/Math.abs(mean!):ci.halfWidth):"미제공",unit:relative?"%":"J/건"})
    notes.push({label:"노드 전체 전력",value:row?.powerSource?.kind&&row.powerSource.nodeName?"장치 단독 전력 아님":"출처·범위 미제공",unit:""})
    notes.push({label:"데이터셋 메모 · 용도 일치 미검증",value:String(protocol?.dataset?.name??row?.condition.dataset??"상세 조건"),unit:""})
  }
  for(const check of checks.filter(check=>check.result!=="pass"&&!(check.code==="IncrementalConfidence"&&measured)))notes.push({label:measurementCheckText(check),value:check.result==="fail"?"불일치":check.result==="unknown"?"미확인":"참고",unit:""})
  return [...new Map(notes.map(note=>[`${note.label}/${note.value}/${note.unit}`,note])).values()]
}

export function assessmentReasons(assessment?:MeasurementAssessment|null) {
  return [...new Set((assessment?.reasons??[]).map(reason=>reason.label&&/[가-힣]/.test(reason.label)?reason.label:measurementReasonSummary(reason.message||reason.code)))]
}
