import { definitionStage, finite, object, recordedComparisons, type Experiment, type ExperimentStage, type ExperimentRow, type PublishedMetric, type Published } from "./experiments.ts"
import type { ComparisonReference } from "./evaluation-definition.ts"
import { stringifySchedulerJSON } from "./scheduler.ts"
import { formatMeasurementNumber } from "./measurements.ts"
import { settingControls, settingFields, settingTargets } from "./scheduler-settings.ts"

// Exactly the two publisher shapes. No current registration or live terminal buckets enter these rows.
export function experimentRows(stage:Pick<ExperimentStage,"kpis">):ExperimentRow[] {
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
      add("deadlineSuccessFraction","기한 안 성공 / offered",row.deadlineSuccessFraction,row.deadlineSuccess??row.successNumerator,row.offered??row.successDenominator)
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
export function stageSeconds(stage:ExperimentStage):number|null {
  const seconds=(Date.parse(stage.to)-Date.parse(stage.from))/1000
  return Number.isFinite(seconds)&&seconds>0?seconds:null
}
function sameDisplayedDuration(a:ExperimentStage,b:ExperimentStage):boolean {
  return stageSeconds(a)!==null&&stageSeconds(b)!==null&&formatResult(stageSeconds(a),"s")===formatResult(stageSeconds(b),"s")
}
const canonical=(value:unknown):string=>stringifySchedulerJSON(value&&typeof value==="object"&&!Array.isArray(value)?Object.fromEntries(Object.entries(value).sort(([a],[b])=>a.localeCompare(b)).map(([key,item])=>[key,canonical(item)])):value??null)
export function differentMeasurementRules(a:ExperimentStage,b:ExperimentStage):string[] {
  const left=object(a.kpis.measurementRules),right=object(b.kpis.measurementRules)
  return [...new Set([...Object.keys(left),...Object.keys(right)])].filter(key=>canonical(left[key])!==canonical(right[key]))
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
export function settingsPatchKeyLabel(path:string):string {
  const field=settingFields.find(field=>field.key===path)??settingTargets.find(field=>field.key===path)
  if(field)return field.label
  const control=settingControls.find(control=>`controls.${control.key}`===path)
  if(control)return control.label
  const demand=settingTargets.find(field=>field.key==="minimumDemand")!
  return path.startsWith("minimumDemand.")?`${demand.label} · ${path.slice("minimumDemand.".length)}`:path
}
export function settingsPatchEntries(patch:Published,prefix=""):[string,unknown][] {
  return Object.entries(patch).sort(([a],[b])=>a.localeCompare(b)).flatMap(([key,value])=>{
    const path=prefix?`${prefix}.${key}`:key
    return Object.keys(object(value)).length?settingsPatchEntries(object(value),path):[[path,value] as [string,unknown]]
  })
}
export function definitionPolicy(stage:Pick<ExperimentStage,"id">,run:Experiment):string {
  let keys:string[]|null|undefined
  if(run.facts){
    const matches=run.facts.stages?.filter(item=>item.id===stage.id)
    keys=matches?.length===1?matches[0].settingsPatchKeys:undefined
  }else{
    const spec=definitionStage(run,stage)
    if(spec)keys=settingsPatchEntries(spec.settingsPatch??run.definition?.spec.settingsPatch??{}).map(([key])=>key)
  }
  if(!Array.isArray(keys)||!keys.every(key=>typeof key==="string"))return "미기록"
  return keys.length?keys.map(settingsPatchKeyLabel).join(", "):"변경 없음"
}
function resolveComparison(run:Experiment,ref:ComparisonReference,runs:Experiment[]) {
  const matches=ref.historical?runs.filter(other=>other.id===ref.historical!.publicationID):[run]
  if(matches.length!==1)return undefined
  const target=matches[0],id=ref.historical?.stageID??ref.stageID
  const stages=target.stages.filter(stage=>stage.id===id)
  return stages.length===1?{run:target,stage:stages[0]}:undefined
}
export function definitionComparisons(run:Experiment,runs:Experiment[]=[run]) {
  return (recordedComparisons(run)??[]).flatMap(comparison=>{
    const left=resolveComparison(run,comparison.left,runs),right=resolveComparison(run,comparison.right,runs)
    return left&&right?[{key:`${left.run.id}/${left.stage.id}−${right.run.id}/${right.stage.id}`,chill:left.stage,control:right.stage,leftRun:left.run,rightRun:right.run,basis:"정의 비교",reason:comparison.reason}]:[]
  })
}
export function frozenSLO(row:ExperimentRow,run?:Experiment) {
  const {workloadUID:uid,definitionID,identitySource}=row.original
  if(typeof uid!=="string"||!uid||typeof definitionID!=="string"||!definitionID)return null
  const source=object(identitySource)
  if(typeof source.store!=="string"||typeof source.key!=="string"||!run?.sources.some(item=>item.store===source.store&&item.key===source.key))return null
  const workloads=run.definition?.workloads.filter(w=>w.uid===uid)
  const workload=workloads?.length===1?workloads[0]:undefined
  const definition=object(workload),model=object(definition.model),preprocessor=object(definition.preprocessor)
  const parts=[definition.uid,definition.generation,model.uid,model.generation,preprocessor.uid,preprocessor.generation]
  if(parts.some(value=>value===undefined||value===null)||definitionID!==parts.map(String).join("/"))return null
  const slo=workload?.slo
  return slo??null
}

// Display the publisher's verdict and counts; never derive a new D1 in the dashboard.
export function experimentServiceResults(stage:ExperimentStage,run:Experiment) {
  const components=object(stage.kpis.components),verdicts=object(object(components.slo).workloads),fidelity=object(components.loadFidelity)
  return experimentRows(stage).map(row=>{
    const declared=run.definition?.workloads.filter(workload=>row.uid?workload.uid===row.uid:workload.name===row.name)
    const success=resultMetric(row,"success")
    return {row,verdict:verdicts[row.name]??row.original.D1,
      declaredSLO:declared?.length===1?declared[0].slo:null,appliedSLO:frozenSLO(row,run),
      success:success.numerator,offered:success.denominator,
      outcomes:object(row.original.outcomeClasses),fidelity:object(fidelity[row.name]??row.original.issuanceFidelity)}
  })
}

export function durationMismatch(a:ExperimentStage,b:ExperimentStage):boolean {
  const left=stageSeconds(a),right=stageSeconds(b)
  return left!==null&&right!==null&&Math.abs(left-right)/right>.01
}

export function publishedEnergy(power:Published,legacy?:unknown) {
  const partial=power.energyStatus==="PARTIAL"
  const joules=power.energyStatus==="UNAVAILABLE"?null:partial?finite(power.observedJoules):finite(power.wholePoolJoules??legacy)
  return {joules,partial}
}
