"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { CurvePlanSpec, MeasurementPlanList, ProfilesView } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { profileForPlan } from "@/lib/measurement-coverage"
import { formatMeasurementTime, measurementReasonSummary, significant } from "@/lib/measurements"
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
      {key:"energy",label:"배치별 J/건",value:p=><ul className="grid gap-1 tabular-nums">{p.batches?.map(b=><li key={b.batch}>배치 {b.batch}: {significant(b.energyPerInputJoules)} J/건</li>)??"미제공"}</ul>},
      {key:"date",label:"측정일 (KST)",sortValue:p=>p.measuredAt,value:p=><time className="tabular-nums" title={p.measuredAt}>{formatMeasurementTime(p.measuredAt)}</time>},
      {key:"source",label:"측정 근거",value:p=><div className="min-w-40">{(plans.data?.items??[]).filter(plan=>profileForPlan(plan,[p]).length>0).map(plan=><Link key={plan.metadata.uid} className="flex min-h-11 items-center text-primary underline" href={planLink(plan,`/scheduler/profiles?${params}`)}>{plan.metadata.name}</Link>)}<SourceJSON value={p} title="동결 조건과 원본 수치"/></div>},
    ]}/></QueryGate>
    {!!query.data?.insufficiencies?.length&&<section className="space-y-3"><h2 className="font-semibold">연결된 측정 경고</h2>{query.data.insufficiencies.map((issue,index)=><div key={index} className="rounded-md border p-4 text-sm"><p>{issue.source.Namespace}/{issue.source.Name}</p><p>{measurementReasonSummary(issue.reason)}</p><details><summary className="min-h-11 cursor-pointer py-2">사유 원문</summary><p>{issue.code}: {issue.reason}</p></details></div>)}</section>}
    <p className="text-sm text-muted-foreground">catalog 사용 가능은 논문 적격 또는 다섯 근거 완비 인증이 아니에요. 배치 CI·합쳐진 p95는 원본에 없으면 미제공이에요.</p><SourceJSON value={query.data} title="catalog 전체 원본"/>
  </Screen>
}
