

export type ActiveInjection = {id:string;action:string;offsetSeconds:number;outcome?:string;nodeName?:string;podName?:string}
export type ActiveEvaluation = {name:string;uid:string;startedAt?:string;stageIndex:number;stagesTotal:number;stage?:{id:string;startedAt?:string;goAt?:string;plannedSeconds:number;injections:ActiveInjection[]}}
export type ActiveEvaluations = {items:ActiveEvaluation[]}
export type EvaluationProgressEvent = {phase:"loadStarted"|"loadEnded"|"drainStarted"|"drainEnded"|"flushStarted"|"collectStarted"|"collectEnded"|"judgeStarted"|"judgeEnded";at:string;producerRowsMissing?:Record<string,number>;bytes?:number|bigint}
export function evaluationRemaining(stage:ActiveEvaluation["stage"],now:number):number|null {
  if(!stage?.goAt)return null
  const end=Date.parse(stage.goAt)+stage.plannedSeconds*1000
  return Number.isFinite(end)?Math.max(0,Math.ceil((end-now)/1000)):null
}
