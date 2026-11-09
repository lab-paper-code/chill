"use client"

import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { SchedulerCapacity, SchedulerControllerState, SchedulerFeedbackRate } from "@/lib/read-types"
import { planSearchFacts, capacityKappaHat, capacityTests, type CapacityView } from "@/lib/capacity"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { DataTable } from "@/components/data-table"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"

export function PlanSearchEvidence({capacity}:{capacity?:SchedulerCapacity}) {
  if(!capacity?.search&&!capacity?.plans?.length)return null
  return <section className="space-y-3"><h3>구성 탐색 근거</h3>
    {capacity.search&&<Facts items={planSearchFacts(capacity.search)}/>}
    {!!capacity.plans?.length&&<DataTable data={capacity.plans} columns={[
      {key:"node",label:"장치",value:plan=>plan.node.name},
      {key:"key",label:"계획",value:plan=>plan.key},
      {key:"eligible",label:"적격",value:plan=>plan.eligible?"예":"아니요"},
      {key:"reason",label:"사유",value:plan=>plan.reason??"없음"},
      {key:"dominated",label:"가지치기 사유",value:plan=>plan.dominated??"없음"},
    ]}/>
  </section>
}

export function FeedbackRateEvidence({tests}:{tests?:SchedulerFeedbackRate[]|null}) {
  if(!tests?.length)return <p>기록된 요청률 검사 근거 없음</p>
  return <div className="min-w-0 space-y-4">{tests.map((evidence,index)=>{
    const instance=evidence.option.instance
    return <section key={index} className="min-w-0 space-y-3 border-t pt-3 [&_dd]:break-all">
      <Facts items={[["인스턴스",evidence.instanceID],["κ̂ (κHat)",capacityKappaHat(evidence)],["장치 이름 / UID",`${instance.node.name} / ${instance.node.uid}`],["전력모드",instance.profile?.condition.powerMode??"미기록"],["프로파일 ID",instance.profileID??instance.profile?.id??"미기록"],["적격 서비스 UID",evidence.option.compatibleUIDs?.join(", ")||"없음"],["GPU UUID",instance.allocation.gpuUUID??"없음"],["메모리",`${instance.allocation.memoryBytes} bytes`],["최대 배치",String(instance.maxBatch)]]}/>
      {evidence.tests?.length?<div className="overflow-x-auto"><table className="w-full text-sm">
        <caption className="sr-only">현재 조건에서 검사한 요청률과 창 판정</caption>
        <thead><tr>{["입력 식별자", "검사 요청률 (req/s)", "판정", "합격 창 수", "창당 요청 수"].map(label=><th key={label} scope="col" className="p-3 text-left">{label}</th>)}</tr></thead>
        <tbody>{evidence.tests.map((test,index)=><tr key={index} className="border-t"><th scope="row" className="max-w-80 break-all p-3 text-left font-normal">{test.inputKey}</th><td className="p-3 text-right tabular-nums">{test.rate}</td><td className="p-3">{test.met?"충족":"미충족"}</td><td className="p-3 text-right tabular-nums">{test.passed}</td><td className="p-3 text-right tabular-nums">{test.requests}</td></tr>)}</tbody>
      </table></div>:<p>검사한 요청률 없음 · 통과 근거 없음</p>}
      <details><summary className="min-h-11 cursor-pointer py-3">검사 입력 원본</summary><SourceJSON inline value={evidence}/></details>
    </section>
  })}</div>
}

export function DecisionRateEvidence({capacity,names=new Map()}:{capacity?:SchedulerCapacity|null;names?:ReadonlyMap<string,string>}) {
  const number=(value:unknown)=>typeof value==="number"&&Number.isFinite(value)?value.toFixed(4):"미기록"
  return <section className="min-w-0 space-y-3"><h3 className="font-semibold">선택안·유지안 요청률 검사</h3>
    <p>κ̂는 현재 조건에서 검사한 요청률 중 최대 통과값이에요. 통과 근거 없는 0은 물리 처리량 0을 뜻하지 않아요. 검사하지 않은 요청률의 충족 여부는 알 수 없어요.</p>
    {!capacity?.workloads?.length?<p>기록된 Workload 진단 없음</p>:capacity.workloads.map(workload=><section key={workload.uid} className="min-w-0 space-y-3 border-t pt-3">
      <h4 className="break-all font-medium">{names.get(workload.uid)??workload.uid}</h4>
      <Facts items={[["서비스 UID",workload.uid],["계획 수요 (req/s)",number(workload.rate)],["대기 요청 수",number(workload.queued)],["목표 상대 프로파일 요청률 (req/s)",number(workload.routingRate)],["전환 중 유지 상대 프로파일 요청률 (req/s)",number(workload.retainedRoutingRate)]]}/>
      <p>라우팅 값은 프로파일 요청률의 상대 가중치이며 SLO 처리 용량 보장이 아니에요.</p>
      <details><summary className="min-h-11 cursor-pointer py-3">인스턴스별 상대 프로파일 요청률 가중치</summary><Facts items={Object.entries(workload.weights??{}).map(([id,weight])=>[id,String(weight)])}/></details>
      <h5 className="font-medium">선택안</h5><FeedbackRateEvidence tests={workload.tests}/>
      <h5 className="font-medium">유지안</h5><FeedbackRateEvidence tests={workload.keepTests}/>
    </section>)}
  </section>
}

export function CapacityEvidence() {
  const params=useSearchParams(),{token}=useSession()
  const query=useChillQuery<CapacityView>("v1/scheduler/capacity",{decoder:parseSchedulerJSON,poll:true})
  const current=useChillQuery<SchedulerControllerState>("v1/scheduler/state?view=compact",{decoder:parseSchedulerJSON,poll:true})
  const workload=params.get("workload")??(params.get("open")?.startsWith("service:")?params.get("open")!.slice(8):"")
  const tests=capacityTests(query.data?.tests??[],workload),decision=current.data?.decision
  const capacity=decision?.capacity,filtered=capacity&&{...capacity,workloads:workload?capacity.workloads?.filter(row=>row.uid===workload)??[]:capacity.workloads}
  return <section className="min-w-0 space-y-4"><header className="flex flex-wrap items-center justify-between gap-2"><h3>현재 요청률 검사 근거</h3><Button variant="outline" onClick={()=>{void query.refetch();void current.refetch()}} disabled={!token||query.isFetching||current.isFetching}>새로고침</Button></header>
    <QueryGate query={query}>{query.data&&<>
      <p>현재 조건의 모델 검사예요. κ̂는 검사한 요청률 중 최대 통과값이며, 통과 근거 없는 0은 물리 처리량 0을 뜻하지 않아요.</p>
      <Facts items={[["대상 적격 UID",workload||"전체"],["조회 시각",query.dataUpdatedAt?new Date(query.dataUpdatedAt).toISOString():"미확인"]]}/>
      <FeedbackRateEvidence tests={tests}/>
      <SourceExport value={{source:"v1/scheduler/capacity",workload,capacityReadAt:query.dataUpdatedAt,response:query.data}} filename="chill-scheduler-capacity.json"/>
    </>}</QueryGate>
    <QueryGate query={current}>{decision&&<>
      <Facts items={[["현재 결정 시각",decision.at]]}/>
      <p>아래 선택안·유지안은 현재 결정 응답 기준이에요. 위 검사 목록과 조회 시각이 다를 수 있어요.</p>
      <DecisionRateEvidence capacity={filtered}/>
      <SourceExport value={{source:"v1/scheduler/state?view=compact",readAt:current.dataUpdatedAt,decision}} filename="chill-scheduler-rate-decision.json"/>
    </>}</QueryGate>
  </section>
}
