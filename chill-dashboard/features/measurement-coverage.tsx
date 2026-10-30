"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useRef, useSyncExternalStore } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, useChillQuery, useSession } from "@/lib/client"
import { batchStatisticsText, coverageCells, coverageRequestHref, coverageStates, ownedRuns, profileForPlan, profileVariant, relatedPlans, runAttempt, runSpec, stateSymbols, statisticsMissing, type CompatibilityRead, type CoverageCell, type CoverageState } from "@/lib/measurement-coverage"
import type { CampaignResult, InventoryView, MeasurementPlan, MeasurementPlanList, ModelCompatibility, Passport, ProfilingRun, ProfilesView, RunsView } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

async function readPage<T>(path:string,token:string,signal:AbortSignal):Promise<T> {
  const response=await fetch(`/api/chill/${path}`,{headers:{Authorization:`Bearer ${token}`},signal,cache:"no-store",credentials:"omit"})
  const body=await response.text()
  if(!response.ok) throw new ApiError(response.status,body,null)
  return parseSchedulerJSON(body) as T
}
export function useCoverageInventory() {
  const {token,config}=useSession()
  const query=useQuery<InventoryView,ApiError>({queryKey:["chill","coverage-inventory"],queryFn:async({signal})=>{
    const classes=new Map<string,InventoryView["classes"][number]>(),models=new Map<string,InventoryView["models"][number]>(),seen=new Set<number>(),pending=[0],pages:InventoryView[]=[]
    while(pending.length) {
      const offset=pending.shift()!
      if(seen.has(offset)) continue
      seen.add(offset)
      const page=await readPage<InventoryView>(`v1/inventory?${new URLSearchParams({limit:String(config.maxPageSize),offset:String(offset)})}`,token,signal)
      pages.push(page)
      for(const cls of page.classes) classes.set(cls.uid,cls)
      for(const model of page.models) models.set(model.uid,model)
      for(const next of [page.classPage?.nextOffset,page.modelPage?.nextOffset]) if(next!==undefined) {if(next<=offset) throw new ApiError(0,"inventory 페이지가 전진하지 않아요",null);pending.push(next)}
    }
    return {...pages[0],classes:[...classes.values()],models:[...models.values()],classPage:undefined,modelPage:undefined,sourcePages:pages.map(({classes:_classes,models:_models,...page})=>{void _classes;void _models;return page})}
  }})
  return {...query,data:query.error&&[401,403].includes(query.error.status)?undefined:query.data}
}
export function useAllMeasurementRuns(namespace:string,enabled=true) {
  const {token,config}=useSession()
  const query=useQuery<RunsView,ApiError>({queryKey:["chill",`coverage-runs/${namespace}`],enabled,queryFn:async({signal})=>{
    const runs=new Map<string,RunsView["runs"][number]>(),pages:Omit<RunsView,"runs">[]=[]
    let offset=0,first:RunsView|undefined
    while(true) {
      const page=await readPage<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${new URLSearchParams({limit:String(config.maxPageSize),offset:String(offset)})}`,token,signal)
      first??=page
      for(const run of page.runs) runs.set(run.uid,run)
      const {runs:_runs,...scope}=page;void _runs;pages.push(scope)
      if(page.page?.nextOffset===undefined) break
      if(page.page.nextOffset<=offset) throw new ApiError(0,"Run 페이지가 전진하지 않아요",null)
      offset=page.page.nextOffset
    }
    return {...first,runs:[...runs.values()],page:undefined,sourcePages:pages}
  }})
  return {...query,data:query.error&&[401,403].includes(query.error.status)?undefined:query.data}
}
export function useModelCompatibility(modelSpec:string,executionPath:string,deviceClass:string,nodes:{name:string;uid:string;resourceVersion:string}[],modelUID?:string,classUID?:string) {
  const {token}=useSession()
  return useQuery({queryKey:["chill","model-compatibility",modelSpec,modelUID,executionPath,deviceClass,classUID,nodes],enabled:!!token&&!!modelSpec&&!!executionPath&&!!deviceClass&&nodes.length>0,retry:false,queryFn:async({signal})=>{
    const reads:CompatibilityRead[]=[]
    // Only an opened cell's Nodes (or the single request Node), one GET at a time; power modes share this query.
    for (const node of nodes) {
      const selection={modelSpec,executionPath,deviceClass,node:node.name}
      try {
        const data=await readPage<ModelCompatibility>(`v1/model-compatibility?${new URLSearchParams(selection)}`,token,signal)
        if (Object.entries(selection).some(([key,value])=>data.selection[key as keyof typeof selection]!==value)||data.inputs.node.uid!==node.uid||(modelUID&&data.inputs.modelSpec.uid!==modelUID)||(classUID&&data.inputs.deviceClass.uid!==classUID)) throw new Error("현재 선택 identity와 응답이 달라요")
        reads.push({node:node.name,selection,data})
      } catch(error) {
        if(signal.aborted) throw error
        reads.push({node:node.name,selection,error:error instanceof Error?error.message:"호환성 조회 실패"})
      }
    }
    return reads
  }})
}
export function Statistics({data,batch}:{data:CampaignResult;batch?:number}) {
  const rows=data.statistics?.batches.filter(row=>batch===undefined||row.batch===batch)??[]
  return <section className="grid gap-2 text-xs" aria-label="저장 원근거 통계">{rows.length?rows.map(row=>{const text=batchStatisticsText(row);return <div key={row.batch} className="space-y-1 tabular-nums"><p>배치 {row.batch} · {text.ci}</p><p>{text.p95}</p><p className="text-muted-foreground">{text.trials} · 배치 1회 호출 시간, item 수로 나누지 않아요.</p><p className="text-muted-foreground">{row.confidence.method} / {row.confidence.estimatorVersion}</p></div>}):<p>{statisticsMissing(data.statisticsReason)}</p>}</section>
}
export function OwnedRunLinks({runs,parent,returnTo}:{runs:ProfilingRun[];parent:{namespace:string;uid:string};returnTo:string}) {
  const linked=ownedRuns(runs,parent)
  return linked.length?<ul className="space-y-2">{linked.map(run=><li key={run.uid}><Link className="break-all text-primary underline" href={`/measurements/profiling-runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.name)}?${new URLSearchParams({uid:run.uid,parentUID:parent.uid,returnTo})}`}>{run.name}</Link><p className="text-xs">{runSpec(run).measurement?.workloadMode??"모드 미기록"} · batch {runSpec(run).measurement?.batch??"미기록"} · {runAttempt(run)} · {run.status.conditions?.filter(c=>c.observedGeneration===run.generation).map(c=>`${c.type}=${c.status} ${c.reason??""}`).join(" / ")||"상태 미제공"}</p></li>)}</ul>:<p>소유 연결 기록 없음 · 이름·labels로 소유를 추정하지 않아요.</p>
}
export function MeasurementNavigation({namespace}:{namespace:string}) {
  return <nav aria-label="측정 종류" className="flex flex-wrap gap-2 border-b pb-4 text-sm">{[["/measurements/coverage","커버리지"],["/measurements","요청 목록"],["/scheduler/profiles","프로파일"],["/measurements/runs","개별 회차"],["/measurements/operation-cost-measurements","동작 비용"],["/measurements/dispatch-occupancy-measurements","분배 점유"],["/measurements/check","계획 검사"]].map(([href,label])=><Link key={href} className="rounded-md border bg-card px-3 py-2 hover:border-primary focus-visible:outline-2 focus-visible:outline-ring" href={`${href}?${new URLSearchParams({namespace})}`}>{label}</Link>)}</nav>
}
export function MeasurementFilters({axes,states=true,dates=false}:{axes:{key:string;label:string;values:string[]}[];states?:boolean;dates?:boolean}) {
  const params=useSearchParams(),router=useRouter()
  function set(key:string,value:string) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);next.delete("page");router.replace(`?${next}`,{scroll:false})}
  return <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">{axes.map(axis=><label key={axis.key} className="grid gap-1 text-sm">{axis.label}<select className="h-11 min-w-0 rounded-md border bg-background px-2" value={params.get(axis.key)??""} onChange={event=>set(axis.key,event.target.value)}><option value="">전체</option>{[...new Set(axis.values)].sort().map(value=><option key={value}>{value}</option>)}</select></label>)}
    {states&&<label className="grid gap-1 text-sm">상태<select className="h-11 rounded-md border bg-background px-2" value={params.get("status")??""} onChange={event=>set("status",event.target.value)}><option value="">전체</option>{Object.entries(coverageStates).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>}
    {dates&&["from","to"].map((key,index)=><label key={key} className="grid gap-1 text-sm">생성일 {index?"종료":"시작"} · KST<Input className="h-11" type="date" value={params.get(key)??""} onChange={event=>set(key,event.target.value)}/></label>)}
    <label className="grid gap-1 text-sm">이름 검색<Input className="h-11" value={params.get("q")??""} onChange={event=>set("q",event.target.value)}/></label>
  </div>
}
const stateClass:Record<CoverageState,string>={complete:"border-ok/35 bg-[var(--ok-soft)] text-ok",warning:"border-heat/50 bg-[var(--heat-soft)] text-heat",failed:"border-fail/40 bg-[var(--fail-soft)] text-fail",running:"border-primary/40 bg-[var(--info-soft)] text-primary",unmeasured:"border-dashed bg-card text-muted-foreground",unsupported:"bg-[repeating-linear-gradient(135deg,transparent,transparent_6px,var(--color-muted)_6px,var(--color-muted)_8px)] text-muted-foreground",unknown:"border-muted-foreground/40 bg-[var(--idle-soft)] text-foreground"}
function desktopSubscribe(notify:()=>void) {const media=window.matchMedia("(min-width: 1280px)");media.addEventListener("change",notify);return()=>media.removeEventListener("change",notify)}
export function MeasurementCoveragePage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const inventory=useCoverageInventory(),plans=useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`),catalog=useChillQuery<ProfilesView>("v1/scheduler/profiles",{decoder:parseSchedulerJSON})
  const ready=!!inventory.data&&!!plans.data&&!!catalog.data&&!inventory.error&&!plans.error&&!catalog.error
  const preliminary=coverageCells({inventory:inventory.data,plans:plans.data?.items??[],catalog:catalog.data,complete:ready,runsComplete:false})
  const selectedCandidate=preliminary.find(cell=>cell.key===params.get("cell"))
  const compatibility=useModelCompatibility(selectedCandidate?.model.name??"",selectedCandidate?.path.name??"",selectedCandidate?.cls.name??"",selectedCandidate?.cls.nodes.filter(node=>!params.get("node")||node.name===params.get("node"))??[],selectedCandidate?.model.uid,selectedCandidate?.cls.uid)
  const runs=useAllMeasurementRuns(namespace,ready&&(!!params.get("cell")||preliminary.some(cell=>cell.state==="unknown"&&cell.reason.startsWith("이력 확인"))))
  const cpu=params.get("cpu"),batch=params.get("batch")
  const all=coverageCells({inventory:inventory.data,plans:(plans.data?.items??[]).filter(p=>!cpu||p.spec.cpuQuota===cpu),catalog:catalog.data?{...catalog.data,profiles:(catalog.data.profiles??[]).filter(p=>(!cpu||p.condition.cpu.limit===cpu)&&(!batch||p.batches?.some(b=>String(b.batch)===batch)))}:undefined,runs:runs.data?.runs.filter(run=>(!cpu||runSpec(run).measurement?.cpuQuota===cpu)&&(!batch||String(runSpec(run).measurement?.batch)===batch)),complete:ready&&!runs.error,runsComplete:!!runs.data&&!runs.error,node:params.get("node")??undefined,compatibility:compatibility.data})
  const filtered=all.filter(cell=>(!cpu||cell.plans.length>0||cell.profiles.length>0||cell.runs.length>0)&&(!batch||cell.profiles.some(p=>p.batches?.some(b=>String(b.batch)===batch))||cell.runs.length>0)&&(!params.get("model")||cell.model.name===params.get("model"))&&(!params.get("deviceClass")||cell.cls.name===params.get("deviceClass"))&&(!params.get("path")||cell.path.name===params.get("path"))&&(!params.get("powerMode")||cell.mode===params.get("powerMode"))&&(!params.get("status")||cell.state===params.get("status"))&&(!params.get("q")||`${cell.model.name} ${cell.path.name} ${cell.cls.name} ${cell.plans.map(p=>p.metadata.name).join(" ")}`.toLowerCase().includes(params.get("q")!.toLowerCase()))&&params.get("paper")!=="1")
  const selected=all.find(cell=>cell.key===params.get("cell")),desktop=useSyncExternalStore(desktopSubscribe,()=>window.matchMedia("(min-width: 1280px)").matches,()=>false)
  const trigger=useRef<HTMLButtonElement|null>(null)
  function select(cell:CoverageCell,button:HTMLButtonElement) {trigger.current=button;const next=new URLSearchParams(params.toString());next.set("cell",cell.key);next.delete("variant");router.replace(`?${next}`,{scroll:false})}
  function close() {const next=new URLSearchParams(params.toString());next.delete("cell");next.delete("variant");router.replace(`?${next}`,{scroll:false});trigger.current?.focus()}
  const classes=[...new Map(filtered.map(cell=>[cell.cls.uid,cell.cls])).values()],rows=[...new Map(filtered.map(cell=>[`${cell.model.uid}/${cell.path.name}`,{model:cell.model,path:cell.path}])).values()]
  const mobileClass=params.get("mobileClass")??classes[0]?.uid
  const orphanPlans=(plans.data?.items??[]).filter(plan=>!preliminary.some(cell=>cell.plans.some(p=>p.metadata.uid===plan.metadata.uid)))
  const unlinkedIssues=(catalog.data?.insufficiencies??[]).filter(issue=>!(plans.data?.items??[]).some(plan=>plan.metadata.namespace===issue.source.Namespace&&plan.metadata.name===issue.source.Name))
  const counts=Object.fromEntries(Object.keys(coverageStates).map(state=>[state,filtered.filter(cell=>cell.state===state).length]))
  return <Screen width="data" actionPlacement="header" title="측정 커버리지" lede="선언 후보 대비 측정 현황 — 실행 지원 여부를 모두 보장하지 않아요" breadcrumbs={[{href:"/measurements",label:"측정"},{label:"커버리지"}]} action={<Button asChild><Link href="/measurements/new">새 측정 요청</Link></Button>}>
    <MeasurementNavigation namespace={namespace}/>
    <section className="grid gap-3" aria-label="커버리지 집계와 필터">
    <div className="flex flex-wrap gap-x-5 gap-y-2 border-y py-4 text-sm tabular-nums"><strong>표시 후보 {filtered.length} / 전체 선언 후보 {preliminary.length}</strong>{Object.entries(coverageStates).map(([key,label])=><span key={key}>{stateSymbols[key as CoverageState]} {label} {counts[key]}</span>)}</div>
    <p className="text-sm text-muted-foreground tabular-nums">요청 {plans.data?.items?.length??"미확인"}건 / 현재 profile {catalog.data?.profiles?.length??"미확인"}개 / Run {runs.data?`${runs.data.runs.length}개`:runs.isFetching?"이력 확인 중":"미조회"} — 서로 다른 모집단이며 지원 완료율이 아니에요.</p>
    <MeasurementFilters axes={[{key:"model",label:"모델",values:all.map(c=>c.model.name)},{key:"deviceClass",label:"장치 종류",values:all.map(c=>c.cls.name)},{key:"node",label:"Node",values:inventory.data?.classes.flatMap(c=>c.nodes.map(n=>n.name))??[]},{key:"path",label:"실행 경로",values:all.map(c=>c.path.name)},{key:"powerMode",label:"전력모드",values:all.map(c=>c.mode)},{key:"cpu",label:"CPU quota",values:(plans.data?.items??[]).flatMap(p=>p.spec.cpuQuota?[p.spec.cpuQuota]:[])},{key:"batch",label:"실제 batch",values:preliminary.flatMap(c=>c.batches.map(String))}]}/>
    <label className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={params.get("paper")==="1"} onChange={event=>{const next=new URLSearchParams(params.toString());if(event.target.checked)next.set("paper","1");else next.delete("paper");router.replace(`?${next}`,{scroll:false})}}/>논문용 검토만</label>
    {params.get("paper")==="1"&&<p className="text-sm text-muted-foreground">catalog 목록에는 CI가 없으므로 일괄 적격 판정을 하지 않아요. 필터를 해제하고 조건 패널에서 선택 요청의 보존 원근거 CI를 확인하세요. 논문 적격/통계적 유의성 인증이 아니에요.</p>}
    <QueryGate query={inventory}><QueryGate query={plans}><QueryGate query={catalog}>{null}</QueryGate></QueryGate></QueryGate>
    {runs.error&&<p role="alert">Run 전 범위 조회 실패 · 미측정으로 판정하지 않아요. {runs.error.message}</p>}
    <label className="grid gap-1 text-sm md:hidden">작은 화면에서 볼 장치 종류<select className="h-11 rounded-md border bg-background px-2" value={mobileClass??""} onChange={event=>{const next=new URLSearchParams(params.toString());next.set("mobileClass",event.target.value);router.replace(`?${next}`,{scroll:false})}}>{classes.map(cls=><option key={cls.uid} value={cls.uid}>{cls.name}</option>)}</select></label>
    <p className="text-xs text-muted-foreground">표 안에서 가로로 더 볼 수 있어요. 상태 셀을 열면 이유와 실제 조건을 볼 수 있어요.</p>
    </section>
    <div className="grid min-w-0 items-start gap-5 xl:grid-cols-[minmax(0,1fr)_22rem]">
      <div className="min-w-0 rounded-xl border [&>[data-slot=table-container]]:max-h-[70dvh] [&>[data-slot=table-container]]:overflow-auto" aria-label="선언 후보 측정 행렬"><Table tabIndex={0} aria-label="모델 실행 경로별 장치 전력모드 측정 현황" className="w-max min-w-full border-separate border-spacing-0 text-sm"><TableHeader className="sticky top-0 z-10 bg-muted"><TableRow><TableHead rowSpan={2} className="sticky left-0 z-20 min-w-48 border-r bg-card">모델 / 실행 경로</TableHead>{classes.map(cls=><TableHead key={cls.uid} colSpan={cls.powerModes.filter(mode=>filtered.some(c=>c.cls.uid===cls.uid&&c.mode===mode.name)).length} className={`${cls.uid===mobileClass?"":"hidden md:table-cell"} border-r text-center`}>{cls.name}</TableHead>)}</TableRow><TableRow>{classes.flatMap(cls=>cls.powerModes.filter(mode=>filtered.some(c=>c.cls.uid===cls.uid&&c.mode===mode.name)).map(mode=><TableHead key={`${cls.uid}/${mode.name}`} className={`${cls.uid===mobileClass?"":"hidden md:table-cell"} min-w-32 border-r text-center`}>{mode.name}</TableHead>))}</TableRow></TableHeader><TableBody>
        {rows.map(({model,path})=><TableRow key={`${model.uid}/${path.name}`}><TableHead className="sticky left-0 z-10 w-48 min-w-48 max-w-48 whitespace-normal border-r bg-card px-4 [overflow-wrap:anywhere]"><span className="block font-semibold">{model.name}</span><span className="text-xs text-muted-foreground">{path.name}</span></TableHead>{classes.flatMap(cls=>cls.powerModes.filter(mode=>filtered.some(c=>c.cls.uid===cls.uid&&c.mode===mode.name)).map(mode=>{const cell=filtered.find(c=>c.model.uid===model.uid&&c.path.name===path.name&&c.cls.uid===cls.uid&&c.mode===mode.name);return <TableCell key={`${cls.uid}/${mode.name}`} className={`${cls.uid===mobileClass?"":"hidden md:table-cell"} border-r p-1`}>{cell?<button type="button" onClick={event=>select(cell,event.currentTarget)} aria-pressed={selected?.key===cell.key} className={`grid min-h-24 w-full min-w-32 scroll-mt-24 scroll-mr-2 scroll-ml-52 gap-1 rounded-lg border p-3 text-left focus-visible:outline-2 focus-visible:outline-ring ${stateClass[cell.state]} ${selected?.key===cell.key?"ring-2 ring-primary":""}`}><strong>{stateSymbols[cell.state]} {coverageStates[cell.state]}</strong><span className="text-xs tabular-nums">{cell.variants>1?`조건 ${cell.variants}개`:cell.batches.length?`batch ${cell.batches.join(", ")}`:cell.state==="unknown"?cell.reason.startsWith("이력 확인")?"이력 확인 중":"이유 확인":""}</span><span className="text-xs tabular-nums">요청 {cell.plans.length} · 관련 Run {runs.data?cell.runs.length:"미조회"}</span>{cell.profiles.length>0&&<span className="text-xs tabular-nums">완료 Node {cell.completeNodes}/{cell.nodes}</span>}</button>:<span className="text-xs text-muted-foreground">필터 제외</span>}</TableCell>}))}</TableRow>)}
      </TableBody></Table>{!filtered.length&&<p className="p-6">표시할 후보가 없어요. 필터를 바꿔 주세요.</p>}</div>
      <aside className="hidden min-w-0 rounded-xl border bg-card p-5 xl:block">{desktop&&selected?<CoveragePanel key={selected.key} cell={selected} namespace={namespace} reads={compatibility.data} compatibilityPending={compatibility.isFetching} close={close}/>:<><h2 className="font-semibold">선택 조건</h2><p className="mt-3 text-sm text-muted-foreground">셀을 선택하면 Node별 근거, 실제 batch와 시도 이력을 이 자리에서 볼 수 있어요.</p></>}</aside>
    </div>
    <Dialog open={!!selected&&!desktop} onOpenChange={open=>{if(!open)close()}}><DialogContent className="max-h-[90dvh] overflow-y-auto" onCloseAutoFocus={event=>{event.preventDefault();trigger.current?.focus()}}><DialogHeader><DialogTitle>선택 조건</DialogTitle><DialogDescription>선언, 사용 가능한 근거와 시도 이력을 구분해요.</DialogDescription></DialogHeader>{selected&&<CoveragePanel key={selected.key} cell={selected} namespace={namespace} reads={compatibility.data} compatibilityPending={compatibility.isFetching} close={close}/>}</DialogContent></Dialog>
    <section className="space-y-2 border-t pt-4 text-xs text-muted-foreground tabular-nums"><h2 className="font-semibold text-foreground">조회 범위와 상태 범례</h2><p>namespace {namespace} · inventory 양쪽 페이지 끝까지 / BEO namespace 목록 / catalog 전체. 서로 다른 시각의 GET이며 원자적 snapshot이 아니에요.</p><p>inventory readAt {inventory.data?.readAt??"미확인"} · BEO 조회 {plans.dataUpdatedAt?new Date(plans.dataUpdatedAt).toISOString():"미확인"} · catalog 조회 {catalog.dataUpdatedAt?new Date(catalog.dataUpdatedAt).toISOString():"미확인"} · Run {runs.data?.readAt??"미조회"}</p><p>완료=현재 catalog 근거. ∕ 지원 안 함=명시적 선언 불일치. ○ 미측정=전 범위 이력 없음. ? 확인 불가=미조회/오류/선언 모순. 일치하는 선언은 runtime 지원 보장이 아니에요.</p><Button variant="outline" onClick={()=>{void inventory.refetch();void plans.refetch();void catalog.refetch();if(runs.data||runs.error)void runs.refetch()}}>목록 새로 고침</Button><SourceJSON value={{inventory:inventory.data,profiles:catalog.data,namespace,readAt:{plans:plans.dataUpdatedAt,catalog:catalog.dataUpdatedAt},runs:runs.data?{readAt:runs.data.readAt,count:runs.data.runs.length}:null}} title="조회 범위 원본"/></section>
    {!!orphanPlans.length&&<section><h2 className="font-semibold">현재 선언 밖 이력 {orphanPlans.length}건</h2><ul className="mt-2 space-y-2 text-sm">{orphanPlans.map(plan=><li key={plan.metadata.uid}><Link className="text-primary underline" href={planLink(plan)}>{plan.metadata.name}</Link> · {plan.spec.state.candidate.modelSpec} / {plan.spec.state.candidate.deviceClass} / {plan.spec.state.candidate.powerMode}</li>)}</ul></section>}
    {unlinkedIssues.length>0&&<section className="space-y-2"><h2 className="font-semibold">연결 안 된 경고 {unlinkedIssues.length}개</h2>{unlinkedIssues.map((issue,index)=><p key={index} className="text-sm">{issue.source.Namespace}/{issue.source.Name} · {issue.code} · {issue.reason}</p>)}</section>}
  </Screen>
}
export function planLink(plan:MeasurementPlan,returnTo?:string) {return `/measurements/batch-energy-optimizations/${encodeURIComponent(plan.metadata.namespace)}/${encodeURIComponent(plan.metadata.name)}?${new URLSearchParams({uid:plan.metadata.uid,...(returnTo?{returnTo}:{})})}`}
function CoveragePanel({cell,namespace,reads,compatibilityPending,close}:{cell:CoverageCell;namespace:string;reads?:CompatibilityRead[];compatibilityPending:boolean;close:()=>void}) {
  const params=useSearchParams(),router=useRouter(),returnTo=`/measurements/coverage?${params}`
  const variants=[...new Map(cell.profiles.map(profile=>[profileVariant(profile),profile])).values()],profile=variants.find(p=>p.id===params.get("variant"))??variants[0]
  const plan=cell.plans.find(p=>p.metadata.uid===params.get("request"))??[...cell.plans].sort((a,b)=>b.metadata.creationTimestamp.localeCompare(a.metadata.creationTimestamp))[0]
  const result=useChillQuery<CampaignResult>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations/${encodeURIComponent(plan?.metadata.name??"-")}/result`,{enabled:!!plan,decoder:parseSchedulerJSON})
  const body=result.data?.result as {completedTrials?:{run:{namespace:string;name:string;uid:string};measurement?:{batch?:number};attempt?:number}[];excludedTrials?:{run:{namespace:string;name:string;uid:string};measurement?:{batch?:number};attempt?:number}[]}|null|undefined
  const validResult=!plan||(result.data?.campaign.uid===plan.metadata.uid&&result.data.campaign.namespace===plan.metadata.namespace&&result.data.campaign.name===plan.metadata.name)
  const trial=validResult?(body?.completedTrials?.find(item=>item.run.uid===params.get("trial"))??body?.completedTrials?.[0]):undefined
  const passport=useChillQuery<Passport>(`v1/namespaces/${encodeURIComponent(trial?.run.namespace??namespace)}/profiling-runs/${encodeURIComponent(trial?.run.name??"-")}/evidence-passport`,{enabled:!!trial,decoder:parseSchedulerJSON})
  const runs=useAllMeasurementRuns(namespace)
  const statisticsMatch=!!profile&&!!plan&&validResult&&profileForPlan(plan,[profile]).length>0
  const set=(key:string,value:string)=>{const next=new URLSearchParams(params.toString());next.set(key,value);router.replace(`?${next}`,{scroll:false})}
  const node=params.get("node")??(cell.cls.nodes.length===1?cell.cls.nodes[0].name:"")
  return <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4 text-sm [&>*]:min-w-0"><div className="flex min-w-0 items-start justify-between gap-3"><h2 className="min-w-0 font-semibold [overflow-wrap:anywhere]">{cell.model.name}<span className="block text-muted-foreground">{cell.path.name} / {cell.cls.name} / {cell.mode}</span></h2><Button variant="outline" size="sm" onClick={close}>닫기</Button></div><p className={`rounded-md border p-3 ${stateClass[cell.state]}`}><strong>{stateSymbols[cell.state]} {coverageStates[cell.state]}</strong><span className="mt-1 block">{cell.reason}</span></p><p>{cell.compatibility.reason}</p>{cell.notes.map(note=><p key={note} className="text-heat">{note}</p>)}
    <label className="grid min-w-0 gap-1">Node<select className="h-11 min-w-0 max-w-full rounded-md border bg-background px-2" value={node} onChange={event=>set("node",event.target.value)}><option value="">전체 Node · 요청 시 명시 선택</option>{cell.cls.nodes.map(n=><option key={n.uid}>{n.name}</option>)}</select></label>
    <section className="space-y-2" aria-label="Node별 정적 호환성"><h3 className="font-semibold">현재 선언의 정적 지원</h3>{compatibilityPending&&<p role="status">선택 조건 Node만 조회 중이에요.</p>}{reads?.map(read=><div key={read.node} className="break-words text-xs"><strong>{read.node}</strong><p>{read.error?`지원 확인 불가 — 조회 실패 ${read.error}`:read.data?.verdict==="Compatible"?"정적 실행 지원 확인":`${read.data?.verdict==="Incompatible"?"지원 안 함":"지원 확인 불가"} — ${read.data?.reasons.map(reason=>`${reason.message} (${reason.code})`).join(" / ")}`}</p>{read.data&&<p>조회 {read.data.readAt}</p>}</div>)}<p className="text-xs text-muted-foreground">실제 실행·Ready·전력모드 적격성·측정 완료·admission 성공은 별도예요. 다른 셀 전체를 일괄 조회하지 않아요.</p></section>
    <p className="tabular-nums">조건 {cell.variants}개 · 현재 profile {cell.profiles.length}개 · 요청 {cell.plans.length}건 · 연결 후보 Run {cell.runs.length}개</p>
    {variants.length>0&&<label className="grid gap-1">정확 동결 조건<select className="h-11 min-w-0 rounded-md border bg-background px-2" value={profile?.id} onChange={event=>set("variant",event.target.value)}>{variants.map((p,index)=><option key={p.id} value={p.id}>{index+1}: {p.nodeName} / CPU {p.condition.cpu.limit} / {p.condition.activeProtocol.dataset?.name??"데이터셋 미제공"}</option>)}</select></label>}
    {profile&&<><p className="break-words text-xs text-muted-foreground">이미지 {profile.condition.runtimeImage} · affinity {profile.condition.cpu.affinityCPUs?.join(", ")??"미지정"} · 실제 Node UID {profile.condition.nodeUID}</p><Table><TableHeader><TableRow><TableHead>batch</TableHead><TableHead className="text-right">증분 J/건</TableHead><TableHead>오차 / 지연</TableHead></TableRow></TableHeader><TableBody>{profile.batches?.map(batch=><TableRow key={batch.batch}><TableCell className="tabular-nums">{batch.batch}</TableCell><TableCell className="text-right tabular-nums">{Number.isFinite(batch.energyPerInputJoules)?batch.energyPerInputJoules.toPrecision(4):"품질 경고"}</TableCell><TableCell className="whitespace-normal text-xs">{statisticsMatch&&result.data?<Statistics data={result.data} batch={batch.batch}/>:<>CI 미제공<br/>p95 미제공 · 관측 {batch.durationNanos?.length??0}회</>}</TableCell></TableRow>)}</TableBody></Table><div className="space-y-2" aria-label="선택 조건 배치별 에너지">{(profile.batches??[]).filter(b=>Number.isFinite(b.energyPerInputJoules)&&b.energyPerInputJoules>=0).map(batch=><div key={batch.batch} className="grid grid-cols-[3rem_1fr] items-center gap-2 text-xs"><span>batch {batch.batch}</span><div className="h-3 bg-muted"><div className="h-full bg-primary" style={{width:`${batch.energyPerInputJoules/Math.max(...(profile.batches??[]).map(b=>Number.isFinite(b.energyPerInputJoules)?b.energyPerInputJoules:0),Number.EPSILON)*100}%`}}/></div></div>)}</div><p className="text-xs text-muted-foreground">배치별 증분 에너지와 선택 요청이 정확히 같은 저장 참조일 때만 통계를 연결해요. 논문 적격을 인증하지 않아요.</p><SourceJSON value={profile} title="선택 동결 조건·수치 원본"/></>}
    {trial&&<section className="grid gap-2"><label className="grid gap-1">지연을 확인할 정확 회차<select className="h-11 min-w-0 rounded-md border bg-background px-2" value={trial.run.uid} onChange={event=>set("trial",event.target.value)}>{body?.completedTrials?.map(item=><option key={item.run.uid} value={item.run.uid}>{item.run.name}</option>)}</select></label><QueryGate query={passport}>{passport.data&&passport.data.run.uid!==trial.run.uid?<p role="alert">다른 Run UID의 근거라 연결하지 않아요.</p>:passport.data&&<p className="tabular-nums">선택 회차 batch {passport.data.measurement.runtime.batch} · p95 {passport.data.measurement.runtime.latencyMilliseconds.count>0?(Number.isFinite(passport.data.measurement.runtime.latencyMilliseconds.p95)&&passport.data.measurement.runtime.latencyMilliseconds.p95>=0?`${passport.data.measurement.runtime.latencyMilliseconds.p95.toLocaleString("ko-KR",{maximumFractionDigits:3})} ms`:`${String(passport.data.measurement.runtime.latencyMilliseconds.p95)} · 품질 경고`):"미제공(표본 없음)"} · 지연 표본 {passport.data.measurement.runtime.latencyMilliseconds.count}개. 회차 하나의 분포이며 전체 반복을 합친 p95가 아니에요.</p>}</QueryGate></section>}
    <h3 className="font-semibold">시도 이력 · 소유 회차는 정확 참조만</h3>{cell.plans.map(p=><div key={p.metadata.uid} className="rounded-md border p-3"><Link className="break-all text-primary underline" href={planLink(p,returnTo)}>{p.metadata.name}</Link><p>{p.status?.phase??"상태 미제공"} · Node {p.spec.state.targetNode} · CPU {p.spec.cpuQuota??"미지정"}</p><p className="text-xs text-muted-foreground">{p.status?.conditions?.filter(c=>c.status==="False").map(c=>c.message??c.reason).join(" / ")}</p><button className="min-h-11 text-primary underline" onClick={()=>set("request",p.metadata.uid)}>이 요청의 정확 회차 보기</button></div>)}
    {plan&&<><p className="break-all">선택 요청: {plan.metadata.name}</p><QueryGate query={result}>{!validResult?<p role="alert">같은 이름의 다른 UID예요. 회차를 연결하지 않아요.</p>:result.data&&<><Statistics data={result.data}/>{body?<ul className="space-y-2">{[...(body.completedTrials??[]).map(trial=>({trial,excluded:false})),...(body.excludedTrials??[]).map(trial=>({trial,excluded:true}))].map(({trial,excluded})=><li key={trial.run.uid}><Link className="break-all text-primary underline" href={`/measurements/profiling-runs/${encodeURIComponent(trial.run.namespace)}/${encodeURIComponent(trial.run.name)}?${new URLSearchParams({uid:trial.run.uid,returnTo,parentUID:plan.metadata.uid})}`}>{excluded?"제외":"완료"} · batch {trial.measurement?.batch??"미제공"} · {trial.run.name}</Link></li>)}</ul>:<p>저장된 결과 없음 · 소유 Run은 아래에서 별도로 확인해요.</p>}</>}</QueryGate><QueryGate query={runs}>{runs.data&&!runs.error?<OwnedRunLinks runs={runs.data.runs} parent={{namespace:plan.metadata.namespace,uid:plan.metadata.uid}} returnTo={returnTo}/>:<p>Run 전 범위 조회 전 · 소유 회차 없음으로 단정하지 않아요.</p>}</QueryGate></>}
    <Link className="text-primary underline" href={`/measurements/runs?${new URLSearchParams({namespace,model:cell.model.name,deviceClass:cell.cls.name,path:cell.path.name,powerMode:cell.mode,...(node?{node}:{}),returnTo})}`}>조건별 관련 Run 보기</Link>
    {cell.compatibility.state==="unsupported"?<Button disabled>선언 불일치로 요청할 수 없어요</Button>:<Button asChild><Link href={coverageRequestHref(cell,namespace,node,returnTo)}>이 조건으로 측정 요청</Link></Button>}
  </div>
}

export function MeasurementPlansPage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const query=useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`),inventory=useCoverageInventory()
  const plans=query.data?.items??[],phaseLabels:Record<string,string>={Complete:"실행 완료 · 근거는 상세 확인",Failed:"실패",Measuring:"진행 중"}
  const axes=[{key:"model",label:"모델",values:plans.map(p=>p.spec.state.candidate.modelSpec)},{key:"deviceClass",label:"장치 종류",values:plans.map(p=>p.spec.state.candidate.deviceClass)},{key:"node",label:"Node",values:plans.map(p=>p.spec.state.targetNode)},{key:"powerMode",label:"전력모드",values:plans.map(p=>p.spec.state.candidate.powerMode)},{key:"path",label:"실행 경로",values:plans.map(p=>p.spec.state.candidate.executionPath)},{key:"status",label:"실행 상태",values:plans.map(p=>p.status?.phase??"미제공")}]
  const value=(p:MeasurementPlan,key:string)=>key==="model"?p.spec.state.candidate.modelSpec:key==="deviceClass"?p.spec.state.candidate.deviceClass:key==="node"?p.spec.state.targetNode:key==="powerMode"?p.spec.state.candidate.powerMode:key==="path"?p.spec.state.candidate.executionPath:key==="status"?p.status?.phase??"미제공":p.metadata.creationTimestamp
  const sort=params.get("sort")??"date",desc=params.get("direction")!=="asc"
  const filtered=plans.filter(p=>axes.every(axis=>!params.get(axis.key)||params.get(axis.key)===value(p,axis.key))&&(!params.get("q")||p.metadata.name.toLowerCase().includes(params.get("q")!.toLowerCase()))&&(!params.get("from")||Date.parse(p.metadata.creationTimestamp)>=Date.parse(`${params.get("from")}T00:00:00+09:00`))&&(!params.get("to")||Date.parse(p.metadata.creationTimestamp)<Date.parse(`${params.get("to")}T00:00:00+09:00`)+86400000)).sort((a,b)=>(desc?-1:1)*value(a,sort).localeCompare(value(b,sort)))
  const pages=Math.max(1,Math.ceil(filtered.length/25)),page=Math.min(pages,Math.max(1,Number(params.get("page"))||1)),shown=filtered.slice((page-1)*25,page*25),returnTo=`/measurements?${params}`
  function update(key:string,v:string) {const next=new URLSearchParams(params.toString());next.set(key,v);router.replace(`?${next}`,{scroll:false})}
  return <Screen width="data" actionPlacement="header" title="측정 요청 목록" lede="요청 한 건에 한 행. 실행 완료와 사용 가능한 근거는 서로 달라요." breadcrumbs={[{label:"측정"}]} action={<Button asChild><Link href="/measurements/new">새 측정 요청</Link></Button>}>
    <Link className="rounded-xl border border-primary/30 bg-[var(--info-soft)] p-5 text-primary" href={`/measurements/coverage?${new URLSearchParams({namespace})}`}><strong className="block text-lg">측정 커버리지</strong><span>어떤 선언 후보가 측정됐고, 어떤 조합이 비어 있는지 행렬에서 확인하세요.</span></Link><MeasurementNavigation namespace={namespace}/><MeasurementFilters axes={axes} states={false} dates/>
    <QueryGate query={query}><div className="min-w-0 overflow-x-auto rounded-xl border"><Table><TableHeader><TableRow>{[["model","모델"],["deviceClass","장치 종류"],["node","Node"],["powerMode","전력모드"],["path","실행 경로"],["status","실행 상태 / 후속 요청"],["date","생성일 · KST"]].map(([key,label])=><TableHead key={key} aria-sort={sort===key?desc?"descending":"ascending":"none"}><button className="min-h-11 whitespace-nowrap" onClick={()=>{const next=new URLSearchParams(params.toString());next.set("sort",key);next.set("direction",sort===key&&desc?"asc":"desc");router.replace(`?${next}`,{scroll:false})}}>{label} {sort===key?desc?"↓":"↑":"↕"}</button></TableHead>)}</TableRow></TableHeader><TableBody>{shown.map(plan=><TableRow key={plan.metadata.uid}><TableCell><Link className="text-primary underline" href={planLink(plan,returnTo)}>{plan.spec.state.candidate.modelSpec}<span className="block max-w-72 break-all text-xs text-muted-foreground">{plan.metadata.name}</span></Link></TableCell><TableCell>{plan.spec.state.candidate.deviceClass}</TableCell><TableCell>{plan.spec.state.targetNode}</TableCell><TableCell>{plan.spec.state.candidate.powerMode}</TableCell><TableCell>{plan.spec.state.candidate.executionPath}<span className="block text-xs text-muted-foreground">CPU {plan.spec.cpuQuota??"미지정"}</span></TableCell><TableCell>{phaseLabels[plan.status?.phase??""]??"상태 미확인"}{plan.status?.phase==="Failed"&&relatedPlans(plan,plans).filter(item=>item.plan.metadata.creationTimestamp>plan.metadata.creationTimestamp).slice(0,1).map(item=><Link key={item.plan.metadata.uid} className="block text-xs text-primary underline" href={planLink(item.plan,returnTo)}>후속 관련 요청 · 동결 identity 미확인</Link>)}</TableCell><TableCell className="whitespace-nowrap text-right tabular-nums" title={plan.metadata.creationTimestamp}>{new Date(plan.metadata.creationTimestamp).toLocaleString("ko-KR",{timeZone:"Asia/Seoul",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"})}</TableCell></TableRow>)}</TableBody></Table>{!filtered.length&&<p className="p-5">조건에 맞는 요청이 없어요.</p>}</div></QueryGate>
    <div className="flex items-center justify-between gap-3 tabular-nums"><span className="text-sm">{filtered.length}건 / {page}–{pages}쪽 · 25행씩</span><div className="flex gap-2"><Button variant="outline" disabled={page<=1} onClick={()=>update("page",String(page-1))}>이전</Button><Button variant="outline" disabled={page>=pages} onClick={()=>update("page",String(page+1))}>다음</Button></div></div>
    <p className="text-xs text-muted-foreground">현재 선언 장치 {inventory.data?.classes.length??"미확인"}종 · 생성일 필터이며 profile 측정일이 아니에요. UI 필터는 upstream으로 전달하지 않아요.</p><SourceJSON value={{namespace,filters:Object.fromEntries(params),readAt:query.dataUpdatedAt,source:query.data}} title="목록 원본과 조회 범위"/>
  </Screen>
}
