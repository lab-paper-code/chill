"use client"

import { ApiError, useChillQueries } from "@/lib/client"
import { Button } from "@/components/ui/button"
import { TriangleAlert } from "lucide-react"
import { campaignScoreboard, decodeExperiment, definitionComparisonReason, experimentDifference, experimentOutcome, experimentPath, historicalPublicationIDs, injectionRows, type Experiment, type ExperimentStage } from "@/lib/experiments"
import { formatMeasurementNumber } from "@/lib/measurements"

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
  return <dl aria-label={stage?"단계 판정 3축":"실험 판정 3축"} className="flex flex-wrap gap-x-4 gap-y-1 text-xs">{([["execution","실행"],["measurement","측정"],["cleanup","정리"]] as const).map(([axis,label])=><div key={axis} className="flex items-center gap-1"><dt className="text-muted-foreground">{label}</dt><dd title={outcome[axis].reason} className={["Failed","FAIL","INVALID"].includes(outcome[axis].value)?"font-medium text-destructive":""}>{outcomeWords[outcome[axis].value]??"미기록"}</dd></div>)}</dl>
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

export function ExperimentLimits({run,runs=[run],detail=false}:{run:Experiment;runs?:Experiment[];detail?:boolean}) {
  const {coverageCounts:c}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return null
  const reasons=[["규칙 다름",c.ruleDifferences],["규칙 미기록",c.ruleMissing],["정의 미기록",c.identityUnknown],["정의 다름",c.identityDifferences],["부하 다름",c.loadDifferences],["부하 미기록",c.loadUnknown],["길이 다름·미확인",c.durationDifferences],["대응 없음",c.unpaired]] as const
  const limited=reasons.filter(([,count])=>count>0)
  if(!limited.length)return null
  const table=<table aria-label="비교 제한 원인" className="w-auto text-left text-xs font-normal tabular-nums"><thead><tr><th scope="col" className="pr-3 font-normal">제한 원인</th><th scope="col" className="text-right font-normal">구간 수</th></tr></thead><tbody>{limited.map(([label,count])=><tr key={label}><th scope="row" className="pr-3 font-normal">{label}</th><td className="text-right">{count}</td></tr>)}</tbody></table>
  if(detail)return table
  return <div className="group relative w-fit text-muted-foreground" tabIndex={0} aria-label={`제한 ${c.limited+c.unpaired}구간`}><span className="inline-flex items-center gap-1 whitespace-nowrap text-xs"><TriangleAlert className="size-3" aria-hidden="true"/>{c.limited+c.unpaired}</span><div role="tooltip" className="invisible absolute bottom-full left-0 z-30 mb-1 w-max rounded border bg-popover p-3 text-popover-foreground shadow-sm group-hover:visible group-focus-within:visible">{table}</div></div>
}

export function ExperimentCoverage({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {coverageCounts:c}=campaignScoreboard(run,runs),total=c.matched+c.limited+c.unpaired
  if(!run.definition&&run.comparisons===undefined)return null
  const groups=[{label:"일치",count:c.matched,tone:"bg-primary"},{label:"참고",count:c.limited,tone:"bg-heat"},{label:"대응 없음",count:c.unpaired,tone:"bg-muted-foreground/35"}]
  return <div className="flex h-2 w-full min-w-24 overflow-hidden rounded-sm bg-muted" role="img" aria-label={groups.map(group=>`${group.label} ${group.count}`).join(" · ")} title={groups.map(group=>`${group.label} ${group.count}`).join(" · ")}>{groups.filter(group=>group.count>0).map(group=><span key={group.label} className={group.tone} style={{width:`${100*group.count/total}%`}}/>)}</div>
}

export function ExperimentConclusion({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {summary,referenceSummary,reference,coverageCounts}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return <table className="w-full text-xs text-muted-foreground" aria-label="실험 정의"><thead><tr>{["정책","비교","같은 부하"].map(label=><th key={label} className="text-left font-normal">{label}</th>)}</tr></thead><tbody><tr>{["정책","비교","같은 부하"].map(label=><td key={label}>미기록</td>)}</tr></tbody></table>
  if(!coverageCounts.pairs)return null
  const judged=reference?referenceSummary:summary
  return <table className="w-full text-xs tabular-nums" aria-label="실험 결론"><thead><tr>{["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"].map(label=><th key={label} className="px-2 pb-1 text-right font-normal text-muted-foreground">{label}</th>)}</tr></thead><tbody><tr>{[{value:judged.energyPercent,baseline:0,higher:false},{value:judged.successDelta,baseline:0,higher:true},{value:judged.p95Ratio,baseline:1,higher:false}].map((item,index)=><td key={index} className="p-1"><div className={`rounded border px-2 py-1.5 text-right ${experimentDifference(item.value,item.baseline,item.higher).tone}`}><strong>{experimentDifference(item.value,item.baseline,item.higher).text}</strong></div></td>)}</tr></tbody></table>
}

export function ExperimentComparisonLegend() {
  return <span className="text-xs text-muted-foreground" title="일치: 게시 정의의 부하 시나리오 UID·부하 생성기와 서비스 식별자·정의, 측정 규칙, 표시 구간 길이가 같음. 참고: 조건이 다르거나 미기록. 구간 막대는 일치·참고·대응 없음 순. 제한 원인은 중복될 수 있어요.">Δ 왼쪽−오른쪽 / p95 왼쪽÷오른쪽</span>
}
