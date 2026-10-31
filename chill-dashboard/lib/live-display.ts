import { isLiveActionActive, type LiveAction, type LiveDecision, type LiveEvent, type LiveInstance, type LiveMetric, type LiveMetrics, type LiveSnapshot, type LiveState, type LiveWorkload } from "./live.ts"

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
export function serviceTrafficState(metric:LiveMetric|undefined):string {
  return !metric?"관측 대기":metric.received===0?"무유입":"활동 중"
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
  return !!metric && n!=null && n>0 && metric.withinSLO/n*100<workload.slo.percentile
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
export function currentObservation(fresh:boolean,terminals:number|undefined):boolean {
  return fresh&&terminals!=null
}
export function quietReason(fresh:boolean,terminals:number|undefined,received:number|undefined,changes:number):string {
  if(!currentObservation(fresh,terminals))return "현재 상태 미확인. 마지막 관측 기준 수치이며 현재 동작을 단정하지 않아요."
  return changes?"변경을 진행하고 있어요.":received===0?"새 요청 없이 현재 구성을 유지하고 있어요.":"요청을 처리하며 현재 구성을 유지하고 있어요."
}
export function minuteMetrics(metrics:LiveMetrics[],uid:string,end:number):LiveMetric[] {
  const windowEnd=Math.floor(end/1000)*1000
  return metrics.filter(row=>Date.parse(row.bucketAt)>=windowEnd-60000&&Date.parse(row.bucketAt)<windowEnd).flatMap(row=>row.workloads[uid]?[row.workloads[uid]]:[])
}
export function boardVerdict({fresh,terminals,failed,sloMisses,queued,changes,delayed,serving,off,total,admissionBlocked,received=0,declared=serving,unserved=0}:{fresh:boolean;terminals:number|undefined;failed:number|undefined;sloMisses:number;queued:number;changes:number;delayed:number;serving:number;off:number;total:number;admissionBlocked:boolean;received?:number;declared?:number;unserved?:number}) {
  if (!currentObservation(fresh,terminals)) return {label:"데이터 끊김",tone:"unknown",reason:"최신 관측을 기다려요. 마지막 수치를 현재 상태로 판단하지 마세요"}
  if ((failed??0)>0 || admissionBlocked || unserved>0 || (!serving&&(received>0||queued>0))) return {label:"이상: 요청 처리 확인 필요",tone:"danger",reason:`실패 ${failed??0}건 / 유입 있으나 서빙 없음 ${unserved}개 / 접수 거절 ${admissionBlocked?"있음":"없음"}. 서비스 상세에서 확인하세요`}
  if (!serving && total>0 && off===total && declared===0 && received===0 && queued===0 && changes===0 && delayed===0) return {label:"정상: 계획된 정지",tone:"healthy",reason:`자동제어 꺼짐 ${off}/${total} / 선언 인스턴스 0 / 유입 0. 요청 처리 없이 대기 중이에요`}
  if (!serving) return {label:"이상: 서빙 준비 확인",tone:"warning",reason:`서빙 인스턴스 0 / 선언 ${declared}개 / 자동제어 꺼짐 ${off}/${total}. 계획된 정지 조건과 다르므로 구성·변경을 확인하세요`}
  if (sloMisses>0) return {label:"SLO 위반",tone:"danger",reason:`목표 미달 서비스 ${sloMisses}개를 확인하세요`}
  if (delayed>0) return {label:"정체",tone:"danger",reason:`예상 대비 지연 ${delayed}건. 종결 미확인으로 실제 상태 확인이 필요해요`}
  if (changes>0) return {label:"진행",tone:"warning",reason:`구성 변경 ${changes}건이 진행 중이에요`}
  if (queued>0) return {label:"주의",tone:"warning",reason:"배정 전·후 대기 요청을 확인하세요"}
  if (terminals===0) return {label:"표본 없음",tone:"unknown",reason:"종결 표본이 없어 SLO를 판정할 수 없어요"}
  return {label:"정상",tone:"healthy",reason:"종결 응답 기준으로 서비스 SLO 목표를 충족해요"}
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
  if (!metric || !metric.latencySamples) return null
  switch(workload.slo.percentile) {
    case 50:return metric.p50ms
    case 90:return metric.p90ms
    case 95:return metric.p95ms
    case 99:return metric.p99ms
    default:return undefined
  }
}
export function retainedEvents(events:LiveEvent[],now:number,keep=false):LiveEvent[] {
  return events.filter(event=>Date.parse(event.at)<=now&&(keep||event.kind!=="decision"||event.decision.state!=="Keep")).slice(-30)
}
export function idleGroups(workloads:LiveWorkload[]) {
  const groups=new Map<string,{model:string;workloads:LiveWorkload[]}>()
  for(const workload of workloads) {
    const group=groups.get(workload.model)??{model:workload.model,workloads:[]}
    group.workloads.push(workload);groups.set(workload.model,group)
  }
  return [...groups.values()].sort((a,b)=>a.model.localeCompare(b.model))
}
