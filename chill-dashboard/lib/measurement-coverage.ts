import type { ExecutionPath, ModelSpec } from "./domain.ts"
import type { BatchProgress, InventoryClass, InventoryView, MeasurementCoverageCell, MeasurementDisplayStatus, MeasurementFailure, MeasurementPlanSpec, ModelCompatibility, ProfilingRun } from "./read-types.ts"
import { safeMeasurementReturn } from "./measurements.ts"

export function deviceLabel(name:string) {
  return name.replace(/^edge-server-/,"").replace(/^jetson-agx-orin-/,"AGX Orin ").replace(/^jetson-orin-nano-/,"Orin Nano ").replace(/-/g," ")
}
export function conditionLabel(condition:Record<string,unknown>) {
  const cpu=condition.cpu as {request?:string;limit?:string;affinityCPUs?:number[]}|undefined
  const source=condition.metricSource as {nodeName?:string}|undefined
  const state=condition.state as {targetNode?:string;cpuRequest?:string}|undefined
  const active=condition.activeProtocol as {dataset?:{name?:string}|string}|undefined
  const affinity=cpu?.affinityCPUs??condition.cpuAffinity as number[]|undefined
  return `Node ${source?.nodeName??state?.targetNode??condition.nodeUID??"미지정"} / CPU ${cpu?.limit||cpu?.request||condition.cpuQuota||state?.cpuRequest||"미지정"}${affinity?.length?` (코어 ${affinity.join(",")})`:""} / 데이터셋 ${typeof active?.dataset==="string"?active.dataset:active?.dataset?.name??"미지정"}`
}
export function powerModeLabel(name:string,activeWatts:readonly number[]=[]) {
  const watts=activeWatts.filter(Number.isFinite)
  return watts.length?`${name} · 실측 ${Math.min(...watts).toFixed(1)}${Math.min(...watts)!==Math.max(...watts)?`–${Math.max(...watts).toFixed(1)}`:""} W`:`${name} · 전력 미확인`
}
export function measurementAge(value:string,now=Date.now()) {
  const timestamp=Date.parse(value)
  if(!Number.isFinite(timestamp))return "시각 미기록"
  const days=Math.floor((now-timestamp)/86400000)
  return days<0?"시각 확인 필요":days===0?"오늘":`${days}일 전`
}

export type CoverageModel = InventoryView["models"][number] & {spec:ModelSpec}
export function coverageSelection(search:string,cell:{id:string;model:{uid:string};deviceClass:{uid:string}}|undefined,open:boolean) {
  const next=new URLSearchParams(search)
  if(cell)next.set("group",`${cell.model.uid}/${cell.deviceClass.uid}`)
  if(open&&cell)next.set("cell",cell.id);else next.delete("cell")
  next.delete("variant")
  return next
}

// Hide only proven declaration conflicts; a missing runtime declaration remains unknown.
export function coveragePathVisible(cell:MeasurementCoverageCell,inventory?:InventoryView) {
  const model=inventory?.models.find(model=>model.uid===cell.model.uid) as CoverageModel|undefined
  const cls=inventory?.classes.find(cls=>cls.uid===cell.deviceClass.uid)
  const path=model?.spec.executionPaths.find(path=>path.name===cell.executionPath)
  if(model&&path&&cls&&staticCompatibility(model,path,cls).state==="unsupported")return false
  return cell.support.verdict!=="Incompatible"&&!cell.support.reasons.some(reason=>reason.code==="ArtifactArchitectureMismatch")
}
export function coverageWatts(cells:MeasurementCoverageCell[]) {
  return cells.flatMap(cell=>cell.variants.flatMap(variant=>variant.resultRows.flatMap(row=>row.activeWatts!==null&&Number.isFinite(row.activeWatts)?[row.activeWatts]:[])))
}
export function coverageModes(cells:MeasurementCoverageCell[]) {
  return [...new Set(cells.map(cell=>cell.powerMode))].sort((a,b)=>{
    const left=coverageWatts(cells.filter(cell=>cell.powerMode===a)),right=coverageWatts(cells.filter(cell=>cell.powerMode===b))
    return (left.length?Math.min(...left):Infinity)-(right.length?Math.min(...right):Infinity)||a.localeCompare(b)
  })
}
export function coverageVariant(cell:MeasurementCoverageCell,id?:string) {
  return cell.variants.find(variant=>variant.id===id)
    ??cell.variants.find(variant=>variant.assessment.displayStatus===cell.assessment.displayStatus)
    ??cell.variants.find(variant=>variant.id===cell.representative?.variantId)
    ??cell.variants[0]
}
export function coverageRepresentative(cell:MeasurementCoverageCell):MeasurementCoverageCell["representative"] {
  const variant=coverageVariant(cell),row=variant?.resultRows.find(row=>row.batch===1)??variant?.resultRows[0]
  return variant&&row?{variantId:variant.id,batch:row.batch,incrementalJoulesPerItem:row.incrementalJoulesPerItem,p95Milliseconds:row.p95Milliseconds}:null
}
export function coverageProgress(cell:MeasurementCoverageCell) {
  return cell.batchProgress??coverageVariant(cell)?.batchProgress??null
}
export const coverageMeasurementStates = {usable:"측정 있음",review:"검토 필요",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"미확인",unsupported:"지원 안 함"}
export function coverageMeasurementStatus(cell:MeasurementCoverageCell):MeasurementDisplayStatus {
  const progress=coverageProgress(cell),status=cell.assessment.displayStatus
  if(status!=="usable")return status
  return progress?.target.length&&!progress.target.every(batch=>progress.measured.includes(batch))?"insufficient":"usable"
}
export function coverageQuality(cell:MeasurementCoverageCell) {
  const assessments=cell.variants.length?cell.variants.map(variant=>variant.assessment):[cell.assessment]
  const checks=assessments.flatMap(assessment=>assessment.checklist.checks)
  const labels:Record<string,string>={ActiveTrials:"실행 반복 부족",IdleTrials:"유휴 반복 부족",IncrementalConfidence:"오차 범위 미확인",ClientLatency:"지연 표본 미확인",PowerSource:"전력 출처 미확인",FrozenPowerWindowPolicy:"전력 기록 기준 미확인",ExactConditionLineage:"측정 조건 연결 미확인",TargetBatch:"배치 근거 미확인",DatasetInputContract:"입력 연결 미확인"}
  const reasons=[...new Map(checks.filter(check=>check.result!=="pass").map(check=>[check.code,{code:check.code,label:labels[check.code]??"기타 품질 확인"}])).values()]
  if(!checks.length)reasons.push({code:"QualityUnavailable",label:"품질 근거 없음"})
  const met=assessments.every(assessment=>assessment.checklist.criteriaMet===true)
  const unmet=assessments.some(assessment=>assessment.checklist.criteriaMet===false||assessment.checklist.checks.some(check=>check.result==="fail"))
  return {label:met?"기준 충족":unmet?"기준 미충족":"품질 미확인",reasons}
}
export function coverageQualityReasons(cells:MeasurementCoverageCell[]) {
  const counts=new Map<string,{code:string;label:string;count:number}>()
  for(const cell of cells)for(const reason of coverageQuality(cell).reasons) {
    const prior=counts.get(reason.code)
    if(prior)prior.count++;else counts.set(reason.code,{...reason,count:1})
  }
  return [...counts.values()].sort((a,b)=>b.count-a.count)
}
export function coverageBatchState(progress:BatchProgress|null,batch:number) {
  if(!progress)return "unknown"
  if(progress.measured.includes(batch))return "measured"
  if(progress.inProgress.includes(batch))return "running"
  if(progress.missing.includes(batch))return "missing"
  if(progress.unknown.includes(batch))return "unknown"
  return progress.target.includes(batch)?"missing":"outside"
}

export function coverageProfileQuery(cell:MeasurementCoverageCell) {
  return new URLSearchParams({model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,limit:"50"})
}
export function coverageFailureHref(failure:MeasurementFailure,namespace:string,returnTo:string) {
  if(failure.runName)return `/measurements/profiling-runs/${encodeURIComponent(failure.runNamespace||namespace)}/${encodeURIComponent(failure.runName)}?${new URLSearchParams({uid:failure.runId.split("/").at(-1)??"",returnTo})}`
  const [requestNamespace,requestName,uid]=failure.requestId.split("/")
  return `/measurements/batch-energy-optimizations/${encodeURIComponent(requestNamespace||namespace)}/${encodeURIComponent(requestName)}?${new URLSearchParams({uid:uid??"",returnTo})}`
}
export function coverageStatusCounts(cells:MeasurementCoverageCell[]) {
  const counts:Record<MeasurementDisplayStatus,number>={usable:0,review:0,insufficient:0,problem:0,running:0,unmeasured:0,unknown:0,unsupported:0}
  for(const cell of cells)counts[coverageMeasurementStatus(cell)]++
  return counts
}
export function coverageRequestHref(cell:MeasurementCoverageCell,namespace:string,returnTo:string,variantId?:string,targetBatches:number[]=[]) {
  const variant=cell.variants.find(variant=>variant.id===variantId),condition=variant?.condition
  const progress=variant?.batchProgress??coverageProgress(cell)
  const batches=progress?.missing.length?progress.missing:progress?.target.length?progress.target:targetBatches
  const node=(condition?.metricSource as {nodeName?:string}|undefined)?.nodeName??(condition?.state as {targetNode?:string}|undefined)?.targetNode??cell.support.node?.name
  return `/measurements/new?${new URLSearchParams({namespace,model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,...(batches.length?{batches:batches.join(",")}:{}),returnTo,...(variant?{variantId:variant.id}:{}),...(node?{node}:{})})}`
}
export type Compatibility = {state:"candidate"|"unsupported"|"unknown";reason:string}
export function staticCompatibility(model:Pick<CoverageModel,"spec">, path:ExecutionPath, cls:InventoryClass):Compatibility {
  const artifact = model.spec.artifacts.find(item => item.name === path.artifact)
  if (!artifact?.source.architecture || !cls.architecture) return {state:"unknown",reason:"모델 파일 또는 architecture 선언 미확인"}
  if (artifact.source.architecture !== cls.architecture) return {state:"unsupported",reason:`architecture 불일치: 파일 ${artifact.source.architecture} / 장치 ${cls.architecture}`}
  if(path.runtime.backend==="rknnlite"&&cls.accelerator&&cls.accelerator!=="rk3588-npu")return {state:"unsupported",reason:`RKNNLite는 rk3588-npu 전용입니다. 장치 가속기: ${cls.accelerator}`}
  return {state:"candidate",reason:"선언상 후보 · 실제 runtime 지원 미확인"}
}
export type CompatibilityRead = {node:string;selection?:ModelCompatibility["selection"];data?:ModelCompatibility;error?:string}
export function serverCompatibility(fallback:Compatibility, reads:CompatibilityRead[]):Compatibility {
  if(fallback.state==="unsupported")return fallback
  if (!reads.length) return fallback
  const reason=reads.map(read=>`${read.node}: ${read.error??read.data?.reasons.map(reason=>`${reason.message} (${reason.code})`).join(" / ")??"미조회"}`).join(" · ")
  if (reads.some(read=>read.error||!read.data)) return {state:"unknown",reason:`지원 확인 불가 — ${reason}`}
  if (reads.every(read=>read.data?.verdict==="Compatible")) return {state:"candidate",reason:"정적 실행 지원 확인 · 측정 완료·Ready·admission 보장 아님"}
  if (reads.every(read=>read.data?.verdict==="Incompatible")) return {state:"unsupported",reason:`지원 안 함 — ${reason}`}
  return {state:"unknown",reason:`지원 확인 불가 — ${reason}`}
}

export function runAttempt(run:ProfilingRun) {
  const raw=run.labels?.["edge.dacs.io/attempt"]
  return raw&&/^[1-9]\d*$/.test(raw)&&Number.isSafeInteger(Number(raw))?`${raw}회차`:"회차 기록 없음"
}


export function stableCondition(value:unknown):string {
  return JSON.stringify(value, (_key,item) => typeof item === "bigint" ? item.toString() : item && typeof item === "object" && !Array.isArray(item) ? Object.fromEntries(Object.entries(item).sort(([a],[b]) => a.localeCompare(b))) : item)
}
export function planVariant(spec:MeasurementPlanSpec) {
  return stableCondition({state:spec.state,cpuQuota:spec.cpuQuota,cpuAffinity:spec.cpuAffinity,activeProtocol:spec.activeProtocol,idleProtocol:spec.idleProtocol})
}
export type RunSpec = {candidate?:MeasurementPlanSpec["state"]["candidate"];targetNode?:string;cpuRequest?:string;measurement?:{batch?:number;cpuQuota?:string;cpuAffinity?:number[];workloadMode?:string;dataset?:string;[key:string]:unknown};metricObservation?:unknown}
export const runSpec = (run:ProfilingRun) => run.spec as RunSpec
export function runGroupKey(run:ProfilingRun) {
  const spec = runSpec(run), {batch:_batch,...measurement} = spec.measurement ?? {}
  void _batch
  return stableCondition({namespace:run.namespace,candidate:spec.candidate,targetNode:spec.targetNode,cpuRequest:spec.cpuRequest,measurement,metricObservation:spec.metricObservation})
}
export function currentRunCondition(run:ProfilingRun,type:string) {
  return run.status.conditions?.find(condition => condition.type === type && condition.observedGeneration === run.generation)
}





export type MeasurementPrefill = {active:boolean;values:Record<string,string>;errors:string[];returnTo:string;node:string}
export function resolveMeasurementPrefill(params:URLSearchParams,namespace:string,inventory:InventoryView):MeasurementPrefill {
  const keys=["namespace","model","deviceClass","node","path","powerMode","from","uid","fromNamespace","fromUID","returnTo"],errors:string[]=[]
  for(const key of keys) if(params.getAll(key).length>1) errors.push(`${key}: 같은 키가 중복됐어요`)
  const values=Object.fromEntries(keys.map(key=>[key,params.get(key)??""]))
  const active=["model","deviceClass","node","path","powerMode"].some(key=>params.has(key))
  if(values.namespace && values.namespace !== namespace) errors.push(`namespace 접근 범위 불일치: ${values.namespace}`)
  if(values.fromNamespace && values.fromNamespace !== namespace) errors.push(`복사 원본 namespace 접근 범위 불일치: ${values.fromNamespace}`)
  if(active && values.from) errors.push("복사 원본과 조합 초깃값을 함께 사용할 수 없어요")
  const model=inventory.models.find(model=>model.name===values.model) as CoverageModel|undefined,cls=inventory.classes.find(cls=>cls.name===values.deviceClass)
  if(active) {
    if(!model) errors.push(`모델을 찾지 못했어요: ${values.model}`)
    if(!cls) errors.push(`장치 종류를 찾지 못했어요: ${values.deviceClass}`)
    const path=model?.spec.executionPaths.find(path=>path.name===values.path)
    if(!path) errors.push(`경로를 찾지 못했어요: ${values.path}`)
    if(cls && !cls.powerModes.some(mode=>mode.name===values.powerMode)) errors.push(`전력모드를 찾지 못했어요: ${values.powerMode}`)
    if(values.node && !cls?.nodes.some(node=>node.name===values.node)) errors.push(`해당 장치 종류의 Node가 아니에요: ${values.node}`)
    if(model && path && cls) {const verdict=staticCompatibility(model,path,cls);if(verdict.state==="unsupported") errors.push(verdict.reason)}
  }
  return {active,values,errors,returnTo:safeMeasurementReturn(values.returnTo),node:values.node || (cls?.nodes.length===1?cls.nodes[0].name:"")}
}
