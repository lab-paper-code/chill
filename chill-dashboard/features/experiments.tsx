"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { TriangleAlert } from "lucide-react"
import { Bar, CartesianGrid, ComposedChart, Legend, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { campaignGroups, campaignScoreboard, commitExperimentSelection, experimentChartData, experimentHeatmap, experimentPath, experimentRows, resultMetric, stageHistoryLink, stagePolicy, stageRate, stageSeconds, type Experiment, type ExperimentHeatmapRow } from "@/lib/experiments"
import { comparisonValidity, experimentDifference, selectedExperimentStage } from "@/lib/experiments"
import { formatMeasurementNumber } from "@/lib/measurements"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Empty, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"

const number=(value:unknown,unit="")=>typeof value==="bigint"?String(value):typeof value==="number"&&Number.isFinite(value)?formatMeasurementNumber(value,unit):"—"
const tone=(value:number|null,baseline=0,higherIsBetter=false)=>experimentDifference(value,baseline,higherIsBetter).tone

function Conclusion({run}:{run:Experiment}) {
  const {summary,coverageCounts}=campaignScoreboard(run)
  return <dl className="flex flex-wrap gap-2 text-xs tabular-nums" aria-label="실험 결론">
    {[{label:"에너지 Δ (%)",value:summary.energyPercent,baseline:0,higher:false},{label:"최악 성공률 Δ (%p)",value:summary.successDelta,baseline:0,higher:true},{label:"최악 p95 (배)",value:summary.p95Ratio,baseline:1,higher:false}].map(item=><div key={item.label} className={`flex items-center gap-2 rounded border px-3 py-2 ${tone(item.value,item.baseline,item.higher)}`}><dt>{item.label}</dt><dd className="font-semibold">{experimentDifference(item.value,item.baseline,item.higher).text}</dd></div>)}
    {coverageCounts.limited>0&&<div className="flex items-center gap-2 rounded border border-heat px-3 py-2 text-heat"><dt className="flex items-center gap-1"><TriangleAlert className="size-4" aria-hidden="true"/>제한 비교 (개)</dt><dd className="font-semibold">{coverageCounts.limited}</dd></div>}
  </dl>
}

function IntervalDrawer({run,row,service,onClose}:{run:Experiment;row:ExperimentHeatmapRow|undefined;service:string;onClose:()=>void}) {
  const stages=row?[row.stage,...(row.other?[row.other]:[])]:[]
  const names=[...new Set(stages.flatMap(stage=>experimentRows(stage).map(item=>item.name)))].filter(name=>!service||name===service)
  return <Sheet open={!!row} onOpenChange={open=>{if(!open)onClose()}}><SheetContent className="w-full overflow-y-auto sm:max-w-2xl" aria-describedby={undefined}>
    <SheetHeader><SheetTitle>{row?.id}</SheetTitle></SheetHeader>
    {row&&<div className="space-y-5 px-4 pb-6">
      <div className="overflow-x-auto"><table className="w-full text-xs tabular-nums"><thead><tr>{["정책","시작 (UTC)","종료 (UTC)"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{stages.map(stage=><tr key={stage.id} className="border-t"><th className="p-2 text-left font-normal">{stagePolicy(stage)}</th><td className="p-2">{stage.from}</td><td className="p-2">{stage.to}</td></tr>)}</tbody></table></div>
      {row.other&&<table aria-label="비교 조건" className="w-full text-sm"><thead><tr>{["부하","서비스 식별자·정의","측정 규칙"].map(label=><th key={label} className="p-2 text-left">{label}</th>)}</tr></thead><tbody><tr>{Object.entries(comparisonValidity(row.stage,row.other).conditions).map(([key,value])=><td key={key} className="border-t p-2">{value}</td>)}</tr></tbody></table>}
      <div className="overflow-x-auto"><table className="w-full text-sm tabular-nums"><thead><tr>{["정책","요청률 (req/s)","길이 (s)","에너지 (J)","평균 전력 (W)"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead><tbody>{stages.map(stage=>{const seconds=stageSeconds(stage),energy=stage.kpis.wholePoolJoules;return <tr key={stage.id} className="border-t"><th className="p-2 text-left font-normal">{stagePolicy(stage)}</th><td className="p-2 text-right">{number(stageRate(stage),"req/s")}</td><td className="p-2 text-right">{number(seconds,"s")}</td><td className="p-2 text-right">{number(energy,"J")}</td><td className="p-2 text-right">{number(typeof energy==="number"&&seconds?energy/seconds:null,"W")}</td></tr>})}</tbody></table></div>
      <div className="overflow-x-auto"><table className="w-full text-sm tabular-nums"><thead><tr>{["서비스","정책","성공률 (%)","p95 (ms)","요청 N","p95 N"].map(label=><th key={label} className="p-2 text-right first:text-left">{label}</th>)}</tr></thead><tbody>{names.flatMap(name=>stages.map(stage=>{
        const candidates=experimentRows(stage).filter(item=>item.name===name),item=candidates.length===1?candidates[0]:undefined
        const success=item?resultMetric(item,"success"):null,p95=item?resultMetric(item,"p95"):null
        return <tr key={`${name}/${stage.id}`} className="border-t"><th className="max-w-52 break-words p-2 text-left font-normal">{name}</th><td className="p-2">{stagePolicy(stage)}</td><td className="p-2 text-right">{number(typeof success?.value==="number"?success.value*100:null,"%")}</td><td className="p-2 text-right">{number(p95?.value,"ms")}</td><td className="p-2 text-right">{number(success?.denominator)}</td><td className="p-2 text-right">{number(p95?.denominator)}</td></tr>
      }))}</tbody></table></div>
      <div className="flex flex-wrap gap-2">{stages.map(stage=><Button key={stage.id} variant="outline" asChild><Link prefetch={false} href={stageHistoryLink(stage)}>{stagePolicy(stage)} · 판단·전환 기록 열기</Link></Button>)}</div>
      <SourceJSON inline title="원문" value={{experiment:run.id,stages,sources:run.sources}}/>
      <SourceExport filename="experiment-interval.json" value={{experiment:run.id,stages,sources:run.sources}}/>
    </div>}
  </SheetContent></Sheet>
}

function ExperimentDetail({run}:{run:Experiment}) {
  const rows=React.useMemo(()=>experimentHeatmap(run),[run])
  const names=React.useMemo(()=>[...new Set(run.stages.flatMap(stage=>experimentRows(stage).map(row=>row.name)))],[run])
  const [service,setService]=React.useState("")
  const params=useSearchParams()
  const selected=selectedExperimentStage(run,params.get("a")??"",params.get("as"))
  const chart=React.useMemo(()=>experimentChartData(run,service),[run,service])
  const row=rows.find(row=>row.stage===selected&&(!row.other||params.get("b")===run.id&&params.get("bs")===row.other.id))
  function open(row:ExperimentHeatmapRow,name="") {
    if(name)setService(name)
    commitExperimentSelection(window.location,window.history,{a:run.id,as:row.id,b:row.other?run.id:"",bs:row.other?.id??""})
  }
  return <>
    <section aria-label="구간별 정책 차이" className="overflow-x-auto rounded-lg border" tabIndex={0}>
      <table className="w-full text-sm tabular-nums">
        <thead className="bg-muted/50"><tr><th scope="col" rowSpan={2} className="p-3 text-left">구간</th><th scope="col" rowSpan={2} className="p-3 text-right">부하<br/>req/s</th><th scope="col" rowSpan={2} className="p-3 text-right">에너지 Δ<br/>%</th>{names.map(name=><th key={name} scope="colgroup" colSpan={2} className="max-w-56 break-words border-l p-2 text-center font-medium">{name}</th>)}</tr><tr>{names.flatMap(name=>[<th key={`${name}/success`} scope="col" className="border-l px-2 py-2 text-right text-xs">성공률 Δ %p</th>,<th key={`${name}/p95`} scope="col" className="px-2 py-2 text-right text-xs">p95 배율</th>])}</tr></thead>
        <tbody>{rows.map((row,index)=><tr key={row.key} className={`border-t ${row.other?"":"bg-muted text-muted-foreground"}`}>
          <th scope="row" className="whitespace-nowrap p-1 text-left font-medium"><button type="button" className="flex min-h-10 w-full items-center gap-2 rounded px-2 text-left focus-visible:outline-2 focus-visible:outline-primary" onClick={()=>open(row)} title={row.id}>{String(index+1).padStart(2,"0")}{!row.other&&<span title={row.reason}><TriangleAlert aria-label="비교 불가" className="size-4"/></span>}{row.other&&comparisonValidity(row.stage,row.other).limited&&<TriangleAlert aria-label="비교 제한" className="size-4 text-heat"/>}</button></th>
          <td className="p-1 text-right"><button className="min-h-10 w-full rounded px-2 text-right focus-visible:outline-2 focus-visible:outline-primary" onClick={()=>open(row)}>{number(row.rate,"req/s")}</button></td>
          <td className="p-1"><button className={`min-h-10 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary ${tone(row.energyPercent)}`} onClick={()=>open(row)}>{experimentDifference(row.energyPercent).text}</button></td>
          {names.flatMap(name=>{const item=row.services.find(item=>item.name===name);return [<td key={`${name}/success`} className="border-l p-1"><button className={`min-h-10 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary ${tone(item?.successDelta??null,0,true)}`} onClick={()=>open(row,name)} aria-label={`${name} 성공률 상세`}>{experimentDifference(item?.successDelta??null,0,true).text}</button></td>,<td key={`${name}/p95`} className="p-1"><button className={`min-h-10 w-full rounded px-2 text-right font-semibold focus-visible:outline-2 focus-visible:outline-primary ${tone(item?.p95Ratio??null,1)}`} onClick={()=>open(row,name)} aria-label={`${name} p95 상세`}>{experimentDifference(item?.p95Ratio??null,1).text}</button></td>]})}
        </tr>)}</tbody>
      </table>
    </section>
    <section aria-label="구간별 요청률과 지연" className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">구간별 요청률·p95</h2><label className="flex items-center gap-2 text-sm">서비스<select className="min-h-10 max-w-80 rounded border bg-background px-2" value={service} onChange={event=>setService(event.target.value)}><option value="">요청률</option>{names.map(name=><option key={name} value={name}>{name}</option>)}</select></label></div>
      <div className="h-[250px]" role="img" aria-label="구간별 요청률 막대와 선택 서비스의 자동·고정 p95 및 당시 SLO">
        <ResponsiveContainer width="100%" height="100%"><ComposedChart accessibilityLayer data={chart} margin={{left:0,right:8,top:10,bottom:0}}>
          <CartesianGrid vertical={false}/><XAxis dataKey="interval" tick={{fontSize:11}}/><YAxis yAxisId="rate" width={60} unit=" req/s" tick={{fontSize:10}}/>{service&&<YAxis yAxisId="latency" orientation="right" width={70} unit=" ms" tick={{fontSize:10}}/>}<Tooltip/><Legend wrapperStyle={{fontSize:11}}/>
          <Bar yAxisId="rate" dataKey="rate" name="보낸 요청률" fill="var(--primary)" opacity={service ? .25 : .8} maxBarSize={60} isAnimationActive={false}/>
          {service&&<><Line yAxisId="latency" dataKey="automaticP95" name="자동 운영 p95" stroke="var(--color-teal-600, #0d9488)" strokeWidth={2} connectNulls={false} isAnimationActive={false}/><Line yAxisId="latency" dataKey="fixedP95" name="고정 구성 p95" stroke="var(--color-slate-500, #64748b)" strokeWidth={2} connectNulls={false} isAnimationActive={false}/><Line yAxisId="latency" dataKey="automaticSLO" name="자동 운영 SLO" stroke="var(--heat)" strokeDasharray="4 4" dot={false} connectNulls={false} isAnimationActive={false}/><Line yAxisId="latency" dataKey="fixedSLO" name="고정 구성 SLO" stroke="var(--heat)" strokeDasharray="2 5" dot={false} connectNulls={false} isAnimationActive={false}/></>}
        </ComposedChart></ResponsiveContainer>
      </div>
    </section>
    <IntervalDrawer run={run} row={row} service={service} onClose={()=>commitExperimentSelection(window.location,window.history,{as:"",b:"",bs:""})}/>
  </>
}

export function ExperimentsPage() {
  const params=useSearchParams(),id=params.get("a")??""
  const list=useChillQuery<{items:Experiment[]}>("v1/experiments",{decoder:parseSchedulerJSON})
  const query=useChillQuery<Experiment>(experimentPath(id),{enabled:!!id,decoder:parseSchedulerJSON})
  const run=query.data?.id===id?query.data:undefined
  if(id)return <Screen width="data" title={<span className="flex flex-wrap items-center gap-x-4 gap-y-2"><span className="break-all text-xl">{id}</span>{run&&<time className="text-sm font-normal text-muted-foreground" dateTime={run.from}>{new Date(run.from).toLocaleDateString("ko-KR",{timeZone:"Asia/Seoul"})}</time>}</span>} actionPlacement="header" action={<div className="flex flex-wrap items-center gap-3">{run&&<Conclusion run={run}/>}<Button variant="outline" asChild><Link href="/experiments">목록</Link></Button></div>}><QueryGate query={query}>{run&&<ExperimentDetail key={run.id} run={run}/>}</QueryGate></Screen>
  return <Screen width="data" title="실험"><QueryGate query={list}>{list.data&&<section aria-label="실험 목록" className="space-y-3">{campaignGroups(list.data.items).map(({run})=><Link prefetch={false} key={run.id} href={`/experiments?${new URLSearchParams({a:run.id})}`} className="flex flex-wrap items-center justify-between gap-4 rounded-lg border px-5 py-4 focus-visible:outline-2 focus-visible:outline-primary"><div className="min-w-0"><h2 className="break-all font-semibold">{run.id}</h2><time className="text-xs text-muted-foreground" dateTime={run.from}>{new Date(run.from).toLocaleDateString("ko-KR",{timeZone:"Asia/Seoul"})}</time></div><Conclusion run={run}/></Link>)}{!list.data.items.length&&<Empty title="게시 기록 없음"/>}</section>}</QueryGate></Screen>
}
