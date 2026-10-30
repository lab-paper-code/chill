"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { MeasurementFilters } from "@/features/measurement-coverage"
import { useChillQuery, useSession } from "@/lib/client"
import { runAttempt, runSpec } from "@/lib/measurement-coverage"
import { formatMeasurementTime, safeMeasurementReturn } from "@/lib/measurements"
import type { RunsView } from "@/lib/read-types"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Button } from "@/components/ui/button"

function RunList({group=false}:{group?:boolean}) {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const page=Math.max(1,Number(params.get("page"))||1),limit=25,search=new URLSearchParams({limit:String(limit),offset:String((page-1)*limit)})
  for(const key of ["q","condition","model","deviceClass","node","path","powerMode","cpu","status","group","parentUID"]) {const value=params.get(key);if(value)search.set(key,value)}
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${search}`)
  const runs=query.data?.runs??[],rows=runs,returnTo=`${group?"/measurements/group":"/measurements/runs"}?${params}`
  const back=safeMeasurementReturn(params.get("returnTo"),`/measurements/runs?namespace=${encodeURIComponent(namespace)}`)
  function go(nextPage:number) {const next=new URLSearchParams(params.toString());next.set("page",String(nextPage));router.replace(`?${next}`,{scroll:false})}
  return <Screen width="data" actionPlacement="header" title={group?"조건별 측정 회차":"측정 회차"} lede="서버가 반환한 회차와 판정을 보여 줘요. 상세에서 원본 실행 상태와 사유를 확인하세요." back={group?[back,"상위 측정"]:undefined} breadcrumbs={group?[{href:"/measurements/runs",label:"측정 회차"},{label:"조건별 회차"}]:undefined} action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="runs" namespace={namespace}/>
    <MeasurementFilters editable axes={[["model","모델"],["deviceClass","장치 종류"],["node","Node"],["path","실행 경로"],["powerMode","전력모드"],["cpu","CPU quota"]].map(([key,label])=>({key,label,values:runs.flatMap(run=>{const s=runSpec(run);const value=({model:s.candidate?.modelSpec,deviceClass:s.candidate?.deviceClass,node:s.targetNode,path:s.candidate?.executionPath,powerMode:s.candidate?.powerMode,cpu:s.measurement?.cpuQuota} as Record<string,string|undefined>)[key];return value?[value]:[]})}))}/>
    <p role="status" className="text-sm tabular-nums">전체 {query.data?.page?.total??"미확인"}회차 / 이 페이지 {runs.length}회차 / 조건에 맞는 {rows.length}회차</p>
    <p className="text-xs text-muted-foreground">모든 필터는 서버 전체 회차에 적용한 뒤 25건씩 표시합니다.</p>
    <QueryGate query={query}><div className="min-w-0 overflow-x-auto rounded-lg border"><Table><TableHeader><TableRow>{["회차 / 생성 시각 (KST)","모델 / 장치 / Node","전력모드 / 경로","부하 / batch / 반복","CPU / 데이터셋","서버 판정","사유 / 부모 요청"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{rows.map(run=>{const s=runSpec(run);return <TableRow key={run.uid}>
      <TableCell><Link className="inline-flex min-h-11 items-center break-all text-primary underline" href={`/measurements/profiling-runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid,returnTo})}`}>{run.name}</Link><time className="block text-xs" title={run.creationTimestamp}>{formatMeasurementTime(run.creationTimestamp)}</time></TableCell>
      <TableCell><p>{s.candidate?.modelSpec??"미제공"}</p><p>{s.candidate?.deviceClass??"미제공"}</p><p>{s.targetNode??"미제공"}</p></TableCell>
      <TableCell><p>{s.candidate?.powerMode??"미제공"}</p><p>{s.candidate?.executionPath??"미제공"}</p></TableCell>
      <TableCell><p>{s.measurement?.workloadMode??"미제공"}</p><p>batch {s.measurement?.batch??"미제공"}</p><p>{runAttempt(run)}</p></TableCell>
      <TableCell className="whitespace-normal"><p>{s.measurement?.cpuQuota??"미제공"} / {s.measurement?.cpuAffinity?.join(", ")??"affinity 미지정"}</p><p>{s.measurement?.dataset??"데이터셋 미제공"}</p></TableCell>
      <TableCell><MeasurementStatus assessment={run.assessment}/></TableCell>
      <TableCell className="max-w-96 whitespace-normal"><ul className="space-y-1 text-xs">{run.assessment?.reasons?.map((reason,index)=><li key={index}>{reason.message} ({reason.code})</li>)}</ul>{run.ownerReferences?.filter(owner=>owner.controller&&owner.kind==="BatchEnergyOptimization").map(owner=><Link key={owner.uid} className="flex min-h-11 items-center break-all text-primary underline" href={`/measurements/batch-energy-optimizations/${encodeURIComponent(run.namespace)}/${encodeURIComponent(owner.name)}?uid=${encodeURIComponent(owner.uid)}`}>{owner.name}</Link>)}</TableCell>
    </TableRow>})}</TableBody></Table></div></QueryGate>
    {!query.isPending&&!query.error&&!rows.length&&<p>조건에 맞는 회차가 없어요. 필터를 변경하세요.</p>}
    <div className="flex items-center justify-between gap-3"><span className="text-sm">{page}페이지</span><div className="flex gap-2"><Button variant="outline" disabled={page===1||query.isFetching} onClick={()=>go(page-1)}>이전</Button><Button variant="outline" disabled={query.data?.page?.nextOffset===undefined||query.isFetching} onClick={()=>go(page+1)}>다음</Button></div></div>
    <SourceExport value={query.data} filename="chill-measurement-runs.json"/><SourceJSON value={query.data} title="현재 페이지 회차 원본"/>
  </Screen>
}
export function MeasurementRunsPage() {return <RunList/>}
export function MeasurementGroupPage() {return <RunList group/>}
