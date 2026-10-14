"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { Line, LineChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { useChillQuery } from "@/lib/client"
import type { ObservationResult, SchedulerCandidate, SchedulerObservation, SchedulerObservationPage, SchedulerView } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { readSchedulerRecord, schedulerRecordMatches, schedulerRecordParams, schedulerRecordPath, schedulerPageParams, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { DataTable } from "@/components/data-table"
import { DetailsDialog, ReadRegion, SourceExport, SourceJSON } from "@/components/read-region"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import { HistoryControls, HistoryPaging, useSchedulerHistory } from "./scheduler-history"
import { SnapshotDetails, CandidateDetails, judgmentLabels, PowerInterval } from "./scheduler"
import { SettingsSummary } from "./scheduler-settings"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"


function RecordReturn({record}:{record:SchedulerRecordRef|null}) {
  return <p role="alert">선택한 기록 확인 필요. <Link href={`/scheduler/history?${record?new URLSearchParams({from:record.from,to:record.to}):""}`}>해당 구간 기록으로 돌아가기</Link></p>
}
function PowerDetails({event,record}:{event:SchedulerObservation;record:SchedulerRecordRef}) {
  const power=event.power
  if(!power)return <RecordReturn record={record}/>
  return <div className="space-y-4"><p>{event.node?.name??"노드 이름 없음"} / 노드 UID {event.node?.uid??"제공 없음"}</p><p>계측기 {event.meter||"제공 없음"} / 계측 범위 {event.powerScope||"제공 없음"}</p><p>사건 {event.at} / 측정 {power.startedAt} ~ {power.endedAt}</p><p>출처 {power.source.kind} / 지표 {power.source.metric} / 노드 {power.source.nodeName??"제공 없음"}</p><SourceJSON inline title="계측 출처와 조건" value={power.source}/><p>에너지 {event.powerJoules==null?"측정값 없음":`${event.powerJoules} J`}</p>{event.powerError&&<p role="alert">에너지 관측 오류: {event.powerError}</p>}<p>표본 {power.samples?.length??0}개 / 실패 {power.failures?.length??0}개 / 시도 {power.summary.attempts}회</p><p>생산자 요약: 성공 {power.summary.successfulSamples}개, 실패 {power.summary.failures}개, 관측 기간 {power.summary.observationDurationSeconds}초, 최대 표본 간격 {power.summary.maximumSampleGapSeconds}초</p><p>표본 observedAt은 collector 수신 시각입니다. sourceTimestampAbsent: {String(power.summary.sourceTimestampAbsent)}</p><dl>{Object.entries(event.powerSampling??{}).map(([key,value])=><React.Fragment key={key}><dt>sampling {key}</dt><dd>{String(value)}{key==="MaxEvents"?"개":" ns"}</dd></React.Fragment>)}{Object.entries(event.powerCoverage??{}).map(([key,value])=><React.Fragment key={key}><dt>coverage {key}</dt><dd>{String(value)}{key==="MinimumCoverage"?"":" ns"}</dd></React.Fragment>)}</dl>{!event.powerSampling&&<p>표본 수집 정책 제공 없음</p>}{!event.powerCoverage&&<p>에너지 coverage 정책 제공 없음</p>}
    {power.samples?.length?<ChartContainer config={{watts:{label:"전력 (W)",color:"var(--chart-2)"}}} className="aspect-auto h-[250px] w-full"><LineChart data={power.samples.map(({observedAt,watts})=>({observedAt,watts:Number(watts)}))}><CartesianGrid vertical={false}/><XAxis dataKey="observedAt"/><YAxis unit="W"/><ChartTooltip content={<ChartTooltipContent/>}/><Line dataKey="watts" stroke="var(--color-watts)" dot={false} isAnimationActive={false}/></LineChart></ChartContainer>:<p>전력 표본 0개. 에너지 제공 상태와 구분합니다.</p>}
    <details><summary>개별 표본과 수집 실패</summary>{power.samples?.map((sample,index)=><p key={`s${index}`}>{sample.observedAt} / {sample.watts} W / 요청 지연 {sample.requestLatencySeconds}초</p>)}{power.failures?.map((failure,index)=><p key={`f${index}`}>{failure.observedAt} / {failure.reason}: {failure.message} / 요청 지연 {failure.requestLatencySeconds}초</p>)}</details><SourceExport filename="scheduler-power-selected.json" value={{scope:"선택 기록",source:"v1/observations/scheduler",...record,event}}/><SourceJSON inline value={event}/></div>
}
function SelectedPower({record}:{record:SchedulerRecordRef|null}) {
  const query=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(record),{enabled:!!record,decoder:parseSchedulerJSON})
  const event=query.data?.events?.[0]
  if(!record)return <RecordReturn record={record}/>
  return <ReadRegion inlineSource title="선택 전력 기록" query={query}>{query.data&&(schedulerRecordMatches(record,event)&&record.kind==="power"&&event?<PowerDetails event={event} record={record}/>:<RecordReturn record={record}/>)}</ReadRegion>
}
export function SchedulerPowerPage() {
  const history=useSchedulerHistory(),router=useRouter(),params=useSearchParams()
  const opener=React.useRef<HTMLAnchorElement|null>(null),heading=React.useRef<HTMLHeadingElement|null>(null)
  const node=params.get("filterNode")??"",powerRows=history.rows.filter(row=>!!row.event.power),rows=powerRows.filter(row=>!node||row.event.node?.uid===node)
  const nodes=new Map(powerRows.map(row=>[row.event.node?.uid??"",row.event.node?.name??"노드 이름 없음"]))
  return <div className="page">
    <h1 ref={heading} tabIndex={-1}>전력 기록</h1><p>전체·활성 실행 그룹의 실측 소비와 개별 계측 기록을 구분합니다. 개별 기록의 기간별 에너지를 더하지 않습니다.</p><Link href={`/scheduler/history?${new URLSearchParams(history.submitted)}`}>같은 구간의 판단 기록</Link>
    <HistoryControls history={history}/><PowerInterval from={history.submitted.from} to={history.submitted.to}/>
    <label>현재 가져온 개별 기록에서 노드 선택<select value={node} onChange={event=>{const next=new URLSearchParams(params.toString());if(event.target.value)next.set("filterNode",event.target.value);else next.delete("filterNode");router.replace(`/scheduler/power?${next}`)}}><option value="">전체 노드</option>{node&&!nodes.has(node)&&<option value={node}>선택한 이전 노드 ({node})</option>}{Array.from(nodes).filter(([uid])=>uid).map(([uid,name])=><option key={uid} value={uid}>{name} ({uid})</option>)}</select></label><p className="text-sm text-muted-foreground">노드 필터와 표시 개수·페이지는 아래 원본 기록에만 적용되며 전체 구간의 POOL 집계는 바꾸지 않습니다.</p>
    {["offset","kind","at","id"].some(key=>params.has(key))&&<Dialog open onOpenChange={open=>{if(!open)router.replace(`/scheduler/power?${schedulerPageParams(new URLSearchParams(params.toString()),params.get("cursor")??undefined)}`,{scroll:false})}}><DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-3xl" onCloseAutoFocus={event=>{event.preventDefault();if(opener.current?.isConnected)opener.current.focus();else heading.current?.focus()}}><DialogHeader><DialogTitle>전력 기록 상세</DialogTitle><DialogDescription>선택한 노드와 계측 범위의 단일 기록입니다.</DialogDescription></DialogHeader><SelectedPower record={readSchedulerRecord(params)}/></DialogContent></Dialog>}
    <ReadRegion title="현재 페이지의 전력 기록" query={history.query}><p>현재 페이지 {rows.length}개. 전체 구간 집계가 아닙니다.</p><DataTable data={rows} columns={[{key:"at",label:"사건 시각",value:row=>row.event.at},{key:"node",label:"노드",value:row=>`${row.event.node?.name??"제공 없음"} (${row.event.node?.uid??"UID 없음"})`},{key:"scope",label:"계측 범위",value:row=>`${row.event.meter??"없음"} / ${row.event.powerScope??"없음"}`},{key:"energy",label:"실측 에너지",value:row=>row.event.powerJoules==null?"측정값 없음":`${row.event.powerJoules} J`},{key:"samples",label:"표본 / 실패",value:row=>`${row.event.power?.samples?.length??0} / ${row.event.power?.failures?.length??0}`},{key:"detail",label:"상세",value:row=><Link scroll={false} onClick={event=>{opener.current=event.currentTarget}} href={`/scheduler/power?${new URLSearchParams({...Object.fromEntries(params),...Object.fromEntries(schedulerRecordParams(row.ref))})}`}>측정 자세히</Link>}]}/><HistoryPaging history={history}/>{history.query.data&&<SourceExport filename="scheduler-power-page.json" value={{scope:"현재 페이지",source:"v1/observations/scheduler",from:history.submitted.from,to:history.submitted.to,cursor:history.cursor,filterNode:node,rows}}/>}</ReadRegion>
  </div>
}

type ServiceIdentity={uid:string;definition:string}
function identities(event:SchedulerObservation):ServiceIdentity[] {
  const decision=event.decision
  const all=[decision?.keep,...(decision?.candidates??[]),decision?.target].flatMap(candidate=>candidate?.slo??[])
  return all.filter((row,index)=>all.findIndex(item=>item.uid===row.uid&&item.definition===row.definition)===index).map(({uid,definition})=>({uid,definition}))
}
function Expected({candidate}:{candidate:SchedulerCandidate|null}) {
  if(!candidate)return <p>후보 제공 없음</p>
  return <><p>{candidate.id} / {judgmentLabels[candidate.status] ?? candidate.status} / 예상 에너지 {candidate.expectedJoules==null?"계산값 없음":`${candidate.expectedJoules} J`}</p>{candidate.slo?.map((slo,index)=><p key={index} className="break-all">UID {slo.uid} / 정의 {slo.definition}: 예상 {slo.expectedNanos==null?"계산값 없음":`${Number(slo.expectedNanos) / 1e6} ms`}, {slo.percentile}% 기준, 목표 {nanosToMilliseconds(slo.targetNanos)} ms, 표본 {slo.samples}개, 목표 초과 비율 {slo.normalizedExceedance}<SourceJSON inline title="제공된 예상 결과" value={slo.predictedOutcomes}/></p>)}<p>{candidate.reasons?.join(" / ")||"사유 제공 없음"}</p><p>가정: {candidate.assumptions?.join(" / ")||"제공 없음"}</p><p>출처: {candidate.sources?.join(" / ")||"제공 없음"}</p></>
}
function StoredDetails({event}:{event:SchedulerObservation}) {
  const {decision,snapshot}=event
  if(!decision)return null
  const view:SchedulerView|undefined=snapshot?{settings:decision.settings,state:{snapshot,decision,actions:snapshot.actions,results:null}}:undefined
  return <>{snapshot?<DetailsDialog label="초기 구성 자세히" title="판단과 함께 제공된 초기 구성"><SnapshotDetails snapshot={snapshot}/></DetailsDialog>:<p>짝지어진 초기 snapshot 제공 없음. 다른 시각의 구성을 추정하지 않습니다.</p>}{view&&[decision.keep,decision.target].filter((candidate):candidate is SchedulerCandidate=>!!candidate).map((candidate,index)=><DetailsDialog key={`${candidate.id}-${index}`} label={candidate.keep?"유지안 근거":"목표 근거"} title="선택 기록의 후보 근거"><CandidateDetails candidate={candidate} view={view}/></DetailsDialog>)}</>
}
function ActualSummary({data}:{data:ObservationResult}) {
  return <><p>owner 전체 구간 {data.from} ~ {data.to}</p><p>전체 {data.total}건 / 선택 조건 {data.selected}건</p><p>모집단 {data.samplePopulation} / 방법 {data.quantileMethod}</p>{Object.entries(data.outcomes).map(([outcome,count])=><p key={outcome}>{outcome}: {count}건</p>)}{data.cohorts.length?data.cohorts.map(cohort=><p key={cohort.definition} className="break-all">정의 {cohort.definition} / 전체 {cohort.total}건 / 지연 표본 {cohort.sampleCount}개 / {cohort.percentile===undefined?"백분위 미제공":`${cohort.percentile}%`} 지연 {cohort.quantileNanos==null?"값 없음":`${Number(cohort.quantileNanos)/1e6} ms`} / 목표 {cohort.targetLatencyNanos==null?"미제공":`${nanosToMilliseconds(cohort.targetLatencyNanos)} ms`}</p>):<p>제공된 cohort 없음. 지연 값을 계산하지 않습니다.</p>}{data.incompleteWindows?.length?<p role="status">관측 공백: {data.incompleteWindows.join(" / ")}</p>:null}</>
}
function ActualPair({identity,a,b,aRef,bRef}:{identity:ServiceIdentity;a:SchedulerObservation;b:SchedulerObservation;aRef:SchedulerRecordRef;bRef:SchedulerRecordRef}) {
  const [ap,bp]=[a,b].map(event=>new URLSearchParams({uid:identity.uid,definition:identity.definition,from:event.decision!.search.windowStart,to:event.decision!.search.windowEnd,limit:"1"}))
  const valid=(params:URLSearchParams)=>!!identity.uid&&!!identity.definition&&!!params.get("from")&&!!params.get("to")&&!params.get("from")!.startsWith("0001-")&&!params.get("to")!.startsWith("0001-")&&params.get("from")!==params.get("to")
  const aq=useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(identity.uid)}?${new URLSearchParams(Array.from(ap).filter(([key])=>key!=="uid"))}`,{enabled:valid(ap),decoder:parseSchedulerJSON})
  const bq=useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(identity.uid)}?${new URLSearchParams(Array.from(bp).filter(([key])=>key!=="uid"))}`,{enabled:valid(bp),decoder:parseSchedulerJSON})
  return <section className="space-y-3"><h3 className="break-all">서비스 UID {identity.uid} / 고정 정의 {identity.definition}</h3><div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>A 입력 구간의 실제 결과</th><th>B 입력 구간의 실제 결과</th></tr></thead><tbody><tr>{[{params:ap,query:aq,label:"A"},{params:bp,query:bq,label:"B"}].map(({params,query,label})=><td key={label} className="align-top p-3">{valid(params)?<ReadRegion title={`${label} owner 요청 결과`} query={query}>{query.data&&<ActualSummary data={query.data}/>}<Link href={`/observations?${params}`}>같은 UID와 정의와 구간으로 요청 관측 열기</Link></ReadRegion>:<p>명시적 입력 구간 또는 고정 정의 제공 없음: 실제 결과 조회 불가</p>}</td>)}</tr></tbody></table></div><SourceExport filename="scheduler-comparison-actual.json" value={{scope:"선택 A/B 기록과 서비스별 실제 owner 조회",source:"v1/observations/workloads/:uid",records:{a:{ref:aRef,event:a},b:{ref:bRef,event:b}},identity,conditions:{a:ap.toString(),b:bp.toString()},actual:{a:aq.data??null,b:bq.data??null}}}/></section>
}
export function SchedulerComparePage() {
  const params=useSearchParams(),aRef=readSchedulerRecord(params,"a"),bRef=readSchedulerRecord(params,"b")
  const aq=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(aRef),{enabled:!!aRef,decoder:parseSchedulerJSON}),bq=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(bRef),{enabled:!!bRef,decoder:parseSchedulerJSON})
  const a=aq.data?.events?.[0],b=bq.data?.events?.[0]
  const aOK=aRef&&schedulerRecordMatches(aRef,a)&&aRef.kind==="decision"&&a?.decision,bOK=bRef&&schedulerRecordMatches(bRef,b)&&bRef.kind==="decision"&&b?.decision
  if(!aOK||!bOK)return <div className="page"><h1>판단 기록 비교</h1><Link href="/scheduler/history">기록 목록에서 A와 B 선택</Link>{!aRef?<RecordReturn record={null}/>:<ReadRegion title="A 선택 기록" query={aq}>{aq.data&&!aOK&&<RecordReturn record={aRef}/>}</ReadRegion>}{!bRef?<RecordReturn record={null}/>:<ReadRegion title="B 선택 기록" query={bq}>{bq.data&&!bOK&&<RecordReturn record={bRef}/>}</ReadRegion>}</div>
  const left=a!,right=b!,ad=left.decision!,bd=right.decision!
  const scopes=(event:SchedulerObservation)=>event.snapshot?.baselines?.map(item=>({node:item.node.uid,meter:item.meter,scope:item.scope}))??null
  const aIDs=identities(left),bIDs=identities(right),aScopes=scopes(left),bScopes=scopes(right)
  const conditions=[
    String(ad.settings.horizonNanos)!==String(bd.settings.horizonNanos)?"미래 예상 기간이 다릅니다.":null,
    !aIDs.length||!bIDs.length?"양쪽 서비스 UID와 고정 정의의 제공 상태를 확인합니다.":aIDs.length!==bIDs.length||aIDs.some(item=>!bIDs.some(other=>item.uid===other.uid&&item.definition===other.definition))?"서비스 UID 또는 고정 정의가 다릅니다.":null,
    !aScopes?.length||!bScopes?.length?"양쪽 계측기와 범위의 제공 상태를 확인합니다.":aScopes.length!==bScopes.length||aScopes.some(item=>!bScopes.some(other=>item.node===other.node&&item.meter===other.meter&&item.scope===other.scope))?"제공된 계측 범위가 다릅니다.":null,
    (!left.snapshot||!right.snapshot)?"짝지어진 초기 구성을 모두 확인할 수 없습니다.":null,
    ad.search.population!==bd.search.population?"입력 모집단이 다릅니다.":null,
  ].filter(Boolean)
  // ponytail: scans two selected node lists; index UIDs if large snapshots make comparison slow.
  const differentNodes=(a:SchedulerCandidate["nodes"]|undefined,b:SchedulerCandidate["nodes"]|undefined)=>(a?.length??0)!==(b?.length??0)||a?.some(node=>!b?.some(other=>node.node.uid===other.node.uid))
  if(differentNodes(left.snapshot?.nodes,right.snapshot?.nodes)||differentNodes(ad.target?.nodes,bd.target?.nodes))conditions.push("초기 또는 목표 노드 UID가 다릅니다.")
  const services=[...aIDs,...bIDs].filter((item,index,all)=>all.findIndex(row=>row.uid===item.uid&&row.definition===item.definition)===index)
  const columns=[{event:left,ref:aRef!},{event:right,ref:bRef!}]
  const rows:{label:string;render:(event:SchedulerObservation,ref:SchedulerRecordRef)=>React.ReactNode}[]=[
    {label:"기록 출처와 조회 범위",render:(event,ref)=><p>v1/observations/scheduler / {ref.from} ~ {ref.to} / offset {ref.offset} / 사건 {event.at}</p>},
    {label:"입력 관측 구간",render:event=><p>{event.decision!.search.windowStart} ~ {event.decision!.search.windowEnd}</p>},
    {label:"모집단과 부하",render:event=><><p>{event.decision!.search.population}</p><p>수신 {event.decision!.search.received} / 접수 {event.decision!.search.accepted} / 계산 입력 {event.decision!.search.computeInputs}</p><SourceJSON inline title="서비스별 제공 부하" value={event.decision!.search.loads}/></>},
    {label:"미래 예상 기간",render:event=><p>판단 {event.decision!.at} 기준 horizon {nanosToMilliseconds(event.decision!.settings.horizonNanos)} ms. 입력 관측 구간과 별개입니다.</p>},
    {label:"서비스 UID와 고정 정의",render:event=><>{identities(event).map(item=><p className="break-all" key={`${item.uid}/${item.definition}`}>{item.uid} / {item.definition}</p>)}{!identities(event).length&&<p>예상 SLO의 서비스 정의 제공 없음</p>}<SourceJSON inline title="초기 구성의 서비스 정의" value={event.snapshot?.workloads??null}/></>},
    {label:"운영 설정",render:event=><SettingsSummary settings={event.decision!.settings}/>},
    {label:"초기 구성과 후보 근거",render:event=><StoredDetails event={event}/>},
    {label:"유지안 예상 SLO와 J",render:event=><Expected candidate={event.decision!.keep}/>},
    {label:"목표 예상 SLO와 J",render:event=><Expected candidate={event.decision!.target}/>},
    {label:"판단 사유와 참조",render:event=><><p>{judgmentLabels[event.decision!.status] ?? event.decision!.status}: {event.decision!.reasons?.join(" / ")||"사유 제공 없음"}</p><p>참조: {event.decision!.references?.join(" / ")||"제공 없음"}</p><SourceJSON inline title="제공된 탐색 근거" value={event.decision!.search}/></>},
    {label:"제공된 변경 계획과 실행 관측",render:event=><><SourceJSON inline title="유지안 동작" value={event.decision!.keep.actions}/><SourceJSON inline title="목표 동작" value={event.decision!.target?.actions??null}/><SourceJSON inline title="짝지어진 구성의 진행 동작" value={event.snapshot?.actions??null}/>{event.action?<SourceJSON inline title="같은 기록의 실행 관측" value={event.action}/>:<p>이 판단 기록에 실행 결과 제공 없음. 다른 사건의 동작 ID만으로 결과를 결합하지 않습니다.</p>}</>},
    {label:"관련 전력 관측",render:event=><><Link href={`/scheduler/power?${new URLSearchParams({from:event.decision!.search.windowStart,to:event.decision!.search.windowEnd})}`}>명시적 관련 구간의 계측 기록 확인</Link><SourceJSON inline title="제공된 계측기와 범위" value={scopes(event)}/><p>관련 시간창 조회이며 개별 동작의 인과적 에너지 귀속이 아닙니다.</p></>},
  ]
  return <div className="page"><h1>판단 기록 비교</h1><Link href="/scheduler/history">판단 기록</Link><section aria-label="비교 조건 확인"><h2>결과를 보기 전 조건 확인</h2>{conditions.length?conditions.map((message,index)=><p role="status" key={index}>{message}</p>):<p>제공된 horizon, 서비스 정의와 계측 범위가 같습니다. 같은 실험이나 인과 관계를 보장하지 않습니다.</p>}</section><div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>비교 항목</th><th>A</th><th>B</th></tr></thead><tbody>{rows.map(row=><tr key={row.label}><th className="align-top p-3">{row.label}</th>{columns.map(({event,ref},index)=><td key={index} className="align-top p-3">{row.render(event,ref)}</td>)}</tr>)}</tbody></table></div><h2>입력 관측 구간의 실제 요청 결과</h2><p>각 UID와 고정 정의와 명시적 구간의 owner 전체 구간 cohort를 조회합니다. 미래 horizon의 결과, 실험 점수 또는 동작별 귀속으로 해석하지 않습니다. 개별 요청은 limit=1이어도 통계는 owner 전체 구간 값입니다.</p>{services.length?services.map(identity=><ActualPair key={`${identity.uid}/${identity.definition}`} identity={identity} a={left} b={right} aRef={aRef!} bRef={bRef!}/>):<p>고정 서비스 정의가 없어 실제 요청 결과 조회 불가</p>}<SourceExport filename="scheduler-comparison-selected.json" value={{scope:"선택 A/B 기록과 제공 조건",source:"v1/observations/scheduler",a:{ref:aRef,event:left},b:{ref:bRef,event:right},conditions}}/><SourceJSON inline title="선택 A/B 기록 원본" value={{a:left,b:right}}/></div>
}
