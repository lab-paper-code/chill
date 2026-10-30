"use client"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, useSession } from "@/lib/client"
import { coverageSelection, coveragePathVisible, coverageWatts, coverageModes, coverageRepresentative, coverageProgress, coverageBatchState, coverageRequestHref, powerModeLabel, deviceLabel, conditionLabel, type CompatibilityRead } from "@/lib/measurement-coverage"
import type { BatchProgress, InventoryView, ModelCompatibility, MeasurementCoverageResponse, MeasurementCoverageCell, MeasurementFailure, ResultRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
const coverageStates={usable:"사용 가능",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"확인 불가"}
import { formatMeasurementNumber as number } from "@/lib/measurements"
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
const count=(value:number|null|undefined)=>value??"미확인"
export function CoverageSummary({data}:{data:MeasurementCoverageResponse}) {
  const s=data.summary
  return <section className="coverage-summary" aria-label="측정 현황"><strong className={s.completeCellCount?"text-ok":"text-muted-foreground"}>사용 가능 {count(s.completeCellCount)} / 지원 확인 {count(s.supportedCellCount)}</strong>{(["insufficient","problem","running","unmeasured","unknown"] as const).map(status=><span key={status} className={s.countsByDisplayStatus[status]?`status-${status}`:"text-muted-foreground"}>{coverageStates[status]} {count(s.countsByDisplayStatus[status])}</span>)}<p>기준 시각 {data.dataAsOf?new Date(data.dataAsOf).toLocaleString("ko-KR"):"미확인"} ({data.freshness==="fresh"?"최신":"이전 자료"}, {data.completeness==="complete"?"전체 확인":"일부 확인"})</p></section>
}
export function CoverageError({error,retry}:{error:ApiError|null;retry:()=>void}) {
  return <section role={error?"alert":"status"} className="coverage-message"><p>{error?error.status===503?"서버가 커버리지를 아직 준비하지 못했습니다. 잠시 후 다시 조회하세요.":`커버리지 조회 실패 (HTTP ${error.status})로 운영 가능 범위와 다음 조치를 확인할 수 없습니다.`:"서버에서 커버리지를 준비 중입니다. 준비되지 않으면 자동으로 다시 조회합니다."}</p>{error&&<><p>{error.status===404?"서버의 커버리지 기능 배포 상태를 관리자에게 확인하세요.":"측정 요청 목록은 별도로 확인할 수 있습니다."}</p><details><summary>오류 코드·원문</summary><pre className="whitespace-pre-wrap break-all">{error.body}</pre></details><Button variant="outline" onClick={retry}>다시 조회</Button><Link href="/measurements/requests">측정 요청 목록 확인</Link></>}</section>
}
export function MeasurementCoveragePage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const query=useMeasurementCoverage(namespace),data=query.data,trigger=useRef<HTMLButtonElement|null>(null)
  const inventory=useCoverageInventory()
  const selected=data?.cells.find(cell=>cell.id===params.get("cell"))
  const models=[...new Map((data?.cells??[]).map(cell=>[cell.model.uid,cell.model])).values()].sort((a,b)=>a.name.localeCompare(b.name))
  const model=params.get("model")??selected?.model.name??models[0]?.name
  function update(key:string,value:string) {const next=new URLSearchParams(params.toString());if(value)next.set(key,value);else next.delete(key);router.replace(`?${next}`,{scroll:false})}
  function close(){router.replace(`?${coverageSelection(params.toString(),selected,false)}`,{scroll:false});trigger.current?.focus()}
  function select(cell:MeasurementCoverageCell,button:HTMLButtonElement){trigger.current=button;const next=coverageSelection(params.toString(),cell,true);next.set("model",cell.model.name);router.replace(`?${next}`,{scroll:false})}
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
  return <Screen title="측정 커버리지" lede="모델별 측정 현황과 부족 배치를 확인하세요." width="data" actionPlacement="header" action={<Button variant="outline" disabled={query.isFetching} onClick={()=>void query.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="coverage" namespace={namespace}/>
    {!data?<CoverageError error={query.isFetching?null:query.error} retry={()=>void query.refetch()}/>:<>
      <CoverageSummary data={data}/>
      <nav className="coverage-models" aria-label="모델 선택">{models.map(item=><button key={item.uid} aria-current={item.name===model?"page":undefined} onClick={()=>update("model",item.name)}>{item.name}</button>)}</nav>
      <label className="coverage-mobile-model">모델<select value={model??""} onChange={event=>update("model",event.target.value)}>{models.map(item=><option key={item.uid} value={item.name}>{item.name}</option>)}</select></label>
      <div className="coverage-toolbar">
        <fieldset className="coverage-targets"><legend>목표 배치</legend><Button variant={!targets.length?"default":"outline"} onClick={()=>update("batches","")}>요청 범위</Button>{batches.map(batch=><Button key={batch} variant={targets.includes(batch)?"default":"outline"} aria-pressed={targets.includes(batch)} onClick={()=>update("batches",(targets.includes(batch)?targets.filter(value=>value!==batch):[...targets,batch]).sort((a,b)=>a-b).join(","))}>{batch}</Button>)}</fieldset>
        <details><summary>표시 필터</summary><MeasurementFilters axes={[{key:"deviceClass",label:"장치",values:data.cells.map(cell=>cell.deviceClass.name)},{key:"path",label:"실행 경로",values:data.cells.filter(cell=>cell.model.name===model&&coveragePathVisible(cell,inventory.data)).map(cell=>cell.executionPath)},{key:"powerMode",label:"전력모드",values:data.cells.map(cell=>cell.powerMode)}]}/><div className="grid gap-3 sm:grid-cols-2"><label>Node<Input value={params.get("node")??""} onChange={event=>update("node",event.target.value)}/></label><label>CPU quota<Input value={params.get("cpu")??""} onChange={event=>update("cpu",event.target.value)}/></label></div></details>
      </div>
      <p className="coverage-key">배치 칸: ■ 기준 충족 / ▧ 측정됨 / ◷ 진행 / □ 부족 / ? 미확인 / — 목표 밖. 아키텍처 불가만 숨기며 실행 지원 미확인은 흐린 칸으로 남깁니다.</p>
      <div className="coverage-workspace">
        <div className="coverage-pivot" aria-label={`${model??""} 장치별 커버리지`}>
          {!classes.length&&<p className="p-4">현재 필터에 맞는 조건이 없습니다. 선택한 조건 상세는 유지됩니다.</p>}
          {classes.map(cls=>{
            const deviceCells=cells.filter(cell=>cell.deviceClass.uid===cls.uid),paths=[...new Set(deviceCells.map(cell=>cell.executionPath))].sort(),modes=coverageModes(deviceCells)
            return <section key={cls.uid} className="coverage-device"><h2>{deviceLabel(cls.name)} <small>{deviceCells.length}개 조건</small></h2>
              <div className="coverage-desktop-table"><table><caption className="sr-only">{deviceLabel(cls.name)} 전력모드별 실행 경로</caption><thead><tr><th scope="col">전력모드<br/><small>실측 W 낮은 순</small></th>{paths.map(path=><th key={path} scope="col">{path.replace(/^firstfloor-/,"")}</th>)}</tr></thead><tbody>{modes.map(mode=><tr key={mode}><th scope="row">{powerModeLabel(mode,coverageWatts(deviceCells.filter(cell=>cell.powerMode===mode)))}</th>{paths.map(path=>{const cell=deviceCells.find(cell=>cell.powerMode===mode&&cell.executionPath===path);return <td key={path}>{cell?<CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/>:<span>조건 없음</span>}</td>})}</tr>)}</tbody></table></div>
              <div className="coverage-mobile-cells">{modes.map(mode=><section key={mode}><h3>{powerModeLabel(mode,coverageWatts(deviceCells.filter(cell=>cell.powerMode===mode)))}</h3>{deviceCells.filter(cell=>cell.powerMode===mode).map(cell=><div key={cell.id}><h4>{cell.executionPath.replace(/^firstfloor-/,"")}</h4><CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/></div>)}</section>)}</div>
            </section>
          })}
        </div>
        <aside className="coverage-inspector" aria-label="선택 조건 상세">{selected?<CoverageDetail cell={selected} namespace={namespace} close={close} visible={cells.some(cell=>cell.id===selected.id)}/>:<div className="coverage-inspector-empty"><h2>조건 상세</h2><p>왼쪽에서 칸을 선택하세요.</p><p>신뢰 근거, 배치별 결과와 실패 회차를 확인하고 같은 조건으로 측정을 요청할 수 있습니다.</p></div>}</aside>
      </div>
      <section className="coverage-detail"><h2>다음 조치</h2><CoverageActions data={data} namespace={namespace}/></section>
      {data.sources.filter(source=>source.error).map(source=><p role="alert" key={source.kind}>{source.kind}: 조회하지 못한 자료가 있습니다 ({source.error?.code})</p>)}
      <details><summary>상태 용어와 조회 범위</summary><p>사용 가능: 채택 근거와 목표 배치 충족. 부족: 목표 배치 누락. 문제: 실패 또는 근거 충돌. 진행: 실행 중. 미측정: 측정 이력 없음. 확인 불가: 선언이나 근거 확인 필요.</p><p>색이 흐린 칸도 측정 이력을 확인할 수 있습니다. 서로 다른 정확 조건의 배치를 합쳐 완료로 표시하지 않습니다. 복수 조건의 막대는 대표 조건 기준입니다. namespace {namespace}.</p></details>
    </>}
  </Screen>
}
const batchSymbols={complete:"■",measured:"▧",running:"◷",missing:"□",unknown:"?",outside:"—"}
function BatchBar({progress}:{progress:BatchProgress|null}) {
  const batches=[...new Set([1,2,4,8,16,32,...(progress?.target??[])])].sort((a,b)=>a-b)
  return <span className="coverage-batch-bar" aria-label="배치별 측정 현황">{batches.map(batch=>{const state=coverageBatchState(progress,batch),label=`배치 ${batch}: ${{complete:"기준 충족",measured:"측정됨",running:"진행",missing:"부족",unknown:"미확인",outside:"목표 밖"}[state]}`;return <span key={batch} className={`batch-${state}`} title={label} aria-label={label}><b aria-hidden="true">{batchSymbols[state]}</b>{batch}</span>})}</span>
}
function CoverageCell({cell,selected,select}:{cell:MeasurementCoverageCell;selected:boolean;select:(cell:MeasurementCoverageCell,button:HTMLButtonElement)=>void}) {
  const representative=coverageRepresentative(cell)
  return <button className={`coverage-cell ${cell.support.verdict!=="Compatible"?"coverage-cell-unconfirmed":""}`} aria-pressed={selected} aria-controls="coverage-selected-condition" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} ${cell.assessment.label} 상세`} onClick={event=>select(cell,event.currentTarget)}>
    <MeasurementStatus assessment={cell.assessment}/><BatchBar progress={coverageProgress(cell)}/>
    <span className="coverage-representative">{representative?`배치 ${representative.batch} p95 ${number(representative.p95Milliseconds,"ms")} ms / 추가 ${number(representative.incrementalJoulesPerItem,"J/건")} J/건`:"대표값 없음"}</span>
    {cell.variantCount>1&&<small>정확 조건 {cell.variantCount}개 / 대표 조건 막대</small>}
    {cell.failureSummary.length>0&&<strong className="text-fail">{cell.failureSummary[0].label??"기타"}{cell.failureSummary.length>1?` (${cell.failureSummary.length}회)`:""}</strong>}
  </button>
}
function failureHref(failure:MeasurementFailure,namespace:string,returnTo:string) {
  return `/measurements/profiling-runs/${encodeURIComponent(failure.runNamespace||namespace)}/${encodeURIComponent(failure.runName)}?${new URLSearchParams({uid:failure.runId.split("/").at(-1)??"",returnTo})}`
}
export function CoverageActions({data,namespace}:{data:MeasurementCoverageResponse;namespace:string}) {
  const seen=new Set<string>()
  const actions=[...data.nextActions].sort((a,b)=>Number(b.kind==="failure")-Number(a.kind==="failure")).filter(action=>{const key=`${action.cellId}/${action.kind}/${action.reasonCode}`;if(seen.has(key))return false;seen.add(key);return true})
  return actions.length?<ul className="coverage-actions">{actions.map(action=>{
    const cell=data.cells.find(cell=>cell.id===action.cellId)
    if(!cell)return null
    const failure=action.kind==="failure"?cell.failureSummary.find(failure=>failure.requestId===action.entityId)??cell.failureSummary[0]:undefined
    const selection=new URLSearchParams({namespace,model:cell.model.name,cell:cell.id,...(action.variantId?{variant:action.variantId}:{})})
    return <li key={`${cell.id}/${action.kind}/${action.reasonCode}`}><MeasurementStatus assessment={cell.assessment}/><div><Link href={failure?.runName?failureHref(failure,namespace,`/measurements?${selection}`):`/measurements?${selection}`}>{cell.model.name} / {deviceLabel(cell.deviceClass.name)} / {cell.powerMode} / {cell.executionPath.replace(/^firstfloor-/,"")}</Link><p>{failure?`${failure.label??"기타"} — 실패 회차 확인`:action.kind==="missing-batches"?"부족한 배치를 측정 요청으로 채우세요.":"조건 상세에서 필요한 근거를 확인하세요."}</p></div></li>
  })}</ul>:<p>확인된 추가 조치가 없습니다. 확인 불가 범위도 살펴보세요.</p>
}
function TrustLine({row}:{row:ResultRow|undefined}) {
  const source=row?.powerSource
  const power=source?[source.kind,source.metric,source.nodeName].filter(value=>typeof value==="string").join(" / ")||"출처 상세 참조":"미제공"
  const uncertainty=row?.confidence?`${number(row.confidence.level*100)}% 신뢰구간 ${row.incrementalJoulesPerItem?`±${number(100*row.confidence.halfWidth/Math.abs(row.incrementalJoulesPerItem),"%")}%`:`±${number(row.confidence.halfWidth,"J/건")} J/건`}`:"오차 미제공"
  return <p className="coverage-trust">신뢰 근거: {row?`독립 반복 실행 ${number(row.activeTrialCount)}회 / 유휴 ${number(row.idleTrialCount)}회, 추가 에너지 ${uncertainty}, 전력 ${power} (장치 단독 전력 아님)`:"저장된 측정 결과 없음"}. 데이터셋 용도 일치는 확인이 필요합니다.</p>
}
function CoverageDetail({cell,namespace,close,visible}:{cell:MeasurementCoverageCell;namespace:string;close:()=>void;visible:boolean}) {
  const params=useSearchParams(),router=useRouter(),panel=useRef<HTMLDivElement>(null)
  const variant=cell.variants.find(variant=>variant.id===params.get("variant"))??cell.variants.find(variant=>variant.id===coverageRepresentative(cell)?.variantId)??cell.variants[0]
  const progress=variant?.batchProgress??coverageProgress(cell),rows=variant?.resultRows??[],returnTo=`/measurements?${params}`
  useEffect(()=>{panel.current?.focus({preventScroll:true});if(window.matchMedia("(max-width: 767px)").matches)panel.current?.scrollIntoView({block:"start"})},[cell.id])
  function choose(id:string){const next=new URLSearchParams(params.toString());next.set("variant",id);router.replace(`?${next}`,{scroll:false})}
  return <div id="coverage-selected-condition" ref={panel} tabIndex={-1} role="region" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} 상세`} className="coverage-selected">
    <header><h2>{deviceLabel(cell.deviceClass.name)}</h2><Button variant="outline" onClick={close}>닫기</Button></header>
    <p className="font-semibold break-words">{cell.model.name}</p><p>선택 경로: {powerModeLabel(cell.powerMode,coverageWatts([cell]))}<br/>{cell.executionPath}</p>
    {!visible&&<p className="text-muted-foreground">이 조건은 현재 탭·필터 밖에 있습니다. 선택은 유지됩니다.</p>}
    <MeasurementStatus assessment={cell.assessment}/><TrustLine row={rows.find(row=>row.batch===1)??rows[0]}/>
    <Button asChild><Link href={coverageRequestHref(cell,namespace,returnTo,variant?.id,params.get("batches")?.split(",").map(Number)??[])}>이 조건으로 측정 요청</Link></Button>
    <p className="text-sm">조건과 부족 배치를 채운 요청 화면을 엽니다. 자동으로 제출하지 않습니다.</p>
    {cell.variantCount>1&&<label className="grid gap-2">측정 조건 {cell.variantCount}개<select value={variant?.id??""} onChange={event=>choose(event.target.value)}>{cell.variants.map(item=><option key={item.id} value={item.id}>{conditionLabel(item.condition)} / {String(item.condition.name??item.id.slice(-8))}</option>)}</select></label>}
    {variant&&<p className="text-sm">{conditionLabel(variant.condition)}</p>}
    <BatchBar progress={progress}/><p>부족 배치: {progress?.missing.join(" · ")||"없음 또는 목표 미지정"}</p>
    {cell.failureSummary.length>0&&<section><h3>실패 회차</h3>{[...new Map(cell.failureSummary.map(failure=>[failure.runId||`${failure.requestId}/${failure.code}`,failure])).values()].map((failure,index)=><p key={index} className="coverage-failure"><strong>{failure.label??"기타"}</strong>{failure.runName&&<Link href={failureHref(failure,namespace,returnTo)}>{failure.runName} 회차 확인</Link>}<span>{failure.message}</span></p>)}</section>}
    {rows.length>0&&<section><h3>배치별 결과</h3><div className="overflow-x-auto"><table className="coverage-results"><thead><tr><th>배치</th><th>p95 ms</th><th>총 J/건</th><th>추가 J/건</th></tr></thead><tbody>{rows.map(row=><tr key={row.id}><th scope="row">{row.batch}</th><td>{number(row.p95Milliseconds,"ms")}</td><td>{number(row.totalJoulesPerItem,"J/건")}</td><td>{number(row.incrementalJoulesPerItem,"J/건")}</td></tr>)}</tbody></table></div><p className="text-sm">현재 스케줄러 {variant?.assessment.scope.schedulerUsed===true?"사용":variant?.assessment.scope.schedulerUsed===false?"미사용":"사용 여부 미확인"}</p></section>}
    <details><summary>신뢰 근거·조건·원문</summary><p>실행 지원: {cell.support.verdict==="Compatible"?"확인":cell.support.verdict==="Incompatible"?"지원 안 함":"확인 불가"}</p><ul>{(variant?.assessment??cell.assessment).checklist.checks.map(check=><li key={check.code}>{check.message} ({check.result})</li>)}</ul><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify({condition:variant?.condition,rows,support:cell.support,history:cell.history},null,2)}</pre></details>
  </div>
}
