"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { ChevronRight, Cpu, Copy, Server, Zap } from "lucide-react"
import { useChillQuery } from "@/lib/client"
import { useLive } from "@/lib/use-live"
import { useCoverageInventory } from "@/hooks/use-measurement-data"
import type { LiveState } from "@/lib/live"
import type { PoolsView, PowerModeActuator, SchedulerControllerState, SchedulerAllocation } from "@/lib/read-types"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { deviceReasons, displayTime, liveReason } from "@/lib/live-display"
import { LiveObservation, ObservationAge } from "@/components/observation-age"
import { SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { ServingInstance } from "@/components/serving-instance"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip"
import { deviceRows, deviceInstances, deviceImageName, devicePowerScale, devicePowerPercent, type DeviceRow } from "./resources-data"
import { usePowerSeries, PowerTrend } from "./power-series"
import "./resources.css"

export function powerModeText(actuator:PowerModeActuator) {
  switch(actuator.kind){
    case "Fixed": return "장비 기본 상태"
    case "JetsonNVPModel": return "Jetson 전력모드 선택"
    case "NvidiaLockedGPUClock": return "GPU 클럭 고정"
  }
}
const number=(value:number|null|undefined)=>value==null?"미관측":value.toLocaleString("ko-KR",{maximumFractionDigits:1})
const time=(value:string|null|undefined)=>displayTime(value,"KST")||"미기록"
function Ready({row}:{row:DeviceRow}) {return <StatusBadge tone={row.ready===null?"idle":row.ready?"ok":"warn"}>{row.ready===null?"준비 미확인":row.ready?"준비됨":"준비 안 됨"}</StatusBadge>}
function Allocation({value}:{value?:SchedulerAllocation}) {return <>{value?<dl className="devices-facts"><dt>GPU</dt><dd>{value.gpuUUID||"없음"}</dd><dt>메모리 (B)</dt><dd>{String(value.memoryBytes)}</dd></dl>:"미제공"}</>}
function ReadState({label,query}:{label:string;query:{isPending:boolean;error:Error|null;refetch:()=>unknown}}) {
  const status=(query.error as {status?:number}|null)?.status
  return query.error?<div role="alert">{label}: {status===401||status===403?"권한 없음":"조회 실패"} <button type="button" onClick={()=>void query.refetch()}>다시 조회</button></div>:query.isPending?<span role="status">{label}: 조회 중</span>:null
}
function InstanceRows({row,state,resources}:{row:DeviceRow;state:LiveState;resources?:SchedulerControllerState}) {
  const instances=deviceInstances(row,state,resources?.snapshot)
  return <div className="devices-instance-list">{instances.length?instances.map((item,index)=><div className="devices-instance" role="group" aria-label={`물리 인스턴스 ${index+1}`} key={item.id}>
    <div className="devices-instance-label" aria-hidden="true">물리 인스턴스 {index+1}</div>
    {item.services.length?item.services.map(service=>{
      return <div className="devices-service-instance" key={service.uid} style={{"--service-color":`var(--chart-${Array.from(service.uid).reduce((sum,char)=>sum+char.charCodeAt(0),0)%5+1})`} as React.CSSProperties}>
        {item.live?<ServingInstance instance={item.live} workloadUID={service.uid} weights={state.snapshot?.routing[service.uid]} name={service.name||"서비스명 미기록"}/>:<div className="devices-rest-instance"><strong>{service.name||"서비스명 미기록"}</strong><StatusBadge tone={item.stopped||item.ready===null?"idle":item.ready?"ok":"warn"}>{item.stopped?"중단":item.ready===null?"상태 미확인":item.ready?"준비됨":"준비 중"}</StatusBadge><span aria-label="실제 분배율">분배 미관측</span><span aria-label="처리 요청">처리 {number(item.pool?(item.pool.running[service.uid]??0):undefined)}</span><span aria-label="대기 요청">대기 {number(item.pool?(item.pool.waiting[service.uid]??0):undefined)}</span></div>}
        <Link className="devices-service-link" href={`/workloads/${encodeURIComponent(service.name||service.uid)}?uid=${encodeURIComponent(service.uid)}`} aria-label={`${service.name||"서비스"} 상세`}><ChevronRight aria-hidden="true"/></Link>
      </div>
    }):<p className="devices-empty-instance">서비스 미확인</p>}
    {item.actual?.profile&&<p className="devices-model"><Cpu aria-hidden="true"/>{item.actual.profile.condition.logicalModel||"모델 미기록"}<span>{deviceImageName(item.actual.profile.condition.artifact.image)}</span></p>}
  </div>):<p className="devices-empty-instance">{state.snapshot||resources?"인스턴스 없음":"인스턴스 관측 대기"}</p>}</div>
}
function RawInfo({value,children}:{value:unknown;children:React.ReactNode}) {
  const [copy,setCopy]=React.useState("")
  return <details className="devices-original"><summary>원본 정보</summary><div className="devices-source-actions"><button type="button" onClick={async()=>{try{await navigator.clipboard.writeText(stringifySchedulerJSON(safeSource(value),2));setCopy("복사됨")}catch{setCopy("복사 실패 · 자료를 내려받아 주세요")}}}><Copy aria-hidden="true"/>원본 복사</button><SourceExport value={value} filename="chill-device.json"/><span role="status">{copy}</span></div>{children}<SourceJSON inline value={value}/></details>
}

export function DevicesPage() {
  const live=useLive(),inventory=useCoverageInventory(),pools=useChillQuery<PoolsView>("v1/pools",{poll:true}),resources=useChillQuery<SchedulerControllerState>("v1/scheduler/state",{decoder:parseSchedulerJSON,poll:true})
  const params=useSearchParams(),router=useRouter(),opener=React.useRef<HTMLButtonElement|null>(null),[now,setNow]=React.useState(()=>Date.now())
  React.useEffect(()=>{const timer=setInterval(()=>setNow(Date.now()),1000);return()=>clearInterval(timer)},[])
  const state=live.state,metric=state.metrics.at(-1),clock=now
  const rows=deviceRows(state,inventory.data,pools.data,resources.data?.snapshot),scale=devicePowerScale(rows,state.powerScale),power=usePowerSeries(state)
  const selectedUID=params.get("node"),selected=rows.find(row=>row.uid===selectedUID)
  const inventoryNode=selected?.classes[0]?.nodes.find(node=>node.uid===selectedUID)
  const selectedReasons=selected?deviceReasons(selected.reasons,selected.powerMode):[]
  const start=power.points[0]?.from??null,end=power.points.length?new Date(power.points.at(-1)!.to).toISOString():null
  const last=state.lastPower
  const gaps=state.gaps.filter(row=>Date.parse(row.to)>=(start??0)&&Date.parse(row.from)<=(end?Date.parse(end):0)&&(row.scope==="all"||row.scope==="power"||row.scope.startsWith("node:")))
  function select(uid:string|null,button?:HTMLButtonElement){if(button)opener.current=button;const next=new URLSearchParams(params.toString());if(uid)next.set("node",uid);else next.delete("node");router.replace(`/operations/devices${next.size?`?${next}`:""}`,{scroll:false})}
  return <div className="devices-page">
    <header className="devices-heading"><div><h1>장치</h1><p>장치에서 실행 중인 서비스와 전력을 확인하세요.</p></div><div className="devices-heading-status"><LiveObservation state={state} now={clock}/><Link href="/">서비스 현황<ChevronRight aria-hidden="true"/></Link></div></header>
    <section aria-labelledby="devices-table-title" className="devices-fleet"><div className="devices-section-heading"><h2 id="devices-table-title"><Server aria-hidden="true"/>장치별 배치</h2><span>{state.snapshot||inventory.data||pools.data?`${rows.length}대`:"관측 대기"}</span></div>
      <table className="devices-table devices-fleet-table"><colgroup><col className="devices-name-column"/><col className="devices-power-column"/><col/></colgroup><thead><tr><th>장치 / 준비 상태</th><th>현재 전력 <span className="devices-scale">0–{number(scale)} W</span></th><th>서비스 인스턴스</th></tr></thead><tbody>{rows.map(row=><tr key={row.uid} aria-selected={selectedUID===row.uid}>
        <th scope="row" data-label="장치"><button className="devices-select" type="button" onClick={event=>select(row.uid,event.currentTarget)}><Server aria-hidden="true"/><span>{row.name||"이름 미확인"}<small>{row.configured?"구성 장치":"구성 밖 관측"}</small></span><ChevronRight aria-hidden="true"/></button><Ready row={row}/></th>
        <td data-label="전력" className="devices-power-cell"><div className="devices-power-value"><strong>{number(row.watts)}</strong><span>{row.watts===null?"":"W"}</span></div><span className="devices-bar" aria-label={`공통 눈금 ${scale} W`} data-missing={row.watts===null}><i style={{width:`${devicePowerPercent(row,scale)??0}%`}}/></span><small className="devices-observed-slot">{row.watts===null?"전력 미관측":time(row.powerAt)}</small></td>
        <td data-label="서비스 인스턴스"><InstanceRows row={row} state={state} resources={resources.data}/></td>
      </tr>)}</tbody></table>
      {!rows.length&&<p className="devices-empty">{state.snapshot||inventory.data||pools.data?"관측된 장치 없음":"장치 관측 대기"}</p>}
      <div className="devices-read-state"><ReadState label="장비 사양" query={inventory}/><ReadState label="배치·자원 역할" query={pools}/><ReadState label="자원 할당" query={resources}/></div>
    </section>
    <section className="devices-power-history"><details><summary><Zap aria-hidden="true"/>전체 전력 이력</summary><div className="devices-history-body">
      <table className="devices-table devices-power-facts"><thead><tr><th>전체 계측 (W)</th><th>전력 관측</th><th>구성 장치 (개)</th><th>계측 장치 (개)</th></tr></thead><tbody><tr><td data-label="전체 계측 (W)" className="devices-numeric">{number(last?.watts)}<span className="devices-observed-slot"><ObservationAge at={last?.bucketAt} now={clock} label="전력 관측"/></span></td><td data-label="전력 관측">{time(last?.bucketAt)}</td><td data-label="구성 장치 (개)" className="devices-numeric">{state.snapshot?.nodes.length??"미확인"}</td><td data-label="계측 장치 (개)" className="devices-numeric">{metric?Object.keys(metric.nodes).length:"미확인"}</td></tr></tbody></table>
      <PowerTrend points={power.points}/><ReadState label="전력 이력" query={power.query}/>
      {!!gaps.length&&<table className="devices-table devices-gap-table"><caption>누락 구간</caption><thead><tr><th>시작</th><th>종료</th><th>범위</th><th>사유</th></tr></thead><tbody>{gaps.map((gap,index)=><tr key={index}><td data-label="시작">{time(gap.from)}</td><td data-label="종료">{time(gap.to)}</td><td data-label="범위">{gap.scope}</td><td data-label="사유">{gap.reason}</td></tr>)}</tbody></table>}
      <Link href={`/records?${new URLSearchParams({kind:"power",...(start===null?{}:{from:new Date(start).toISOString()}),...(end?{to:end}:{})})}`}>전력 기록</Link>
    </div></details></section>
    <Sheet open={!!selectedUID} onOpenChange={open=>{if(!open)select(null)}}><SheetContent className="devices-sheet" aria-describedby={undefined} onCloseAutoFocus={event=>{if(opener.current?.isConnected){event.preventDefault();opener.current.focus()}}}><SheetHeader className="devices-sheet-heading"><SheetTitle><Server aria-hidden="true"/>{selected?.name||"장치 상세"}</SheetTitle></SheetHeader><div className="devices-detail">{selected?<>
      <div className="devices-summary"><div><span>서빙 준비</span><Ready row={selected}/></div><div><span>전력 모드</span><strong>{selected.powerMode||"미확인"}</strong></div><div className="devices-summary-power"><span>현재 전력</span><strong>{number(selected.watts)}<small>{selected.watts===null?"":" W"}</small></strong><time>{time(selected.powerAt)}</time></div></div>
      <section aria-labelledby="device-instances-title"><div className="devices-section-heading"><h3 id="device-instances-title">서비스 인스턴스</h3><span>{selected.instances.length}개</span></div><InstanceRows row={selected} state={state} resources={resources.data}/></section>
      <section aria-label="장치 전력 추세"><PowerTrend points={power.points} nodeUID={selected.uid}/></section>
      <section className="devices-conditions" aria-labelledby="device-conditions-title"><h3 id="device-conditions-title">운영 조건</h3><div className="devices-reasons"><Tooltip><TooltipTrigger asChild><button type="button" className="devices-reason" data-active={selectedReasons.length>0}>{selectedReasons.length?`확인 필요 ${selectedReasons.length}건`:selected.ready===null?"준비 사유 미확인":"추가 사유 없음"}</button></TooltipTrigger><TooltipContent>{selectedReasons.length?selectedReasons.map(liveReason).join("\n"):"현재 관측에 추가 사유가 기록되지 않았습니다."}</TooltipContent></Tooltip></div><dl className="devices-facts"><dt>자원 역할</dt><dd>{selected.roles.join(", ")||(pools.data?"지정 없음":"미확인")}</dd><dt>스케줄링</dt><dd>{selected.schedulingDisabled===null?"미확인":selected.schedulingDisabled?"중지":"허용"}</dd></dl></section>
      <RawInfo value={{device:selected,live:{node:state.snapshot?.nodes.find(node=>node.uid===selected.uid),instances:state.snapshot?.instances.filter(item=>item.node.uid===selected.uid),routing:state.snapshot?.routing},pools:pools.data,inventory:inventory.data,resources:resources.data}}>
        <dl className="devices-facts"><dt>장치 UID</dt><dd>{selected.uid}</dd><dt>Kubernetes 준비</dt><dd>{inventoryNode?.ready??"미확인"}</dd><dt>배치 기준</dt><dd>{time(pools.data?.observedAt)}</dd><dt>자원 기준</dt><dd>{time(resources.data?.snapshot.at)}</dd></dl>
        <details><summary>배치 대상 서비스</summary>{selected.services.length?selected.services.map(service=><p key={service.uid}><Link href={`/workloads/${encodeURIComponent(service.name||service.uid)}?uid=${encodeURIComponent(service.uid)}`}>{service.name||"서비스명 미기록"}</Link></p>):<p>배치 대상 미기록</p>}</details>
        <details><summary>사양·전력모드</summary>{selected.classes.length?selected.classes.map(cls=><React.Fragment key={cls.uid}><dl className="devices-facts"><dt>장비 종류</dt><dd>{cls.name}</dd><dt>아키텍처</dt><dd>{cls.architecture||"미제공"}</dd><dt>메모리</dt><dd>{cls.memory||"미제공"}</dd><dt>가속기</dt><dd>{cls.accelerator||"미제공"}</dd></dl><table className="devices-table"><thead><tr><th>전력모드</th><th>제어</th><th>모드 번호</th><th>최소 MHz</th><th>최대 MHz</th></tr></thead><tbody>{cls.powerModes.map(mode=><tr key={mode.name}><td data-label="전력모드">{mode.name}</td><td data-label="제어">{powerModeText(mode.actuator)}</td><td data-label="모드 번호">{mode.actuator.kind==="JetsonNVPModel"?mode.actuator.jetsonNVPModel.id:"해당 없음"}</td><td data-label="최소 MHz">{mode.actuator.kind==="NvidiaLockedGPUClock"?mode.actuator.nvidiaLockedGPUClock.minMHz:"해당 없음"}</td><td data-label="최대 MHz">{mode.actuator.kind==="NvidiaLockedGPUClock"?mode.actuator.nvidiaLockedGPUClock.maxMHz:"해당 없음"}</td></tr>)}</tbody></table></React.Fragment>):<p>{inventory.data?"미제공":"미확인"}</p>}</details>
        <details><summary>노드 자원</summary>{(resources.data?.snapshot.nodes??[]).filter(node=>node.node.uid===selected.uid).map(node=><React.Fragment key={node.node.uid}><dl className="devices-facts"><dt>GPU</dt><dd>{node.gpuUUIDs?.join(", ")||"없음"}</dd><dt>메모리 (B)</dt><dd>{String(node.memoryBytes)}</dd></dl><h4>예약</h4><Allocation value={node.reserved}/><h4>서빙 외 할당</h4>{node.nonServing?.length?node.nonServing.map((value,index)=><Allocation key={index} value={value}/>):<p>서빙 외 할당 없음</p>}</React.Fragment>)}</details>
        <details><summary>인스턴스 원문·할당</summary>{deviceInstances(selected,state,resources.data?.snapshot).map(item=><div key={item.id} className="devices-instance-raw"><h4>{item.id}</h4><dl className="devices-facts"><dt>실행 경로</dt><dd>{item.actual?.profile?.condition.artifact.executionPath||"미제공"}</dd><dt>이미지</dt><dd>{item.actual?.profile?.condition.artifact.image||"미제공"}</dd><dt>프로파일</dt><dd>{item.actual?.profileID||item.actual?.profile?.id||item.pool?.profile||item.live?.profileID||"미제공"}</dd><dt>관측 시각</dt><dd>{time(item.actual?.observedAt||item.pool?.observedAt||item.live?.observedAt)}</dd></dl><h4>실제 할당</h4><Allocation value={item.actual?.allocation}/><h4>목표 할당</h4><Allocation value={resources.data?.decision.target?.instances?.find(target=>target.id===item.id&&target.node.uid===selected.uid)?.allocation}/></div>)}</details>
      </RawInfo>
      <div className="devices-read-state"><ReadState label="장비 사양" query={inventory}/><ReadState label="배치·자원 역할" query={pools}/><ReadState label="자원 할당" query={resources}/></div>
    </>:<><p>선택 장치 미확인</p><SourceJSON inline title="선택 원본" value={{nodeUID:selectedUID}}/></>}</div></SheetContent></Sheet>
  </div>
}
