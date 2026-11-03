import type { SchedulerObservation, SchedulerObservationSummary } from "./read-types.ts"

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

/** Server kind filters include Keep; collapse only adjacent judgments in the loaded page. */
export function groupHistoryKeeps<T extends {event:SchedulerObservationSummary}>(rows:T[]):(T&{keepCount:number})[] {
  const grouped:(T&{keepCount:number})[]=[]
  for(const row of rows) {
    const keep=row.event.decision?.status==="Keep",previous=grouped.at(-1)
    if(keep&&previous?.keepCount)previous.keepCount++
    else grouped.push({...row,keepCount:keep?1:0})
  }
  return grouped
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
