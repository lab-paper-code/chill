"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import type { CurvePlanSpec, MeasurementAssessment, ProfilesView, ResultRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { parseMeasurementJSON } from "@/lib/measurement-summary"
import { executionPathLabel, formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"
import { assessmentReasons, measurementQuality, measurementSchedulerUsed } from "@/lib/measurement-assessment"
import { STATUS_AXES } from "@/lib/status"
import { StatusCell } from "@/components/status-cell"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts } from "@/components/screen"
import { DataTable, useServerTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { AssessmentDetails, ResultRows } from "@/features/measurement-plans"
import { MeasurementFilters } from "@/features/measurement-coverage"

export const conditionRows = (condition:CurvePlanSpec):[string,string][] => [
  ["모델 파일", `${condition.logicalModel} / ${condition.artifact.name}`],
  ["실행 방식", `${condition.runtimeFamily} / ${condition.backend}`],
  ["전처리기", condition.preprocessor.name || "해당 없음"],
  ["데이터셋", condition.activeProtocol.dataset?.name ?? "미기록"],
]

// Embedded by the canonical results frame; this owns only the profiles API and its pagination.
export function SchedulerProfilesPage() {
  const params=useSearchParams(),router=useRouter(),tableState=useServerTable(),trigger=React.useRef<HTMLAnchorElement|null>(null)
  const search=new URLSearchParams(tableState.query)
  search.set("view","summary")
  search.set("summary","v2")
  for(const key of ["q","model","deviceClass","node","powerMode","path","status"]) {const value=params.get(key);if(value)search.set(key,value)}
  const query=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${search}`,{decoder:parseMeasurementJSON})
  const detail=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams({profile:params.get("profile")??"",limit:"1"})}`,{enabled:!!params.get("profile"),decoder:parseSchedulerJSON})
  const selected=detail.data?.profiles?.find(profile=>profile.id===params.get("profile"))??query.data?.profiles?.find(profile=>profile.id===params.get("profile"))
  function href(id:string) {const next=new URLSearchParams(params.toString());next.set("kind","throughput");next.set("profile",id);return `/measurements/results?${next}`}
  return <>
    <MeasurementFilters editable axes={[{key:"model",label:"모델",values:query.data?.filters?.models??[]},{key:"deviceClass",label:"장치 종류",values:query.data?.filters?.deviceClasses??[]},{key:"node",label:"장치",values:query.data?.filters?.nodes??[]},{key:"powerMode",label:"전력모드",values:query.data?.filters?.powerModes??[]},{key:"path",label:"실행 경로",values:query.data?.filters?.paths??[]}]}/>
    <QueryGate query={query}><DataTable searchable={false} data={query.data?.profiles??[]} server={{...tableState,total:query.data?.page?.total??0,busy:query.isFetching}} columns={[
      {key:"model",label:"모델",sortValue:p=>p.condition.logicalModel,value:p=><Link className="min-w-0 whitespace-normal [overflow-wrap:anywhere] text-primary underline" href={href(p.id)} scroll={false} onClick={event=>{trigger.current=event.currentTarget}}>{p.condition.logicalModel}</Link>},
      {key:"condition",label:"장치·조건",value:p=><div className="grid min-w-0 gap-1 text-xs [overflow-wrap:anywhere]"><span>{p.nodeName}</span><span>{p.condition.deviceClass}</span><span>{p.condition.powerMode}</span><span>{executionPathLabel(p.condition.artifact.executionPath)}</span>{p.reuse&&<span>재사용 대상 {p.reuse.targetNode.name}</span>}</div>},
      {key:"batch",label:"배치",unit:"건",numeric:true,value:p=><ProfileBatches rows={p.resultRows} batches={p.batches}/>},
      {key:"p95",label:"p95",unit:"ms",numeric:true,value:p=><ProfileValues rows={p.resultRows} metric="p95Milliseconds"/>},
      {key:"totalEnergy",label:"총 에너지",unit:"J/건",numeric:true,value:p=><ProfileValues rows={p.resultRows} metric="totalJoulesPerItem"/>},
      {key:"incrementalEnergy",label:"추가 에너지",unit:"J/건",numeric:true,value:p=><ProfileValues rows={p.resultRows} metric="incrementalJoulesPerItem"/>},
      {key:"currentUse",label:"현재 사용",value:p=><ProfileUse assessment={p.assessment}/>},
      {key:"quality",label:"품질 판정",value:p=><ProfileQuality assessment={p.assessment}/>},
      {key:"created",label:"측정일 (KST)",sortValue:p=>p.measuredAt,value:p=><time className="tabular-nums" dateTime={p.measuredAt}>{formatMeasurementTime(p.measuredAt)}</time>},
    ]}/></QueryGate>
    <Button className="justify-self-start" variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>
    <Sheet open={!!params.get("profile")} onOpenChange={open=>{if(!open){const next=new URLSearchParams(params.toString());next.delete("profile");router.replace(`/measurements/results?${next}`,{scroll:false})}}}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-4xl" onCloseAutoFocus={event=>{if(trigger.current){event.preventDefault();trigger.current.focus()}}}>
        <SheetHeader><SheetTitle>{selected?.condition.logicalModel??"처리 성능"}</SheetTitle><SheetDescription className="break-all">{selected?.nodeName??params.get("profile")}</SheetDescription></SheetHeader>
        <div className="grid min-w-0 gap-5 px-4 pb-6"><QueryGate query={detail}>
          {selected?<>
            <div className="flex flex-wrap gap-4"><StatusCell axis="측정" label={selected.assessment?.label??"미확인"} tone="idle"/><ProfileUse assessment={selected.assessment}/><ProfileQuality assessment={selected.assessment}/></div>
            <ResultRows rows={selected.resultRows??[]} filename={`chill-profile-${selected.id}.csv`} emptyMessage="미제공"/>
            <Facts label="동결 조건" items={[...conditionRows(selected.condition),["장치",selected.nodeName],["전력모드",selected.condition.powerMode],["실행 경로",executionPathLabel(selected.condition.artifact.executionPath)],["측정 시각",formatMeasurementTime(selected.measuredAt)]]}/>
            <details><summary className="min-h-11 cursor-pointer py-2">판정 근거</summary><AssessmentDetails assessment={selected.assessment} rows={selected.resultRows}/></details>
            <details><summary className="min-h-11 cursor-pointer py-2">원본·출처</summary><Facts items={[["프로파일 ID",selected.id],["설정 버전",detail.data?.configVersion??"미제공"]]}/><SourceExport value={selected} filename={`chill-profile-${selected.id}.json`}/><SourceJSON inline value={selected} title="동결 조건·원본 수치"/></details>
          </>:<StatusCell axis="측정" label="미확인" tone="idle" reason="현재 목록에 선택한 프로파일 ID 없음"/>}
        </QueryGate><Button className="justify-self-start" variant="outline" disabled={detail.isFetching} onClick={()=>void detail.refetch()}>갱신</Button></div>
      </SheetContent>
    </Sheet>
    <QueryGate query={query}>
      {!!query.data?.insufficiencies?.length&&<section className="grid gap-2"><h2 className="font-semibold">측정 제한</h2><table className="w-full table-fixed text-left text-sm"><thead><tr><th>저장 영역</th><th>요청</th><th>사유</th></tr></thead><tbody>{query.data.insufficiencies.map((issue,index)=><tr key={index}><td className="break-all">{issue.source.Namespace}</td><td className="break-all"><Link className="underline" href={`/measurements/requests?${new URLSearchParams({namespace:issue.source.Namespace,request:issue.source.Name})}`}>{issue.source.Name}</Link></td><td className="break-words"><span title={issue.reason}>{issue.code==="ReplacementSelection"?"대체 측정 선택":issue.code==="ProfileReuseRejected"?"재사용 불가 · 대상 측정 필요":"측정 제한 · 사유 확인"}</span></td></tr>)}</tbody></table></section>}
      <details><summary className="min-h-11 cursor-pointer py-2">목록 원본</summary><SourceExport value={query.data} filename="chill-profiles.json"/><SourceJSON inline value={query.data} title="프로파일 목록 원본"/></details>
    </QueryGate>
  </>
}
function ProfileValues({rows,metric}:{rows?:ResultRow[];metric:"p95Milliseconds"|"totalJoulesPerItem"|"incrementalJoulesPerItem"}) {
  return rows?.length?<span>{formatMeasurementNumber(rows[0][metric],metric==="p95Milliseconds"?"ms":"J/건")}</span>:<span>미제공</span>
}
function ProfileBatches({rows,batches}:{rows?:ResultRow[];batches?:{batch:number}[]|null}) {
  const representative=rows?.[0]?.batch??batches?.[0]?.batch
  return representative===undefined?<span>미제공</span>:<div className="grid min-w-0 gap-1"><span>대표 배치 {representative} · 첫 기록</span><details><summary className="min-h-11 cursor-pointer py-2">전체 배치</summary>{rows?.length?<ResultRows rows={rows}/>:<ul>{batches?.map((batch,index)=><li key={index}>{batch.batch}</li>)}</ul>}</details></div>
}
function ProfileUse({assessment}:{assessment?:MeasurementAssessment}) {
  const used=measurementSchedulerUsed(assessment)
  return <StatusCell axis={STATUS_AXES.currentUse} label={used===true?"사용 중":used===false?"과거 측정":"미확인"} tone={used===true?"info":"idle"}/>
}
function ProfileQuality({assessment}:{assessment?:MeasurementAssessment}) {
  const quality=measurementQuality([assessment])
  return <StatusCell axis={STATUS_AXES.quality} label={quality.label} tone={quality.state==="pass"?"ok":quality.state==="fail"?"fail":"idle"} reason={quality.state==="unknown"?assessmentReasons(assessment).join(" · ")||undefined:undefined}/>
}
