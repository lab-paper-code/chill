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
export type LiveMetrics = Envelope & {type:"metrics";bucketAt:string;workloads:Record<string,LiveMetric>;nodes:Record<string,{watts:number|null;observedAt:string|null}>;pool:{wholeWatts:number|null;activeWatts:number|null};gaps?:LiveGap[]}
export type LiveCluster = {kind:"pod"|"issue";at:string;event:string;pod?:{uid:string;namespace?:string;name?:string;node?:string;phase?:string;ready?:string};source?:string;reason?:string;previousReason?:string}
export type LiveActionEvent = LiveAction & {phase:"started"|"completed"|"failed";result?:{status:string;error?:string}}
export type LiveEvent = Envelope & ({type:"event";kind:"decision";decision:LiveDecision;action?:never;cluster?:never}|{type:"event";kind:"action";action:LiveActionEvent;decision?:never;cluster?:never}|{type:"event";kind:"cluster";cluster:LiveCluster;decision?:never;action?:never})
export type LiveSnapshot = {workloads:LiveWorkload[];nodes:LiveNode[];instances:LiveInstance[];routing:Record<string,Record<string,number>>;decision:LiveDecision|null;actions:LiveAction[];reservations:LiveReservation[]}
export type LiveDelta = Partial<LiveSnapshot> & {removed?:{instances?:string[];actions?:string[]}}
export type LiveMessage = (Envelope & {type:"hello";protocol:"chill-live/1";serverAt:string;retainFrom:number}) | (Envelope & {type:"snapshot"} & LiveSnapshot) | (Envelope & {type:"delta"} & LiveDelta) | LiveMetrics | LiveEvent | (Envelope & {type:"heartbeat";serverAt:string})
export type LiveState = {
  snapshot:LiveSnapshot|null;checkpoint:Envelope|null;metrics:LiveMetrics[];events:LiveEvent[];gaps:LiveGap[]
  lastReceivedAt:number|null;lastObservedAt:string|null;status:string
  definitions:Record<string,LiveWorkload>;resyncRequired:boolean;historyGaps:LiveGap[]
}
export function initialLiveState():LiveState {
  return {snapshot:null,checkpoint:null,metrics:[],events:[],gaps:[],lastReceivedAt:null,lastObservedAt:null,status:"idle",definitions:{},resyncRequired:false,historyGaps:[]}
}

function invalid():never { throw new Error("Invalid chill-live/1 message") }
function object(value:unknown):Record<string,unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value) || ![Object.prototype,null].includes(Object.getPrototypeOf(value))) return invalid()
  return value as Record<string,unknown>
}
function keys(row:Record<string,unknown>,allowed:string[]) {
  if (Object.keys(row).some(key => !allowed.includes(key))) invalid()
}
function text(value:unknown):string { return typeof value === "string" ? value : invalid() }
function boolean(value:unknown):boolean { return typeof value === "boolean" ? value : invalid() }
function number(value:unknown):number { return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : invalid() }
function count(value:unknown):number { const result=number(value); return Number.isSafeInteger(result) ? result : invalid() }
function timestamp(value:unknown):string {
  const result=text(value)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(result)) return invalid()
  const date=new Date(result)
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0,19) === result.slice(0,19) ? result : invalid()
}
function nullable<T>(value:unknown,parse:(value:unknown)=>T):T|null { return value === null ? null : parse(value) }
function array<T>(value:unknown,parse:(value:unknown)=>T):T[] { return Array.isArray(value) ? value.map(parse) : invalid() }
function map<T>(value:unknown,parse:(value:unknown)=>T):Record<string,T> { return Object.fromEntries(Object.entries(object(value)).map(([key,row])=>[key,parse(row)])) }
function unique<T>(rows:T[],key:(row:T)=>string):T[] { return new Set(rows.map(key)).size === rows.length ? rows : invalid() }
function nodeRef(value:unknown) { const row=object(value);keys(row,["uid","name"]);return {uid:text(row.uid),name:text(row.name)} }
function workload(value:unknown):LiveWorkload {
  const row=object(value);keys(row,["uid","name","model","definitionID","slo","autonomous","admission","pending","readiness"])
  const slo=object(row.slo),ready=object(row.readiness);keys(slo,["percentile","targetLatencyNanos"]);keys(ready,["ready","reason","observedAt"])
  const percentile=number(slo.percentile),targetLatencyNanos=number(slo.targetLatencyNanos)
  if (percentile > 100 || !Number.isInteger(targetLatencyNanos)) invalid()
  return {uid:text(row.uid),name:text(row.name),model:text(row.model),definitionID:nullable(row.definitionID,text),slo:{percentile,targetLatencyNanos},autonomous:boolean(row.autonomous),admission:boolean(row.admission),pending:count(row.pending),readiness:{ready:boolean(ready.ready),reason:text(ready.reason),observedAt:timestamp(ready.observedAt)}}
}
function node(value:unknown):LiveNode {
  const row=object(value);keys(row,["uid","name","powerMode","ready","measurement","reasons","observedAt"])
  return {uid:text(row.uid),name:text(row.name),powerMode:text(row.powerMode),ready:boolean(row.ready),measurement:boolean(row.measurement),...(row.reasons === undefined ? {} : {reasons:array(row.reasons,text)}),observedAt:nullable(row.observedAt,timestamp)}
}
function instance(value:unknown):LiveInstance {
  const row=object(value);keys(row,["id","node","ownerWorkloadUID","profileID","ready","stopped","routable","running","waiting","observedAt"])
  return {id:text(row.id),node:nodeRef(row.node),ownerWorkloadUID:text(row.ownerWorkloadUID),profileID:text(row.profileID),ready:boolean(row.ready),stopped:boolean(row.stopped),routable:map(row.routable === null ? {} : row.routable,boolean),running:map(row.running === null ? {} : row.running,count),waiting:map(row.waiting === null ? {} : row.waiting,count),observedAt:timestamp(row.observedAt)}
}
function decision(value:unknown):LiveDecision { const row=object(value);keys(row,["at","state","reason","explanation"]);return {at:timestamp(row.at),state:text(row.state),reason:text(row.reason),explanation:text(row.explanation)} }
export function liveActionKey(row:LiveAction):string { return JSON.stringify([row.id,row.node.uid,row.startedAt]) }
/** Shared by the live board and operating menu: history and plans are not executing changes. */
export function isLiveActionActive(row:{status:string;completedAt?:string}):boolean { return !row.completedAt && (row.status === "starting" || row.status === "running") }
function action(value:unknown,event=false):LiveAction {
  const row=object(value);keys(row,["id","kind","node","workloadUID","status","startedAt","completedAt","expectedSeconds","expectedSource",...(event ? ["phase","result"] : [])])
  let expectedSource:LiveAction["expectedSource"]=null
  if (row.expectedSource !== null) {
    const source=object(row.expectedSource);keys(source,["costID","reference","statistic","trialCount"])
    if (source.statistic !== "mean") invalid()
    object(source.reference)
    expectedSource={costID:text(source.costID),reference:source.reference,statistic:"mean",trialCount:count(source.trialCount)}
  }
  return {id:text(row.id),kind:text(row.kind),node:nodeRef(row.node),workloadUID:text(row.workloadUID),status:text(row.status),startedAt:timestamp(row.startedAt),...(row.completedAt === undefined ? {} : {completedAt:timestamp(row.completedAt)}),expectedSeconds:nullable(row.expectedSeconds,number),expectedSource}
}
function reservation(value:unknown):LiveReservation {
  const row=object(value);keys(row,["owner","nodes","workloadUIDs","podUIDs","instances","reason","explanation"])
  return {owner:text(row.owner),nodes:array(row.nodes,nodeRef),workloadUIDs:array(row.workloadUIDs === null ? [] : row.workloadUIDs,text),podUIDs:array(row.podUIDs === null ? [] : row.podUIDs,text),instances:array(row.instances === null ? [] : row.instances,text),reason:text(row.reason),explanation:text(row.explanation)}
}
function metric(value:unknown):LiveMetric {
  const row=object(value);keys(row,["received","completed","failed","withinSLO","latencySamples","p50ms","p90ms","p95ms","p99ms"])
  const result={received:count(row.received),completed:count(row.completed),failed:count(row.failed),withinSLO:count(row.withinSLO),latencySamples:count(row.latencySamples),p50ms:nullable(row.p50ms,number),p90ms:nullable(row.p90ms,number),p95ms:nullable(row.p95ms,number),p99ms:nullable(row.p99ms,number)}
  if (result.withinSLO > result.completed || result.latencySamples > result.completed + result.failed) invalid()
  return result
}
function gap(value:unknown):LiveGap { const row=object(value);keys(row,["from","to","scope","reason"]);const result={from:timestamp(row.from),to:timestamp(row.to),scope:text(row.scope),reason:text(row.reason)};return compareAt(result.from,result.to) <= 0 ? result : invalid() }
const snapshotKeys=["workloads","nodes","instances","routing","decision","actions","reservations"]
function projection(row:Record<string,unknown>,full:boolean):LiveDelta {
  const result:LiveDelta={}
  for (const key of snapshotKeys) if (full || Object.hasOwn(row,key)) {
    switch (key) {
      case "workloads": result.workloads=unique(array(row.workloads,workload),row=>row.uid);break
      case "nodes": result.nodes=unique(array(row.nodes,node),row=>row.uid);break
      case "instances": result.instances=unique(array(row.instances,instance),row=>row.id);break
      case "actions": result.actions=unique(array(row.actions,value=>action(value)),liveActionKey);break
      case "routing": result.routing=map(row.routing,value=>map(value,number));break
      case "decision": result.decision=nullable(row.decision,decision);break
      case "reservations": result.reservations=array(row.reservations,reservation);break
    }
  }
  return result
}
function cluster(value:unknown):LiveCluster {
  const row=object(value);keys(row,["kind","at","event","pod","source","reason","previousReason"])
  if (row.kind !== "pod" && row.kind !== "issue") return invalid()
  const result:LiveCluster={kind:row.kind,at:timestamp(row.at),event:text(row.event)}
  for (const key of ["source","reason","previousReason"] as const) if (row[key] !== undefined) result[key]=text(row[key])
  if (row.pod !== undefined) {
    const pod=object(row.pod);keys(pod,["uid","namespace","name","node","phase","ready"])
    result.pod={uid:text(pod.uid)}
    for (const key of ["namespace","name","node","phase","ready"] as const) if (pod[key] !== undefined) result.pod[key]=text(pod[key])
  }
  return result
}
/** Reject malformed frames before they enter the authenticated Query cache. */
export function parseLiveMessage(input:string|unknown):LiveMessage|null {
  try {
    if (typeof input === "string") {
      if (input.length > 1<<20 || new TextEncoder().encode(input).length > 1<<20) return null
      input=JSON.parse(input)
    }
    const row=object(input),base={seq:count(row.seq),at:timestamp(row.at)}
    const allowed=["type","seq","at"]
    switch (row.type) {
      case "hello": {
        keys(row,[...allowed,"protocol","serverAt","retainFrom"])
        const retainFrom=count(row.retainFrom)
        if (row.protocol !== "chill-live/1" || retainFrom > base.seq+1 || retainFrom === 0) return null
        return {...base,type:"hello",protocol:"chill-live/1",serverAt:timestamp(row.serverAt),retainFrom}
      }
      case "snapshot": keys(row,[...allowed,...snapshotKeys]);return {...base,type:"snapshot",...projection(row,true)} as LiveMessage
      case "delta": {
        keys(row,[...allowed,...snapshotKeys,"removed"])
        const result:LiveDelta=projection(row,false)
        if (row.removed !== undefined) {
          const removed=object(row.removed);keys(removed,["instances","actions"])
          result.removed={}
          if (removed.instances !== undefined) result.removed.instances=array(removed.instances,text)
          if (removed.actions !== undefined) result.removed.actions=array(removed.actions,value=>{
            const key=text(value),tuple=JSON.parse(key)
            if (!Array.isArray(tuple) || tuple.length !== 3 || tuple.some(value=>typeof value !== "string")) return invalid()
            timestamp(tuple[2]);return key
          })
        }
        return {...base,type:"delta",...result}
      }
      case "metrics": {
        keys(row,[...allowed,"bucketAt","workloads","nodes","pool","gaps"])
        const bucketAt=timestamp(row.bucketAt),pool=object(row.pool);keys(pool,["wholeWatts","activeWatts"])
        if (new Date(bucketAt).getTime()%1000 || /\.[0-9]*[1-9][0-9]*Z$/.test(bucketAt)) return null
        return {...base,type:"metrics",bucketAt,workloads:map(row.workloads,metric),nodes:map(row.nodes,value=>{const node=object(value);keys(node,["watts","observedAt"]);return {watts:nullable(node.watts,number),observedAt:nullable(node.observedAt,timestamp)}}),pool:{wholeWatts:nullable(pool.wholeWatts,number),activeWatts:nullable(pool.activeWatts,number)},...(row.gaps === undefined ? {} : {gaps:array(row.gaps,gap)})}
      }
      case "event": {
        keys(row,[...allowed,"kind","decision","action","cluster"])
        if (row.kind === "decision" && row.action === undefined && row.cluster === undefined) return {...base,type:"event",kind:"decision",decision:decision(row.decision)}
        if (row.kind === "cluster" && row.action === undefined && row.decision === undefined) return {...base,type:"event",kind:"cluster",cluster:cluster(row.cluster)}
        if (row.kind !== "action" || row.cluster !== undefined || row.decision !== undefined) return null
        const payload=object(row.action),parsed=action(payload,true),phase=payload.phase
        if (phase !== "started" && phase !== "completed" && phase !== "failed") return null
        const result:LiveActionEvent={...parsed,phase}
        if (payload.result !== undefined) { const row=object(payload.result);keys(row,["status","error"]);result.result={status:text(row.status),...(row.error === undefined ? {} : {error:text(row.error)})} }
        return {...base,type:"event",kind:"action",action:result}
      }
      case "heartbeat": keys(row,[...allowed,"serverAt"]);return {...base,type:"heartbeat",serverAt:timestamp(row.serverAt)}
      default:return null
    }
  } catch { return null }
}
function compareAt(a:string,b:string):number {
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
function eventKey(event:LiveEvent):string {
  return event.kind === "decision" ? JSON.stringify(["decision",event.decision.at]) : event.kind === "action" ? JSON.stringify(["action",liveActionKey(event.action),event.action.phase]) : JSON.stringify(["cluster",event.cluster])
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
/** Messages passed here have already crossed parseLiveMessage once. */
export function applyLiveMessage(state:LiveState,message:LiveMessage,receivedAt=Date.now()):LiveState {
  if (message.type === "hello") return {...state,lastReceivedAt:receivedAt,status:"resuming"}
  const checkpoint=state.checkpoint
  if (message.type === "snapshot" && checkpoint?.seq === message.seq && checkpoint.at === message.at) return {...state,lastReceivedAt:receivedAt,status:"live",resyncRequired:false}
  if (message.type !== "snapshot" && checkpoint && message.seq <= checkpoint.seq) return {...state,lastReceivedAt:receivedAt}
  if (message.type !== "snapshot" && (state.resyncRequired || !checkpoint || !state.snapshot || message.seq !== checkpoint.seq+1)) return {...state,lastReceivedAt:receivedAt,status:"resuming",resyncRequired:true}
  let snapshot=state.snapshot,metrics=state.metrics,events=state.events,historyGaps=state.historyGaps,definitions=state.definitions
  if (message.type === "snapshot") {
    const {workloads,nodes,instances,routing,decision,actions,reservations}=message
    if (checkpoint && (checkpoint.seq !== message.seq || checkpoint.at !== message.at)) {
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
  } else if (message.type === "event" && !events.some(row=>eventKey(row)===eventKey(message))) {
    events=[...events,message]
    if (events.length > 1000) {
      const important=events.filter(row=>row.kind!=="decision"||row.decision.state!=="Keep").slice(-30)
      const protectedEvents=new Set(important)
      const recent=new Set(events.filter(row=>!protectedEvents.has(row)).slice(-(1000-important.length)))
      const removed=events.filter(row=>!protectedEvents.has(row)&&!recent.has(row)),previous=historyGaps.find(row=>row.scope==="events" && row.reason==="event history limit")
      historyGaps=historyGaps.filter(row=>row!==previous)
      historyGaps=addGap(historyGaps,{from:previous?.from ?? removed[0].at,to:events[events.indexOf(removed.at(-1)!)+1].at,scope:"events",reason:"event history limit"})
      events=events.filter(row=>protectedEvents.has(row)||recent.has(row))
    }
  }
  if ((message.type === "snapshot" || message.type === "delta") && message.workloads) {
    definitions={...definitions}
    for (const row of message.workloads) definitions[JSON.stringify([row.uid,row.definitionID])]=row
  }
  const gaps=metrics===state.metrics && historyGaps===state.historyGaps ? state.gaps : metrics.reduce((gaps,row)=>(row.gaps ?? []).reduce(addGap,gaps),historyGaps)
  return {...state,snapshot,metrics,events,historyGaps,definitions,gaps,checkpoint:{seq:message.seq,at:message.at},lastReceivedAt:receivedAt,lastObservedAt:latestAt(state.lastObservedAt,observed(message)),status:"live",resyncRequired:false}
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
      const message=typeof event.data === "string" ? parseLiveMessage(event.data) : null
      if (!message || (!hello && message.type!=="hello") || (hello && message.type==="hello")) {
        blocked=true;status("protocol-error");clearTimers();current.close(1002,"Invalid live message");return
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
