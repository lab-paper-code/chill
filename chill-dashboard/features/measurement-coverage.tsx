"use client"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, useChillQuery, useSession } from "@/lib/client"
import { measurementAge, coverageSelection, coveragePathVisible, coverageWatts, coverageModes, coverageVariant, coverageRepresentative, coverageProgress, coverageBatchState, coverageRequestHref, powerModeLabel, deviceLabel, conditionLabel, type CompatibilityRead } from "@/lib/measurement-coverage"
import { requestInputs } from "@/lib/measurement-request"
import type { DatasetResource, PreprocessorResource } from "@/lib/domain"
import type { BatchProgress, InventoryView, ModelCompatibility, MeasurementCoverageResponse, MeasurementCoverageCell, MeasurementFailure, ProfilesView, RunsView, ResultRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
const coverageStates={usable:"사용 가능",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"확인 불가"}
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
const count=(value:number|null|undefined)=>value??"미확인"
export function CoverageSummary({data}:{data:MeasurementCoverageResponse}) {
  const inventory=useCoverageInventory(),s=data.summary
  const hidden=data.cells.filter(cell=>!coveragePathVisible(cell,inventory.data))
  const counts={...s.countsByDisplayStatus}
  for(const cell of hidden){const status=cell.assessment.displayStatus;if(counts[status]!==null)counts[status]=Math.max(0,counts[status]!-1)}
  return <section className="coverage-summary" aria-label="측정 현황"><strong className={s.completeCellCount?"text-ok":"text-muted-foreground"}>사용 가능 {count(s.completeCellCount)} / 지원 확인 {count(s.supportedCellCount)}</strong>{(["insufficient","problem","running","unmeasured","unknown"] as const).map(status=><span key={status} className={counts[status]?`status-${status}`:"text-muted-foreground"}>{coverageStates[status]} {count(counts[status])}</span>)}<span>— 지원 안 됨 {hidden.length} (기본 숨김)</span><p title={data.freshness==="stale"?"서버가 이 자료를 이전 자료(stale)로 표시했습니다. 최신 실행·설정과 일치하는지 갱신해 확인하세요.":undefined}>기준 시각 {data.dataAsOf?new Date(data.dataAsOf).toLocaleString("ko-KR"):"미확인"} ({data.freshness==="fresh"?"최신":"이전 자료"}, {data.completeness==="complete"?"전체 확인":"일부 확인"}). 상태별 수는 선언상 지원 불가 조건을 제외한 전체 모델 범위입니다.</p></section>
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
      <p className="coverage-key">○ 미측정 / — 지원 안 됨 (기본 숨김) / ? 확인 불가 (사유 확인). 배치: ■ 기준 충족 / ▧ 측정됨 / ◷ 진행 / □ 부족 / — 목표 밖. 측정 충족은 운영 적합성 승인이 아닙니다.</p>
      <nav className="coverage-device-index" aria-label="장치별 요약">{classes.map(cls=>{const own=cells.filter(cell=>cell.deviceClass.uid===cls.uid);return <a key={cls.uid} href={`#coverage-device-${encodeURIComponent(cls.uid)}`} onClick={()=>{const element=document.getElementById(`coverage-device-${encodeURIComponent(cls.uid)}`);if(element instanceof HTMLDetailsElement)element.open=true}}>{deviceLabel(cls.name)} <strong>{own.filter(cell=>cell.assessment.displayStatus==="usable").length}/{own.length}</strong><small> 측정 충족/표시 조건</small></a>})}</nav>
      <details className="text-sm"><summary className="min-h-11 cursor-pointer py-2">— 지원 안 됨 · 숨긴 조건 {data.cells.filter(cell=>cell.model.name===model&&!coveragePathVisible(cell,inventory.data)).length}개와 사유</summary><ul className="grid gap-2">{data.cells.filter(cell=>cell.model.name===model&&!coveragePathVisible(cell,inventory.data)).map(cell=><li key={cell.id}>{deviceLabel(cell.deviceClass.name)} / {cell.powerMode} / {cell.executionPath}: {cell.support.reasons.map(reason=>reason.message).join(" / ")||"모델 파일 아키텍처 또는 가속기 선언이 장치와 일치하지 않습니다."}</li>)}</ul></details>
      <div className="coverage-workspace">
        <div className="coverage-pivot" aria-label={`${model??""} 장치별 커버리지`}>
          {!classes.length&&<p className="p-4">현재 필터에 맞는 조건이 없습니다. 선택한 조건 상세는 유지됩니다.</p>}
          {classes.map(cls=>{
            const deviceCells=cells.filter(cell=>cell.deviceClass.uid===cls.uid),paths=[...new Set(deviceCells.map(cell=>cell.executionPath))].sort(),modes=coverageModes(deviceCells)
            return <details key={cls.uid} id={`coverage-device-${encodeURIComponent(cls.uid)}`} className="coverage-device" open={selected?.deviceClass.uid===cls.uid||(!selected&&cls.uid===classes[0]?.uid)}><summary><h2>{deviceLabel(cls.name)} <small>사용 가능 {deviceCells.filter(cell=>cell.assessment.displayStatus==="usable").length} / {deviceCells.length}개 조건</small></h2></summary>
              <div className="coverage-desktop-table"><table><caption className="sr-only">{deviceLabel(cls.name)} 전력모드별 실행 경로</caption><thead><tr><th scope="col">전력모드<br/><small>실측 W 낮은 순</small></th>{paths.map(path=><th key={path} scope="col">{path.replace(/^firstfloor-/,"")}</th>)}</tr></thead><tbody>{modes.map(mode=><tr key={mode}><th scope="row"><span>{mode}</span><small>{powerModeLabel(mode,coverageWatts(deviceCells.filter(cell=>cell.powerMode===mode))).split(" · ")[1]}</small></th>{paths.map(path=>{const cell=deviceCells.find(cell=>cell.powerMode===mode&&cell.executionPath===path);return <td key={path}>{cell?<CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/>:<span>조건 없음</span>}</td>})}</tr>)}</tbody></table></div>
              <div className="coverage-mobile-cells">{modes.map(mode=><section key={mode}><h3>{powerModeLabel(mode,coverageWatts(deviceCells.filter(cell=>cell.powerMode===mode)))}</h3>{deviceCells.filter(cell=>cell.powerMode===mode).map(cell=><div key={cell.id}><h4>{cell.executionPath.replace(/^firstfloor-/,"")}</h4><CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/></div>)}</section>)}</div>
            </details>
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
  const representative=coverageRepresentative(cell),progress=coverageProgress(cell)
  const warnings=cell.variants.flatMap(variant=>variant.assessment.checklist.checks).filter(check=>check.result!=="pass")
  return <button data-coverage-cell={cell.id} className={`coverage-cell ${cell.support.verdict!=="Compatible"?"coverage-cell-unconfirmed":""}`} aria-pressed={selected} aria-controls="coverage-selected-condition" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} ${cell.assessment.label} 상세`} onClick={event=>select(cell,event.currentTarget)}>
    <MeasurementStatus assessment={cell.assessment}/>
    <span className="coverage-representative">기준 충족 배치: {progress?.criteriaMet.join("·")||"없음"}{progress?.missing.length?` / 부족 ${progress.missing.join("·")}`:""}</span>
    {warnings.length>0&&<span className="coverage-warning">△ 신뢰 근거 검토 {warnings.length}건</span>}
    {cell.support.verdict==="Unknown"&&<span className="coverage-representative">? {cell.support.reasons.map(reason=>reason.message).join(" / ")||"실행 지원 근거 미확인"}</span>}
    <span className="coverage-representative">{representative?`배치 ${representative.batch} p95 ${number(representative.p95Milliseconds,"ms")} ms / 추가 ${number(representative.incrementalJoulesPerItem,"J/건")} J/건`:"대표값 없음"}</span>
    {cell.variantCount>1&&<small>정확 조건 {cell.variantCount}개 / 대표 조건 막대</small>}
    {cell.failureSummary.length>0&&<strong className="text-fail">{cell.failureSummary[0].label??"기타"}{cell.failureSummary.length>1?` (${cell.failureSummary.length}회)`:""}</strong>}
  </button>
}
function failureHref(failure:MeasurementFailure,namespace:string,returnTo:string) {
  return failure.runName?`/measurements/profiling-runs/${encodeURIComponent(failure.runNamespace||namespace)}/${encodeURIComponent(failure.runName)}?${new URLSearchParams({uid:failure.runId.split("/").at(-1)??"",returnTo})}`:`/measurements/runs?${new URLSearchParams({namespace,parentUID:failure.requestId.split("/").at(-1)??"",status:"problem",returnTo})}`
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
    return <li key={`${cell.id}/${action.kind}/${action.reasonCode}`}><MeasurementStatus assessment={cell.assessment}/><div><Link href={`/measurements?${selection}`}>{cell.model.name} / {deviceLabel(cell.deviceClass.name)} / {cell.powerMode} / {cell.executionPath.replace(/^firstfloor-/,"")}</Link><p>{failure?`${failure.label??"기타"} — 실패 회차 확인`:action.kind==="missing-batches"?"부족한 배치를 측정 요청으로 채우세요.":action.message||"조건 상세에서 필요한 근거를 확인하세요."}</p>{failure&&<Link href={failureHref(failure,namespace,`/measurements?${selection}`)}>실패 회차 열기</Link>}</div></li>
  })}</ul>:<p>확인된 추가 조치가 없습니다. 확인 불가 범위도 살펴보세요.</p>
}
function TrustLine({row}:{row:ResultRow|undefined}) {
  const source=row?.powerSource
  const power=source?[source.kind,source.metric,source.nodeName].filter(value=>typeof value==="string").join(" / ")||"출처 상세 참조":"미제공"
  const uncertainty=row?.confidence?`${number(row.confidence.level*100)}% 신뢰구간 ${row.incrementalJoulesPerItem?`±${number(100*row.confidence.halfWidth/Math.abs(row.incrementalJoulesPerItem),"%")}%`:`±${number(row.confidence.halfWidth,"J/건")} J/건`}`:"오차 미제공"
  return <section className="coverage-trust"><h3>측정 근거 · 운영 적합성은 별도 검토</h3><p>{row?`독립 반복 실행 ${number(row.activeTrialCount)}회 / 유휴 ${number(row.idleTrialCount)}회`:"저장된 측정 결과 없음"}</p><p>추가 에너지 {uncertainty}</p><p>전력 {power} · 장치 단독 전력 아님</p><p className="coverage-warning">△ 데이터셋 용도 일치 확인 필요</p></section>
}
function CoverageDetail({cell,namespace,close,visible}:{cell:MeasurementCoverageCell;namespace:string;close:()=>void;visible:boolean}) {
  const params=useSearchParams(),router=useRouter(),panel=useRef<HTMLDivElement>(null)
  const variant=coverageVariant(cell,params.get("variant")??undefined)
  const progress=variant?.batchProgress??coverageProgress(cell),rows=variant?.resultRows??[],returnTo=`/measurements?${params}`
  const filters={model:cell.model.name,deviceClass:cell.deviceClass.name,path:cell.executionPath,powerMode:cell.powerMode,limit:"200"}
  const profiles=useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams(filters)}`,{enabled:rows.length>0,decoder:parseSchedulerJSON})
  const runs=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${new URLSearchParams({...filters,status:"problem"})}`,{enabled:cell.failureSummary.length>0})
  const preprocessors=useChillQuery<{items:PreprocessorResource[]}>("v1/preprocessors",{enabled:rows.length>0})
  const datasets=useChillQuery<{items:DatasetResource[]}>("v1/datasets",{enabled:rows.length>0})
  const condition=variant?.condition
  const active=condition?.activeProtocol as {dataset?:string|{name?:string}}|undefined
  const pre=condition?.preprocessor as {name?:string}|string|undefined
  const legacy=condition?.state as {candidate?:{preprocessor?:string}}|undefined
  const dataset=typeof active?.dataset==="string"?active.dataset:active?.dataset?.name??""
  const preprocessor=typeof pre==="string"?pre:pre?.name??legacy?.candidate?.preprocessor??""
  const inputs=preprocessors.data&&datasets.data?requestInputs(cell.model.name,"",preprocessor,dataset,{preprocessors:preprocessors.data.items,datasets:datasets.data.items}):undefined
  const profileIDs=new Set(rows.flatMap(row=>[row.profileId,row.sourceProfileId].filter(Boolean)))
  const measured=(profiles.data?.profiles??[]).filter(profile=>profileIDs.has(profile.id)).map(profile=>profile.measuredAt).filter(at=>Number.isFinite(Date.parse(at))).sort()
  const checks=(variant?.assessment??cell.assessment).checklist.checks.filter(check=>check.result!=="pass")
  useEffect(()=>{panel.current?.focus({preventScroll:true});if(window.matchMedia("(max-width: 767px)").matches)panel.current?.scrollIntoView({block:"start"})},[cell.id])
  function choose(id:string){const next=new URLSearchParams(params.toString());next.set("variant",id);router.replace(`?${next}`,{scroll:false})}
  return <div id="coverage-selected-condition" ref={panel} tabIndex={-1} role="region" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} 상세`} className="coverage-selected">
    <header><h2>{deviceLabel(cell.deviceClass.name)}</h2><Button variant="outline" onClick={close}>닫기</Button></header>
    <p className="font-semibold break-words">{cell.model.name}</p><p>선택 경로: {powerModeLabel(cell.powerMode,coverageWatts([cell]))}<br/>{cell.executionPath}</p>
    {!visible&&<p className="text-muted-foreground">이 조건은 현재 탭·필터 밖에 있습니다. 선택은 유지됩니다.</p>}
    <MeasurementStatus assessment={cell.assessment}/>
    <section className="coverage-warning" aria-label="운영 신뢰 검토"><h3>△ 운영 적합성 검토 필요</h3><p>{inputs?.warning||"모델·데이터셋 용도 일치는 별도 확인이 필요합니다."}</p>{checks.length>0&&<ul>{checks.map(check=><li key={check.code}>{check.result==="unknown"?"미확인":"주의"}: {check.message}</li>)}</ul>}<p>측정 충족과 운영 승인은 다릅니다. 서버의 채택 판정과 원값은 유지합니다.</p></section>
    <p className="text-sm">{measured.length?<><time dateTime={measured.at(-1)}>{formatMeasurementTime(measured.at(-1)!)}</time> / {measurementAge(measured.at(-1)!)}</>:profiles.isPending&&rows.length?"측정 일시 조회 중":profiles.error?"측정 일시 조회 실패":"측정 일시 미기록 또는 현재 조회 범위에 없음"}</p>
    <TrustLine row={rows.find(row=>row.batch===1)??rows[0]}/>
    <Button asChild><Link href={coverageRequestHref(cell,namespace,returnTo,variant?.id,params.get("batches")?.split(",").map(Number)??[])}>이 조건으로 측정 요청</Link></Button>
    <p className="text-sm">조건과 부족 배치를 채운 요청 화면을 엽니다. 자동으로 제출하지 않습니다.</p>
    {cell.variantCount>1&&<label className="grid gap-2">측정 조건 {cell.variantCount}개<select value={variant?.id??""} onChange={event=>choose(event.target.value)}>{cell.variants.map(item=><option key={item.id} value={item.id}>{conditionLabel(item.condition)} / {String(item.condition.name??item.id.slice(-8))}</option>)}</select></label>}
    {cell.variantCount>1&&<p className="text-sm text-muted-foreground">칸 상태는 전체 조건 기준입니다. 아래 배치와 수치는 선택한 조건 기준이며 서로 다른 조건의 결과를 합치지 않습니다.</p>}
    {variant&&<p className="text-sm">{conditionLabel(variant.condition)}</p>}
    <BatchBar progress={progress}/><p>선택 조건 목표: {progress?.target.join(" · ")||"미지정"} / 부족 배치: {progress?progress.missing.join(" · ")||"없음":"미확인"}</p>
    {cell.failureSummary.length>0&&<section><h3>실패 회차</h3>{[...new Map(cell.failureSummary.map(failure=>[failure.runId||`${failure.requestId}/${failure.code}`,failure])).values()].map((failure,index)=>{const run=runs.data?.runs.find(run=>run.uid===failure.runId.split("/").at(-1));const at=run?.status.conditions?.filter(condition=>condition.status==="False").map(condition=>condition.lastTransitionTime).filter((value):value is string=>!!value).sort().at(-1)??run?.creationTimestamp;return <div key={index} className="coverage-failure"><strong>{failure.label??"기타"}</strong><Link href={failureHref(failure,namespace,returnTo)}>{failure.runName||"이 요청의 실패 회차"} 확인</Link><p>{at?`${formatMeasurementTime(at)} / ${measurementAge(at)}`:"실패 시각 미기록 또는 현재 조회 범위에 없음"}</p><span>{failure.message}</span></div>})}</section>}
    {rows.length>0&&<section><h3>배치별 결과</h3><div className="overflow-x-auto"><table className="coverage-results"><thead><tr><th>배치</th><th>p95 ms</th><th>총 J/건</th><th>추가 J/건</th></tr></thead><tbody>{rows.map(row=><tr key={row.id}><th scope="row">{row.batch}</th><td>{number(row.p95Milliseconds,"ms")}</td><td>{number(row.totalJoulesPerItem,"J/건")}</td><td>{number(row.incrementalJoulesPerItem,"J/건")}</td></tr>)}</tbody></table></div><p className="text-sm">현재 스케줄러 {variant?.assessment.scope.schedulerUsed===true?"사용":variant?.assessment.scope.schedulerUsed===false?"미사용":"사용 여부 미확인"}</p></section>}
    <details><summary>신뢰 근거·조건·원문</summary><p>실행 지원: {cell.support.verdict==="Compatible"?"확인":cell.support.verdict==="Incompatible"?"지원 안 함":"확인 불가"}</p><ul>{(variant?.assessment??cell.assessment).checklist.checks.map(check=><li key={check.code}>{check.message} ({check.result})</li>)}</ul><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify({condition:variant?.condition,rows,support:cell.support,history:cell.history},null,2)}</pre></details>
  </div>
}
