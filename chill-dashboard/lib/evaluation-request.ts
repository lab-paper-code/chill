export type EvaluationSpec = Record<string, unknown>
export type EvaluationCheck = {id:string;passed:boolean;reason?:string}
export type EvaluationLiveTarget = {component:string;resource:{apiVersion:string;kind:string;namespace:string;name:string};liveImageIDs:string[];ready:boolean}
export type EvaluationPreflight = {checks:EvaluationCheck[];liveTarget?:EvaluationLiveTarget[]}
const record=(value:unknown):value is Record<string,unknown>=>!!value&&typeof value==="object"&&!Array.isArray(value)

export function evaluationSameSpec(left:unknown,right:unknown):boolean {
  if(Object.is(left,right))return true
  if(Array.isArray(left)&&Array.isArray(right))return left.length===right.length&&left.every((value,index)=>evaluationSameSpec(value,right[index]))
  if(record(left)&&record(right))return Object.keys(left).length===Object.keys(right).length&&Object.keys(left).every(key=>Object.hasOwn(right,key)&&evaluationSameSpec(left[key],right[key]))
  return false
}

export function evaluationActions(phase:string):{edit:boolean;remove:"삭제"|"중지";release:boolean} {
  return {edit:phase==="Pending",remove:["Pending","Done"].includes(phase)?"삭제":"중지",release:phase==="Blocked"}
}

export function evaluationPreflight(value:unknown):EvaluationPreflight|null {
  if(!record(value)||!Array.isArray(value.checks)||!value.checks.length||!value.checks.every(check=>record(check)&&typeof check.id==="string"&&typeof check.passed==="boolean"&&(check.reason===undefined||typeof check.reason==="string")))return null
  if(value.liveTarget!==undefined&&(!Array.isArray(value.liveTarget)||!value.liveTarget.every(target=>{
    if(!record(target)||!record(target.resource))return false
    const resource=target.resource
    return typeof target.component==="string"&&["apiVersion","kind","namespace","name"].every(key=>typeof resource[key]==="string")&&Array.isArray(target.liveImageIDs)&&target.liveImageIDs.every(image=>typeof image==="string")&&typeof target.ready==="boolean"
  })))return null
  return value as EvaluationPreflight
}

export function evaluationCanSubmit(answer:EvaluationPreflight|null,checkedBody:string,body:string,unknown:boolean):boolean {
  return !unknown&&!!answer?.checks.length&&answer.checks.every(check=>check.passed)&&checkedBody===body
}

export function evaluationReleaseBody(uid:string,reason:string):{uid:string;reason:string} {
  if(!uid.trim()||!reason.trim())throw new Error("확인 사유 필수")
  return {uid,reason:reason.trim()}
}
