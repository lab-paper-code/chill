"use client"

import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { CurvePlanSpec, ProfilesView } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { formatMeasurementTime } from "@/lib/measurements"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Screen, Facts } from "@/components/screen"
import { DataTable, useServerTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { MeasurementNavigation, MeasurementStatus, MeasurementEvidenceChecks } from "@/components/measurement-navigation"
import { AssessmentDetails, ResultRows } from "@/features/measurement-plans"
import { MeasurementFilters } from "@/features/measurement-coverage"

export const conditionRows = (condition:CurvePlanSpec):[string,string][] => [
  ["모델 파일", `${condition.logicalModel} / ${condition.artifact.name}`],
  ["실행 방식", `${condition.runtimeFamily} / ${condition.backend}`],
  ["전처리기", condition.preprocessor.name || "없음"],
  ["데이터셋", condition.activeProtocol.dataset?.name ?? "없음"],
]
export function SchedulerProfilesPage() {
  const {config}=useSession(),params=useSearchParams(),namespace=params.get("namespace")??config.namespace,tableState=useServerTable()
  const search=new URLSearchParams(tableState.query)
  for(const key of ["q","model","deviceClass","node","powerMode","path","cpu","status"]) {const value=params.get(key);if(value)search.set(key,value)}
  const query=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${search}`,{decoder:parseSchedulerJSON})
  const detail=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams({profile:params.get("profile")??"",limit:"1"})}`,{enabled:!!params.get("profile"),decoder:parseSchedulerJSON})
  const selected=detail.data?.profiles?.find(profile=>profile.id===params.get("profile"))??query.data?.profiles?.find(profile=>profile.id===params.get("profile"))
  function href(id:string) {const next=new URLSearchParams(params.toString());next.set("profile",id);return `/scheduler/profiles?${next}#profile-result`}
  return <Screen width="data" actionPlacement="header" title="측정 프로파일" lede="같은 장비와 실행 조건에서 측정한 결과예요. 다른 Node, 이미지, CPU, 데이터셋은 합치지 않아요." action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="profiles" namespace={namespace}/>
    <MeasurementFilters editable axes={[{key:"model",label:"모델",values:query.data?.filters?.models??[]},{key:"deviceClass",label:"장치 종류",values:query.data?.filters?.deviceClasses??[]},{key:"node",label:"Node",values:query.data?.filters?.nodes??[]},{key:"powerMode",label:"전력모드",values:query.data?.filters?.powerModes??[]},{key:"path",label:"실행 경로",values:query.data?.filters?.paths??[]},{key:"cpu",label:"CPU quota",values:query.data?.filters?.cpus??[]}]}/>
    <QueryGate query={query}><DataTable searchable={false} data={query.data?.profiles??[]} server={{...tableState,total:query.data?.page?.total??0,busy:query.isFetching}} columns={[
      {key:"model",label:"모델",sortValue:p=>p.condition.logicalModel,value:p=><Link className="inline-flex min-h-11 items-center text-primary underline" href={href(p.id)}>{p.condition.logicalModel}</Link>},
      {key:"deviceClass",label:"장치 종류",sortValue:p=>p.condition.deviceClass,value:p=>p.condition.deviceClass},
      {key:"node",label:"Node",sortValue:p=>p.nodeName,value:p=><span>{p.nodeName}{p.reuse&&<span className="block text-xs">재사용 대상 {p.reuse.targetNode.name}</span>}</span>},
      {key:"powerMode",label:"전력모드",sortValue:p=>p.condition.powerMode,value:p=>p.condition.powerMode},
      {key:"path",label:"실행 경로",sortValue:p=>p.condition.artifact.executionPath,value:p=>p.condition.artifact.executionPath},
      {key:"cpu",label:"CPU / affinity",sortValue:p=>p.condition.cpu.limit,value:p=><span className="tabular-nums">{p.condition.cpu.limit} / {p.condition.cpu.affinityCPUs?.join(", ")??"미지정"}</span>},
      {key:"batch",label:"실제 batch",value:p=><span className="tabular-nums">{p.batches?.map(b=>b.batch).join(", ")??"미제공"}</span>},
      {key:"status",label:"스케줄러 / 측정",sortValue:p=>p.assessment?.displayStatus??"unknown",value:p=><><MeasurementStatus assessment={p.assessment}/><MeasurementEvidenceChecks assessment={p.assessment} rows={p.resultRows}/></>},
      {key:"created",label:"측정일 (KST)",sortValue:p=>p.measuredAt,value:p=><time className="tabular-nums" title={p.measuredAt}>{formatMeasurementTime(p.measuredAt)}</time>},
      {key:"source",label:"결과와 출처",value:p=><Link className="inline-flex min-h-11 items-center text-primary underline" href={href(p.id)}>결과 행 보기</Link>},
    ]}/></QueryGate>
    {params.get("profile")&&<QueryGate query={detail}>{!selected&&!detail.isPending&&!detail.error&&<p className="notice">선택한 profile ID가 현재 catalog에 없어요. 다른 프로파일로 대체하지 않아요.</p>}</QueryGate>}
    {selected&&<section id="profile-result" className="scroll-mt-24 space-y-5 border-t pt-5" aria-label="선택 프로파일 결과"><h2 className="break-all text-lg font-semibold">{selected.condition.logicalModel} / {selected.nodeName}</h2><ResultRows rows={selected.resultRows??[]} filename={`chill-profile-${selected.id}.csv`}/><details><summary>판정·근거·동결 조건</summary><AssessmentDetails assessment={selected.assessment} rows={selected.resultRows}/><Facts label="동결 조건" items={conditionRows(selected.condition)}/><SourceExport value={selected} filename={`chill-profile-${selected.id}.json`}/><SourceJSON value={selected} title="동결 조건과 원본 수치"/></details></section>}
    {!!query.data?.insufficiencies?.length&&<section className="space-y-3 border-t pt-4"><h2 className="font-semibold">측정 결과 확인 필요</h2><ul className="space-y-3 text-sm">{query.data.insufficiencies.map((issue,index)=><li key={index}><p>{issue.source.Namespace}/{issue.source.Name}</p><p>사용할 측정 결과가 부족해요. 원본 자료에서 상세 사유를 확인하세요.</p></li>)}</ul></section>}
    <p className="text-sm text-muted-foreground">수치가 제공되지 않으면 미제공으로 남겨요.</p><SourceExport value={query.data} filename="chill-profiles.json"/><SourceJSON value={query.data} title="프로파일 목록 원본"/>
  </Screen>
}
