"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { MeasurementFilters } from "@/features/measurement-coverage"
import { useChillQuery, useSession } from "@/lib/client"
import { runAttempt, runSpec } from "@/lib/measurement-coverage"
import { formatMeasurementTime, safeMeasurementReturn } from "@/lib/measurements"
import type { RunsView } from "@/lib/read-types"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { DataTable, useServerTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"

function RunList({group=false}:{group?:boolean}) {
  const params=useSearchParams(),{config}=useSession(),namespace=params.get("namespace")??config.namespace,tableState=useServerTable()
  const search=new URLSearchParams(tableState.query)
  for(const key of ["q","condition","model","deviceClass","node","path","powerMode","cpu","status","group","parentUID"]) {const value=params.get(key);if(value)search.set(key,value)}
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${search}`)
  const runs=query.data?.runs??[],returnTo=`${group?"/measurements/group":"/measurements/runs"}?${params}`
  const back=safeMeasurementReturn(params.get("returnTo"),`/measurements/runs?namespace=${encodeURIComponent(namespace)}`)
  return <Screen width="data" actionPlacement="header" title={group?"조건별 측정 회차":"측정 회차"} lede="서버가 반환한 회차와 판정을 보여 줘요. 상세에서 실행 기록과 사유를 확인하세요." back={group?[back,"상위 측정"]:undefined} breadcrumbs={group?[{href:"/measurements/runs",label:"측정 회차"},{label:"조건별 회차"}]:undefined} action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="runs" namespace={namespace}/>
    <MeasurementFilters editable axes={[["model","모델"],["deviceClass","장치 종류"],["node","Node"],["path","실행 경로"],["powerMode","전력모드"],["cpu","CPU quota"]].map(([key,label])=>({key,label,values:runs.flatMap(run=>{const s=runSpec(run);const value=({model:s.candidate?.modelSpec,deviceClass:s.candidate?.deviceClass,node:s.targetNode,path:s.candidate?.executionPath,powerMode:s.candidate?.powerMode,cpu:s.measurement?.cpuQuota} as Record<string,string|undefined>)[key];return value?[value]:[]})}))}/>
    <QueryGate query={query}><DataTable searchable={false} data={runs} server={{...tableState,total:query.data?.page?.total??0,busy:query.isFetching}} columns={[
      {key:"name",label:"회차",sortValue:run=>run.name,value:run=><Link className="break-all text-primary underline" href={`/measurements/profiling-runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid,returnTo})}`}>{run.name}</Link>},
      {key:"created",label:"생성 시각 (KST)",sortValue:run=>run.creationTimestamp,value:run=><time title={run.creationTimestamp}>{formatMeasurementTime(run.creationTimestamp)}</time>},
      {key:"model",label:"모델 / 장치 / Node",sortValue:run=>runSpec(run).candidate?.modelSpec??"",value:run=>{const s=runSpec(run);return <><p>{s.candidate?.modelSpec??"미제공"}</p><p>{s.candidate?.deviceClass??"미제공"}</p><p>{s.targetNode??"미제공"}</p></>}},
      {key:"powerMode",label:"전력모드 / 경로",sortValue:run=>runSpec(run).candidate?.powerMode??"",value:run=>{const s=runSpec(run);return <><p>{s.candidate?.powerMode??"미제공"}</p><p>{s.candidate?.executionPath??"미제공"}</p></>}},
      {key:"load",label:"부하 / 배치 / 반복",value:run=>{const s=runSpec(run);return <><p>{s.measurement?.workloadMode??"미제공"}</p><p>배치 {s.measurement?.batch??"미제공"}</p><p>{runAttempt(run)}</p></>}},
      {key:"cpu",label:"CPU / 데이터셋",value:run=>{const s=runSpec(run);return <><p>{s.measurement?.cpuQuota??"미제공"} / {s.measurement?.cpuAffinity?.join(", ")??"코어 미지정"}</p><p>{s.measurement?.dataset??"데이터셋 미제공"}</p></>}},
      {key:"status",label:"서버 판정",sortValue:run=>run.assessment?.displayStatus??"unknown",value:run=><MeasurementStatus assessment={run.assessment}/>},
      {key:"reason",label:"사유 / 부모 요청",value:run=><div className="max-w-96"><ul className="space-y-1 text-xs">{run.assessment?.reasons?.map((reason,index)=><li key={index}>{reason.label??reason.message}</li>)}</ul>{run.ownerReferences?.filter(owner=>owner.controller&&owner.kind==="BatchEnergyOptimization").map(owner=><Link key={owner.uid} className="break-all text-primary underline" href={`/measurements/batch-energy-optimizations/${encodeURIComponent(run.namespace)}/${encodeURIComponent(owner.name)}?uid=${encodeURIComponent(owner.uid)}`}>{owner.name}</Link>)}</div>},
    ]}/></QueryGate>
    <SourceExport value={query.data} filename="chill-measurement-runs.json"/><SourceJSON value={query.data} title="현재 페이지 회차 원본"/>
  </Screen>
}
export function MeasurementRunsPage() {return <RunList/>}
export function MeasurementGroupPage() {return <RunList group/>}
