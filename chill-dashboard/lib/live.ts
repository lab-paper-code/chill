import type { OperatingPower } from "./read-types.ts"

export type LiveWorkload = {
  uid:string; name:string; model:string; definitionID:string|null
  slo:{percentile:number;targetLatencyNanos:number}; autonomous:boolean; admission:boolean; pending:number
  readiness:{ready:boolean;reason:string;observedAt:string}
}
export type LiveNode = {uid:string;name:string;powerMode:string;ready:boolean;measurement:boolean;reasons?:string[];observedAt:string|null}
export type LiveInstance = {
  id:string;node:{uid:string;name:string};ownerWorkloadUID:string;profileID:string;ready:boolean;stopped:boolean
  routable:Record<string,boolean>;running:Record<string,number>;waiting:Record<string,number>;observedAt:string
}
export type LiveDecision = {at:string;state:string;reason:string;explanation:string}
export type LiveAction = {
  id:string;kind:string;node:{uid:string;name:string};workloadUID:string;status:string;startedAt:string;completedAt?:string
  expectedSeconds:number|null;expectedSource:{costID:string;reference:unknown;statistic:"mean";trialCount:number}|null
}
export type LiveReservation = {owner:string;nodes:{uid:string;name:string}[];workloadUIDs:string[];podUIDs:string[];instances:string[];reason:string;explanation:string}
export type LiveMetric = {received:number;completed:number;failed:number;withinSLO:number;latencySamples:number;p50ms:number|null;p90ms:number|null;p95ms:number|null;p99ms:number|null}
export type LiveGap = {from:string;to:string;scope:string;reason:string}
type Envelope = {seq:number;at:string}
export type LiveMetrics = Envelope & {type:"metrics";bucketAt:string;chill_obslog_queue_length:number;chill_obslog_oldest_wait_seconds:number;workloads:Record<string,LiveMetric>;nodes:Record<string,{watts:number|null;observedAt:string|null}>;pool:{wholeWatts:number|null;activeWatts:number|null};gaps?:LiveGap[]}
export type LiveCluster = {kind:"pod"|"issue";at:string;event:string;pod?:{uid:string;namespace?:string;name?:string;node?:string;phase?:string;ready?:string};source?:string;reason?:string;previousReason?:string}
export type LiveActionEvent = LiveAction & {phase:"started"|"completed"|"failed";result?:{status:string;error?:string}}
export type LiveEvent = {at:string;seq?:number} & ({type:"event";kind:"decision";decision:LiveDecision;action?:never;cluster?:never}|{type:"event";kind:"action";action:LiveActionEvent;decision?:never;cluster?:never}|{type:"event";kind:"cluster";cluster:LiveCluster;decision?:never;action?:never})
export type LiveSnapshot = {workloads:LiveWorkload[];nodes:LiveNode[];instances:LiveInstance[];routing:Record<string,Record<string,number>>;decision:LiveDecision|null;actions:LiveAction[];reservations:LiveReservation[]}
export type LiveDelta = Partial<LiveSnapshot> & {removed?:{instances?:string[];actions?:string[]}}
export type LiveSequenceRange = {fromSeq:number;toSeq:number}
export type LiveResumeHistory = {
  reason:"fresh"|"checkpoint-unmatched";replayed:false
  requested:{afterSeq:number;throughSeq:number;at?:string}
  retained:{events:LiveSequenceRange|null;power:LiveSequenceRange|null}
  gaps:(LiveSequenceRange & {scope:"events"|"power";reason:"NoRetainedFrames"})[]
}
export type RecordedLivePage = {events:LiveEvent[];from:string;to:string;nextCursor?:string;coverage:OperatingPower["coverage"]}
export type LiveMessage = (Envelope & {type:"hello";protocol:"chill-live/1";serverAt:string;retainFrom:number}) | (Envelope & {type:"snapshot";history?:LiveResumeHistory} & LiveSnapshot) | (Envelope & {type:"delta"} & LiveDelta) | LiveMetrics | (Envelope & LiveEvent) | (Envelope & {type:"heartbeat";serverAt:string})
export type LiveState = {
  snapshot:LiveSnapshot|null;checkpoint:Envelope|null;metrics:LiveMetrics[];events:LiveEvent[];gaps:LiveGap[]
  lastReceivedAt:number|null;lastObservedAt:string|null;lastPower:{bucketAt:string;watts:number}|null;status:string
  protocolError?:string
  powerScale:number
  definitions:Record<string,LiveWorkload>;resyncRequired:boolean;historyGaps:LiveGap[]
  resumeHistory:LiveResumeHistory|null;eventArchive:Omit<RecordedLivePage,"events"> & {partial:boolean}|null;eventArchiveError:string|null
  eventArchiveErrorDetail?: unknown
}
export function initialLiveState():LiveState {
  return {snapshot:null,checkpoint:null,metrics:[],events:[],gaps:[],lastReceivedAt:null,lastObservedAt:null,lastPower:null,powerScale:1,status:"idle",definitions:{},resyncRequired:false,historyGaps:[],resumeHistory:null,eventArchive:null,eventArchiveError:null}
}

function invalid(reason="Invalid chill-live/1 message"):never { throw new Error(reason) }
function field<T>(key:string|number,value:unknown,parse:(value:unknown)=>T):T {
  try { return parse(value) } catch (error) { throw new Error(`${key}: ${error instanceof Error ? error.message : String(error)}`) }
}
function object(value:unknown):Record<string,unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value) || ![Object.prototype,null].includes(Object.getPrototypeOf(value))) return invalid("Expected object")
  return value as Record<string,unknown>
}
function text(value:unknown):string { return typeof value === "string" ? value : invalid("Expected string") }
function boolean(value:unknown):boolean { return typeof value === "boolean" ? value : invalid("Expected boolean") }
function number(value:unknown):number { return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : invalid("Expected finite nonnegative number") }
function count(value:unknown):number { const result=number(value); return Number.isSafeInteger(result) ? result : invalid("Expected safe integer") }
function timestamp(value:unknown):string {
  const result=text(value)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(result)) return invalid("Expected RFC3339Nano UTC timestamp")
  const date=new Date(result)
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0,19) === result.slice(0,19) ? result : invalid("Invalid calendar timestamp")
}
function nullable<T>(value:unknown,parse:(value:unknown)=>T):T|null { return value === null ? null : parse(value) }
function array<T>(value:unknown,parse:(value:unknown)=>T):T[] { return Array.isArray(value) ? value.map((row,index)=>field(index,row,parse)) : invalid("Expected array") }
function map<T>(value:unknown,parse:(value:unknown)=>T):Record<string,T> { return Object.fromEntries(Object.entries(object(value)).map(([key,row])=>[key,field(key,row,parse)])) }
function unique<T>(rows:T[],key:(row:T)=>string):T[] { return new Set(rows.map(key)).size === rows.length ? rows : invalid("Duplicate identity") }
function nodeRef(value:unknown) { const row=object(value);return {uid:field("uid",row.uid,text),name:field("name",row.name,text)} }
function workload(value:unknown):LiveWorkload {
  const row=object(value)
  const slo=field("slo",row.slo,object),ready=field("readiness",row.readiness,object)
  const percentile=field("slo.percentile",slo.percentile,number),targetLatencyNanos=field("slo.targetLatencyNanos",slo.targetLatencyNanos,number)
  if (percentile > 100) invalid("slo.percentile: Expected value at most 100")
  if (!Number.isInteger(targetLatencyNanos)) invalid("slo.targetLatencyNanos: Expected integer")
  return {uid:field("uid",row.uid,text),name:field("name",row.name,text),model:field("model",row.model,text),definitionID:field("definitionID",row.definitionID,value=>nullable(value,text)),slo:{percentile,targetLatencyNanos},autonomous:field("autonomous",row.autonomous,boolean),admission:field("admission",row.admission,boolean),pending:field("pending",row.pending,count),readiness:{ready:field("readiness.ready",ready.ready,boolean),reason:field("readiness.reason",ready.reason,text),observedAt:field("readiness.observedAt",ready.observedAt,timestamp)}}
}
function node(value:unknown):LiveNode {
  const row=object(value)
  return {uid:field("uid",row.uid,text),name:field("name",row.name,text),powerMode:field("powerMode",row.powerMode,text),ready:field("ready",row.ready,boolean),measurement:field("measurement",row.measurement,boolean),...(row.reasons === undefined ? {} : {reasons:field("reasons",row.reasons,value=>array(value,text))}),observedAt:field("observedAt",row.observedAt,value=>nullable(value,timestamp))}
}
function instance(value:unknown):LiveInstance {
  const row=object(value)
  return {id:field("id",row.id,text),node:field("node",row.node,nodeRef),ownerWorkloadUID:field("ownerWorkloadUID",row.ownerWorkloadUID,text),profileID:field("profileID",row.profileID,text),ready:field("ready",row.ready,boolean),stopped:field("stopped",row.stopped,boolean),routable:field("routable",row.routable === null ? {} : row.routable,value=>map(value,boolean)),running:field("running",row.running === null ? {} : row.running,value=>map(value,count)),waiting:field("waiting",row.waiting === null ? {} : row.waiting,value=>map(value,count)),observedAt:field("observedAt",row.observedAt,timestamp)}
}
function decision(value:unknown):LiveDecision { const row=object(value);return {at:field("at",row.at,timestamp),state:field("state",row.state,text),reason:field("reason",row.reason,text),explanation:field("explanation",row.explanation,text)} }
export function liveActionKey(row:LiveAction):string { return JSON.stringify([row.id,row.node.uid,row.startedAt]) }
/** Shared by the live board and operating menu: history and plans are not executing changes. */
export function isLiveActionActive(row:{status:string;completedAt?:string}):boolean { return !row.completedAt && (row.status === "starting" || row.status === "running") }
function action(value:unknown):LiveAction {
  const row=object(value)
  let expectedSource:LiveAction["expectedSource"]=null
  if (row.expectedSource !== null) {
    const source=field("expectedSource",row.expectedSource,object)
    if (source.statistic !== "mean") invalid("expectedSource.statistic: Expected mean")
    field("expectedSource.reference",source.reference,object)
    expectedSource={costID:field("expectedSource.costID",source.costID,text),reference:source.reference,statistic:"mean",trialCount:field("expectedSource.trialCount",source.trialCount,count)}
  }
  return {id:field("id",row.id,text),kind:field("kind",row.kind,text),node:field("node",row.node,nodeRef),workloadUID:field("workloadUID",row.workloadUID,text),status:field("status",row.status,text),startedAt:field("startedAt",row.startedAt,timestamp),...(row.completedAt === undefined ? {} : {completedAt:field("completedAt",row.completedAt,timestamp)}),expectedSeconds:field("expectedSeconds",row.expectedSeconds,value=>nullable(value,number)),expectedSource}
}
function reservation(value:unknown):LiveReservation {
  const row=object(value)
  return {owner:field("owner",row.owner,text),nodes:field("nodes",row.nodes,value=>array(value,nodeRef)),workloadUIDs:field("workloadUIDs",row.workloadUIDs === null ? [] : row.workloadUIDs,value=>array(value,text)),podUIDs:field("podUIDs",row.podUIDs === null ? [] : row.podUIDs,value=>array(value,text)),instances:field("instances",row.instances === null ? [] : row.instances,value=>array(value,text)),reason:field("reason",row.reason,text),explanation:field("explanation",row.explanation,text)}
}
function metric(value:unknown):LiveMetric {
  const row=object(value)
  const result={received:field("received",row.received,count),completed:field("completed",row.completed,count),failed:field("failed",row.failed,count),withinSLO:field("withinSLO",row.withinSLO,count),latencySamples:field("latencySamples",row.latencySamples,count),p50ms:field("p50ms",row.p50ms,value=>nullable(value,number)),p90ms:field("p90ms",row.p90ms,value=>nullable(value,number)),p95ms:field("p95ms",row.p95ms,value=>nullable(value,number)),p99ms:field("p99ms",row.p99ms,value=>nullable(value,number))}
  if (result.withinSLO > result.completed) invalid("withinSLO: Exceeds completed")
  if (result.latencySamples > result.completed + result.failed) invalid("latencySamples: Exceeds completed + failed")
  return result
}
function gap(value:unknown):LiveGap { const row=object(value);const result={from:field("from",row.from,timestamp),to:field("to",row.to,timestamp),scope:field("scope",row.scope,text),reason:field("reason",row.reason,text)};return compareAt(result.from,result.to) <= 0 ? result : invalid("from: Exceeds to") }
const snapshotKeys=["workloads","nodes","instances","routing","decision","actions","reservations"]
function projection(row:Record<string,unknown>,full:boolean):LiveDelta {
  const result:LiveDelta={}
  for (const key of snapshotKeys) if (full || Object.hasOwn(row,key)) {
    field(key,row[key],value=>{
      switch (key) {
        case "workloads": result.workloads=unique(array(value,workload),row=>row.uid);break
        case "nodes": result.nodes=unique(array(value,node),row=>row.uid);break
        case "instances": result.instances=unique(array(value,instance),row=>row.id);break
        case "actions": result.actions=unique(array(value,action),liveActionKey);break
        case "routing": result.routing=map(value,value=>map(value,number));break
        case "decision": result.decision=nullable(value,decision);break
        case "reservations": result.reservations=array(value,reservation);break
      }
    })
  }
  return result
}
function cluster(value:unknown):LiveCluster {
  const row=object(value)
  if (row.kind !== "pod" && row.kind !== "issue") return invalid("kind: Expected pod or issue")
  const result:LiveCluster={kind:row.kind,at:field("at",row.at,timestamp),event:field("event",row.event,text)}
  for (const key of ["source","reason","previousReason"] as const) if (row[key] !== undefined) result[key]=field(key,row[key],text)
  if (row.pod !== undefined) {
    const pod=field("pod",row.pod,object)
    result.pod={uid:field("pod.uid",pod.uid,text)}
    for (const key of ["namespace","name","node","phase","ready"] as const) if (pod[key] !== undefined) result.pod[key]=field(`pod.${key}`,pod[key],text)
  }
  return result
}
function sequenceRange(value:unknown):LiveSequenceRange {
  const row=object(value)
  const fromSeq=field("fromSeq",row.fromSeq,count),toSeq=field("toSeq",row.toSeq,count)
  return fromSeq > 0 && fromSeq <= toSeq ? {fromSeq,toSeq} : invalid("fromSeq: Expected positive value at most toSeq")
}
function resumeHistory(value:unknown):LiveResumeHistory {
  const row=object(value)
  if (row.reason!=="fresh" && row.reason!=="checkpoint-unmatched") return invalid("reason: Expected fresh or checkpoint-unmatched")
  if (row.replayed!==false) return invalid("replayed: Expected false")
  const requested=field("requested",row.requested,object),retained=field("retained",row.retained,object)
  const result:LiveResumeHistory={reason:row.reason,replayed:false,requested:{afterSeq:field("requested.afterSeq",requested.afterSeq,count),throughSeq:field("requested.throughSeq",requested.throughSeq,count),...(requested.at===undefined?{}:{at:field("requested.at",requested.at,timestamp)})},retained:{events:field("retained.events",retained.events,value=>nullable(value,sequenceRange)),power:field("retained.power",retained.power,value=>nullable(value,sequenceRange))},gaps:field("gaps",row.gaps,value=>array(value,value=>{
    const row=object(value)
    if (row.scope!=="events" && row.scope!=="power") return invalid("scope: Expected events or power")
    if (row.reason!=="NoRetainedFrames") return invalid("reason: Expected NoRetainedFrames")
    return {...sequenceRange({fromSeq:row.fromSeq,toSeq:row.toSeq}),scope:row.scope,reason:row.reason}
  }))}
  if (Object.values(result.retained).some(range=>range && range.toSeq>result.requested.throughSeq)) return invalid("retained: Exceeds requested.throughSeq")
  if (result.gaps.some(gap=>gap.fromSeq<=result.requested.afterSeq || gap.toSeq>result.requested.throughSeq)) return invalid("gaps: Outside requested sequence range")
  return result
}
function eventPayload(row:Record<string,unknown>):LiveEvent {
  const base={type:"event" as const,at:field("at",row.at,timestamp)}
  if (row.kind==="decision" && row.action===undefined && row.cluster===undefined) return {...base,kind:"decision",decision:field("decision",row.decision,decision)}
  if (row.kind==="cluster" && row.action===undefined && row.decision===undefined) return {...base,kind:"cluster",cluster:field("cluster",row.cluster,cluster)}
  if (row.kind!=="action" || row.cluster!==undefined || row.decision!==undefined) return invalid("kind: Expected matching decision, cluster or action payload")
  const payload=field("action",row.action,object),parsed=field("action",payload,action),phase=payload.phase
  if (phase!=="started" && phase!=="completed" && phase!=="failed") return invalid("action.phase: Expected started, completed or failed")
  const result:LiveActionEvent={...parsed,phase}
  if (payload.result!==undefined) {const row=field("action.result",payload.result,object);result.result={status:field("action.result.status",row.status,text),...(row.error===undefined?{}:{error:field("action.result.error",row.error,text)})}}
  return {...base,kind:"action",action:result}
}
/** Archive rows have no live envelope and cannot advance its checkpoint. */
export function parseRecordedLiveEvent(value:unknown):LiveEvent|null {
  try {
    const row=object(value)
    if (row.type!=="event") return null
    return eventPayload(row)
  } catch {return null}
}
export function parseRecordedLivePage(value:unknown):RecordedLivePage|null {
  try {
    const row=object(value)
    const from=timestamp(row.from),to=timestamp(row.to)
    if (compareAt(from,to)>=0 || Date.parse(to)-Date.parse(from)>86_400_000) return null
    const events=array(row.events,value=>{
      const event=parseRecordedLiveEvent(value)
      return event && compareAt(event.at,from)>=0 && compareAt(event.at,to)<0 ? event : invalid()
    })
    if (events.length>100) return null
    const coverage=object(row.coverage)
    const bounds=(row:Record<string,unknown>)=>{
      const from=timestamp(row.from),to=timestamp(row.to)
      return compareAt(from,to)<=0 ? {from,to} : invalid()
    }
    const availableFrom=nullable(coverage.from,timestamp),availableTo=nullable(coverage.to,timestamp)
    if (availableFrom && availableTo && compareAt(availableFrom,availableTo)>0) return null
    const nextCursor=row.nextCursor===undefined?undefined:text(row.nextCursor)
    if (nextCursor==="") return null
    return {from,to,events,...(nextCursor===undefined?{}:{nextCursor}),coverage:{from:availableFrom,to:availableTo,sources:nullable(coverage.sources,value=>array(value,value=>{
      const row=object(value)
      return {...bounds(row),stream:text(row.stream),source:text(row.source),...(row.store===undefined?{}:{store:text(row.store)}),...(row.key===undefined?{}:{key:text(row.key)})}
    })),gaps:nullable(coverage.gaps,value=>array(value,value=>{
      const row=object(value)
      return {...bounds(row),stream:text(row.stream),reason:text(row.reason)}
    }))}}
  } catch {return null}
}
/** Reject malformed frames before they enter the authenticated Query cache. */
export function parseLiveMessage(input:string|unknown,onReject?:(reason:string)=>void):LiveMessage|null {
  let frameType="unknown"
  try {
    if (typeof input === "string") {
      if (input.length > 1<<20 || new TextEncoder().encode(input).length > 1<<20) return invalid("Frame exceeds 1 MiB")
      input=JSON.parse(input)
    }
    const row=object(input)
    if (typeof row.type === "string") frameType=row.type
    const base={seq:field("seq",row.seq,count),at:field("at",row.at,timestamp)}
    switch (row.type) {
      case "hello": {
        const retainFrom=field("retainFrom",row.retainFrom,count)
        if (row.protocol !== "chill-live/1") return invalid("protocol: Expected chill-live/1")
        if (retainFrom > base.seq+1 || retainFrom === 0) return invalid("retainFrom: Expected positive value at most seq + 1")
        return {...base,type:"hello",protocol:"chill-live/1",serverAt:field("serverAt",row.serverAt,timestamp),retainFrom}
      }
      case "snapshot": return {...base,type:"snapshot",...projection(row,true),...(row.history===undefined?{}:{history:field("history",row.history,resumeHistory)})} as LiveMessage
      case "delta": {
        const result:LiveDelta=projection(row,false)
        if (row.removed !== undefined) {
          const removed=field("removed",row.removed,object)
          result.removed={}
          if (removed.instances !== undefined) result.removed.instances=field("removed.instances",removed.instances,value=>array(value,text))
          if (removed.actions !== undefined) result.removed.actions=field("removed.actions",removed.actions,value=>array(value,value=>{
            const key=text(value),tuple=JSON.parse(key)
            if (!Array.isArray(tuple) || tuple.length !== 3 || tuple.some(value=>typeof value !== "string")) return invalid("Expected action identity tuple")
            field("startedAt",tuple[2],timestamp);return key
          }))
        }
        return {...base,type:"delta",...result}
      }
      case "metrics": {
        const bucketAt=field("bucketAt",row.bucketAt,timestamp),pool=field("pool",row.pool,object)
        if (new Date(bucketAt).getTime()%1000 || /\.[0-9]*[1-9][0-9]*Z$/.test(bucketAt)) return invalid("bucketAt: Expected whole UTC second")
        return {...base,type:"metrics",bucketAt,chill_obslog_queue_length:field("chill_obslog_queue_length",row.chill_obslog_queue_length,count),chill_obslog_oldest_wait_seconds:field("chill_obslog_oldest_wait_seconds",row.chill_obslog_oldest_wait_seconds,number),workloads:field("workloads",row.workloads,value=>map(value,metric)),nodes:field("nodes",row.nodes,value=>map(value,value=>{const node=object(value);return {watts:field("watts",node.watts,value=>nullable(value,number)),observedAt:field("observedAt",node.observedAt,value=>nullable(value,timestamp))}})),pool:{wholeWatts:field("pool.wholeWatts",pool.wholeWatts,value=>nullable(value,number)),activeWatts:field("pool.activeWatts",pool.activeWatts,value=>nullable(value,number))},...(row.gaps === undefined ? {} : {gaps:field("gaps",row.gaps,value=>array(value,gap))})}
      }
      case "event": {
        return {...eventPayload(row),...base} as Envelope & LiveEvent
      }
      case "heartbeat": return {...base,type:"heartbeat",serverAt:field("serverAt",row.serverAt,timestamp)}
      default:return invalid("type: Unknown live frame type")
    }
  } catch (error) { onReject?.(`${frameType}: ${error instanceof Error ? error.message : String(error)}`);return null }
}
export function compareAt(a:string,b:string):number {
  const normalized=(at:string)=>at.slice(0,19)+"."+(at.slice(19,-1).replace(/^\./,"")).padEnd(9,"0")
  return normalized(a).localeCompare(normalized(b))
}
function latestAt(previous:string|null,values:(string|null|undefined)[]):string|null {
  return values.reduce<string|null>((latest,at)=>at && (!latest || compareAt(at,latest)>0) ? at : latest,previous)
}
function observed(message:LiveMessage):(string|null|undefined)[] {
  if (message.type === "heartbeat" || message.type === "hello") return []
  if (message.type === "metrics") return [...(Object.keys(message.workloads).length ? [message.bucketAt] : []),...Object.values(message.nodes).map(row=>row.observedAt)]
  if (message.type === "event") return [message.kind === "decision" ? message.decision.at : message.kind === "cluster" ? message.cluster.at : message.action.completedAt ?? message.action.startedAt]
  return [...(message.workloads ?? []).map(row=>row.readiness.observedAt),...(message.nodes ?? []).map(row=>row.observedAt),...(message.instances ?? []).map(row=>row.observedAt),...(message.actions ?? []).map(row=>row.completedAt ?? row.startedAt),message.decision?.at]
}
/** Payload identity is shared by archive/live overlap and rendering; seq is transport-only. */
export function eventKey(event:LiveEvent):string {
  return JSON.stringify([event.kind,event.kind==="decision"?event.decision:event.kind==="action"?event.action:event.cluster],(_key,value)=>value && typeof value==="object" && !Array.isArray(value) ? Object.fromEntries(Object.keys(value).sort().map(key=>[key,value[key]])) : value)
}
function upsert<T>(previous:T[],rows:T[]|undefined,key:(row:T)=>string,removed:string[]=[]):T[] {
  if (!rows && !removed.length) return previous
  const result=new Map(previous.map(row=>[key(row),row]))
  for (const row of rows ?? []) result.set(key(row),row)
  for (const id of removed) result.delete(id)
  return [...result.values()]
}
function addGap(gaps:LiveGap[],row:LiveGap):LiveGap[] {
  return gaps.some(old=>old.from===row.from && old.to===row.to && old.scope===row.scope && old.reason===row.reason) ? gaps : [...gaps,row]
}
function mergeEvents(state:LiveState,rows:LiveEvent[]):Pick<LiveState,"events"|"historyGaps"> {
  const merged=new Map(state.events.map(row=>[eventKey(row),row]))
  for (const row of rows) {
    const key=eventKey(row),previous=merged.get(key)
    if (!previous || row.seq!==undefined) merged.set(key,row)
  }
  let events=[...merged.values()].sort((a,b)=>compareAt(a.at,b.at)||(a.seq??0)-(b.seq??0)),historyGaps=state.historyGaps
  if (events.length>1000) {
    const important=events.filter(row=>row.kind!=="decision"||row.decision.state!=="Keep").slice(-30)
    const protectedEvents=new Set(important),recent=new Set(events.filter(row=>!protectedEvents.has(row)).slice(-(1000-important.length)))
    const removed=events.filter(row=>!protectedEvents.has(row)&&!recent.has(row)),previous=historyGaps.find(row=>row.scope==="events" && row.reason==="event history limit")
    historyGaps=historyGaps.filter(row=>row!==previous)
    historyGaps=addGap(historyGaps,{from:previous && compareAt(previous.from,removed[0].at)<0 ? previous.from : removed[0].at,to:events[events.indexOf(removed.at(-1)!)+1].at,scope:"events",reason:"event history limit"})
    events=events.filter(row=>protectedEvents.has(row)||recent.has(row))
  }
  return {events,historyGaps}
}
export function applyRecordedLivePage(state:LiveState,page:RecordedLivePage,first:boolean):LiveState {
  const {events:rows,...archive}=page,merged=mergeEvents(state,rows),old=first?null:state.eventArchive?.coverage
  const combine=<T,>(previous:T[]|null|undefined,next:T[]|null):T[]|null=>previous?.length ? [...new Map([...previous,...next??[]].map(row=>[JSON.stringify(row),row])).values()] : next
  const coverage=old ? {from:latestCoverageFrom(old.from,archive.coverage.from),to:latestAt(old.to,[archive.coverage.to]),sources:combine(old.sources,archive.coverage.sources),gaps:combine(old.gaps,archive.coverage.gaps)} : archive.coverage
  return {...state,...merged,gaps:state.metrics.reduce((gaps,row)=>(row.gaps??[]).reduce(addGap,gaps),merged.historyGaps),eventArchive:{...archive,coverage,partial:archive.nextCursor!==undefined},eventArchiveError:null}
}
function latestCoverageFrom(a:string|null,b:string|null):string|null {return !a ? b : !b || compareAt(a,b)<0 ? a : b}
/** Messages passed here have already crossed parseLiveMessage once. */
export function applyLiveMessage(state:LiveState,message:LiveMessage,receivedAt=Date.now()):LiveState {
  if (message.type === "hello") return {...state,lastReceivedAt:receivedAt,status:"resuming"}
  const checkpoint=state.checkpoint
  if (message.type === "snapshot" && checkpoint?.seq === message.seq && checkpoint.at === message.at) return {...state,...(message.history?{resumeHistory:message.history}:{}),lastReceivedAt:receivedAt,status:"live",resyncRequired:false}
  if (message.type !== "snapshot" && checkpoint && message.seq <= checkpoint.seq) return {...state,lastReceivedAt:receivedAt}
  if (message.type !== "snapshot" && (state.resyncRequired || !checkpoint || !state.snapshot || message.seq !== checkpoint.seq+1)) return {...state,lastReceivedAt:receivedAt,status:"resuming",resyncRequired:true}
  let snapshot=state.snapshot,metrics=state.metrics,events=state.events,historyGaps=state.historyGaps,definitions=state.definitions,lastPower=state.lastPower,powerScale=state.powerScale
  if (message.type === "snapshot") {
    const {workloads,nodes,instances,routing,decision,actions,reservations}=message
    if (!message.history && checkpoint && (checkpoint.seq !== message.seq || checkpoint.at !== message.at)) {
      const from=compareAt(checkpoint.at,message.at)<=0 ? checkpoint.at : message.at,to=compareAt(checkpoint.at,message.at)<=0 ? message.at : checkpoint.at
      historyGaps=addGap(historyGaps,{from,to,scope:"all",reason:"resume history unavailable"})
    }
    snapshot={workloads,nodes,instances,routing,decision,actions,reservations}
  } else if (message.type === "delta" && snapshot) {
    snapshot={workloads:message.workloads ?? snapshot.workloads,nodes:message.nodes ?? snapshot.nodes,routing:message.routing ?? snapshot.routing,reservations:message.reservations ?? snapshot.reservations,decision:message.decision === undefined ? snapshot.decision : message.decision,instances:upsert(snapshot.instances,message.instances,row=>row.id,message.removed?.instances),actions:upsert(snapshot.actions,message.actions,liveActionKey,message.removed?.actions)}
  } else if (message.type === "metrics") {
    metrics=[...metrics.filter(row=>compareAt(row.bucketAt,message.bucketAt)!==0),message].sort((a,b)=>compareAt(a.bucketAt,b.bucketAt))
    const cutoff=new Date(metrics[metrics.length-1].bucketAt).getTime()-599_000
    metrics=metrics.filter(row=>new Date(row.bucketAt).getTime()>=cutoff).slice(-600)
    for(const node of Object.values(message.nodes))if(node.watts!=null)powerScale=Math.max(powerScale,node.watts)
    if(message.pool.wholeWatts!=null&&(!lastPower||compareAt(message.bucketAt,lastPower.bucketAt)>=0))lastPower={bucketAt:message.bucketAt,watts:message.pool.wholeWatts}
    else if(message.pool.wholeWatts==null&&lastPower?.bucketAt===message.bucketAt){
      lastPower=null
      for(let index=metrics.length-1;index>=0;index--){
        const row=metrics[index]
        if(row.pool.wholeWatts!=null){lastPower={bucketAt:row.bucketAt,watts:row.pool.wholeWatts};break}
      }
    }
  } else if (message.type === "event") {
    ({events,historyGaps}=mergeEvents(state,[message]))
  }
  if ((message.type === "snapshot" || message.type === "delta") && message.workloads) {
    definitions={...definitions}
    for (const row of message.workloads) definitions[JSON.stringify([row.uid,row.definitionID])]=row
  }
  const gaps=metrics===state.metrics && historyGaps===state.historyGaps ? state.gaps : metrics.reduce((gaps,row)=>(row.gaps ?? []).reduce(addGap,gaps),historyGaps)
  return {...state,snapshot,metrics,events,historyGaps,definitions,lastPower,powerScale,gaps,...(message.type==="snapshot" && message.history?{resumeHistory:message.history}:{}),checkpoint:{seq:message.seq,at:message.at},lastReceivedAt:receivedAt,lastObservedAt:latestAt(state.lastObservedAt,observed(message)),status:"live",resyncRequired:false}
}

export type LiveConnection = {reconnect:()=>void;close:()=>void}
/** One connection owner per authenticated provider; no token ever enters a URL. */
export function connectLive(token:string,read:()=>LiveState,update:(change:(state:LiveState)=>LiveState)=>void):LiveConnection {
  let socket:WebSocket|null=null,retry:NodeJS.Timeout|undefined,watchdog:NodeJS.Timeout|undefined,attempt=0,closed=false,blocked=false
  const status=(value:string)=>update(state=>({...state,status:value}))
  function clearTimers() { clearTimeout(retry);clearTimeout(watchdog);retry=undefined;watchdog=undefined }
  function detach() {
    if (!socket) return
    socket.onopen=null;socket.onmessage=null;socket.onclose=null;socket.onerror=null
    socket.close();socket=null
  }
  function schedule() {
    if (closed || blocked || document.visibilityState === "hidden") return
    const delay=Math.min(30_000,500*2**Math.min(attempt++,6))*(1+Math.random()*0.2)
    retry=setTimeout(open,Math.min(30_000,delay))
  }
  function touch(current:WebSocket) {
    clearTimeout(watchdog)
    watchdog=setTimeout(()=>{if (!closed && socket===current) current.close(4000,"Live receive timeout")},15_000)
  }
  function open() {
    if (closed || blocked || document.visibilityState === "hidden") return
    clearTimers();detach();status(read().snapshot ? "reconnecting" : "connecting")
    const url=new URL("/api/live",globalThis.location.href);url.protocol=url.protocol === "https:" ? "wss:" : "ws:"
    let current:WebSocket
    try { current=new WebSocket(url);socket=current } catch { status("reconnecting");schedule();return }
    touch(current)
    let hello=false,resumePending=false
    current.onopen=()=>{if (socket!==current || closed) return;current.send(JSON.stringify({type:"auth",token}));touch(current)}
    current.onmessage=event=>{
      if (socket!==current || closed || blocked) return
      let rejection="unknown: Expected JSON text frame"
      const message=typeof event.data === "string" ? parseLiveMessage(event.data,reason=>{rejection=reason}) : null
      if (!message || (!hello && message.type!=="hello") || (hello && message.type==="hello")) {
        if (message) rejection=`${message.type}: ${hello ? "Unexpected duplicate hello" : "Expected hello before other frames"}`
        blocked=true;update(state=>({...state,status:"protocol-error",protocolError:rejection}));clearTimers();current.close(4002,"Invalid live message");return
      }
      touch(current)
      if (message.type === "hello") {
        hello=true
        update(state=>applyLiveMessage(state,message))
        const checkpoint=read().checkpoint
        current.send(JSON.stringify(checkpoint && checkpoint.seq > 0 ? {type:"resume",from:checkpoint.seq,at:checkpoint.at} : {type:"resume",from:0}))
        return
      }
      update(state=>applyLiveMessage(state,message))
      const state=read()
      if (state.resyncRequired && !resumePending) { resumePending=true;current.send(JSON.stringify({type:"resume",from:0})) }
      if (message.type === "snapshot" || (!state.resyncRequired && state.status==="live")) { resumePending=false;attempt=0 }
    }
    current.onerror=()=>{ if (socket===current && !closed) current.close() }
    current.onclose=event=>{
      if (socket!==current || closed) return
      socket=null;clearTimers()
      if (event.code === 4401 || event.code === 4403) {
        blocked=true;update(()=>({...initialLiveState(),status:event.code===4401 ? "unauthorized" : "forbidden"}));return
      }
      if (blocked) return
      status("reconnecting");schedule()
    }
  }
  function visibility() {
    if (document.visibilityState === "hidden") { clearTimers();detach();if (!blocked) status("paused-hidden") }
    else if (!closed && !blocked) open()
  }
  document.addEventListener("visibilitychange",visibility)
  visibility()
  return {
    reconnect:()=>{if (!closed && !["unauthorized","forbidden"].includes(read().status)) {blocked=false;attempt=0;open()}},
    close:()=>{closed=true;clearTimers();detach();document.removeEventListener("visibilitychange",visibility)}
  }
}
