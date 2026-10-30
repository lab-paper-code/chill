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
export function resultRowsLatex(rows:ResultRow[]):string {
  const escape=(value:unknown)=>String(value??"").replace(/[\\&%$#_{}~^]/g,character=>({"\\":"\\textbackslash{}","&":"\\&","%":"\\%","$":"\\$","#":"\\#","_":"\\_","{":"\\{","}":"\\}","~":"\\textasciitilde{}","^":"\\textasciicircum{}"}[character]!)).replace(/[\r\n]+/g," ")
  return ["\\begin{longtable}{lll}","Row & Field & Value \\\\","\\hline",...rows.flatMap((row,index)=>Object.entries(safeSource(row) as Record<string,unknown>).map(([key,value])=>`${index+1} & ${escape(key)} & ${escape(typeof value==="object"?stringifySchedulerJSON(value):value)} \\\\`)),"\\end{longtable}"].join("\n")
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
export function operationDuration(value?:unknown) {
  if (value === undefined || value === null) return "실측 시간 기록 없음"
  if ((typeof value !== "number" && typeof value !== "bigint") || (typeof value === "number" && !Number.isSafeInteger(value))) return `${String(value)} ns · 품질 경고·사용 확인 필요`
  return `${formatMeasurementDuration(value)}${value < 0 ? " · 품질 경고·사용 확인 필요" : ""}`
}
export function operationWallEnergy(value?:unknown) {
  if (value === undefined || value === null) return "수용된 에너지 없음"
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? `${formatMeasurementNumber(value,"J")} J` : "품질 경고·사용 확인 필요"
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

// Some Run status fields are JSON-encoded strings; decode only structured values.
export function runStatusObject(value:unknown):Record<string,unknown>|undefined {
  if (typeof value === "string") { try { value=JSON.parse(value) } catch { return undefined } }
  return value!==null&&typeof value==="object"&&!Array.isArray(value)?value as Record<string,unknown>:undefined
}
export const cleanRunLog=(value:unknown)=>typeof value==="string"?value.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g,""):""
export function runFailure(run:ProfilingRun) {
  const termination=runStatusObject(run.status.runtimeTermination),log=cleanRunLog(termination?.logTail)
  const condition=run.status.conditions?.find(c=>c.type==="ExecutionComplete"&&c.observedGeneration===run.generation&&c.status==="False")
  const text=[log,condition?.message,condition?.reason].filter(Boolean).join("\n")
  const memory=/out of memory|failed to allocate memory|CUDA.*alloc|OOMKilled/i.test(text)
  const timeout=/timed? ?out|deadline exceeded/i.test(text)
  const excerpt=memory?text.match(/(?:failed to allocate memory|out of memory|OOMKilled|CUDA.*alloc)[^\n]*/i)?.[0]:text.split("\n").find(line=>/error|failed|timed? ?out|deadline exceeded/i.test(line))??condition?.message??condition?.reason
  return {title:memory?"메모리 할당 실패":timeout?"실행 제한 시간 초과":"회차 실행 실패",excerpt:excerpt?.trim().slice(0,240),exitCode:termination?.exitCode,log,
    next:memory?"해당 장비의 메모리와 함께 실행 중인 작업을 확인하고, 배치 크기나 실행 경로를 조정하세요.":timeout?"장비와 실행 로그를 확인하고 측정 시간·준비 시간을 검토하세요.":"아래 종료 상태와 로그를 확인하고 측정 조건을 수정하세요."}
}
