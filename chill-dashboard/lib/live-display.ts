import { compareAt, isLiveActionActive, type LiveAction, type LiveDecision, type LiveEvent, type LiveInstance, type LiveMetric, type LiveMetrics, type LiveSnapshot, type LiveState, type LiveWorkload } from "./live.ts"

export const instanceSymbols = [
  ["×", "제거"], ["◌", "분배 해제 뒤 잔여 실행"], ["●", "실제 처리"],
  ["◐", "분배 중 · 처리 대기"], ["○", "준비됨 · 미분배"], ["◇", "준비 중"],
] as const
export function belongsToPool(instance:LiveInstance, uid:string):boolean {
  return instance.ownerWorkloadUID===uid || instance.routable[uid]===true || (instance.running[uid]??0)>0 || (instance.waiting[uid]??0)>0
}
export function instanceSymbol(instance:LiveInstance,uid:string) {
  const running=(instance.running[uid]??0)>0,routable=instance.routable[uid]===true
  return instanceSymbols[instance.stopped?0:!routable&&running?1:routable&&running?2:routable?3:instance.ready?4:5]
}
export function ratio(numerator:number|undefined|null,denominator:number|undefined|null):string {
  return numerator==null||denominator==null?"":denominator===0?"비율 없음":`${numerator}/${denominator} (${(numerator/denominator*100).toFixed(1)}%)`
}
export function terminalCount(metric:LiveMetric|undefined):number|undefined {
  return metric ? metric.completed+metric.failed : undefined
}
export function receivedLabel(metric:LiveMetric|undefined):string {
  return !metric?"":metric.received===0?"0 req/s · 무유입":`${metric.received} req/s`
}
export function isStale(at:string|null|undefined,now:number):boolean {
  return !at || !Number.isFinite(Date.parse(at)) || now-Date.parse(at)>5000
}
export function workloadLabel(workload:LiveWorkload,instances:LiveInstance[],metric:LiveMetric|undefined,now:number):string {
  const rows=instances.filter(instance=>belongsToPool(instance,workload.uid))
  const running=rows.filter(instance=>(instance.running[workload.uid]??0)>0)
  if(running.length)return `${running.every(instance=>isStale(instance.observedAt,now))?"마지막 관측 · ":""}실제 처리 중`
  if(workload.definitionID && rows.some(instance=>instance.ready&&!instance.stopped))return "실행 준비됨"
  if(metric && metric.received>0)return "요청 받음"
  return "등록됨"
}
export function displayNumber(value:number|null|undefined,unit="",digits=0):string {
  return value==null?"":`${value.toLocaleString("ko-KR",{minimumFractionDigits:digits,maximumFractionDigits:digits})}${unit}`
}
export function displayTime(at:string|null|undefined,zone:"UTC"|"KST"):string {
  return !at||!Number.isFinite(Date.parse(at))?"":new Intl.DateTimeFormat("ko-KR",{timeZone:zone==="KST"?"Asia/Seoul":"UTC",hour:"2-digit",minute:"2-digit",second:"2-digit",hourCycle:"h23"}).format(new Date(at))+` ${zone}`
}
export function elapsed(at:string|number|null|undefined,now:number):string {
  if(at==null)return ""
  const time=typeof at==="number"?at:Date.parse(at)
  return Number.isFinite(time)?`${duration(Math.max(0,(now-time)/1000))} 전`:""
}
export function duration(seconds:number):string {
  const value=Math.max(0,Math.floor(seconds))
  if(value<60)return `${value}초`
  if(value<3600)return `${Math.floor(value/60)}분${value%60?` ${value%60}초`:""}`
  return `${Math.floor(value/3600)}시간${Math.floor(value%3600/60)?` ${Math.floor(value%3600/60)}분`:""}`
}
/** Estimates are means, not deadlines: flag after 3×; without one, use five minutes. */
export function actionProgress(action:LiveAction,now:number):"terminal"|"progress"|"delayed" {
  if(!isLiveActionActive(action))return "terminal"
  const threshold=action.expectedSeconds!=null&&action.expectedSeconds>0?action.expectedSeconds*3:300
  return now-Date.parse(action.startedAt)>threshold*1000?"delayed":"progress"
}
export const actionOrigin=(action:LiveAction)=>action.id.startsWith("manual-")||action.kind.startsWith("manual-")?"수동":"자동"
export function liveFresh(state:LiveState,now:number):boolean {
  return state.status==="fixture"||state.status==="live"&&!isStale(state.lastObservedAt,now)&&!!state.metrics.at(-1)&&!isStale(state.metrics.at(-1)?.bucketAt,now)
}
export function changeListStatus(state:LiveState,now:number) {
  const actions=state.snapshot?.actions.filter(action=>actionProgress(action,now)!=="terminal")??[],delayed=actions.filter(action=>actionProgress(action,now)==="delayed").length
  const fresh=liveFresh(state,now)
  return {fresh,title:!state.snapshot?"변경 관측을 기다려요":fresh?`변경 진행 ${actions.length-delayed}건 / 정체 ${delayed}건`:`현재 상태 미확인 · 마지막 관측 ${displayTime(state.lastObservedAt,"KST")||"미수신"}`,emptyLabel:fresh?"진행·정체 변경이 없어요.":"마지막 관측에는 진행·정체 변경이 없었어요. 현재 상태는 미확인이에요."}
}
export function scopeSnapshot(snapshot:LiveSnapshot,uid?:string):LiveSnapshot {
  if(!uid)return snapshot
  return {...snapshot,workloads:snapshot.workloads.filter(row=>row.uid===uid),instances:snapshot.instances.filter(row=>belongsToPool(row,uid)),routing:{[uid]:snapshot.routing[uid]??{}},actions:snapshot.actions.filter(row=>row.workloadUID===uid),reservations:snapshot.reservations.filter(row=>row.workloadUIDs.includes(uid)),decision:null}
}
export const scopeEvents=(events:LiveEvent[],uid?:string)=>uid?events.filter(event=>event.kind==="action"&&event.action.workloadUID===uid):events
export const recentEvents=(events:LiveEvent[],now:number)=>events.filter(event=>Date.parse(event.at)>=now-30000&&Date.parse(event.at)<=now&&(event.kind!=="decision"||event.decision.state!=="Keep"))
export function routingShare(weights:Record<string,number>|undefined,id:string):number|undefined {
  if(!weights || weights[id]==null)return undefined
  const total=Object.values(weights).reduce((sum,weight)=>sum+weight,0)
  return total>0?weights[id]/total*100:undefined
}

export function workloadQueue(workload:LiveWorkload,instances:LiveInstance[]):number {
  return workload.pending+instances.reduce((sum,row)=>sum+(row.waiting[workload.uid]??0),0)
}
export function sloTargetMissed(workload:LiveWorkload,metric:LiveMetric|undefined):boolean {
  const n=terminalCount(metric)
  return !!metric && metric.received>0 && n!=null && n>0 && metric.withinSLO/n*100<workload.slo.percentile
}
/** Smaller ranks appear first. Missing measurements are not idle. */
export function servicePriority(workload:LiveWorkload,metric:LiveMetric|undefined,changes:number,queued=workload.pending):number {
  if ((metric?.failed??0)>0 || (!workload.admission && (queued>0 || (metric?.received??0)>0 || (terminalCount(metric)??0)>0))) return 0
  if (sloTargetMissed(workload,metric)) return 1
  if (queued>0) return 2
  if (changes>0) return 3
  if (!metric || metric.received>0 || (terminalCount(metric)??0)>0) return 4
  return 5
}
export type ServiceRow = {workload:LiveWorkload;metric:LiveMetric|undefined;queued:number;changes:number;priority:number;idle:boolean;status:{label:string;tone:string;reason:string}}
export function serviceRows(snapshot:LiveSnapshot,metrics:Record<string,LiveMetric>,now:number):ServiceRow[] {
  return snapshot.workloads.map(workload=>{
    const metric=metrics[workload.uid],queued=workloadQueue(workload,snapshot.instances),actions=snapshot.actions.filter(action=>action.workloadUID===workload.uid&&actionProgress(action,now)!=="terminal"),changes=actions.length
    const priority=servicePriority(workload,metric,changes,queued)
    const idle=priority===5&&!snapshot.instances.some(row=>belongsToPool(row,workload.uid)&&!row.stopped)&&!snapshot.reservations.some(row=>row.workloadUIDs.includes(workload.uid))
    const unserved=(metric?.received??0)>0&&!snapshot.instances.some(row=>!row.stopped&&row.ready&&row.routable[workload.uid])
    const status=priority===0?{label:"이상",tone:"danger",reason:(metric?.failed??0)>0?"요청 실패":"접수 거절"}:
      sloTargetMissed(workload,metric)?{label:"이상",tone:"danger",reason:"SLO 위반"}:
      unserved?{label:"이상",tone:"danger",reason:"유입 있음 / 처리 인스턴스 없음"}:
      actions.some(action=>actionProgress(action,now)==="delayed")?{label:"주의",tone:"warning",reason:"예상 대비 지연"}:
      queued>0?{label:"주의",tone:"warning",reason:"대기 있음"}:
      changes>0?{label:"주의",tone:"warning",reason:"변경 중"}:
      !metric?{label:"주의",tone:"warning",reason:"관측 대기"}:
      metric.received===0?{label:"무부하",tone:"unknown",reason:"유입 없음"}:
      !metric.latencySamples||sloLatency(workload,metric)===null?{label:"표본 부족",tone:"warning",reason:"유입 있음 / 지연 표본 없음"}:
      sloLatency(workload,metric)===undefined?{label:"주의",tone:"warning",reason:"SLO 분위수 미제공"}:
      {label:"정상",tone:"healthy",reason:"SLO 충족"}
    return {workload,metric,queued,changes,priority,idle,status}
  }).sort((a,b)=>a.priority-b.priority||(b.metric?.received??0)-(a.metric?.received??0)||a.workload.name.localeCompare(b.workload.name))
}
/** Whole-pool watts include metered nodes absent from the live configuration. */
export function powerBreakdown(uids:string[],metrics:LiveMetrics|undefined) {
  const values=uids.map(uid=>metrics?.nodes[uid]?.watts),whole=metrics?.pool.wholeWatts
  const measured=values.every(value=>value!=null)?values.reduce<number>((sum,value)=>sum+value!,0):null
  return {whole:whole??null,measured,other:whole!=null&&measured!=null&&whole>=measured?whole-measured:null}
}
/** Latest completed reducer bucket anchors both trend and completeness across consumers. */
export function powerWindow(metrics:LiveMetrics[]) {
  const end=metrics.length?Date.parse(metrics.at(-1)!.bucketAt)+1000:0,start=Math.max(end-300000,metrics.length?Date.parse(metrics[0].bucketAt):end)
  const recent=metrics.filter(row=>Date.parse(row.bucketAt)>=start&&Date.parse(row.bucketAt)<end),nodes:Record<string,number>={}
  for(const row of recent)for(const [uid,node] of Object.entries(row.nodes))if(node.watts!=null)nodes[uid]=(nodes[uid]??0)+1
  return {end,expected:Math.max(0,Math.floor((end-start)/1000)),observed:recent.filter(row=>row.pool.wholeWatts!=null).length,nodes,segments:trendSegments(recent.map(row=>({at:Date.parse(row.bucketAt),value:row.pool.wholeWatts})),end)}
}
export function minuteMetrics(metrics:LiveMetrics[],uid:string,end:number):LiveMetric[] {
  const windowEnd=Math.floor(end/1000)*1000
  return metrics.filter(row=>Date.parse(row.bucketAt)>=windowEnd-60000&&Date.parse(row.bucketAt)<windowEnd).flatMap(row=>row.workloads[uid]?[row.workloads[uid]]:[])
}
export function boardVerdict(rows:ServiceRow[],fresh:boolean,ongoingGap:boolean,changes=0,delayed=0) {
  if(ongoingGap||!fresh)return {label:"이상",tone:"danger",reason:"관측 공백 진행"}
  const anomaly=rows.find(row=>row.status.tone==="danger")
  if(anomaly)return {label:"이상",tone:"danger",reason:anomaly.status.reason}
  if(delayed>0)return {label:"주의",tone:"warning",reason:"예상 대비 지연"}
  if(changes>0)return {label:"주의",tone:"warning",reason:"변경 중"}
  const caution=rows.find(row=>row.status.tone==="warning")
  if(caution)return {label:"주의",tone:"warning",reason:caution.status.reason}
  if(rows.some(row=>row.status.label==="정상"))return {label:"정상",tone:"healthy",reason:"SLO 충족"}
  if(rows.length&&rows.every(row=>row.metric?.received===0))return {label:"무부하",tone:"unknown",reason:"유입 없음"}
  return {label:"주의",tone:"warning",reason:"서비스 관측 대기"}
}
export function decisionReason(reason:string|null|undefined,automation:boolean[]):string {
  return automation.length>0&&automation.every(enabled=>!enabled)?"자동제어 대상 없음":liveReason(reason)
}
export function decisionText(decision:LiveDecision|null|undefined):string {
  return decision?.explanation || (decision?"판단 사유가 제공되지 않았어요":"아직 판단 기록이 없어요")
}
export function mergeKeepEvents(events:LiveEvent[]):{event:LiveEvent;count:number;from:string}[] {
  const rows:{event:LiveEvent;count:number;from:string}[]=[]
  for (const event of events) {
    const previous=rows.at(-1)
    if (event.kind==="decision" && event.decision.state==="Keep" && previous?.event.kind==="decision" && previous.event.decision.state==="Keep" && previous.event.decision.reason===event.decision.reason && previous.event.decision.explanation===event.decision.explanation) {
      previous.event=event;previous.count++
    } else rows.push({event,count:1,from:event.at})
  }
  return rows
}
/** Points use elapsed time, never stitch absent seconds into a continuous trend. */
export function trendSegments(points:{at:number;value:number|null}[],end:number):string[] {
  const visible=points.filter(row=>row.at>=end-300000 && row.at<=end),max=Math.max(1,...visible.map(row=>row.value??0))
  const segments:string[]=[]
  let previous:number|null=null
  for (const point of visible) {
    if (point.value==null) {previous=null;continue}
    const position=`${((point.at-(end-300000))/300000*120).toFixed(2)},${(28-point.value/max*24).toFixed(2)}`
    if (previous!=null && point.at-previous<=1000) segments[segments.length-1]+=` ${position}`
    else segments.push(position)
    previous=point.at
  }
  return segments
}
export function serviceDevices(snapshot:LiveSnapshot,uid:string) {
  const references=new Map(snapshot.instances.filter(row=>belongsToPool(row,uid)&&!row.stopped).map(row=>[row.node.uid,row.node]))
  return [...references.values()].map(reference=>({uid:reference.uid,name:reference.name,powerMode:snapshot.nodes.find(row=>row.uid===reference.uid)?.powerMode??""}))
}

export function sloLatency(workload:LiveWorkload,metric:LiveMetric|undefined):number|null|undefined {
  if (!metric || metric.received===0 || !metric.latencySamples) return null
  switch(workload.slo.percentile) {
    case 50:return metric.p50ms
    case 90:return metric.p90ms
    case 95:return metric.p95ms
    case 99:return metric.p99ms
    default:return undefined
  }
}
/** Merge touching half-open buckets by scope; recovery requires a later valid observation. */
export function gapEpisodes(gaps:LiveState["gaps"],now:number,metrics:LiveMetrics[]=[]) {
  const merged:LiveState["gaps"]=[]
  for(const gap of gaps.filter(row=>Date.parse(row.from)<=now).sort((a,b)=>a.scope.localeCompare(b.scope)||compareAt(a.from,b.from))) {
    const previous=merged.at(-1)
    if(previous?.scope===gap.scope&&compareAt(gap.from,previous.to)<=0) {
      if(compareAt(gap.to,previous.to)>0)previous.to=gap.to
    } else merged.push({...gap})
  }
  return merged.map(gap=>{
    const lost=gap.scope==="events"||gap.reason==="resume history unavailable"
    const recovered=!lost&&metrics.some(row=>Date.parse(row.bucketAt)>=Date.parse(gap.to)&&Date.parse(row.bucketAt)<=now&&(gap.scope.startsWith("node:")?row.nodes[gap.scope.slice(5)]?.watts!=null:gap.scope.startsWith("workload:")?!!row.workloads[gap.scope.slice(9)]:gap.scope==="workloads"?Object.keys(row.workloads).length>0:true))
    return {...gap,seconds:Math.max(0,(Date.parse(gap.to)-Date.parse(gap.from))/1000),recovered,lost}
  })
}
export function liveReason(reason:string|null|undefined):string {
  if(!reason)return "미제공"
  const labels:Record<string,string>={ExcludedNode:"제외 노드",Keep:"유지","power sample unavailable":"전력 표본 누락","resume history unavailable":"재연결 기록 공백","event history limit":"사건 보관 한도",Disabled:"자동제어 꺼짐",NotReady:"준비 안 됨",Ready:"준비됨"}
  return labels[reason]??(/[a-zA-Z]/.test(reason)?"기타 사유":reason)
}
export function retainedEvents(events:LiveEvent[],now:number,keep=false):LiveEvent[] {
  return events.filter(event=>Date.parse(event.at)<=now&&(keep||event.kind!=="decision"||event.decision.state!=="Keep")).slice(-30)
}

export function incidentFlow(events:LiveEvent[],gaps:LiveState["gaps"],now:number,keep=false,metrics:LiveMetrics[]=[]) {
  const incidents=[
    ...mergeKeepEvents(retainedEvents(events,now,keep)).map(row=>({kind:"event" as const,at:row.event.at,key:`event:${row.event.seq}`,...row})),
    ...gapEpisodes(gaps,now,metrics).filter(gap=>gap.lost||!gap.recovered||gap.seconds>=5).map(gap=>({kind:"gap" as const,at:gap.from,key:`gap:${gap.scope}:${gap.from}:${gap.reason}`,gap})),
  ]
  return incidents.sort((a,b)=>compareAt(b.at,a.at)||(b.kind==="event"?b.event.seq:0)-(a.kind==="event"?a.event.seq:0)).slice(0,30)
}
/** The badge counts only incidents present in the same rendered list. */
export function incidentSummary(events:LiveEvent[],gaps:LiveState["gaps"],now:number,metrics:LiveMetrics[]=[]) {
  const rows=incidentFlow(events,gaps,now,false,metrics)
  return {rows,newCount:rows.filter(row=>Date.parse(row.at)>=now-30000).length}
}
