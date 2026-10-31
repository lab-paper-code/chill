"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { MeasurementPlanList } from "@/lib/read-types"
import { formatMeasurementTime } from "@/lib/measurements"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"
import { DataTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { MeasurementNavigation, MeasurementStatus, MeasurementEvidenceChecks } from "@/components/measurement-navigation"
import { MeasurementFilters } from "@/features/measurement-coverage"

export function MeasurementRequestsPage() {
  const params=useSearchParams(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const query=useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`)
  const plans=query.data?.items??[]
  const axes=[{key:"model",label:"모델",values:plans.map(p=>p.spec.state.candidate.modelSpec)},{key:"deviceClass",label:"장치 종류",values:plans.map(p=>p.spec.state.candidate.deviceClass)},{key:"node",label:"Node",values:plans.map(p=>p.spec.state.targetNode)},{key:"powerMode",label:"전력모드",values:plans.map(p=>p.spec.state.candidate.powerMode)},{key:"path",label:"실행 경로",values:plans.map(p=>p.spec.state.candidate.executionPath)}]
  const shown=plans.filter(p=>axes.every(axis=>!params.get(axis.key)||params.get(axis.key)===(axis.key==="model"?p.spec.state.candidate.modelSpec:axis.key==="deviceClass"?p.spec.state.candidate.deviceClass:axis.key==="node"?p.spec.state.targetNode:axis.key==="powerMode"?p.spec.state.candidate.powerMode:p.spec.state.candidate.executionPath))&&(!params.get("q")||p.metadata.name.toLocaleLowerCase().includes(params.get("q")!.toLocaleLowerCase()))&&(!params.get("status")||p.assessment?.displayStatus===params.get("status")))
  return <Screen width="data" actionPlacement="header" title="측정 요청" lede="서버가 판정한 사용 상태와 요청의 실행 기록을 확인하세요." action={<Button asChild><Link href="/measurements/new">새 측정 요청</Link></Button>}>
    <MeasurementNavigation active="requests" namespace={namespace}/><MeasurementFilters axes={axes}/>
    <QueryGate query={query}><DataTable key={params.toString()} searchable={false} data={shown} columns={[
      {key:"name",label:"요청 / 모델",sortValue:p=>p.metadata.name,value:p=><Link className="inline-flex min-h-11 items-center text-primary underline" href={`/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(p.metadata.name)}?${new URLSearchParams({uid:p.metadata.uid,returnTo:`/measurements/requests?${params}`})}`}>{p.metadata.name}<br/>{p.spec.state.candidate.modelSpec}</Link>},
      {key:"condition",label:"장치 / 조건",sortValue:p=>p.spec.state.targetNode,value:p=><span>{p.spec.state.targetNode}<br/>{p.spec.state.candidate.powerMode} / {p.spec.state.candidate.executionPath}<br/>배치 {p.spec.batchSizes?.join(", ")??"선언 범위"}</span>},
      {key:"status",label:"스케줄러 / 측정",sortValue:p=>p.assessment?.label??"",value:p=><><MeasurementStatus assessment={p.assessment}/><MeasurementEvidenceChecks assessment={p.assessment}/></>},
      {key:"reason",label:"원인 / 다음 조치",value:p=>p.assessment?.failureSummary?.length?<ul className="space-y-2 whitespace-normal">{p.assessment.failureSummary.map(failure=><li key={`${failure.runId}/${failure.code}`}><p>{failure.label??failure.message}{failure.exitCode!=null?` · 종료 코드 ${failure.exitCode}`:""}</p>{failure.runName&&<Link className="inline-flex min-h-11 items-center underline" href={`/measurements/profiling-runs/${encodeURIComponent(failure.runNamespace)}/${encodeURIComponent(failure.runName)}?${new URLSearchParams({uid:failure.runId,returnTo:`/measurements/requests?${params}`})}`}>실패 회차 확인</Link>}</li>)}</ul>:p.assessment?.reasons.map(r=>r.label??r.message).join("\n")||"제공된 사유 없음"},
      {key:"execution",label:"실행 상태 (원본)",value:p=>p.status?.phase??"미제공"},
      {key:"created",label:"생성일 (KST)",sortValue:p=>p.metadata.creationTimestamp,value:p=>formatMeasurementTime(p.metadata.creationTimestamp)},
    ]}/></QueryGate><SourceJSON value={query.data} title="요청 목록 원본"/>
  </Screen>
}
