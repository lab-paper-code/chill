"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useState, type ReactNode } from "react"
import { useChillMutation, useChillQuery, useSession, type ApiError } from "@/lib/client"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { RowDialog, Rows, Screen } from "@/components/screen"
import { PreflightAnswer, preflightConclusion } from "@/features/read-pages"
import { powerModeText } from "@/features/resources"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { DatasetResource, PreprocessorResource, ExecutionPath, ModelList, ModelResource, WorkloadView } from "@/lib/domain"
import type { InventoryClass, InventoryNode, MeasurementMutation, MeasurementPlan, MeasurementPlanList, MeasurementPlanSpec, MeasurementPreflight, MeasurementVariant, ProfilesView } from "@/lib/read-types"
import { datasetGap, requestNumber, requestBatches, requestDefaults, requestInputs, requestTargetBatches, requestConditionChanges, requestPreflightReason } from "@/lib/measurement-request"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { resolveMeasurementPrefill, serverCompatibility, staticCompatibility, type MeasurementPrefill } from "@/lib/measurement-coverage"
import { useCoverageInventory, useModelCompatibility, useMeasurementCoverage } from "@/features/measurement-coverage"
import { MeasurementNavigation } from "@/components/measurement-navigation"
import { measurementRequestName } from "@/lib/measurements"

const back: [string, string] = ["/measurements", "측정"]
const steps = ["workload", "device", "run", "size", "advanced", "name", "review"] as const
type Step = typeof steps[number]
const batchChoices = [1, 2, 4, 8, 16]
const dns1123 = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

type Advanced = { warmup: number; activeDuration: number; activeWindow: number; activeInterval: number; activeTimeout: number; settling: number; idleDuration: number; idleWindow: number; idleInterval: number; idleTimeout: number; retryLimit: number; affinity: string }
type NumberKey = Exclude<keyof Advanced, "affinity">
const advancedFields: [key: NumberKey, label: string, min: number][] = [
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
type Protocol = { values: Advanced; from: string | null }
// model, preprocessor and dataset come from the chosen Workload's spec refs (or a copied plan); the API takes catalog names.
type Draft = { workload: string; model: string; preprocessor: string; dataset: string; explicitPreprocessor?:boolean; explicitDataset?:boolean; deviceClass: string; node: string; path: string; powerMode: string; batchSizes: number[]; trials: number | null; cpu: string | null; protocol: Protocol | null; name: string | null }
type Context = { models: ModelResource[]; workloads: WorkloadView[]; classes: InventoryClass[]; plans: MeasurementPlan[]; preprocessors: PreprocessorResource[]; datasets: DatasetResource[]; profiles:ProfilesView["profiles"]; prefill?:boolean }

const emptyDraft: Draft = { workload: "", model: "", preprocessor: "", dataset: "", deviceClass: "", node: "", path: "", powerMode: "", batchSizes: [], trials: null, cpu: null, protocol: null, name: null }
const blankAdvanced: Advanced = { warmup: NaN, activeDuration: NaN, activeWindow: NaN, activeInterval: NaN, activeTimeout: NaN, settling: NaN, idleDuration: NaN, idleWindow: NaN, idleInterval: NaN, idleTimeout: NaN, retryLimit: NaN, affinity: "" }
function copyAdvanced(spec: MeasurementPlanSpec): Advanced {
  const active = spec.activeProtocol, idle = spec.idleProtocol
  return {
    warmup: active.warmupIterations ?? NaN, activeDuration: active.durationSeconds ?? NaN, activeWindow: active.metricObservation?.durationSeconds ?? NaN,
    activeInterval: active.metricObservation?.intervalMilliseconds ?? NaN, activeTimeout: active.metricObservation?.timeoutMilliseconds ?? NaN,
    settling: idle.settlingDurationSeconds ?? NaN, idleDuration: idle.durationSeconds ?? NaN, idleWindow: idle.metricObservation?.durationSeconds ?? NaN,
    idleInterval: idle.metricObservation?.intervalMilliseconds ?? NaN, idleTimeout: idle.metricObservation?.timeoutMilliseconds ?? NaN,
    retryLimit: spec.retryLimit, affinity: "",
  }
}
function fillDefaults(draft:Draft,context:Context,inherited=false):Draft {
  const artifact=context.models.find(model=>model.metadata.name===draft.model)?.spec.executionPaths.find(path=>path.name===draft.path)?.artifact??""
  const inputs=requestInputs(draft.model,artifact,draft.preprocessor,draft.dataset,context,{preprocessor:inherited||!draft.explicitPreprocessor,dataset:inherited||!draft.explicitDataset})
  const filled={...draft,preprocessor:inputs.preprocessor,dataset:inputs.dataset}
  const defaults=requestDefaults(filled,context.plans,context.profiles)
  if(!defaults)return filled
  const spec=defaults.spec
  return {...filled,node:filled.node||spec.state.targetNode,trials:filled.trials??spec.trialsPerBatch,cpu:filled.cpu??spec.cpuQuota??spec.state.cpuRequest??null,protocol:filled.protocol??{values:copyAdvanced(spec),from:defaults.source}}
}
function parseAffinity(text: string): number[] | null {
  const parts = text.split(/[\s,]+/).filter(Boolean)
  if (parts.some(part => !/^\d+$/.test(part) || Number(part) > 2147483647)) return null
  return [...new Set(parts.map(Number))].sort((a, b) => a - b)
}
function advancedProblem(values: Advanced) {
  const bad = advancedFields.find(([key, , min]) => !Number.isInteger(values[key]) || values[key] < min || values[key] > 2147483647)
  if (bad) return `"${bad[1]}"에 ${bad[2]} 이상의 정수를 적어 주세요.`
  if (values.retryLimit > 10) return "다시 시도할 횟수는 10번까지 정할 수 있어요."
  if (values.activeTimeout >= values.activeInterval || values.idleTimeout >= values.idleInterval) return "전력 기록 대기 한도는 기록 간격보다 짧아야 해요."
  if (values.activeWindow < values.activeDuration || values.idleWindow < values.idleDuration) return "전력 기록 시간은 측정 시간보다 짧을 수 없어요."
  if (!parseAffinity(values.affinity)) return "CPU 코어 번호는 0 이상의 정수를 쉼표로 나눠 적어 주세요."
  return ""
}
const nodeReason = (node: InventoryNode) => node.ready !== "True" ? "지금 준비되지 않았어요" : node.schedulingDisabled ? "새 작업을 받지 않도록 막혀 있어요" : ""
// Only a proven static mismatch blocks a path; runtime support is checked by preflight.
function pathReason(model: ModelResource, path: ExecutionPath, cls: InventoryClass) {
  const verdict = staticCompatibility(model, path, cls)
  return verdict.state === "unsupported" ? verdict.reason : ""
}
const detailHref = (namespace: string, name: string, uid?: string) => `/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid ? `?uid=${encodeURIComponent(uid)}` : ""}`
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
  const cpu = d.cpu ?? "1"
  const powerMode = cls?.powerModes.length === 1 ? cls.powerModes[0].name : d.powerMode
  const declared = artifact?.derivation?.declaredBatchSizes?.length ? artifact.derivation.declaredBatchSizes : null
  const max = path?.runtime.settings?.firstfloor?.maxBatchSize
  const taken = new Set(plans.map(p => p.metadata.name))
  const name = d.name ?? (model && node && path ? measurementRequestName({...d,powerMode,cpu}, taken) : "")
  const affinity = parseAffinity(protocol.values.affinity) ?? []
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
        return /^[1-9]\d*$/.test(cpu) ? "" : "CPU 개수를 골라 주세요."
      case "advanced": return advancedProblem(protocol.values)
      case "name":
        if (!name) return "이름을 적어 주세요."
        if (name.length > 200 || !dns1123.test(name)) return "이름은 200자 이하의 영문 소문자, 숫자, '-', '.'로 적고, 처음과 끝은 영문 소문자나 숫자여야 해요."
        return taken.has(name) ? "같은 이름의 측정이 이미 있어요. 다른 이름을 적어 주세요." : ""
      case "review": return ""
    }
  }
  return { model, cls, path, protocol, trials, cpu, powerMode, declared, max, taken, name, affinity, problem }
}
function manifest(d: Draft, context: Context, namespace: string) {
  const v = view(d, context)
  const p = v.protocol.values
  const spec: MeasurementPlanSpec = {
    state: { candidate: { modelSpec: d.model, preprocessor: d.preprocessor, deviceClass: d.deviceClass, executionPath: d.path, powerMode: v.powerMode }, targetNode: d.node, cpuRequest: v.cpu },
    activeProtocol: { warmupIterations: p.warmup, durationSeconds: p.activeDuration, dataset: d.dataset, metricObservation: { intervalMilliseconds: p.activeInterval, timeoutMilliseconds: p.activeTimeout, durationSeconds: p.activeWindow } },
    idleProtocol: { settlingDurationSeconds: p.settling, durationSeconds: p.idleDuration, metricObservation: { intervalMilliseconds: p.idleInterval, timeoutMilliseconds: p.idleTimeout, durationSeconds: p.idleWindow } },
    cpuQuota: v.cpu,
    ...(v.affinity.length ? { cpuAffinity: v.affinity } : {}),
    trialsPerBatch: v.trials,
    // The CRD forbids batchSizes when the artifact declares its own set.
    ...(v.declared ? {} : { batchSizes: [...d.batchSizes].sort((a, b) => a - b) }),
    retryLimit: p.retryLimit,
  }
  return { apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization", metadata: { name: v.name, namespace }, spec }
}

// `?from=` copies a whole existing plan; the funnel then opens at the first step that needs attention, else the name.
function begin(from: string | null, context: Context) {
  const source = from ? context.plans.find(p => p.metadata.name === from) : undefined
  if (!source) return { draft: emptyDraft, index: 0, error: from ? "복사할 측정을 찾지 못해서 처음부터 골라요." : "", copied: false }
  const s = source.spec
  const draft: Draft = { workload: "", model: s.state.candidate.modelSpec, preprocessor: s.state.candidate.preprocessor, dataset: s.activeProtocol.dataset ?? "", deviceClass: s.state.candidate.deviceClass, node: s.state.targetNode, path: s.state.candidate.executionPath, powerMode: s.state.candidate.powerMode, batchSizes: s.batchSizes ?? [], trials: s.trialsPerBatch, cpu: s.cpuQuota ?? s.state.cpuRequest ?? null, protocol: { values: copyAdvanced(s), from: source.metadata.name }, name: null }
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
function AdvancedFields({ value, set, showErrors }: { value: Advanced; set: (value: Advanced) => void; showErrors:boolean }) {
  return <div className="grid gap-4 sm:grid-cols-2">
    {advancedFields.map(([key, label, min]) => {
      const issue=!Number.isInteger(value[key])||value[key]<min||value[key]>(key==="retryLimit"?10:2147483647)?`${min} 이상의 정수를 입력하세요.`:key==="activeTimeout"&&value.activeTimeout>=value.activeInterval||key==="idleTimeout"&&value.idleTimeout>=value.idleInterval?"기록 간격보다 짧게 입력하세요.":key==="activeWindow"&&value.activeWindow<value.activeDuration||key==="idleWindow"&&value.idleWindow<value.idleDuration?"측정 시간 이상으로 입력하세요.":""
      return <label htmlFor={`measurement-${key}`} key={key} className="grid gap-2 text-sm">{label}<Input id={`measurement-${key}`} aria-invalid={showErrors&&!!issue} aria-describedby={showErrors&&issue?`measurement-${key}-error`:undefined} type="number" inputMode="numeric" min={min} max={key === "retryLimit" ? 10 : 2147483647} step={1} value={Number.isNaN(value[key]) ? "" : value[key]} onChange={event => set({ ...value, [key]: event.target.valueAsNumber })} />{showErrors&&issue&&<span id={`measurement-${key}-error`} className="text-destructive">{issue}</span>}</label>
    })}
    <label htmlFor="measurement-affinity" className="grid gap-2 text-sm sm:col-span-2">사용할 CPU 코어 번호 · 비워 두면 정하지 않아요<Input id="measurement-affinity" aria-invalid={showErrors&&!parseAffinity(value.affinity)} inputMode="numeric" placeholder="예: 3 또는 2, 3" value={value.affinity} onChange={event => set({ ...value, affinity: event.target.value })} />{showErrors&&!parseAffinity(value.affinity)&&<span className="text-destructive">0 이상의 코어 번호를 쉼표로 나눠 입력하세요.</span>}</label>
  </div>
}

export function MeasurementRequestPage() {
  const { config } = useSession(), params = useSearchParams()
  const from = params.get("from")
  const plans = useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(config.namespace)}/batch-energy-optimizations`)
  const models = useChillQuery<ModelList>("v1/models")
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads")
  const preprocessors = useChillQuery<{items:PreprocessorResource[]}>("v1/preprocessors")
  const datasets = useChillQuery<{items:DatasetResource[]}>("v1/datasets")
  const inventory = useCoverageInventory()
  const coverage=useMeasurementCoverage(config.namespace,!!params.get("variantId"))
  const profiles=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams({limit:"50",...(params.get("model")?{model:params.get("model")!}:{}),...(params.get("deviceClass")?{deviceClass:params.get("deviceClass")!}:{}),...(params.get("node")?{node:params.get("node")!}:{}),...(params.get("path")?{path:params.get("path")!}:{}),...(params.get("powerMode")?{powerMode:params.get("powerMode")!}:{})})}`,{decoder:parseSchedulerJSON})
  if(params.get("variantId")&&!coverage.data&&!coverage.error)return <Screen title="측정 요청" lede="선택 조건과 부족 배치를 확인하고 있어요."/>
  if (!plans.data || !models.data || !workloads.data || !inventory.data || !preprocessors.data || !datasets.data || profiles.isPending) return <Screen back={back} title="측정 요청" lede="고를 수 있는 서비스와 장비를 확인하고 있어요.">
    <MeasurementNavigation namespace={config.namespace}/>
    <QueryGate query={plans}><QueryGate query={models}><QueryGate query={workloads}><QueryGate query={inventory}><QueryGate query={preprocessors}><QueryGate query={datasets}>{null}</QueryGate></QueryGate></QueryGate></QueryGate></QueryGate></QueryGate>
  </Screen>
  const prefill = resolveMeasurementPrefill(new URLSearchParams(params), config.namespace, inventory.data)
  const batches=requestBatches(new URLSearchParams(params));if(batches.error)prefill.errors.push(batches.error)
  const source = from ? plans.data.items?.find(plan=>plan.metadata.name===from) : undefined
  if (from && (!source || ((prefill.values.fromUID || prefill.values.uid) && source.metadata.uid!==(prefill.values.fromUID || prefill.values.uid)))) prefill.errors.push("복사 원본의 namespace/UID를 확인할 수 없어요")
  const context: Context = { models: models.data.items, workloads: workloads.data, classes: inventory.data.classes, plans: plans.data.items ?? [], preprocessors:preprocessors.data?.items??[], datasets:datasets.data?.items??[], profiles:profiles.data?.profiles??[], prefill:prefill.active }
  const selected=coverage.data?.cells.flatMap(cell=>cell.variants).find(variant=>variant.id===params.get("variantId"))
  return <Funnel key={params.toString()} namespace={config.namespace} from={from} context={context} prefill={prefill} selected={selected} refetchPlans={plans.refetch} checking={plans.isFetching} />
}

function Funnel({ namespace, from, context, prefill, selected, refetchPlans, checking }: { namespace: string; from: string | null; context: Context; prefill:MeasurementPrefill; selected?:MeasurementVariant; refetchPlans: () => Promise<{ error: ApiError | null }>; checking: boolean }) {
  const router = useRouter(), params=useSearchParams()
  const dry = useChillMutation(), pre = useChillMutation(), send = useChillMutation()
  const variantId=params.get("variantId")??""
  const source=selected&&context.plans.find(plan=>selected.requestIds.includes(`${namespace}/${plan.metadata.name}/${plan.metadata.uid}`))
  const [d, setD] = useState<Draft>(() => {
    if(source) {
      const copied=begin(source.metadata.name,context).draft
      const max=context.models.find(model=>model.metadata.name===copied.model)?.spec.executionPaths.find(path=>path.name===copied.path)?.runtime.settings?.firstfloor?.maxBatchSize
      return fillDefaults({...copied,...(prefill.active?{model:prefill.values.model,deviceClass:prefill.values.deviceClass,node:prefill.node,path:prefill.values.path,powerMode:prefill.values.powerMode}:{}),batchSizes:requestTargetBatches(new URLSearchParams(params),selected?.batchProgress.target??copied.batchSizes,max),name:null},context,true)
    }
    const max=context.models.find(model=>model.metadata.name===prefill.values.model)?.spec.executionPaths.find(path=>path.name===prefill.values.path)?.runtime.settings?.firstfloor?.maxBatchSize
    const draft=prefill.active ? {...emptyDraft,model:prefill.values.model,deviceClass:prefill.values.deviceClass,node:prefill.node,path:prefill.values.path,powerMode:prefill.values.powerMode,batchSizes:requestTargetBatches(new URLSearchParams(params),selected?.batchProgress.target,max)} : {...begin(from,context).draft,...(!from&&params.has("batches")?{batchSizes:requestTargetBatches(new URLSearchParams(params))}:{})}
    return fillDefaults(draft,context,true)
  })
  const [error,setError]=useState(""),[unknown,setUnknown]=useState(false),[checked,setChecked]=useState(false)
  const [showUnknown,setShowUnknown]=useState(false)
  const v=view(d,context), listPath=`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`
  const support=useModelCompatibility(d.model,d.path,d.deviceClass,v.cls?.nodes.filter(node=>node.name===d.node)??[],v.model?.metadata.uid,v.cls?.uid)
  const verdict=v.model&&v.path&&v.cls?serverCompatibility(staticCompatibility(v.model,v.path,v.cls),support.data??[]):undefined
  const answer=pre.data?.data as MeasurementPreflight|undefined
  const explanation=answer?{...answer,...(answer.batchPlan?{batchPlan:{...answer.batchPlan,reasons:answer.batchPlan.reasons.map(reason=>({...reason,message:requestPreflightReason(reason)}))}}:{})}:undefined
  const changes=selected&&answer?requestConditionChanges(selected.condition,answer.plan?.shared as unknown as Record<string,unknown>|undefined):[]
  const admitted=dry.isSuccess&&(dry.data?.data as MeasurementMutation|null)?.request==="NOT_SUBMITTED"
  const passed=admitted&&answer?.resolves===true
  const pending=dry.isPending||pre.isPending
  const update=(patch:Partial<Draft>)=>{
    setD(current=>fillDefaults({...current,...patch,...(patch.preprocessor!==undefined?{explicitPreprocessor:!!patch.preprocessor}:{}),...(patch.dataset!==undefined?{explicitDataset:!!patch.dataset}:{})},context))
    setError("");dry.reset();pre.reset();send.reset()
  }
  function verify() {
    const issue=steps.filter(step=>step!=="review").map(step=>v.problem(step)).find(Boolean)
    if(issue){setError(issue);requestAnimationFrame(()=>document.getElementById("measurement-errors")?.focus());return}
    if(verdict?.state==="unsupported"){setError(verdict.reason);return}
    if(verdict?.state==="unknown"&&!showUnknown){setError("지원 미확인 조합을 검사하려면 명시 검사 항목을 선택하세요.");return}
    // Keep the checked identity through success, transport uncertainty and manual identity lookup.
    const pinned={...d,name:v.name};setD(pinned);setError("")
    const body=manifest(pinned,context,namespace)
    dry.mutate({path:`${listPath}?dryRun=All`,method:"POST",body})
    pre.mutate({path:`${listPath}/preflight${variantId?`?${new URLSearchParams({variantId})}`:""}`,method:"POST",body})
  }
  function submit() {
    if(!passed||unknown)return
    send.mutate({path:listPath,method:"POST",body:manifest(d,context,namespace)},{
      onSuccess:response=>{const task=(response.data as MeasurementMutation|null)?.task;router.replace(detailHref(task?.metadata.namespace??namespace,task?.metadata.name??v.name,task?.metadata.uid))},
      onError:e=>{if(e.unknownOutcome||e.status===0||failure(e).code==="MeasurementAuditIncomplete")setUnknown(true)},
    })
  }
  const chips=[...new Set([...batchChoices.filter(batch=>!v.max||batch<=v.max),...d.batchSizes])].sort((a,b)=>a-b)
  const blocked=prefill.errors.length>0
  const found=checked?context.plans.find(plan=>plan.metadata.name===v.name):undefined
  const remaining=steps.filter(step=>step!=="review").map(step=>({step,issue:v.problem(step)})).filter(item=>item.issue)
  const inputs=requestInputs(d.model,v.path?.artifact??"",d.preprocessor,d.dataset,context)
  const preprocessor=context.preprocessors.find(item=>item.metadata.name===d.preprocessor)
  const noCompatibleDataset=!!preprocessor&&!context.datasets.some(item=>!datasetGap(item.spec,preprocessor.spec))
  return <Screen width="data" title="측정 요청" lede="모델·장치 종류·처리 건수를 고르고, 검사 결과를 확인한 뒤 요청하세요.">
    <MeasurementNavigation active="requests" namespace={namespace}/>
    {unknown&&<section role="alert" className="notice space-y-3"><h2>접수 결과 미확인 — 같은 이름으로 확인하세요</h2><p>{v.name}: 자동 재제출과 이름 변경을 막았습니다.</p><Button disabled={checking} onClick={()=>void refetchPlans().then(result=>{setChecked(!result.error);if(result.error)setError("목록 조회 실패: 다시 확인하세요.")})}>같은 이름의 접수 기록 조회</Button>{found?<Link className="underline" href={detailHref(namespace,found.metadata.name,found.metadata.uid)}>접수된 요청 열기</Link>:checked&&<p>아직 목록에서 확인되지 않았어요. 자동 재제출하지 않습니다.</p>}</section>}
    {variantId&&!selected&&<p role="status" className="notice">전달된 조건을 확인할 수 없어 부족 배치를 자동 제외하지 않았습니다. 직접 조건을 지정하고 중복 범위를 검사하세요.</p>}
    <fieldset disabled={unknown||pending||send.isPending} className="space-y-8 disabled:opacity-60">
      <section className="grid gap-5 border-t pt-5 md:grid-cols-2" aria-label="측정 대상">
        <label className="grid content-start gap-2 text-sm">모델<select aria-invalid={!!error&&!v.model} className="min-h-11 w-full rounded border bg-background p-2" value={d.model} onChange={event=>{const model=context.models.find(item=>item.metadata.name===event.target.value);update({model:event.target.value,workload:"",preprocessor:"",dataset:"",...(!model?.spec.executionPaths.some(path=>path.name===d.path)?{path:""}:{})})}}><option value="">모델 선택</option>{context.models.map(model=><option key={model.metadata.uid} value={model.metadata.name}>{model.metadata.name}</option>)}</select>{error&&!v.model&&<span className="text-destructive">모델을 선택하세요.</span>}</label>
        <div className="space-y-3"><label className="grid gap-2 text-sm">등록 서비스에서 조건 가져오기 (선택)<select className="min-h-11 rounded-md border bg-background px-3" value={d.workload} onChange={event=>{
          const workload=context.workloads.find(item=>item.desired.metadata.name===event.target.value)
          if(!workload){update({workload:""});return}
          const spec=workload.desired.spec,changed=spec.modelRef.name!==d.model
          update({workload:workload.desired.metadata.name,model:spec.modelRef.name,preprocessor:spec.preprocessorRef.name,dataset:spec.datasetRef?.name??"",...(changed&&!context.models.find(model=>model.metadata.name===spec.modelRef.name)?.spec.executionPaths.some(path=>path.name===d.path)?{path:""}:{})})
        }}><option value="">직접 조건 지정</option>{context.workloads.filter(workload=>!d.model||workload.desired.spec.modelRef.name===d.model).map(workload=><option key={workload.desired.metadata.uid} value={workload.desired.metadata.name}>{workload.desired.metadata.name}</option>)}</select></label>
        <label className="grid gap-2 text-sm">전처리기<select className="min-h-11 rounded border bg-background p-2" value={d.preprocessor} onChange={event=>update({preprocessor:event.target.value})}><option value="">전처리기 선택</option>{context.preprocessors.map(item=><option key={item.metadata.uid} value={item.metadata.name}>{item.metadata.name}</option>)}</select>{error&&!d.preprocessor&&<span className="text-destructive">등록된 전처리기를 선택하세요.</span>}</label><label className="grid gap-2 text-sm">데이터셋<select aria-describedby="request-input-guidance" className="min-h-11 rounded border bg-background p-2" value={d.dataset} onChange={event=>update({dataset:event.target.value})}><option value="">데이터셋 선택</option>{context.datasets.map(item=>{const gap=datasetGap(item.spec,preprocessor?.spec);return <option key={item.metadata.uid} value={item.metadata.name} disabled={!!gap}>{item.metadata.name}{gap?` · ${gap}`:""}</option>})}</select>{error&&!d.dataset&&<span className="text-destructive">등록된 데이터셋을 선택하세요.</span>}</label><p id="request-input-guidance" role={inputs.warning?"status":undefined} className={inputs.warning?"rounded border p-3 text-sm":"text-sm text-muted-foreground"}>{inputs.warning||"전처리기 입력 필드의 이름·형식·최대 크기 조건에 맞는 데이터셋을 자동 선택해요. 추가 필드는 허용하고, 직접 선택한 값은 자동 변경하지 않아요."}{inputs.warning&&(!inputs.dataset||noCompatibleDataset)&&<> <Link className="underline" href="/datasets/new">데이터셋 등록</Link></>}</p></div>
      </section>
      <section className="grid gap-5 border-t pt-5 md:grid-cols-2" aria-label="장치와 실행 조건">
        <div className="space-y-4"><Pick label="장치 종류" name="device-class" value={d.deviceClass} set={deviceClass=>{const cls=context.classes.find(item=>item.name===deviceClass);update({deviceClass,node:cls?.nodes.some(node=>node.name===d.node)?d.node:cls?.nodes.find(node=>!nodeReason(node))?.name??"",powerMode:cls?.powerModes.some(mode=>mode.name===d.powerMode)?d.powerMode:cls?.powerModes.length===1?cls.powerModes[0].name:""})}} options={context.classes.map(cls=>({value:cls.name,label:cls.name,hint:`사용 가능 장비 ${cls.nodes.filter(node=>!nodeReason(node)).length}대`,disabled:cls.nodes.some(node=>!nodeReason(node))?undefined:"지금 사용할 수 있는 장비가 없어요"}))}/>{v.cls&&<details><summary className="min-h-11 cursor-pointer py-2 text-sm">실행 장비 선택 · {d.node||"미선택"}</summary><Pick label="실행 장비" name="node" value={d.node} set={node=>update({node})} options={v.cls.nodes.map(node=>({value:node.name,label:node.name,disabled:nodeReason(node)}))}/></details>}</div>
        <div className="space-y-4"><Pick label="실행 경로" name="path" value={d.path} set={path=>update({path})} options={(v.model?.spec.executionPaths??[]).filter(path=>!v.cls||!pathReason(v.model!,path,v.cls)).map(path=>({value:path.name,label:path.name,hint:`${path.runtime.family} / ${path.runtime.backend}`}))}/>
          {(!v.model||!v.cls)?<p className="text-sm text-muted-foreground">모델과 장치 종류를 먼저 선택하면 실행 방식을 고를 수 있어요.</p>:!v.model.spec.executionPaths.some(path=>!pathReason(v.model!,path,v.cls!))&&<p role="status" className="notice">이 장치에서 사용할 실행 방식이 없어요. 다른 장치 종류를 선택하거나 모델의 실행 방식 등록을 확인하세요.</p>}
          {v.model&&v.cls&&v.model.spec.executionPaths.some(path=>pathReason(v.model!,path,v.cls!))&&<details><summary className="min-h-11 cursor-pointer py-2">지원 안 되는 경로와 이유</summary>{v.model.spec.executionPaths.filter(path=>pathReason(v.model!,path,v.cls!)).map(path=><p key={path.name}>{path.name}: {pathReason(v.model!,path,v.cls!)}</p>)}</details>}
          {error&&(v.problem("device")||v.problem("run"))&&<p role="alert" className="text-sm text-destructive">{v.problem("device")||v.problem("run")}</p>}
          {v.cls&&<Pick label="전력 모드" name="mode" value={v.powerMode} set={powerMode=>update({powerMode})} options={v.cls.powerModes.map(mode=>({value:mode.name,label:mode.name,hint:powerModeText(mode.actuator)}))}/>}
          {verdict&&<div className="space-y-2 text-sm"><p>{support.isFetching?"호환성 확인 중":verdict.reason}</p>{verdict.state==="unknown"&&<><p>지원 미확인: 장치 선언·runtime 지원을 담당자가 확인해야 합니다.</p><label className="flex min-h-11 items-center gap-2"><input type="checkbox" checked={showUnknown} onChange={event=>setShowUnknown(event.target.checked)}/>미확인 조합을 명시적으로 검사</label></>}</div>}
        </div>
      </section>
      <section className="grid gap-4 border-t pt-5 md:grid-cols-2" aria-label="목표 배치와 반복">
        <fieldset className="space-y-3"><legend className="font-semibold">한 번에 처리할 건수</legend><div className="flex flex-wrap gap-2">{(v.declared??chips).map(batch=><label key={batch} className="inline-flex min-h-11 items-center gap-2 rounded-full border px-4"><input type="checkbox" disabled={!!v.declared} checked={!!v.declared||d.batchSizes.includes(batch)} onChange={event=>update({batchSizes:event.target.checked?[...d.batchSizes,batch].sort((a,b)=>a-b):d.batchSizes.filter(item=>item!==batch)})}/>{batch}건</label>)}</div><p className="text-sm text-muted-foreground">{v.declared?"모델에 등록된 처리 건수를 모두 측정해요. 일부만 선택할 수 없어요.":"CPU·데이터셋·측정 방식이 같은 기존 결과가 있는지 검사해요."}</p></fieldset>
        <div className="space-y-4"><label className="grid gap-2 text-sm">배치별 반복 횟수<Input aria-invalid={!!error&&(!Number.isInteger(v.trials)||v.trials<3)} type="number" min={3} value={Number.isFinite(v.trials)?v.trials:""} onChange={event=>update({trials:event.target.valueAsNumber})}/>{error&&(!Number.isInteger(v.trials)||v.trials<3)&&<span className="text-destructive">3 이상의 정수를 입력하세요.</span>}</label><label className="grid gap-2 text-sm">CPU 개수<Input aria-invalid={!!error&&!/^[1-9]\d*$/.test(v.cpu)} value={v.cpu} onChange={event=>update({cpu:event.target.value})}/>{error&&!/^[1-9]\d*$/.test(v.cpu)&&<span className="text-destructive">1 이상의 정수를 입력하세요.</span>}</label></div>
        {error&&v.problem("size")&&<p role="alert" className="text-sm text-destructive">{v.problem("size")}</p>}
      </section>
      <section className="space-y-3 border-t pt-5" aria-label="실제 측정 기본값"><h2 className="font-semibold">측정 방식</h2><p className="text-sm">예열 {requestNumber(v.protocol.values.warmup)}회 / 측정 {requestNumber(v.protocol.values.activeDuration)}초 / 유휴 {requestNumber(v.protocol.values.idleDuration)}초 / 배치별 {requestNumber(v.trials)}회 반복</p><p className="break-words text-sm text-muted-foreground">출처: {v.protocol.from??"직접 입력"}{from&&` / 요청 복사 원본: ${from}`}</p><details open={!!error&&!!v.problem("advanced")}><summary className="min-h-11 cursor-pointer py-2">고급 측정 설정 · 예열·관측 간격 등 11개 수치와 CPU 코어</summary>{!d.protocol&&<p role="status" className="text-sm text-amber-700 dark:text-amber-300">사용할 측정 방식 근거가 없어요. 아래 수치를 직접 지정하세요.</p>}<AdvancedFields showErrors={!!error} value={v.protocol.values} set={values=>update({protocol:{values,from:"직접 입력"}})}/></details></section>
      <label className="grid gap-2 border-t pt-5 text-sm">요청 이름 (검사한 이름 고정)<Input aria-invalid={!!error&&!!v.problem("name")} value={v.name} maxLength={200} onChange={event=>update({name:event.target.value})}/>{error&&v.problem("name")&&<span className="text-destructive">{v.problem("name")}</span>}</label>
    </fieldset>
    <div id="measurement-errors" tabIndex={-1} role={error||blocked?"alert":undefined} className={error||blocked||dry.error||pre.error||send.error?"space-y-2":"hidden"}>
      {prefill.errors.map(issue=><p key={issue} className="text-destructive">{issue}</p>)}{error&&<p className="text-destructive">{error}</p>}
      {[dry.error,pre.error,send.error].filter((item):item is ApiError=>!!item).map((item,index)=><p key={index} className="text-destructive">HTTP {item.status} / {failure(item).code??"응답 코드 없음"}: {failure(item).message} 조건·권한·연결을 확인한 뒤 직접 다시 검사하세요.</p>)}
    </div>
    {verdict?.state==="unknown"&&!showUnknown&&<p className="text-sm text-muted-foreground">미확인 조합은 위 동의 항목을 선택한 뒤 검사하세요.</p>}
    {remaining.length>0&&<section aria-label="검사 전 남은 조건" className="rounded border p-3 text-sm"><h2 className="font-semibold">검사하려면 다음 조건을 채우세요</h2><ul className="list-inside list-disc">{remaining.map(({step,issue})=><li key={step}>{issue}</li>)}</ul></section>}
    {selected&&<section aria-label="이전 결과와 이번 요청" className="space-y-3 rounded border p-4 text-sm"><h2 className="font-semibold">이전 저장 결과와 이번 요청의 재사용은 별개예요</h2><p>이전 목표: {selected.batchProgress.target.join(", ")||"미기록"}건 · 저장된 측정 배치: {selected.batchProgress.measured.join(", ")||"없음"} · 이번 선택: {d.batchSizes.join(", ")||"미지정"}</p><p>{!answer?"요청 범위를 검사하면 현재 실행 조건에서 기존 결과를 제외할 수 있는지 확인해요.":answer.batchPlan?.conditionMatch==="exact"?"서버가 현재 동결 실행 조건과 일치하는 근거를 확인했어요. 이번 요청에서 제외되는 배치는 아래 검사 결과를 보세요.":"기존 저장 결과는 그대로 남아 있어요. 현재 동결 실행 조건에서 재사용이 확인되지 않아 이미 측정한 배치에서 제외될 수 있어요. 이 표시는 과거 결과가 없다는 뜻이 아니에요."}</p>{changes.length>0?<div className="overflow-x-auto"><table className="w-full text-left"><caption className="py-2 text-left font-medium">제공된 동결 조건에서 실제 달라진 필드</caption><thead><tr><th className="p-2">조건</th><th className="p-2">이전 저장값</th><th className="p-2">현재 검사값</th></tr></thead><tbody>{changes.map(change=><tr key={change.field} className="border-t"><th className="break-all p-2 font-medium">{change.label}<span className="block text-xs text-muted-foreground">{change.field}</span></th><td className="break-all p-2">{change.before}</td><td className="break-all p-2">{change.after}</td></tr>)}</tbody></table></div>:answer&&answer.batchPlan?.conditionMatch!=="exact"?<p>제공된 필드만으로 구체적인 변경을 확인하지 못했어요. 누락된 조건을 같다고 가정하지 않으며, 이미지 고정 여부 등 아래 제외 사유를 확인하세요.</p>:null}</section>}
    {explanation&&<section className="space-y-4 border-t pt-5"><h2 className="font-semibold">{preflightConclusion(explanation)}</h2><PreflightAnswer answer={explanation}/></section>}
    <p role="status" className={passed?"rounded-lg border border-primary bg-primary/10 p-4 font-semibold":"text-sm text-muted-foreground"}>{passed?"검사 통과 · 아직 제출되지 않았어요. 아래 버튼으로 측정을 요청하세요. 입력을 바꾸면 다시 검사해야 해요.":"검사는 요청 가능 여부만 확인하며 측정을 시작하지 않아요."} 필요한 결과가 모이면 일부 선택 건수의 실행 전에 측정을 마칠 수 있어요.</p>
    <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={blocked||pending||unknown} onClick={verify}>{pending?"검사 중":"요청 범위 검사"}</Button><Button disabled={!passed||blocked||send.isPending||unknown} onClick={submit}>측정 요청하기</Button></div>
    <Rows><RowDialog label="보낼 요청 원본" title="측정 요청"><SourceJSON inline value={manifest(d,context,namespace)}/></RowDialog>{(answer||pre.error)&&<RowDialog label="검사 원본" title="검사 원본"><SourceJSON inline value={answer??pre.error?.data}/></RowDialog>}</Rows>
  </Screen>
}
