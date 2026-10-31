import type { SchedulerObservation } from "./read-types.ts"

export type SchedulerRecordRef = {from:string;to:string;offset:string;kind:string;at:string;id:string;node?:string;startedAt?:string}

export function schedulerHistoryKindMatches(filter:string,kind:string):boolean {
  return filter==="all"||filter===kind||filter==="decision-action"&&(kind==="decision"||kind==="action")
}

export function schedulerRecordRef(event:SchedulerObservation,from:string,to:string,offset:string):SchedulerRecordRef {
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
  return `v1/observations/scheduler?${new URLSearchParams(ref?{from:ref.from,to:ref.to,limit:"1",cursor:ref.offset}:{})}`
}

export function schedulerRecordMatches(ref:SchedulerRecordRef,event:SchedulerObservation|undefined):boolean {
  if(!event)return false
  const actual=schedulerRecordRef(event,ref.from,ref.to,ref.offset)
  return actual.kind===ref.kind&&actual.at===ref.at&&actual.id===ref.id&&actual.node===ref.node&&actual.startedAt===ref.startedAt
}

export function schedulerPageParams(params:URLSearchParams,cursor?:string):URLSearchParams {
  const next=new URLSearchParams(params)
  for(const key of ["offset","kind","at","id","node","startedAt"])next.delete(key)
  if(cursor)next.set("cursor",cursor);else next.delete("cursor")
  return next
}
