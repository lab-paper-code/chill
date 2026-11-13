import { isLiveActionActive, type LiveAction, type LiveActionEvent, type LiveInstance, type LiveMetric, type LiveMetrics, type LiveWorkload } from "./live.ts"

export function belongsToPool(instance:LiveInstance, uid:string):boolean {
  return instance.ownerWorkloadUID===uid || instance.routable[uid]===true || (instance.running[uid]??0)>0 || (instance.waiting[uid]??0)>0
}
export function ratio(numerator:number|undefined|null,denominator:number|undefined|null):string {
  return numerator==null||denominator==null?"":denominator===0?"비율 없음":`${numerator}/${denominator} (${(numerator/denominator*100).toFixed(1)}%)`
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
export function routingShare(weights:Record<string,number>|undefined,id:string):number|undefined {
  if(!weights || weights[id]==null)return undefined
  const total=Object.values(weights).reduce((sum,weight)=>sum+weight,0)
  return total>0?weights[id]/total*100:undefined
}

export function workloadQueue(workload:LiveWorkload,instances:LiveInstance[]):number {
  return workload.pending+instances.reduce((sum,row)=>sum+(row.waiting[workload.uid]??0),0)
}
export type ServiceRow = {workload:LiveWorkload;metric:LiveMetric|undefined;queued:number;changes:number;priority:number;idle:boolean;status:{label:string;tone:string;reason:string}}
export function minuteMetrics(metrics:LiveMetrics[],uid:string,end:number):LiveMetric[] {
  const windowEnd=Math.floor(end/1000)*1000
  return metrics.filter(row=>Date.parse(row.bucketAt)>=windowEnd-60000&&Date.parse(row.bucketAt)<windowEnd).flatMap(row=>row.workloads[uid]?[row.workloads[uid]]:[])
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
  if(reason?.includes("native preparation Pod failed"))return {reason:"실행 준비 파드 실패",check:"실행 준비 파드 종료 사유·서빙 기록 확인"}
  return {reason:liveReason(reason),check:reason?"장치·변경 원본 확인":"조치 근거 미제공"}
}
export function isMeasurementPrecheck(action:LiveActionEvent):boolean {
  return (action.result?.status??action.status)==="NOT_SUBMITTED"&&!action.result?.error
}
export function isActionFailure(action:LiveActionEvent):boolean {
  return action.phase==="failed"&&!isMeasurementPrecheck(action)
}
export function liveReason(reason:string|null|undefined):string {
  if(!reason)return "미제공"
  if(reason==="PowerModeUnavailable")return "전력모드 확인 불가"
  if(reason==="PowerModeObservationUnavailable")return "전력모드 관측 불가"
  if(reason==="DeadlineInfeasible")return "SLO 기한 내 완료 불가로 거절"
  if(reason==="QueueFull")return "대기 큐가 가득 차 거절"
  if(reason==="PowerTransitionUnavailable")return "전력 전환 근거 없음"
  if(reason==="PlacementInventoryMissing")return "배치 인벤토리 없음"
  if(reason==="Feasible")return "실행 가능"
  if(reason.includes("await fresh exact")||reason.includes("creation-outcome-unknown")||reason.includes("identity-awaiting-container")||reason.includes("dependency/Ready/handoff"))return reservationReason(reason)
  if(reason.includes("did not reach power mode"))return "전력모드 전환 시간 초과"
  if(reason.includes("Pod is not labelled as a serving instance"))return "서비스 인스턴스가 아닌 파드"
  const observedLabels:Record<string,string>={InstanceUnavailable:"인스턴스 상태 확인 불가",UnconfirmedInstance:"인스턴스 미확인",NodeUnavailable:"노드 사용 불가",PlacementBudgetUnavailable:"배치 자원 예산 확인 불가"}
  const labels:Record<string,string>={Running:"실행 중",Pending:"대기",Failed:"실패",Succeeded:"완료",Unknown:"미확인",created:"생성",observed:"관측",changed:"변경",deleted:"삭제","server-start":"서버 시작",raised:"문제 감지",resolved:"해소",ObservationUnavailable:"관측 기록 사용 불가",ShuttingDown:"종료 중",ExcludedNode:"제외 노드",Keep:"유지","power sample unavailable":"전력 표본 누락","resume history unavailable":"재연결 기록 공백","event history limit":"사건 보관 한도",Disabled:"CHILL 자동 운영 꺼짐",NotReady:"준비 안 됨",Ready:"준비됨",MissingDataset:"데이터셋 없음",MissingEvidence:"측정 근거 없음",Preparing:"배치 준비 중",RestartDrain:"재시작 후 요청 정리",AppliedDefinitionUnavailable:"적용된 서비스 정의 없음",ReservedScope:"변경 자원 예약 중",PartialObservationWindow:"관측 시간 부족",CapacityPending:"처리 용량 계산 대기",NoPlanMeetsSLO:"SLO 충족 구성 없음",EvaluatedCandidatesExceedSLOWithinReportedSearchBounds:"검토 구성 SLO 초과",MissingOperationCost:"변경 비용 근거 없음",HeldAfterOverload:"부하 증가 후 구성 유지",ReplacementUnpaid:"교체 비용 미회수",TransitionGainWithinUncertainty:"절감량 오차 범위 이내",IdleInstanceRemoved:"유휴 인스턴스 제거",ObservedOperationExecutionError:"변경 실행 오류",PreparationFailed:"배치 준비 실패"}
  return [...new Set(reason.split("; ").map(part=>{const separator=part.indexOf(":"),code=separator<0?part:part.slice(0,separator);return observedLabels[code]??labels[code]??part}))].join("; ")
}
