export type EvaluationSpec = Record<string, unknown>
export type EvaluationCheck = {id:string;passed:boolean;reason?:string}
export type EvaluationPreflight = {checks:EvaluationCheck[];liveTarget?:unknown}
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
  return value as EvaluationPreflight
}

export function evaluationCanSubmit(answer:EvaluationPreflight|null,checkedBody:string,body:string,unknown:boolean):boolean {
  return !unknown&&!!answer?.checks.length&&answer.checks.every(check=>check.passed)&&checkedBody===body
}

export function evaluationReleaseBody(uid:string,reason:string):{uid:string;reason:string} {
  if(!uid.trim()||!reason.trim())throw new Error("UID·확인 사유 필수")
  return {uid,reason:reason.trim()}
}

// An uncertain mutation is never retried. Read both active and retained publications.
export async function evaluationOperation<T>(send:()=>Promise<T>,refresh:()=>Promise<unknown>):Promise<T> {
  try {return await send()} catch(error) {
    if(record(error)&&(error.unknownOutcome===true||error.status===0))await refresh().catch(()=>undefined)
    throw error
  }
}
