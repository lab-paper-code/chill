"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { TriangleAlert } from "lucide-react"
import { useChillQuery, useSession } from "@/lib/client"
import type { DispatchOccupancyMeasurement, MeasurementEvidenceLocation, MeasurementProjection, MeasurementProjectionList, OperationCostMeasurement } from "@/lib/read-types"
import { formatMeasurementTime, evidenceLocations, measurementHref, measurementListHref, measurementPath, measurementUIDMismatch, measurementReasonSummary, occupancyDuration, occupancyThroughput, occupancyReason, occupancySupply, operationTotals, operationSteps, type OperationStep, type ProjectionMeasurementKind } from "@/lib/measurements"
import { operationStageLabel, operationStageLabels, largestOperationStages, operationDirection, operationModel, operationTransitionCosts, operationCostTime, operationCostEnergy, operationCostOutliers, operationCostSummary, type OperationCostRow } from "@/lib/operation-cost"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts, RowDialog, Rows, Screen } from "@/components/screen"
import { DataTable, type DataColumn } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { AssessmentDetails, ResultRows } from "@/features/measurement-plans"
import { MeasurementFilters } from "@/features/measurement-coverage"

const measurementTitles = {"operation-cost-measurements":"동작 비용 측정","dispatch-occupancy-measurements":"분배 점유시간 측정"}
const measurementTime = formatMeasurementTime

export function ProjectionMeasurementListPage({kind}:{kind:ProjectionMeasurementKind}) {
  const params = useSearchParams(), {config} = useSession()
  const [namespaceOpen,setNamespaceOpen] = React.useState(false)
  const namespace = params.get("namespace") ?? config.namespace, path = measurementPath(kind,namespace), title = measurementTitles[kind]
  const query = useChillQuery<MeasurementProjectionList>(path,{decoder:parseSchedulerJSON})
  const all=query.data?.items??[]
  const axes=[{key:"node",label:"노드",values:all.map(item=>item.node)},...(kind==="dispatch-occupancy-measurements"?[{key:"workload",label:"서비스",values:all.flatMap(item=>item.kind==="DispatchOccupancyMeasurement"?[item.conditions.workload]:[])}]:[])]
  const items=all.filter(item=>(!params.get("q")||item.metadata.name.toLocaleLowerCase().includes(params.get("q")!.toLocaleLowerCase()))&&(!params.get("status")||item.assessment?.displayStatus===params.get("status"))&&(!params.get("node")||item.node===params.get("node"))&&(!params.get("workload")||(item.kind==="DispatchOccupancyMeasurement"&&item.conditions.workload===params.get("workload"))))
  const costs=new Map(items.flatMap(item=>item.kind==="OperationCostMeasurement"?[[item,operationCostSummary(operationSteps(item))] as const]:[]))
  const costValue=(item:MeasurementProjection,kind:"prepare"|"remove",metric:"duration"|"energy")=>{const values=costs.get(item as OperationCostMeasurement)?.[kind][metric];if(!values)return "—";const from=metric==="duration"?operationCostTime(Math.round(values[0])):operationCostEnergy(values[0]),to=metric==="duration"?operationCostTime(Math.round(values[1])):operationCostEnergy(values[1]);return from===to?from:`${from}–${to}`}
  const columns:DataColumn<MeasurementProjection>[] = [
    {key:"name",label:"측정",value:item => <Link className="break-all underline underline-offset-4" href={measurementHref(kind,item.metadata)}>{item.metadata.name}</Link>,sortValue:item => item.metadata.name},
    ...(kind === "operation-cost-measurements" ? [
      {key:"model",label:"모델",value:(item:MeasurementProjection)=>costs.get(item as OperationCostMeasurement)?.models.join(" / ")||"미기록"},
      {key:"device",label:"장치",value:(item:MeasurementProjection)=>item.node},
      {key:"prepareTime",label:"준비 범위 (s)",value:(item:MeasurementProjection)=>costValue(item,"prepare","duration")},
      {key:"prepareEnergy",label:"준비 범위 (J)",value:(item:MeasurementProjection)=>costValue(item,"prepare","energy")},
      {key:"removeTime",label:"제거 범위 (s)",value:(item:MeasurementProjection)=>costValue(item,"remove","duration")},
      {key:"removeEnergy",label:"제거 범위 (J)",value:(item:MeasurementProjection)=>costValue(item,"remove","energy")},
      {key:"attempts",label:"횟수 (회)",value:(item:MeasurementProjection)=>costs.get(item as OperationCostMeasurement)?.count??0},
      {key:"deviation",label:"편차",value:(item:MeasurementProjection)=>{const cost=costs.get(item as OperationCostMeasurement);return <div className="grid gap-1">{!!cost?.multiple&&<CostOutlier multiple={cost.multiple}/>} {cost?.insufficient?<span title="동일 장치·측정·모델/프로파일·동작별 유효 기록이 3개 미만"><TriangleAlert className="mr-1 inline size-3" aria-hidden="true"/>비교 표본 부족</span>:!cost?.multiple?"범위 이내":null}</div>}},
    ] : [
      {key:"node",label:"장치",value:(item:MeasurementProjection)=>item.node},
      {key:"workload",label:"서비스",sortValue:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"?item.conditions.workload:undefined,value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? item.conditions.workload : "종류 확인 필요"},
      {key:"reason",label:"사유",value:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"?<span title={item.status.reason||item.assessment?.reasons.map(reason=>reason.message||reason.code).join("\n")}>{occupancyReason(item)}</span>:"—"},
      {key:"d",label:"분배 점유시간 (ms/요청)",sortValue:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"&&occupancyDuration(item.status.perRequestNanos)!=="—"?item.status.perRequestNanos:undefined,value:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"?occupancyDuration(item.status.perRequestNanos):"—"},
      {key:"x",label:"완료 처리량 (req/s)",sortValue:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"&&occupancyThroughput(item.status.throughput)!=="—"?Number(item.status.throughput):undefined,value:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"?occupancyThroughput(item.status.throughput):"—"},
    ]),
    {key:"phase",label:"측정 상태",value:item => <MeasurementStatus assessment={item.assessment}/>,sortValue:item => item.assessment?.label??""},
  ]
  return <Screen width="data" actionPlacement="header" title={title} action={<Rows><RowDialog open={namespaceOpen} onOpenChange={setNamespaceOpen} label="저장 영역" value={namespace} title="저장 영역 선택"><NamespaceForm key={namespace} kind={kind} namespace={namespace} done={() => setNamespaceOpen(false)}/></RowDialog></Rows>}>
    <MeasurementNavigation active={kind} namespace={namespace}/>
    <MeasurementFilters axes={axes}/>
    <QueryGate query={query}>{query.data && <>
      <div className={kind==="operation-cost-measurements"?"measurement-operation-table":undefined}><DataTable key={`${kind}/${namespace}/${params}`} searchable={false} data={items} columns={columns}/></div>
      {kind==="operation-cost-measurements"&&<OperationCostLegend/>}
      <p className="text-sm text-muted-foreground">마지막 조회 · {measurementTime(query.dataUpdatedAt)}</p>
      <Rows><RowDialog label="원본 자료" title={`${title} 목록 원본`} description="조회 응답 JSON">
        <SourceExport filename={`chill-${kind}.json`} value={{kind,namespace,queriedAt:new Date(query.dataUpdatedAt).toISOString(),source:query.data}}/><SourceJSON inline value={query.data}/>
      </RowDialog></Rows>
    </>}</QueryGate>
    <Button variant="outline" disabled={query.isFetching} onClick={() => void query.refetch()}>다시 불러오기</Button>
  </Screen>
}
function NamespaceForm({kind,namespace,done}:{kind:ProjectionMeasurementKind;namespace:string;done:()=>void}) {
  const [value,setValue] = React.useState(namespace), router = useRouter()
  return <form className="grid gap-4" onSubmit={event => {event.preventDefault();router.push(measurementListHref(kind,value.trim()));done()}}>
    <label className="grid gap-1.5">저장 영역<Input value={value} onChange={event => setValue(event.target.value)} required pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?" maxLength={63}/></label><Button type="submit">이 저장 영역 보기</Button>
  </form>
}

export function ProjectionMeasurementDetail({kind,namespace,name}:{kind:ProjectionMeasurementKind;namespace:string;name:string}) {
  const expectedUID = useSearchParams().get("uid"), path = measurementPath(kind,namespace,name)
  const query = useChillQuery<MeasurementProjection>(path,{decoder:parseSchedulerJSON})
  const data = query.data, other = !!data && measurementUIDMismatch(data.metadata.uid,expectedUID), title = measurementTitles[kind]
  return <Screen width="data" actionPlacement="header" back={[measurementListHref(kind,namespace),title]} title={other ? "측정 식별자 불일치" : title}
    status={!other&&data?<MeasurementStatus assessment={data.assessment}/>:undefined} lede={other ? "동일 이름 · 다른 UID" : `${name} / ${namespace}`}>
    <MeasurementNavigation active={kind} namespace={namespace}/>
    <QueryGate query={query}>{data && <>
      {!other && <>
        <details><summary className="min-h-11 cursor-pointer py-2 text-sm">판정·측정 대상</summary><AssessmentDetails assessment={data.assessment} rows={data.resultRows}/>
        {!!data.resultRows?.length&&<ResultRows rows={data.resultRows} filename={`chill-${kind}-${name}.csv`}/>}
        <Facts label="측정 대상" items={[["측정 종류",measurementTitles[kind]],["노드",data.node],["등록 시각",measurementTime(data.metadata.creationTimestamp)]]}/></details>
        {data.kind === "OperationCostMeasurement" ? <OperationResult data={data}/> : <OccupancyResult data={data}/>}
      </>}
      <details><summary className="min-h-11 cursor-pointer py-2 text-sm">원본 CR 식별자</summary><Facts items={[["이름",data.metadata.name],["저장 영역",data.metadata.namespace],["UID",data.metadata.uid],["resourceVersion",data.metadata.resourceVersion],["apiVersion",data.references.cr.apiVersion],["kind",data.references.cr.kind],["참조 저장 영역 / 이름",`${data.references.cr.namespace} / ${data.references.cr.name}`],["참조 UID",data.references.cr.uid]]}/></details>
      {!other && <Rows label="원본"><RowDialog label="원본 자료" title={`${title} 원본`} description="조건·결과 조회 응답 JSON">
        <SourceExport filename={`chill-${kind}-${name}.json`} value={{kind,namespace,name,expectedUID,queriedAt:new Date(query.dataUpdatedAt).toISOString(),units:{perRequestNanos:"ns/request",throughput:"req/s",durationSeconds:"s",durationNanos:"ns",wallJoules:"J (whole-node-wall)"},source:data}}/>
        <SourceJSON inline value={data}/>
      </RowDialog></Rows>}
      <p className="text-sm text-muted-foreground">마지막 조회 · {measurementTime(query.dataUpdatedAt)}</p>
    </>}</QueryGate>
    <Button variant="outline" disabled={query.isFetching} onClick={() => void query.refetch()}>다시 불러오기</Button>
  </Screen>
}

function BEOReference({namespace,name}:{namespace:string;name:string}) {
  return <Link className="underline underline-offset-4" href={`/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`}>{name} (현재 이름 기준 / UID 없는 참조)</Link>
}
function TransitionCosts({data}:{data:OperationCostMeasurement}) {
  const rows=operationTransitionCosts(operationSteps(data))
  return rows.length?<OperationCostValues rows={rows} device={data.node}/>:<span>전환 단계 미제공</span>
}
function CostOutlier({multiple}:{multiple:number|undefined}) {
  return multiple===undefined?null:<span className="inline-flex max-w-full flex-wrap items-center justify-end gap-1 text-xs font-semibold" aria-label={`이상값 · 같은 측정·동작 중앙값의 ${multiple.toFixed(1)}배`}><TriangleAlert className="size-3 shrink-0" aria-hidden="true"/><span>{multiple.toFixed(1)}×</span></span>
}
function OperationCostValues({rows,device}:{rows:OperationCostRow[];device:string}) {
  const outliers=operationCostOutliers(rows)
  return <div className="operation-cost-values min-w-0 overflow-x-auto">
    <table className="operation-cost-core w-full text-xs tabular-nums">
      <thead><tr><th scope="col">단계 / 전환 방향</th><th scope="col">모델</th><th scope="col">장비</th><th scope="col">시간 (s)</th><th scope="col">에너지 (J)</th><th scope="col">편차</th></tr></thead>
      <tbody>{rows.map(({step,result},index)=><tr key={index}>
        <td className="break-words">{operationStageLabel(step)}<span className="block text-muted-foreground">{index+1}회{step.condition?.kind==="power"?` · ${result?.fromPowerMode||"출발 미기록"} → ${result?.toPowerMode||"도착 미기록"}`:""}</span></td>
        <td className="break-all">{result?.model||"모델 미기록"}</td>
        <td className="break-all">{device||"장비 미기록"}</td>
        <td className="text-right"><span className="grid justify-items-end gap-1">{operationCostTime(result?.durationNanos)}<CostOutlier multiple={outliers[index].duration}/></span></td>
        <td className="text-right"><span className="grid justify-items-end gap-1">{result?.verdict==="Accepted"?operationCostEnergy(result.wallJoules):"미제공"}<CostOutlier multiple={outliers[index].energy}/></span></td>
        <td>{outliers[index].insufficient?<span className="inline-flex items-center gap-1" title="동일 대상 유효 기록 3개 미만"><TriangleAlert className="size-3" aria-hidden="true"/>비교 표본 부족</span>:"—"}</td>
      </tr>)}</tbody>
    </table>
  </div>
}
function OperationCostLegend() {
  return <details><summary>비용·편차 범례</summary><table aria-label="비용·편차 범례" className="w-full text-left text-xs tabular-nums"><tbody><tr><th>목록 비용</th><td>측정 내 준비·제거 최솟값–최댓값 / 채택 에너지</td></tr><tr><th>횟수</th><td>유휴 제외 동작 결과 수</td></tr><tr><th>비교 대상</th><td>동일 장치·측정·모델/프로파일·전환 방향</td></tr><tr><th>이상값</th><td>동일 대상 유효 기록 3개 이상 / 중앙값의 3배 이상</td></tr><tr><th>⚠ 비교 표본 부족</th><td>동일 대상 유효 기록 3개 미만 / 이상 여부 미판정</td></tr></tbody></table></details>
}
function OperationTarget({step}:{step:OperationStep}) {
  const condition=step.condition
  if(condition?.kind==="power")return <p className="text-xs text-muted-foreground">{operationDirection(step)} · 계획 목표: {condition.powerMode||"미기록"}</p>
  if(condition?.kind==="prepare"||condition?.kind==="remove")return <p className="text-xs text-muted-foreground">{operationModel(step)} · 측정 프로파일: {condition.batchEnergyOptimization||"미기록"}</p>
  return null
}
function OperationResult({data}:{data:OperationCostMeasurement}) {
  const totals=operationTotals(data),steps=operationSteps(data),largest=largestOperationStages(steps)
  const columns:DataColumn<OperationStep>[] = [
    {key:"name",label:"단계 이름",value:step => <span>{operationStageLabel(step)}{largest.duration.includes(step)&&<strong className="block">최장 시간</strong>}{largest.energy.includes(step)&&<strong className="block">최대 에너지</strong>}<details><summary className="min-h-11 cursor-pointer py-2 text-xs">단계 코드</summary>{step.name}</details></span>},
    {key:"condition",label:"대상·전환 방향",value:step => <OperationTarget step={step}/>},
    {key:"duration",label:"실측 시간 (s)",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => <li key={index}>{operationCostTime(result.durationNanos)}</li>)}</ul> : "미제공"},
    {key:"energy",label:"전체 장비 에너지 (J · 채택)",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => <li key={index}>{result.verdict==="Accepted"?operationCostEnergy(result.wallJoules):"채택된 에너지 없음"}</li>)}</ul> : "미제공"},
    {key:"result",label:"저장 결과",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => {
      const locations = result.key ? evidenceLocations(data.references.evidence.filter(location => location.key === result.key)) : []
      return <li key={index} className="grid gap-1">
        <p className="text-sm">{measurementReasonSummary(result.reason||result.verdict)}</p>
        <details><summary className="min-h-11 cursor-pointer py-2">단계 원본 기록</summary><p>판정 원문: {result.verdict || "없음"}</p><p className="break-words">원본 사유: {result.reason||"기록 없음"}</p><p className="break-all">동작 ID: {result.operationID || "기록 없음"}</p>{locations.length ? <Locations locations={locations}/> : <p className="break-all">key: {result.key || "없음"} (위치 없음)</p>}</details>
      </li>
    })}</ul> : "아직 기록된 결과가 없어요"},
  ]
  return <>
    <details><summary className="min-h-11 cursor-pointer py-2 text-sm">전체 시나리오 합계 (유휴 포함)</summary><Facts items={[["전체 시간 (s)",totals.durationNanos===null?"시간 기록 오류":operationCostTime(totals.durationNanos)],["채택 에너지 (J)",totals.wallJoules===null?"에너지 기록 오류":operationCostEnergy(totals.wallJoules)]]}/></details>
    <section className="grid gap-2" aria-label="전환 1회 비용"><h2 className="font-semibold">전환 1회 비용 (유휴 제외)</h2><TransitionCosts data={data}/></section>
    <OperationCostLegend/>
    <details><summary className="min-h-11 cursor-pointer py-2 text-sm">단계별 기록과 사유 보기</summary><div className="measurement-operation-table measurement-operation-steps"><DataTable data={steps} columns={columns}/></div></details>
    <Rows label="자세히"><RowDialog label="순서 있는 동작 조건" title="순서 있는 동작 조건" description="저장된 단계 순서">
      <ol className="grid list-decimal gap-5 pl-5">{data.conditions.steps.map((step,index) => <li key={index}><Facts label={step.name} items={[
        ["동작 종류",operationStageLabels[step.kind] ?? "확인이 필요해요"],
        ...(step.kind === "power" ? [["목표 전력모드",step.powerMode ?? "기록되지 않았어요"] as [string,React.ReactNode]] : []),
        ...(step.kind === "prepare" || step.kind === "remove" ? [["측정 프로파일 참조",step.batchEnergyOptimization ? <BEOReference namespace={data.metadata.namespace} name={step.batchEnergyOptimization}/> : "기록되지 않았어요"] as [string,React.ReactNode]] : []),
        ...(step.kind === "idle" ? [["계획 유휴 시간 (실측 아님)",step.idleDurationSeconds === undefined ? "기록되지 않았어요" : `${step.idleDurationSeconds} s`] as [string,React.ReactNode]] : []),
      ]}/></li>)}</ol>
    </RowDialog><RowDialog label="근거 위치" title="논리 근거 위치" description="저장소 / 키"><Locations locations={evidenceLocations(data.references.evidence)}/></RowDialog></Rows>
  </>
}
function OccupancyResult({data}:{data:DispatchOccupancyMeasurement}) {
  const c = data.conditions, s = data.status
  return <>
    <table aria-label="분배 점유시간 결과" className="w-full text-left text-sm tabular-nums"><thead><tr>{["분배 점유시간 (ms/요청)","완료 처리량 (req/s)","결과 전력모드","사유"].map(label=><th key={label} className="p-3">{label}</th>)}</tr></thead><tbody><tr><td className="p-3">{occupancyDuration(s.perRequestNanos)}</td><td className="p-3">{occupancyThroughput(s.throughput)}</td><td className="p-3">{s.powerMode||"미기록"}</td><td className="p-3" title={s.reason}>{occupancyReason(data)}</td></tr></tbody></table>
    <Rows label="자세히"><RowDialog label="측정 조건" title="분배 점유시간 측정 조건" description="예열 제외 · 현재 이름 기준 참조">
      <Facts items={[
        ["워크로드",<Link key="workload" className="underline underline-offset-4" href={`/workloads/${encodeURIComponent(c.workload)}`}>{c.workload} (현재 이름 기준 / UID 없는 참조)</Link>],
        ["측정 프로파일 참조",<BEOReference key="beo" namespace={data.metadata.namespace} name={c.batchEnergyOptimization}/>],
        ["Node",data.node],["배치",`${c.batch}건/batch`],["공급 방식",occupancySupply(c.supplyMode)],["동시 송신자",`${c.workers}명`],["예열",`${c.warmupSeconds} s (측정에서 제외)`],["측정 구간",`${c.durationSeconds} s`],
      ]}/>
    </RowDialog><RowDialog label="결과와 근거 위치" title="결과와 논리 근거 위치" description="저장소 / 키"><Locations locations={evidenceLocations(data.references.evidence,s.result)}/></RowDialog></Rows>
  </>
}
function Locations({locations,empty="근거 위치 미제공"}:{locations:MeasurementEvidenceLocation[];empty?:string}) {
  return locations.length ? <ul className="grid gap-3">{locations.map((location,index) => <li key={index}><Facts items={[["store",location.store],["key",location.key]]}/></li>)}</ul> : <p>{empty}</p>
}

export function UnsupportedTransitionMeasurement() {
  return <Screen back={["/measurements","측정"]} title="전력모드 쌍의 전환 비용 원본은 아직 조회 API가 없어요." lede="동작 비용 측정과는 다른 자료예요."/>
}
