import type { DatasetResource, DatasetSpec, PreprocessorResource, PreprocessorSpec } from "./domain.ts"
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

export function requestTargetBatches(params:URLSearchParams,previousTarget?:number[],max?:number) {
  return params.has("batches")?requestBatches(params).values:previousTarget!==undefined?[...previousTarget]:requestInitialBatches([],max)
}

// Mirror modelcatalog.Compatible: the preprocessor's inputs may be a subset of the dataset fields.
export function datasetGap(dataset:DatasetSpec,preprocessor:PreprocessorSpec|undefined) {
  const records=new Map(dataset.fields.map(field=>[field.name,field]))
  return (preprocessor?.input.fields??[]).map(field=>{
    const record=records.get(field.name)
    return !record?`${field.name} 필드가 없어요`:record.format!==field.format?`${field.name} 형식이 달라요 (${field.format} 필요)`:record.maxBytes>field.maxBytes?`${field.name} 크기가 최대 ${field.maxBytes} B보다 클 수 있어요`:""
  }).filter(Boolean).join(" · ")
}

export const requestNumber=(value:number)=>Number.isFinite(value)?String(value):"—"

// Catalog contracts, not names, select the preprocessor and dataset.
export function requestInputs(model:string,artifact:string,preprocessor:string,dataset:string,context:{preprocessors:PreprocessorResource[];datasets:DatasetResource[]},inherited:{preprocessor?:boolean;dataset?:boolean}={}) {
  const candidates=context.preprocessors.filter(item=>item.spec.artifacts.some(entry=>entry.target.model===model&&(!artifact||entry.target.artifact===artifact)))
  const explicit=context.preprocessors.find(item=>item.metadata.name===preprocessor)
  const pre=explicit&&(!inherited.preprocessor||candidates.includes(explicit))?explicit:candidates.length===1?candidates[0]:undefined
  const compatible=(item:DatasetResource)=>!!pre&&!datasetGap(item.spec,pre.spec)
  const datasets=context.datasets.filter(compatible)
  const explicitDataset=context.datasets.find(item=>item.metadata.name===dataset)
  const selected=explicitDataset&&(!inherited.dataset||compatible(explicitDataset))?explicitDataset:datasets.length===1?datasets[0]:undefined
  const contract=pre?.spec.input.fields.map(field=>`${field.name} (${field.format}, 최대 ${field.maxBytes} B)`).join(", ")
  const warning=!pre?"모델·실행 경로에 연결된 전처리기의 입력 계약을 확인할 수 없어요. 카탈로그에서 전처리기 연결을 확인하고 직접 선택하세요.":!candidates.includes(pre)?"선택한 전처리기가 이 모델·실행 경로에 연결되어 있지 않아요. 카탈로그 연결을 확인하세요.":selected&&!compatible(selected)?`데이터셋이 전처리기 입력 조건에 맞지 않아요: ${datasetGap(selected.spec,pre.spec)}`:!selected?datasets.length>1?`입력 조건 ${contract}에 맞는 데이터셋이 여러 개예요. 사용할 데이터셋을 직접 선택하세요.`:`입력 조건 ${contract}에 맞는 등록 데이터셋이 없어요. 필드 이름·형식이 같고 최대 크기가 입력 한도 이하인 데이터셋을 등록하세요. 추가 필드는 허용해요.`:""
  return {preprocessor:pre?.metadata.name??"",dataset:selected?.metadata.name??"",warning}
}

// Compare retained facts only: absent fields are unknown, not proof of a change.
export function requestConditionChanges(previous:Record<string,unknown>,current:Record<string,unknown>|undefined) {
  const changes:{field:string;label:string;before:string;after:string}[]=[]
  const labels:Record<string,string>={nodeUID:"Node UID","cpu.affinityCPUs":"CPU 코어 고정","cpu.request":"CPU 요청량","cpu.limit":"CPU 상한",runtimeImage:"실행 이미지",powerMode:"전력 모드","artifact.executionPath":"실행 경로","activeProtocol.dataset.name":"데이터셋","preprocessor.name":"전처리기"}
  function compare(before:Record<string,unknown>,after:Record<string,unknown>,prefix="") {
    for(const [key,value] of Object.entries(before)) {
      if(key==="name"&&!prefix||key==="plannedBatchSizes"&&!prefix||value===undefined||value===null||after[key]===undefined||after[key]===null)continue
      const field=prefix?`${prefix}.${key}`:key,next=after[key]
      if(typeof value==="object"&&!Array.isArray(value)&&typeof next==="object"&&!Array.isArray(next))compare(value as Record<string,unknown>,next as Record<string,unknown>,field)
      else if(JSON.stringify(value)!==JSON.stringify(next))changes.push({field,label:labels[field]??field,before:typeof value==="string"?value:JSON.stringify(value),after:typeof next==="string"?next:JSON.stringify(next)})
    }
  }
  if(current)compare(previous,current)
  return changes
}

export function requestPreflightReason(reason:{code:string;message:string}) {
  const messages:Record<string,string>={
    VariantConditionChanged:"기존 결과와 현재 동결 실행 조건의 일치를 확인하지 못해 기존 배치 제외를 적용하지 않았어요. 기존 결과가 삭제되거나 미측정으로 바뀐 것은 아니며, 이 요청에서의 재사용만 확인되지 않았어요.",
    ImmutableImagesUnconfirmed:"실행·모델·전처리기 이미지의 불변 식별자를 모두 확인하지 못했어요. 현재는 요청 조건만 확인되어 기존 결과 재사용을 확정할 수 없어요.",
    DeclarationUIDsNotRetained:"보관된 동결 식별자는 일치하지만 당시 모델·장치 종류·전처리기 선언 UID가 남아 있지 않아요.",
    DeclaredBatchSizes:"모델 파일에 처리 건수 집합이 지정되어 있어 일부 건수만 선택할 수 없어요.",
    PlannedRequestOverlap:"같은 요청 조건으로 계획된 기존 요청이 있어요. 계획의 중복만으로 실제 실행이나 측정 완료를 확인할 수는 없어요.",
    RequestListUnconfirmed:"진행 중인 요청 목록을 확인하지 못했어요. 측정 중 배치가 비어 있어도 겹치는 대기 요청이 없다고 확정할 수는 없어요.",
    PlanUnresolved:"실행 계획을 확정하지 못해 기존 결과의 재사용과 추가 측정 범위를 확인할 수 없어요.",
  }
  return messages[reason.code]??reason.message.replace(/\s*\(FixtureOnly\)/g,"")
}

export type RequestRejectionGuidance={target:string;fields:string[];message:string;constraints:[string,string][]}
export function requestRejectionGuidance(reason:string):RequestRejectionGuidance|null {
  if(/model-loaded idle measurement protocol is invalid|curve plan idle.*protocol is invalid/i.test(reason))return {target:"measurement-settling",fields:["settling"],message:"유휴 측정 조건 확인",constraints:[["안정 시간 (s)","≥ 1"],["자동 유휴 예열 (회)","0"],["자동 유휴 데이터셋","없음"],["서버 유휴 배치 · ORT","1"]]}
  if(/invalid or under-covering metric observation request|metric.*(?:interval|timeout|duration)/i.test(reason))return {target:"measurement-activeWindow",fields:["activeWindow","activeTimeout","idleWindow","idleTimeout"],message:"전력 기록 조건 확인",constraints:[["전력 기록 시간","≥ 측정 시간"],["대기 한도","< 기록 간격"],["입력값","양의 정수"]]}
  if(/active measurement cannot contain settlingDurationSeconds|curve plan active.*protocol is invalid/i.test(reason))return {target:"measurement-activeDuration",fields:["activeDuration","warmup"],message:"실행 측정 조건 확인",constraints:[["측정 시간 (s)","≥ 1"],["예열 (회)","≥ 0"],["실행 중 안정 시간","미전송"]]}
  if(/batch.*(?:maxBatchSize|positive|valid|supported)|planned batch size/i.test(reason))return {target:"measurement-batches",fields:[],message:"처리 건수 확인",constraints:[["처리 건수","≥ 1 · ≤ 경로 최대 배치"],["모델 선언 배치","전체 선택"]]}
  if(/dataset|preprocessor/i.test(reason))return {target:"request-input-guidance",fields:[],message:"입력 연결 확인",constraints:[["전처리기","모델 파일 연결"],["데이터셋","입력 이름·형식 일치 · 크기 상한 이하"],["UID·이미지 누락","카탈로그 등록 수정"]]}
  if(/cpu|affinity/i.test(reason))return {target:"measurement-affinity",fields:["affinity"],message:"CPU 조건 확인",constraints:[["CPU 개수","≥ 1"],["코어 번호","≥ 0 · 쉼표 구분 · 미지정 가능"]]}
  if(/power.?mode|target.?node|candidate.*compatible|executionPath/i.test(reason))return {target:"measurement-device",fields:[],message:"실행 장비 조건 확인",constraints:[["실행 노드","준비 상태"],["전력 모드","장치 등록값"],["실행 경로","호환 선언"]]}
  return null
}

export function requestOperationalSummary(answer?:unknown) {
  const value=answer&&typeof answer==="object"?answer as {servingImpact?:unknown;estimatedDurationSeconds?:unknown}:undefined
  const impact=typeof value?.servingImpact==="string"&&value.servingImpact.trim()?value.servingImpact:"미제공"
  const seconds=value?.estimatedDurationSeconds
  return {servingImpact:impact,estimatedDurationSeconds:typeof seconds==="number"&&Number.isFinite(seconds)&&seconds>=0?seconds:null}
}
