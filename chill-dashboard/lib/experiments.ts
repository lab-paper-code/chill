import type { SchedulerObservation, SchedulerObservationPage } from "./read-types.ts"
import { parseSchedulerJSON, stringifySchedulerJSON } from "./scheduler.ts"
import { evaluationDefinition, evaluationComparisons, type EvaluationComparison, type EvaluationDefinition, type ComparisonReference } from "./evaluation-definition.ts"
import { formatMeasurementNumber } from "./measurements.ts"

export type Published = Record<string, unknown>
export type ExperimentStage = {id:string;from:string;to:string;kpis:Published}
export type OutcomeAxis = {status?:string;verdict?:string;reason?:string;residuals?:{kind:string;name:string;reason:string}[]}
export type InjectionOutcome = {id:string;action?:string;outcome?:"Executed"|"Cancelled"|"Invalid";reason?:string;scheduledOffsetSeconds?:number;actualOffsetSeconds?:number}
export type ExperimentOutcome = {execution?:OutcomeAxis;measurement?:OutcomeAxis;cleanup?:OutcomeAxis;stages?:{id:string;execution?:OutcomeAxis;measurement?:OutcomeAxis;cleanup?:OutcomeAxis;injections?:InjectionOutcome[]}[]}
export type Experiment = {schemaVersion:string;id:string;from:string;to:string;status:string;stages:ExperimentStage[];sources:{store:string;key:string}[];definition?:EvaluationDefinition;comparisons?:EvaluationComparison[];outcome?:ExperimentOutcome}
export function decodeExperiment(text:string):Experiment {
  return readExperiment(parseSchedulerJSON(text))
}
function readExperiment(value:unknown):Experiment {
  const run=object(value)
  return {...run,definition:evaluationDefinition(run.definition),comparisons:evaluationComparisons(run.comparisons),outcome:readOutcome(run.outcome)} as Experiment
}
export function decodeExperiments(text:string):{items:Experiment[]} {
  const list=object(parseSchedulerJSON(text))
  return {...list,items:Array.isArray(list.items)?list.items.map(readExperiment):[]}
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

// Exactly the two publisher shapes. No current registration or live terminal buckets enter these rows.
export function experimentRows(stage:ExperimentStage):ExperimentRow[] {
  const k=stage.kpis
  return (["workloads","requests"] as const).flatMap(shape=>Object.entries(object(k[shape])).map(([name,value])=>{
    const row=object(value),missing=object(row.missingReasons),base=`kpis.${shape}.${name}`
    const metrics:PublishedMetric[]=[]
    const add=(key:string,label:string,value:unknown,numerator:unknown=null,denominator:unknown=null,reasonKey=key,unit?:string)=>metrics.push({key,label,value,numerator,denominator,population:row.population,reason:missing[reasonKey],source:`${base}.${key}`,unit})
    add("offered","생성기 offered",row.offered)
    if(shape==="workloads") {
      add("httpSuccess","생성기 HTTP 성공",row.httpSuccess)
      add("successFraction","HTTP 성공 / offered",row.successFraction,row.successNumerator,row.successDenominator)
      add("withinSLOFraction","SLO 안 / offered",row.withinSLOFraction,row.withinSLONumerator,row.withinSLODenominator)
    } else {
      add("deadlineSuccessFraction","기한 안 성공 / offered",row.deadlineSuccessFraction,row.successNumerator,row.successDenominator)
      add("actualGatewayIdentities","생성기 응답의 서버 identity",row.actualGatewayIdentities)
      add("rawPopulation.uniqueRequests","서버 수신 cohort (기록된 고유 요청)",object(row.rawPopulation).uniqueRequests)
      add("rawPopulation.complete","서버 수신 cohort 보존 완전성",object(row.rawPopulation).complete)
      add("offeredResponsePopulation.uniqueFinalRows","offered 응답의 서버 final 기록",object(row.offeredResponsePopulation).uniqueFinalRows)
      add("D1","게시된 요청 판정",row.D1)
    }
    const latencyKey=shape==="workloads"?"latencyMS":"completedLatencyNS",latency=object(row[latencyKey])
    for(const percentile of ["p50","p95","p99"])add(`${latencyKey}.${percentile}`,`성공 응답 지연 ${percentile}`,latency[percentile],null,latency.samples,latencyKey,shape==="workloads"?"ms":"ns")
    add("outcomes","생성기 결과별 수",row.outcomes)
    const fidelity=object(row.issuanceFidelity)
    add("issuanceFidelity","생성기 발행 충실도",fidelity.status,fidelity.supplied,fidelity.offered,"issuanceFidelity")
    // The map key is the publisher's recorded name, not a lookup key into today's Workloads.
    return {name,uid:Object.hasOwn(row,"workloadUID")?row.workloadUID:row.uid,definition:Object.hasOwn(row,"definitionID")?row.definitionID:row.definition,identitySource:row.identitySource,missingReasons:missing,substages:Array.isArray(row.substages)?row.substages.map(value=>{const part=object(value);return {...part,alpha:finite(part.alpha)}}):null,metrics,source:base,original:row}
  }))
}

export type ExperimentSubstage = Published & {alpha:number|null}
export type ExperimentRow = {name:string;uid:unknown;definition:unknown;identitySource:unknown;missingReasons:Published;substages:ExperimentSubstage[]|null;metrics:PublishedMetric[];source:string;original:Published}
export const finite = (value:unknown):number|null => typeof value==="number"&&Number.isFinite(value)&&value>=0?value:null
export function alphaText(stage:ExperimentStage,run:Experiment):string {
  const spec=definitionStage(run,stage),scenario=run.definition?.loadScenarios.find(s=>s.uid===spec?.loadScenarioRef.uid)
  return scenario?scenario.spec.parts.map(part=>`${part.id}: ${part.alpha?Object.entries(part.alpha).map(([name,value])=>`${name} α ${value}`).join(", "):"직접 요청률 · α 없음"}`).join(" / "):"미기록"
}
export function ratio(value:unknown,numerator:unknown,denominator:unknown):number|null {
  if(denominator===0)return null
  const provided=finite(value)
  if(provided!==null)return provided<=1?provided:null
  const n=finite(numerator),d=finite(denominator)
  return n!==null&&d!==null&&d>0&&n<=d?n/d:null
}
export function resultMetric(row:ExperimentRow,key:"success"|"http"|"p95"):PublishedMetric {
  const request=row.source.startsWith("kpis.requests.")
  const originalKey=key==="success"?(request?"deadlineSuccessFraction":"withinSLOFraction"):key==="http"?"successFraction":request?"completedLatencyNS.p95":"latencyMS.p95"
  const metric=row.metrics.find(metric=>metric.key===originalKey)
  const value=key==="p95"?finite(metric?.value):ratio(metric?.value,metric?.numerator,metric?.denominator)
  return {...metric,key:originalKey,label:key==="success"?(request?"기한 내 성공 / 보낸 요청":"SLO 내 성공 / 보낸 요청"):key==="http"?"HTTP 성공 / 보낸 요청":"성공 응답 p95",value:value===null?null:key==="p95"&&request?value/1e6:value,rawValue:metric?.value,numerator:metric?.numerator,denominator:metric?.denominator,population:metric?.population,reason:metric?.reason,source:metric?.source??`${row.source}.${originalKey}`,unit:key==="p95"?"ms":"%"}
}
export function comparisonRows(a:ExperimentStage,b?:ExperimentStage) {
  const left=experimentRows(a),right=b?experimentRows(b):[]
  return [...new Set([...left,...right].map(row=>row.name))].map(name=>({name,a:left.filter(row=>row.name===name),b:right.filter(row=>row.name===name)}))
}
export function formatResult(value:unknown,unit=""):string {
  if(typeof value==="bigint"&&value>=0n&&(!unit||unit==="건"))return `${value}${unit?` ${unit}`:""}`
  const n=finite(value)
  if(n===null)return "미제공"
  return `${formatMeasurementNumber(unit==="%"?n*100:n,unit)}${unit?` ${unit}`:""}`
}
const displayStep=(unit:string)=>unit==="W" ? 0.1 : unit==="J/건"||unit==="J/item" ? 0.0001 : ["%","%p","ms"].includes(unit) ? 0.01 : 1
export function difference(a:unknown,b:unknown,unit:string,comparable=true):string {
  const left=finite(a),right=finite(b)
  if(!comparable||left===null||right===null)return "계산 불가"
  const delta=left-right,scaled=unit==="%"?delta*100:delta,label=unit==="%"?"%p":unit
  if(scaled!==0&&Math.abs(scaled)<displayStep(label))return `${displayStep(label)} ${label} 미만 · 방향 없음`
  return `${scaled>0?"+":""}${formatMeasurementNumber(scaled,label)}${label?` ${label}`:""}`
}
export function comparisonDirection(a:unknown,b:unknown,unit:string,aName:string,bName:string):string {
  const left=finite(a),right=finite(b)
  if(left===null||right===null)return "비교 불가"
  const delta=(left-right)*(unit==="%"?100:1)
  if(delta!==0&&Math.abs(delta)<displayStep(unit))return "표시단위 미만 · 방향 없음"
  return delta===0?`${aName} / ${bName}: 같음`:`${aName} 값이 ${bName} 값보다 ${delta<0?"낮음":"높음"}`
}
export function relativeEnergy(a:unknown,b:unknown):string {
  const left=finite(a),right=finite(b)
  if(left===null||right===null||right===0)return "계산 불가"
  const change=100*(left-right)/right
  if(change!==0&&Math.abs(change)<.01)return "0.01 % 미만 · 방향 없음"
  return `${change>0?"+":""}${formatMeasurementNumber(change,"%")} %`
}
export function stageSeconds(stage:ExperimentStage):number|null {
  const seconds=(Date.parse(stage.to)-Date.parse(stage.from))/1000
  return Number.isFinite(seconds)&&seconds>0?seconds:null
}
function sameDisplayedDuration(a:ExperimentStage,b:ExperimentStage):boolean {
  return stageSeconds(a)!==null&&stageSeconds(b)!==null&&formatResult(stageSeconds(a),"s")===formatResult(stageSeconds(b),"s")
}
export function stageRate(stage:ExperimentStage,run:Experiment):number|null {
  const spec=definitionStage(run,stage),scenario=run.definition?.loadScenarios.find(s=>s.uid===spec?.loadScenarioRef.uid)
  if(!scenario||scenario.spec.parts.some(part=>!part.rates))return null
  const seconds=scenario.spec.parts.reduce((sum,part)=>sum+part.durationSeconds,0)
  return seconds>0?scenario.spec.parts.reduce((sum,part)=>sum+Object.values(part.rates!).reduce((sum,rate)=>sum+rate,0)*part.durationSeconds,0)/seconds:null
}

export function energyText(stage:ExperimentStage,key="wholePoolJoules") {
  const value=stage.kpis[key],bound=stage.kpis.gapErrorBoundJoules
  return value==null?"":`${publishedText(value)}${bound==null?"":` ± ${publishedText(bound)}`} J`
}
const canonical=(value:unknown):string=>stringifySchedulerJSON(value&&typeof value==="object"&&!Array.isArray(value)?Object.fromEntries(Object.entries(value).sort(([a],[b])=>a.localeCompare(b)).map(([key,item])=>[key,canonical(item)])):value??null)
export function differentMeasurementRules(a:ExperimentStage,b:ExperimentStage):string[] {
  const left=object(a.kpis.measurementRules),right=object(b.kpis.measurementRules)
  return [...new Set([...Object.keys(left),...Object.keys(right)])].filter(key=>canonical(left[key])!==canonical(right[key]))
}

// Preserve RFC3339 nanoseconds: Date's millisecond truncation would omit same-time judgments.
export function decisionRange(at:string):{from:string;to:string}|null {
  const match=/^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(at)
  if(!match)return null
  const local=new Date(`${match[1]}Z`),whole=new Date(`${match[1]}${match[3]}`)
  if(!Number.isFinite(local.getTime())||local.toISOString().slice(0,19)!==match[1]||!Number.isFinite(whole.getTime()))return null
  const fraction=(match[2]??"").padEnd(9,"0"),next=Number(fraction)+1,second=whole.toISOString().slice(0,19)
  return {from:`${second}.${fraction}Z`,to:next===1e9?new Date(whole.getTime()+1000).toISOString().replace(".000Z",".000000000Z"):`${second}.${String(next).padStart(9,"0")}Z`}
}
export function decisionIdentity(event:SchedulerObservation):string {
  const d=event.decision
  return d?JSON.stringify([d.at,d.status,d.keep?.id,d.target?.id??null]):""
}
export function evidenceError(error:{status:number;data:unknown}):string {
  const code=object(error.data).code
  return error.status===504||code==="RequestTimeout"||code==="DashboardRelayTimeout"?"조회 시간 초과":"근거 조회 실패"
}

export function comparisonValidity(a:ExperimentStage,b:ExperimentStage,leftRun?:Experiment,rightRun?:Experiment) {
  const rows=comparisonRows(a,b),left=stageSeconds(a),right=stageSeconds(b)
  const x=leftRun?definitionStage(leftRun,a):undefined,y=rightRun?definitionStage(rightRun,b):undefined
  const sameLoad=x&&y&&leftRun?.definition&&rightRun?.definition?x.loadScenarioRef.uid===y.loadScenarioRef.uid&&canonical(leftRun.definition.spec.loadGenerator)===canonical(rightRun.definition.spec.loadGenerator):null
  let unknown=0,changed=0
  for(const row of rows) {
    const x=row.a[0],y=row.b[0]
    if(row.a.length!==1||row.b.length!==1||x.uid==null||y.uid==null||x.definition==null||y.definition==null||x.uid===""||y.uid===""||x.definition===""||y.definition==="")unknown++
    else if(x.uid!==y.uid||x.definition!==y.definition)changed++
  }
  const ruleKeys=differentMeasurementRules(a,b)
  const rulesMissing=!Object.keys(object(a.kpis.measurementRules)).length||!Object.keys(object(b.kpis.measurementRules)).length
  return {
    conditions:{load:sameLoad===null?"미기록":sameLoad?"같음":"다름",identity:changed?"다름":unknown||!rows.length?"미확인":"같음",rules:rulesMissing?"미확인":ruleKeys.length?"다름":"같음"},
    identityUnknown:unknown>0||!rows.length,identityDifferent:changed>0,
    load:sameLoad===null?"미기록":sameLoad?"같음":"다름",
    duration:`구간 길이 ${left===null||right===null?"미제공":sameDisplayedDuration(a,b)?"같음":"다름"} · A ${formatResult(left,"s")} / B ${formatResult(right,"s")}`,
    rules:`측정 규칙 ${rulesMissing?"미기록 항목 있음":ruleKeys.length?"차이 있음":"기록된 항목 차이 없음"}${ruleKeys.length?` · 다른 항목: ${ruleKeys.join(", ")}`:""}`,
    identity:rows.length?`서비스 등록·정의: 미확인 ${unknown} / 다름 ${changed} / 같은 등록·정의 ${rows.length-unknown-changed}개`:"서비스 등록·정의: 서비스 기록 없어 미확인",
    limited:rulesMissing||!!ruleKeys.length||!sameDisplayedDuration(a,b)||!rows.length||unknown>0||changed>0||sameLoad!==true,
  }
}
export function definitionStage(run:Experiment,stage:ExperimentStage) {
  if(run.stages.filter(s=>s.id===stage.id).length!==1)return undefined
  return run.definition?.spec.stages.find(s=>s.id===stage.id)
}
export function definitionPolicy(stage:ExperimentStage,run:Experiment):string {
  const spec=definitionStage(run,stage)
  if(!spec)return "미기록"
  if(spec.mode==="autonomous")return "자동 운영"
  if(spec.mode==="fixed")return "고정 구성"
  const labels={placement:"배치",resources:"자원",replicas:"복제본",routing:"라우팅",batching:"배칭",power:"전력",membership:"구성원"}
  const controls=(spec.control??run.definition!.spec.control).controls
  const disabled=Object.entries(labels).filter(([key])=>!controls[key as keyof typeof controls]).map(([,label])=>label)
  return `제어요소 끔${disabled.length?` · ${disabled.join(", ")}`:""}`
}
export function stageLabel(stage:ExperimentStage,run:Experiment):string {
  const spec=definitionStage(run,stage)
  return spec?`${stage.id} / ${spec.row} / ${definitionPolicy(stage,run)}`:`${stage.id} / 미기록`
}
function resolveComparison(run:Experiment,ref:ComparisonReference,runs:Experiment[]) {
  const matches=ref.historical?runs.filter(other=>other.id===ref.historical!.publicationID):[run]
  if(matches.length!==1)return undefined
  const target=matches[0],id=ref.historical?.stageID??ref.stageID
  const stages=target.stages.filter(stage=>stage.id===id)
  return stages.length===1?{run:target,stage:stages[0]}:undefined
}
export function recordedComparisons(run:Experiment):EvaluationComparison[]|undefined {
  return run.definition?run.definition.spec.comparisons??[]:run.comparisons
}
export function definitionComparisons(run:Experiment,runs:Experiment[]=[run]) {
  return (recordedComparisons(run)??[]).flatMap(comparison=>{
    const left=resolveComparison(run,comparison.left,runs),right=resolveComparison(run,comparison.right,runs)
    return left&&right?[{key:`${left.run.id}/${left.stage.id}−${right.run.id}/${right.stage.id}`,chill:left.stage,control:right.stage,leftRun:left.run,rightRun:right.run,basis:"정의 비교",reason:comparison.reason}]:[]
  })
}
export function definitionComparisonReason(run:Experiment,runs:Experiment[]=[run]):string {
  const comparisons=recordedComparisons(run)
  if(!comparisons)return "미기록"
  if(!comparisons.length)return "비교 없음"
  return comparisons.map(c=>`${c.reason}${!resolveComparison(run,c.left,runs)||!resolveComparison(run,c.right,runs)?" · 대상 게시 없음":""}`).join(" / ")
}
export function campaignGroups(runs:Experiment[]) {
  return runs.map(run=>({run,pairs:definitionComparisons(run,runs),title:run.definition?.run.name??run.id,question:run.definition?.spec.question??"미기록",comparisonReason:definitionComparisonReason(run,runs)})).sort((a,b)=>Date.parse(b.run.from)-Date.parse(a.run.from))
}
export function experimentSelection(search:string,values:Record<string,string>):URLSearchParams {
  const next=new URLSearchParams(search)
  for(const [key,value] of Object.entries(values)) {
    if(value)next.set(key,value);else next.delete(key)
    if((key==="a"||key==="b")&&!Object.hasOwn(values,`${key}s`))next.delete(`${key}s`)
    if(key==="a"&&!Object.hasOwn(values,"al"))next.delete("al")
  }
  for(const field of ["at","decision","record","evidence","evidenceCursor"])next.delete(field)
  return next
}
export function commitExperimentSelection(location:Pick<Location,"search">,history:Pick<History,"replaceState">,values:Record<string,string>) {
  history.replaceState(null,"",`/experiments/results?${experimentSelection(location.search,values)}`)
}
export function frozenP95SLO(row:ExperimentRow,run?:Experiment):number|null {
  const uid=row.original.workloadUID
  if(typeof uid!=="string"||!uid)return null
  const workload=run?.definition?.workloads.find(w=>w.uid===uid)
  return workload?.slo.percentile===95?Number(workload.slo.targetLatencyNanos)/1e6:null
}

export function serviceDelta(a:ExperimentRow|undefined,b:ExperimentRow|undefined) {
  const left=a?resultMetric(a,"success"):null,right=b?resultMetric(b,"success"):null
  const ap=a?finite(resultMetric(a,"p95").value):null,bp=b?finite(resultMetric(b,"p95").value):null
  return `성공 ${difference(left?.value,right?.value,"%",left?.key===right?.key)}; p95 ${difference(ap,bp,"ms")}${ap!==null&&bp!==null?bp===0?" (배율 계산 불가 · B=0)":` (×${(ap/bp).toFixed(2)})`:""}`
}


export function campaignTradeoffs(run:Experiment,runs:Experiment[]=[run]) {
  const comparisons=definitionComparisons(run,runs).map(pair=>({...pair,
    energyDelta:finite(pair.chill.kpis.wholePoolJoules)!==null&&finite(pair.control.kpis.wholePoolJoules)!==null?Number(pair.chill.kpis.wholePoolJoules)-Number(pair.control.kpis.wholePoolJoules):null,
    services:comparisonRows(pair.chill,pair.control).map(row=>{
      const a=row.a.length===1?row.a[0]:undefined,b=row.b.length===1?row.b[0]:undefined
      const am=a?resultMetric(a,"success"):null,bm=b?resultMetric(b,"success"):null
      const av=finite(am?.value),bv=finite(bm?.value),ap=a?finite(resultMetric(a,"p95").value):null,bp=b?finite(resultMetric(b,"p95").value):null
      return {name:row.name,a,b,successDelta:am?.key===bm?.key&&av!==null&&bv!==null?(av-bv)*100:null,p95Ratio:ap!==null&&bp!==null&&bp>0?ap/bp:null,p95Delta:ap!==null&&bp!==null?ap-bp:null}
    }),
  }))
  const worstSuccess=Math.min(0,...comparisons.flatMap(row=>row.services.map(service=>service.successDelta??0)))
  const worstP95=Math.max(0,...comparisons.flatMap(row=>row.services.map(service=>service.p95Delta??0)))
  return comparisons.map(row=>({...row,services:row.services.map(service=>({...service,worstSuccess:worstSuccess<0&&service.successDelta===worstSuccess,worstP95:worstP95>0&&service.p95Delta===worstP95}))}))
}

export function campaignScoreboard(run:Experiment,runs:Experiment[]=[run]) {
  const comparisons=campaignTradeoffs(run,runs),services=comparisons.flatMap(pair=>pair.services.map(service=>({...service,pair})))
  const energies=comparisons.filter(pair=>pair.energyDelta!==null)
  const successes=services.filter(service=>service.successDelta!==null),latencies=services.filter(service=>service.p95Delta!==null)
  const worstSuccess=successes.reduce<typeof successes[number]|null>((worst,service)=>!worst||service.successDelta!<worst.successDelta!?service:worst,null)
  const worstLatency=latencies.reduce<typeof latencies[number]|null>((worst,service)=>!worst||service.p95Delta!>worst.p95Delta!?service:worst,null)
  const compared=new Set(comparisons.flatMap(pair=>[pair.chill,pair.control]))
  const ruleMissing=comparisons.filter(pair=>!Object.keys(object(pair.chill.kpis.measurementRules)).length||!Object.keys(object(pair.control.kpis.measurementRules)).length).length
  const ruleDifferences=comparisons.filter(pair=>differentMeasurementRules(pair.chill,pair.control).length).length
  const durationDifferences=comparisons.filter(pair=>!sameDisplayedDuration(pair.chill,pair.control)).length
  const summarize=(pairs:typeof comparisons)=>{
    const energies=pairs.filter(pair=>pair.energyDelta!==null),services=pairs.flatMap(pair=>pair.services)
    const base=energies.reduce((sum,pair)=>sum+Number(pair.control.kpis.wholePoolJoules),0)
    const successes=services.flatMap(service=>service.successDelta===null?[]:[service.successDelta]),ratios=services.flatMap(service=>service.p95Ratio===null?[]:[service.p95Ratio])
    return {energyPercent:energies.length&&base>0?100*energies.reduce((sum,pair)=>sum+pair.energyDelta!,0)/base:null,successDelta:successes.length?Math.min(...successes):null,p95Ratio:ratios.length?Math.max(...ratios):null}
  }
  const validities=comparisons.map(pair=>({pair,validity:comparisonValidity(pair.chill,pair.control,pair.leftRun,pair.rightRun)}))
  const matched=validities.filter(row=>!row.validity.limited).map(row=>row.pair),limited=validities.filter(row=>row.validity.limited).map(row=>row.pair)
  const summary=summarize(matched),referenceSummary=summarize(limited)
  const reference=!matched.length&&limited.length>0,judged=reference?referenceSummary:summary,pairs=reference?limited:matched
  let conclusion="차이 없음"
  if(!pairs.length)conclusion=!recordedComparisons(run)?"미기록":recordedComparisons(run)!.length?"대상 게시 없음":"비교 없음"
  else if(judged.successDelta!==null&&judged.successDelta<=-.01||judged.p95Ratio!==null&&judged.p95Ratio>=1.01)conclusion="SLO 악화"
  else if(judged.energyPercent!==null&&judged.energyPercent<=-.01)conclusion="에너지 감소"
  else if(judged.energyPercent!==null&&judged.energyPercent>=.01)conclusion="에너지 증가"
  else if(pairs.some(pair=>pair.services.some(service=>service.successDelta!==null&&service.successDelta>=.01||service.p95Ratio!==null&&service.p95Ratio<=.99)))conclusion="SLO 개선"
  else if(judged.energyPercent===null||judged.successDelta===null||judged.p95Ratio===null||pairs.some(pair=>pair.energyDelta===null||pair.services.some(service=>service.successDelta===null||service.p95Ratio===null)))conclusion="결과 부족"
  return {
    summary,referenceSummary,conclusion,reference,
    energyCounts:{decreased:energies.filter(pair=>pair.energyDelta!<0).length,increased:energies.filter(pair=>pair.energyDelta!>0).length,equal:energies.filter(pair=>pair.energyDelta===0).length,missing:comparisons.length-energies.length,
      delta:energies.length?energies.reduce((sum,pair)=>sum+pair.energyDelta!,0):null},
    quality:[
      {label:"최대 성공률 하락",unit:"%p",value:worstSuccess?.successDelta==null?null:Math.min(0,worstSuccess.successDelta),service:worstSuccess?.name??"—",stage:worstSuccess?.pair.key??"—",compared:successes.length,missing:services.length-successes.length},
      {label:"최대 p95 증가",unit:"ms",value:worstLatency?.p95Delta==null?null:Math.max(0,worstLatency.p95Delta),ratio:worstLatency?.p95Ratio??null,service:worstLatency?.name??"—",stage:worstLatency?.pair.key??"—",compared:latencies.length,missing:services.length-latencies.length},
    ],
    coverageCounts:{stages:run.stages.length,pairs:comparisons.length,unpaired:run.stages.filter(stage=>!compared.has(stage)).length,ruleMissing,ruleDifferences,durationDifferences,
      identityUnknown:validities.filter(row=>row.validity.identityUnknown).length,identityDifferences:validities.filter(row=>row.validity.identityDifferent).length,
      loadUnknown:validities.filter(row=>row.validity.conditions.load==="미기록").length,loadDifferences:validities.filter(row=>row.validity.conditions.load==="다름").length,
      matched:matched.length,limited:limited.length},
  }
}

export function durationMismatch(a:ExperimentStage,b:ExperimentStage):boolean {
  const left=stageSeconds(a),right=stageSeconds(b)
  return left!==null&&right!==null&&Math.abs(left-right)/right>.01
}

export type ExperimentHeatmapRow = {
  key:string;id:string;stage:ExperimentStage;other:ExperimentStage|null;leftRun:Experiment;rightRun:Experiment;rate:number|null;durationDifferent:boolean;reason:string;energyPercent:number|null
  services:{name:string;a:ExperimentRow|undefined;b:ExperimentRow|undefined;successDelta:number|null;p95Ratio:number|null;p95Delta:number|null;worstSuccess:boolean;worstP95:boolean}[]
}
export function experimentIntervalSelection(run:Experiment,row:ExperimentHeatmapRow):Record<string,string> {
  return {a:run.id,al:row.leftRun.id,as:row.stage.id,b:row.other?row.rightRun.id:"",bs:row.other?.id??""}
}

// One row per policy comparison; unmatched publications retain their own frozen stage.
export function experimentHeatmap(run:Experiment,runs:Experiment[]=[run]):ExperimentHeatmapRow[] {
  const comparisons=campaignTradeoffs(run,runs),used=new Set(comparisons.flatMap(row=>[row.chill,row.control]))
  const stages=[...comparisons.map(row=>({stage:row.chill,other:row.control,comparison:row,leftRun:row.leftRun,rightRun:row.rightRun})),...run.stages.filter(stage=>!used.has(stage)).map(stage=>({stage,other:null,comparison:null,leftRun:run,rightRun:run}))]
  return stages.sort((a,b)=>Date.parse(a.stage.from)-Date.parse(b.stage.from)).map(({stage,other,comparison,leftRun,rightRun},index)=>{
    const base=other?finite(other.kpis.wholePoolJoules):null,energy=finite(stage.kpis.wholePoolJoules)
    return {key:`${stage.id}/${index}`,id:stage.id,stage,other,leftRun,rightRun,rate:stageRate(stage,leftRun),durationDifferent:!!other&&durationMismatch(stage,other),
      reason:comparison?.reason??definitionComparisonReason(run,runs),
      energyPercent:base!==null&&base>0&&energy!==null?100*(energy-base)/base:null,
      services:comparison?.services??[]}
  })
}
export function experimentDifference(value:number|null,baseline=0,higherIsBetter=false) {
  const delta=value===null?null:value-baseline
  const small=delta!==null&&delta!==0&&Math.abs(delta)<.01
  return {text:value===null?"미기록":small&&baseline===0?"0.01 미만":formatMeasurementNumber(value,"%"),tone:delta===null||small||delta===0?"bg-muted/50 text-muted-foreground":(delta>0)===higherIsBetter?"bg-ok/10 text-ok":"bg-destructive/10 text-destructive"}
}

export function experimentChartData(run:Experiment,service:string,runs:Experiment[]=[run]) {
  return experimentHeatmap(run,runs).map((row,index)=>{
    const own=experimentRows(row.stage).filter(item=>item.name===service),other=row.other?experimentRows(row.other).filter(item=>item.name===service):[]
    return {id:row.id,interval:index+1,rate:row.rate,leftLabel:stageLabel(row.stage,row.leftRun),rightLabel:row.other?stageLabel(row.other,row.rightRun):"비교 없음",
      leftP95:own.length===1?resultMetric(own[0],"p95").value:null,
      rightP95:other.length===1?resultMetric(other[0],"p95").value:null,
      leftSLO:own.length===1?frozenP95SLO(own[0],row.leftRun):null,
      rightSLO:other.length===1?frozenP95SLO(other[0],row.rightRun):null}
  })
}

export function stageHistoryLink(stage:ExperimentStage):string {
  return `/records?${new URLSearchParams({from:stage.from,to:stage.to,limit:"100",filter:"decision-action",order:"asc"})}`
}
export function stageActivity(page:SchedulerObservationPage) {
  const events=page.events??[],decisions=events.filter(event=>event.decision),actions=events.filter(event=>event.action)
  return {decisions:decisions.length,actions:actions.length,partial:!!page.nextCursor||!!page.gapUntil,statuses:[...new Set(decisions.map(event=>event.decision!.status))],kinds:[...new Set(actions.map(event=>event.action!.kind))]}
}

export const experimentPath=(id:string)=>`v1/experiments/${encodeURIComponent(id)}`
export const stageEvidencePath=(stage:ExperimentStage)=>`v1/observations/scheduler?${new URLSearchParams({from:stage.from,to:stage.to,limit:"100"})}`
export function selectedExperimentStage(run:Experiment|undefined,id:string,stageID:string|null):ExperimentStage|undefined {
  if(!id||run?.id!==id)return undefined
  const stages=run.stages.filter(stage=>stage.id===stageID)
  return stages.length===1?stages[0]:undefined
}
export function publishedVerdict(stage:ExperimentStage):string {
  const status=String(stage.kpis.status??"미제공")
  return status==="FAIL"?"요청 목표 미달":status.includes("NOT_READY")?"판정 근거 부족":status.includes("NOT_EVALUATED")?"아직 판정하지 않음":status.includes("DESCRIPTIVE")?"게시된 관측 결과":status
}

export function stageLoadLabel(stage:ExperimentStage,run:Experiment):string {
  const spec=definitionStage(run,stage),scenario=run.definition?.loadScenarios.find(s=>s.uid===spec?.loadScenarioRef.uid)
  return scenario?scenario.spec.parts.map(part=>`${part.id}: ${Object.entries(part.rates??part.alpha??{}).map(([name,value])=>`${name} ${part.rates?`${value} req/s`:`α ${value}`}`).join(", ")}`).join(" / "):"미기록"
}
