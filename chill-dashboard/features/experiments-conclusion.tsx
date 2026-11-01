"use client"

import Link from "next/link"
import { ApiError, useChillQueries } from "@/lib/client"
import { Button } from "@/components/ui/button"
import { campaignScoreboard, decodeExperiment, definitionComparisonReason, experimentDifference, experimentOutcome, experimentPath, historicalPublicationIDs, injectionRows, experimentRows, observedStageRate, resultMetric, stageSeconds, type Experiment, type ExperimentStage } from "@/lib/experiments"
import { formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"

// The list already carries comparison endpoints and KPIs; only omitted historical publications need a GET.
export function useHistoricalExperiments(publications:Experiment[],catalog:Experiment[]=publications,enabled=true) {
  const ids=[...new Set(publications.flatMap(historicalPublicationIDs))].filter(id=>!catalog.some(run=>run.id===id)&&!publications.some(run=>run.id===id))
  const queries=useChillQueries<Experiment>(ids.map(experimentPath),{enabled,decoder:decodeExperiment})
  const fetched=queries.flatMap((query,index)=>query.data?.id===ids[index]?[query.data]:[])
  const failed=queries.filter(query=>query.error&&!(query.error instanceof ApiError&&query.error.status===404))
  return {runs:[...publications,...catalog.filter(run=>!publications.some(item=>item.id===run.id)),...fetched],pending:ids.length>0&&(!enabled||queries.some(query=>query.isPending)),error:failed[0]?.error??null,refetch:()=>{for(const query of failed)void query.refetch()}}
}

export function HistoricalExperimentError({refetch}:{refetch:()=>void}) {
  return <div role="alert" className="notice"><p>대상 게시 조회 실패</p><Button type="button" variant="outline" onClick={refetch}>다시 불러오기</Button></div>
}

const outcomeWords:Record<string,string>={Complete:"완료",Failed:"실패",PASS:"통과",FAIL:"실패",INVALID:"무효",NOT_EVALUATED:"미평가",Executed:"실행",Cancelled:"취소",Invalid:"무효"}

export function ExperimentOutcome({run,stage}:{run:Experiment;stage?:ExperimentStage}) {
  const outcome=experimentOutcome(run,stage)
  return <dl aria-label={stage?"단계 판정 3축":"실험 판정 3축"} className="flex flex-wrap gap-x-4 gap-y-1 text-sm">{([["execution","실행"],["measurement","측정"],["cleanup","정리"]] as const).map(([axis,label])=><div key={axis} className="flex items-start gap-2"><dt className="text-muted-foreground">{label}</dt><dd className={["Failed","FAIL","INVALID"].includes(outcome[axis].value)?"font-medium text-destructive":""}>{outcomeWords[outcome[axis].value]??"미기록"}{outcome[axis].reason&&<div className="text-xs font-normal text-muted-foreground">{outcome[axis].reason}</div>}</dd></div>)}</dl>
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

export function ExperimentAbsoluteValues({run,service="",onSelect}:{run:Experiment;service?:string;onSelect?:(stage:ExperimentStage,service:string)=>void}) {
  const number=(value:unknown,unit="")=>typeof value==="number"&&Number.isFinite(value)?unit==="req/s"?value>0&&value<.01?"0.01 미만":value.toLocaleString("ko-KR",{minimumFractionDigits:2,maximumFractionDigits:2}):formatMeasurementNumber(value,unit):"미제공"
  return <div className="min-w-0 space-y-2" aria-label="단계별 원값">{[...run.stages].sort((a,b)=>Date.parse(a.from)-Date.parse(b.from)).map((stage,index)=><details key={`${stage.id}/${index}`} open={index===0} className="min-w-0 rounded border p-2">
    <summary className="min-h-10 cursor-pointer break-all text-sm font-medium">{stage.id}</summary>
    <table className="mb-2 w-full text-left text-xs tabular-nums max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2"><tbody><tr><th className="font-normal text-muted-foreground">길이 (s)</th><td>{number(stageSeconds(stage),"s")}</td><th className="font-normal text-muted-foreground">에너지 (J)</th><td>{number(stage.kpis.wholePoolJoules,"J")}</td></tr><tr><th className="font-normal text-muted-foreground">시작 (KST)</th><td>{formatMeasurementTime(stage.from)}</td><th className="font-normal text-muted-foreground">종료 (KST)</th><td>{formatMeasurementTime(stage.to)}</td></tr></tbody></table>
    <table className="w-full text-sm tabular-nums max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2 max-md:[&>tbody>tr>th]:col-span-2 max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:flex-col max-md:[&>tbody>tr>td]:text-left max-md:[&>tbody>tr>td]:before:text-xs max-md:[&>tbody>tr>td]:before:text-muted-foreground max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]">
      <thead><tr>{["서비스","발행 요청률 (요청/s)","기한 내 성공률 (%)","성공 응답 p95 (ms)"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead>
      <tbody>{experimentRows(stage).filter(row=>!service||row.name===service).map(row=><tr key={row.source} className="border-t"><th className="max-w-64 break-words p-2 text-left font-normal">{onSelect?<button type="button" className="min-h-10 text-left underline" onClick={()=>onSelect(stage,row.name)}>{row.name}</button>:<Link prefetch={false} className="inline-flex min-h-10 items-center underline" href={`/experiments/results?${new URLSearchParams({a:run.id,al:run.id,as:stage.id,service:row.name})}`}>{row.name}</Link>}</th><td data-label="발행 요청률 (요청/s)" className="p-2 text-right">{number(observedStageRate(stage,row.name),"req/s")}</td><td data-label="기한 내 성공률 (%)" className="p-2 text-right">{number(typeof resultMetric(row,"success").value==="number"?Number(resultMetric(row,"success").value)*100:null,"%")}</td><td data-label="성공 응답 p95 (ms)" className="p-2 text-right">{number(resultMetric(row,"p95").value,"ms")}</td></tr>)}</tbody>
    </table>
  </details>)}</div>
}

export function ExperimentResultRow({run,runs,pending,error}:{run:Experiment;runs:Experiment[];pending:boolean;error:boolean}) {
  const score=campaignScoreboard(run,runs),c=score.coverageCounts
  const state=error?"조회 실패":pending?"조회 중":!run.definition&&run.comparisons===undefined?"미기록":c.matched?"일치":c.limited?"참고":score.conclusion==="대상 게시 없음"?"대상 없음":"비교 없음"
  const metrics=score.reference?score.referenceSummary:score.summary
  const outcome=experimentOutcome(run)
  if(!run.definition&&run.comparisons===undefined)return <tr className="border-t"><th scope="row" className="max-w-64 break-all p-3 text-left align-top font-medium"><Link prefetch={false} className="underline" href={`/experiments/results?${new URLSearchParams({a:run.id})}`}>{run.id}</Link><div className="mt-2 text-xs font-normal text-muted-foreground">비교・정책・같은 부하 미기록</div></th><td colSpan={11} className="min-w-0 p-3"><ExperimentAbsoluteValues run={run}/></td></tr>
  return <tr className="border-t">
    <th scope="row" className="p-3 text-left font-medium">{state}</th>
    <td data-label="실험" className="max-w-64 break-all p-3"><Link prefetch={false} className="underline" href={`/experiments/results?${new URLSearchParams({a:run.id})}`}>{run.definition?.run.name??run.id}</Link></td>
    {[c.matched,c.limited,c.unpaired].map((count,index)=><td key={index} data-label={["일치 (개)","참고 (개)","대응 없음 (개)"][index]} className="p-3 text-right tabular-nums">{pending||error?"미확인":state==="미기록"?"미기록":count}</td>)}
    {(["execution","measurement","cleanup"] as const).map((axis,index)=><td key={axis} data-label={["실행","측정","정리"][index]} className="p-3">{outcomeWords[outcome[axis].value]??"미기록"}{outcome[axis].reason&&<div className="text-xs text-muted-foreground">{outcome[axis].reason}</div>}</td>)}
    {[{value:metrics.energyPercent,base:0,higher:false},{value:metrics.successDelta,base:0,higher:true},{value:metrics.p95Ratio,base:1,higher:false}].map((metric,index)=><td key={index} data-label={["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"][index]} className="p-3 text-right tabular-nums">{error||pending?"미확인":metric.value===null?"미기록":experimentDifference(metric.value,metric.base,metric.higher).text}</td>)}
    <td data-label="제한 사유" className="max-w-80 break-words p-3">{pending||error?"미확인":definitionComparisonReason(run,runs)}{c.limited>0&&<ExperimentLimits run={run} runs={runs}/>}</td>
  </tr>
}
