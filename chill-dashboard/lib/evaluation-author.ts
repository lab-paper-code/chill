import { parseSchedulerJSON, stringifySchedulerJSON } from "./scheduler.ts"
import { evaluationSameSpec } from "./evaluation-request.ts"

export type Spec = Record<string, unknown>
export type LoadScenario = {name:string;uid:string;spec:Spec;inUse:string[];createdAt?:string}
export type EvidenceInput = {store:string;key:string;size:number|bigint;kind:string;set?:string;name?:string}
export type WorkloadChange = {name:string;before:Spec;after:Spec}
export const object = (value:unknown):Spec => value!==null&&typeof value==="object"&&!Array.isArray(value)?value as Spec:{}
export const records = (value:unknown):Spec[] => Array.isArray(value)?value.map(object):[]
export function specValue(spec:Spec,path:readonly (string|number)[]):unknown {
  return path.reduce<unknown>((value,key)=>value!==null&&typeof value==="object"?(value as Spec)[key]:undefined,spec)
}
// Patch the original tree, not a reconstructed form model: future fields survive editing.
export function patchSpec(spec:Spec,path:readonly (string|number)[],value:unknown):Spec {
  const copy=structuredClone(spec)
  let node:Spec|unknown[]=copy
  for(let i=0;i<path.length-1;i++) {
    const key=path[i],next=Object.hasOwn(node,key)?(node as Spec)[key]:undefined
    if(next===null||typeof next!=="object") Object.defineProperty(node,key,{value:typeof path[i+1]==="number"?[]:{},enumerable:true,writable:true,configurable:true})
    node=(node as Spec)[key] as Spec
  }
  const key=path[path.length-1]
  if(value===undefined)delete (node as Spec)[key]
  else Object.defineProperty(node,key,{value,enumerable:true,writable:true,configurable:true})
  return copy
}
export function parseSpec(text:string):Spec {
  const parsed=parseSchedulerJSON(text)
  if(parsed===null||typeof parsed!=="object"||Array.isArray(parsed))throw new Error("JSON 객체 필요")
  return parsed as Spec
}
export const specJSON=(spec:unknown)=>stringifySchedulerJSON(spec,2)
// Copy-only cutover: remove named retired schema fields, never filter extensions.
export function copyAuthorDraft(source:{name?:string;uid?:string;spec:Spec},kind:"evaluation"|"load"):{name:string;spec:Spec;removedFields:string[]} {
  const spec=structuredClone(source.spec),removedFields:string[]=[]
  function remove(node:Spec,keys:string[],prefix="") {
    for(const key of keys)if(Object.hasOwn(node,key)){delete node[key];removedFields.push(`${prefix}${key}`)}
  }
  if(kind==="evaluation") {
    remove(spec,["control"])
    records(spec.stages).forEach((stage,index)=>remove(stage,["mode","startState","control"],`stages[${index}].`))
    records(object(spec.target).components).forEach((component,index)=>remove(component,["image","sourceSHA"],`target.components[${index}].`))
  } else {
    remove(spec,["alpha","kappaRef","rateBasisRef","seedRef","payloadManifestRef"])
    records(spec.parts).forEach((part,index)=>remove(part,["alpha"],`parts[${index}].`))
  }
  return {name:"",spec,removedFields}
}
export function workloadChanges(spec:Spec,live:unknown):WorkloadChange[] {
  const current=object(live)
  return records(spec.workloads).flatMap(before=>{
    const after=object(current[String(before.name)])
    return typeof after.uid==="string"&&(typeof after.generation==="number"||typeof after.generation==="bigint")&&(before.uid!==after.uid||String(before.generation)!==String(after.generation))?[{name:String(before.name),before,after}]:[]
  })
}
export function adoptWorkloads(spec:Spec,changes:WorkloadChange[],selected:string[]=[]):Spec {
  return {...structuredClone(spec),workloads:records(spec.workloads).map(workload=>{
    const change=changes.find(change=>selected.includes(change.name)&&change.name===workload.name&&change.before.uid===workload.uid&&String(change.before.generation)===String(workload.generation))
    return change?{...workload,uid:change.after.uid,generation:change.after.generation}:structuredClone(workload)
  })}
}
export const canDeleteLoad=(item:LoadScenario)=>Array.isArray(item.inUse)&&item.inUse.length===0&&!!item.uid
export function removeLoadWorkload(spec:Spec,index:number):Spec {
  const name=String(records(spec.workloads)[index]?.name??"")
  let next=patchSpec(spec,["workloads"],records(spec.workloads).filter((_,i)=>i!==index))
  const paths:(string|number)[][]=[["arrivalDistribution"],["traceRefs"],["seeds"],["datasetRefs"],...records(spec.parts).map((_,i)=>["parts",i,"rates"])]
  for(const path of paths)if(Object.hasOwn(object(specValue(next,path)),name))next=patchSpec(next,[...path,name],undefined)
  return next
}
export function evaluationReplacement(originalUID:string,expectedSpec:Spec,status:number,data:unknown):{state:"updated";uid:string}|{state:"unchanged"|"deleted"|"unknown"} {
  if(status===404)return {state:"deleted"}
  const item=object(data)
  if(status!==200||typeof item.uid!=="string"||!item.uid)return {state:"unknown"}
  if(item.uid===originalUID)return {state:"unchanged"}
  return evaluationSameSpec(item.spec,expectedSpec)?{state:"updated",uid:item.uid}:{state:"unknown"}
}
