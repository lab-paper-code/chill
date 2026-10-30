"use client"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import { useMeasurementCoverage, CoverageSummary, CoverageError } from "@/features/measurement-coverage"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { campaignGroups, comparisonRows, difference, finite, resultMetric, type Experiment } from "@/lib/experiments"
import { parseSchedulerJSON } from "@/lib/scheduler"

export function OverviewPage() {
  const {config}=useSession(),params=useSearchParams(),namespace=params.get("namespace")??config.namespace
  const coverage=useMeasurementCoverage(namespace),experiments=useChillQuery<{items:Experiment[]}>("v1/experiments",{decoder:parseSchedulerJSON})
  const recent=campaignGroups(experiments.data?.items??[])[0]
  const services=new Map<string,{success:{delta:number;stage:string}|null;p95:{delta:number;stage:string}|null}>()
  for(const pair of recent?.pairs??[])for(const row of comparisonRows(pair.chill,pair.control)) {
    const current=services.get(row.name)??{success:null,p95:null}
    if(row.a.length===1&&row.b.length===1)for(const key of ["success","p95"] as const) {
      const a=finite(resultMetric(row.a[0],key).value),b=finite(resultMetric(row.b[0],key).value)
      if(a!==null&&b!==null){const delta=(a-b)*(key==="success"?100:1);if(!current[key]||(key==="success"?delta<current[key]!.delta:delta>current[key]!.delta))current[key]={delta,stage:pair.key}}
    }
    services.set(row.name,current)
  }
  return <Screen title="측정·실험 현황" width="data" lede="사용 가능한 범위와 다음 운영 조치를 확인하세요." actionPlacement="header" action={<Button variant="outline" onClick={()=>{void coverage.refetch();void experiments.refetch()}} disabled={coverage.isFetching||experiments.isFetching}>갱신</Button>}>
    {coverage.data?<CoverageSummary data={coverage.data}/>:<CoverageError error={coverage.error} retry={()=>void coverage.refetch()}/>}
    <section className="coverage-detail"><h2>최근 실험 비교</h2>{recent?<><p>{recent.run.id}</p><p>{recent.question}</p>{recent.pairs.length?<><div className="overview-stages">{recent.pairs.map(pair=><div key={pair.key}><strong>{pair.key}</strong><p>CHILL − 정적 구성</p><p>{difference(pair.chill.kpis.wholePoolJoules,pair.control.kpis.wholePoolJoules,"J")}</p><small>출처: 동결 단계 kpis.wholePoolJoules</small></div>)}</div><h3>서비스별 가장 큰 손실</h3>{[...services].map(([name,s])=><p key={name}>{name}: 성공률 최소 차이 {s.success?`${s.success.delta.toFixed(2)} %p (${s.success.stage})`:"비교 불가"}, p95 최대 증가 {s.p95?`${s.p95.delta.toFixed(2)} ms (${s.p95.stage})`:"비교 불가"}</p>)}<p className="text-sm text-muted-foreground">대응 단계 전체를 표시합니다. Δ는 원값으로 계산하며 표시값 차이와 마지막 자리가 다를 수 있습니다. 서비스별 성공률·p95는 동결 kpis의 같은 이름 결과입니다.</p></>:<p>정확히 대응하는 단계가 없어 비교할 수 없습니다. 최신 기록은 유지합니다.</p>}</>:<p>{experiments.error?"실험 기록 조회 실패":"실험 기록 확인 중"}</p>}<Link href="/experiments">실험 전체 보기</Link></section>
    <div className="overview-columns"><section className="coverage-detail"><h2>다음 조치</h2>{coverage.data?.nextActions.length?coverage.data.nextActions.map((a,i)=><p key={i}><Link href={`/measurements?${new URLSearchParams({namespace,cell:a.cellId,...(a.variantId?{variant:a.variantId}:{})})}`}>{a.message}</Link></p>):<p>{coverage.data?"확인된 추가 조치가 없습니다. 커버리지의 미확인 범위도 확인하세요.":"커버리지 조회 전에는 다음 조치를 확정할 수 없습니다."}</p>}<Link href="/operations/flow">운영 흐름 보드</Link></section><section className="coverage-detail"><h2>모델 × 장치 커버리지</h2>{coverage.data?.groups.map(g=><p key={`${g.model.uid}/${g.deviceClass.uid}`}><Link href={`/measurements?${new URLSearchParams({namespace,group:`${g.model.uid}/${g.deviceClass.uid}`})}`}>{g.model.name} / {g.deviceClass.name}: {g.complete??"미확인"} / {g.supported??"미확인"}</Link></p>)}<Link href="/measurements">커버리지 열기</Link></section></div>
  </Screen>
}
