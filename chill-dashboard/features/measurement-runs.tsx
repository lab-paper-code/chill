"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useAllMeasurementRuns, MeasurementFilters, MeasurementNavigation } from "@/features/measurement-coverage"
import { useChillQuery, useSession } from "@/lib/client"
import { currentRunCondition, exactRunTrial, runEvidenceState, runGroupKey, runSpec } from "@/lib/measurement-coverage"
import { safeMeasurementReturn } from "@/lib/measurements"
import type { CampaignResult, ProfilingRun } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Button } from "@/components/ui/button"

function matchingRuns(runs:ProfilingRun[],params:URLSearchParams) {
  return runs.filter(run=>{
    const spec=runSpec(run),candidate=spec.candidate
    return [["model",candidate?.modelSpec],["deviceClass",candidate?.deviceClass],["node",spec.targetNode],["path",candidate?.executionPath],["powerMode",candidate?.powerMode],["cpu",spec.measurement?.cpuQuota]].every(([key,value])=>!params.get(key!)||params.get(key!)===value)&&(!params.get("q")||run.name.includes(params.get("q")!))&&(!params.get("status")||runEvidenceState(run)===params.get("status"))&&(!params.get("condition")||(()=>{const [type,status]=params.get("condition")!.split("=");return currentRunCondition(run,type)?.status===status})())
  })
}
export function MeasurementRunsPage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace,query=useAllMeasurementRuns(namespace)
  const runs=query.data?.runs??[],filtered=matchingRuns(runs,new URLSearchParams(params)),groups=new Map<string,ProfilingRun[]>()
  for(const run of filtered) {const key=runGroupKey(run),items=groups.get(key);if(items)items.push(run);else groups.set(key,[run])}
  const rows=[...groups],pages=Math.max(1,Math.ceil(rows.length/25)),page=Math.min(pages,Math.max(1,Number(params.get("page"))||1)),returnTo=`/measurements/runs?${params}`
  function go(nextPage:number) {const next=new URLSearchParams(params.toString());next.set("page",String(nextPage));router.replace(`?${next}`,{scroll:false})}
  return <Screen width="data" actionPlacement="header" title="개별 측정 회차" lede="모델·장치·전력모드와 측정 조건별로 회차를 모았어요." breadcrumbs={[{href:"/measurements",label:"측정"},{label:"개별 회차"}]}>
    <MeasurementNavigation namespace={namespace}/><MeasurementFilters axes={[{key:"model",label:"모델",values:runs.flatMap(r=>runSpec(r).candidate?.modelSpec?[runSpec(r).candidate!.modelSpec]:[])},{key:"deviceClass",label:"장치 종류",values:runs.flatMap(r=>runSpec(r).candidate?.deviceClass?[runSpec(r).candidate!.deviceClass]:[])},{key:"node",label:"Node",values:runs.flatMap(r=>runSpec(r).targetNode?[runSpec(r).targetNode!]:[])},{key:"path",label:"실행 경로",values:runs.flatMap(r=>runSpec(r).candidate?.executionPath?[runSpec(r).candidate!.executionPath]:[])},{key:"powerMode",label:"전력모드",values:runs.flatMap(r=>runSpec(r).candidate?.powerMode?[runSpec(r).candidate!.powerMode]:[])},{key:"cpu",label:"CPU quota",values:runs.flatMap(r=>runSpec(r).measurement?.cpuQuota?[runSpec(r).measurement!.cpuQuota!]:[])}]}/>
    <p className="text-sm tabular-nums">전체 {runs.length}회차 / 표시 {filtered.length}회차 / 조건 {groups.size}개</p>
    <QueryGate query={query}><div className="min-w-0 overflow-x-auto rounded-xl border"><Table><TableHeader><TableRow>{["모델 / 장치 종류","Node / 경로","전력모드","전처리 / 데이터셋","CPU / protocol","관련 Run"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{rows.slice((page-1)*25,page*25).map(([key,items])=>{const spec=runSpec(items[0]),c=spec.candidate,next=new URLSearchParams(params.toString());next.set("group",key);next.set("returnTo",returnTo);return <TableRow key={key}><TableCell><Link className="text-primary underline" href={`/measurements/group?${next}`}>{c?.modelSpec??"미제공"}</Link><span className="block text-xs text-muted-foreground">{c?.deviceClass??"미제공"}</span></TableCell><TableCell>{spec.targetNode??"미제공"}<span className="block text-xs">{c?.executionPath??"미제공"}</span></TableCell><TableCell>{c?.powerMode??"미제공"}</TableCell><TableCell>{c?.preprocessor??"미제공"}<span className="block text-xs">{spec.measurement?.dataset??"미제공"}</span></TableCell><TableCell className="tabular-nums">{spec.measurement?.cpuQuota??"미제공"} / affinity {spec.measurement?.cpuAffinity?.join(",")??"미지정"}<span className="block text-xs">{spec.measurement?.workloadMode??"미제공"} · {String(spec.measurement?.durationSeconds??"미제공")}s</span></TableCell><TableCell className="text-right tabular-nums">{items.length}<span className="block text-xs text-muted-foreground">소유 미확인</span></TableCell></TableRow>})}</TableBody></Table>{!groups.size&&<p className="p-5">조건에 맞는 회차가 없어요.</p>}</div></QueryGate>
    <div className="flex items-center justify-between tabular-nums"><span>{page} / {pages}</span><div className="flex gap-2"><Button variant="outline" disabled={page===1} onClick={()=>go(page-1)}>이전</Button><Button variant="outline" disabled={page===pages} onClick={()=>go(page+1)}>다음</Button></div></div><SourceJSON value={{namespace,readAt:query.data?.readAt,resourceVersion:query.data?.resourceVersion,filters:Object.fromEntries(params),runs:filtered}} title="조회 조건과 회차 원본"/>
  </Screen>
}
export function MeasurementGroupPage() {
  const params=useSearchParams(),{config}=useSession(),namespace=params.get("namespace")??config.namespace,query=useAllMeasurementRuns(namespace)
  const parent=params.get("parent")??"",parentUID=params.get("parentUID"),parentResult=useChillQuery<CampaignResult>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations/${encodeURIComponent(parent||"-")}/result`,{enabled:!!parent,decoder:parseSchedulerJSON})
  const result=parentResult.data,parentMatches=!!parentUID&&result?.campaign.uid===parentUID
  const rows=matchingRuns(query.data?.runs??[],new URLSearchParams(params)).filter(run=>!params.get("group")||runGroupKey(run)===params.get("group")).sort((a,b)=>a.creationTimestamp.localeCompare(b.creationTimestamp))
  const back=safeMeasurementReturn(params.get("returnTo"),"/measurements/runs"),returnTo=`/measurements/group?${params}`
  return <Screen width="data" actionPlacement="header" title="조건별 측정 회차" lede="부하 측정과 유휴 기준 회차를 나누어 보여 줘요." back={[back,"상위 측정"]} breadcrumbs={[{href:"/measurements",label:"측정"},{href:back,label:"개별 회차"},{label:"조건별 회차"}]}>
    <MeasurementNavigation namespace={namespace}/>
    {parent&&<QueryGate query={parentResult}><p>{parentMatches?"요청 연결 확인됨":"요청 연결을 확인하지 못했어요."}</p></QueryGate>}
    <QueryGate query={query}>{[false,true].map(idle=>{const items=rows.filter(run=>(runSpec(run).measurement?.workloadMode==="idle-model-loaded")===idle);return items.length>0&&<section key={String(idle)} className="min-w-0 space-y-3"><h2 className="font-semibold">{idle?"모델만 올려둔 기준 전력":"부하 측정"}</h2><div className="overflow-x-auto rounded-xl border"><Table><TableHeader><TableRow>{["원본 Run / 생성일(KST)","조건 / batch","원본 attempt","실행 / 근거","소유 관계"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{items.map(run=>{const spec=runSpec(run),linked=parentMatches?exactRunTrial(run,result?.result):undefined;return <TableRow key={run.uid}><TableCell><Link className="break-all text-primary underline" href={`/measurements/profiling-runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid,returnTo,...(parentMatches?{parentUID:parentUID!,parent}:{})})}`}>{run.name}</Link><span className="block text-xs tabular-nums" title={run.creationTimestamp}>{new Date(run.creationTimestamp).toLocaleString("ko-KR",{timeZone:"Asia/Seoul"})}</span></TableCell><TableCell className="tabular-nums">{spec.candidate?.powerMode??"미제공"} / CPU {spec.measurement?.cpuQuota??"미제공"}<span className="block">batch {spec.measurement?.batch??"미제공"}</span></TableCell><TableCell className="text-right tabular-nums">{linked?.trial.attempt??"미제공 · 재번호 안 함"}</TableCell><TableCell>실행 {currentRunCondition(run,"ExecutionComplete")?.status??"세대 미확인"}<span className="block">근거 {currentRunCondition(run,"EvidenceVerified")?.status??"세대 미확인"}</span></TableCell><TableCell>{linked?linked.excluded?"정확 참조 · 제외":"정확 참조 · 완료":"관련 후보 — 소유 관계 미확인"}</TableCell></TableRow>})}</TableBody></Table></div></section>})}{!rows.length&&query.data&&<p>이 조건의 회차가 없어요.</p>}</QueryGate><SourceJSON value={{namespace,filter:Object.fromEntries(params),runs:rows}} title="조건과 원본 Run"/>
  </Screen>
}
