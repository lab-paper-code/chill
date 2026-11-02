"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { MeasurementPlanList } from "@/lib/read-types"
import { executionPathLabel, formatMeasurementTime, measurementReasonSummary } from "@/lib/measurements"
import { requestExecutionStatus, requestHref } from "@/lib/measurement-request"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"
import { DataTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { StatusCell } from "@/components/status-cell"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { MeasurementFilters, useMeasurementCoverage } from "@/features/measurement-coverage"
import { MeasurementPlanSheet } from "@/features/measurement-plans"
import { coverageUsageAssessment } from "@/lib/measurement-coverage"
import { parseMeasurementJSON } from "@/lib/measurement-summary"

export function MeasurementRequestsPage({name,namespace:routeNamespace}:{name?:string;namespace?:string}={}) {
  const params=useSearchParams(),{config}=useSession(),namespace=routeNamespace??params.get("namespace")??config.namespace
  const query=useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations?view=summary`,{decoder:parseMeasurementJSON})
  const coverage=useMeasurementCoverage(namespace)
  const plans=(query.data?.items??[]).map(plan=>{const usage=coverageUsageAssessment(coverage.data?.cells??[],plan.spec);return usage&&plan.assessment?{...plan,assessment:{...plan.assessment,scope:usage.scope}}:plan})
  const axes=[{key:"model",label:"모델",values:plans.map(p=>p.spec.state.candidate.modelSpec)},{key:"deviceClass",label:"장치 종류",values:plans.map(p=>p.spec.state.candidate.deviceClass)},{key:"node",label:"Node",values:plans.map(p=>p.spec.state.targetNode)},{key:"powerMode",label:"전력모드",values:plans.map(p=>p.spec.state.candidate.powerMode)},{key:"path",label:"실행 경로",values:plans.map(p=>p.spec.state.candidate.executionPath)}]
  const shown=plans.filter(p=>axes.every(axis=>!params.get(axis.key)||params.get(axis.key)===(axis.key==="model"?p.spec.state.candidate.modelSpec:axis.key==="deviceClass"?p.spec.state.candidate.deviceClass:axis.key==="node"?p.spec.state.targetNode:axis.key==="powerMode"?p.spec.state.candidate.powerMode:p.spec.state.candidate.executionPath))&&(!params.get("q")||p.metadata.name.toLocaleLowerCase().includes(params.get("q")!.toLocaleLowerCase()))&&(!params.get("status")||p.assessment?.displayStatus===params.get("status")))
  const listParams=new URLSearchParams(params);for(const key of ["uid","request","returnTo"])listParams.delete(key)
  const returnTo=`/measurements/requests?${listParams}`
  const selected=name??params.get("request")
  return <Screen width="data" actionPlacement="header" title="측정 요청" action={<Button asChild><Link href={`/measurements/requests/new?${new URLSearchParams({namespace})}`}>측정 요청</Link></Button>}>
    <MeasurementNavigation active="requests" namespace={namespace}/><MeasurementFilters axes={axes}/>
    <Link className="inline-flex min-h-11 items-center underline" href={`/measurements/requests?${new URLSearchParams({namespace,view:"runs"})}`}>전체 회차 검색</Link>
    <QueryGate query={query}><DataTable urlState key={params.toString()} searchable={false} data={shown} columns={[
      {key:"name",label:"요청 / 모델",sortValue:p=>p.metadata.name,value:p=><Link data-request-name={p.metadata.name} className="inline-flex min-h-11 items-center break-all text-primary underline" href={`${requestHref(namespace,p.metadata.name,p.metadata.uid)}&${new URLSearchParams({returnTo})}`}>{p.metadata.name}<br/>{p.spec.state.candidate.modelSpec}</Link>},
      {key:"condition",label:"장치 / 모드 / 경로",sortValue:p=>p.spec.state.targetNode,value:p=><span>{p.spec.state.targetNode}<br/>{p.spec.state.candidate.deviceClass}<br/>{p.spec.state.candidate.powerMode}<br/>{executionPathLabel(p.spec.state.candidate.executionPath)}</span>},
      {key:"execution",label:"실행",value:p=><StatusCell axis="실행" label={requestExecutionStatus(p.status?.phase)} tone={p.status?.phase==="Failed"?"fail":"idle"}/>},
      {key:"status",label:"측정 / 현재 사용 / 품질",value:p=><MeasurementStatus assessment={p.assessment}/>},
      {key:"completed",label:"완료 회차",unit:"회",numeric:true,value:p=>p.status?.completedTrials??"미제공"},
      {key:"planned",label:"예정 회차",unit:"회",numeric:true,value:()=>"미제공"},
      {key:"reason",label:"사유",value:p=>{const reasons=p.assessment?.failureSummary?.length?p.assessment.failureSummary:p.assessment?.reasons;return reasons?.length?<ul className="space-y-2">{reasons.map((reason,index)=><li key={index}>{measurementReasonSummary(reason.message||reason.code)}</li>)}</ul>:"미제공"}},
      {key:"created",label:"생성 (KST)",sortValue:p=>p.metadata.creationTimestamp,value:p=>formatMeasurementTime(p.metadata.creationTimestamp)},
    ]}/></QueryGate>
    <SourceJSON value={query.data} title="요청 목록 원본"/>
    {selected&&<MeasurementPlanSheet namespace={namespace} name={selected}/>}
  </Screen>
}
