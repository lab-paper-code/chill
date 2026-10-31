import type { DispatchOccupancyMeasurement, MeasurementEvidenceLocation, MeasurementPlanSpec, OperationCondition, OperationCostMeasurement, OperationStepStatus, ProfilingRun, ResultRow } from "./read-types.ts"
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
export function measurementUncertainty(row:ResultRow|undefined):string {
  const ci=row?.confidence,mean=row?.incrementalJoulesPerItem
  if(!ci||!Number.isFinite(ci.level)||ci.level<=0||ci.level>=1||!Number.isFinite(ci.halfWidth)||ci.halfWidth<0)return "오차 미제공"
  const relative=typeof mean==="number"&&Number.isFinite(mean)&&mean!==0?100*ci.halfWidth/Math.abs(mean):null
  return `${formatMeasurementNumber(ci.level*100)}% 오차 범위 ${relative!==null&&Number.isFinite(relative)?`±${formatMeasurementNumber(relative,"%")}% (평균 대비)`:`±${formatMeasurementNumber(ci.halfWidth,"J/건")} J/건`}`
}
export function measurementCheckText(check:{code:string;message:string}):string {
  const labels:Record<string,string>={
    ExactConditionLineage:"측정 당시 조건과 원본 결과의 연결",TargetBatch:"이 배치의 실제 측정",
    ActiveTrials:"작업을 실행한 독립 반복 횟수",IdleTrials:"작업 없이 잰 반복 횟수",
    IncrementalConfidence:"작업으로 늘어난 에너지의 오차 범위",ClientLatency:"응답 100건 중 95번째 지연과 표본 수",
    PowerSource:"전력을 잰 계측 출처",FrozenPowerWindowPolicy:"전력 기록의 표본 수·누락 구간·실패 처리 기준",
  }
  return labels[check.code]??check.message
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
  if(!reason.trim())return "상세 사유 미제공"
  const projectionReasons:Record<string,string>={
    MeasurementFailed:"측정 실패",
    IsolationUnconfirmed:"장비 단독 사용 미확인",
    MeasurementEvidenceMissing:"측정 근거 없음",
    Insufficient:"채택 근거 부족",
    Accepted:"측정 근거 채택",
  }
  if(projectionReasons[reason])return projectionReasons[reason]
  if(/unknown field/i.test(reason))return "설정 항목 미지원"
  if(/native preparation Pod failed/i.test(reason))return "모델 준비 실패"
  if(/Stored occupancy does not match/i.test(reason))return "요청·점유시간 근거 불일치"
  if(/Signed negative d/i.test(reason))return "점유시간 음수"
  if(codeText[reason]) return codeText[reason].join(" ")
  if(/exactly one proven primitive state effect/i.test(reason)) return "단일 상태 변경 미확인"
  if(/timeout|timed out|deadline exceeded/i.test(reason)) return "측정 대기 시간 초과"
  if(/memory|OOM/i.test(reason)) return "측정 메모리 부족"
  return /[가-힣]/.test(reason)?reason:"상세 사유: 원문"
}
// Numeric table cells stay unitless; diagnostics belong in the adjacent reason column.
export function occupancyDuration(value?:unknown) {
  if ((typeof value !== "number" && typeof value !== "bigint") || (typeof value === "number" && !Number.isSafeInteger(value)) || value < 0) return "—"
  const milliseconds=Number(nanosToMilliseconds(value))
  return Number.isFinite(milliseconds)?formatMeasurementNumber(milliseconds,"ms"):"—"
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
export const occupancyThroughput = (value?:string) => !value || !/^[+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value) || !Number.isFinite(Number(value)) || Number(value) < 0 ? "—" : significant(Number(value))
export function occupancyReason(data:DispatchOccupancyMeasurement) {
  const {perRequestNanos:d,throughput:x,reason}=data.status
  if((typeof d==="number"||typeof d==="bigint")&&d<0)return "점유시간 음수"
  if(d!==undefined&&occupancyDuration(d)==="—")return "점유시간 오류"
  if(x&&occupancyThroughput(x)==="—")return "처리량 오류"
  const label=data.assessment?.reasons[0]?.label
  if(label&&label.length<=24&&/[가-힣]/.test(label))return label
  const recorded=reason||data.assessment?.reasons[0]?.message||data.assessment?.reasons[0]?.code||""
  if(recorded) {
    const summary=measurementReasonSummary(recorded)
    return summary==="상세 사유: 원문"?"사유 확인 필요":summary.length<=24?summary:"측정 조건 확인 필요"
  }
  return data.assessment?.displayStatus==="problem"?"상세 사유 미제공":d===undefined||!x?"결과 없음":"—"
}
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
  const spec=run.spec as {targetNode?:string;candidate?:{modelSpec?:string;executionPath?:string};measurement?:{batch?:number}}
  const smaller=spec.measurement?.batch===1?"다른 CPU·int8 경로 / 큰 메모리 장치":"더 작은 배치"
  const actions:Record<string,[string,string][]>={
    GPUMemoryExhausted:[["점검","GPU 여유 메모리·동시 작업"],["재요청",smaller],["동시 점유","당시 기록 없음"]],
    HostOOM:[["점검","시스템 메모리·동시 작업"],["재요청",smaller]],
    PowerObservationTimeout:[["점검","전력 계측기 연결·응답·대기 한도·간격"],["재요청","전력 관측 복구 후 동일 조건"],["판정 범위","모델 성능 판정 제외"]],
    ImagePullFailed:[["점검","이미지 주소·버전·가져오기 권한"],["재요청","이미지 접근 복구 후"]],
    RuntimeFailed:[["점검","최초 오류·모델 파일·장치 지원·실행 환경"],["재요청","지원 실행 경로"]],
  }
  return {title:failure?.label??"실패 분류 미제공",excerpt:failure?.message??"실패 분류 미제공",exitCode:failure?.exitCode??termination?.exitCode,log,
    target:[["노드",spec.targetNode??"미기록"],["모델",spec.candidate?.modelSpec??"미기록"],["경로",spec.candidate?.executionPath??"미기록"]] as [string,string][],
    actions:actions[failure?.code??""]??[["점검","실행 조건·환경·최초 오류"],["전달","대상 노드·모델 정보"]]}
}
