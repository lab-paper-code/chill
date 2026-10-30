import type { LiveDecision, LiveEvent, LiveInstance, LiveMetric, LiveSnapshot, LiveWorkload } from "./live.ts"

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
  return Number.isFinite(time)?`${Math.max(0,Math.floor((now-time)/1000))}초 전`:""
}
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
export function boardVerdict(fresh:boolean,terminals:number|undefined,failed:number|undefined,sloMisses:number|undefined,queued:number,changes:number,admissionBlocked:boolean) {
  if (!fresh || terminals==null) return {label:"판정 불가",tone:"unknown",reason:"최신 관측을 기다리고 있어요"}
  if ((failed??0)>0 || admissionBlocked) return {label:"이상",tone:"danger",reason:"실패 응답 또는 요청 접수 불가를 확인하세요"}
  if ((sloMisses??0)>0 || queued>0 || changes>0) return {label:"주의",tone:"warning",reason:sloMisses?"SLO 목표 미달 서비스가 있어요":queued?"처리를 기다리는 요청이 있어요":"구성 변경이 진행 중이에요"}
  if (terminals===0) return {label:"판정 불가",tone:"unknown",reason:"종결 표본이 없어 SLO를 판정할 수 없어요"}
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
