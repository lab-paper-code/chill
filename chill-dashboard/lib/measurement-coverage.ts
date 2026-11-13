import type { ExecutionPath, ModelSpec } from "./domain.ts"
import type { BatchProgress, InventoryClass, InventoryView, MeasurementCoverageCell, MeasurementDisplayStatus, MeasurementPlanSpec, ProfilingRun } from "./read-types.ts"
import { safeMeasurementReturn } from "./measurements.ts"

export type CoverageModel = InventoryView["models"][number] & {spec:ModelSpec}

// Hide only proven declaration conflicts; a missing runtime declaration remains unknown.
export function coveragePathVisible(cell:MeasurementCoverageCell,inventory?:InventoryView) {
  const model=inventory?.models.find(model=>model.uid===cell.model.uid) as CoverageModel|undefined
  const cls=inventory?.classes.find(cls=>cls.uid===cell.deviceClass.uid)
  const path=model?.spec.executionPaths.find(path=>path.name===cell.executionPath)
  if(model&&path&&cls&&staticCompatibility(model,path,cls).state==="unsupported")return false
  return cell.support.verdict!=="Incompatible"&&!cell.support.reasons.some(reason=>reason.code==="ArtifactArchitectureMismatch")
}
export function coverageSupportReasons(cell:MeasurementCoverageCell) {
  return cell.support.reasons.length?cell.support.reasons:[{code:"DeclarationMismatch",message:"장치와 실행 경로 불일치"}]
}
export function coverageVariant(cell:MeasurementCoverageCell,id?:string) {
  return cell.variants.find(variant=>variant.id===id)
    ??cell.variants.find(variant=>variant.assessment.displayStatus===cell.assessment.displayStatus)
    ??cell.variants.find(variant=>variant.id===cell.representative?.variantId)
    ??cell.variants[0]
}
export function coverageProgress(cell:MeasurementCoverageCell) {
  const variant=coverageVariant(cell)
  return cell.batchProgress??variant?.batchProgress??null
}
export function coverageMeasurementStatus(cell:MeasurementCoverageCell,inventory?:InventoryView):MeasurementDisplayStatus {
  const progress=coverageProgress(cell),status=cell.assessment.displayStatus
  if(!coveragePathVisible(cell,inventory))return "unsupported"
  if(status!=="usable")return status
  return progress?.target.length&&!progress.target.every(batch=>progress.measured.includes(batch))?"insufficient":"usable"
}
export function coverageBatchState(progress:BatchProgress|null,batch:number) {
  if(!progress)return "unknown"
  if(progress.measured.includes(batch))return "measured"
  if(progress.inProgress.includes(batch))return "running"
  if(progress.missing.includes(batch))return "missing"
  if(progress.unknown.includes(batch))return "unknown"
  return progress.target.includes(batch)?"missing":"outside"
}
export function coverageRequestHref(cell:MeasurementCoverageCell,namespace:string,returnTo:string,variantId?:string) {
  const variant=cell.variants.find(variant=>variant.id===variantId),condition=variant?.condition
  const node=(condition?.metricSource as {nodeName?:string}|undefined)?.nodeName??(condition?.state as {targetNode?:string}|undefined)?.targetNode??cell.support.node?.name
  return `/measurements/requests/new?${new URLSearchParams({namespace,model:cell.model.name,modelUID:cell.model.uid,deviceClass:cell.deviceClass.name,deviceClassUID:cell.deviceClass.uid,path:cell.executionPath,powerMode:cell.powerMode,returnTo,...(variant?{variantId:variant.id}:{}),...(node?{node,...(cell.support.node?.name===node?{nodeUID:cell.support.node.uid}:{})}:{})})}`
}
export type Compatibility = {state:"candidate"|"unsupported"|"unknown";reason:string}
export function staticCompatibility(model:Pick<CoverageModel,"spec">, path:ExecutionPath, cls:InventoryClass):Compatibility {
  const artifact = model.spec.artifacts.find(item => item.name === path.artifact)
  if (!artifact?.source.architecture || !cls.architecture) return {state:"unknown",reason:"아키텍처 선언 없음"}
  if (artifact.source.architecture !== cls.architecture) return {state:"unsupported",reason:"아키텍처 불일치"}
  if(path.runtime.backend==="rknnlite"&&cls.accelerator&&cls.accelerator!=="rk3588-npu")return {state:"unsupported",reason:"RK3588 NPU 전용"}
  return {state:"candidate",reason:"실행 지원 미확인"}
}

export function runAttempt(run:ProfilingRun) {
  const raw=run.labels?.["edge.dacs.io/attempt"]
  return raw&&/^[1-9]\d*$/.test(raw)&&Number.isSafeInteger(Number(raw))?`${raw}회차`:"회차 기록 없음"
}
export type RunSpec = {candidate?:MeasurementPlanSpec["state"]["candidate"];targetNode?:string;measurement?:{batch?:number;workloadMode?:string;dataset?:string;[key:string]:unknown};metricObservation?:unknown}
export const runSpec = (run:ProfilingRun) => run.spec as RunSpec
export function currentRunCondition(run:ProfilingRun,type:string) {
  return run.status.conditions?.find(condition => condition.type === type && condition.observedGeneration === run.generation)
}

export type MeasurementPrefill = {active:boolean;values:Record<string,string>;errors:string[];returnTo:string;node:string}
export function resolveMeasurementPrefill(params:URLSearchParams,namespace:string,inventory:InventoryView):MeasurementPrefill {
  const keys=["namespace","model","modelUID","deviceClass","deviceClassUID","node","nodeUID","path","powerMode","from","uid","fromNamespace","fromUID","returnTo"],errors:string[]=[]
  for(const key of keys) if(params.getAll(key).length>1) errors.push("값 중복")
  const values=Object.fromEntries(keys.map(key=>[key,params.get(key)??""]))
  const active=["model","deviceClass","node","path","powerMode"].some(key=>params.has(key))
  if(values.namespace && values.namespace !== namespace) errors.push("다른 작업 영역")
  if(values.fromNamespace && values.fromNamespace !== namespace) errors.push("복사 원본이 다른 작업 영역")
  if(active && values.from) errors.push("복사 원본과 조합 동시 지정")
  const model=inventory.models.find(model=>model.name===values.model) as CoverageModel|undefined,cls=inventory.classes.find(cls=>cls.name===values.deviceClass)
  if(active) {
    if(!model) errors.push("모델 없음")
    if(!cls) errors.push("장치 종류 없음")
    if(values.modelUID&&model?.uid!==values.modelUID)errors.push("모델이 다시 등록됨")
    if(values.deviceClassUID&&cls?.uid!==values.deviceClassUID)errors.push("장치 종류가 다시 등록됨")
    if(values.nodeUID&&cls?.nodes.find(node=>node.name===values.node)?.uid!==values.nodeUID)errors.push("장치가 다시 등록됨")
    const path=model?.spec.executionPaths.find(path=>path.name===values.path)
    if(!path) errors.push("실행 경로 없음")
    if(cls && !cls.powerModes.some(mode=>mode.name===values.powerMode)) errors.push("전력 모드 없음")
    if(values.node && !cls?.nodes.some(node=>node.name===values.node)) errors.push("장치 종류에 없는 장치")
    if(model && path && cls) {const verdict=staticCompatibility(model,path,cls);if(verdict.state==="unsupported") errors.push(verdict.reason)}
  }
  return {active,values,errors,returnTo:safeMeasurementReturn(values.returnTo),node:values.node || (cls?.nodes.length===1?cls.nodes[0].name:"")}
}
