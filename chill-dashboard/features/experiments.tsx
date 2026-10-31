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

import { ExperimentConclusion, ExperimentComparisonLegend, ExperimentCoverage, ExperimentInjections, ExperimentJudgment, ExperimentLimits, ExperimentOutcome, ExperimentResultRow, HistoricalExperimentError, useHistoricalExperiments } from "@/features/experiments-conclusion"
import { SchedulerIntervalEvents } from "@/features/scheduler-evidence"
import { EvaluationCleanup, EvaluationLaunch } from "@/features/evaluation-request"
const number=(value:unknown,unit="")=>typeof value==="bigint"?String(value):typeof value==="number"&&Number.isFinite(value)?formatMeasurementNumber(value,unit):"미기록"
const tone=(value:number|null,baseline=0,higherIsBetter=false)=>experimentDifference(value,baseline,higherIsBetter).tone


function IntervalDrawer({run,runs,row,service,onClose,restoreFocus}:{run:Experiment;runs:Experiment[];row:ExperimentHeatmapRow|undefined;service:string;onClose:()=>void;restoreFocus:()=>void}) {
  const stages=row?[{stage:row.stage,owner:row.leftRun},...(row.other&&row.rightRun?[{stage:row.other,owner:row.rightRun}]:[])]:[]
  const names=[...new Set(stages.flatMap(({stage})=>experimentRows(stage).map(item=>item.name)))].filter(name=>!service||name===service)
  const reason=row?.reason??definitionComparisonReason(run,runs)
  return <Sheet open={!!row} onOpenChange={open=>{if(!open)onClose()}}><SheetContent className="w-full overflow-y-auto sm:max-w-2xl" aria-describedby={undefined} onCloseAutoFocus={event=>{event.preventDefault();restoreFocus()}}>
    <SheetHeader><SheetTitle title={reason}>{row&&stageLabel(row.stage,row.leftRun)}</SheetTitle></SheetHeader>
    {row&&<div className="space-y-5 px-4 pb-6">
      {stages.filter(({owner})=>owner.id!==run.id).map(({owner})=><Link key={owner.id} prefetch={false} className="block text-sm underline" href={`/experiments/results?${new URLSearchParams({a:owner.id})}`}>{owner.definition?.run.name??owner.id}</Link>)}
      <ExperimentLimits run={run} runs={runs}/>
      <div className="overflow-x-auto"><table className="w-full text-xs tabular-nums"><thead><tr>{["단계 / 행 / 모드","시작 (KST)","종료 (KST)"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{stages.map(({stage,owner})=><tr key={`${owner.id}/${stage.id}`} className="border-t"><th className="p-2 text-left font-normal">{stageLabel(stage,owner)}</th><td className="p-2">{formatMeasurementTime(stage.from)}</td><td className="p-2">{formatMeasurementTime(stage.to)}</td></tr>)}</tbody></table></div>
      <table aria-label="비교 조건" className="w-full text-sm"><thead><tr>{["부하","서비스 식별자·정의","측정 규칙"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody><tr>{Object.entries(row.other?comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).conditions:{load:"미기록",identity:"미기록",rules:"미기록"}).map(([key,value])=><td key={key} className={`border-t p-2 ${value==="미기록"?"text-muted-foreground":""}`}>{value}</td>)}</tr></tbody></table>
      <div className="overflow-x-auto"><table className="w-full text-sm tabular-nums"><thead><tr>{["정책","부하","길이 (s)","에너지 (J)","평균 전력 (W)"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead><tbody>{stages.map(({stage,owner})=>{const seconds=stageSeconds(stage),energy=stage.kpis.wholePoolJoules,policy=definitionPolicy(stage,owner);return <tr key={`${owner.id}/${stage.id}`} className="border-t"><th className={`p-2 text-left font-normal ${policy==="미기록"?"text-muted-foreground":""}`}>{policy}</th><td className="p-2 text-right">{stageLoadLabel(stage,owner)}</td><td className="p-2 text-right">{number(seconds,"s")}</td><td className="p-2 text-right">{number(energy,"J")}</td><td className="p-2 text-right">{number(typeof energy==="number"&&seconds?energy/seconds:null,"W")}</td></tr>})}</tbody></table></div>
      <div className="overflow-x-auto"><table className="w-full text-sm tabular-nums"><thead><tr>{["서비스","단계 / 행 / 모드","성공률 (%)","p95 (ms)","SLO (ms)","요청 N","p95 N"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead><tbody>{names.flatMap(name=>stages.map(({stage,owner})=>{
        const candidates=experimentRows(stage).filter(item=>item.name===name),item=candidates.length===1?candidates[0]:undefined
        const success=item?resultMetric(item,"success"):null,p95=item?resultMetric(item,"p95"):null,slo=item?frozenP95SLO(item,owner):null
        return <tr key={`${name}/${owner.id}/${stage.id}`} className="border-t"><th className="max-w-52 break-words p-2 text-left font-normal">{name}</th><td className="p-2">{stageLabel(stage,owner)}</td><td className="p-2 text-right">{number(typeof success?.value==="number"?success.value*100:null,"%")}</td><td className="p-2 text-right">{number(p95?.value,"ms")}</td><td className={`p-2 text-right ${slo===null?"text-muted-foreground":""}`}>{slo===null?"미기록":number(slo,"ms")}</td><td className="p-2 text-right">{number(success?.denominator)}</td><td className="p-2 text-right">{number(p95?.denominator)}</td></tr>
      }))}</tbody></table></div>
      {stages.map(({stage,owner})=><section key={`${owner.id}/${stage.id}`} className="space-y-2" aria-label={`${stage.id} 판정·주입`}><h3 className="text-sm font-semibold">{stageLabel(stage,owner)}</h3><ExperimentOutcome run={owner} stage={stage}/><ExperimentInjections run={owner} stage={stage}/></section>)}
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
  const row=rows.find(row=>Object.entries(experimentIntervalSelection(run,row)).every(([key,value])=>(params.get(key)??"")===value))
  function open(row:ExperimentHeatmapRow,name="") {
    trigger.current=document.activeElement instanceof HTMLElement?document.activeElement:null
    commitExperimentSelection(window.location,window.history,{...experimentIntervalSelection(run,row),...(name?{service:name}:{})})
  }
  return <>
    <section aria-label="구간별 정책 차이" className="overflow-x-auto rounded-lg border" tabIndex={0}>
      <table className="w-full text-sm tabular-nums">
        <thead className="bg-muted/50"><tr><th scope="col" rowSpan={2} className="p-3 text-left">비교 상태</th><th scope="col" rowSpan={2} className="p-3 text-left">단계 / 행 / 모드</th><th scope="col" rowSpan={2} className="p-3 text-right">부하</th><th scope="col" rowSpan={2} className="p-3 text-right">에너지 Δ<br/>%</th>{names.filter(name=>!service||name===service).map(name=><th key={name} scope="colgroup" colSpan={2} className="max-w-56 break-words border-l p-2 text-center font-medium">{name}</th>)}</tr><tr>{names.filter(name=>!service||name===service).flatMap(name=>[<th key={`${name}/success`} scope="col" className="border-l px-2 py-2 text-right text-xs">성공률 Δ %p</th>,<th key={`${name}/p95`} scope="col" className="px-2 py-2 text-right text-xs">p95 배율</th>])}</tr></thead>
        <tbody>{rows.map(row=><tr key={row.key} className={`border-t ${row.other?"":"bg-muted text-muted-foreground"}`}>
          <td className="p-2 font-medium">{!row.other?(!run.definition?"미기록":row.reason.includes("대상 게시 없음")?"대상 없음":"비교 없음"):comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).limited?"참고":"일치"}<div className="max-w-60 whitespace-normal text-xs font-normal text-muted-foreground">{row.reason}</div></td>
          <th scope="row" className="whitespace-nowrap p-1 text-left font-medium"><button type="button" className="flex min-h-10 w-full items-center gap-2 rounded px-2 text-left focus-visible:outline-2 focus-visible:outline-primary" onClick={()=>open(row)} title={row.reason}>{stageLabel(row.stage,row.leftRun)}{!row.other&&<span>{!run.definition?"미기록":row.reason.includes("대상 게시 없음")?"대상 게시 없음":"비교 없음"}</span>}{row.other&&comparisonValidity(row.stage,row.other,row.leftRun,row.rightRun).limited&&<TriangleAlert aria-label="비교 제한" className="size-4 text-heat"/>}</button></th>
          <td className="p-1 text-right"><button className="min-h-10 w-full rounded px-2 text-right focus-visible:outline-2 focus-visible:outline-primary" onClick={()=>open(row)}>{stageLoadLabel(row.stage,row.leftRun)}</button></td>
          <td className="p-1"><button className={`min-h-10 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary ${tone(row.energyPercent)}`} onClick={()=>open(row)}>{experimentDifference(row.energyPercent).text}</button></td>
          {names.filter(name=>!service||name===service).flatMap(name=>{const item=row.services.find(item=>item.name===name);return [<td key={`${name}/success`} className="border-l p-1"><button className={`min-h-10 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary ${tone(item?.successDelta??null,0,true)}`} onClick={()=>open(row,name)} aria-label={`${name} 성공률 상세`}>{experimentDifference(item?.successDelta??null,0,true).text}</button></td>,<td key={`${name}/p95`} className="p-1"><button className={`min-h-10 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary ${tone(item?.p95Ratio??null,1)}`} onClick={()=>open(row,name)} aria-label={`${name} p95 상세`}>{experimentDifference(item?.p95Ratio??null,1).text}</button></td>]})}
        </tr>)}</tbody>
      </table>
    </section>
    <section aria-label="구간별 요청률과 지연" className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">구간별 요청률·p95</h2><label className="flex items-center gap-2 text-sm">서비스<select className="min-h-10 max-w-80 rounded border bg-background px-2" value={service} onChange={event=>commitExperimentSelection(window.location,window.history,{service:event.target.value})}><option value="">전체</option>{names.map(name=><option key={name} value={name}>{name}</option>)}</select></label></div>
      <div className="h-[250px]" role="img" aria-label="구간별 요청률 막대와 선택 서비스의 왼쪽·오른쪽 p95 및 정의 SLO">
        <ResponsiveContainer width="100%" height="100%"><ComposedChart accessibilityLayer data={chart} margin={{left:0,right:8,top:10,bottom:0}}>
          <CartesianGrid vertical={false}/><XAxis dataKey="interval" tick={{fontSize:11}}/><YAxis yAxisId="rate" width={60} unit=" req/s" tick={{fontSize:10}}/>{service&&<YAxis yAxisId="latency" orientation="right" width={70} unit=" ms" tick={{fontSize:10}}/>}<Tooltip/><Legend wrapperStyle={{fontSize:11}}/>
          <Bar yAxisId="rate" dataKey="rate" name="계획 평균 요청률" fill="var(--primary)" opacity={service ? .25 : .8} maxBarSize={60} isAnimationActive={false}/>
          {service&&<><Line yAxisId="latency" dataKey="leftP95" name="왼쪽 p95" stroke="var(--color-teal-600, #0d9488)" strokeWidth={2} connectNulls={false} isAnimationActive={false}/><Line yAxisId="latency" dataKey="rightP95" name="오른쪽 p95" stroke="var(--color-slate-500, #64748b)" strokeWidth={2} connectNulls={false} isAnimationActive={false}/><Line yAxisId="latency" dataKey="leftSLO" name="왼쪽 SLO" stroke="var(--heat)" strokeDasharray="4 4" dot={false} connectNulls={false} isAnimationActive={false}/><Line yAxisId="latency" dataKey="rightSLO" name="오른쪽 SLO" stroke="var(--heat)" strokeDasharray="2 5" dot={false} connectNulls={false} isAnimationActive={false}/></>}
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
  const selected=id?(run?[run]:[]):publications
  const historical=useHistoricalExperiments(selected,publications,!!list.data||!!list.error)
  const runs=historical.runs
  if(id)return <Screen width="data" back={["/experiments/results","실험 결과"]} title={run?.definition?.run.name??id} actionPlacement="header" action={<div className="flex flex-wrap gap-2">{run?.definition&&<EvaluationLaunch publicationID={run.id} label="다시 실행"/>}<Button variant="outline" asChild><Link href="/experiments">실행</Link></Button></div>}><QueryGate query={query}>{run&&<><section aria-label="비교 판독" className="space-y-3"><ExperimentOutcome run={run}/><EvaluationCleanup key={run.id} run={run}/>{historical.error?<HistoricalExperimentError refetch={historical.refetch}/>:historical.pending?<strong>조회 중</strong>:<><ExperimentJudgment run={run} runs={runs}/><div className="text-sm">{definitionComparisonReason(run,runs)}</div><ExperimentConclusion run={run} runs={runs}/><div className="grid gap-3 sm:grid-cols-2"><ExperimentLimits run={run} runs={runs}/><ExperimentCoverage run={run} runs={runs}/></div></>}<ExperimentComparisonLegend/></section>{!historical.error&&!historical.pending&&<ExperimentDetail key={run.id} run={run} runs={runs}/>}</>}</QueryGate></Screen>
  return <Screen width="data" title="실험 결과" actionPlacement="header" action={<div className="flex flex-wrap items-center gap-2"><ExperimentComparisonLegend/><Button variant="outline" asChild><Link href="/experiments">실행</Link></Button><Button variant="outline" asChild><Link href="/experiments/load-scenarios">부하 설정</Link></Button><EvaluationLaunch/></div>}><QueryGate query={list}>{list.data&&<section aria-label="실험 결과 표">{historical.error&&<HistoricalExperimentError refetch={historical.refetch}/>}<div className="overflow-x-auto"><table className="w-full text-sm max-md:[&>thead]:sr-only max-md:[&>tbody>tr]:grid max-md:[&>tbody>tr>td]:flex max-md:[&>tbody>tr>td]:max-w-none max-md:[&>tbody>tr>td]:justify-between max-md:[&>tbody>tr>td]:gap-3 max-md:[&>tbody>tr>td]:before:content-[attr(data-label)]"><thead><tr>{["비교 상태","실험","일치 (개)","참고 (개)","대응 없음 (개)","실행","측정","정리","에너지 Δ (%)","최악 성공률 Δ (%p)","최악 p95 (배)","제한 사유"].map((label,index)=><th key={label} className={`p-3 ${[2,3,4,8,9,10].includes(index)?"text-right":"text-left"}`}>{label}</th>)}</tr></thead><tbody>{campaignGroups(publications).map(({run})=><ExperimentResultRow key={run.id} run={run} runs={runs} pending={historical.pending} error={!!historical.error}/>)}</tbody></table></div>{!publications.length&&<Empty title="기록 없음"/>}</section>}</QueryGate></Screen>
}
