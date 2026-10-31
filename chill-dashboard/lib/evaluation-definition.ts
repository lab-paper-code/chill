export type EvaluationControls = Record<"placement"|"resources"|"replicas"|"routing"|"batching"|"power"|"membership",boolean>
export type DefinitionInjection = {id:string;action:string;offsetSeconds:number}
export type DefinitionStage = {id:string;row:string;mode:"autonomous"|"fixed"|"controls-disabled";loadScenarioRef:{name:string;uid:string};control?:{controls:EvaluationControls};failureInjection?:DefinitionInjection[]}
export type ComparisonReference = {stageID:string;historical?:never}|{stageID?:never;historical:{publicationID:string;stageID:string}}
export type EvaluationComparison = {left:ComparisonReference;right:ComparisonReference;reason:string}
export type EvaluationDefinition = {
  schema:"evaluation-definition.v1alpha1"
  run:{name:string;uid:string}
  spec:{question:string;control:{controls:EvaluationControls};loadGenerator:Record<string,unknown>;stages:DefinitionStage[];comparisons?:EvaluationComparison[]}
  loadScenarios:{name:string;uid:string;spec:{parts:{id:string;durationSeconds:number;rates?:Record<string,number>;alpha?:Record<string,number>}[]}}[]
  workloads:{name:string;uid:string;generation:number|bigint;slo:{percentile:number;targetLatencyNanos:number|bigint}}[]
}
const record=(value:unknown):value is Record<string,unknown>=>!!value&&typeof value==="object"&&!Array.isArray(value)
const text=(value:unknown):value is string=>typeof value==="string"&&value.length>0
const nonnegative=(value:unknown):value is number=>typeof value==="number"&&Number.isFinite(value)&&value>=0
const positiveInteger=(value:unknown):boolean=>typeof value==="bigint"?value>0n:typeof value==="number"&&Number.isSafeInteger(value)&&value>0
const controls=(value:unknown):boolean=>record(value)&&record(value.controls)&&["placement","resources","replicas","routing","batching","power","membership"].every(key=>typeof (value.controls as Record<string,unknown>)[key]==="boolean")
const reference=(value:unknown):value is Record<string,unknown>&{name:string;uid:string}=>record(value)&&text(value.name)&&text(value.uid)
const comparisonReference=(value:unknown):boolean=>{
  if(!record(value))return false
  if(text(value.stageID))return value.historical===undefined
  const h=value.historical
  return value.stageID===undefined&&record(h)&&text(h.publicationID)&&/^[A-Za-z0-9_-][A-Za-z0-9_.-]*$/.test(h.publicationID)&&text(h.stageID)
}
export function evaluationComparisons(value:unknown):EvaluationComparison[]|undefined {
  return Array.isArray(value)&&value.every(c=>record(c)&&text(c.reason)&&comparisonReference(c.left)&&comparisonReference(c.right))?value as EvaluationComparison[]:undefined
}
// Validate only the frozen fields this reader consumes, once at the HTTP boundary.
export function evaluationDefinition(value:unknown):EvaluationDefinition|undefined {
  if(!record(value)||value.schema!=="evaluation-definition.v1alpha1"||!reference(value.run)||!record(value.spec))return undefined
  const spec=value.spec,generator=spec.loadGenerator
  if(!text(spec.question)||!controls(spec.control)||!record(generator)||!text(generator.image)||!text(generator.sourceSHA)||!record(generator.resources)||!record(generator.shards)||!Object.keys(generator.shards).length||!Object.values(generator.shards).every(positiveInteger)||!record(generator.barrier)||!nonnegative(generator.barrier.maxStartSkewNanos)||!Number.isSafeInteger(generator.barrier.maxStartSkewNanos)||!positiveInteger(generator.barrier.minimumReadySamples)||!Array.isArray(spec.stages)||!spec.stages.length)return undefined
  if(!spec.stages.every(stage=>record(stage)&&text(stage.id)&&text(stage.row)&&["autonomous","fixed","controls-disabled"].includes(String(stage.mode))&&reference(stage.loadScenarioRef)&&(stage.control===undefined||controls(stage.control))))return undefined
  if(!spec.stages.every(stage=>stage.failureInjection===undefined||(Array.isArray(stage.failureInjection)&&stage.failureInjection.every((i:unknown)=>record(i)&&text(i.id)&&text(i.action)&&nonnegative(i.offsetSeconds))&&new Set(stage.failureInjection.map((i:DefinitionInjection)=>i.id)).size===stage.failureInjection.length)))return undefined
  const ids=spec.stages.map(stage=>stage.id)
  if(new Set(ids).size!==ids.length)return undefined
  const comparisons=evaluationComparisons(spec.comparisons)
  if(spec.comparisons!==undefined&&(!comparisons||!comparisons.every(c=>[c.left,c.right].every(ref=>ref.stageID===undefined||ids.includes(ref.stageID)))))return undefined
  if(!Array.isArray(value.loadScenarios)||!value.loadScenarios.length||!value.loadScenarios.every(s=>reference(s)&&record(s.spec)&&Array.isArray(s.spec.parts)&&s.spec.parts.length&&s.spec.parts.every((part:unknown)=>{
    if(!record(part)||!text(part.id)||!nonnegative(part.durationSeconds)||part.durationSeconds===0)return false
    const map=part.rates??part.alpha
    return (part.rates===undefined)!==(part.alpha===undefined)&&record(map)&&Object.keys(map).length>0&&Object.values(map).every(nonnegative)
  })))return undefined
  if(!Array.isArray(value.workloads)||!value.workloads.length||!value.workloads.every(w=>reference(w)&&positiveInteger(w.generation)&&record(w.slo)&&nonnegative(w.slo.percentile)&&w.slo.percentile>0&&w.slo.percentile<=100&&positiveInteger(w.slo.targetLatencyNanos)))return undefined
  const scenarios=value.loadScenarios
  if(new Set(scenarios.map(s=>s.uid)).size!==scenarios.length||new Set(value.workloads.map(w=>w.uid)).size!==value.workloads.length)return undefined
  if(!spec.stages.every(stage=>scenarios.some(s=>s.uid===stage.loadScenarioRef.uid)))return undefined
  return value as EvaluationDefinition
}
