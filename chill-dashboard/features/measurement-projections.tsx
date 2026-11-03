"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { TriangleAlert } from "lucide-react"
import { useChillMutation, useChillQuery, useSession } from "@/lib/client"
import type { DispatchOccupancyMeasurement, MeasurementEvidenceLocation, MeasurementProjection, MeasurementProjectionList, OperationCostMeasurement } from "@/lib/read-types"
import { formatMeasurementTime, evidenceLocations, measurementPath, measurementUIDMismatch, measurementReasonSummary, occupancySupply, operationTotals, operationSteps, type OperationStep, type ProjectionMeasurementKind } from "@/lib/measurements"
import { operationStageLabel, operationStageLabels, largestOperationStages, operationDirection, operationModel, operationTransitionCosts, operationCostTime, operationCostEnergy, operationCostOutliers, operationCostSummary, type OperationCostRow, type OperationCostKind, type OperationCostSummary } from "@/lib/operation-cost"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { normalizeStatusLabel } from "@/lib/status"
import { StatusCell } from "@/components/status-cell"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts, Screen } from "@/components/screen"
import { DataTable, type DataColumn } from "@/components/data-table"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { MeasurementNavigation, measurementStates } from "@/components/measurement-navigation"
import { AssessmentDetails, ResultRows } from "@/features/measurement-plans"
import { MeasurementFilters } from "@/features/measurement-coverage"
import { SchedulerProfilesPage } from "@/features/scheduler-profiles"
import { occupancyDisplayReason, occupancyElapsed, occupancyMilliseconds, occupancyNumber, occupancyPhase, occupancyRunning, occupancyRequestHref } from "@/lib/dispatch-occupancy"

const resultKinds = [["throughput","처리 성능"],["operation-cost","변경 비용"],["dispatch-occupancy","분배 처리시간"]] as const
const projectionTitles = {"operation-cost-measurements":"변경 비용","dispatch-occupancy-measurements":"분배 처리시간"}

export function MeasurementResultsPage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const namespace=params.get("namespace")??config.namespace
  const kind=resultKinds.find(([key])=>key===params.get("kind"))?.[0]??"throughput"
  function changeKind(value:string) {
    const next=new URLSearchParams(params.toString())
    next.set("kind",value)
    for(const key of ["profile","measurement","uid","page","offset","sort","order","status","workload","deviceClass","powerMode","path","cpu"])next.delete(key)
    router.push(`/measurements/results?${next}`,{scroll:false})
  }
  return <Screen width="data" title="측정 결과">
    <MeasurementNavigation active="results" namespace={namespace}/>
    <div className="flex flex-wrap items-end gap-4">
      <label className="grid gap-1.5 text-sm">종류<select className="min-h-11 rounded-md border bg-background px-3" value={kind} onChange={event=>changeKind(event.target.value)}>{resultKinds.map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>
      <NamespaceForm key={namespace} namespace={namespace}/>
    </div>
    {kind==="throughput"?<SchedulerProfilesPage/>:<ProjectionResults key={`${kind}/${namespace}`} kind={kind==="operation-cost"?"operation-cost-measurements":"dispatch-occupancy-measurements"} namespace={namespace}/>}
  </Screen>
}

function NamespaceForm({namespace}:{namespace:string}) {
  const params=useSearchParams(),router=useRouter(),[value,setValue]=React.useState(namespace)
  return <form className="flex flex-wrap items-end gap-2" onSubmit={event=>{
    event.preventDefault()
    const next=new URLSearchParams(params.toString());next.set("namespace",value.trim())
    for(const key of ["profile","measurement","uid","page","offset"])next.delete(key)
    router.push(`/measurements/results?${next}`,{scroll:false})
  }}><label className="grid gap-1.5 text-sm">저장 영역<Input className="min-h-11" value={value} onChange={event=>setValue(event.target.value)} required pattern="[a-z0-9]([\-a-z0-9]*[a-z0-9])?" maxLength={63}/></label><Button variant="outline" type="submit">조회</Button></form>
}

function ProjectionResults({kind,namespace}:{kind:ProjectionMeasurementKind;namespace:string}) {
  const params=useSearchParams(),router=useRouter(),trigger=React.useRef<HTMLAnchorElement|null>(null)
  const path=measurementPath(kind,namespace)
  const query=useChillQuery<MeasurementProjectionList>(path,{decoder:parseSchedulerJSON,poll:data=>kind==="dispatch-occupancy-measurements"&&!!data?.items?.some(item=>item.kind==="DispatchOccupancyMeasurement"&&occupancyRunning(item))})
  const name=params.get("measurement"),expectedUID=params.get("uid")
  const detail=useChillQuery<MeasurementProjection>(measurementPath(kind,namespace,name??""),{enabled:!!name,decoder:parseSchedulerJSON,poll:(data,error)=>error?.status!==404&&data?.kind==="DispatchOccupancyMeasurement"&&occupancyRunning(data)})
  const all=query.data?.items??[]
  const axes=[{key:"node",label:"장치",values:all.map(item=>item.node)},...(kind==="operation-cost-measurements"?[{key:"model",label:"모델",values:all.flatMap(item=>item.kind==="OperationCostMeasurement"?operationSteps(item).flatMap(step=>step.results.flatMap(result=>result.model?[result.model]:[])):[])}]:[{key:"workload",label:"서비스",values:all.flatMap(item=>item.kind==="DispatchOccupancyMeasurement"?[item.conditions.workload]:[])}])]
  const items=all.filter(item=>(!params.get("q")||item.metadata.name.toLocaleLowerCase().includes(params.get("q")!.toLocaleLowerCase()))&&(!params.get("status")||item.assessment?.displayStatus===params.get("status"))&&(!params.get("node")||item.node===params.get("node"))&&(!params.get("model")||kind!=="operation-cost-measurements"||(item.kind==="OperationCostMeasurement"&&operationSteps(item).some(step=>step.results.some(result=>result.model===params.get("model")))))&&(!params.get("workload")||(item.kind==="DispatchOccupancyMeasurement"&&item.conditions.workload===params.get("workload"))))
  const selected=detail.data??all.find(item=>item.metadata.name===name)
  const other=!!selected&&measurementUIDMismatch(selected.metadata.uid,expectedUID)
  function href(item:MeasurementProjection) {
    const next=new URLSearchParams(params.toString());next.set("kind",kind==="operation-cost-measurements"?"operation-cost":"dispatch-occupancy");next.set("namespace",namespace);next.set("measurement",item.metadata.name);next.set("uid",item.metadata.uid)
    return `/measurements/results?${next}`
  }
  function select(item:MeasurementProjection,label:React.ReactNode) {
    return <Link className="min-w-0 whitespace-normal [overflow-wrap:anywhere] underline underline-offset-4" href={href(item)} scroll={false} onClick={event=>{trigger.current=event.currentTarget}}>{label}</Link>
  }
  const columns:DataColumn<DispatchOccupancyMeasurement>[]=[
    {key:"name",label:"측정",value:item=>select(item,item.metadata.name),sortValue:item=>item.metadata.name},
    {key:"node",label:"장치",value:item=>item.node,sortValue:item=>item.node},
    {key:"workload",label:"서비스",value:item=>item.conditions.workload,sortValue:item=>item.conditions.workload},
    {key:"batch",label:"배치",unit:"건",numeric:true,value:item=>item.batch??item.conditions.batch},
    {key:"d",label:"점유시간",unit:"ms/요청",numeric:true,value:item=><OccupancyValue value={occupancyMilliseconds(item.d?.perRequestNanos)} unverified={item.assessment?.displayStatus==="problem"||item.phase==="Failed"||item.phase==="Insufficient"}/>,sortValue:item=>item.d?.perRequestNanos??undefined},
    {key:"x",label:"완료 처리량",unit:"req/s",numeric:true,value:item=><OccupancyValue value={occupancyNumber(item.d?.throughput)} unverified={item.assessment?.displayStatus==="problem"||item.phase==="Failed"||item.phase==="Insufficient"}/>,sortValue:item=>item.d?.throughput??undefined},
    {key:"phase",label:"측정",value:item=><ProjectionStatus data={item}/>},
    {key:"elapsed",label:"경과",value:item=>occupancyElapsed(item)},
    {key:"reason",label:"사유",value:item=>occupancyDisplayReason(item)==="—"?"해당 없음":occupancyDisplayReason(item)},
  ]
  return <>
    <MeasurementFilters axes={axes}/>
    {kind==="dispatch-occupancy-measurements"&&<Button className="justify-self-start" asChild><Link href={occupancyRequestHref(namespace)}>d 측정 요청</Link></Button>}
    <QueryGate query={query}>{kind==="operation-cost-measurements"?<OperationCostTable items={items.filter((item):item is OperationCostMeasurement=>item.kind==="OperationCostMeasurement")} select={select}/>:<DataTable urlState key={`${namespace}/${params}`} searchable={false} data={items.filter((item):item is DispatchOccupancyMeasurement=>item.kind==="DispatchOccupancyMeasurement")} columns={columns}/>}</QueryGate>
    <Button className="justify-self-start" variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>
    {!!query.data&&<Facts items={[["조회 시각",formatMeasurementTime(query.dataUpdatedAt)]]}/>}
    <Sheet open={!!name} onOpenChange={open=>{if(!open){const next=new URLSearchParams(params.toString());next.delete("measurement");next.delete("uid");router.replace(`/measurements/results?${next}`,{scroll:false})}}}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-4xl" onCloseAutoFocus={event=>{if(trigger.current){event.preventDefault();trigger.current.focus()}}}>
        <SheetHeader><SheetTitle>{other?"측정 식별자 불일치":projectionTitles[kind]}</SheetTitle><SheetDescription className="break-all">{name} / {namespace}</SheetDescription></SheetHeader>
        <div className="grid min-w-0 gap-5 px-4 pb-6">
          <QueryGate query={detail}>{selected&&(other?<Facts items={[["요청 UID",expectedUID],["현재 UID",selected.metadata.uid]]}/>:<ProjectionResult data={selected}/>)}</QueryGate>
          <Button className="justify-self-start" variant="outline" disabled={detail.isFetching} onClick={()=>void detail.refetch()}>갱신</Button>
          {selected?.kind==="DispatchOccupancyMeasurement"&&!!detail.data&&!detail.error&&!other&&<DeleteOccupancy key={`${selected.metadata.uid}/${selected.metadata.resourceVersion}`} data={selected} refresh={()=>{void detail.refetch();void query.refetch()}} onDeleted={()=>{void query.refetch();const next=new URLSearchParams(params.toString());next.delete("measurement");next.delete("uid");router.replace(`/measurements/results?${next}`,{scroll:false})}}/>}
        </div>
      </SheetContent>
    </Sheet>
    <QueryGate query={query}><details><summary className="min-h-11 cursor-pointer py-2">목록 원본</summary><SourceExport filename={`chill-${kind}.json`} value={{kind,namespace,queriedAt:new Date(query.dataUpdatedAt).toISOString(),source:query.data}}/><SourceJSON inline value={query.data}/></details></QueryGate>
  </>
}

type CostListRow={measurement:OperationCostMeasurement;operation:OperationCostKind|undefined;summary:OperationCostSummary}
function OperationCostTable({items,select}:{items:OperationCostMeasurement[];select:(item:MeasurementProjection,label:React.ReactNode)=>React.ReactNode}) {
  const rows:CostListRow[]=items.flatMap(measurement=>{const summary=operationCostSummary(operationSteps(measurement));return (summary.operationKinds.length?summary.operationKinds:[undefined]).map(operation=>({measurement,operation,summary}))})
  const firstInsufficient=rows.find(row=>!row.operation||row.summary[row.operation].insufficient)
  const columns:DataColumn<CostListRow>[]=[
    {key:"model",label:"모델",value:row=>select(row.measurement,(row.operation?row.summary[row.operation].models:row.summary.models).join(" / ")||(row.operation==="power"?"해당 없음":"미기록")),sortValue:row=>(row.operation?row.summary[row.operation].models:row.summary.models).join(" / ")},
    {key:"node",label:"장치",value:row=>row.measurement.node,sortValue:row=>row.measurement.node},
    {key:"operation",label:"동작",value:row=>row.operation?operationStageLabels[row.operation]:"미기록",sortValue:row=>row.operation},
    ...(["duration","energy"] as const).flatMap(metric=>([0,1] as const).map(index=>({key:`${metric}-${index}`,label:`${metric==="duration"?"시간":"에너지"} ${index===0?"중앙값":"최대"}`,unit:metric==="duration"?"s":"J",numeric:true,value:(row:CostListRow)=>{const value=row.operation?row.summary[row.operation][metric]?.[index]:undefined;return value===undefined?"미제공":metric==="duration"?operationCostTime(Math.round(value)):operationCostEnergy(value)},sortValue:(row:CostListRow)=>row.operation?row.summary[row.operation][metric]?.[index]:undefined}))),
    {key:"samples",label:"표본 수 (시간 / 에너지)",unit:"개",numeric:true,value:row=>row.operation?`${row.summary[row.operation].durationSamples} / ${row.summary[row.operation].energySamples}`:"미기록"},
    {key:"phase",label:"측정",value:row=><ProjectionStatus data={row.measurement}/>},
    {key:"source",label:"측정·편차",value:row=><div className="grid min-w-0 gap-1">{select(row.measurement,row.measurement.metadata.name)}<CostMarkers summary={row.summary} operation={row.operation} explainInsufficient={row===firstInsufficient}/></div>},
  ]
  return <DataTable urlState searchable={false} data={rows} columns={columns}/>
}
function CostMarkers({summary,operation,explainInsufficient}:{summary:OperationCostSummary;operation:OperationCostKind|undefined;explainInsufficient:boolean}) {
  const markers=operation==="prepare"||operation==="remove"?summary.maximumOutliers[operation]:{}
  return <>{Object.entries(markers).filter(([,multiple])=>multiple!==undefined).map(([metric,multiple])=><span key={metric} className="text-xs text-destructive">{metric==="duration"?"시간":"에너지"} <CostOutlier multiple={multiple}/></span>)}{operation==="power"&&!!summary.powerMultiple&&<span className="text-xs text-destructive">전력 전환 <CostOutlier multiple={summary.powerMultiple}/></span>}{(!operation||summary[operation].insufficient)&&<span className="text-xs text-muted-foreground">{explainInsufficient?"판정 불가 · 비교 표본 부족":"표본 부족"}</span>}</>
}
function ProjectionStatus({data}:{data:MeasurementProjection}) {
  if(data.kind==="DispatchOccupancyMeasurement") {
    const phase=data.phase
    return <StatusCell axis="측정" label={occupancyPhase(phase)} tone={phase==="Failed"||phase==="Insufficient"?"fail":phase==="Complete"?"ok":phase==="Measuring"?"info":"idle"}/>
  }
  const status=data.assessment?.displayStatus??"unknown"
  return <StatusCell axis="측정" label={normalizeStatusLabel(measurementStates[status])} tone={status==="problem"?"fail":status==="usable"?"ok":status==="running"?"info":"idle"}/>
}
function OccupancyValue({value,unverified=false}:{value:string;unverified?:boolean}) {return <span className={`inline-flex items-center gap-1 tabular-nums ${unverified?"font-normal text-muted-foreground":""}`}>{unverified&&<><TriangleAlert className="size-3 shrink-0" aria-hidden="true"/><span className="sr-only">미검증 원자료 · </span></>}{value==="—"?"미제공":value}</span>}
function CostOutlier({multiple}:{multiple:number|undefined}) {
  return multiple===undefined?null:<span className="inline-flex items-center gap-1 text-xs font-semibold tabular-nums" aria-label={`이상값 · 같은 측정·동작 중앙값의 ${multiple.toFixed(1)}배`}><TriangleAlert className="size-3 shrink-0" aria-hidden="true"/>{multiple.toFixed(1)}×</span>
}

function ProjectionResult({data}:{data:MeasurementProjection}) {
  return <>
    <ProjectionStatus data={data}/>
    {data.kind==="OperationCostMeasurement"?<OperationResult data={data}/>:<OccupancyResult data={data}/>}
    {!!data.resultRows?.length&&<ResultRows rows={data.resultRows} filename={`chill-${data.metadata.name}.csv`}/>}
    <details><summary className="min-h-11 cursor-pointer py-2">판정 근거</summary><AssessmentDetails assessment={data.assessment} rows={data.resultRows}/></details>
    <Facts items={[["측정",data.metadata.name],["저장 영역",data.metadata.namespace],["장치",data.node],["등록 시각",formatMeasurementTime(data.metadata.creationTimestamp)]]}/>
    <details><summary className="min-h-11 cursor-pointer py-2">원본·근거 위치</summary>
      <Facts items={[["UID",data.metadata.uid],["resourceVersion",data.metadata.resourceVersion],["apiVersion",data.references.cr.apiVersion],["kind",data.references.cr.kind],["참조 저장 영역 / 이름",`${data.references.cr.namespace} / ${data.references.cr.name}`],["참조 UID",data.references.cr.uid]]}/>
      <Locations locations={evidenceLocations(data.references.evidence,data.kind==="DispatchOccupancyMeasurement"?data.status.result:undefined)}/>
      <SourceExport filename={`chill-${data.metadata.name}.json`} value={{units:{perRequestNanos:"ns/request",throughput:"req/s",durationNanos:"ns",wallJoules:"J (whole-node-wall)"},source:data}}/>
      <SourceJSON inline value={data}/>
    </details>
  </>
}
function BEOReference({namespace,name}:{namespace:string;name:string}) {
  return <Link className="break-all underline underline-offset-4" href={`/measurements/requests?${new URLSearchParams({namespace,request:name})}`}>{name}</Link>
}
function OperationCostValues({rows,device}:{rows:OperationCostRow[];device:string}) {
  const outliers=operationCostOutliers(rows)
  return <table className="w-full table-fixed text-left text-xs [&_th]:break-words [&_th]:p-2 [&_td]:p-2 [&_td]:align-top [&_td]:[overflow-wrap:anywhere] max-sm:[&_thead]:hidden max-sm:[&_tr]:block max-sm:[&_td]:grid max-sm:[&_td]:grid-cols-2 max-sm:[&_td]:gap-2 max-sm:[&_td]:before:content-[attr(data-label)]">
    <thead><tr><th scope="col">동작·전환 방향</th><th scope="col">모델</th><th scope="col">장치</th><th scope="col" className="text-right">시간 (s)</th><th scope="col" className="text-right">에너지 (J · 채택)</th><th scope="col">편차</th></tr></thead>
    <tbody>{rows.map(({step,result},index)=><tr key={index} className="border-b">
      <td data-label="동작·전환 방향"><span>{operationStageLabel(step)}{step.condition?.kind==="power"&&<span className="block text-muted-foreground">{result?.fromPowerMode||"미기록"} → {result?.toPowerMode||"미기록"}</span>}</span></td>
      <td data-label="모델">{result?.model||(["power","idle"].includes(step.condition?.kind??"")?"해당 없음":"미기록")}</td><td data-label="장치">{device||"미기록"}</td>
      <td data-label="시간 (s)" className="text-right tabular-nums"><span>{result?.durationNanos==null?"미기록":operationCostTime(result.durationNanos)}<CostOutlier multiple={outliers[index].duration}/></span></td>
      <td data-label="에너지 (J · 채택)" className="text-right tabular-nums"><span>{result?.verdict==="Accepted"?result.wallJoules==null?"미기록":operationCostEnergy(result.wallJoules):"미제공"}<CostOutlier multiple={outliers[index].energy}/></span></td>
      <td data-label="편차">{outliers[index].insufficient?"표본 부족":"해당 없음"}</td>
    </tr>)}</tbody>
  </table>
}
function OperationRanges({steps,device}:{steps:OperationStep[];device:string}) {
  const summary=operationCostSummary(steps)
  return <section className="grid min-w-0 gap-3" aria-label="동작·장치별 중앙값–최대 범위"><h3 className="font-semibold">중앙값–최대 범위 · {device}</h3>
    <p className="text-xs text-muted-foreground">각 막대는 같은 동작·장치의 실측 중앙값부터 최대값까지입니다. 시간과 에너지는 별도 척도입니다.</p>
    <table className="w-full table-fixed text-left text-xs [&_th]:break-words [&_td]:break-words [&_th]:p-2 [&_td]:p-2 max-sm:[&_thead]:hidden max-sm:[&_tr]:grid max-sm:[&_tr]:grid-cols-2 max-sm:[&_th]:col-span-2 max-sm:[&_td]:before:block max-sm:[&_td]:before:content-[attr(data-label)]"><thead><tr><th>동작</th><th>지표</th><th>중앙값</th><th>최대</th><th>범위</th></tr></thead><tbody>{summary.operationKinds.flatMap(kind=>(["duration","energy"] as const).map(metric=>{
      const values=summary[kind][metric],unit=metric==="duration"?"s":"J",format=metric==="duration"?(value:number)=>operationCostTime(Math.round(value)):operationCostEnergy
      const label=`${operationStageLabels[kind]} · ${device} · ${metric==="duration"?"시간":"에너지"}`
      const start=values&&values[1]>0?values[0]/values[1]*100:0
      return <tr key={`${kind}/${metric}`} className="border-b"><th scope="row">{operationStageLabels[kind]}</th><td data-label="지표" className="max-sm:col-span-2">{metric==="duration"?"시간":"에너지"} ({unit})</td><td data-label="중앙값" className="tabular-nums">{values?format(values[0]):"미제공"}</td><td data-label="최대" className="tabular-nums">{values?format(values[1]):"미제공"}</td><td data-label="범위" className="max-sm:col-span-2">{values?<div role="img" aria-label={`${label}: 중앙값 ${format(values[0])}, 최대 ${format(values[1])} ${unit}`} className="relative h-3 w-full rounded bg-muted"><span className="absolute inset-y-0 right-0 min-w-px rounded bg-primary" style={{left:`${start}%`}}/></div>:"미제공"}</td></tr>
    }))}</tbody></table>
    {summary.insufficient&&<p className="text-xs text-muted-foreground">판정 불가 · 비교 표본 부족 — 범위는 실측 분포이며 품질 판정이 아닙니다.</p>}
  </section>
}
function OperationTarget({step}:{step:OperationStep}) {
  const condition=step.condition
  if(condition?.kind==="power")return <span>{operationDirection(step)}<span className="block text-xs">계획 목표: {condition.powerMode||"미기록"}</span></span>
  if(condition?.kind==="prepare"||condition?.kind==="remove")return <span>{operationModel(step)}<span className="block text-xs">측정 프로파일: {condition.batchEnergyOptimization||"미기록"}</span></span>
  return <span>해당 없음</span>
}
function OperationResult({data}:{data:OperationCostMeasurement}) {
  const totals=operationTotals(data),steps=operationSteps(data),largest=largestOperationStages(steps),rows=operationTransitionCosts(steps)
  const columns:DataColumn<OperationStep>[]=[
    {key:"name",label:"단계",value:step=><span>{operationStageLabel(step)}{largest.duration.includes(step)&&<strong className="block">최장 시간</strong>}{largest.energy.includes(step)&&<strong className="block">최대 에너지</strong>}<span className="block text-xs">{step.name}</span></span>},
    {key:"condition",label:"대상·전환 방향",value:step=><OperationTarget step={step}/>},
    {key:"duration",label:"실측 시간",unit:"s",numeric:true,value:step=>step.results.length?<ul className="grid gap-2">{step.results.map((result,index)=><li key={index}>{result.durationNanos==null?"미기록":operationCostTime(result.durationNanos)}</li>)}</ul>:"미기록"},
    {key:"energy",label:"전체 장치 에너지 (채택)",unit:"J",numeric:true,value:step=>step.results.length?<ul className="grid gap-2">{step.results.map((result,index)=><li key={index}>{result.verdict==="Accepted"?result.wallJoules==null?"미기록":operationCostEnergy(result.wallJoules):"미제공"}</li>)}</ul>:"미기록"},
    {key:"result",label:"저장 결과",value:step=>step.results.length?<ul className="grid gap-2">{step.results.map((result,index)=><li key={index}>{measurementReasonSummary(result.reason||result.verdict)}<details><summary className="min-h-11 cursor-pointer py-2">단계 원본</summary><Facts items={[["판정 원문",result.verdict||"미기록"],["원본 사유",result.reason||"미기록"],["동작 ID",result.operationID||"미기록"],["키",result.key||"미기록"]]}/><Locations locations={result.key?evidenceLocations(data.references.evidence.filter(location=>location.key===result.key)):[]}/></details></li>)}</ul>:"미기록"},
  ]
  return <>
    <section className="grid min-w-0 gap-2" aria-label="전환 1회 비용"><h2 className="font-semibold">전환 1회 비용</h2>{rows.length?<><OperationRanges steps={steps} device={data.node}/><details><summary className="min-h-11 cursor-pointer py-2">개별 전환 실측값·편차</summary><OperationCostValues rows={rows} device={data.node}/></details></>:<p>미기록</p>}</section>
    <details><summary className="min-h-11 cursor-pointer py-2">전체 시나리오 합계 (유휴 포함)</summary><Facts items={[["전체 시간 (s)",totals.durationNanos===null?"시간 기록 오류":operationCostTime(totals.durationNanos)],["채택 에너지 (J)",totals.wallJoules===null?"에너지 기록 오류":operationCostEnergy(totals.wallJoules)]]}/></details>
    <details><summary className="min-h-11 cursor-pointer py-2">단계별 기록·사유</summary><DataTable data={steps} columns={columns}/></details>
    <details><summary className="min-h-11 cursor-pointer py-2">동작 조건</summary><ol className="grid list-decimal gap-5 pl-5">{data.conditions.steps.map((step,index)=><li key={index}><Facts label={step.name} items={[["동작",operationStageLabels[step.kind]??"미확인"],...(step.kind==="power"?[["목표 전력모드",step.powerMode??"미기록"] as [string,React.ReactNode]]:[]),...(["prepare","remove"].includes(step.kind)?[["측정 프로파일 참조",step.batchEnergyOptimization?<BEOReference namespace={data.metadata.namespace} name={step.batchEnergyOptimization}/>:"미기록"] as [string,React.ReactNode]]:[]),...(step.kind==="idle"?[["계획 유휴 시간 (s · 실측 아님)",step.idleDurationSeconds??"미기록"] as [string,React.ReactNode]]:[])]}/></li>)}</ol></details>
  </>
}
function OccupancyResult({data}:{data:DispatchOccupancyMeasurement}) {
  const c=data.conditions,s=data.status,result=data.d,phase=data.phase,unverified=data.assessment?.displayStatus==="problem"||phase==="Failed"||phase==="Insufficient"
  return <>
    <table aria-label="분배 처리시간 결과" className="w-full table-fixed text-left text-sm [&_th]:p-2 [&_th]:[overflow-wrap:anywhere] [&_td]:p-2 [&_td]:[overflow-wrap:anywhere]">{unverified&&<caption className="pb-2 text-left text-sm text-muted-foreground"><TriangleAlert className="mr-1 inline size-4" aria-hidden="true"/>미검증 원자료 · 채택값 아님</caption>}<thead><tr><th className="text-right">d (ms/요청)</th><th className="text-right">완료 처리량 (req/s)</th><th className="text-right">유휴 비율 (%)</th><th>판정</th></tr></thead><tbody><tr><td className="text-right tabular-nums"><OccupancyValue value={occupancyMilliseconds(result?.perRequestNanos)} unverified={unverified}/></td><td className="text-right tabular-nums">{occupancyNumber(result?.throughput)}</td><td className="text-right tabular-nums">{occupancyNumber(result?.idleFraction,true)}</td><td>{result?.verdict?measurementReasonSummary(result.verdict):"미제공"}</td></tr></tbody></table>
    <Facts items={[["사유",occupancyDisplayReason(data)],["경과",occupancyElapsed(data)],["시작",data.measurementWindow?formatMeasurementTime(data.measurementWindow.start):"미기록"],["종료",data.measurementWindow?formatMeasurementTime(data.measurementWindow.end):"미기록"],["결과 전력모드",s.powerMode||"미기록"]]}/>
    <details><summary className="min-h-11 cursor-pointer py-2">측정 조건</summary><Facts items={[["서비스",<Link key="workload" className="break-all underline underline-offset-4" href={`/workloads/${encodeURIComponent(c.workload)}`}>{c.workload}</Link>],["측정 프로파일 참조",<BEOReference key="beo" namespace={data.metadata.namespace} name={c.batchEnergyOptimization}/>],["프로파일 ID",data.profileID||"미기록"],["장치",data.node],["배치 (건)",c.batch],["공급 방식",occupancySupply(c.supplyMode)],["동시 송신자 (명)",c.workers],["예열 (s · 측정 제외)",c.warmupSeconds],["측정 구간 (s)",c.durationSeconds],["참조 UID","미기록"]]}/></details>
  </>
}
function DeleteOccupancy({data,refresh,onDeleted}:{data:DispatchOccupancyMeasurement;refresh:()=>void;onDeleted:()=>void}) {
  const mutation=useChillMutation(),[open,setOpen]=React.useState(false),[understood,setUnderstood]=React.useState(false),[unknown,setUnknown]=React.useState(false)
  const blocked=data.phase==="Measuring",identity=data.metadata
  const error=mutation.error
  function remove() {
    if(blocked||!understood||unknown||!identity.uid||!identity.resourceVersion)return
    mutation.mutate({path:`${measurementPath("dispatch-occupancy-measurements",identity.namespace,identity.name)}?${new URLSearchParams({uid:identity.uid})}`,method:"DELETE",version:identity.resourceVersion},{onSuccess:()=>{setOpen(false);onDeleted()},onError:failure=>{setOpen(false);if(failure.unknownOutcome||failure.status===0)setUnknown(true)}})
  }
  return <section className="grid gap-3" aria-label="측정 삭제"><Button className="justify-self-start" variant="destructive" disabled={blocked||mutation.isPending||unknown||!identity.uid||!identity.resourceVersion} onClick={()=>setOpen(true)}>{blocked?"측정 중":"삭제"}</Button>{error&&<div role="alert"><p>{unknown?"삭제 결과 미확인 · 재삭제 차단":error.status===409?"삭제 충돌 · 측정 상태가 변경되었거나 측정 중입니다. 최신 상태를 조회하세요.":"삭제 실패"}</p><Button variant="outline" onClick={refresh}>최신 상태 조회</Button><SourceJSON value={error.data??error.body} title="삭제 사유 원문"/></div>}<Dialog open={open} onOpenChange={setOpen}><DialogContent><DialogHeader><DialogTitle>측정을 영구히 삭제할까요?</DialogTitle><DialogDescription>이 측정 기록과 자체 근거 파일을 삭제합니다. 되돌릴 수 없습니다.</DialogDescription></DialogHeader><Facts items={[["측정",identity.name],["UID",identity.uid]]}/><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={understood} disabled={mutation.isPending||unknown} onChange={event=>setUnderstood(event.target.checked)}/>근거 파일까지 영구 삭제됨을 확인했습니다.</label><Button variant="destructive" disabled={!understood||blocked||mutation.isPending||unknown} onClick={remove}>{mutation.isPending?"삭제 중":"삭제"}</Button></DialogContent></Dialog></section>
}
function Locations({locations}:{locations:MeasurementEvidenceLocation[]}) {
  return locations.length?<ul className="grid gap-3">{locations.map((location,index)=><li key={index}><Facts items={[["저장소",location.store],["키",location.key]]}/></li>)}</ul>:<p>미제공</p>
}
export function UnsupportedTransitionMeasurement() {
  return <Screen back={["/measurements/results?kind=operation-cost","측정 결과"]} title="전력모드 전환 비용"><StatusCell axis="측정" label="지원 안 함" tone="idle" reason="전력모드 쌍 원본 조회 API 미제공"/></Screen>
}
