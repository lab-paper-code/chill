"use client"

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useChillMutation, useSession } from "@/lib/client"
import type { RunsView, Passport, NativeSample, MeasurementPreflight } from "@/lib/read-types"
import { formatMeasurementNumber as num, formatMeasurementTime, runFailure, runStatusObject, exactRun, safeMeasurementReturn } from "@/lib/measurements"
import { currentRunCondition, measurementAge, runAttempt, runSpec } from "@/lib/measurement-coverage"
import { codeText } from "@/lib/labels"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Screen, Rows, RowDialog, Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { DataTable, type DataColumn } from "@/components/data-table"

import { MeasurementNavigation, MeasurementStatus } from "@/components/measurement-navigation"
import { AssessmentDetails } from "@/features/measurement-plans"

// /measurements/new review and /measurements/check share this conclusion and plan summary; the raw plan stays in 원본.
export const preflightConclusion=(answer:MeasurementPreflight)=>answer.resolves?"이 조건으로 측정할 수 있어요":"이 조건으로는 측정할 수 없어요"
export function PreflightAnswer({answer}:{answer:MeasurementPreflight}) {
  const shared=answer.plan?.shared,sizes=shared?.plannedBatchSizes??[],batch=answer.batchPlan
  return <div className="space-y-4">
    {batch&&<><Facts label="실행 범위" items={[
      ["선택한 처리 건수",batch.requestedBatches.join(", ")||"미지정"],
      ["요청 가능한 처리 건수",batch.requestableBatches.join(", ")||"없음"],
      ["실행 예정 처리 건수",batch.plannedBatches.join(", ")||"미확정"],
      ["이미 측정한 처리 건수",batch.measuredBatches.join(", ")||"없음"],
      ["측정 중인 처리 건수",batch.inProgressBatches.join(", ")||"없음"],
      ["추가 측정할 처리 건수",batch.missingBatches.join(", ")||"없음"],
      ["일부 건수만 선택",batch.subsetAllowed?"가능":"등록된 건수를 모두 측정해요"],
      ["기존 측정과 조건 확인",batch.conditionMatch==="exact"?"실행 조건까지 같아요":batch.conditionMatch==="planned-only"?"요청 조건은 같지만 실제 실행 조건은 아직 확인하지 못했어요":"확인 불가"],
      ["선택 범위보다 먼저 완료",batch.searchMayStopEarly?"필요한 결과가 모이면 일부 실행 전에 마칠 수 있어요":"예정 범위를 실행해요"],
    ]}/>{!!batch.reasons?.length&&<ul className="space-y-2 text-sm">{batch.reasons.map((reason,index)=><li key={index}>{reason.message.replace(/\s*\(FixtureOnly\)/g,"")}<details><summary className="cursor-pointer py-2">원문 코드</summary>{reason.code}</details></li>)}</ul>}</>}
    {!answer.resolves||!shared?<p role="alert" className="text-sm text-destructive">{answer.reason??"실행 계획이 제공되지 않았어요."}</p>:<Facts label="이렇게 측정해요" items={[
      ["한 번에 처리할 건수",sizes.length?`${sizes.join(", ")}건`:"경로 선언 범위"],
      ["크기마다 반복",`${answer.campaign.spec.trialsPerBatch}번`],
      ["한 회차 측정 구간",`예열 ${String(shared.activeProtocol.warmupIterations)}번 뒤 ${String(shared.activeProtocol.durationSeconds)}초`],
      ["예상 총 소요 시간", "서버가 전체 예상 시간을 제공하지 않았어요. 예열·장비 준비·반복 횟수에 따라 달라져요."],
      ["서빙 영향", "서버가 영향 범위를 제공하지 않았어요. 요청 전 실행 장비와 CPU 사용 범위를 확인하세요."],
    ]}/>}
  </div>
}

// Funnel: storage area → plan → review, then the preflight answer.
export function MeasurementCheckPage() {
  const {config}=useSession(),mutation=useChillMutation()
  const [step,setStep]=React.useState(0),[namespace,setNamespace]=React.useState(config.namespace),[source,setSource]=React.useState(""),[plan,setPlan]=React.useState<object|null>(null),[error,setError]=React.useState("")
  const back:[string,string]=["/measurements","측정 결과"]
  const answer=mutation.data?.data as MeasurementPreflight|undefined
  if(mutation.data&&answer)return <Screen back={back} status={<StatusBadge tone={answer.resolves?"ok":"fail"}>{answer.resolves?"측정 가능":"측정 불가"}</StatusBadge>} title={preflightConclusion(answer)} lede="조건만 확인했어요. 측정을 접수하지는 않았어요."
    action={<Button variant="outline" onClick={()=>{mutation.reset();setStep(0)}}>다른 계획 확인하기</Button>}>
    <MeasurementNavigation active="requests" namespace={config.namespace}/>
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
    <MeasurementNavigation active="requests" namespace={config.namespace}/>
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
type Thermal = {observationAvailableSamples?:number;activeSamples?:number;latchedSamples?:number;maximumTemperatureCelsius?:{maximum?:number};issues?:unknown}
type Coresidence = {continuous?:boolean;initialActivePods?:number;finalActivePods?:number;materialEvents?:number;coResidents?:{namespace:string;name:string;chillPlaced:boolean}[]}
const plain=(code:string)=>codeText[code]?.[0]??code
// One row per profile batch execution; stage spans are the batch's, recordIDs and units are per request.
const sampleColumns:DataColumn<NativeSample>[]=[
  {key:"batch",label:"배치",value:row=>`${String(row.batchID)} / ${row.batchSize}건`,sortValue:row=>row.batchSize},
  {key:"records",label:"데이터셋 기록",value:row=>row.recordIDs?.join(", ")||"없음"},
  {key:"units",label:"단위 수",value:row=>row.units?.join(", ")||"없음"},
  {key:"stages",label:"전처리 / 추론 / 후처리 ms",value:row=>`${num(row.preprocessMs,"ms")} / ${num(row.backendCallMs,"ms")} / ${num(row.postprocessMs,"ms")}`,sortValue:row=>row.backendCallMs},
  {key:"firstToken",label:"첫 토큰 ms",value:row=>row.firstTokenMs===undefined?"생성형 아님":num(row.firstTokenMs,"ms")},
  {key:"service",label:"runner 교환 / 요청 ms",value:row=>`${num(row.runnerServiceMs,"ms")} / ${num(row.nativeRequestMs,"ms")}`,sortValue:row=>row.nativeRequestMs},
]
function measuredWhat(data:Passport) {
  const spec=data.run.spec as RunSpec, r=data.measurement.runtime
  const at=formatMeasurementTime(r.measurementStartedAt)
  return [spec.candidate?.modelSpec, spec.targetNode ?? data.measurement.power.source.nodeName, plain(r.provider), `${at}에 ${num(r.elapsedSeconds)}초 동안`].filter(Boolean).join(" / ")
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
      ["p50",`${num(l.p50,"ms")} ms`],
      ["p95",`${num(l.p95,"ms")} ms`],
      ["p99",`${num(l.p99,"ms")} ms`],
      ["지연 표본 수 N",`${l.count}개`],
      ["처리한 요청",`${r.completedCalls.toLocaleString("ko-KR")}건 / 한 번에 ${r.batch}건씩`],
    ]}/>
    <Facts label="전력 (노드 전체)" items={[
      ["평균",p.watts?`${num(p.watts.mean,"W")} W`:"값 없음"],
      ["가장 낮을 때 – 높을 때",p.watts?`${num(p.watts.minimum,"W")} – ${num(p.watts.maximum,"W")} W`:"값 없음"],
      ["계측",`${p.source.nodeName ?? "노드"} 전원 계측기 / ${p.samples}번 측정${p.failures?`, ${p.failures}번 실패`:""}`],
      ["최대 표본 간격",`${num(p.maximumSampleGapSeconds)} s`],
      ["원본 시각",p.sourceTimestampAbsent?"관측 시각은 수집 시각":"원본 timestamp 제공"],
    ]}/>
    <p className="-mt-4 px-1 text-sm text-muted-foreground">노드 벽전력이며 장치 단독 전력이 아니에요. {pods?.initialActivePods!==undefined?`같은 노드에서 함께 돌던 Pod ${pods.initialActivePods}개를 포함한 값이에요. `:""}지금 운영 중 소비와는 달라요.</p>
    <Facts label="측정 조건" items={[
      ["모델",spec.candidate?.modelSpec??"정보 없음"],
      ["전처리기",spec.candidate?.preprocessor??"없음"],
      ["데이터셋",spec.measurement?.dataset ?? "없음"],
      ["장비",spec.targetNode??p.source.nodeName??"정보 없음"],
      ["장치 종류",spec.candidate?.deviceClass??"정보 없음"],
      ["측정한 실행 환경",`${plain(r.provider)} / ${plain(r.family)} ${r.version} / ${r.architecture}`],
      ["CPU",spec.measurement?.cpuQuota?`${spec.measurement.cpuQuota}개${spec.measurement.cpuAffinity?.length?` (코어 ${spec.measurement.cpuAffinity.join(", ")})`:""}`:"제한 없음"],
      ["예열",`${r.warmupIterations}번 실행 후 측정`],
    ]}/>
    <Facts label="관측 사실" items={[
      ["발열",thermal?.observationAvailableSamples ? `관측 ${thermal.observationAvailableSamples}개 / 제한 관측 ${throttled}개`:"관측 없음 / 정상 여부 미확인"],
      ["발열 기록",thermal?.issues?JSON.stringify(thermal.issues):"제공된 issue 없음 / 미관측 정상 판정 아님"],
      ["다른 작업",pods?podsChanged?"측정 중 같은 노드의 Pod가 바뀌었어요":"측정 중 같은 노드의 Pod 변화가 없었어요":"관측 없음"],
      ["CHILL이 배치하지 않은 Pod",pods?pods.coResidents?.filter(pod=>!pod.chillPlaced).map(pod=>`${pod.namespace}/${pod.name}`).join(", ")||"없음":"관측 없음"],
    ]}/>
  </>
}

export function MeasurementDetail({namespace,name}:{namespace:string;name:string}) {
  const params=useSearchParams(),expectedUID=params.get("uid")
  const [offset,setOffset]=React.useState(0)
  const back=safeMeasurementReturn(params.get("returnTo"),`/measurements/runs?namespace=${encodeURIComponent(namespace)}`)
  const lookup=useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${new URLSearchParams({q:name,limit:"200",offset:String(offset)})}`)
  const run=exactRun(lookup.data?.runs??[],namespace,name,expectedUID),failed=!!run&&currentRunCondition(run,"ExecutionComplete")?.status==="False"
  const noEvidence=failed&&!run?.status.evidence
  const passport=useChillQuery<Passport>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs/${encodeURIComponent(name)}/evidence-passport`,{enabled:!!run&&!noEvidence})
  const data=passport.data,other=!!data&&data.run.uid!==run?.uid
  const failure=run?runFailure(run):undefined
  const statusFacts=(value:unknown):[string,React.ReactNode][]=>{const object=runStatusObject(value);return object?Object.entries(object).map(([key,value])=>[({namespace:"저장 영역",name:"이름",uid:"식별자",nodeName:"실행 Node",podName:"실행 Pod",phase:"진행 상태",provider:"실행 제공자",architecture:"아키텍처",family:"런타임",version:"버전"} as Record<string,string>)[key]??key,typeof value==="object"?<SourceJSON key={key} value={value} title="세부 기록"/>:String(value)]):[["기록",value===undefined?"미제공":String(value)]]}
  return <Screen width="data" actionPlacement="header" back={[back,"측정으로 돌아가기"]} breadcrumbs={[{href:back,label:"측정 회차"},{label:name}]} title={`${name} 측정 결과`} status={<MeasurementStatus assessment={run?.assessment}/>} lede={data&&!other?measuredWhat(data):"실행 상태와 검증 근거는 별개예요."} action={<Button variant="outline" disabled={lookup.isFetching} onClick={()=>void lookup.refetch()}>갱신</Button>}>
    <MeasurementNavigation active="runs" namespace={namespace}/>
    <QueryGate query={lookup}>{lookup.data&&!run&&<div className="space-y-3"><p>이 조회 페이지에서 정확한 namespace/name/UID의 회차를 찾지 못했어요. 같은 이름의 다른 회차로 대체하지 않아요.</p>{lookup.data.page?.nextOffset!==undefined&&<Button variant="outline" onClick={()=>setOffset(lookup.data!.page!.nextOffset!)}>다음 조회 페이지에서 찾기</Button>}</div>}{run&&<>
      <Facts label="회차 조건" items={[
        ["모델 / 장치",`${runSpec(run).candidate?.modelSpec??"미기록"} / ${runSpec(run).candidate?.deviceClass??"미기록"}`],
        ["실행 Node / 전력모드",`${runSpec(run).targetNode??"미기록"} / ${runSpec(run).candidate?.powerMode??"미기록"}`],
        ["실행 경로 / 배치",`${runSpec(run).candidate?.executionPath??"미기록"} / ${runSpec(run).measurement?.batch??"미기록"}건 / ${runAttempt(run)}`],
        ["회차 생성",`${formatMeasurementTime(run.creationTimestamp)} / ${measurementAge(run.creationTimestamp)}`],
        ...(currentRunCondition(run,"ExecutionComplete")?.lastTransitionTime?[["실행 판정 시각",`${formatMeasurementTime(currentRunCondition(run,"ExecutionComplete")!.lastTransitionTime!)} / ${measurementAge(currentRunCondition(run,"ExecutionComplete")!.lastTransitionTime!)}`] as [string,React.ReactNode]]:[]),
      ]}/>
      <AssessmentDetails assessment={run.assessment}/>
      {failed&&<section role="alert" className="rounded-xl border border-destructive/40 bg-destructive/5 p-4 space-y-2"><h2 className="font-semibold text-destructive">{failure?.title}</h2><p className="text-sm break-words">{failure?.excerpt??"상세 원인 미제공"}{failure?.exitCode!==undefined?` (종료 코드 ${String(failure.exitCode)})`:""}</p><p className="text-sm">{failure?.next}</p></section>}
      <details><summary className="min-h-11 cursor-pointer py-2 text-sm">실행 조건·환경·로그 원문</summary>
        <Facts label="원본 실행 조건" items={["ExecutionComplete","EvidenceVerified"].map(type=>{const c=currentRunCondition(run,type);return [type,c?`${c.status} / ${c.reason??""}${c.message?`: ${c.message}`:""}`:"상태 미확인"] as [string,React.ReactNode]})}/>
        <Facts label="실행 작업" items={statusFacts(run.status.job)}/><Facts label="실제 실행 환경" items={statusFacts(run.status.actualExecution)}/>
        <section className="grid gap-2"><h2>실행 로그</h2>{failure?.log?<pre className="source-json">{failure.log}</pre>:<p>종료 로그 미제공</p>}</section>
      </details>
      {noEvidence?<p className="notice">실행 실패로 검증 근거가 생성되지 않았어요.</p>:passport.error?<div className="notice"><p>검증 근거를 불러오지 못했어요. {passport.error.status===404?"보관된 근거가 없거나 아직 게시되지 않았어요.":"연결 상태를 확인하고 다시 조회하세요."}</p><Button variant="outline" onClick={()=>void passport.refetch()}>근거 다시 조회</Button></div>:!data?<p>검증 근거 확인 중</p>:other?<p role="alert">같은 이름의 다른 회차 근거예요. 이 회차의 결과로 사용할 수 없어요.</p>:<PassportSummary data={data}/>}
      <Rows>{data&&!other&&!!data.measurement.runtime.nativeSamples?.length&&<RowDialog label="배치 실행 기록" title="배치 실행 기록"><DataTable data={data.measurement.runtime.nativeSamples} columns={sampleColumns}/></RowDialog>}<RowDialog label="원본 자료" title="측정 원본 자료"><SourceExport value={{run,passport:other?undefined:data}} filename={`chill-passport-${name}.json`}/><SourceJSON inline value={{run,passport:other?undefined:data}}/></RowDialog></Rows>
    </>}</QueryGate>
  </Screen>
}

