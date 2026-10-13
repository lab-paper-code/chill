"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams, type ReadonlyURLSearchParams } from "next/navigation"
import type { QueryObserverBaseResult } from "@tanstack/react-query"
import { useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { SchedulerObservation, SchedulerObservationPage } from "@/lib/read-types"
import { parseSchedulerJSON, nanosToMilliseconds } from "@/lib/scheduler"
import { schedulerRecordRef, schedulerRecordParams, readSchedulerRecord, schedulerRecordPath, schedulerRecordMatches, schedulerPageParams, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { SnapshotDetails, CandidateDetails, judgmentLabels } from "@/features/scheduler"
import { SettingChanges } from "@/features/scheduler-settings"
import { ReadRegion, SourceJSON, SourceExport } from "@/components/read-region"
import { DataTable } from "@/components/data-table"
import { SubNav } from "@/components/sub-nav"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

export type SchedulerHistoryRow={event:SchedulerObservation;ref:SchedulerRecordRef}
export type SchedulerHistoryQuery={
  query:QueryObserverBaseResult<SchedulerObservationPage,ApiError>;rows:SchedulerHistoryRow[];params:ReadonlyURLSearchParams
  submitted:{from:string;to:string};cursor:string;draft:{from:string;to:string};setDraft:React.Dispatch<React.SetStateAction<{from:string;to:string}>>
  limit:number;setLimit:React.Dispatch<React.SetStateAction<number>>;validation:string;maxPageSize:number;apply:()=>void;page:(cursor?:string)=>void
}
const kinds:Record<string,string>={decision:"운영 판단",snapshot:"실제 구성",settings:"운영 설정 변경",action:"변경 동작",power:"전력 관측",other:"기타 기록"}
function localDateTime(value:string){const date=new Date(value);return Number.isFinite(date.getTime())?new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,16):""}

export function useSchedulerHistory():SchedulerHistoryQuery {
  const params=useSearchParams(),router=useRouter(),pathname=usePathname(),{config}=useSession()
  const [entry]=React.useState(()=>{const now=Date.now();return {from:new Date(now-3600000).toISOString(),to:new Date(now).toISOString()}})
  const submitted={from:params.get("from")??entry.from,to:params.get("to")??entry.to},cursor=params.get("cursor")??"0"
  const [draft,setDraft]=React.useState(submitted),[limit,setLimit]=React.useState(Number(params.get("limit")??config.pageSize)),[validation,setValidation]=React.useState("")
  const address=new URLSearchParams({...submitted,limit:params.get("limit")??String(config.pageSize)}).toString(),[previousAddress,setPreviousAddress]=React.useState(address)
  if(previousAddress!==address){setPreviousAddress(address);setDraft(submitted);setLimit(Number(params.get("limit")??config.pageSize))}
  React.useEffect(()=>{if(!params.has("from")||!params.has("to")){const next=new URLSearchParams(params.toString());if(!next.has("from"))next.set("from",entry.from);if(!next.has("to"))next.set("to",entry.to);router.replace(`${pathname}?${next}`)}},[entry,params,pathname,router])
  const request=new URLSearchParams({...submitted,limit:params.get("limit")??String(config.pageSize)});if(params.has("cursor"))request.set("cursor",params.get("cursor")!)
  const query=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${request}`,{decoder:parseSchedulerJSON})
  const rows=(query.data?.events??[]).map((event,index)=>({event,ref:schedulerRecordRef(event,submitted.from,submitted.to,(BigInt(cursor)+BigInt(index)).toString())}))
  function apply(){
    const from=Date.parse(draft.from),to=Date.parse(draft.to)
    if(!Number.isFinite(from)||!Number.isFinite(to)||from>to||draft.from===draft.to||to-from>config.maxWindowHours*3600000||!Number.isInteger(limit)||limit<1||limit>config.maxPageSize){setValidation(`조회 구간은 최대 ${config.maxWindowHours}시간, 표시 개수는 1~${config.maxPageSize}로 선택합니다.`);return}
    const next=schedulerPageParams(new URLSearchParams(params.toString()));next.set("from",draft.from);next.set("to",draft.to);next.set("limit",String(limit));setValidation("");router.replace(`${pathname}?${next}`)
  }
  function page(nextCursor?:string){router.replace(`${pathname}?${schedulerPageParams(new URLSearchParams(params.toString()),nextCursor)}`)}
  return {query,rows,params,submitted,cursor,draft,setDraft,limit,setLimit,validation,maxPageSize:config.maxPageSize,apply,page}
}

export function HistoryControls({history}:{history:SchedulerHistoryQuery}){
  return <><form className="toolbar" onSubmit={event=>{event.preventDefault();history.apply()}}><label>시작<Input type="datetime-local" value={localDateTime(history.draft.from)} onChange={event=>history.setDraft(current=>({...current,from:event.target.value?new Date(event.target.value).toISOString():""}))}/></label><label>종료<Input type="datetime-local" value={localDateTime(history.draft.to)} onChange={event=>history.setDraft(current=>({...current,to:event.target.value?new Date(event.target.value).toISOString():""}))}/></label><label>표시 개수<Input type="number" min={1} max={history.maxPageSize} value={Number.isFinite(history.limit)?history.limit:""} onChange={event=>history.setLimit(Number(event.target.value))}/></label><Button type="submit">구간 적용</Button></form>{history.validation&&<p role="alert">{history.validation}</p>}<p className="break-all">제출된 구간 {history.submitted.from} ~ {history.submitted.to} [from, to)</p></>
}
export function HistoryPaging({history}:{history:SchedulerHistoryQuery}){
  return <><p>현재 가져온 원본 기록 {history.rows.length}개 / 시작 offset {history.cursor}. 보관 중인 append 순서이며 전체 구간의 건수와 완전성은 제공하지 않습니다.</p><div className="toolbar"><Button variant="outline" onClick={()=>history.page()} disabled={history.cursor==="0"}>첫 기록</Button><Button variant="outline" onClick={()=>history.page(history.query.data?.nextCursor)} disabled={!history.query.data?.nextCursor}>다음 기록</Button></div></>
}

function RecordDetails({row}:{row:SchedulerHistoryRow}){
  const event=row.event,decision=event.decision,snapshot=event.snapshot
  const view=decision&&snapshot?{settings:decision.settings,state:{decision,snapshot,actions:snapshot.actions,results:null}}:null
  return <><p>{kinds[row.ref.kind]??row.ref.kind} / 사건 {event.at} / offset {row.ref.offset}</p>{decision&&<><h3>판단 {judgmentLabels[decision.status] ?? decision.status}</h3><p>판단 시각 {decision.at} / 자동 적용 {decision.settings.controls.enabled?"사용":"미사용"}</p><p>입력 관측 {decision.search.windowStart} ~ {decision.search.windowEnd}, 모집단 {decision.search.population}, 미래 예상 기간 {nanosToMilliseconds(decision.settings.horizonNanos)} ms</p><p>{decision.reasons?.join(", ")||"제공된 판단 사유 없음"}</p>{view?<><details><summary>기록의 유지안 근거</summary><CandidateDetails candidate={decision.keep} view={view}/></details>{decision.target&&<details><summary>기록의 선택 목표 근거</summary><CandidateDetails candidate={decision.target} view={view}/></details>}</>:<p>같은 기록에 실제 구성 snapshot이 제공되지 않았습니다. 후보 원본은 아래에서 확인합니다.</p>}</>}{snapshot&&<details><summary>기록의 실제 구성</summary><SnapshotDetails snapshot={snapshot}/></details>}{event.settings&&<><h3>설정 변경</h3><p>행위자 {event.settings.actorUsername} / {event.settings.actorUID}</p><p>검증 {event.settings.validatedAt} / Runtime 적용 {event.settings.appliedAt??"제공 없음"}</p><p>마지막 관측 노드 {event.settings.affected?.map(node=>`${node.name} (${node.uid})`).join(", ")||"없음"}</p>{event.settings.error&&<p role="alert">{event.settings.error}</p>}<SettingChanges before={event.settings.before} after={event.settings.after} stored/><p>설정 저장과 실제 물리 전환은 별도입니다.</p></>}{event.action&&<><p>{event.action.kind} / {event.action.node.name} / {event.action.phase} / 시작 {event.action.startedAt} / 완료 {event.action.completedAt??"제공 없음"}</p><Link href={`/transitions/${encodeURIComponent(event.action.id)}?${schedulerRecordParams(row.ref)}`}>이 변경 동작 상세</Link></>}{(event.power||event.powerError||event.powerJoules!==undefined)&&<Link href={`/scheduler/power?${schedulerRecordParams(row.ref)}`}>이 전력 관측 상세</Link>}<SourceJSON inline title="선택 기록 원본" value={event}/><SourceExport value={{conditions:row.ref,population:decision?.search.population??null,source:event}} filename="chill-scheduler-selected-record.json"/></>
}

export function SchedulerHistoryPage({settingsOnly=false}:{settingsOnly?:boolean}){
  const history=useSchedulerHistory(),router=useRouter(),pathname=usePathname(),params=history.params
  const filter=settingsOnly?"settings":params.get("filter")??"all",rows=history.rows.filter(row=>filter==="all"||row.ref.kind===filter)
  const opener=React.useRef<HTMLButtonElement|null>(null),heading=React.useRef<HTMLHeadingElement|null>(null)
  const a=readSchedulerRecord(params,"a"),b=readSchedulerRecord(params,"b"),selected=readSchedulerRecord(params)
  const requested=["offset","kind","at","id","node","startedAt"].some(key=>params.has(key))
  const record=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(selected),{decoder:parseSchedulerJSON,enabled:requested&&!!selected})
  const event=record.data?.events?.[0],matches=selected&&schedulerRecordMatches(selected,event)
  function select(row:SchedulerHistoryRow,prefix=""){
    const next=new URLSearchParams(params.toString())
    for(const [key,value] of schedulerRecordParams(row.ref,prefix))next.set(key,value)
    router.replace(`${pathname}?${next}`,{scroll:false})
  }
  function close(){
    router.replace(`${pathname}?${schedulerPageParams(new URLSearchParams(params.toString()),params.get("cursor")??undefined)}`,{scroll:false})
  }
  const compare=new URLSearchParams()
  if(a)for(const [key,value] of schedulerRecordParams(a,"a"))compare.set(key,value)
  if(b)for(const [key,value] of schedulerRecordParams(b,"b"))compare.set(key,value)
  return <div className="page">
    <h1 ref={heading} tabIndex={-1}>{settingsOnly?"운영 설정 변경 기록":"운영 판단 기록"}</h1>
    <SubNav label="운영 기록 관련 화면" links={[...(settingsOnly?[["/changes","서비스 변경"] as [string,string]]:[]),["/changes?scope=scheduler","운영 설정 변경"],...(settingsOnly?[]:[["/scheduler/history","운영 판단 기록"] as [string,string]]),["/scheduler","현재 운영 판단"],["/scheduler/power","전력 기록"],["/transitions?view=history","변경 동작 기록"]]}/>
    <HistoryControls history={history}/>
    {!settingsOnly&&<label>현재 가져온 기록에서 종류<select value={filter} onChange={event=>{
      const next=new URLSearchParams(params.toString());next.set("filter",event.target.value);router.replace(`${pathname}?${next}`)
    }}><option value="all">모든 종류</option>{Object.entries(kinds).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>}
    <ReadRegion title="현재 페이지의 보관 기록" query={history.query}>{history.query.data&&<>
      <p>조회된 페이지 일부이며 구간 전체 집계가 아닙니다.</p>
      <DataTable data={rows} columns={[
        {key:"at",label:"사건 시각",value:row=>row.event.at},
        {key:"kind",label:"종류",value:row=>kinds[row.ref.kind]??row.ref.kind},
        {key:"subject",label:settingsOnly?"행위자와 적용":"대상",value:row=>row.event.settings?`${row.event.settings.actorUsername} / ${row.event.settings.appliedAt??row.event.settings.error??"검증된 의도"}`:row.event.action?`${row.event.action.node.name} / ${row.event.action.kind} / ${row.event.action.phase}`:(row.event.decision ? judgmentLabels[row.event.decision.status] ?? row.event.decision.status : row.event.node?.name) ?? "제공 없음"},
        {key:"detail",label:"선택",value:row=><div className="toolbar">
          <Button variant="ghost" onClick={event=>{opener.current=event.currentTarget;select(row)}}>상세</Button>
          {row.event.decision&&!settingsOnly&&<><Button variant="ghost" onClick={()=>select(row,"a")}>A로 선택</Button><Button variant="ghost" onClick={()=>select(row,"b")}>B로 선택</Button></>}
        </div>},
      ]}/>
      <HistoryPaging history={history}/>
      <SourceExport value={{conditions:{...history.submitted,cursor:history.cursor,limit:params.get("limit")??history.limit,filter},source:history.query.data}} filename="chill-scheduler-current-page.json"/>
    </>}</ReadRegion>
    {!settingsOnly&&<section aria-label="비교할 판단 선택">
      <p>A {a?`${a.at} / offset ${a.offset}`:"판단 목록에서 선택"}</p><p>B {b?`${b.at} / offset ${b.offset}`:"판단 목록에서 선택"}</p>
      {a&&b?<Link href={`/scheduler/compare?${compare}`}>선택한 두 판단 비교</Link>:<p>판단 기록에서 A와 B를 각각 선택합니다.</p>}
    </section>}
    <Dialog open={requested} onOpenChange={open=>{if(!open)close()}}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-3xl" onCloseAutoFocus={event=>{
        event.preventDefault();if(opener.current?.isConnected)opener.current.focus();else heading.current?.focus()
      }}>
        <DialogHeader><DialogTitle>선택 기록 상세</DialogTitle><DialogDescription>원래 조회 구간의 append offset과 종류, 대상 및 사건 시각으로 기록을 확인합니다.</DialogDescription></DialogHeader>
        {!selected?<p role="alert">선택한 기록 확인 필요. 구간과 offset, 예상 종류 및 시각을 함께 확인합니다.</p>:
          <ReadRegion inlineSource title="선택한 단일 기록" query={record}>{record.isSuccess&&(matches&&event?
            <RecordDetails row={{event,ref:selected}}/>:<p role="alert">선택한 기록 확인 필요. 보관 범위나 사건 identity가 달라졌습니다. 같은 시각의 다른 기록으로 대체하지 않습니다.</p>
          )}</ReadRegion>
        }
        <Button variant="outline" onClick={close}>해당 구간 기록으로 돌아가기</Button>
      </DialogContent>
    </Dialog>
  </div>
}
