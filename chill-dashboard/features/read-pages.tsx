"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useChillMutation, useSession } from "@/lib/client"
import type { CampaignResult, RunsView, ProfilingRun, Passport } from "@/lib/read-types"
import { QueryGate, SourceJSON, SourceExport, StatusBadge, statusLabel } from "@/components/read-region"
import { Screen, Rows, Row, RowDialog, Facts, Empty } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

const runFilters:[string,string][]=[["","전체"],["ExecutionComplete=True","실행 완료"],["ExecutionComplete=False","실행 미완료"],["ExecutionComplete=Unknown","실행 확인 중"],["EvidenceVerified=True","근거 검증 완료"],["EvidenceVerified=False","근거 미검증"],["EvidenceVerified=Unknown","근거 확인 중"]]
function runCandidate(row:ProfilingRun,key:string) {
  const candidate=row.spec.candidate
  return candidate&&typeof candidate==="object"&&key in candidate?String((candidate as Record<string,unknown>)[key]):"정보 없음"
}
function runStatus(row:ProfilingRun) {
  const evidence=row.status.conditions?.find(item=>item.type==="EvidenceVerified")
  const execution=row.status.conditions?.find(item=>item.type==="ExecutionComplete")
  return evidence?.status==="True"?<StatusBadge tone="ok">근거 검증 완료</StatusBadge>:execution?.status==="True"?<StatusBadge tone="warn">실행 완료 · 근거 미검증</StatusBadge>:execution?.status==="False"?<StatusBadge tone="fail">실행 미완료</StatusBadge>:<StatusBadge tone="idle">상태 확인 중</StatusBadge>
}

export function MeasurementsPage() {
  const {config}=useSession(),params=useSearchParams(),router=useRouter(),[filterOpen,setFilterOpen]=React.useState(false)
  const namespace=params.get("namespace")??config.namespace
  const allowed=new URLSearchParams();["q","offset","limit","condition"].forEach(key=>{const value=params.get(key);if(value!==null)allowed.set(key,value)});if(!allowed.has("limit"))allowed.set("limit",String(config.pageSize))
  const query=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${allowed}`)
  const data=query.data,page=data?.page,runs=data?.runs??[],condition=params.get("condition")??""
  function go(offset:number) {
    const next=new URLSearchParams(params.toString());next.set("offset",String(offset));router.replace(`/measurements?${next}`)
  }
  const total=page?.total??runs.length
  return <Screen back={["/records","기록"]} title={!data?"측정 결과":total?`측정 ${total}건이 있어요`:"조건에 맞는 측정이 없어요"} lede="모델을 장비에서 실행해 잰 결과와 근거예요.">
    <Rows label="찾기 조건">
      <RowDialog open={filterOpen} onOpenChange={setFilterOpen} label="찾기 조건" value={[params.get("q"),condition&&(runFilters.find(([value])=>value===condition)?.[1]??"이전 상세 조건")].filter(Boolean).join(" · ")||"전체"} title="찾기 조건" description="이름과 상태로 측정을 찾아요.">
        <RunFilterForm done={()=>setFilterOpen(false)}/>
      </RowDialog>
      <RowDialog label="이전 측정 직접 찾기" hint="정확한 등록 이름을 알 때" title="이전 측정 직접 찾기" description="이전 자료의 정확한 참조를 알고 있을 때만 사용하세요. 측정 계획 결과와 전환 비용은 아직 목록으로 볼 수 없어요."><EvidenceLookup/></RowDialog>
      <Row href="/measurements/check" label="측정 계획 확인" hint="기존 계획의 조건만 검사해요"/>
      {data&&<RowDialog label="자료 내보내기" title="자료 내보내기" description="지금 보이는 측정 목록을 파일로 받아요."><SourceExport value={{conditions:{namespace,...Object.fromEntries(allowed)},source:data}} filename="chill-profiling-runs.json"/><SourceJSON inline value={data}/></RowDialog>}
    </Rows>
    <QueryGate query={query}>{data&&<>
      {runs.length?<Rows label="측정">{runs.map(row=><Row key={row.uid} href={`/measurements/profiling-runs/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`} label={row.name} hint={`${runCandidate(row,"modelSpec")} · ${runCandidate(row,"deviceClass")}`} value={runStatus(row)}/>)}</Rows>:<Empty title="조건에 맞는 측정이 없어요"/>}
      {page&&(page.offset>0||page.nextOffset!==undefined)&&<div className="flex gap-2"><Button variant="outline" disabled={page.offset===0} onClick={()=>go(Math.max(0,page.offset-page.limit))}>이전</Button><Button variant="outline" disabled={page.nextOffset===undefined} onClick={()=>go(page.nextOffset??0)}>다음</Button></div>}
    </>}</QueryGate>
  </Screen>
}

// Mounted only while its dialog is open, so it always starts from the submitted URL.
function RunFilterForm({done}:{done:()=>void}) {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const [q,setQ]=React.useState(params.get("q")??""),[condition,setCondition]=React.useState(params.get("condition")??""),[limit,setLimit]=React.useState(params.get("limit")??String(config.pageSize))
  const choices=runFilters.some(([value])=>value===condition)?runFilters:[...runFilters,[condition,"이전 상세 조건"] as [string,string]]
  return <form className="grid gap-5" onSubmit={event=>{
    event.preventDefault()
    const next=new URLSearchParams(params.toString());next.set("q",q);next.set("limit",limit);next.set("offset","0");if(condition)next.set("condition",condition);else next.delete("condition")
    router.replace(`/measurements?${next}`);done()
  }}>
    <label className="grid gap-1.5">이름<Input value={q} onChange={event=>setQ(event.target.value)} maxLength={256}/></label>
    <fieldset className="grid gap-2"><legend className="mb-1">상태</legend>{choices.map(([value,label])=><label key={value} className="flex items-center gap-2"><input type="radio" name="condition" checked={condition===value} onChange={()=>setCondition(value)}/>{label}</label>)}</fieldset>
    <label className="grid gap-1.5">한 번에 볼 개수<Input type="number" min={1} max={config.maxPageSize} value={limit} onChange={event=>setLimit(event.target.value)} required/></label>
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

// Funnel: storage area → plan → review, then the preflight answer.
export function MeasurementCheckPage() {
  const {config}=useSession(),mutation=useChillMutation()
  const [step,setStep]=React.useState(0),[namespace,setNamespace]=React.useState(config.namespace),[source,setSource]=React.useState(""),[plan,setPlan]=React.useState<object|null>(null),[error,setError]=React.useState("")
  const back:[string,string]=["/measurements","측정 결과"]
  if(mutation.data)return <Screen back={back} status={<StatusBadge tone="info">조건 확인 응답</StatusBadge>} title="조건 확인 결과를 받았어요" lede="측정을 접수하거나 끝낸 것은 아니에요."
    action={<Button variant="outline" onClick={()=>{mutation.reset();setStep(0)}}>다른 계획 확인하기</Button>}>
    <Rows><RowDialog label="확인 결과 보기" title="조건 확인 결과"><SourceJSON inline title="조건 확인 결과" value={{status:mutation.data.status,source:mutation.data.data}}/></RowDialog></Rows>
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
type RunSpec = {candidate?:{modelSpec?:string;deviceClass?:string;executionPath?:string;powerMode?:string};targetNode?:string;measurement?:{cpuQuota?:string;cpuAffinity?:number[];durationSeconds?:number;inputGenerator?:string}}
type Thermal = {samples?:number;activeSamples?:number;latchedSamples?:number;maximumTemperatureCelsius?:{maximum?:number};issues?:number}
type Coresidence = {continuous?:boolean;initialActivePods?:number;finalActivePods?:number;materialEvents?:number}
const num=(value:number,digits=1)=>value.toLocaleString("ko-KR",{maximumFractionDigits:digits})
const providers:Record<string,string>={CUDAExecutionProvider:"GPU (CUDA)",CPUExecutionProvider:"CPU",TensorrtExecutionProvider:"GPU (TensorRT)"}
const conditionTrue=(data:Passport,type:string)=>data.run.conditions?.find(condition=>condition.type===type)?.status==="True"
const verified=(data:Passport)=>conditionTrue(data,"ExecutionComplete")&&conditionTrue(data,"EvidenceVerified")&&data.measurement.runtime.outputValidation.passed
function measuredWhat(data:Passport) {
  const spec=data.run.spec as RunSpec, r=data.measurement.runtime
  const at=new Date(r.measurementStartedAt).toLocaleString("ko-KR",{dateStyle:"medium",timeStyle:"short"})
  return [spec.candidate?.modelSpec, spec.targetNode ?? data.measurement.power.source.nodeName, providers[r.provider] ?? r.provider, `${at}에 ${num(r.elapsedSeconds)}초 동안`].filter(Boolean).join(" · ")
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
      ["모델",spec.candidate?.modelSpec ?? "정보 없음"],
      ["장비",`${spec.targetNode ?? p.source.nodeName ?? "정보 없음"}${spec.candidate?.deviceClass?` · ${spec.candidate.deviceClass}`:""}`],
      ["실행 방식",`${providers[r.provider] ?? r.provider} · ${r.family} ${r.version}`],
      ["CPU",spec.measurement?.cpuQuota?`${spec.measurement.cpuQuota}개${spec.measurement.cpuAffinity?.length?` (코어 ${spec.measurement.cpuAffinity.join(", ")})`:""}`:"제한 없음"],
      ["예열",`${r.warmupIterations}번 실행 후 측정`],
    ]}/>
    <Facts label="믿을 수 있는지" items={[
      ["실행과 근거",verified(data)?<StatusBadge tone="ok">검증 완료</StatusBadge>:<StatusBadge tone="warn">확인 필요</StatusBadge>],
      ["출력 확인",r.outputValidation.passed?"모든 응답이 올바른 형태였어요":"올바르지 않은 응답이 있었어요"],
      ["발열",thermal?throttled?`성능 제한이 ${throttled}번 걸렸어요`:`성능 제한 없음${thermal.maximumTemperatureCelsius?.maximum!==undefined?` · 최고 ${thermal.maximumTemperatureCelsius.maximum}°C`:""}`:"관측 없음"],
      ["다른 작업",pods?podsChanged?"측정 중 같은 노드의 Pod가 바뀌었어요":"측정 중 같은 노드의 Pod 변화가 없었어요":"관측 없음"],
    ]}/>
  </>
}

export function MeasurementDetail({kind,namespace,name}:{kind:string;namespace:string;name:string}) {
  const expectedUID=useSearchParams().get("uid")
  const passport=useChillQuery<Passport>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs/${encodeURIComponent(name)}/evidence-passport`,{enabled:kind==="profiling-runs"})
  const campaign=useChillQuery<CampaignResult>(`v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations/${encodeURIComponent(name)}/result`,{enabled:kind==="batch-energy-optimizations"})
  const back:[string,string]=[`/measurements?namespace=${encodeURIComponent(namespace)}`,"측정 결과"]
  if(kind==="profiling-runs"){
    const data=passport.data,other=!!data&&!!expectedUID&&data.run.uid!==expectedUID
    return <Screen back={back} status={other?<StatusBadge tone="fail">다른 측정</StatusBadge>:data?<StatusBadge tone={verified(data)?"ok":"warn"}>{verified(data)?"검증된 측정":"검증 확인 필요"}</StatusBadge>:undefined}
      title={!data?`${name} 측정 결과`:other?"같은 이름으로 다시 등록된 다른 측정이에요":`요청 1건을 평균 ${num(data.measurement.runtime.latencyMilliseconds.mean)}ms에 처리했어요`}
      lede={other?"이전 자료로 사용할 수 없어요.":data?measuredWhat(data):name}>
      <QueryGate query={passport}>{data&&!other&&<PassportSummary data={data}/>}
        {data&&<Rows><RowDialog label="원본 자료" hint="실행 조건, 검증, 지연, 전력의 원래 값" title="측정 원본 자료" description="측정 전력은 현재 운영 소비가 아니에요.">
          <SourceExport value={{conditions:{kind,namespace,name,expectedUID},source:data}} filename={`chill-passport-${name}.json`}/>
          <SourceJSON inline title="실행 조건·검증·지연·전력 원본" value={data}/>
        </RowDialog></Rows>}
      </QueryGate>
    </Screen>
  }
  if(kind==="batch-energy-optimizations"){
    const data=campaign.data,other=!!data&&!!expectedUID&&data.campaign.uid!==expectedUID,failing=data?.campaign.conditions?.filter(condition=>condition.status==="False").length??0
    return <Screen back={back} status={other?<StatusBadge tone="fail">다른 계획</StatusBadge>:undefined}
      title={!data?`${name} 측정 계획 결과`:other?"같은 이름으로 다시 등록된 다른 계획이에요":data.result===null?"아직 완료된 결과가 없어요":"측정 계획 결과가 나왔어요"}
      lede={other?"이전 자료로 사용할 수 없어요.":data?.result===null?`${name} · 상태와 원인을 확인해 보세요.`:name}>
      <QueryGate query={campaign}>{data&&<>
        <Facts items={[["상태",data.campaign.phase?statusLabel(data.campaign.phase):"확인 안 됨"],["완료 측정",`${data.campaign.completedTrials}건`],["확인이 필요한 조건",`${failing}개`]]}/>
        <Rows><RowDialog label="결과와 근거" hint="상태 원인과 결과 원본" title="계획 결과와 근거">
          {data.campaign.conditions?.map(condition=><p key={condition.type} role={condition.status==="False"?"alert":undefined}><strong>{condition.reason}</strong>: {condition.message}</p>)}
          {data.result===null&&<p>완료 결과가 아직 없어요. 위 상태와 원인을 확인하세요.</p>}
          <SourceExport value={{conditions:{kind,namespace,name,expectedUID},source:data}} filename={`chill-result-${name}.json`}/>
          <SourceJSON inline value={data}/>
        </RowDialog></Rows>
      </>}</QueryGate>
    </Screen>
  }
  return <Screen back={back} status={<StatusBadge tone="idle">연결 준비 중</StatusBadge>} title="이 자료는 아직 볼 수 없어요" lede="이 자료 종류는 연결을 준비하고 있어요."/>
}

export function ExperimentsPage() {
  return <Screen back={["/records","기록"]} status={<StatusBadge tone="idle">연결 준비 중</StatusBadge>} title="실험 기능은 아직 준비 중이에요"
    lede="지금은 판단 기록 두 개를 골라 조건과 예상, 실제 결과를 나란히 볼 수 있어요. 실험 실행이나 성공 판정을 대신하지는 않아요."
    action={<Button asChild><Link href="/scheduler/history">판단 기록에서 두 기록 고르기</Link></Button>}/>
}
