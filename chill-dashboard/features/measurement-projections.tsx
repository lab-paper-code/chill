"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { DispatchOccupancyMeasurement, MeasurementEvidenceLocation, MeasurementProjection, MeasurementProjectionList, OperationCostMeasurement, WireInteger } from "@/lib/read-types"
import { measurementReasonSummary, formatMeasurementTime, evidenceLocations, measurementHref, measurementListHref, measurementPath, measurementState, measurementUIDMismatch, occupancyDuration, occupancySupply, occupancyThroughput, operationDuration, operationWallEnergy, operationSteps, type OperationStep, type ProjectionMeasurementKind } from "@/lib/measurements"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON, StatusBadge } from "@/components/read-region"
import { Empty, Facts, RowDialog, Rows, Screen } from "@/components/screen"
import { DataTable, type DataColumn } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { MeasurementNavigation } from "@/features/measurement-coverage"

const measurementTitles = {"operation-cost-measurements":"동작 비용 측정","dispatch-occupancy-measurements":"분배 점유시간 측정"}
const measurementTime = formatMeasurementTime
function Phase({phase,quality=false}:{phase?:string;quality?:boolean}) {
  const state = measurementState(phase)
  return <span className="grid gap-1"><StatusBadge tone={quality?"warn":state.tone}>{quality?"품질 경고·사용 확인 필요":state.label}</StatusBadge>{quality&&<small>원본 상태: {phase??"미제공"}</small>}</span>
}
function OccupancyDuration({value}:{value?:WireInteger}) {
  return <span>{occupancyDuration(value)}</span>
}

export function ProjectionMeasurementListPage({kind}:{kind:ProjectionMeasurementKind}) {
  const params = useSearchParams(), {config} = useSession()
  const [namespaceOpen,setNamespaceOpen] = React.useState(false)
  const namespace = params.get("namespace") ?? config.namespace, path = measurementPath(kind,namespace), title = measurementTitles[kind]
  const probe = useChillQuery<MeasurementProjectionList>(path,{decoder:parseSchedulerJSON})
  const query = useChillQuery<MeasurementProjectionList>(path,{decoder:parseSchedulerJSON,poll:!probe.error && (probe.data?.items ?? []).some(item => item.status.phase === "Measuring")})
  const items = [...(query.data?.items ?? [])].sort((a,b) => b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp))
  const columns:DataColumn<MeasurementProjection>[] = [
    {key:"name",label:"이름",value:item => <Link className="underline underline-offset-4" href={measurementHref(kind,item.metadata)}>{item.metadata.name}</Link>,sortValue:item => item.metadata.name},
    {key:"node",label:"Node",value:item => item.node,sortValue:item => item.node},
    {key:"phase",label:"측정 상태",value:item => <Phase phase={item.status.phase} quality={item.kind==="DispatchOccupancyMeasurement"&&(occupancyDuration(item.status.perRequestNanos)+occupancyThroughput(item.status.throughput)).includes("품질 경고")}/>,sortValue:item => item.status.phase ?? ""},
    ...(kind === "operation-cost-measurements" ? [{key:"steps",label:"단계 기록",value:(item:MeasurementProjection) => item.kind === "OperationCostMeasurement" ? `기록 ${item.status.steps?.length ?? 0} / 계획 ${item.conditions.steps.length}단계` : "종류 확인 필요"}] : [
      {key:"workload",label:"워크로드",value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? item.conditions.workload : "종류 확인 필요"},
      {key:"batch",label:"배치",value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? `${item.conditions.batch}건/batch` : "종류 확인 필요"},
      {key:"d",label:"분배 점유시간 d",value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? <OccupancyDuration value={item.status.perRequestNanos}/> : "결과 없음"},
      {key:"x",label:"지속 완료 처리량 X",value:(item:MeasurementProjection) => item.kind === "DispatchOccupancyMeasurement" ? occupancyThroughput(item.status.throughput) : "결과 없음"},
    ]),
    {key:"created",label:"등록 시각 (KST)",value:item => <time title={item.metadata.creationTimestamp}>{measurementTime(item.metadata.creationTimestamp)}</time>,sortValue:item => item.metadata.creationTimestamp},
  ]
  return <Screen width="data" actionPlacement="header" back={[`/measurements?namespace=${encodeURIComponent(namespace)}`,"측정"]} title={title} lede="이 저장 영역의 측정 조건과 기록된 결과를 조회해요. 새 측정을 제출하지 않아요.">
    <MeasurementNavigation namespace={namespace}/>
    <Rows><RowDialog open={namespaceOpen} onOpenChange={setNamespaceOpen} label="저장 영역" value={namespace} title="저장 영역 선택"><NamespaceForm key={namespace} kind={kind} namespace={namespace} done={() => setNamespaceOpen(false)}/></RowDialog></Rows>
    <QueryGate query={query}>{query.data && <>
      <p className="text-sm text-muted-foreground">이 저장 영역에서 받은 {items.length}개</p>
      {items.length ? <DataTable key={`${kind}/${namespace}`} data={items} columns={columns}/> : <Empty title={`아직 등록된 ${title}이 없어요`}><p>저장 영역을 확인하거나 측정 화면으로 돌아가 주세요.</p><Link className="underline" href={`/measurements?namespace=${encodeURIComponent(namespace)}`}>측정으로 돌아가기</Link></Empty>}
      <p className="text-sm text-muted-foreground">마지막 조회 · {measurementTime(query.dataUpdatedAt)}</p>
      <Rows><RowDialog label="원본 자료" title={`${title} 목록 원본`} description="안전한 projection 응답이에요. 전체 CR이나 raw Evidence가 아니에요.">
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
  const probe = useChillQuery<MeasurementProjection>(path,{decoder:parseSchedulerJSON})
  const query = useChillQuery<MeasurementProjection>(path,{decoder:parseSchedulerJSON,poll:!probe.error && probe.data?.status.phase === "Measuring" && !measurementUIDMismatch(probe.data.metadata.uid,expectedUID)})
  const data = query.data, other = !!data && measurementUIDMismatch(data.metadata.uid,expectedUID), title = measurementTitles[kind]
  return <Screen width="data" actionPlacement="header" back={[measurementListHref(kind,namespace),title]} title={other ? "같은 이름으로 다시 등록된 다른 측정이에요" : title}
    status={other ? <StatusBadge tone="fail">다른 측정</StatusBadge> : data ? <Phase phase={data.status.phase} quality={data.kind==="DispatchOccupancyMeasurement"&&(occupancyDuration(data.status.perRequestNanos)+occupancyThroughput(data.status.throughput)).includes("품질 경고")}/> : undefined} lede={other ? "이전 자료로 사용할 수 없어요." : `${name} · ${namespace}`}>
    <MeasurementNavigation namespace={namespace}/>
    <QueryGate query={query}>{data && <>
      {!other && <>
        {data.status.reason && <div className="notice break-words"><p>측정 사유: {measurementReasonSummary(data.status.reason)}</p><details><summary className="min-h-11 cursor-pointer py-2">사유 원문</summary><p>{data.status.reason}</p></details></div>}
        <Facts label="측정 대상" items={[["측정 종류",data.kind],["Node",data.node],["등록 시각",measurementTime(data.metadata.creationTimestamp)]]}/>
        {data.kind === "OperationCostMeasurement" ? <OperationResult data={data}/> : <OccupancyResult data={data}/>}
      </>}
      <details><summary className="min-h-11 cursor-pointer py-2 text-sm">원본 CR 식별자</summary><Facts items={[["이름",data.metadata.name],["저장 영역",data.metadata.namespace],["UID",data.metadata.uid],["resourceVersion",data.metadata.resourceVersion],["apiVersion",data.references.cr.apiVersion],["kind",data.references.cr.kind],["참조 저장 영역 / 이름",`${data.references.cr.namespace} / ${data.references.cr.name}`],["참조 UID",data.references.cr.uid]]}/></details>
      {!other && <Rows label="원본"><RowDialog label="원본 자료" title={`${title} 원본`} description="조건과 저장된 결과의 안전한 projection이에요. 전체 CR이나 raw Evidence가 아니에요.">
        <SourceExport filename={`chill-${kind}-${name}.json`} value={{kind,namespace,name,expectedUID,queriedAt:new Date(query.dataUpdatedAt).toISOString(),units:{perRequestNanos:"ns/request",throughput:"req/s",durationSeconds:"s",durationNanos:"ns",wallJoules:"J (whole-node-wall)"},source:data}}/>
        <SourceJSON inline value={data}/>
      </RowDialog></Rows>}
      <p className="text-sm text-muted-foreground">마지막 조회 · {measurementTime(query.dataUpdatedAt)}</p>
    </>}</QueryGate>
    <Button variant="outline" disabled={query.isFetching} onClick={() => void query.refetch()}>다시 불러오기</Button>
  </Screen>
}

const stepKinds:Record<string,string> = {power:"전력모드 변경",prepare:"준비",idle:"유휴",remove:"제거"}
function BEOReference({namespace,name}:{namespace:string;name:string}) {
  return <Link className="underline underline-offset-4" href={`/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`}>{name} (현재 이름 기준 · UID 없는 참조)</Link>
}
function OperationResult({data}:{data:OperationCostMeasurement}) {
  const columns:DataColumn<OperationStep>[] = [
    {key:"name",label:"단계 이름",value:step => step.name},
    {key:"condition",label:"조건 연결",value:step => step.condition ? stepKinds[step.condition.kind] ?? "확인이 필요해요" : "조건 연결 없음"},
    {key:"duration",label:"실측 시간",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => <li key={index}>{operationDuration(result.durationNanos)}</li>)}</ul> : operationDuration()},
    {key:"energy",label:"수용된 전체 Node 벽에너지",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => <li key={index}>{operationWallEnergy(result.wallJoules)}</li>)}</ul> : operationWallEnergy()},
    {key:"result",label:"저장 결과",value:step => step.results.length ? <ul className="grid gap-2">{step.results.map((result,index) => {
      const locations = result.key ? evidenceLocations(data.references.evidence.filter(location => location.key === result.key)) : []
      return <li key={index} className="grid gap-1">
        <StatusBadge tone={result.verdict === "Accepted" ? "ok" : "warn"}>{result.verdict === "Accepted" ? "채택" : "확인이 필요해요"}</StatusBadge>
        <span className="break-words">사유: {result.reason ? measurementReasonSummary(result.reason) : "기록 없음"}</span>{result.reason&&<details><summary className="min-h-11 cursor-pointer py-2">사유 원문</summary><p className="break-words">{result.reason}</p></details>}
        <details><summary className="min-h-11 cursor-pointer py-2">근거 위치</summary><p>판정 원문: {result.verdict || "없음"}</p><p className="break-all">동작 ID: {result.operationID || "기록 없음"}</p>{locations.length ? <Locations locations={locations}/> : <p className="break-all">key: {result.key || "없음"} (위치 없음)</p>}</details>
      </li>
    })}</ul> : "아직 기록된 결과가 없어요"},
  ]
  return <>
    <p className="notice">실측 시간: {data.status.steps?.some(step=>step.durationNanos!==undefined&&step.durationNanos!==null)?"기록된 단계별 값 제공":"기록 없음"}. 수용된 전체 장비 에너지: {data.status.steps?.some(step=>step.wallJoules!==undefined&&step.wallJoules!==null)?"기록된 단계별 값 제공":"기록 없음"}. 계획 유휴 시간과 실제 동작 시간은 별도예요.</p>
    <p className="text-sm text-muted-foreground">기록 {data.status.steps?.length ?? 0} / 계획 {data.conditions.steps.length}단계 · 완료 상태만으로 단계 채택을 재판정하지 않아요.</p>
    <DataTable data={operationSteps(data)} columns={columns}/>
    <Rows label="자세히"><RowDialog label="순서 있는 동작 조건" title="순서 있는 동작 조건" description="조건에 기록된 순서예요. 단계별로 해당 동작의 조건만 표시해요.">
      <ol className="grid list-decimal gap-5 pl-5">{data.conditions.steps.map((step,index) => <li key={index}><Facts label={step.name} items={[
        ["동작 종류",stepKinds[step.kind] ?? "확인이 필요해요"],
        ...(step.kind === "power" ? [["목표 전력모드",step.powerMode ?? "기록되지 않았어요"] as [string,React.ReactNode]] : []),
        ...(step.kind === "prepare" || step.kind === "remove" ? [["측정 프로파일 참조",step.batchEnergyOptimization ? <BEOReference namespace={data.metadata.namespace} name={step.batchEnergyOptimization}/> : "기록되지 않았어요"] as [string,React.ReactNode]] : []),
        ...(step.kind === "idle" ? [["계획 유휴 시간 (실측 아님)",step.idleDurationSeconds === undefined ? "기록되지 않았어요" : `${step.idleDurationSeconds} s`] as [string,React.ReactNode]] : []),
      ]}/></li>)}</ol>
    </RowDialog><RowDialog label="근거 위치" title="논리 근거 위치" description="store와 key는 논리 위치예요. HTTP 다운로드 주소가 아니에요."><Locations locations={evidenceLocations(data.references.evidence)}/></RowDialog></Rows>
  </>
}
function OccupancyResult({data}:{data:DispatchOccupancyMeasurement}) {
  const c = data.conditions, s = data.status
  return <>
    <p>d는 배치 실행 외에 요청 1건이 인스턴스를 점유한 시간이에요. 현재 응답 지연과는 달라요.</p>
    <Facts label="측정 결과" items={[["요청당 분배 점유시간 d",<OccupancyDuration key="d" value={s.perRequestNanos}/>],["지속 완료 처리량 X",occupancyThroughput(s.throughput)],["결과 전력모드",s.powerMode || "결과 없음"]]}/>
    <Rows label="자세히"><RowDialog label="측정 조건" title="분배 점유시간 측정 조건" description="예열은 측정 시작에서 제외해요. 참조에 UID가 없어 현재 이름을 기준으로 연결해요.">
      <Facts items={[
        ["워크로드",<Link key="workload" className="underline underline-offset-4" href={`/workloads/${encodeURIComponent(c.workload)}`}>{c.workload} (현재 이름 기준 · UID 없는 참조)</Link>],
        ["측정 프로파일 참조",<BEOReference key="beo" namespace={data.metadata.namespace} name={c.batchEnergyOptimization}/>],
        ["Node",data.node],["배치",`${c.batch}건/batch`],["공급 방식",occupancySupply(c.supplyMode)],["동시 송신자",`${c.workers}명`],["예열",`${c.warmupSeconds} s (측정에서 제외)`],["측정 구간",`${c.durationSeconds} s`],
      ]}/>
    </RowDialog><RowDialog label="결과와 근거 위치" title="결과와 논리 근거 위치" description="store와 key는 논리 위치예요. HTTP 다운로드 주소가 아니에요."><Locations locations={evidenceLocations(data.references.evidence,s.result)}/></RowDialog></Rows>
  </>
}
function Locations({locations,empty="아직 저장된 근거 위치가 없어요"}:{locations:MeasurementEvidenceLocation[];empty?:string}) {
  return locations.length ? <ul className="grid gap-3">{locations.map((location,index) => <li key={index}><Facts items={[["store",location.store],["key",location.key]]}/></li>)}</ul> : <p>{empty}</p>
}

export function UnsupportedTransitionMeasurement() {
  return <Screen back={["/measurements","측정"]} title="전력모드 쌍의 전환 비용 원본은 아직 조회 API가 없어요." lede="동작 비용 측정과는 다른 자료예요."/>
}
