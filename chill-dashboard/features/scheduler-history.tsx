"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams, type ReadonlyURLSearchParams } from "next/navigation"
import type { QueryObserverBaseResult } from "@tanstack/react-query"
import { useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { SchedulerCapacity, SchedulerObservation, SchedulerObservationPage, SchedulerObservationSummary, SchedulerObservationSummaryPage } from "@/lib/read-types"
import type { WorkloadSummary } from "@/lib/domain"
import { parseSchedulerJSON, nanosToMilliseconds } from "@/lib/scheduler"
import { schedulerRecordRef, schedulerRecordParams, readSchedulerRecord, schedulerRecordPath, schedulerRecordMatches, schedulerPageParams, schedulerComparisonParams, schedulerHistoryNodeMatches, schedulerHistoryKindMatches, schedulerScopeChanges, historyReturnTo, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { gapNotice, utcDateTime } from "@/lib/date-time"
import { displayTime, candidatePlans } from "@/lib/live-display"
import { SnapshotDetails, CandidateDetails, judgmentLabels, actionKinds, actionStatuses, actionNodeName } from "@/features/scheduler"
import { SettingChanges } from "@/features/scheduler-settings"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { statusLabel } from "@/lib/labels"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { RecordsNavigation, RecordsRange } from "@/components/records-navigation"
import { HistoryActionDetails, CurrentTransitionEvidence } from "./scheduler-transitions"
import { SchedulerDecisionSummary, SchedulerPathFeedback, SchedulerWorkloadStates } from "./scheduler-workload-states"
import { resolveWorkloadIdentities } from "@/lib/workload-identity"
import { DecisionRateEvidence } from "./scheduler-capacity"
import { compareAt } from "@/lib/live"

export type SchedulerHistoryRow<T extends SchedulerObservationSummary=SchedulerObservation>={event:T;ref:SchedulerRecordRef}
export type SchedulerHistoryQuery<T extends SchedulerObservationSummary=SchedulerObservation>={
  query:QueryObserverBaseResult<Omit<SchedulerObservationPage,"events">&{events:T[]|null},ApiError>;rows:SchedulerHistoryRow<T>[];params:ReadonlyURLSearchParams
  submitted:{from:string;to:string};cursor:string;draft:{from:string;to:string};setDraft:React.Dispatch<React.SetStateAction<{from:string;to:string}>>
  limit:number;setLimit:React.Dispatch<React.SetStateAction<number>>;validation:string;maxPageSize:number;apply:()=>boolean;page:(cursor?:string)=>void
}
const kinds:Record<string,string>={decision:"운영 판단",snapshot:"실제 구성",settings:"운영 설정 변경",action:"변경 동작",power:"전력 측정",other:"기타 기록"}
const historyFilters:Record<string,string>={all:"모든 종류","decision-action":"판단·전환 기록",...kinds}
// Shared record timestamps use the same 24-hour KST clock as the live board.
export const when=(value?:string)=>!value||value.startsWith("0001-")||!Number.isFinite(Date.parse(value))?"미기록":`${new Intl.DateTimeFormat("ko-KR",{timeZone:"Asia/Seoul",month:"numeric",day:"numeric"}).format(new Date(value))} ${displayTime(value,"KST")}`
export const actorName=(name?:string)=>name?.startsWith("system:serviceaccount:")?name.split(":").at(-1)!:name??"기록 없음"

export function useSchedulerHistory(defaultFilter="decision-action"):SchedulerHistoryQuery<SchedulerObservationSummary> {
  const params=useSearchParams(),router=useRouter(),pathname=usePathname(),{config}=useSession()
  const [entry]=React.useState(()=>{const now=Date.now();return {from:new Date(now-3600000).toISOString(),to:new Date(now).toISOString()}})
  const submitted={from:params.get("from")??entry.from,to:params.get("to")??entry.to},cursor=params.get("cursor")??"0"
  const [draft,setDraft]=React.useState(submitted),[limit,setLimit]=React.useState(Number(params.get("limit")??config.pageSize)),[validation,setValidation]=React.useState("")
  const address=new URLSearchParams({...submitted,limit:params.get("limit")??String(config.pageSize)}).toString(),[previousAddress,setPreviousAddress]=React.useState(address)
  if(previousAddress!==address){setPreviousAddress(address);setDraft(submitted);setLimit(Number(params.get("limit")??config.pageSize))}
  React.useEffect(()=>{if(!params.has("from")||!params.has("to")){const next=new URLSearchParams(params.toString());if(!next.has("from"))next.set("from",entry.from);if(!next.has("to"))next.set("to",entry.to);router.replace(`${pathname}?${next}`)}},[entry,params,pathname,router])
  const request=new URLSearchParams({...submitted,limit:params.get("limit")??String(config.pageSize)});if(params.has("cursor"))request.set("cursor",params.get("cursor")!)
  for(const key of ["order","filter","uid"])if(params.has(key)&&(key!=="uid"||params.get("kind")!=="power"))request.set(key,params.get(key)!)
  request.set("filter",params.get("filter")??defaultFilter)
  request.set("view",params.get("timeline")==="scope"?"compact":"summary")
  const query=useChillQuery<SchedulerObservationSummaryPage>(`v1/observations/scheduler?${request}`,{decoder:parseSchedulerJSON})
  const rows=(query.data?.events??[]).map((event,index)=>({event,ref:schedulerRecordRef(event,submitted.from,submitted.to,(BigInt(cursor)+BigInt(index)).toString())}))
  function apply(){
    const from=Date.parse(draft.from),to=Date.parse(draft.to)
    if(!Number.isFinite(from)||!Number.isFinite(to)||from>to||draft.from===draft.to||to-from>config.maxWindowHours*3600000||!Number.isInteger(limit)||limit<1||limit>config.maxPageSize){setValidation(`기간은 최대 ${config.maxWindowHours}시간, 한 번에 1~${config.maxPageSize}건까지 볼 수 있어요.`);return false}
    const next=schedulerPageParams(new URLSearchParams(params.toString()));next.set("from",draft.from);next.set("to",draft.to);next.set("limit",String(limit));setValidation("");router.replace(`${pathname}?${next}`)
    return true
  }
  function page(nextCursor?:string){router.replace(`${pathname}?${schedulerPageParams(new URLSearchParams(params.toString()),nextCursor)}`)}
  return {query,rows,params,submitted,cursor,draft,setDraft,limit,setLimit,validation,maxPageSize:config.maxPageSize,apply,page}
}

// The window filter as one row; extra filter rows of the same screen go in `children`.
export function HistoryControls({history,children}:{history:SchedulerHistoryQuery<SchedulerObservationSummary>;children?:React.ReactNode}){
  const [open,setOpen]=React.useState(false)
  const inputTimes=[history.draft.from,history.draft.to].map(value=>{const time=Date.parse(value);return Number.isFinite(time)?new Date(time+9*3600000).toISOString().slice(0,16):""})
  return <Rows label="조회 조건">
    <RowDialog open={open} onOpenChange={next=>{setOpen(next);if(next)history.setDraft(history.submitted)}} label="기간 변경" title="조회 기간" description="시작 시각부터 끝 시각 직전까지의 기록을 보여드려요.">
      <form noValidate className="grid gap-4" onSubmit={event=>{event.preventDefault();if(history.apply())setOpen(false)}}>
        <label className="grid gap-1.5">시작 KST<Input type="datetime-local" value={inputTimes[0]} onChange={event=>{const value=event.currentTarget.validity.valid?event.currentTarget.value:"",from=utcDateTime(value?`${value}+09:00`:"");history.setDraft(current=>({...current,from}))}}/></label>
        <label className="grid gap-1.5">종료 KST<Input type="datetime-local" value={inputTimes[1]} onChange={event=>{const value=event.currentTarget.validity.valid?event.currentTarget.value:"",to=utcDateTime(value?`${value}+09:00`:"");history.setDraft(current=>({...current,to}))}}/></label>
        <label className="grid gap-1.5">한 번에 볼 기록 수<Input type="number" min={1} max={history.maxPageSize} value={Number.isFinite(history.limit)?history.limit:""} onChange={event=>history.setLimit(Number(event.target.value))}/></label>
        {history.validation&&<p role="alert">{history.validation}</p>}
        <Button type="submit">이 기간으로 보기</Button>
      </form>
    </RowDialog>
    {children}
  </Rows>
}
export function HistoryPaging({history}:{history:SchedulerHistoryQuery<SchedulerObservationSummary>}){
  return <div className="grid gap-2">{history.query.data?.gapUntil&&<p role="status">{gapNotice(history.query.data.gapUntil)}</p>}<div className="flex gap-2"><Button variant="outline" onClick={()=>history.page()} disabled={history.cursor==="0"}>처음으로</Button><Button variant="outline" onClick={()=>history.page(history.query.data?.nextCursor)} disabled={!history.query.data?.nextCursor}>다음 기록 보기</Button></div></div>
}

export function DecisionCapacity({capacity,names}:{capacity?:SchedulerCapacity|null;names:ReadonlyMap<string,string>}){
  return <DecisionRateEvidence capacity={capacity} names={names}/>
}

function RecordDetails({row,names}:{row:SchedulerHistoryRow;names:ReadonlyMap<string,string>}){
  const event=row.event,decision=event.decision,snapshot=event.snapshot
  const view=decision&&snapshot?{settings:decision.settings,state:{decision,snapshot,actions:snapshot.actions,results:null}}:null
  const serviceNames=new Map(names)
  for(const workload of snapshot?.workloads??[])if(workload)serviceNames.set(workload.uid,workload.name)
  return <div className="space-y-4 [&_dd]:break-all">
    <Facts items={[["종류",kinds[row.ref.kind]??"기타 기록"],["사건 시각 KST",when(event.at)],["보관 순서",row.ref.offset]]}/>
    {decision&&<>
      <SchedulerDecisionSummary at={when(decision.at)} status={`${judgmentLabels[decision.status]??"판단 미확인"} (${decision.status})`} reasons={decision.reasons}/>
      <SchedulerWorkloadStates workloads={decision.workloads} names={serviceNames}/>
      <SchedulerPathFeedback decision={decision} names={serviceNames}/>
      <details><summary>판단 입력·설정</summary><Facts items={[["자동 운영 설정 대상",decision.settings.autonomousWorkloads==null?"미기록":`${decision.settings.autonomousWorkloads.length}개`],["입력 관측 KST",`${when(decision.search.windowStart)} ~ ${when(decision.search.windowEnd)}`],["에너지 비교 기간",decision.capacity?`${nanosToMilliseconds(decision.capacity.periodNanos)} ms`:"미기록"]]}/></details>
      <DecisionCapacity capacity={decision.capacity} names={serviceNames}/>
      {view?<>{candidatePlans(decision.keep,decision.target).map(candidate=><details key={candidate.id}><summary>{candidate.keep?"유지안 근거":"선택 목표 근거"}</summary><CandidateDetails candidate={candidate} view={view}/></details>)}</>:<p>같은 기록에 실제 구성이 없어요. 후보 원본은 아래 원본에서 확인해요.</p>}
    </>}
    {snapshot&&<details><summary>기록의 실제 구성</summary><SnapshotDetails snapshot={snapshot}/></details>}
    {event.settings&&<>
      <h3 className="font-semibold">설정 변경</h3>
      <Facts items={[["담당자",actorName(event.settings.actorUsername)],["검증 KST",when(event.settings.validatedAt)],["실행 환경 적용 KST",when(event.settings.appliedAt)],["마지막 관측 노드",event.settings.affected?.map(node=>node.name||"이름 미제공").join(", ")||"없음"]]}/>
      {event.settings.error&&<p role="alert">{event.settings.error}</p>}
      <SettingChanges before={event.settings.before} after={event.settings.after} stored/>
      <Facts items={[["저장·물리 전환","별도"]]} />
    </>}
    {event.action&&<>
      <Facts items={[["동작",actionKinds[event.action.kind]??"구성 변경"],["노드",actionNodeName(event.action.node)],["단계",actionStatuses[event.action.phase]??"미확인"],["시작 KST",when(event.action.startedAt)],["완료 KST",when(event.action.completedAt)]]}/>
      <HistoryActionDetails row={row}/>
    </>}
    {decision&&<><h3 className="font-semibold">입력 부하·용량</h3><SourceJSON inline title="기록된 부하" value={decision.search.loads}/><SourceJSON inline title="기록된 용량" value={decision.capacity}/></>}
    <SourceJSON inline title="선택 기록 원본" value={event}/>
    <SourceExport value={{conditions:row.ref,population:decision?.search.population??null,source:event}} filename="chill-scheduler-selected-record.json"/>
  </div>
}

export function SchedulerHistoryPage({settingsOnly=false}:{settingsOnly?:boolean}){
  const history=useSchedulerHistory(settingsOnly?"settings":"decision-action"),router=useRouter(),pathname=usePathname(),params=history.params
  const workloads=useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const uid=params.get("uid")??"",serviceName=workloads.data?.find(row=>row.desired.metadata.uid===uid)?.desired.metadata.name
  const filter=settingsOnly?"settings":params.get("filter")??"decision-action",filterNode=params.get("filterNode")??""
  const rows=history.rows.filter(row=>schedulerHistoryKindMatches(filter,row.ref.kind)&&schedulerHistoryNodeMatches(filterNode,row.event))
  const scopeView=!settingsOnly&&params.get("timeline")==="scope"
  const pageDecisions=history.rows as SchedulerHistoryRow[]
  const scopeRows=scopeView?schedulerScopeChanges(pageDecisions.filter(row=>schedulerHistoryNodeMatches(filterNode,row.event))):[]
  const pageTimes=scopeView?history.rows.map(row=>row.event.at).sort(compareAt):[]
  const names=resolveWorkloadIdentities(workloads.data??[],[],null,[])
  const targetEvent=filterNode?history.rows.find(row=>schedulerHistoryNodeMatches(filterNode,row.event))?.event:undefined
  const nodeName=(targetEvent?.action?.node??targetEvent?.node)?.name||"선택 장치"
  const opener=React.useRef<HTMLElement|null>(null),[filterOpen,setFilterOpen]=React.useState(false)
  const a=readSchedulerRecord(params,"a"),b=readSchedulerRecord(params,"b"),selected=readSchedulerRecord(params)
  const requested=["offset","at","id","node","startedAt"].some(key=>params.has(key))
  const record=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(selected),{decoder:parseSchedulerJSON,enabled:requested&&!!selected})
  const event=record.data?.events?.[0],matches=selected&&schedulerRecordMatches(selected,event)
  function target(uid:string){
    const next=schedulerPageParams(new URLSearchParams(params.toString()));if(uid)next.set("uid",uid);else next.delete("uid")
    router.replace(`${pathname}?${next}`)
  }
  function select(row:SchedulerHistoryRow<SchedulerObservationSummary>,prefix=""){
    const next=new URLSearchParams(params.toString())
    for(const [key,value] of schedulerRecordParams(row.ref,prefix))next.set(key,value)
    router.replace(`${pathname}?${next}`,{scroll:false})
  }
  function close(){
    router.replace(`${pathname}?${schedulerPageParams(new URLSearchParams(params.toString()),params.get("cursor")??undefined)}`,{scroll:false})
  }
  const compare=schedulerComparisonParams(new URLSearchParams(params.toString()))
  const same=(ref:SchedulerRecordRef|null,row:SchedulerHistoryRow<SchedulerObservationSummary>)=>!!ref&&ref.offset===row.ref.offset&&ref.at===row.ref.at&&ref.from===row.ref.from&&ref.to===row.ref.to
  const loaded=!!history.query.data,returnTo=historyReturnTo(params.get("returnTo"))
  return <Screen width="data" title="기록"
    action={!settingsOnly&&(a||b)?<>{a&&b?<Button asChild><Link href={`/records?${compare}`} scroll={false}>판단 비교</Link></Button>:<Button disabled>판단 비교</Button>}</>:undefined}>
    <RecordsNavigation/>
    <RecordsRange target={filterNode?nodeName:serviceName??(uid||"클러스터 전체")} from={history.submitted.from} to={history.submitted.to} at={history.query.data?.to}/>
    <HistoryControls history={history}>
      <RowDialog label="대상 변경" title="대상 서비스">
        <QueryGate query={workloads}><label className="grid gap-2">서비스<select className="min-h-11 rounded-md border bg-background px-2" value={uid} onChange={event=>target(event.target.value)}>
          <option value="">클러스터 전체</option>
          {uid&&!serviceName&&<option value={uid}>이름 미확인 · {uid}</option>}
          {workloads.data?.map(row=><option key={row.desired.metadata.uid} value={row.desired.metadata.uid}>{row.desired.metadata.name}</option>)}
        </select></label></QueryGate>
        {uid&&<Facts items={[["서비스 UID",uid]]}/>}
        <details><summary className="min-h-11 cursor-pointer py-2">UID 직접 입력</summary><form className="grid gap-3" onSubmit={event=>{event.preventDefault();target(String(new FormData(event.currentTarget).get("uid")??"").trim())}}>
          <label>서비스 UID<Input name="uid" defaultValue={uid}/></label><Button type="submit">조회</Button>
        </form></details>
      </RowDialog>
      {filterNode&&<RowDialog label="장치 사건 · 현재 페이지" value={nodeName} title="장치 사건 범위">
        <Facts items={[["대상",nodeName],["적용 범위","불러온 현재 페이지"],["포함 기록","장치가 명시된 사건"],["클러스터 판단","장치 필터 해제 후 확인"],...(params.get("eventAt")?[["기준 사건 시각 KST",when(params.get("eventAt")!)] as [string,string]]:[])]}/>
        <Button variant="outline" onClick={()=>{const next=new URLSearchParams(params.toString());next.delete("filterNode");router.replace(`${pathname}?${next}`,{scroll:false})}}>전체 장치</Button>
        <SourceJSON inline title="장치 조회 조건 원본" value={{filterNode,eventAt:params.get("eventAt"),scope:"loaded-page"}}/>
      </RowDialog>}
      <Row href={`/records?${new URLSearchParams({...history.submitted,scope:"management",...(params.get("uid")?{uid:params.get("uid")!}:{})})}`} label="서비스 관리 변경"/>
      {!settingsOnly&&<RowDialog open={filterOpen} onOpenChange={setFilterOpen} label="기록 종류" value={historyFilters[filter]??"이전 조건"} title="기록 종류" description="서버 필터 · 다음 페이지에도 유지 · 판단·전환 = 운영 판단 + 변경 동작">
        <fieldset className="grid gap-2">{Object.entries(historyFilters).map(([key,label])=><label key={key} className="flex min-h-11 items-center gap-2"><input type="radio" name="kind" checked={filter===key} onChange={()=>{const next=schedulerPageParams(new URLSearchParams(params.toString()));next.set("filter",key);router.replace(`${pathname}?${next}`);setFilterOpen(false)}}/>{label}</label>)}</fieldset>
      </RowDialog>}
      {history.query.data&&<RowDialog label="자료 내보내기" title="자료 내보내기" description="지금 불러온 기록을 파일로 받아요. 기간 전체 집계는 아니에요.">
        <SourceExport value={{projection:scopeView?"compact":"summary",conditions:{...history.submitted,cursor:history.cursor,limit:params.get("limit")??history.limit,filter,filterNode,nodeFilterScope:filterNode?"loaded-page":null,eventAt:params.get("eventAt")},source:history.query.data}} filename="chill-scheduler-current-page.json"/>
      </RowDialog>}
    </HistoryControls>
    {!settingsOnly&&<label className="flex min-h-11 items-center gap-2">보기<select className="min-h-11 rounded-md border bg-background px-2" value={scopeView?"scope":"records"} onChange={event=>{const next=schedulerPageParams(new URLSearchParams(params.toString()),history.cursor);if(event.target.value==="scope")next.set("timeline","scope");else next.delete("timeline");router.replace(`${pathname}?${next}`)}}><option value="records">기록 목록</option><option value="scope">서비스 상태 변화 · 현재 페이지</option></select></label>}
    <QueryGate query={history.query}>{loaded&&<>
      {scopeView?<section aria-label="서비스 상태 변화" className="space-y-4">
        <p className="text-sm text-muted-foreground">현재 페이지 · {history.rows.length}건 · {when(pageTimes.at(0))} ~ {when(pageTimes.at(-1))} · 첫 행은 페이지 기준 상태 · 이전 페이지와의 변화는 미확인</p>
        {scopeRows.length?scopeRows.map((row,index)=><section key={row.ref.offset} className="space-y-3 rounded-lg border p-3"><h2 className="font-semibold">{index===0?"페이지 기준 상태":"서비스 상태 변화"} · {when(row.event.decision?.at)}</h2><SchedulerDecisionSummary at={when(row.event.decision?.at)} status={`${judgmentLabels[row.event.decision!.status]??"미확인"} (${row.event.decision!.status})`} reasons={row.event.decision?.reasons}/><SchedulerWorkloadStates workloads={row.event.decision?.workloads} names={resolveWorkloadIdentities(workloads.data??[],[],row.event.snapshot?.workloads??[],[])}/><Button variant="outline" onClick={()=>{opener.current=document.activeElement as HTMLElement|null;select(row)}}>판단 근거</Button></section>):<Empty title="현재 페이지에 판단 기록 없음"/>}
      </section>:<>
      {rows.length?<div className="rounded-lg border"><table className="block w-full text-sm sm:table"><thead className="hidden sm:table-header-group"><tr>{["시각 KST","대상","동작·판단","결과","선택"].map(label=><th key={label} className="p-3 text-left">{label}</th>)}</tr></thead><tbody className="block sm:table-row-group">{rows.map(row=>{
        const ev=row.event,mark=[same(a,row)&&"A",same(b,row)&&"B"].filter(Boolean).join(" · ")
        return <tr key={row.ref.offset} className="grid gap-1 border-t p-3 first:border-t-0 sm:table-row sm:p-0"><td className="min-w-0 tabular-nums sm:p-3"><span className="text-muted-foreground sm:hidden">시각 KST </span>{when(ev.at)}</td><td className="min-w-0 [overflow-wrap:anywhere] sm:p-3"><span className="text-muted-foreground sm:hidden">대상 </span>{ev.action?actionNodeName(ev.action.node):ev.node?.name??params.get("uid")??"클러스터"}</td><td className="min-w-0 sm:p-3"><span className="text-muted-foreground sm:hidden">동작·판단 </span>{ev.decision?judgmentLabels[ev.decision.status]??"미확인":ev.action?actionKinds[ev.action.kind]??kinds.action:kinds[row.ref.kind]??kinds.other}</td><td className="min-w-0 sm:p-3"><span className="text-muted-foreground sm:hidden">결과 </span>{ev.settings?ev.settings.error?"실패":ev.settings.appliedAt?"완료":"검증됨":ev.action?actionStatuses[ev.action.phase]??statusLabel(ev.action.phase):mark||"해당 없음"}</td><td className="sm:p-3"><Button variant="outline" onClick={()=>{opener.current=document.activeElement as HTMLElement|null;select(row)}}>근거</Button></td></tr>
      })}</tbody></table></div>:<Empty title="기록 없음"/>}
      </>}
      <HistoryPaging history={history}/>
    </>}</QueryGate>
    <Sheet open={requested} onOpenChange={open=>{if(!open)close()}}>
      <SheetContent className="w-full overflow-y-auto p-6 sm:max-w-3xl" onCloseAutoFocus={event=>{
        event.preventDefault();if(opener.current?.isConnected)opener.current.focus();else{const heading=document.querySelector<HTMLElement>(".screen-head h1");if(heading){heading.tabIndex=-1;heading.focus()}}
      }}>
        <SheetHeader><SheetTitle>선택 기록 근거</SheetTitle></SheetHeader>
        {!selected?(params.get("view")==="current"&&params.has("id")&&!params.has("offset")&&!params.has("at")?<CurrentTransitionEvidence/>:<p role="alert">기록 식별자 미확인</p>):
          <QueryGate query={record}>{record.isSuccess&&(matches&&event?<>
            <RecordDetails row={{event,ref:selected}} names={names}/>
            {event.decision&&!settingsOnly&&<div className="flex flex-wrap gap-2">
              <Button variant="outline" disabled={same(a,{event,ref:selected})} onClick={()=>select({event,ref:selected},"a")}>{same(a,{event,ref:selected})?"A로 골랐어요":"A로 고르기"}</Button>
              <Button variant="outline" disabled={same(b,{event,ref:selected})} onClick={()=>select({event,ref:selected},"b")}>{same(b,{event,ref:selected})?"B로 골랐어요":"B로 고르기"}</Button>
            </div>}
          </>:<p role="alert">기록을 확인하지 못했어요. 보관 범위나 기록이 바뀌었어요. 같은 시각의 다른 기록으로 대신하지 않아요.</p>)}</QueryGate>
        }
        <Button variant="outline" onClick={close}>목록으로 돌아가기</Button>
        {returnTo&&<Button variant="outline" asChild><Link href={returnTo}>실험 구간으로 돌아가기</Link></Button>}
      </SheetContent>
    </Sheet>
  </Screen>
}
