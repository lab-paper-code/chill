import type { MeasurementEvidenceLocation, MeasurementPlanSpec, OperationCondition, OperationCostMeasurement, OperationStepStatus, ProfilingRun, ResultRow } from "./read-types.ts"
import { codeText } from "./labels.ts"
import { nanosToMilliseconds, stringifySchedulerJSON } from "./scheduler.ts"
import { safeSource } from "./source-export.ts"

export function measurementRequestName(condition:{model:string;node:string;powerMode:string;path:string;cpu:string|null},taken:Set<string>,now=new Date()):string {
  const day=`${now.getFullYear()}${String(now.getMonth()+1).padStart(2,"0")}${String(now.getDate()).padStart(2,"0")}`
  const clean=(text:string)=>text.toLowerCase().replace(/[^a-z0-9.-]+/g,"-").replace(/[-.]*\.[-.]*/g,".").replace(/-+/g,"-").slice(0,200).replace(/^[^a-z0-9]+|[^a-z0-9]+$/g,"")
  const base=clean(`${condition.model}-${condition.node}-${condition.powerMode}-${condition.path}-cpu${condition.cpu??"1"}-${day}`)
  let name=base
  for(let suffix=2;taken.has(name);suffix++)name=clean(`${base.slice(0,190)}-${suffix}`)
  return name
}
export function formatMeasurementNumber(value:unknown, unit=""):string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "미제공"
  const digits = unit === "J/건" || unit === "J/item" ? 4 : unit === "W" ? 1 : ["ms", "%", "%p"].includes(unit) ? 2 : 0
  return value.toLocaleString("ko-KR", {minimumFractionDigits:digits,maximumFractionDigits:digits})
}
export function resultRowsCSV(rows:ResultRow[]):string {
  const keys = ["id","rowKind","profileId","sourceProfileId","batch","condition","p95Milliseconds","latencySampleCount","idleWatts","activeWatts","totalJoulesPerItem","incrementalJoulesPerItem","confidence","activeTrialCount","idleTrialCount","completedItems","completedCalls","powerSource","assessment","sources","missingReasons","reuse"] as const
  const escape = (value:unknown) => {
    let text=value == null ? "" : typeof value === "object" ? stringifySchedulerJSON(safeSource(value)) : String(safeSource(value))
    if(typeof value==="string"&&/^[\s]*[=+\-@]/.test(text))text=`'${text}`
    return `"${text.replaceAll('"','""')}"`
  }
  return [keys.map(escape).join(","),...rows.map(row=>keys.map(key=>escape(row[key])).join(","))].join("\r\n")
}
export type ProjectionMeasurementKind = "operation-cost-measurements" | "dispatch-occupancy-measurements"
export const isProjectionMeasurementKind = (kind:string):kind is ProjectionMeasurementKind => kind === "operation-cost-measurements" || kind === "dispatch-occupancy-measurements"
export const measurementPath = (kind:ProjectionMeasurementKind, namespace:string, name?:string) => `v1/namespaces/${encodeURIComponent(namespace)}/${kind}${name === undefined ? "" : `/${encodeURIComponent(name)}`}`
export const measurementListHref = (kind:ProjectionMeasurementKind, namespace:string) => `/measurements/${kind}?namespace=${encodeURIComponent(namespace)}`
export const measurementHref = (kind:ProjectionMeasurementKind, metadata:{namespace:string;name:string;uid:string}) => `/measurements/${kind}/${encodeURIComponent(metadata.namespace)}/${encodeURIComponent(metadata.name)}?uid=${encodeURIComponent(metadata.uid)}`
export const measurementUIDMismatch = (uid:string, expectedUID:string|null) => !!expectedUID && uid !== expectedUID
export const measurementReason = (reason:string) => codeText[reason] ? `${codeText[reason].join(" ")} (${reason})` : reason
export function measurementReasonSummary(reason:string) {
  if(codeText[reason]) return codeText[reason].join(" ")
  if(/exactly one proven primitive state effect/i.test(reason)) return "단일 상태 변경으로 확인되지 않아 비용을 채택하지 못했어요. 준비·제거 단계의 측정 근거를 확인하세요."
  if(/timeout|timed out|deadline exceeded/i.test(reason)) return "측정 결과를 기다리는 시간이 초과됐어요. 장비 상태와 측정 대기 시간을 확인하세요."
  if(/memory|OOM/i.test(reason)) return "측정에 필요한 메모리를 확보하지 못했어요. 배치 크기와 장비 메모리를 확인하세요."
  return /[가-힣]/.test(reason)?reason:"측정 근거가 채택되지 않았어요. 아래 원문에서 상세 사유를 확인하세요."
}
export function occupancyDuration(value?:unknown) {
  if (value === undefined) return "결과 없음"
  if ((typeof value !== "number" && typeof value !== "bigint") || (typeof value === "number" && (!Number.isFinite(value) || !Number.isSafeInteger(value)))) return "품질 경고: 점유시간을 변환할 수 없어요"
  if (value < 0) return "품질 경고: 점유시간이 음수로 기록됐어요"
  return `${formatMeasurementNumber(Number(nanosToMilliseconds(value)),"ms")}ms/request`
}
export function operationDuration(value?:unknown,unit:"ms"|"s"="ms",maximumFractionDigits=9) {
  if (value === undefined || value === null) return "실측 시간 기록 없음"
  if ((typeof value !== "number" && typeof value !== "bigint") || (typeof value === "number" && !Number.isSafeInteger(value)) || value < 0) return "품질 경고: 실측 시간이 올바르지 않아요"
  const seconds=Number(nanosToMilliseconds(value))/1000
  if(!Number.isFinite(seconds))return "품질 경고: 실측 시간이 올바르지 않아요"
  return unit==="s"?`${seconds.toLocaleString("ko-KR",{maximumFractionDigits})} s`:formatMeasurementDuration(value)
}
export function operationWallEnergy(value?:unknown,maximumFractionDigits?:number) {
  if (value === undefined || value === null) return "수용된 에너지 없음"
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? `${maximumFractionDigits===undefined?significant(value):value.toLocaleString("ko-KR",{maximumFractionDigits})} J` : "품질 경고·사용 확인 필요"
}
export const occupancyThroughput = (value?:string) => !value ? "결과 없음" : !/^[+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value) || !Number.isFinite(Number(value)) || Number(value) < 0 ? "품질 경고·사용 확인 필요" : `${significant(Number(value))} req/s`
export const significant = (value:number) => Number.isFinite(value) ? value.toLocaleString("ko-KR",{maximumSignificantDigits:3}) : `${String(value)} · 품질 경고`
export function formatMeasurementDuration(value:number|bigint) {
  const milliseconds=Number(nanosToMilliseconds(value))
  return `${formatMeasurementNumber(milliseconds,"ms")} ms`
}
export function formatMeasurementTime(value:string|number) {
  const date=new Date(value)
  return Number.isFinite(date.getTime()) ? `${date.toLocaleString("ko-KR",{timeZone:"Asia/Seoul",dateStyle:"medium",timeStyle:"medium"})} KST` : "시각 없음"
}
export function measurementConditionDiff(before:MeasurementPlanSpec,after:MeasurementPlanSpec) {
  const fields: [string,unknown,unknown][]=[
    ["CPU",before.cpuQuota,after.cpuQuota],["CPU 코어",before.cpuAffinity,after.cpuAffinity],["배치",before.batchSizes,after.batchSizes],["반복",before.trialsPerBatch,after.trialsPerBatch],["재시도 한도",before.retryLimit,after.retryLimit],
    ...Object.keys({...before.state,...after.state}).filter(key=>key!=="candidate").map(key=>[key,(before.state as unknown as Record<string,unknown>)[key],(after.state as unknown as Record<string,unknown>)[key]] as [string,unknown,unknown]),
    ...Object.keys({...before.state.candidate,...after.state.candidate}).map(key=>[key,(before.state.candidate as unknown as Record<string,unknown>)[key],(after.state.candidate as unknown as Record<string,unknown>)[key]] as [string,unknown,unknown]),
    ...(["activeProtocol","idleProtocol"] as const).flatMap(protocol=>Object.keys({...before[protocol],...after[protocol]}).map(key=>[`${protocol==="activeProtocol"?"부하":"유휴"} ${key}`,(before[protocol] as Record<string,unknown>)[key],(after[protocol] as Record<string,unknown>)[key]] as [string,unknown,unknown])),
  ]
  const display=(value:unknown)=>value===undefined?"미지정":typeof value==="object"?JSON.stringify(value):String(value)
  return fields.filter(([,a,b])=>JSON.stringify(a)!==JSON.stringify(b)).map(([label,a,b])=>`${label}: ${display(a)} → ${display(b)}`)
}
export function safeMeasurementReturn(value:string|null, fallback="/measurements") {
  if (!value || !value.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(value)) return fallback
  try {
    const url = new URL(value,"https://dashboard.invalid")
    return url.origin === "https://dashboard.invalid" && (/^\/measurements(?:\/|$)/.test(url.pathname) || url.pathname === "/scheduler/profiles" || /^\/experiments(?:\/|$)/.test(url.pathname)) ? value : fallback
  } catch { return fallback }
}
export const exactRun = (rows:ProfilingRun[],namespace:string,name:string,uid:string|null) => rows.find(run=>run.namespace===namespace&&run.name===name&&(!uid||run.uid===uid))
export const occupancySupply = (value?:string) => value === "closed-loop" ? "고정 동시 요청" : value === "poisson" ? "포아송 공급" : value || "기록되지 않았어요"
export function evidenceLocations(evidence:MeasurementEvidenceLocation[], result?:MeasurementEvidenceLocation) {
  return [...evidence,...(result ? [result] : [])].filter((location,index,all) => all.findIndex(other => other.store === location.store && other.key === location.key) === index)
}
export type OperationStep = {name:string;condition:OperationCondition|undefined;results:OperationStepStatus[]}
// Preserve condition order and every recorded result, including unmatched status-only steps.
export function operationSteps(data:OperationCostMeasurement):OperationStep[] {
  const recorded = data.status.steps ?? [], planned = data.conditions.steps
  return [
    ...planned.map(condition => ({name:condition.name,condition,results:recorded.filter(result => result.name === condition.name)})),
    ...recorded.filter(result => !planned.some(condition => condition.name === result.name)).map(result => ({name:result.name,condition:undefined,results:[result]})),
  ]
}

export type OperationTotals={durationNanos:bigint|null|undefined;wallJoules:number|null|undefined}
// A total needs exactly one result for every planned step; never label a partial sum as complete.
export function operationTotals(data:OperationCostMeasurement):OperationTotals {
  const planned=data.conditions.steps,recorded=data.status.steps??[]
  if (!planned.length || planned.some(step=>!recorded.some(result=>result.name===step.name))) return {durationNanos:undefined,wallJoules:undefined}
  if (recorded.length!==planned.length || planned.some(step=>recorded.filter(result=>result.name===step.name).length!==1)) return {durationNanos:null,wallJoules:null}
  let durationNanos:bigint|null|undefined=0n,wallJoules:number|null|undefined=0
  for (const step of recorded) {
    const duration=step.durationNanos,energy=step.wallJoules
    if (duration==null) {if(durationNanos!==null)durationNanos=undefined}
    else if ((typeof duration!=="number"&&typeof duration!=="bigint") || (typeof duration==="number"&&!Number.isSafeInteger(duration)) || duration<0) durationNanos=null
    else if (durationNanos!==undefined&&durationNanos!==null) durationNanos+=BigInt(duration)
    if (step.verdict!=="Accepted" || energy==null) {if(wallJoules!==null)wallJoules=undefined}
    else if (typeof energy!=="number" || !Number.isFinite(energy) || energy<0) wallJoules=null
    else if (wallJoules!==undefined&&wallJoules!==null) {wallJoules+=energy;if(!Number.isFinite(wallJoules))wallJoules=null}
  }
  return {durationNanos,wallJoules}
}

// Some Run status fields are JSON-encoded strings; decode only structured values.
export function runStatusObject(value:unknown):Record<string,unknown>|undefined {
  if (typeof value === "string") { try { value=JSON.parse(value) } catch { return undefined } }
  return value!==null&&typeof value==="object"&&!Array.isArray(value)?value as Record<string,unknown>:undefined
}
export const cleanRunLog=(value:unknown)=>typeof value==="string"?value.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g,""):""
export function runFailure(run:ProfilingRun) {
  const termination=runStatusObject(run.status.runtimeTermination),log=cleanRunLog(termination?.logTail)
  const failure=run.assessment?.failureSummary?.[0]
  const next:Record<string,string>={GPUMemoryExhausted:"GPU 메모리와 함께 실행 중인 작업을 확인하고 실행 경로를 검토하세요.",HostOOM:"Node 메모리와 함께 실행 중인 작업을 확인하세요.",PowerObservationTimeout:"전력 계측기와 관측 대기 한도를 확인하세요.",ImagePullFailed:"실행 이미지 주소와 가져오기 권한을 확인하세요.",RuntimeFailed:"회차의 실행 환경과 측정 조건을 확인하세요."}
  return {title:failure?.label??"실패 분류 미제공",excerpt:failure?.message??"서버의 실패 분류가 제공되지 않았어요. 원본 상태를 확인하세요.",exitCode:failure?.exitCode??termination?.exitCode,log,next:next[failure?.code??""]??"원본 실행 상태를 확인하고 담당자에게 문의하세요."}
}
