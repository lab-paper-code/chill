"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { DispatchOccupancyMeasurement, MeasurementEvidenceLocation, MeasurementProjection, MeasurementProjectionList, OperationCostMeasurement } from "@/lib/read-types"
import { formatMeasurementTime, evidenceLocations, measurementHref, measurementListHref, measurementPath, measurementUIDMismatch, measurementReasonSummary, occupancyDuration, occupancyThroughput, occupancySupply, operationDuration, operationWallEnergy, operationTotals, operationSteps, type OperationStep, type ProjectionMeasurementKind } from "@/lib/measurements"
import { operationStageLabel, operationStageLabels, largestOperationStages, operationDirection, operationModel, operationTransitionCosts } from "@/lib/operation-cost"
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
  const columns:DataColumn<MeasurementProjection>[] = [
    {key:"name",label:"측정 / 노드",value:item => <div className="break-words"><Link className="underline underline-offset-4" href={measurementHref(kind,item.metadata)}>{item.metadata.name}</Link><p className="text-xs">노드 {item.node}</p></div>,sortValue:item => item.metadata.name},
    {key:"phase",label:"측정 상태",value:item => <MeasurementStatus assessment={item.assessment}/>,sortValue:item => item.assessment?.label??""},
    ...(kind === "operation-cost-measurements" ? [
      {key:"transition",label:"전환 1회 비용 (유휴 제외)",value:(item:MeasurementProjection)=>item.kind==="OperationCostMeasurement"?<TransitionCosts data={item}/>:"미제공"},
    ] : [
      {key:"workload",label:"서비스",sortValue:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"?item.conditions.workload:undefined,value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? item.conditions.workload : "종류 확인 필요"},
      {key:"d",label:"분배 점유시간 (ms/요청)",sortValue:(item:MeasurementProjection)=>{if(item.kind!=="DispatchOccupancyMeasurement")return undefined;const value=item.status.perRequestNanos;return value!=null&&(typeof value==="bigint"||Number.isSafeInteger(value))&&value>=0?value:undefined},value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? occupancyDuration(item.status.perRequestNanos).replace(/ ms(?:\/요청)?$/,"") : "미제공"},
      {key:"x",label:"완료 처리량 (req/s)",sortValue:(item:MeasurementProjection)=>item.kind==="DispatchOccupancyMeasurement"&&item.status.throughput&&/ req\/s$/.test(occupancyThroughput(item.status.throughput))?Number(item.status.throughput):undefined,value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? occupancyThroughput(item.status.throughput).replace(/ req\/s$/,"") : "미제공"},
    ]),
  ]
  return <Screen width="data" actionPlacement="header" title={title} action={<Rows><RowDialog open={namespaceOpen} onOpenChange={setNamespaceOpen} label="저장 영역" value={namespace} title="저장 영역 선택"><NamespaceForm key={namespace} kind={kind} namespace={namespace} done={() => setNamespaceOpen(false)}/></RowDialog></Rows>}>
    <MeasurementNavigation active={kind} namespace={namespace}/>
    <MeasurementFilters axes={axes}/>
    <QueryGate query={query}>{query.data && <>
      <div className={kind==="operation-cost-measurements"?"measurement-operation-table":undefined}><DataTable key={`${kind}/${namespace}/${params}`} searchable={false} data={items} columns={columns}/></div>
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
  return rows.length?<OperationCostValues rows={rows}/>:<span>전환 단계 미제공</span>
}
function OperationCostValues({rows}:{rows:{step:OperationStep;result:OperationStep["results"][number]|undefined}[]}) {
  return <div role="table" aria-label="단계별 비용" className="grid min-w-0 grid-cols-[minmax(0,1fr)_max-content_max-content] gap-x-3 gap-y-1 text-xs tabular-nums">
    <div role="row" className="col-span-3 grid grid-cols-subgrid"><span role="columnheader">단계</span><span role="columnheader">시간 (s)</span><span role="columnheader">에너지 (J)</span></div>
    {rows.map(({step,result},index)=><div role="row" key={index} className="col-span-3 grid grid-cols-subgrid items-start"><span role="cell" className="break-words">{operationStageLabel(step)}{step.condition?.kind==="power"&&result?<span className="block">{result.fromPowerMode||"출발 미기록"} → {result.toPowerMode||"도착 미기록"}</span>:null}</span><span role="cell" className="text-right">{result?.durationNanos==null?"미제공":operationDuration(result.durationNanos,"s").replace(/ s$/,"")}</span><span role="cell" className="text-right">{result?.verdict==="Accepted"&&result.wallJoules!=null?operationWallEnergy(result.wallJoules).replace(/ J$/,""):"미제공"}</span></div>)}
  </div>
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
    {key:"duration",label:"실측 시간",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => <li key={index}>{operationDuration(result.durationNanos,"s",1)}</li>)}</ul> : "미제공"},
    {key:"energy",label:"전체 장비 에너지 (채택된 기록)",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => <li key={index}>{result.verdict==="Accepted"?operationWallEnergy(result.wallJoules,1):"채택된 에너지 없음"}</li>)}</ul> : "미제공"},
    {key:"result",label:"저장 결과",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => {
      const locations = result.key ? evidenceLocations(data.references.evidence.filter(location => location.key === result.key)) : []
      return <li key={index} className="grid gap-1">
        <p className="text-sm">{measurementReasonSummary(result.reason||result.verdict)}</p>
        <details><summary className="min-h-11 cursor-pointer py-2">단계 원본 기록</summary><p>판정 원문: {result.verdict || "없음"}</p><p className="break-words">원본 사유: {result.reason||"기록 없음"}</p><p className="break-all">동작 ID: {result.operationID || "기록 없음"}</p>{locations.length ? <Locations locations={locations}/> : <p className="break-all">key: {result.key || "없음"} (위치 없음)</p>}</details>
      </li>
    })}</ul> : "아직 기록된 결과가 없어요"},
  ]
  return <>
    <details><summary className="min-h-11 cursor-pointer py-2 text-sm">전체 시나리오 합계 (유휴 포함)</summary><Facts items={[["전체 시간",totals.durationNanos===null?"시간 기록 오류":operationDuration(totals.durationNanos,"s",1)],["채택 에너지",totals.wallJoules===null?"에너지 기록 오류":operationWallEnergy(totals.wallJoules,1)]]}/></details>
    <section className="grid gap-2" aria-label="전환 1회 비용"><h2 className="font-semibold">전환 1회 비용 (유휴 제외)</h2><TransitionCosts data={data}/></section>
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
    <Facts label="측정 결과" items={[["요청당 분배 점유시간 d",occupancyDuration(s.perRequestNanos)],["지속 완료 처리량 X",occupancyThroughput(s.throughput)],["결과 전력모드",s.powerMode || "결과 없음"]]}/>
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
