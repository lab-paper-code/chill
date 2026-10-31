"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import type { SchedulerObservationPage } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { object } from "@/lib/experiments"
import { campaignGroups, campaignScoreboard, campaignTradeoffs, experimentSelection, policyChartStyle, stageActivity, stageHistoryLink, commitExperimentSelection, comparisonDirection, policyStageGroups, serviceDelta, stageChartData, stagePolicy, alphaText, comparisonRows, differentMeasurementRules, difference, evidenceError, experimentRows, formatResult, relativeEnergy, resultMetric, stageRate, stageSeconds, stageLoadLabel, type Experiment, type ExperimentRow, type ExperimentStage, type PublishedMetric } from "@/lib/experiments"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Screen, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Bar, BarChart, CartesianGrid, Cell, ErrorBar, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"

// Published nested objects are readable facts, never JSON strings in the result table.
function PublishedFacts({value}:{value:unknown}) {
  if(value==null)return <span>미제공</span>
  if(typeof value!=="object")return <span>{String(value)}</span>
  return <details><summary className="cursor-pointer py-2">원본 필드 {Object.keys(value).length}개</summary><dl className="max-h-80 space-y-1 overflow-auto">{Object.entries(value).map(([key,item])=><div key={key} className="border-l pl-2"><dt className="text-muted-foreground">{key}</dt><dd><PublishedFacts value={item}/></dd></div>)}</dl></details>
}
function MetricValue({metric}:{metric:PublishedMetric}) {
  return <div className="space-y-1">
    <p className="font-semibold tabular-nums">{formatResult(metric.value,metric.unit)}</p>
    <p className="text-xs text-muted-foreground">{metric.unit==="ms"?`표본 N ${metric.denominator??"미제공"}`:`${metric.numerator??"미제공"} / ${metric.denominator??"미제공"}`}</p>
    <details className="text-xs"><summary className="cursor-pointer py-2">원값·모집단·출처</summary>
      <dl className="space-y-2 break-all"><dt>원본 정밀도 (출처 단위)</dt><dd><PublishedFacts value={metric.rawValue}/></dd><dt>모집단</dt><dd><PublishedFacts value={metric.population}/></dd><dt>출처</dt><dd>{metric.source}</dd><dt>결손</dt><dd><PublishedFacts value={metric.reason}/></dd></dl>
    </details>
  </div>
}
function Identity({row}:{row?:ExperimentRow}) {
  return row?<details className="space-y-1 break-all text-xs text-muted-foreground"><summary className="cursor-pointer py-2">{row.uid==null||row.definition==null?"당시 서비스 식별 정보 미기록":"당시 서비스 식별 정보"}</summary><p>등록 식별자 {String(row.uid??"미제공")}</p><p>적용 정의 {String(row.definition??"미제공")}</p><PublishedFacts value={row.identitySource}/></details>:<span className="text-muted-foreground">한쪽 기록 없음</span>
}
function Substages({row}:{row?:ExperimentRow}) {
  if(!row)return <span>한쪽 기록 없음</span>
  return <details className="space-y-2 break-all text-xs"><summary className="cursor-pointer py-2">예정 하위 단계 {row.substages?.length??0}개</summary><div className="max-h-80 overflow-auto">
    <p className="text-muted-foreground">부하 생성 시작 기준 예정 구간</p>
    {!row.substages?.length?<p>세부 부하 계획 미기록</p>:<ol className="space-y-3">{row.substages.map((part,index)=><li key={index} className="space-y-1 border-l pl-2">
      <p className="font-medium">{String(part.id??"ID 미제공")} · {alphaText(part.alpha,row.missingReasons.alpha)}</p>
      {part.alpha===null&&<p>α 결손: {String(row.missingReasons.alpha??"과거 α 기록 없음")}</p>}
      <p className="tabular-nums">예정 offset [{String(part.startOffsetNanos??"미제공")}, {String(part.endOffsetNanos??"미제공")}) ns</p>
      <p className="tabular-nums">예정 시각 [{String(part.from??"미제공")}, {String(part.to??"미제공")})</p>
      {(part.from==null||part.to==null)&&<p>부하 생성 시작 시각 미기록</p>}
      <p>계획 출처</p><PublishedFacts value={part.source}/>
      {part.source==null&&<p>{String(row.missingReasons.substageSource??"출처 기록 없음")}</p>}
    </li>)}</ol>}
  </div></details>
}
function ServiceRows({name,left,right,compare,aName,bName}:{name:string;left:ExperimentRow[];right:ExperimentRow[];compare:boolean;aName:string;bName:string}) {
  const [aIndex,setA]=React.useState(""),[bIndex,setB]=React.useState("")
  const a=left.length===1?left[0]:left[Number(aIndex)],b=right.length===1?right[0]:right[Number(bIndex)]
  const chosenA=left.length>1&&aIndex===""?undefined:a,chosenB=right.length>1&&bIndex===""?undefined:b
  const candidate=(rows:ExperimentRow[],value:string,set:(v:string)=>void)=>rows.length>1?<label className="grid gap-1 text-xs">동명 후보 선택<select aria-label={`${name} 후보`} className="min-h-11 max-w-full rounded border bg-background p-2" value={value} onChange={e=>set(e.target.value)}><option value="">비교 후보를 선택하세요</option>{rows.map((row,i)=><option key={row.source} value={i}>{row.source} / {String(row.uid??"식별자 미기록")}</option>)}</select></label>:null
  return <><tr className="border-t bg-muted/40"><th scope="row" className="p-3 text-left align-top break-words">{name}</th><td className="p-3 align-top">{candidate(left,aIndex,setA)}<Identity row={chosenA}/></td>{compare&&<><td className="p-3 align-top">{candidate(right,bIndex,setB)}<Identity row={chosenB}/></td><td className="p-3 align-top text-xs">{chosenA&&chosenB?(chosenA.uid==null||chosenB.uid==null||chosenA.definition==null||chosenB.definition==null?"서비스 식별 정보 미기록":chosenA.uid===chosenB.uid&&chosenA.definition===chosenB.definition?"같은 등록·정의":"등록 또는 정의 다름"):"비교 대상 없음"}</td></>}</tr>{(["success","http","p95"] as const).map(key=>{
    const am=chosenA?resultMetric(chosenA,key):null,bm=chosenB?resultMetric(chosenB,key):null
    const sameMeaning=key!=="success"||am?.key===bm?.key
    return <tr key={key} className="border-t"><th scope="row" className="p-3 text-left align-top font-normal">{key==="success"?"기한·SLO 내 성공 / 보낸 요청":key==="http"?"HTTP 성공 / 보낸 요청":"성공 응답 p95"}</th><td className="p-3 align-top">{am?<><p className="text-xs text-muted-foreground">{am.label}</p><MetricValue metric={am}/></>:"미제공"}</td>{compare&&<><td className="p-3 align-top">{bm?<><p className="text-xs text-muted-foreground">{bm.label}</p><MetricValue metric={bm}/></>:"미제공"}</td><td className="p-3 align-top tabular-nums">{difference(am?.value,bm?.value,key==="p95"?"ms":"%",sameMeaning)}<p className="mt-1 text-xs">{sameMeaning?comparisonDirection(am?.value,bm?.value,key==="p95"?"ms":"%",aName,bName):"성공 지표 정의가 달라 방향 비교 불가"}</p></td></>}</tr>
  })}<tr className="border-t"><th scope="row" className="p-3 text-left align-top font-normal">α · 예정 하위 단계</th><td className="p-3 align-top"><Substages row={chosenA}/></td>{compare&&<><td className="p-3 align-top"><Substages row={chosenB}/></td><td className="p-3 align-top text-xs">게시된 workload별 계획 · 관측 전환/인과 차이 아님</td></>}</tr><tr className="border-t"><td colSpan={compare?4:2} className="p-3"><details className="text-xs"><summary className="cursor-pointer py-2">보낸 요청·결과별 수·SLO·원본 정밀도</summary><div className="grid gap-4 sm:grid-cols-2"><div><h4>A 기록</h4><PublishedFacts value={chosenA?.original}/></div>{compare&&<div><h4>B 기록</h4><PublishedFacts value={chosenB?.original}/></div>}</div></details></td></tr></>
}
function Rules({a,b}:{a:ExperimentStage;b:ExperimentStage}) {
  const keys=differentMeasurementRules(a,b),left=stageSeconds(a),right=stageSeconds(b)
  return <aside className="space-y-2 rounded-lg border border-heat/40 bg-heat/5 p-4 text-sm"><p className="font-medium">우열 결론 검토: {keys.length?"측정 규칙 차이 있음":!a.kpis.measurementRules||!b.kpis.measurementRules?"측정 규칙 미기록":"측정 규칙 차이 없음"} / {left===null||right===null?"구간 길이 미제공":left===right?"구간 길이 같음":"구간 길이 다름"}</p><p>A {formatResult(left,"s")} / B {formatResult(right,"s")}{left!==right&&<> / 길이 차이 {difference(left,right,"s")}</>}. 실행 조건과 SLO의 미제공 항목은 원본에서 확인하세요.</p><details><summary className="cursor-pointer py-2">측정 규칙과 차이 자세히</summary>{keys.length>0&&<p>{keys.join(", ")}</p>}<div className="grid gap-4 sm:grid-cols-2"><PublishedFacts value={a.kpis.measurementRules}/><PublishedFacts value={b.kpis.measurementRules}/></div></details></aside>
}
function Energy({stage,other}:{stage:ExperimentStage;other?:ExperimentStage}) {
  return <section className="space-y-3" aria-label="공유 POOL 에너지"><h3 className="font-semibold">공유 POOL 에너지 · 서비스 행 합산·배분 금지</h3><div className="overflow-x-auto" tabIndex={0} role="region" aria-label="공유 에너지 비교표"><table className="w-full min-w-[580px] text-sm"><thead><tr><th className="p-3 text-left">범위</th><th className="p-3 text-left">A</th>{other&&<><th className="p-3 text-left">B</th><th className="p-3 text-left">A−B</th><th className="p-3 text-left">B 대비 변화</th></>}</tr></thead><tbody>{(["wholePoolJoules","activePoolJoules"] as const).map(key=><tr key={key} className="border-t tabular-nums"><th className="p-3 text-left font-normal">{key==="wholePoolJoules"?"전체 POOL":"활성 POOL"}</th><td className="p-3">{formatResult(stage.kpis[key],"J")}</td>{other&&<><td className="p-3">{formatResult(other.kpis[key],"J")}</td><td className="p-3">{difference(stage.kpis[key],other.kpis[key],"J")}</td><td className="p-3">{relativeEnergy(stage.kpis[key],other.kpis[key])}</td></>}</tr>)}</tbody></table></div><p className="text-sm text-muted-foreground">기간·부하가 다르면 총 J가 낮다는 사실만으로 효율 개선이라고 할 수 없어요. ±는 95% CI가 아니라 관측 공백 오차 상한이에요.</p><div className="grid gap-4 sm:grid-cols-2">{[stage,...other?[other]:[]].map((s,i)=><div key={i} className="space-y-2 text-sm"><p>{i===0?"A":"B"} 공백 오차 상한 ± {formatResult(s.kpis.gapErrorBoundJoules,"J")}</p><details><summary className="cursor-pointer py-2">에너지 원값·관측 범위·결손</summary><PublishedFacts value={{wholePoolJoules:s.kpis.wholePoolJoules,activePoolJoules:s.kpis.activePoolJoules,energyPopulation:s.kpis.energyPopulation,energyCoverage:s.kpis.energyCoverage,missingReasons:object(s.kpis.missingReasons).energy,source:"kpis.wholePoolJoules / activePoolJoules / gapErrorBoundJoules"}}/></details></div>)}</div></section>
}
function Results({stage,other,a,b}:{stage:ExperimentStage;other?:ExperimentStage;a:string;b:string}) {
  const rows=comparisonRows(stage,other)
  return <section id="experiment-results" tabIndex={-1} className="space-y-4 scroll-mt-4 focus-visible:outline-2 focus-visible:outline-primary" aria-label="단계 결과 표">
    <h2 className="text-xl font-semibold">{other?"서비스별 운영 결과 비교":"서비스별 측정 결과"}</h2>
    <p className="text-sm text-muted-foreground">성공률은 기한·SLO를 충족한 요청 / 보낸 요청, p95는 성공 응답의 지연입니다.</p>
    {!other&&<p className="text-xs text-muted-foreground">비교할 구성을 고르면 결과 차이가 표시돼요.</p>}
    <p className="text-xs text-muted-foreground md:hidden">표를 좌우로 이동하면 B와 A−B 마지막 열까지 볼 수 있어요.</p>
    <div className="overflow-x-auto rounded-lg border" tabIndex={0} role="region" aria-label="서비스별 A/B 비교표">
      <table className="w-full min-w-[700px] text-sm"><caption className="sr-only">기록 당시 서비스명 기준의 운영 결과 비교</caption>
        <thead className="bg-muted/60"><tr><th scope="col" className="w-1/4 p-3 text-left">서비스 / 지표</th><th scope="col" className="p-3 text-left break-all">A · {stagePolicy(stage)}<details className="font-normal text-xs"><summary className="cursor-pointer py-2">기록·구간 코드</summary>{a} / {stage.id}</details></th>{other&&<><th scope="col" className="p-3 text-left break-all">B · {stagePolicy(other)}<details className="font-normal text-xs"><summary className="cursor-pointer py-2">기록·구간 코드</summary>{b} / {other.id}</details></th><th scope="col" className="p-3 text-left">결과 차이 A−B</th></>}</tr></thead>
        <tbody>{rows.map(row=><ServiceRows key={`${a}/${stage.id}/${b}/${other?.id}/${row.name}`} name={row.name} left={row.a} right={row.b} compare={!!other} aName={`${stagePolicy(stage)} (${stage.id})`} bName={other?`${stagePolicy(other)} (${other.id})`:"B"}/>)}</tbody>
      </table>
    </div>
    {!rows.length&&<Empty title="지원하는 게시 KPI가 없어요"><p>workloads / requests KPI 미제공. 게시 원본을 확인해 주세요.</p></Empty>}
    <Energy stage={stage} other={other}/><SourceJSON title="게시 KPI 원본 (펼쳐서 확인)" value={{a:stage.kpis,b:other?.kpis??null}}/>
  </section>
}
function Timeline({run,selected,label,onSelect}:{run:Experiment;selected?:ExperimentStage;label:string;onSelect:(id:string)=>void}) {
  const start=Date.parse(run.from),span=Date.parse(run.to)-start
  const stages=[...run.stages].sort((a,b)=>Date.parse(a.from)-Date.parse(b.from))
  return <section className="min-w-0 space-y-3">
    <h3 className="font-semibold">{label} 단계와 실제 구간</h3>
    <p className="break-words text-xs text-muted-foreground">{run.from} ~ {run.to}</p>
    <div className="relative h-5 overflow-hidden rounded border" aria-hidden="true">{stages.map((stage,index)=>span>0&&stageSeconds(stage)!==null?<span key={`${stage.id}/${index}`} style={{left:`${100*(Date.parse(stage.from)-start)/span}%`,width:`${100*(Date.parse(stage.to)-Date.parse(stage.from))/span}%`}} className={`absolute h-full border-r ${selected?.id===stage.id?"bg-primary":"bg-primary/20"}`}/>:null)}</div>
    <ol className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">{stages.map((stage,index)=><li key={`${stage.id}/${index}`}><button type="button" onClick={()=>onSelect(stage.id)} aria-pressed={selected?.id===stage.id} className={`min-h-11 w-full rounded border p-3 text-left focus-visible:outline-2 focus-visible:outline-primary ${selected?.id===stage.id?"border-primary bg-primary/10":"hover:bg-muted"}`}><span className="block break-words font-medium">{index+1}. {stage.id}</span><span className="block text-xs tabular-nums">{formatResult(stageSeconds(stage),"s")} / {formatResult(stageRate(stage),"req/s")}</span></button></li>)}</ol>
    {selected&&<details className="rounded border p-3"><summary className="cursor-pointer py-2 text-sm">선택 단계의 서비스 식별 정보와 세부 부하 계획</summary><div className="space-y-3">{experimentRows(selected).map(row=><div key={row.source}><h4 className="break-words font-medium">{row.name}</h4><Identity row={row}/><Substages row={row}/></div>)}</div></details>}
  </section>
}

function PolicyDot({cx,cy,value,policy,hollow=false,r=4}:{cx?:number;cy?:number;value?:unknown;policy:string;hollow?:boolean;r?:number}) {
  if(cx==null||cy==null||typeof value!=="number"||!Number.isFinite(value))return <g/>
  const {color,shape}=policyChartStyle(policy),fill=hollow?"var(--background)":color
  return shape==="square"?<rect x={cx-r} y={cy-r} width={r*2} height={r*2} fill={fill} stroke={color} strokeWidth={2}/>:shape==="diamond"?<polygon points={`${cx},${cy-r} ${cx+r},${cy} ${cx},${cy+r} ${cx-r},${cy}`} fill={fill} stroke={color} strokeWidth={2}/>:<circle cx={cx} cy={cy} r={r} fill={fill} stroke={color} strokeWidth={2}/>
}

function StageCharts({run}:{run:Experiment}) {
  const data=stageChartData(run),names=[...new Set(data.flatMap(point=>Object.keys(point.services)))]
  const {policies,groups}=policyStageGroups(run)
  const color=(policy:string)=>policyChartStyle(policy).color
  const chart=(title:string,unit:string,get:(point:typeof data[number])=>number|null,bar=false,service?:string)=>{
    const points=groups.map(({axis,series})=>Object.fromEntries([["axis",axis],...series.flatMap((point,index)=>{
      const value=point?get(point):null
      return [[`p${index}`,bar||point?.paired?value:null],[`u${index}`,point&&!point.paired?value:null],
        [`s${index}`,service?point?.services[service]?.sloMS??null:null],[`e${index}`,point?.wattError??null],[`paired${index}`,point?.paired??false]]
    })]))
    const xAxis=<XAxis dataKey="axis" interval={0} height={65} angle={-35} textAnchor="end" tick={{fontSize:11,fill:"var(--foreground)"}} tickFormatter={(_,index)=>String(index+1)}/>
    const yAxis=<YAxis width={54} domain={unit==="%"?[0,100]:undefined} tick={{fontSize:11,fill:"var(--foreground)"}} tickFormatter={value=>formatResult(unit==="%"?Number(value)/100:Number(value),unit).replace(` ${unit}`,"")}/>
    const legend=<Legend wrapperStyle={{fontSize:11}}/>
    const tooltip=<Tooltip formatter={value=>formatResult(typeof value==="number"?(unit==="%"?value/100:value):null,unit)}/>
    return <figure className="min-w-0 space-y-2" key={title}>
      <figcaption className="text-sm font-medium">{title} ({unit})</figcaption>
      <div className="h-[180px] w-full" role="img" aria-label={`${title} · 고정 구성 회색 사각, 자동 운영 청록 원 · 비대응은 속 빈 마커 · 결측은 빈 구간`}>
        <ResponsiveContainer width="100%" height="100%">{bar?
          <BarChart accessibilityLayer aria-label={`${title} 단계별 막대 차트`} data={points} margin={{left:0,right:8,top:8,bottom:0}}>
            <CartesianGrid vertical={false}/>{xAxis}{yAxis}{tooltip}{legend}
            {policies.map((policy,index)=><Bar key={policy} dataKey={`p${index}`} name={policy} legendType={policyChartStyle(policy).shape} fill={color(policy)} stroke={color(policy)} strokeDasharray={policy==="고정 구성"?"3 2":undefined} isAnimationActive={false}>
              {points.map((point,i)=><Cell key={i} fill={point[`paired${index}`]?color(policy):"var(--background)"}/>)}
              <ErrorBar dataKey={`e${index}`} width={4} stroke="var(--foreground)"/>
            </Bar>)}
          </BarChart>:
          <LineChart accessibilityLayer aria-label={`${title} 단계별 선 차트`} data={points} margin={{left:0,right:8,top:8,bottom:0}}>
            <CartesianGrid vertical={false}/>{xAxis}{yAxis}{tooltip}{legend}
            {policies.map((policy,index)=><React.Fragment key={policy}>
              <Line dataKey={`p${index}`} name={policy} legendType={policyChartStyle(policy).shape} stroke={color(policy)} strokeWidth={2} dot={props=><PolicyDot {...props} policy={policy} r={4}/>} activeDot={props=><PolicyDot {...props} policy={policy} r={5}/>} isAnimationActive={false} connectNulls={false}/>
              <Line dataKey={`u${index}`} name={`${policy} 비대응`} stroke="none" legendType="none" dot={props=><PolicyDot {...props} policy={policy} hollow r={4}/>} activeDot={props=><PolicyDot {...props} policy={policy} hollow r={5}/>} isAnimationActive={false} connectNulls={false}/>
              {service&&<Line dataKey={`s${index}`} name={`${policy} 당시 p95 목표`} legendType="plainline" type="stepAfter" stroke="var(--heat)" strokeDasharray="2 4" dot={false} isAnimationActive={false} connectNulls={false}/>}
            </React.Fragment>)}
          </LineChart>}
        </ResponsiveContainer>
      </div>
      {points.some(point=>policies.some((_,index)=>point[`p${index}`]==null&&point[`u${index}`]==null))&&<p className="text-xs text-muted-foreground">결측: {points.flatMap((point,index)=>policies.filter((_,policyIndex)=>point[`p${policyIndex}`]==null&&point[`u${policyIndex}`]==null).map(policy=>`${index+1}단계 ${policy}`)).join(", ")}. 선을 연결하거나 0으로 대체하지 않습니다.</p>}
    </figure>
  }
  return <section aria-label="단계별 부하와 결과" className="min-w-0 space-y-5">
    <h2 className="text-xl font-semibold">정책별 단계 비교</h2>
    <p className="text-sm text-muted-foreground">비교 구간은 처음 실행된 시각 순서예요. 서로 다른 시각에 실행된 같은 단계·부하를 나란히 놓은 비교 축이며 실제 연속 시간축은 아니에요.</p>
    <p className="text-sm">고정 구성: 회색 사각 · 자동 운영: 청록 원 · 당시 p95 목표: 주황 점선. 단계 코드가 다른 비교는 속 빈 마커예요. 결측은 비워 두며 전환의 인과 효과로 단정하지 않아요.</p>
    <ol className="flex flex-wrap gap-x-5 gap-y-2 text-sm" aria-label="차트 구간 범례">{groups.map(({axis,series},index)=><li key={axis}><strong>{index+1}.</strong> {formatResult(series.find(Boolean)?.rate,"req/s")} <span className="text-muted-foreground">({axis})</span></li>)}</ol>
    <div className="grid min-w-0 gap-4 md:grid-cols-2">{chart("평균 보낸 요청률","req/s",point=>point.rate)}{chart("전체 POOL 평균 전력","W",point=>point.watts,true)}</div>
    <p className="text-xs text-muted-foreground">평균 전력은 구간의 전체 에너지를 실제 실행 시간으로 나눈 값이에요. 오차막대는 전력 기록이 비어 있는 동안의 오차 한도예요.</p>
    <div className="grid min-w-0 gap-5 md:grid-cols-2 xl:grid-cols-3">{names.map(name=><article key={name} className="min-w-0 space-y-4 border-t pt-3" aria-label={`${name} 성공률과 p95`}>
      <h3 className="break-words font-semibold">{name}</h3>
      {chart("기한·SLO 내 성공률","%",point=>point.services[name]?.success??null)}
      {chart("성공 응답 p95","ms",point=>point.services[name]?.p95??null,false,name)}
    </article>)}</div>
    <p className="text-xs text-muted-foreground">점선 SLO는 해당 단계 KPI에 기록된 p95 목표만 표시합니다. 목표 미기록·다른 percentile은 선을 그리지 않으며 현재 서비스 설정을 소급하지 않습니다.</p>
    <details className="text-xs text-muted-foreground"><summary className="min-h-11 cursor-pointer py-2">비교 불가·누락 사유 원문</summary><ul className="space-y-1">{data.filter(point=>!point.paired||Object.values(point.services).some(service=>service.reason)||names.some(name=>!point.services[name])).map((point,index)=><li key={`${point.id}/${index}`} className="break-words">{point.id}: {!point.paired&&point.reason}{names.map(name=>point.services[name]?.reason||!point.services[name]?` / ${name}: ${point.services[name]?.reason??"이 단계에 서비스 기록 없음"}`:"").join("")}</li>)}</ul></details>
  </section>
}

function StageSummary({run,onSelect}:{run:Experiment;onSelect:(a:string,b:string)=>void}) {
  const points=stageChartData(run)
  return <section aria-label="단계 전체 요약" className="space-y-3">
    <h2 className="text-xl font-semibold">전 단계 결과</h2>
    <p className="text-sm text-muted-foreground">모든 단계의 결과예요. 변화량은 같은 부하의 다른 구성과 비교해요. 서비스별 성공률과 지연을 하나의 평균으로 합치지 않아요.</p>
    <div className="overflow-x-auto" tabIndex={0} role="region" aria-label="전 단계 결과와 대응 이유"><table className="w-full min-w-[900px] text-sm">
      <thead><tr>{["단계 / 정책","대응 상태·이유","평균 req/s","전체 POOL J / 평균 W","J / 보낸 요청","전 서비스 성공률 / p95 / Δ","CHILL이 한 일"].map(label=><th scope="col" key={label} className="p-3 text-left">{label}</th>)}</tr></thead>
      <tbody>{points.map((point,index)=><tr key={`${point.id}/${index}`} className="border-t">
        <th scope="row" className="p-3 text-left align-top"><p className="break-words">{stageLoadLabel(point.stage,run)}</p><p className="text-xs font-normal">{point.policy}</p><details className="text-xs font-normal"><summary className="cursor-pointer py-2">구간 코드</summary>{point.id}</details>{point.reason!=="동일 단계 식별자 중복"&&<button type="button" className="min-h-11 underline underline-offset-4" onClick={()=>onSelect(point.id,point.counterpart?.id??"")}>단계 자세히</button>}</th>
        <td className="p-3 align-top"><p>{point.counterpart?point.basis:"비교할 같은 부하 기록 없음"}</p><details className="text-xs"><summary className="cursor-pointer py-2">연결 근거 원문</summary>{point.reason}{point.counterpart&&<p className="break-words">{point.counterpart.id}</p>}</details></td>
        <td className="p-3 text-right align-top tabular-nums">{formatResult(point.rate,"req/s")}</td>
        <td className="p-3 text-right align-top tabular-nums">{formatResult(point.energy,"J")}<br/>{formatResult(point.watts,"W")}{point.counterpart&&<p className={`mt-2 text-xs ${point.energy!=null&&point.counterpart.kpis.wholePoolJoules!=null?(point.energy<Number(point.counterpart.kpis.wholePoolJoules)?"text-green-700 dark:text-green-400":point.energy>Number(point.counterpart.kpis.wholePoolJoules)?"text-destructive":""):""}`}>Δ {difference(point.energy,point.counterpart.kpis.wholePoolJoules,"J")}<br/>{relativeEnergy(point.energy,point.counterpart.kpis.wholePoolJoules)}</p>}</td>
        <td className="p-3 text-right align-top tabular-nums">{formatResult(point.joulesPerRequest,"J/건")}</td>
        <td className="space-y-3 p-3 align-top">{comparisonRows(point.stage,point.counterpart??undefined).map(row=>{
          const service=point.services[row.name]
          return <div key={row.name}><p className="break-words font-medium">{row.name}</p><p className="tabular-nums">성공 {formatResult(service?.success==null?null:service.success/100,"%")} / p95 {formatResult(service?.p95,"ms")}</p><p className={service?.p95!=null&&service.sloMS!=null&&service.p95>service.sloMS?"text-destructive font-medium":"text-xs"}>{service?.sloMS==null?"당시 p95 목표 미기록":`목표 ${formatResult(service.sloMS,"ms")} · ${service.p95==null?"지연 미기록":service.p95>service.sloMS?"목표 초과":"목표 이내"}`}</p>{service?.reason&&<details className="text-xs"><summary className="cursor-pointer py-2">누락 사유 원문</summary>{service.reason}</details>}{point.counterpart&&<p className="text-xs">변화 {serviceDelta(row.a.length===1?row.a[0]:undefined,row.b.length===1?row.b[0]:undefined)}</p>}</div>
        })}</td>
        <td className="p-3 align-top"><Link prefetch={false} href={stageHistoryLink(point.stage)} className="inline-flex min-h-11 items-center underline underline-offset-4">이 구간 판단·전환 기록</Link></td>
      </tr>)}</tbody>
    </table></div>
  </section>
}
function Evidence({stage}:{stage:ExperimentStage}) {
  const query=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams({from:stage.from,to:stage.to,limit:"100"})}`,{decoder:parseSchedulerJSON})
  const activity=query.data?stageActivity(query.data):null
  return <section className="space-y-2 rounded border p-4" aria-label={`${stage.id} CHILL이 한 일`}>
    <h3 className="font-semibold">{stagePolicy(stage)} · CHILL이 한 일</h3>
    <p className="break-words text-sm">{stage.id}: {stage.from} ~ {stage.to}</p>
    {query.isFetching&&<p role="status">이 구간의 판단·전환 기록을 찾고 있어요.</p>}
    {query.error&&<p role="status">{evidenceError(query.error)}. 기록 화면에서 같은 구간을 다시 조회하세요.</p>}
    {activity&&!query.error&&<><p className="text-sm font-medium">{activity.partial?"가져온 첫 페이지":"조회된 구간"}: 판단 {activity.decisions}개 / 전환 사건 {activity.actions}개{activity.partial&&" · 전체 건수 아님"}</p><p className="text-sm">{activity.statuses.length?`판단 상태: ${activity.statuses.join(", ")}`:"판단 기록 없음"}{activity.kinds.length?` / 전환 종류: ${activity.kinds.join(", ")}`:" / 전환 기록 없음"}</p>{query.data?.gapUntil&&<p className="text-sm">보관 공백 있음. 기록 없음은 동작하지 않았다는 뜻이 아니에요.</p>}</>}
    <Link prefetch={false} href={stageHistoryLink(stage)} className="inline-flex min-h-11 items-center text-sm font-medium underline underline-offset-4">이 구간 판단·전환 기록 열기</Link>
    <p className="text-xs text-muted-foreground">출처: 스케줄러 관측 API. 최신 기록으로 대체하지 않으며 결과 변화의 원인으로 단정하지 않아요.</p>
  </section>
}

function Scoreboard({run}:{run:Experiment}) {
  const {metrics,coverage}=campaignScoreboard(run)
  return <section aria-label="자동 운영과 고정 구성 결과 점수판" className="space-y-2">
    <p className="text-xs text-muted-foreground md:hidden">표를 좌우로 이동하면 비교 범위와 결손까지 볼 수 있어요.</p>
    <div className="overflow-x-auto rounded border" tabIndex={0} role="region" aria-label="실험 결과 점수판">
      <table className="w-full min-w-[580px] text-sm"><caption className="sr-only">자동 운영 − 고정 구성, 서비스별 결과는 평균하지 않음</caption>
        <thead className="bg-muted/60"><tr><th scope="col" className="p-3 text-left">지표</th><th scope="col" className="p-3 text-left">자동 운영 − 고정 구성</th><th scope="col" className="p-3 text-left">비교 범위·결손</th></tr></thead>
        <tbody>{metrics.map(metric=><tr key={metric.label} className="border-t"><th scope="row" className="p-3 text-left align-top font-medium">{metric.label}</th><td className="p-3 align-top font-semibold tabular-nums">{metric.value}</td><td className="p-3 align-top text-xs">{metric.detail}</td></tr>)}</tbody>
      </table>
    </div>
    <p className="break-words text-xs text-muted-foreground">{coverage}</p>
    <p className="text-xs text-muted-foreground">관측값 비교이며 에너지 절감만으로 우열을 판단하지 않아요. 구간 길이·측정 규칙 차이는 아래 상세 근거에서 확인하세요. 성공률·p95는 서비스별로 비교하며 평균하지 않아요.</p>
  </section>
}

function Tradeoffs({run,onSelect}:{run:Experiment;onSelect:(a:string,b:string)=>void}) {
  const rows=campaignTradeoffs(run)
  const chip=(text:string,tone:"gain"|"loss"|"neutral")=><span className={`inline-block rounded border px-2 py-1 text-sm ${tone==="gain"?"border-ok/40 text-ok":tone==="loss"?"border-destructive/40 text-destructive":"border-border text-foreground"}`}>{tone==="gain"?"득 · ":tone==="loss"?"실 · ":""}{text}</span>
  return <section id="experiment-tradeoffs" tabIndex={-1} className="space-y-3 scroll-mt-4" aria-label="구간별 득실 결론">
    <h2 className="text-xl font-semibold">자동 운영으로 얻은 것과 잃은 것</h2>
    <Scoreboard run={run}/>
    <p className="text-sm">자동 운영 − 고정 구성. 아래 비교 구간은 자동 운영의 실행 시각 순서예요. 에너지 절감만으로 더 낫다고 판단하지 않아요. 가장 큰 성공률 하락·p95 증가도 해당 구간에 표시해요.</p>
    {!rows.length&&<p>비교할 같은 부하 구간이 없어요. 아래 전 단계 결과에서 개별 값을 확인하세요.</p>}
    <ul className="grid gap-x-8 md:grid-cols-2">{rows.map(row=><li key={row.key} className="space-y-3 border-b py-4">
      <div className="flex flex-wrap items-center justify-between gap-2"><button type="button" className="min-h-11 text-left font-semibold underline underline-offset-4" onClick={()=>onSelect(row.chill.id,row.control.id)}>{stageLoadLabel(row.chill,run)} <span className="text-sm font-normal">({row.key})</span></button><span className="text-sm">{row.basis}</span></div>
      <div className="flex flex-wrap gap-2">{chip(`에너지 Δ ${difference(row.chill.kpis.wholePoolJoules,row.control.kpis.wholePoolJoules,"J")} (${relativeEnergy(row.chill.kpis.wholePoolJoules,row.control.kpis.wholePoolJoules)})`,row.energyDelta===null?"neutral":row.energyDelta<0?"gain":row.energyDelta>0?"loss":"neutral")}</div>
      <ul className="space-y-2">{row.services.map(service=><li key={service.name} className="space-y-1"><p className="break-words text-sm font-medium">{service.name}{service.worstSuccess&&" · 최대 성공률 하락"}{service.worstP95&&" · 최대 p95 증가"}</p><div className="flex flex-wrap gap-2">{chip(`성공률 Δ ${difference(service.a?resultMetric(service.a,"success").value:null,service.b?resultMetric(service.b,"success").value:null,"%",service.successDelta!==null)}`,service.successDelta===null?"neutral":service.successDelta<-.01?"loss":service.successDelta>.01?"gain":"neutral")}{chip(`p95 ${service.p95Ratio===null?"배율 계산 불가":`×${service.p95Ratio.toFixed(2)}`} (${difference(service.a?resultMetric(service.a,"p95").value:null,service.b?resultMetric(service.b,"p95").value:null,"ms")})`,service.p95Delta===null?"neutral":service.p95Delta>.01?"loss":service.p95Delta<-.01?"gain":"neutral")}</div></li>)}</ul>
      <details className="text-sm"><summary className="min-h-11 cursor-pointer py-2">비교 근거·주의점</summary><p>{row.reason}</p><p className="break-words">{row.chill.id} ↔ {row.control.id}</p><p>관측 요청률 {formatResult(stageRate(row.chill),"req/s")} / {formatResult(stageRate(row.control),"req/s")}. 에너지는 각 실제 구간의 합계예요.</p><Rules a={row.chill} b={row.control}/></details>
    </li>)}</ul>
  </section>
}
function Verdict({stage}:{stage:ExperimentStage}) {
  const status=String(stage.kpis.status??"미제공")
  const label=status.includes("NOT_READY")?"준비되지 않음":status.includes("NOT_EVALUATED")?"확인 전":status.includes("DESCRIPTIVE")?"측정 결과":status==="FAIL"?"결과 확인 실패":"결과 확인"
  return <StatusBadge tone={status==="FAIL"?"fail":"info"}>{label}</StatusBadge>
}
export function ExperimentsPage() {
  const params=useSearchParams(),list=useChillQuery<{items:Experiment[]}>("v1/experiments",{decoder:parseSchedulerJSON})
  const a=params.get("a")??"",b=params.get("b")??""
  const aq=useChillQuery<Experiment>(`v1/experiments/${encodeURIComponent(a)}`,{enabled:!!a,decoder:parseSchedulerJSON}),bq=useChillQuery<Experiment>(`v1/experiments/${encodeURIComponent(b)}`,{enabled:!!b,decoder:parseSchedulerJSON})
  const stage=aq.data?.stages.find(stage=>stage.id===params.get("as")),other=bq.data?.stages.find(stage=>stage.id===params.get("bs"))
  const focusSummary=React.useRef("")
  React.useEffect(()=>{if(focusSummary.current&&stage){document.getElementById(focusSummary.current)?.focus();focusSummary.current=""}},[stage,other,params])
  function select(values:Record<string,string>) {
    // Read the synchronous URL, not the previous render's search params: two
    // rapid select changes must compose before React has rendered.
    focusSummary.current=values.as?(a?"experiment-summary":"experiment-tradeoffs"):""
    commitExperimentSelection(window.location,window.history,values)
  }
  return <Screen width="data" breadcrumbs={[{label:"실험"}]} title={a?"실험 결과 비교":"실험 목록"} lede="실험 기록의 전 단계와 전 서비스 결과를 보고, 정확히 대응하는 정책 단계 또는 직접 고른 A/B를 비교합니다.">
    {!a?<QueryGate query={list}>{list.data&&<section aria-label="실험 캠페인 목록" className="space-y-4">{campaignGroups(list.data.items).map(({run,title,question,pairs})=>{
      const pair=pairs[0],first=[...run.stages].sort((a,b)=>Date.parse(a.from)-Date.parse(b.from))[0]
      const href=`/experiments?${experimentSelection("",pair?{a:run.id,as:pair.chill.id,b:run.id,bs:pair.control.id}:{a:run.id,as:first?.id??"",b:"",bs:""})}`
      return <article key={run.id} className="space-y-3 rounded-lg border p-4 sm:p-5">
        <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><h2 className="text-lg font-semibold"><Link prefetch={false} href={href} className="inline-flex min-h-11 items-center break-all underline underline-offset-4">{title}</Link></h2></div><time className="text-sm tabular-nums" dateTime={run.from}>{new Date(run.from).toLocaleDateString("ko-KR",{timeZone:"Asia/Seoul"})}</time></div>
        <p className="text-sm">{question}</p>
        <Scoreboard run={run}/>
        <p className="text-sm text-muted-foreground">전 단계의 서비스별 득실은 상세 결과에서 확인하세요.</p>
        <Button variant="outline" asChild><Link prefetch={false} href={href}>운영 비교 결론 보기</Link></Button>
      </article>
    })}{!list.data.items.length&&<Empty title="게시된 실험이 없어요"/>}</section>}</QueryGate>:<Button variant="outline" onClick={()=>select({a:"",as:"",b:"",bs:""})}>실험 목록으로</Button>}
    {a&&<QueryGate query={aq}>{aq.data&&<Tradeoffs run={aq.data} onSelect={(as,bs)=>select({a,as,b:a,bs})}/>}</QueryGate>}
    <details className="rounded border p-3"><summary className="cursor-pointer py-2 text-sm">다른 기록 직접 비교</summary><QueryGate query={list}>{list.data&&<div className="grid gap-4 md:grid-cols-2">{(["a","b"] as const).map(key=><fieldset key={key} className="grid min-w-0 gap-3 rounded border p-3"><legend className="px-1 font-medium">{key.toUpperCase()} 비교 기록</legend><label className="grid min-w-0 gap-2 text-sm">게시 기록<select aria-label={`${key.toUpperCase()} 게시 기록`} value={key==="a"?a:b} onChange={event=>select({[key]:event.target.value})} className="min-h-11 w-full min-w-0 rounded border bg-background p-2"><option value="">기록 선택</option>{list.data!.items.map(run=><option key={run.id} value={run.id}>{run.id}</option>)}</select></label><label className="grid min-w-0 gap-2 text-sm">단계<select aria-label={`${key.toUpperCase()} 단계`} value={params.get(`${key}s`)??""} onChange={event=>select({[`${key}s`]:event.target.value})} className="min-h-11 w-full min-w-0 rounded border bg-background p-2"><option value="">단계 선택</option>{(key==="a"?aq.data:bq.data)?.stages.map((stage,index)=><option key={`${stage.id}/${index}`} value={stage.id} disabled={(key==="a"?aq.data:bq.data)!.stages.filter(other=>other.id===stage.id).length>1}>{stage.id}{(key==="a"?aq.data:bq.data)!.stages.filter(other=>other.id===stage.id).length>1?" · 중복 식별자 (전 단계 표에서 확인)":""}</option>)}</select></label></fieldset>)}</div>}</QueryGate></details>
    {a&&!stage&&<QueryGate query={aq}>{aq.data&&<p>결과를 볼 A 단계를 선택하세요.</p>}</QueryGate>}{b&&!other&&<QueryGate query={bq}>{bq.data&&<p>비교할 B 단계를 선택하세요.</p>}</QueryGate>}
    {stage&&<section id="experiment-summary" tabIndex={-1} aria-label="결론 요약" className="space-y-3 border-l-4 border-primary pl-4">
      <h2 className="text-xl font-semibold">{other?"선택 구간의 운영 결과 비교":"선택 구간의 결과"}</h2>
      <p className="break-words text-sm font-medium">A {stagePolicy(stage)}: {stage.id}{other&&<> / B {stagePolicy(other)}: {other.id}</>}</p>
      <div className="flex flex-wrap gap-2"><span>A <Verdict stage={stage}/></span>{other&&<span>B <Verdict stage={other}/></span>}</div>
      {other&&<details><summary className="cursor-pointer py-3 text-sm">실행 조건과 측정 규칙 검토</summary><Rules a={stage} b={other}/></details>}
      <div className="grid gap-3 md:grid-cols-2"><Evidence key={`${a}/${stage.id}`} stage={stage}/>{other&&<Evidence key={`${b}/${other.id}`} stage={other}/>}</div>
    </section>}
    {aq.data&&<><StageCharts run={aq.data}/><StageSummary run={aq.data} onSelect={(as,bs)=>select({a,as,b:bs?a:"",bs})}/><details className="rounded border p-3"><summary className="cursor-pointer py-3 text-sm">실제 실행 순서와 단계 구간 (시간순 레인)</summary><Timeline run={aq.data} selected={stage} label="A" onSelect={id=>select({as:id})}/></details></>}
    {bq.data&&b!==a&&<><StageCharts run={bq.data}/><StageSummary run={bq.data} onSelect={(bs)=>select({bs})}/><details className="rounded border p-3"><summary className="cursor-pointer py-3 text-sm">B 실제 실행 순서와 단계 구간 (시간순 레인)</summary><Timeline run={bq.data} selected={other} label="B" onSelect={id=>select({bs:id})}/></details></>}
    {stage&&<><Results stage={stage} other={other} a={a} b={b}/><SourceJSON title="게시 출처" value={{a:aq.data?.sources,b:bq.data?.sources}}/></>}
    {aq.data&&<SourceExport filename="experiment-comparison.json" value={{a:aq.data,aStage:stage?.id??null,b:bq.data??null,bStage:other?.id??null}}/>}
    <p className="text-xs text-muted-foreground">Δ는 반올림 전 원값으로 계산하므로 표시값끼리 뺀 결과와 마지막 자리가 다를 수 있습니다. 표시단위 미만 차이는 방향을 표시하지 않습니다. 원값·복사·내보내기는 원본 정밀도를 유지합니다. 기준 B가 0이어도 절대 차이·%p는 계산하며 상대 %·배율만 계산할 수 없습니다.</p>
    <p className="text-xs text-muted-foreground">게시된 구간별 결과를 비교해요. 서비스 성공률·지연은 평균하지 않고 공유 에너지는 서비스별로 나누지 않아요. 실제 전환 기록이 없는 구간의 변화는 표시하지 않아요.</p>
  </Screen>
}
