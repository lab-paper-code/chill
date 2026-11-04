"use client"

import Link from "next/link"
import { useQuery } from "@tanstack/react-query"
import { ApiError, decodeResponse, relay, useChillQueries, useSession, warmingRetry, warmingDelay } from "@/lib/client"
import { Button } from "@/components/ui/button"
import { decodeExperiment, decodeExperimentPage, experimentOutcome, experimentPath, finite, historicalPublicationIDs, injectionRows, object, publishedText, recordedComparisons, type Experiment, type ExperimentFacts as RecordedFacts, type ExperimentPage, type ExperimentStage, type Published } from "@/lib/experiments"
import { campaignScoreboard, comparisonValidity, definitionComparisonReason, definitionComparisons, definitionPolicy, experimentRows, experimentServiceResults, publishedEnergy, ratio, settingsPatchEntries, settingsPatchKeyLabel, stageLoadParts, stageSeconds } from "@/lib/experiment-comparison"
import { formatMeasurementDuration, formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"

export function usePublishedExperimentFacts(id:string,enabled=true) {
  const {token}=useSession()
  return useQuery<RecordedFacts|null,ApiError>({queryKey:["chill","experiment-facts",id],enabled:!!token&&!!id&&enabled,retry:warmingRetry,retryDelay:warmingDelay,queryFn:async({signal})=>{
    let offset=0
    for(;;) {
      const response=await relay(`v1/experiments?${new URLSearchParams({limit:"5",offset:String(offset)})}`,token,signal)
      const page=decodeResponse(response.body,decodeExperimentPage) as ExperimentPage
      const publication=page.items.find(item=>item.id===id)
      if(publication)return publication.facts??null
      if(page.page?.nextOffset===undefined)return null
      if(page.page.nextOffset<=offset)throw new ApiError(0,"실험 목록 페이지가 전진하지 않아요",null)
      offset=page.page.nextOffset
    }
  }})
}

// Only explicit historical comparison references may request another publication.
export function useHistoricalExperiments(publications:Experiment[],catalog:Experiment[]=publications,enabled=true) {
  const ids=[...new Set(publications.flatMap(historicalPublicationIDs).filter(id=>!catalog.some(run=>run.id===id)&&!publications.some(run=>run.id===id)))]
  const queries=useChillQueries<Experiment>(ids.map(experimentPath),{enabled,decoder:decodeExperiment})
  const fetched=queries.flatMap((query,index)=>query.data?.id===ids[index]?[query.data]:[])
  const failed=queries.filter(query=>query.error&&!(query.error instanceof ApiError&&query.error.status===404))
  return {runs:[...publications,...catalog.filter(run=>!publications.some(item=>item.id===run.id)),...fetched],pending:ids.length>0&&(!enabled||queries.some(query=>query.isPending)),error:failed[0]?.error??null,refetch:()=>{for(const query of failed)void query.refetch()}}
}

export function HistoricalExperimentError({refetch}:{refetch:()=>void}) {
  return <div role="alert" className="notice"><p>대상 게시 조회 실패</p><Button type="button" variant="outline" onClick={refetch}>다시 불러오기</Button></div>
}

const outcomeWords:Record<string,string>={Complete:"완료",Failed:"실패",PASS:"통과",FAIL:"실패",INVALID:"무효",NOT_EVALUATED:"미평가",Executed:"실행",Cancelled:"취소",Invalid:"무효"}
const outcomeReasons:Record<string,string>={IdentityMismatch:"실행 대상 식별자 불일치",NoControlEffects:"복구할 제어 변경 없음",ProcedureComplete:"절차 완료",CleanupFailed:"정리 실패",ExecutionLost:"실행 기록 확인 불가",RemoteEffectUnknown:"원격 변경 결과 미확인"}
const outcomeLabel=(axis:{value:string;reason:string})=>axis.reason==="SLOFailed"?"SLO 미충족":outcomeWords[axis.value]??"미기록"

export function ExperimentOutcome({run,stage}:{run:Experiment;stage?:ExperimentStage}) {
  const outcome=experimentOutcome(run,stage)
  return <dl aria-label={stage?"단계 판정 3축":"실험 판정 3축"} className="flex flex-wrap gap-x-4 gap-y-1 text-sm">{([["execution","실행"],["measurement","측정"],["cleanup","정리"]] as const).map(([axis,label])=><div key={axis} className="flex items-start gap-2"><dt className="text-muted-foreground">{label}</dt><dd className={["Failed","FAIL","INVALID"].includes(outcome[axis].value)?"font-medium text-destructive":""}>{outcomeLabel(outcome[axis])}{outcome[axis].reason&&outcome[axis].reason!=="미기록"&&outcome[axis].reason!=="SLOFailed"&&<div className="text-xs font-normal text-muted-foreground">{outcomeReasons[outcome[axis].reason]??"원문 확인"}</div>}</dd></div>)}</dl>
}

const resultNumber=(value:unknown,unit="")=>typeof value==="bigint"?value.toLocaleString("ko-KR"):finite(value)===null?"미기록":formatMeasurementNumber(value,unit)
const resultPercent=(value:number|null)=>value===null?"미기록":`${resultNumber(value*100,"%")}%`
const resultTime=(value:unknown)=>typeof value==="string"||typeof value==="number"?formatMeasurementTime(value):"미기록"
const resultWords:Record<string,string>={FAIL:"FAIL",PASS:"PASS",OBSERVED_THRESHOLD_MET:"충족",NOT_EVALUATED:"미평가",PARTIAL:"부분 발행",OBSERVED:"관측됨",WITHIN_REPORTED_THRESHOLD:"허용 지연 이내",EXCEEDS_REPORTED_THRESHOLD:"허용 지연 초과"}
const resultWord=(value:unknown)=>typeof value==="string"?resultWords[value]??value:"미기록"

export function ExperimentVerdict({run}:{run:Experiment}) {
  const failed=[...new Set(run.stages.flatMap(stage=>Object.entries(object(object(object(stage.kpis.components).slo).workloads)).filter(([,value])=>value==="FAIL").map(([name])=>name)))]
  const verdict=run.outcome?.measurement?.verdict??run.status??"미기록"
  return <h2 aria-label="게시 판정" className={`text-lg font-semibold ${verdict==="FAIL"?"text-destructive":""}`}>{resultWord(verdict)}{failed.length?` · SLO 미달: ${failed.join(", ")}`:""}</h2>
}

function ResultFraction({value}:{value:number|null}) {
  return <div className="space-y-1"><span>{resultPercent(value)}</span><div aria-hidden="true" className="h-2 overflow-hidden rounded-sm bg-muted">{value!==null&&<div className="h-full bg-primary" style={{width:`${value*100}%`}}/>}</div></div>
}

function ExperimentEnergy({stage}:{stage:ExperimentStage}) {
  const components=object(stage.kpis.components),drain=object(components.drain),power=object(stage.kpis.power),energy=object(components.energy)
  const nodes=Array.isArray(power.nodes)?power.nodes.map(object):[],reasons=Array.isArray(power.reasons)?power.reasons.filter((reason):reason is string=>typeof reason==="string"):[]
  const affected=nodes.filter(node=>reasons.some(reason=>reason.startsWith(`Declared Node ${object(node.node).name}:`)&&reason.includes("partial/unavailable source or coverage below reporting policy"))).length
  const coverage=object(power.energyCoverage),gap=finite(coverage.maximumGapSeconds),{joules,partial}=publishedEnergy(power,stage.kpis.wholePoolJoules)
  const seconds=stageSeconds(stage),meanWatts=joules!==null&&seconds!==null?joules/seconds:null
  const drainReasons=Array.isArray(drain.reasons)?drain.reasons.filter((reason):reason is string=>typeof reason==="string"):[]
  const drainPower=object(stage.kpis.powerDrainInclusive)
  const drainEnergy=publishedEnergy(drainPower)
  return <section aria-label="에너지·배출" className="space-y-1 border-y py-3 text-sm">
    <p><strong>에너지 {publishedText(power.energyStatus??energy.offered)||"미기록"}</strong>{joules!==null?` · ${joules.toLocaleString("ko-KR",{maximumFractionDigits:1})} J ${partial?"관측 (일부 노드 coverage 미달)":"풀 전체"}`:" · 미기록"}{affected>0?` · ${affected}/${nodes.length} 노드 계측 출처 또는 공백 기준 미충족`:reasons.length?` · ${reasons[0]}${reasons.length>1?` 외 ${reasons.length-1}개`:""}`:""}{gap!==null&&` · 최대 공백 ${gap.toLocaleString("ko-KR",{minimumFractionDigits:1,maximumFractionDigits:1})} s`}</p>
    <p>{partial?"관측 평균 전력":"풀 평균 전력"} {resultNumber(meanWatts,"W")} W{partial&&" (일부 노드 coverage 미달)"}</p>
    <p className={drain.complete===false?"text-destructive":"text-muted-foreground"}> 게시 당시 요청 배출 {drain.complete===true?"완료":drain.complete===false?"미완료":"미기록"}{drainReasons.length?` · ${drainReasons.map(reason=>reason==="ProducerEOFWithoutCompleteDrain"?"생성기 종료 후 요청 배출 미완료":reason).join(", ")}`:""}</p>
    <details className="text-xs"><summary className="min-h-11 cursor-pointer">에너지 계측 근거·배출 포함 범위</summary>
      <div className="space-y-2 pb-2">
        <p>부하 구간 {resultTime(power.from)} – {resultTime(power.to)}{nodes.length?` · 선언 ${nodes.length}노드`:""}</p>
        {nodes.length>0&&<div className="overflow-x-auto"><table aria-label="노드별 에너지 coverage" className="w-full text-left tabular-nums">
          <thead><tr>{["노드","관측 J","coverage","사유"].map(label=><th scope="col" className="p-2 font-medium" key={label}>{label}</th>)}</tr></thead>
          <tbody>{nodes.map((node,index)=>{const source=object(node.sourceIdentity),nodeReasons=Array.isArray(node.reasons)?node.reasons.filter((reason):reason is string=>typeof reason==="string"):[];return <tr className="border-t align-top" key={String(object(node.node).uid??index)}>
            <th scope="row" className="p-2 font-normal">{publishedText(object(node.node).name)||"미기록"}</th>
            <td className="whitespace-nowrap p-2">{resultNumber("reportedMeterJoules" in node?node.reportedMeterJoules:"meterJoules" in node?node.meterJoules:node.joules)} J</td>
            <td className="whitespace-nowrap p-2">공백 {resultPercent(finite(node.gapFraction))}<span className="block">최대 {finite(node.maximumSampleGapSeconds)?.toLocaleString("ko-KR",{maximumFractionDigits:2})??"미기록"} s</span><span className="block">읽기 실패 {resultNumber(node.readFailures)}</span></td>
            <td className="p-2"><span className="block">{node.meterComplete===true?"계측 완전":node.meterComplete===false?"계측 불완전":"계측 완전성 미기록"}</span><span className="block">{node.policyMet===true?"공백 정책 충족":node.policyMet===false?"공백 정책 미충족":"공백 정책 미기록"}</span><span className="block">출처 {publishedText(source.kind)||"미기록"}{typeof source.metric==="string"?` / ${source.metric}`:""}</span>{nodeReasons.map(reason=><span className="block break-words" key={reason}>{reason}</span>)}</td>
          </tr>})}</tbody>
        </table></div>}
        {reasons.filter(reason=>!nodes.some(node=>reason.startsWith(`Declared Node ${object(node.node).name}:`)&&reason.includes("partial/unavailable source or coverage below reporting policy"))).map(reason=><p className="break-words" key={reason}>{reason}</p>)}
        <p>배출 포함 에너지 {publishedText(drainPower.energyStatus??energy.drainInclusive)||"미기록"} · {resultNumber(drainEnergy.joules)} J{drainEnergy.joules!==null&&(drainEnergy.partial?" 관측 (일부 노드 coverage 미달)":" 풀 전체")}</p>
        {Object.keys(drainPower).length>0&&<p>배출 포함 구간 {resultTime(drainPower.from)} – {resultTime(drainPower.to)}</p>}
        <p>공백 정책: 비율 {resultPercent(finite(object(coverage.policy).maxGapFraction))} · 단일 공백 {resultNumber(object(coverage.policy).maxSingleGapSeconds)} s</p>
      </div>
    </details>
  </section>
}

type StageResultsProps = {run:Experiment;stage:ExperimentStage;provisional?:never}|{provisional:Published;run?:never;stage?:never}
export function ExperimentStageResults({run,stage,provisional,service="",onSelect}:StageResultsProps&{service?:string;onSelect?:(service:string)=>void}) {
  const results=(provisional?experimentRows({kpis:{requests:provisional.workloads}}).map(row=>({
    row,verdict:undefined,declaredSLO:null,appliedSLO:null,success:row.original.deadlineSuccess,offered:row.original.offered,
    fraction:ratio(undefined,row.original.deadlineSuccess,row.original.offered),p95:null,p50:null,
    outcomes:object(row.original.outcomes),
    fidelity:object(object(provisional.generatorQuality)[row.name]),
  })):experimentServiceResults(stage,run)).filter(({row})=>!service||row.name===service)
  const parts=[...new Set(results.flatMap(({row})=>row.substages?.flatMap(part=>typeof part.id==="string"?[part.id]:[])??[]))]
  const outcomes=[["completed","완료","bg-ok"],["timeout","시간 초과","bg-heat"],["error","오류","bg-destructive"],["unfinished-or-no-final","미완료","bg-muted-foreground"]] as const
  const sloText=(slo:unknown)=>{const value=object(slo),target=value.targetLatencyNanos;return Object.keys(value).length?`p${finite(value.percentile)??"미기록"} · ${typeof target==="number"||typeof target==="bigint"?formatMeasurementDuration(target):"미기록 ms"}`:"미기록"}
  return <div className="min-w-0 space-y-4">
    {provisional&&<header className="space-y-1"><h2 className="font-semibold">실행 중 · 잠정</h2><p className="text-sm tabular-nums">asOf {resultTime(provisional.asOf)} · 집계 마감 {resultTime(provisional.closedThrough)} (KST)</p><p className="text-xs text-muted-foreground">현재 요청은 집계 마감까지의 예정 도착입니다. 결과 대기·미발행 포함은 진행 중 요청을 포함하며 이후 성공으로 바뀔 수 있습니다. 요청 배출 전에는 판정하지 않습니다.</p></header>}
    <div className="overflow-x-auto">
      <table aria-label="서비스별 결과" className="w-full text-left text-sm tabular-nums">
        <thead><tr>{["서비스","D1 판정",provisional?"적용 SLO":"선언 SLO",provisional?"성공 / 필요":"성공 / 요청",provisional?"현재 요청 · 도달 상태":"완료 지연 p95 / p50 (ms)",provisional?"요청 결과":"클라이언트 증거·요청 결과",provisional?"부하 발행률":"발행 증거 비율"].map(label=><th scope="col" key={label} className="whitespace-nowrap p-2 font-medium">{label}</th>)}</tr></thead>
        <tbody>{results.map(({row,verdict,declaredSLO,appliedSLO,success,offered,fraction,p95,p50,outcomes:counts,fidelity})=>{
          const total=provisional?finite(offered):finite(fidelity.issued)??finite(row.original.clientRows)
          return <tr key={row.source} className="border-t align-top">
            <th scope="row" className="max-w-52 break-words p-2 font-medium">{onSelect?<button className="min-h-11 max-w-52 whitespace-normal break-words text-left underline focus-visible:outline-2 focus-visible:outline-primary" onClick={()=>onSelect(row.name)}>{row.name} 결과 자세히</button>:row.name}</th>
            <td className={`whitespace-nowrap p-2 font-medium ${!provisional&&verdict==="FAIL"?"text-destructive":""}`}>{provisional?"진행 중":resultWord(verdict)}</td>
            <td className="whitespace-nowrap p-2">{provisional?sloText(row.original.slo):<>{declaredSLO?sloText(declaredSLO):"미기록"}<span className="block text-xs text-muted-foreground">{appliedSLO?`적용 확인: ${sloText(appliedSLO)}`:"적용 미확인"}</span></>}</td>
            <td className="min-w-32 whitespace-nowrap p-2"><span>{resultNumber(success)} / {resultNumber(provisional?row.original.requiredSuccessfulRank:offered)}</span><div className="text-xs text-muted-foreground">{resultPercent(fraction)}</div></td>
            <td className="whitespace-nowrap p-2">{provisional?<>{resultNumber(offered)}<span className={`block text-xs ${row.original.reachable===false?"text-destructive":"text-muted-foreground"}`}>{row.original.reachable===true?"도달 가능":row.original.reachable===false?"도달 불가":"미기록"}</span></>:<>{resultNumber(p95,"ms")} / {resultNumber(p50,"ms")}<span className="block text-xs text-muted-foreground">p95 표본 {resultNumber(row.metrics.find(metric=>metric.key.endsWith(".p95"))?.denominator)}</span></>}</td>
            <td className="min-w-64 p-2">
              <div aria-hidden="true" className="mb-1 flex h-3 overflow-hidden rounded-sm bg-muted">{outcomes.map(([key,,color])=>{const count=finite(counts[key]);return count!==null&&total!==null&&total>0?<span key={key} className={color} style={{width:`${count/total*100}%`}}/>:null})}</div>
              <div className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">{outcomes.map(([key,label,color])=><span key={key} className="whitespace-nowrap"><i aria-hidden="true" className={`mr-1 inline-block size-2 ${color}`}/>{label} {resultNumber(counts[key])}</span>)}</div>
              <div className="mt-1 text-xs text-muted-foreground">{provisional?<>결과 대기·미발행 포함 {resultNumber(row.original.unfinishedOrUnsent)}<span className="block">위 결과 분류와 겹치는 집계 · 미발행 {resultNumber(fidelity.unissued)}</span></>:<><span className="block">클라이언트 수집 {resultNumber(row.original.clientRows)}</span><span className="block">클라이언트 행 누락 {resultNumber(fidelity.missingClientRows)} · 초과 {resultNumber(fidelity.extraClientRows)}</span><span className="block">발행 증거 부족 {resultNumber(fidelity.unissued)}</span><span className="block">gateway 관측 미기록</span></>}
                {typeof row.original.identityMismatchClientRows==="number"&&row.original.identityMismatchClientRows>0&&<span className="block">신원 불일치 행 {resultNumber(row.original.identityMismatchClientRows)}</span>}
              </div>
            </td>
            <td className="whitespace-nowrap p-2">{resultPercent(finite(fidelity.issuanceFraction))}<span className="block text-xs text-muted-foreground">{!provisional&&fidelity.status==="PARTIAL"?"부분 증거":resultWord(fidelity.status)}</span></td>
          </tr>
        })}{!results.length&&<tr><td colSpan={7} className="p-3 text-muted-foreground">서비스 결과 미기록</td></tr>}</tbody>
      </table>
    </div>
    <section aria-label="구간별 성공" className="space-y-2">
      <h3 className="text-sm font-semibold">구간별 성공률</h3>
      {parts.length?<div className="overflow-x-auto"><table aria-label="구간별 성공률" className="w-full text-left text-sm tabular-nums">
        <thead><tr><th scope="col" className="p-2 font-medium">서비스</th>{parts.map(id=><th scope="col" className="min-w-36 p-2 font-medium" key={id}>{id}</th>)}</tr></thead>
        <tbody>{results.map(({row})=><tr key={row.source} className="border-t"><th scope="row" className="max-w-52 break-words p-2 font-normal">{row.name}</th>{parts.map(id=>{const part=row.substages?.find(part=>part.id===id);return <td key={id} className="p-2"><ResultFraction value={ratio(undefined,part?.deadlineSuccess,part?.offered)}/><span className="text-xs text-muted-foreground">{resultNumber(part?.deadlineSuccess)} / {resultNumber(provisional?part?.requiredSuccessfulRank:part?.offered)}{provisional&&<> · 현재 요청 {resultNumber(part?.offered)}</>}</span></td>})}</tr>)}</tbody>
      </table></div>:<p className="text-sm text-muted-foreground">구간별 실측 미기록</p>}
    </section>
    {stage&&<ExperimentEnergy stage={stage}/>}
    {results.some(({fidelity})=>Object.keys(object(fidelity.generatorQuality)).length>0)&&<details className="text-xs"><summary className="min-h-11 cursor-pointer">생성기 발행 품질</summary><table aria-label="발행 지연 품질" className="w-full text-left tabular-nums"><thead><tr>{["서비스","허용 지연 (ms)","초과 / 발행",provisional?"잠정 품질":"게시 품질"].map(label=><th scope="col" className="p-2" key={label}>{label}</th>)}</tr></thead><tbody>{results.map(({row,fidelity})=>{const quality=object(fidelity.generatorQuality),tolerance=finite(object(quality.policy).latenessToleranceNS);return <tr key={row.source} className="border-t"><th scope="row" className="break-words p-2 font-normal">{row.name}</th><td className="p-2">{resultNumber(tolerance===null?null:tolerance/1e6)}</td><td className="p-2">{resultNumber(quality.exceeding)} / {resultNumber(quality.denominator)}</td><td className="p-2">{resultWord(quality.thresholdLabel)}</td></tr>})}</tbody></table></details>}
  </div>
}

export function ExperimentInjections({run,stage}:{run:Experiment;stage:ExperimentStage}) {
  const rows=injectionRows(run,stage)
  return <section className="space-y-2" aria-label="장애 주입 결과"><h3 className="text-sm font-semibold">장애 주입</h3>{rows.length?<div className="overflow-x-auto"><table className="w-full text-xs tabular-nums"><thead><tr>{["주입 ID","동작","결과","사유","예정 (s)","실제 (s)"].map(label=><th key={label} scope="col" className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{rows.map(row=><tr key={row.id} className="border-t"><th scope="row" className="p-2 text-left font-normal">{row.id}</th><td className="p-2">{row.action==="delete-serving-pod"?"서빙 파드 삭제":row.action}</td><td className="p-2">{outcomeWords[row.outcome]??"미기록"}</td><td className="break-words p-2">{row.reason}</td><td className="p-2">{row.scheduledOffsetSeconds===null?"미기록":formatMeasurementNumber(row.scheduledOffsetSeconds,"s")}</td><td className="p-2">{row.actualOffsetSeconds===null?"미기록":formatMeasurementNumber(row.actualOffsetSeconds,"s")}</td></tr>)}</tbody></table></div>:<span className="text-xs text-muted-foreground">{run.definition?"주입 없음":"미기록"}</span>}</section>
}

export function ExperimentJudgment({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const pairs=definitionComparisons(run,runs),recorded=recordedComparisons(run)
  const word=recorded===undefined?"비교 미기록":!recorded.length?"비교 없음":definitionComparisonReason(run,runs).includes("대상 게시 없음")?"대상 게시 없음":pairs.some(pair=>comparisonValidity(pair.chill,pair.control,pair.leftRun,pair.rightRun).limited)?"참고":"일치"
  return <strong aria-label="비교 상태" className="text-sm font-medium">{word}</strong>
}

export function ExperimentLimits({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {coverageCounts:c}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return null
  const reasons=[["규칙 다름",c.ruleDifferences],["규칙 미기록",c.ruleMissing],["정의 미기록",c.identityUnknown],["정의 다름",c.identityDifferences],["부하 다름",c.loadDifferences],["부하 미기록",c.loadUnknown],["길이 다름·미확인",c.durationDifferences],["대응 없음",c.unpaired]] as const
  const limited=reasons.filter(([,count])=>count>0)
  if(!limited.length)return null
  return <table aria-label="비교 제한 원인" className="w-full table-fixed text-left text-xs font-normal"><thead><tr><th scope="col" className="pr-3 font-normal">제한 원인</th><th scope="col" className="text-right font-normal">구간 수 (개)</th></tr></thead><tbody>{limited.map(([label,count])=><tr key={label}><th scope="row" className="break-words pr-3 font-normal">{label}</th><td className="text-right tabular-nums">{count}</td></tr>)}</tbody></table>
}

export function ExperimentCoverage({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {coverageCounts:c}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return null
  const groups=[["일치",c.matched],["참고",c.limited],["대응 없음",c.unpaired]] as const
  return <table aria-label="비교 구간 수" className="w-full table-fixed text-left text-xs"><thead><tr><th scope="col" className="font-normal">비교 상태</th><th scope="col" className="text-right font-normal">구간 수 (개)</th></tr></thead><tbody>{groups.map(([label,count])=><tr key={label}><th scope="row" className="break-words font-normal">{label}</th><td className="text-right tabular-nums">{count}</td></tr>)}</tbody></table>
}


export function ExperimentFacts({run}:{run:Experiment}) {
  const facts=run.facts
  const text=(value:unknown)=>typeof value==="string"||typeof value==="number"||typeof value==="bigint"?String(value):"미기록"
  const patchRows=(label:string,patch:Published|null|undefined):[string,string][]=>{
    if(patch==null)return []
    const entries=settingsPatchEntries(patch)
    return entries.length?entries.map(([path,value])=>[`${label} · ${settingsPatchKeyLabel(path)} (${path})`,value===null?"null":publishedText(value)]):[[label,"변경 없음"]]
  }
  const rows:[string,string][]=[
    ["이름",facts?.name??run.definition?.run.name??"미기록"],
    ["질문",facts?.question??run.definition?.spec.question??"미기록"],
    ...patchRows("Run 운영 설정 패치",run.definition?.spec.settingsPatch),
    ...(run.definition?.spec.stages.flatMap(stage=>patchRows(`단계 운영 설정 패치 · ${stage.id}`,stage.settingsPatch))??[]),
    ...(facts?.stages?.length?facts.stages.flatMap(stage=>[
      ["단계 ID",stage.id],
      ["단계 행",stage.row??"미기록"],
      ["변경 설정",definitionPolicy(stage,run)],
      ["부하 시나리오 이름",text(stage.loadScenarioRef?.name)],
      ["부하 시나리오 UID",text(stage.loadScenarioRef?.uid)],
    ] as [string,string][]):run.definition?.spec.stages.map(stage=>[`변경 설정 · ${stage.id}`,definitionPolicy(stage,run)] as [string,string])??[["변경 설정","미기록"] as [string,string]]),
    ...(facts?.sloSources?.length?facts.sloSources.flatMap(source=>[
      ["SLO 출처 이름",source.name],
      ["SLO 출처 UID",source.uid],
      ["SLO 출처 정의",text(source.generation)],
      ["SLO 백분위",text(source.slo?.percentile)],
      ["SLO 목표 지연",typeof source.slo?.targetLatencyNanos==="number"||typeof source.slo?.targetLatencyNanos==="bigint"?formatMeasurementDuration(source.slo.targetLatencyNanos):"미기록"],
    ] as [string,string][]):[["SLO 출처","미기록"] as [string,string]]),
    ...(facts?.loadSources?.length?facts.loadSources.flatMap(source=>[["부하 출처 이름",source.name],["부하 출처 UID",source.uid]] as [string,string][]):[["부하 출처","미기록"] as [string,string]]),
  ]
  return <details className="min-w-0 text-xs font-normal"><summary className="min-h-10 cursor-pointer">질문·출처</summary><table aria-label="기록된 실험 사실" className="w-full table-fixed text-left text-xs [&_th]:break-words [&_td]:[overflow-wrap:anywhere]"><thead><tr><th className="w-1/3 p-1 font-normal">항목</th><th className="p-1 font-normal">값</th></tr></thead><tbody>{rows.map(([label,value],index)=><tr key={index} className="border-t"><th scope="row" className="p-1 font-normal">{label}</th><td className="p-1">{value}</td></tr>)}</tbody></table></details>
}

export function ExperimentMetadata({run,stage}:{run:Experiment;stage:ExperimentStage}) {
  const parts=stageLoadParts(stage,run),rules=object(stage.kpis.measurementRules)
  const loadReference=run.facts?.stages?.find(item=>item.id===stage.id)?.loadScenarioRef
  const loadMissing=!run.definition&&loadReference?"목록 미제공":"게시 기록 없음"
  const number=(value:number)=>value.toLocaleString("ko-KR",{maximumFractionDigits:2})
  const coverage=object(rules.energyCoverage)
  const ruleRows=[
    ["실행기 소스 버전",typeof rules.harnessRepositoryCommit==="string"?rules.harnessRepositoryCommit:"미기록","—"],
    ["전력 결손 비율 상한",typeof coverage.maxGapFraction==="number"&&Number.isFinite(coverage.maxGapFraction)?number(coverage.maxGapFraction*100):"미기록","%"],
    ["단일 전력 공백 상한",typeof coverage.maxSingleGapSeconds==="number"&&Number.isFinite(coverage.maxSingleGapSeconds)?number(coverage.maxSingleGapSeconds):"미기록","s"],
    ["근거 출처",rules.evidenceSource==="api"?"공개 API":rules.evidenceSource==null?"미기록":"원문 확인","—"],
    ["GC 집계 방식",rules.gcMeaning==="runtime-metrics-delta"?"런타임 누적값 차이":rules.gcMeaning==null?"미기록":"원문 확인","—"],
  ]
  return <div className="min-w-0 space-y-2 text-xs">
    <table aria-label="단계 변경 설정" className="w-full table-fixed text-left"><tbody><tr className="border-t"><th scope="row" className="w-1/3 p-1 font-normal">변경 설정</th><td className="break-words p-1">{definitionPolicy(stage,run)}</td></tr></tbody></table>
    <table aria-label="측정 규칙" className="w-full text-left tabular-nums"><thead><tr>{["항목","값","단위"].map(label=><th key={label} scope="col" className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{ruleRows.map(([label,value,unit])=><tr key={label} className="border-t"><th scope="row" className="p-1 font-normal">{label}</th><td className="break-all p-1">{value}</td><td className="p-1">{unit}</td></tr>)}</tbody></table>
    <table aria-label="계획 부하" className="w-full table-fixed text-left tabular-nums [&_td]:[overflow-wrap:anywhere]"><thead><tr>{["구간","서비스","길이 (s)","요청률 (req/s)"].map(label=><th key={label} className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{parts.length?parts.flatMap(part=>Object.entries(part.rates).map(([name,value])=><tr key={`${part.id}/${name}`} className="border-t"><th className="p-1 font-normal">{part.id}</th><td className="break-all p-1">{name}</td><td className="p-1">{number(part.durationSeconds)}</td><td className="p-1">{number(value)}</td></tr>)):<tr><td colSpan={4} title={loadReference?"부하 시나리오 참조만 목록에 제공됩니다. 상세에서 계획 구간을 확인하세요.":undefined}>{loadMissing}</td></tr>}</tbody></table>
    <table aria-label="서비스 식별자·정의" className="w-full text-left tabular-nums"><thead><tr>{["서비스","식별자","정의"].map(label=><th key={label} scope="col" className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{experimentRows(stage).map(row=><tr key={row.source} className="border-t"><th scope="row" className="break-all p-1 font-normal">{row.name}</th><td className="break-all p-1">{publishedText(row.uid)||"미기록"}</td><td className="break-all p-1">{publishedText(row.definition)||"미기록"}</td></tr>)}</tbody></table>
    {!!run.definition?.workloads.length&&<table aria-label="정의 출처 SLO" className="w-full table-fixed text-left tabular-nums [&_td]:[overflow-wrap:anywhere]"><caption className="text-left" title="게시 당시 동결된 서비스 정의입니다. 요청 원값에 서비스 UID가 없으면 단계 적용 SLO로 연결하지 않습니다.">정의 출처 SLO</caption><thead><tr>{["서비스","식별자","세대","분위수","목표 지연 (ms)"].map(label=><th key={label} className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{run.definition.workloads.map(workload=><tr key={workload.uid} className="border-t"><th className="break-all p-1 font-normal">{workload.name}</th><td className="p-1">{workload.uid}</td><td className="p-1">{String(workload.generation)}</td><td className="p-1">{workload.slo.percentile}</td><td className="p-1">{number(Number(workload.slo.targetLatencyNanos)/1e6)}</td></tr>)}</tbody></table>}
  </div>
}


export function ExperimentResultRow({run,runs=[run],pending=false,error=false,offset=0,service=""}:{run:Experiment;runs?:Experiment[];pending?:boolean;error?:boolean;offset?:number;service?:string}) {
  const recorded=recordedComparisons(run),pairs=definitionComparisons(run,runs)
  const state=error?"조회 실패":pending?"조회 중":recorded===undefined?(run.facts?"상세에서 확인":"비교 미기록"):!recorded.length?"비교 없음":pairs.length?"기록된 비교쌍": "상세에서 확인"
  const policies=[...new Set((run.facts?.stages??run.definition?.spec.stages??run.stages).map(stage=>definitionPolicy(stage,run)))]
  const outcome=experimentOutcome(run)
  return <tr className="border-t">
    <th scope="row" className="max-w-64 break-all p-3 text-left font-medium"><Link prefetch={false} className="inline-flex min-h-11 items-center underline" href={`/experiments/results?${new URLSearchParams({a:run.id,offset:String(offset),...(service?{service}:{})})}`}>{run.facts?.name??run.definition?.run.name??run.id}</Link></th>
    <td data-label="기간 (KST)" className="p-3 text-xs">{formatMeasurementTime(run.from)}<br/>{formatMeasurementTime(run.to)}</td>
    <td data-label="정책" className="max-w-64 break-words p-3">{policies.join(" / ")||"미기록"}<span className="block text-xs text-muted-foreground">{run.stages.length}단계</span></td>
    {(["execution","measurement","cleanup"] as const).map((axis,index)=><td key={axis} data-label={["실행","측정","정리"][index]} className="p-3">{outcomeLabel(outcome[axis])}</td>)}
    <td data-label="비교 상태" className="p-3">{state}</td>
  </tr>
}
