"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { Bar, CartesianGrid, ComposedChart, Line, ResponsiveContainer, Tooltip as ChartTooltip, XAxis, YAxis } from "recharts"
import { useChillQuery, useSession } from "@/lib/client"
import { decodeExperiment, decodeExperimentPage, experimentPath, injectionRows, type Experiment, type ExperimentPage } from "@/lib/experiments"
import { comparisonRows, comparisonValidity, experimentChartData, experimentHeatmap, experimentIntervalSelection, experimentRows, relativeEnergy, serviceDelta, type ExperimentHeatmapRow } from "@/lib/experiment-comparison"
import { commitExperimentSelection } from "@/lib/dashboard-routes"
import { formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Empty, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ExperimentCoverage, ExperimentFacts, ExperimentInjections, ExperimentJudgment, ExperimentLimits, ExperimentMetadata, ExperimentOutcome, ExperimentResultRow, ExperimentStageResults, ExperimentVerdict, HistoricalExperimentError, useHistoricalExperiments, usePublishedExperimentFacts } from "@/features/experiments-conclusion"
import { SchedulerIntervalEvents } from "@/features/scheduler-evidence"
import { EvaluationCleanup, EvaluationLaunch } from "@/features/evaluation-request"

const number=(value:unknown,unit="")=>typeof value==="bigint"?String(value):typeof value==="number"&&Number.isFinite(value)?unit==="req/s"&&value>0&&value<.01?"0.01 미만":formatMeasurementNumber(value,unit==="req/s"?"%":unit):"미제공"
const intervalTable="w-full text-sm tabular-nums max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2 max-md:[&>tbody>tr>th]:col-span-2 max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:min-w-0 max-md:[&>tbody>tr>td]:flex-col max-md:[&>tbody>tr>td]:text-left max-md:[&>tbody>tr>td]:before:text-xs max-md:[&>tbody>tr>td]:before:text-muted-foreground max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]"

function ExperimentHelp() {
  const [open,setOpen]=React.useState(false)
  return <Tooltip open={open} onOpenChange={setOpen}><TooltipTrigger asChild><Button variant="ghost" aria-expanded={open} onClick={event=>{event.preventDefault();setOpen(value=>!value)}}>도움말</Button></TooltipTrigger><TooltipContent className="max-w-80 whitespace-normal leading-relaxed">D1는 게시된 판정입니다. 성공/요청은 기한 안에 성공한 수/offered이며 미발행도 분모에 포함됩니다. 결과 막대는 발행 요청의 완료·시간 초과·오류·미완료를 구분하며 미발행은 별도입니다. 완료 지연은 completed 응답 기준입니다. 선언 SLO는 동결 정의의 목표이며 적용 확인은 요청 identity와 출처까지 연결된 경우입니다. 구간 성공률은 part 실측이며 아래 차트는 단계 집계입니다. 비교 차이는 후보−기준이고 정책 효과를 보장하지 않습니다. 사건은 같은 기간의 클러스터 전체 기록이며 인과를 뜻하지 않습니다.</TooltipContent></Tooltip>
}

function SelectedValues({row,service="",onSelect}:{row:ExperimentHeatmapRow;service?:string;onSelect?:(service:string)=>void}) {
  return <div className="space-y-5">
    {row.other&&<h3 className="text-sm font-semibold">후보</h3>}
    <ExperimentStageResults run={row.leftRun} stage={row.stage} service={service} onSelect={onSelect}/>
    {row.other&&<section className="space-y-2" aria-label="기준 결과"><h3 className="text-sm font-semibold">기준</h3><ExperimentStageResults run={row.rightRun} stage={row.other} service={service}/></section>}
    {row.other&&<section aria-label="후보−기준 차이" className="space-y-2 text-sm">
      <h3 className="font-semibold">후보−기준 차이</h3>
      <p>에너지 상대 차이 {relativeEnergy(row.stage.kpis.wholePoolJoules,row.other.kpis.wholePoolJoules)}</p>
      <table aria-label="서비스별 비교 차이" className="w-full text-left tabular-nums"><thead><tr><th scope="col" className="p-2 font-medium">서비스</th><th scope="col" className="p-2 font-medium">성공률 차이·p95 차이 및 배율</th></tr></thead><tbody>{comparisonRows(row.stage,row.other).filter(item=>!service||item.name===service).map(item=><tr key={item.name} className="border-t"><th scope="row" className="break-words p-2 font-normal">{item.name}</th><td className="p-2">{serviceDelta(item.a.length===1?item.a[0]:undefined,item.b.length===1?item.b[0]:undefined)}</td></tr>)}</tbody></table>
      <p className="text-xs text-muted-foreground">{row.reason} · 값의 차이는 정책 효과를 보장하지 않습니다.</p>
    </section>}
  </div>
}

function IntervalDrawer({run,runs,row,service,onClose,restoreFocus,returnTo="",side="candidate",onSideChange,onServiceChange}:{run:Experiment;runs:Experiment[];row:ExperimentHeatmapRow|undefined;service:string;onClose:()=>void;restoreFocus:()=>void;returnTo?:string;side?:string;onSideChange?:(side:string)=>void;onServiceChange?:(service:string)=>void}) {
  const stages=row?[{stage:row.stage,owner:row.leftRun,label:row.other?"후보":"선택 단계"},...(row.other?[{stage:row.other,owner:row.rightRun,label:"기준"}]:[])]:[]
  const eventStage=side==="baseline"&&stages[1]?stages[1]:stages[0]
  const names=[...new Set(stages.flatMap(({stage})=>experimentRows(stage).map(item=>item.name)))]
  const sourceLinks=stages.filter(({owner})=>owner.id!==run.id)
  const sourceOffset=new URLSearchParams(returnTo.split("?")[1]??"").get("offset")
  return <Sheet open={!!row} onOpenChange={open=>{if(!open)onClose()}}><SheetContent className="w-full overflow-y-auto sm:max-w-2xl" aria-describedby={undefined} onCloseAutoFocus={event=>{event.preventDefault();restoreFocus()}}>
    <SheetHeader><SheetTitle>{row&&row.stage.id}</SheetTitle></SheetHeader>
    {row&&<div className="space-y-5 px-4 pb-6">
      <dl className="space-y-2 text-sm">{stages.map(({stage,owner,label})=><div key={`${owner.id}/${stage.id}`}><dt className="font-medium">{label} · {stage.id}</dt><dd className="text-xs text-muted-foreground">{formatMeasurementTime(stage.from)} – {formatMeasurementTime(stage.to)}</dd></div>)}</dl>
      {onServiceChange&&<label className="flex items-center gap-2 text-sm">서비스 <select className="min-h-11 min-w-0 max-w-full rounded border bg-background px-2" value={service} onChange={event=>onServiceChange(event.target.value)}><option value="">전체</option>{names.map(name=><option key={name} value={name}>{name}</option>)}</select></label>}
      <SelectedValues row={row} service={service}/>
      {stages.filter(({stage,owner})=>injectionRows(owner,stage).length>0).map(({stage,owner})=><ExperimentInjections key={`${owner.id}/${stage.id}`} run={owner} stage={stage}/>)}
      <section className="space-y-2" aria-label="같은 기간 관찰"><h3 className="text-sm font-semibold">관찰</h3>{row.other&&<label className="flex items-center gap-3 text-sm">관찰 구간 <select className="min-h-11 rounded border bg-background px-2" value={side} onChange={event=>onSideChange?.(event.target.value)}><option value="candidate">후보</option><option value="baseline">기준</option></select></label>}{eventStage&&<SchedulerIntervalEvents key={`${eventStage.owner.id}/${eventStage.stage.id}`} from={eventStage.stage.from} to={eventStage.stage.to} returnTo={returnTo} label={eventStage.label}/>}</section>
      <details>
        <summary className="min-h-11 cursor-pointer text-sm">비교 조건·측정 규칙·계획 부하·정의 출처</summary>
        <div className="space-y-4">
          {stages.map(({stage,owner})=><ExperimentOutcome key={`${owner.id}/${stage.id}`} run={owner} stage={stage}/>)}
          <dl className="text-sm"><dt>비교 사유</dt><dd>{row.reason}</dd></dl>
          {sourceLinks.map(({owner,stage})=><Link key={`${owner.id}/${stage.id}`} prefetch={false} className="inline-flex min-h-11 break-all text-sm underline" href={`/experiments/results?${new URLSearchParams({a:owner.id,...(service?{service}:{}),...(sourceOffset?{offset:sourceOffset}:{})})}`}>{owner.definition?.run.name??owner.id}</Link>)}
          <ExperimentLimits run={run} runs={runs}/>
          {row.other&&<table aria-label="비교 조건" className={intervalTable}><thead><tr>{["부하","서비스 식별자·정의","측정 규칙"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody><tr>{Object.entries(comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).conditions).map(([key,value])=><td key={key} className="border-t p-2">{value}</td>)}</tr></tbody></table>}
          {stages.map(({stage,owner})=><ExperimentMetadata key={`${owner.id}/${stage.id}`} run={owner} stage={stage}/>)}
        </div>
      </details>
      <details><summary className="min-h-11 cursor-pointer text-sm">원문·내보내기</summary><SourceJSON inline title="원문" value={{experiment:run.id,stages,sources:run.sources}}/><SourceExport filename="experiment-interval.json" value={{experiment:run.id,stages,sources:run.sources}}/></details>
    </div>}
  </SheetContent></Sheet>
}

function ExperimentCharts({run,runs,service,rows,selected,onSelect}:{run:Experiment;runs:Experiment[];service:string;rows:ExperimentHeatmapRow[];selected:ExperimentHeatmapRow|undefined;onSelect:(row:ExperimentHeatmapRow)=>void}) {
  const data=experimentChartData(run,service,runs),paired=rows.some(row=>row.other)
  const lanes=[
    {label:"요청률",unit:"req/s",keys:["observedRate","rightObservedRate","rate","rightRate"]},
    {label:"성공 응답 p95",unit:"ms",keys:["leftP95","rightP95","leftSLO","rightSLO"]},
    {label:"에너지",unit:"J",keys:["leftEnergy","rightEnergy","leftObservedEnergy","rightObservedEnergy"]},
  ] as const
  const labels={observedRate:paired?"후보 발행":"발행",rightObservedRate:"기준 발행",rate:paired?"후보 계획 평균":"계획 평균",rightRate:"기준 계획 평균",leftP95:paired?"후보 p95":"p95",rightP95:"기준 p95",leftSLO:paired?"후보 적용 SLO":"적용 SLO",rightSLO:"기준 적용 SLO",leftEnergy:paired?"후보 풀 전체":"풀 전체",rightEnergy:"기준 풀 전체",leftObservedEnergy:paired?"후보 관측 (일부 노드 coverage 미달)":"관측 (일부 노드 coverage 미달)",rightObservedEnergy:"기준 관측 (일부 노드 coverage 미달)"}
  const maxPlanRate=Math.max(1,...data.flatMap(item=>[...item.plans,...item.rightPlans].map(part=>part.rate??0)))
  const columns={gridTemplateColumns:`repeat(${Math.max(1,rows.length)},minmax(0,1fr))`}
  const hasSubstages=rows.some(row=>[row.stage,...(row.other?[row.other]:[])].some(stage=>experimentRows(stage).some(item=>(!service||item.name===service)&&!!item.substages?.length)))
  return <section aria-label="공유 구간 차트" className="min-w-0 overflow-x-auto rounded-lg border p-2">
    <div className="space-y-1" style={{minWidth:rows.length>4?rows.length*160:undefined}}>
      {lanes.map((lane,laneIndex)=>{
        const keys=lane.keys.filter(key=>data.some(item=>item[key]!==null))
        const measured=laneIndex===0?data.some(item=>item.observedRate!==null||item.rightObservedRate!==null):laneIndex===1?data.some(item=>item.leftP95!==null||item.rightP95!==null):keys.length>0
        return <div key={lane.label} className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-3 px-2 text-xs"><h2 className="font-semibold">{lane.label} ({lane.unit})</h2>{keys.map(key=><span key={key} className={key.startsWith("right")?"text-muted-foreground":"text-primary"}>{labels[key]}{key.includes("SLO")?" ┄":key==="rate"||key==="rightRate"?" ▱":key.startsWith("right")?" □":" ■"}</span>)}{!measured&&keys.length>0&&<span className="text-muted-foreground">실측 미제공</span>}</div>
          {laneIndex===1&&!service?<div className="flex h-[82px] items-center justify-center text-sm text-muted-foreground">서비스 선택</div>:!keys.length||laneIndex===1&&!measured?<div className="flex h-[82px] items-center justify-center text-sm text-muted-foreground">실측 미제공</div>:
            <div className="h-[82px] min-w-0"><ResponsiveContainer width="100%" height="100%"><ComposedChart accessibilityLayer syncId={`experiment-${run.id}`} data={data} margin={{left:0,right:8,top:4,bottom:0}}>
              <CartesianGrid vertical={false}/><XAxis dataKey="key" hide/><YAxis width={64} tick={{fontSize:10}}/><ChartTooltip labelFormatter={(_label,payload)=>payload[0]?.payload?.interval??""}/>
              {keys.map(key=>key.includes("SLO")?
                <Line key={key} dataKey={key} name={labels[key]} type="stepAfter" stroke="var(--heat)" strokeDasharray={key.startsWith("right")?"2 4":"6 3"} connectNulls={false} dot={{r:3}} isAnimationActive={false}/>:
                <Bar key={key} dataKey={key} name={labels[key]} fill={key.startsWith("right")||key==="rate"?"var(--muted-foreground)":"var(--primary)"} fillOpacity={key==="rate"||key==="rightRate"?0.15:0.8} stroke={key.startsWith("right")||key==="rate"?"var(--muted-foreground)":"var(--primary)"} strokeDasharray={key==="rate"||key==="rightRate"?"4 2":undefined} maxBarSize={44} onClick={entry=>{const row=rows.find(row=>row.key===entry.payload?.key);if(row)onSelect(row)}} isAnimationActive={false}/>
              )}
            </ComposedChart></ResponsiveContainer></div>
          }
          {laneIndex===0&&data.some(item=>item.plans.length>1||item.rightPlans.length>1)&&<div className="grid gap-1 pl-16 pr-2" style={columns} aria-label="계획 하위 구간">
            <span className="col-span-full text-xs text-muted-foreground">{hasSubstages?"계획 오프셋 · 차트 실측은 단계 집계":"계획 오프셋 · 구간 실측 미제공"}</span>
            {data.map((item,index)=><div key={item.key} className="min-w-0">{[item.plans,item.rightPlans].map((parts,side)=>parts.length>1&&<div key={side} className="flex gap-1">{parts.map(part=><button key={part.id} className="relative min-h-11 min-w-0 flex-1 break-words rounded border border-dashed px-1 text-left text-xs focus-visible:outline-2 focus-visible:outline-primary" style={{flexGrow:part.to-part.from}} onClick={()=>onSelect(rows[index])}>
              <span aria-hidden="true" className="absolute inset-y-0 left-0 bg-muted-foreground/15" style={{width:`${100*(part.rate??0)/maxPlanRate}%`}}/>
              <span className="relative">{paired?(side?"기준 계획 ":"후보 계획 "):"계획 "}{part.id} +{part.from}–{part.to}s<br/>{part.rate===null?"요청률 미제공":`${number(part.rate,"req/s")} req/s`}</span>
            </button>)}</div>)}</div>)}
          </div>}
        </div>
      })}
      <div className="grid gap-1 pl-16 pr-2" style={columns} aria-label="구간 선택">{rows.map(row=><button key={row.key} type="button" aria-pressed={row.key===selected?.key} className="min-h-11 min-w-0 break-words rounded border px-1 text-left text-xs focus-visible:outline-2 focus-visible:outline-primary aria-pressed:border-primary aria-pressed:bg-primary/5" onClick={()=>onSelect(row)}>
        {row.stage.id}<span className="block text-muted-foreground">{formatMeasurementTime(row.stage.from)} – {formatMeasurementTime(row.stage.to)}</span>{row.other&&<span className="block text-muted-foreground">기준 {row.other.id} · {formatMeasurementTime(row.other.from)} – {formatMeasurementTime(row.other.to)}</span>}
      </button>)}</div>
    </div>
  </section>
}

function ExperimentDetail({run,runs,pending=false,error,refetch}:{run:Experiment;runs:Experiment[];pending?:boolean;error?:unknown;refetch:()=>void}) {
  const rows=React.useMemo(()=>experimentHeatmap(run,runs),[run,runs])
  const names=React.useMemo(()=>[...new Set(rows.flatMap(row=>[row.stage,...(row.other?[row.other]:[])].flatMap(stage=>experimentRows(stage).map(item=>item.name))))],[rows])
  const params=useSearchParams(),requestedService=params.get("service")??"",service=requestedService||(names.length===1?names[0]:"")
  const trigger=React.useRef<HTMLElement|null>(null),selectionArea=React.useRef<HTMLElement|null>(null)
  const [cleanupOpen,setCleanupOpen]=React.useState(false)
  const selectedRow=rows.find(row=>Object.entries(experimentIntervalSelection(run,row)).every(([key,value])=>(params.get(key)??(key==="al"?run.id:""))===value))
  const selected=selectedRow??rows.find(row=>row.other)??rows[0],drawerRow=params.get("sheet")==="closed"?undefined:selectedRow
  React.useEffect(()=>{if(!requestedService&&names.length===1)commitExperimentSelection(window.location,window.history,{service:names[0]})},[names,requestedService])
  function open(row:ExperimentHeatmapRow,name=service) {
    trigger.current=document.activeElement instanceof HTMLElement&&document.activeElement!==document.body?document.activeElement:null
    commitExperimentSelection(window.location,window.history,{...experimentIntervalSelection(run,row),service:name,eventSide:"",sheet:""},drawerRow?"replace":"push")
  }
  const returnTo=`/experiments/results?${params}`
  return <>
    <section aria-label="게시 결과 요약" className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-3"><ExperimentVerdict run={run}/><ExperimentHelp/></div>
      {run.outcome?.cleanup?.status==="Failed"&&<><Button variant="outline" onClick={()=>setCleanupOpen(true)}>정리 실패 · 풀 상태 확인</Button><Dialog open={cleanupOpen} onOpenChange={setCleanupOpen}><DialogContent className="max-h-[85vh] overflow-y-auto" aria-describedby={undefined}><DialogHeader><DialogTitle>풀 상태 확인</DialogTitle></DialogHeader><EvaluationCleanup run={run}/></DialogContent></Dialog></>}
      {rows.length>1&&<label className="text-sm">{rows.some(row=>row.other)?"비교쌍":"단계"} <select className="min-h-11 max-w-full rounded border bg-background px-2" value={selected?.key??""} onChange={event=>{const row=rows.find(row=>row.key===event.target.value);if(row)commitExperimentSelection(window.location,window.history,{...experimentIntervalSelection(run,row),sheet:drawerRow?"":"closed",eventSide:""})}}>{rows.map(row=><option key={row.key} value={row.key}>{row.other?`${row.stage.id} / ${row.other.id}`:row.stage.id}</option>)}</select></label>}
    </section>
    <section aria-label="선택 단계 수치" className="min-w-0 space-y-2">{selected?<><h2 className="text-sm font-semibold">{selected.stage.id} · 단계 집계</h2><SelectedValues row={selected} onSelect={name=>open(selected,name)}/></>:<Empty title="실측 단계 없음"/>}</section>
    <section ref={selectionArea} tabIndex={-1} aria-label="차트와 구간 선택" className="min-w-0 space-y-3 focus-visible:outline-2 focus-visible:outline-primary">
      <div className="flex flex-wrap items-center justify-between gap-3"><label className="text-sm">차트 서비스 <select className="min-h-11 max-w-full rounded border bg-background px-2" value={service} onChange={event=>commitExperimentSelection(window.location,window.history,{service:event.target.value})}><option value="">전체</option>{names.map(name=><option key={name} value={name}>{name}</option>)}</select></label>{selected&&<Button variant="outline" onClick={()=>open(selected)}>구간 자세히</Button>}</div>
      <ExperimentCharts run={run} runs={runs} service={service} rows={rows} selected={selected} onSelect={open}/>
    </section>
    <details><summary className="min-h-11 cursor-pointer text-sm">실행·정리·비교 조건·정의 출처</summary><div className="space-y-3"><ExperimentOutcome run={run}/>{error?<HistoricalExperimentError refetch={refetch}/>:pending?<strong>조회 중</strong>:<ExperimentJudgment run={run} runs={runs}/>}<div className="grid gap-3 sm:grid-cols-2"><ExperimentLimits run={run} runs={runs}/><ExperimentCoverage run={run} runs={runs}/></div><ExperimentFacts run={run}/>{selected&&<ExperimentMetadata run={selected.leftRun} stage={selected.stage}/>}</div></details>
    <details><summary className="min-h-11 cursor-pointer text-sm">게시 원문·내보내기</summary><SourceJSON inline title="게시 원문" value={run}/><SourceExport filename={`${run.id}.json`} value={run}/></details>
    <IntervalDrawer run={run} runs={runs} row={drawerRow} service={service} side={params.get("eventSide")??"candidate"} onSideChange={side=>commitExperimentSelection(window.location,window.history,{eventSide:side})} onServiceChange={name=>commitExperimentSelection(window.location,window.history,{service:name})} returnTo={returnTo} onClose={()=>commitExperimentSelection(window.location,window.history,{sheet:"closed"})} restoreFocus={()=>{if(trigger.current?.isConnected)trigger.current.focus();else selectionArea.current?.focus()}}/>
  </>
}

function PublicationRunLink({run}:{run:Experiment}) {
  const identity=run.definition?.run
  const params=useSearchParams()
  const query=useChillQuery<{name:string;uid:string}>(`v1/evaluations/${encodeURIComponent(identity?.name??"_")}`,{enabled:!!identity,decoder:parseSchedulerJSON})
  if(!identity)return null
  if(query.error)return <span className="text-xs">{query.error.status===404?"실행 없음":<Button variant="outline" onClick={()=>void query.refetch()}>실행 조회 실패 · 다시 조회</Button>}</span>
  if(!query.data)return <span className="text-xs text-muted-foreground">실행 조회 중</span>
  if(query.data.uid!==identity.uid)return <span className="text-xs text-muted-foreground">실행 교체됨</span>
  const selection=new URLSearchParams(params.toString());selection.set("uid",identity.uid)
  return <Button variant="outline" asChild><Link href={`/experiments/runs/${encodeURIComponent(identity.name)}?${selection}`}>진행</Link></Button>
}

export function ExperimentsPage() {
  const params=useSearchParams(),id=params.get("a")??"",offset=params.get("offset")??"0"
  const query=useChillQuery<Experiment>(experimentPath(id),{enabled:!!id,decoder:decodeExperiment})
  const source=query.data?.id===id?query.data:undefined
  const facts=usePublishedExperimentFacts(id,!!source&&!source.facts)
  const run=React.useMemo(()=>!source||!source.facts&&facts.data===undefined?undefined:facts.data&&!source.facts?{...source,facts:facts.data}:source,[source,facts.data])
  const historical=useHistoricalExperiments(run?[run]:[],run?[run]:[],!!run)
  return <Screen width="data" back={[`/experiments?${new URLSearchParams({view:"published",offset,...(params.get("service")?{service:params.get("service")!}:{})})}`,"게시 결과"]} title={run?.definition?.run.name??run?.facts?.name??id} actionPlacement="header" action={<nav aria-label="실험 상세" className="flex flex-wrap gap-2">{run&&<PublicationRunLink run={run}/>}<span aria-current="page" className="inline-flex min-h-11 items-center px-2 text-sm">결과</span>{run?.definition&&<EvaluationLaunch publicationID={run.id} label="다시 실행"/>}</nav>}><QueryGate query={query}><QueryGate query={source&&!source.facts?facts:query}>{run&&<ExperimentDetail key={run.id} run={run} runs={historical.runs} pending={historical.pending} error={historical.error} refetch={historical.refetch}/>}</QueryGate></QueryGate></Screen>
}

export function PublishedExperimentsPage() {
  const params=useSearchParams(),{config}=useSession()
  const requested=Number(params.get("offset")??0),offset=Number.isSafeInteger(requested)&&requested>=0?requested:0,limit=Math.min(5,config.maxPageSize)
  const list=useChillQuery<ExperimentPage>(`v1/experiments?${new URLSearchParams({offset:String(offset),limit:String(limit)})}`,{decoder:decodeExperimentPage})
  const publications=list.data?.items??[],page=list.data?.page
  const pageURL=(next:number)=>{const search=new URLSearchParams(params.toString());search.set("view","published");search.set("offset",String(next));return `/experiments?${search}`}
  return <Screen width="data" title="실험" actionPlacement="header" action={<div className="flex flex-wrap gap-2"><Button variant="outline" asChild><Link href="/experiments/load-scenarios">부하 설정</Link></Button><EvaluationLaunch/></div>}>
    <nav aria-label="실험 목록 보기" className="flex flex-wrap items-center gap-3"><Button variant="outline" asChild><Link href="/experiments">실행</Link></Button><span aria-current="page" className="text-sm font-medium">게시 결과</span>{list.data&&<span className="text-xs tabular-nums">현재 {publications.length?`${offset+1}–${offset+publications.length}`:"0"} / 전체 {page?.total??"미제공"}개</span>}<div className="ml-auto flex gap-2">{offset>0?<Button variant="outline" asChild><Link href={pageURL(Math.max(0,offset-(page?.limit??limit)))}>이전</Link></Button>:<Button variant="outline" disabled>이전</Button>}{page?.nextOffset!==undefined?<Button variant="outline" asChild><Link href={pageURL(page.nextOffset)}>다음</Link></Button>:<Button variant="outline" disabled>다음</Button>}</div></nav>
    <QueryGate query={list}>{list.data&&<section aria-label="게시 목록" className="min-w-0"><table className={intervalTable}><thead><tr>{["실험","기간 (KST)","정책","실행","측정","정리","비교 상태"].map(label=><th key={label} className="p-3 text-left">{label}</th>)}</tr></thead><tbody>{[...publications].sort((a,b)=>Date.parse(b.from)-Date.parse(a.from)).map(run=><ExperimentResultRow key={run.id} run={run} offset={offset} service={params.get("service")??""}/>)}</tbody></table>{!publications.length&&<Empty title="게시 기록 없음"/>}</section>}</QueryGate>
  </Screen>
}
