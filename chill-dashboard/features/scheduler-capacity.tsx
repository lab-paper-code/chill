"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { SchedulerSettings, WireInteger } from "@/lib/read-types"
import { capacityComparison, capacityFields, capacityPercentile, capacityPolling, capacityRate, capacityReason, capacityRows, capacityStatus, selectedCapacityRow, type CapacityJudgment, type CapacityRow, type CapacityView } from "@/lib/capacity"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { DataTable } from "@/components/data-table"
import { QueryGate, SourceExport, SourceJSON, StatusBadge } from "@/components/read-region"
import { Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"

const value=(number:WireInteger|undefined,unit="")=>number==null?"미제공":`${number}${unit?` ${unit}`:""}`
const nanos=(number:WireInteger|undefined)=>number==null?"미제공":`${number} ns (${nanosToMilliseconds(number)} ms)`
const at=(time?:string)=>!time||time.startsWith("0001-")?"미기록":`${time} (UTC) / ${new Date(time).toLocaleString("ko-KR",{timeZone:"Asia/Seoul"})} (KST)`
const checked=(time:number)=>time?at(new Date(time).toISOString()):"미확인"

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
      <Facts items={[["행 key",row.key],["처리량 상한",row.status==="CapacityPending"&&row.mu===0?"값 없음":`${row.mu} req/s`],["처리 용량",capacityRate(row.kappa)],["가장 짧은 배치 실행",value(row.minimumServiceNanos,"ns")],["계산 시각",at(row.builtAt)],["계산 경과",nanos(row.elapsedNanos)],["용량 조회 시각",checked(capacityAt)]]}/>
      {row.reason==="MissingDispatchOccupancy"&&<Link href="/measurements/results?kind=dispatch-occupancy">분배 처리시간 측정</Link>}
    </section>
    <section className="space-y-3"><h3 className="font-semibold">입력</h3>
      <Facts items={[["워크로드 UID",input.workloadUID],["SLO 백분위",`p${input.slo.percentile}`],["SLO 목표",nanos(input.slo.targetLatencyNanos)],["Node 이름 / UID",`${input.node.name} / ${input.node.uid}`],["전력모드",input.powerMode],["프로파일 ID",input.profileID],["GPU UUID",allocation.gpuUUID??"값 없음"],["메모리",value(allocation.memoryBytes,"bytes")],["최대 배치",value(input.maxBatch,"건/batch")],...capacityFields.map(field=>[field.label,field.key==="capacityWindowNanos"?`${input[field.key]} ns (${Number(input[field.key])/1e9} s)`:value(input[field.key],field.unit)] as [string,string])]}/>
      <p className="text-sm text-muted-foreground">이분탐색 폭은 처리량 상한에 대한 종료 폭 비율이며 달성한 정밀도가 아니에요.</p>
      <h4 className="font-medium">분배 점유시간 d와 출처</h4>{d?<Facts items={[["Node 이름 / UID",`${d.node.name} / ${d.node.uid}`],["전력모드",d.powerMode],["실행 이미지",d.runtimeImage],["모델",d.model],["d",value(d.perRequestNanos,"ns/request")],["측정 기록 식별자",d.source]]}/>:<p>분배 점유시간 근거가 없어요</p>}
    </section>
    <section className="space-y-3"><h3 className="font-semibold">현재 설정 대조</h3><p>행에 사용한 값과 서버의 현재 적용값이에요. 편집 초안은 아니며, 설정이 달라도 오류를 뜻하지 않아요.</p><Facts items={[["용량 조회 시각",checked(capacityAt)],["설정 조회 시각",checked(settings.dataUpdatedAt)]]}/>
      {settings.error&&<p role="alert">현재 설정과 대조하지 못했어요</p>}
      <QueryGate query={settings}>{settings.data&&!settings.error&&<div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr>{["계산 설정","행에 사용한 값","현재 적용값","같음·다름"].map(label=><th key={label} scope="col" className="p-3 text-left whitespace-nowrap">{label}</th>)}</tr></thead><tbody>{capacityComparison(input,settings.data).map(field=><tr key={field.key} className="border-t"><th scope="row" className="p-3 text-left font-medium">{field.label}</th><td className="p-3">{value(field.used,field.unit)}</td><td className="p-3">{value(field.current,field.unit)}</td><td className="p-3">{field.same?"같음":"다름"}</td></tr>)}</tbody></table></div>}</QueryGate>
      <Button variant="outline" onClick={()=>void settings.refetch()} disabled={settings.isFetching}>현재 설정 새로고침</Button><Link href="/configuration?section=capacity">용량 설정</Link>
    </section>
    <section className="space-y-3"><h3 className="font-semibold">탐색 판정</h3><p>응답의 실행 순서예요. 조기에 판정을 끝낼 수 있어 창당 요청 수를 전체 요청 수나 실측 성공률로 해석하지 않아요.</p>
      <Judgments title="격자 판정" rows={row.grid} passing={input.capacityPassingWindows} empty="기록된 격자 판정이 없어요"/>
      <Judgments title="이분탐색 판정" rows={row.bisection} passing={input.capacityPassingWindows} empty="이분탐색 기록이 없어요"/>
    </section>
    <section className="space-y-3"><h3 className="font-semibold">원본</h3><SourceJSON inline value={row}/></section>
  </div>
}

export function CapacityEvidence() {
  const params=useSearchParams(),router=useRouter(),client=useQueryClient(),{token}=useSession()
  const cached=client.getQueryState<CapacityView,ApiError>(["chill","v1/scheduler/capacity"])
  const query=useChillQuery<CapacityView>("v1/scheduler/capacity",{decoder:parseSchedulerJSON,poll:!cached?.error&&capacityPolling(cached?.data?.rows??[])})
  const settings=useChillQuery<SchedulerSettings>("v1/scheduler/settings",{decoder:parseSchedulerJSON})
  const workload=params.get("workload")??(params.get("open")?.startsWith("service:")?params.get("open")!.slice(8):""),key=params.get("row")??""
  const all=query.data?.rows??[],rows=capacityRows(all,workload),selected=selectedCapacityRow(rows,key)
  function select(row?:string) {
    const next=new URLSearchParams(params.toString());next.set("evidence","capacity")
    if(row)next.set("row",row);else next.delete("row")
    router.replace(`/?${next}`,{scroll:false})
  }
  const exportValue={source:"v1/scheduler/capacity",workload,rowKey:key,capacityReadAt:query.dataUpdatedAt,settingsReadAt:settings.dataUpdatedAt,response:query.data,settings:settings.error?undefined:settings.data}
  return <section className="space-y-4"><header className="flex flex-wrap items-center justify-between gap-2"><h3>인스턴스 용량 근거</h3><Button variant="outline" onClick={()=>void query.refetch()} disabled={!token||query.isFetching}>새로고침</Button></header>
    <QueryGate query={query}>{query.data&&<>
      <Facts items={[["용량 조회",checked(query.dataUpdatedAt)],["대상 UID",workload||"전체"]]}/>
      {params.has("row")?<><Button variant="outline" onClick={()=>select()}>용량 행 목록</Button>{selected?<CapacityDetails row={selected} settings={settings} capacityAt={query.dataUpdatedAt}/>:<p>항목 없음 · 선택 key 미확인</p>}</>:!rows.length?<Facts label="처리 용량" items={[["상태","처리 용량 근거 없음"],["조회 범위",workload?"선택 서비스":"전체 서비스"],["누락 사유","서버 미제공"],["계산 완료 예정","서버 미제공"]]}/>:<DataTable data={rows} columns={[
        {key:"workload",label:"서비스 UID",value:row=><button type="button" className="min-h-11 break-all text-left underline underline-offset-4" onClick={()=>select(row.key)}>{row.input.workloadUID}</button>},
        {key:"node",label:"장치",value:row=>row.input.node.name},
        {key:"power",label:"전력모드",value:row=>row.input.powerMode},
        {key:"profile",label:"프로파일",value:row=>row.input.profileID},
        {key:"batch",label:"최대 배치",unit:"건/batch",numeric:true,value:row=>String(row.input.maxBatch)},
        {key:"status",label:"상태·사유",value:row=><><StatusBadge tone={row.status==="Built"?"ok":row.status==="CapacityRejected"?"warn":"idle"}>{capacityStatus(row.status)}</StatusBadge>{row.reason&&<p>{capacityReason(row.reason)}</p>}</>},
        {key:"kappa",label:"처리 용량",unit:"req/s",numeric:true,value:row=>row.kappa??"판정 불가"},
        {key:"built",label:"계산 시각",value:row=>at(row.builtAt)},
        {key:"previous",label:"이전 결과",value:row=>row.previous?"다시 계산 중":"해당 없음"},
      ]}/>}
      <SourceExport value={exportValue} filename="chill-scheduler-capacity.json"/><SourceJSON inline value={query.data}/>
    </>}</QueryGate>
  </section>
}
