"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { CurvePlanSpec, MeasurementPlanList, ProfilesView } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { profileForPlan } from "@/lib/measurement-coverage"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"
import { DataTable } from "@/components/data-table"
import { MeasurementNavigation, planLink } from "@/features/measurement-coverage"

export const conditionRows = (condition:CurvePlanSpec):[string,string][] => [
  ["모델 파일", `${condition.logicalModel} / ${condition.artifact.name}`],
  ["실행 방식", `${condition.runtimeFamily} / ${condition.backend}`],
  ["전처리기", condition.preprocessor.name || "없음"],
  ["데이터셋", condition.activeProtocol.dataset?.name ?? "없음"],
]
export function SchedulerProfilesPage() {
  const {config}=useSession(),params=useSearchParams(),namespace=params.get("namespace")??config.namespace
  const query=useChillQuery<ProfilesView>("v1/scheduler/profiles",{decoder:parseSchedulerJSON})
  const plans=useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`)
  return <Screen width="data" actionPlacement="header" title="측정 프로파일" lede="현재 catalog의 정확 동결 조건별 사용 가능 결과예요. 다른 Node·이미지·CPU·데이터셋은 합치지 않아요." breadcrumbs={[{href:"/measurements",label:"측정"},{label:"프로파일"}]}>
    <MeasurementNavigation namespace={namespace}/>
    <QueryGate query={query}><DataTable data={query.data?.profiles??[]} columns={[
      {key:"model",label:"모델",sortValue:p=>p.condition.logicalModel,value:p=>p.condition.logicalModel},
      {key:"class",label:"장치 종류",sortValue:p=>p.condition.deviceClass,value:p=>p.condition.deviceClass},
      {key:"node",label:"Node",sortValue:p=>p.nodeName,value:p=><span>{p.nodeName}{p.reuse&&<span className="block text-xs">서버 확인 재사용 · {p.reuse.targetNode.name}</span>}</span>},
      {key:"mode",label:"전력모드",sortValue:p=>p.condition.powerMode,value:p=>p.condition.powerMode},
      {key:"path",label:"실행 경로",sortValue:p=>p.condition.artifact.executionPath,value:p=>p.condition.artifact.executionPath},
      {key:"cpu",label:"CPU / affinity",sortValue:p=>p.condition.cpu.limit,value:p=><span className="tabular-nums">{p.condition.cpu.limit} / {p.condition.cpu.affinityCPUs?.join(", ")??"미지정"}</span>},
      {key:"batch",label:"실제 batch",value:p=><span className="tabular-nums">{p.batches?.map(b=>b.batch).join(", ")??"미제공"}</span>},
      {key:"date",label:"측정일 · KST",sortValue:p=>p.measuredAt,value:p=><span className="whitespace-nowrap tabular-nums" title={p.measuredAt}>{new Date(p.measuredAt).toLocaleString("ko-KR",{timeZone:"Asia/Seoul"})}</span>},
      {key:"source",label:"근거 / 정확 출처",value:p=><div className="min-w-40"><SourceJSON value={p} title="동결 조건과 수치"/>{(plans.data?.items??[]).filter(plan=>profileForPlan(plan,[p]).length>0).map(plan=><Link key={plan.metadata.uid} className="block text-primary underline" href={planLink(plan,`/scheduler/profiles?${params}`)}>정확 결과 참조: {plan.metadata.name}</Link>)}<p className="text-xs text-muted-foreground">ID 문자열로 요청 UID를 추측하지 않아요.</p></div>},
    ]}/></QueryGate>
    {!!query.data?.insufficiencies?.length&&<section className="space-y-3"><h2 className="font-semibold">연결된/연결 안 된 경고</h2>{query.data.insufficiencies.map((issue,index)=><div key={index} className="rounded-md border p-4 text-sm"><p>{issue.source.Namespace}/{issue.source.Name} · {issue.code==="ReplacementSelection"?"대체된 이력":issue.code}</p><p>{issue.reason}</p></div>)}</section>}
    <p className="text-sm text-muted-foreground">catalog 사용 가능은 논문 적격 또는 다섯 근거 완비 인증이 아니에요. 배치 CI·합쳐진 p95는 원본에 없으면 미제공이에요.</p><SourceJSON value={query.data} title="catalog 전체 원본"/>
  </Screen>
}
