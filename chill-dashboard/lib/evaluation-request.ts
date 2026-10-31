export type EvaluationSpec = Record<string, unknown>
export type EvaluationCheck = {id:string;passed:boolean;reason?:string}
export type EvaluationPreflight = {checks:EvaluationCheck[];liveTarget?:unknown}
export type EvaluationDigestChange = {name:string;before:string;after:string;images:string[]}
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

// Adopt only explicitly selected current image IDs for the same target identity.
export function evaluationDigestChanges(spec:EvaluationSpec,liveTarget:unknown):EvaluationDigestChange[] {
  const target=spec.target
  if(!record(target)||!Array.isArray(target.components)||!Array.isArray(liveTarget))return []
  const components=target.components
  return liveTarget.flatMap(current=>{
    if(!record(current)||current.match!==false||typeof current.component!=="string"||typeof current.specImage!=="string"||!Array.isArray(current.liveImageIDs)||!current.liveImageIDs.every(image=>typeof image==="string"))return []
    const matches=components.filter((component:unknown)=>record(component)&&component.name===current.component&&component.image===current.specImage)
    const component=matches.length===1?matches[0]:undefined
    if(!record(component)||!record(component.resource)||!record(current.resource))return []
    const resource=component.resource,liveResource=current.resource
    if(!["apiVersion","kind","namespace","name"].every(key=>typeof resource[key]==="string"&&resource[key]===liveResource[key]))return []
    const images=[...new Set(current.liveImageIDs as string[])].filter(Boolean)
    return [{name:current.component,before:current.specImage,after:images.length===1?images[0]:"",images}]
  })
}

export function evaluationAdoptDigests(spec:EvaluationSpec,changes:EvaluationDigestChange[],selected:string[]=[]):EvaluationSpec {
  const copy=structuredClone(spec),target=copy.target
  if(record(target)&&Array.isArray(target.components))for(const component of target.components) {
    if(!record(component)||!selected.includes(String(component.name)))continue
    const change=changes.find(change=>change.name===component.name&&change.before===component.image)
    if(change?.after&&change.images.includes(change.after)) {
      const digest=/(?:^|@|:\/\/)(sha256:[a-f0-9]{64})$/i.exec(change.after)?.[1]
      component.image=digest&&change.before.includes("@")?`${change.before.slice(0,change.before.lastIndexOf("@"))}@${digest}`:change.after
    }
  }
  return copy
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
