"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { SchedulerSettings, WireInteger } from "@/lib/read-types"
import { capacityComparison, capacityFields, capacityPath, capacityPercentile, capacityPolling, capacityRate, capacityReason, capacityRows, capacityStatus, selectedCapacityRow, type CapacityJudgment, type CapacityRow, type CapacityView } from "@/lib/capacity"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { DataTable } from "@/components/data-table"
import { QueryGate, SourceExport, SourceJSON, StatusBadge } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

const value=(number:WireInteger|undefined,unit="")=>number==null?"값 없음":`${number}${unit?` ${unit}`:""}`
const nanos=(number:WireInteger|undefined)=>number==null?"값 없음":`${number} ns (${nanosToMilliseconds(number)} ms)`
const at=(time?:string)=>!time||time.startsWith("0001-")?"계산 시각 없음":`${time} (UTC) / ${new Date(time).toLocaleString("ko-KR",{timeZone:"Asia/Seoul"})} (KST)`
const checked=(time:number)=>time?at(new Date(time).toISOString()):"아직 조회하지 않았어요"

function Judgments({title,rows,passing,empty}:{title:string;rows?:CapacityJudgment[];passing:WireInteger;empty:string}) {
  return <section className="space-y-3"><h3 className="font-semibold">{title}</h3>{rows?.length?<div className="overflow-x-auto"><table className="w-full text-sm">
    <thead><tr>{["실행 순번","요청률 (req/s)","창당 요청 수","합격 창 수 / 필요한 창 수","서버 판정","실제 판정한 창 수","창별 SLO 백분위 지연 (ms, 완료 여유 포함)"].map(label=><th key={label} scope="col" className="p-3 text-left whitespace-nowrap">{label}</th>)}</tr></thead>
    <tbody>{rows.map((row,index)=><tr key={index} className="border-t"><th scope="row" className="p-3 text-left">{index+1}</th><td className="p-3">{row.rate}</td><td className="p-3">{String(row.requests)}</td><td className="p-3">{String(row.passed)} / {String(passing)}</td><td className="p-3">{row.met?"충족":"미충족"}</td><td className="p-3">{row.percentileMS?.length??0}개</td><td className="p-3"><ol className="min-w-52 list-inside list-decimal">{row.percentileMS?.map((latency,window)=><li key={window}>{capacityPercentile(latency)}</li>)}</ol>{!row.percentileMS?.length&&"지연 기록 없음"}</td></tr>)}</tbody>
  </table></div>:<p>{empty}</p>}</section>
}

function CapacityDetails({row,settings,capacityAt}:{row:CapacityRow;settings:{data?:SchedulerSettings;error:ApiError|null;isPending:boolean;isFetching:boolean;dataUpdatedAt:number;refetch:()=>unknown};capacityAt:number}) {
  const input=row.input,allocation=input.allocation,d=input.dispatchOccupancy
  return <div className="min-w-0 space-y-6 [&_dd]:break-all">
    <section className="space-y-3"><h3 className="font-semibold">결과</h3><StatusBadge tone={row.status==="Built"?"ok":row.status==="CapacityRejected"?"warn":"idle"}>{capacityStatus(row.status)}</StatusBadge><p>{capacityReason(row.reason)}</p>{row.previous&&<p>이전 계산 결과 · 다시 계산 중</p>}
      <Facts items={[["행 key",row.key],["처리량 상한 μ",row.status==="CapacityPending"&&row.mu===0?"값 없음":`${row.mu} req/s`],["계산 용량 κ",capacityRate(row.kappa)],["가장 짧은 배치 실행",value(row.minimumServiceNanos,"ns")],["계산 시각",at(row.builtAt)],["계산 경과",nanos(row.elapsedNanos)],["용량 조회 시각",checked(capacityAt)]]}/>
      {row.reason==="MissingDispatchOccupancy"&&<Link href="/measurements/dispatch-occupancy-measurements">분배 점유시간 측정 찾아보기</Link>}
    </section>
    <section className="space-y-3"><h3 className="font-semibold">입력</h3>
      <Facts items={[["워크로드 UID",input.workloadUID],["SLO 백분위",`p${input.slo.percentile}`],["SLO 목표",nanos(input.slo.targetLatencyNanos)],["Node 이름 / UID",`${input.node.name} / ${input.node.uid}`],["전력모드",input.powerMode],["프로파일 ID",input.profileID],["CPU 코어 ID 목록",allocation.cpus===null?"null":`[${allocation.cpus.join(", ")}]`],["GPU UUID",allocation.gpuUUID??"값 없음"],["메모리",value(allocation.memoryBytes,"bytes")],["스레드",value(allocation.threads,"개")],["CPU 수",value(allocation.cpuCount,"개")],["CPU 예약",allocation.cpuReserved?"예약함":"예약 안 함"],["최대 배치",value(input.maxBatch,"건/batch")],...capacityFields.map(field=>[field.label,field.key==="capacityWindowNanos"?`${input[field.key]} ns (${Number(input[field.key])/1e9} s)`:value(input[field.key],field.unit)] as [string,string])]}/>
      <p className="text-sm text-muted-foreground">이분탐색 폭은 μ에 대한 종료 폭 비율이며 달성한 정밀도가 아니에요.</p>
      <h4 className="font-medium">분배 점유시간 d와 출처</h4>{d?<Facts items={[["Node 이름 / UID",`${d.node.name} / ${d.node.uid}`],["전력모드",d.powerMode],["실행 이미지",d.runtimeImage],["모델",d.model],["d",value(d.perRequestNanos,"ns/request")],["측정 기록 식별자",d.source]]}/>:<p>분배 점유시간 근거가 없어요</p>}
    </section>
    <section className="space-y-3"><h3 className="font-semibold">현재 설정 대조</h3><p>행에 사용한 값과 서버의 현재 적용값이에요. 편집 초안은 아니며, 설정이 달라도 오류를 뜻하지 않아요.</p><Facts items={[["용량 조회 시각",checked(capacityAt)],["설정 조회 시각",checked(settings.dataUpdatedAt)]]}/>
      {settings.error&&<p role="alert">현재 설정과 대조하지 못했어요</p>}
      <QueryGate query={settings}>{settings.data&&!settings.error&&<div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr>{["계산 설정","행에 사용한 값","현재 적용값","같음·다름"].map(label=><th key={label} scope="col" className="p-3 text-left whitespace-nowrap">{label}</th>)}</tr></thead><tbody>{capacityComparison(input,settings.data).map(field=><tr key={field.key} className="border-t"><th scope="row" className="p-3 text-left font-medium">{field.label}</th><td className="p-3">{value(field.used,field.unit)}</td><td className="p-3">{value(field.current,field.unit)}</td><td className="p-3">{field.same?"같음":"다름"}</td></tr>)}</tbody></table></div>}</QueryGate>
      <Button variant="outline" onClick={()=>void settings.refetch()} disabled={settings.isFetching}>현재 설정 새로고침</Button><Rows><Row href="/configuration?section=capacity" label="설정에서 확인하기"/></Rows>
    </section>
    <section className="space-y-3"><h3 className="font-semibold">탐색 판정</h3><p>응답의 실행 순서예요. 조기에 판정을 끝낼 수 있어 창당 요청 수를 전체 요청 수나 실측 성공률로 해석하지 않아요.</p>
      <Judgments title="격자 판정" rows={row.grid} passing={input.capacityPassingWindows} empty="기록된 격자 판정이 없어요"/>
      <Judgments title="이분탐색 판정" rows={row.bisection} passing={input.capacityPassingWindows} empty="이분탐색 기록이 없어요"/>
    </section>
    <section className="space-y-3"><h3 className="font-semibold">원본</h3><SourceJSON inline value={row}/></section>
  </div>
}

export function SchedulerCapacityPage() {
  const params=useSearchParams(),router=useRouter(),client=useQueryClient(),{token}=useSession()
  const cached=client.getQueryState<CapacityView,ApiError>(["chill","v1/scheduler/capacity"])
  const query=useChillQuery<CapacityView>("v1/scheduler/capacity",{decoder:parseSchedulerJSON,poll:!cached?.error&&capacityPolling(cached?.data?.rows??[])})
  const settings=useChillQuery<SchedulerSettings>("v1/scheduler/settings",{decoder:parseSchedulerJSON})
  const workload=params.get("workload")??"",key=params.get("row")??"",all=query.data?.rows??[],rows=capacityRows(all,workload),selected=selectedCapacityRow(rows,key)
  const opener=React.useRef<HTMLElement|null>(null),launchers=React.useRef(new Map<string,HTMLButtonElement>()),closingKey=React.useRef(key)
  const [filterOpen,setFilterOpen]=React.useState(false)
  const workloads=Array.from(new Set(all.map(row=>row.input.workloadUID)))
  const exportValue={source:"v1/scheduler/capacity",workload,rowKey:key,capacityReadAt:query.dataUpdatedAt,settingsReadAt:settings.dataUpdatedAt,response:query.data,settings:settings.error?undefined:settings.data}
  return <Screen back={["/configuration?section=capacity","용량 설정"]} title="인스턴스 용량 계산" lede="인스턴스 하나가 이 조건에서 SLO를 만족하도록 계산한 요청률이에요. 실제 유입이나 지금의 SLO 관측은 아니에요." action={<Button onClick={()=>void query.refetch()} disabled={!token||query.isFetching}>용량 새로고침</Button>}>
    <Rows><RowDialog label="워크로드 UID" value={workload||"전체"} title="워크로드 고르기" description="받은 현재 용량 행에만 적용해요." open={filterOpen} onOpenChange={setFilterOpen}><fieldset className="grid gap-3">{["",...(workload&&!workloads.includes(workload)?[workload]:[]),...workloads].map(uid=><label key={uid} className="flex min-h-11 items-center gap-2 break-all"><input type="radio" name="workload" checked={workload===uid} onChange={()=>{router.replace(capacityPath(uid),{scroll:false});setFilterOpen(false)}}/>{uid||"전체 워크로드"}</label>)}</fieldset></RowDialog><Row href="/configuration?section=capacity" label="설정에서 확인하기"/></Rows>
    <QueryGate query={query}>{query.data&&<>
      <p className="text-sm text-muted-foreground">현재 계산 대상에서 받은 {all.length}개 · 용량 조회 {checked(query.dataUpdatedAt)}</p>
      {rows.length?<DataTable data={rows} columns={[
        {key:"workload",label:"워크로드 UID",value:row=><button type="button" className="min-h-11 break-all text-left underline underline-offset-4" ref={element=>{if(element)launchers.current.set(row.key,element);else launchers.current.delete(row.key)}} onClick={()=>{opener.current=document.activeElement as HTMLElement|null;closingKey.current=row.key;router.push(capacityPath(workload,row.key),{scroll:false})}}>{row.input.workloadUID}</button>,sortValue:row=>row.input.workloadUID},
        {key:"node",label:"Node",value:row=>row.input.node.name,sortValue:row=>row.input.node.name},
        {key:"power",label:"전력모드",value:row=>row.input.powerMode},
        {key:"profile",label:"프로파일",value:row=>row.input.profileID},
        {key:"batch",label:"최대 배치 (건/batch)",value:row=>String(row.input.maxBatch)},
        {key:"status",label:"상태 / 사유",value:row=><div className="min-w-48 space-y-1"><StatusBadge tone={row.status==="Built"?"ok":row.status==="CapacityRejected"?"warn":"idle"}>{capacityStatus(row.status)}</StatusBadge>{row.reason&&<p>{capacityReason(row.reason)}</p>}</div>},
        {key:"kappa",label:"κ (req/s)",value:row=>capacityRate(row.kappa)},
        {key:"built",label:"계산 시각",value:row=>at(row.builtAt),sortValue:row=>row.builtAt},
        {key:"previous",label:"이전 결과",value:row=>row.previous?"이전 계산 결과 · 다시 계산 중":"아니요"},
      ]}/>:<Empty title={all.length?"이 워크로드의 용량 행이 없어요":"아직 계산 대상 용량 행이 없어요. 자동제어와 측정 근거를 확인해 주세요."}><Rows>{workload&&<Row href={capacityPath()} label="워크로드 필터 해제"/>}<Row href="/configuration?section=capacity" label="용량 설정 확인하기"/></Rows></Empty>}
      <Rows><RowDialog label="자료 내보내기" title="현재 용량 응답 내보내기"><SourceExport value={exportValue} filename="chill-scheduler-capacity.json"/><SourceJSON inline value={query.data}/></RowDialog></Rows>
    </>}</QueryGate>
    <Dialog open={params.has("row")} onOpenChange={open=>{if(!open){closingKey.current=key;router.replace(capacityPath(workload),{scroll:false})}}}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-5xl" onCloseAutoFocus={event=>{event.preventDefault();const launcher=launchers.current.get(closingKey.current);if(launcher?.isConnected)launcher.focus();else if(opener.current?.isConnected)opener.current.focus();else{const heading=document.querySelector<HTMLElement>(".screen-head h1");if(heading){heading.tabIndex=-1;heading.focus()}}}}>
        <DialogHeader><DialogTitle>용량 행의 계산 근거</DialogTitle><DialogDescription>선택한 key의 현재 제공 여부를 확인해요. 다른 행으로 대체하지 않아요.</DialogDescription></DialogHeader>
        <QueryGate query={query}>{query.data&&(selected?<><CapacityDetails row={selected} settings={settings} capacityAt={query.dataUpdatedAt}/><SourceExport value={exportValue} filename="chill-capacity-row.json"/></>:<Empty title="이 용량 행을 찾을 수 없어요. 현재 계산 대상에서 빠졌을 수 있어요."><Rows><Row href={capacityPath()} label="전체 용량 목록 보기"/></Rows></Empty>)}</QueryGate>
      </DialogContent>
    </Dialog>
  </Screen>
}
