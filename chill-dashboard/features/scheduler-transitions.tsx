"use client"

import * as React from "react"
import type { NodeRef, OperationResult, OperationState, SchedulerActionObservation, SchedulerOperation } from "@/lib/read-types"
import { workloadRoleLabel } from "@/lib/read-types"
import { nanosToMilliseconds } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { SourceJSON } from "@/components/read-region"
import { statusLabel } from "@/lib/labels"
import type { SchedulerHistoryRow } from "./scheduler-history"
import { actionNodeName, actionKinds as kinds, actionStatuses as statuses } from "./scheduler"
import { useChillQuery, useSession } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import type { SchedulerControllerState, SchedulerObservationPage, Routing } from "@/lib/read-types"
import { decisionRange } from "@/lib/experiments"
import { Facts } from "@/components/screen"
import { QueryGate, SourceExport } from "@/components/read-region"
import { useSearchParams } from "next/navigation"

const time=(value?:string)=>!value||value.startsWith("0001-")?"제공된 시각 없음":`${new Date(value).toLocaleString("ko-KR",{timeZone:"Asia/Seoul",dateStyle:"short",timeStyle:"medium"})} KST`
const label=(value:string)=>statuses[value]??statusLabel(value)

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

function Native({value}:{value:unknown}) {
  const entries=Object.entries(object(safeSource(value))??{})
  return <>{entries.length?entries.map(([id,state])=>{
    const fields=object(state),requests=Array.isArray(fields?.requests)?fields.requests:[]
    return <details key={id}><summary className="break-all">{id} 실행기 관측</summary><dl className="grid gap-2">{Object.entries(fields??{}).filter(([,value])=>value!==null&&["string","number","bigint","boolean"].includes(typeof value)).map(([key,value])=><React.Fragment key={key}><dt>{({at:"수신 관측 시각",ready:"실행 준비",acceptedRequests:"접수 요청 수",completedRequests:"완료 요청 수",executions:"실행 수"} as Record<string,string>)[key]??key}</dt><dd className="break-all">{typeof value==="boolean"?(value?"예":"아니요"):String(value)}</dd></React.Fragment>)}</dl>{requests.length>0&&<><h4>관측된 실행기 요청</h4><ul>{requests.map((request,index)=>{const row=object(request);return <li key={index}>{String(row?.phase??"단계 미제공")} / 큐 {String(row?.queueMS??"미제공")} ms / 실행 경과 {String(row?.serviceElapsedMS??"미제공")} ms</li>})}</ul></>}<SourceJSON inline value={state}/></details>
  }):<p>제공된 실행기 관측 없음</p>}</>
}

function StateDetails({state,title}:{state:OperationState;title:string}) {
  return <section className="space-y-3"><h3>{title}</h3><p>관측 {time(state.at)}</p><p>노드 <Node node={state.node}/> / 전력모드 {state.powerMode||"제공 없음"}</p><h4>관측된 상태</h4><Lines values={state.states}/><Lines values={state.reasons}/><p>CHILL이 배치하지 않은 Pod {state.pods?state.pods.filter(pod=>!pod.labels?.[workloadRoleLabel]).map(pod=>`${pod.namespace}/${pod.name}`).join(", ")||"없음":"제공된 목록 없음"}</p><h4>인스턴스</h4>{state.instances?.length?state.instances.map((instance,index)=><details key={index}><summary>{instance.id} / 준비 {instance.ready?"완료":"확인 대기"}</summary><p><Node node={instance.node}/> / 프로파일 {instance.profile} / 관측 {time(instance.observedAt)}</p>{instance.reason&&<p>{instance.reason}</p>}<ul>{instance.workloads?.map(workload=><li key={workload.uid}>{workload.name} ({workload.uid}) / 대기 {String(instance.waiting?.[workload.uid]??"미제공")} / 실행 {String(instance.running?.[workload.uid]??"미제공")} / 배정 후 확인 대기 {String(instance.assignedUnknown?.[workload.uid]??"미제공")}</li>)}</ul></details>):<p>제공된 인스턴스 없음</p>}<h4>실행기 관측</h4><Native value={state.native}/></section>
}

function OperationDetails({value}:{value:SchedulerOperation}) {
  const target=value.target
  const plans=target?.actions?.filter(action=>action.id===value.id&&action.node.uid===value.node.uid&&action.kind===value.kind&&action.instance===value.instance)??[]
  const plan=plans.length===1?plans[0]:undefined
  return <section className="space-y-3">
    <h3>실행 대상과 계획</h3>
    <p>{kinds[value.kind]??value.kind} / <Node node={value.node}/></p>
    <p>인스턴스 {value.instance||"제공 없음"}</p>
    {plan?<><p>제공된 목표 안의 계획 상태 {label(plan.status)} / 계획 소요 시간 {nanosToMilliseconds(plan.elapsedNanos)} ms</p><p>변경 전 {plan.from||"제공 없음"} / 목표 {plan.to||"제공 없음"} / 취소 가능 관측 {plan.cancelable?"예":"아니요"}</p><Dependencies ids={plan.dependsOn}/>{plan.reason&&<p>{plan.reason}</p>}</>:<p>이 실행 계획에 단일하게 일치하는 동작의 소요 시간, 취소 가능 여부와 의존 동작은 제공되지 않았습니다.</p>}
    {value.prepare&&<><p>준비 이름 {value.prepare.name} / 메모리 {String(value.prepare.memoryBytes)} bytes</p><p>준비 인스턴스 {value.prepare.instance.id} / 연결 서비스 {value.prepare.instance.workloadUID||"없음"}</p><p>CPU {value.prepare.instance.allocation.cpus?.join(", ")||"없음"}, GPU {value.prepare.instance.allocation.gpuUUID||"없음"}, 메모리 {String(value.prepare.instance.allocation.memoryBytes)} bytes</p><SourceJSON inline value={{bundle:value.prepare.bundle,store:value.prepare.store,config:value.prepare.config}}/></>}
    {value.actuation!==undefined&&<details><summary>제공된 전력 제어 계획</summary><SourceJSON inline value={value.actuation}/></details>}
    {target&&<><p>목표 {target.id} / {target.status} / 예상 에너지 {target.expectedJoules==null?"계산값 없음":`${target.expectedJoules} J`}</p><Lines values={target.reasons}/><details><summary>목표의 노드, 인스턴스와 요청 분배</summary>{target.nodes?.map((node,index)=><p key={index}><Node node={node.node}/> / 전력모드 {node.powerMode} / 준비 {node.ready?"완료":"확인 대기"}</p>)}{target.instances?.map((instance,index)=><p key={index}>{instance.id} / {instance.node.name} / CPU {instance.allocation.cpus?.join(", ")||"없음"}, GPU {instance.allocation.gpuUUID||"없음"}, 메모리 {String(instance.allocation.memoryBytes)} bytes / 서비스 {instance.workloadUID||"없음"}</p>)}<p>요청 분배는 원래 가중치이며 확률이 아닙니다.</p>{Object.entries(target.routing??{}).map(([uid,weights])=><p key={uid}>{uid}: {Object.entries(weights).map(([id,weight])=>`${id} ${weight}`).join(", ")}</p>)}</details></>}</section>
}

function RoutingReason({at,declared,cursor,visited=[]}:{at:string;declared?:Routing;cursor?:string;visited?:string[]}){
  const {config}=useSession(),range=decisionRange(at)
  const params=new URLSearchParams({...range,limit:String(config.pageSize),...(cursor?{cursor}:{})})
  const query=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${params}`,{enabled:!!range,decoder:parseSchedulerJSON})
  const change=query.data?.events?.map(event=>event.settings).find(settings=>{
    const after=settings?.afterStaticRouting
    return settings?.validatedAt===at&&declared!==undefined&&after!==undefined&&Object.keys(after).length===Object.keys(declared).length&&Object.entries(declared).every(([uid,weights])=>Object.hasOwn(after,uid)&&Object.keys(after[uid]).length===Object.keys(weights).length&&Object.entries(weights).every(([id,weight])=>after[uid][id]===weight))
  })
  const next=query.data?.nextCursor
  if(!change&&next&&!visited.includes(next))return <RoutingReason at={at} declared={declared} cursor={next} visited={[...visited,next]}/>
  if(!range)return <p role="alert">설정 변경 시각 미확인</p>
  return <QueryGate query={query}>{query.data&&<>
    <Facts items={[["선택 이유",change?.reason??"미기록"],["행위자",change?.actorUsername??"미기록"],["설정 변경 시각",change?time(change.validatedAt):"미기록"]]}/>
    {change&&<SourceJSON inline title="일치하는 설정 변경 원본" value={change}/>}
  </>}</QueryGate>
}

function RoutingSplit({value}:{value:OperationResult}){
  const declared=value.operation.staticRouting,actual=value.actual.routing
  const services=[...new Set([...Object.keys(declared??{}),...Object.keys(actual??{})])]
  const share=(routing:Routing|undefined,uid:string,id:string,missing:string)=>{
    if(!routing)return missing
    const weights=routing[uid]??{},total=Object.values(weights).reduce((sum,weight)=>sum+weight,0)
    return `${total?((weights[id]??0)/total*100).toLocaleString("ko-KR",{maximumFractionDigits:1}):0}%`
  }
  return <section aria-label="설정·실제 요청 분배" className="space-y-3">
    <h3>서비스·인스턴스 분배</h3>
    <div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>서비스 UID</th><th>인스턴스</th><th>설정한 비율</th><th>실제 비율</th></tr></thead><tbody>{services.flatMap(uid=>[...new Set([...Object.keys(declared?.[uid]??{}),...Object.keys(actual?.[uid]??{})])].map(id=><tr key={`${uid}/${id}`}><td className="break-all">{uid}</td><td className="break-all">{id}</td><td className="text-right tabular-nums">{share(declared,uid,id,"미기록")}</td><td className="text-right tabular-nums">{share(actual,uid,id,"미확인")}</td></tr>))}</tbody></table></div>
    {!services.length&&<p>분배 기록 없음</p>}
    {value.operation.settingsValidatedAt?<RoutingReason at={value.operation.settingsValidatedAt} declared={declared}/>:<p>설정 변경 시각 미기록</p>}
  </section>
}

function ResultDetails({value}:{value:OperationResult}) {
  return <><p role="status">관측 결과 {label(value.status)}</p><p>시작 {time(value.startedAt)} / 종료 {time(value.endedAt)}</p>{value.error&&<p role="alert" className="break-all">{value.error}</p>}{value.operation.kind==="manual-static-routing"&&<RoutingSplit value={value}/>}<h3>제공된 진행 기록</h3><Lines values={value.progress}/><OperationDetails value={value.operation}/><StateDetails title="변경 전 관측" state={value.before}/><StateDetails title="실제 변경 후 관측" state={value.actual}/>{value.drainNative&&<section><h3>기존 요청 마무리 관측</h3><Native value={value.drainNative}/></section>}</>
}

function Dependencies({ids}:{ids:string[]|null}) {
  return <section><h3>선행 동작</h3>{ids?.length?<ul>{ids.map(id=><li key={id}>{id}</li>)}</ul>:<p>기록 없음</p>}</section>
}
export function HistoryActionDetails({row}:{row:SchedulerHistoryRow}) {
  const action=row.event.action!
  const expected=operation(action.expected),actual=result(action.actual,action)
  const validExpected=expected?.id===action.id&&expected.node.uid===action.node.uid
  return <section className="space-y-4"><p>사건 {time(row.event.at)} / 동작 {action.id} / <Node node={action.node}/></p><p>{kinds[action.kind]??action.kind} / 관측 단계 {label(action.phase)} / 인스턴스 {action.instance||"제공 없음"}</p><p>시작 {time(action.startedAt)} / 완료 {time(action.completedAt)}</p>{action.error&&<p role="alert" className="break-all">{action.error}</p>}{actual?<ResultDetails value={actual}/>:<>{validExpected?<OperationDetails value={expected}/>:<p>표시할 실행 계획 필드가 제공되지 않았습니다. 제공된 내용은 원본에서 확인합니다.</p>}<p>이 사건에서 구조화된 변경 전후와 진행 결과를 확인하지 못했습니다. 다른 시점의 결과로 대체하지 않습니다.</p></>}{action.evidence?.length?<details><summary>제공된 근거</summary><SourceJSON inline value={action.evidence}/></details>:null}</section>
}

export function CurrentTransitionEvidence(){
  const params=useSearchParams(),query=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON})
  const id=params.get("id"),node=params.get("node"),startedAt=params.get("startedAt"),stage=params.get("stage")
  const actions=stage==="result"?[]:(query.data?.actions??[]).filter(row=>row.id===id&&(!node||row.node.uid===node))
  const results=stage==="action"?[]:(query.data?.results??[]).filter(row=>row.operation.id===id&&(!node||row.operation.node.uid===node)&&(!startedAt||row.startedAt===startedAt))
  const selected=actions.length+results.length===1?actions[0]??results[0]:undefined
  return <QueryGate query={query}>{query.data&&(selected?<><h3>현재 변경 근거</h3>{results[0]?<ResultDetails value={results[0]}/>:<SourceJSON inline value={actions[0]}/>}<SourceExport value={selected} filename="scheduler-transition-current.json"/></>:<p role="alert">{actions.length+results.length?"동작 선택 필요":"현재 동작 미확인"}</p>)}</QueryGate>
}
