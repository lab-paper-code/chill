"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { Line, LineChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { useChillQuery } from "@/lib/client"
import type { ObservationResult, SchedulerCandidate, SchedulerObservation, SchedulerObservationPage, SchedulerObservationSummaryPage, SchedulerView } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { gapNotice } from "@/lib/date-time"
import { readSchedulerRecord, schedulerRecordMatches, schedulerRecordParams, schedulerRecordPath, schedulerRecordRef, schedulerPageParams, powerNumber, powerPolicyFacts, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { QueryGate, SourceExport, SourceJSON, StatusBadge } from "@/components/read-region"
import { Screen, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import { HistoryControls, HistoryPaging, useSchedulerHistory, when } from "./scheduler-history"
import { SnapshotDetails, CandidateDetails, judgmentLabels, actionKinds, actionNodeName, PowerInterval } from "./scheduler"
import { SettingsSummary } from "./scheduler-settings"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { RecordsNavigation, RecordsRange } from "@/components/records-navigation"

export function SchedulerIntervalEvents({from,to}:{from:string;to:string}) {
  const params=new URLSearchParams({from,to,view:"summary",filter:"decision-action",order:"asc",limit:"10"})
  const query=useChillQuery<SchedulerObservationSummaryPage>(`v1/observations/scheduler?${params}`,{decoder:parseSchedulerJSON})
  const historyParams=new URLSearchParams({from,to,filter:"decision-action",order:"asc"})
  return <section className="space-y-3" aria-label="구간 판단·전환">
    <QueryGate query={query}>{query.data&&<>
      {query.data.gapUntil&&<p role="status">{gapNotice(query.data.gapUntil)}</p>}
      {query.data.events?.length?<div className="overflow-x-auto"><table className="w-full text-sm">
        <caption className="sr-only">구간 판단·전환 기록</caption>
        <thead><tr>{["시각 (KST)","종류","대상"].map(heading=><th key={heading} scope="col" className="p-3 text-left">{heading}</th>)}</tr></thead>
        <tbody>{query.data.events.slice(0,10).map((event,index)=>{
          const ref=schedulerRecordRef(event,from,to,String(index))
          const href=`/records?${schedulerRecordParams(ref)}`
          return <tr key={ref.offset} className="border-t"><td className="whitespace-nowrap p-3"><Link className="underline underline-offset-4" href={href}>{when(event.at)}</Link></td><td className="p-3">{event.action?actionKinds[event.action.kind]??"변경 동작":`판단 · ${judgmentLabels[event.decision?.status??""]??"미확인"}`}</td><td className="break-all p-3">{event.action?actionNodeName(event.action.node):event.node?actionNodeName(event.node):"실행 그룹"}</td></tr>
        })}</tbody>
      </table></div>:<p role="status">판단·전환 기록 없음</p>}
      <Link className="inline-block underline underline-offset-4" href={`/records?${historyParams}`}>{query.data.nextCursor?"구간 기록 더 보기":"구간 기록 열기"}</Link>
    </>}</QueryGate>
  </section>
}

function RecordReturn({record}:{record:SchedulerRecordRef|null}) {
  return <p role="alert">선택 기록 미확인 <Link href={`/records?${record?new URLSearchParams({from:record.from,to:record.to}):""}`}>같은 기간 기록</Link></p>
}
function PowerDetails({event,record}:{event:SchedulerObservation;record:SchedulerRecordRef}) {
  const power=event.power
  if(!power)return <RecordReturn record={record}/>
  return <div className="min-w-0 space-y-4">
    <Facts items={[
      ["노드",event.node?.name??"노드 이름 없음"],["노드 식별자",event.node?.uid??"미제공"],["계측기",event.meter||"미제공"],["계측 범위",event.powerScope||"미제공"],
      ["출처 종류",power.source.kind],["전력 지표",power.source.metric],["출처 노드",power.source.nodeName??"미제공"],
      ["사건 시각 KST",when(event.at)],["측정 시작 KST",when(power.startedAt)],["측정 종료 KST",when(power.endedAt)],
      ["실측 에너지 (J)",powerNumber(event.powerJoules)],["저장 표본 (개)",powerNumber(power.samples?.length??0)],["저장 실패 (개)",powerNumber(power.failures?.length??0)],
      ["수집 시도 (회)",powerNumber(power.summary.attempts)],["성공 표본 (개)",powerNumber(power.summary.successfulSamples)],["수집 실패 (개)",powerNumber(power.summary.failures)],
      ["관측 기간 (초)",powerNumber(power.summary.observationDurationSeconds)],["최대 표본 간격 (초)",powerNumber(power.summary.maximumSampleGapSeconds)],
      ["평균 요청 지연 (초)",powerNumber(power.summary.meanRequestLatencySeconds)],["요청 지연 p95 (초)",powerNumber(power.summary.p95RequestLatencySeconds)],
      ["측정기 시각 제공",power.summary.sourceTimestampAbsent?"없음":"있음"],["표본 시각 기준","수집기 수신"],
      ...powerPolicyFacts(event),
    ]}/>
    {event.powerError&&<p role="alert">에너지 관측 오류: {event.powerError}</p>}
    <SourceJSON inline title="계측 출처와 조건" value={power.source}/>
    {power.samples?.length?<ChartContainer config={{watts:{label:"전력 (W)",color:"var(--chart-2)"}}} className="aspect-auto h-[250px] w-full"><LineChart data={power.samples.map(({observedAt,watts})=>({observedAt:when(observedAt),watts:Number(watts)}))}><CartesianGrid vertical={false}/><XAxis dataKey="observedAt"/><YAxis unit="W"/><ChartTooltip content={<ChartTooltipContent/>}/><Line dataKey="watts" stroke="var(--color-watts)" dot={false} isAnimationActive={false}/></LineChart></ChartContainer>:<Facts items={[["전력 표본","없음"]]}/>}
    <details><summary>개별 표본과 수집 실패</summary>
      {power.samples?.map((sample,index)=><Facts key={`s${index}`} items={[["수신 시각 KST",when(sample.observedAt)],["전력 (W)",powerNumber(sample.watts)],["요청 지연 (초)",powerNumber(sample.requestLatencySeconds)]]}/>)}
      {power.failures?.map((failure,index)=><Facts key={`f${index}`} items={[["실패 시각 KST",when(failure.observedAt)],["수집 상태","실패"],["요청 지연 (초)",powerNumber(failure.requestLatencySeconds)]]}/>)}
    </details>
    <SourceExport filename="scheduler-power-selected.json" value={{scope:"선택 기록",source:"v1/observations/scheduler",...record,event}}/>
    <SourceJSON inline value={event}/>
  </div>
}
function SelectedPower({record}:{record:SchedulerRecordRef|null}) {
  const query=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(record),{enabled:!!record,decoder:parseSchedulerJSON})
  const event=query.data?.events?.[0]
  if(!record)return <RecordReturn record={record}/>
  return <QueryGate query={query}>{query.data&&(schedulerRecordMatches(record,event)&&record.kind==="power"&&event?<PowerDetails event={event} record={record}/>:<RecordReturn record={record}/>)}</QueryGate>
}
export function SchedulerPowerPage() {
  const history=useSchedulerHistory(),router=useRouter(),params=useSearchParams()
  const opener=React.useRef<HTMLElement|null>(null),[nodeOpen,setNodeOpen]=React.useState(false)
  const node=params.get("filterNode")??"",powerRows=history.rows.filter(row=>!!row.event.power),rows=powerRows.filter(row=>!node||row.event.node?.uid===node)
  const nodes=new Map(powerRows.map(row=>[row.event.node?.uid??"",row.event.node?.name??"노드 이름 없음"]))
  const nodeChoices:[string,string][]=[["","전체 노드"],...(node&&!nodes.has(node)?[[node,"이전에 고른 노드"] as [string,string]]:[]),...Array.from(nodes).filter(([uid])=>uid)]
  return <Screen width="data" title="기록">
    <RecordsNavigation/>
    <RecordsRange target={node?nodes.get(node)??node:"전체 노드"} from={history.submitted.from} to={history.submitted.to} at={history.query.data?.to}/>
    <HistoryControls history={history}>
      <RowDialog open={nodeOpen} onOpenChange={setNodeOpen} label="노드" value={node?nodes.get(node)??"이전에 고른 노드":"전체 노드"} title="노드 고르기" description="불러온 기록에만 적용돼요. 실행 그룹 전체 집계는 바뀌지 않아요.">
        <fieldset className="grid gap-2">{nodeChoices.map(([uid,name])=><label key={uid} className="flex items-center gap-2"><input type="radio" name="node" checked={node===uid} onChange={()=>{const next=new URLSearchParams(params.toString());if(uid)next.set("filterNode",uid);else next.delete("filterNode");router.replace(`/records?${next}`);setNodeOpen(false)}}/>{name}{uid&&<span className="break-all text-xs text-muted-foreground">{uid}</span>}</label>)}</fieldset>
      </RowDialog>
      <RowDialog label="실행 그룹 전체 전력" hint="고른 기간의 전체·활성 실행 그룹 소비" title="실행 그룹 전체 전력" description="고른 기간 전체의 실측 소비예요. 노드 고르기와 페이지는 여기에 적용되지 않아요."><PowerInterval from={history.submitted.from} to={history.submitted.to}/></RowDialog>
      <Row href={`/records?${new URLSearchParams(history.submitted)}`} label="같은 기간 판단 기록"/>
      {history.query.data&&<RowDialog label="자료 내보내기" title="자료 내보내기" description="지금 불러온 전력 기록을 파일로 받아요.">
        <SourceExport filename="scheduler-power-page-summary.json" value={{projection:"summary",scope:"현재 페이지",source:"v1/observations/scheduler",from:history.submitted.from,to:history.submitted.to,cursor:history.cursor,filterNode:node,rows}}/>
      </RowDialog>}
    </HistoryControls>
    <QueryGate query={history.query}>{history.query.data&&<>
      {rows.length?<div className="rounded-lg border"><table className="block w-full text-sm sm:table"><thead className="hidden sm:table-header-group"><tr>{["시각 KST","노드","실측 에너지 (J)","근거"].map(label=><th key={label} className="p-3 text-left">{label}</th>)}</tr></thead><tbody className="block sm:table-row-group">{rows.map(row=><tr key={row.ref.offset} className="grid gap-1 border-t p-3 first:border-t-0 sm:table-row sm:p-0"><td className="min-w-0 sm:p-3"><span className="text-muted-foreground sm:hidden">시각 KST </span>{when(row.event.at)}</td><td className="min-w-0 [overflow-wrap:anywhere] sm:p-3"><span className="text-muted-foreground sm:hidden">노드 </span>{row.event.node?.name??"미기록"}</td><td className="tabular-nums sm:p-3 sm:text-right"><span className="text-muted-foreground sm:hidden">실측 에너지 (J) </span>{powerNumber(row.event.powerJoules)}</td><td className="sm:p-3"><Button variant="outline" onClick={()=>{
        opener.current=document.activeElement as HTMLElement|null
        router.push(`/records?${new URLSearchParams({...Object.fromEntries(params),...Object.fromEntries(schedulerRecordParams(row.ref))})}`,{scroll:false})
      }}>근거</Button></td></tr>)}</tbody></table></div>:<Empty title="기록 없음"/>}
      <HistoryPaging history={history}/>
    </>}</QueryGate>
    <Sheet open={["offset","at","id"].some(key=>params.has(key))} onOpenChange={open=>{if(!open){const next=schedulerPageParams(new URLSearchParams(params.toString()),params.get("cursor")??undefined);next.set("kind","power");router.replace(`/records?${next}`,{scroll:false})}}}>
      <SheetContent className="w-full overflow-y-auto p-6 sm:max-w-3xl" onCloseAutoFocus={event=>{
        event.preventDefault();if(opener.current?.isConnected)opener.current.focus();else{const heading=document.querySelector<HTMLElement>(".screen-head h1");if(heading){heading.tabIndex=-1;heading.focus()}}
      }}>
        <SheetHeader><SheetTitle>전력 측정 근거</SheetTitle></SheetHeader>
        <SelectedPower record={readSchedulerRecord(params)}/>
      </SheetContent>
    </Sheet>
  </Screen>
}

type ServiceIdentity={uid:string;definition:string}
function identities(event:SchedulerObservation):ServiceIdentity[] {
  const decision=event.decision
  const all=[decision?.keep,decision?.target].flatMap(candidate=>candidate?.slo??[])
  return all.filter((row,index)=>all.findIndex(item=>item.uid===row.uid&&item.definition===row.definition)===index).map(({uid,definition})=>({uid,definition}))
}
function Expected({candidate}:{candidate:SchedulerCandidate|null}) {
  if(!candidate)return <p>후보가 제공되지 않았어요.</p>
  return <><p>{candidate.id} / {judgmentLabels[candidate.status] ?? candidate.status} / 예상 에너지 {candidate.expectedJoules==null?"계산값 없음":`${candidate.expectedJoules} J`}</p>{candidate.slo?.map((slo,index)=><p key={index} className="break-all">UID {slo.uid} / 정의 {slo.definition}: 예상 {slo.expectedNanos==null?"계산값 없음":`${Number(slo.expectedNanos) / 1e6} ms`}, {slo.percentile}% 기준, 목표 {nanosToMilliseconds(slo.targetNanos)} ms, 표본 {slo.samples}개, 목표 초과 비율 {slo.normalizedExceedance}<SourceJSON inline title="제공된 예상 결과" value={slo.predictedOutcomes}/></p>)}<p>{candidate.reasons?.join(" / ")||"사유 제공 없음"}</p><p>가정: {candidate.assumptions?.join(" / ")||"제공 없음"}</p><p>출처: {candidate.sources?.join(" / ")||"제공 없음"}</p></>
}
function StoredDetails({event}:{event:SchedulerObservation}) {
  const {decision,snapshot}=event
  if(!decision)return null
  const view:SchedulerView|undefined=snapshot?{settings:decision.settings,state:{snapshot,decision,actions:snapshot.actions,results:null}}:undefined
  return <>{snapshot?<details><summary>초기 구성</summary><SnapshotDetails snapshot={snapshot}/></details>:<p>초기 구성 미기록</p>}{view&&[decision.keep,decision.target].filter((candidate):candidate is SchedulerCandidate=>!!candidate).map((candidate,index)=><details key={`${candidate.id}-${index}`}><summary>{candidate.keep?"유지안 근거":"목표 근거"}</summary><CandidateDetails candidate={candidate} view={view}/></details>)}</>
}
function ActualSummary({data}:{data:ObservationResult}) {
  return <><p>전체 구간 {data.from} ~ {data.to}</p><p>전체 {data.total}건 / 선택 조건 {data.selected}건</p><p>모집단 {data.samplePopulation} / 방법 {data.quantileMethod}</p>{Object.entries(data.outcomes).map(([outcome,count])=><p key={outcome}>{outcome}: {count}건</p>)}{data.cohorts.length?data.cohorts.map(cohort=><p key={cohort.definition} className="break-all">정의 {cohort.definition} / 전체 {cohort.total}건 / 지연 표본 {cohort.sampleCount}개 / {cohort.percentile===undefined?"백분위 미제공":`${cohort.percentile}%`} 지연 {cohort.quantileNanos==null?"값 없음":`${Number(cohort.quantileNanos)/1e6} ms`} / 목표 {cohort.targetLatencyNanos==null?"미제공":`${nanosToMilliseconds(cohort.targetLatencyNanos)} ms`}</p>):<p>제공된 집계가 없어 지연 값을 계산하지 않아요.</p>}{data.gapUntil?<p role="status">{gapNotice(data.gapUntil)}</p>:null}</>
}
function ActualPair({identity,a,b,aRef,bRef}:{identity:ServiceIdentity;a:SchedulerObservation;b:SchedulerObservation;aRef:SchedulerRecordRef;bRef:SchedulerRecordRef}) {
  const [ap,bp]=[a,b].map(event=>new URLSearchParams({uid:identity.uid,definition:identity.definition,from:event.decision!.search.windowStart,to:event.decision!.search.windowEnd,limit:"1"}))
  const valid=(params:URLSearchParams)=>!!identity.uid&&!!identity.definition&&!!params.get("from")&&!!params.get("to")&&!params.get("from")!.startsWith("0001-")&&!params.get("to")!.startsWith("0001-")&&params.get("from")!==params.get("to")
  const aq=useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(identity.uid)}?${new URLSearchParams(Array.from(ap).filter(([key])=>key!=="uid"))}`,{enabled:valid(ap),decoder:parseSchedulerJSON})
  const bq=useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(identity.uid)}?${new URLSearchParams(Array.from(bp).filter(([key])=>key!=="uid"))}`,{enabled:valid(bp),decoder:parseSchedulerJSON})
  return <section className="space-y-3"><h3 className="break-all font-semibold">서비스 {identity.uid} / 고정 정의 {identity.definition}</h3>
    {[{params:ap,query:aq,label:"A"},{params:bp,query:bq,label:"B"}].map(({params,query,label})=><div key={label} className="space-y-2"><h4 className="font-medium">{label} 입력 구간의 실제 결과</h4>{valid(params)?<><QueryGate query={query}>{query.data&&<ActualSummary data={query.data}/>}</QueryGate><Link href={`/records?kind=requests&${params}`}>같은 조건 요청 기록</Link></>:<p>입력 구간·고정 정의 미기록</p>}</div>)}
    <SourceExport filename="scheduler-comparison-actual.json" value={{scope:"선택 A/B 기록과 서비스별 실제 ChillServer 조회",source:"v1/observations/workloads/:uid",records:{a:{ref:aRef,event:a},b:{ref:bRef,event:b}},identity,conditions:{a:ap.toString(),b:bp.toString()},actual:{a:aq.data??null,b:bq.data??null}}}/>
  </section>
}
export function ComparisonEvidence() {
  const params=useSearchParams(),aRef=readSchedulerRecord(params,"a"),bRef=readSchedulerRecord(params,"b")
  const aq=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(aRef),{enabled:!!aRef,decoder:parseSchedulerJSON}),bq=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(bRef),{enabled:!!bRef,decoder:parseSchedulerJSON})
  const a=aq.data?.events?.[0],b=bq.data?.events?.[0]
  const aOK=aRef&&schedulerRecordMatches(aRef,a)&&aRef.kind==="decision"&&a?.decision,bOK=bRef&&schedulerRecordMatches(bRef,b)&&bRef.kind==="decision"&&b?.decision
  if(!aOK||!bOK){
    return <section aria-label="판단 비교">
      {aRef&&<QueryGate query={aq}>{aq.data&&!aOK&&<RecordReturn record={aRef}/>}</QueryGate>}
      {bRef&&<QueryGate query={bq}>{bq.data&&!bOK&&<RecordReturn record={bRef}/>}</QueryGate>}
      {(!aRef||!bRef)&&<p>비교 선택 미기록</p>}
    </section>
  }
  const left=a!,right=b!,ad=left.decision!,bd=right.decision!
  const scopes=(event:SchedulerObservation)=>event.snapshot?.baselines?.map(item=>({node:item.node.uid,meter:item.meter,scope:item.scope}))??null
  const aIDs=identities(left),bIDs=identities(right),aScopes=scopes(left),bScopes=scopes(right)
  const conditions=[
    String(ad.capacity?.periodNanos)!==String(bd.capacity?.periodNanos)?"에너지 비교 기간이 달라요.":null,
    !aIDs.length||!bIDs.length?"한쪽 또는 양쪽에 서비스 정보가 없어요.":aIDs.length!==bIDs.length||aIDs.some(item=>!bIDs.some(other=>item.uid===other.uid&&item.definition===other.definition))?"서비스나 고정 정의가 달라요.":null,
    !aScopes?.length||!bScopes?.length?"한쪽 또는 양쪽에 계측 범위 정보가 없어요.":aScopes.length!==bScopes.length||aScopes.some(item=>!bScopes.some(other=>item.node===other.node&&item.meter===other.meter&&item.scope===other.scope))?"계측 범위가 달라요.":null,
    (!left.snapshot||!right.snapshot)?"짝지어진 초기 구성을 모두 확인할 수 없어요.":null,
    ad.search.population!==bd.search.population?"입력 모집단이 달라요.":null,
  ].filter((message):message is string=>!!message)
  // ponytail: scans two selected node lists; index UIDs if large snapshots make comparison slow.
  const differentNodes=(a:SchedulerCandidate["nodes"]|undefined,b:SchedulerCandidate["nodes"]|undefined)=>(a?.length??0)!==(b?.length??0)||a?.some(node=>!b?.some(other=>node.node.uid===other.node.uid))
  if(differentNodes(left.snapshot?.nodes,right.snapshot?.nodes)||differentNodes(ad.target?.nodes,bd.target?.nodes))conditions.push("초기 또는 목표 노드가 달라요.")
  const services=[...aIDs,...bIDs].filter((item,index,all)=>all.findIndex(row=>row.uid===item.uid&&row.definition===item.definition)===index)
  const columns=[{event:left,ref:aRef!},{event:right,ref:bRef!}]
  // The one comparison table: plain values only.
  const summary:[string,(event:SchedulerObservation)=>React.ReactNode][]=[
    ["판단",event=>judgmentLabels[event.decision!.status]??"확인이 필요해요"],
    ["판단 시각",event=>when(event.decision!.at)],
    ["입력 관측 구간",event=>`${when(event.decision!.search.windowStart)} – ${when(event.decision!.search.windowEnd)}`],
    ["에너지 비교 기간",event=>event.decision!.capacity?`${nanosToMilliseconds(event.decision!.capacity.periodNanos)} ms`:"기록 없음"],
    ["받은 요청 / 접수",event=>`${event.decision!.search.received} / ${event.decision!.search.accepted}`],
    ["유지했을 때 예상 에너지",event=>event.decision!.keep.expectedJoules==null?"계산값 없음":`${event.decision!.keep.expectedJoules} J`],
    ["바꿨을 때 예상 에너지",event=>!event.decision!.target?"목표 없음":event.decision!.target.expectedJoules==null?"계산값 없음":`${event.decision!.target.expectedJoules} J`],
    ["실행 기록",event=>event.action?"있어요":"없어요"],
  ]
  // Dependent evidence, one dialog per group, A then B.
  const evidence:[group:string,items:[string,(event:SchedulerObservation,ref:SchedulerRecordRef)=>React.ReactNode][]][]=[
    ["입력과 조건",[
      ["기록 출처와 조회 범위",(event,ref)=><p className="break-all">v1/observations/scheduler / {ref.from} ~ {ref.to} / offset {ref.offset} / 사건 {event.at}</p>],
      ["입력 관측 구간",event=><p>{event.decision!.search.windowStart} ~ {event.decision!.search.windowEnd}</p>],
      ["모집단과 부하",event=><><p>{event.decision!.search.population}</p><p>수신 {event.decision!.search.received} / 접수 {event.decision!.search.accepted} / 계산 입력 {event.decision!.search.computeInputs}</p><SourceJSON inline title="서비스별 제공 부하" value={event.decision!.search.loads}/></>],
      ["에너지 비교 기간",event=><p>판단 {event.decision!.at} 기준 {event.decision!.capacity?`${nanosToMilliseconds(event.decision!.capacity.periodNanos)} ms`:"기록 없음"}. 입력 관측 구간과 별개예요.</p>],
      ["서비스와 고정 정의",event=><>{identities(event).map(item=><p className="break-all" key={`${item.uid}/${item.definition}`}>{item.uid} / {item.definition}</p>)}{!identities(event).length&&<p>예상 응답 목표에 서비스 정의가 없어요.</p>}<SourceJSON inline title="초기 구성의 서비스 정의" value={event.snapshot?.workloads??null}/></>],
    ]],
    ["운영 설정",[["운영 설정",event=><SettingsSummary settings={event.decision!.settings}/>]]],
    ["예상 결과와 후보 근거",[
      ["초기 구성과 후보 근거",event=><StoredDetails event={event}/>],
      ["유지안 예상 응답과 에너지",event=><Expected candidate={event.decision!.keep}/>],
      ["목표 예상 응답과 에너지",event=><Expected candidate={event.decision!.target}/>],
    ]],
    ["판단 사유와 변경 계획",[
      ["판단 사유와 참조",event=><><p>{judgmentLabels[event.decision!.status] ?? event.decision!.status}: {event.decision!.reasons?.join(" / ")||"사유 제공 없음"}</p><p>참조: {event.decision!.references?.join(" / ")||"제공 없음"}</p><SourceJSON inline title="제공된 탐색 근거" value={event.decision!.search}/></>],
      ["변경 계획과 실행 관측",event=><><SourceJSON inline title="유지안 동작" value={event.decision!.keep.actions}/><SourceJSON inline title="목표 동작" value={event.decision!.target?.actions??null}/><SourceJSON inline title="짝지어진 구성의 진행 동작" value={event.snapshot?.actions??null}/>{event.action?<SourceJSON inline title="같은 기록의 실행 관측" value={event.action}/>:<p>이 판단 기록에는 실행 결과가 없어요. 다른 사건의 동작 ID만으로 결과를 잇지 않아요.</p>}</>],
    ]],
    ["관련 전력 측정",[["관련 전력 측정",event=><><Link href={`/records?kind=power&${new URLSearchParams({from:event.decision!.search.windowStart,to:event.decision!.search.windowEnd})}`}>입력 구간 전력 기록</Link><SourceJSON inline title="제공된 계측기와 범위" value={scopes(event)}/></>]]],
  ]
  return <section aria-label="판단 비교" className="space-y-4">
    <StatusBadge tone={conditions.length?"warn":"ok"}>{conditions.length?`조건 불일치 ${conditions.length}개`:"동일 조건"}</StatusBadge>
    <div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th className="p-3 text-left">항목</th><th className="p-3 text-left">A</th><th className="p-3 text-left">B</th></tr></thead><tbody>{summary.map(([label,render])=><tr key={label} className="border-t"><th className="p-3 text-left align-top font-medium text-muted-foreground">{label}</th>{columns.map(({event},index)=><td key={index} className="p-3 align-top">{render(event)}</td>)}</tr>)}</tbody></table></div>
    <details><summary>비교 조건</summary>{conditions.length?<ul>{conditions.map(message=><li key={message}>{message}</li>)}</ul>:<p>제공 조건 일치 · 인과 관계 미확인</p>}</details>
    {evidence.map(([group,items])=><details key={group}><summary>{group}</summary>{items.map(([label,render])=><section key={label} className="space-y-3"><h3 className="font-semibold">{label}</h3>{columns.map(({event,ref},index)=><div key={index} className="min-w-0 space-y-2 border-l-2 pl-3"><h4>{index?"B":"A"}</h4>{render(event,ref)}</div>)}</section>)}</details>)}
    <details><summary>실제 요청 결과</summary>{services.length?services.map(identity=><ActualPair key={`${identity.uid}/${identity.definition}`} identity={identity} a={left} b={right} aRef={aRef!} bRef={bRef!}/>):<p>고정 서비스 정의 미기록</p>}</details>
    <SourceExport filename="scheduler-comparison-selected.json" value={{scope:"선택 A/B 기록과 제공 조건",source:"v1/observations/scheduler",a:{ref:aRef,event:left},b:{ref:bRef,event:right},conditions}}/><SourceJSON inline title="선택 A/B 기록 원본" value={{a:left,b:right}}/>
  </section>
}
