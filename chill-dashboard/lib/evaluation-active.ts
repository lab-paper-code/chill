import type { LiveEvent } from "./live"

export type ActiveInjection = {id:string;action:string;offsetSeconds:number;outcome?:string;nodeName?:string;podName?:string}
export type ActiveEvaluation = {name:string;uid:string;startedAt?:string;stageIndex:number;stagesTotal:number;stage?:{id:string;startedAt?:string;goAt?:string;plannedSeconds:number;injections:ActiveInjection[]}}
export type ActiveEvaluations = {items:ActiveEvaluation[]}
export type EvaluationPhaseSummary = {name:string;uid:string;phase:string}
export type EvaluationProgressProcedure = {step:string;startedAt:string|null;finishedAt:string|null}
export type EvaluationStageProgress = {id:string;procedures?:EvaluationProgressProcedure[]|null;provisionalD1?:{asOf?:string}|null}
export function evaluationLastProgressAt(stage:EvaluationStageProgress|undefined,procedures:EvaluationProgressProcedure[]|null|undefined):number|null {
  let latest:number|null=null
  const record=(value:string|null|undefined)=>{
    const at=value?Date.parse(value):NaN
    if(Number.isFinite(at)&&(latest===null||at>latest))latest=at
  }
  for(const procedure of [...stage?.procedures??[],...procedures??[]]){
    record(procedure.startedAt)
    record(procedure.finishedAt)
  }
  record(stage?.provisionalD1?.asOf)
  return latest
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
