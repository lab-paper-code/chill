"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams, type ReadonlyURLSearchParams } from "next/navigation"
import type { QueryObserverBaseResult } from "@tanstack/react-query"
import { useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { SchedulerObservation, SchedulerObservationPage } from "@/lib/read-types"
import { parseSchedulerJSON, nanosToMilliseconds } from "@/lib/scheduler"
import { schedulerRecordRef, schedulerRecordParams, readSchedulerRecord, schedulerRecordPath, schedulerRecordMatches, schedulerPageParams, schedulerHistoryKindMatches, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { gapNotice, localDateTime, utcDateTime } from "@/lib/date-time"
import { SnapshotDetails, CandidateDetails, judgmentLabels, actionKinds, actionStatuses, actionNodeName } from "@/features/scheduler"
import { SettingChanges } from "@/features/scheduler-settings"
import { QueryGate, SourceJSON, SourceExport, statusLabel } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

export type SchedulerHistoryRow={event:SchedulerObservation;ref:SchedulerRecordRef}
export type SchedulerHistoryQuery={
  query:QueryObserverBaseResult<SchedulerObservationPage,ApiError>;rows:SchedulerHistoryRow[];params:ReadonlyURLSearchParams
  submitted:{from:string;to:string};cursor:string;draft:{from:string;to:string};setDraft:React.Dispatch<React.SetStateAction<{from:string;to:string}>>
  limit:number;setLimit:React.Dispatch<React.SetStateAction<number>>;validation:string;maxPageSize:number;apply:()=>boolean;page:(cursor?:string)=>void
}
const kinds:Record<string,string>={decision:"운영 판단",snapshot:"실제 구성",settings:"운영 설정 변경",action:"변경 동작",power:"전력 측정",other:"기타 기록"}
const historyFilters:Record<string,string>={all:"모든 종류","decision-action":"판단·전환 기록",...kinds}
// Shared by the record screens: readable local time, and a person instead of a service account path.
export const when=(value?:string)=>!value||value.startsWith("0001-")||!Number.isFinite(Date.parse(value))?"정보 없음":new Date(value).toLocaleString("ko-KR",{month:"numeric",day:"numeric",hour:"2-digit",minute:"2-digit",second:"2-digit"})
export const actorName=(name?:string)=>name?.startsWith("system:serviceaccount:")?name.split(":").at(-1)!:name??"기록 없음"

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
    if(!Number.isFinite(from)||!Number.isFinite(to)||from>to||draft.from===draft.to||to-from>config.maxWindowHours*3600000||!Number.isInteger(limit)||limit<1||limit>config.maxPageSize){setValidation(`기간은 최대 ${config.maxWindowHours}시간, 한 번에 1~${config.maxPageSize}건까지 볼 수 있어요.`);return false}
    const next=schedulerPageParams(new URLSearchParams(params.toString()));next.set("from",draft.from);next.set("to",draft.to);next.set("limit",String(limit));setValidation("");router.replace(`${pathname}?${next}`)
    return true
  }
  function page(nextCursor?:string){router.replace(`${pathname}?${schedulerPageParams(new URLSearchParams(params.toString()),nextCursor)}`)}
  return {query,rows,params,submitted,cursor,draft,setDraft,limit,setLimit,validation,maxPageSize:config.maxPageSize,apply,page}
}

// The window filter as one row; extra filter rows of the same screen go in `children`.
export function HistoryControls({history,children}:{history:SchedulerHistoryQuery;children?:React.ReactNode}){
  const [open,setOpen]=React.useState(false)
  return <Rows label="조회 조건">
    <RowDialog open={open} onOpenChange={next=>{setOpen(next);if(next)history.setDraft(history.submitted)}} label="기간" value={`${when(history.submitted.from)} – ${when(history.submitted.to)}`} title="조회 기간" description="시작 시각부터 끝 시각 직전까지의 기록을 보여드려요.">
      <form noValidate className="grid gap-4" onSubmit={event=>{event.preventDefault();if(history.apply())setOpen(false)}}>
        <label className="grid gap-1.5">시작<Input type="datetime-local" value={localDateTime(history.draft.from)} onChange={event=>{const from=utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"");history.setDraft(current=>({...current,from}))}}/></label>
        <label className="grid gap-1.5">종료<Input type="datetime-local" value={localDateTime(history.draft.to)} onChange={event=>{const to=utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"");history.setDraft(current=>({...current,to}))}}/></label>
        <label className="grid gap-1.5">한 번에 볼 기록 수<Input type="number" min={1} max={history.maxPageSize} value={Number.isFinite(history.limit)?history.limit:""} onChange={event=>history.setLimit(Number(event.target.value))}/></label>
        {history.validation&&<p role="alert">{history.validation}</p>}
        <Button type="submit">이 기간으로 보기</Button>
      </form>
    </RowDialog>
    {children}
  </Rows>
}
export function HistoryPaging({history}:{history:SchedulerHistoryQuery}){
  return <div className="grid gap-2">{history.query.data?.gapUntil&&<p role="status">{gapNotice(history.query.data.gapUntil)}</p>}<p className="text-sm text-muted-foreground">기록을 {history.rows.length}건 불러왔어요. 기간 전체 건수는 아니에요.</p><div className="flex gap-2"><Button variant="outline" onClick={()=>history.page()} disabled={history.cursor==="0"}>처음으로</Button><Button variant="outline" onClick={()=>history.page(history.query.data?.nextCursor)} disabled={!history.query.data?.nextCursor}>다음 기록 보기</Button></div></div>
}

function RecordDetails({row}:{row:SchedulerHistoryRow}){
  const event=row.event,decision=event.decision,snapshot=event.snapshot
  const view=decision&&snapshot?{settings:decision.settings,state:{decision,snapshot,actions:snapshot.actions,results:null}}:null
  return <div className="space-y-4 [&_dd]:break-all">
    <Facts items={[["종류",kinds[row.ref.kind]??row.ref.kind],["사건 시각",event.at],["보관 순서",row.ref.offset]]}/>
    {decision&&<>
      <h3 className="font-semibold">판단: {judgmentLabels[decision.status]??statusLabel(decision.status)}</h3>
      <Facts items={[["판단 시각",decision.at],["자동 제어 서비스",`${decision.settings.autonomousWorkloads?.length??0}개`],["입력 관측 구간",`${decision.search.windowStart} ~ ${decision.search.windowEnd}`],["모집단",decision.search.population],["에너지 비교 기간",decision.capacity?`${nanosToMilliseconds(decision.capacity.periodNanos)} ms`:"기록 없음"]]}/>
      <p>{decision.reasons?.join(", ")||"제공된 판단 사유가 없어요."}</p>
      {view?<><details><summary>유지안 근거</summary><CandidateDetails candidate={decision.keep} view={view}/></details>{decision.target&&<details><summary>선택 목표 근거</summary><CandidateDetails candidate={decision.target} view={view}/></details>}</>:<p>같은 기록에 실제 구성이 없어요. 후보 원본은 아래 원본에서 확인해요.</p>}
    </>}
    {snapshot&&<details><summary>기록의 실제 구성</summary><SnapshotDetails snapshot={snapshot}/></details>}
    {event.settings&&<>
      <h3 className="font-semibold">설정 변경</h3>
      <Facts items={[["담당자",`${event.settings.actorUsername} / ${event.settings.actorUID}`],["검증",event.settings.validatedAt],["실행 환경 적용",event.settings.appliedAt??"제공 없음"],["마지막 관측 노드",event.settings.affected?.map(node=>`${node.name} (${node.uid})`).join(", ")||"없음"]]}/>
      {event.settings.error&&<p role="alert">{event.settings.error}</p>}
      <SettingChanges before={event.settings.before} after={event.settings.after} stored/>
      <p className="text-sm text-muted-foreground">설정 저장과 실제 물리 전환은 따로 일어나요.</p>
    </>}
    {event.action&&<>
      <Facts items={[["동작",actionKinds[event.action.kind]??event.action.kind],["노드",actionNodeName(event.action.node)],["단계",actionStatuses[event.action.phase]??event.action.phase],["시작",event.action.startedAt],["완료",event.action.completedAt??"제공 없음"]]}/>
      <Link href={`/transitions/${encodeURIComponent(event.action.id)}?${schedulerRecordParams(row.ref)}`}>이 변경 동작 자세히 보기</Link>
    </>}
    {(event.power||event.powerError||event.powerJoules!==undefined)&&<Link href={`/scheduler/power?${schedulerRecordParams(row.ref)}`}>이 전력 측정 자세히 보기</Link>}
    <SourceJSON inline title="선택 기록 원본" value={event}/>
    <SourceExport value={{conditions:row.ref,population:decision?.search.population??null,source:event}} filename="chill-scheduler-selected-record.json"/>
  </div>
}

export function SchedulerHistoryPage({settingsOnly=false}:{settingsOnly?:boolean}){
  const history=useSchedulerHistory(),router=useRouter(),pathname=usePathname(),params=history.params
  const filter=settingsOnly?"settings":params.get("filter")??"all",rows=history.rows.filter(row=>schedulerHistoryKindMatches(filter,row.ref.kind))
  const opener=React.useRef<HTMLElement|null>(null),[filterOpen,setFilterOpen]=React.useState(false)
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
  const same=(ref:SchedulerRecordRef|null,row:SchedulerHistoryRow)=>!!ref&&ref.offset===row.ref.offset&&ref.at===row.ref.at&&ref.from===row.ref.from&&ref.to===row.ref.to
  const loaded=!!history.query.data
  const title=settingsOnly?(!loaded?"운영 설정 변경 이력":rows.length?`운영 설정 변경 ${rows.length}건을 찾았어요`:"이 기간에는 운영 설정 변경이 없어요"):filter==="decision-action"?(!loaded?"판단·전환 기록":rows.length?`판단·전환 기록 ${rows.length}건을 불러왔어요`:"가져온 기록에 판단·전환 기록이 없어요"):(!loaded?"운영 판단 기록":rows.length?`운영 기록 ${rows.length}건을 불러왔어요`:"이 기간에는 운영 기록이 없어요")
  return <Screen back={["/records","기록"]} title={title} lede={settingsOnly?"누가 언제 운영 설정을 바꿨는지 보여드려요.":"기록을 열어 비교할 판단 두 개(A, B)를 고를 수 있어요."}
    action={!settingsOnly&&(a||b)?<><p className="text-sm text-muted-foreground">A {a?when(a.at):"아직 고르지 않았어요"} · B {b?when(b.at):"아직 고르지 않았어요"}</p>{a&&b?<Button asChild><Link href={`/scheduler/compare?${compare}`}>두 기록 비교하기</Link></Button>:<Button disabled>두 기록 비교하기</Button>}</>:undefined}>
    <HistoryControls history={history}>
      {!settingsOnly&&<RowDialog open={filterOpen} onOpenChange={setFilterOpen} label="기록 종류" value={historyFilters[filter]??"이전 조건"} title="기록 종류" description="불러온 기록 중에서 골라 보여드려요. 판단·전환 기록은 운영 판단과 변경 동작만 포함해요. 전력 측정은 제외하며, 다음 기록에도 같은 필터를 유지해요.">
        <fieldset className="grid gap-2">{Object.entries(historyFilters).map(([key,label])=><label key={key} className="flex min-h-11 items-center gap-2"><input type="radio" name="kind" checked={filter===key} onChange={()=>{const next=new URLSearchParams(params.toString());next.set("filter",key);router.replace(`${pathname}?${next}`);setFilterOpen(false)}}/>{label}</label>)}</fieldset>
      </RowDialog>}
      {history.query.data&&<RowDialog label="자료 내보내기" title="자료 내보내기" description="지금 불러온 기록을 파일로 받아요. 기간 전체 집계는 아니에요.">
        <SourceExport value={{conditions:{...history.submitted,cursor:history.cursor,limit:params.get("limit")??history.limit,filter},source:history.query.data}} filename="chill-scheduler-current-page.json"/>
      </RowDialog>}
    </HistoryControls>
    <QueryGate query={history.query}>{loaded&&<>
      {filter==="decision-action"&&<p className="text-sm text-muted-foreground">지금 가져온 기록 중 운영 판단과 변경 동작만 표시해요. 전력 측정은 제외해요. 이 페이지에 없으면 다음 기록에도 확인해 주세요. 기간 전체 건수는 아니에요.</p>}
      {rows.length?<Rows label="기록">{rows.map(row=>{
        const ev=row.event,mark=[same(a,row)&&"A",same(b,row)&&"B"].filter(Boolean).join(" · ")
        return <Row key={row.ref.offset} label={ev.decision?judgmentLabels[ev.decision.status]??statusLabel(ev.decision.status):ev.action?actionKinds[ev.action.kind]??kinds.action:kinds[row.ref.kind]??kinds.other}
          hint={`${when(ev.at)}${ev.decision?` · ${ev.decision.status}`:ev.settings?` · ${actorName(ev.settings.actorUsername)}`:ev.action?` · ${actionNodeName(ev.action.node)}`:ev.node?` · ${ev.node.name}`:""}`}
          value={mark||(ev.settings?ev.settings.error?"실패":ev.settings.appliedAt?"적용됨":"검증됨":ev.action?actionStatuses[ev.action.phase]??statusLabel(ev.action.phase):undefined)}
          onClick={()=>{opener.current=document.activeElement as HTMLElement|null;select(row)}}/>
      })}</Rows>:<Empty title={settingsOnly?"이 기간에는 운영 설정 변경이 없어요":filter==="decision-action"?"가져온 기록에 판단·전환 기록이 없어요":"보여줄 기록이 없어요"}>{history.query.data?.nextCursor&&<p>다음 기록에 더 있을 수 있어요.</p>}</Empty>}
      <HistoryPaging history={history}/>
    </>}</QueryGate>
    <Dialog open={requested} onOpenChange={open=>{if(!open)close()}}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-3xl" onCloseAutoFocus={event=>{
        event.preventDefault();if(opener.current?.isConnected)opener.current.focus();else{const heading=document.querySelector<HTMLElement>(".screen-head h1");if(heading){heading.tabIndex=-1;heading.focus()}}
      }}>
        <DialogHeader><DialogTitle>기록 자세히</DialogTitle><DialogDescription>고른 기록을 원래 기간과 순서 그대로 확인해요.</DialogDescription></DialogHeader>
        {!selected?<p role="alert">기록을 확인하지 못했어요. 기간과 순서, 종류와 시각이 모두 필요해요.</p>:
          <QueryGate query={record}>{record.isSuccess&&(matches&&event?<>
            <RecordDetails row={{event,ref:selected}}/>
            {event.decision&&!settingsOnly&&<div className="flex flex-wrap gap-2">
              <Button variant="outline" disabled={same(a,{event,ref:selected})} onClick={()=>select({event,ref:selected},"a")}>{same(a,{event,ref:selected})?"A로 골랐어요":"A로 고르기"}</Button>
              <Button variant="outline" disabled={same(b,{event,ref:selected})} onClick={()=>select({event,ref:selected},"b")}>{same(b,{event,ref:selected})?"B로 골랐어요":"B로 고르기"}</Button>
            </div>}
          </>:<p role="alert">기록을 확인하지 못했어요. 보관 범위나 기록이 바뀌었어요. 같은 시각의 다른 기록으로 대신하지 않아요.</p>)}</QueryGate>
        }
        <Button variant="outline" onClick={close}>목록으로 돌아가기</Button>
      </DialogContent>
    </Dialog>
  </Screen>
}
