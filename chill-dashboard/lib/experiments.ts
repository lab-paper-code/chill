import type { SchedulerObservation, SchedulerObservationPage } from "./read-types.ts"
import { stringifySchedulerJSON } from "./scheduler.ts"
import { formatMeasurementNumber } from "./measurements.ts"

export type Published = Record<string, unknown>
export type ExperimentStage = {id:string;from:string;to:string;kpis:Published}
export type Experiment = {schemaVersion:string;id:string;from:string;to:string;status:string;stages:ExperimentStage[];sources:{store:string;key:string}[]}
export const object = (value:unknown):Published => value && typeof value === "object" && !Array.isArray(value) ? value as Published : {}
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
export function alphaText(alpha:number|null,reason:unknown) {
  return alpha!==null?`α ${alpha}`:typeof reason==="string"&&/^Direct (?:request )?rates/i.test(reason)?"직접 요청률 설정 — α 없음":"α 미제공"
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
export function stageRate(stage:ExperimentStage):number|null {
  const rows=experimentRows(stage),seconds=stageSeconds(stage)
  // Two publisher shapes for one name are alternative candidates, not additive traffic.
  if(!rows.length||new Set(rows.map(row=>row.name)).size!==rows.length||!seconds)return null
  const offered=rows.map(row=>finite(row.original.offered))
  return offered.every(value=>value!==null)?offered.reduce<number>((sum,value)=>sum+value!,0)/seconds:null
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

// Only the complete published E5 suffix identifies a policy counterpart.
export function stagePolicy(stage:ExperimentStage):string {
  return stage.id.startsWith("full-E5-")?"자동 운영":stage.id.startsWith("static-E5-")?"고정 구성":"기타 구성"
}
export function stagePairing(run:Experiment,stage:ExperimentStage) {
  const match=/^(full|static)-(E5-.+)$/.exec(stage.id)
  if(!match)return {paired:false,counterpart:null,reason:"대응 정책·단계 식별자 미기록"}
  const own=run.stages.filter(other=>other.id===stage.id)
  const expected=`${match[1]==="full"?"static":"full"}-${match[2]}`
  const candidates=run.stages.filter(other=>other.id===expected)
  if(own.length>1||candidates.length>1)return {paired:false,counterpart:null,reason:"동일 단계 식별자 중복"}
  if(!candidates.length)return {paired:false,counterpart:null,reason:`정확한 대응 단계 ${expected} 없음`}
  return {paired:true,counterpart:candidates[0],reason:"단계·반복 코드 일치 · 부하 계획 미확인"}
}
// A load comparison is descriptive, not an exact repeated-stage pairing.
function sameLoad(a:ExperimentStage,b:ExperimentStage):boolean {
  const rows=comparisonRows(a,b)
  return !!rows.length&&rows.every(row=>{
    if(row.a.length!==1||row.b.length!==1)return false
    const left=finite(row.a[0].original.offered),right=finite(row.b[0].original.offered),as=stageSeconds(a),bs=stageSeconds(b)
    if(left===null||right===null||as===null||bs===null)return false
    const ar=left/as,br=right/bs
    return ar===br||Math.abs(ar-br)/Math.max(ar,br)<=.01
  })
}
export function comparisonValidity(a:ExperimentStage,b:ExperimentStage) {
  const rows=comparisonRows(a,b),left=stageSeconds(a),right=stageSeconds(b)
  let unknown=0,changed=0
  for(const row of rows) {
    const x=row.a[0],y=row.b[0]
    if(row.a.length!==1||row.b.length!==1||x.uid==null||y.uid==null||x.definition==null||y.definition==null||x.uid===""||y.uid===""||x.definition===""||y.definition==="")unknown++
    else if(x.uid!==y.uid||x.definition!==y.definition)changed++
  }
  const ruleKeys=differentMeasurementRules(a,b)
  const rulesMissing=!Object.keys(object(a.kpis.measurementRules)).length||!Object.keys(object(b.kpis.measurementRules)).length
  const loadUnknown=!rows.length||rows.some(row=>row.a.length!==1||row.b.length!==1||finite(row.a[0].original.offered)===null||finite(row.b[0].original.offered)===null)||left===null||right===null
  return {
    conditions:{load:loadUnknown?"미확인":sameLoad(a,b)?"같음":"다름",identity:changed?"다름":unknown||!rows.length?"미확인":"같음",rules:rulesMissing?"미확인":ruleKeys.length?"다름":"같음"},
    load:sameLoad(a,b)?"서비스별 평균 요청률 · 차이 1% 이내":"서비스별 평균 요청률 · 다르거나 미확인",
    duration:`구간 길이 ${left===null||right===null?"미제공":sameDisplayedDuration(a,b)?"같음":"다름"} · A ${formatResult(left,"s")} / B ${formatResult(right,"s")}`,
    rules:`측정 규칙 ${rulesMissing?"미기록 항목 있음":ruleKeys.length?"차이 있음":"기록된 항목 차이 없음"}${ruleKeys.length?` · 다른 항목: ${ruleKeys.join(", ")}`:""}`,
    identity:rows.length?`서비스 등록·정의: 미확인 ${unknown} / 다름 ${changed} / 같은 등록·정의 ${rows.length-unknown-changed}개`:"서비스 등록·정의: 서비스 기록 없어 미확인",
    limited:rulesMissing||!!ruleKeys.length||!sameDisplayedDuration(a,b)||!rows.length||unknown>0||changed>0||!sameLoad(a,b),
  }
}
export function stageComparison(run:Experiment,stage:ExperimentStage) {
  const exact=stagePairing(run,stage)
  if(exact.paired||exact.reason==="동일 단계 식별자 중복"||stagePolicy(stage)==="기타 구성")return {...exact,basis:"단계 대응"}
  const candidates=(own:ExperimentStage)=>run.stages.filter(other=>stagePolicy(other)!=="기타 구성"&&stagePolicy(other)!==stagePolicy(own)&&!stagePairing(run,other).paired&&stagePairing(run,other).reason!=="동일 단계 식별자 중복"&&sameLoad(own,other))
  const matches=candidates(stage)
  if(matches.length!==1||candidates(matches[0]).length!==1)return {...exact,basis:"비교 없음",reason:`${exact.reason} · 같은 부하 후보 ${matches.length}개${matches.length?" (유일한 양방향 연결 아님)":""}`}
  return {paired:false,counterpart:matches[0],basis:"같은 부하 비교",reason:"단계 코드 다름 · 서비스별 평균 요청률 차이 1% 이내 · 양방향 유일 후보"}
}
export function campaignPairs(run:Experiment) {
  return [...run.stages].sort((a,b)=>Date.parse(a.from)-Date.parse(b.from)).filter(stage=>stage.id.startsWith("full-E5-")).flatMap(chill=>{
    const pairing=stageComparison(run,chill)
    return pairing.counterpart?[{key:chill.id.slice(5),chill,control:pairing.counterpart,basis:pairing.basis,reason:pairing.reason}]:[]
  })
}
export function campaignGroups(runs:Experiment[]) {
  return runs.map(run=>{
    const pairs=campaignPairs(run)
    const reasons=[...new Set(run.stages.map(stage=>stageComparison(run,stage)).filter(pair=>!pair.counterpart).map(pair=>pair.reason))]
    return {run,pairs,title:run.id,
      question:pairs.length?"자동 운영과 고정 구성의 성공률·지연·에너지 비교":"부하·운영 구성별 성공률·지연·에너지",
      comparisonReason:pairs.length?`같은 부하 비교 ${pairs.length}쌍`:reasons.join(" / ")||"게시된 단계 없음",
    }
  }).sort((a,b)=>Number(!!b.pairs.length)-Number(!!a.pairs.length)||Date.parse(b.run.from)-Date.parse(a.run.from))
}
export function experimentSelection(search:string,values:Record<string,string>):URLSearchParams {
  const next=new URLSearchParams(search)
  for(const [key,value] of Object.entries(values)) {
    if(value)next.set(key,value);else next.delete(key)
    if((key==="a"||key==="b")&&!Object.hasOwn(values,`${key}s`))next.delete(`${key}s`)
  }
  for(const field of ["at","decision","record","evidence","evidenceCursor"])next.delete(field)
  return next
}
export function commitExperimentSelection(location:Pick<Location,"search">,history:Pick<History,"replaceState">,values:Record<string,string>) {
  history.replaceState(null,"",`/experiments?${experimentSelection(location.search,values)}`)
}
export function frozenP95SLO(row:ExperimentRow):number|null {
  const measurement=object(row.original.measurementSLO)
  if(measurement.percentile===95) {
    const nanos=finite(measurement.targetLatencyNanos)
    return nanos===null?null:nanos/1e6
  }
  return row.original.sloPercentile===95?finite(row.original.sloMS):null
}
export function policyChartStyle(policy:string):{color:string;shape:"square"|"circle"|"diamond"} {
  return policy==="고정 구성"?{color:"var(--color-slate-500, #64748b)",shape:"square"}:policy==="자동 운영"?{color:"var(--color-teal-600, #0d9488)",shape:"circle"}:{color:"var(--chart-3)",shape:"diamond"}
}
export function stageChartData(run:Experiment) {
  return [...run.stages].sort((a,b)=>Date.parse(a.from)-Date.parse(b.from)).map((stage,index)=>{
    const rows=experimentRows(stage),services:Record<string,{success:number|null;p95:number|null;sloMS:number|null;reason:string}>={}
    const seconds=stageSeconds(stage),rate=stageRate(stage)
    const energy=finite(stage.kpis.wholePoolJoules),bound=finite(stage.kpis.gapErrorBoundJoules),pairing=stageComparison(run,stage)
    for(const row of rows) {
      const unique=rows.filter(other=>other.name===row.name).length===1
      const success=unique?finite(resultMetric(row,"success").value):null,p95=unique?finite(resultMetric(row,"p95").value):null
      services[row.name]={success:success===null?null:success*100,p95,sloMS:unique?frozenP95SLO(row):null,
        reason:!unique?"동명 서비스 KPI 중복":success===null||p95===null?publishedText(row.missingReasons)||"성공률 또는 p95 미제공":""}
    }
    const axisStage=pairing.counterpart&&stagePolicy(stage)==="고정 구성"?pairing.counterpart:stage
    return {id:stage.id,stage,step:index+1,axis:axisStage.id.replace(/^(full|static)-/,""),policy:stagePolicy(stage),...pairing,rate,energy,
      watts:energy!==null&&seconds!==null?energy/seconds:null,
      wattError:bound!==null&&seconds!==null?bound/seconds:null,
      joulesPerRequest:energy!==null&&rate!==null&&rate>0&&seconds!==null?energy/(rate*seconds):null,services}
  })
}

export function serviceDelta(a:ExperimentRow|undefined,b:ExperimentRow|undefined) {
  const left=a?resultMetric(a,"success"):null,right=b?resultMetric(b,"success"):null
  const ap=a?finite(resultMetric(a,"p95").value):null,bp=b?finite(resultMetric(b,"p95").value):null
  return `성공 ${difference(left?.value,right?.value,"%",left?.key===right?.key)}; p95 ${difference(ap,bp,"ms")}${ap!==null&&bp!==null?bp===0?" (배율 계산 불가 · B=0)":` (×${(ap/bp).toFixed(2)})`:""}`
}

export function policyStageGroups(run:Experiment) {
  const points=stageChartData(run)
  const policies=[...new Set(points.map(point=>point.policy))]
  const axes=[...new Set(points.map(point=>point.axis))]
  return {policies,groups:axes.flatMap(axis=>{
    const candidates=points.filter(point=>point.axis===axis)
    if(candidates.some(point=>point.reason==="동일 단계 식별자 중복"))return candidates.map((point,index)=>({
      axis:`${axis} · 중복 ${index+1}`,series:policies.map(policy=>policy===point.policy?point:null),
    }))
    return [{axis,series:policies.map(policy=>candidates.find(point=>point.policy===policy)??null)}]
  })}
}

export function campaignTradeoffs(run:Experiment) {
  const comparisons=campaignPairs(run).map(pair=>({...pair,
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

export function campaignScoreboard(run:Experiment) {
  const comparisons=campaignTradeoffs(run),services=comparisons.flatMap(pair=>pair.services.map(service=>({...service,pair})))
  const energies=comparisons.filter(pair=>pair.energyDelta!==null)
  const successes=services.filter(service=>service.successDelta!==null),latencies=services.filter(service=>service.p95Delta!==null)
  const worstSuccess=successes.reduce<typeof successes[number]|null>((worst,service)=>!worst||service.successDelta!<worst.successDelta!?service:worst,null)
  const worstLatency=latencies.reduce<typeof latencies[number]|null>((worst,service)=>!worst||service.p95Delta!>worst.p95Delta!?service:worst,null)
  const compared=new Set(comparisons.flatMap(pair=>[pair.chill,pair.control]))
  const ruleMissing=comparisons.filter(pair=>!Object.keys(object(pair.chill.kpis.measurementRules)).length||!Object.keys(object(pair.control.kpis.measurementRules)).length).length
  const ruleDifferences=comparisons.filter(pair=>differentMeasurementRules(pair.chill,pair.control).length).length
  const durationDifferences=comparisons.filter(pair=>!sameDisplayedDuration(pair.chill,pair.control)).length
  const energyBase=energies.reduce((sum,pair)=>sum+Number(pair.control.kpis.wholePoolJoules),0)
  const ratios=services.map(service=>service.p95Ratio).filter((value):value is number=>value!==null)
  return {
    summary:{energyPercent:energies.length&&energyBase>0?100*energies.reduce((sum,pair)=>sum+pair.energyDelta!,0)/energyBase:null,successDelta:worstSuccess?.successDelta??null,p95Ratio:ratios.length?Math.max(...ratios):null},
    energyCounts:{decreased:energies.filter(pair=>pair.energyDelta!<0).length,increased:energies.filter(pair=>pair.energyDelta!>0).length,equal:energies.filter(pair=>pair.energyDelta===0).length,missing:comparisons.length-energies.length,
      delta:energies.length?energies.reduce((sum,pair)=>sum+pair.energyDelta!,0):null},
    quality:[
      {label:"최대 성공률 하락",unit:"%p",value:worstSuccess?.successDelta==null?null:Math.min(0,worstSuccess.successDelta),service:worstSuccess?.name??"—",stage:worstSuccess?.pair.key??"—",compared:successes.length,missing:services.length-successes.length},
      {label:"최대 p95 증가",unit:"ms",value:worstLatency?.p95Delta==null?null:Math.max(0,worstLatency.p95Delta),ratio:worstLatency?.p95Ratio??null,service:worstLatency?.name??"—",stage:worstLatency?.pair.key??"—",compared:latencies.length,missing:services.length-latencies.length},
    ],
    coverageCounts:{stages:run.stages.length,pairs:comparisons.length,unpaired:run.stages.filter(stage=>!compared.has(stage)).length,ruleMissing,ruleDifferences,durationDifferences,
      limited:comparisons.filter(pair=>comparisonValidity(pair.chill,pair.control).limited).length},
  }
}

export function durationMismatch(a:ExperimentStage,b:ExperimentStage):boolean {
  const left=stageSeconds(a),right=stageSeconds(b)
  return left!==null&&right!==null&&Math.abs(left-right)/right>.01
}

export type ExperimentHeatmapRow = {
  key:string;id:string;stage:ExperimentStage;other:ExperimentStage|null;rate:number|null;durationDifferent:boolean;reason:string;energyPercent:number|null
  services:{name:string;a:ExperimentRow|undefined;b:ExperimentRow|undefined;successDelta:number|null;p95Ratio:number|null;p95Delta:number|null;worstSuccess:boolean;worstP95:boolean}[]
}

// One row per policy comparison; unmatched publications retain their own frozen stage.
export function experimentHeatmap(run:Experiment):ExperimentHeatmapRow[] {
  const comparisons=campaignTradeoffs(run),used=new Set(comparisons.flatMap(row=>[row.chill,row.control]))
  const stages=[...comparisons.map(row=>({stage:row.chill,other:row.control,comparison:row})),...run.stages.filter(stage=>!used.has(stage)).map(stage=>({stage,other:null,comparison:null}))]
  return stages.sort((a,b)=>Date.parse(a.stage.from)-Date.parse(b.stage.from)).map(({stage,other,comparison},index)=>{
    const base=other?finite(other.kpis.wholePoolJoules):null,energy=finite(stage.kpis.wholePoolJoules)
    return {key:`${stage.id}/${index}`,id:stage.id,stage,other,rate:stageRate(stage),durationDifferent:!!other&&durationMismatch(stage,other),
      reason:other?"":stageComparison(run,stage).reason,
      energyPercent:base!==null&&base>0&&energy!==null?100*(energy-base)/base:null,
      services:comparison?.services??[]}
  })
}
export function experimentDifference(value:number|null,baseline=0,higherIsBetter=false) {
  const delta=value===null?null:value-baseline
  const small=delta!==null&&delta!==0&&Math.abs(delta)<.01
  return {text:value===null?"—":small&&baseline===0?"0.01 미만":formatMeasurementNumber(value,"%"),tone:delta===null||small||delta===0?"bg-muted/50 text-muted-foreground":(delta>0)===higherIsBetter?"bg-ok/10 text-ok":"bg-destructive/10 text-destructive"}
}

export function experimentChartData(run:Experiment,service:string) {
  return experimentHeatmap(run).map((row,index)=>{
    const own=experimentRows(row.stage).filter(item=>item.name===service),other=row.other?experimentRows(row.other).filter(item=>item.name===service):[]
    const automatic=stagePolicy(row.stage)==="자동 운영"?own:other,fixed=stagePolicy(row.stage)==="고정 구성"?own:other
    return {id:row.id,interval:index+1,rate:row.rate,
      automaticP95:automatic.length===1?resultMetric(automatic[0],"p95").value:null,
      fixedP95:fixed.length===1?resultMetric(fixed[0],"p95").value:null,
      automaticSLO:automatic.length===1?frozenP95SLO(automatic[0]):null,
      fixedSLO:fixed.length===1?frozenP95SLO(fixed[0]):null}
  })
}

export function stageHistoryLink(stage:ExperimentStage):string {
  return `/scheduler/history?${new URLSearchParams({from:stage.from,to:stage.to,limit:"100",filter:"decision-action"})}`
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
  const rate=stageRate(stage),rates=[...new Set(run.stages.map(stageRate).filter((value):value is number=>value!==null))].sort((a,b)=>a-b)
  if(rate===null)return "요청률 미기록"
  const index=rates.indexOf(rate),level=rates.length<2?"일정":index===0?"낮음":index===rates.length-1?"높음":"중간"
  return `부하 ${level} · ${formatResult(rate,"req/s")}`
}
