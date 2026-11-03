"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import { useLive } from "@/lib/use-live"
import { useCoverageInventory } from "./measurement-coverage"
import type { PoolsView, PowerModeActuator, SchedulerControllerState, SchedulerAllocation } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { displayTime, isStale, liveFresh, liveReason, powerWindow } from "@/lib/live-display"
import { STATUS_AXES } from "@/lib/status"
import { StatusCell } from "@/components/status-cell"
import { SourceJSON } from "@/components/read-region"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { deviceRows, devicePowerScale, devicePowerPercent, type DeviceRow } from "./resources-data"
import "./resources.css"

export function powerModeText(actuator:PowerModeActuator) {
  switch(actuator.kind){
    case "Fixed": return "장비 기본 상태"
    case "JetsonNVPModel": return "Jetson 전력모드 선택"
    case "NvidiaLockedGPUClock": return "GPU 클럭 고정"
  }
}
const number=(value:number|null|undefined)=>value==null?"미제공":value.toLocaleString("ko-KR",{maximumFractionDigits:1})
const time=(value:string|null|undefined)=>displayTime(value,"KST")||"미기록"
function Ready({row}:{row:DeviceRow}) {return <StatusCell axis={STATUS_AXES.placement} label={row.ready===null?"미확인":row.ready?"준비됨":"준비 안 됨"} tone={row.ready===null?"idle":row.ready?"ok":"warn"}/>}
function Allocation({value}:{value?:SchedulerAllocation}) {return <>{value?<dl className="devices-facts"><dt>GPU</dt><dd>{value.gpuUUID||"없음"}</dd><dt>메모리 (B)</dt><dd>{String(value.memoryBytes)}</dd></dl>:"미제공"}</>}
function ReadState({label,query}:{label:string;query:{isPending:boolean;error:Error|null;refetch:()=>unknown}}) {
  const status=(query.error as {status?:number}|null)?.status
  return query.error?<div role="alert">{label}: {status===401||status===403?"권한 없음":"조회 실패"} <button type="button" onClick={()=>void query.refetch()}>다시 조회</button></div>:query.isPending?<span role="status">{label}: 조회 중</span>:null
}

export function DevicesPage() {
  const live=useLive(),inventory=useCoverageInventory(),pools=useChillQuery<PoolsView>("v1/pools",{poll:true}),resources=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON,poll:true})
  const params=useSearchParams(),router=useRouter(),opener=React.useRef<HTMLButtonElement|null>(null),[now,setNow]=React.useState(()=>Date.now())
  React.useEffect(()=>{const timer=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(timer)},[])
  const state=live.state,metric=state.metrics.at(-1),clock=state.status==="fixture"&&metric?Date.parse(metric.bucketAt)+1000:now
  const rows=deviceRows(state,inventory.data,pools.data,resources.data?.snapshot),scale=devicePowerScale(rows,state.powerScale),window=powerWindow(state.metrics)
  const selectedUID=params.get("node"),selected=rows.find(row=>row.uid===selectedUID),fresh=liveFresh(state,clock)
  const inventoryNode=selected?.classes[0]?.nodes.find(node=>node.uid===selectedUID)
  const selectedReasons=selected?.reasons.filter(reason=>reason!=="PowerModeUnavailable"||selected.powerMode.toLowerCase()!=="fixed")??[]
  const start=window.expected?window.end-window.expected*1000:null,end=window.end?new Date(window.end).toISOString():null
  const last=state.lastPower,lastStale=!!last&&(isStale(last.bucketAt,clock)||metric?.pool.wholeWatts==null||!fresh)
  const max=Math.max(1,...state.metrics.filter(row=>Date.parse(row.bucketAt)>=window.end-300000).map(row=>row.pool.wholeWatts??0))
  const gaps=state.gaps.filter(row=>Date.parse(row.to)>=(start??0)&&Date.parse(row.from)<=window.end&&(row.scope==="all"||row.scope==="power"||row.scope.startsWith("node:")))
  function select(uid:string|null,button?:HTMLButtonElement){if(button)opener.current=button;const next=new URLSearchParams(params.toString());if(uid)next.set("node",uid);else next.delete("node");router.replace(`/operations/devices${next.size?`?${next}`:""}`,{scroll:false})}
  return <div className="devices-page">
    <header className="devices-heading"><h1>장치</h1><dl><dt>범위</dt><dd>전체 계측 / 구성 장치</dd><dt>기준 시각</dt><dd>{time(state.lastObservedAt)}</dd></dl><StatusCell axis={STATUS_AXES.observation} label={fresh?"최신":state.snapshot?"오래된 관측":"미확인"} tone={fresh?"ok":"warn"}/><Link href="/">현재 상태</Link></header>
    <section aria-labelledby="devices-power-title"><h2 id="devices-power-title">전력 추세</h2>
      <table className="devices-table devices-power-facts"><thead><tr><th>전체 계측 (W)</th><th>전력 관측</th><th>실제 구간 KST</th><th>관측 / 구간 (s)</th><th>누락 (s)</th><th>구성 장치 (개)</th><th>계측 장치 (개)</th></tr></thead><tbody><tr><td data-label="전체 계측 (W)" className="devices-numeric">{number(last?.watts)}{lastStale&&<strong>마지막값 · 오래된 관측</strong>}</td><td data-label="전력 관측">{time(last?.bucketAt)}</td><td data-label="실제 구간 KST">{start===null?"미기록":`${time(new Date(start).toISOString())} – ${time(end)}`}</td><td data-label="관측 / 구간 (s)" className="devices-numeric">{window.observed} / {window.expected}</td><td data-label="누락 (s)" className="devices-numeric">{window.expected-window.observed}</td><td data-label="구성 장치 (개)" className="devices-numeric">{state.snapshot?.nodes.length??"미확인"}</td><td data-label="계측 장치 (개)" className="devices-numeric">{metric?Object.keys(metric.nodes).length:"미확인"}</td></tr></tbody></table>
      <figure className="devices-trend"><figcaption>전체 계측 전력 (W) · 0–{number(max)}</figcaption>{window.expected?<svg viewBox="0 0 120 32" preserveAspectRatio="none" role="img" aria-label={`전체 계측 전력 추세, 누락 ${window.expected-window.observed}초`}><path d="M0 0V28H120" fill="none" stroke="currentColor" opacity=".3"/>{window.segments.map((points,index)=><polyline key={index} points={points} fill="none" stroke="currentColor" strokeWidth=".5" vectorEffect="non-scaling-stroke"/>)}{window.segments.flatMap((points,index)=>points.split(" ").map((point,offset)=>{const [cx,cy]=point.split(",");return <circle key={`${index}:${offset}`} cx={cx} cy={cy} r=".35"/>}))}</svg>:<p>전력 관측 없음</p>}</figure>
      {!!gaps.length&&<table className="devices-table devices-gap-table"><caption>누락 구간</caption><thead><tr><th>시작</th><th>종료</th><th>범위</th><th>사유</th></tr></thead><tbody>{gaps.map((gap,index)=><tr key={index}><td data-label="시작">{time(gap.from)}</td><td data-label="종료">{time(gap.to)}</td><td data-label="범위">{gap.scope}</td><td data-label="사유">{gap.reason}</td></tr>)}</tbody></table>}
      <Link href={`/records?${new URLSearchParams({kind:"power",...(start===null?{}:{from:new Date(start).toISOString()}),...(end?{to:end}:{})})}`}>전력 기록</Link>
    </section>
    <section aria-labelledby="devices-table-title"><div className="devices-section-heading"><h2 id="devices-table-title">장치·배치</h2><span>공통 눈금 0–{number(scale)} W</span></div>
      <table className="devices-table"><thead><tr><th>장치</th><th>준비</th><th>전력 (W)</th><th>서비스</th><th>인스턴스</th><th>관측 시각</th></tr></thead><tbody>{rows.map(row=><tr key={row.uid} aria-selected={selectedUID===row.uid}><th scope="row" data-label="장치"><button type="button" onClick={event=>select(row.uid,event.currentTarget)}>{row.name||"이름 미확인"}</button><small>{row.uid}</small><small>{row.configured?"구성 장치":"구성 밖"}</small></th><td data-label="준비"><Ready row={row}/></td><td data-label="전력 (W)" className="devices-numeric">{number(row.watts)}{row.watts!==null&&<span className="devices-bar" aria-label={`공통 눈금 ${scale} W`}><i style={{width:`${devicePowerPercent(row,scale)}%`}}/></span>}{row.watts===null?<small>관측 공백</small>:isStale(row.powerAt,clock)&&<small>오래된 관측</small>}</td><td data-label="서비스">{row.services.length?row.services.map(service=><a key={service.uid} href={`/workloads/${encodeURIComponent(service.name||service.uid)}?uid=${encodeURIComponent(service.uid)}`}>{service.name||service.uid}</a>):pools.data?"항목 없음":"미확인"}</td><td data-label="인스턴스" className="devices-numeric">{pools.data||state.snapshot?row.instances.length:"미확인"}</td><td data-label="관측 시각">{time(row.powerAt||row.observedAt)}</td></tr>)}</tbody></table>
      {!rows.length&&<p>{state.snapshot||inventory.data||pools.data?"항목 없음":"장치 관측 대기"}</p>}
      <div className="devices-read-state"><ReadState label="장비 사양" query={inventory}/><ReadState label="배치·자원 역할" query={pools}/><ReadState label="자원 할당" query={resources}/></div>
    </section>
    <Sheet open={!!selectedUID} onOpenChange={open=>{if(!open)select(null)}}><SheetContent className="devices-sheet" aria-describedby={undefined} onCloseAutoFocus={event=>{if(opener.current?.isConnected){event.preventDefault();opener.current.focus()}}}><SheetHeader><SheetTitle>{selected?.name||"장치 상세"}</SheetTitle></SheetHeader><div className="devices-detail">{selected?<>
      <dl className="devices-facts"><dt>Kubernetes 준비</dt><dd>{inventoryNode?inventoryNode.ready==="True"?"준비됨":inventoryNode.ready==="False"?"준비 안 됨":"미확인":"미확인"}</dd><dt>서빙 준비</dt><dd><Ready row={selected}/></dd><dt>전력 (W)</dt><dd>{number(selected.watts)}</dd><dt>전력 관측</dt><dd>{time(selected.powerAt)}</dd><dt>전력모드</dt><dd>{selected.powerMode||"미확인"}</dd><dt>전력모드 준비</dt><dd>{selected.powerMode.toLowerCase()==="fixed"?"해당 없음 · 고정":selected.reasons.includes("PowerModeUnavailable")?"확인 불가":selected.powerMode?"확인됨":"미확인"}</dd><dt>자원 역할</dt><dd>{selected.roles.join(", ")||(pools.data?"항목 없음":"미확인")}</dd><dt>구성 후보</dt><dd>{selected.reasons.includes("ExcludedNode")?"제외 노드":"제외 사유 없음"}</dd><dt>스케줄링 설정</dt><dd>{selected.schedulingDisabled===null?"미확인":selected.schedulingDisabled?"중지":"허용"}</dd><dt>공유 서비스 (개)</dt><dd>{selected.services.length}</dd><dt>배치 기준</dt><dd>{time(pools.data?.observedAt)}</dd><dt>자원 기준</dt><dd>{time(resources.data?.snapshot.at)}</dd></dl>
      <dl className="devices-facts"><dt>준비·자원 역할 사유</dt><dd>{selectedReasons.length?selectedReasons.map(reason=><div key={reason}>{liveReason(reason)}</div>):"해당 없음"}</dd></dl>
      <h3>사양·전력모드</h3>{selected.classes.length?selected.classes.map(cls=><React.Fragment key={cls.uid}><dl className="devices-facts"><dt>장비 종류</dt><dd>{cls.name}</dd><dt>아키텍처</dt><dd>{cls.architecture||"미제공"}</dd><dt>메모리</dt><dd>{cls.memory||"미제공"}</dd><dt>가속기</dt><dd>{cls.accelerator||"미제공"}</dd></dl><table className="devices-table"><thead><tr><th>전력모드</th><th>제어</th><th>모드 번호</th><th>최소 MHz</th><th>최대 MHz</th></tr></thead><tbody>{cls.powerModes.map(mode=><tr key={mode.name}><td data-label="전력모드">{mode.name}</td><td data-label="제어">{powerModeText(mode.actuator)}</td><td data-label="모드 번호">{mode.actuator.kind==="JetsonNVPModel"?mode.actuator.jetsonNVPModel.id:"해당 없음"}</td><td data-label="최소 MHz">{mode.actuator.kind==="NvidiaLockedGPUClock"?mode.actuator.nvidiaLockedGPUClock.minMHz:"해당 없음"}</td><td data-label="최대 MHz">{mode.actuator.kind==="NvidiaLockedGPUClock"?mode.actuator.nvidiaLockedGPUClock.maxMHz:"해당 없음"}</td></tr>)}</tbody></table></React.Fragment>):<p>{inventory.data?"미제공":"미확인"}</p>}
      <h3>노드 자원</h3>{(resources.data?.snapshot.nodes??[]).filter(node=>node.node.uid===selected.uid).map(node=><React.Fragment key={node.node.uid}><dl className="devices-facts"><dt>GPU</dt><dd>{node.gpuUUIDs?.join(", ")||"없음"}</dd><dt>메모리 (B)</dt><dd>{String(node.memoryBytes)}</dd></dl><h4>예약</h4><Allocation value={node.reserved}/><h4>서빙 외 할당</h4>{node.nonServing?.length?node.nonServing.map((value,index)=><Allocation key={index} value={value}/>):<p>항목 없음</p>}</React.Fragment>)}
      <h3>인스턴스·배치</h3>{selected.instances.length?selected.instances.map(id=>{const actual=resources.data?.snapshot.instances?.find(item=>item.id===id&&item.node.uid===selected.uid),pool=selected.poolInstances.find(item=>item.id===id),liveInstance=state.snapshot?.instances.find(item=>item.id===id&&item.node.uid===selected.uid),target=resources.data?.decision.target?.instances?.find(item=>item.id===id&&item.node.uid===selected.uid);return <div className="devices-instance" key={id}><h4>{id}</h4><dl className="devices-facts"><dt>서비스</dt><dd>{pool?.workloads?.map(item=>item.name||item.uid).join(", ")||actual?.workloadUID||liveInstance?.ownerWorkloadUID||"미확인"}</dd><dt>모델</dt><dd>{actual?.profile?.condition.logicalModel||"미제공"}</dd><dt>실행 경로</dt><dd>{actual?.profile?.condition.artifact.executionPath||"미제공"}</dd><dt>프로파일</dt><dd>{actual?.profileID||actual?.profile?.id||pool?.profile||liveInstance?.profileID||"미제공"}</dd><dt>관측 시각</dt><dd>{time(actual?.observedAt||pool?.observedAt||liveInstance?.observedAt)}</dd></dl><h4>실제 할당</h4><Allocation value={actual?.allocation}/><h4>목표 할당</h4><Allocation value={target?.allocation}/></div>}):<p>항목 없음</p>}
      <ReadState label="장비 사양" query={inventory}/><ReadState label="배치·자원 역할" query={pools}/><ReadState label="자원 할당" query={resources}/>
      <details><summary>원본</summary><SourceJSON inline value={{device:selected,pools:pools.data,inventory:inventory.data,resources:resources.data}}/></details>
    </>:<p>선택 장치 미확인 · {selectedUID}</p>}</div></SheetContent></Sheet>
  </div>
}
