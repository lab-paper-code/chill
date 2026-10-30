import type { ExecutionPath, ModelSpec } from "./domain.ts"
import type { InventoryClass, InventoryView, MeasurementPlanSpec, ModelCompatibility, ProfilingRun } from "./read-types.ts"
import { safeMeasurementReturn } from "./measurements.ts"

export function powerModeLabel(name:string,activeWatts:readonly number[]=[]) {
  const watts=activeWatts.filter(Number.isFinite)
  return watts.length?`${name} · 실측 ${Math.min(...watts).toFixed(1)}${Math.min(...watts)!==Math.max(...watts)?`–${Math.max(...watts).toFixed(1)}`:""} W`:`${name} · 전력 미확인`
}

export type CoverageModel = InventoryView["models"][number] & {spec:ModelSpec}
export function coverageSelection(search:string,cell:{id:string;model:{uid:string};deviceClass:{uid:string}}|undefined,open:boolean) {
  const next=new URLSearchParams(search)
  if(cell)next.set("group",`${cell.model.uid}/${cell.deviceClass.uid}`)
  if(open&&cell)next.set("cell",cell.id);else next.delete("cell")
  next.delete("variant")
  return next
}
export type Compatibility = {state:"candidate"|"unsupported"|"unknown";reason:string}
export function staticCompatibility(model:Pick<CoverageModel,"spec">, path:ExecutionPath, cls:InventoryClass):Compatibility {
  const artifact = model.spec.artifacts.find(item => item.name === path.artifact)
  if (!artifact?.source.architecture || !cls.architecture) return {state:"unknown",reason:"모델 파일 또는 architecture 선언 미확인"}
  if (artifact.source.architecture !== cls.architecture) return {state:"unsupported",reason:`architecture 불일치: 파일 ${artifact.source.architecture} / 장치 ${cls.architecture}`}
  return {state:"candidate",reason:"선언상 후보 · 실제 runtime 지원 미확인"}
}
export type CompatibilityRead = {node:string;selection?:ModelCompatibility["selection"];data?:ModelCompatibility;error?:string}
export function serverCompatibility(fallback:Compatibility, reads:CompatibilityRead[]):Compatibility {
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
