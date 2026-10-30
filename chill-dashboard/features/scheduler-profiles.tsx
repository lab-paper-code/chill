"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { CurvePlanSpec, ProfilesView } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { formatMeasurementTime } from "@/lib/measurements"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Screen, Facts } from "@/components/screen"
import { DataTable } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { AssessmentDetails, ResultRows } from "@/features/measurement-plans"

export const conditionRows = (condition:CurvePlanSpec):[string,string][] => [
  ["모델 파일", `${condition.logicalModel} / ${condition.artifact.name}`],
  ["실행 방식", `${condition.runtimeFamily} / ${condition.backend}`],
  ["전처리기", condition.preprocessor.name || "없음"],
  ["데이터셋", condition.activeProtocol.dataset?.name ?? "없음"],
]
export function SchedulerProfilesPage() {
  const router=useRouter(),{config}=useSession(),params=useSearchParams(),namespace=params.get("namespace")??config.namespace
  const model=params.get("model")??"",deviceClass=params.get("deviceClass")??"",offset=Math.max(0,Number(params.get("offset"))||0)
  const query=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams({limit:"25",offset:String(offset),...(model?{model}:{}),...(deviceClass?{deviceClass}:{})})}`,{decoder:parseSchedulerJSON})
  const detail=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams({profile:params.get("profile")??"",limit:"1"})}`,{enabled:!!params.get("profile"),decoder:parseSchedulerJSON})
  function filter(key:string,value:string) {const next=new URLSearchParams(params.toString());next.delete("profile");next.delete("offset");if(value)next.set(key,value);else next.delete(key);router.replace(`/scheduler/profiles?${next}`)}
  const selected=detail.data?.profiles?.find(profile=>profile.id===params.get("profile"))??query.data?.profiles?.find(profile=>profile.id===params.get("profile"))
  function href(id:string) {const next=new URLSearchParams(params.toString());next.set("profile",id);return `/scheduler/profiles?${next}#profile-result`}
  return <Screen width="data" actionPlacement="header" title="측정 프로파일" lede="현재 catalog의 정확 동결 조건별 결과예요. 다른 Node, 이미지, CPU, 데이터셋은 합치지 않아요." action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="profiles" namespace={namespace}/>
    <div className="flex flex-wrap gap-4"><label className="grid gap-2 text-sm">모델<select className="min-h-11 rounded border bg-background p-2" value={model} onChange={event=>filter("model",event.target.value)}><option value="">전체 모델</option>{query.data?.filters?.models.map(name=><option key={name}>{name}</option>)}</select></label><label className="grid gap-2 text-sm">장치 종류<select className="min-h-11 rounded border bg-background p-2" value={deviceClass} onChange={event=>filter("deviceClass",event.target.value)}><option value="">전체 장치</option>{query.data?.filters?.deviceClasses.map(name=><option key={name}>{name}</option>)}</select></label></div>
    <QueryGate query={query}><DataTable data={query.data?.profiles??[]} columns={[
      {key:"model",label:"모델",sortValue:p=>p.condition.logicalModel,value:p=><Link className="inline-flex min-h-11 items-center text-primary underline" href={href(p.id)}>{p.condition.logicalModel}</Link>},
      {key:"class",label:"장치 종류",sortValue:p=>p.condition.deviceClass,value:p=>p.condition.deviceClass},
      {key:"node",label:"Node",sortValue:p=>p.nodeName,value:p=><span>{p.nodeName}{p.reuse&&<span className="block text-xs">재사용 대상 {p.reuse.targetNode.name}</span>}</span>},
      {key:"mode",label:"전력모드",sortValue:p=>p.condition.powerMode,value:p=>p.condition.powerMode},
      {key:"path",label:"실행 경로",sortValue:p=>p.condition.artifact.executionPath,value:p=>p.condition.artifact.executionPath},
      {key:"cpu",label:"CPU / affinity",sortValue:p=>p.condition.cpu.limit,value:p=><span className="tabular-nums">{p.condition.cpu.limit} / {p.condition.cpu.affinityCPUs?.join(", ")??"미지정"}</span>},
      {key:"batch",label:"실제 batch",value:p=><span className="tabular-nums">{p.batches?.map(b=>b.batch).join(", ")??"미제공"}</span>},
      {key:"status",label:"서버 판정",value:p=><MeasurementStatus assessment={p.assessment}/>},
      {key:"date",label:"측정일 (KST)",sortValue:p=>p.measuredAt,value:p=><time className="tabular-nums" title={p.measuredAt}>{formatMeasurementTime(p.measuredAt)}</time>},
      {key:"source",label:"결과와 출처",value:p=><Link className="inline-flex min-h-11 items-center text-primary underline" href={href(p.id)}>결과 행 보기</Link>},
    ]}/></QueryGate>
    {query.data?.page&&<nav className="flex flex-wrap items-center gap-3" aria-label="프로파일 페이지"><Button variant="outline" disabled={offset===0} onClick={()=>filter("offset",String(Math.max(0,offset-25)))}>이전</Button><span className="text-sm tabular-nums">전체 {query.data.page.total}건 · {offset+1}부터</span><Button variant="outline" disabled={query.data.page.nextOffset==null} onClick={()=>filter("offset",String(query.data?.page?.nextOffset))}>다음</Button></nav>}
    {params.get("profile")&&<QueryGate query={detail}>{!selected&&!detail.isPending&&!detail.error&&<p className="notice">선택한 profile ID가 현재 catalog에 없어요. 다른 프로파일로 대체하지 않아요.</p>}</QueryGate>}
    {selected&&<section id="profile-result" className="scroll-mt-24 space-y-5 border-t pt-5" aria-label="선택 프로파일 결과"><h2 className="break-all text-lg font-semibold">{selected.condition.logicalModel} / {selected.nodeName} 결과</h2><ResultRows rows={selected.resultRows??[]} filename={`chill-profile-${selected.id}.csv`}/><AssessmentDetails assessment={selected.assessment}/><Facts label="동결 조건" items={conditionRows(selected.condition)}/><SourceExport value={selected} filename={`chill-profile-${selected.id}.json`}/><SourceJSON value={selected} title="동결 조건과 원본 수치"/></section>}
    {!!query.data?.insufficiencies?.length&&<section className="space-y-3 border-t pt-4"><h2 className="font-semibold">측정 근거 사유</h2><ul className="space-y-3 text-sm">{query.data.insufficiencies.map((issue,index)=><li key={index}><p>{issue.source.Namespace}/{issue.source.Name}</p><p className="break-words">{issue.code}: {issue.reason}</p></li>)}</ul></section>}
    <p className="text-sm text-muted-foreground">catalog 채택과 결과 체크리스트 충족은 별개예요. 통계가 제공되지 않으면 미제공으로 남겨요.</p><SourceExport value={query.data} filename="chill-profiles.json"/><SourceJSON value={query.data} title="catalog 전체 원본"/>
  </Screen>
}
