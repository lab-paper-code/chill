"use client"

import * as React from "react"
import { useChillQuery, useChillQueries } from "@/lib/client"
import type { LiveState } from "@/lib/live"
import type { OperatingPowerSeries } from "@/lib/read-types"
import { POWER_BUCKETS, mergePowerSeries, mergePowerSeriesChunks, parsePowerSeriesJSON, powerSegments, powerSeriesPath, powerSeriesPaths, type PowerBucket, type PowerPoint } from "@/lib/power-series"
import { displayTime } from "@/lib/live-display"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"

export function usePowerSeries(state:LiveState) {
  const [range]=React.useState(()=>{const to=state.status==="fixture"&&state.metrics.length?Date.parse(state.metrics.at(-1)!.bucketAt)+1000:Date.now();return {from:new Date(to-900000).toISOString(),to:new Date(to).toISOString()}})
  const query=useChillQuery<OperatingPowerSeries>(powerSeriesPath(range.from,range.to)!,{decoder:parsePowerSeriesJSON,enabled:state.status!=="fixture"})
  const [history,setHistory]=React.useState(()=>({input:state.metrics,rows:state.metrics}))
  let retained=history.rows
  if(history.input!==state.metrics) {
    const rows=[...new Map([...retained,...state.metrics].map(row=>[row.bucketAt,row])).values()].sort((a,b)=>Date.parse(a.bucketAt)-Date.parse(b.bucketAt))
    const latest=rows.length?Date.parse(rows.at(-1)!.bucketAt):0
    retained=rows.filter(row=>Date.parse(row.bucketAt)>latest-900000).slice(-900)
    setHistory({input:state.metrics,rows:retained})
  }
  const end=Math.max(Date.parse(range.to),retained.length?Date.parse(retained.at(-1)!.bucketAt)+1000:0)
  return {query,points:mergePowerSeries(query.data,retained,end)}
}

export function PowerTrend({points,nodeUID}:{points:PowerPoint[];nodeUID?:string}) {
  const segments=powerSegments(points,nodeUID),start=points[0]?.from??0,end=points.at(-1)?.to??start
  const watts=(point:PowerPoint)=>(nodeUID?point.nodes[nodeUID]:point.wholeWatts)
  const values=segments.flatMap(segment=>segment.map(point=>watts(point)!))
  const minimum=values.length?Math.min(...values):0,maximum=values.length?Math.max(...values):0
  const padding=Math.max((maximum-minimum)*.15,Math.abs(maximum)*.02,.5)
  const roughStep=(maximum-minimum+padding*2)/4,magnitude=10**Math.floor(Math.log10(roughStep))
  const step=([1,2,5,10].find(value=>value>=roughStep/magnitude)??10)*magnitude
  const lower=Math.max(0,Math.floor((minimum-padding)/step)*step),upper=Math.ceil((maximum+padding)/step)*step
  const ticks=[upper,(lower+upper)/2,lower]
  const digits=Math.max(0,Math.min(20,1-Math.floor(Math.log10(step))))
  const x=(at:number)=>(at-start)/Math.max(1,end-start)*100,y=(value:number)=>92-(value-lower)/(upper-lower)*84
  const gaps:{from:number;to:number}[]=[]
  let cursor=start
  for(const segment of segments){
    if(segment[0].from>cursor)gaps.push({from:cursor,to:segment[0].from})
    cursor=segment.at(-1)!.to
  }
  if(cursor<end)gaps.push({from:cursor,to:end})
  const observed=points.reduce((sum,point)=>sum+(watts(point)==null?0:point.to-point.from),0)/1000
  return <figure className="devices-trend" style={{margin:0,color:"var(--primary)",minWidth:0}}>
    <figcaption title="구간별 실측 평균입니다. 관측 공백은 연결하지 않으며 실측 0 W와 구분합니다." style={{color:"var(--muted-foreground)",fontSize:12,marginBottom:8}}>{nodeUID?"선택 장치":"전체 계측"} 전력 추세 (W · KST){gaps.length>0&&<span style={{display:"block",marginTop:4}}>점선 영역은 관측 없음 · 실측 0 W는 선으로 표시</span>}</figcaption>
    {points.length?<><div style={{display:"grid",gridTemplateColumns:"max-content minmax(0, 1fr)",columnGap:8,fontSize:12,fontVariantNumeric:"tabular-nums",color:"var(--muted-foreground)"}}>
      <div style={{position:"relative",minWidth:"3ch"}}>
        {ticks.map((tick,index)=><span key={index} data-power-tick={tick} style={{position:"absolute",top:`${8+index*42}%`,right:0,transform:"translateY(-50%)",whiteSpace:"nowrap"}}>{tick.toLocaleString("ko-KR",{maximumFractionDigits:digits})}</span>)}
        <span aria-hidden="true" style={{visibility:"hidden",whiteSpace:"nowrap"}}>{upper.toLocaleString("ko-KR",{maximumFractionDigits:digits})}</span>
      </div>
      <div style={{position:"relative",minWidth:0}}>
        <svg viewBox="0 0 100 100" preserveAspectRatio="none" style={{display:"block",width:"100%",height:144,overflow:"visible",color:"var(--primary)"}} role="img" aria-label="구간별 전력 추세 (W)">
          <path d="M0 8H100M0 50H100M0 92H100" fill="none" stroke="var(--muted-foreground)" strokeWidth="1" strokeDasharray="2 4" opacity=".3" vectorEffect="non-scaling-stroke"/>
          {gaps.map(gap=><rect key={gap.from} data-gap-from={gap.from} data-gap-to={gap.to} x={x(gap.from)} y="8" width={x(gap.to)-x(gap.from)} height="84" fill="var(--muted-foreground)" fillOpacity=".1" stroke="var(--muted-foreground)" strokeDasharray="3 3" strokeWidth="1" vectorEffect="non-scaling-stroke"><title>{`${displayTime(new Date(gap.from).toISOString(),"KST")} – ${displayTime(new Date(gap.to).toISOString(),"KST")}: 관측 없음`}</title></rect>)}
          {segments.map((segment,index)=><polyline key={index} data-from={segment[0].from} data-to={segment.at(-1)!.to} points={segment.flatMap(point=>[`${x(point.from)},${y(watts(point)!)}`,`${x(point.to)},${y(watts(point)!)}`]).join(" ")} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke"><title>{segment.map(point=>`${displayTime(new Date(point.from).toISOString(),"KST")} – ${displayTime(new Date(point.to).toISOString(),"KST")}: ${watts(point)} W`).join("; ")}</title></polyline>)}
        </svg>
        {!values.length&&<span style={{position:"absolute",inset:0,display:"grid",placeItems:"center",color:"var(--foreground)"}}>전력 관측 없음</span>}
      </div>
      <div aria-hidden="true"/>
      <div style={{display:"flex",justifyContent:"space-between",gap:4,whiteSpace:"nowrap"}}>
        {[start,(start+end)/2,end].map((at,index)=><time key={index} dateTime={new Date(at).toISOString()} title={displayTime(new Date(at).toISOString(),"KST")}>{displayTime(new Date(at).toISOString(),"KST").replace(" KST","")}</time>)}
      </div>
    </div><details className="devices-trend-range" style={{overflowX:"auto",color:"var(--muted-foreground)",fontSize:12}}><summary style={{cursor:"pointer",padding:"12px 0",minHeight:44}}>관측 시간 상세</summary><table aria-label="전력 관측 범위" style={{minWidth:440,fontSize:12}}><thead><tr><th>시작 KST</th><th>종료 KST</th><th>평균 제공 (s)</th><th>전체 (s)</th><th>관측 공백 (s)</th></tr></thead><tbody><tr><td>{displayTime(new Date(start).toISOString(),"KST")}</td><td>{displayTime(new Date(end).toISOString(),"KST")}</td><td>{observed}</td><td>{(end-start)/1000}</td><td>{Math.max(0,(end-start)/1000-observed)}</td></tr></tbody></table></details></>:<p>전력 관측 없음</p>}
  </figure>
}

export function LivePowerTrend({state}:{state:LiveState}) {
  const {points,query}=usePowerSeries(state)
  const observed=points.some(point=>point.wholeWatts!==null)
  return <section aria-label="운영 전력 추세"><h2>전력 추세</h2><table><thead><tr><th>최근 (min)</th><th>기록 간격 (s)</th><th>실시간 간격 (s)</th></tr></thead><tbody><tr><td>15</td><td>5</td><td>1</td></tr></tbody></table>{observed||query.data||state.status==="fixture"?<PowerTrend points={points}/>:!query.error&&<p role="status">전력 이력 조회 중</p>}{query.error&&<p role="alert" title={query.error.body}>{query.error.status===0&&query.error.body.includes("형식 불일치")?"전력 형식 불일치":"전력 이력 조회 실패"} · {observed?"실시간 관측만 표시":"실시간 관측 미확인"} <button type="button" onClick={()=>void query.refetch()}>다시 조회</button></p>}</section>
}

export function PowerTimeline({from,to}:{from:string;to:string}) {
  const [bucket,setBucket]=React.useState<PowerBucket>("5s"),paths=powerSeriesPaths(from,to,bucket)
  const queries=useChillQueries<OperatingPowerSeries>(paths??[],{enabled:!!paths,decoder:parsePowerSeriesJSON})
  const error=queries.find(query=>query.error)?.error??null,isPending=queries.some(query=>query.isPending)
  const query={
    data:error||isPending?undefined:mergePowerSeriesChunks(queries.map(query=>query.data)),
    error,isPending,isFetching:queries.some(query=>query.isFetching),
    failureReason:queries.find(query=>query.failureReason)?.failureReason,
    dataUpdatedAt:queries.length?Math.min(...queries.map(query=>query.dataUpdatedAt)):0,
    refetch:()=>Promise.all(queries.map(query=>query.refetch())),
  }
  // The series endpoint owns timeline buckets; the independent period reducer owns the summary.
  const points=mergePowerSeries(query.data,[],Date.parse(to),3600000)
  return <section aria-label="기간 전력 추세"><h3>기간 전력 추세 (W)</h3><label>집계 간격 <select value={bucket} onChange={event=>setBucket(event.target.value as PowerBucket)}>{(Object.keys(POWER_BUCKETS) as PowerBucket[]).map(value=><option key={value} value={value} disabled={!powerSeriesPath(from,to,value)}>{POWER_BUCKETS[value]/1000}초</option>)}</select></label>
    {!paths?<p role="status" title="최대 1시간·900구간까지 조회합니다. 기간을 줄이거나 집계 간격을 늘려 주세요. 기간 요약은 별도 집계입니다.">조회 범위 초과</p>:<QueryGate query={query}>{query.data&&<><PowerTrend points={points}/><SourceExport filename="chill-power-timeline.json" value={{source:"v1/observations/power",conditions:{from,to,bucket,format:"columns"},requests:paths,result:query.data}}/><SourceJSON title="전력 시간 구간 원본" value={query.data}/></>}</QueryGate>}
  </section>
}
