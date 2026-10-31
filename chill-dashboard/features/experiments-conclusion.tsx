import { TriangleAlert } from "lucide-react"
import { campaignScoreboard, experimentDifference, type Experiment } from "@/lib/experiments"
export function ExperimentConclusion({run}:{run:Experiment}) {
  const {summary,referenceSummary,coverageCounts}=campaignScoreboard(run)
  const groups=[...(coverageCounts.matched?[{label:"일치 비교",summary}]:[]),...(coverageCounts.limited?[{label:"참고 · 제한 비교",summary:referenceSummary}]:[])]
  if(!groups.length)return null
  return <table className="w-full text-xs tabular-nums" aria-label="실험 결론"><thead><tr>{["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"].map(label=><th key={label} className="px-2 pb-1 text-right font-normal text-muted-foreground">{label}</th>)}</tr></thead><tbody>
    {groups.map(group=><tr key={group.label}>{[{value:group.summary.energyPercent,baseline:0,higher:false},{value:group.summary.successDelta,baseline:0,higher:true},{value:group.summary.p95Ratio,baseline:1,higher:false}].map((item,index)=><td key={index} className="p-1"><div className={`rounded border px-2 py-1 text-right ${group.label==="일치 비교"?experimentDifference(item.value,item.baseline,item.higher).tone:"border-heat/50 bg-heat/5 text-foreground"}`}><span className="block text-xs">{group.label==="일치 비교"?null:<TriangleAlert className="mr-1 inline size-3" aria-hidden="true"/>}{group.label}</span><strong>{experimentDifference(item.value,item.baseline,item.higher).text}</strong></div></td>)}</tr>)}
  </tbody></table>
}

export function ExperimentComparisonLegend() {
  return <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground" aria-label="비교 범례" title="일치 비교: 서비스 식별자·정의, 측정 규칙, 표시 구간 길이가 같고 서비스별 평균 요청률 차이가 1% 이내인 비교. 참고: 이 조건 중 다르거나 미확인인 비교. Δ는 자동−고정, p95 배율은 자동÷고정."><span>Δ 자동−고정 / p95 자동÷고정</span><span className="inline-flex items-center gap-1"><TriangleAlert className="size-3" aria-hidden="true"/>제한 비교·대상 없음</span></div>
}
