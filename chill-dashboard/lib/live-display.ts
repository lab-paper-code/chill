import type { LiveInstance, LiveMetric, LiveWorkload } from "./live.ts"

export const instanceSymbols = [
  ["×", "제거"], ["◌", "분배 해제 뒤 잔여 실행"], ["●", "실제 처리"],
  ["◐", "분배 중 · 처리 대기"], ["○", "준비됨 · 미분배"], ["◇", "준비 중"],
] as const
export function belongsToPool(instance:LiveInstance, uid:string):boolean {
  return instance.ownerWorkloadUID===uid || instance.routable[uid]===true || (instance.running[uid]??0)>0
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
