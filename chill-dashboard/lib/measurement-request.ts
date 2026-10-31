import type { DatasetResource, PreprocessorResource } from "./domain.ts"
import type { MeasurementPlan, MeasurementPlanSpec, ProfilesView } from "./read-types.ts"

export type RequestCondition = { model:string; deviceClass:string; node:string; path:string; powerMode:string }
export type RequestDefaults = { spec:MeasurementPlanSpec; source:string; exact:boolean }

// Reuse actual server values, never invent protocol constants or copy another device's CPU allocation.
export function requestDefaults(condition:RequestCondition, plans:MeasurementPlan[], profiles:ProfilesView["profiles"]):RequestDefaults|null {
  const same=(model:string,deviceClass:string,node:string,path:string,powerMode:string)=>!!condition.model&&!!condition.deviceClass&&!!condition.path&&!!condition.powerMode&&model===condition.model&&deviceClass===condition.deviceClass&&(!condition.node||node===condition.node)&&path===condition.path&&powerMode===condition.powerMode
  const newest=[...plans].sort((a,b)=>b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp)||a.metadata.name.localeCompare(b.metadata.name))
  const plan=newest.find(plan=>{const s=plan.spec;return same(s.state.candidate.modelSpec,s.state.candidate.deviceClass,s.state.targetNode,s.state.candidate.executionPath,s.state.candidate.powerMode)})
  if(plan)return {spec:{...plan.spec,cpuAffinity:undefined},source:`기존 요청 ${plan.metadata.name} (${plan.spec.state.candidate.deviceClass}, 동일 조건)`,exact:true}
  const profile=profiles?.filter(profile=>{const c=profile.condition;return same(c.logicalModel,c.deviceClass,profile.nodeName,c.artifact.executionPath,c.powerMode)}).sort((a,b)=>b.measuredAt.localeCompare(a.measuredAt)||a.id.localeCompare(b.id))[0]
  if(!profile) {
    const source=newest.find(plan=>plan.status?.phase==="Complete"&&!!condition.model&&plan.spec.state.candidate.modelSpec===condition.model)??newest.find(plan=>plan.status?.phase==="Complete"&&!!condition.path&&plan.spec.state.candidate.executionPath===condition.path)
    if(!source)return null
    const spec=source.spec
    return {exact:false,source:`기존 요청 ${source.metadata.name} (${spec.state.candidate.deviceClass}, ${spec.state.candidate.modelSpec===condition.model?"같은 모델":"같은 실행 경로"}의 최근 성공 측정 방식)`,spec:{...spec,state:{...spec.state,targetNode:"",cpuRequest:undefined,candidate:{...spec.state.candidate,preprocessor:spec.state.candidate.modelSpec===condition.model?spec.state.candidate.preprocessor:""}},activeProtocol:{...spec.activeProtocol,...(spec.state.candidate.modelSpec===condition.model?{}:{dataset:undefined})},cpuQuota:undefined,cpuAffinity:undefined,batchSizes:undefined,trialsPerBatch:3}}
  }
  const c=profile.condition,active=c.activeProtocol,idle=c.idleProtocol
  return {exact:true,source:`측정 프로파일 ${profile.id} (동일 조건; 반복 3회는 화면 기본값, 재시도는 직접 지정)`,spec:{
    state:{candidate:{modelSpec:c.logicalModel,deviceClass:c.deviceClass,executionPath:c.artifact.executionPath,powerMode:c.powerMode,preprocessor:c.preprocessor.name},targetNode:profile.nodeName,cpuRequest:c.cpu.request},
    activeProtocol:{warmupIterations:active.warmupIterations,durationSeconds:active.durationSeconds,dataset:active.dataset?.name,metricObservation:{intervalMilliseconds:active.metricIntervalMilliseconds,timeoutMilliseconds:active.metricTimeoutMilliseconds,durationSeconds:active.metricDurationSeconds}},
    idleProtocol:{settlingDurationSeconds:idle.settlingDurationSeconds,durationSeconds:idle.durationSeconds,metricObservation:{intervalMilliseconds:idle.metricIntervalMilliseconds,timeoutMilliseconds:idle.metricTimeoutMilliseconds,durationSeconds:idle.metricDurationSeconds}},
    cpuQuota:c.cpu.limit,trialsPerBatch:3,retryLimit:NaN,
  }}
}

export function requestBatches(params:URLSearchParams) {
  if(!params.has("batches"))return {values:[] as number[],error:""}
  const raw=params.get("batches")??"",parts=raw.split(",")
  if(params.getAll("batches").length!==1||raw&&parts.some(part=>!/^\d+$/.test(part)||!Number.isSafeInteger(Number(part))||Number(part)<1||Number(part)>2147483647))return {values:[] as number[],error:"처리 건수를 읽을 수 없어요. 커버리지에서 조건을 다시 선택하세요."}
  if(!raw)return {values:[] as number[],error:""}
  return {values:[...new Set(parts.map(Number))].sort((a,b)=>a-b),error:""}
}

export function requestInitialBatches(batches:number[],max?:number) {
  return [...new Set(batches.length?batches:[1,2,4].filter(batch=>!max||batch<=max))].sort((a,b)=>a-b)
}

// Catalog contracts, not names, select the preprocessor and dataset.
export function requestInputs(model:string,artifact:string,preprocessor:string,dataset:string,context:{preprocessors:PreprocessorResource[];datasets:DatasetResource[]}) {
  const candidates=context.preprocessors.filter(item=>item.spec.artifacts.some(entry=>entry.target.model===model&&(!artifact||entry.target.artifact===artifact)))
  const pre=context.preprocessors.find(item=>item.metadata.name===preprocessor)??(candidates.length===1?candidates[0]:undefined)
  const compatible=(item:DatasetResource)=>!!pre&&pre.spec.input.fields.every(field=>item.spec.fields.some(input=>input.name===field.name&&input.format===field.format&&input.maxBytes<=field.maxBytes))
  const datasets=context.datasets.filter(compatible)
  const exact=datasets.filter(item=>item.spec.fields.length===pre?.spec.input.fields.length)
  const selected=context.datasets.find(item=>item.metadata.name===dataset)??(exact.length===1?exact[0]:datasets.length===1?datasets[0]:undefined)
  const warning=selected&&!compatible(selected)?"데이터셋 입력 계약이 전처리기와 일치하지 않습니다.":selected&&selected.spec.fields.length!==pre?.spec.input.fields.length?"데이터셋에 추가 필드가 있습니다. 모델·데이터셋 용도 일치를 검토하세요.":""
  return {preprocessor:pre?.metadata.name??"",dataset:selected?.metadata.name??"",warning}
}
