"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { MeasurementFilters } from "@/features/measurement-coverage"
import { useChillQuery, useSession } from "@/lib/client"
import { currentRunCondition, runAttempt, runSpec } from "@/lib/measurement-coverage"
import { executionPathLabel, formatMeasurementTime, measurementReasonSummary, workloadModeLabel } from "@/lib/measurements"
import { requestHref, requestRunHref } from "@/lib/measurement-request"
import type { ProfilingRun, RunsView } from "@/lib/read-types"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { DataTable, useServerTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"

const columns = (parent?:{name:string;uid:string},returnTo?:string) => [
  {key:"name",label:"회차 / 부모 요청",sortValue:(run:ProfilingRun)=>run.name,value:(run:ProfilingRun)=>{const owner=parent??run.ownerReferences?.find(owner=>owner.controller&&owner.kind==="BatchEnergyOptimization");return <div className="space-y-2"><Link className="inline-flex min-h-11 items-center break-all text-primary underline" href={requestRunHref(run.namespace,run.name,run.uid,owner,returnTo)}>{run.name}</Link>{!parent&&owner&&<Link className="block break-all text-primary underline" href={requestHref(run.namespace,owner.name,owner.uid)}>{owner.name}</Link>}<details><summary className="min-h-11 cursor-pointer py-2">조건·사유·출처</summary><RunDetails run={run}/></details></div>}},
  {key:"status",label:"측정 / 현재 사용 / 품질",value:(run:ProfilingRun)=><><MeasurementStatus assessment={run.assessment}/><ul className="space-y-1 text-sm">{run.assessment?.reasons?.map((reason,index)=><li key={index}>{measurementReasonSummary(reason.message||reason.code)}</li>)}</ul></>},
  {key:"batch",label:"배치",unit:"건",numeric:true,value:(run:ProfilingRun)=>runSpec(run).measurement?.batch??"미제공"},
  {key:"attempt",label:"반복",value:(run:ProfilingRun)=>runAttempt(run)},
  {key:"completed",label:"실행 종료 (KST)",value:(run:ProfilingRun)=>{const condition=currentRunCondition(run,"ExecutionComplete");return condition&&["True","False"].includes(condition.status)&&condition.lastTransitionTime?<time title={condition.lastTransitionTime}>{formatMeasurementTime(condition.lastTransitionTime)}</time>:"미제공"}},
]

function RunDetails({run}:{run:ProfilingRun}) {
  const s=runSpec(run)
  const facts:[string,string][]=[["모델",s.candidate?.modelSpec??"미제공"],["장치 종류",s.candidate?.deviceClass??"미제공"],["실행 장비",s.targetNode??"미제공"],["전력모드",s.candidate?.powerMode??"미제공"],["실행 경로",s.candidate?.executionPath?executionPathLabel(s.candidate.executionPath):"미제공"],["부하 방식",workloadModeLabel(s.measurement?.workloadMode)],["데이터셋",s.measurement?.dataset??"미제공"],["생성 (KST)",formatMeasurementTime(run.creationTimestamp)],["저장 영역",run.namespace],["UID",run.uid]]
  return <div className="min-w-0 space-y-2 whitespace-normal text-sm"><dl>{facts.map(([label,value])=><div className="grid gap-1 border-b py-2" key={label}><dt className="font-medium">{label}</dt><dd className="break-all">{value}</dd></div>)}</dl><details><summary className="min-h-11 cursor-pointer py-2">실행 경로·부하 방식·사유 원문</summary><dl><dt>실행 경로 원문</dt><dd className="break-all">{s.candidate?.executionPath??"미제공"}</dd><dt>부하 방식 원문</dt><dd className="break-all">{s.measurement?.workloadMode??"미제공"}</dd></dl><SourceJSON inline value={run.assessment?.reasons} title="사유 원문"/></details></div>
}

export function MeasurementChildRuns({namespace,parent}:{namespace:string;parent:{name:string;uid:string}}) {
  const table=useServerTable(undefined,false)
  const params=useSearchParams(),returnTo=`${requestHref(namespace,parent.name,parent.uid)}&${new URLSearchParams({returnTo:params.get("returnTo")??""})}`
  const search=new URLSearchParams(table.query);search.set("parentUID",parent.uid)
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${search}`)
  return <QueryGate query={query}><DataTable data={query.data?.runs??[]} columns={columns(parent,returnTo)} searchable={false} server={{...table,total:query.data?.page?.total??0,busy:query.isFetching}}/></QueryGate>
}

export function MeasurementRunsPage() {
  const params=useSearchParams(),{config}=useSession(),namespace=params.get("namespace")??config.namespace,tableState=useServerTable()
  const search=new URLSearchParams(tableState.query)
  for(const key of ["q","condition","model","deviceClass","node","path","powerMode","status","group","parentUID"]) {const value=params.get(key);if(value)search.set(key,value)}
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${search}`)
  const runs=query.data?.runs??[]
  return <Screen width="data" actionPlacement="header" title="측정 요청" action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="requests" namespace={namespace}/>
    <div className="flex min-h-11 items-center gap-4"><Link className="underline" href={`/measurements/requests?${new URLSearchParams({namespace})}`}>요청 목록</Link><h2 className="font-semibold">전체 회차 검색</h2></div>
    <MeasurementFilters editable axes={[["model","모델"],["deviceClass","장치 종류"],["node","Node"],["path","실행 경로"],["powerMode","전력모드"]].map(([key,label])=>({key,label,values:runs.flatMap(run=>{const s=runSpec(run);const value=({model:s.candidate?.modelSpec,deviceClass:s.candidate?.deviceClass,node:s.targetNode,path:s.candidate?.executionPath,powerMode:s.candidate?.powerMode} as Record<string,string|undefined>)[key];return value?[value]:[]})}))}/>
    <QueryGate query={query}><DataTable searchable={false} data={runs} server={{...tableState,total:query.data?.page?.total??0,busy:query.isFetching}} columns={columns(undefined,`/measurements/requests?${params}`)}/></QueryGate>
    <SourceExport value={query.data} filename="chill-measurement-runs.json"/><SourceJSON value={query.data} title="현재 페이지 회차 원본"/>
  </Screen>
}
