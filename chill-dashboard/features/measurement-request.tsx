"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, decodeResponse, relay, warmingRetry, warmingDelay, isCoverageWarming, useChillMutation, useChillQuery, useSession } from "@/lib/client"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"
import { PreflightAnswer, preflightConclusion } from "@/features/read-pages"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { DatasetResource, PreprocessorResource, ExecutionPath, ModelList, ModelResource, WorkloadSummary } from "@/lib/domain"
import type { InventoryClass, InventoryNode, MeasurementMutation, MeasurementPlan, MeasurementPlanList, MeasurementPlanSpec, MeasurementPreflight, MeasurementVariant, ProfilesView } from "@/lib/read-types"
import { datasetGap, requestNumber, requestBatches, requestDefaults, requestInputs, requestTargetBatches, requestConditionChanges, requestPreflightReason, requestRejectionGuidance, requestCheckCurrent, requestCopySource, requestCopyQuery, requestHref, type RequestRejectionGuidance } from "@/lib/measurement-request"
import { parseMeasurementJSON } from "@/lib/measurement-summary"
import { deviceLabel, resolveMeasurementPrefill, serverCompatibility, staticCompatibility, type MeasurementPrefill } from "@/lib/measurement-coverage"
import { useCoverageInventory, useModelCompatibility, useMeasurementCoverage } from "@/features/measurement-coverage"
import { MeasurementNavigation } from "@/components/measurement-navigation"
import { executionPathLabel, measurementRequestName } from "@/lib/measurements"

const back: [string, string] = ["/measurements/requests", "측정 요청"]
const steps = ["workload", "device", "run", "size", "advanced", "name", "review"] as const
type Step = typeof steps[number]
const batchChoices = [1, 2, 4, 8, 16]
const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

type Advanced = { warmup: number; activeDuration: number; activeWindow: number; activeInterval: number; activeTimeout: number; settling: number; idleDuration: number; idleWindow: number; idleInterval: number; idleTimeout: number; retryLimit: number }
const advancedFields: [key: keyof Advanced, label: string, min: number][] = [
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
type Protocol = { values: Advanced; from: string | null; spec?:MeasurementPlanSpec }
// model, preprocessor and dataset come from the chosen Workload's spec refs (or a copied plan); the API takes catalog names.
type Draft = { workload: string; model: string; preprocessor: string; dataset: string; explicitPreprocessor?:boolean; explicitDataset?:boolean; deviceClass: string; node: string; path: string; powerMode: string; batchSizes: number[]; trials: number | null; protocol: Protocol | null; name: string | null }
type Context = { models: ModelResource[]; workloads: WorkloadSummary[]; classes: InventoryClass[]; plans: MeasurementPlan[]; preprocessors: PreprocessorResource[]; datasets: DatasetResource[]; profiles:ProfilesView["profiles"]; prefill?:boolean }

const emptyDraft: Draft = { workload: "", model: "", preprocessor: "", dataset: "", deviceClass: "", node: "", path: "", powerMode: "", batchSizes: [], trials: null, protocol: null, name: null }
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
function fillDefaults(draft:Draft,context:Context,inherited=false):Draft {
  const artifact=context.models.find(model=>model.metadata.name===draft.model)?.spec.executionPaths.find(path=>path.name===draft.path)?.artifact??""
  const inputs=requestInputs(draft.model,artifact,draft.preprocessor,draft.dataset,context,{preprocessor:inherited||!draft.explicitPreprocessor,dataset:inherited||!draft.explicitDataset})
  const filled={...draft,preprocessor:inputs.preprocessor,dataset:inputs.dataset}
  const defaults=requestDefaults(filled,context.plans,context.profiles)
  if(!defaults)return filled
  const spec=defaults.spec
  return {...filled,node:filled.node||spec.state.targetNode,trials:filled.trials??spec.trialsPerBatch,protocol:filled.protocol&&(inherited||filled.protocol.from===null)?filled.protocol:{values:copyAdvanced(spec),from:defaults.source,spec}}
}
function advancedProblem(values: Advanced) {
  const bad = advancedFields.find(([key, , min]) => !Number.isInteger(values[key]) || values[key] < min || values[key] > 2147483647)
  if (bad) return `"${bad[1]}"에 ${bad[2]} 이상의 정수를 적어 주세요.`
  if (values.retryLimit > 10) return "다시 시도할 횟수는 10번까지 정할 수 있어요."
  if (values.activeTimeout >= values.activeInterval || values.idleTimeout >= values.idleInterval) return "전력 기록 대기 한도는 기록 간격보다 짧아야 해요."
  if (values.activeWindow < values.activeDuration || values.idleWindow < values.idleDuration) return "전력 기록 시간은 측정 시간보다 짧을 수 없어요."
  return ""
}
const nodeReason = (node: InventoryNode) => node.ready !== "True" ? "지금 준비되지 않았어요" : node.schedulingDisabled ? "새 작업을 받지 않도록 막혀 있어요" : ""
// Only a proven static mismatch blocks a path; runtime support is checked by preflight.
function pathReason(model: ModelResource, path: ExecutionPath, cls: InventoryClass) {
  const verdict = staticCompatibility(model, path, cls)
  return verdict.state === "unsupported" ? verdict.reason : ""
}
function failure(error: ApiError) {
  const data = (error.data && typeof error.data === "object" ? error.data : {}) as { code?: string }
  return { code:data.code, message:data.code==="DashboardOriginDenied"?"현재 접속 주소에서 검사할 수 없어요. 관리자에게 허용 주소 설정을 요청하세요.":error.status===403?"측정 요청 권한이 없어요. 계정의 권한을 확인하세요.":error.status===401?"로그인이 만료됐어요. 다시 로그인하세요.":error.status===409?"같은 이름의 요청이나 조건이 변경됐어요. 목록을 확인하고 다시 검사하세요.":error.status===400||error.status===422?"측정 조건을 처리할 수 없어요. 모델·장치·측정 방식 값을 확인하세요.":"서버 응답을 확인하지 못했어요. 연결 상태를 확인한 뒤 다시 검사하세요." }
}

// Everything the steps read: current selections resolved against real data, plus the copied protocol.
function view(d: Draft, { models, classes, plans, preprocessors, datasets }: Context) {
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
      case "workload": return !model?"모델을 선택하세요.":!preprocessor?"전처리기를 선택하세요.":!dataset?"데이터셋을 선택하세요.":datasetGap(dataset.spec,preprocessor.spec)
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
        if (!Number.isInteger(trials) || trials < 3 || trials > 2147483647) return "반복 횟수는 3번 이상이어야 해요."
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
function manifest(d: Draft, context: Context, namespace: string) {
  const v = view(d, context)
  const p = v.protocol.values
  const spec: MeasurementPlanSpec = {
    state: { candidate: { modelSpec: d.model, preprocessor: d.preprocessor, deviceClass: d.deviceClass, executionPath: d.path, powerMode: v.powerMode }, targetNode: d.node },
    activeProtocol: { ...v.protocol.spec?.activeProtocol, warmupIterations: p.warmup, durationSeconds: p.activeDuration, dataset: d.dataset, metricObservation: { intervalMilliseconds: p.activeInterval, timeoutMilliseconds: p.activeTimeout, durationSeconds: p.activeWindow } },
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
function begin(from: string | null, context: Context) {
  const source = from ? context.plans.find(p => p.metadata.name === from) : undefined
  if (!source) return { draft: emptyDraft, index: 0, error: from ? "복사할 측정을 찾지 못해서 처음부터 골라요." : "", copied: false }
  const s = source.spec
  const draft: Draft = { workload: "", model: s.state.candidate.modelSpec, preprocessor: s.state.candidate.preprocessor, dataset: s.activeProtocol.dataset ?? "", deviceClass: s.state.candidate.deviceClass, node: s.state.targetNode, path: s.state.candidate.executionPath, powerMode: s.state.candidate.powerMode, batchSizes: s.batchSizes ?? [], trials: s.trialsPerBatch, protocol: { values: copyAdvanced(s), from: source.metadata.name, spec:s }, name: null }
  const v = view(draft, context)
  const open = steps.findIndex(step => step !== "name" && step !== "review" && v.problem(step))
  return { draft, index: open < 0 ? steps.indexOf("name") : open, error: open < 0 ? "" : v.problem(steps[open]), copied: true }
}

type Option = { value: string; label: ReactNode; hint?: ReactNode; disabled?: string }
// Pick one existing item; unavailable ones stay visible with the reason.
function Pick({ label, name, value, set, options }: { label: string; name: string; value: string; set: (value: string) => void; options: Option[] }) {
  return <fieldset className="rows-group"><legend className="rows-label">{label}</legend><ul className="rows">{options.map(option => <li key={option.value}><label className={`row ${option.disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer"}`}>
    <input type="radio" name={name} value={option.value} checked={value === option.value} disabled={!!option.disabled} onChange={() => set(option.value)} />
    <span className="row-main"><span className="row-text">{option.label}</span>{(option.disabled || option.hint) && <span className="row-hint">{option.disabled || option.hint}</span>}</span>
  </label></li>)}</ul></fieldset>
}
function AdvancedFields({ value, set, showErrors, rejection }: { value: Advanced; set: (value: Advanced) => void; showErrors:boolean; rejection:RequestRejectionGuidance|null }) {
  const timings=[{label:"실행 측정",duration:value.activeDuration,window:value.activeWindow,interval:value.activeInterval,timeout:value.activeTimeout},{label:"유휴 측정",duration:value.idleDuration,window:value.idleWindow,interval:value.idleInterval,timeout:value.idleTimeout}]
  return <div className="grid gap-4 sm:grid-cols-2">
    <section aria-label="측정 시간과 전력 기록 창" className="min-w-0 space-y-3 sm:col-span-2">
      <h3 className="font-medium">측정 시간과 전력 기록 창</h3>
      <p className="text-xs text-muted-foreground">길이 비교 · 실제 시작 시각 미제공 · 예열 시간 미제공</p>
      <table className="w-full text-right text-xs tabular-nums"><thead><tr><th className="text-left">단계</th><th>측정 (s)</th><th>기록 창 (s)</th><th>간격 (ms)</th><th>대기 (ms)</th></tr></thead><tbody>{timings.map(row=><tr key={row.label}><th className="text-left font-normal">{row.label}</th><td>{requestNumber(row.duration)}</td><td>{requestNumber(row.window)}</td><td>{requestNumber(row.interval)}</td><td>{requestNumber(row.timeout)}</td></tr>)}<tr><th className="text-left font-normal">유휴 안정</th><td>{requestNumber(value.settling)}</td><td colSpan={3}>—</td></tr><tr><th className="text-left font-normal">예열 (회)</th><td>{requestNumber(value.warmup)}</td><td colSpan={3}>시간 환산 불가</td></tr></tbody></table>
      {timings.map(row=>{const max=Math.max(row.duration,row.window);return Number.isFinite(max)&&max>0&&row.duration>=0&&row.window>=0?<div key={row.label} className="space-y-1 text-xs" aria-label={`${row.label} 상대 길이`}><span>{row.label} · 측정 시간</span><div className="h-2 rounded bg-primary" style={{width:`${row.duration/max*100}%`}}/><span>전력 기록 창</span><div className="h-2 rounded bg-muted-foreground" style={{width:`${row.window/max*100}%`}}/></div>:null})}
    </section>
    {advancedFields.map(([key, label, min]) => {
      const issue=!Number.isInteger(value[key])||value[key]<min||value[key]>(key==="retryLimit"?10:2147483647)?"정수 범위 확인":key==="activeTimeout"&&value.activeTimeout>=value.activeInterval||key==="idleTimeout"&&value.idleTimeout>=value.idleInterval?"기록 간격 초과":key==="activeWindow"&&value.activeWindow<value.activeDuration||key==="idleWindow"&&value.idleWindow<value.idleDuration?"측정 시간 미달":""
      const serverIssue=rejection?.fields.includes(key)?"조건 재확인":"",message=showErrors&&issue?issue:serverIssue
      return <label htmlFor={`measurement-${key}`} key={key} className="grid gap-2 text-sm">{label}<Input id={`measurement-${key}`} aria-invalid={!!message} aria-describedby={message?`measurement-${key}-error`:undefined} type="number" inputMode="numeric" min={min} max={key === "retryLimit" ? 10 : 2147483647} step={1} value={Number.isNaN(value[key]) ? "" : value[key]} onChange={event => set({ ...value, [key]: event.target.valueAsNumber })} />{message&&<span id={`measurement-${key}-error`} className="text-destructive">{message}</span>}</label>
    })}
    <table aria-label="수치 제약" className="w-full text-left text-xs tabular-nums sm:col-span-2"><thead><tr><th>항목</th><th>최소</th><th>최대</th></tr></thead><tbody>{advancedFields.map(([key,label,min])=><tr key={key}><th className="font-normal">{label}</th><td>{min}</td><td>{key==="retryLimit"?10:"제한 없음"}</td></tr>)}</tbody></table>
  </div>
}

export function MeasurementRequestPage() {
  const { config, token } = useSession(), params = useSearchParams(),router=useRouter()
  const from = params.get("from"),namespace=params.get("namespace")??config.namespace
  const plans = useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations?view=summary`,{decoder:parseMeasurementJSON})
  const models = useChillQuery<ModelList>("v1/models")
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const preprocessors = useChillQuery<{items:PreprocessorResource[]}>("v1/preprocessors")
  const datasets = useChillQuery<{items:DatasetResource[]}>("v1/datasets")
  const inventory = useCoverageInventory()
  const coverage=useMeasurementCoverage(namespace,!!params.get("variantId"))
  const profiles=useQuery<ProfilesView,ApiError>({queryKey:["chill","request-profile-history"],enabled:!!token,retry:warmingRetry,retryDelay:warmingDelay,queryFn:async({signal})=>{
    const all:NonNullable<ProfilesView["profiles"]>=[]
    let offset=0
    for(;;) {
      const response=await relay(`v1/scheduler/profiles?${new URLSearchParams({view:"summary",limit:"50",offset:String(offset)})}`,token,signal)
      const page=decodeResponse(response.body,parseMeasurementJSON) as ProfilesView
      all.push(...page.profiles??[])
      if(page.page?.nextOffset===undefined)return {...page,profiles:all}
      if(page.page.nextOffset<=offset)throw new ApiError(0,"프로파일 페이지가 전진하지 않습니다",null)
      offset=page.page.nextOffset
    }
  }})
  const copy=from?requestCopySource(from,namespace,params.get("fromUID")||params.get("uid"),plans.data?.items??[]):undefined
  useEffect(()=>{
    if(!copy?.source||params.get("fromNamespace")&&params.get("fromNamespace")!==namespace)return
    const pinned=requestCopyQuery(new URLSearchParams(params),copy.source)
    if(pinned!==params.toString())router.replace(`/measurements/requests/new?${pinned}`,{scroll:false})
  },[copy?.source,namespace,params,router])
  if(params.get("variantId")&&!coverage.data&&!coverage.error)return <Screen title="측정 요청"><p role="status">선택 조건 조회 중</p></Screen>
  if (!plans.data || !models.data || !workloads.data || !inventory.data || !preprocessors.data || !datasets.data || profiles.isPending || profiles.error) return <Screen back={back} title="측정 요청">
    <MeasurementNavigation active="requests" namespace={namespace}/>
    <QueryGate query={plans}><QueryGate query={models}><QueryGate query={workloads}><QueryGate query={inventory}><QueryGate query={preprocessors}><QueryGate query={datasets}><QueryGate query={profiles}>{null}</QueryGate></QueryGate></QueryGate></QueryGate></QueryGate></QueryGate></QueryGate>
  </Screen>
  const prefill = resolveMeasurementPrefill(new URLSearchParams(params), namespace, inventory.data)
  const batches=requestBatches(new URLSearchParams(params));if(batches.error)prefill.errors.push(batches.error)
  if(copy?.error)prefill.errors.push(copy.error)
  const context: Context = { models: models.data.items, workloads: workloads.data, classes: inventory.data.classes, plans: plans.data.items ?? [], preprocessors:preprocessors.data?.items??[], datasets:datasets.data?.items??[], profiles:profiles.data?.profiles??[], prefill:prefill.active }
  const selected=coverage.data?.cells.flatMap(cell=>cell.variants).find(variant=>variant.id===params.get("variantId"))
  return <Funnel key={params.toString()} namespace={namespace} from={from} context={context} prefill={prefill} selected={selected} refetchPlans={plans.refetch} checking={plans.isFetching} />
}

function Funnel({ namespace, from, context, prefill, selected, refetchPlans, checking }: { namespace: string; from: string | null; context: Context; prefill:MeasurementPrefill; selected?:MeasurementVariant; refetchPlans: () => Promise<{ error: ApiError | null }>; checking: boolean }) {
  const router = useRouter(), params=useSearchParams()
  const dry = useChillMutation(), pre = useChillMutation(), send = useChillMutation()
  const variantId=params.get("variantId")??""
  const source=selected&&context.plans.find(plan=>selected.requestIds.includes(`${namespace}/${plan.metadata.name}/${plan.metadata.uid}`))
  const copy=from?requestCopySource(from,namespace,params.get("fromUID")||params.get("uid"),context.plans):undefined
  const [copiedIdentity]=useState(()=>copy?.source?.metadata)
  const sourceChanged=!!copiedIdentity&&copy?.source?.metadata.uid!==copiedIdentity.uid
  const copyError=copy?.error||(sourceChanged?"복사 원본 식별자가 변경됐어요. 같은 이름의 다른 요청으로 대체하지 않아요.":"")
  const [d, setD] = useState<Draft>(() => {
    if(source) {
      const copied=begin(source.metadata.name,context).draft
      const max=context.models.find(model=>model.metadata.name===copied.model)?.spec.executionPaths.find(path=>path.name===copied.path)?.runtime.settings?.firstfloor?.maxBatchSize
      return fillDefaults({...copied,...(prefill.active?{model:prefill.values.model,deviceClass:prefill.values.deviceClass,node:prefill.node,path:prefill.values.path,powerMode:prefill.values.powerMode}:{}),batchSizes:requestTargetBatches(new URLSearchParams(params),selected?.batchProgress.missing??copied.batchSizes,max),name:null},context,true)
    }
    const max=context.models.find(model=>model.metadata.name===prefill.values.model)?.spec.executionPaths.find(path=>path.name===prefill.values.path)?.runtime.settings?.firstfloor?.maxBatchSize
    const draft=prefill.active ? {...emptyDraft,model:prefill.values.model,deviceClass:prefill.values.deviceClass,node:prefill.node,path:prefill.values.path,powerMode:prefill.values.powerMode,batchSizes:requestTargetBatches(new URLSearchParams(params),selected?.batchProgress.missing,max)} : {...begin(copy?.source?from:null,context).draft,...(!from&&params.has("batches")?{batchSizes:requestTargetBatches(new URLSearchParams(params))}:{})}
    return fillDefaults(draft,context,true)
  })
  const [error,setError]=useState(""),[unknown,setUnknown]=useState(false),[checked,setChecked]=useState(false)
  const [showUnknown,setShowUnknown]=useState(false)
  const [stage,setStage]=useState(0),[checkedInput,setCheckedInput]=useState<string|null>(null)
  const v=view(d,context), listPath=`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
  const support=useModelCompatibility(d.model,d.path,d.deviceClass,v.cls?.nodes.filter(node=>node.name===d.node)??[],v.model?.metadata.uid,v.cls?.uid)
  const verdict=v.model&&v.path&&v.cls?serverCompatibility(staticCompatibility(v.model,v.path,v.cls),support.data??[]):undefined
  const answer=pre.data?.data as MeasurementPreflight|undefined
  const explanation=answer?{...answer,...(answer.batchPlan?{batchPlan:{...answer.batchPlan,reasons:answer.batchPlan.reasons.map(reason=>({...reason,message:requestPreflightReason(reason)}))}}:{})}:undefined
  const rejectedReason=pre.error&&[400,422].includes(pre.error.status)?`${pre.error.body} ${JSON.stringify(pre.error.data)}`:answer?.resolves===false?answer.reason??"":""
  const rejection=requestRejectionGuidance(rejectedReason)
  const showAdvanced=!!rejection?.fields.length||!!error&&!!v.problem("advanced")
  useEffect(()=>{
    if(!rejectedReason&&!error)return
    for(const detail of document.querySelectorAll<HTMLDetailsElement>("#measurement-conditions,#measurement-advanced"))if(detail.id==="measurement-conditions"||showAdvanced)detail.open=true
    if(rejectedReason)document.getElementById("measurement-rejection")?.focus()
  },[rejectedReason,error,showAdvanced])
  const changes=selected&&answer?requestConditionChanges(selected.condition,answer.plan?.shared as unknown as Record<string,unknown>|undefined):[]
  const admitted=dry.isSuccess&&(dry.data?.data as MeasurementMutation|null)?.request==="NOT_SUBMITTED"
  const identities={source:params.toString(),copiedIdentity,model:v.model?.metadata,device:v.cls&&{uid:v.cls.uid,resourceVersion:v.cls.resourceVersion},node:v.cls?.nodes.find(node=>node.name===d.node),preprocessor:context.preprocessors.find(item=>item.metadata.name===d.preprocessor)?.metadata,dataset:context.datasets.find(item=>item.metadata.name===d.dataset)?.metadata,showUnknown}
  const inputKey=JSON.stringify({body:manifest(d,context,namespace),...identities})
  const passed=requestCheckCurrent(checkedInput,inputKey,admitted,answer?.resolves)
  const pending=dry.isPending||pre.isPending
  const update=(patch:Partial<Draft>)=>{
    setD(current=>fillDefaults({...current,...patch,...(patch.preprocessor!==undefined?{explicitPreprocessor:!!patch.preprocessor}:{}),...(patch.dataset!==undefined?{explicitDataset:!!patch.dataset}:{})},context))
    setError("");setCheckedInput(null);dry.reset();pre.reset();send.reset()
  }
  function verify() {
    const issue=steps.filter(step=>step!=="review").map(step=>v.problem(step)).find(Boolean)
    if(issue){setError(issue);setStage(v.problem("workload")?0:1);requestAnimationFrame(()=>document.getElementById("measurement-errors")?.focus());return}
    if(verdict?.state==="unsupported"){setError(verdict.reason);return}
    if(verdict?.state==="unknown"&&!showUnknown){setError("지원 미확인 조합을 검사하려면 명시 검사 항목을 선택하세요.");return}
    // Keep the checked identity through success, transport uncertainty and manual identity lookup.
    const pinned={...d,name:v.name};setD(pinned);setError("")
    const body=manifest(pinned,context,namespace)
    setCheckedInput(JSON.stringify({body,...identities}))
    dry.mutate({path:`${listPath}?dryRun=All`,method:"POST",body})
    pre.mutate({path:`${listPath}/preflight${variantId?`?${new URLSearchParams({variantId})}`:""}`,method:"POST",body})
  }
  function submit() {
    if(!passed||unknown)return
    send.mutate({path:listPath,method:"POST",body:manifest(d,context,namespace)},{
      onSuccess:response=>{const task=(response.data as MeasurementMutation|null)?.task;router.replace(requestHref(task?.metadata.namespace??namespace,task?.metadata.name??v.name,task?.metadata.uid))},
      onError:e=>{if(e.unknownOutcome||e.status===0||failure(e).code==="MeasurementAuditIncomplete")setUnknown(true)},
    })
  }
  const chips=[...new Set([...batchChoices.filter(batch=>!v.max||batch<=v.max),...d.batchSizes])].sort((a,b)=>a-b)
  const blocked=prefill.errors.length>0||!!copyError
  const found=checked?context.plans.find(plan=>plan.metadata.name===v.name):undefined
  const remaining=steps.filter(step=>step!=="review").map(step=>({step,issue:v.problem(step)})).filter(item=>item.issue)
  const invalidStep=error?steps.find(step=>v.problem(step)===error):undefined
  const inputs=requestInputs(d.model,v.path?.artifact??"",d.preprocessor,d.dataset,context)
  const preprocessor=context.preprocessors.find(item=>item.metadata.name===d.preprocessor)
  const noCompatibleDataset=!!preprocessor&&!context.datasets.some(item=>!datasetGap(item.spec,preprocessor.spec))
  function next() {
    const issue=stage===0?v.problem("workload"):steps.filter(step=>step!=="workload"&&step!=="review").map(step=>v.problem(step)).find(Boolean)
    if(issue){setError(issue);return}
    setError("");setStage(stage+1)
  }
  return <Screen width="data" title="측정 요청" back={back}>
    <MeasurementNavigation active="requests" namespace={namespace}/>
    <nav aria-label="요청 단계"><ol className="flex flex-wrap gap-3">{["대상","조건","검사·제출"].map((label,index)=><li key={label}><Button variant={stage===index?"default":"outline"} aria-current={stage===index?"step":undefined} disabled={index>stage||unknown||pending||send.isPending} onClick={()=>setStage(index)}>{label}</Button></li>)}</ol></nav>
    {unknown&&<section role="alert" className="notice space-y-3"><h2>접수 결과 미확인</h2><p>{v.name}: 자동 재제출·이름 변경 차단</p><Button disabled={checking} onClick={()=>void refetchPlans().then(result=>{setChecked(!result.error);if(result.error)setError("목록 조회 실패: 다시 확인하세요.")})}>같은 이름의 접수 기록 조회</Button>{found?<Link className="underline" href={requestHref(namespace,found.metadata.name,found.metadata.uid)}>접수된 요청 열기</Link>:checked&&<p>목록 미확인 · 자동 재제출 차단</p>}</section>}
    {variantId&&!selected&&<p role="status" className="notice">조건 미확인 · 부족 배치 자동 제외 안 됨</p>}
    {stage===0&&prefill.active&&<div aria-label="선택 조건 요약" className="flex min-h-11 flex-wrap items-center gap-x-4 gap-y-1 border-y py-2 text-sm"><span>장치 <strong>{deviceLabel(d.deviceClass)}</strong></span><span>Node <strong>{d.node||"미선택"}</strong></span><span>전력모드 <strong>{v.powerMode||"미선택"}</strong></span><span>경로 <strong>{d.path?executionPathLabel(d.path):"미선택"}</strong></span><Button variant="outline" disabled={unknown||pending||send.isPending} onClick={()=>{setStage(1);requestAnimationFrame(()=>{const conditions=document.getElementById("measurement-conditions") as HTMLDetailsElement|null;if(conditions)conditions.open=true})}}>조건 변경</Button></div>}
    <fieldset disabled={unknown||pending||send.isPending} className="space-y-8 disabled:opacity-60">
      <section hidden={stage!==0} className="grid gap-5 border-t pt-5 md:grid-cols-2" aria-label="측정 대상">
        <label className="grid content-start gap-2 text-sm">모델<select aria-invalid={!!error&&!v.model} aria-describedby={error&&v.problem("workload")?"measurement-workload-error":undefined} className="min-h-11 w-full rounded border bg-background p-2" value={d.model} onChange={event=>{const model=context.models.find(item=>item.metadata.name===event.target.value);update({model:event.target.value,workload:"",preprocessor:"",dataset:"",...(!model?.spec.executionPaths.some(path=>path.name===d.path)?{path:""}:{})})}}><option value="">모델 선택</option>{context.models.map(model=><option key={model.metadata.uid} value={model.metadata.name}>{model.metadata.name}</option>)}</select></label>
        <div className="space-y-3"><label className="grid gap-2 text-sm">등록 서비스에서 조건 가져오기 (선택)<select className="min-h-11 rounded-md border bg-background px-3" value={d.workload} onChange={event=>{
          const workload=context.workloads.find(item=>item.desired.metadata.name===event.target.value)
          if(!workload){update({workload:""});return}
          const spec=workload.desired.spec,changed=spec.modelRef.name!==d.model
          update({workload:workload.desired.metadata.name,model:spec.modelRef.name,preprocessor:spec.preprocessorRef.name,dataset:spec.datasetRef?.name??"",...(changed&&!context.models.find(model=>model.metadata.name===spec.modelRef.name)?.spec.executionPaths.some(path=>path.name===d.path)?{path:""}:{})})
        }}><option value="">직접 조건 지정</option>{context.workloads.filter(workload=>!d.model||workload.desired.spec.modelRef.name===d.model).map(workload=><option key={workload.desired.metadata.uid} value={workload.desired.metadata.name}>{workload.desired.metadata.name}</option>)}</select></label>
        {v.protocol.from&&<table aria-label="가져온 측정 방식" className="w-full text-left text-xs"><tbody><tr><th>측정 방식 출처</th><td>{v.protocol.from}</td></tr><tr><th>복사 범위</th><td>측정 시간·수집 간격·품질 기준</td></tr><tr><th>대상 장치</th><td>{d.deviceClass||"미선택"}</td></tr></tbody></table>}
        <label className="grid gap-2 text-sm">전처리기<select required aria-invalid={!!error&&!d.preprocessor} aria-describedby={error&&v.problem("workload")?"measurement-workload-error":undefined} className="min-h-11 rounded border bg-background p-2" value={d.preprocessor} onChange={event=>update({preprocessor:event.target.value})}><option value="">전처리기 선택</option>{context.preprocessors.map(item=><option key={item.metadata.uid} value={item.metadata.name}>{item.metadata.name}</option>)}</select></label><label className="grid gap-2 text-sm">데이터셋<select required aria-invalid={!!error&&!d.dataset} aria-describedby={`request-input-guidance${error&&v.problem("workload")?" measurement-workload-error":""}`} className="min-h-11 rounded border bg-background p-2" value={d.dataset} onChange={event=>update({dataset:event.target.value})}><option value="">데이터셋 선택</option>{context.datasets.map(item=>{const gap=datasetGap(item.spec,preprocessor?.spec);return <option key={item.metadata.uid} value={item.metadata.name} disabled={!!gap}>{item.metadata.name}{gap?` · ${gap}`:""}</option>})}</select></label>{inputs.warning&&<details id="request-input-guidance" className="text-sm"><summary>입력 계약 확인 필요</summary><p>{inputs.warning}</p>{(!inputs.dataset||noCompatibleDataset)&&<Link className="underline" href="/datasets/new">데이터셋 등록</Link>}</details>}</div>
      {!inputs.warning&&<span id="request-input-guidance" className="sr-only">입력 계약 일치</span>}
      {error&&v.problem("workload")&&<p id="measurement-workload-error" tabIndex={-1} className="text-sm text-destructive">{v.problem("workload")}</p>}
      </section>
      <div hidden={stage!==1} className="space-y-5">
      <section id="measurement-batches" className="grid gap-4 border-t pt-5 md:grid-cols-2" aria-label="목표 배치와 반복">
        <fieldset className="space-y-3"><legend className="font-semibold">배치 (건)</legend><div className="flex flex-wrap gap-2">{(v.declared??chips).map(batch=><label key={batch} className="inline-flex min-h-11 items-center gap-2 rounded border px-4"><input type="checkbox" name="batchSizes" value={batch} disabled={!!v.declared} checked={!!v.declared||d.batchSizes.includes(batch)} onChange={event=>update({batchSizes:event.target.checked?[...d.batchSizes,batch].sort((a,b)=>a-b):d.batchSizes.filter(item=>item!==batch)})}/>{batch}</label>)}</div>{v.declared&&<p className="text-sm text-muted-foreground">모델 선언 범위</p>}</fieldset>
        <label className="grid gap-2 text-sm">배치별 반복 횟수<Input aria-invalid={!!error&&(!Number.isInteger(v.trials)||v.trials<3||v.trials>2147483647)} aria-describedby={error&&v.problem("size")?"measurement-size-error":undefined} type="number" min={3} value={Number.isFinite(v.trials)?v.trials:""} onChange={event=>update({trials:event.target.valueAsNumber})}/></label>
        {error&&v.problem("size")&&<p id="measurement-size-error" tabIndex={-1} className="text-sm text-destructive">{v.problem("size")}</p>}
      </section>
      <details id="measurement-conditions" open={!!v.problem("device")||!!v.problem("run")||undefined} className="space-y-4"><summary className="font-semibold">실행 조건·측정 방식</summary>
      <section id="measurement-device" className="grid gap-5 border-t pt-5 md:grid-cols-2" aria-label="장치와 실행 조건">
        <div className="space-y-4"><Pick label="장치 종류" name="device-class" value={d.deviceClass} set={deviceClass=>{const cls=context.classes.find(item=>item.name===deviceClass);update({deviceClass,node:cls?.nodes.some(node=>node.name===d.node)?d.node:cls?.nodes.find(node=>!nodeReason(node))?.name??"",powerMode:cls?.powerModes.some(mode=>mode.name===d.powerMode)?d.powerMode:cls?.powerModes.length===1?cls.powerModes[0].name:""})}} options={context.classes.map(cls=>({value:cls.name,label:cls.name,disabled:cls.nodes.some(node=>!nodeReason(node))?undefined:"준비된 장비 없음"}))}/>{v.cls&&<details open={!v.cls.nodes.some(node=>node.name===d.node&&!nodeReason(node))||undefined}><summary className="min-h-11 cursor-pointer py-2 text-sm">실행 장비 · {d.node||"미선택"}</summary><Pick label="실행 장비" name="node" value={d.node} set={node=>update({node})} options={v.cls.nodes.map(node=>({value:node.name,label:node.name,disabled:nodeReason(node)}))}/></details>}</div>
        <div className="space-y-4"><Pick label="실행 경로" name="path" value={d.path} set={path=>update({path})} options={(v.model?.spec.executionPaths??[]).filter(path=>!v.cls||!pathReason(v.model!,path,v.cls)).map((path,index)=>({value:path.name,label:executionPathLabel(path.name,index)}))}/>
          {(!v.model||!v.cls)?<p className="text-sm text-muted-foreground">모델·장치 미선택</p>:!v.model.spec.executionPaths.some(path=>!pathReason(v.model!,path,v.cls!))&&<p role="status" className="notice">지원 경로 없음</p>}
          {v.model&&v.cls&&v.model.spec.executionPaths.some(path=>pathReason(v.model!,path,v.cls!))&&<details><summary className="min-h-11 cursor-pointer py-2">지원 안 되는 경로와 이유</summary>{v.model.spec.executionPaths.filter(path=>pathReason(v.model!,path,v.cls!)).map((path,index)=><p key={path.name}>{executionPathLabel(path.name,index)}: {pathReason(v.model!,path,v.cls!)}</p>)}</details>}
          {error&&(v.problem("device")||v.problem("run"))&&<p id="measurement-run-error" tabIndex={-1} className="text-sm text-destructive">{v.problem("device")||v.problem("run")}</p>}
          {v.cls&&<Pick label="전력 모드" name="mode" value={v.powerMode} set={powerMode=>update({powerMode})} options={v.cls.powerModes.map(mode=>({value:mode.name,label:mode.name}))}/>}
          {verdict&&<div className="space-y-2 text-sm"><p>{support.isFetching?"호환성 조회 중":verdict.reason}</p>{verdict.state==="unknown"&&<label className="flex min-h-11 items-center gap-2"><input type="checkbox" checked={showUnknown} onChange={event=>{setShowUnknown(event.target.checked);setCheckedInput(null);dry.reset();pre.reset()}}/>지원 미확인 조합 검사 동의</label>}</div>}
          {!!support.data?.some(read=>read.data?.diagnostics?.length)&&<ul aria-label="실행 선언 진단" className="list-inside list-disc text-sm">{support.data.flatMap(read=>(read.data?.diagnostics??[]).map((diagnostic,index)=><li key={`${read.node}/${index}`}>{read.node}: {diagnostic}</li>))}</ul>}
        </div>
      </section>
      <section className="space-y-3 border-t pt-5" aria-label="실제 측정 기본값"><h2 className="font-semibold">측정 방식</h2><table className="w-full text-right text-sm tabular-nums"><thead><tr><th>예열 (회)</th><th>실행 측정 (s)</th><th>유휴 측정 (s)</th><th>배치별 반복 (회)</th></tr></thead><tbody><tr><td>{requestNumber(v.protocol.values.warmup)}</td><td>{requestNumber(v.protocol.values.activeDuration)}</td><td>{requestNumber(v.protocol.values.idleDuration)}</td><td>{requestNumber(v.trials)}</td></tr></tbody></table><details id="measurement-advanced"><summary className="min-h-11 cursor-pointer py-2">고급 측정 설정</summary>{!d.protocol&&<p role="status" className="text-sm">측정 방식 미제공</p>}<AdvancedFields showErrors={!!error} rejection={rejection} value={v.protocol.values} set={values=>update({protocol:{values,from:null,spec:v.protocol.spec}})}/>{error&&v.problem("advanced")&&<p id="measurement-advanced-error" tabIndex={-1} className="text-sm text-destructive">{v.problem("advanced")}</p>}</details></section>
      <label className="grid gap-2 border-t pt-5 text-sm">요청 이름 (검사한 이름 고정)<Input aria-invalid={!!error&&!!v.problem("name")} aria-describedby={error&&v.problem("name")?"measurement-name-error":undefined} value={v.name} maxLength={200} onChange={event=>update({name:event.target.value})}/>{error&&v.problem("name")&&<span id="measurement-name-error" tabIndex={-1} className="text-destructive">{v.problem("name")}</span>}</label>
      </details>
      </div>
    </fieldset>
    <div id="measurement-errors" tabIndex={-1} role={error||blocked?"alert":undefined} className={error||blocked||dry.error||pre.error||send.error?"space-y-2":"hidden"}>
      {prefill.errors.map(issue=><p key={issue} className="text-destructive">{issue}</p>)}{error&&(invalidStep?<a className="text-destructive underline" href={`#measurement-${invalidStep==="device"?"run":invalidStep}-error`}>입력 조건 확인</a>:<p className="text-destructive">{error}</p>)}
      {copyError&&!prefill.errors.includes(copyError)&&<p className="text-destructive">{copyError}</p>}
      {send.error&&<p className="text-destructive">{isCoverageWarming(send.error)?"초기화 중":send.error.status?`제출 · HTTP ${send.error.status} / ${failure(send.error).code??"응답 코드 없음"}: ${failure(send.error).message}`:"제출 · 응답 미수신"}</p>}
    </div>
    {(dry.data||dry.error||pre.data||pre.error)&&<table aria-label="검사 응답" className="w-full text-left text-sm"><thead><tr><th>검사</th><th>서버 응답</th><th>결과</th></tr></thead><tbody>{[["접수 검사",dry],["실행 계획 검사",pre]].map(([label,query])=>{const check=query as typeof dry;return <tr key={String(label)}><th className="font-normal">{String(label)}</th><td>{check.data?`HTTP ${check.data.status}`:check.error?.status?`HTTP ${check.error.status}`:check.error?"응답 미수신":"검사 중"}</td><td>{isCoverageWarming(check.error)?"초기화 중":check.error?"검사 미완료":check.data?label==="접수 검사"?(admitted?"형식 검사 통과":"응답 확인"):answer?.resolves?"실행 계획 확인":"실행 계획 미확정":"대기"}</td></tr>})}</tbody></table>}
    {rejectedReason&&<section id="measurement-rejection" tabIndex={-1} role="alert" className="notice space-y-2 text-sm"><h2 className="font-semibold">사전검사 거부</h2>{rejection?<><a className="underline" href={`#${rejection.target}`}>{rejection.message}</a><table aria-label="거부 조건" className="w-full text-left tabular-nums"><thead><tr><th>항목</th><th>제약</th></tr></thead><tbody>{rejection.constraints.map(([label,value])=><tr key={label}><th className="font-normal">{label}</th><td>{value}</td></tr>)}</tbody></table></>:<span>수정 필드 미제공</span>}<details><summary className="min-h-11 cursor-pointer py-2">서버 거부 사유 원문</summary><pre className="whitespace-pre-wrap break-words">{rejectedReason}</pre></details></section>}
    {verdict?.state==="unknown"&&!showUnknown&&<p className="text-sm text-muted-foreground">지원 미확인 · 명시 검사 동의 필요</p>}
    {!error&&remaining.length>0&&<details className="rounded border p-3 text-sm"><summary>검사 전 미입력 조건</summary><ul className="list-inside list-disc">{remaining.map(({step,issue})=><li key={step}>{issue}</li>)}</ul></details>}
    {selected&&<details className="space-y-3 rounded border p-4 text-sm"><summary>이전 결과·재사용 조건</summary><table className="w-full text-left tabular-nums"><thead><tr><th>이전 목표 배치 (건)</th><th>기측정 배치 (건)</th><th>이번 선택 배치 (건)</th></tr></thead><tbody><tr><td>{selected.batchProgress.target.join(", ")||"미기록"}</td><td>{selected.batchProgress.measured.join(", ")||"없음"}</td><td>{d.batchSizes.join(", ")||"미지정"}</td></tr></tbody></table><p>{!answer?"재사용 미검사":answer.batchPlan?.conditionMatch==="exact"?"동결 실행 조건 일치":"기존 결과 유지 · 재사용 미확인"}</p>{changes.length>0&&<table className="w-full text-left"><thead><tr><th>변경 필드</th><th>이전</th><th>현재</th></tr></thead><tbody>{changes.map(change=><tr key={change.field}><th className="font-normal">{change.label}</th><td>{change.before}</td><td>{change.after}</td></tr>)}</tbody></table>}</details>}
    {explanation&&<section className="space-y-4 border-t pt-5"><h2 className="font-semibold">{preflightConclusion(explanation)}</h2><PreflightAnswer answer={explanation}/></section>}
    {pre.error&&<table className="w-full text-left text-sm"><thead><tr><th>서빙 영향 · 서비스 (개)</th><th>예상 점유 · 최소 (s)</th></tr></thead><tbody><tr><td>미확인</td><td>미확인</td></tr></tbody></table>}
    {stage===2&&<section aria-label="검사·제출" className="space-y-3"><table className="w-full text-left text-sm"><caption className="text-left font-semibold">제출 전 검토</caption><tbody>{[["요청",v.name],["모델",d.model],["장치 종류",d.deviceClass],["Node",d.node],["전력모드",v.powerMode],["실행 경로",d.path?executionPathLabel(d.path):"미제공"],["전처리기",d.preprocessor],["데이터셋",d.dataset],["측정 방식 출처",v.protocol.from??"직접 입력"],["원본 요청",copiedIdentity?.name??source?.metadata.name??from??"복사 안 함"],["원본 저장 영역",copiedIdentity?.namespace??source?.metadata.namespace??(from?"미확인":"해당 없음")],["원본 UID",copiedIdentity?.uid??source?.metadata.uid??(from?"미확인":"해당 없음")]].map(([label,value])=><tr key={label}><th className="py-2 font-medium">{label}</th><td className="break-all">{value||"미제공"}</td></tr>)}</tbody></table><p role="status" className="text-sm font-semibold">미제출</p></section>}
    <div className="flex flex-wrap gap-2">{stage>0&&<Button variant="outline" disabled={unknown||pending||send.isPending} onClick={()=>setStage(stage-1)}>이전</Button>}{stage<2?<Button disabled={blocked||unknown||pending||send.isPending} onClick={next}>다음</Button>:<><Button variant="outline" disabled={blocked||pending||unknown||send.isPending} onClick={verify}>{pending?"검사 중":"요청 범위 검사"}</Button><Button disabled={!passed||blocked||send.isPending||unknown} onClick={submit}>측정 요청</Button></>}</div>
    <details><summary className="min-h-11 cursor-pointer py-2">보낼 요청 원본</summary><SourceJSON inline value={manifest(d,context,namespace)}/></details>{(answer||pre.error||dry.error)&&<details><summary className="min-h-11 cursor-pointer py-2">검사 원본</summary><SourceJSON inline value={{접수검사:dry.data??dry.error,실행계획검사:answer??pre.error}}/></details>}
  </Screen>
}
