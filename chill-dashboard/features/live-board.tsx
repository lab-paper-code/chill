"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import { useLive } from "@/lib/use-live"
import type { LiveEvent, LiveInstance, LiveMetric, LiveMetrics, LiveSnapshot, LiveState, LiveWorkload } from "@/lib/live"
import type { SchedulerObservation, SchedulerObservationPage, SchedulerView } from "@/lib/read-types"
import { decisionRange, evidenceError } from "@/lib/experiments"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { belongsToPool, displayNumber, displayTime, elapsed, instanceSymbol, instanceSymbols, isStale, ratio, receivedLabel, routingShare, terminalCount, workloadLabel } from "@/lib/live-display"
import { CandidateDetails } from "./scheduler"
import "./live-board.css"

type Zone="UTC"|"KST"
type Select=(key:"open"|"at",value:string|null,opener?:HTMLElement)=>void
const activeAction=(action:{status:string})=>action.status==="starting"||action.status==="running"
const raw=(value:unknown)=>stringifySchedulerJSON(safeSource(value),2)
function Facts({items}:{items:[string,React.ReactNode][]}) {
  return <dl className="live-facts">{items.map(([label,value])=><div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
}
function Arrow({label}:{label:string}) {
  return <div className="live-arrow"><span>{label}</span><svg viewBox="0 0 90 16" aria-hidden="true"><path d="M0 8H85M78 1L85 8L78 15" fill="none" stroke="currentColor" strokeWidth="1.5"/></svg></div>
}
function StateStrip({state,status,reconnect,now,zone,setZone}:{state:LiveState;status:string;reconnect:()=>void;now:number;zone:Zone;setZone:(zone:Zone)=>void}) {
  const params=useSearchParams(),router=useRouter(),path=usePathname(),{config}=useSession()
  const fixture=status==="fixture",stale=isStale(state.lastObservedAt,now)
  const connection:Record<string,string>={connecting:"연결 중",resuming:"재개 중",live:"연결됨",reconnecting:"재연결 중",disconnected:"연결 끊김",unauthorized:"인증 필요",forbidden:"권한 없음","protocol-error":"메시지 계약 오류",idle:"연결 대기",fixture:"검사용"}
  return <section className="live-strip" aria-label="라이브 상태">
    <div><strong>{fixture?"검사용 fixture":status!=="live"?"연결 끊김 · 재개 대기":!state.snapshot?"관측 대기":stale?"관측 오래됨":"실시간"}</strong><span>{connection[status]??status}</span><span>단계 {fixture?params.get("fixture"):"미지정"}</span></div>
    <div className="live-autonomous">{state.snapshot?.workloads.map(workload=><span key={workload.uid} title={workload.uid}>{workload.name} · 자동제어 {workload.autonomous?"켜짐":"꺼짐 · 마지막 구성 유지"}</span>)}</div>
    <div><span title={state.lastReceivedAt==null?undefined:new Date(state.lastReceivedAt).toISOString()}>마지막 수신 {state.lastReceivedAt==null?"":displayTime(new Date(state.lastReceivedAt).toISOString(),zone)} {elapsed(state.lastReceivedAt,now)}</span><span title={state.lastObservedAt??undefined}>마지막 관측 {displayTime(state.lastObservedAt,zone)} {elapsed(state.lastObservedAt,now)} {stale?"· 오래된 관측":""}</span></div>
    <div><span title={state.metrics.at(-1)?.bucketAt}>마지막 완료 버킷 {displayTime(state.metrics.at(-1)?.bucketAt,zone)} {elapsed(state.metrics.at(-1)?.bucketAt,now)}</span></div>
    <div><button type="button" onClick={()=>setZone(zone==="UTC"?"KST":"UTC")} aria-label={`시간대 ${zone}, 전환`}>{zone} 전환</button>{status!=="live"&&!fixture&&<button type="button" onClick={reconnect}>다시 연결</button>}
      {config.liveFixture&&<label>검사용 장면 <select value={params.get("fixture")??"live"} onChange={event=>{const next=new URLSearchParams(params.toString());next.delete("open");next.delete("at");if(event.target.value==="live")next.delete("fixture");else next.set("fixture",event.target.value);router.replace(`${path}?${next}`,{scroll:false})}}><option value="live">실시간</option><option value="empty">빈 상태</option><option value="first">첫 구성</option><option value="expand">확장</option><option value="shrink">축소</option></select></label>}
    </div>
  </section>
}
function InstanceMark({instance,uid}:{instance:LiveInstance;uid:string}) {
  const [symbol,label]=instanceSymbol(instance,uid)
  return <span className={`live-instance live-symbol-${instanceSymbols.findIndex(row=>row[0]===symbol)}`} title={`${instance.id} · ${label}`}><span aria-hidden="true">{symbol}</span><span className="sr-only">{instance.id}: {label}</span></span>
}
function NodeGroups({snapshot,workload,select,detail=false}:{snapshot:LiveSnapshot;workload:LiveWorkload;select:Select;detail?:boolean}) {
  const rows=snapshot.instances.filter(instance=>belongsToPool(instance,workload.uid)),groups=new Map<string,LiveInstance[]>()
  for(const instance of rows)groups.set(instance.node.uid,[...(groups.get(instance.node.uid)??[]),instance])
  if(!rows.length)return <p className="live-muted">(인스턴스 없음)</p>
  return <div className="live-node-groups">{[...groups].map(([uid,instances])=>{
    const node=snapshot.nodes.find(node=>node.uid===uid),actions=snapshot.actions.filter(action=>activeAction(action)&&action.node.uid===uid&&(!action.workloadUID||action.workloadUID===workload.uid))
    return <section className={`live-node ${node?.ready===false?"live-node-unavailable":""}`} key={uid}>
      {detail?<button type="button" onClick={event=>select("open",`node:${uid}`,event.currentTarget)}>{instances[0].node.name} · Node 펼치기</button>:<span className="live-muted">{instances[0].node.name}</span>}
      {node?.ready===false&&<span>사용 불가</span>}{node?.measurement&&<span>측정 중</span>}{!node&&<span className="live-muted">전력모드 미확인</span>}
      {actions.map(action=><span className="live-action-badge" key={JSON.stringify([action.id,uid,action.startedAt])}>{action.kind} 진행</span>)}
      <div className="live-instances">{instances.map(instance=>detail?<button key={instance.id} type="button" onClick={event=>select("open",`inst:${instance.id}`,event.currentTarget)}><InstanceMark instance={instance} uid={workload.uid}/><span>{instance.id}</span><span>분배 {displayNumber(routingShare(snapshot.routing[workload.uid],instance.id),"%",1)} · running {instance.running[workload.uid]??""} · 배정 뒤 native waiting {instance.waiting[workload.uid]??""}</span></button>:<InstanceMark key={instance.id} instance={instance} uid={workload.uid}/>)}</div>
    </section>
  })}</div>
}
function EventLane({events,select,zone,start,end}:{events:LiveEvent[];select:Select;zone:Zone;start:number;end:number}) {
  const visible=events.filter(event=>Date.parse(event.at)>=start&&Date.parse(event.at)<=end)
  return <section className="live-events" aria-label="사건 레인"><h3>사건 <small>◆ 판단 · ◇ 작업 · △ 클러스터</small></h3>
    <div className="live-event-track" aria-hidden="true">{visible.map(event=><span key={event.seq} style={{left:`${(Date.parse(event.at)-start)/(end-start)*100}%`}}>{event.kind==="decision"?"◆":event.kind==="action"?"◇":"△"}</span>)}</div>
    {visible.length?<ol>{[...visible].reverse().map(event=><li key={event.seq}>
      <time dateTime={event.at} title={event.at}>{displayTime(event.at,zone)}</time>{event.kind==="decision"&&event.decision?<button type="button" onClick={e=>select("at",event.decision!.at,e.currentTarget)}>◆ {event.decision.state} · {event.decision.reason}</button>:event.kind==="action"?<span>◇ {event.action?.kind} · {event.action?.phase} · {event.action?.node.name} {event.action?.result?.error}</span>:<span>△ {event.cluster?.kind} · {event.cluster?.event} · {event.cluster?.pod?.name} {event.cluster?.pod?.phase} {event.cluster?.pod?.ready} {event.cluster?.source} {event.cluster?.reason}{event.cluster?.previousReason?` · 이전: ${event.cluster.previousReason}`:""}</span>}
    </li>)}</ol>:<p className="live-muted">받은 사건 없음</p>}
  </section>
}
function Controls({snapshot,metrics,select,zone}:{snapshot:LiveSnapshot|null;metrics:LiveMetrics|undefined;select:Select;zone:Zone}) {
  const actions=snapshot?.actions.filter(activeAction),decision=snapshot?.decision
  return <section className="live-controls" aria-label="CHILL 제어 흐름">
    <div><h3>관측</h3><p>전체 POOL {displayNumber(metrics?.pool.wholeWatts," W",1)}</p><p>활성 POOL {displayNumber(metrics?.pool.activeWatts," W",1)}</p><small>공유 전력 · 레인 합산 금지</small></div><Arrow label="C1"/>
    <div><h3>판단</h3>{decision?<button type="button" onClick={event=>select("at",decision.at,event.currentTarget)}><time title={decision.at}>{displayTime(decision.at,zone)}</time><strong>{decision.state}</strong><span>{decision.reason==="CapacityPending"?"용량 측정 근거를 기다리고 있어요":decision.reason==="TransitionInProgress"?"목표로 전환 중이에요":decision.reason}</span></button>:<p className="live-muted">판단 기록 없음</p>}</div><Arrow label="C2"/>
    <div><h3>작업 {actions?`${actions.length} 진행`:""}</h3>{actions?.map(action=><p key={JSON.stringify([action.id,action.node.uid,action.startedAt])}>{action.kind} · {action.node.name}<small>{action.expectedSeconds==null?"예상 시간 없음":`예상 ${action.expectedSeconds}초`}{action.expectedSource?` · ${action.expectedSource.costID} / ${raw(action.expectedSource.reference)} / ${action.expectedSource.statistic} / ${action.expectedSource.trialCount}회`:""}</small></p>)}</div><Arrow label="C3 / C4"/>
    <div><h3>예약</h3><button type="button" onClick={event=>select("open","reservations",event.currentTarget)}>예약 {snapshot?.reservations.length??""} 펼치기</button><small>판단 주기마다 갱신</small></div>
  </section>
}
function MetricCard({workload,metric,instances,now}:{workload:LiveWorkload;metric:LiveMetric|undefined;instances:LiveInstance[];now:number}) {
  const n=terminalCount(metric),values=metric?[metric.withinSLO,metric.completed-metric.withinSLO,metric.failed]:[]
  return <section className="live-metric-card" aria-label="POOL 완료된 1초 버킷 지표">
    <h2>{workloadLabel(workload,instances,metric,now)}</h2><Facts items={[["수신",receivedLabel(metric)],["성공 completed / N",ratio(metric?.completed,n)],["SLO 안 withinSLO / N",ratio(metric?.withinSLO,n)],["종결 N = completed + failed",metric?`${metric.completed} + ${metric.failed} = ${n}`:""],["p95",n?displayNumber(metric?.p95ms," ms"):""],["p99",n?displayNumber(metric?.p99ms," ms"):""],["적용 SLO",workload.slo?`p${workload.slo.percentile} ≤ ${displayNumber(Number(workload.slo.targetLatencyNanos)/1e6," ms")}`:""],["지연 표본",metric?.latencySamples],["접수 가능",workload.admission?"가능":"불가"],["배정 전 pending",workload.pending]]}/>
    <p className="live-muted">성공률은 응답 종결 집단 기준입니다. 수신 초의 요청과 다른 집단이며 실패도 유효 종단 지연 표본에 포함합니다.</p>
    <div className="live-outcomes" role="img" aria-label={metric?`SLO 안 ${values[0]}, SLO 밖 ${values[1]}, 실패 ${values[2]}, 종결 ${n}`:"종결 관측 없음"}>{n!=null&&n>0&&values.map((value,index)=><span key={index} className={`live-outcome-${index}`} style={{width:`${value/n*100}%`}}/>)}</div>
    <p>{metric?`■ SLO 안 ${values[0]} · ▨ SLO 밖 ${values[1]} · × 실패 ${values[2]}${n===0?" · 비율 없음":""}`:""}</p>
  </section>
}

type Series={label:string;color:string;read:(metric:LiveMetrics)=>number|null|undefined;dashed?:boolean}
function TimeLane({title,metrics,series,start,end,gaps,zone}:{title:string;metrics:LiveMetrics[];series:Series[];start:number;end:number;gaps:LiveState["gaps"];zone:Zone}) {
  const maxima=series.map(row=>Math.max(1,...metrics.map(metric=>row.read(metric)??0)))
  const hatch=React.useId()
  return <section className="live-time-lane"><h3>{title}</h3><p className="live-chart-legend">{series.map((row,index)=><span key={row.label} style={{color:row.color}}>{row.dashed?"◇":"●"} {row.label} <small>축 상한 {displayNumber(maxima[index])}</small></span>)}</p>
    <svg viewBox="0 0 1000 110" role="img" aria-label={`${title}, 받은 버킷만 표시. 각 계열 독립 축, 공백 연결 없음`} preserveAspectRatio="none">
      <defs><pattern id={hatch} width="8" height="8" patternUnits="userSpaceOnUse"><path d="M-2 2L2-2M0 8L8 0M6 10L10 6" stroke="#9aa6b0" strokeWidth="1"/></pattern></defs>
      <line x1="0" y1="100" x2="1000" y2="100" stroke="#68717a"/>
      {gaps.filter(gap=>Date.parse(gap.to)>=start&&Date.parse(gap.from)<=end).map((gap,index)=><rect key={index} x={Math.max(0,(Date.parse(gap.from)-start)/(end-start)*1000)} width={Math.max(2,(Math.min(end,Date.parse(gap.to))-Math.max(start,Date.parse(gap.from)))/(end-start)*1000)} y="0" height="100" fill={`url(#${hatch})`} opacity=".5"><title>{gap.scope}: {gap.reason}</title></rect>)}
      {series.map((row,index)=><g key={row.label} fill={row.color}>{metrics.map(metric=>{const value=row.read(metric);if(value==null)return null;const x=(Date.parse(metric.bucketAt)-start)/(end-start)*1000,y=96-value/maxima[index]*86;return row.dashed?<path key={metric.bucketAt} d={`M${x} ${y-3}l3 3 -3 3 -3 -3z`}><title>{metric.bucketAt} {row.label}: {value}</title></path>:<circle key={metric.bucketAt} cx={x} cy={y} r="2.5"><title>{metric.bucketAt} {row.label}: {value}</title></circle>})}</g>)}
    </svg><div className="live-chart-axis"><span>{displayTime(new Date(start).toISOString(),zone)}</span><span>{displayTime(new Date(end).toISOString(),zone)}</span></div>
    <details><summary>{title} 받은 버킷 값</summary><div className="live-table-wrap"><table><thead><tr><th>원본 bucketAt</th>{series.map(row=><th key={row.label}>{row.label}</th>)}</tr></thead><tbody>{metrics.map(metric=><tr key={metric.bucketAt}><th>{metric.bucketAt}</th>{series.map(row=><td key={row.label}>{displayNumber(row.read(metric),"",2)}</td>)}</tr>)}</tbody></table></div></details>
  </section>
}
function Timeline({state,uid,zone,select,end}:{state:LiveState;uid:string;zone:Zone;select:Select;end:number}) {
  const start=end-600000,metrics=state.metrics.filter(metric=>Date.parse(metric.bucketAt)>=start&&Date.parse(metric.bucketAt)<=end)
  const percentage=(metric:LiveMetrics,key:"completed"|"withinSLO")=>{const row=metric.workloads[uid],n=terminalCount(row);return row&&n?row[key]/n*100:null}
  const slo=state.snapshot?.workloads.find(row=>row.uid===uid)?.slo
  const requestGaps=state.gaps.filter(gap=>gap.scope==="all"||gap.scope===`workload:${uid}`)
  const powerGaps=state.gaps.filter(gap=>gap.scope==="all"||gap.scope==="power"||gap.scope.startsWith("node:"))
  return <section className="live-timeline"><h2>최근 10분 · 받은 구간만</h2><p className="live-muted">접속 이전·재동기화·계측 결손은 공백입니다. 점은 완료된 1초 버킷이며 공백을 선으로 잇지 않습니다.</p>
    <p className="live-muted">현재 적용 SLO: {slo?`p${slo.percentile} ≤ ${displayNumber(Number(slo.targetLatencyNanos)/1e6," ms")}`:""} · 과거 버킷의 정의를 현재 정의로 대체하지 않습니다.</p>
    <TimeLane title="부하" metrics={metrics} start={start} end={end} gaps={requestGaps} zone={zone} series={[{label:"수신 req/s",color:"#a5d6ee",read:metric=>metric.workloads[uid]?.received}]}/>
    <TimeLane title="성공률 · 지연 / SLO" metrics={metrics} start={start} end={end} gaps={requestGaps} zone={zone} series={[{label:"성공 %",color:"#9fdbbc",read:metric=>percentage(metric,"completed")},{label:"SLO 안 %",color:"#dcc58b",read:metric=>percentage(metric,"withinSLO"),dashed:true},{label:"p95 ms",color:"#b6b4ef",read:metric=>terminalCount(metric.workloads[uid])?metric.workloads[uid]?.p95ms:null},{label:"p99 ms",color:"#edaeab",read:metric=>terminalCount(metric.workloads[uid])?metric.workloads[uid]?.p99ms:null,dashed:true}]}/>
    <TimeLane title="공유 POOL 전력" metrics={metrics} start={start} end={end} gaps={powerGaps} zone={zone} series={[{label:"전체 W",color:"#a5d6ee",read:metric=>metric.pool.wholeWatts},{label:"활성 W",color:"#dcc58b",read:metric=>metric.pool.activeWatts,dashed:true}]}/>
    <EventLane events={state.events} select={select} zone={zone} start={start} end={end}/>
  </section>
}
function Expansion({value,snapshot,metrics,uid,zone,close}:{value:string;snapshot:LiveSnapshot|null;metrics:LiveMetrics|undefined;uid?:string;zone:Zone;close:()=>void}) {
  const instance=value.startsWith("inst:")?snapshot?.instances.find(row=>row.id===value.slice(5)):undefined,node=value.startsWith("node:")?snapshot?.nodes.find(row=>row.uid===value.slice(5)):undefined
  return <section className="live-expansion" aria-label="선택 상세"><header><h2>{value==="reservations"?"예약 장부":instance?`인스턴스 ${instance.id}`:node?`Node ${node.name}`:"선택 상세"}</h2><button type="button" onClick={close}>닫기</button></header>
    {value==="reservations"?<><p>판단 주기마다 갱신 · 작업 종료로 해제를 추정하지 않습니다.</p>{snapshot?.reservations.length?snapshot.reservations.map((reservation,index)=><Facts key={index} items={[["owner",reservation.owner],["Node",reservation.nodes.map(row=>`${row.name} (${row.uid})`).join(", ")],["워크로드 UID",reservation.workloadUIDs.join(", ")],["Pod UID",reservation.podUIDs.join(", ")],["인스턴스",reservation.instances.join(", ")],["유지 사유",reservation.reason||"미확인"]]}/>):<p>{snapshot?"예약 해제 · 현재 예약 없음":"예약 미제공"}</p>}</>:instance?<><Facts items={[["프로파일",instance.profileID],["owner UID",instance.ownerWorkloadUID],["관측",<time key="time" title={instance.observedAt}>{displayTime(instance.observedAt,zone)}</time>],["ready",String(instance.ready)],["stopped",String(instance.stopped)]]}/>{(uid?[uid]:Object.keys(instance.routable)).map(key=><Facts key={key} items={[["POOL UID",key],["상태",instanceSymbol(instance,key).join(" ")],["분배",displayNumber(routingShare(snapshot?.routing[key],instance.id),"%",1)],["원본 가중치",snapshot?.routing[key]?.[instance.id]],["routable",instance.routable[key]==null?"":String(instance.routable[key])],["실제 running",instance.running[key]],["배정 뒤 native waiting",instance.waiting[key]]]} />)}<p>배정 뒤 native waiting은 queued/deferred입니다. 배정 전 pending 및 assignedUnknown과 합치지 않습니다.</p></>:node?<Facts items={[["UID",node.uid],["전력모드",node.powerMode],["전력",displayNumber(metrics?.nodes[node.uid]?.watts," W",1)],["전력 관측",displayTime(metrics?.nodes[node.uid]?.observedAt,zone)],["ready (서버 판정)",String(node.ready)],["measurement",String(node.measurement)],["사유",node.reasons?.join(" / ")],["관측",displayTime(node.observedAt,zone)]]}/>:<p>현재 snapshot에 선택한 항목이 없습니다. 다른 항목으로 바꾸지 않습니다.</p>}
  </section>
}
function Evidence({at,close}:{at:string;close:()=>void}) {
  const range=decisionRange(at),[page,setPage]=React.useState<{cursor?:string;visited:string[]}>({visited:[]}),{cursor}=page
  const path=`v1/observations/scheduler?${new URLSearchParams({...range,limit:"100",...(cursor?{cursor}:{})})}`
  const query=useChillQuery<SchedulerObservationPage>(path,{enabled:!!range,decoder:parseSchedulerJSON})
  const match=query.data?.events?.find(event=>event.decision?.at===at)
  const next=query.data?.nextCursor
  if(!match&&next&&!page.visited.includes(next))setPage({cursor:next,visited:[...page.visited,next]})
  const event=match,decision=event?.decision,view:SchedulerView|undefined=decision&&event?.snapshot?{settings:decision.settings,state:{snapshot:event.snapshot,decision,actions:event.snapshot.actions,results:null}}:undefined
  const occupancy=(event?.snapshot as (NonNullable<SchedulerObservation["snapshot"]>&{dispatchOccupancy?:{node:{uid:string;name:string};powerMode:string;runtimeImage:string;model:string;perRequestNanos:number|bigint;source:string}[]})|undefined)?.dispatchOccupancy
  const exhausted=!!query.data&&!match&&(!next||page.visited.includes(next))
  return <aside className="live-evidence" aria-labelledby="live-evidence-title"><header><h2 id="live-evidence-title">선택 판단 근거</h2><button type="button" onClick={close}>닫기 · Esc</button></header><p className="live-muted">새 판단이 와도 선택은 바뀌지 않습니다.</p><p className="live-original-time">{at}</p>
    {!range?<p role="alert">올바른 원본 판단 시각이 아닙니다.</p>:query.error?<p role="alert">{evidenceError(query.error)}</p>:!event?<p role="status">{exhausted?"기록 없음":"선택 시각의 근거 조회 중"}</p>:decision&&<><h3>{decision.status}</h3><p>{decision.reasons?.join(" / ")}</p>
      <h3>수요 · 최소 수요 · κ</h3>{decision.capacity?.workloads?.map(row=><Facts key={row.uid} items={[["UID",row.uid],["수요",row.rate],["최소 수요",row.minimumDemand],["κ 신규 / 유지",`${row.kappaNew} / ${row.kappaRetained}`],["대기",row.queued],["backlog",row.backlog],["대기 (초)",row.waitSeconds],["drain (초)",row.drainSeconds]]}/>)}
      <Facts items={[["유지안 예상 에너지",displayNumber(decision.keep.expectedJoules==null?null:decision.keep.expectedJoules/1000," kJ",3)],["변경안 예상 에너지",displayNumber(decision.target?.expectedJoules==null?null:decision.target.expectedJoules/1000," kJ",3)]]}/>
      <h3>분배 점유시간 d · 판단 여유 B</h3><Facts items={[["판단 여유 B",displayNumber(decision.capacity?.gainUncertaintyJoules==null?null:decision.capacity.gainUncertaintyJoules/1000," kJ",3)]]}/>{occupancy?.map((row,index)=><Facts key={index} items={[["Node",`${row.node.name} (${row.node.uid})`],["전력모드",row.powerMode],["모델 / runtime",`${row.model} / ${row.runtimeImage}`],["d / 요청",displayNumber(Number(row.perRequestNanos)/1e6," ms")],["출처",row.source]]}/>)}
      {view&&[decision.keep,decision.target].filter(candidate=>candidate!=null).map((candidate,index)=><section className="live-candidate" key={index}><CandidateDetails candidate={candidate} view={view}/></section>)}
      {!view&&<p>짝지어진 snapshot 미제공 · 현재 구성으로 대체하지 않습니다.</p>}
      <details><summary>판단 원본 JSON</summary><pre>{raw(event)}</pre></details>
    </>}
  </aside>
}
function LiveStage({uid}:{uid?:string}) {
  const {state,status,reconnect}=useLive(),params=useSearchParams(),router=useRouter(),path=usePathname(),[now,setNow]=React.useState(()=>Date.now()),[zone,setZone]=React.useState<Zone>("UTC")
  const heading=React.useRef<HTMLHeadingElement>(null),openers=React.useRef<Partial<Record<"open"|"at",HTMLElement>>>({}),open=params.get("open"),at=params.get("at")
  const previousSelection=React.useRef({open,at})
  const snapshot=state.snapshot,metrics=state.metrics.at(-1),workload=snapshot?.workloads.find(row=>row.uid===uid),fixture=params.get("fixture")
  const end=status==="fixture"&&metrics?Date.parse(metrics.bucketAt)+1000:now,start=end-600000
  React.useEffect(()=>{const timer=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(timer)},[])
  const select:Select=React.useCallback((key,value,opener)=>{if(opener)openers.current[key]=opener;const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);router.push(`${path}?${next}`,{scroll:false})},[params,path,router])
  React.useEffect(()=>{
    const previous=previousSelection.current
    previousSelection.current={open,at}
    const closed=previous.at&&!at?"at":previous.open&&!open?"open":null
    if(closed){const target=openers.current[closed];if(target?.isConnected)target.focus();else heading.current?.focus()}
  },[open,at])
  React.useEffect(()=>{const escape=(event:KeyboardEvent)=>{if(event.key==="Escape"){if(at)select("at",null);else if(open)select("open",null)}};document.addEventListener("keydown",escape);return()=>document.removeEventListener("keydown",escape)},[at,open,select])
  React.useEffect(()=>{if(uid)return;const focus=()=>{const id=decodeURIComponent(window.location.hash.slice(1));if(id.startsWith("pool-"))document.getElementById(id)?.focus()};focus();window.addEventListener("hashchange",focus);return()=>window.removeEventListener("hashchange",focus)},[uid,snapshot?.workloads.length])
  const poolHref=(id:string)=>`/pools/${encodeURIComponent(id)}${fixture?`?${new URLSearchParams({fixture})}`:""}`
  const workloadRows=[...(snapshot?.workloads??[])].sort((a,b)=>a.name.localeCompare(b.name)||a.uid.localeCompare(b.uid))
  const boardMetrics=metrics&&workloadRows.length>0&&workloadRows.every(row=>metrics.workloads[row.uid])?workloadRows.map(row=>metrics.workloads[row.uid]):null
  const received=boardMetrics?.reduce((sum,row)=>sum+row.received,0),completed=boardMetrics?.reduce((sum,row)=>sum+row.completed,0),failed=boardMetrics?.reduce((sum,row)=>sum+row.failed,0),withinSLO=boardMetrics?.reduce((sum,row)=>sum+row.withinSLO,0)
  const totalN=completed==null||failed==null?undefined:completed+failed
  return <div className="live-stage"><StateStrip state={state} status={status} reconnect={reconnect} now={now} zone={zone} setZone={setZone}/><div className={`live-layout ${at?"live-with-evidence":""}`}><main className="live-main">
    <header className="live-heading">{uid&&<Link href={`/${fixture?`?${new URLSearchParams({fixture})}`:""}#pool-${encodeURIComponent(uid)}`}>◂ 보드로</Link>}<h1 ref={heading} tabIndex={-1}>{uid?`POOL ${workload?.name??uid}`:"CHILL 흐름 보드"}</h1><p>{uid?`${uid}${workload?` · 모델 ${workload.model}`:""}`:"수신에서 실행으로, 관측에서 구성으로"}</p></header>
    {!uid&&<p><Link href="/measurements">연구 진척: 측정 커버리지와 최근 실험 결론 보기</Link></p>}
    {uid?<>{workload?<><MetricCard workload={workload} metric={metrics?.workloads[uid]} instances={snapshot!.instances} now={now}/><section className="live-pool-instances"><h2>인스턴스 · Node</h2><NodeGroups snapshot={snapshot!} workload={workload} select={select} detail/></section></>:<p>현재 snapshot에 이 워크로드 UID가 없습니다.</p>}<Controls snapshot={snapshot} metrics={metrics} select={select} zone={zone}/>{open&&<Expansion value={open} snapshot={snapshot} metrics={metrics} uid={uid} zone={zone} close={()=>select("open",null)}/>}<Timeline state={state} uid={uid} zone={zone} select={select} end={end}/></>:<>
      <section className="live-flow" aria-label="CHILL 고정 구조 흐름"><div className="live-client"><h2>클라이언트</h2><p>부하 생성기</p></div><Arrow label={`E1 수신 ${displayNumber(received," req/s")}`}/><section className="live-server"><h2>ChillServer</h2><div className="live-intake">접수 · 유입 큐 · 분배</div><div className="live-lanes">
        {workloadRows.length?workloadRows.map(row=>{const metric=metrics?.workloads[row.uid];return <section className="live-pool-lane" key={row.uid} id={`pool-${row.uid}`} tabIndex={-1}><header><Link href={poolHref(row.uid)}><h3>POOL {row.name}</h3><span>열기 ▸</span></Link><strong>{workloadLabel(row,snapshot!.instances,metric,now)}</strong></header><div className="live-lane-values"><span>E1 {receivedLabel(metric)}</span><span>E4 성공 {ratio(metric?.completed,terminalCount(metric))}</span></div><p className="live-muted">E2 접수 {row.admission?"가능":"불가"} · 배정 전 pending {row.pending}</p><NodeGroups snapshot={snapshot!} workload={row} select={select}/></section>}):<p className="live-empty">아직 POOL 구성이 없습니다. 워크로드가 들어오면 UID별 경로가 여기에 나타납니다.</p>}
      </div></section><Arrow label={`E4 종결 ${displayNumber(totalN,"/s")}`}/><div className="live-response"><h2>응답</h2><p>SLO 안 {ratio(withinSLO,totalN)}</p></div></section>
      <div className="live-symbol-legend" aria-label="인스턴스 기호">{instanceSymbols.map(([symbol,label])=><span key={symbol}>{symbol} {label}</span>)}</div><Controls snapshot={snapshot} metrics={metrics} select={select} zone={zone}/>{open&&<Expansion value={open} snapshot={snapshot} metrics={metrics} zone={zone} close={()=>select("open",null)}/>}<EventLane events={state.events} select={select} zone={zone} start={start} end={end}/>
    </>}
    {!!state.gaps.length&&<section className="live-gaps" aria-label="관측 공백"><h2>관측 공백</h2>{state.gaps.map((gap,index)=><p key={index}>{gap.from} ~ {gap.to} · {gap.scope}: {gap.reason}</p>)}</section>}
  </main>{at&&<Evidence key={at} at={at} close={()=>select("at",null)}/>}</div></div>
}
export function LiveBoardPage(){return <LiveStage/>}
export function LivePoolPage({id}:{id:string}){return <LiveStage uid={id}/>}
