import { TriangleAlert } from "lucide-react"
import { campaignScoreboard, experimentDifference, type Experiment } from "@/lib/experiments"

export function ExperimentJudgment({run}:{run:Experiment}) {
  const {conclusion,reference}=campaignScoreboard(run)
  return <div className="flex items-center gap-2"><strong aria-label="실험 판정" className={`text-xl font-semibold ${conclusion==="SLO 악화"?"text-destructive":conclusion==="결과 부족"||conclusion==="비교 없음"?"text-muted-foreground":"text-primary"}`}>{conclusion}</strong>{reference&&<span className="rounded border border-heat/50 px-1.5 py-0.5 text-xs text-muted-foreground">참고</span>}</div>
}

export function ExperimentLimits({run,detail=false}:{run:Experiment;detail?:boolean}) {
  const {coverageCounts:c}=campaignScoreboard(run)
  const reasons=[["규칙 다름",c.ruleDifferences],["규칙 미확인",c.ruleMissing],["정의 미확인",c.identityUnknown],["정의 다름",c.identityDifferences],["부하 다름",c.loadDifferences],["부하 미확인",c.loadUnknown],["길이 다름·미확인",c.durationDifferences],["대응 없음",c.unpaired]] as const
  const limited=reasons.filter(([,count])=>count>0)
  if(!limited.length)return null
  const table=<table aria-label="비교 제한 원인" className="w-auto text-left text-xs font-normal tabular-nums"><thead><tr><th scope="col" className="pr-3 font-normal">제한 원인</th><th scope="col" className="text-right font-normal">구간 수</th></tr></thead><tbody>{limited.map(([label,count])=><tr key={label}><th scope="row" className="pr-3 font-normal">{label}</th><td className="text-right">{count}</td></tr>)}</tbody></table>
  if(detail)return table
  return <div className="group relative w-fit text-muted-foreground" tabIndex={0} aria-label={`제한 ${c.limited+c.unpaired}구간`}><span className="inline-flex items-center gap-1 whitespace-nowrap text-xs"><TriangleAlert className="size-3" aria-hidden="true"/>{c.limited+c.unpaired}</span><div role="tooltip" className="invisible absolute bottom-full left-0 z-30 mb-1 w-max rounded border bg-popover p-3 text-popover-foreground shadow-sm group-hover:visible group-focus-within:visible">{table}</div></div>
}

export function ExperimentCoverage({run}:{run:Experiment}) {
  const {coverageCounts:c}=campaignScoreboard(run),total=c.matched+c.limited+c.unpaired
  const groups=[{label:"일치",count:c.matched,tone:"bg-primary"},{label:"참고",count:c.limited,tone:"bg-heat"},{label:"대응 없음",count:c.unpaired,tone:"bg-muted-foreground/35"}]
  return <div className="flex h-2 w-full min-w-24 overflow-hidden rounded-sm bg-muted" role="img" aria-label={groups.map(group=>`${group.label} ${group.count}`).join(" · ")} title={groups.map(group=>`${group.label} ${group.count}`).join(" · ")}>{groups.filter(group=>group.count>0).map(group=><span key={group.label} className={group.tone} style={{width:`${100*group.count/total}%`}}/>)}</div>
}

export function ExperimentConclusion({run}:{run:Experiment}) {
  const {summary,referenceSummary,reference,coverageCounts}=campaignScoreboard(run)
  if(!coverageCounts.pairs)return null
  const judged=reference?referenceSummary:summary
  return <table className="w-full text-xs tabular-nums" aria-label="실험 결론"><thead><tr>{["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"].map(label=><th key={label} className="px-2 pb-1 text-right font-normal text-muted-foreground">{label}</th>)}</tr></thead><tbody><tr>{[{value:judged.energyPercent,baseline:0,higher:false},{value:judged.successDelta,baseline:0,higher:true},{value:judged.p95Ratio,baseline:1,higher:false}].map((item,index)=><td key={index} className="p-1"><div className={`rounded border px-2 py-1.5 text-right ${experimentDifference(item.value,item.baseline,item.higher).tone}`}><strong>{experimentDifference(item.value,item.baseline,item.higher).text}</strong></div></td>)}</tr></tbody></table>
}

export function ExperimentComparisonLegend() {
  return <span className="text-xs text-muted-foreground" title="일치: 서비스 식별자·정의, 측정 규칙, 표시 구간 길이가 같고 서비스별 평균 요청률 차이가 1% 이내. 참고: 조건이 다르거나 미확인. 구간 막대는 일치·참고·대응 없음 순. 제한 원인은 중복될 수 있어요.">Δ 자동−고정 / p95 자동÷고정</span>
}
