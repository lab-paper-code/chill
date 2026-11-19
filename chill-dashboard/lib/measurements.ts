import type { OperationCondition, OperationCostMeasurement, OperationStepStatus, ProfilingRun } from "./read-types.ts"
import { codeMessage } from "./status-labels.ts"

export function measurementRequestName(condition:{model:string;node:string;powerMode:string;path:string},taken:Set<string>,now=new Date()):string {
  const day=`${now.getFullYear()}${String(now.getMonth()+1).padStart(2,"0")}${String(now.getDate()).padStart(2,"0")}`
  const clean=(text:string)=>text.toLowerCase().replace(/[^a-z0-9.-]+/g,"-").replace(/[-.]*\.[-.]*/g,".").replace(/-+/g,"-").slice(0,200).replace(/^[^a-z0-9]+|[^a-z0-9]+$/g,"")
  const base=clean(`${condition.model}-${condition.node}-${condition.powerMode}-${condition.path}-${day}`)
  let name=base
  for(let suffix=2;taken.has(name);suffix++)name=clean(`${base.slice(0,190)}-${suffix}`)
  return name
}
export function formatMeasurementNumber(value:unknown, unit=""):string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "미제공"
  const digits = unit === "J/건" || unit === "J/item" ? 4 : unit === "W" ? 1 : ["ms", "%", "%p"].includes(unit) ? 2 : 0
  return value.toLocaleString("ko-KR", {minimumFractionDigits:digits,maximumFractionDigits:digits})
}
export type ProjectionMeasurementKind = "operation-cost-measurements"
export const measurementPath = (kind:ProjectionMeasurementKind, namespace:string, name?:string) => `v1/namespaces/${encodeURIComponent(namespace)}/${kind}${name === undefined ? "" : `/${encodeURIComponent(name)}`}`
export function measurementReasonSummary(reason:string) {
  if(!reason.trim())return "상세 사유 미제공"
  const projectionReasons:Record<string,string>={
    MeasurementFailed:"측정 실패",
    IsolationUnconfirmed:"장비 단독 사용 미확인",
    MeasurementEvidenceMissing:"측정 근거 없음",
    Insufficient:"채택 근거 부족",
    Accepted:"측정 근거 채택",
    CatalogUnavailable:"카탈로그 조회 불가",
  }
  if(projectionReasons[reason])return projectionReasons[reason]
  if(/No verified catalog profile/i.test(reason))return "현재 조건의 검증된 프로파일 없음"
  if(/unknown field/i.test(reason))return "설정 항목 미지원"
  if(/native preparation Pod failed/i.test(reason))return "모델 준비 실패"
  const message=codeMessage(reason)
  if(message) return message
  if(/exactly one proven primitive state effect/i.test(reason)) return "단일 상태 변경 미확인"
  if(/timeout|timed out|deadline exceeded/i.test(reason)) return "측정 대기 시간 초과"
  if(/memory|OOM/i.test(reason)) return "측정 메모리 부족"
  return /[가-힣]/.test(reason)?reason:"상세 사유: 원문"
}
export function safeMeasurementReturn(value:string|null, fallback="/measurements") {
  if (!value || !value.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(value)) return fallback
  try {
    const url = new URL(value,"https://dashboard.invalid")
    return url.origin === "https://dashboard.invalid" && (/^\/measurements(?:\/|$)/.test(url.pathname) || url.pathname === "/scheduler/profiles" || /^\/experiments(?:\/|$)/.test(url.pathname)) ? value : fallback
  } catch { return fallback }
}
export const exactRun = (rows:ProfilingRun[],namespace:string,name:string,uid:string|null) => rows.find(run=>run.namespace===namespace&&run.name===name&&(!uid||run.uid===uid))
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
  const failure=run.assessment?.failureSummary?.[0]
  const spec=run.spec as {targetNode?:string;candidate?:{modelSpec?:string;executionPath?:string};measurement?:{batch?:number}}
  const smaller=spec.measurement?.batch===1?"다른 CPU 정수 8비트 경로 / 큰 메모리 장치":"더 작은 배치"
  const actions:Record<string,[string,string][]>={
    GPUMemoryExhausted:[["점검","GPU 여유 메모리, 동시 작업"],["재요청",smaller],["동시 점유","당시 기록 없음"]],
    HostOOM:[["점검","시스템 메모리, 동시 작업"],["재요청",smaller]],
    PowerObservationTimeout:[["점검","전력 계측기 연결, 응답, 대기 한도, 간격"],["재요청","전력 관측 복구 후 동일 조건"],["판정 범위","모델 성능 판정 제외"]],
    ImagePullFailed:[["점검","이미지 주소, 버전, 가져오기 권한"],["재요청","이미지 접근 복구 후"]],
    RuntimeFailed:[["점검","최초 오류, 모델 파일, 장치 지원, 실행 환경"],["재요청","지원 실행 경로"]],
  }
  return {title:failure?.label??"실패 분류 미제공",excerpt:failure?.message??"실패 분류 미제공",exitCode:failure?.exitCode??termination?.exitCode,log,
    target:[["장치",spec.targetNode??"미기록"],["모델",spec.candidate?.modelSpec??"미기록"],["경로",spec.candidate?.executionPath??"미기록"]] as [string,string][],
    actions:actions[failure?.code??""]??[["점검","실행 조건, 환경, 최초 오류"],["전달","대상 장치, 모델 정보"]]}
}
