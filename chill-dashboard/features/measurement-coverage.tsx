"use client"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef, useState } from "react"
import { ApiError, isCoverageWarming, useChillQuery, useSession } from "@/lib/client"
import { coverageQuality, coverageQualityReasons, coverageFilterVisible, coverageSupportReasons, coverageMeasurementStatus, coverageMeasurementStates, coverageFailureHref, measurementAge, coverageSelection, coveragePathVisible, coverageWatts, coverageModes, coverageVariant, coverageProgress, coverageBatchState, coverageRequestHref, coverageScopeCounts, deviceLabel, conditionLabel } from "@/lib/measurement-coverage"
import type { BatchProgress, OccupancyObservations, OccupancyStatistics, InventoryView, MeasurementCoverageSummaryResponse as MeasurementCoverageResponse, MeasurementCoverageCell } from "@/lib/read-types"
import { useCoverageInventory, useMeasurementPlans, useMeasurementCoverage, useCoverageProfiles, useCoverageRuns } from "@/hooks/use-measurement-data"
import { MeasurementFilters } from "@/components/measurement-filters"
import { parseMeasurementJSON } from "@/lib/measurement-summary"
import { MeasurementNavigation, MeasurementEvidenceChecks } from "@/components/measurement-navigation"
import { measurementUsage, measurementSchedulerUsed, measurementQuality } from "@/lib/measurement-assessment"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
const coverageStates=coverageMeasurementStates
import { executionPathLabel, measurementReasonSummary, formatMeasurementNumber as number, formatMeasurementTime } from "@/lib/measurements"
import { coverageOccupancyRequestHref, occupancyStates, occupancySummary, occupancyMilliseconds, occupancyNumber, occupancyStatistic, occupancyMatches, occupancyObservationPath, occupancyObservationLimit, occupancyReasonLabel, occupancyResultHref, occupancyRequestHref } from "@/lib/dispatch-occupancy"
import "./measurement-coverage.css"
export function CoverageError({error,retry}:{error:ApiError|null;retry:()=>void}) {
  const summaryInvalid=(error?.data as {code?:unknown}|null)?.code==="MeasurementSummaryInvalid",warming=!!error&&isCoverageWarming(error)
  return <section role={error&&!warming?"alert":"status"} className="coverage-message"><p>{warming?"초기화 중":error?summaryInvalid?error.body:`커버리지 조회 실패 (HTTP ${error.status})`:"조회 중"}</p>{error&&!warming&&<><details><summary>오류 코드·원문</summary><pre className="whitespace-pre-wrap break-all">{error.body}</pre></details><Button variant="outline" onClick={retry}>다시 조회</Button><Link href="/measurements/requests">측정 요청 목록</Link></>}</section>
}
export function MeasurementCoveragePage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const query=useMeasurementCoverage(namespace),data=query.data,trigger=useRef<HTMLButtonElement|null>(null)
  const inventory=useCoverageInventory()
  const selected=data?.cells.find(cell=>cell.id===params.get("cell"))
  const selectedID=selected?.id
  const models=[...new Map((data?.cells??[]).map(cell=>[cell.model.uid,cell.model])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const model=params.get("model")??selected?.model.name??models[0]?.name
  function update(key:string,value:string) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);router.replace(`?${next}`,{scroll:false})}
  function close(){router.replace(`?${coverageSelection(params.toString(),selected,false)}`,{scroll:false});trigger.current?.focus()}
  function select(cell:MeasurementCoverageCell,button:HTMLButtonElement){trigger.current=button;const next=coverageSelection(params.toString(),cell,true);next.set("model",cell.model.name);router.push(`?${next}`,{scroll:false})}
  useEffect(()=>{
    if(!selectedID)return
    const buttons=document.querySelectorAll<HTMLButtonElement>(`[data-coverage-cell="${CSS.escape(selectedID)}"]`)
    const button=[...buttons].find(item=>item.getClientRects().length>0)
    button?.scrollIntoView({block:"nearest",inline:"nearest"})
  },[selectedID])
  const cells=(data?.cells??[]).filter(cell=>(!params.get("status")||coverageFilterVisible(cell,inventory.data,params.get("status")??"",params.get("supportReason")??""))&&cell.model.name===model&&
    (!params.get("deviceClass")||cell.deviceClass.name===params.get("deviceClass"))&&
    (!params.get("status")||params.get("status")==="unsupported"||coverageMeasurementStatus(cell,inventory.data)===params.get("status"))&&
    (!params.get("path")||cell.executionPath===params.get("path"))&&
    (!params.get("powerMode")||cell.powerMode===params.get("powerMode"))&&
    (!params.get("q")||`${cell.deviceClass.name} ${cell.executionPath} ${cell.powerMode}`.toLowerCase().includes(params.get("q")!.toLowerCase())))
  const classes=[...new Map(cells.map(cell=>[cell.deviceClass.uid,cell.deviceClass])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const batches=[...new Set([1,2,4,8,16,32,...(data?.scope.targetBatches??[])])].sort((a,b)=>a-b)
  const targets=params.get("batches")?.split(",").map(Number)??[]
  const filterLabels:Record<string,string>={deviceClass:"장치",path:"실행 경로",powerMode:"전력모드",status:"상태",supportReason:"지원 사유",node:"장비",q:"검색",batches:"목표 배치"}
  const activeFilters=Object.entries(filterLabels).flatMap(([key,label])=>{const value=params.get(key);return value?[[key,label,key==="status"?coverageStates[value as keyof typeof coverageStates]??"기타 상태":key==="path"?executionPathLabel(value):key==="deviceClass"?deviceLabel(value):key==="supportReason"?"선택한 사유":value]]:[]})
  return <Screen title="측정" width="data" actionPlacement="header" action={<div className="flex gap-2"><Button asChild><Link href={`/measurements/requests/new?${new URLSearchParams({namespace})}`}>측정 요청</Link></Button><Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button></div>}>
    <MeasurementNavigation active="coverage" namespace={namespace}/>
    {!data?<CoverageError error={query.isFetching?null:query.error} retry={()=>void query.refetch()}/>:<>
      <section className="coverage-controls" aria-label="모델·측정 범위">
      <nav className="coverage-models" aria-label="모델 선택">{models.map(item=><button key={item.uid} aria-current={item.name===model?"page":undefined} onClick={()=>update("model",item.name)}>{item.name}</button>)}</nav>
      <label className="coverage-mobile-model">모델<select value={model??""} onChange={event=>update("model",event.target.value)}>{models.map(item=><option key={item.uid} value={item.name}>{item.name}</option>)}</select></label>
      <div className="coverage-toolbar">
        <label className="grid gap-1 text-sm">장치<select className="h-11 rounded-md border bg-background px-2" value={params.get("deviceClass")??""} onChange={event=>update("deviceClass",event.target.value)}><option value="">전체 장치</option>{[...new Set(data.cells.filter(cell=>cell.model.name===model).map(cell=>cell.deviceClass.name))].sort().map(name=><option key={name} value={name}>{deviceLabel(name)}</option>)}</select></label>
        <details className="coverage-filter-panel"><summary className="min-h-11 cursor-pointer py-2">목표 배치·세부 필터</summary><div className="coverage-filter-content">
          <fieldset className="coverage-targets"><legend>목표 배치</legend><Button variant={!targets.length?"default":"outline"} onClick={()=>update("batches","")}>요청 범위</Button>{batches.map(batch=><Button key={batch} variant={targets.includes(batch)?"default":"outline"} aria-pressed={targets.includes(batch)} onClick={()=>update("batches",(targets.includes(batch)?targets.filter(value=>value!==batch):[...targets,batch]).sort((a,b)=>a-b).join(","))}>{batch}</Button>)}</fieldset>
          <MeasurementFilters axes={[{key:"path",label:"실행 경로",values:data.cells.filter(cell=>cell.model.name===model&&coveragePathVisible(cell,inventory.data)).map(cell=>cell.executionPath)},{key:"powerMode",label:"전력모드",values:data.cells.map(cell=>cell.powerMode)}]}/><label className="grid gap-3">장비<Input value={params.get("node")??""} onChange={event=>update("node",event.target.value)}/></label>
        </div></details>
      </div>
      {activeFilters.length>0&&<nav aria-label="선택한 필터" className="flex flex-wrap gap-2">{activeFilters.map(([key,label,value])=><Button key={key} variant="outline" onClick={()=>update(key,"")}>{label}: {value} · 해제</Button>)}<Button variant="ghost" onClick={()=>{const next=new URLSearchParams(params.toString());for(const key of [...Object.keys(filterLabels),"cell","variant","group"])next.delete(key);router.replace(`?${next}`,{scroll:false})}}>전체 해제</Button></nav>}
      <dl className="flex flex-wrap gap-x-6 gap-y-2 text-sm" aria-label="범위 기준"><div><dt>기준 시각 KST</dt><dd>{data.dataAsOf?formatMeasurementTime(data.dataAsOf):"미기록"}</dd></div><div><dt>신선도</dt><dd>{data.freshness==="fresh"?"최신":"오래된 관측"}</dd></div><div><dt>조회 갱신</dt><dd>{query.isFetching?"조회 중":"완료"}</dd></div></dl>
      <CoverageCounts cells={cells} inventory={inventory.data}/>
      </section>
      <div className="coverage-workspace">
        <div className="coverage-pivot" aria-label={`${model??""} 장치별 커버리지`}>
          {!classes.length&&<p className="p-4">조건 없음 · 필터 변경</p>}
          <div className="coverage-desktop-table"><table><caption className="sr-only">장치·전력모드별 실행 경로</caption><thead><tr><th scope="col">장치 · 전력모드</th>{[...new Set(cells.map(cell=>cell.executionPath))].sort().map((path,index)=><th key={path} scope="col">{executionPathLabel(path,index)}</th>)}</tr></thead><tbody>
          {classes.flatMap(cls=>{
            const deviceCells=cells.filter(cell=>cell.deviceClass.uid===cls.uid),paths=[...new Set(cells.map(cell=>cell.executionPath))].sort()
            const modes=coverageModes(deviceCells)
            const device=<><span>{deviceLabel(cls.name)}</span><small>{inventory.data?.classes.find(item=>item.uid===cls.uid)?.nodes.map(node=>node.name).join(", ")||"노드 미확인"}</small></>
            return modes.map((mode,index)=><tr key={`${cls.uid}/${mode}`}><th scope="row">{index===0&&device}<span>{mode}</span></th>{paths.map((path,pathIndex)=>{const cell=deviceCells.find(cell=>cell.powerMode===mode&&cell.executionPath===path);return <td key={path} data-path={executionPathLabel(path,pathIndex)}>{cell?<CoverageCell cell={cell} inventory={inventory.data} selected={cell.id===selected?.id} select={select}/>:<span>지원 여부 미확인</span>}</td>})}</tr>)
          })}
          </tbody></table></div>
        </div>
        <Sheet open={!!selected} onOpenChange={open=>{if(!open)close()}}><SheetContent className="w-full overflow-y-auto sm:w-[800px] sm:max-w-[calc(100vw-2rem)]" aria-describedby={undefined} onCloseAutoFocus={event=>{event.preventDefault();trigger.current?.focus()}}><SheetHeader><SheetTitle>측정 결과 상세</SheetTitle></SheetHeader>{selected&&<CoverageDetail cell={selected} namespace={namespace} close={close} visible={cells.some(cell=>cell.id===selected.id)}/>}</SheetContent></Sheet>
      </div>
      <section aria-label="다음 조치" className="grid gap-2"><details className="coverage-detail"><summary>다음 조치</summary><CoverageActions data={data} namespace={namespace}/></details>
      {data.sources.filter(source=>source.error).map(source=><p role="alert" key={source.kind}>{source.kind}: 조회하지 못한 자료가 있습니다 ({source.error?.code})</p>)}
      <details><summary className="min-h-11 cursor-pointer py-2">지원 불가 사유</summary><UnsupportedReasons cells={data.cells.filter(cell=>cell.model.name===model&&!coveragePathVisible(cell,inventory.data))}/></details>
      </section>
    </>}
  </Screen>
}
function CoverageCounts({cells,inventory}:{cells:MeasurementCoverageCell[];inventory?:InventoryView}) {
  const counts=coverageScopeCounts(cells,inventory)
  const allPolicyUnset=counts.policyUnsetCells>0&&counts.policyUnsetCells===counts.cells-counts.noEvidenceCells
  const metrics=[
    ["측정 있음 / 지원 조합",`${counts.measuredCells}/${counts.cells}`],
    ["배치 측정 / 목표",counts.targetBatches?`${counts.measuredBatches}/${counts.targetBatches}`:"목표 미제공"],
    ["현재 사용 / 지원 조합",`${counts.usedCells}/${counts.cells}`],
    ...(!allPolicyUnset?[["정책 미설정 / 측정 근거",`${counts.policyUnsetCells}/${counts.cells-counts.noEvidenceCells}`],["품질 미확인 / 측정 근거",`${counts.unknownQualityCells-counts.policyUnsetCells}/${counts.cells-counts.noEvidenceCells}`]]:[]),
    ["측정 근거 없음 (조합)",String(counts.noEvidenceCells)],
    ["문제 / 지원 조합",`${counts.failedCells}/${counts.cells}`],
    ["지원 안 함 (집계 제외)",String(counts.unsupportedCells)],
    ...(counts.missingTargetCells?[["목표 미제공 (조합)",String(counts.missingTargetCells)]]:[]),
  ]
  // Keep the common policy state separate from measured coverage and current use.
  const policyStatus=allPolicyUnset?<p role="status" title={`PolicyUnset · ${counts.policyUnsetCells}/${counts.cells-counts.noEvidenceCells} · 품질 판정 기준 미설정이며 측정 근거 부재나 현재 사용 차단을 뜻하지 않습니다.`}>전체 정책 미설정</p>:null
  return <div className="coverage-summary">{policyStatus}<table className="w-full text-left text-sm tabular-nums" aria-label="선택 범위 집계"><thead><tr>{metrics.map(([label])=><th key={label}>{label}</th>)}</tr></thead><tbody><tr>{metrics.map(([label,value])=><td key={label} data-label={label}>{value}</td>)}</tr></tbody></table><details><summary className="min-h-11 cursor-pointer py-2 text-sm">집계 축·품질 근거</summary><table className="w-full text-left text-sm"><tbody><tr><th scope="row">측정</th><td>표의 측정 있음·문제 상태와 동일한 조합 기준</td></tr><tr><th scope="row">현재 사용</th><td>스케줄러 참조 여부 · 품질 판정과 별개</td></tr><tr><th scope="row">실패 요청</th><td><Link className="underline" href="/measurements/requests?status=problem">요청별 실패 기록</Link></td></tr>{coverageQualityReasons(cells.filter(cell=>coveragePathVisible(cell,inventory))).map(reason=><tr key={reason.code}><th scope="row">{reason.label}</th><td>{reason.count} 조합</td></tr>)}</tbody></table></details></div>
}
function UnsupportedReasons({cells}:{cells:MeasurementCoverageCell[]}) {
  const params=useSearchParams(),groups=new Map<string,{label:string;count:number}>()
  for(const cell of cells)for(const reason of [...new Map(coverageSupportReasons(cell).map(reason=>[reason.code,reason])).values()]){
    const label=reason.code==="ArtifactArchitectureMismatch"?"모델·장치 아키텍처 불일치":measurementReasonSummary(reason.label||reason.message||reason.code)
    const group=groups.get(reason.code)
    if(group)group.count++;else groups.set(reason.code,{label,count:1})
  }
  return <table className="w-full text-left text-sm tabular-nums"><thead><tr><th>지원 불가 사유</th><th>조합 (개)</th></tr></thead><tbody>{[...groups].map(([key,group])=>{const query=new URLSearchParams(params.toString());query.set("status","unsupported");query.set("supportReason",key);for(const filter of ["deviceClass","path","powerMode","q","cell","variant"])query.delete(filter);return <tr key={key}><th className="font-normal"><Link className="inline-flex min-h-11 items-center text-primary underline" href={`/measurements?${query}`}>{group.label}</Link></th><td>{group.count}</td></tr>})}</tbody></table>
}
const batchSymbols={measured:"▧",running:"◷",missing:"□",unknown:"?",outside:"—"}
function BatchBar({progress}:{progress:BatchProgress|null}) {
  const batches=[...new Set([1,2,4,8,16,32,...(progress?.target??[])])].sort((a,b)=>a-b)
  return <span className="coverage-batch-bar" aria-label="배치별 측정 현황">{batches.map(batch=>{const state=coverageBatchState(progress,batch),label=`배치 ${batch}: ${{measured:"측정 있음",running:"진행",missing:"부족",unknown:"미확인",outside:"목표 밖"}[state]}`;return <span key={batch} className={`batch-${state}`} title={label} aria-label={label}><b aria-hidden="true">{batchSymbols[state]}</b>{batch}</span>})}</span>
}
function CoverageCell({cell,inventory,selected,select}:{cell:MeasurementCoverageCell;inventory?:InventoryView;selected:boolean;select:(cell:MeasurementCoverageCell,button:HTMLButtonElement)=>void}) {
  const progress=coverageProgress(cell)
  const state=coverageMeasurementStatus(cell,inventory),label=state==="unknown"&&cell.support.verdict==="Unknown"?"지원 여부 미확인":coverageStates[state]
  const measured=progress?.measured.filter(batch=>progress.target.includes(batch)).length??0
  return <button data-coverage-cell={cell.id} className={`coverage-cell ${cell.support.verdict!=="Compatible"?"coverage-cell-unconfirmed":""}`} aria-pressed={selected} aria-controls="coverage-selected-condition" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${executionPathLabel(cell.executionPath)} ${label} · ${occupancySummary(cell.occupancy)}`} onClick={event=>select(cell,event.currentTarget)}>
    <span>{label}</span>
    {progress&&progress.target.length>0&&<span className="coverage-mini-progress" aria-label="측정 배치 / 목표 배치"><progress max={progress.target.length} value={measured}/><span>{measured}/{progress.target.length}</span></span>}
    <small className="text-xs text-muted-foreground">{occupancySummary(cell.occupancy)}</small>
  </button>
}
export function CoverageActions({data,namespace}:{data:MeasurementCoverageResponse;namespace:string}) {
  const inventory=useCoverageInventory()
  const seen=new Set<string>()
  const actions=[...data.nextActions].sort((a,b)=>Number(b.kind==="failure")-Number(a.kind==="failure")).filter(action=>{const key=`${action.cellId}/${action.kind}/${action.reasonCode}`;if(seen.has(key))return false;seen.add(key);return true})
  return actions.length?<ul className="coverage-actions">{actions.map(action=>{
    const cell=data.cells.find(cell=>cell.id===action.cellId)
    if(!cell||!coveragePathVisible(cell,inventory.data))return null
    const quality=coverageQuality(cell)
    const failure=action.kind==="failure"?cell.failureSummary.find(failure=>failure.requestId===action.entityId)??cell.failureSummary[0]:undefined
    const selection=new URLSearchParams({namespace,model:cell.model.name,cell:cell.id,...(action.variantId?{variant:action.variantId}:{})})
    return <li key={`${cell.id}/${action.kind}/${action.reasonCode}`}><div className="grid gap-1 text-sm"><strong>{coverageStates[coverageMeasurementStatus(cell)]}</strong><span title={quality.reasons.map(reason=>reason.label).join(" / ")}>{quality.label}</span><span>{measurementUsage(cell.assessment)}</span></div><div><Link href={`/measurements?${selection}`}>{cell.model.name} / {deviceLabel(cell.deviceClass.name)} / {cell.powerMode} / {executionPathLabel(cell.executionPath)}</Link><p>{failure?measurementReasonSummary(failure.label||failure.message||failure.code):action.kind==="missing-batches"?"부족":coverageStates[coverageMeasurementStatus(cell)]}</p>{failure&&<Link href={coverageFailureHref(failure,namespace,`/measurements?${selection}`)}>{failure.runName?"실패 회차 열기":"실패 요청 열기"}</Link>}</div></li>
  })}</ul>:<p>확인된 추가 조치가 없습니다. 확인 불가 범위도 살펴보세요.</p>
}
export function OccupancyStatisticsFacts({statistics,pairs}:{statistics?:OccupancyStatistics|null;pairs?:number|null}) {
  return <dl className="grid gap-1 text-sm"><div><dt className="inline">중앙값 (ms/요청) </dt><dd className="inline tabular-nums">{occupancyStatistic(statistics?.median)}</dd></div><div><dt className="inline">IQR (ms/요청) </dt><dd className="inline tabular-nums">{occupancyStatistic(statistics?.iqr)}</dd></div><div><dt className="inline">p10 / p90 (ms/요청) </dt><dd className="inline tabular-nums">{occupancyStatistic(statistics?.p10)} / {occupancyStatistic(statistics?.p90)}</dd></div><div><dt className="inline">음수 비율 (%) </dt><dd className="inline tabular-nums">{occupancyNumber(statistics?.negativeFraction,true)}</dd></div><div><dt className="inline">쌍 (개) </dt><dd className="inline tabular-nums">{pairs??"미제공"}</dd></div></dl>
}
export function CoverageOccupancyPeriod({cell}:{cell:MeasurementCoverageCell}) {
  const adoptedWindow=cell.occupancy?.find(row=>row.schedulerD?.window)?.schedulerD?.window
  const [from,setFrom]=useState(adoptedWindow?.start??""),[to,setTo]=useState(adoptedWindow?.end??"")
  const [submitted,setSubmitted]=useState<string|null>(()=>adoptedWindow?occupancyObservationPath(adoptedWindow.start,adoptedWindow.end,undefined):null)
  const [maximumSeconds,setMaximumSeconds]=useState<number|undefined>(undefined)
  const [applied,setApplied]=useState<{path:string;data:OccupancyObservations}|null>(null)
  const path=occupancyObservationPath(from,to,maximumSeconds)
  const observations=useChillQuery<OccupancyObservations>(submitted??"v1/observations/occupancy",{enabled:!!submitted,decoder:parseMeasurementJSON})
  const maximum=occupancyObservationLimit(observations.error)
  if(maximum!==undefined&&maximum!==maximumSeconds)setMaximumSeconds(maximum)
  if(submitted&&observations.data&&!observations.error&&observations.data!==applied?.data)setApplied({path:submitted,data:observations.data})
  if(applied&&observations.error&&[401,403].includes(observations.error.status))setApplied(null)
  const matches=(applied?.data.rows??[]).filter(row=>cell.occupancy?.some(tuple=>occupancyMatches(row,cell,tuple)))
  return <div className="grid gap-3" aria-label="기간별 관측 d(B)">
    <h4>기간별 관측 d(B)</h4><p className="text-sm text-muted-foreground">스케줄러 d의 관측 구간을 기본 조회합니다. RFC3339 시각을 입력하세요.{maximumSeconds!==undefined&&` 최대 ${maximumSeconds} s`}</p>
    <div className="grid gap-3 sm:grid-cols-2"><label className="grid gap-1 text-sm">관측 시작<Input className="min-h-11" value={from} onChange={event=>setFrom(event.target.value)}/></label><label className="grid gap-1 text-sm">관측 종료<Input className="min-h-11" value={to} onChange={event=>setTo(event.target.value)}/></label></div>
    {from&&to&&!path&&<p role="alert">종료는 시작 이후이며 서버가 제공한 집계 구간 상한 이하여야 합니다.</p>}
    <Button className="justify-self-start" variant="outline" disabled={!path||observations.isFetching} onClick={()=>{if(path===submitted)void observations.refetch();else setSubmitted(path)}}>관측 조회</Button>
    {observations.error&&submitted&&<p role="status">관측 조회 실패 · HTTP {observations.error.status} · {observations.error.body} · 이전 조회 구간과 결과를 유지합니다.</p>}
    {observations.isFetching&&submitted&&<p role="status">관측 조회 중</p>}
    {applied&&<><p className="text-sm">적용 구간 {applied.data.window.start} / {applied.data.window.end}</p>{matches.length?<table className="coverage-results" aria-label="기간별 관측 통계"><thead><tr><th>Node / UID</th><th>프로파일 / 배치</th><th>관측 d(B)</th><th>런타임 이미지 (표시 전용)</th></tr></thead><tbody>{matches.map((row,index)=><tr key={index}><th scope="row">{row.node.name}<small className="block break-all">{row.node.uid}</small></th><td className="break-all">{row.profile.profileID} / {row.batch}</td><td><OccupancyStatisticsFacts statistics={row.statistics} pairs={row.pairs}/></td><td className="break-all">{row.runtimeImage||"미제공"}</td></tr>)}</tbody></table>:<p>선택 조건에 일치하는 관측 없음</p>}</>}
  </div>
}
function CoverageDetail({cell,namespace,close,visible}:{cell:MeasurementCoverageCell;namespace:string;close:()=>void;visible:boolean}) {
  const params=useSearchParams(),panel=useRef<HTMLDivElement>(null)
  const variant=coverageVariant(cell,params.get("variant")??undefined)
  const progress=variant?.batchProgress??coverageProgress(cell),rows=variant?.resultRows??[],returnTo=`/measurements?${params}`
  const requests=useMeasurementPlans(namespace,cell.failureSummary.some(failure=>!failure.runName))
  const profiles=useCoverageProfiles(cell,rows.length>0)
  const running=coverageMeasurementStatus(cell)==="running"
  const runs=useCoverageRuns(cell,namespace)
  const currentRuns=(runs.data?.runs??[]).filter(run=>variant?.runIds.includes(`${run.namespace}/${run.name}/${run.uid}`))
  const completeRuns=currentRuns.filter(run=>run.status.conditions?.some(condition=>condition.type==="ExecutionComplete"&&condition.status==="True"&&condition.observedGeneration===run.generation)).length
  const stage=currentRuns.flatMap(run=>run.status.conditions?.filter(condition=>condition.observedGeneration===run.generation)??[]).find(condition=>condition.type==="ExecutionComplete"&&condition.status==="Unknown")?.reason
  const stageLabel=({JobRunning:"실행 작업 진행",JobPending:"실행 작업 대기"} as Record<string,string>)[stage??""]??"미제공"
  const watts=coverageWatts([cell])
  const profileIDs=new Set(rows.flatMap(row=>[row.profileId,row.sourceProfileId].filter(Boolean)))
  const measured=(profiles.data?.profiles??[]).filter(profile=>profileIDs.has(profile.id)).map(profile=>profile.measuredAt).filter(at=>Number.isFinite(Date.parse(at))).sort()
  const assessment=variant?.assessment??cell.assessment
  useEffect(()=>{panel.current?.focus({preventScroll:true});if(window.matchMedia("(max-width: 767px)").matches)panel.current?.scrollIntoView({block:"start"})},[cell.id])
  return <div id="coverage-selected-condition" ref={panel} tabIndex={-1} role="region" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${executionPathLabel(cell.executionPath)} 상세`} className="coverage-selected">
    <header><h2>{deviceLabel(cell.deviceClass.name)}</h2><Button variant="outline" onClick={close}>닫기</Button></header>
    <p className="font-semibold break-words">{cell.model.name}</p><table className="coverage-results"><tbody><tr><th scope="row">전력모드</th><td>{cell.powerMode}</td></tr><tr><th scope="row">실행 경로</th><td>{executionPathLabel(cell.executionPath)}</td></tr><tr><th scope="row">실측 전력 범위 (W)</th><td>{watts.length?`${number(Math.min(...watts),"W")}–${number(Math.max(...watts),"W")}`:"미확인"}</td></tr></tbody></table>
    <table className="coverage-results"><thead><tr><th>측정 상태</th><th>현재 사용</th><th>품질 판정</th><th>제한 사유</th></tr></thead><tbody><tr><td>{coverageStates[coverageMeasurementStatus(cell)]}</td><td>{measurementSchedulerUsed(assessment)===true?"사용 중":measurementSchedulerUsed(assessment)===false?"미사용":"미확인"}</td><td>{measurementQuality([assessment]).label}</td><td>{coverageQuality(cell).reasons.map(reason=>reason.label).join(" / ")||"해당 없음"}</td></tr></tbody></table>
    <table className="coverage-results" aria-label="현재 측정"><tbody><tr><th scope="row">현재 측정 ID</th><td className="break-all">{cell.currentMeasurement?.id??"없음"}</td></tr><tr><th scope="row">현재 측정 시각 KST</th><td>{cell.currentMeasurement?<time dateTime={cell.currentMeasurement.measuredAt}>{formatMeasurementTime(cell.currentMeasurement.measuredAt)}</time>:"미제공"}</td></tr></tbody></table>
    <section aria-label="d · 분배 점유시간"><h3>d · 분배 점유시간</h3>{cell.occupancy?.length?<table className="coverage-results"><thead><tr><th scope="col">Node / 배치</th><th scope="col">스케줄러 d (ms/요청)</th><th scope="col">관측 d(B)</th><th scope="col">측정 기록</th><th scope="col">조치</th></tr></thead><tbody>{cell.occupancy.map(row=><tr key={`${row.node.uid}/${row.profileID}/${row.batch}`}><th scope="row">{row.node.name} / {row.batch}<small className="block break-all">{row.node.uid}<br/>{row.profileID}</small></th><td><dl className="grid gap-1 text-sm"><div><dt className="inline">출처 </dt><dd className="inline">{row.schedulerD?{measured:"측정",dom:"DOM",default:"기본값"}[row.schedulerD.source]:"미제공"}</dd></div><div><dt className="inline">값 (ms/요청) </dt><dd className="inline tabular-nums">{occupancyMilliseconds(row.schedulerD?.value)}</dd></div><div><dt className="inline">쌍 (개) </dt><dd className="inline">{row.schedulerD?.pairs??"미제공"}</dd></div><div><dt className="inline">갱신 시각 KST </dt><dd className="inline">{row.schedulerD?.updatedAt?formatMeasurementTime(row.schedulerD.updatedAt):"미제공"}</dd></div></dl></td><td><OccupancyStatisticsFacts statistics={row.observedD?.statistics} pairs={row.observedD?.pairs}/></td><td>{occupancyStates[row.state]}<small className="block">{occupancyReasonLabel(row.reason)}</small></td><td>{(row.state==="none"||row.state==="failed")?<Link className="inline-flex min-h-11 items-center underline" href={coverageOccupancyRequestHref(cell,row,namespace)}>d 요청</Link>:row.request?<Link className="inline-flex min-h-11 items-center underline" href={occupancyResultHref(namespace,row.request.name,row.request.uid)}>진행·결과</Link>:"해당 없음"}</td></tr>)}</tbody></table>:<p>없음</p>}<Link className="inline-flex min-h-11 items-center underline" href={occupancyRequestHref(namespace,{model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,measurementID:cell.currentMeasurement?.id})}>d 요청</Link><CoverageOccupancyPeriod key={cell.id} cell={cell}/></section>
    {!!cell.support.diagnostics?.length&&<section aria-label="실행 선언 진단"><h3>실행 선언 진단</h3><ul className="list-inside list-disc text-sm">{cell.support.diagnostics.map((diagnostic,index)=><li key={index}>{diagnostic}</li>)}</ul></section>}
    {running&&<section aria-label="진행 회차"><h3>진행 회차</h3><table className="coverage-results"><thead><tr><th>확인된 완료 / 조회 회차 (회)</th><th>예정 회차 (회)</th><th>현재 단계</th></tr></thead><tbody><tr><td>{runs.error?"조회 실패":variant?.runIds.length?`${completeRuns}/${currentRuns.length}`:"미제공"}</td><td>미제공</td><td>{stageLabel}</td></tr></tbody></table></section>}
    <section><h3>배치별 결과</h3>{rows.length>0?<table className="coverage-results"><thead><tr><th>배치 (건)</th><th>p95 (ms)</th><th>총 (J/건)</th><th>추가 (J/건)</th><th>반복 실행/유휴 (회)</th><th>오차 (%)</th><th>제공된 기준</th></tr></thead><tbody>{rows.map(row=><tr key={row.id}><th scope="row">{row.batch}</th><td>{row.p95Milliseconds===null?"미기록":number(row.p95Milliseconds,"ms")}</td><td>{row.totalJoulesPerItem===null?"미기록":number(row.totalJoulesPerItem,"J/건")}</td><td>{row.incrementalJoulesPerItem===null?"미기록":number(row.incrementalJoulesPerItem,"J/건")}</td><td>{row.activeTrialCount??"미기록"} / {row.idleTrialCount??"미기록"}</td><td>{row.confidence&&row.incrementalJoulesPerItem!==null&&row.incrementalJoulesPerItem!==0?number(row.confidence.halfWidth/Math.abs(row.incrementalJoulesPerItem)*100,"%"):"미제공"}</td><td>미제공</td></tr>)}</tbody></table>:<p>배치 결과: {coverageMeasurementStatus(cell)==="unmeasured"?"미측정":"미기록"}</p>}</section>
    {!visible&&<p className="text-muted-foreground">이 조건은 현재 탭·필터 밖에 있습니다. 선택은 유지됩니다.</p>}
    <details className="coverage-condition-verdict"><summary>추가 확인</summary><MeasurementEvidenceChecks assessment={assessment} rows={rows}/></details>
    <div className="flex flex-wrap gap-3"><Button asChild><Link href={coverageRequestHref(cell,namespace,returnTo,variant?.id)}>동일 조건 요청</Link></Button><Button variant="outline" asChild><Link href={`/measurements/requests?${new URLSearchParams({namespace,model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,view:"runs"})}`}>실행 기록</Link></Button></div>
    <details><summary>선택 조건·배치 범위</summary><BatchBar progress={progress}/><table className="w-full text-left text-sm"><thead><tr><th>목표 배치 (건)</th><th>부족 배치 (건)</th></tr></thead><tbody><tr><td>{progress?.target.join(", ")||"미지정"}</td><td>{progress?.missing.join(", ")||"없음"}</td></tr></tbody></table>{variant&&<p className="text-xs">{conditionLabel(variant.condition)}</p>}</details>
    <div className="text-sm">{measured.length?<p>실제 측정 <time dateTime={measured.at(-1)}>{formatMeasurementTime(measured.at(-1)!)}</time> / {measurementAge(measured.at(-1)!)}</p>:isCoverageWarming(profiles.error??profiles.failureReason)?<p role="status">초기화 중</p>:profiles.isPending&&rows.length?<p>측정 시각 조회 중</p>:profiles.error?<><p role="alert">{(profiles.error.data as {code?:unknown}|null)?.code==="MeasurementSummaryInvalid"?profiles.error.body:`측정 시각 조회 실패 (HTTP ${profiles.error.status})`} · 수치 근거는 유지합니다.</p><Button variant="outline" onClick={()=>void profiles.refetch()}>측정 시각 다시 조회</Button></>:<p>측정 시각 미기록 또는 현재 프로파일에 없음</p>}</div>
    {cell.failureSummary.length>0&&<section><h3>실패 기록</h3>{[...new Map(cell.failureSummary.map(failure=>[failure.runId||`${failure.requestId}/${failure.code}`,failure])).values()].map((failure,index)=>{const run=runs.data?.runs.find(run=>run.uid===failure.runId.split("/").at(-1));const [requestNamespace,requestName,requestUID]=failure.requestId.split("/");const request=requests.data?.items?.find(item=>item.metadata.namespace===requestNamespace&&item.metadata.name===requestName&&item.metadata.uid===requestUID);const conditions=run?run.status.conditions?.filter(condition=>condition.observedGeneration===run.generation):request?.status?.conditions;const at=conditions?.filter(condition=>condition.status==="False").map(condition=>condition.lastTransitionTime).filter((value):value is string=>!!value).sort().at(-1);return <div key={index} className="coverage-failure"><strong>{measurementReasonSummary(failure.label||failure.message||failure.code)}</strong><Link href={coverageFailureHref(failure,namespace,returnTo)}>{failure.runName?"실패 회차 확인":"실패 요청 사유 확인"}</Link><table className="w-full text-left text-sm"><tbody><tr><th scope="row">실패 판정 시각</th><td>{at?formatMeasurementTime(at):"미기록 또는 조회 범위 밖"}</td></tr><tr><th scope="row">사유</th><td>{measurementReasonSummary(conditions?.find(condition=>condition.status==="False")?.message||failure.message)}</td></tr></tbody></table><details><summary className="min-h-11 cursor-pointer py-2">실패 원문</summary><pre className="whitespace-pre-wrap break-words">{JSON.stringify({failure,conditions},null,2)}</pre></details></div>})}</section>}
    <details><summary>근거 확인</summary><table className="coverage-results"><thead><tr><th>항목</th><th>판정</th></tr></thead><tbody>{(assessment.checklist?.checks??[]).map((check,index)=><tr key={`${check.code}/${index}`}><th className="font-normal">{measurementReasonSummary(check.message)}</th><td>{check.result==="unknown"&&check.reason==="PolicyUnset"?"정책 미설정":{pass:"확인",warn:"주의",fail:"불일치",unknown:"미확인"}[check.result]}</td></tr>)}</tbody></table></details>
    <details><summary>원문</summary><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify({executionPath:cell.executionPath,currentMeasurement:cell.currentMeasurement,condition:variant?.condition,rows,support:cell.support,history:cell.history,assessment,runs:runs.data},null,2)}</pre></details>
  </div>
}
