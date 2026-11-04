import { parseSchedulerJSON, stringifySchedulerJSON } from "./scheduler.ts"
import { evaluationDefinition, evaluationComparisons, type EvaluationComparison, type EvaluationDefinition } from "./evaluation-definition.ts"

export type Published = Record<string, unknown>
export type ExperimentStage = {id:string;from:string;to:string;kpis:Published}
export type OutcomeAxis = {status?:string;verdict?:string;reason?:string;residuals?:{kind:string;name:string;reason:string}[]}
export type InjectionOutcome = {id:string;action?:string;outcome?:"Executed"|"Cancelled"|"Invalid";reason?:string;scheduledOffsetSeconds?:number;actualOffsetSeconds?:number}
export type ExperimentOutcome = {execution?:OutcomeAxis;measurement?:OutcomeAxis;cleanup?:OutcomeAxis;stages?:{id:string;execution?:OutcomeAxis;measurement?:OutcomeAxis;cleanup?:OutcomeAxis;injections?:InjectionOutcome[]}[]}
export type ExperimentFacts = {name?:string;question?:string;stages?:{id:string;row?:string;settingsPatchKeys?:string[]|null;loadScenarioRef?:Published}[];sloSources?:{name:string;uid:string;generation?:unknown;slo?:Published}[];loadSources?:{name:string;uid:string}[]}
export type Experiment = {schemaVersion:string;id:string;from:string;to:string;status:string;stages:ExperimentStage[];sources:{store:string;key:string}[];facts?:ExperimentFacts;definition?:EvaluationDefinition;comparisons?:EvaluationComparison[];outcome?:ExperimentOutcome}
export function decodeExperiment(text:string):Experiment {
  return readExperiment(parseSchedulerJSON(text))
}
function readExperiment(value:unknown):Experiment {
  const run=object(value)
  return {...run,definition:evaluationDefinition(run.definition),comparisons:evaluationComparisons(run.comparisons),outcome:readOutcome(run.outcome)} as Experiment
}
export function decodeExperiments(text:string):Pick<ExperimentPage,"items"|"unreadable"> {
  const list=object(parseSchedulerJSON(text))
  return {...list,items:Array.isArray(list.items)?list.items.map(readExperiment):[]}
}
export type ExperimentPage = {items:Experiment[];unreadable?:{id:string;reason:string}[]|null;page?:{offset:number;limit:number;total:number;nextOffset?:number}}
export function decodeExperimentPage(text:string):ExperimentPage {
  const list=decodeExperiments(text),page=object(object(list).page)
  if(!Object.hasOwn(list,"page"))return list
  if(!Number.isSafeInteger(page.offset)||Number(page.offset)<0||!Number.isSafeInteger(page.limit)||Number(page.limit)<1||Number(page.limit)>50||!Number.isSafeInteger(page.total)||Number(page.total)<0||list.items.length>Number(page.limit)||(page.nextOffset!==undefined&&(!Number.isSafeInteger(page.nextOffset)||Number(page.nextOffset)<=Number(page.offset))))throw new Error("실험 목록 페이지 정보 미제공")
  return {...list,page} as ExperimentPage
}
export const object = (value:unknown):Published => value && typeof value === "object" && !Array.isArray(value) ? value as Published : {}
function readOutcome(value:unknown):ExperimentOutcome|undefined {
  if(!value||typeof value!=="object"||Array.isArray(value))return undefined
  const source=object(value)
  const axes=(value:Published)=>{
    const axis=(key:string,field:"status"|"verdict")=>{
      const row=object(value[key])
      const residuals=key==="cleanup"&&Array.isArray(row.residuals)?row.residuals.flatMap(value=>{const item=object(value);return typeof item.kind==="string"&&typeof item.name==="string"&&typeof item.reason==="string"?[{kind:item.kind,name:item.name,reason:item.reason}]:[]}):undefined
      return typeof row[field]==="string"||typeof row.reason==="string"?{[field]:typeof row[field]==="string"?row[field]:undefined,reason:typeof row.reason==="string"?row.reason:undefined,...(residuals===undefined?{}:{residuals})}:undefined
    }
    return {execution:axis("execution","status"),measurement:axis("measurement","verdict"),cleanup:axis("cleanup","status")}
  }
  return {...axes(source),stages:Array.isArray(source.stages)?source.stages.flatMap(value=>{
    const stage=object(value)
    if(typeof stage.id!=="string")return []
    const injections=Array.isArray(stage.injections)?stage.injections.flatMap(value=>{
      const i=object(value)
      if(typeof i.id!=="string")return []
      return [{id:i.id,action:typeof i.action==="string"?i.action:undefined,outcome:["Executed","Cancelled","Invalid"].includes(String(i.outcome))?i.outcome as InjectionOutcome["outcome"]:undefined,reason:typeof i.reason==="string"?i.reason:undefined,scheduledOffsetSeconds:finite(i.scheduledOffsetSeconds)??undefined,actualOffsetSeconds:finite(i.actualOffsetSeconds)??undefined}]
    }):undefined
    return [{id:stage.id,...axes(stage),...(injections===undefined?{}:{injections})}]
  }):undefined}
}
export function experimentOutcome(run:Experiment,stage?:ExperimentStage) {
  const matches=stage?run.outcome?.stages?.filter(item=>item.id===stage.id):undefined
  const source=stage?(matches?.length===1?matches[0]:undefined):run.outcome
  const axis=(value:OutcomeAxis|undefined,key:"status"|"verdict")=>({value:value?.[key]??"미기록",reason:value?.reason??"미기록"})
  return {execution:axis(source?.execution,"status"),measurement:axis(source?.measurement,"verdict"),cleanup:axis(source?.cleanup,"status")}
}
export function injectionRows(run:Experiment,stage:ExperimentStage) {
  const plan=definitionStage(run,stage)?.failureInjection??[],matches=run.outcome?.stages?.filter(item=>item.id===stage.id)
  const results=matches?.length===1?matches[0].injections??[]:[]
  return [...new Set([...plan,...results].map(i=>i.id))].map(id=>{
    const planned=plan.find(i=>i.id===id),candidates=results.filter(i=>i.id===id),result=candidates.length===1?candidates[0]:undefined
    return {id,action:planned?.action??result?.action??"미기록",outcome:result?.outcome??"미기록",reason:result?.reason??"미기록",scheduledOffsetSeconds:result?.scheduledOffsetSeconds??planned?.offsetSeconds??null,actualOffsetSeconds:result?.actualOffsetSeconds??null}
  })
}
export function historicalPublicationIDs(run:Experiment):string[] {
  return [...new Set((recordedComparisons(run)??[]).flatMap(c=>[c.left,c.right].flatMap(ref=>ref.historical?[ref.historical.publicationID]:[])))]
}
export const publishedText = (value:unknown):string => value == null ? "" : typeof value === "object" ? stringifySchedulerJSON(value) : String(value)
export type PublishedMetric = {key:string;label:string;value:unknown;rawValue?:unknown;numerator:unknown;denominator:unknown;population:unknown;reason:unknown;source:string;unit?:string}


export type ExperimentSubstage = Published & {alpha:number|null}
export type ExperimentRow = {name:string;uid:unknown;definition:unknown;identitySource:unknown;missingReasons:Published;substages:ExperimentSubstage[]|null;metrics:PublishedMetric[];source:string;original:Published}
export const finite = (value:unknown):number|null => typeof value==="number"&&Number.isFinite(value)&&value>=0?value:null


export function definitionStage(run:Experiment,stage:Pick<ExperimentStage,"id">) {
  if(run.stages.filter(s=>s.id===stage.id).length!==1)return undefined
  return run.definition?.spec.stages.find(s=>s.id===stage.id)
}
export function recordedComparisons(run:Experiment):EvaluationComparison[]|undefined {
  return run.definition?run.definition.spec.comparisons??[]:run.comparisons
}


export const experimentPath=(id:string)=>`v1/experiments/${encodeURIComponent(id)}`
export function selectedExperimentStage(run:Experiment|undefined,id:string,stageID:string|null):ExperimentStage|undefined {
  if(!id||run?.id!==id)return undefined
  const stages=run.stages.filter(stage=>stage.id===stageID)
  return stages.length===1?stages[0]:undefined
}
