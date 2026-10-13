"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import type { NodeRef, OperationResult, OperationState, SchedulerAction, SchedulerActionObservation, SchedulerControllerState, SchedulerObservationPage, SchedulerOperation } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { readSchedulerRecord, schedulerRecordMatches, schedulerRecordParams, schedulerRecordPath, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { safeSource } from "@/lib/source-export"
import { DataTable } from "@/components/data-table"
import { DetailsDialog, ReadRegion, SourceExport, SourceJSON } from "@/components/read-region"
import { HistoryControls, HistoryPaging, useSchedulerHistory, type SchedulerHistoryRow } from "@/features/scheduler-history"

const kinds:Record<string,string>={prepare:"실행 준비",route:"요청 분배 변경",stop:"요청 접수 중지",drain:"기존 요청 마무리",remove:"인스턴스 제거",power:"전력모드 변경",membership:"실행 그룹 소속 변경",resize:"인스턴스 교체"}
const statuses:Record<string,string>={planned:"계획됨",starting:"시작 기록",running:"실행 중",complete:"완료",failed:"실패",cancel:"취소",partial:"일부 변경 관측"}
const time=(value?:string)=>!value||value.startsWith("0001-")?"제공된 시각 없음":value
const label=(value:string)=>statuses[value]??value

function Lines({values}:{values:string[]|null|undefined}) {return values?.length?<ul className="list-inside list-disc">{values.map((value,index)=><li key={index}>{value}</li>)}</ul>:<p>제공된 항목 없음</p>}
function Node({node}:{node:NodeRef}) {return <span>{node.name||"이름 미제공"} <small className="break-all">({node.uid})</small></span>}
function object(value:unknown):Record<string,unknown>|undefined {return value!==null&&typeof value==="object"&&!Array.isArray(value)?value as Record<string,unknown>:undefined}
function operation(value:unknown):SchedulerOperation|undefined {
  const item=object(value),node=object(item?.node)
  return item&&typeof item.id==="string"&&typeof item.kind==="string"&&typeof node?.uid==="string"&&typeof node.name==="string"?item as SchedulerOperation:undefined
}
function result(value:unknown,action:SchedulerActionObservation):OperationResult|undefined {
  const item=object(value),op=operation(item?.operation)
  return item&&op&&op.id===action.id&&op.node.uid===action.node.uid&&item.startedAt===action.startedAt&&typeof item.status==="string"&&object(item.before)&&object(item.actual)?item as OperationResult:undefined
}
function historyLink(ref:SchedulerRecordRef) {return `/transitions/${encodeURIComponent(ref.id)}?${schedulerRecordParams(ref)}&view=history`}
function historyWindow(from?:string|null,to?:string|null) {
  return `/transitions?${new URLSearchParams({view:"history",...(from&&to?{from,to}:{})})}`
}

function Native({value}:{value:unknown}) {
  const entries=Object.entries(object(safeSource(value))??{})
  return <>{entries.length?entries.map(([id,state])=>{
    const fields=object(state),requests=Array.isArray(fields?.requests)?fields.requests:[]
    return <details key={id}><summary className="break-all">{id} 실행기 관측</summary><dl className="grid gap-2">{Object.entries(fields??{}).filter(([,value])=>value!==null&&["string","number","bigint","boolean"].includes(typeof value)).map(([key,value])=><React.Fragment key={key}><dt>{({at:"수신 관측 시각",ready:"실행 준비",acceptedRequests:"접수 요청 수",completedRequests:"완료 요청 수",executions:"실행 수"} as Record<string,string>)[key]??key}</dt><dd className="break-all">{typeof value==="boolean"?(value?"예":"아니요"):String(value)}</dd></React.Fragment>)}</dl>{requests.length>0&&<><h4>관측된 실행기 요청</h4><ul>{requests.map((request,index)=>{const row=object(request);return <li key={index}>{String(row?.phase??"단계 미제공")} / 큐 {String(row?.queueMS??"미제공")} ms / 실행 경과 {String(row?.serviceElapsedMS??"미제공")} ms</li>})}</ul></>}<SourceJSON inline value={state}/></details>
  }):<p>제공된 실행기 관측 없음</p>}</>
}

function StateDetails({state,title}:{state:OperationState;title:string}) {
  return <section className="space-y-3"><h3>{title}</h3><p>관측 {time(state.at)}</p><p>노드 <Node node={state.node}/> / 실행 그룹 {state.pool||"제공 없음"} / 전력모드 {state.powerMode||"제공 없음"}</p><h4>관측된 상태</h4><Lines values={state.states}/><Lines values={state.reasons}/><p>Pod {state.pods?.join(", ")||"제공된 목록 없음"}</p><h4>인스턴스</h4>{state.instances?.length?state.instances.map((instance,index)=><details key={index}><summary>{instance.id} / 준비 {instance.ready?"완료":"확인 대기"}</summary><p><Node node={instance.node}/> / 실행 그룹 {instance.pool} / 프로파일 {instance.profile} / 관측 {time(instance.observedAt)}</p>{instance.reason&&<p>{instance.reason}</p>}<ul>{instance.workloads?.map(workload=><li key={workload.uid}>{workload.name} ({workload.uid}) / 대기 {String(instance.waiting?.[workload.uid]??"미제공")} / 실행 {String(instance.running?.[workload.uid]??"미제공")} / 배정 후 확인 대기 {String(instance.assignedUnknown?.[workload.uid]??"미제공")}</li>)}</ul></details>):<p>제공된 인스턴스 없음</p>}<h4>실행기 관측</h4><Native value={state.native}/></section>
}

function OperationDetails({value}:{value:SchedulerOperation}) {
  const target=value.target
  const plans=target?.actions?.filter(action=>action.id===value.id&&action.node.uid===value.node.uid&&action.kind===value.kind&&action.instance===value.instance)??[]
  const plan=plans.length===1?plans[0]:undefined
  return <section className="space-y-3">
    <h3>실행 대상과 계획</h3>
    <p>{kinds[value.kind]??value.kind} / <Node node={value.node}/></p>
    <p>인스턴스 {value.instance||"제공 없음"} / 실행 그룹 {value.pool||"제공 없음"}</p>
    {plan?<><p>제공된 목표 안의 계획 상태 {label(plan.status)} / 계획 소요 시간 {nanosToMilliseconds(plan.elapsedNanos)} ms</p><p>변경 전 {plan.from||"제공 없음"} / 목표 {plan.to||"제공 없음"} / 취소 가능 관측 {plan.cancelable?"예":"아니요"}</p><Dependencies ids={plan.dependsOn}/>{plan.reason&&<p>{plan.reason}</p>}</>:<p>이 실행 계획에 단일하게 일치하는 동작의 소요 시간, 취소 가능 여부와 의존 동작은 제공되지 않았습니다.</p>}
    {value.prepare&&<><p>준비 이름 {value.prepare.name} / 메모리 {String(value.prepare.memoryBytes)} bytes</p><p>준비 인스턴스 {value.prepare.instance.id} / 연결 서비스 {value.prepare.instance.workloadUIDs?.join(", ")||"없음"}</p><p>CPU {value.prepare.instance.allocation.cpus?.join(", ")||"없음"}, GPU {value.prepare.instance.allocation.gpuUUID||"없음"}, 메모리 {String(value.prepare.instance.allocation.memoryBytes)} bytes</p><SourceJSON inline value={{bundle:value.prepare.bundle,store:value.prepare.store,config:value.prepare.config}}/></>}
    {value.actuation!==undefined&&<details><summary>제공된 전력 제어 계획</summary><SourceJSON inline value={value.actuation}/></details>}
    {target&&<><p>목표 {target.id} / {target.status} / 예상 에너지 {target.expectedJoules==null?"계산값 없음":`${target.expectedJoules} J`}</p><Lines values={target.reasons}/><details><summary>목표의 노드, 인스턴스와 요청 분배</summary>{target.nodes?.map((node,index)=><p key={index}><Node node={node.node}/> / 실행 그룹 {node.pool} / 전력모드 {node.powerMode} / 준비 {node.ready?"완료":"확인 대기"}</p>)}{target.instances?.map((instance,index)=><p key={index}>{instance.id} / {instance.node.name} / CPU {instance.allocation.cpus?.join(", ")||"없음"}, GPU {instance.allocation.gpuUUID||"없음"}, 메모리 {String(instance.allocation.memoryBytes)} bytes / 서비스 {instance.workloadUIDs?.join(", ")||"없음"}</p>)}<p>요청 분배는 원래 가중치이며 확률이 아닙니다.</p>{Object.entries(target.routing??{}).map(([uid,weights])=><p key={uid}>{uid}: {Object.entries(weights).map(([id,weight])=>`${id} ${weight}`).join(", ")}</p>)}</details></>}</section>
}

function ResultDetails({value}:{value:OperationResult}) {
  return <><p role="status">관측 결과 {label(value.status)}</p><p>시작 {time(value.startedAt)} / 종료 {time(value.endedAt)}</p>{value.error&&<p role="alert" className="break-all">{value.error}</p>}<h3>제공된 진행 기록</h3><Lines values={value.progress}/><OperationDetails value={value.operation}/><StateDetails title="변경 전 관측" state={value.before}/><StateDetails title="실제 변경 후 관측" state={value.actual}/>{value.drainNative&&<section><h3>기존 요청 마무리 관측</h3><Native value={value.drainNative}/></section>}</>
}

type CurrentRow={action?:SchedulerAction;result?:OperationResult;id:string;node:NodeRef;kind:string;status:string}
function currentLink(row:CurrentRow) {
  return `/transitions/${encodeURIComponent(row.id)}?${new URLSearchParams({view:"current",stage:row.result?"result":"action",node:row.node.uid,...(row.result?{startedAt:row.result.startedAt}:{})})}`
}
function Dependencies({ids,rows}:{ids:string[]|null;rows?:CurrentRow[]}) {
  return <><h3>먼저 필요한 동작</h3>{ids?.length?<ul>{ids.map((id,index)=>{const matches=rows?.filter(row=>row.id===id);return <li key={index}>{matches?.length===1?<Link href={currentLink(matches[0])}>{id}</Link>:rows?<Link href={`/transitions/${encodeURIComponent(id)}?view=current`}>{id} 동작 선택</Link>:<span>{id} / 일치하는 기록이 제공되지 않았습니다.</span>}</li>})}</ul>:<p>제공된 의존 동작 없음</p>}</>
}
function CurrentDetails({row,rows}:{row:CurrentRow;rows:CurrentRow[]}) {
  const action=row.action
  return <div className="space-y-4"><p>동작 {row.id} / <Node node={row.node}/></p>{action?<><p>{kinds[action.kind]??action.kind} / {label(action.status)}</p><p>{action.status==="planned"?"계획 소요 시간":"보고된 실행 경과 또는 소요 시간"} {nanosToMilliseconds(action.elapsedNanos)} ms</p><p>변경 전 {action.from||"제공 없음"} / 목표 {action.to||"제공 없음"} / 인스턴스 {action.instance||"제공 없음"}</p><p>취소 가능 관측 {action.cancelable?"예":"아니요"}. 이 API에는 수동 취소 또는 재시도 조작이 없습니다.</p>{action.reason&&<p className="break-all">{action.reason}</p>}<p>비용 근거 {action.costID||"제공 없음"}</p><Dependencies ids={action.dependsOn} rows={rows}/><p>현재 동작에는 시작 시각이 제공되지 않아 결과 기록과 임의로 합치지 않습니다. 결과는 목록에서 별도로 선택합니다.</p></>:row.result&&<ResultDetails value={row.result}/>}<SourceExport filename="scheduler-transition-selected-current.json" value={{source:"GET v1/scheduler/state",range:"선택한 현재 동작 또는 결과",identity:{id:row.id,node:row.node.uid,stage:row.result?"result":"action",startedAt:row.result?.startedAt},data:action??row.result}}/><SourceJSON inline value={action??row.result}/></div>
}
function HistoryActionDetails({row}:{row:SchedulerHistoryRow}) {
  const action=row.event.action!
  const expected=operation(action.expected),actual=result(action.actual,action)
  const validExpected=expected?.id===action.id&&expected.node.uid===action.node.uid
  return <section className="space-y-4"><p>사건 {time(row.event.at)} / 동작 {action.id} / <Node node={action.node}/></p><p>{kinds[action.kind]??action.kind} / 관측 단계 {label(action.phase)} / 인스턴스 {action.instance||"제공 없음"}</p><p>시작 {time(action.startedAt)} / 완료 {time(action.completedAt)}</p>{action.error&&<p role="alert" className="break-all">{action.error}</p>}{actual?<ResultDetails value={actual}/>:<>{validExpected?<OperationDetails value={expected}/>:<p>표시할 실행 계획 필드가 제공되지 않았습니다. 제공된 내용은 원본에서 확인합니다.</p>}<p>이 사건에서 구조화된 변경 전후와 진행 결과를 확인하지 못했습니다. 다른 시점의 결과로 대체하지 않습니다.</p></>}{action.evidence?.length?<details><summary>제공된 근거</summary><SourceJSON inline value={action.evidence}/></details>:null}</section>
}
function HistoryDetails({row,known=[]}:{row:SchedulerHistoryRow;known?:SchedulerHistoryRow[]}) {
  const action=row.event.action!
  const paired=known.filter(other=>other.ref.offset!==row.ref.offset&&other.event.action?.id===action.id&&other.event.action.node.uid===action.node.uid&&other.event.action.startedAt===action.startedAt)
  const exportValue={source:"GET v1/observations/scheduler",range:"선택 기록",from:row.ref.from,to:row.ref.to,cursor:row.ref.offset,population:"로컬 scheduler journal의 제공된 사건",identity:row.ref,data:row.event}
  return <div className="space-y-4"><HistoryActionDetails row={row}/>{paired.length?<><h3>현재 가져온 기록 중 시작 identity가 일치하는 단계</h3>{paired.map(other=><details key={other.ref.offset}><summary>{label(other.event.action!.phase)} / {other.event.at}</summary><HistoryActionDetails row={other}/><Link href={historyLink(other.ref)}>이 단계의 정확한 기록 열기</Link></details>)}</>:<p>이 기록의 단계만 관측되었습니다. 동작 ID, 노드 UID와 시작 시각이 모두 일치하는 다른 기록은 제공되지 않았습니다.</p>}<p>현재 상태를 과거의 의존 동작으로 대체하지 않습니다. 제공된 과거 동작의 계획을 표시하며 일치하는 의존 기록의 시작 identity는 제공되지 않았습니다.</p><p>취소 가능 여부는 관측 정보입니다. 수동 취소 또는 재시도 API는 제공되지 않습니다.</p><SourceExport value={exportValue} filename="scheduler-transition-selected-record.json"/><SourceJSON inline value={exportValue}/></div>
}

function HistoryList() {
  const history=useSchedulerHistory(),[node,setNode]=React.useState("")
  const actions=history.rows.filter(row=>!!row.event.action),nodes=new Map(actions.map(row=>[row.event.action!.node.uid,row.event.action!.node.name]))
  const rows=actions.filter(row=>!node||row.event.action!.node.uid===node)
  const page={source:"GET v1/observations/scheduler",range:"현재 페이지",...history.submitted,cursor:history.cursor,population:"로컬 scheduler journal의 제공된 사건",filter:{kind:"action",node:node||null,scope:"현재 가져온 기록에서"},records:rows}
  return <><HistoryControls history={history}/><ReadRegion title="선택 구간의 변경 기록" query={history.query}><p>현재 가져온 기록에서 동작 사건을 표시합니다. 현재 페이지 {history.rows.length}건 중 동작 {actions.length}건, 표시 {rows.length}건입니다. 전체 구간 건수 또는 집계가 아닙니다.</p><label>현재 가져온 기록에서 노드 선택<select value={node} onChange={event=>setNode(event.target.value)}><option value="">전체 노드</option>{node&&!nodes.has(node)&&<option value={node}>이전 페이지 선택 ({node})</option>}{Array.from(nodes,([uid,name])=><option key={uid} value={uid}>{name} ({uid})</option>)}</select></label><DataTable data={rows} columns={[
    {key:"at",label:"사건 시각",value:row=>row.event.at,sortValue:row=>row.event.at},
    {key:"kind",label:"동작",value:row=>kinds[row.event.action!.kind]??row.event.action!.kind},
    {key:"node",label:"노드",value:row=><Node node={row.event.action!.node}/>},
    {key:"status",label:"관측 단계",value:row=>label(row.event.action!.phase)},
    {key:"detail",label:"상세",value:row=><div className="grid gap-2"><DetailsDialog label="제공된 단계 보기" title="변경 기록 상세"><HistoryDetails row={row} known={actions}/></DetailsDialog><Link href={historyLink(row.ref)}>정확한 기록 열기</Link></div>},
  ]}/><HistoryPaging history={history}/><SourceExport value={page} filename="scheduler-transition-current-page.json"/><SourceJSON inline value={page}/></ReadRegion></>
}

function HistoryRecord({id}:{id:string}) {
  const params=useSearchParams(),ref=readSchedulerRecord(params)
  const valid=!!ref&&ref.kind==="action"&&ref.id===id
  const query=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(ref),{enabled:valid,decoder:parseSchedulerJSON})
  const event=query.data?.events?.[0],matches=valid&&schedulerRecordMatches(ref,event)&&!!event?.action
  const back=historyWindow(ref?.from??params.get("from"),ref?.to??params.get("to"))
  return <><Link href={back}>선택한 구간의 변경 기록으로 돌아가기</Link>{!valid?<p role="alert">선택한 기록 확인 필요. 구간, offset과 예상 identity가 모두 필요합니다. 현재 동작으로 대체하지 않습니다.</p>:<ReadRegion title="선택한 변경 기록" query={query}>{matches&&event?<HistoryDetails row={{event,ref}}/>:<p role="alert">선택한 기록 확인 필요. 보관 범위 또는 기록 identity가 달라졌습니다. 같은 동작 ID의 다른 사건으로 대체하지 않습니다.</p>}</ReadRegion>}</>
}

function CurrentList({id}:{id?:string}) {
  const params=useSearchParams()
  const query=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const rows:CurrentRow[]=[...(query.data?.actions??[]).map(action=>({action,id:action.id,node:action.node,kind:action.kind,status:action.status})),...(query.data?.results??[]).map(result=>({result,id:result.operation.id,node:result.operation.node,kind:result.operation.kind,status:result.status}))]
  const stage=params.get("stage"),node=params.get("node"),startedAt=params.get("startedAt")
  const selected=rows.filter(row=>row.id===id&&(!stage||stage===(row.result?"result":"action"))&&(node===null||row.node.uid===node)&&(startedAt===null||row.result?.startedAt===startedAt))
  const selectionRequested=stage!==null||node!==null||startedAt!==null
  const tableRows=id?selected:rows
  return <ReadRegion title={id?"선택한 현재 동작":"현재 동작과 실행 결과"} query={query}>{id&&<Link href="/transitions?view=current">현재 목록으로 돌아가기</Link>}{id&&selected.length===1?<CurrentDetails row={selected[0]} rows={rows}/>:<>{id&&<p role={selected.length?"status":"alert"}>{selected.length?"같은 동작 ID의 항목이 여럿입니다. 노드와 시작 시각을 확인하고 동작 또는 결과를 선택하세요.":selectionRequested?"선택한 동작 확인 필요. 현재 상태에 같은 identity가 없습니다. 다른 결과로 대체하지 않습니다.":"현재 상태에 이 동작이 없습니다. 기록 목록에서 제공된 사건을 선택하세요."}</p>}<p>동작 상태와 결과 사건을 별도 행으로 표시합니다. 시작 시각 없는 동작을 같은 ID의 결과에 임의로 합치지 않습니다.</p><DataTable data={tableRows} columns={[
    {key:"kind",label:"동작",value:row=>kinds[row.kind]??row.kind},
    {key:"stage",label:"자료",value:row=>row.result?"실행 결과":"현재 동작"},
    {key:"node",label:"노드",value:row=><Node node={row.node}/>},
    {key:"target",label:"대상",value:row=>row.action?row.action.to||row.action.instance||"제공 없음":row.result?.operation.pool||row.result?.operation.instance||"제공 없음"},
    {key:"status",label:"상태",value:row=>label(row.status)},
    {key:"elapsed",label:"계획 또는 경과",value:row=>row.action?`${row.action.status==="planned"?"계획":"경과/소요"} ${nanosToMilliseconds(row.action.elapsedNanos)} ms`:"결과에서 별도 확인"},
    {key:"cancelable",label:"취소 가능 관측",value:row=>row.action?(row.action.cancelable?"예":"아니요"):"제공 없음"},
    {key:"started",label:"시작 시각",value:row=>time(row.result?.startedAt)},
    {key:"detail",label:"상세",value:row=><div className="grid gap-2"><DetailsDialog label="대상과 진행 보기" title="변경 진행 상세"><CurrentDetails row={row} rows={rows}/></DetailsDialog><Link href={currentLink(row)}>이 동작 열기</Link></div>},
  ]}/>{!id&&<><p>현재 API가 제공하는 변경 조작은 운영 설정 저장입니다. 수동 취소와 재시도는 제공되지 않습니다.</p><SourceExport value={{source:"GET v1/scheduler/state",range:"현재 응답의 동작과 결과",observedAt:query.data?.snapshot.at,actions:query.data?.actions,results:query.data?.results}} filename="scheduler-transition-current.json"/><SourceJSON inline value={{actions:query.data?.actions,results:query.data?.results}}/></>}</>}</ReadRegion>
}

export function SchedulerTransitionsPage({id}:{id?:string}) {
  const params=useSearchParams(),history=params.get("view")==="history"||params.has("offset")||params.has("kind")||params.has("at")
  return <div className="page grid gap-6"><h1>{id?"변경 상세":"변경 진행"}</h1><nav className="toolbar" aria-label="변경 진행 관련 화면"><Link href="/scheduler">현재 운영 판단</Link><Link href="/configuration">운영 설정</Link><Link href="/transitions?view=current" aria-current={!history?"page":undefined}>현재 변경</Link><Link href={historyWindow(params.get("from"),params.get("to"))} aria-current={history?"page":undefined}>구간의 변경 기록</Link></nav>{history?(id?<HistoryRecord id={id}/>:<HistoryList/>):<CurrentList id={id}/>}</div>
}
