"use client"

import Link from "next/link"
import { ApiError, useChillQueries } from "@/lib/client"
import { Button } from "@/components/ui/button"
import { campaignScoreboard, decodeExperiment, definitionComparisonReason, definitionComparisons, definitionPolicy, experimentControlLabels, experimentDifference, experimentOutcome, experimentPath, historicalPublicationIDs, injectionRows, experimentRows, frozenP95SLO, object, observedStageRate, publishedText, resultMetric, stageLoadParts, stageSeconds, type Experiment, type ExperimentStage } from "@/lib/experiments"
import { formatMeasurementDuration, formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"

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

export function ExperimentInjections({run,stage}:{run:Experiment;stage:ExperimentStage}) {
  const rows=injectionRows(run,stage)
  return <section className="space-y-2" aria-label="장애 주입 결과"><h3 className="text-sm font-semibold">장애 주입</h3>{rows.length?<div className="overflow-x-auto"><table className="w-full text-xs tabular-nums"><thead><tr>{["주입 ID","동작","결과","사유","예정 (s)","실제 (s)"].map(label=><th key={label} scope="col" className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{rows.map(row=><tr key={row.id} className="border-t"><th scope="row" className="p-2 text-left font-normal">{row.id}</th><td className="p-2">{row.action==="delete-serving-pod"?"서빙 파드 삭제":row.action}</td><td className="p-2">{outcomeWords[row.outcome]??"미기록"}</td><td className="break-words p-2">{row.reason}</td><td className="p-2">{row.scheduledOffsetSeconds===null?"미기록":formatMeasurementNumber(row.scheduledOffsetSeconds,"s")}</td><td className="p-2">{row.actualOffsetSeconds===null?"미기록":formatMeasurementNumber(row.actualOffsetSeconds,"s")}</td></tr>)}</tbody></table></div>:<span className="text-xs text-muted-foreground">{run.definition?"주입 없음":"미기록"}</span>}</section>
}

export function ExperimentJudgment({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {conclusion,reference}=campaignScoreboard(run,runs),reason=definitionComparisonReason(run,runs)
  const word=!run.definition&&run.comparisons===undefined?"미기록":reason.includes("대상 게시 없음")?"대상 게시 없음":conclusion
  return <div className="flex items-center gap-2" title={reason}><strong aria-label="실험 판정" className={`text-xl font-semibold ${word==="SLO 악화"?"text-destructive":["결과 부족","비교 없음","미기록","대상 게시 없음"].includes(word)?"text-muted-foreground":"text-primary"}`}>{word}</strong>{reference&&<span className="rounded border border-heat/50 px-1.5 py-0.5 text-xs text-muted-foreground">참고</span>}</div>
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

export function ExperimentConclusion({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {summary,referenceSummary,reference,coverageCounts}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return <table className="w-full text-xs text-muted-foreground" aria-label="실험 정의"><thead><tr>{["정책","비교","같은 부하"].map(label=><th key={label} className="text-left font-normal">{label}</th>)}</tr></thead><tbody><tr>{["정책","비교","같은 부하"].map(label=><td key={label}>미기록</td>)}</tr></tbody></table>
  if(!coverageCounts.pairs)return null
  const judged=reference?referenceSummary:summary
  return <table className="w-full text-xs tabular-nums" aria-label="실험 결론"><thead><tr>{["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"].map(label=><th key={label} className="px-2 pb-1 text-right font-normal text-muted-foreground">{label}</th>)}</tr></thead><tbody><tr>{[{value:judged.energyPercent,baseline:0,higher:false},{value:judged.successDelta,baseline:0,higher:true},{value:judged.p95Ratio,baseline:1,higher:false}].map((item,index)=><td key={index} className="p-1"><div className={`rounded border px-2 py-1.5 text-right ${experimentDifference(item.value,item.baseline,item.higher).tone}`}><strong>{experimentDifference(item.value,item.baseline,item.higher).text}</strong></div></td>)}</tr></tbody></table>
}

export function ExperimentComparisonLegend() {
  return <span className="text-xs text-muted-foreground" title="일치: 게시 정의의 부하 시나리오 UID·부하 생성기와 서비스 식별자·정의, 측정 규칙, 표시 구간 길이가 같음. 참고: 조건이 다르거나 미기록. 제한 원인은 중복될 수 있어요.">Δ 왼쪽−오른쪽 / p95 왼쪽÷오른쪽</span>
}

export function ExperimentFacts({run}:{run:Experiment}) {
  const facts=run.facts
  const text=(value:unknown)=>typeof value==="string"||typeof value==="number"||typeof value==="bigint"?String(value):"미기록"
  const controlRows=(control:unknown):[string,string][]=>{
    if(!control)return [["제어 정책","미기록"]]
    const policy=object(control),controls=object(policy.controls)
    return [
      ...Object.entries(experimentControlLabels).map(([key,label])=>[label,controls[key]===true?"켬":controls[key]===false?"끔":"미기록"] as [string,string]),
      ["축소 여유 비율",text(policy.scaleDownHeadroomFraction)],
      ...(Object.keys(object(policy.minimumDemand)).length?Object.entries(object(policy.minimumDemand)).map(([uid,value])=>[`최소 수요 (req/s) · ${uid}`,text(value)] as [string,string]):[["최소 수요 (req/s)","미기록"] as [string,string]]),
      ...(Array.isArray(policy.sourceIDs)&&policy.sourceIDs.length?policy.sourceIDs.map(id=>["제어 출처 ID",text(id)] as [string,string]):[["제어 출처 ID","미기록"] as [string,string]]),
    ]
  }
  const rows:[string,string][]=[
    ["이름",facts?.name??"미기록"],
    ["질문",facts?.question??"미기록"],
    ...controlRows(facts?.control),
    ...(facts?.stages?.length?facts.stages.flatMap(stage=>[
      ["단계 ID",stage.id],
      ["단계 행",stage.row??"미기록"],
      ["단계 정책",definitionPolicy(stage,run)],
      ...(stage.control?controlRows(stage.control):[]),
      ["부하 시나리오 이름",text(stage.loadScenarioRef?.name)],
      ["부하 시나리오 UID",text(stage.loadScenarioRef?.uid)],
    ] as [string,string][]):[["단계 정책","미기록"] as [string,string]]),
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
    <table aria-label="측정 규칙" className="w-full text-left tabular-nums"><thead><tr>{["항목","값","단위"].map(label=><th key={label} scope="col" className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{ruleRows.map(([label,value,unit])=><tr key={label} className="border-t"><th scope="row" className="p-1 font-normal">{label}</th><td className="break-all p-1">{value}</td><td className="p-1">{unit}</td></tr>)}</tbody></table>
    <table aria-label="계획 부하" className="w-full table-fixed text-left tabular-nums [&_td]:[overflow-wrap:anywhere]"><thead><tr>{["구간","서비스","길이 (s)","요청률 (req/s)","α"].map(label=><th key={label} className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{parts.length?parts.flatMap(part=>Object.entries(part.rates??part.alpha??{}).map(([name,value])=><tr key={`${part.id}/${name}`} className="border-t"><th className="p-1 font-normal">{part.id}</th><td className="break-all p-1">{name}</td><td className="p-1">{number(part.durationSeconds)}</td><td className="p-1">{part.rates?number(value):"미기록"}</td><td className="p-1">{part.alpha?number(value):"미기록"}</td></tr>)):<tr><td colSpan={5} title={loadReference?"부하 시나리오 참조만 목록에 제공됩니다. 상세에서 계획 구간을 확인하세요.":undefined}>{loadMissing}</td></tr>}</tbody></table>
    <table aria-label="서비스 식별자·정의" className="w-full text-left tabular-nums"><thead><tr>{["서비스","식별자","정의"].map(label=><th key={label} scope="col" className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{experimentRows(stage).map(row=><tr key={row.source} className="border-t"><th scope="row" className="break-all p-1 font-normal">{row.name}</th><td className="break-all p-1">{publishedText(row.uid)||"미기록"}</td><td className="break-all p-1">{publishedText(row.definition)||"미기록"}</td></tr>)}</tbody></table>
    {!!run.definition?.workloads.length&&<table aria-label="정의 출처 SLO" className="w-full table-fixed text-left tabular-nums [&_td]:[overflow-wrap:anywhere]"><caption className="text-left" title="게시 당시 동결된 서비스 정의입니다. 요청 원값에 서비스 UID가 없으면 단계 적용 SLO로 연결하지 않습니다.">정의 출처 SLO</caption><thead><tr>{["서비스","식별자","세대","분위수","목표 지연 (ms)"].map(label=><th key={label} className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{run.definition.workloads.map(workload=><tr key={workload.uid} className="border-t"><th className="break-all p-1 font-normal">{workload.name}</th><td className="p-1">{workload.uid}</td><td className="p-1">{String(workload.generation)}</td><td className="p-1">{workload.slo.percentile}</td><td className="p-1">{number(Number(workload.slo.targetLatencyNanos)/1e6)}</td></tr>)}</tbody></table>}
  </div>
}

export function ExperimentAbsoluteValues({run,service="",onSelect,compact=false}:{run:Experiment;service?:string;onSelect?:(stage:ExperimentStage,service:string)=>void;compact?:boolean}) {
  const number=(value:unknown,unit="")=>typeof value==="number"&&Number.isFinite(value)?unit==="req/s"?value>0&&value<.01?"0.01 미만":value.toLocaleString("ko-KR",{minimumFractionDigits:2,maximumFractionDigits:2}):formatMeasurementNumber(value,unit):"미제공"
  return <div className="min-w-0 space-y-2" aria-label="단계별 원값">{[...run.stages].sort((a,b)=>Date.parse(a.from)-Date.parse(b.from)).map((stage,index)=><details key={`${stage.id}/${index}`} open={index===0} className="min-w-0 rounded border p-2">
    <summary className="min-h-10 cursor-pointer break-all text-sm font-medium">{stage.id}</summary>
    <table className="mb-2 w-full text-left text-xs tabular-nums max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2"><tbody><tr><th className="font-normal text-muted-foreground">정책</th><td colSpan={3}>{definitionPolicy(stage,run)}</td></tr><tr><th className="font-normal text-muted-foreground">길이 (s)</th><td>{number(stageSeconds(stage),"s")}</td><th className="font-normal text-muted-foreground">에너지 (J)</th><td>{number(stage.kpis.wholePoolJoules,"J")}</td></tr><tr><th className="font-normal text-muted-foreground">시작 (KST)</th><td>{formatMeasurementTime(stage.from)}</td><th className="font-normal text-muted-foreground">종료 (KST)</th><td>{formatMeasurementTime(stage.to)}</td></tr></tbody></table>
    <table className="w-full text-sm tabular-nums max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2 max-md:[&>tbody>tr>th]:col-span-2 max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:flex-col max-md:[&>tbody>tr>td]:text-left max-md:[&>tbody>tr>td]:before:text-xs max-md:[&>tbody>tr>td]:before:text-muted-foreground max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]">
      <thead><tr>{["서비스","발행 요청률 (요청/s)","기한 내 성공률 (%)","성공 응답 p95 (ms)","적용 SLO p95 (ms)"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead>
      <tbody>{experimentRows(stage).filter(row=>!service||row.name===service).map(row=>{
        const slo=frozenP95SLO(row,run)
        return <tr key={row.source} className="border-t"><th className="max-w-64 break-words p-2 text-left font-normal">{onSelect?<button type="button" className="min-h-10 text-left underline" onClick={()=>onSelect(stage,row.name)}>{row.name}</button>:<Link prefetch={false} className="inline-flex min-h-10 items-center underline" href={`/experiments/results?${new URLSearchParams({a:run.id,al:run.id,as:stage.id,service:row.name})}`}>{row.name}</Link>}</th><td data-label="발행 요청률 (요청/s)" className="p-2 text-right">{number(observedStageRate(stage,row.name),"req/s")}</td><td data-label="기한 내 성공률 (%)" className="p-2 text-right">{number(typeof resultMetric(row,"success").value==="number"?Number(resultMetric(row,"success").value)*100:null,"%")}</td><td data-label="성공 응답 p95 (ms)" className="p-2 text-right">{number(resultMetric(row,"p95").value,"ms")}</td><td data-label="적용 SLO p95 (ms)" className="p-2 text-right" title={slo===null?"요청 원값의 서비스 UID와 동결 정의를 연결할 근거가 없습니다. 정의 출처는 상세에서 별도로 확인하세요.":undefined}>{slo===null?"적용 SLO 미확인":number(slo,"ms")}</td></tr>
      })}</tbody>
    </table>
    {!compact&&<details className="mt-2 min-w-0"><summary className="min-h-10 cursor-pointer text-xs">측정 규칙·부하·정의 출처</summary><ExperimentMetadata run={run} stage={stage}/></details>}
  </details>)}</div>
}

export function ExperimentResultRow({run,runs,pending,error}:{run:Experiment;runs:Experiment[];pending:boolean;error:boolean}) {
  const score=campaignScoreboard(run,runs),c=score.coverageCounts
  const state=error?"조회 실패":pending?"조회 중":!run.definition&&run.comparisons===undefined?"미기록":c.matched?"일치":c.limited?"참고":score.conclusion==="대상 게시 없음"?"대상 없음":"비교 없음"
  const metrics=score.reference?score.referenceSummary:score.summary
  const outcome=experimentOutcome(run)
  if(!definitionComparisons(run,runs).length&&(run.definition||run.comparisons===undefined))return <tr className="border-t"><th scope="row" className="w-64 max-w-64 break-all p-3 text-left align-top font-medium"><Link prefetch={false} className="underline" href={`/experiments/results?${new URLSearchParams({a:run.id})}`}>{run.facts?.name??run.definition?.run.name??run.id}</Link><div className="mt-2 text-xs font-normal text-muted-foreground">{run.facts?"비교 미기록":run.definition?state:"비교・정책・같은 부하 미기록"}</div>{run.outcome&&<div className="mt-3 font-normal"><ExperimentOutcome run={run}/></div>}<ExperimentFacts run={run}/></th><td colSpan={11} className="min-w-0 p-3"><ExperimentAbsoluteValues run={run} compact/></td></tr>
  return <tr className="border-t">
    <th scope="row" className="p-3 text-left font-medium">{state}</th>
    <td data-label="실험" className="max-w-64 break-all p-3"><Link prefetch={false} className="underline" href={`/experiments/results?${new URLSearchParams({a:run.id})}`}>{run.facts?.name??run.definition?.run.name??run.id}</Link><ExperimentFacts run={run}/></td>
    {[c.matched,c.limited,c.unpaired].map((count,index)=><td key={index} data-label={["일치 (개)","참고 (개)","대응 없음 (개)"][index]} className="p-3 text-right tabular-nums">{pending||error?"미확인":state==="미기록"?"미기록":count}</td>)}
    {(["execution","measurement","cleanup"] as const).map((axis,index)=><td key={axis} data-label={["실행","측정","정리"][index]} className="p-3">{outcomeLabel(outcome[axis])}{outcome[axis].reason&&outcome[axis].reason!=="SLOFailed"&&<div className="text-xs text-muted-foreground">{outcome[axis].reason}</div>}</td>)}
    {[{value:metrics.energyPercent,base:0,higher:false},{value:metrics.successDelta,base:0,higher:true},{value:metrics.p95Ratio,base:1,higher:false}].map((metric,index)=><td key={index} data-label={["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"][index]} className="p-3 text-right tabular-nums">{error||pending?"미확인":metric.value===null?"미기록":experimentDifference(metric.value,metric.base,metric.higher).text}</td>)}
    <td data-label="제한 사유" className="max-w-80 break-words p-3">{pending||error?"미확인":definitionComparisonReason(run,runs)}{c.limited>0&&<ExperimentLimits run={run} runs={runs}/>}</td>
  </tr>
}
