import type { LiveEvent } from "./live"

export type ActiveInjection = {id:string;action:string;offsetSeconds:number;outcome?:string;nodeName?:string;podName?:string}
export type ActiveEvaluation = {name:string;uid:string;startedAt?:string;stageIndex:number;stagesTotal:number;stage?:{id:string;mode:string;startedAt?:string;goAt?:string;plannedSeconds:number;injections:ActiveInjection[]}}
export type ActiveEvaluations = {items:ActiveEvaluation[]}
export type EvaluationPhaseSummary = {name:string;uid:string;phase:string}
export function evaluationPoolBlocked(uid:string,runs:readonly EvaluationPhaseSummary[]):boolean {
  return runs.some(run=>run.uid===uid&&run.phase==="Blocked")
}

export function evaluationRemaining(stage:ActiveEvaluation["stage"],now:number):number|null {
  if(!stage?.goAt)return null
  const end=Date.parse(stage.goAt)+stage.plannedSeconds*1000
  return Number.isFinite(end)?Math.max(0,Math.ceil((end-now)/1000)):null
}
export function evaluationInjectionMatch(event:LiveEvent,runs:readonly ActiveEvaluation[]):boolean {
  const nodeName=event.kind==="action"?event.action.node.name:event.kind==="cluster"?event.cluster.pod?.node:undefined
  const podName=event.kind==="cluster"?event.cluster.pod?.name:undefined
  return runs.some(run=>run.stage?.injections.some(injection=>!!injection.nodeName&&injection.nodeName===nodeName||!!injection.podName&&injection.podName===podName))
}
export function evaluationStopPath(run:Pick<ActiveEvaluation,"name"|"uid">):string {
  return `v1/evaluations/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid})}`
}
