"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { decodeResponse, useChillMutation, useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { MeasurementProjectionList, OccupancyMutation, OccupancyRequest, OccupancyTask } from "@/lib/read-types"
import type { WorkloadSummary } from "@/lib/domain"
import { occupancyBody, occupancyChecked, occupancyRequestHref, occupancyResponseState, occupancyResultHref } from "@/lib/dispatch-occupancy"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { executionPathLabel } from "@/lib/measurements"
import { useCoverageInventory, useMeasurementCoverage, useMeasurementPlans } from "@/features/measurement-coverage"
import { MeasurementNavigation } from "@/components/measurement-navigation"
import { QueryGate, SourceJSON } from "@/components/read-region"
import { Screen, Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

export function MeasurementKindSelector({kind,disabled=false}:{kind:"profile"|"dispatch-occupancy";disabled?:boolean}) {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  return <label className="grid justify-self-start gap-1.5 text-sm">측정 종류<select className="min-h-11 rounded-md border bg-background px-3" disabled={disabled} value={kind} onChange={event=>router.push(event.target.value==="dispatch-occupancy"?occupancyRequestHref(params.get("namespace")??config.namespace):`/measurements/requests/new?${new URLSearchParams({namespace:params.get("namespace")??config.namespace})}`)}><option value="profile">프로파일</option><option value="dispatch-occupancy">d · 분배 점유시간</option></select></label>
}

export function OccupancyProtocol({task}:{task:OccupancyTask}) {
  const spec=task.spec
  if(spec.minimumPairs!==undefined&&spec.minimumPairs>0) {
    const fields:[string,unknown][]=[["측정 방식","짧은 버스트"],["배치 (건)",spec.batch],["공급 방식 (서버 고정)",spec.supplyMode],["최소 쌍 (개)",spec.minimumPairs],["예열 (s · 측정 제외)",spec.warmupSeconds],["측정 구간 (s)",spec.durationSeconds],["동시 송신자 (명)",spec.workers],["요청 수명 (s)",spec.requestLifetimeSeconds],["드레인 (s)",spec.drainSeconds],["프로파일 ID",spec.conditions?.profileID]]
    return <section className="grid gap-3" aria-label="서버 측정 프로토콜"><h2 className="font-semibold">서버 측정 프로토콜 · 읽기 전용</h2><table className="w-full table-fixed text-left text-sm [&_th]:p-2 [&_td]:p-2 [&_td]:break-all"><thead><tr><th>항목</th><th>서버 값</th></tr></thead><tbody>{fields.map(([label,value])=><tr key={label} className="border-b"><th scope="row" className="font-normal">{label}</th><td>{value==null?"미제공":String(value)}</td></tr>)}</tbody></table><details><summary className="min-h-11 cursor-pointer py-2">정책·입력 근거 원본</summary><SourceJSON inline value={task}/></details></section>
  }
  let sizing:Record<string,unknown>|undefined
  try {
    const inputs=typeof spec.conditions?.inputsJSON==="string"?parseSchedulerJSON(spec.conditions.inputsJSON):null
    if(inputs&&typeof inputs==="object"&&"computed" in inputs) {
      const computed=inputs.computed
      if(computed&&typeof computed==="object"&&"saturationSizing" in computed) {
        const value=computed.saturationSizing
        if(value&&typeof value==="object"&&!Array.isArray(value))sizing=value as Record<string,unknown>
      }
    }
  } catch { /* Unavailable frozen inputs remain unavailable; never infer sizing from current policy. */ }
  const fields:[string,unknown][]=[["공급 방식 (서버 고정)",spec.supplyMode==="closed-loop"?spec.supplyMode:undefined],["예열 (s)",spec.warmupSeconds],["측정 구간 (s)",spec.durationSeconds],["동시 송신자 (명)",spec.workers],["최소 배치 표본 (개)",spec.minimumBatches],["최대 유휴 비율",spec.maximumIdleFraction],["요청 수명 (s)",spec.requestLifetimeSeconds],["전달 여유 (ms)",spec.deliveryMarginMillis],["드레인 (s)",spec.drainSeconds],["준비 (s)",spec.preparationSeconds],["작업 준비 (s)",spec.jobSetupSeconds],["수집 (s)",spec.collectionSeconds],["계획 경과 (s)",spec.plannedElapsedSeconds],["난수 시드",spec.seed],["구형 공급 배율",spec.rateFactor],["프로파일 ID",spec.conditions?.profileID],["최소 사이클 (회)",spec.conditions?.minimumCycles],["대기 배치 (개)",spec.conditions?.waitingBatches],["요청당 최대 유휴 (ns)",spec.conditions?.maximumIdleNanosPerRequest]]
  fields.push(["선택 송신자 (명)",sizing?.selectedWorkers],["필요 송신자 (명)",sizing?.requiredWorkers],["제한 요인",sizing?.limitingBound],["송신자 상한 (명)",sizing?.workerCap],["바이트 한도 송신자 (명)",sizing?.byteCapWorkers],["수명 한도 송신자 (명)",sizing?.lifetimeCapWorkers],["가득 찬 대기 배치 (개)",sizing?.fullQueuedBatches],["재충전 (s)",sizing?.refillSeconds],["잔여 유휴 (s)",sizing?.residualIdleSeconds],["예상 유휴 비율",sizing?.estimatedIdleFraction],["포화 산정 최대 유휴 비율",sizing?.maximumIdleFraction])
  return <section className="grid gap-3" aria-label="서버 측정 프로토콜"><h2 className="font-semibold" title="서버 정책에서 채운 고정 측정 방식입니다. 사용자는 대상·워크로드·Node·배치만 선택합니다.">서버 측정 프로토콜 · 읽기 전용</h2><table className="w-full table-fixed text-left text-sm [&_th]:p-2 [&_td]:p-2 [&_td]:break-all"><thead><tr><th scope="col">항목</th><th scope="col">서버 값</th></tr></thead><tbody>{fields.map(([label,value])=><tr key={label} className="border-b"><th scope="row" className="font-normal">{label}</th><td className="tabular-nums">{value==null?"미제공":String(value)}</td></tr>)}</tbody></table><details><summary className="min-h-11 cursor-pointer py-2">정책·입력 근거 원본</summary><SourceJSON inline value={task}/></details></section>
}

function MutationError({error}:{error:ApiError|null}) {
  if(!error)return null
  const data=error.data
  const code=data&&typeof data==="object"&&"code" in data&&typeof data.code==="string"?data.code:undefined
  return <div role="alert" className="grid gap-2"><p>{occupancyResponseState(code)} · HTTP {error.status}</p><SourceJSON value={error.data??error.body} title="사유 원문"/></div>
}

export function DispatchOccupancyRequestPage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession(),namespace=params.get("namespace")??config.namespace
  const plans=useMeasurementPlans(namespace)
  const workloads=useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary"),inventory=useCoverageInventory()
  const coverage=useMeasurementCoverage(namespace)
  const dry=useChillMutation(),send=useChillMutation()
  const [selection,setSelection]=React.useState<{plan?:string;workload?:string;batch?:string;name?:string}>({})
  const [checked,setChecked]=React.useState<{key:string;body:OccupancyRequest}|null>(null),[issue,setIssue]=React.useState(""),[unknown,setUnknown]=React.useState(false)
  const [response,setResponse]=React.useState<OccupancyMutation|undefined>(undefined)
  const listPath=`v1/namespaces/${encodeURIComponent(namespace)}/dispatch-occupancy-measurements`
  const records=useChillQuery<MeasurementProjectionList>(listPath,{enabled:unknown,decoder:parseSchedulerJSON})
  const allPlans=plans.data?.items??[]
  const profileID=params.get("profileID")
  const currentIDs=new Set(coverage.data?.cells.flatMap(cell=>cell.currentMeasurement?[cell.currentMeasurement.id]:[])??[])
  const measurementID=params.get("measurementID")??(profileID?coverage.data?.cells.find(cell=>cell.currentMeasurement?.id===profileID||cell.variants.some(variant=>variant.profileIds.includes(profileID)))?.currentMeasurement?.id:undefined)
  const profilePlan=measurementID?allPlans.find(plan=>`${plan.metadata.namespace}/${plan.metadata.name}/${plan.metadata.uid}`===measurementID&&currentIDs.has(measurementID)):undefined
  const candidates=allPlans.filter(plan=>{const c=plan.spec.state.candidate;return currentIDs.has(`${plan.metadata.namespace}/${plan.metadata.name}/${plan.metadata.uid}`)&&(!params.get("model")||c.modelSpec===params.get("model"))&&(!params.get("deviceClass")||c.deviceClass===params.get("deviceClass"))&&(!params.get("path")||c.executionPath===params.get("path"))&&(!params.get("powerMode")||c.powerMode===params.get("powerMode"))})
  const initialPlan=params.get("batchEnergyOptimization")??profilePlan?.metadata.name??(candidates.length===1?candidates[0].metadata.name:"")
  const planName=selection.plan??initialPlan,plan=candidates.find(plan=>plan.metadata.name===planName)
  const nodeName=plan?.spec.state.targetNode??""
  const nodes=(inventory.data?.classes.find(cls=>cls.name===plan?.spec.state.candidate.deviceClass)?.nodes??[]).filter(node=>node.name===nodeName)
  const node=nodes[0]
  const workloadChoices=(workloads.data??[]).filter(workload=>!plan||workload.desired.spec.modelRef.name===plan.spec.state.candidate.modelSpec)
  const workloadName=selection.workload??params.get("workload")??workloadChoices.find(workload=>workload.desired.metadata.uid===params.get("workloadUID"))?.desired.metadata.name??""
  const workload=workloadChoices.find(workload=>workload.desired.metadata.name===workloadName)
  const batchText=selection.batch??params.get("batch")??"",batch=Number(batchText)
  const name=selection.name??""
  const body=occupancyBody({name,node:nodeName,batchEnergyOptimization:planName,workload:workloadName,batch})
  const key=JSON.stringify({body,planUID:plan?.metadata.uid,planRV:plan?.metadata.resourceVersion,nodeUID:node?.uid,nodeRV:node?.resourceVersion,workloadUID:workload?.desired.metadata.uid,workloadRV:workload?.desired.metadata.resourceVersion})
  const passed=occupancyChecked(key,checked?.key??null,response)&&(!name||response?.task?.metadata.name===name),pending=dry.isPending||send.isPending
  const prefillMismatch=!!(params.get("measurementID")||profileID)&&!profilePlan||!!params.get("node")&&params.get("node")!==nodeName||!!params.get("nodeUID")&&node?.uid!==params.get("nodeUID")
  const invalid=prefillMismatch?"선택한 근거 식별자 불일치":!plan?"측정할 셀을 선택하세요.":!node?"Node를 선택하세요.":node.ready!=="True"||node.schedulingDisabled?"Node 준비 안 됨":!workload?"워크로드를 선택하세요.":!/^\d+$/.test(batchText)||!Number.isSafeInteger(batch)||batch<1||batch>2147483647?"배치 범위 확인":name&&(name.length>200||!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/.test(name))?"요청 이름은 200자 이하의 영문 소문자, 숫자, '-', '.'로 적고, 처음과 끝은 영문 소문자나 숫자여야 해요.":""
  const frozenName=response?.task?.metadata.name
  function update(patch:typeof selection) {setSelection(current=>({...current,...patch}));setChecked(null);setResponse(undefined);setIssue("");dry.reset();send.reset()}
  function verify() {
    if(invalid){setIssue(invalid);return}
    setIssue("");setChecked({key,body});setResponse(undefined)
    dry.mutate({path:`${listPath}?dryRun=All`,method:"POST",body},{onSuccess:result=>{
      try {setResponse(decodeResponse(result.body,parseSchedulerJSON) as OccupancyMutation)}
      catch(error) {setChecked(null);setIssue(error instanceof Error?error.message:"서버 응답 해석 실패")}
    }})
  }
  function submit() {
    if(!passed||!checked||unknown||invalid||!frozenName)return
    send.mutate({path:listPath,method:"POST",body:occupancyBody({...checked.body,name:frozenName})},{onSuccess:result=>{
      const accepted=result.data as OccupancyMutation
      if(accepted.request==="Accepted"&&accepted.task?.metadata.name)router.replace(occupancyResultHref(namespace,accepted.task.metadata.name,accepted.task.metadata.uid))
      else if(accepted.request!=="NOT_SUBMITTED")setUnknown(true)
    },onError:error=>{const data=error.data;if(error.unknownOutcome||error.status===0||data&&typeof data==="object"&&"code" in data&&data.code==="MeasurementAuditIncomplete")setUnknown(true)}})
  }
  const found=records.data?.items?.find(item=>item.metadata.name===frozenName)
  const sent=send.data?.data
  return <Screen width="data" title="측정 요청" back={["/measurements","측정"]}>
    <MeasurementNavigation active="requests" namespace={namespace}/><MeasurementKindSelector kind="dispatch-occupancy" disabled={pending||unknown}/>
    <section className="grid gap-4" aria-label="d 측정 대상"><fieldset disabled={pending||unknown} className="grid gap-4 disabled:opacity-60 sm:grid-cols-2">
      <QueryGate query={coverage}><QueryGate query={plans}><label className="grid gap-1.5 text-sm sm:col-span-2">셀<select className="min-h-11 min-w-0 rounded-md border bg-background px-3" value={planName} onChange={event=>update({plan:event.target.value,workload:undefined})}><option value="">셀 선택</option>{candidates.map(plan=>{const c=plan.spec.state.candidate;return <option key={plan.metadata.uid} value={plan.metadata.name}>{c.modelSpec} / {c.deviceClass} / {executionPathLabel(c.executionPath)} / {c.powerMode} / {plan.spec.state.targetNode}</option>})}</select></label>{!candidates.length&&<p role="status">현재 프로파일 없음 · 프로파일 측정 필요</p>}</QueryGate></QueryGate>
      <QueryGate query={inventory}><label className="grid gap-1.5 text-sm">Node<select className="min-h-11 rounded-md border bg-background px-3" value={nodeName} disabled aria-describedby="occupancy-node-help"><option value="">Node 선택</option>{nodes.map(node=><option key={node.uid} value={node.name}>{node.name}{node.ready!=="True"||node.schedulingDisabled?" · 준비 안 됨":""}</option>)}</select><span id="occupancy-node-help" className="text-xs text-muted-foreground">선택한 BEO의 대상 Node · 다른 장비는 셀을 바꿔 선택하세요.</span></label></QueryGate>
      <QueryGate query={workloads}><label className="grid gap-1.5 text-sm">워크로드<select className="min-h-11 rounded-md border bg-background px-3" value={workloadName} onChange={event=>update({workload:event.target.value})}><option value="">워크로드 선택</option>{workloadChoices.map(workload=><option key={workload.desired.metadata.uid} value={workload.desired.metadata.name}>{workload.desired.metadata.name}</option>)}</select></label></QueryGate>
      <label className="grid gap-1.5 text-sm">배치 (건)<Input className="min-h-11" type="number" inputMode="numeric" min={1} max={2147483647} step={1} value={batchText} onChange={event=>update({batch:event.target.value})}/></label>
      <label className="grid gap-1.5 text-sm sm:col-span-2">요청 이름 (선택)<Input type="text" maxLength={200} pattern="[a-z0-9]([a-z0-9\-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9\-]*[a-z0-9])?)*" value={name} onChange={event=>update({name:event.target.value})}/><span className="text-xs text-muted-foreground">비우면 서버에서 정해요. 같은 조건을 다시 측정하려면 새 이름을 적어 기존 실패 기록을 보존하세요.</span></label>
    </fieldset>{prefillMismatch&&<p role="alert">선택한 근거 식별자 불일치 · 커버리지 또는 용량 근거에서 다시 선택하세요.</p>}{issue&&<p role="alert">{issue}</p>}<Button className="justify-self-start" variant="outline" disabled={pending||unknown||plans.isPending||coverage.isPending||workloads.isPending||inventory.isPending} onClick={verify}>{dry.isPending?"검사 중":"검사"}</Button><MutationError error={dry.error}/></section>
    {passed&&response?.task&&<OccupancyProtocol task={response.task}/>}
    <section className="grid gap-3" aria-label="검사·제출">{passed&&<Facts items={[["검사","완료"],["접수",occupancyResponseState(undefined,response?.request)],["요청 이름",frozenName]]}/>}<Button className="justify-self-start" disabled={!passed||pending||unknown||!!invalid} onClick={submit}>{send.isPending?"제출 중":"측정 요청"}</Button><MutationError error={send.error}/>{!!sent&&typeof sent==="object"&&"request" in sent&&sent.request==="NOT_SUBMITTED"&&<p role="status">미제출</p>}{unknown&&<div role="alert" className="grid gap-3"><p>접수 결과 미확인 · 자동 재제출 차단</p><Button variant="outline" disabled={records.isFetching} onClick={()=>void records.refetch()}>접수 기록 조회</Button><QueryGate query={records}>{found?<Link className="underline" href={occupancyResultHref(namespace,found.metadata.name,found.metadata.uid)}>접수된 측정 열기</Link>:<p>같은 이름의 접수 기록 미확인</p>}</QueryGate></div>}</section>
  </Screen>
}
