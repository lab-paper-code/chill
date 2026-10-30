"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import type { SchedulerObservationPage } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { comparisonRows, decisionIdentity, decisionRange, differentMeasurementRules, difference, evidenceError, formatResult, object, relativeEnergy, resultMetric, stageRate, stageSeconds, type Experiment, type ExperimentRow, type ExperimentStage, type PublishedMetric } from "@/lib/experiments"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Screen, Rows, Row, Empty, Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// Published nested objects are readable facts, never JSON strings in the result table.
function PublishedFacts({value}:{value:unknown}) {
  if(value==null)return <span>미제공</span>
  if(typeof value!=="object")return <span>{String(value)}</span>
  return <dl className="space-y-1">{Object.entries(value).map(([key,item])=><div key={key} className="border-l pl-2"><dt className="text-muted-foreground">{key}</dt><dd><PublishedFacts value={item}/></dd></div>)}</dl>
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
  return row?<div className="space-y-1 break-all text-xs text-muted-foreground"><p>UID {String(row.uid??"미제공")}</p><p>정의 {String(row.definition??"미제공")}</p></div>:<span className="text-muted-foreground">한쪽 기록 없음</span>
}
function ServiceRows({name,left,right,compare}:{name:string;left:ExperimentRow[];right:ExperimentRow[];compare:boolean}) {
  const [aIndex,setA]=React.useState(""),[bIndex,setB]=React.useState("")
  const a=left.length===1?left[0]:left[Number(aIndex)],b=right.length===1?right[0]:right[Number(bIndex)]
  const chosenA=left.length>1&&aIndex===""?undefined:a,chosenB=right.length>1&&bIndex===""?undefined:b
  const candidate=(rows:ExperimentRow[],value:string,set:(v:string)=>void)=>rows.length>1?<label className="grid gap-1 text-xs">동명 후보 선택<select aria-label={`${name} 후보`} className="min-h-11 max-w-full rounded border bg-background p-2" value={value} onChange={e=>set(e.target.value)}><option value="">비교 후보를 선택하세요</option>{rows.map((row,i)=><option key={row.source} value={i}>{row.source} / {String(row.uid??"UID 미제공")}</option>)}</select></label>:null
  return <><tr className="border-t bg-muted/40"><th scope="row" className="p-3 text-left align-top break-words">{name}</th><td className="p-3 align-top">{candidate(left,aIndex,setA)}<Identity row={chosenA}/></td>{compare&&<><td className="p-3 align-top">{candidate(right,bIndex,setB)}<Identity row={chosenB}/></td><td className="p-3 align-top text-xs">{chosenA&&chosenB?(chosenA.uid==null||chosenB.uid==null||chosenA.definition==null||chosenB.definition==null?"identity 미제공":chosenA.uid===chosenB.uid&&chosenA.definition===chosenB.definition?"UID·정의 같음":"UID·정의 다름"):"비교 대상 없음"}</td></>}</tr>{(["success","http","p95"] as const).map(key=>{
    const am=chosenA?resultMetric(chosenA,key):null,bm=chosenB?resultMetric(chosenB,key):null
    const sameMeaning=key!=="success"||am?.key===bm?.key
    return <tr key={key} className="border-t"><th scope="row" className="p-3 text-left align-top font-normal">{key==="success"?"기한·SLO 내 성공 / 보낸 요청":key==="http"?"HTTP 성공 / 보낸 요청":"성공 응답 p95"}</th><td className="p-3 align-top">{am?<><p className="text-xs text-muted-foreground">{am.label}</p><MetricValue metric={am}/></>:"미제공"}</td>{compare&&<><td className="p-3 align-top">{bm?<><p className="text-xs text-muted-foreground">{bm.label}</p><MetricValue metric={bm}/></>:"미제공"}</td><td className="p-3 align-top tabular-nums">{difference(am?.value,bm?.value,key==="p95"?"ms":"%",sameMeaning)}</td></>}</tr>
  })}<tr className="border-t"><td colSpan={compare?4:2} className="p-3"><details className="text-xs"><summary className="cursor-pointer py-2">보낸 요청·결과별 수·SLO·원본 정밀도</summary><div className="grid gap-4 sm:grid-cols-2"><div><h4>A 기록</h4><PublishedFacts value={chosenA?.original}/></div>{compare&&<div><h4>B 기록</h4><PublishedFacts value={chosenB?.original}/></div>}</div></details></td></tr></>
}
function Rules({a,b}:{a:ExperimentStage;b:ExperimentStage}) {
  const keys=differentMeasurementRules(a,b)
  return <aside className="space-y-2 rounded-lg border border-heat/40 bg-heat/5 p-4 text-sm"><p className="font-medium">동명 기록의 설명형 비교 · 통제 실험이나 인과·안전성·통계적 승리 판정이 아니에요.</p>{keys.length>0&&<p>측정 규칙 다름: {keys.join(", ")}</p>}{(!a.kpis.measurementRules||!b.kpis.measurementRules)&&<p>측정 규칙 식별자 미제공</p>}<p>구간 길이: A {formatResult(stageSeconds(a),"s")} / B {formatResult(stageSeconds(b),"s")} · {stageSeconds(a)===stageSeconds(b)?"길이 같음":"길이 다름"}. 실행 조건·SLO는 미제공 항목이 있어 동일 조건을 보장하지 않아요.</p><details><summary className="cursor-pointer py-2">게시된 측정 규칙</summary><div className="grid gap-4 sm:grid-cols-2"><PublishedFacts value={a.kpis.measurementRules}/><PublishedFacts value={b.kpis.measurementRules}/></div></details></aside>
}
function Energy({stage,other}:{stage:ExperimentStage;other?:ExperimentStage}) {
  return <section className="space-y-3" aria-label="공유 POOL 에너지"><h3 className="font-semibold">공유 POOL 에너지 · 서비스 행 합산·배분 금지</h3><div className="overflow-x-auto" tabIndex={0} role="region" aria-label="공유 에너지 비교표"><table className="w-full min-w-[580px] text-sm"><thead><tr><th className="p-3 text-left">범위</th><th className="p-3 text-left">A</th>{other&&<><th className="p-3 text-left">B</th><th className="p-3 text-left">A−B</th><th className="p-3 text-left">B 대비 변화</th></>}</tr></thead><tbody>{(["wholePoolJoules","activePoolJoules"] as const).map(key=><tr key={key} className="border-t tabular-nums"><th className="p-3 text-left font-normal">{key==="wholePoolJoules"?"전체 POOL":"활성 POOL"}</th><td className="p-3">{formatResult(stage.kpis[key],"J")}</td>{other&&<><td className="p-3">{formatResult(other.kpis[key],"J")}</td><td className="p-3">{difference(stage.kpis[key],other.kpis[key],"J")}</td><td className="p-3">{relativeEnergy(stage.kpis[key],other.kpis[key])}</td></>}</tr>)}</tbody></table></div><p className="text-sm text-muted-foreground">기간·부하가 다르면 총 J가 낮다는 사실만으로 효율 개선이라고 할 수 없어요. ±는 95% CI가 아니라 관측 공백 오차 상한이에요.</p><div className="grid gap-4 sm:grid-cols-2">{[stage,...other?[other]:[]].map((s,i)=><div key={i} className="space-y-2 text-sm"><p>{i===0?"A":"B"} 공백 오차 상한 ± {formatResult(s.kpis.gapErrorBoundJoules,"J")}</p><details><summary className="cursor-pointer py-2">에너지 원값·관측 범위·결손</summary><PublishedFacts value={{wholePoolJoules:s.kpis.wholePoolJoules,activePoolJoules:s.kpis.activePoolJoules,energyPopulation:s.kpis.energyPopulation,energyCoverage:s.kpis.energyCoverage,missingReasons:object(s.kpis.missingReasons).energy,source:"kpis.wholePoolJoules / activePoolJoules / gapErrorBoundJoules"}}/></details></div>)}</div></section>
}
function Results({stage,other,a,b}:{stage:ExperimentStage;other?:ExperimentStage;a:string;b:string}) {
  const rows=comparisonRows(stage,other)
  return <section id="experiment-results" tabIndex={-1} className="space-y-4 scroll-mt-4 focus-visible:outline-2 focus-visible:outline-primary" aria-label="단계 결과 표">
    <h2 className="text-xl font-semibold">{other?"서비스별 A/B 관측값":"서비스별 측정 결과"}</h2>
    <p className="text-sm text-muted-foreground">첫 지표는 기한·SLO 내 성공 / 보낸 요청이에요. HTTP 성공과 구별하며, p95는 성공 응답만의 지연이에요. 서비스별 %·p95를 평균내 전체 결과로 만들지 않아요.</p>
    {other&&<Rules a={stage} b={other}/>}
    <p className="text-xs text-muted-foreground md:hidden">표를 좌우로 이동하면 B와 A−B 마지막 열까지 볼 수 있어요.</p>
    <div className="overflow-x-auto rounded-lg border" tabIndex={0} role="region" aria-label="서비스별 A/B 비교표">
      <table className="w-full min-w-[700px] text-sm"><caption className="sr-only">기록 당시 서비스명 기준의 설명형 비교</caption>
        <thead className="bg-muted/60"><tr><th scope="col" className="w-1/4 p-3 text-left">서비스 / 지표</th><th scope="col" className="p-3 text-left break-all">A · {a}<p className="font-normal">{stage.id}</p></th>{other&&<><th scope="col" className="p-3 text-left break-all">B · {b}<p className="font-normal">{other.id}</p></th><th scope="col" className="p-3 text-left">관측 차이 A−B</th></>}</tr></thead>
        <tbody>{rows.map(row=><ServiceRows key={`${a}/${stage.id}/${b}/${other?.id}/${row.name}`} name={row.name} left={row.a} right={row.b} compare={!!other}/>)}</tbody>
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
    <h3 className="font-semibold">{label} 단계 · 실제 구간 [from,to)</h3>
    <p className="break-all text-xs text-muted-foreground">{run.from} ~ {run.to}</p>
    <div className="overflow-x-auto rounded-lg border p-3" tabIndex={0} role="region" aria-label={`${label} 단계 타임라인`}>
      <div className="relative h-24 min-w-[560px]">{stages.map(stage=>{
        const valid=span>0&&stageSeconds(stage)!==null
        return <button key={stage.id} type="button" onClick={()=>onSelect(stage.id)} aria-pressed={selected?.id===stage.id}
          title={`${stage.id}: ${stage.from} ~ ${stage.to} · ${formatResult(stageRate(stage),"req/s")}`}
          style={valid?{left:`${100*(Date.parse(stage.from)-start)/span}%`,width:`${100*(Date.parse(stage.to)-Date.parse(stage.from))/span}%`}:undefined}
          className={`absolute top-0 h-24 overflow-hidden rounded border px-2 text-left focus-visible:outline-2 focus-visible:outline-primary ${selected?.id===stage.id?"border-primary bg-primary/10":"bg-muted"}`}>
          <span className="block truncate font-semibold">{stage.id}</span>
          <span className="block truncate text-xs tabular-nums">{formatResult(stageSeconds(stage),"s")}</span>
          <span className="block truncate text-xs tabular-nums">{formatResult(stageRate(stage),"req/s")}</span>
        </button>
      })}</div>
    </div>
    {selected&&<p className="break-words text-sm tabular-nums">{label} 선택 {selected.id} · {formatResult(stageSeconds(selected),"s")} · 단계 평균 보낸 요청률 {formatResult(stageRate(selected),"req/s")}</p>}
    <p className="text-xs text-muted-foreground">α 미제공 · 요청률은 offered / 실제 기간인 ‘단계 평균 보낸 요청률’이며 목표 부하가 아니에요. 단계 집계만 표시해요.</p>
  </section>
}
function Evidence({stage}:{stage:ExperimentStage}) {
  const params=useSearchParams(),router=useRouter(),[draft,setDraft]=React.useState(params.get("at")??"")
  const at=params.get("at")??"",identity=params.get("decision"),cursor=params.get("evidenceCursor")??""
  const range=at?decisionRange(at):params.get("evidence")==="stage"?{from:stage.from,to:stage.to}:null
  const query=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams({...range,limit:"20",...(cursor?{cursor}:{})})}`,{enabled:!!range,decoder:parseSchedulerJSON})
  const events=(query.data?.events??[]).map((event,index)=>({event,offset:String(Number(cursor||"0")+index)})).filter(({event})=>event.decision&&(!at||decisionRange(event.decision.at)?.from===decisionRange(at)?.from))
  const selected=events.filter(({event,offset})=>decisionIdentity(event)===identity&&offset===params.get("record"))
  function choose(values:Record<string,string>) {const next=new URLSearchParams(params.toString());for(const key of ["at","decision","record","evidenceCursor","evidence"])next.delete(key);for(const [key,value] of Object.entries(values))next.set(key,value);router.replace(`/experiments?${next}`,{scroll:false})}
  return <section className="space-y-4" aria-label="판단 근거"><h2 className="text-xl font-semibold">A 기록 당시 판단 근거</h2><p className="break-all text-sm text-muted-foreground">선택 단계 {stage.from} ~ {stage.to}. 최신 판단으로 대체하지 않아요.</p><Button variant="outline" onClick={()=>choose({evidence:"stage"})}>단계 판단 기록 찾기</Button><form className="flex flex-wrap items-end gap-2" onSubmit={event=>{event.preventDefault();if(decisionRange(draft))choose({at:draft})}}><label className="grid min-w-0 flex-1 gap-2 text-sm">decision.at (RFC3339Nano)<Input value={draft} onChange={event=>setDraft(event.target.value)} required/></label><Button type="submit" disabled={!decisionRange(draft)}>근거 조회</Button></form>{range&&<p className="break-all text-xs text-muted-foreground">출처 /observations/scheduler · {range.from} ≤ at &lt; {range.to} · cursor {cursor||"0"}</p>}{query.isFetching&&range&&<p role="status">보관 근거 조회 중</p>}{query.error&&<div role="alert"><p>{evidenceError(query.error)}</p><SourceJSON value={query.error.data}/></div>}{query.data&&!query.error&&<>{!events.length&&!query.data.nextCursor&&<p role="status">기록 없음</p>}{events.length>0&&<Rows label="단계 판단 목록">{events.map(({event,offset})=><Row key={offset} label={`${event.decision!.status} · 유지 ${event.decision!.keep?.id??""} · 목표 ${event.decision!.target?.id??""}`} hint={`${event.decision!.at} · 원본 행 ${offset}`} onClick={()=>choose(at?{at,decision:decisionIdentity(event),record:offset,...(cursor?{evidenceCursor:cursor}:{})}:{at:event.decision!.at})}/>)}</Rows>}{selected.length===1&&<><Facts items={[["고정 decision.at",selected[0].event.decision!.at],["판단 identity",identity??""],["원본 행",selected[0].offset],["게시된 판단",selected[0].event.decision!.status]]}/><SourceJSON title="선택한 보관 근거" value={selected[0].event}/></>}{identity&&!selected.length&&<p role="status">이 페이지에 고른 판단 기록 없음</p>}<SourceJSON title="조회 coverage·출처·결손" value={object(query.data).coverage}/>{query.data.nextCursor&&<Button variant="outline" onClick={()=>choose({...at?{at}:{evidence:"stage"},evidenceCursor:query.data!.nextCursor!})}>다음 근거 기록</Button>}{cursor&&<Button variant="outline" onClick={()=>choose(at?{at}:{evidence:"stage"})}>첫 근거 기록</Button>}</>}</section>
}
function Verdict({stage}:{stage:ExperimentStage}) {
  const status=String(stage.kpis.status??"미제공")
  const label=status.includes("NOT_READY")?"준비되지 않음":status.includes("NOT_EVALUATED")?"미평가":status.includes("DESCRIPTIVE")?"설명형 결과":status==="FAIL"?"게시 판정 실패":"게시 판정"
  return <StatusBadge tone={status==="FAIL"?"fail":"info"}>{label} · {status}</StatusBadge>
}
export function ExperimentsPage() {
  const params=useSearchParams(),router=useRouter(),list=useChillQuery<{items:Experiment[]}>("v1/experiments")
  const a=params.get("a")??"",b=params.get("b")??""
  const aq=useChillQuery<Experiment>(`v1/experiments/${encodeURIComponent(a)}`,{enabled:!!a}),bq=useChillQuery<Experiment>(`v1/experiments/${encodeURIComponent(b)}`,{enabled:!!b})
  const stage=aq.data?.stages.find(stage=>stage.id===params.get("as")),other=bq.data?.stages.find(stage=>stage.id===params.get("bs"))
  const [focusResult,setFocusResult]=React.useState(false)
  React.useEffect(()=>{if(focusResult&&stage){document.getElementById("experiment-results")?.focus();setFocusResult(false)}},[focusResult,stage,other])
  function setSelection(key:string,value:string,focus=false) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);if(key==="a"||key==="b")next.delete(`${key}s`);for(const field of ["at","decision","record","evidence","evidenceCursor"])next.delete(field);router.replace(`/experiments?${next}`,{scroll:false});if(focus)setFocusResult(true)}
  return <Screen width="data" breadcrumbs={[{href:"/records",label:"기록"},{label:"실험"}]} title="실험 결과 비교" lede="게시된 기록의 성공률·지연·공유 에너지를 비교해요. 차이는 A−B로 표시하며 성공이나 안전성을 다시 판정하지 않아요.">
    <QueryGate query={list}>{list.data&&<><div className="grid gap-4 md:grid-cols-2">{(["a","b"] as const).map(key=><fieldset key={key} className="grid min-w-0 gap-3 rounded-lg border p-4"><legend className="px-1 font-semibold">{key.toUpperCase()} 비교 기록</legend><label className="grid min-w-0 gap-2 text-sm">게시 기록<select value={key==="a"?a:b} onChange={event=>setSelection(key,event.target.value)} className="min-h-11 w-full min-w-0 rounded-md border bg-background p-2"><option value="">기록 선택</option>{list.data!.items.map(run=><option key={run.id} value={run.id}>{run.id}</option>)}</select></label><label className="grid min-w-0 gap-2 text-sm">단계<select value={params.get(`${key}s`)??""} onChange={event=>setSelection(`${key}s`,event.target.value)} className="min-h-11 w-full min-w-0 rounded-md border bg-background p-2"><option value="">단계 선택</option>{(key==="a"?aq.data:bq.data)?.stages.map(stage=><option key={stage.id} value={stage.id}>{stage.id} · {formatResult(stageSeconds(stage),"s")}</option>)}</select></label></fieldset>)}</div>{!list.data.items.length&&<Empty title="게시된 실험이 없어요"/>}</>}</QueryGate>
    {a&&<QueryGate query={aq}>{aq.data&&!stage&&<p>결과를 볼 A 단계를 골라 주세요.</p>}</QueryGate>}{b&&<QueryGate query={bq}>{bq.data&&!other&&<p>비교할 B 단계를 골라 주세요.</p>}</QueryGate>}
    {stage&&<section aria-label="결론 요약" className="space-y-3 border-l-4 border-primary pl-4">
      <h2 className="text-xl font-semibold">{other?"두 기록의 관측값 차이를 비교하고 있어요":"선택 단계의 게시 결과예요"}</h2>
      <div className="flex flex-wrap gap-2"><Verdict stage={stage}/>{other&&<Verdict stage={other}/>}</div>
      <p className="text-sm tabular-nums">전체 POOL A {formatResult(stage.kpis.wholePoolJoules,"J")}{other&&<> / B {formatResult(other.kpis.wholePoolJoules,"J")} · A−B {difference(stage.kpis.wholePoolJoules,other.kpis.wholePoolJoules,"J")}</>}</p>
      <ul className="divide-y text-sm">{comparisonRows(stage,other).map(row=>{
        const left=row.a.length===1?row.a[0]:undefined,right=row.b.length===1?row.b[0]:undefined
        return <li key={row.name} className="grid gap-1 py-2 sm:grid-cols-[minmax(0,1fr)_2fr]"><span className="break-words font-medium">{row.name}</span><span className="tabular-nums">기한·SLO 내 성공 A {left?formatResult(resultMetric(left,"success").value,"%"):"미제공"}{other&&<> / B {right?formatResult(resultMetric(right,"success").value,"%"):"미제공"}</>}<br/>성공 응답 p95 A {left?formatResult(resultMetric(left,"p95").value,"ms"):"미제공"}{other&&<> / B {right?formatResult(resultMetric(right,"p95").value,"ms"):"미제공"}</>}</span></li>
      })}</ul>
      <p className="text-sm text-muted-foreground">동명 기록의 설명형 비교예요. identity·조건·측정 규칙이 다르거나 미제공일 수 있어요. 전체 성공률·p95는 서비스 평균으로 만들지 않아요.</p>
    </section>}
    <div className="grid gap-6">{aq.data&&<Timeline run={aq.data} selected={stage} label="A" onSelect={id=>setSelection("as",id,true)}/ >}{bq.data&&<Timeline run={bq.data} selected={other} label="B" onSelect={id=>setSelection("bs",id,true)}/>}</div>
    {stage&&<><Results stage={stage} other={other} a={a} b={b}/><Evidence key={`${a}/${stage.id}`} stage={stage}/><SourceJSON title="게시 출처" value={{a:aq.data?.sources,b:bq.data?.sources}}/><SourceExport filename="experiment-comparison.json" value={{a:aq.data,aStage:stage.id,b:bq.data??null,bStage:other?.id??null}}/></>}
  </Screen>
}
