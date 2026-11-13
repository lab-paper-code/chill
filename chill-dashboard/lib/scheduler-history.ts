import type { SchedulerObservationSummary } from "./read-types.ts"

export type SchedulerRecordRef = {from:string;to:string;offset:string;kind:string;at:string;id:string;node?:string;startedAt?:string}


export function schedulerRecordRef(event:SchedulerObservationSummary,from:string,to:string,offset:string):SchedulerRecordRef {
  const kind=event.decision?"decision":event.snapshot?"snapshot":event.settings?"settings":event.action?"action":event.power?"power":"other"
  const id=event.action?.id??event.node?.uid??event.settings?.actorUID??event.decision?.at??event.snapshot?.at??""
  return {from,to,offset:String(event.offset??offset),kind,at:event.at,id,...(event.action?{node:event.action.node.uid,startedAt:event.action.startedAt}:{})}
}

export function schedulerRecordPath(ref:SchedulerRecordRef|null):string {
  return `v1/observations/scheduler?${new URLSearchParams(ref?{from:ref.from,to:ref.to,limit:"1",cursor:ref.offset,view:"compact"}:{})}`
}

export function schedulerRecordMatches(ref:SchedulerRecordRef,event:SchedulerObservationSummary|undefined):boolean {
  if(!event)return false
  const actual=schedulerRecordRef(event,ref.from,ref.to,ref.offset)
  return actual.kind===ref.kind&&actual.at===ref.at&&actual.id===ref.id&&actual.node===ref.node&&actual.startedAt===ref.startedAt
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
