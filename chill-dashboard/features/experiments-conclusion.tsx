import { TriangleAlert } from "lucide-react"
import { campaignScoreboard, experimentDifference, type Experiment } from "@/lib/experiments"

export function ExperimentJudgment({run}:{run:Experiment}) {
  const {conclusion}=campaignScoreboard(run)
  return <strong aria-label="실험 판정" className={`inline-flex rounded-md border px-3 py-2 text-base ${conclusion==="SLO 악화"?"border-destructive/40 bg-destructive/10 text-destructive":conclusion==="참고만 가능"||conclusion==="결과 부족"||conclusion==="비교 대상 없음"?"border-heat/50 bg-heat/10":"border-primary/30 bg-primary/5"}`}>{conclusion}</strong>
}

export function ExperimentLimits({run}:{run:Experiment}) {
  const {coverageCounts:c}=campaignScoreboard(run)
  const reasons=[["규칙 다름",c.ruleDifferences],["규칙 미확인",c.ruleMissing],["정의 미확인",c.identityUnknown],["정의 다름",c.identityDifferences],["부하 다름",c.loadDifferences],["부하 미확인",c.loadUnknown],["길이 다름·미확인",c.durationDifferences],["대응 없음",c.unpaired]] as const
  return <span aria-label="비교 제한 원인" className="text-xs font-normal text-muted-foreground tabular-nums" title="비교 원인별 대응 구간 수, 대응 없음은 단독 구간 수. 한 비교가 여러 원인에 포함될 수 있어요.">{reasons.filter(([,count])=>count>0).map(([label,count])=>`${label} ${count}`).join(" · ")}</span>
}

export function ExperimentConclusion({run}:{run:Experiment}) {
  const {summary,referenceSummary,coverageCounts}=campaignScoreboard(run)
  const groups=[...(coverageCounts.matched?[{limited:false,summary}]:[]),...(coverageCounts.limited?[{limited:true,summary:referenceSummary}]:[])]
  if(!groups.length)return null
  return <table className="w-full text-xs tabular-nums" aria-label="실험 결론"><thead><tr>{["에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)"].map(label=><th key={label} className="px-2 pb-1 text-right font-normal text-muted-foreground">{label}</th>)}</tr></thead><tbody>
    {groups.map(group=><tr key={String(group.limited)}>{[{value:group.summary.energyPercent,baseline:0,higher:false},{value:group.summary.successDelta,baseline:0,higher:true},{value:group.summary.p95Ratio,baseline:1,higher:false}].map((item,index)=><td key={index} className="p-1"><div className={`flex items-center justify-end gap-1 rounded border px-2 py-1.5 text-right ${!group.limited?experimentDifference(item.value,item.baseline,item.higher).tone:"border-heat/50 bg-heat/5 text-foreground"}`}>{group.limited&&<TriangleAlert className="size-3 shrink-0 text-heat" aria-label="제한 비교"/>}<strong>{experimentDifference(item.value,item.baseline,item.higher).text}</strong></div></td>)}</tr>)}
  </tbody></table>
}

export function ExperimentComparisonLegend() {
  return <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground" aria-label="비교 범례" title="일치 비교: 서비스 식별자·정의, 측정 규칙, 표시 구간 길이가 같고 서비스별 평균 요청률 차이가 1% 이내인 비교. 참고: 이 조건 중 다르거나 미확인인 비교. Δ는 자동−고정, p95 배율은 자동÷고정."><span>Δ 자동−고정 / p95 자동÷고정</span><span className="inline-flex items-center gap-1"><TriangleAlert className="size-3" aria-hidden="true"/>제한 비교·대상 없음</span></div>
}
