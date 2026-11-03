"use client"

import * as React from "react"
import { useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import type { LiveState } from "@/lib/live"
import type { OperatingPowerSeries } from "@/lib/read-types"
import { POWER_BUCKETS, mergePowerSeries, powerSegments, powerSeriesPath, type PowerBucket, type PowerPoint } from "@/lib/power-series"
import { displayTime } from "@/lib/live-display"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"

export function usePowerSeries(state:LiveState) {
  const [range]=React.useState(()=>{const to=state.status==="fixture"&&state.metrics.length?Date.parse(state.metrics.at(-1)!.bucketAt)+1000:Date.now();return {from:new Date(to-900000).toISOString(),to:new Date(to).toISOString()}})
  const query=useChillQuery<OperatingPowerSeries>(powerSeriesPath(range.from,range.to)!,{decoder:parseSchedulerJSON,enabled:state.status!=="fixture"})
  const [retained,setRetained]=React.useState(state.metrics)
  React.useEffect(()=>{setRetained(previous=>{
    const rows=[...new Map([...previous,...state.metrics].map(row=>[row.bucketAt,row])).values()].sort((a,b)=>Date.parse(a.bucketAt)-Date.parse(b.bucketAt))
    const latest=rows.length?Date.parse(rows.at(-1)!.bucketAt):0
    return rows.filter(row=>Date.parse(row.bucketAt)>latest-900000).slice(-900)
  })},[state.metrics])
  const end=Math.max(Date.parse(range.to),retained.length?Date.parse(retained.at(-1)!.bucketAt)+1000:0)
  return {query,points:mergePowerSeries(query.data,retained,end)}
}

export function PowerTrend({points,nodeUID}:{points:PowerPoint[];nodeUID?:string}) {
  const segments=powerSegments(points,nodeUID),start=points[0]?.from??0,end=points.at(-1)?.to??start
  const max=Math.max(1,...points.map(point=>(nodeUID?point.nodes[nodeUID]:point.wholeWatts)??0))
  const observed=points.reduce((sum,point)=>sum+((nodeUID?point.nodes[nodeUID]:point.wholeWatts)==null?0:point.to-point.from),0)/1000
  return <figure className="devices-trend"><figcaption title="구간별 실측 평균입니다. 관측 공백은 연결하지 않으며 실측 0 W와 구분합니다.">{nodeUID?"선택 장치":"전체 계측"} 전력 추세 (W) · 눈금 0–{max.toLocaleString("ko-KR",{maximumFractionDigits:1})} W</figcaption>
    {points.length?<><svg viewBox="0 0 120 32" preserveAspectRatio="none" className="h-40 w-full" role="img" aria-label="구간별 전력 추세 (W)">
      <path d="M0 0V28H120" fill="none" stroke="currentColor" opacity=".3"/>
      {segments.map((segment,index)=><polyline key={index} data-from={segment[0].from} data-to={segment.at(-1)!.to} points={segment.flatMap(point=>{const y=28-((nodeUID?point.nodes[nodeUID]:point.wholeWatts)??0)/max*26;return [`${(point.from-start)/(end-start)*120},${y}`,`${(point.to-start)/(end-start)*120},${y}`]}).join(" ")} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke"><title>{segment.map(point=>`${displayTime(new Date(point.from).toISOString(),"KST")} – ${displayTime(new Date(point.to).toISOString(),"KST")}: ${nodeUID?point.nodes[nodeUID]:point.wholeWatts} W`).join("; ")}</title></polyline>)}
    </svg><table aria-label="전력 관측 범위"><thead><tr><th>시작 KST</th><th>종료 KST</th><th>평균 제공 (s)</th><th>전체 (s)</th><th>관측 공백 (s)</th></tr></thead><tbody><tr><td>{displayTime(new Date(start).toISOString(),"KST")}</td><td>{displayTime(new Date(end).toISOString(),"KST")}</td><td>{observed}</td><td>{(end-start)/1000}</td><td>{Math.max(0,(end-start)/1000-observed)}</td></tr></tbody></table></>:<p>전력 관측 없음</p>}
  </figure>
}

export function LivePowerTrend({state}:{state:LiveState}) {
  const {points,query}=usePowerSeries(state)
  return <section aria-label="운영 전력 추세"><h2>전력 추세</h2><table><thead><tr><th>최근 (min)</th><th>기록 간격 (s)</th><th>실시간 간격 (s)</th></tr></thead><tbody><tr><td>15</td><td>5</td><td>1</td></tr></tbody></table><PowerTrend points={points}/>{query.error&&<p role="alert">전력 이력 조회 실패 · 실시간 관측만 표시 <button type="button" onClick={()=>void query.refetch()}>다시 조회</button></p>}</section>
}

export function PowerTimeline({from,to}:{from:string;to:string}) {
  const [bucket,setBucket]=React.useState<PowerBucket>("5s"),path=powerSeriesPath(from,to,bucket)
  const query=useChillQuery<OperatingPowerSeries>(path??"v1/observations/power",{enabled:!!path,decoder:parseSchedulerJSON})
  // The series endpoint owns timeline buckets; the independent period reducer owns the summary.
  const points=mergePowerSeries(query.data,[],Date.parse(to),3600000)
  return <section aria-label="기간 전력 추세"><h3>기간 전력 추세 (W)</h3><label>집계 간격 <select value={bucket} onChange={event=>setBucket(event.target.value as PowerBucket)}>{(Object.keys(POWER_BUCKETS) as PowerBucket[]).map(value=><option key={value} value={value} disabled={!powerSeriesPath(from,to,value)}>{POWER_BUCKETS[value]/1000}초</option>)}</select></label>
    {!path?<p role="status" title="최대 1시간·900구간까지 조회합니다. 기간을 줄이거나 집계 간격을 늘려 주세요. 기간 요약은 별도 집계입니다.">조회 범위 초과</p>:<QueryGate query={query}>{query.data&&<><PowerTrend points={points}/><SourceExport filename="chill-power-timeline.json" value={{source:"v1/observations/power",conditions:{from,to,bucket},result:query.data}}/><SourceJSON title="전력 시간 구간 원본" value={query.data}/></>}</QueryGate>}
  </section>
}
