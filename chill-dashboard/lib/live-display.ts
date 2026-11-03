import { compareAt, eventKey, isLiveActionActive, type LiveAction, type LiveActionEvent, type LiveEvent, type LiveInstance, type LiveMetric, type LiveMetrics, type LiveSnapshot, type LiveState, type LiveWorkload } from "./live.ts"
import { STATUS_AXES, type StatusValue } from "./status.ts"
import type { SchedulerCandidate } from "./read-types.ts"

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
export function workloadReadiness(workload:LiveWorkload,instances:LiveInstance[]):string {
  return workload.readiness.reason==="Preparing"&&!workload.autonomous&&!instances.some(row=>belongsToPool(row,workload.uid)&&!row.stopped)?"미배치 · 자동제어 꺼짐":workload.readiness.ready?"준비됨":liveReason(workload.readiness.reason)
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
export function minuteMetrics(metrics:LiveMetrics[],uid:string,end:number):LiveMetric[] {
  const windowEnd=Math.floor(end/1000)*1000
  return metrics.filter(row=>Date.parse(row.bucketAt)>=windowEnd-60000&&Date.parse(row.bucketAt)<windowEnd).flatMap(row=>row.workloads[uid]?[row.workloads[uid]]:[])
}
export function boardVerdict(rows:ServiceRow[],fresh:boolean,ongoingGap:boolean,changes=0,delayed=0) {
  const observation:StatusValue={axis:STATUS_AXES.observation,
    label:ongoingGap?"관측 공백":fresh?"최신":"오래된 관측",
    tone:ongoingGap||!fresh?"warn":"ok",
    reason:ongoingGap?"관측 공백 진행":fresh?undefined:"관측 신선도 부족"}
  const impact=()=>{
    if(!fresh)return {label:"미확인",tone:"unknown",reason:"현재 서비스 영향 미확인"}
    const anomaly=rows.find(row=>row.status.tone==="danger")
    if(anomaly)return {label:"이상",tone:"danger",reason:anomaly.status.reason}
    if(delayed>0)return {label:"주의",tone:"warning",reason:"예상 대비 지연"}
    if(changes>0)return {label:"주의",tone:"warning",reason:"변경 중"}
    const caution=rows.find(row=>row.status.tone==="warning")
    if(caution)return {label:"주의",tone:"warning",reason:caution.status.reason}
    if(rows.some(row=>row.status.label==="정상"))return {label:"정상",tone:"healthy",reason:"SLO 충족"}
    if(rows.length&&rows.every(row=>row.metric?.received===0))return {label:"정상 · 무부하",tone:"healthy",reason:"유입 없음"}
    return {label:"주의",tone:"warning",reason:"서비스 관측 대기"}
  }
  return {axis:STATUS_AXES.serviceImpact,...impact(),observation}
}
export function decisionReason(reason:string|null|undefined,automation:boolean[]):string {
  return automation.length>0&&automation.every(enabled=>!enabled)?"자동제어 대상 없음":liveReason(reason)
}
export function candidatePlans(keep:SchedulerCandidate,target:SchedulerCandidate|null):SchedulerCandidate[] {
  return target&&target.id!==keep.id?[keep,target]:[keep]
}
export function candidateEvaluation(candidate:SchedulerCandidate):string {
  return candidate.slo?.length?`평가 대상 ${candidate.slo.length}개`:"평가 대상 없음"
}
/** Retained, distinct server judgments only; no estimate of unseen decisions. */
export function decisionActivity(events:LiveEvent[],now:number) {
  const decisions=new Map(events.filter(event=>event.kind==="decision"&&Date.parse(event.at)>=now-300000&&Date.parse(event.at)<=now).map(event=>[event.decision!.at,event.decision!]))
  const rows=[...decisions.values()].sort((a,b)=>compareAt(a.at,b.at))
  return {count:rows.length,kept:rows.filter(row=>row.state==="Keep").length,lastAt:rows.at(-1)?.at??null}
}
export function retentionNotice(state:LiveState,zone:"UTC"|"KST"):string {
  const gaps=state.eventArchive?.coverage.gaps??[],hasGap=!!(gaps.length||state.resumeHistory?.gaps.length)
  const ranges=[...new Set(gaps.map(gap=>`${displayTime(gap.from,zone)}–${displayTime(gap.to,zone)}`))]
  if(ranges.length&&state.resumeHistory?.gaps.length)ranges.push("재연결 기간 미제공")
  return [hasGap?`보존 공백 · ${ranges.join(" / ")||"기간 미제공"}`:null,state.eventArchive?.partial?"최근 사건 일부만 표시":null].filter(Boolean).join(" · ")
}
export function clusterResult(events:LiveEvent[]):string {
  const rows=events.filter(event=>event.kind==="cluster").sort((a,b)=>compareAt(a.at,b.at)),first=rows[0]?.cluster,last=rows.at(-1)?.cluster
  const ready=(value:string|undefined)=>value==="True"?"예":value==="False"?"아니오":"미확인"
  return first?.pod?.ready&&last?.pod?.ready&&first.pod.ready!==last.pod.ready?`준비 ${ready(first.pod.ready)} → ${ready(last.pod.ready)} (${duration((Date.parse(rows.at(-1)!.at)-Date.parse(rows[0].at))/1000)})`:
    `${liveReason(last?.pod?.phase)}${last?.pod?.ready?` · 준비 ${ready(last.pod.ready)}`:""}`
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
export function incidentHistoryHref(uid?:string,at?:string,node?:string,identity?:string):string {
  const end=at?Date.parse(at):Date.now(),range={from:new Date(end-300000).toISOString(),to:new Date(end+(at?300000:0)).toISOString()}
  return `/records?${new URLSearchParams({...range,order:"asc",filter:"decision-action",...(uid?{uid}:{}),...(node?{filterNode:node}:{}),...(at?{eventAt:at}:{}),...(identity?{open:`event:${identity}`}:{})})}`
}
export function reservationReason(reason:string):string {
  const labels=[
    ["await fresh exact ACK/untouched/absence proof","장치 반환·실행 종료·전력 안정 확인 대기"],
    ["creation-outcome-unknown","생성 결과 확인 대기"],
    ["identity-awaiting-container","동일 실행기 식별 확인 대기"],
    ["dependency/Ready/handoff proof missing","선행 작업·실행 준비·반환 확인 대기"],
  ]
  const matched=labels.filter(([code])=>reason.includes(code)).map(([,label])=>label)
  return matched.length?[...new Set(matched)].join(" / "):liveReason(reason)
}
export function incidentDiagnosis(reason:string|null|undefined) {
  const power=/did not reach power mode (\S+) within (\S+)/.exec(reason??"")
  if(power)return {reason:"전력모드 전환 시간 초과",mode:power[1],check:"장치 전원·전력모드 제어기 확인"}
  if(reason?.includes("Pod is not labelled as a serving instance"))return {reason:"서비스 인스턴스가 아닌 파드",check:"파드 역할·측정 작업 확인"}
  return {reason:liveReason(reason),check:reason?"장치·변경 원본 확인":"조치 근거 미제공"}
}
export function isMeasurementPrecheck(action:LiveActionEvent):boolean {
  return (action.result?.status??action.status)==="NOT_SUBMITTED"&&!action.result?.error
}
export function isActionFailure(action:LiveActionEvent):boolean {
  return action.phase==="failed"&&!isMeasurementPrecheck(action)
}
export function actionEventResult(action:LiveActionEvent):string {
  return isMeasurementPrecheck(action)?"사전 검사 · 미제출":action.phase==="started"?"시작":action.phase==="completed"?"완료":"실패"
}
export function groupIncidentEvents(events:LiveEvent[]) {
  const rows:{event:LiveEvent;count:number;from:string;members:LiveEvent[]}[]=[],failures=new Map<string,typeof rows[number]>()
  for(const event of events){
    const key=event.kind==="action"&&isActionFailure(event.action)?JSON.stringify([event.action.node.uid,event.action.workloadUID,event.action.kind,event.action.phase,event.action.result?.status,event.action.result?.error]):
      event.kind==="cluster"?JSON.stringify([event.cluster.kind,event.cluster.reason?.includes("Pod is not labelled as a serving instance")?"nonserving-pods":event.cluster.pod?.uid??event.cluster.source,event.cluster.event,event.cluster.pod?.phase,event.cluster.reason]):null
    const previous=key?failures.get(key):undefined
    if(previous){previous.event=event;previous.count++;previous.members.push(event)}
    else {const row={event,count:1,from:event.at,members:[event]};rows.push(row);if(key)failures.set(key,row)}
  }
  return rows
}
export function deviceReasons(reasons:string[],powerMode:string):string[] {
  const detailed=reasons.includes("PowerModeObservationUnavailable")||reasons.includes("PowerTransitionUnavailable")
  return reasons.filter(reason=>reason==="PowerModeUnavailable"?!detailed&&powerMode.toLowerCase()!=="fixed":
    reason==="PowerModeObservationUnavailable"||reason==="PowerTransitionUnavailable"?powerMode.toLowerCase()!=="fixed":true)
}
export function deviceAlerts(snapshot:LiveSnapshot|null,events:LiveEvent[]) {
  const alerts=new Map<string,{uid:string;name:string;reason:string}>()
  for(const node of snapshot?.nodes??[]) {
    const reasons=deviceReasons(node.reasons??[],node.powerMode)
    if(!node.ready||reasons.length)alerts.set(node.uid,{uid:node.uid,name:node.name,reason:reasons.length?reasons.map(liveReason).join(" / "):"준비 미확인"})
  }
  const latest=new Map<string,LiveEvent>()
  for(const event of events)if(event.kind==="action"&&event.action.node.uid&&event.action.status!=="validated"&&!isMeasurementPrecheck(event.action))latest.set(JSON.stringify([event.action.node.uid,event.action.kind]),event)
  for(const event of latest.values())if(event.kind==="action"&&isActionFailure(event.action))alerts.set(event.action.node.uid,{...event.action.node,reason:incidentDiagnosis(event.action.result?.error).reason})
  return [...alerts.values()]
}
export function liveReason(reason:string|null|undefined):string {
  if(!reason)return "미제공"
  if(reason==="PowerModeUnavailable")return "전력모드 확인 불가"
  if(reason==="PowerModeObservationUnavailable")return "전력모드 관측 불가"
  if(reason==="PowerTransitionUnavailable")return "전력 전환 근거 없음"
  if(reason==="PlacementInventoryMissing")return "배치 인벤토리 없음"
  if(reason==="Feasible")return "실행 가능"
  if(reason.includes("await fresh exact")||reason.includes("creation-outcome-unknown")||reason.includes("identity-awaiting-container")||reason.includes("dependency/Ready/handoff"))return reservationReason(reason)
  if(reason.includes("did not reach power mode"))return "전력모드 전환 시간 초과"
  if(reason.includes("Pod is not labelled as a serving instance"))return "서비스 인스턴스가 아닌 파드"
  const labels:Record<string,string>={Running:"실행 중",Pending:"대기",Failed:"실패",Succeeded:"완료",Unknown:"미확인",created:"생성",observed:"관측",changed:"변경",deleted:"삭제","server-start":"서버 시작",raised:"문제 감지",resolved:"해소",ObservationUnavailable:"관측 기록 사용 불가",ShuttingDown:"종료 중",ExcludedNode:"제외 노드",Keep:"유지","power sample unavailable":"전력 표본 누락","resume history unavailable":"재연결 기록 공백","event history limit":"사건 보관 한도",Disabled:"자동제어 꺼짐",NotReady:"준비 안 됨",Ready:"준비됨",MissingDataset:"데이터셋 없음",MissingEvidence:"측정 근거 없음",Preparing:"배치 준비 중",RestartDrain:"재시작 후 요청 정리",AppliedDefinitionUnavailable:"적용된 서비스 정의 없음",ReservedScope:"변경 자원 예약 중",PartialObservationWindow:"관측 시간 부족",BootstrapCapacityPending:"초기 처리 용량 계산 대기",CapacityPending:"처리 용량 계산 대기",NoPlanMeetsSLO:"SLO 충족 구성 없음",EvaluatedCandidatesExceedSLOWithinReportedSearchBounds:"검토 구성 SLO 초과",MissingOperationCost:"변경 비용 근거 없음",HeldAfterOverload:"부하 증가 후 구성 유지",DemandIncreasing:"유입 증가",ReplacementUnpaid:"교체 비용 미회수",ReplacementDwell:"교체 후 최소 유지",TransitionGainWithinUncertainty:"절감량 오차 범위 이내",IdleInstanceRemoved:"유휴 인스턴스 제거",ObservedOperationExecutionError:"변경 실행 오류",PreparationFailed:"배치 준비 실패"}
  return reason.split("; ").map(part=>{const separator=part.indexOf(":"),code=separator<0?part:part.slice(0,separator);return labels[code]??(/[a-z]/i.test(part)?"상세 사유 원본 참조":part)}).join("; ")
}

export function incidentFlow(events:LiveEvent[],gaps:LiveState["gaps"],now:number,keep=false,metrics:LiveMetrics[]=[]) {
  const incidents=[
    ...groupIncidentEvents(events.filter(event=>Date.parse(event.at)<=now&&(keep||event.kind!=="decision"||event.decision.state!=="Keep"))).map(row=>({kind:"event" as const,at:row.event.at,key:`event:${row.event.seq??eventKey(row.event)}`,...row})),
    ...gapEpisodes(gaps,now,metrics).filter(gap=>gap.lost||!gap.recovered||gap.seconds>=5).map(gap=>({kind:"gap" as const,at:gap.from,key:`gap:${gap.scope}:${gap.from}:${gap.reason}`,gap})),
  ]
  return incidents.sort((a,b)=>compareAt(b.at,a.at)||(b.kind==="event"?b.event.seq??0:0)-(a.kind==="event"?a.event.seq??0:0)).slice(0,30)
}
/** The badge counts only incidents present in the same rendered list. */
export function incidentSummary(events:LiveEvent[],gaps:LiveState["gaps"],now:number,metrics:LiveMetrics[]=[]) {
  const rows=incidentFlow(events,gaps,now,false,metrics)
  return {rows,newCount:rows.filter(row=>Date.parse(row.at)>=now-30000).length}
}
