"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useChillMutation, useSession } from "@/lib/client"
import type { RunsView, ProfilingRun, Passport, NativeSample, MeasurementPreflight } from "@/lib/read-types"
import { codeText } from "@/lib/labels"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { DataTable, type DataColumn } from "@/components/data-table"

const runFilters:[string,string][]=[["","전체"],["ExecutionComplete=True","실행 완료"],["ExecutionComplete=False","실행 미완료"],["ExecutionComplete=Unknown","실행 확인 중"],["EvidenceVerified=True","근거 검증 완료"],["EvidenceVerified=False","근거 미검증"],["EvidenceVerified=Unknown","근거 확인 중"]]
function runCandidate(row:ProfilingRun,key:string) {
  const candidate=row.spec.candidate
  return candidate&&typeof candidate==="object"&&key in candidate?String((candidate as Record<string,unknown>)[key]):"정보 없음"
}
type RunListSpec = {targetNode?:string;measurement?:{batch?:number;cpuQuota?:string;workloadMode?:string}}
const listSpec=(row:ProfilingRun)=>row.spec as RunListSpec
// Only problems are worth a badge; a verified run is the normal case.
function runProblem(row:ProfilingRun):"failed"|"unverified"|"pending"|null {
  const evidence=row.status.conditions?.find(item=>item.type==="EvidenceVerified")?.status
  const execution=row.status.conditions?.find(item=>item.type==="ExecutionComplete")?.status
  return evidence==="True"?null:execution==="False"?"failed":execution==="True"?"unverified":"pending"
}
const problemBadge={failed:<StatusBadge tone="fail">실행 실패</StatusBadge>,unverified:<StatusBadge tone="warn">근거 미검증</StatusBadge>,pending:<StatusBadge tone="info">진행 중</StatusBadge>}
function problemSummary(rows:ProfilingRun[]) {
  const failed=rows.filter(row=>runProblem(row)==="failed").length,other=rows.filter(row=>runProblem(row)&&runProblem(row)!=="failed").length
  return failed?<StatusBadge tone="fail">실패 {failed}건</StatusBadge>:other?<StatusBadge tone="warn">확인 필요 {other}건</StatusBadge>:null
}
const day=(at:string)=>new Date(at).toLocaleDateString("ko-KR",{month:"long",day:"numeric"})
const groupKey=(row:ProfilingRun)=>[runCandidate(row,"modelSpec"),listSpec(row).targetNode??"정보 없음",runCandidate(row,"executionPath")] as const

// The list groups runs by what was measured: model, node and execution path.
function useRunList() {
  const {config}=useSession(),params=useSearchParams()
  const namespace=params.get("namespace")??config.namespace
  const allowed=new URLSearchParams();["q","offset","condition"].forEach(key=>{const value=params.get(key);if(value!==null)allowed.set(key,value)});allowed.set("limit",String(config.maxPageSize))
  return {namespace,allowed,query:useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${allowed}`)}
}

export function MeasurementRunsPage() {
  const params=useSearchParams(),router=useRouter(),[filterOpen,setFilterOpen]=React.useState(false)
  const {namespace,allowed,query}=useRunList()
  const data=query.data,page=data?.page,runs=data?.runs??[],condition=params.get("condition")??""
  function go(offset:number) {
    const next=new URLSearchParams(params.toString());next.set("offset",String(offset));router.replace(`/measurements/runs?${next}`)
  }
  const groups=new Map<string,ProfilingRun[]>()
  for(const row of runs){const key=JSON.stringify(groupKey(row));groups.set(key,[...(groups.get(key)??[]),row])}
  const models=[...new Set(runs.map(row=>runCandidate(row,"modelSpec")))].sort()
  const total=page?.total??runs.length,failed=runs.filter(row=>runProblem(row)==="failed").length
  const shared=new URLSearchParams(allowed);shared.delete("limit");shared.set("namespace",namespace)
  return <Screen back={["/measurements","측정"]} status={failed?<StatusBadge tone="fail">실패 {failed}건</StatusBadge>:undefined}
    title={!data?"개별 측정 회차":total?`개별 측정 회차 ${total.toLocaleString("ko-KR")}건이 있어요`:"조건에 맞는 측정이 없어요"}
    lede={data&&runs.length?`모델 ${models.length}개를 장비와 실행 방식 ${groups.size}가지로 쟀어요.`:"모델을 장비에서 실행해 잰 결과와 근거예요."}>
    <QueryGate query={query}>{data&&<>
      {models.map(model=><Rows key={model} label={model}>{[...groups].filter(([key])=>JSON.parse(key)[0]===model).sort(([a],[b])=>a.localeCompare(b)).map(([key,rows])=>{
        const [, node, path]=JSON.parse(key) as string[]
        const dates=rows.map(row=>row.creationTimestamp).sort(),first=day(dates[0]),last=day(dates[dates.length-1])
        const href=`/measurements/group?${new URLSearchParams([...shared,["model",model],["node",node],["path",path]])}`
        return <Row key={key} href={href} label={`${node} · ${path}`} hint={`${rows.length}번 측정 · ${first===last?first:`${first} – ${last}`}`} value={problemSummary(rows)}/>
      })}</Rows>)}
      {!runs.length&&<Empty title="조건에 맞는 측정이 없어요"/>}
      {page&&(page.offset>0||page.nextOffset!==undefined)&&<div className="grid gap-2"><p className="text-sm text-muted-foreground">한 번에 {page.limit}건씩 묶어 보여줘요. 지금은 {page.offset+1}–{page.offset+runs.length}번째예요.</p><div className="flex gap-2"><Button variant="outline" disabled={page.offset===0} onClick={()=>go(Math.max(0,page.offset-page.limit))}>이전 측정</Button><Button variant="outline" disabled={page.nextOffset===undefined} onClick={()=>go(page.nextOffset??0)}>다음 측정</Button></div></div>}
    </>}</QueryGate>
    <Rows label="더 하기">
      <RowDialog open={filterOpen} onOpenChange={setFilterOpen} label="찾기 조건" value={[params.get("q"),condition&&(runFilters.find(([value])=>value===condition)?.[1]??"이전 상세 조건")].filter(Boolean).join(" · ")||"전체"} title="찾기 조건" description="이름과 상태로 측정을 찾아요.">
        <RunFilterForm done={()=>setFilterOpen(false)}/>
      </RowDialog>
      <RowDialog label="이전 측정 직접 찾기" hint="정확한 등록 이름을 알 때" title="이전 측정 직접 찾기" description="이전 자료의 정확한 참조를 알고 있을 때만 사용하세요. 측정 계획 결과와 전환 비용은 아직 목록으로 볼 수 없어요."><EvidenceLookup/></RowDialog>
      <Row href="/measurements/check" label="측정 계획 확인" hint="기존 계획의 조건만 검사해요"/>
      {data&&<RowDialog label="자료 내보내기" title="자료 내보내기" description="지금 보이는 측정 목록을 파일로 받아요."><SourceExport value={{conditions:{namespace,...Object.fromEntries(allowed)},source:data}} filename="chill-profiling-runs.json"/><SourceJSON inline value={data}/></RowDialog>}
    </Rows>
  </Screen>
}

// One measured condition: the individual runs, split into loaded and idle-baseline runs.
export function MeasurementGroupPage() {
  const params=useSearchParams(),{query}=useRunList()
  const model=params.get("model")??"",node=params.get("node")??"",path=params.get("path")??""
  const back=new URLSearchParams(params.toString());["model","node","path"].forEach(key=>back.delete(key))
  const rows=(query.data?.runs??[]).filter(row=>{const [m,n,p]=groupKey(row);return m===model&&n===node&&p===path})
    .sort((a,b)=>(listSpec(a).measurement?.batch??0)-(listSpec(b).measurement?.batch??0)||Number(listSpec(a).measurement?.cpuQuota??0)-Number(listSpec(b).measurement?.cpuQuota??0)||a.creationTimestamp.localeCompare(b.creationTimestamp))
  const idle=(row:ProfilingRun)=>listSpec(row).measurement?.workloadMode==="idle-model-loaded"
  const section=(label:string,items:ProfilingRun[])=>items.length>0&&<Rows label={label}>{items.map(row=>{
    const m=listSpec(row).measurement,same=items.filter(other=>listSpec(other).measurement?.batch===m?.batch&&listSpec(other).measurement?.cpuQuota===m?.cpuQuota)
    return <Row key={row.uid} href={`/measurements/profiling-runs/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`}
      label={[m?.batch!==undefined&&!idle(row)?`한 번에 ${m.batch}건`:null,m?.cpuQuota?`CPU ${m.cpuQuota}개`:null].filter(Boolean).join(" · ")||row.name}
      hint={`${same.indexOf(row)+1}회차 · ${new Date(row.creationTimestamp).toLocaleString("ko-KR",{month:"long",day:"numeric",hour:"2-digit",minute:"2-digit"})}`}
      value={(()=>{const problem=runProblem(row);return problem?problemBadge[problem]:null})()}/>
  })}</Rows>
  return <Screen back={[`/measurements/runs?${back}`,"개별 측정 회차"]} status={problemSummary(rows)??undefined}
    title={query.data?rows.length?`${node}에서 ${path}로 ${rows.length}번 쟀어요`:"이 조건의 측정을 찾지 못했어요":"측정 결과"} lede={model}>
    <QueryGate query={query}>
      {section("부하를 걸고 잰 측정",rows.filter(row=>!idle(row)))}
      {section("모델만 올려두고 잰 기준 전력",rows.filter(idle))}
      {query.data?.page?.nextOffset!==undefined&&<p className="text-sm text-muted-foreground">측정이 많아 일부만 불러왔어요. 측정 결과 목록에서 다음 측정을 넘겨 보세요.</p>}
    </QueryGate>
  </Screen>
}

// Mounted only while its dialog is open, so it always starts from the submitted URL.
function RunFilterForm({done}:{done:()=>void}) {
  const params=useSearchParams(),router=useRouter()
  const [q,setQ]=React.useState(params.get("q")??""),[condition,setCondition]=React.useState(params.get("condition")??"")
  const choices=runFilters.some(([value])=>value===condition)?runFilters:[...runFilters,[condition,"이전 상세 조건"] as [string,string]]
  return <form className="grid gap-5" onSubmit={event=>{
    event.preventDefault()
    const next=new URLSearchParams(params.toString());next.set("q",q);next.delete("limit");next.set("offset","0");if(condition)next.set("condition",condition);else next.delete("condition")
    router.replace(`/measurements/runs?${next}`);done()
  }}>
    <label className="grid gap-1.5">이름<Input value={q} onChange={event=>setQ(event.target.value)} maxLength={256}/></label>
    <fieldset className="grid gap-2"><legend className="mb-1">상태</legend>{choices.map(([value,label])=><label key={value} className="flex items-center gap-2"><input type="radio" name="condition" checked={condition===value} onChange={()=>setCondition(value)}/>{label}</label>)}</fieldset>
    <Button type="submit">이 조건으로 찾기</Button>
  </form>
}

function EvidenceLookup() {
  const {config}=useSession();const router=useRouter()
  const [kind,setKind]=React.useState("profiling-runs"),[namespace,setNamespace]=React.useState(config.namespace),[name,setName]=React.useState(""),[uid,setUID]=React.useState("")
  return <form className="grid gap-4" onSubmit={event=>{event.preventDefault();router.push(`/measurements/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}${uid?`?uid=${encodeURIComponent(uid)}`:""}`)}}>
    <fieldset className="grid gap-2"><legend className="mb-1">자료 종류</legend>{[["profiling-runs","측정 근거"],["batch-energy-optimizations","측정 계획 결과"],["power-mode-transition-costs","전환 비용"]].map(([value,label])=><label key={value} className="flex items-center gap-2"><input type="radio" name="kind" checked={kind===value} onChange={()=>setKind(value)}/>{label}</label>)}</fieldset>
    <label className="grid gap-1.5">저장 영역<Input value={namespace} onChange={event=>setNamespace(event.target.value)} required/></label>
    <label className="grid gap-1.5">등록 이름<Input value={name} onChange={event=>setName(event.target.value)} required/></label>
    <label className="grid gap-1.5">원본 UID (선택)<Input value={uid} onChange={event=>setUID(event.target.value)}/></label>
    <Button type="submit">자료 열기</Button>
  </form>
}

// /measurements/new review and /measurements/check share this conclusion and plan summary; the raw plan stays in 원본.
export const preflightConclusion=(answer:MeasurementPreflight)=>answer.resolves?"이 조건으로 측정할 수 있어요":"이 조건으로는 측정할 수 없어요"
export function PreflightAnswer({answer}:{answer:MeasurementPreflight}) {
  if(!answer.resolves||!answer.plan)return <p role="alert" className="text-sm text-destructive">{answer.reason}</p>
  // CurvePlanSpec.activeProtocol keeps untyped fields open; ChillServer always writes both of these.
  const shared=answer.plan.shared,{warmupIterations,durationSeconds}=shared.activeProtocol,sizes=shared.plannedBatchSizes??[]
  return <Facts label="이렇게 측정해요" items={[
    ["한 번에 처리할 건수",sizes.length?`${sizes.join(", ")}건`:"자동으로 정해요"],
    ["크기마다 반복",`${answer.campaign.spec.trialsPerBatch}번`],
    ["한 회차 측정 구간",`예열 ${String(warmupIterations)}번 뒤 ${String(durationSeconds)}초`],
  ]}/>
}

// Funnel: storage area → plan → review, then the preflight answer.
export function MeasurementCheckPage() {
  const {config}=useSession(),mutation=useChillMutation()
  const [step,setStep]=React.useState(0),[namespace,setNamespace]=React.useState(config.namespace),[source,setSource]=React.useState(""),[plan,setPlan]=React.useState<object|null>(null),[error,setError]=React.useState("")
  const back:[string,string]=["/measurements","측정 결과"]
  const answer=mutation.data?.data as MeasurementPreflight|undefined
  if(mutation.data&&answer)return <Screen back={back} status={<StatusBadge tone={answer.resolves?"ok":"fail"}>{answer.resolves?"측정 가능":"측정 불가"}</StatusBadge>} title={preflightConclusion(answer)} lede="조건만 확인했어요. 측정을 접수하지는 않았어요."
    action={<Button variant="outline" onClick={()=>{mutation.reset();setStep(0)}}>다른 계획 확인하기</Button>}>
    <PreflightAnswer answer={answer}/>
    <Rows><RowDialog label="원본 보기" title="조건 확인 원본"><SourceJSON inline title="조건 확인 원본" value={{status:mutation.data.status,source:answer}}/></RowDialog></Rows>
  </Screen>
  function next() {
    if(step===0&&!namespace.trim()){setError("저장 영역을 입력해 주세요.");return}
    if(step===1){
      let value:unknown
      try{value=JSON.parse(source)}catch{setError("올바른 JSON 자료를 넣어 주세요.");return}
      if(!value||typeof value!=="object"||!("spec" in value)||!("metadata" in value)){setError("metadata와 spec이 들어 있는 기존 계획이 필요해요.");return}
      setPlan(value)
    }
    if(step===2){mutation.mutate({path:`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations/preflight`,method:"POST",body:plan});return}
    setError("");setStep(step+1)
  }
  const planName=plan&&"metadata" in plan&&plan.metadata&&typeof plan.metadata==="object"&&"name" in plan.metadata?String(plan.metadata.name):"이름 없음"
  return <Screen back={back} status={<span className="text-sm text-muted-foreground">{step+1} / 3</span>}
    title={["어느 저장 영역의 계획인가요?","확인할 측정 계획을 넣어 주세요","이 계획의 조건을 확인할까요?"][step]}
    lede={["기존 계획의 조건만 검사해요. 측정을 등록하거나 실행하지 않아요.","계획 파일을 고르거나 JSON을 붙여 넣어요.","조건만 검사하고 측정은 시작하지 않아요."][step]}
    action={<div className="flex gap-2">{step>0&&<Button variant="outline" onClick={()=>{setError("");mutation.reset();setStep(step-1)}}>이전</Button>}<Button className="flex-1" disabled={mutation.isPending} onClick={next}>{step===2?"조건 확인하기":"다음"}</Button></div>}>
    {step===0&&<label className="grid gap-1.5">저장 영역<Input value={namespace} onChange={event=>setNamespace(event.target.value)} required/></label>}
    {step===1&&<div className="grid gap-4">
      <label className="grid gap-1.5">계획 파일<Input type="file" accept="application/json,.json" onChange={async event=>{const file=event.currentTarget.files?.[0];if(file)setSource(await file.text())}}/></label>
      <label className="grid gap-1.5">또는 JSON 붙여 넣기<textarea value={source} onChange={event=>setSource(event.target.value)}/></label>
    </div>}
    {step===2&&<><Facts items={[["저장 영역",namespace],["계획 이름",planName]]}/><Rows><RowDialog label="계획 원본 보기" title="계획 원본"><SourceJSON inline value={plan}/></RowDialog></Rows></>}
    {error&&<p role="alert">{error}</p>}
    {mutation.error&&<div role="alert" className="notice"><p>{mutation.error.unknownOutcome?"결과를 확인하지 못했어요. 다시 보내기 전에 상태를 확인해 주세요.":mutation.error.message}</p><SourceJSON value={mutation.error.data??mutation.error.body}/></div>}
  </Screen>
}

// Fields the passport carries but the shared read type leaves open.
type RunSpec = {candidate?:{modelSpec?:string;preprocessor?:string;deviceClass?:string;executionPath?:string;powerMode?:string};targetNode?:string;measurement?:{cpuQuota?:string;cpuAffinity?:number[];durationSeconds?:number;dataset?:string}}
type Thermal = {samples?:number;activeSamples?:number;latchedSamples?:number;maximumTemperatureCelsius?:{maximum?:number};issues?:number}
type Coresidence = {continuous?:boolean;initialActivePods?:number;finalActivePods?:number;materialEvents?:number}
const num=(value:number,digits=1)=>value.toLocaleString("ko-KR",{maximumFractionDigits:digits})
const plain=(code:string)=>codeText[code]?.[0]??code
const conditionTrue=(data:Passport,type:string)=>data.run.conditions?.find(condition=>condition.type===type)?.status==="True"
const verified=(data:Passport)=>conditionTrue(data,"ExecutionComplete")&&conditionTrue(data,"EvidenceVerified")
// One row per profile batch execution; stage spans are the batch's, recordIDs and units are per request.
const sampleColumns:DataColumn<NativeSample>[]=[
  {key:"batch",label:"배치",value:row=>`${String(row.batchID)} · ${row.batchSize}건`,sortValue:row=>row.batchSize},
  {key:"records",label:"데이터셋 기록",value:row=>row.recordIDs?.join(", ")||"없음"},
  {key:"units",label:"단위 수",value:row=>row.units?.join(", ")||"없음"},
  {key:"stages",label:"전처리 / 추론 / 후처리 ms",value:row=>`${num(row.preprocessMs,2)} / ${num(row.backendCallMs,2)} / ${num(row.postprocessMs,2)}`,sortValue:row=>row.backendCallMs},
  {key:"firstToken",label:"첫 토큰 ms",value:row=>row.firstTokenMs===undefined?"생성형 아님":num(row.firstTokenMs,2)},
  {key:"service",label:"runner 교환 / 요청 ms",value:row=>`${num(row.runnerServiceMs,2)} / ${num(row.nativeRequestMs,2)}`,sortValue:row=>row.nativeRequestMs},
]
function measuredWhat(data:Passport) {
  const spec=data.run.spec as RunSpec, r=data.measurement.runtime
  const at=new Date(r.measurementStartedAt).toLocaleString("ko-KR",{dateStyle:"medium",timeStyle:"short"})
  return [spec.candidate?.modelSpec, spec.targetNode ?? data.measurement.power.source.nodeName, plain(r.provider), `${at}에 ${num(r.elapsedSeconds)}초 동안`].filter(Boolean).join(" · ")
}
// One measurement, read top to bottom: how fast, how much power, under what conditions, and whether to trust it.
function PassportSummary({data}:{data:Passport}) {
  const r=data.measurement.runtime, p=data.measurement.power, l=r.latencyMilliseconds
  const spec=data.run.spec as RunSpec, thermal=data.measurement.thermal as Thermal|undefined, pods=data.measurement.workload as Coresidence|undefined
  const throttled=(thermal?.activeSamples??0)+(thermal?.latchedSamples??0)
  const podsChanged=!!pods&&((pods.materialEvents??0)>0||pods.initialActivePods!==pods.finalActivePods)
  return <>
    <Facts label="처리 성능" items={[
      ["처리량",`초당 ${num(r.throughputItemsPerSecond)}건`],
      ["응답 시간 (절반)",`${num(l.p50)}ms 이하`],
      ["응답 시간 (95%)",`${num(l.p95)}ms 이하`],
      ["응답 시간 (99%)",`${num(l.p99)}ms 이하`],
      ["처리한 요청",`${r.completedCalls.toLocaleString("ko-KR")}건 · 한 번에 ${r.batch}건씩`],
    ]}/>
    <Facts label="전력 (노드 전체)" items={[
      ["평균",p.watts?`${num(p.watts.mean)} W`:"값 없음"],
      ["가장 낮을 때 – 높을 때",p.watts?`${num(p.watts.minimum)} – ${num(p.watts.maximum)} W`:"값 없음"],
      ["계측",`${p.source.nodeName ?? "노드"} 전원 계측기 · ${p.samples}번 측정${p.failures?`, ${p.failures}번 실패`:""}`],
    ]}/>
    <p className="-mt-4 px-1 text-sm text-muted-foreground">{pods?.initialActivePods!==undefined?`같은 노드에서 함께 돌던 Pod ${pods.initialActivePods}개를 포함한 값이에요. `:""}지금 운영 중 소비와는 달라요.</p>
    <Facts label="측정 조건" items={[
      ["모델",`${spec.candidate?.modelSpec ?? "정보 없음"}${spec.candidate?.preprocessor?` · 전처리기 ${spec.candidate.preprocessor}`:""}`],
      ["데이터셋",spec.measurement?.dataset ?? "없음"],
      ["장비",`${spec.targetNode ?? p.source.nodeName ?? "정보 없음"}${spec.candidate?.deviceClass?` · ${spec.candidate.deviceClass}`:""}`],
      ["측정한 실행 환경",`${plain(r.provider)} · ${plain(r.family)} ${r.version} · ${r.architecture}`],
      ["CPU",spec.measurement?.cpuQuota?`${spec.measurement.cpuQuota}개${spec.measurement.cpuAffinity?.length?` (코어 ${spec.measurement.cpuAffinity.join(", ")})`:""}`:"제한 없음"],
      ["예열",`${r.warmupIterations}번 실행 후 측정`],
    ]}/>
    <Facts label="믿을 수 있는지" items={[
      ["실행과 근거",verified(data)?<StatusBadge tone="ok">검증 완료</StatusBadge>:<StatusBadge tone="warn">확인 필요</StatusBadge>],
      ["발열",thermal?throttled?`성능 제한이 ${throttled}번 걸렸어요`:`성능 제한 없음${thermal.maximumTemperatureCelsius?.maximum!==undefined?` · 최고 ${thermal.maximumTemperatureCelsius.maximum}°C`:""}`:"관측 없음"],
      ["다른 작업",pods?podsChanged?"측정 중 같은 노드의 Pod가 바뀌었어요":"측정 중 같은 노드의 Pod 변화가 없었어요":"관측 없음"],
    ]}/>
  </>
}

export function MeasurementDetail({kind,namespace,name}:{kind:string;namespace:string;name:string}) {
  const expectedUID=useSearchParams().get("uid")
  const passport=useChillQuery<Passport>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs/${encodeURIComponent(name)}/evidence-passport`,{enabled:kind==="profiling-runs"})
  const back:[string,string]=[`/measurements/runs?namespace=${encodeURIComponent(namespace)}`,"개별 측정 회차"]
  if(kind==="profiling-runs"){
    const data=passport.data,other=!!data&&!!expectedUID&&data.run.uid!==expectedUID
    return <Screen back={back} status={other?<StatusBadge tone="fail">다른 측정</StatusBadge>:data?<StatusBadge tone={verified(data)?"ok":"warn"}>{verified(data)?"검증된 측정":"검증 확인 필요"}</StatusBadge>:undefined}
      title={!data?`${name} 측정 결과`:other?"같은 이름으로 다시 등록된 다른 측정이에요":`요청 1건을 평균 ${num(data.measurement.runtime.latencyMilliseconds.mean)}ms에 처리했어요`}
      lede={other?"이전 자료로 사용할 수 없어요.":data?measuredWhat(data):name}>
      <QueryGate query={passport}>{data&&!other&&<PassportSummary data={data}/>}
        {data&&<Rows>
          {!!data.measurement.runtime.nativeSamples?.length&&<RowDialog label="배치 실행 기록" hint="데이터셋 기록, 단위 수, 단계 시간" value={`${data.measurement.runtime.nativeSamples.length}번`} title="배치 실행 기록" description="배치 단계 시간은 같은 배치의 요청이 함께 써요. 단위 수는 분석용 관측값이에요.">
            <DataTable data={data.measurement.runtime.nativeSamples} columns={sampleColumns}/>
          </RowDialog>}
          <RowDialog label="원본 자료" hint="실행 조건, 검증, 지연, 전력의 원래 값" title="측정 원본 자료" description="측정 전력은 현재 운영 소비가 아니에요.">
          <SourceExport value={{conditions:{kind,namespace,name,expectedUID},source:data}} filename={`chill-passport-${name}.json`}/>
          <SourceJSON inline title="실행 조건·검증·지연·전력 원본" value={data}/>
        </RowDialog></Rows>}
      </QueryGate>
    </Screen>
  }
  return <Screen back={back} status={<StatusBadge tone="idle">연결 준비 중</StatusBadge>} title="이 자료는 아직 볼 수 없어요" lede="이 자료 종류는 연결을 준비하고 있어요."/>
}

export function ExperimentsPage() {
  return <Screen back={["/records","기록"]} status={<StatusBadge tone="idle">연결 준비 중</StatusBadge>} title="실험 기능은 아직 준비 중이에요"
    lede="지금은 판단 기록 두 개를 골라 조건과 예상, 실제 결과를 나란히 볼 수 있어요. 실험 실행이나 성공 판정을 대신하지는 않아요."
    action={<Button asChild><Link href="/scheduler/history">판단 기록에서 두 기록 고르기</Link></Button>}/>
}
