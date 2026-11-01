"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { TriangleAlert } from "lucide-react"
import { Bar, CartesianGrid, ComposedChart, Legend, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { useChillQuery } from "@/lib/client"
import { campaignGroups, commitExperimentSelection, decodeExperiment, decodeExperiments, definitionComparisonReason, definitionPolicy, experimentChartData, experimentHeatmap, experimentPath, experimentRows, frozenP95SLO, resultMetric, stageHistoryLink, stageLabel, stageLoadLabel, stageSeconds, type Experiment, type ExperimentHeatmapRow } from "@/lib/experiments"
import { comparisonValidity, experimentDifference, experimentIntervalSelection } from "@/lib/experiments"
import { formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Empty, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"

import { ExperimentAbsoluteValues, ExperimentConclusion, ExperimentComparisonLegend, ExperimentCoverage, ExperimentInjections, ExperimentJudgment, ExperimentLimits, ExperimentOutcome, ExperimentResultRow, HistoricalExperimentError, useHistoricalExperiments } from "@/features/experiments-conclusion"
import { SchedulerIntervalEvents } from "@/features/scheduler-evidence"
import { EvaluationCleanup, EvaluationLaunch } from "@/features/evaluation-request"
const number=(value:unknown,unit="")=>typeof value==="bigint"?String(value):typeof value==="number"&&Number.isFinite(value)?formatMeasurementNumber(value,unit):"미기록"
const tone=(value:number|null,baseline=0,higherIsBetter=false)=>experimentDifference(value,baseline,higherIsBetter).tone
const intervalTable="w-full text-sm tabular-nums max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2 max-md:[&>tbody>tr>th]:col-span-2 max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:flex-col max-md:[&>tbody>tr>td]:text-left max-md:[&>tbody>tr>td]:before:text-xs max-md:[&>tbody>tr>td]:before:text-muted-foreground max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]"


function IntervalDrawer({run,runs,row,service,onClose,restoreFocus}:{run:Experiment;runs:Experiment[];row:ExperimentHeatmapRow|undefined;service:string;onClose:()=>void;restoreFocus:()=>void}) {
  const stages=row?[{stage:row.stage,owner:row.leftRun},...(row.other&&row.rightRun?[{stage:row.other,owner:row.rightRun}]:[])]:[]
  const names=[...new Set(stages.flatMap(({stage})=>experimentRows(stage).map(item=>item.name)))].filter(name=>!service||name===service)
  const reason=row?.reason??definitionComparisonReason(run,runs)
  return <Sheet open={!!row} onOpenChange={open=>{if(!open)onClose()}}><SheetContent className="w-full overflow-y-auto sm:max-w-2xl" aria-describedby={undefined} onCloseAutoFocus={event=>{event.preventDefault();restoreFocus()}}>
    <SheetHeader><SheetTitle title={reason}>{row&&stageLabel(row.stage,row.leftRun)}</SheetTitle></SheetHeader>
    {row&&<div className="space-y-5 px-4 pb-6">
      {stages.filter(({owner})=>owner.id!==run.id).map(({owner})=><Link key={owner.id} prefetch={false} className="block text-sm underline" href={`/experiments/results?${new URLSearchParams({a:owner.id})}`}>{owner.definition?.run.name??owner.id}</Link>)}
      <ExperimentLimits run={run} runs={runs}/>
      <div className="min-w-0"><table className={intervalTable}><thead><tr>{["단계 / 행 / 모드","시작 (KST)","종료 (KST)"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{stages.map(({stage,owner})=><tr key={`${owner.id}/${stage.id}`} className="border-t"><th className="break-all p-2 text-left font-normal">{stageLabel(stage,owner)}</th><td data-label="시작 (KST)" className="p-2">{formatMeasurementTime(stage.from)}</td><td data-label="종료 (KST)" className="p-2">{formatMeasurementTime(stage.to)}</td></tr>)}</tbody></table></div>
      <table aria-label="비교 조건" className={intervalTable}><thead><tr>{["부하","서비스 식별자·정의","측정 규칙"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody><tr>{Object.entries(row.other?comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).conditions:{load:"미기록",identity:"미기록",rules:"미기록"}).map(([key,value])=><td key={key} data-label={{load:"부하",identity:"서비스 식별자·정의",rules:"측정 규칙"}[key]} className={`border-t p-2 ${value==="미기록"?"text-muted-foreground":""}`}>{value}</td>)}</tr></tbody></table>
      <div className="min-w-0"><table className={intervalTable}><thead><tr>{["정책","부하","길이 (s)","에너지 (J)","평균 전력 (W)"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead><tbody>{stages.map(({stage,owner})=>{const seconds=stageSeconds(stage),energy=stage.kpis.wholePoolJoules,policy=definitionPolicy(stage,owner);return <tr key={`${owner.id}/${stage.id}`} className="border-t"><th className={`p-2 text-left font-normal ${policy==="미기록"?"text-muted-foreground":""}`}>{policy}</th><td data-label="부하" className="break-words p-2 text-right">{stageLoadLabel(stage,owner)}</td><td data-label="길이 (s)" className="p-2 text-right">{number(seconds,"s")}</td><td data-label="에너지 (J)" className="p-2 text-right">{number(energy,"J")}</td><td data-label="평균 전력 (W)" className="p-2 text-right">{number(typeof energy==="number"&&seconds?energy/seconds:null,"W")}</td></tr>})}</tbody></table></div>
      <div className="min-w-0"><table className={intervalTable}><thead><tr>{["서비스","단계 / 행 / 모드","성공률 (%)","p95 (ms)","SLO (ms)","요청 N","p95 N"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead><tbody>{names.flatMap(name=>stages.map(({stage,owner})=>{
        const candidates=experimentRows(stage).filter(item=>item.name===name),item=candidates.length===1?candidates[0]:undefined
        const success=item?resultMetric(item,"success"):null,p95=item?resultMetric(item,"p95"):null,slo=item?frozenP95SLO(item,owner):null
        return <tr key={`${name}/${owner.id}/${stage.id}`} className="border-t"><th className="max-w-52 break-words p-2 text-left font-normal">{name}</th><td data-label="단계 / 행 / 모드" className="break-all p-2">{stageLabel(stage,owner)}</td><td data-label="성공률 (%)" className="p-2 text-right">{number(typeof success?.value==="number"?success.value*100:null,"%")}</td><td data-label="p95 (ms)" className="p-2 text-right">{number(p95?.value,"ms")}</td><td data-label="SLO (ms)" className={`p-2 text-right ${slo===null?"text-muted-foreground":""}`}>{slo===null?"미기록":number(slo,"ms")}</td><td data-label="요청 N" className="p-2 text-right">{number(success?.denominator)}</td><td data-label="p95 N" className="p-2 text-right">{number(p95?.denominator)}</td></tr>
      }))}</tbody></table></div>
      {stages.filter(({stage,owner})=>owner.outcome?.stages?.some(item=>item.id===stage.id)||owner.definition?.spec.stages.find(item=>item.id===stage.id)?.failureInjection?.length).map(({stage,owner})=><section key={`${owner.id}/${stage.id}`} className="space-y-2" aria-label={`${stage.id} 판정·주입`}><h3 className="text-sm font-semibold">{stageLabel(stage,owner)}</h3><ExperimentOutcome run={owner} stage={stage}/><ExperimentInjections run={owner} stage={stage}/></section>)}
      <section className="space-y-2" aria-label="구간 판단·전환 사건"><h3 className="text-sm font-semibold">{stageLabel(row.stage,row.leftRun)} · 판단·전환</h3><SchedulerIntervalEvents from={row.stage.from} to={row.stage.to}/></section>
      <div className="flex flex-wrap gap-2">{stages.map(({stage,owner})=><Button key={`${owner.id}/${stage.id}`} variant="outline" asChild><Link prefetch={false} href={stageHistoryLink(stage)}>{stageLabel(stage,owner)} · 판단·전환 기록 열기</Link></Button>)}</div>
      <SourceJSON inline title="원문" value={{experiment:run.id,stages,sources:run.sources}}/>
      <SourceExport filename="experiment-interval.json" value={{experiment:run.id,stages,sources:run.sources}}/>
    </div>}
  </SheetContent></Sheet>
}

function ExperimentDetail({run,runs}:{run:Experiment;runs:Experiment[]}) {
  const rows=React.useMemo(()=>experimentHeatmap(run,runs),[run,runs])
  const names=React.useMemo(()=>[...new Set(rows.flatMap(row=>[row.stage,...(row.other?[row.other]:[])].flatMap(stage=>experimentRows(stage).map(item=>item.name))))],[rows])
  const params=useSearchParams(),service=params.get("service")??""
  const trigger=React.useRef<HTMLElement|null>(null)
  const chart=React.useMemo(()=>experimentChartData(run,service,runs),[run,service,runs])
  const absolute=!run.definition&&run.comparisons===undefined
  const row=rows.find(row=>Object.entries(experimentIntervalSelection(run,row)).every(([key,value])=>(params.get(key)??"")===value))
  function open(row:ExperimentHeatmapRow,name="") {
    trigger.current=document.activeElement instanceof HTMLElement?document.activeElement:null
    commitExperimentSelection(window.location,window.history,{...experimentIntervalSelection(run,row),...(name?{service:name}:{})})
  }
  return <>
    {absolute&&<ExperimentAbsoluteValues run={run} service={service} onSelect={(stage,name)=>{const row=rows.find(row=>row.stage===stage);if(row)open(row,name)}}/>}
    {!absolute&&
    <section aria-label="구간별 정책 차이" className="min-w-0 rounded-lg border">
      <table className="w-full text-sm tabular-nums max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:grid-cols-2 max-md:[&>tbody>tr>th]:col-span-2 max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:min-w-0 max-md:[&>tbody>tr>td]:flex-col max-md:[&>tbody>tr>td]:break-words max-md:[&>tbody>tr>td]:text-left max-md:[&>tbody>tr>td]:before:text-xs max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]">
        <thead className="bg-muted/50"><tr><th scope="col" rowSpan={2} className="p-3 text-left">비교 상태</th><th scope="col" rowSpan={2} className="p-3 text-left">단계 / 행 / 모드</th><th scope="col" rowSpan={2} className="p-3 text-right">부하</th><th scope="col" rowSpan={2} className="p-3 text-right">에너지 Δ<br/>%</th>{names.filter(name=>!service||name===service).map(name=><th key={name} scope="colgroup" colSpan={2} className="max-w-56 break-words border-l p-2 text-center font-medium">{name}</th>)}</tr><tr>{names.filter(name=>!service||name===service).flatMap(name=>[<th key={`${name}/success`} scope="col" className="border-l px-2 py-2 text-right text-xs">성공률 Δ %p</th>,<th key={`${name}/p95`} scope="col" className="px-2 py-2 text-right text-xs">p95 배율</th>])}</tr></thead>
        <tbody>{rows.map(row=><tr key={row.key} className={`border-t ${row.other?"":"bg-muted text-muted-foreground"}`}>
          <td data-label="비교 상태" className="p-2 font-medium">{!row.other?(!run.definition?"미기록":row.reason.includes("대상 게시 없음")?"대상 없음":"비교 없음"):comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).limited?"참고":"일치"}<div className="max-w-60 whitespace-normal text-xs font-normal text-muted-foreground">{row.reason}</div></td>
          <th scope="row" className="break-words p-1 text-left font-medium"><button type="button" className="flex min-h-11 w-full items-center gap-2 rounded px-2 text-left focus-visible:outline-2 focus-visible:outline-primary" onClick={()=>open(row)} title={row.reason}>{stageLabel(row.stage,row.leftRun)}{row.other&&comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).limited&&<TriangleAlert aria-label="비교 제한" className="size-4 shrink-0 text-heat"/>}</button></th>
          <td data-label="부하" className="p-1 text-right"><button className="min-h-11 w-full break-words rounded px-2 text-right focus-visible:outline-2 focus-visible:outline-primary max-md:text-left" onClick={()=>open(row)}>{stageLoadLabel(row.stage,row.leftRun)}</button></td>
          <td data-label="에너지 Δ (%)" className="p-1"><button className={`min-h-11 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary max-md:text-left ${tone(row.energyPercent)}`} onClick={()=>open(row)}>{experimentDifference(row.energyPercent).text}</button></td>
          {names.filter(name=>!service||name===service).flatMap(name=>{const item=row.services.find(item=>item.name===name);return [<td key={`${name}/success`} data-label={`${name} 성공률 Δ (%p)`} className="border-l p-1"><button className={`min-h-11 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary max-md:text-left ${tone(item?.successDelta??null,0,true)}`} onClick={()=>open(row,name)} aria-label={`${name} 성공률 상세`}>{experimentDifference(item?.successDelta??null,0,true).text}</button></td>,<td key={`${name}/p95`} data-label={`${name} p95 배율`} className="p-1"><button className={`min-h-11 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary max-md:text-left ${tone(item?.p95Ratio??null,1)}`} onClick={()=>open(row,name)} aria-label={`${name} p95 상세`}>{experimentDifference(item?.p95Ratio??null,1).text}</button></td>]})}
        </tr>)}</tbody>
      </table>
    </section>
    }
    <section aria-label="구간별 요청률과 지연" className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">구간별 요청률·p95</h2><label className="flex min-w-0 flex-wrap items-center gap-2 text-sm">서비스<select className="min-h-10 w-full max-w-80 rounded border bg-background px-2" value={service} onChange={event=>commitExperimentSelection(window.location,window.history,{service:event.target.value})}><option value="">전체</option>{names.map(name=><option key={name} value={name}>{name}</option>)}</select></label></div>
      <div className="h-[250px] min-w-0" role="img" aria-label="구간별 발행 요청률과 기록된 계획 요청률·서비스 p95·SLO">
        <ResponsiveContainer width="100%" height="100%"><ComposedChart accessibilityLayer data={chart} margin={{left:0,right:8,top:10,bottom:0}}>
          <CartesianGrid vertical={false}/><XAxis dataKey="interval" tick={{fontSize:11}}/><YAxis yAxisId="rate" width={60} unit=" 요청/s" tick={{fontSize:10}}/>{service&&<YAxis yAxisId="latency" orientation="right" width={70} unit=" ms" tick={{fontSize:10}}/>}<Tooltip/><Legend wrapperStyle={{fontSize:11}}/>
          {chart.some(row=>row.observedRate!==null)&&<Bar yAxisId="rate" dataKey="observedRate" name="발행 요청률" fill="var(--primary)" opacity={service ? .25 : .8} maxBarSize={60} isAnimationActive={false}/>}
          {chart.some(row=>row.rate!==null)&&<Bar yAxisId="rate" dataKey="rate" name="계획 평균 요청률" fill="var(--muted-foreground)" opacity={.35} maxBarSize={60} isAnimationActive={false}/>}
          {service&&(["leftP95","rightP95","leftSLO","rightSLO"] as const).filter(key=>chart.some(row=>row[key]!==null)).map(key=><Line key={key} yAxisId="latency" dataKey={key} name={{leftP95:"선택 구간 p95",rightP95:"비교 구간 p95",leftSLO:"선택 구간 SLO",rightSLO:"비교 구간 SLO"}[key]} stroke={key.includes("SLO")?"var(--heat)":key==="leftP95"?"var(--color-teal-600, #0d9488)":"var(--color-slate-500, #64748b)"} strokeWidth={2} strokeDasharray={key.includes("SLO")?"4 4":undefined} connectNulls={false} isAnimationActive={false}/>)}
        </ComposedChart></ResponsiveContainer>
      </div>
    </section>
    <IntervalDrawer run={run} runs={runs} row={row} service={service} onClose={()=>commitExperimentSelection(window.location,window.history,{al:"",as:"",b:"",bs:""})} restoreFocus={()=>trigger.current?.focus()}/>
  </>
}

export function ExperimentsPage() {
  const params=useSearchParams(),id=params.get("a")??""
  const list=useChillQuery<{items:Experiment[]}>("v1/experiments",{decoder:decodeExperiments})
  const query=useChillQuery<Experiment>(experimentPath(id),{enabled:!!id,decoder:decodeExperiment})
  const run=query.data?.id===id?query.data:undefined
  const publications=list.data?.items??[]
  const legacyOnly=publications.every(run=>!run.definition&&run.comparisons===undefined)
  const selected=id?(run?[run]:[]):publications
  const historical=useHistoricalExperiments(selected,publications,!!list.data||!!list.error)
  const runs=historical.runs
  if(id)return <Screen width="data" back={["/experiments/results","실험 결과"]} title={run?.definition?.run.name??id} actionPlacement="header" action={<div className="flex flex-wrap gap-2">{run?.definition&&<EvaluationLaunch publicationID={run.id} label="다시 실행"/>}<Button variant="outline" asChild><Link href="/experiments">실행</Link></Button></div>}><QueryGate query={query}>{run&&<><section aria-label="비교 판독" className="space-y-2">{run.outcome&&<ExperimentOutcome run={run}/>}<EvaluationCleanup key={run.id} run={run}/>{!run.definition&&run.comparisons===undefined?<ExperimentConclusion run={run} runs={runs}/>:<>{historical.error?<HistoricalExperimentError refetch={historical.refetch}/>:historical.pending?<strong>조회 중</strong>:<><div className="flex flex-wrap items-center justify-between gap-3"><ExperimentJudgment run={run} runs={runs}/><ExperimentComparisonLegend/></div><ExperimentConclusion run={run} runs={runs}/><details><summary className="min-h-11 cursor-pointer text-sm">비교 조건·제한</summary><div className="grid gap-3 sm:grid-cols-2"><ExperimentLimits run={run} runs={runs}/><ExperimentCoverage run={run} runs={runs}/></div></details></>}</>}<SourceJSON inline title="게시 원문" value={run}/></section>{!historical.error&&!historical.pending&&<ExperimentDetail key={run.id} run={run} runs={runs}/>}</>}</QueryGate></Screen>
  return <Screen width="data" title="실험 결과" actionPlacement="header" action={<div className="flex flex-wrap items-center gap-2">{!legacyOnly&&<ExperimentComparisonLegend/>}<Button variant="outline" asChild><Link href="/experiments">실행</Link></Button><Button variant="outline" asChild><Link href="/experiments/load-scenarios">부하 설정</Link></Button><EvaluationLaunch/></div>}><QueryGate query={list}>{list.data&&<section aria-label="실험 결과 표">{historical.error&&<HistoricalExperimentError refetch={historical.refetch}/>}<div className="min-w-0 overflow-x-auto"><table className="w-full text-sm max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr]:min-w-0 max-md:[&>tbody>tr>td]:block max-md:[&>tbody>tr>td]:min-w-0 max-md:[&>tbody>tr>td]:max-w-none max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]"><thead><tr>{legacyOnly?<><th className="p-3 text-left">실험</th><th colSpan={11} className="p-3 text-left">단계별 원값</th></>:["비교 상태","실험","일치 (개)","참고 (개)","대응 없음 (개)","실행","측정","정리","에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)","제한 사유"].map((label,index)=><th key={label} className={`p-3 ${[2,3,4,8,9,10].includes(index)?"text-right":"text-left"}`}>{label}</th>)}</tr></thead><tbody>{campaignGroups(publications).map(({run})=><ExperimentResultRow key={run.id} run={run} runs={runs} pending={historical.pending} error={!!historical.error}/>)}</tbody></table></div>{!publications.length&&<Empty title="기록 없음"/>}</section>}</QueryGate></Screen>
}
