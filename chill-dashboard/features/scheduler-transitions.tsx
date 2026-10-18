"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import type { NodeRef, OperationResult, OperationState, Routing, SchedulerAction, SchedulerActionObservation, SchedulerControllerState, SchedulerObservationPage, SchedulerOperation } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { readSchedulerRecord, schedulerRecordMatches, schedulerRecordParams, schedulerRecordPath, type SchedulerRecordRef } from "@/lib/scheduler-history"
import { safeSource } from "@/lib/source-export"
import { QueryGate, SourceExport, SourceJSON, StatusBadge, statusLabel } from "@/components/read-region"
import { Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { HistoryControls, HistoryPaging, useSchedulerHistory, type SchedulerHistoryRow } from "@/features/scheduler-history"
import { actionNodeName, actionKinds as kinds, actionStatuses as statuses, actionTones } from "@/features/scheduler"

const time=(value?:string)=>!value||value.startsWith("0001-")?"제공된 시각 없음":value
const when=(value?:string)=>{const date=new Date(value??"");return !value||value.startsWith("0001-")||!Number.isFinite(date.getTime())?"시각 없음":date.toLocaleString("ko-KR",{dateStyle:"short",timeStyle:"medium"})}
const label=(value:string)=>statuses[value]??statusLabel(value)
const kindLabel=(value:string)=>kinds[value]??statusLabel(value)
const Badge=({status}:{status:string})=><StatusBadge tone={actionTones[status]??"idle"}>{label(status)}</StatusBadge>

function Lines({values}:{values:string[]|null|undefined}) {return values?.length?<ul className="list-inside list-disc">{values.map((value,index)=><li key={index}>{value}</li>)}</ul>:<p>제공된 항목 없음</p>}
function Node({node}:{node:NodeRef}) {return <span>{actionNodeName(node)} <small className="break-all">({node.uid})</small></span>}
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

const routingKind="manual-static-routing"
function routingConclusion(result:OperationResult):[title:string,lede:string] {
  const split=`설정 revision ${result.operation.sourceConfigRV||"없음"}의 분배`
  if(result.status==="complete")return [`${split}가 반영됐어요`,"설정한 비율과 실제로 읽어 온 비율을 나란히 보여줘요."]
  if(result.status==="running"||result.status==="starting")return [`${split}를 반영하고 있어요`,"실행기가 준비되기를 기다리고 있어요. 반영되면 실제 비율이 채워져요."]
  return [`${split}를 ${result.status==="partial"?"일부만 반영했어요":"반영하지 못했어요"}`,"설정한 분배를 반영하지 못했어요. 원본 보기에서 ChillServer가 남긴 이유를 확인해 주세요."]
}
// Declared split (operation.staticRouting) next to the read-back split (actual.routing), as each service's share per instance.
function RoutingSplit({result}:{result:OperationResult}) {
  const state=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const declared=result.operation.staticRouting??{},actual=result.actual.routing
  const services=new Map((state.data?.snapshot.workloads??[]).flatMap(item=>item?[[item.uid,item.name] as const]:[]))
  const nodes=new Map((result.operation.target?.instances??[]).map(instance=>[instance.id,instance.node.name]))
  const ids=[...new Set([declared,actual??{}].flatMap(routing=>Object.values(routing).flatMap(weights=>Object.keys(weights))))].sort()
  const instanceName=(id:string)=>{const node=nodes.get(id)??"이전";const same=ids.filter(other=>(nodes.get(other)??"이전")===node);return `${node} 실행기${same.length>1?` ${same.indexOf(id)+1}`:""}`}
  const share=(routing:Routing|undefined,uid:string,id:string)=>{
    if(!routing)return "반영 대기"
    const weights=routing[uid]??{},total=Object.values(weights).reduce((sum,weight)=>sum+weight,0)
    return total?`${(100*(weights[id]??0)/total).toLocaleString("ko-KR",{maximumFractionDigits:1})}%`:"0%"
  }
  const uids=[...new Set([...Object.keys(declared),...Object.keys(actual??{})])]
  return <section className="rows-group" aria-label="서비스 → 인스턴스 비율"><h2 className="rows-label">서비스 → 인스턴스 비율</h2><div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>서비스 → 인스턴스</th><th>설정한 비율</th><th>실제 비율</th></tr></thead><tbody>
    {uids.flatMap(uid=>[...new Set([...Object.keys(declared[uid]??{}),...Object.keys(actual?.[uid]??{})])].map(id=><tr key={`${uid}/${id}`}><td>{services.get(uid)??"이전 등록 서비스"} → {instanceName(id)}</td><td>{share(declared,uid,id)}</td><td>{share(actual,uid,id)}</td></tr>))}
  </tbody></table></div>{!uids.length&&<p>설정한 분배가 기록되지 않았어요.</p>}</section>
}

type CurrentRow={action?:SchedulerAction;result?:OperationResult;id:string;node:NodeRef;kind:string;status:string}
function currentLink(row:CurrentRow) {
  return `/transitions/${encodeURIComponent(row.id)}?${new URLSearchParams({view:"current",stage:row.result?"result":"action",node:row.node.uid,...(row.result?{startedAt:row.result.startedAt}:{})})}`
}
function currentRows(data?:SchedulerControllerState):CurrentRow[] {
  return [...(data?.actions??[]).map(action=>({action,id:action.id,node:action.node,kind:action.kind,status:action.status})),...(data?.results??[]).map(result=>({result,id:result.operation.id,node:result.operation.node,kind:result.operation.kind,status:result.status}))]
}
function Dependencies({ids,rows}:{ids:string[]|null;rows?:CurrentRow[]}) {
  return <><h3>먼저 필요한 동작</h3>{ids?.length?<ul>{ids.map((id,index)=>{const matches=rows?.filter(row=>row.id===id);return <li key={index}>{matches?.length===1?<Link href={currentLink(matches[0])}>{id}</Link>:rows?<Link href={`/transitions/${encodeURIComponent(id)}?view=current`}>{id} 동작 선택</Link>:<span>{id} / 일치하는 기록이 제공되지 않았습니다.</span>}</li>})}</ul>:<p>제공된 의존 동작 없음</p>}</>
}
// One current action or one result. Actions carry no start time, so they are never merged with a result of the same ID.
function CurrentSummary({row,rows}:{row:CurrentRow;rows:CurrentRow[]}) {
  const {action,result}=row
  const source=<RowDialog label="원본 자료와 내보내기" title="원본 자료">
    {action&&<><Facts items={[["동작 ID",<span key="id" className="break-all">{row.id}</span>],["노드 UID",<span key="node" className="break-all">{row.node.uid}</span>],["인스턴스",action.instance||"제공 없음"],["비용 근거",action.costID||"제공 없음"]]}/>{action.reason&&<p className="break-all">{action.reason}</p>}<p className="text-sm text-muted-foreground">현재 동작에는 시작 시각이 없어 결과 기록과 합치지 않아요. 결과는 목록에서 따로 골라요. 이 API에는 수동 취소나 재시도가 없어요.</p></>}
    {result?.error&&<p role="alert" className="break-all">{result.error}</p>}
    <SourceExport filename="scheduler-transition-selected-current.json" value={{source:"GET v1/scheduler/state",range:"선택한 현재 동작 또는 결과",identity:{id:row.id,node:row.node.uid,stage:result?"result":"action",startedAt:result?.startedAt},data:action??result}}/><SourceJSON inline value={action??result}/>
  </RowDialog>
  if(action)return <>
    <Facts items={[["노드",actionNodeName(row.node)],["상태",label(action.status)],[action.status==="planned"?"예상 소요 시간":"경과 또는 소요 시간",`${nanosToMilliseconds(action.elapsedNanos)} ms`],["변경 전",action.from||"제공 없음"],["목표",action.to||"제공 없음"],["취소 가능",action.cancelable?"예":"아니요"]]}/>
    <Rows><RowDialog label="먼저 필요한 동작" value={`${action.dependsOn?.length??0}개`} title="먼저 필요한 동작"><Dependencies ids={action.dependsOn} rows={rows}/></RowDialog>{source}</Rows>
  </>
  return result?<>
    <Facts items={[["노드",actionNodeName(row.node)],["결과",label(result.status)],["시작",when(result.startedAt)],["종료",when(result.endedAt)]]}/>
    {row.kind===routingKind?<RoutingSplit result={result}/>:result.error&&<p role="alert">실행 중 오류가 있었어요. 내용은 원본 자료에서 볼 수 있어요.</p>}
    <Rows>
      <RowDialog label="진행 기록" value={`${result.progress?.length??0}개`} title="진행 기록"><Lines values={result.progress}/></RowDialog>
      <RowDialog label="실행 대상과 계획" title="실행 대상과 계획"><OperationDetails value={result.operation}/></RowDialog>
      <RowDialog label="변경 전 상태" title="변경 전 관측"><StateDetails title="변경 전 관측" state={result.before}/></RowDialog>
      <RowDialog label="실제 변경 후 상태" title="실제 변경 후 관측"><StateDetails title="실제 변경 후 관측" state={result.actual}/></RowDialog>
      {result.drainNative&&<RowDialog label="기존 요청 마무리" title="기존 요청 마무리 관측"><Native value={result.drainNative}/></RowDialog>}
      {source}
    </Rows>
  </>:null
}

function CurrentList() {
  const query=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const rows=currentRows(query.data),actions=rows.filter(row=>row.action),results=rows.filter(row=>row.result)
  return <Screen back={["/scheduler","운영 판단"]} title={!query.data?"변경 진행":actions.length?`진행 중인 변경이 ${actions.length}건 있어요`:"진행 중인 변경이 없어요"} lede="동작과 실행 결과를 따로 보여줘요. 목표나 설정 저장만으로 실제 변경이 끝났다고 보지 않아요.">
    <QueryGate query={query}>{query.data&&<>
      {actions.length>0&&<Rows label="지금 동작">{actions.map((row,index)=><Row key={`${row.id}/${row.node.uid}/${index}`} href={currentLink(row)} label={kindLabel(row.kind)} hint={`${actionNodeName(row.node)}${row.action?.from||row.action?.to?` · ${row.action.from||"제공 없음"} → ${row.action.to||"제공 없음"}`:""}`} value={<Badge status={row.status}/>}/>)}</Rows>}
      {results.length>0&&<Rows label="실행 결과">{results.map((row,index)=><Row key={`${row.id}/${row.node.uid}/${row.result!.startedAt}/${index}`} href={currentLink(row)} label={kindLabel(row.kind)} hint={`${actionNodeName(row.node)} · ${when(row.result!.startedAt)} 시작`} value={<Badge status={row.status}/>}/>)}</Rows>}
    </>}</QueryGate>
    <Rows label="더 보기">
      <Row href="/transitions?view=history" label="변경 기록" hint="구간을 골라 지나간 변경 보기"/>
      {query.data&&<RowDialog label="원본 자료와 내보내기" title="현재 변경 원본"><p>현재 API가 제공하는 변경 조작은 운영 설정 저장이에요. 수동 취소와 재시도는 없어요. <Link href="/configuration">운영 설정</Link></p><SourceExport value={{source:"GET v1/scheduler/state",range:"현재 응답의 동작과 결과",observedAt:query.data.snapshot.at,actions:query.data.actions,results:query.data.results}} filename="scheduler-transition-current.json"/><SourceJSON inline value={{actions:query.data.actions,results:query.data.results}}/></RowDialog>}
    </Rows>
  </Screen>
}

function CurrentAction({id}:{id:string}) {
  const params=useSearchParams()
  const query=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const rows=currentRows(query.data)
  const stage=params.get("stage"),node=params.get("node"),startedAt=params.get("startedAt")
  const selected=rows.filter(row=>row.id===id&&(!stage||stage===(row.result?"result":"action"))&&(node===null||row.node.uid===node)&&(startedAt===null||row.result?.startedAt===startedAt))
  const selectionRequested=stage!==null||node!==null||startedAt!==null
  const row=selected.length===1?selected[0]:undefined
  const routing=row?.result&&row.kind===routingKind?routingConclusion(row.result):undefined
  return <Screen back={["/transitions","변경 진행"]} status={row&&<Badge status={row.status}/>}
    title={!query.data?"변경 상세":routing?routing[0]:row?`${kindLabel(row.kind)}${row.result?" 결과":""}`:selected.length?"같은 동작이 여럿 있어요":"이 동작을 지금 찾을 수 없어요"}
    lede={!query.data?undefined:routing?routing[1]:row?`${actionNodeName(row.node)}에서 ${row.result?"실행한":"진행하는"} 변경이에요.`:selected.length?"노드와 시작 시각을 보고 하나를 골라 주세요.":selectionRequested?"현재 상태에 같은 동작이 없어요. 다른 결과로 대신 보여주지 않아요.":"현재 상태에 이 동작이 없어요. 변경 기록에서 찾아 주세요."}>
    <QueryGate query={query}>{query.data&&(row?<CurrentSummary row={row} rows={rows}/>:selected.length?<Rows label="고를 항목">{selected.map((item,index)=><Row key={index} href={currentLink(item)} label={item.result?"실행 결과":"현재 동작"} hint={`${actionNodeName(item.node)} · ${item.result?`${when(item.result.startedAt)} 시작`:"시작 시각 없음"}`} value={<Badge status={item.status}/>}/>)}</Rows>:<Rows><Row href="/transitions?view=history" label="변경 기록에서 찾기"/></Rows>)}</QueryGate>
  </Screen>
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
  return <Screen back={["/transitions","변경 진행"]} title={!history.query.data?"변경 기록":rows.length?`변경 동작 ${rows.length}건을 찾았어요`:"이 구간에 변경 동작이 없어요"} lede="지금 가져온 기록만 보여줘요. 구간 전체 건수는 아니에요.">
    <HistoryControls history={history}><RowDialog label="노드" value={node?nodes.get(node)??"이전 페이지에서 고른 노드":"전체 노드"} title="노드 고르기"><label className="grid gap-1.5">지금 가져온 기록의 노드<select value={node} onChange={event=>setNode(event.target.value)}><option value="">전체 노드</option>{node&&!nodes.has(node)&&<option value={node}>이전 페이지에서 고른 노드</option>}{Array.from(nodes,([uid,name])=><option key={uid} value={uid}>{name||"이름 미제공"}</option>)}</select></label></RowDialog></HistoryControls>
    <QueryGate query={history.query}>{history.query.data&&<>
      {rows.length>0&&<Rows label="변경 동작">{rows.map(row=><RowDialog key={row.ref.offset} label={kindLabel(row.event.action!.kind)} hint={`${actionNodeName(row.event.action!.node)} · ${when(row.event.at)}`} value={<Badge status={row.event.action!.phase}/>} title="변경 기록 상세"><HistoryDetails row={row} known={actions}/><Link href={historyLink(row.ref)}>이 기록만 따로 열기</Link></RowDialog>)}</Rows>}
      <HistoryPaging history={history}/>
      <Rows><RowDialog label="원본 자료와 내보내기" title="현재 페이지 원본"><p>현재 페이지 {history.rows.length}건 중 동작 {actions.length}건, 표시 {rows.length}건이에요. 구간 전체 건수나 집계가 아니에요.</p><SourceExport value={page} filename="scheduler-transition-current-page.json"/><SourceJSON inline value={page}/></RowDialog></Rows>
    </>}</QueryGate>
  </Screen>
}

function HistoryRecord({id}:{id:string}) {
  const params=useSearchParams(),ref=readSchedulerRecord(params)
  const valid=!!ref&&ref.kind==="action"&&ref.id===id
  const query=useChillQuery<SchedulerObservationPage>(schedulerRecordPath(ref),{enabled:valid,decoder:parseSchedulerJSON})
  const event=query.data?.events?.[0],matches=valid&&schedulerRecordMatches(ref,event)&&!!event?.action
  const action=matches?event!.action:undefined
  const routed=action?.kind===routingKind?result(action.actual,action):undefined,routing=routed&&routingConclusion(routed)
  return <Screen back={[historyWindow(ref?.from??params.get("from"),ref?.to??params.get("to")),"변경 기록"]} status={action&&<Badge status={action.phase}/>}
    title={routing?routing[0]:action?kindLabel(action.kind):valid&&!query.data?"변경 기록":"기록을 확인해야 해요"}
    lede={!valid?"구간, 위치와 기록 정보가 모두 필요해요. 현재 동작으로 대신 보여주지 않아요.":routing?routing[1]:action?`${actionNodeName(action.node)} · ${when(event!.at)}`:query.data?"보관 범위나 기록이 달라졌어요. 같은 동작의 다른 사건으로 대신 보여주지 않아요.":undefined}>
    {valid&&<QueryGate query={query}>{action&&ref&&<>
      <Facts items={[["노드",actionNodeName(action.node)],...(routing?[["기록 시각",when(event!.at)] as [string,string]]:[]),["관측 단계",label(action.phase)],["시작",when(action.startedAt)],["완료",when(action.completedAt)]]}/>
      {routed?<RoutingSplit result={routed}/>:action.error&&<p role="alert">실행 중 오류가 있었어요. 내용은 자세히에서 볼 수 있어요.</p>}
      <Rows><RowDialog label="변경 내용 자세히" hint="계획, 변경 전후 상태와 원본" title="변경 기록 상세"><HistoryDetails row={{event:event!,ref}}/></RowDialog></Rows>
    </>}</QueryGate>}
  </Screen>
}

export function SchedulerTransitionsPage({id}:{id?:string}) {
  const params=useSearchParams(),history=params.get("view")==="history"||params.has("offset")||params.has("kind")||params.has("at")
  return history?(id?<HistoryRecord id={id}/>:<HistoryList/>):id?<CurrentAction id={id}/>:<CurrentList/>
}
