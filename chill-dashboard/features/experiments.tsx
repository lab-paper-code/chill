"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery } from "@/lib/client"
import type { SchedulerObservationPage } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { decisionIdentity, decisionRange, differentMeasurementRules, energyText, evidenceError, experimentRows, object, publishedText, type Experiment, type ExperimentStage, type PublishedMetric } from "@/lib/experiments"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Screen, Rows, Row, Empty, Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

function MetricValue({metric}:{metric:PublishedMetric}) {
  return <div className="space-y-1 break-words"><p className="font-medium tabular-nums">{publishedText(metric.value)}{metric.value!=null&&metric.unit?` ${metric.unit}`:""}</p><details className="text-xs text-muted-foreground"><summary className="cursor-pointer py-2">분자·분모·출처·결손</summary><dl className="space-y-1 break-all"><dt>분자</dt><dd>{publishedText(metric.numerator)}</dd><dt>분모 / 표본</dt><dd>{publishedText(metric.denominator)}</dd><dt>모집단</dt><dd>{publishedText(metric.population)}</dd><dt>출처</dt><dd>{metric.source}</dd><dt>결손 사유</dt><dd>{publishedText(metric.reason)}</dd></dl></details></div>
}

function Results({stage,other}:{stage:ExperimentStage;other?:ExperimentStage}) {
  const rows=experimentRows(stage),comparisons=other?experimentRows(other):[]
  return <section className="space-y-4" aria-label="단계 결과 표">
    <h2 className="text-xl font-semibold">{other?"두 실행·두 단계 비교":"단계 결과 표"}</h2>
    <p className="text-sm text-muted-foreground">생성기 offered · 서버 수신 cohort · 라이브 종결 버킷은 서로 다른 모집단이에요. 라이브 종결은 이 게시물에 없으며 수신−종결로 만들지 않아요. 빈칸은 미제공 값이에요.</p>
    {other&&<Rules a={stage} b={other}/>}
    <div className="overflow-x-auto" tabIndex={0} role="region" aria-label="단계 KPI 표"><table className="w-full text-sm"><caption className="sr-only">게시된 단계 KPI와 원래 분모 및 출처</caption><thead><tr><th scope="col" className="p-3 text-left">기록 당시 이름 / UID / 정의</th><th scope="col" className="p-3 text-left">항목</th><th scope="col" className="p-3 text-left">A · {stage.id}</th>{other&&<th scope="col" className="p-3 text-left">B · {other.id}</th>}</tr></thead><tbody>
      {rows.flatMap(row=>row.metrics.map((metric,index)=>{
        // Do not join same-name registrations or claim distinct historical UIDs are one population.
        const peer=comparisons.find(item=>row.uid!=null&&row.definition!=null&&item.uid===row.uid&&item.definition===row.definition)
        return <tr key={`${row.source}/${metric.key}`} className="border-t"><th scope="row" className="max-w-56 break-all p-3 text-left align-top font-normal">{index===0&&<>{row.name}<p className="text-xs text-muted-foreground">UID {publishedText(row.uid)}<br/>정의 {publishedText(row.definition)}</p></>}</th><td className="p-3 align-top">{metric.label}</td><td className="min-w-48 p-3 align-top"><MetricValue metric={metric}/></td>{other&&<td className="min-w-48 p-3 align-top">{peer?.metrics.find(item=>item.key===metric.key)&&<MetricValue metric={peer.metrics.find(item=>item.key===metric.key)!}/>}</td>}</tr>
      }))}
      {other&&comparisons.filter(row=>!rows.some(item=>row.uid!=null&&row.definition!=null&&item.uid===row.uid&&item.definition===row.definition)).flatMap(row=>row.metrics.map((metric,index)=><tr key={`b/${row.source}/${metric.key}`} className="border-t"><th scope="row" className="max-w-56 break-all p-3 text-left align-top font-normal">{index===0&&<>{row.name}<p className="text-xs text-muted-foreground">UID {publishedText(row.uid)}<br/>정의 {publishedText(row.definition)}</p></>}</th><td className="p-3 align-top">{metric.label}</td><td/><td className="min-w-48 p-3 align-top"><MetricValue metric={metric}/></td></tr>))}
    </tbody></table></div>
    {!rows.length&&<Empty title="지원하는 게시 KPI가 없어요"><p>kpis.workloads 또는 kpis.requests만 표시해요. 원본을 확인해 주세요.</p></Empty>}
    {other&&<p className="text-sm text-muted-foreground">UID·정의가 같은 행만 나란히 놓아요. 신원이 없는 옛 게시물과 다른 정의는 별도 행으로 보존해요.</p>}
    <Energy stage={stage} label="A"/>{other&&<Energy stage={other} label="B"/>}
    <SourceJSON title="A 게시 KPI 원본 (모든 결과·결손·제한)" value={stage.kpis}/>{other&&<SourceJSON title="B 게시 KPI 원본 (모든 결과·결손·제한)" value={other.kpis}/>}
  </section>
}
function Rules({a,b}:{a:ExperimentStage;b:ExperimentStage}) {
  const keys=differentMeasurementRules(a,b)
  return <div className="space-y-2">{keys.length>0&&<><StatusBadge tone="warn">측정 규칙 다름</StatusBadge><p className="text-sm break-all">{keys.join(", ")}</p></>}{(!a.kpis.measurementRules||!b.kpis.measurementRules)&&<p className="text-sm">측정 규칙 식별자 미제공</p>}<SourceJSON title="측정 규칙 식별자 A / B" value={{a:a.kpis.measurementRules??null,b:b.kpis.measurementRules??null}}/></div>
}
function Energy({stage,label}:{stage:ExperimentStage;label:string}) {
  const missing=object(stage.kpis.missingReasons)
  return <section className="space-y-2"><h3 className="font-semibold">{label} · 공유 POOL 에너지 · 행 합산 금지</h3><Facts items={[["전체 POOL",energyText(stage)],["활성 POOL",energyText(stage,"activePoolJoules")],["모집단",publishedText(stage.kpis.energyPopulation)],["분자 / 분모",""],["출처","kpis.wholePoolJoules / kpis.activePoolJoules / kpis.gapErrorBoundJoules"],["결손 사유",publishedText(missing.energy)]]}/><SourceJSON title="에너지 coverage·오차 상한" value={{energyCoverage:stage.kpis.energyCoverage??null,gapErrorBoundJoules:stage.kpis.gapErrorBoundJoules??null}}/></section>
}

function Evidence({stage}:{stage:ExperimentStage}) {
  const params=useSearchParams(),router=useRouter(),[draft,setDraft]=React.useState(params.get("at")??"")
  const at=params.get("at")??"",identity=params.get("decision"),cursor=params.get("evidenceCursor")??""
  const range=at?decisionRange(at):params.get("evidence")==="stage"?{from:stage.from,to:stage.to}:null
  const query=useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams({...range,limit:"20",...(cursor?{cursor}:{})})}`,{enabled:!!range,decoder:parseSchedulerJSON})
  const events=(query.data?.events??[]).map((event,index)=>({event,offset:String(Number(cursor||"0")+index)})).filter(({event})=>event.decision&&(!at||decisionRange(event.decision.at)?.from===decisionRange(at)?.from))
  const selected=events.filter(({event,offset})=>decisionIdentity(event)===identity&&offset===params.get("record"))
  function choose(values:Record<string,string>) {const next=new URLSearchParams(params.toString());for(const key of ["at","decision","record","evidenceCursor","evidence"])next.delete(key);for(const [key,value] of Object.entries(values))next.set(key,value);router.replace(`/experiments?${next}`,{scroll:false})}
  return <section className="space-y-4" aria-label="판단 근거">
    <h2 className="text-xl font-semibold">기록 당시 판단 근거</h2>
    <p className="text-sm text-muted-foreground">단계의 보관 판단에서 고르거나 기록의 decision.at을 넣으세요. 최신 판단으로 대체하지 않아요. 단계 구간: {stage.from} ~ {stage.to}</p>
    <Button variant="outline" onClick={()=>choose({evidence:"stage"})}>단계 판단 기록 찾기</Button>
    <form className="flex flex-wrap items-end gap-2" onSubmit={event=>{event.preventDefault();if(decisionRange(draft))choose({at:draft})}}>
      <label className="grid flex-1 gap-2 text-sm">decision.at (RFC3339Nano)<Input value={draft} onChange={event=>setDraft(event.target.value)} required/></label><Button type="submit" disabled={!decisionRange(draft)}>근거 조회</Button>
    </form>
    {range&&<p className="break-all text-xs text-muted-foreground">출처 /observations/scheduler · {range.from} ≤ at &lt; {range.to} · cursor {cursor||"0"}</p>}
    {query.isFetching&&range&&<p role="status">보관 근거 조회 중</p>}
    {query.error&&<div role="alert"><p>{evidenceError(query.error)}</p><SourceJSON value={query.error.data}/></div>}
    {query.data&&!query.error&&<>
      {!events.length&&!query.data.nextCursor&&<p role="status">기록 없음</p>}
      {events.length>0&&<Rows label={at?"동시각 판단 identity 선택":"단계 판단 목록"}>{events.map(({event,offset})=><Row key={offset}
        label={`${event.decision!.status} · 유지 ${event.decision!.keep?.id??""} · 목표 ${event.decision!.target?.id??""}`}
        hint={`${event.decision!.at} · 원본 행 ${offset}`}
        onClick={()=>choose(at?{at,decision:decisionIdentity(event),record:offset,...(cursor?{evidenceCursor:cursor}:{})}:{at:event.decision!.at})}/>)}</Rows>}
      {selected.length===1&&<>
        <Facts items={[["고정 decision.at",selected[0].event.decision!.at],["판단 identity",identity??""],["원본 행",selected[0].offset],["게시된 판단",selected[0].event.decision!.status]]}/>
        <SourceJSON inline title="선택한 보관 근거" value={selected[0].event}/>
      </>}
      {identity&&!selected.length&&<p role="status">이 페이지에 고른 판단 기록 없음</p>}
      <SourceJSON title="조회 coverage·출처·결손" value={object(query.data).coverage}/>
      {query.data.nextCursor&&<Button variant="outline" onClick={()=>choose({...at?{at}:{evidence:"stage"},evidenceCursor:query.data!.nextCursor!})}>다음 근거 기록</Button>}
      {cursor&&<Button variant="outline" onClick={()=>choose(at?{at}:{evidence:"stage"})}>첫 근거 기록</Button>}
    </>}
  </section>
}

export function ExperimentsPage() {
  const params=useSearchParams(),router=useRouter(),list=useChillQuery<{items:Experiment[]}>("v1/experiments")
  const a=params.get("a")??"",b=params.get("b")??""
  const aq=useChillQuery<Experiment>(`v1/experiments/${encodeURIComponent(a)}`,{enabled:!!a}),bq=useChillQuery<Experiment>(`v1/experiments/${encodeURIComponent(b)}`,{enabled:!!b})
  const stage=aq.data?.stages.find(stage=>stage.id===params.get("as")),other=bq.data?.stages.find(stage=>stage.id===params.get("bs"))
  function setSelection(key:string,value:string) {const next=new URLSearchParams(params.toString());next.set(key,value);if(key==="a"||key==="b")next.delete(`${key}s`);for(const field of ["at","decision","record","evidence","evidenceCursor"])next.delete(field);router.replace(`/experiments?${next}`)}
  return <Screen back={["/records","기록"]} title="게시된 실험 결과" lede="불변 실행·단계의 값과 원래 모집단을 비교해요. 화면은 성공이나 안전성을 다시 판정하지 않아요.">
    <QueryGate query={list}>{list.data&&<><Rows label="실행·단계 목록">{list.data.items.map(run=><li key={run.id} className="min-w-0 p-4">
      <h2 className="break-all font-semibold">{run.id} · {run.status}</h2>
      <p className="break-all text-xs text-muted-foreground">{run.from} ~ {run.to}</p>
      <ul>{run.stages.map(stage=><Row key={stage.id} label={stage.id}
        hint={<span className="break-all">{stage.from}<br/>{stage.to}<br/>{publishedText(stage.kpis.status)}</span>}
        onClick={()=>{router.replace(`/experiments?${new URLSearchParams({a:run.id,as:stage.id})}`)}}/>)}</ul>
    </li>)}</Rows>{!list.data.items.length&&<Empty title="게시된 실험이 없어요"/>}
      <div className="grid gap-4 sm:grid-cols-2">{(["a","b"] as const).map((key)=><fieldset key={key} className="grid min-w-0 gap-3"><legend className="mb-2 font-semibold">{key.toUpperCase()} 실행·단계</legend><label className="grid gap-2 text-sm">실행<select value={key==="a"?a:b} onChange={event=>setSelection(key,event.target.value)} className="min-h-11 min-w-0 rounded-md border bg-background p-2"><option value="">선택 안 함</option>{list.data!.items.map(run=><option key={run.id} value={run.id}>{run.id}</option>)}</select></label><label className="grid gap-2 text-sm">단계<select value={params.get(`${key}s`)??""} onChange={event=>setSelection(`${key}s`,event.target.value)} className="min-h-11 min-w-0 rounded-md border bg-background p-2"><option value="">단계 선택</option>{(key==="a"?aq:bq).data?.stages.map(stage=><option key={stage.id} value={stage.id}>{stage.id}</option>)}</select></label></fieldset>)}</div>
    </>}</QueryGate>
    {a&&<QueryGate query={aq}>{aq.data&&!stage&&<p>결과를 볼 A 단계를 골라 주세요.</p>}</QueryGate>}{b&&<QueryGate query={bq}>{bq.data&&!other&&<p>비교할 B 단계를 골라 주세요.</p>}</QueryGate>}
    {stage&&<><Facts items={[["A 실행 / 단계",`${a} / ${stage.id}`],["구간 [from,to)",`${stage.from} ~ ${stage.to}`],["게시 상태",publishedText(stage.kpis.status)]]}/>{other&&<Facts items={[["B 실행 / 단계",`${b} / ${other.id}`],["구간 [from,to)",`${other.from} ~ ${other.to}`],["게시 상태",publishedText(other.kpis.status)]]}/>}<Results stage={stage} other={other}/><Evidence key={`${a}/${stage.id}`} stage={stage}/><SourceJSON title="A 게시 출처" value={aq.data?.sources}/>{b&&<SourceJSON title="B 게시 출처" value={bq.data?.sources}/>}<SourceExport filename="experiment-comparison.json" value={{a:aq.data,aStage:stage.id,b:bq.data??null,bStage:other?.id??null}}/></>}
  </Screen>
}
