import type { DatasetResource, DatasetSpec, PreprocessorResource, PreprocessorSpec, ModelResource, WorkloadSummary, ExecutionPath } from "./domain.ts"
import type { ActiveProtocol, Condition, MeasurementCoverageCell, MeasurementPlan, MeasurementPlanSpec, ProfilesView, InventoryClass, InventoryNode } from "./read-types.ts"
import { safeMeasurementReturn, measurementRequestName } from "./measurements.ts"
import { staticCompatibility } from "./measurement-coverage.ts"

export type RequestCondition = { model:string; deviceClass:string; node:string; path:string; powerMode:string }
export type RequestDefaults = { spec:MeasurementPlanSpec; source:string; exact:boolean }

export function requestHref(namespace:string,name:string,uid?:string) {
  return `/measurements/requests/${encodeURIComponent(name)}?${new URLSearchParams({namespace,...(uid?{uid}:{})})}`
}
export function requestRunHref(namespace:string,name:string,uid?:string,parent?:{name:string;uid:string},returnTo?:string) {
  const back=parent?requestHref(namespace,parent.name,parent.uid):`/measurements/requests?${new URLSearchParams({namespace,view:"runs"})}`
  const query=new URLSearchParams({namespace,...(uid?{uid}:{}),...(parent?{requestUID:parent.uid}:{run:name}),returnTo:safeMeasurementReturn(returnTo??null,back)})
  return parent?`/measurements/requests/${encodeURIComponent(parent.name)}/runs/${encodeURIComponent(name)}?${query}`:`/measurements/requests?${query}`
}
export function requestExecutionStatus(phase?:string) {
  return phase==="Complete"?"종료":phase==="Failed"?"실패":phase==="Measuring"?"진행 중":"미확인"
}

export function requestCopySource(name:string,namespace:string,uid:string|null,plans:MeasurementPlan[]) {
  const source=plans.find(plan=>plan.metadata.name===name&&plan.metadata.namespace===namespace)
  return !source?.metadata.uid||uid&&source.metadata.uid!==uid
    ?{source:undefined,error:"복사 원본의 저장 영역·식별자를 확인할 수 없어요. 같은 이름의 다른 요청으로 대체하지 않아요."}
    :{source,error:""}
}

export function requestCopyQuery(params:URLSearchParams,source:MeasurementPlan) {
  const query=new URLSearchParams(params)
  query.set("namespace",source.metadata.namespace)
  query.set("fromNamespace",source.metadata.namespace)
  query.set("fromUID",source.metadata.uid)
  return query.toString()
}

// Bind both successful checks to the complete input, including inherited source identity.
export function requestCheckCurrent(checked:string|null,current:string,admitted:boolean,resolves?:boolean) {
  return checked!==null&&checked===current&&admitted&&resolves===true
}

export function requestUsedResults(cells:MeasurementCoverageCell[],spec:MeasurementPlanSpec) {
  const candidate=spec.state.candidate
  return cells.filter(cell=>cell.model.name===candidate.modelSpec&&cell.deviceClass.name===candidate.deviceClass&&cell.executionPath===candidate.executionPath&&cell.powerMode===candidate.powerMode).flatMap(cell=>cell.variants.filter(variant=>{
    const condition=variant.condition,node=(condition.metricSource as {nodeName?:string}|undefined)?.nodeName??(condition.state as {targetNode?:string}|undefined)?.targetNode
    return node===spec.state.targetNode&&variant.assessment.scope.schedulerUsed===true
  }).flatMap(variant=>variant.resultRows))
}

// Reuse actual server values, never invent protocol constants.
export function requestDefaults(condition:RequestCondition, plans:MeasurementPlan[], profiles:ProfilesView["profiles"]):RequestDefaults|null {
  const same=(model:string,deviceClass:string,node:string,path:string,powerMode:string)=>!!condition.model&&!!condition.deviceClass&&!!condition.path&&!!condition.powerMode&&model===condition.model&&deviceClass===condition.deviceClass&&(!condition.node||node===condition.node)&&path===condition.path&&powerMode===condition.powerMode
  const newest=[...plans].sort((a,b)=>b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp)||a.metadata.name.localeCompare(b.metadata.name))
  const plan=newest.find(plan=>{const s=plan.spec;return same(s.state.candidate.modelSpec,s.state.candidate.deviceClass,s.state.targetNode,s.state.candidate.executionPath,s.state.candidate.powerMode)})
  if(plan)return {spec:{...plan.spec,activeProtocol:{...plan.spec.activeProtocol,workload:undefined}},source:`기존 요청 ${plan.metadata.name} (${plan.spec.state.candidate.deviceClass}, 동일 조건)`,exact:true}
  const profile=profiles?.filter(profile=>{const c=profile.condition;return same(c.logicalModel,c.deviceClass,profile.nodeName,c.artifact.executionPath,c.powerMode)}).sort((a,b)=>b.measuredAt.localeCompare(a.measuredAt)||a.id.localeCompare(b.id))[0]
  if(!profile) {
    const source=newest.find(plan=>plan.status?.phase==="Complete"&&!!condition.model&&plan.spec.state.candidate.modelSpec===condition.model)??newest.find(plan=>plan.status?.phase==="Complete"&&!!condition.path&&plan.spec.state.candidate.executionPath===condition.path)
    if(!source)return null
    const spec=source.spec
    return {exact:false,source:`기존 요청 ${source.metadata.name} (${spec.state.candidate.deviceClass}, ${spec.state.candidate.modelSpec===condition.model?"같은 모델":"같은 실행 경로"}의 최근 성공 측정 방식)`,spec:{...spec,state:{...spec.state,targetNode:"",candidate:{...spec.state.candidate,preprocessor:spec.state.candidate.modelSpec===condition.model?spec.state.candidate.preprocessor:""}},activeProtocol:{...spec.activeProtocol,workload:undefined,...(spec.state.candidate.modelSpec===condition.model?{}:{dataset:undefined})},batchSizes:undefined,trialsPerBatch:3}}
  }
  const c=profile.condition,active=c.activeProtocol,idle=c.idleProtocol
  return {exact:true,source:`측정 프로파일 ${profile.id} (동일 조건; 반복 3회는 화면 기본값, 재시도는 직접 지정)`,spec:{
    state:{candidate:{modelSpec:c.logicalModel,deviceClass:c.deviceClass,executionPath:c.artifact.executionPath,powerMode:c.powerMode,preprocessor:c.preprocessor.name},targetNode:profile.nodeName},
    activeProtocol:{warmupIterations:active.warmupIterations,durationSeconds:active.durationSeconds,dataset:active.dataset?.name,metricObservation:{intervalMilliseconds:active.metricIntervalMilliseconds,timeoutMilliseconds:active.metricTimeoutMilliseconds,durationSeconds:active.metricDurationSeconds}},
    idleProtocol:{settlingDurationSeconds:idle.settlingDurationSeconds,durationSeconds:idle.durationSeconds,metricObservation:{intervalMilliseconds:idle.metricIntervalMilliseconds,timeoutMilliseconds:idle.metricTimeoutMilliseconds,durationSeconds:idle.metricDurationSeconds}},
    trialsPerBatch:3,retryLimit:NaN,
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
export function requestInputs(model:string,artifact:string,preprocessor:string,dataset:string,context:{preprocessors:PreprocessorResource[];datasets:DatasetResource[];plans?:MeasurementPlan[];profiles?:ProfilesView["profiles"]},inherited:{preprocessor?:boolean;dataset?:boolean}={}) {
  const candidates=context.preprocessors.filter(item=>item.spec.artifacts.some(entry=>entry.target.model===model&&(!artifact||entry.target.artifact===artifact)))
  const history=[
    ...(context.plans??[]).filter(plan=>plan.spec.state.candidate.modelSpec===model).map(plan=>({at:plan.metadata.creationTimestamp,preprocessor:plan.spec.state.candidate.preprocessor,dataset:plan.spec.activeProtocol.dataset})),
    ...(context.profiles??[]).filter(profile=>profile.condition.logicalModel===model).map(profile=>({at:profile.measuredAt,preprocessor:profile.condition.preprocessor.name,dataset:profile.condition.activeProtocol.dataset?.name})),
  ].sort((a,b)=>b.at.localeCompare(a.at))
  const explicit=context.preprocessors.find(item=>item.metadata.name===preprocessor)
  const priorPre=history.map(entry=>candidates.find(item=>item.metadata.name===entry.preprocessor)).find(Boolean)
  const pre=explicit&&(!inherited.preprocessor||candidates.includes(explicit))?explicit:priorPre??(candidates.length===1?candidates[0]:undefined)
  const compatible=(item:DatasetResource)=>!!pre&&!datasetGap(item.spec,pre.spec)
  // Server subset compatibility cannot distinguish image classification from VQA. Never infer a task from surplus fields.
  const datasets=context.datasets.filter(item=>compatible(item)&&item.spec.fields.length===pre!.spec.input.fields.length)
  const priorDataset=history.map(entry=>context.datasets.find(item=>item.metadata.name===entry.dataset&&compatible(item))).find(Boolean)
  const explicitDataset=context.datasets.find(item=>item.metadata.name===dataset)
  const selected=explicitDataset&&(!inherited.dataset||compatible(explicitDataset)&&history.some(entry=>entry.dataset===dataset&&entry.preprocessor===pre?.metadata.name))?explicitDataset:priorDataset??(datasets.length===1?datasets[0]:undefined)
  const contract=pre?.spec.input.fields.map(field=>`${field.name} (${field.format}, 최대 ${field.maxBytes} B)`).join(", ")
  const warning=!pre?"모델·실행 경로에 연결된 전처리기의 입력 계약을 확인할 수 없어요. 카탈로그에서 전처리기 연결을 확인하고 직접 선택하세요.":!candidates.includes(pre)?"선택한 전처리기가 이 모델·실행 경로에 연결되어 있지 않아요. 카탈로그 연결을 확인하세요.":selected&&!compatible(selected)?`데이터셋이 전처리기 입력 조건에 맞지 않아요: ${datasetGap(selected.spec,pre.spec)}`:!selected?datasets.length>1?`입력 조건 ${contract}에 맞는 데이터셋이 여러 개예요. 사용할 데이터셋을 직접 선택하세요.`:`입력 조건 ${contract}과 필드 집합이 같은 유일한 데이터셋을 확인하지 못했어요. 등록 데이터셋을 직접 선택하세요. 추가 필드는 명시 선택할 때 허용해요.`:""
  return {preprocessor:pre?.metadata.name??"",dataset:selected?.metadata.name??"",warning}
}

// Compare retained facts only: absent fields are unknown, not proof of a change.
export function requestConditionChanges(previous:Record<string,unknown>,current:Record<string,unknown>|undefined) {
  const changes:{field:string;label:string;before:string;after:string}[]=[]
  const labels:Record<string,string>={nodeUID:"Node UID",runtimeImage:"실행 이미지",powerMode:"전력 모드","artifact.executionPath":"실행 경로","activeProtocol.dataset.name":"데이터셋","preprocessor.name":"전처리기"}
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
  if(/power.?mode|target.?node|candidate.*compatible|executionPath/i.test(reason))return {target:"measurement-device",fields:[],message:"실행 장비 조건 확인",constraints:[["실행 노드","준비 상태"],["전력 모드","장치 등록값"],["실행 경로","호환 선언"]]}
  return null
}

export function requestOperationalSummary(answer?:unknown,pools?:unknown) {
  const value=answer as {resolves?:boolean;campaign?:{spec?:MeasurementPlanSpec};batchPlan?:{plannedBatches:number[]}}|undefined
  const spec=value?.campaign?.spec,batches=value?.batchPlan?.plannedBatches??spec?.batchSizes
  const active=spec?.activeProtocol.durationSeconds,idle=spec?.idleProtocol.durationSeconds,settling=spec?.idleProtocol.settlingDurationSeconds
  // Warmup is an iteration count, not seconds. Only timed request intervals form this lower bound.
  const intervals=[active,idle,settling],trials=spec?.trialsPerBatch
  const known=value?.resolves===true&&batches&&trials!==undefined&&Number.isSafeInteger(trials)&&trials>0&&intervals.every(seconds=>typeof seconds==="number"&&Number.isFinite(seconds)&&seconds>=0)
  const seconds=known?(batches.length?trials*(batches.length*active!+settling!+idle!):0):null
  const view=pools as {workloads?:{workload:{uid:string};instances?:{node:{name:string};stopped:boolean}[]|null}[]|null;issues?:unknown[]|null}|undefined
  const servingImpact=value?.resolves===true&&spec?.state.targetNode&&view&&Array.isArray(view.workloads)&&!view.issues?.length&&view.workloads.every(pool=>Array.isArray(pool.instances))
    ?new Set(view.workloads.filter(pool=>pool.instances!.some(instance=>!instance.stopped&&instance.node.name===spec.state.targetNode)).map(pool=>pool.workload.uid)).size:null
  return {servingImpact,estimatedDurationSeconds:seconds!==null&&Number.isFinite(seconds)?seconds:null}
}

export function requestFailureFacts(conditions:Condition[]=[]):[string,string][] {
  return conditions.length?conditions.flatMap(condition=>[["서버 상태",`${condition.type}: ${condition.status}`],["서버 사유",condition.reason||"미제공"],["서버 메시지",condition.message||"미제공"]] as [string,string][]):[["서버 사유","미제공"],["서버 메시지","미제공"]]
}
export const steps = ["workload", "device", "run", "size", "advanced", "name", "review"] as const
export type Step = typeof steps[number]
const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

export type Advanced = { warmup: number; activeDuration: number; activeWindow: number; activeInterval: number; activeTimeout: number; settling: number; idleDuration: number; idleWindow: number; idleInterval: number; idleTimeout: number; retryLimit: number }
export const advancedFields: [key: keyof Advanced, label: string, min: number][] = [
  ["warmup", "측정 전 예열 횟수", 0],
  ["activeDuration", "측정 시간 · 초", 1],
  ["activeWindow", "전력 기록 시간 · 초", 1],
  ["activeInterval", "전력 기록 간격 · 밀리초", 1],
  ["activeTimeout", "전력 기록 대기 한도 · 밀리초", 1],
  ["settling", "쉬는 상태 안정 시간 · 초", 1],
  ["idleDuration", "쉬는 상태 측정 시간 · 초", 1],
  ["idleWindow", "쉬는 상태 전력 기록 시간 · 초", 1],
  ["idleInterval", "쉬는 상태 전력 기록 간격 · 밀리초", 1],
  ["idleTimeout", "쉬는 상태 전력 기록 대기 한도 · 밀리초", 1],
  ["retryLimit", "실패하면 다시 시도할 횟수", 0],
]
export type Protocol = { values: Advanced; from: string | null; spec?:MeasurementPlanSpec; target?:string; copied?:boolean }
// model, preprocessor and dataset come from the chosen Workload's spec refs (or a copied plan); the API takes catalog names.
export type Draft = { workload: string; model: string; preprocessor: string; dataset: string; explicitPreprocessor?:boolean; explicitDataset?:boolean; deviceClass: string; node: string; path: string; powerMode: string; batchSizes: number[]; trials: number | null; protocol: Protocol | null; name: string | null }
export type Context = { models: ModelResource[]; workloads: WorkloadSummary[]; classes: InventoryClass[]; plans: MeasurementPlan[]; preprocessors: PreprocessorResource[]; datasets: DatasetResource[]; profiles:ProfilesView["profiles"]; prefill?:boolean }

export const emptyDraft: Draft = { workload: "", model: "", preprocessor: "", dataset: "", deviceClass: "", node: "", path: "", powerMode: "", batchSizes: [], trials: null, protocol: null, name: null }
const blankAdvanced: Advanced = { warmup: NaN, activeDuration: NaN, activeWindow: NaN, activeInterval: NaN, activeTimeout: NaN, settling: NaN, idleDuration: NaN, idleWindow: NaN, idleInterval: NaN, idleTimeout: NaN, retryLimit: NaN }
function copyAdvanced(spec: MeasurementPlanSpec): Advanced {
  const active = spec.activeProtocol, idle = spec.idleProtocol
  return {
    warmup: active.warmupIterations ?? NaN, activeDuration: active.durationSeconds ?? NaN, activeWindow: active.metricObservation?.durationSeconds ?? NaN,
    activeInterval: active.metricObservation?.intervalMilliseconds ?? NaN, activeTimeout: active.metricObservation?.timeoutMilliseconds ?? NaN,
    settling: idle.settlingDurationSeconds ?? NaN, idleDuration: idle.durationSeconds ?? NaN, idleWindow: idle.metricObservation?.durationSeconds ?? NaN,
    idleInterval: idle.metricObservation?.intervalMilliseconds ?? NaN, idleTimeout: idle.metricObservation?.timeoutMilliseconds ?? NaN,
    retryLimit: spec.retryLimit,
  }
}
export function fillDefaults(draft:Draft,context:Context,inherited=false):Draft {
  const artifact=context.models.find(model=>model.metadata.name===draft.model)?.spec.executionPaths.find(path=>path.name===draft.path)?.artifact??""
  const inputs=draft.workload?{preprocessor:draft.preprocessor,dataset:draft.dataset}:requestInputs(draft.model,artifact,draft.preprocessor,draft.dataset,context,{preprocessor:inherited||!draft.explicitPreprocessor,dataset:inherited||!draft.explicitDataset})
  const filled={...draft,preprocessor:inputs.preprocessor,dataset:inputs.dataset}
  const target=JSON.stringify([filled.model,filled.deviceClass,filled.node,filled.path,filled.powerMode])
  if(filled.protocol&&(filled.protocol.copied||filled.protocol.from===null||filled.protocol.target===target))return filled
  const defaults=requestDefaults(filled,context.plans,context.profiles)
  if(!defaults)return filled
  const spec=defaults.spec
  return {...filled,node:filled.node||spec.state.targetNode,trials:filled.trials??spec.trialsPerBatch,protocol:{values:copyAdvanced(spec),from:defaults.source,spec,target:JSON.stringify([filled.model,filled.deviceClass,filled.node||spec.state.targetNode,filled.path,filled.powerMode])}}
}
// Only an explicit Workload choice can bind a changed input tuple.
export function updateDraft(current:Draft,patch:Partial<Draft>,context:Context,inherited=false):Draft {
  const next=fillDefaults({...current,...patch,...(patch.preprocessor!==undefined?{explicitPreprocessor:!!patch.preprocessor}:{}),...(patch.dataset!==undefined?{explicitDataset:!!patch.dataset}:{})},context,inherited)
  if(next.model===current.model&&next.preprocessor===current.preprocessor&&next.dataset===current.dataset)return next
  return {...next,workload:patch.workload||"",protocol:next.protocol?.spec?{...next.protocol,spec:{...next.protocol.spec,activeProtocol:{...next.protocol.spec.activeProtocol,workload:undefined}}}:next.protocol}
}
function advancedProblem(values: Advanced) {
  const bad = advancedFields.find(([key, , min]) => !Number.isInteger(values[key]) || values[key] < min || values[key] > 2147483647)
  if (bad) return `"${bad[1]}"에 ${bad[2]} 이상의 정수를 적어 주세요.`
  if (values.retryLimit > 10) return "다시 시도할 횟수는 10번까지 정할 수 있어요."
  if (values.activeTimeout >= values.activeInterval || values.idleTimeout >= values.idleInterval) return "전력 기록 대기 한도는 기록 간격보다 짧아야 해요."
  if (values.activeWindow < values.activeDuration || values.idleWindow < values.idleDuration) return "전력 기록 시간은 측정 시간보다 짧을 수 없어요."
  return ""
}
export const nodeReason = (node: InventoryNode) => node.ready !== "True" ? "지금 준비되지 않았어요" : node.schedulingDisabled ? "새 작업을 받지 않도록 막혀 있어요" : ""
// Only a proven static mismatch blocks a path; runtime support is checked by preflight.
export function pathReason(model: ModelResource, path: ExecutionPath, cls: InventoryClass) {
  const verdict = staticCompatibility(model, path, cls)
  return verdict.state === "unsupported" ? verdict.reason : ""
}
// Everything the steps read: current selections resolved against real data, plus the copied protocol.
export function view(d: Draft, { models, classes, plans, preprocessors, datasets }: Context) {
  const model = models.find(m => m.metadata.name === d.model)
  const cls = classes.find(c => c.name === d.deviceClass)
  const node = cls?.nodes.find(n => n.name === d.node)
  const path = model?.spec.executionPaths.find(p => p.name === d.path)
  const artifact = path && model?.spec.artifacts.find(a => a.name === path.artifact)
  const preprocessor=preprocessors.find(item=>item.metadata.name===d.preprocessor)
  const dataset=datasets.find(item=>item.metadata.name===d.dataset)
  const protocol: Protocol = d.protocol ?? { values: blankAdvanced, from: null }
  const trials = d.trials ?? 3
  const powerMode = cls?.powerModes.length === 1 ? cls.powerModes[0].name : d.powerMode
  const declared = artifact?.derivation?.declaredBatchSizes?.length ? artifact.derivation.declaredBatchSizes : null
  const max = path?.runtime.settings?.firstfloor?.maxBatchSize
  const taken = new Set(plans.map(p => p.metadata.name))
  const name = d.name ?? (model && node && path ? measurementRequestName({...d,powerMode}, taken) : "")
  function problem(step: Step): string {
    switch (step) {
      case "workload": return !d.workload?"측정할 Workload를 선택하세요. 등록된 Workload가 없으면 먼저 등록하세요.":!model?"모델을 선택하세요.":!preprocessor?"전처리기를 선택하세요.":!dataset?"데이터셋을 선택하세요.":datasetGap(dataset.spec,preprocessor.spec)
      case "device":
        if (!cls || !node) return "잴 장비를 골라 주세요."
        if (nodeReason(node)) return `${node.name}는 ${nodeReason(node)}. 다른 장비를 골라 주세요.`
        return model && !model.spec.executionPaths.some(p => !pathReason(model, p, cls)) ? "이 모델은 이 장비에서 실행할 수 없어요. 다른 장비를 골라 주세요." : ""
      case "run":
        if (!model || !cls || !path || pathReason(model, path, cls)) return "실행 방식을 골라 주세요."
        if (!cls.powerModes.length) return "이 장비에 등록된 전력 모드가 없어서 잴 수 없어요."
        return cls.powerModes.some(m => m.name === powerMode) ? "" : "전력 모드를 골라 주세요."
      case "size":
        if (!declared && (!d.batchSizes.length || (max && d.batchSizes.some(b => b > max)))) return max ? `한 번에 처리할 건수를 ${max}건 이하로 하나 이상 골라 주세요.` : "한 번에 처리할 건수를 하나 이상 골라 주세요."
        if (trials !== 3) return "반복 횟수는 정확히 3번이어야 해요."
        return ""
      case "advanced": return advancedProblem(protocol.values)
      case "name":
        if (!name) return "이름을 적어 주세요."
        if (name.length > 200 || !dns1123.test(name)) return "이름은 200자 이하의 영문 소문자, 숫자, '-', '.'로 적고, 처음과 끝은 영문 소문자나 숫자여야 해요."
        return taken.has(name) ? "같은 이름의 측정이 이미 있어요. 다른 이름을 적어 주세요." : ""
      case "review": return ""
    }
  }
  return { model, cls, path, protocol, trials, powerMode, declared, max, taken, name, problem }
}
export function manifest(d: Draft, context: Context, namespace: string) {
  const v = view(d, context)
  const p = v.protocol.values
  const activeProtocol = v.protocol.spec?.activeProtocol ?? {}
  const spec: MeasurementPlanSpec & {activeProtocol:ActiveProtocol} = {
    state: { candidate: { modelSpec: d.model, preprocessor: d.preprocessor, deviceClass: d.deviceClass, executionPath: d.path, powerMode: v.powerMode }, targetNode: d.node },
    activeProtocol: { ...activeProtocol, workload: d.workload, warmupIterations: p.warmup, durationSeconds: p.activeDuration, dataset: d.dataset, metricObservation: { intervalMilliseconds: p.activeInterval, timeoutMilliseconds: p.activeTimeout, durationSeconds: p.activeWindow } },
    idleProtocol: { ...v.protocol.spec?.idleProtocol, settlingDurationSeconds: p.settling, durationSeconds: p.idleDuration, metricObservation: { intervalMilliseconds: p.idleInterval, timeoutMilliseconds: p.idleTimeout, durationSeconds: p.idleWindow } },
    trialsPerBatch: v.trials,
    // The CRD forbids batchSizes when the artifact declares its own set.
    ...(v.declared ? {} : { batchSizes: [...d.batchSizes].sort((a, b) => a - b) }),
    retryLimit: p.retryLimit,
    ...(v.protocol.spec?.pointPolicy?{pointPolicy:v.protocol.spec.pointPolicy}:{}),
  }
  return { apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization", metadata: { name: v.name, namespace }, spec }
}

// `?from=` copies a whole existing plan; the funnel then opens at the first step that needs attention, else the name.
export function begin(from: string | null, context: Context) {
  const source = from ? context.plans.find(p => p.metadata.name === from) : undefined
  if (!source) return { draft: emptyDraft, index: 0, error: from ? "복사할 측정을 찾지 못해서 처음부터 골라요." : "", copied: false }
  const s = source.spec
  // A copied name is a selection only while that Workload is still one of the choices.
  const workload = context.workloads.some(item => item.desired.metadata.name === s.activeProtocol.workload) ? s.activeProtocol.workload! : ""
  const draft: Draft = { workload, model: s.state.candidate.modelSpec, preprocessor: s.state.candidate.preprocessor, dataset: s.activeProtocol.dataset ?? "", deviceClass: s.state.candidate.deviceClass, node: s.state.targetNode, path: s.state.candidate.executionPath, powerMode: s.state.candidate.powerMode, batchSizes: s.batchSizes ?? [], trials: s.trialsPerBatch, protocol: { values: copyAdvanced(s), from: source.metadata.name, spec:s, copied:true }, name: null }
  const v = view(draft, context)
  const open = steps.findIndex(step => step !== "name" && step !== "review" && v.problem(step))
  return { draft, index: open < 0 ? steps.indexOf("name") : open, error: open < 0 ? "" : v.problem(steps[open]), copied: true }
}
