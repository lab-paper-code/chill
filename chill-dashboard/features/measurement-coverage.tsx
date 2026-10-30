"use client"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useRef, useSyncExternalStore } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, useSession } from "@/lib/client"
import { coverageSelection, powerModeLabel, type CompatibilityRead } from "@/lib/measurement-coverage"
import type { InventoryView, ModelCompatibility, MeasurementCoverageResponse, MeasurementCoverageCell, MeasurementVariant } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"
const coverageStates={usable:"사용 가능",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"확인 불가"}
import { ResultRows, AssessmentDetails } from "@/features/measurement-plans"
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


export function MeasurementFilters({axes,states=true,dates=false}:{axes:{key:string;label:string;values:string[]}[];states?:boolean;dates?:boolean}) {
  const params=useSearchParams(),router=useRouter()
  function set(key:string,value:string) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);next.delete("page");router.replace(`?${next}`,{scroll:false})}
  return <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">{axes.map(axis=><label key={axis.key} className="grid gap-1 text-sm">{axis.label}<select className="h-11 min-w-0 rounded-md border bg-background px-2" value={params.get(axis.key)??""} onChange={event=>set(axis.key,event.target.value)}><option value="">전체</option>{[...new Set(axis.values)].sort().map(value=><option key={value}>{value}</option>)}</select></label>)}
    {states&&<label className="grid gap-1 text-sm">상태<select className="h-11 rounded-md border bg-background px-2" value={params.get("status")??""} onChange={event=>set("status",event.target.value)}><option value="">전체</option>{Object.entries(coverageStates).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>}
    {dates&&["from","to"].map((key,index)=><label key={key} className="grid gap-1 text-sm">생성일 {index?"종료":"시작"} · KST<Input className="h-11" type="date" value={params.get(key)??""} onChange={event=>set(key,event.target.value)}/></label>)}
    <label className="grid gap-1 text-sm">이름 검색<Input className="h-11" value={params.get("q")??""} onChange={event=>set("q",event.target.value)}/></label>
  </div>
}

export function useMeasurementCoverage(namespace:string,enabled=true) {
  const {token}=useSession(),params=useSearchParams()
  const query=new URLSearchParams()
  for(const key of ["batches","node"]) {const value=params.get(key);if(value)query.set(key,value)}
  const path=`v1/namespaces/${encodeURIComponent(namespace)}/measurement-coverage${query.size?`?${query}`:""}`
  const result=useQuery<MeasurementCoverageResponse,ApiError>({queryKey:["chill",path],enabled,retry:false,refetchOnMount:"always",queryFn:async({signal})=>{
    let response=await fetch(`/api/chill/${path}`,{headers:{Authorization:`Bearer ${token}`},signal,cache:"no-store",credentials:"omit"})
    if(response.status===503) {
      const error=await response.clone().json().catch(()=>null)
      const delay=Number(response.headers.get("Retry-After"))
      if(error?.code==="CoverageWarming"&&Number.isFinite(delay)&&delay>0) {
        await new Promise<void>((resolve,reject)=>{const timer=setTimeout(resolve,delay*1000);signal.addEventListener("abort",()=>{clearTimeout(timer);reject(signal.reason)},{once:true})})
        response=await fetch(`/api/chill/${path}`,{headers:{Authorization:`Bearer ${token}`},signal,cache:"no-store",credentials:"omit"})
      }
    }
    const text=await response.text();let data:unknown;try{data=JSON.parse(text)}catch{data=null}
    if(!response.ok)throw new ApiError(response.status,text,data)
    return parseSchedulerJSON(text) as MeasurementCoverageResponse
  }})
  return {...result,data:result.error?undefined:result.data}
}
function subscribeWide(notify:()=>void){const media=window.matchMedia("(min-width: 768px)");media.addEventListener("change",notify);return()=>media.removeEventListener("change",notify)}
const count=(value:number|null|undefined)=>value??"미확인"
export function CoverageSummary({data}:{data:MeasurementCoverageResponse}) {
  const s=data.summary
  return <section className="coverage-summary" aria-label="측정 현황"><strong>사용 가능 {count(s.completeCellCount)} / 지원 확인 {count(s.supportedCellCount)}</strong><span>기준 충족 {count(s.criteriaMetCellCount)}</span><span>재사용 {count(s.reusedCellCount)}</span><span>확인 불가 {count(s.countsByDisplayStatus.unknown)}</span><span>지원 미확인 {count(s.unknownSupportCount)}</span><p>기준 시각 {data.dataAsOf?new Date(data.dataAsOf).toLocaleString("ko-KR"):"미확인"} ({data.freshness==="fresh"?"최신":"이전 자료"}, {data.completeness==="complete"?"전체 확인":"일부 확인"})</p></section>
}
export function CoverageError({error,retry}:{error:ApiError|null;retry:()=>void}) {
  return <section role={error?"alert":"status"} className="coverage-message"><p>{error?`커버리지 조회 실패 (HTTP ${error.status})로 운영 가능 범위와 다음 조치를 확인할 수 없습니다.`:"커버리지 확인 중"}</p>{error&&<><p>{error.status===404?"서버에 커버리지 기능이 배포되지 않았거나 경로가 다릅니다. 클러스터 관리자에게 API 배포 상태를 확인하세요.":"접속 권한과 서버 상태를 확인한 뒤 다시 조회하세요."}</p><details><summary>오류 상세</summary><pre className="whitespace-pre-wrap break-all">{error.body}</pre></details><Button variant="outline" onClick={retry}>다시 조회</Button><Link href="/measurements/requests">측정 요청 목록 확인</Link></>}</section>
}
export function MeasurementCoveragePage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const query=useMeasurementCoverage(namespace),data=query.data,trigger=useRef<HTMLButtonElement|null>(null)
  const wide=useSyncExternalStore(subscribeWide,()=>window.matchMedia("(min-width: 768px)").matches,()=>true)
  const selected=data?.cells.find(c=>c.id===params.get("cell"))
  const group=data?.groups.find(g=>g.cellIds.includes(selected?.id??"")||`${g.model.uid}/${g.deviceClass.uid}`===params.get("group"))
  function update(key:string,value:string){const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);if(key==="group"){next.delete("cell");next.delete("variant")}router.replace(`?${next}`,{scroll:false})}
  function close(){router.replace(`?${coverageSelection(params.toString(),selected,false)}`,{scroll:false});trigger.current?.focus()}
  function select(cell:MeasurementCoverageCell,button:HTMLButtonElement){trigger.current=button;router.replace(`?${coverageSelection(params.toString(),cell,true)}`,{scroll:false})}
  const cells=(data?.cells??[]).filter(c=>(params.get("unsupported")==="1"||(c.support.verdict!=="Incompatible"&&c.assessment.displayStatus!=="unsupported"))&&(!params.get("model")||c.model.name===params.get("model"))&&(!params.get("deviceClass")||c.deviceClass.name===params.get("deviceClass"))&&(!params.get("status")||c.assessment.displayStatus===params.get("status"))&&(!params.get("path")||c.executionPath===params.get("path"))&&(!params.get("powerMode")||c.powerMode===params.get("powerMode"))&&(!params.get("cpu")||c.variants.some(v=>String((v.condition.cpu as {limit?:unknown}|undefined)?.limit??"")===params.get("cpu")))&&(!params.get("q")||`${c.model.name} ${c.deviceClass.name} ${c.executionPath} ${c.powerMode}`.toLowerCase().includes(params.get("q")!.toLowerCase())))
  const groups=(data?.groups??[]).filter(g=>g.cellIds.some(id=>cells.some(c=>c.id===id)))
  const classes=[...new Map((data?.groups??[]).filter(g=>!params.get("deviceClass")||g.deviceClass.name===params.get("deviceClass")).map(g=>[g.deviceClass.uid,g.deviceClass])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const models=[...new Map(groups.map(g=>[g.model.uid,g.model])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const mobileClass=classes.some(c=>c.uid===params.get("mobileClass"))?params.get("mobileClass"):classes[0]?.uid
  const detailCells=cells.filter(c=>group?.cellIds.includes(c.id))
  const paths=[...new Set(detailCells.map(c=>c.executionPath))].sort()
  const modes=[...new Set(detailCells.map(c=>c.powerMode))].sort()
  const mobilePath=paths.includes(params.get("detailPath")??"")?params.get("detailPath"):paths[0]
  const batchValues=[...new Set([1,2,4,8,16,32,...(data?.scope.targetBatches??[])])].sort((a,b)=>a-b)
  const targets=params.get("batches")?.split(",").map(Number)??[]
  return <Screen title="측정 커버리지" lede="스케줄러가 사용할 수 있는 조건과 부족한 배치를 확인하세요." width="data" actionPlacement="header" action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="coverage" namespace={namespace}/>
    <fieldset className="coverage-targets"><legend>목표 배치</legend><Button variant={!targets.length?"default":"outline"} onClick={()=>update("batches","")}>요청 범위</Button>{batchValues.map(batch=><Button key={batch} variant={targets.includes(batch)?"default":"outline"} aria-pressed={targets.includes(batch)} onClick={()=>update("batches",(targets.includes(batch)?targets.filter(v=>v!==batch):[...targets,batch]).sort((a,b)=>a-b).join(","))}>{batch}</Button>)}</fieldset>
    {!data?<CoverageError error={query.error} retry={()=>void query.refetch()}/>:<>
      <CoverageSummary data={data}/>
      <div className="coverage-legend">{Object.entries(coverageStates).map(([status,label])=><button key={status} aria-pressed={params.get("status")===status} onClick={()=>update("status",params.get("status")===status?"":status)}>{label} {count(data.summary.countsByDisplayStatus[status as keyof typeof data.summary.countsByDisplayStatus])}</button>)}<span>지원 안 함 {count(data.summary.unsupportedCellCount)}</span></div>
      <label className="flex min-h-11 items-center gap-2 text-sm"><input type="checkbox" checked={params.get("unsupported")==="1"} onChange={e=>update("unsupported",e.target.checked?"1":"")}/>지원 안 함도 표시</label>
      <details><summary>표시 필터</summary><MeasurementFilters axes={[{key:"model",label:"모델",values:data.cells.map(c=>c.model.name)},{key:"deviceClass",label:"장치",values:data.cells.map(c=>c.deviceClass.name)},{key:"path",label:"실행 경로",values:data.cells.map(c=>c.executionPath)},{key:"powerMode",label:"전력모드",values:data.cells.map(c=>c.powerMode)}]} states={false}/><div className="grid gap-3 sm:grid-cols-2"><label>Node<Input value={params.get("node")??""} onChange={e=>update("node",e.target.value)}/></label><label>CPU quota<Input value={params.get("cpu")??""} onChange={e=>update("cpu",e.target.value)}/></label></div></details>
      <label className="grid gap-1 text-sm md:hidden">표시할 장치<select className="min-h-11 rounded-md border bg-background px-2" value={mobileClass??""} onChange={e=>update("mobileClass",e.target.value)}>{classes.map(c=><option key={c.uid} value={c.uid}>{c.name}</option>)}</select></label>
      <div className="coverage-table-wrap"><table className="coverage-table coverage-matrix"><caption>모델 × 장치: 사용 가능 / 지원 확인 조합</caption><thead><tr><th scope="col">모델</th>{classes.map(c=><th scope="col" key={c.uid} className={c.uid===mobileClass?"":"hidden md:table-cell"}>{c.name}</th>)}</tr></thead><tbody>{models.map(m=><tr key={m.uid}><th scope="row">{m.name}</th>{classes.map(c=>{const g=groups.find(g=>g.model.uid===m.uid&&g.deviceClass.uid===c.uid);return <td key={c.uid} className={c.uid===mobileClass?"":"hidden md:table-cell"}>{g?<button onClick={()=>update("group",`${m.uid}/${c.uid}`)}>사용 가능 {count(g.complete)} / 지원 확인 {count(g.supported)}{g.unknownSupport?` (미확인 ${g.unknownSupport})`:""}</button>:"조건 없음"}</td>})}</tr>)}</tbody></table></div>
      {group&&<section className="coverage-detail"><h2>{group.model.name} / {group.deviceClass.name}</h2><label className="grid gap-1 text-sm md:hidden">표시할 실행 경로<select value={mobilePath??""} onChange={e=>update("detailPath",e.target.value)}>{paths.map(path=><option key={path}>{path}</option>)}</select></label><div className="coverage-table-wrap"><table className="coverage-table coverage-matrix"><caption>전력모드 × 실행 경로: 상태와 측정 / 목표 배치</caption><thead><tr><th scope="col">전력모드</th>{paths.map(path=><th key={path} scope="col" className={path===mobilePath?"":"hidden md:table-cell"}>{path}</th>)}</tr></thead><tbody>{modes.map(mode=><tr key={mode}><th scope="row">{powerModeLabel(mode,detailCells.filter(c=>c.powerMode===mode).flatMap(c=>c.variants.flatMap(v=>v.resultRows.flatMap(r=>r.activeWatts===null?[]:[r.activeWatts]))))}</th>{paths.map(path=>{const c=detailCells.find(c=>c.powerMode===mode&&c.executionPath===path);return <td key={path} className={path===mobilePath?"":"hidden md:table-cell"}>{c?<button aria-label={`${mode} ${path} ${c.assessment.label} 상세`} onClick={e=>select(c,e.currentTarget)}><MeasurementStatus assessment={c.assessment}/><span className="block">{c.variantCount>1?`정확 조건 ${c.variantCount}개 · 열어서 확인`:c.batchProgress?`측정 ${c.batchProgress.measured.join("·")||"없음"} / 목표 ${c.batchProgress.target.join("·")||"미지정"}`:"배치 정보 없음"}</span>{c.assessment.displayStatus==="problem"&&<span className="block text-fail">{c.assessment.reasons[0]?.message??c.failureSummary[0]?.message??"원인 상세를 확인하세요"}</span>}</button>:"조건 없음"}</td>})}</tr>)}</tbody></table></div></section>}
      {selected&&wide&&<CoverageDetail cell={selected} namespace={namespace} close={close}/>}
      <section className="coverage-detail"><h2>다음 조치</h2>{data.nextActions.length?[...data.nextActions].sort((a,b)=>Number(b.kind==="failure")-Number(a.kind==="failure")).map((a,i)=><p key={i}><Link href={`?${new URLSearchParams({...Object.fromEntries(params),cell:a.cellId,...(a.variantId?{variant:a.variantId}:{})})}`}>{a.message}</Link></p>):<p>확인된 추가 조치가 없습니다. 확인 불가 조건과 조회 범위도 확인하세요.</p>}</section>
      {data.sources.filter(s=>s.error).map(s=><p role="alert" key={s.kind}>{s.kind}: {s.error?.message} ({s.error?.code})</p>)}
      <details><summary>상태와 조회 범위</summary><p>사용 가능은 채택된 정확 근거와 목표 배치를 충족한 조건입니다. 기준 충족은 결과 체크리스트까지 충족한 부분집합입니다. 재사용은 별도 확인하며 현재 스케줄러 사용 여부는 조건 상세에 표시합니다.</p><p>실패 조건 {count(data.summary.failedCellCount)}, 현재 실패 요청 {count(data.summary.currentFailedRequestCount)}, 과거 실패 요청 {count(data.summary.historicalFailedRequestCount)}. 지원 범위: {data.scope.supportBasis}, namespace {namespace}.</p></details>
    </>}
    <Dialog open={!!selected&&!wide} onOpenChange={open=>{if(!open)close()}}><DialogContent className="max-h-[90dvh] overflow-y-auto" onCloseAutoFocus={event=>{event.preventDefault();trigger.current?.focus()}}><DialogHeader><DialogTitle>선택 조건</DialogTitle><DialogDescription>조건별 결과와 부족 배치를 확인합니다.</DialogDescription></DialogHeader>{selected&&<CoverageDetail cell={selected} namespace={namespace} close={close}/>}</DialogContent></Dialog>
  </Screen>
}
function CoverageDetail({cell,namespace,close}:{cell:MeasurementCoverageCell;namespace:string;close:()=>void}) {
  const params=useSearchParams(),router=useRouter()
  const variant=cell.variants.find(v=>v.id===params.get("variant"))??(cell.variantCount===1?cell.variants[0]:undefined)
  function choose(id:string){const next=new URLSearchParams(params.toString());next.set("variant",id);router.replace(`?${next}`,{scroll:false})}
  const request=variant?`/measurements/new?${new URLSearchParams({namespace,model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,variantId:variant.id,returnTo:`/measurements?${params}`,...(cell.support.node?{node:cell.support.node.name}:{}),batches:variant.batchProgress.missing.join(",")})}`:null
  return <section className="coverage-detail" aria-label="선택 조건 상세"><div className="flex items-center justify-between gap-3"><h2>{cell.model.name} / {cell.deviceClass.name} / {powerModeLabel(cell.powerMode,cell.variants.flatMap(v=>v.resultRows.flatMap(r=>r.activeWatts===null?[]:[r.activeWatts])))} / {cell.executionPath}</h2><Button variant="outline" onClick={close}>닫기</Button></div><MeasurementStatus assessment={cell.assessment}/>{cell.assessment.reasons.map((r,i)=><p key={i}>{r.message} ({r.code})</p>)}{cell.variantCount>1&&<label>조건 {cell.variantCount}개<select value={variant?.id??""} onChange={e=>choose(e.target.value)}><option value="" disabled>하나의 정확 조건을 선택하세요</option>{cell.variants.map((v,i)=><option key={v.id} value={v.id}>조건 {i+1}: {JSON.stringify(v.condition)}</option>)}</select></label>}
    {variant&&cell.variantCount>1&&<div className="flex flex-wrap gap-2"><Button variant="outline" disabled={cell.variants.indexOf(variant)===0} onClick={()=>choose(cell.variants[cell.variants.indexOf(variant)-1].id)}>이전 조건</Button><Button variant="outline" disabled={cell.variants.indexOf(variant)===cell.variants.length-1} onClick={()=>choose(cell.variants[cell.variants.indexOf(variant)+1].id)}>다음 조건</Button></div>}
    {variant&&<VariantDetail variant={variant}/>}
    {cell.failureSummary.map((f,i)=><p key={i} className="text-fail">{f.message} ({f.code}) {f.requestId} {f.runId}</p>)}
    <p>실행 지원: {cell.support.verdict==="Compatible"?"확인":cell.support.verdict==="Incompatible"?"지원 안 함":"확인 불가"}. {cell.support.reasons.map(r=>`${r.message} (${r.code})`).join(" / ")}</p>
    <details><summary>시도 이력과 원본 상태</summary><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify({history:cell.history,execution:cell.assessment.execution,evidence:cell.assessment.evidence,failureSummary:cell.failureSummary},null,2)}</pre></details>
    {request&&<Button asChild><Link href={request}>요청 범위 확인</Link></Button>}
  </section>
}
function VariantDetail({variant}:{variant:MeasurementVariant}) {
  const p=variant.batchProgress,a=variant.assessment
  return <><p>현재 스케줄러 {a.scope.schedulerUsed===true?"사용":a.scope.schedulerUsed===false?"미사용":"사용 여부 미확인"}{a.scope.reused?" (재사용)":""}</p><p>목표 {p.target.join(", ")||"미지정"} / 측정 {p.measured.join(", ")||"없음"} / 부족 {p.missing.join(", ")||"없음"} / 진행 {p.inProgress.join(", ")||"없음"}</p><details><summary>정확 조건</summary><pre className="whitespace-pre-wrap break-all">{JSON.stringify(variant.condition,null,2)}</pre></details><ResultRows rows={variant.resultRows}/><AssessmentDetails assessment={a}/></>
}
