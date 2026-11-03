import type { SchedulerDecision, SchedulerObservation, SchedulerObservationPage, SchedulerObservationSummary } from "./read-types.ts"
import { object, type ExperimentStage } from "./experiments.ts"
import { compareAt } from "./live.ts"

export type SchedulerRecordRef = {from:string;to:string;offset:string;kind:string;at:string;id:string;node?:string;startedAt?:string}

/** UI-only return state: never accept an external or non-experiment destination. */
export function historyReturnTo(value:string|null|undefined):string|null {
  if(!value||!value.startsWith("/")||value.startsWith("//")||/[\\\u0000-\u0020\u007f]/.test(value))return null
  const url=new URL(value,"https://dashboard.invalid")
  if(url.origin!=="https://dashboard.invalid"||!/^\/experiments(?:\/|$)/.test(url.pathname))return null
  return `${url.pathname}${url.search}${url.hash}`
}

export function schedulerHistoryKindMatches(filter:string,kind:string):boolean {
  return filter==="all"||filter===kind||filter==="decision-action"&&(kind==="decision"||kind==="action")
}

/** Node filtering is limited to the loaded summary page; cluster judgments have no node. */
export function schedulerHistoryNodeMatches(filter:string,event:SchedulerObservationSummary):boolean {
  const node=event.action?.node??event.node
  return !filter||!!node&&(node.uid===filter||node.name===filter)
}

type ScopeObservation=SchedulerObservationSummary&{decision?:Pick<SchedulerDecision,"at"|"status">&Partial<Pick<SchedulerDecision,"workloads"|"reasons">>}
function scopeSignature(event:ScopeObservation,codesOnly=false):string|null {
  const workloads=event.decision?.workloads
  return workloads==null?null:JSON.stringify(workloads.map(row=>[row.uid,row.state,codesOnly?row.reason?.split(":")[0]??null:row.reason??null]).sort((a,b)=>String(a[0]).localeCompare(String(b[0]))))
}

/** Never hide a scope/reason change behind a run of Keep judgments. */
export function groupHistoryKeeps<T extends {event:ScopeObservation}>(rows:T[]):(T&{keepCount:number})[] {
  const grouped:(T&{keepCount:number})[]=[]
  for(const row of rows) {
    const keep=row.event.decision?.status==="Keep",previous=grouped.at(-1),scope=scopeSignature(row.event)
    if(keep&&previous?.keepCount&&scope!==null&&scope===scopeSignature(previous.event)&&JSON.stringify(row.event.decision?.reasons)===JSON.stringify(previous.event.decision?.reasons))previous.keepCount++
    else grouped.push({...row,keepCount:keep?1:0})
  }
  return grouped
}

/** Chronological baseline + changes within one loaded page, not a period-wide history. */
export function schedulerScopeChanges<T extends {event:ScopeObservation;ref:SchedulerRecordRef}>(rows:T[]):T[] {
  const decisions=rows.filter(row=>row.event.decision).sort((a,b)=>compareAt(a.event.decision!.at,b.event.decision!.at))
  let previous:string|null=null
  return decisions.filter((row,index)=>{
    const signature=scopeSignature(row.event,true),changed=index===0||signature===null||signature!==previous
    previous=signature
    return changed
  })
}

export function schedulerRecordRef(event:SchedulerObservationSummary,from:string,to:string,offset:string):SchedulerRecordRef {
  const kind=event.decision?"decision":event.snapshot?"snapshot":event.settings?"settings":event.action?"action":event.power?"power":"other"
  const id=event.action?.id??event.node?.uid??event.settings?.actorUID??event.decision?.at??event.snapshot?.at??""
  return {from,to,offset:String(event.offset??offset),kind,at:event.at,id,...(event.action?{node:event.action.node.uid,startedAt:event.action.startedAt}:{})}
}

export function schedulerRecordParams(ref:SchedulerRecordRef,prefix=""):URLSearchParams {
  const params=new URLSearchParams()
  for(const [key,value] of Object.entries(ref))if(value!==undefined)params.set(prefix?`${prefix}${key[0].toUpperCase()}${key.slice(1)}`:key,value)
  return params
}

export function readSchedulerRecord(params:Pick<URLSearchParams,"get">,prefix=""):SchedulerRecordRef|null {
  const get=(key:string)=>params.get(prefix?`${prefix}${key[0].toUpperCase()}${key.slice(1)}`:key)
  const from=get("from"),to=get("to"),offset=get("offset"),kind=get("kind"),at=get("at"),id=get("id")
  if(!from||!to||!offset||!/^\d+$/.test(offset)||!kind||!at||id===null)return null
  return {from,to,offset,kind,at,id,...(get("node")!==null?{node:get("node")!}:{}),...(get("startedAt")!==null?{startedAt:get("startedAt")!}:{})}
}

export function schedulerRecordPath(ref:SchedulerRecordRef|null):string {
  return `v1/observations/scheduler?${new URLSearchParams(ref?{from:ref.from,to:ref.to,limit:"1",cursor:ref.offset,view:"compact"}:{})}`
}

export function schedulerRecordMatches(ref:SchedulerRecordRef,event:SchedulerObservationSummary|undefined):boolean {
  if(!event)return false
  const actual=schedulerRecordRef(event,ref.from,ref.to,ref.offset)
  return actual.kind===ref.kind&&actual.at===ref.at&&actual.id===ref.id&&actual.node===ref.node&&actual.startedAt===ref.startedAt
}

export function schedulerPageParams(params:URLSearchParams,cursor?:string):URLSearchParams {
  const next=new URLSearchParams(params)
  for(const key of ["offset","kind","at","id","node","startedAt"])next.delete(key)
  if(params.get("kind")==="power")next.set("kind","power")
  if(cursor)next.set("cursor",cursor);else next.delete("cursor")
  return next
}

export function schedulerComparisonParams(params:URLSearchParams):URLSearchParams {
  const next=schedulerPageParams(params,params.get("cursor")??undefined)
  next.set("compare","1")
  return next
}

export const powerNumber=(value:number|undefined|null)=>value==null?"미제공":value.toLocaleString("ko-KR",{maximumFractionDigits:3})

export function powerPolicyFacts(event:Pick<SchedulerObservation,"powerSampling"|"powerCoverage">):[string,string][] {
  const sampling=event.powerSampling,coverage=event.powerCoverage
  const seconds=(value:bigint|number)=>powerNumber(Number(value)/1e9)
  return [
    ...(sampling?[
      ["표본 간격 (초)",seconds(sampling.Interval)],["수집 기간 (초)",seconds(sampling.Duration)],
      ["요청 제한 시간 (초)",seconds(sampling.RequestTimeout)],
    ] as [string,string][]:[["표본 수집 정책","미제공"]] as [string,string][]),
    ...(coverage?[
      ["경계 허용 거리 (초)",seconds(coverage.MaximumBoundaryDistance)],["최소 관측 비율 (%)",powerNumber(coverage.MinimumCoverage*100)],
      ["최대 표본 간격 기준 (초)",seconds(coverage.MaximumGap)],
    ] as [string,string][]:[["에너지 범위 정책","미제공"]] as [string,string][]),
  ]
}
// Preserve RFC3339 nanoseconds: Date's millisecond truncation would omit same-time judgments.
export function decisionRange(at:string):{from:string;to:string}|null {
  const match=/^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(at)
  if(!match)return null
  const local=new Date(`${match[1]}Z`),whole=new Date(`${match[1]}${match[3]}`)
  if(!Number.isFinite(local.getTime())||local.toISOString().slice(0,19)!==match[1]||!Number.isFinite(whole.getTime()))return null
  const fraction=(match[2]??"").padEnd(9,"0"),next=Number(fraction)+1,second=whole.toISOString().slice(0,19)
  return {from:`${second}.${fraction}Z`,to:next===1e9?new Date(whole.getTime()+1000).toISOString().replace(".000Z",".000000000Z"):`${second}.${String(next).padStart(9,"0")}Z`}
}
export function decisionIdentity(event:SchedulerObservation):string {
  const d=event.decision
  return d?JSON.stringify([d.at,d.status,d.keep?.id,d.target?.id??null]):""
}
export function evidenceError(error:{status:number;data:unknown}):string {
  const code=object(error.data).code
  return error.status===504||code==="RequestTimeout"||code==="DashboardRelayTimeout"?"조회 시간 초과":"근거 조회 실패"
}
export function stageHistoryLink(stage:ExperimentStage):string {
  return `/records?${new URLSearchParams({from:stage.from,to:stage.to,limit:"100",filter:"decision-action",order:"asc"})}`
}
export function stageActivity(page:SchedulerObservationPage) {
  const events=page.events??[],decisions=events.filter(event=>event.decision),actions=events.filter(event=>event.action)
  return {decisions:decisions.length,actions:actions.length,partial:!!page.nextCursor||!!page.gapUntil,statuses:[...new Set(decisions.map(event=>event.decision!.status))],kinds:[...new Set(actions.map(event=>event.action!.kind))]}
}
export const stageEvidencePath=(stage:ExperimentStage)=>`v1/observations/scheduler?${new URLSearchParams({from:stage.from,to:stage.to,limit:"100"})}`
