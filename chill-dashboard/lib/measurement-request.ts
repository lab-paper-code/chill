import type { DatasetResource, DatasetSpec, PreprocessorResource, PreprocessorSpec, ModelResource, WorkloadSummary, ExecutionPath } from "./domain.ts"
import type { ActiveProtocol, MeasurementPlan, MeasurementPlanSpec, MeasurementTiming, ProfilesView, InventoryClass, InventoryNode } from "./read-types.ts"
import { measurementRequestName } from "./measurements.ts"
import { staticCompatibility } from "./measurement-coverage.ts"

export type RequestCondition = { model:string; deviceClass:string; node:string; path:string; powerMode:string }
export type RequestDefaults = { spec:MeasurementPlanSpec; source:string; exact:boolean }

export function requestHref(namespace:string,name:string,uid?:string) {
  return `/measurements/requests/${encodeURIComponent(name)}?${new URLSearchParams({namespace,...(uid?{uid}:{})})}`
}

export function requestCopySource(name:string,namespace:string,uid:string|null,plans:MeasurementPlan[]) {
  const source=plans.find(plan=>plan.metadata.name===name&&plan.metadata.namespace===namespace)
  return !source?.metadata.uid||uid&&source.metadata.uid!==uid
    ?{source:undefined,error:"복사 원본 없음"}
    :{source,error:""}
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
    return {exact:false,source:`기존 요청 ${source.metadata.name} (${spec.state.candidate.deviceClass}, ${spec.state.candidate.modelSpec===condition.model?"같은 모델":"같은 실행 경로"}의 최근 성공 측정 방식)`,spec:{...spec,state:{...spec.state,targetNode:"",candidate:{...spec.state.candidate,preprocessor:spec.state.candidate.modelSpec===condition.model?spec.state.candidate.preprocessor:""}},activeProtocol:{...spec.activeProtocol,workload:undefined,...(spec.state.candidate.modelSpec===condition.model?{}:{dataset:undefined})}}}
  }
  const c=profile.condition,active=c.activeProtocol
  return {exact:true,source:`측정 프로파일 ${profile.id} (동일 조건)`,spec:{
    state:{candidate:{modelSpec:c.logicalModel,deviceClass:c.deviceClass,executionPath:c.artifact.executionPath,powerMode:c.powerMode,preprocessor:c.preprocessor.name},targetNode:profile.nodeName},
    activeProtocol:{warmupIterations:active.warmupIterations,dataset:active.dataset?.name},
  }}
}

// Mirror modelcatalog.Compatible: the preprocessor's inputs may be a subset of the dataset fields.
export function datasetGap(dataset:DatasetSpec,preprocessor:PreprocessorSpec|undefined) {
  const records=new Map(dataset.fields.map(field=>[field.name,field]))
  return (preprocessor?.input.fields??[]).map(field=>{
    const record=records.get(field.name)
    return !record?`${field.name} 필드 없음`:record.format!==field.format?`${field.name} 형식 불일치 (${field.format} 필요)`:record.maxBytes>field.maxBytes?`${field.name} 크기가 최대 ${field.maxBytes} B 초과 가능`:""
  }).filter(Boolean).join(", ")
}

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
  return {preprocessor:pre?.metadata.name??"",dataset:selected?.metadata.name??""}
}

export type RequestRejectionGuidance={target:string;fields:string[];message:string;constraints:[string,string][]}
export function requestRejectionGuidance(reason:string):RequestRejectionGuidance|null {
  if(/batch.*(?:maxBatchSize|positive|valid|supported)|planned batch size/i.test(reason))return {target:"measurement-batches",fields:[],message:"처리 건수 확인",constraints:[["처리 건수","≥ 1, ≤ 경로 최대 배치"],["모델 선언 배치","전체 선택"]]}
  if(/dataset|preprocessor/i.test(reason))return {target:"request-input-guidance",fields:[],message:"입력 연결 확인",constraints:[["전처리기","모델 파일 연결"],["데이터셋","입력 이름과 형식 일치, 크기 상한 이하"],["UID 또는 이미지 누락","카탈로그 등록 수정"]]}
  if(/power.?mode|target.?node|candidate.*compatible|executionPath/i.test(reason))return {target:"measurement-device",fields:[],message:"실행 장치 조건 확인",constraints:[["실행 장치","준비 상태"],["전력 모드","장치 등록값"],["실행 경로","호환 선언"]]}
  return null
}
export const steps = ["workload", "device", "run", "advanced", "name", "review"] as const
export type Step = typeof steps[number]
const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

export type Advanced = { warmup: number; requestLifetime:number; deliveryMargin:number; framedDecodedRequestUpperBytes:number
  meterLag:number; minimumPowerWindow:number; midLevelRatio:number; sparseGap:number; levelLimit:number; postLoadWindow:number; confidence:number; observationInterval:number; observationTimeout:number; maximumOperation:number; maximumGap:number }
export const advancedFields: [key: keyof Advanced, label: string, min: number, max?:number, step?:number|"any"][] = [
  ["warmup", "측정 전 예열 횟수", 0],
  ["requestLifetime", "요청 수명 (초)", 1],
  ["deliveryMargin", "응답 전달 여유 (밀리초)", 1],
  ["framedDecodedRequestUpperBytes", "디코딩된 요청 프레임 상한 (바이트)", 1, Number.MAX_SAFE_INTEGER],
  ["meterLag", "전력계 지연 (초)", 0, 3600, "any"],
  ["minimumPowerWindow", "최소 전력 관측 (초)", 0, 3600, "any"],
  ["midLevelRatio", "중간 수준 비율", 0, 1, "any"],
  ["sparseGap", "저부하 평균 간격 (초)", 0, 3600, "any"],
  ["levelLimit", "수준 시간 한도 (초)", 0, 86400, "any"],
  ["postLoadWindow", "부하 뒤 관측 (초)", 0, 3600, "any"],
  ["confidence", "신뢰 수준", 0.5, 1, "any"],
  ["observationInterval", "동작 전력 기록 간격 (밀리초)", 1],
  ["observationTimeout", "동작 전력 기록 대기 한도 (밀리초)", 1],
  ["maximumOperation", "동작 시간 한도 (초)", 1],
  ["maximumGap", "전력 기록 최대 공백 (밀리초)", 1],
]
export type Protocol = { values: Advanced; from: string | null; spec?:MeasurementPlanSpec; target?:string; copied?:boolean }
// model, preprocessor and dataset come from the chosen Workload's spec refs (or a copied plan); the API takes catalog names.
export type Draft = { workload: string; model: string; preprocessor: string; dataset: string; explicitPreprocessor?:boolean; explicitDataset?:boolean; deviceClass: string; node: string; path: string; powerMode: string; protocol: Protocol | null; name: string | null }
export type Context = { models: ModelResource[]; workloads: WorkloadSummary[]; classes: InventoryClass[]; plans: MeasurementPlan[]; preprocessors: PreprocessorResource[]; datasets: DatasetResource[]; profiles:ProfilesView["profiles"]; prefill?:boolean }

export const emptyDraft: Draft = { workload: "", model: "", preprocessor: "", dataset: "", deviceClass: "", node: "", path: "", powerMode: "", protocol: null, name: null }
// config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml: an operator can submit without typing any value.
export const defaultAdvanced: Advanced = {
  warmup: 20, requestLifetime: 30, deliveryMargin: 100, framedDecodedRequestUpperBytes: 2097152,
  meterLag: 3, minimumPowerWindow: 15, midLevelRatio: 0.25, sparseGap: 1, levelLimit: 300, postLoadWindow: 60, confidence: 0.95,
  observationInterval: 100, observationTimeout: 80, maximumOperation: 600, maximumGap: 500,
}
export const defaultTiming: MeasurementTiming = { changeIntervalP95Seconds: "1", maximumRequestSeconds: "0.08", maximumReceiptGapSeconds: "1", source: "sample values, not measured: replace with the Node meter's observed timing" }
// A copied value the schema rejects (absent, or 0 from a session profile's projected protocol) takes the default.
const positive = (value: number | undefined, fallback: number) => value !== undefined && value > 0 ? value : fallback
const finite = (value: number | undefined, fallback: number) => value !== undefined && Number.isFinite(value) ? value : fallback
function copyAdvanced(spec: MeasurementPlanSpec): Advanced {
  const session = spec.session, d = defaultAdvanced
  return {
    warmup: finite(spec.activeProtocol.warmupIterations, d.warmup),
    requestLifetime: positive(session?.requestLifetimeSeconds, d.requestLifetime), deliveryMargin: positive(session?.deliveryMarginMilliseconds, d.deliveryMargin),
    framedDecodedRequestUpperBytes: positive(session?.framedDecodedRequestUpperBytes, d.framedDecodedRequestUpperBytes),
    meterLag: finite(session?.meterLagSeconds, d.meterLag), minimumPowerWindow: positive(session?.minimumPowerWindowSeconds, d.minimumPowerWindow), midLevelRatio: positive(session?.midLevelRatio, d.midLevelRatio),
    sparseGap: positive(session?.sparseMeanGapSeconds, d.sparseGap), levelLimit: positive(session?.levelDurationLimitSeconds, d.levelLimit), postLoadWindow: positive(session?.postLoadWindowSeconds, d.postLoadWindow),
    confidence: positive(Number(session?.confidenceLevel), d.confidence), observationInterval: positive(session?.observation.intervalMilliseconds, d.observationInterval),
    observationTimeout: positive(session?.observation.requestTimeoutMilliseconds, d.observationTimeout), maximumOperation: positive(session?.observation.maximumOperationSeconds, d.maximumOperation),
    maximumGap: positive(session?.observation.maximumGapMilliseconds, d.maximumGap),
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
  return {...filled,node:filled.node||spec.state.targetNode,protocol:{values:copyAdvanced(spec),from:defaults.source,spec,target:JSON.stringify([filled.model,filled.deviceClass,filled.node||spec.state.targetNode,filled.path,filled.powerMode])}}
}
// Only an explicit Workload choice can bind a changed input tuple.
export function updateDraft(current:Draft,patch:Partial<Draft>,context:Context,inherited=false):Draft {
  const next=fillDefaults({...current,...patch,...(patch.preprocessor!==undefined?{explicitPreprocessor:!!patch.preprocessor}:{}),...(patch.dataset!==undefined?{explicitDataset:!!patch.dataset}:{})},context,inherited)
  if(next.model===current.model&&next.preprocessor===current.preprocessor&&next.dataset===current.dataset)return next
  return {...next,workload:patch.workload||"",protocol:next.protocol?.spec?{...next.protocol,spec:{...next.protocol.spec,activeProtocol:{...next.protocol.spec.activeProtocol,workload:undefined}}}:next.protocol}
}
const exclusiveZero: Partial<Record<keyof Advanced, true>> = { minimumPowerWindow: true, midLevelRatio: true, sparseGap: true, levelLimit: true, postLoadWindow: true }
/** Short inline error of one protocol value. */
export function advancedIssue(values: Advanced, key: keyof Advanced): string {
  const field = advancedFields.find(([name]) => name === key)
  if (!field) return ""
  const [, , min, max = 2147483647, step = 1] = field
  const value = values[key]
  if (key === "confidence" && !(value >= 0.5 && value < 1)) return "0.5 이상 1 미만"
  if (key === "midLevelRatio" && !(value > 0 && value < 1)) return "0과 1 사이 값"
  if (!Number.isFinite(value) || (step !== "any" && !Number.isInteger(value)) || value < min || (exclusiveZero[key] && value === 0) || value > max)
    return exclusiveZero[key] ? "0보다 큰 값" : step === "any" ? `${min} 이상` : `${min} 이상 정수`
  if (key === "observationTimeout" && value >= values.observationInterval) return "기록 간격보다 짧게"
  if (key === "levelLimit" && value <= values.meterLag + values.minimumPowerWindow) return "지연과 최소 관측의 합보다 길게"
  if (key === "deliveryMargin" && value >= values.requestLifetime * 1000) return "요청 수명보다 짧게"
  return ""
}
function advancedProblem(values: Advanced) {
  const key = (Object.keys(defaultAdvanced) as (keyof Advanced)[]).find(key => advancedIssue(values, key))
  if (!key) return ""
  const label = advancedFields.find(([name]) => name === key)![1]
  return `"${label}": ${advancedIssue(values, key)}`
}
export const nodeReason = (node: InventoryNode) => node.ready !== "True" ? "준비 안 됨" : node.schedulingDisabled ? "새 작업 차단됨" : ""
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
  const preprocessor=preprocessors.find(item=>item.metadata.name===d.preprocessor)
  const dataset=datasets.find(item=>item.metadata.name===d.dataset)
  const protocol: Protocol = d.protocol ?? { values: defaultAdvanced, from: null }
  const powerMode = cls?.powerModes.length === 1 ? cls.powerModes[0].name : d.powerMode
  const taken = new Set(plans.map(p => p.metadata.name))
  const name = d.name ?? (model && node && path ? measurementRequestName({...d,powerMode}, taken) : "")
  function problem(step: Step): string {
    switch (step) {
      case "workload": return !d.workload?"측정할 서비스 선택 필요, 없으면 서비스 먼저 등록":!model?"모델 선택 필요":!preprocessor?"전처리기 선택 필요":!dataset?"데이터셋 선택 필요":datasetGap(dataset.spec,preprocessor.spec)
      case "device":
        if (!cls || !node) return "장치 선택 필요"
        if (nodeReason(node)) return `${node.name}: ${nodeReason(node)}, 다른 장치 선택 필요`
        return model && !model.spec.executionPaths.some(p => !pathReason(model, p, cls)) ? "이 모델을 실행할 수 없는 장치, 다른 장치 선택 필요" : ""
      case "run":
        if (!model || !cls || !path || pathReason(model, path, cls)) return "실행 경로 선택 필요"
        if (!cls.powerModes.length) return "등록된 전력 모드가 없는 장치"
        return cls.powerModes.some(m => m.name === powerMode) ? "" : "전력 모드 선택 필요"
      case "advanced": return advancedProblem(protocol.values)
      case "name":
        if (!name) return "이름 필요"
        if (name.length > 200 || !dns1123.test(name)) return "이름은 200자 이하 영문 소문자, 숫자, '-', '.', 처음과 끝은 영문 소문자나 숫자"
        return taken.has(name) ? "같은 이름의 측정 있음" : ""
      case "review": return ""
    }
  }
  return { model, cls, path, protocol, powerMode, taken, name, problem }
}
export function manifest(d: Draft, context: Context, namespace: string) {
  const v = view(d, context)
  const p = v.protocol.values
  const spec: MeasurementPlanSpec & {activeProtocol:ActiveProtocol} = {
    state: { candidate: { modelSpec: d.model, preprocessor: d.preprocessor, deviceClass: d.deviceClass, executionPath: d.path, powerMode: v.powerMode }, targetNode: d.node },
    activeProtocol: { workload: d.workload, warmupIterations: p.warmup, dataset: d.dataset },
    session: {
      meterLagSeconds: p.meterLag, minimumPowerWindowSeconds: p.minimumPowerWindow, midLevelRatio: p.midLevelRatio, sparseMeanGapSeconds: p.sparseGap,
      levelDurationLimitSeconds: p.levelLimit, postLoadWindowSeconds: p.postLoadWindow, confidenceLevel: String(p.confidence),
      requestLifetimeSeconds: p.requestLifetime, deliveryMarginMilliseconds: p.deliveryMargin, framedDecodedRequestUpperBytes: p.framedDecodedRequestUpperBytes,
      observation: { intervalMilliseconds: p.observationInterval, requestTimeoutMilliseconds: p.observationTimeout, maximumOperationSeconds: p.maximumOperation, maximumGapMilliseconds: p.maximumGap,
        timing: v.protocol.spec?.session?.observation.timing ?? defaultTiming },
    },
  }
  return { apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization", metadata: { name: v.name, namespace }, spec }
}

// `?from=` copies a whole existing plan; the funnel then opens at the first step that needs attention, else the name.
export function begin(from: string | null, context: Context) {
  const source = from ? context.plans.find(p => p.metadata.name === from) : undefined
  if (!source) return { draft: emptyDraft, index: 0, error: from ? "복사할 측정 없음, 처음부터 선택" : "", copied: false }
  const s = source.spec
  // A copied name is a selection only while that Workload is still one of the choices.
  const workload = context.workloads.some(item => item.desired.metadata.name === s.activeProtocol.workload) ? s.activeProtocol.workload! : ""
  const draft: Draft = { workload, model: s.state.candidate.modelSpec, preprocessor: s.state.candidate.preprocessor, dataset: s.activeProtocol.dataset ?? "", deviceClass: s.state.candidate.deviceClass, node: s.state.targetNode, path: s.state.candidate.executionPath, powerMode: s.state.candidate.powerMode, protocol: { values: copyAdvanced(s), from: source.metadata.name, spec:s, copied:true }, name: null }
  const v = view(draft, context)
  const open = steps.findIndex(step => step !== "name" && step !== "review" && v.problem(step))
  return { draft, index: open < 0 ? steps.indexOf("name") : open, error: open < 0 ? "" : v.problem(steps[open]), copied: true }
}
