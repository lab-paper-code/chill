"use client"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError, useChillQuery, useSession } from "@/lib/client"
import { coverageFailureHref, coverageProfileQuery, coverageStatusCounts, measurementAge, coverageSelection, coveragePathVisible, coverageWatts, coverageModes, coverageVariant, coverageRepresentative, coverageProgress, coverageBatchState, coverageRequestHref, powerModeLabel, deviceLabel, conditionLabel, type CompatibilityRead } from "@/lib/measurement-coverage"
import type { BatchProgress, InventoryView, ModelCompatibility, MeasurementCoverageResponse, MeasurementCoverageCell, ProfilesView, RunsView, ResultRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
const coverageStates={usable:"사용 가능",review:"검토 필요",insufficient:"부족",problem:"문제",running:"진행",unmeasured:"미측정",unknown:"확인 불가"}
import { formatMeasurementNumber as number, formatMeasurementTime, measurementCheckText, measurementUncertainty } from "@/lib/measurements"
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
  return <section className="coverage-summary" aria-label="전체 모델 측정 현황"><strong>전체 모델 집계 · {data.scope.namespace} · 표시 조합 {visible.length}</strong><span>아래 모델·장치 선택과 별개{data.scope.supportBasis==="selected-node-static"?" · 지정 노드 범위":""}</span>{Object.entries(coverageStates).map(([status,label])=><span key={status} className={`status-${status}`}>{label} {counts[status as keyof typeof counts]}</span>)}<details><summary>집계 범위·갱신 시각</summary><p>위 상태 합계는 표시 조합 {visible.length}개와 같습니다. 지원 안 됨 {data.cells.length-visible.length}개는 제외했습니다. 아래 표는 선택 모델·필터 범위입니다.</p><p>커버리지 갱신 {data.dataAsOf?formatMeasurementTime(data.dataAsOf):"미확인"} / {data.freshness==="fresh"?"최신":"이전 자료"} / {data.completeness==="complete"?"전체 확인":"일부 확인"}</p><p>갱신 시각은 결과의 실제 측정 시각과 다릅니다.</p></details></section>
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
        <label className="grid gap-1 text-sm">장치<select className="h-11 rounded-md border bg-background px-2" value={params.get("deviceClass")??""} onChange={event=>update("deviceClass",event.target.value)}><option value="">전체 장치</option>{[...new Set(data.cells.filter(cell=>cell.model.name===model&&coveragePathVisible(cell,inventory.data)).map(cell=>cell.deviceClass.name))].sort().map(name=><option key={name} value={name}>{deviceLabel(name)}</option>)}</select></label>
        <details><summary className="min-h-11 cursor-pointer py-2">목표 배치·세부 필터 ({targets.length?targets.join("·"):"요청 범위"})</summary>
          <fieldset className="coverage-targets"><legend>목표 배치</legend><Button variant={!targets.length?"default":"outline"} onClick={()=>update("batches","")}>요청 범위</Button>{batches.map(batch=><Button key={batch} variant={targets.includes(batch)?"default":"outline"} aria-pressed={targets.includes(batch)} onClick={()=>update("batches",(targets.includes(batch)?targets.filter(value=>value!==batch):[...targets,batch]).sort((a,b)=>a-b).join(","))}>{batch}</Button>)}</fieldset>
          <MeasurementFilters axes={[{key:"path",label:"실행 경로",values:data.cells.filter(cell=>cell.model.name===model&&coveragePathVisible(cell,inventory.data)).map(cell=>cell.executionPath)},{key:"powerMode",label:"전력모드",values:data.cells.map(cell=>cell.powerMode)}]}/><div className="grid gap-3 sm:grid-cols-2"><label>Node<Input value={params.get("node")??""} onChange={event=>update("node",event.target.value)}/></label><label>CPU quota<Input value={params.get("cpu")??""} onChange={event=>update("cpu",event.target.value)}/></label></div>
        </details>
      </div>
      <p className="text-sm font-semibold" aria-label="선택 범위 집계">선택 범위: {model} / {params.get("deviceClass")?deviceLabel(params.get("deviceClass")!):"전체 장치"} · 표시 {cells.length}개 중 사용 가능 {cells.filter(cell=>cell.assessment.displayStatus==="usable").length}개</p>
      <p className="coverage-batch-legend" aria-label="배치 기호 범례">배치: ■ 기준 충족 · ▧ 측정 있음 · ◷ 진행 · □ 부족 · ? 미확인 · — 요청 목표 밖 (지원 불가 아님)</p>
      <div className="coverage-workspace">
        <div className="coverage-pivot" aria-label={`${model??""} 장치별 커버리지`}>
          {!classes.length&&<p className="p-4">현재 필터에 맞는 조건이 없습니다. 선택한 조건 상세는 유지됩니다.</p>}
          {classes.map(cls=>{
            const deviceCells=cells.filter(cell=>cell.deviceClass.uid===cls.uid),paths=[...new Set(deviceCells.map(cell=>cell.executionPath))].sort(),modes=coverageModes(deviceCells)
            return <details key={cls.uid} id={`coverage-device-${encodeURIComponent(cls.uid)}`} className="coverage-device" open={selected?.deviceClass.uid===cls.uid||(!selected&&cls.uid===classes[0]?.uid)}><summary><h2>장치 종류: {deviceLabel(cls.name)} <small>노드: {inventory.data?.classes.find(item=>item.uid===cls.uid)?.nodes.map(node=>node.name).join(", ")||"미확인"} · 사용 가능 {deviceCells.filter(cell=>cell.assessment.displayStatus==="usable").length} / {deviceCells.length}개 조건</small></h2></summary>
              <div className="coverage-desktop-table"><table><caption className="sr-only">{deviceLabel(cls.name)} 전력모드별 실행 경로</caption><thead><tr><th scope="col">전력모드<br/><small>실측 W 낮은 순</small></th>{paths.map(path=><th key={path} scope="col">{path.replace(/^firstfloor-/,"")}</th>)}</tr></thead><tbody>{modes.map(mode=><tr key={mode}><th scope="row"><span>{mode}</span><small>{powerModeLabel(mode,coverageWatts(deviceCells.filter(cell=>cell.powerMode===mode))).split(" · ")[1]}</small></th>{paths.map(path=>{const cell=deviceCells.find(cell=>cell.powerMode===mode&&cell.executionPath===path);return <td key={path}>{cell?<CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/>:<span>조건 없음</span>}</td>})}</tr>)}</tbody></table></div>
              <div className="coverage-mobile-cells">{modes.map(mode=><section key={mode}><h3>{powerModeLabel(mode,coverageWatts(deviceCells.filter(cell=>cell.powerMode===mode)))}</h3>{deviceCells.filter(cell=>cell.powerMode===mode).map(cell=><div key={cell.id}><h4>{cell.executionPath.replace(/^firstfloor-/,"")}</h4><CoverageCell cell={cell} selected={cell.id===selected?.id} select={select}/></div>)}</section>)}</div>
            </details>
          })}
        </div>
        <aside className="coverage-inspector" aria-label="선택 조건 상세">{selected?<CoverageDetail cell={selected} namespace={namespace} close={close} visible={cells.some(cell=>cell.id===selected.id)}/>:<div className="coverage-inspector-empty"><h2>조건 상세</h2><p>왼쪽에서 칸을 선택하세요.</p><p>신뢰 근거, 배치별 결과와 실패 회차를 확인하고 같은 조건으로 측정을 요청할 수 있습니다.</p></div>}</aside>
      </div>
      <section className="coverage-detail"><h2>다음 조치</h2><CoverageActions data={data} namespace={namespace}/></section>
      {data.sources.filter(source=>source.error).map(source=><p role="alert" key={source.kind}>{source.kind}: 조회하지 못한 자료가 있습니다 ({source.error?.code})</p>)}
      <details><summary className="min-h-11 cursor-pointer py-2">상태·배치 표시와 숨긴 지원 불가 조건</summary><p>사용 가능: 채택 근거와 목표 배치 충족. 검토 필요: 확인된 입력 계약·품질 불일치. 부족: 목표 배치 누락. 문제: 실행 실패 또는 근거 충돌. 미확인 항목은 상세에 별도로 표시합니다.</p><p>배치: ■ 기준 충족 / ▧ 측정 있음 / ◷ 진행 / □ 부족 / — 목표 밖. 서로 다른 정확 조건의 배치를 합치지 않습니다.</p><p>선택 모델의 지원 불가 {data.cells.filter(cell=>cell.model.name===model&&!coveragePathVisible(cell,inventory.data)).length}개는 숨겼습니다.</p><ul>{data.cells.filter(cell=>cell.model.name===model&&!coveragePathVisible(cell,inventory.data)).map(cell=><li key={cell.id}>{deviceLabel(cell.deviceClass.name)} / {cell.powerMode} / {cell.executionPath}: {cell.support.reasons.map(reason=>reason.message).join(" / ")||"모델 파일 아키텍처 또는 가속기 선언이 장치와 일치하지 않습니다."}</li>)}</ul></details>
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
  const checks=coverageVariant(cell)?.assessment.checklist.checks??cell.assessment.checklist.checks
  const warnings=checks.filter(check=>check.result==="fail"),unknown=checks.filter(check=>check.result==="unknown")
  return <button data-coverage-cell={cell.id} className={`coverage-cell ${cell.support.verdict!=="Compatible"?"coverage-cell-unconfirmed":""}`} aria-pressed={selected} aria-controls="coverage-selected-condition" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} ${cell.assessment.label} 상세`} onClick={event=>select(cell,event.currentTarget)}>
    <MeasurementStatus assessment={cell.assessment}/>
    <BatchBar progress={progress}/>
    {warnings.length>0&&<span className="measurement-status status-review">근거 불일치 {warnings.length}건</span>}
    {unknown.length>0&&<span className="measurement-status coverage-trust-pending">추가 확인 {unknown.length}건 · 운영 승인 별도</span>}
    {cell.support.verdict==="Unknown"&&<span className="coverage-representative" title={cell.support.reasons.map(reason=>reason.message).join(" / ")}>실행 지원 미확인{cell.support.node?` · 노드 ${cell.support.node.name}`:""} · 상세에서 선언 확인</span>}
    <span className="coverage-representative">{representative?`배치 ${representative.batch} p95 ${number(representative.p95Milliseconds,"ms")} ms / 추가 ${number(representative.incrementalJoulesPerItem,"J/건")} J/건`:"대표값 없음"}</span>
    {cell.variantCount>1&&<small>정확 조건 {cell.variantCount}개 / 대표 조건 막대</small>}
    {cell.failureSummary.length>0&&<strong className="text-fail">{cell.failureSummary[0].label??"기타"}{cell.failureSummary.length>1?` (${cell.failureSummary.length}회)`:""}</strong>}
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
function TrustLine({row}:{row:ResultRow|undefined}) {
  const source=row?.powerSource
  const power=source?[source.kind,source.metric,source.nodeName].filter(value=>typeof value==="string").join(" / ")||"출처 상세 참조":"미제공"
  const uncertainty=measurementUncertainty(row)
  return <section className="coverage-trust"><h3>측정 근거</h3><p>{row?`작업 실행 반복 ${number(row.activeTrialCount)}회 / 작업 없이 측정 ${number(row.idleTrialCount)}회`:"저장된 측정 결과 없음"}</p><p>추가 에너지 {uncertainty}</p><p>오차 허용 기준 미설정 · 수치만 표시하며 운영 허용 여부를 판정하지 않습니다.</p><p>전력 {power} · 장치 단독 전력 아님</p></section>
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
  const checks=(variant?.assessment??cell.assessment).checklist.checks.filter(check=>check.result!=="pass")
  useEffect(()=>{panel.current?.focus({preventScroll:true});if(window.matchMedia("(max-width: 767px)").matches)panel.current?.scrollIntoView({block:"start"})},[cell.id])
  function choose(id:string){const next=new URLSearchParams(params.toString());next.set("variant",id);router.replace(`?${next}`,{scroll:false})}
  return <div id="coverage-selected-condition" ref={panel} tabIndex={-1} role="region" aria-label={`${deviceLabel(cell.deviceClass.name)} ${cell.powerMode} ${cell.executionPath} 상세`} className="coverage-selected">
    <header><h2>{deviceLabel(cell.deviceClass.name)}</h2><Button variant="outline" onClick={close}>닫기</Button></header>
    <p className="font-semibold break-words">{cell.model.name}</p><p>선택 경로: {powerModeLabel(cell.powerMode,coverageWatts([cell]))}<br/>{cell.executionPath}</p>
    {!visible&&<p className="text-muted-foreground">이 조건은 현재 탭·필터 밖에 있습니다. 선택은 유지됩니다.</p>}
    <section className="coverage-condition-verdict" aria-label="채택 상태와 신뢰 조건"><MeasurementStatus assessment={cell.assessment}/>
      {checks.length>0&&<><h3>{checks.some(check=>check.result==="fail")?"근거 불일치 · 운영 사용 전 확인":"미확인 조건 · 운영 사용 전 확인"}</h3><ul>{checks.map(check=><li key={`${check.code}/${check.result}`} className="coverage-warning">{check.result==="fail"?"불일치":check.result==="unknown"?"미확인":"참고"}: {measurementCheckText(check)}</li>)}</ul></>}
      <p>사용 가능은 저장 근거와 목표 배치의 충족 상태입니다. 미확인 항목은 통과가 아니며 스케줄러 채택은 별도 운영 승인을 뜻하지 않습니다.</p>
    </section>
    <div className="text-sm">{measured.length?<p>실제 측정 <time dateTime={measured.at(-1)}>{formatMeasurementTime(measured.at(-1)!)}</time> / {measurementAge(measured.at(-1)!)}</p>:profiles.isPending&&rows.length?<p>측정 시각 조회 중</p>:profiles.error?<><p role="alert">측정 시각 조회 실패 (HTTP {profiles.error.status}) · 수치 근거는 유지합니다.</p><Button variant="outline" onClick={()=>void profiles.refetch()}>측정 시각 다시 조회</Button></>:<p>측정 시각 미기록 또는 현재 프로파일에 없음</p>}</div>
    <TrustLine row={rows.find(row=>row.batch===1)??rows[0]}/>
    <Button asChild><Link href={coverageRequestHref(cell,namespace,returnTo,variant?.id,params.get("batches")?.split(",").map(Number)??[])}>이 조건으로 측정 요청</Link></Button>
    <p className="text-sm">조건과 부족 배치를 채운 요청 화면을 엽니다. 자동으로 제출하지 않습니다.</p>
    {cell.variantCount>1&&<label className="grid gap-2">측정 조건 {cell.variantCount}개<select value={variant?.id??""} onChange={event=>choose(event.target.value)}>{cell.variants.map(item=><option key={item.id} value={item.id}>{conditionLabel(item.condition)} / {String(item.condition.name??item.id.slice(-8))}</option>)}</select></label>}
    {cell.variantCount>1&&<p className="text-sm text-muted-foreground">칸 상태는 전체 조건 기준입니다. 아래 배치와 수치는 선택한 조건 기준이며 서로 다른 조건의 결과를 합치지 않습니다.</p>}
    {variant&&<p className="text-sm">{conditionLabel(variant.condition)}</p>}
    <BatchBar progress={progress}/><p>선택 조건 목표: {progress?.target.join(" · ")||"미지정"} / 부족 배치: {progress?progress.missing.join(" · ")||"없음":"미확인"}</p>
    {cell.failureSummary.length>0&&<section><h3>실패 기록</h3>{[...new Map(cell.failureSummary.map(failure=>[failure.runId||`${failure.requestId}/${failure.code}`,failure])).values()].map((failure,index)=>{const run=runs.data?.runs.find(run=>run.uid===failure.runId.split("/").at(-1));const at=run?.status.conditions?.filter(condition=>condition.status==="False").map(condition=>condition.lastTransitionTime).filter((value):value is string=>!!value).sort().at(-1)??run?.creationTimestamp;return <div key={index} className="coverage-failure"><strong>{failure.label??"기타"}</strong><Link href={coverageFailureHref(failure,namespace,returnTo)}>{failure.runName?"실패 회차 확인":"실패 요청 사유 확인"}</Link>{!failure.runName&&<p>연결된 실패 회차가 없습니다. 요청 자체의 실패 사유를 확인하세요.</p>}<p>{at?`${formatMeasurementTime(at)} / ${measurementAge(at)}`:"실패 시각 미기록 또는 현재 조회 범위에 없음"}</p><span>{failure.message}</span></div>})}</section>}
    {rows.length>0&&<section><h3>배치별 결과</h3><div className="overflow-x-auto"><table className="coverage-results"><thead><tr><th>배치</th><th>p95 ms</th><th>총 J/건</th><th>추가 J/건</th></tr></thead><tbody>{rows.map(row=><tr key={row.id}><th scope="row">{row.batch}</th><td>{number(row.p95Milliseconds,"ms")}</td><td>{number(row.totalJoulesPerItem,"J/건")}</td><td>{number(row.incrementalJoulesPerItem,"J/건")}</td></tr>)}</tbody></table></div><p className="text-sm">현재 스케줄러 {variant?.assessment.scope.schedulerUsed===true?"사용":variant?.assessment.scope.schedulerUsed===false?"미사용":"사용 여부 미확인"}</p></section>}
    <details><summary>신뢰 근거·조건·원문</summary><p>실행 지원: {cell.support.verdict==="Compatible"?"확인":cell.support.verdict==="Incompatible"?"지원 안 함":"확인 불가"}</p><ul>{(variant?.assessment??cell.assessment).checklist.checks.map(check=><li key={check.code}>{check.message} ({check.result})</li>)}</ul><pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify({condition:variant?.condition,rows,support:cell.support,history:cell.history},null,2)}</pre></details>
  </div>
}
