"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useChillMutation, useSession } from "@/lib/client"
import type { CampaignResult, InventoryView, InventoryClass, PoolState, PoolsView, RunsView, ProfilingRun, Passport, SchedulerView } from "@/lib/read-types"
import { ReadRegion, SourceJSON, SourceExport, DetailsDialog } from "@/components/read-region"
import { DataTable } from "@/components/data-table"
import { SectionCards } from "@/components/section-cards"
import { Button } from "@/components/ui/button"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"

export function OverviewPage() {
  const [poll,setPoll]=React.useState(false)
  const scheduler=useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll})
  return <div className="page"><h1>시스템 상태</h1><label><input type="checkbox" checked={poll} onChange={event=>setPoll(event.target.checked)}/> 자동 새로고침</label><SectionCards poll={poll}/><Link href="/scheduler">운영 판단과 실제 구성</Link>{scheduler.data&&<p className="text-sm text-muted-foreground">구성 관측 {scheduler.data.state.snapshot.at.startsWith("0001-")?"첫 관측 대기":scheduler.data.state.snapshot.at}. 시스템 준비 상태와 운영 판단은 별개입니다.</p>}</div>
}

function QueryWindow({path,page,extra}:{path:string;page?:{offset:number;limit:number;total:number;nextOffset?:number};extra?:React.ReactNode}) {
  const params=useSearchParams(),router=useRouter()
  const {config}=useSession()
  const [q,setQ]=React.useState(params.get("q")??"")
  const [limit,setLimit]=React.useState(params.get("limit")??String(config.pageSize))
  function navigate(offset:number) {
    const next=new URLSearchParams(params.toString());next.set("q",q);next.set("limit",limit);next.set("offset",String(offset));router.replace(`${path}?${next}`)
  }
  return <><form className="toolbar" onSubmit={event=>{event.preventDefault();navigate(0)}}><label>이름 검색<input value={q} onChange={event=>setQ(event.target.value)} maxLength={256}/></label>{extra}<DetailsDialog label="표시 개수" title="목록 표시 개수"><label>한 번에 보기<input type="number" min="1" max={config.maxPageSize} value={limit} onChange={event=>setLimit(event.target.value)} required/></label></DetailsDialog><Button type="submit">조회</Button></form>{page&&<div className="toolbar"><span>총 {page.total}건</span><Button variant="outline" disabled={page.offset===0} onClick={()=>navigate(Math.max(0,page.offset-page.limit))}>이전</Button><Button variant="outline" disabled={page.nextOffset===undefined} onClick={()=>navigate(page.nextOffset??0)}>다음</Button></div>}</>
}

export function ResourcesPage() {
  const params=useSearchParams();const {config}=useSession()
  const allowed=new URLSearchParams();["q","offset","limit"].forEach(key=>{const value=params.get(key);if(value!==null)allowed.set(key,value)});if(!allowed.has("limit"))allowed.set("limit",String(config.pageSize))
  const query=useChillQuery<InventoryView>(`v1/inventory?${allowed}`)
  return <div className="page"><h1>장비 목록</h1><QueryWindow path="/resources" page={query.data?.classPage}/><ReadRegion title="장비와 노드" query={query}>{query.data&&<><DataTable<InventoryClass> data={query.data.classes??[]} columns={[
    {key:"name",label:"장비 종류",value:row=>row.name,sortValue:row=>row.name},
    {key:"capacity",label:"장비 사양",value:row=>`${row.architecture} · ${row.memory}${row.accelerator?` · ${row.accelerator}`:""}`},
    {key:"nodes",label:"노드",value:row=><ul>{row.nodes.map(node=><li key={node.uid}>{node.name} · {node.ready==="True"?"준비됨":node.ready==="False"?"준비되지 않음":"상태 확인 안 됨"}{node.schedulingDisabled?" · 배정 중지":""}</li>)}</ul>},
    {key:"details",label:"상세",value:row=><DetailsDialog label="장비 자세히" title={row.name}><p>등록된 사양과 노드 상태입니다. 실행 적합성과 여유 용량을 보장하지 않습니다.</p><SourceJSON value={row}/></DetailsDialog>},
  ]}/><SourceJSON value={query.data}/><SourceExport value={{conditions:Object.fromEntries(allowed),source:query.data}} filename="chill-inventory.json"/></>}</ReadRegion></div>
}

export function PoolsPage() {
  const query=useChillQuery<PoolsView>("v1/pools")
  return <div className="page"><h1>실행 그룹</h1><ReadRegion title="현재 실행 그룹" query={query}>{query.data&&<><DataTable<PoolState> data={query.data.items??[]} columns={[
    {key:"name",label:"그룹",value:row=><Link href={`/pools/${encodeURIComponent(row.id)}`}>{row.currentNodes.map(node=>node.name).join(", ")||"노드 없는 그룹"} 자세히</Link>},
    {key:"nodes",label:"노드",value:row=>row.currentNodes.map(node=>node.name).join(", ")||"없음"},
    {key:"workloads",label:"서비스",value:row=><ul>{row.servingWorkloads.map(workload=><li key={workload.uid}><Link href={`/workloads/${encodeURIComponent(workload.name)}?uid=${encodeURIComponent(workload.uid)}`}>{workload.name}</Link></li>)}</ul>},
    {key:"at",label:"관측 시각",value:row=>row.observedAt},
    {key:"detail",label:"상세",value:row=><SourceJSON value={row}/>},
  ]}/><SourceJSON title="조회 상세" value={query.data}/></>}</ReadRegion><Link href="/scheduler?view=snapshot">운영 판단의 실제 자원 구성</Link></div>
}

export function PoolPage({id}:{id:string}) {
  const [poll,setPoll]=React.useState(false)
  const query=useChillQuery<PoolState>(`v1/pools/${encodeURIComponent(id)}`,{poll})
  return <div className="page"><h1>실행 그룹 상세</h1><label><input type="checkbox" checked={poll} onChange={event=>setPoll(event.target.checked)}/> 자동 새로고침</label><ReadRegion title="현재 구성" query={query}>{query.data&&<><DataTable data={query.data.instances??[]} columns={[
    {key:"node",label:"노드",value:row=>row.node.name},
    {key:"ready",label:"상태",value:row=>row.ready?"준비됨":"준비되지 않음"},
    {key:"workloads",label:"서비스",value:row=>row.workloads.map(workload=>workload.name).join(", ")||"없음"},
    {key:"detail",label:"상세",value:row=><SourceJSON value={row}/>},
  ]}/><DetailsDialog label="노드와 서비스 보기" title="그룹에 연결된 대상"><h2>노드</h2><ul>{query.data.currentNodes.map(row=><li key={row.uid}>{row.name}</li>)}</ul><h2>서비스</h2><ul>{query.data.servingWorkloads.map(row=><li key={row.uid}><Link href={`/workloads/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`}>{row.name}</Link></li>)}</ul><p>현재 그룹 관측 {query.data.observedAt}</p><Link href={`/scheduler?view=snapshot&pool=${encodeURIComponent(id)}`}>자원 할당과 전력모드의 실제 구성</Link><details><summary>원본</summary><pre className="source-json">{stringifySchedulerJSON(safeSource(query.data),2)}</pre></details></DetailsDialog><SourceExport value={query.data} filename={`chill-pool-${id}.json`}/></>}</ReadRegion><Link href="/pools">그룹 목록</Link></div>
}

const runFilters=[ ["","전체"],["ExecutionComplete=True","실행 완료"],["ExecutionComplete=False","실행 미완료"],["ExecutionComplete=Unknown","실행 확인 중"],["EvidenceVerified=True","근거 검증 완료"],["EvidenceVerified=False","근거 미검증"],["EvidenceVerified=Unknown","근거 확인 중"] ]
function runCandidate(row:ProfilingRun,key:string) {
  const candidate=row.spec.candidate
  return candidate&&typeof candidate==="object"&&key in candidate?String((candidate as Record<string,unknown>)[key]):"정보 없음"
}
function runStatus(row:ProfilingRun) {
  const evidence=row.status.conditions?.find(item=>item.type==="EvidenceVerified")
  const execution=row.status.conditions?.find(item=>item.type==="ExecutionComplete")
  return evidence?.status==="True"?"근거 검증 완료":execution?.status==="True"?"실행 완료 · 근거 미검증":execution?.status==="False"?"실행 미완료":"상태 확인 중"
}
export function MeasurementsPage() {
  const {config}=useSession();const params=useSearchParams(),router=useRouter()
  const namespace=params.get("namespace")??config.namespace
  const allowed=new URLSearchParams();["q","offset","limit","condition"].forEach(key=>{const value=params.get(key);if(value!==null)allowed.set(key,value)});if(!allowed.has("limit"))allowed.set("limit",String(config.pageSize))
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${allowed}`)
  return <div className="page"><h1>측정 결과</h1><QueryWindow path="/measurements" page={query.data?.page} extra={<label>상태<select value={params.get("condition")??""} onChange={event=>{const next=new URLSearchParams(params.toString());if(event.target.value)next.set("condition",event.target.value);else next.delete("condition");next.set("offset","0");router.replace(`/measurements?${next}`)}}>{!runFilters.some(([value])=>value===(params.get("condition")??""))&&<option value={params.get("condition")!}>기존 상세 조건</option>}{runFilters.map(([value,label])=><option key={value} value={value}>{label}</option>)}</select></label>}/><ReadRegion title="등록된 측정" query={query}>{query.data&&<><DataTable<ProfilingRun> data={query.data.runs??[]} columns={[
    {key:"name",label:"측정",value:row=><Link href={`/measurements/profiling-runs/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`}>{row.name}</Link>,sortValue:row=>row.name},
    {key:"model",label:"모델",value:row=>runCandidate(row,"modelSpec")},
    {key:"device",label:"장비",value:row=>runCandidate(row,"deviceClass")},
    {key:"status",label:"상태",value:runStatus},
    {key:"detail",label:"상세",value:row=><SourceJSON value={row}/>},
  ]}/><SourceJSON value={query.data}/><SourceExport value={{conditions:{namespace,...Object.fromEntries(allowed)},source:query.data}} filename="chill-profiling-runs.json"/></>}</ReadRegion><DetailsDialog label="이전 측정 직접 찾기" title="기존 측정 조회"><EvidenceLookup/></DetailsDialog><Link href="/measurements/check">측정 계획 확인</Link></div>
}

function EvidenceLookup() {
  const {config}=useSession();const router=useRouter()
  const [kind,setKind]=React.useState("profiling-runs"),[namespace,setNamespace]=React.useState(config.namespace),[name,setName]=React.useState(""),[uid,setUID]=React.useState("")
  return <form className="form-grid" onSubmit={event=>{event.preventDefault();router.push(`/measurements/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid?`?uid=${encodeURIComponent(uid)}`:""}`)}}><p>이전 자료의 정확한 참조를 알고 있을 때만 사용하세요. 결과 목록 조회는 아직 제공되지 않습니다.</p><label>자료 종류<select value={kind} onChange={event=>setKind(event.target.value)}><option value="profiling-runs">측정 근거</option><option value="batch-energy-optimizations">측정 계획 결과</option><option value="power-mode-transition-costs">전환 비용</option></select></label><label>저장 영역<input value={namespace} onChange={event=>setNamespace(event.target.value)} required/></label><label>등록 이름<input value={name} onChange={event=>setName(event.target.value)} required/></label><label>원본 UID (선택)<input value={uid} onChange={event=>setUID(event.target.value)}/></label><Button type="submit">자료 열기</Button></form>
}

export function MeasurementCheckPage() {
  const {config}=useSession();const [namespace,setNamespace]=React.useState(config.namespace),[source,setSource]=React.useState(""),[error,setError]=React.useState("")
  const mutation=useChillMutation()
  return <div className="page"><h1>측정 계획 확인</h1><p>기존 계획의 조건만 검사합니다. 측정을 등록하거나 실행하지 않습니다.</p><form className="space-y-3" onSubmit={event=>{event.preventDefault();let value:unknown;try{value=JSON.parse(source)}catch{setError("올바른 JSON 자료를 입력하세요.");return}if(!value||typeof value!=="object"||!("spec" in value)||!("metadata" in value)){setError("metadata와 spec이 포함된 기존 계획이 필요합니다.");return}setError("");mutation.mutate({path:`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations/preflight`,method:"POST",body:value})}}><label>저장 영역<input value={namespace} onChange={event=>setNamespace(event.target.value)} required/></label><label>기존 측정 계획 JSON<textarea value={source} onChange={event=>setSource(event.target.value)} required/></label><Button disabled={mutation.isPending} type="submit">조건 확인</Button></form>{error&&<p role="alert">{error}</p>}{mutation.error&&<div role="alert"><p>{mutation.error.unknownOutcome?"결과를 확인하지 못했습니다.":mutation.error.message}</p><SourceJSON value={mutation.error.data??mutation.error.body}/></div>}{mutation.data&&<><p>조건 확인 응답입니다. 측정 접수 또는 완료가 아닙니다.</p><SourceJSON title="조건 확인 결과" value={{status:mutation.data.status,source:mutation.data.data}}/></>}<Link href="/measurements">측정 목록</Link></div>
}

export function MeasurementDetail({kind,namespace,name}:{kind:string;namespace:string;name:string}) {
  const expectedUID=useSearchParams().get("uid")
  const passport=useChillQuery<Passport>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs/${encodeURIComponent(name)}/evidence-passport`,{enabled:kind==="profiling-runs"})
  const campaign=useChillQuery<CampaignResult>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations/${encodeURIComponent(name)}/result`,{enabled:kind==="batch-energy-optimizations"})
  if(kind!=="profiling-runs"&&kind!=="batch-energy-optimizations")return <Unavailable title="측정 결과"/>
  return <div className="page"><h1>{name} 측정 결과</h1>{kind==="profiling-runs"?<ReadRegion title="검증된 측정 근거" query={passport}>{passport.data&&<>{expectedUID&&passport.data.run.uid!==expectedUID?<p role="alert">같은 이름으로 다시 등록된 다른 측정입니다. 이전 자료로 사용할 수 없습니다.</p>:null}<dl><dt>완료 요청</dt><dd>{passport.data.measurement.runtime.completedCalls}건</dd><dt>처리량</dt><dd>{passport.data.measurement.runtime.throughputItemsPerSecond}건/초</dd><dt>평균 전력</dt><dd>{passport.data.measurement.power.watts?.mean??"값 없음"} W</dd></dl><DetailsDialog label="측정 조건과 근거" title="측정 상세"><p>측정 전력은 현재 운영 소비가 아닙니다.</p><SourceJSON title="실행 조건·검증·지연·전력 원본" value={passport.data}/></DetailsDialog><SourceExport value={{conditions:{kind,namespace,name,expectedUID},source:passport.data}} filename={`chill-passport-${name}.json`}/></>}</ReadRegion>:<ReadRegion title="측정 계획 결과" query={campaign}>{campaign.data&&<>{expectedUID&&campaign.data.campaign.uid!==expectedUID?<p role="alert">같은 이름으로 다시 등록된 다른 계획입니다. 이전 자료로 사용할 수 없습니다.</p>:null}<p>완료 측정 {campaign.data.campaign.completedTrials}건</p><DetailsDialog label="결과 자세히 보기" title="계획 결과와 근거"><SourceJSON value={campaign.data}/></DetailsDialog><SourceExport value={{conditions:{kind,namespace,name,expectedUID},source:campaign.data}} filename={`chill-result-${name}.json`}/></>}</ReadRegion>}<Link href={`/measurements?namespace=${encodeURIComponent(namespace)}`}>측정 목록</Link></div>
}
function Unavailable({title}:{title:string}) {
  return <div className="page"><h1>{title}</h1><p>연결 준비 중</p><Button disabled>아직 사용할 수 없습니다</Button></div>
}
export function ExperimentsPage() {
  return <div className="page"><h1>실험</h1><p>실험 생성과 실행 API 준비 중. 판단 기록 비교는 제공된 조건과 예상 및 실제 관측을 확인하며 실험 실행이나 성공 판정을 대신하지 않습니다.</p><Link href="/scheduler/history">판단 기록에서 비교할 두 조건 선택</Link></div>
}
