"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import { useLive } from "@/lib/use-live"
import type { LiveAction, LiveEvent, LiveMetrics, LiveSnapshot, LiveState, LiveWorkload, RecordedLivePage } from "@/lib/live"
import { eventKey } from "@/lib/live"
import type { SchedulerCandidate, SchedulerDecision, SchedulerObservationPage } from "@/lib/read-types"
import { decisionRange, evidenceError } from "@/lib/scheduler-history"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { actionProgress, actionOrigin, duration, liveFresh, scopeSnapshot, belongsToPool, boardVerdict, displayNumber, displayTime, elapsed, instanceSymbol, routingShare, serviceDevices, serviceRows, gapEpisodes, liveReason, terminalCount, trendSegments, workloadQueue, incidentSummary, sloLatency, minuteMetrics, sloTargetMissed, incidentHistoryHref, incidentDiagnosis, reservationReason, deviceAlerts, workloadReadiness, candidatePlans, candidateEvaluation, decisionActivity, clusterResult, isMeasurementPrecheck, isActionFailure, actionEventResult, retentionNotice, type ServiceRow } from "@/lib/live-display"
import { actionKinds, actionNodeName, actionStatuses, judgmentLabels, SchedulerEvidence } from "./scheduler"
import { StatusCell } from "@/components/status-cell"
import { STATUS_AXES, type StatusTone } from "@/lib/status"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { EvaluationStatusCell } from "./evaluation-active"
import { evaluationInjectionMatch, type ActiveEvaluations } from "@/lib/evaluation-active"
import { LivePowerTrend } from "./power-series"
import "./live-board.css"
import { SchedulerDecisionSummary, SchedulerWorkloadStates } from "./scheduler-workload-states"

type Zone="UTC"|"KST"
type Select=(key:"open"|"at",value:string|null,opener?:HTMLElement)=>void
type IncidentEvent=Omit<Extract<LiveEvent,{kind:"action"}>,"seq">|Omit<Extract<LiveEvent,{kind:"cluster"}>,"seq">|Omit<Extract<LiveEvent,{kind:"decision"}>,"seq">
const raw=(value:unknown)=>stringifySchedulerJSON(safeSource(value),2)
const number=(value:number|null|undefined,unit="",digits=0)=>value==null?"미제공":displayNumber(value,unit,digits)
const percent=(value:number|undefined,n:number|undefined)=>value==null||n==null?"미확인":n===0?"판정 불가":`${(value/n*100).toFixed(1)}%`
const serviceName=(snapshot:LiveSnapshot|null,uid:string)=>snapshot?.workloads.find(row=>row.uid===uid)?.name??"이름 미제공"
const latency=(workload:LiveWorkload,metric:LiveMetrics["workloads"][string]|undefined)=>{const value=sloLatency(workload,metric);return !metric?"미확인":metric.received===0||value===null?"판정 불가":value===undefined?"미제공":`p${workload.slo.percentile} ${number(value,"",1)}`}
const historyHref=incidentHistoryHref
const eventIdentity=(event:IncidentEvent)=>JSON.stringify([event.kind,event.at,event.kind==="action"?event.action.id:event.kind==="cluster"?event.cluster.pod?.uid??event.cluster.source:"",event.kind==="action"?event.action.node.uid:event.kind==="cluster"?event.cluster.event:""])
const nodeLabel=(state:LiveState,uid:string)=>state.snapshot?.nodes.find(node=>node.uid===uid)?.name||state.snapshot?.instances.find(row=>row.node.uid===uid)?.node.name||`보드 밖 노드 (…${uid.slice(-4)})`
const gapLabel=(state:LiveState,scope:string)=>scope.startsWith("node:")?`${nodeLabel(state,scope.slice(5))} 전력`:scope.startsWith("workload:")?`${serviceName(state.snapshot,scope.slice(9))} 요청 지표`:scope==="events"?"사건 기록":scope==="workloads"?"요청 지표":"관측 연결"
const actionLabel=(action:LiveAction)=>action.kind==="measurement-post"?(action.status==="NOT_SUBMITTED"?"측정 사전 검사":action.status==="validated"?"측정 요청 검사":"측정 요청"):action.kind==="measurement-preflight"?"측정 사전 검사":actionKinds[action.kind]??(action.kind==="measurement-handback"?"측정 장치 반환":"구성 변경")
function Facts({items}:{items:[string,React.ReactNode][]}) {
  return <table className="live-facts"><tbody>{items.map(([label,value])=><tr key={label}><th scope="row">{label}</th><td>{value==null||value===""?"미제공":value}</td></tr>)}</tbody></table>
}
function Source({value,label="원본 정보"}:{value:unknown;label?:string}) {
  return <details className="live-source"><summary>{label}</summary><pre>{raw(value)}</pre></details>
}
function MinuteSummary({state,workload,end}:{state:LiveState;workload:LiveWorkload;end:number}) {
  const rows=minuteMetrics(state.metrics,workload.uid,end)
  const received=rows.reduce((sum,row)=>sum+row.received,0),completed=rows.reduce((sum,row)=>sum+row.completed,0),n=rows.reduce((sum,row)=>sum+row.completed+row.failed,0),values=rows.map(row=>sloLatency(workload,row)).filter((value):value is number=>value!=null)
  return <table className="live-minute-summary"><caption>최근 1분</caption><thead><tr><th>관측 초 / 60</th><th>수신 건</th><th>성공 %</th><th>p{workload.slo.percentile} 최소 ms</th><th>최대 ms</th></tr></thead><tbody><tr><td>{rows.length}</td><td>{rows.length?number(received):"미확인"}</td><td>{percent(rows.length?completed:undefined,rows.length?n:undefined)}</td><td>{values.length?number(Math.min(...values),"",1):"판정 불가"}</td><td>{values.length?number(Math.max(...values),"",1):"판정 불가"}</td></tr></tbody></table>
}
function StateStrip({state,status,reconnect,now,zone,paused,togglePause}:{state:LiveState;status:string;reconnect:()=>void;now:number;zone:Zone;paused:boolean;togglePause:()=>void}) {
  const params=useSearchParams(),router=useRouter(),path=usePathname(),{config}=useSession()
  const connection:Record<string,string>={connecting:"연결 중",resuming:"재개 중",live:"실시간",reconnecting:"재연결 중",disconnected:"연결 끊김",unauthorized:"인증 필요",forbidden:"권한 없음","protocol-error":"메시지 계약 오류",idle:"연결 대기",fixture:"검사용","paused-hidden":"일시정지(탭 비활성)"}
  const fresh=liveFresh(state,now),retention=retentionNotice(state,zone)
  return <div className="live-strip" aria-label="관측 상태">
    <div className="live-freshness"><strong>{paused?"화면 일시정지":status==="live"&&!fresh?"관측 지연":connection[status]??status}</strong><span>마지막 수신 <time dateTime={state.lastReceivedAt?new Date(state.lastReceivedAt).toISOString():undefined}>{state.lastReceivedAt?displayTime(new Date(state.lastReceivedAt).toISOString(),zone):"대기"}</time></span></div>
    {retention&&<span role="status" title="서버 보존 범위와 최근 사건 조회 범위. 순번 공백과 사건 수는 다름">{retention}</span>}
    <div className="live-strip-actions"><button type="button" onClick={togglePause} aria-pressed={paused}>{paused?"실시간으로 복귀":"화면 일시정지"}</button>{!["live","fixture","paused-hidden"].includes(status)&&<button type="button" onClick={reconnect}>다시 연결</button>}
      <small>새로고침 시 실시간 복귀</small>
      {config.liveFixture&&<label>검사용 장면 <select value={params.get("fixture")??"live"} onChange={event=>{const next=new URLSearchParams(params.toString());next.delete("open");next.delete("at");if(event.target.value==="live")next.delete("fixture");else next.set("fixture",event.target.value);router.replace(`${path}?${next}`,{scroll:false})}}><option value="live">실시간</option><option value="empty">빈 상태</option><option value="first">첫 구성</option><option value="expand">확장</option><option value="shrink">축소</option></select></label>}
    </div>
  </div>
}
function Sparkline({state,uid,end}:{state:LiveState;uid:string;end:number}) {
  const gaps=state.gaps.filter(gap=>gap.scope==="all"||gap.scope===`workload:${uid}`)
  const points=state.metrics.map(row=>{const at=Date.parse(row.bucketAt);return {at,value:gaps.some(gap=>at>=Date.parse(gap.from)&&at<=Date.parse(gap.to))?null:row.workloads[uid]?.received??null}}),segments=trendSegments(points,end)
  const visible=points.filter(row=>row.at>=end-300000&&row.at<=end&&row.value!=null)
  return <div className="live-spark"><svg viewBox="0 0 120 32" role="img" aria-label={`최근 5분 유입 추세, ${visible.length}초 관측. 결손은 연결하지 않음`}><line x1="0" y1="29" x2="120" y2="29" stroke="currentColor" opacity=".2"/>{segments.map((points,index)=>points.includes(" ")?<polyline key={index} points={points} fill="none" stroke="currentColor" strokeWidth="1.8"/>:<circle key={index} cx={points.split(",")[0]} cy={points.split(",")[1]} r="1.5" fill="currentColor"/>)}</svg><span>{visible.length?`최대 ${number(Math.max(...visible.map(row=>row.value!)))} req/s`:"관측 대기"}</span></div>
}
function ServiceTable({state,rows,select}:{state:LiveState;rows:ServiceRow[];select:Select}) {
  const params=useSearchParams(),router=useRouter(),path=usePathname(),filter=params.get("services")??"전체",individual=params.get("view")==="individual"
  const snapshot=state.snapshot!,allIdle=rows.length>0&&rows.every(row=>row.idle)
  const update=(key:string,value:string)=>{const next=new URLSearchParams(params.toString());next.set(key,value);router.replace(`${path}?${next}`,{scroll:false})}
  const matched=rows.filter(row=>filter==="확인 필요"?["warning","danger"].includes(row.status.tone):filter==="가동"?!row.idle:filter==="유휴"?row.idle:true)
  // Keep the server's stable service order while live metric priorities change.
  const byUID=new Map(matched.map(row=>[row.workload.uid,row])),ordered=snapshot.workloads.flatMap(workload=>{const row=byUID.get(workload.uid);return row?[row]:[]})
  return <section className="live-services" aria-labelledby="live-services-title"><header className="live-section-heading"><h2 id="live-services-title">서비스 상태</h2><div className="live-filters"><label>상태 <select value={filter} onChange={event=>update("services",event.target.value)}>{["확인 필요","가동","유휴","전체"].map(value=><option key={value}>{value}</option>)}</select></label>{allIdle&&<label>보기 <select value={individual?"individual":"summary"} onChange={event=>update("view",event.target.value)}><option value="summary">집계</option><option value="individual">개별</option></select></label>}</div></header>
    {allIdle&&!individual&&(filter==="전체"||filter==="유휴")?<><table className="live-idle-summary"><caption>유휴 서비스 집계</caption><thead><tr><th>서비스 개</th><th>유입 req/s</th><th>배치 개</th><th>접수 허용 개</th><th>CHILL 자동 운영 개</th></tr></thead><tbody><tr><td data-label="서비스 개">{rows.length}</td><td data-label="유입 req/s">{rows.reduce((sum,row)=>sum+row.metric!.received,0)}</td><td data-label="배치 개">{rows.filter(row=>serviceDevices(snapshot,row.workload.uid).length>0).length}</td><td data-label="접수 허용 개">{rows.filter(row=>row.workload.admission).length}</td><td data-label="CHILL 자동 운영 개">{rows.filter(row=>row.workload.autonomous).length}</td></tr></tbody></table><Facts items={[["서비스 위치","전체 미배치"],...(rows.every(row=>!row.workload.autonomous)?[["배치·CHILL 자동 운영","미배치 · CHILL 자동 운영 꺼짐"]] as [string,React.ReactNode][]:[])]}/><details><summary>서비스별 위치</summary><table className="live-service-locations"><thead><tr><th>서비스</th><th>장치</th><th>상태</th></tr></thead><tbody>{ordered.map(({workload})=><tr key={workload.uid}><th><button type="button" onClick={event=>select("open",`service:${workload.uid}`,event.currentTarget)}>{workload.name}</button></th><td>미배치</td><td>{workloadReadiness(workload,snapshot.instances)}</td></tr>)}</tbody></table></details></>:<>
      {!!ordered.length&&<table className="live-service-table"><caption className="sr-only">마지막 완료된 1초 기준 서비스 지표</caption><thead><tr><th>서비스</th><th>장치</th><th>req/s</th><th>관측 지연 ms</th><th>목표 ms</th><th>상태</th></tr></thead><tbody>{ordered.map(({workload,metric,status})=><tr key={workload.uid}>
        <th scope="row" data-label="서비스"><button className="live-service-name" type="button" onClick={event=>select("open",`service:${workload.uid}`,event.currentTarget)}>{workload.name}</button></th>
        <td data-label="장치">{serviceDevices(snapshot,workload.uid).map(node=><Link className="live-device" key={node.uid} href={`/operations/devices?node=${encodeURIComponent(node.uid)}`}>{node.name||"이름 미제공"}</Link>)}{!serviceDevices(snapshot,workload.uid).length&&"미배치"}</td>
        <td data-label="req/s" className="live-numeric">{metric?number(metric.received):"미확인"}</td><td data-label="관측 지연 ms" className="live-numeric">{latency(workload,metric)}</td><td data-label="목표 ms" className="live-numeric">{number(workload.slo.targetLatencyNanos/1e6,"",1)}</td>
        <td data-label="상태"><StatusCell axis={STATUS_AXES.serviceImpact} label={status.label} tone={status.tone==="danger"?"fail":status.tone==="warning"?"warn":status.tone==="healthy"?"ok":"idle"} reason={status.reason}/><small>{workloadReadiness(workload,snapshot.instances)}</small></td>
      </tr>)}</tbody></table>}{!ordered.length&&<p className="live-empty">{rows.length?"해당 서비스 없음":"등록 서비스 없음"}</p>}
    </>}
  </section>
}
function ActionCard({action,snapshot,select,now,zone}:{action:LiveAction;snapshot:LiveSnapshot;select:Select;now:number;zone:Zone}) {
  const delayed=actionProgress(action,now)==="delayed"
  return <button type="button" className={`live-change ${delayed?"live-change-delayed":""}`} onClick={event=>select("open",`action:${JSON.stringify([action.id,action.node.uid,action.startedAt])}`,event.currentTarget)}><strong>{actionKinds[action.kind]??action.kind} <span aria-hidden="true">›</span></strong><span>{actionOrigin(action)} / {delayed?"예상 대비 지연 · 종결 미확인":"진행 중"}</span><small>{displayTime(action.startedAt,zone)} 시작 / {elapsed(action.startedAt,now).replace(" 전"," 경과")}{action.expectedSeconds!=null?` / 예상 ${duration(action.expectedSeconds)}`:" / 예상 시간 미제공"}</small><span className="sr-only">{action.workloadUID?serviceName(snapshot,action.workloadUID):"공유 구성"}</span></button>
}
function Changes({snapshot,select,now,zone}:{snapshot:LiveSnapshot;select:Select;now:number;zone:Zone}) {
  const actions=snapshot.actions.filter(action=>actionProgress(action,now)!=="terminal"),groups=new Map<string,LiveAction[]>()
  for(const action of actions){const key=action.workloadUID;groups.set(key,[...(groups.get(key)??[]),action])}
  const sorted=[...groups].sort(([,a],[,b])=>b.filter(row=>actionProgress(row,now)==="delayed").length-a.filter(row=>actionProgress(row,now)==="delayed").length)
  return <div className="live-change-list">{sorted.map(([uid,rows])=>{const delayed=rows.filter(row=>actionProgress(row,now)==="delayed").length,nodes=[...new Set(rows.map(row=>row.node.uid))];return <details key={uid} className="live-change-group"><summary>{uid?serviceName(snapshot,uid):"공유 구성"} / {rows.length}건 {!!delayed&&<span className="live-delay-badge">예상 대비 지연 {delayed}</span>}</summary>{nodes.map(node=><section key={node}><h3>{actionNodeName(rows.find(row=>row.node.uid===node)!.node)}</h3>{rows.filter(row=>row.node.uid===node).sort((a,b)=>Date.parse(a.startedAt)-Date.parse(b.startedAt)).map(action=><ActionCard key={JSON.stringify([action.id,action.node.uid,action.startedAt])} action={action} snapshot={snapshot} select={select} now={now} zone={zone}/>)}</section>)}</details>})}{!actions.length&&<p className="live-muted">진행 변경 없음</p>}</div>
}
function SideRail({state,select,zone,now}:{state:LiveState;select:Select;zone:Zone;now:number}) {
  const active=useChillQuery<ActiveEvaluations>("v1/evaluations/active")
  const actions=state.snapshot?.actions.filter(action=>actionProgress(action,now)!=="terminal")??[]
  const incidents=incidentSummary(state.events,state.gaps,now,state.metrics).rows,activity=decisionActivity(state.events,now)
  return <aside className="live-rail" aria-labelledby="live-rail-title" id="live-incidents"><header className="live-section-heading"><h2 id="live-rail-title">진행·사건</h2><span>최근 사건</span><Link href={historyHref()}>기록</Link></header>
    <table aria-label="최근 판단 통계"><thead><tr><th title="이번 접속에서 실제 수신한 최근 5분 판단. 새로고침하면 다시 수신하며 서버 전체 이력의 건수가 아님">최근 5분 · 현재 접속 수신 판단 (건)</th><th>유지 (건)</th><th>마지막 판단 KST</th></tr></thead><tbody><tr><td>{activity.count}</td><td>{activity.count>0&&activity.kept===activity.count?`${activity.kept} · 모두 유지`:activity.kept}</td><td>{displayTime(activity.lastAt,zone)||"미수신"}</td></tr></tbody></table>
    <table className="live-incident-table"><thead><tr><th>최근 KST</th><th>대상</th><th>동작</th><th>결과</th><th>사유</th></tr></thead><tbody>
      {actions.map(action=><tr key={`action:${JSON.stringify([action.id,action.node.uid,action.startedAt])}`}><td data-label="시각 KST">{displayTime(action.startedAt,zone)}</td><th scope="row" data-label="대상"><button type="button" onClick={event=>select("open",`action:${JSON.stringify([action.id,action.node.uid,action.startedAt])}`,event.currentTarget)}>{actionNodeName(action.node)}</button></th><td data-label="동작">{actionKinds[action.kind]??action.kind}</td><td data-label="결과">{actionProgress(action,now)==="delayed"?"예상 대비 지연":"진행"}</td><td data-label="사유">미제공</td></tr>)}
      {incidents.map(row=>{
        const event=row.kind==="event"?row.event:null
        const target=row.kind==="gap"?gapLabel(state,row.gap.scope):event?.kind==="action"?(event.action.node.uid?actionNodeName(event.action.node):"측정 요청"):event?.kind==="cluster"?(row.count>1&&event.cluster.reason?.includes("Pod is not labelled as a serving instance")?"비서빙 파드":event.cluster.pod?.name??event.cluster.source??"클러스터"):"전체 서비스"
        const operation=row.kind==="gap"?"관측":event?.kind==="action"?actionLabel(event.action):event?.kind==="cluster"?liveReason(event.cluster.event):"구성 판단"
        const result=row.kind==="gap"?row.gap.lost?"기록 손실":row.gap.recovered?"회복":"관측 공백":event?.kind==="action"?actionEventResult(event.action):event?.kind==="decision"?judgmentLabels[event.decision.state]??"판정 미제공":row.kind==="event"?clusterResult(row.members):"미확인"
        const reason=row.kind==="gap"?liveReason(row.gap.reason):event?.kind==="decision"?liveReason(event.decision.reason):incidentDiagnosis(event?.kind==="cluster"?event.cluster.reason:event?.action.result?.error).reason
        return <tr key={row.key}><td data-label="최근 KST">{displayTime(row.at,zone)}{row.kind==="event"&&row.count>1&&<details><summary>×{row.count}</summary><Facts items={[["최초 KST",displayTime(row.from,zone)],["최근 KST",displayTime(row.at,zone)]]}/>{row.members.map(item=><button key={item.seq??eventKey(item)} type="button" onClick={click=>item.kind==="decision"?select("at",item.decision.at,click.currentTarget):select("open",`event:${eventIdentity(item)}`,click.currentTarget)}>{displayTime(item.at,zone)}</button>)}</details>}</td><th scope="row" data-label="대상"><button type="button" onClick={click=>event?.kind==="decision"?select("at",event.decision.at,click.currentTarget):select("open",row.kind==="gap"?`gap:${JSON.stringify([row.gap.scope,row.gap.from])}`:`event:${eventIdentity(event!)}`,click.currentTarget)}>{target}</button></th><td data-label="동작">{operation}{event&&evaluationInjectionMatch(event,active.error?[]:active.data?.items??[])&&<span className="live-injection-tag">주입</span>}</td><td data-label="결과">{result}</td><td data-label="사유">{reason}</td></tr>
      })}
    </tbody></table>
    {!actions.length&&!incidents.length&&<p className="live-muted">진행·사건 없음</p>}
  </aside>
}
function ServiceMetrics({workload,state,zone}:{workload:LiveWorkload;state:LiveState;zone:Zone}) {
  const metric=state.metrics.at(-1)?.workloads[workload.uid],n=terminalCount(metric),queued=workloadQueue(workload,state.snapshot?.instances??[]),readiness=workloadReadiness(workload,state.snapshot?.instances??[])
  return <><h3>서비스 SLO</h3><Facts items={[[`관측 p${workload.slo.percentile} ms`,latency(workload,metric).replace(`p${workload.slo.percentile} `,"")],[`목표 p${workload.slo.percentile} ms`,`≤ ${number(Number(workload.slo.targetLatencyNanos)/1e6)}`],["상태",readiness],...(readiness==="미배치 · CHILL 자동 운영 꺼짐"?[]:[["CHILL 자동 운영",workload.autonomous?"켜짐":"꺼짐"]] as [string,React.ReactNode][]),["배치",serviceDevices(state.snapshot!,workload.uid).length?"배치됨":"미배치"],["유입 req/s",number(metric?.received)],["성공률 %",percent(metric?.completed,n)]]}/><details><summary>응답·대기 상세</summary><Facts items={[["SLO 안 %",metric?.received===0?"판정 불가":percent(metric?.withinSLO,n)],["참고 p95 ms",n?number(metric?.p95ms,"",1):"판정 불가"],["참고 p99 ms",n?number(metric?.p99ms,"",1):"판정 불가"],["대기 합계",queued],["배정 전",workload.pending],["배정 후",queued-workload.pending],["접수",workload.admission?"허용":"차단"],["종결 표본",number(n)],["지연 표본",number(metric?.latencySamples)],["배정 전 관측",displayTime(workload.readiness.observedAt,zone)]]}/></details></>
}
function ServiceTrend({state,uid,end,zone}:{state:LiveState;uid:string;end:number;zone:Zone}) {
  const rows=state.metrics.filter(row=>Date.parse(row.bucketAt)>=end-300000&&Date.parse(row.bucketAt)<=end)
  return <section className="live-service-trend"><h3>최근 5분 유입</h3><Facts items={[["실제 관측 시작",displayTime(rows[0]?.bucketAt,zone)],["실제 관측 종료",displayTime(rows.at(-1)?.bucketAt,zone)],["관측 초",rows.filter(row=>!!row.workloads[uid]).length]]}/><Sparkline state={state} uid={uid} end={end}/><details><summary>초별 요청·응답 지표</summary><div className="live-table-wrap"><table><thead><tr><th>시각 ({zone})</th><th>req/s</th><th>성공 건</th><th>실패 건</th><th>SLO 안 건</th><th>p95 ms</th></tr></thead><tbody>{rows.map(row=>{const metric=row.workloads[uid];return <tr key={row.bucketAt}><th>{displayTime(row.bucketAt,zone)}</th><td>{number(metric?.received)}</td><td>{number(metric?.completed)}</td><td>{number(metric?.failed)}</td><td>{number(metric?.withinSLO)}</td><td>{number(metric?.p95ms,"",1)}</td></tr>})}</tbody></table></div></details></section>
}
function Instances({state,workload,select,zone}:{state:LiveState;workload:LiveWorkload;select:Select;zone:Zone}) {
  const snapshot=state.snapshot!,instances=snapshot.instances.filter(row=>belongsToPool(row,workload.uid))
  return <section className="live-instance-list"><h3>인스턴스와 장치</h3>{instances.length?instances.map((instance,index)=><div key={instance.id}><button type="button" onClick={event=>select("open",`node:${instance.node.uid}`,event.currentTarget)}>{instance.node.name||nodeLabel(state,instance.node.uid)} / {snapshot.nodes.find(node=>node.uid===instance.node.uid)?.powerMode||"전력모드 미확인"}</button><Facts items={[["인스턴스",index+1],["상태",instanceSymbol(instance,workload.uid)[1]],["분배 %",number(routingShare(snapshot.routing[workload.uid],instance.id),"",1)],["처리 중 건",instance.running[workload.uid]??0],["배정 후 대기 건",instance.waiting[workload.uid]??0],["관측 KST",displayTime(instance.observedAt,zone)]]}/><Source value={instance}/></div>):<p className="live-muted">인스턴스 없음</p>}</section>
}
function CandidateEvidence({candidate,zone,snapshot}:{candidate:SchedulerCandidate;zone:Zone;snapshot:LiveSnapshot|null}) {
  return <section><h3>{candidate.keep?"유지안":"변경안"}</h3><Facts items={[["판정",judgmentLabels[candidate.status]??"판정 미제공"],["성능 평가",candidateEvaluation(candidate)],["예상 에너지 kJ",number(candidate.expectedJoules==null?null:candidate.expectedJoules/1000,"",2)]]}/>
    <div className="live-table-wrap"><table><caption>장치 자원</caption><thead><tr><th>장치 / 모드</th><th>GPU 개</th><th>RAM GiB</th><th>예약 RAM GiB</th><th>관측 KST</th></tr></thead><tbody>{candidate.nodes?.map(row=><tr key={row.node.uid}><th>{row.node.name}<small>{row.powerMode||"미제공"}</small></th><td>{row.gpuUUIDs?.length??"미제공"}</td><td>{number(Number(row.memoryBytes)/2**30,"",2)}</td><td>{number(Number(row.reserved.memoryBytes)/2**30,"",2)}</td><td>{displayTime(row.observedAt,zone)}</td></tr>)}</tbody></table></div>
    {!!candidate.instances?.length&&<div className="live-table-wrap"><table><caption>인스턴스 자원</caption><thead><tr><th>장치</th><th>GPU</th><th>RAM GiB</th><th>상태</th></tr></thead><tbody>{candidate.instances.map(row=><tr key={row.id}><th>{row.node.name}</th><td>{row.allocation.gpuUUID?"사용":"없음"}</td><td>{number(Number(row.allocation.memoryBytes)/2**30,"",2)}</td><td>{row.stopped?"제거":row.ready?"준비됨":"준비 중"}</td></tr>)}</tbody></table></div>}
    {!!candidate.slo?.length&&<div className="live-table-wrap"><table><caption>예상 서비스 지연</caption><thead><tr><th>서비스</th><th>분위수</th><th>예상 ms</th><th>목표 ms</th><th>표본 개</th></tr></thead><tbody>{candidate.slo.map(row=><tr key={row.uid}><th>{serviceName(snapshot,row.uid)}</th><td>p{row.percentile}</td><td>{number(row.expectedNanos==null?null:row.expectedNanos/1e6,"",1)}</td><td>{number(Number(row.targetNanos)/1e6,"",1)}</td><td>{row.samples}</td></tr>)}</tbody></table></div>}
  </section>
}
function Evidence({at,state,zone,cursor,visited=[]}:{at:string;state:LiveState;zone:Zone;cursor?:string;visited?:string[]}) {
  const range=decisionRange(at)
  const query=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams({...range,limit:"100",...(cursor?{cursor}:{})})}`,{enabled:!!range,decoder:parseSchedulerJSON})
  const event=query.data?.events?.find(event=>event.decision?.at===at),next=query.data?.nextCursor
  if(!event&&next&&!visited.includes(next))return <Evidence at={at} state={state} zone={zone} cursor={next} visited={[...visited,next]}/>
  const decision:SchedulerDecision|undefined=event?.decision
  const liveDecision=state.events.find(row=>row.kind==="decision"&&row.decision.at===at)?.decision??(state.snapshot?.decision?.at===at?state.snapshot.decision:null)
  const exhausted=!!query.data&&!event&&(!next||visited.includes(next))
  const names=new Map(state.snapshot?.workloads.map(row=>[row.uid,row.name])??[])
  for(const workload of event?.snapshot?.workloads??[])if(workload)names.set(workload.uid,workload.name)
  const status=decision?.status??liveDecision?.state
  return <><section className="live-evidence-summary"><h3>판단 근거 · 선택 당시</h3><SchedulerDecisionSummary at={displayTime(at,zone)} status={status?`${judgmentLabels[status]??"판단 미확인"} (${status})`:"미기록"} reasons={decision?.reasons??(liveDecision?.reason?[liveDecision.reason]:null)}/></section>
    <Link href={historyHref(undefined,at)}>해당 시점 기록</Link>
    {!range?<p role="alert">판단 시각 오류</p>:query.error?<p role="alert">{evidenceError(query.error)}</p>:!event?<p role="status">{exhausted?"기록 없음":"근거 조회 중"}</p>:decision&&<>
      <SchedulerWorkloadStates workloads={decision.workloads} names={names}/>
      <div className="live-table-wrap"><table><caption>입력 관측 KST</caption><thead><tr><th>시작</th><th>종료</th><th>수신 건</th><th>접수 건</th><th>계산 입력 건</th><th>검토 개</th></tr></thead><tbody><tr><td>{displayTime(decision.search.windowStart,zone)}</td><td>{displayTime(decision.search.windowEnd,zone)}</td><td>{number(decision.search.received)}</td><td>{number(decision.search.accepted)}</td><td>{number(decision.search.computeInputs)}</td><td>{number(decision.search.examined)}</td></tr></tbody></table></div>
      {!!decision.capacity?.workloads?.length&&<div className="live-table-wrap"><table><caption>당시 서비스 수요</caption><thead><tr><th>서비스</th><th>수요 req/s</th><th>미종결 건</th><th>처리 잔량 건</th><th>예상 대기 s</th><th>최소 수요 req/s</th></tr></thead><tbody>{decision.capacity.workloads.map(row=><tr key={row.uid}><th>{serviceName(state.snapshot,row.uid)}</th><td>{number(row.rate,"",2)}</td><td>{row.queued}</td><td>{number(row.backlog,"",2)}</td><td>{number(row.waitSeconds,"",2)}</td><td>{number(row.minimumDemand,"",2)}</td></tr>)}</tbody></table></div>}
      <details><summary>구성 후보·자원</summary>{candidatePlans(decision.keep,decision.target).map(candidate=><CandidateEvidence key={candidate.id} candidate={candidate} zone={zone} snapshot={state.snapshot}/>)}{decision.target?.id===decision.keep.id&&<span>변경 없음</span>}</details><Source value={event} label="판단 원본 JSON"/>
    </>}
  </>
}
function IncidentDetails({identity,state,zone}:{identity:string;state:LiveState;zone:Zone}) {
  const [search,setSearch]=React.useState<{identity:string;cursor?:string}>({identity})
  const cursor=search.identity===identity?search.cursor:undefined
  let at:string|undefined
  try{const ref=JSON.parse(identity);if(Array.isArray(ref)&&ref.length===4&&typeof ref[1]==="string"&&Number.isFinite(Date.parse(ref[1])))at=ref[1]}catch{}
  const local=state.events.find(row=>eventIdentity(row)===identity||String(row.seq)===identity)
  const query=useChillQuery<{events:IncidentEvent[];nextCursor?:string;coverage?:RecordedLivePage["coverage"]}>(`v1/observations/events?${new URLSearchParams({at:at??"",...(cursor?{cursor}:{})})}`,{enabled:!local&&!!at})
  const event=local??query.data?.events.find(row=>eventIdentity(row)===identity)
  const next=query.data?.nextCursor,coverage=query.data?.coverage
  const progress=!local&&<>{coverage&&<p role="status">{!event&&next?"찾는 중 · ":""}읽은 범위 {displayTime(coverage.from??undefined,zone)} ~ {displayTime(coverage.to??undefined,zone)}</p>}{coverage?.gaps?.map((gap,index)=><p key={index} role="status">기록 공백 {displayTime(gap.from,zone)} ~ {displayTime(gap.to,zone)} · {liveReason(gap.reason)}</p>)}{next&&(event?<p role="status">이후 기록 미확인</p>:<button type="button" disabled={next===cursor} onClick={()=>setSearch({identity,cursor:next})}>더 찾기</button>)}</>
  if(!event)return <><p role={query.error?"alert":"status"}>{query.error?evidenceError(query.error):next?"찾는 중":at&&!query.data?"사건 조회 중":"보관 범위 밖 사건"}</p>{progress}<Facts items={[["선택 사건 시각",at?displayTime(at,zone):"미제공"],["서버 사건 보관 범위","미제공"],["현재 탭 보관 시작",displayTime(state.events[0]?.at,zone)],["현재 탭 보관 종료",displayTime(state.events.at(-1)?.at,zone)]]}/><Link href={historyHref(undefined,at,undefined,identity)}>{at?"같은 기간 기록":"최근 기록 검색"}</Link></>
  const action=event.kind==="action"?event.action:null,cluster=event.kind==="cluster"?event.cluster:null
  const diagnosis=incidentDiagnosis(action?.result?.error??cluster?.reason)
  return <>{progress}<Facts items={[["시각 KST",displayTime(event.at,zone)],["종류",action?action.kind.startsWith("measurement-")?"측정 요청":"구성 변경":cluster?"클러스터":"구성 판단"],["대상",action?action.node.uid?actionNodeName(action.node):"측정 요청":cluster?.pod?.name??cluster?.source??"전체 서비스"],...(action?[["동작",actionLabel(action)],["결과",actionEventResult(action)]] as [string,React.ReactNode][]:[]),["사유",diagnosis.reason],...(diagnosis.mode?[["목표 전력모드",diagnosis.mode]] as [string,React.ReactNode][]:[]),...(!action||!isMeasurementPrecheck(action)?[["확인 항목",diagnosis.check]] as [string,React.ReactNode][]:[])]}/>{action?.node.uid&&<Link href={`/operations/devices?node=${encodeURIComponent(action.node.uid)}`}>해당 장치 확인</Link>}<Link href={historyHref(action?.workloadUID,event.at,action?.node.uid,identity)}>해당 시점 기록</Link><Source value={event}/></>
}
export function IncidentRecord({identity}:{identity:string}) {
  const {state}=useLive()
  return <IncidentDetails identity={identity} state={state} zone="KST"/>
}
function Selection({open,state,select,zone,end,now}:{open:string;state:LiveState;select:Select;zone:Zone;end:number;now:number}) {
  const snapshot=state.snapshot,workload=open.startsWith("service:")?snapshot?.workloads.find(row=>row.uid===open.slice(8)):undefined,node=open.startsWith("node:")?snapshot?.nodes.find(row=>row.uid===open.slice(5)):undefined,action=open.startsWith("action:")?snapshot?.actions.find(row=>JSON.stringify([row.id,row.node.uid,row.startedAt])===open.slice(7)):undefined
  if(open==="terms")return <><table><thead><tr><th>용어</th><th>정의·판정 기준</th></tr></thead><tbody>{[
    ["서비스 영향","관측된 요청 실패·목표 초과·대기·변경. 관측 공백과 별도"],
    ["성능 판정","서비스에 설정된 분위수와 지연 목표. 무부하·표본 부족은 판정 불가"],
    ["접수 허용","새 요청 접수 설정. 실제 유입과 별도"],
    ["유휴","유입·종결·대기가 없고 미배치·변경·예약 없는 서비스"],
    ["CHILL 자동 운영","서비스별 자동 구성 변경 설정"],
    ["관측 공백","범위별 연속 누락. 후속 유효 관측으로만 회복 확인"],
    ["예상 대비 지연","제공된 예상 평균 또는 예상 미제공 기본 기준 초과"],
    ["변경 비용 (이전: 동작 비용)","준비·제거·전력모드 전환의 시간·에너지"],
    ["분배 처리시간 (이전: 분배 점유시간)","요청당 점유시간(ms/요청)·완료 처리량(req/s)"],
  ].map(([term,meaning])=><tr key={term}><th scope="row">{term}</th><td>{meaning}</td></tr>)}</tbody></table><Facts items={[["예상 평균 대비 배수",3],["예상 미제공 기준 분",5]]}/></>
  if(open.startsWith("gap:")){
    const gap=gapEpisodes(state.gaps,now,state.metrics).find(row=>JSON.stringify([row.scope,row.from])===open.slice(4))
    return gap?<><h3>관측 공백 / {gapLabel(state,gap.scope)}</h3><Facts items={[["시작 KST",displayTime(gap.from,zone)],["종료 KST",displayTime(gap.to,zone)],["지속 s",number(gap.seconds,"",1)],["상태",gap.lost?"기록 손실":gap.recovered?"회복":"진행"],["사유",liveReason(gap.reason)]]}/><Source value={gap}/></>:<p>보관 범위 밖 사건</p>
  }
  if(open.startsWith("event:"))return <IncidentDetails identity={open.slice(6)} state={state} zone={zone}/>
  if(workload)return <><h3>{workload.name}</h3><nav className="live-detail-links"><Link href={`/workloads/${encodeURIComponent(workload.name)}?uid=${encodeURIComponent(workload.uid)}`}>서비스 설정</Link><Link href={historyHref(workload.uid)}>기록</Link></nav><ServiceMetrics workload={workload} state={state} zone={zone}/><MinuteSummary state={state} workload={workload} end={end}/><Instances state={state} workload={workload} select={select} zone={zone}/><section><h3>진행 변경</h3><Changes snapshot={scopeSnapshot(snapshot!,workload.uid)} select={select} now={now} zone={zone}/></section><ServiceTrend state={state} uid={workload.uid} end={end} zone={zone}/></>
  if(node)return <><h3>{node.name}</h3><Facts items={[["서비스",snapshot?.workloads.filter(workload=>snapshot.instances.some(row=>row.node.uid===node.uid&&!row.stopped&&belongsToPool(row,workload.uid))).map(row=>row.name).join(", ")||"실행 서비스 없음"],["전력모드",node.powerMode],["전력 W",number(state.metrics.at(-1)?.nodes[node.uid]?.watts,"",1)],["서빙 준비",node.ready?"준비됨":"사용 불가"],["측정 작업",node.measurement?"진행 중":"없음"],["관측 KST",displayTime(node.observedAt,zone)],["사유",node.reasons?.length?node.reasons.map(reason=><span key={reason}>{liveReason(reason)} </span>):"미제공"]]}/><Source value={node}/></>
  if(action)return <><h3>{actionKinds[action.kind]??"구성 변경"}</h3><Facts items={[["서비스",action.workloadUID?serviceName(snapshot,action.workloadUID):"공유 구성"],["장치",actionNodeName(action.node)],["실행 방식",actionOrigin(action)],["현재 단계",actionKinds[action.kind]??"구성 변경"],["상태",actionProgress(action,now)==="delayed"?"예상 대비 지연 / 종결 미확인":actionStatuses[action.status]??"미확인"],["시작 KST",displayTime(action.startedAt,zone)],["경과",elapsed(action.startedAt,now).replace(" 전","")],["예상",action.expectedSeconds==null?"미제공":duration(action.expectedSeconds)]]}/><Link href={historyHref(action.workloadUID)}>변경 기록</Link><Source value={action} label="변경 원본과 시간 근거"/></>
  if(open==="reservations")return <>{snapshot?.reservations.length?snapshot.reservations.map((row,index)=><section className="live-reservation-detail" key={index}><h3>{row.nodes.map(node=>node.name).join(", ")||"공유 범위"}</h3><Facts items={[["서비스",row.workloadUIDs.map(uid=>serviceName(snapshot,uid)).join(", ")],["장치 수",row.nodes.length],["사유",reservationReason(row.reason)],["해제 예정","미제공"],["확인 항목","장치 반환·실행 종료·전력 안정"]]}/><Source value={row}/></section>):<p>예약 없음</p>}</>
  return <p>현재 관측에 없는 항목</p>
}
function ReservationCount({count}:{count:number|null}) {
  const [observed,setObserved]=React.useState({current:count,previous:null as number|null})
  if(observed.current!==count)setObserved({current:count,previous:observed.current})
  return <><span className="sr-only">예약 상세 </span><span aria-live="polite">{count??"미확인"}</span>{observed.previous!==null&&observed.previous!==count&&<small className="live-count-change">이전 {observed.previous} → 현재 {count}</small>}</>
}
export function LiveBoardPage() {
  const live=useLive(),{epoch}=useSession(),params=useSearchParams(),router=useRouter(),path=usePathname()
  const [now,setNow]=React.useState(()=>Date.now()),[frozen,setFrozen]=React.useState<{state:LiveState;now:number;epoch:number;fixture:string|null}|null>(null)
  const paused=!!frozen&&frozen.epoch===epoch&&frozen.fixture===params.get("fixture"),state=paused?frozen.state:live.state,clock=paused?frozen.now:now,zone:Zone="KST"
  const heading=React.useRef<HTMLHeadingElement>(null),opener=React.useRef<HTMLElement|null>(null)
  const open=params.get("open"),at=params.get("at"),requestedEvidence=params.get("evidence"),evidence=requestedEvidence==="candidates"||requestedEvidence==="estimates"||requestedEvidence==="load"||requestedEvidence==="capacity"?requestedEvidence:null
  const snapshot=state.snapshot,metrics=state.metrics.at(-1),fixture=params.get("fixture"),end=live.status==="fixture"&&metrics?Date.parse(metrics.bucketAt)+1000:clock
  const selectedWorkload=open?.startsWith("service:")?snapshot?.workloads.find(row=>row.uid===open.slice(8)):undefined
  React.useEffect(()=>{const timer=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(timer)},[])
  const select:Select=React.useCallback((key,value,target)=>{if(target)opener.current=target;const next=new URLSearchParams(params.toString());next.delete("open");next.delete("at");next.delete("evidence");if(value)next.set(key,value);router.push(`${path}?${next}`,{scroll:false})},[params,path,router])
  const chooseEvidence=(kind:string)=>{const next=new URLSearchParams(params.toString());next.delete("at");next.set("evidence",kind);if(open?.startsWith("service:"))next.set("workload",open.slice(8));router.push(`${path}?${next}`,{scroll:false})}
  const changes=snapshot?.actions.filter(action=>actionProgress(action,clock)!=="terminal").length??0,delayed=snapshot?.actions.filter(action=>actionProgress(action,clock)==="delayed").length??0
  const fresh=liveFresh(state,clock),ongoingGap=gapEpisodes(state.gaps,clock,state.metrics).some(gap=>!gap.lost&&!gap.recovered)
  const rows=snapshot?serviceRows(snapshot,metrics?.workloads??{},clock):[],verdict=boardVerdict(rows,fresh,ongoingGap,changes-delayed,delayed)
  const evaluable=rows.filter(row=>sloLatency(row.workload,row.metric)!=null),missed=evaluable.some(row=>sloTargetMissed(row.workload,row.metric))
  const performance={axis:STATUS_AXES.performance,label:!fresh||!evaluable.length?"판정 불가":missed?"목표 초과":"목표 이내",tone:(!fresh||!evaluable.length?"idle":missed?"fail":"ok") as StatusTone,reason:!fresh?"관측 신선도 부족":!evaluable.length?"평가 표본 없음":undefined}
  const alerts=deviceAlerts(snapshot,state.events),failedEvents=state.events.filter(event=>event.kind==="action"&&isActionFailure(event.action)).length
  const summary=`서비스 영향 ${verdict.label} / 성능 판정 ${performance.label} / 관측 상태 ${verdict.observation.label} / 진행 변경 ${changes} / 보관 중 실패 사건 ${failedEvents} / 예약 ${snapshot?.reservations.length??"미확인"} / 장치 확인 ${alerts.length}`
  const sheetOpen=!!(open||at||evidence),title=at?"선택 판단 근거":open==="terms"?"용어 도움말":open?.startsWith("service:")?"서비스 관측":open?.startsWith("node:")?"장치 관측":evidence?"판단 근거":open==="reservations"?"예약 상세":open?.startsWith("action:")?"변경 상세":"사건 상세"
  return <div className="live-stage">
    <header className="live-heading"><h1 ref={heading} tabIndex={-1}>운영 현황</h1><div className="live-scope"><span>범위 전체 서비스</span><span>기준 시각 <time dateTime={metrics?.bucketAt}>{displayTime(metrics?.bucketAt,zone)||"미기록"}</time></span></div><StateStrip state={state} status={live.status} reconnect={live.reconnect} now={clock} zone={zone} paused={paused} togglePause={()=>setFrozen(paused?null:{state:live.state,now,epoch,fixture})}/></header>
    <section className="live-verdict" aria-label="운영 판독"><div className="live-section-heading"><h2>운영 판독</h2><nav className="live-detail-links"><Link href="/operations/devices">장치</Link>{snapshot?.decision&&<button type="button" onClick={event=>select("at",snapshot.decision!.at,event.currentTarget)}>판단 근거</button>}</nav></div>
      <table className="live-reading"><thead className="sr-only"><tr><th>서비스 영향</th><th>성능 판정</th><th>관측 상태</th><th>실험 상태</th></tr></thead><tbody><tr><td data-label="서비스 영향"><StatusCell axis={verdict.axis} label={verdict.label} tone={verdict.tone==="danger"?"fail":verdict.tone==="warning"?"warn":verdict.tone==="healthy"?"ok":"idle"} reason={verdict.reason}/></td><td data-label="성능 판정"><StatusCell {...performance}/></td><td data-label="관측 상태"><StatusCell {...verdict.observation}/></td><td data-label="실험 상태"><EvaluationStatusCell/></td></tr></tbody></table>
      <table className="live-counts"><thead><tr><th>평가 가능 개</th><th>미평가 개</th><th>진행 변경 건</th><th>보관 중 실패 건</th><th>예약 건</th><th>장치 확인 개</th></tr></thead><tbody><tr><td data-label="평가 가능 개">{snapshot?evaluable.length:"미확인"}</td><td data-label="미평가 개">{snapshot?rows.length-evaluable.length:"미확인"}</td><td data-label="진행 변경 건">{snapshot?changes:"미확인"}</td><td data-label="보관 중 실패 건">{failedEvents}</td><td data-label="예약 건"><button type="button" onClick={event=>select("open","reservations",event.currentTarget)}><ReservationCount key={epoch} count={snapshot?.reservations.length??null}/></button></td><td data-label="장치 확인 개"><strong>{fresh?alerts.length:"미확인"}</strong>{alerts.map(node=><Link className="live-device" key={node.uid} href={`/operations/devices?node=${encodeURIComponent(node.uid)}`}>{node.name||"장치"}<small>{node.reason}</small></Link>)}</td></tr></tbody></table>
    </section>
    <p className="sr-only" aria-live={paused?"off":"polite"} aria-atomic="true">{summary}</p>
    <div className="live-board-grid">{snapshot?<ServiceTable state={state} rows={rows} select={select}/>:<section className="live-services"><h2>서비스 상태</h2><p className="live-empty">서비스 구성 수신 대기</p></section>}<SideRail state={state} select={select} zone={zone} now={clock}/></div>
    <LivePowerTrend state={state}/>
    <Sheet open={sheetOpen} onOpenChange={value=>{if(!value)select(at?"at":"open",null)}}><SheetContent className="live-stage live-sheet" aria-describedby={undefined} onCloseAutoFocus={event=>{event.preventDefault();if(opener.current?.isConnected)opener.current.focus();else heading.current?.focus()}}><SheetHeader><SheetTitle>{title}</SheetTitle></SheetHeader><div className="live-sheet-body">
      {at?<Evidence key={at} at={at} state={state} zone={zone}/>:<>{open&&<Selection open={open} state={open==="reservations"&&params.get("workload")&&snapshot?{...state,snapshot:scopeSnapshot(snapshot,params.get("workload")!)}:state} select={select} zone={zone} end={end} now={clock}/>} {(open?.startsWith("service:")||evidence)&&<nav className="live-detail-links" aria-label="판단 근거 종류">{([["candidates","구성 후보"],["estimates","성능 추정"],["load","유입 근거"],["capacity","처리 용량"]] as const).map(([kind,label])=><button type="button" key={kind} aria-pressed={evidence===kind} onClick={()=>chooseEvidence(kind)}>{label}</button>)}</nav>}{evidence?<SchedulerEvidence kind={evidence}/>:open?.startsWith("service:")&&snapshot?.decision&&<Evidence at={snapshot.decision.at} state={state} zone={zone}/>}</>}
      {!at&&selectedWorkload&&<Source value={selectedWorkload}/>}
    </div></SheetContent></Sheet>
  </div>
}
