"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { MeasurementFilters } from "@/features/measurement-coverage"
import { useChillQuery, useSession } from "@/lib/client"
import { runAttempt, runSpec } from "@/lib/measurement-coverage"
import { formatMeasurementTime } from "@/lib/measurements"
import { requestHref, requestRunHref } from "@/lib/measurement-request"
import type { ProfilingRun, RunsView } from "@/lib/read-types"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { MeasurementNavigation, MeasurementStatus, MeasurementEvidenceChecks } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { DataTable, useServerTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"

const columns = (parent?:{name:string;uid:string},returnTo?:string) => [
  {key:"name",label:"회차",sortValue:(run:ProfilingRun)=>run.name,value:(run:ProfilingRun)=>{const owner=parent??run.ownerReferences?.find(owner=>owner.controller&&owner.kind==="BatchEnergyOptimization");return <Link className="break-all text-primary underline" href={requestRunHref(run.namespace,run.name,run.uid,owner,returnTo)}>{run.name}</Link>}},
  {key:"created",label:"생성 (KST)",sortValue:(run:ProfilingRun)=>run.creationTimestamp,value:(run:ProfilingRun)=><time title={run.creationTimestamp}>{formatMeasurementTime(run.creationTimestamp)}</time>},
  {key:"model",label:"모델 / 장치 / Node",value:(run:ProfilingRun)=>{const s=runSpec(run);return <><p>{s.candidate?.modelSpec??"미제공"}</p><p>{s.candidate?.deviceClass??"미제공"}</p><p>{s.targetNode??"미제공"}</p></>}},
  {key:"condition",label:"전력모드 / 경로 / 부하",value:(run:ProfilingRun)=>{const s=runSpec(run);return <><p>{s.candidate?.powerMode??"미제공"}</p><p>{s.candidate?.executionPath??"미제공"}</p><p>{s.measurement?.workloadMode==="active"?"실행":s.measurement?.workloadMode==="idle"?"유휴":s.measurement?.workloadMode??"미제공"}</p></>}},
  {key:"batch",label:"배치",unit:"건",numeric:true,value:(run:ProfilingRun)=>runSpec(run).measurement?.batch??"미제공"},
  {key:"attempt",label:"반복",value:(run:ProfilingRun)=>runAttempt(run)},
  {key:"cpu",label:"CPU / 데이터셋",value:(run:ProfilingRun)=>{const s=runSpec(run);return <><p>{s.measurement?.cpuQuota??"미제공"}</p><p>{s.measurement?.cpuAffinity?.join(", ")??"코어 미지정"}</p><p>{s.measurement?.dataset??"미제공"}</p></>}},
  {key:"status",label:"측정 / 현재 사용 / 품질",value:(run:ProfilingRun)=><><MeasurementStatus assessment={run.assessment}/><MeasurementEvidenceChecks assessment={run.assessment}/></>},
  {key:"reason",label:"사유 / 부모 요청",value:(run:ProfilingRun)=>{const owner=run.ownerReferences?.find(owner=>owner.controller&&owner.kind==="BatchEnergyOptimization");return <div><ul className="space-y-1 text-sm">{run.assessment?.reasons?.map((reason,index)=><li key={index}>{reason.label??reason.message}</li>)}</ul>{owner&&<Link className="break-all text-primary underline" href={requestHref(run.namespace,owner.name,owner.uid)}>{owner.name}</Link>}</div>}},
]

export function MeasurementChildRuns({namespace,parent}:{namespace:string;parent:{name:string;uid:string}}) {
  const table=useServerTable()
  const params=useSearchParams(),returnTo=`${requestHref(namespace,parent.name,parent.uid)}&${new URLSearchParams({returnTo:params.get("returnTo")??""})}`
  const search=new URLSearchParams(table.query);search.set("parentUID",parent.uid)
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${search}`)
  return <QueryGate query={query}><DataTable data={query.data?.runs??[]} columns={columns(parent,returnTo)} searchable={false} server={{...table,total:query.data?.page?.total??0,busy:query.isFetching}}/></QueryGate>
}

export function MeasurementRunsPage() {
  const params=useSearchParams(),{config}=useSession(),namespace=params.get("namespace")??config.namespace,tableState=useServerTable()
  const search=new URLSearchParams(tableState.query)
  for(const key of ["q","condition","model","deviceClass","node","path","powerMode","cpu","status","group","parentUID"]) {const value=params.get(key);if(value)search.set(key,value)}
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${search}`)
  const runs=query.data?.runs??[]
  return <Screen width="data" actionPlacement="header" title="측정 요청" action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="requests" namespace={namespace}/>
    <div className="flex min-h-11 items-center gap-4"><Link className="underline" href={`/measurements/requests?${new URLSearchParams({namespace})}`}>요청 목록</Link><h2 className="font-semibold">전체 회차 검색</h2></div>
    <MeasurementFilters editable axes={[["model","모델"],["deviceClass","장치 종류"],["node","Node"],["path","실행 경로"],["powerMode","전력모드"],["cpu","CPU quota"]].map(([key,label])=>({key,label,values:runs.flatMap(run=>{const s=runSpec(run);const value=({model:s.candidate?.modelSpec,deviceClass:s.candidate?.deviceClass,node:s.targetNode,path:s.candidate?.executionPath,powerMode:s.candidate?.powerMode,cpu:s.measurement?.cpuQuota} as Record<string,string|undefined>)[key];return value?[value]:[]})}))}/>
    <QueryGate query={query}><DataTable searchable={false} data={runs} server={{...tableState,total:query.data?.page?.total??0,busy:query.isFetching}} columns={columns(undefined,`/measurements/requests?${params}`)}/></QueryGate>
    <SourceExport value={query.data} filename="chill-measurement-runs.json"/><SourceJSON value={query.data} title="현재 페이지 회차 원본"/>
  </Screen>
}
