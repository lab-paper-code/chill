"use client"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, useChillQuery, useSession } from "@/lib/client"
import { coverageFailureHref, coverageProfileQuery, coverageStatusCounts, measurementAge, coverageSelection, coveragePathVisible, coverageWatts, coverageModes, coverageVariant, coverageProgress, coverageBatchState, coverageRequestHref, powerModeLabel, deviceLabel, conditionLabel, type CompatibilityRead } from "@/lib/measurement-coverage"
import type { BatchProgress, InventoryView, ModelCompatibility, MeasurementCoverageResponse, MeasurementCoverageCell, ProfilesView, RunsView, ResultRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { MeasurementNavigation, MeasurementStatus, MeasurementEvidenceChecks, measurementStates } from "@/components/measurement-navigation"
import { measurementUsage } from "@/lib/measurement-assessment"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
const coverageStates=measurementStates
import { formatMeasurementNumber as number, formatMeasurementTime } from "@/lib/measurements"
import "./measurement-coverage.css"
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


export function MeasurementFilters({axes,states=true,dates=false,editable=false}:{axes:{key:string;label:string;values:string[]}[];states?:boolean;dates?:boolean;editable?:boolean}) {
  const params=useSearchParams(),router=useRouter()
  function set(key:string,value:string) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);next.delete("page");next.delete("offset");next.delete("profile");router.replace(`?${next}`,{scroll:false})}
  return <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">{axes.map(axis=><label key={axis.key} className="grid min-w-0 gap-1 text-sm">{axis.label}{editable?<><Input className="h-11 min-w-0" list={`measurement-${axis.key}`} placeholder="전체 · 이름 입력" value={params.get(axis.key)??""} onChange={event=>set(axis.key,event.target.value)}/><datalist id={`measurement-${axis.key}`}>{[...new Set(axis.values)].filter(Boolean).sort().map(value=><option key={value} value={value}/>)}</datalist></>:<select className="h-11 min-w-0 rounded-md border bg-background px-2" value={params.get(axis.key)??""} onChange={event=>set(axis.key,event.target.value)}><option value="">전체</option>{[...new Set([...axis.values,...(params.get(axis.key)?[params.get(axis.key)!]:[])])].filter(Boolean).sort().map(value=><option key={value} value={value}>{value}</option>)}</select>}</label>)}
    {states&&<label className="grid gap-1 text-sm">상태<select className="h-11 rounded-md border bg-background px-2" value={params.get("status")??""} onChange={event=>set("status",event.target.value)}><option value="">전체</option>{Object.entries(coverageStates).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>}
    {dates&&["from","to"].map((key,index)=><label key={key} className="grid gap-1 text-sm">생성일 {index?"종료":"시작"} · KST<Input className="h-11" type="date" value={params.get(key)??""} onChange={event=>set(key,event.target.value)}/></label>)}
    <label className="col-span-2 grid min-w-0 gap-1 text-sm lg:col-span-1">이름 검색<Input className="h-11" value={params.get("q")??""} onChange={event=>set("q",event.target.value)}/></label>
  </div>
}

export function useMeasurementCoverage(namespace:string,enabled=true) {
  const {token}=useSession(),params=useSearchParams()
  const query=new URLSearchParams()
  for(const key of ["batches","node"]) {const value=params.get(key);if(value)query.set(key,value)}
  const path=`v1/namespaces/${encodeURIComponent(namespace)}/measurement-coverage${query.size?`?${query}`:""}`
  const result=useQuery<MeasurementCoverageResponse,ApiError>({queryKey:["chill",path],enabled,retry:(attempt,error)=>error.status===503&&attempt<2,retryDelay:2000,refetchOnMount:"always",queryFn:async({signal})=>{
    const response=await fetch(`/api/chill/${path}`,{headers:{Authorization:`Bearer ${token}`},signal,cache:"no-store",credentials:"omit"})
    const text=await response.text();let data:unknown;try{data=JSON.parse(text)}catch{data=null}
    if(!response.ok)throw new ApiError(response.status,text,data)
    return parseSchedulerJSON(text) as MeasurementCoverageResponse
  }})
  return {...result,data:result.error?undefined:result.data}
}
export function CoverageSummary({data}:{data:MeasurementCoverageResponse}) {
  const inventory=useCoverageInventory()
  const visible=data.cells.filter(cell=>coveragePathVisible(cell,inventory.data)),counts=coverageStatusCounts(visible)
  return <details><summary>전체 모델 집계</summary><table className="w-full text-left text-sm tabular-nums"><thead><tr><th>측정 상태</th><th className="text-right">조합 (개)</th></tr></thead><tbody>{Object.entries(coverageStates).map(([status,label])=><tr key={status}><th className="font-normal">{label}</th><td className="text-right">{counts[status as keyof typeof counts]}</td></tr>)}</tbody></table><p className="text-xs">커버리지 갱신: {data.dataAsOf?formatMeasurementTime(data.dataAsOf):"미확인"} · {data.freshness==="fresh"?"최신":"이전 자료"}</p></details>
}
export function CoverageError({error,retry}:{error:ApiError|null;retry:()=>void}) {
  return <section role={error?"alert":"status"} className="coverage-message"><p>{error?error.status===503?"서버가 커버리지를 아직 준비하지 못했습니다. 잠시 후 다시 조회하세요.":`커버리지 조회 실패 (HTTP ${error.status})로 운영 가능 범위와 다음 조치를 확인할 수 없습니다.`:"서버에서 커버리지를 준비 중입니다. 준비되지 않으면 자동으로 다시 조회합니다."}</p>{error&&<><p>{error.status===404?"서버의 커버리지 기능 배포 상태를 관리자에게 확인하세요.":"측정 요청 목록은 별도로 확인할 수 있습니다."}</p><details><summary>오류 코드·원문</summary><pre className="whitespace-pre-wrap break-all">{error.body}</pre></details><Button variant="outline" onClick={retry}>다시 조회</Button><Link href="/measurements/requests">측정 요청 목록 확인</Link></>}</section>
}
export function MeasurementCoveragePage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const query=useMeasurementCoverage(namespace),data=query.data,trigger=useRef<HTMLButtonElement|null>(null)
  const inventory=useCoverageInventory()
  const selected=data?.cells.find(cell=>cell.id===params.get("cell"))
  const selectedID=selected?.id
  const models=[...new Map((data?.cells??[]).map(cell=>[cell.model.uid,cell.model])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const model=params.get("model")??selected?.model.name??models[0]?.name
  function update(key:string,value:string) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);router.replace(`?${next}`,{scroll:false})}
  function close(){router.replace(`?${coverageSelection(params.toString(),selected,false)}`,{scroll:false});trigger.current?.focus()}
  function select(cell:MeasurementCoverageCell,button:HTMLButtonElement){trigger.current=button;const next=coverageSelection(params.toString(),cell,true);next.set("model",cell.model.name);router.replace(`?${next}`,{scroll:false})}
  useEffect(()=>{
    if(!selectedID)return
    const buttons=document.querySelectorAll<HTMLButtonElement>(`[data-coverage-cell="${CSS.escape(selectedID)}"]`)
    const button=[...buttons].find(item=>item.getClientRects().length>0)
    button?.scrollIntoView({block:"nearest",inline:"nearest"})
  },[selectedID])
  const cells=(data?.cells??[]).filter(cell=>coveragePathVisible(cell,inventory.data)&&cell.model.name===model&&
    (!params.get("deviceClass")||cell.deviceClass.name===params.get("deviceClass"))&&
    (!params.get("status")||cell.assessment.displayStatus===params.get("status"))&&
    (!params.get("path")||cell.executionPath===params.get("path"))&&
    (!params.get("powerMode")||cell.powerMode===params.get("powerMode"))&&
    (!params.get("cpu")||cell.variants.some(variant=>String((variant.condition.cpu as {limit?:unknown}|undefined)?.limit??"")===params.get("cpu")))&&
    (!params.get("q")||`${cell.deviceClass.name} ${cell.executionPath} ${cell.powerMode}`.toLowerCase().includes(params.get("q")!.toLowerCase())))
  const classes=[...new Map(cells.map(cell=>[cell.deviceClass.uid,cell.deviceClass])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const batches=[...new Set([1,2,4,8,16,32,...(data?.scope.targetBatches??[])])].sort((a,b)=>a-b)
  const targets=params.get("batches")?.split(",").map(Number)??[]
  return <Screen title="측정 커버리지" width="data" actionPlacement="header" action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="coverage" namespace={namespace}/>
    {!data?<CoverageError error={query.isFetching?null:query.error} retry={()=>void query.refetch()}/>:<>
      <section className="coverage-controls" aria-label="모델·측정 범위">
      <nav className="coverage-models" aria-label="모델 선택">{models.map(item=><button key={item.uid} aria-current={item.name===model?"page":undefined} onClick={()=>update("model",item.name)}>{item.name}</button>)}</nav>
      <label className="coverage-mobile-model">모델<select value={model??""} onChange={event=>update("model",event.target.value)}>{models.map(item=><option key={item.uid} value={item.name}>{item.name}</option>)}</select></label>
      <div className="coverage-toolbar">
        <label className="grid gap-1 text-sm">장치<select className="h-11 rounded-md border bg-background px-2" value={params.get("deviceClass")??""} onChange={event=>update("deviceClass",event.target.value)}><option value="">전체 장치</option>{[...new Set(data.cells.filter(cell=>cell.model.name===model&&coveragePathVisible(cell,inventory.data)).map(cell=>cell.deviceClass.name))].sort().map(name=><option key={name} value={name}>{deviceLabel(name)}</option>)}</select></label>
        <details><summary className="min-h-11 cursor-pointer py-2">목표 배치·세부 필터</summary>
          <fieldset className="coverage-targets"><legend>목표 배치</legend><Button variant={!targets.length?"default":"outline"} onClick={()=>update("batches","")}>요청 범위</Button>{batches.map(batch=><Button key={batch} variant={targets.includes(batch)?"default":"outline"} aria-pressed={targets.includes(batch)} onClick={()=>update("batches",(targets.includes(batch)?targets.filter(value=>value!==batch):[...targets,batch]).sort((a,b)=>a-b).join(","))}>{batch}</Button>)}</fieldset>
          <MeasurementFilters axes={[{key:"path",label:"실행 경로",values:data.cells.filter(cell=>cell.model.name===model&&coveragePathVisible(cell,inventory.data)).map(cell=>cell.executionPath)},{key:"powerMode",label:"전력모드",values:data.cells.map(cell=>cell.powerMode)}]}/><div className="grid gap-3 sm:grid-cols-2"><label>Node<Input value={params.get("node")??""} onChange={event=>update("node",event.target.value)}/></label><label>CPU quota<Input value={params.get("cpu")??""} onChange={event=>update("cpu",event.target.value)}/></label></div>
        </details>
      </div>
      <CoverageCounts cells={cells}/>
      </section>
      <div className="coverage-workspace">
        <div className="coverage-pivot" aria-label={`${model??""} 장치별 커버리지`}>
          {!classes.length&&<p className="p-4">조건 없음 · 필터 변경</p>}
          <div className="coverage-desktop-table"><table><caption className="sr-only">장치·전력모드별 실행 경로</caption><thead><tr><th scope="col">장치 · 전력모드</th>{[...new Set(cells.map(cell=>cell.executionPath))].sort().map(path=><th key={path} scope="col">{path.replace(/^firstfloor-/,"")}</th>)}</tr></thead><tbody>
          {classes.flatMap(cls=>{
            const deviceCells=cells.filter(cell=>cell.deviceClass.uid===cls.uid),paths=[...new Set(cells.map(cell=>cell.executionPath))].sort()
            return coverageModes(deviceCells).map((mode,index)=><tr key={`${cls.uid}/${mode}`}><th scope="row">{index===0&&<><span>{deviceLabel(cls.name)}</span><small>{inventory.data?.classes.find(item=>item.uid===cls.uid)?.nodes.map(node=>node.name).join(", ")||"노드 미확인"}</small></>}<span>{mode}</span></th>{paths.map(path=>{const cell=deviceCells.find(cell=>cell.powerMode===mode&&cell.executionPath===path);return <td key={path}>{cell?<CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/>:<span>—</span>}</td>})}</tr>)
          })}
          </tbody></table></div>
        </div>
        <aside className="coverage-inspector" aria-label="선택 조건 상세">{selected?<CoverageDetail cell={selected} namespace={namespace} close={close} visible={cells.some(cell=>cell.id===selected.id)}/>:<div className="coverage-inspector-empty"><h2>조건 상세</h2><p>조합 선택</p></div>}</aside>
      </div>
      <section aria-label="조치·범례" className="grid gap-2"><details className="coverage-detail"><summary>다음 조치</summary><CoverageActions data={data} namespace={namespace}/></details>
      {data.sources.filter(source=>source.error).map(source=><p role="alert" key={source.kind}>{source.kind}: 조회하지 못한 자료가 있습니다 ({source.error?.code})</p>)}
      <details><summary className="min-h-11 cursor-pointer py-2">범례·지원 불가</summary><p>■ 충족 / ▧ 측정 / ◷ 진행 / □ 부족 / ? 미확인 / — 목표 밖</p><p>● 스케줄러 사용 / ○ 미사용 / ? 사용 미확인</p><UnsupportedReasons cells={data.cells.filter(cell=>cell.model.name===model&&!coveragePathVisible(cell,inventory.data))}/><CoverageSummary data={data}/></details>
      </section>
    </>}
  </Screen>
}
function CoverageCounts({cells}:{cells:MeasurementCoverageCell[]}) {
  const counts=coverageStatusCounts(cells),complete=cells.filter(cell=>{const progress=coverageProgress(cell);return !!progress?.target.length&&progress.target.every(batch=>progress.criteriaMet.includes(batch))}).length
  return <table className="w-full text-left text-sm tabular-nums" aria-label="선택 범위 집계"><thead><tr><th>기준 충족 / 전체 (조합)</th><th>문제 (조합)</th><th>미측정 (조합)</th></tr></thead><tbody><tr><td>{complete}/{cells.length}</td><td>{counts.problem}</td><td>{counts.unmeasured}</td></tr></tbody></table>
}
function UnsupportedReasons({cells}:{cells:MeasurementCoverageCell[]}) {
  const groups=new Map<string,{label:string;count:number}>()
  for(const cell of cells)for(const reason of cell.support.reasons.length?cell.support.reasons:[{code:"DeclarationMismatch",message:"장치·실행 경로 불일치"}]){
    const label=reason.code==="ArtifactArchitectureMismatch"?"모델·장치 아키텍처 불일치":reason.label||reason.message
    const group=groups.get(reason.code)
    if(group)group.count++;else groups.set(reason.code,{label,count:1})
  }
  return <table className="w-full text-left text-sm tabular-nums"><thead><tr><th>지원 불가 사유</th><th>조합 (개)</th></tr></thead><tbody>{[...groups].map(([key,group])=><tr key={key}><th className="font-normal">{group.label}</th><td>{group.count}</td></tr>)}</tbody></table>
}
const batchSymbols={complete:"■",measured:"▧",running:"◷",missing:"□",unknown:"?",outside:"—"}
function BatchBar({progress}:{progress:BatchProgress|null}) {
  const batches=[...new Set([1,2,4,8,16,32,...(progress?.target??[])])].sort((a,b)=>a-b)
  return <span className="coverage-batch-bar" aria-label="배치별 측정 현황">{batches.map(batch=>{const state=coverageBatchState(progress,batch),label=`배치 ${batch}: ${{complete:"기준 충족",measured:"측정됨",running:"진행",missing:"부족",unknown:"미확인",outside:"목표 밖"}[state]}`;return <span key={batch} className={`batch-${state}`} title={label} aria-label={label}><b aria-hidden="true">{batchSymbols[state]}</b>{batch}</span>})}</span>
}
function CoverageCell({cell,selected,select}:{cell:MeasurementCoverageCell;selected:boolean;select:(cell:MeasurementCoverageCell,button:HTMLButtonElement)=>void}) {
  const progress=coverageProgress(cell),used=cell.assessment.scope.schedulerUsed
  const status=cell.assessment.displayStatus
  const label=status==="usable"?(progress?.target.length&&progress.target.every(batch=>progress.criteriaMet.includes(batch))?"충족":"측정"):({review:"검토",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"미확인",unsupported:"불가"} as const)[status]
  return <button data-coverage-cell={cell.id} className={`coverage-cell ${cell.support.verdict!=="Compatible"?"coverage-cell-unconfirmed":""}`} aria-pressed={selected} aria-controls="coverage-selected-condition" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} ${label} ${measurementUsage(cell.assessment)}`} onClick={event=>select(cell,event.currentTarget)}>
    <span className="flex w-full justify-between gap-2"><strong>{label}</strong><span title={measurementUsage(cell.assessment)} aria-label={measurementUsage(cell.assessment)}>{used===true?"●":used===false?"○":"?"}</span></span>
    <BatchBar progress={progress}/>
    <span className="self-end text-xs tabular-nums" title="기준 충족 배치 / 목표 배치">{progress?`${progress.criteriaMet.length}/${progress.target.length}`:"—/—"}</span>
  </button>
}
export function CoverageActions({data,namespace}:{data:MeasurementCoverageResponse;namespace:string}) {
  const inventory=useCoverageInventory()
  const seen=new Set<string>()
  const actions=[...data.nextActions].sort((a,b)=>Number(b.kind==="failure")-Number(a.kind==="failure")).filter(action=>{const key=`${action.cellId}/${action.kind}/${action.reasonCode}`;if(seen.has(key))return false;seen.add(key);return true})
  return actions.length?<ul className="coverage-actions">{actions.map(action=>{
    const cell=data.cells.find(cell=>cell.id===action.cellId)
    if(!cell||!coveragePathVisible(cell,inventory.data))return null
    const failure=action.kind==="failure"?cell.failureSummary.find(failure=>failure.requestId===action.entityId)??cell.failureSummary[0]:undefined
    const selection=new URLSearchParams({namespace,model:cell.model.name,cell:cell.id,...(action.variantId?{variant:action.variantId}:{})})
    return <li key={`${cell.id}/${action.kind}/${action.reasonCode}`}><MeasurementStatus assessment={cell.assessment}/><div><Link href={`/measurements?${selection}`}>{cell.model.name} / {deviceLabel(cell.deviceClass.name)} / {cell.powerMode} / {cell.executionPath.replace(/^firstfloor-/,"")}</Link><p>{failure?`${failure.label??"기타"} — ${failure.runName?"실패 회차 확인":"연결된 실패 회차 없음 · 요청 단계 사유 확인"}`:action.kind==="missing-batches"?"부족한 배치를 측정 요청으로 채우세요.":action.message||"조건 상세에서 필요한 근거를 확인하세요."}</p>{failure&&<Link href={coverageFailureHref(failure,namespace,`/measurements?${selection}`)}>{failure.runName?"실패 회차 열기":"실패 요청 열기"}</Link>}</div></li>
  })}</ul>:<p>확인된 추가 조치가 없습니다. 확인 불가 범위도 살펴보세요.</p>
}
function CoverageDetail({cell,namespace,close,visible}:{cell:MeasurementCoverageCell;namespace:string;close:()=>void;visible:boolean}) {
  const params=useSearchParams(),router=useRouter(),panel=useRef<HTMLDivElement>(null),{token}=useSession()
  const variant=coverageVariant(cell,params.get("variant")??undefined)
  const progress=variant?.batchProgress??coverageProgress(cell),rows=variant?.resultRows??[],returnTo=`/measurements?${params}`
  const filters=coverageProfileQuery(cell)
  const profiles=useQuery<ProfilesView,ApiError>({queryKey:["chill","coverage-profile-times",filters.toString()],enabled:!!token&&rows.length>0,retry:false,queryFn:async({signal})=>{
    const all:NonNullable<ProfilesView["profiles"]>=[]
    let offset=0
    for(;;) {
      filters.set("offset",String(offset))
      const page=await readPage<ProfilesView>(`v1/scheduler/profiles?${filters}`,token,signal)
      all.push(...page.profiles??[])
      if(page.page?.nextOffset===undefined)return {...page,profiles:all}
      if(page.page.nextOffset<=offset)throw new ApiError(0,"프로파일 페이지가 전진하지 않습니다",null)
      offset=page.page.nextOffset
    }
  }})
  const runs=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${new URLSearchParams({model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,limit:"200",status:"problem"})}`,{enabled:cell.failureSummary.length>0})
  const profileIDs=new Set(rows.flatMap(row=>[row.profileId,row.sourceProfileId].filter(Boolean)))
  const measured=(profiles.data?.profiles??[]).filter(profile=>profileIDs.has(profile.id)).map(profile=>profile.measuredAt).filter(at=>Number.isFinite(Date.parse(at))).sort()
  const assessment=variant?.assessment??cell.assessment
  useEffect(()=>{panel.current?.focus({preventScroll:true});if(window.matchMedia("(max-width: 767px)").matches)panel.current?.scrollIntoView({block:"start"})},[cell.id])
  function choose(id:string){const next=new URLSearchParams(params.toString());next.set("variant",id);router.replace(`?${next}`,{scroll:false})}
  return <div id="coverage-selected-condition" ref={panel} tabIndex={-1} role="region" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} 상세`} className="coverage-selected">
    <header><h2>{deviceLabel(cell.deviceClass.name)}</h2><Button variant="outline" onClick={close}>닫기</Button></header>
    <p className="font-semibold break-words">{cell.model.name}</p><p>선택 경로: {powerModeLabel(cell.powerMode,coverageWatts([cell]))}<br/>{cell.executionPath}</p>
    {!visible&&<p className="text-muted-foreground">이 조건은 현재 탭·필터 밖에 있습니다. 선택은 유지됩니다.</p>}
    <details className="coverage-condition-verdict"><summary>추가 확인</summary><MeasurementEvidenceChecks assessment={assessment} rows={rows}/></details>
    <div className="text-sm">{measured.length?<p>실제 측정 <time dateTime={measured.at(-1)}>{formatMeasurementTime(measured.at(-1)!)}</time> / {measurementAge(measured.at(-1)!)}</p>:profiles.isPending&&rows.length?<p>측정 시각 조회 중</p>:profiles.error?<><p role="alert">측정 시각 조회 실패 (HTTP {profiles.error.status}) · 수치 근거는 유지합니다.</p><Button variant="outline" onClick={()=>void profiles.refetch()}>측정 시각 다시 조회</Button></>:<p>측정 시각 미기록 또는 현재 프로파일에 없음</p>}</div>
    <Button asChild><Link href={coverageRequestHref(cell,namespace,returnTo,variant?.id,params.get("batches")?.split(",").map(Number)??[])}>이 조건으로 측정 요청</Link></Button>
    {cell.variantCount>1&&<label className="grid gap-2">측정 조건<select value={variant?.id??""} onChange={event=>choose(event.target.value)}>{cell.variants.map(item=><option key={item.id} value={item.id}>{conditionLabel(item.condition)} / {String(item.condition.name??item.id.slice(-8))}</option>)}</select></label>}
    <details><summary>선택 조건·배치 범위</summary><table className="w-full text-left text-sm"><thead><tr><th>목표 배치 (건)</th><th>부족 배치 (건)</th></tr></thead><tbody><tr><td>{progress?.target.join(", ")||"미지정"}</td><td>{progress?.missing.join(", ")||"없음"}</td></tr></tbody></table>{variant&&<p className="text-xs">{conditionLabel(variant.condition)}</p>}</details>
    {cell.failureSummary.length>0&&<section><h3>실패 기록</h3>{[...new Map(cell.failureSummary.map(failure=>[failure.runId||`${failure.requestId}/${failure.code}`,failure])).values()].map((failure,index)=>{const run=runs.data?.runs.find(run=>run.uid===failure.runId.split("/").at(-1));const at=run?.status.conditions?.filter(condition=>condition.status==="False").map(condition=>condition.lastTransitionTime).filter((value):value is string=>!!value).sort().at(-1)??run?.creationTimestamp;return <div key={index} className="coverage-failure"><strong>{failure.label??"기타"}</strong><Link href={coverageFailureHref(failure,namespace,returnTo)}>{failure.runName?"실패 회차 확인":"실패 요청 사유 확인"}</Link>{!failure.runName&&<p>연결된 실패 회차가 없습니다. 요청 자체의 실패 사유를 확인하세요.</p>}<p>{at?`${formatMeasurementTime(at)} / ${measurementAge(at)}`:"실패 시각 미기록 또는 현재 조회 범위에 없음"}</p><span>{failure.message}</span></div>})}</section>}
    {rows.length>0&&<section><h3>배치별 결과</h3><div className="overflow-x-auto"><table className="coverage-results"><thead><tr><th>배치 (건)</th><th>p95 (ms)</th><th>총 (J/건)</th><th>추가 (J/건)</th></tr></thead><tbody>{rows.map(row=><tr key={row.id}><th scope="row">{row.batch}</th><td>{number(row.p95Milliseconds,"ms")}</td><td>{number(row.totalJoulesPerItem,"J/건")}</td><td>{number(row.incrementalJoulesPerItem,"J/건")}</td></tr>)}</tbody></table></div></section>}
    {rows.length>0&&<section><h3>신뢰 수치</h3><TrustValues rows={rows}/></section>}
    <details><summary>근거 확인</summary><table className="coverage-results"><thead><tr><th>항목</th><th>판정</th></tr></thead><tbody>{assessment.checklist.checks.map(check=><tr key={check.code}><th className="font-normal">{check.message}</th><td>{{pass:"확인",warn:"주의",fail:"불일치",unknown:"미확인"}[check.result]}</td></tr>)}</tbody></table></details>
    <details><summary>원문</summary><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify({condition:variant?.condition,rows,support:cell.support,history:cell.history},null,2)}</pre></details>
  </div>
}
function TrustValues({rows}:{rows:ResultRow[]}) {
  return <div className="overflow-x-auto"><table className="coverage-results"><thead><tr><th>배치 (건)</th><th>실행 반복 (회)</th><th>유휴 반복 (회)</th><th>신뢰 수준 (%)</th><th>구간 (J/건)</th><th>오차 반폭 (J/건)</th><th>상대 반폭 (%)</th><th>전력 출처</th></tr></thead><tbody>{rows.map(row=><tr key={row.id}><th scope="row">{row.batch}</th><td>{row.activeTrialCount??"미제공"}</td><td>{row.idleTrialCount??"미제공"}</td><td>{row.confidence?number(row.confidence.level*100,"%"):"미제공"}</td><td>{row.confidence?`${number(row.confidence.lower,"J/건")}–${number(row.confidence.upper,"J/건")}`:"미제공"}</td><td>{row.confidence?number(row.confidence.halfWidth,"J/건"):"미제공"}</td><td>{row.confidence&&row.incrementalJoulesPerItem?number(row.confidence.halfWidth/Math.abs(row.incrementalJoulesPerItem)*100,"%"):"미제공"}</td><td className="text-left">{row.powerSource?["kind","nodeName","meter","scope"].map(key=>row.powerSource?.[key]).filter(value=>typeof value==="string").join(" / ")||"미제공":"미제공"}</td></tr>)}</tbody></table><table className="coverage-results"><thead><tr><th>오차 허용 기준</th></tr></thead><tbody><tr><td>미설정</td></tr></tbody></table></div>
}
