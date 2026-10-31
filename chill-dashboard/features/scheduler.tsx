"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession, useWorkloadDefinitions } from "@/lib/client"
import { conditionRows } from "@/features/scheduler-profiles"
import type { LoadWindow, OperatingPower, SchedulerAllocation, SchedulerCandidate, SchedulerDecision, SchedulerDefinition, SchedulerInstance, SchedulerNode, SchedulerSnapshot, SchedulerView, WireInteger } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { capacityPath } from "@/lib/capacity"
import { CapacityEvidence } from "@/features/scheduler-capacity"
import { safeSource } from "@/lib/source-export"
import { gapNotice, localDateTime, utcDateTime } from "@/lib/date-time"
import { DataTable } from "@/components/data-table"
import { QueryGate, SourceJSON, SourceExport, StatusBadge, type Tone } from "@/components/read-region"
import { statusLabel } from "@/lib/labels"
import { Facts } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { resolveWorkloadIdentities, type IdentityWorkload } from "@/lib/workload-identity"

export const judgmentLabels:Record<string,string>={Pending:"판단 대기",Keep:"현재 구성 유지",Feasible:"목표 충족",Overload:"예상 부하가 목표 초과",InfeasibleLifetime:"요청 유효기간 충족 어려움",InfeasibleResources:"자원 조건 충족 어려움",InfeasibleTransition:"변경 조건 충족 어려움",ExecutionError:"실행 오류 확인"}
export const judgmentTones:Record<string,Tone>={Keep:"ok",Feasible:"ok",Pending:"idle",Overload:"warn",InfeasibleLifetime:"warn",InfeasibleResources:"warn",InfeasibleTransition:"warn",ExecutionError:"fail"}
export const actionKinds:Record<string,string>={prepare:"실행 준비",route:"요청 분배 변경",unroute:"요청 분배 해제",release:"자원 예약 해제",reset:"서비스 초기화",stop:"요청 접수 중지",drain:"기존 요청 마무리",remove:"인스턴스 제거",power:"전력모드 변경",resize:"인스턴스 교체","manual-static-routing":"설정한 요청 분배 적용"}
// internal/serving/kube/static_routing.go: a configuration-sourced route has no single node (empty NodeRef).
export const actionNodeName=(node:{name:string;uid:string})=>node.uid?node.name||"이름 미제공":"전체 노드"
export const actionStatuses:Record<string,string>={planned:"계획됨",starting:"시작 기록",running:"실행 중",complete:"완료",failed:"실패",cancel:"취소",partial:"일부 변경 관측"}
export const actionTones:Record<string,Tone>={complete:"ok",failed:"fail",partial:"warn",running:"info",starting:"info"}
const observed=(at?:string)=>!!at&&!at.startsWith("0001-")
const joules=(value:number)=>value>=1000?`${(value/1000).toLocaleString("ko-KR",{maximumFractionDigits:2})} kJ`:`${value.toLocaleString("ko-KR",{maximumFractionDigits:1})} J`
const phaseLabels:Record<string,string>={pending:"미배정",assigned:"배정 후 실행 단계 확인 대기",queued:"실제 대기열",deferred:"SLO 위반 후 후순위 대기",running:"실행 중",returning:"응답 반환 중"}
const time=(value:string)=>!value||value.startsWith("0001-")?"미기록":`${new Date(value).toLocaleString("ko-KR",{timeZone:"Asia/Seoul"})} KST`
const ms=(value:WireInteger)=>`${nanosToMilliseconds(value)} ms`
const energy=(value:number|null)=>value==null?"미제공":`${value} J`
const reasonLabels:Record<string,string>={MissingPoolInventory:"실행 그룹의 노드 관측이 필요합니다.",MissingImmutableDefinition:"요청의 고정 서비스 정의가 필요합니다.",NoFullyEvaluableCandidateWithinReportedBounds:"제공된 탐색 범위에서 전체 계산을 마친 후보를 기다립니다.",NoResourceFeasibleCandidateWithinReportedBounds:"제공된 탐색 범위에서 자원 조건을 충족하는 후보가 필요합니다.",NoReadyTransitionWithinReportedBounds:"제공된 탐색 범위에서 실행 가능한 변경을 기다립니다.",EvaluatedCandidatesExceedSLOWithinReportedSearchBounds:"계산한 후보의 예상 지연이 서비스 목표를 초과합니다.",UnevaluableCandidatesRemainPending:"추가 근거를 기다리는 후보가 있습니다.",ExplicitRequestLifetimeCapacityExceeded:"요청 유효기간 안의 처리 용량을 확인합니다.",ObservedOperationExecutionError:"관측된 변경 실행 오류를 확인합니다."}

function Lines({values}:{values?:string[]|null}) {return values?.length?<ul className="list-inside list-disc">{values.map((value,index)=><li key={index}>{reasonLabels[value] ?? value}</li>)}</ul>:<p>제공된 항목 없음</p>}
function Allocation({value}:{value:SchedulerAllocation}) {return <span>CPU {value.cpus?.join(", ")||"목록 없음"} / {value.cpuCount}개, 스레드 {value.threads}, CPU 예약 {value.cpuReserved?"사용":"미사용"}, GPU {value.gpuUUID||"없음"}, 메모리 {String(value.memoryBytes)} bytes</span>}
function ObservationLink({uid,definition,from,to,children}:{uid:string;definition?:string;from:string;to:string;children:React.ReactNode}) {
  if(!uid||!from||!to||from.startsWith("0001-")||to.startsWith("0001-"))return <span>{children}</span>
  const query=new URLSearchParams({uid,from,to});if(definition)query.set("definition",definition)
  return <Link href={`/observations?${query}`}>{children}</Link>
}
function ServiceName({uid,snapshot}:{uid:string;snapshot:SchedulerSnapshot}) {return <span>{resolveWorkloadIdentities([],[],snapshot.workloads,[uid]).get(uid)!} <small className="break-all">({uid})</small></span>}
function SLOs({candidate,decision,snapshot}:{candidate:SchedulerCandidate;decision:SchedulerDecision;snapshot:SchedulerSnapshot}) {
  return <><p>예상 에너지 {energy(candidate.expectedJoules)}. 예상 지연은 반복 비교 결과이며 실측과 구분합니다.</p>{candidate.expectedJoules==null&&<Lines values={candidate.reasons}/>}<div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>서비스</th><th>예상 지연</th><th>목표</th><th>표본 수</th><th>목표 초과 비율</th><th>예상 결과</th></tr></thead><tbody>{candidate.slo?.map((row,index)=><tr key={index}><td><ObservationLink uid={row.uid} definition={row.definition} from={decision.search.windowStart} to={decision.search.windowEnd}><ServiceName uid={row.uid} snapshot={snapshot}/></ObservationLink><small className="block break-all">{row.definition}</small></td><td>{row.expectedNanos==null?"계산값 없음":`${Number(row.expectedNanos) / 1e6} ms`} ({row.percentile}% 기준)</td><td>{ms(row.targetNanos)}</td><td>{row.samples}</td><td>{row.normalizedExceedance}</td><td>{Object.entries(row.predictedOutcomes??{}).map(([key,value])=>`${key}: ${value}`).join(", ")||"제공 없음"}</td></tr>)}</tbody></table></div>{!candidate.slo?.length&&<p>서비스별 예상값 제공 없음</p>}</>
}
function Configuration({nodes,instances,routing,snapshot}:{nodes?:SchedulerNode[]|null;instances?:SchedulerInstance[]|null;routing:SchedulerCandidate["routing"];snapshot:SchedulerSnapshot}) {
  return <><h3>노드</h3>{nodes?.length?nodes.map(node=><section key={node.node.uid} className="space-y-2 rounded border p-3"><h4>{node.node.name}{node.measurement&&" / 측정 중"}</h4><p className="break-all">노드 UID {node.node.uid}</p><p>준비 {node.ready?"완료":"확인 대기"} / 전력모드 {node.powerMode||"제공 없음"} / 관측 {time(node.observedAt)}</p>{!!node.reasons?.length&&<Lines values={node.reasons}/>}<p>전체 CPU {node.cpus?.join(", ")||"없음"}, GPU {node.gpuUUIDs?.join(", ")||"없음"}, 메모리 {String(node.memoryBytes)} bytes</p><p>예약: <Allocation value={node.reserved}/></p><h5>서비스 외 할당</h5>{node.nonServing?.map((allocation,index)=><p key={index}><Allocation value={allocation}/></p>)}{!node.nonServing?.length&&<p>제공된 할당 없음</p>}</section>):<p>제공된 노드 없음</p>}
    <h3>인스턴스와 서비스 연결</h3>{instances?.map(instance=><Instance key={instance.id} instance={instance} snapshot={snapshot}/>)}{!instances?.length&&<p>제공된 인스턴스 없음</p>}
    <h3>요청 분배</h3><p>원래 가중치이며 확률이 아닙니다.</p>{Object.entries(routing??{}).map(([uid,weights])=><div key={uid}><ServiceName uid={uid} snapshot={snapshot}/><ul>{Object.entries(weights).map(([id,weight])=><li key={id}>{id}: {weight}</li>)}</ul></div>)}{!Object.keys(routing??{}).length&&<p>제공된 요청 분배 없음</p>}</>
}
function Instance({instance,snapshot}:{instance:SchedulerInstance;snapshot:SchedulerSnapshot}) {
  const profile=instance.profile
  return <section className="space-y-2 rounded border p-3"><h4 className="break-all">{instance.id}</h4><p>{instance.node.name} / 준비 {instance.ready?"완료":"확인 대기"} / 처리 중지 {instance.stopped?"사용":"미사용"}</p><p><Allocation value={instance.allocation}/></p><p>처리 용량 {instance.capacity}, 원래 가중치 {instance.weight}, 최대 배치 {instance.maxBatch}, 최대 대기 {ms(instance.maxWaitNanos)}</p><p>관측 {time(instance.observedAt)}</p><p>유휴 계측기 {instance.idleMeter} / 범위 {instance.idleScope}</p><p>{instance.workloadUID?<ServiceName uid={instance.workloadUID} snapshot={snapshot}/>:"서비스 없음"}</p>{profile?<><Facts label="측정 조건" items={conditionRows(profile.condition)}/><details><summary>프로파일 {profile.id}</summary><p>노드 {profile.nodeName}, 측정 {time(profile.measuredAt)}, 신뢰 수준 {profile.confidenceLevel}, 유휴 {profile.idleBaselineWatts} W</p><p>조건 원본</p><pre className="source-json">{stringifySchedulerJSON(safeSource(profile.condition),2)}</pre>{profile.batches?.map((batch,index)=><p key={index}>배치 {batch.batch}: 지연 표본 {batch.durationNanos?.map(value=>`${Number(value) / 1e6} ms`).join(", ")||"없음"}, 입력당 {batch.energyPerInputJoules} J, 메모리 {String(batch.memoryBytes)} bytes</p>)}<p>참조와 측정 근거</p><pre className="source-json">{stringifySchedulerJSON(safeSource({reference:profile.reference,evidence:profile.evidence,runtimeMetadata:profile.runtimeMetadata,metricSource:profile.metricSource}),2)}</pre></details></>:<p>프로파일 {instance.profileID??"제공 없음"}</p>}</section>
}
export function SnapshotDetails({snapshot}:{snapshot:SchedulerSnapshot}) {
  return <><p>구성 관측 {time(snapshot.at)}</p><Lines values={snapshot.reasons}/><Configuration nodes={snapshot.nodes} instances={snapshot.instances} routing={snapshot.routing} snapshot={snapshot}/><h3>처리 중 요청</h3><p>배정 후 실행 단계 확인 대기는 실제 대기열과 구분합니다.</p>{snapshot.outstanding?.map((request,index)=><details key={index}><summary>{phaseLabels[request.phase]??request.phase} / {request.definition?.name??request.definitionID??"정의 제공 없음"} / {request.id}</summary><p>서비스 UID {request.definition?.uid}, 인스턴스 {request.instance||"미배정"}, 배치 {request.batchID||"없음"}, 큐 순서 {request.queueOrder}</p><p>수신 {time(request.receivedAt)}, 전체 경과 {ms(request.elapsedNanos)}, 대기 {ms(request.queueAgeNanos)}, 실행 {ms(request.runningElapsedNanos)}, 반환 {ms(request.returnElapsedNanos)}</p>{request.phase==="returning"&&request.nativeOutcome&&<p>native 결과 {request.nativeOutcome}</p>}<p>남은 유효기간 {request.remainingLifetimeNanos==null?"제공 없음":ms(request.remainingLifetimeNanos)}, 도착률 {request.arrivalRate}, native sender 경과 {request.nativeSenderElapsedMS??"제공 없음"} ms</p></details>)}{!snapshot.outstanding?.length&&<p>제공된 처리 중 요청 없음</p>}<h3>참조</h3><Lines values={snapshot.references}/><SourceJSON inline value={snapshot}/></>
}

function CapacityLinks({decision}:{decision:SchedulerDecision}) {
  return <section className="space-y-2"><h3>판단에 참조한 용량 행</h3>{decision.capacity?.workloads?.map(workload=><div key={workload.uid} className="space-y-1"><Link className="break-all" href={capacityPath(workload.uid)}>{workload.uid}</Link>{workload.rows?.map(key=><p key={key}><Link className="break-all" href={capacityPath(workload.uid,key)}>{key}</Link></p>)}</div>)}<Link href={capacityPath()}>용량 계산 근거</Link></section>
}
export function CandidateDetails({candidate,view}:{candidate:SchedulerCandidate;view:SchedulerView}) {
  const {snapshot,decision}=view.state
  return <><p>{candidate.keep?"현재 구성 유지안":"변경 후보"} / {judgmentLabels[candidate.status]??candidate.status} / {candidate.id}</p><Lines values={candidate.reasons}/><SLOs candidate={candidate} decision={decision} snapshot={snapshot}/><CapacityLinks decision={decision}/><h3>비교 조건</h3><p>입력 관측 구간 {time(decision.search.windowStart)} ~ {time(decision.search.windowEnd)}</p><p>에너지 비교 기간 {decision.capacity?ms(decision.capacity.periodNanos):"기록 없음"}</p><p>모집단 {decision.search.population}</p><p>수신 {decision.search.received}, 접수 {decision.search.accepted}, 계산 입력 {decision.search.computeInputs}, 검토 {decision.search.examined}</p><p>에너지를 비교한 노드 {decision.search.comparedNodes?.map(node=>node.name).join(", ")||"없음"}</p><details><summary>탐색 상한</summary><dl>{Object.entries(decision.search.bounds).filter(([key])=>key!=="controls").map(([key,value])=><React.Fragment key={key}><dt>{key}</dt><dd>{String(value)}</dd></React.Fragment>)}</dl><p>제어 조건 {Object.entries(decision.search.bounds.controls).map(([key,value])=>`${key}: ${value?"사용":"미사용"}`).join(", ")}</p></details><h3>서비스별 입력</h3>{decision.search.loads?.map(load=><p key={load.uid}><ServiceName uid={load.uid} snapshot={snapshot}/> 수신 {load.received}, 접수 {load.accepted}, 계산 입력 {load.computeInputs}, {load.inputsPerSecond} 입력/s</p>)}<h3>가정</h3><Lines values={candidate.assumptions}/><h3>출처</h3><Lines values={candidate.sources}/><h3>참조</h3><Lines values={decision.references}/><h3>자원 조건과 목표 구성</h3><p>자원 충족 여부는 ChillServer가 제공한 상태와 사유로 확인합니다.</p><Configuration nodes={candidate.nodes} instances={candidate.instances} routing={candidate.routing} snapshot={snapshot}/><h3>실행 계획</h3>{candidate.actions?.map(action=><p key={action.id}>{action.kind} / {action.node.name} / {action.instance} / {action.status} / 예상 경과 {ms(action.elapsedNanos)} / {action.reason} / 의존 {action.dependsOn?.join(", ")||"없음"}{action.fixed&&" / 모든 후보에 같은 동작"}</p>)}<h3>유휴 기준 전력</h3>{snapshot.baselines?.map(baseline=><details key={baseline.id}><summary>{baseline.node.name}: {baseline.watts} W ({baseline.powerMode})</summary><p>상태 {baseline.state?.join(", ")}, 계측기 {baseline.meter}, 범위 {baseline.scope}, 측정 {time(baseline.measuredAt)}</p><pre className="source-json">{stringifySchedulerJSON(safeSource(baseline.reference),2)}</pre></details>)}{!snapshot.baselines?.length&&<p>제공된 기준 전력 없음</p>}<h3>변경 비용 측정</h3>{snapshot.costs?.map(cost=><details key={cost.id}><summary>{cost.node.name} / {cost.kind} / {cost.from} → {cost.to}</summary><p>전력모드 {cost.powerMode}, 계측기 {cost.meter}, 범위 {cost.scope}, 에너지 범위 {cost.energyScope}, 차감 {cost.subtractedWatts??"제공 없음"} W, 이전 기준 {cost.beforeBaselineWatts??"제공 없음"} W</p><p>이전 상태 {cost.beforeState?.join(", ")}, 목표 상태 {cost.targetState?.join(", ")}</p>{cost.trials?.map((trial,index)=><p key={index}>측정 {index+1}: {ms(trial.durationNanos)}, {trial.joules} J</p>)}<pre className="source-json">{stringifySchedulerJSON(safeSource(cost.reference),2)}</pre></details>)}{!snapshot.costs?.length&&<p>제공된 비용 측정 없음</p>}<SourceJSON inline value={{candidate,decision,snapshot}}/></>
}

export type SchedulerEvidenceKind = "candidates" | "estimates" | "load" | "capacity"
export function SchedulerEvidence({kind}:{kind:SchedulerEvidenceKind}) {
  if(kind==="load")return <LoadEvidence/>
  if(kind==="capacity")return <CapacityEvidence/>
  return <CurrentEvidence kind={kind}/>
}

function CurrentEvidence({kind}:{kind:"candidates"|"estimates"}) {
  const params=useSearchParams()
  const query=useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll:true})
  const [selection,setSelection]=React.useState<{key:string;view:SchedulerView}|null>(null)
  const view=selection?.view??query.data
  const uid=params.get("uid")??params.get("workload")??(params.get("open")?.startsWith("service:")?params.get("open")!.slice(8):"")
  const decision=view?.state.decision,snapshot=view?.state.snapshot
  const candidates=decision&&observed(decision.at)?[decision.keep,...(decision.target?[decision.target]:[])].filter((candidate,index,all)=>candidate.id&&all.findIndex(row=>row.id===candidate.id)===index):[]
  const services=(snapshot?.workloads??[]).filter((row):row is SchedulerDefinition=>!!row&&(!uid||row.uid===uid))
  return <section className="space-y-4"><h3>{kind==="candidates"?"구성·후보 근거":"응답 목표 예상"}</h3><QueryGate query={query}>{view&&decision&&snapshot&&<>
    <Facts items={[["판단 시각",time(decision.at)],["구성 관측",time(snapshot.at)],["입력 시작",time(decision.search.windowStart)],["입력 종료",time(decision.search.windowEnd)]]}/>
    {kind==="candidates"?<>
      <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={()=>setSelection({key:"snapshot",view})}>실제 구성</Button>{candidates.map(candidate=><Button key={candidate.id} variant="outline" onClick={()=>setSelection({key:candidate.id,view})}>{candidate.keep?"유지안":"변경 후보"}</Button>)}</div>
      {selection&&query.data&&(selection.view.state.decision.at!==query.data.state.decision.at||selection.view.state.snapshot.at!==query.data.state.snapshot.at)&&<div role="status"><span>새 판단 수신</span><Button variant="outline" onClick={()=>setSelection({key:selection.key,view:query.data!})}>최신 응답</Button></div>}
      {selection?.key==="snapshot"||(!selection&&params.get("view")==="snapshot")?<SnapshotDetails snapshot={snapshot}/>:selection?<>{candidates.find(row=>row.id===selection.key)?<CandidateDetails candidate={candidates.find(row=>row.id===selection.key)!} view={view}/>:<p>항목 없음</p>}</>:<DataTable data={candidates} columns={[
        {key:"kind",label:"후보",value:row=>row.keep?"유지안":"변경안"},
        {key:"status",label:"판정",value:row=>judgmentLabels[row.status]??statusLabel(row.status)},
        {key:"energy",label:"예상 에너지",unit:"J",numeric:true,value:row=>row.expectedJoules??"판정 불가"},
      ]}/>}
    </>:<>{!observed(decision.at)?<p>미기록</p>:services.length?<DataTable data={services} columns={[
      {key:"service",label:"서비스",value:row=>row.name},
      {key:"percentile",label:"분위수",value:row=>`p${row.slo.percentile}`},
      {key:"target",label:"목표",unit:"ms",numeric:true,value:row=>nanosToMilliseconds(row.slo.targetLatencyNanos)},
      ...candidates.map(candidate=>({key:candidate.id,label:candidate.keep?"유지안 예상":"변경안 예상",unit:"ms",numeric:true,value:(row:SchedulerDefinition)=>{const estimate=candidate.slo?.find(item=>item.uid===row.uid);return estimate?.expectedNanos==null?"판정 불가":Number(estimate.expectedNanos)/1e6}})),
      {key:"details",label:"근거",value:row=><details><summary>표본·결과</summary>{candidates.map(candidate=>{const estimate=candidate.slo?.find(item=>item.uid===row.uid);return <section key={candidate.id}><h4>{candidate.keep?"유지안":"변경안"}</h4><Facts items={[["표본",estimate?.samples??"미제공"],["목표 초과 정도",estimate?.normalizedExceedance??"미제공"],["예상 결과",Object.entries(estimate?.predictedOutcomes??{}).map(([key,value])=>`${statusLabel(key)} ${value}`).join(", ")||"미제공"]]}/><ObservationLink uid={row.uid} definition={estimate?.definition} from={decision.search.windowStart} to={decision.search.windowEnd}>해당 구간 요청 기록</ObservationLink><SourceJSON inline value={{definition:row,estimate}}/></section>})}</details>},
    ]}/>:<p>항목 없음</p>}</>}
    <SourceJSON inline value={view}/><SourceExport filename="chill-scheduler-selected-detail.json" value={{scope:"선택 근거",source:"v1/scheduler",view}}/>
  </>}</QueryGate></section>
}

type WorkloadChoice=IdentityWorkload
function LoadEvidence() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const [entry]=React.useState(()=>{const now=Date.now();return {from:new Date(now-3600000).toISOString(),to:new Date(now).toISOString()}})
  const submitted={from:params.get("from")??entry.from,to:params.get("to")??entry.to}
  const selectedUID=params.get("uid")??params.get("workload")??(params.get("open")?.startsWith("service:")?params.get("open")!.slice(8):"")
  const [draft,setDraft]=React.useState(submitted),[validation,setValidation]=React.useState("")
  const query=useChillQuery<LoadWindow>(`v1/observations/load?${new URLSearchParams(submitted)}`,{decoder:parseSchedulerJSON})
  const workloads=useChillQuery<WorkloadChoice[]>("v1/workloads"),data=query.data
  const recordUIDs=[...(data?.workloads?.map(row=>row.uid)??[]),selectedUID].filter(Boolean)
  const definitions=useWorkloadDefinitions(recordUIDs,submitted)
  const choices=resolveWorkloadIdentities(workloads.data,definitions,[],recordUIDs)
  const rows=(data?.workloads??[]).filter(row=>!selectedUID||row.uid===selectedUID)
  React.useEffect(()=>{if(!params.has("from")||!params.has("to")){const next=new URLSearchParams(params.toString());if(!next.has("from"))next.set("from",entry.from);if(!next.has("to"))next.set("to",entry.to);router.replace(`/?${next}`,{scroll:false})}},[params,entry,router])
  const address=`${submitted.from}/${submitted.to}`,[previousAddress,setPreviousAddress]=React.useState(address)
  if(address!==previousAddress){setPreviousAddress(address);setDraft(submitted)}
  function apply() {
    const from=Date.parse(draft.from),to=Date.parse(draft.to)
    if(!Number.isFinite(from)||!Number.isFinite(to)||from>to||to-from>config.maxWindowHours*3600000){setValidation(`조회 구간 최대 ${config.maxWindowHours}시간`);return}
    const next=new URLSearchParams(params.toString());next.set("from",draft.from);next.set("to",draft.to)
    setValidation("");router.replace(`/?${next}`,{scroll:false})
  }
  return <section className="space-y-4"><h3>구간별 실제 부하</h3>
    <form className="flex flex-wrap items-end gap-3" onSubmit={event=>{event.preventDefault();apply()}}>
      <label>시작 (기기 시간)<input type="datetime-local" value={localDateTime(draft.from)} onChange={event=>setDraft(current=>({...current,from:utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"")}))} required/></label>
      <label>종료 (기기 시간)<input type="datetime-local" value={localDateTime(draft.to)} onChange={event=>setDraft(current=>({...current,to:utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"")}))} required/></label>
      <Button type="submit">조회</Button>{validation&&<p role="alert">{validation}</p>}
    </form>
    <QueryGate query={query}>{data&&<>
      <Facts items={[["실제 시작",time(data.from)],["실제 종료",time(data.to)],["모집단",data.population],["누락",data.gapUntil?gapNotice(data.gapUntil):"없음"]]}/>
      <DataTable data={rows} columns={[
        {key:"service",label:"서비스",value:row=>choices.get(row.uid)!},
        {key:"received",label:"수신",unit:"건",numeric:true,value:row=>row.received},
        {key:"accepted",label:"접수",unit:"건",numeric:true,value:row=>row.accepted},
        {key:"assigned",label:"배정",unit:"건",numeric:true,value:row=>row.assigned},
        {key:"completed",label:"완료 사건",unit:"건",numeric:true,value:row=>row.completed},
        {key:"detail",label:"근거",value:row=><details><summary>비율·배정</summary><Facts items={[["수신 입력/s",row.receivedInputsPerSecond],["접수 입력/s",row.acceptedInputsPerSecond],["배정 입력/s",row.assignedInputsPerSecond],["완료 사건/s",row.completionEventsPerSecond],["서비스 UID",row.uid]]}/><table><thead><tr><th>인스턴스</th><th>배정 건</th></tr></thead><tbody>{Object.entries(row.instanceAssignments??{}).map(([id,count])=><tr key={id}><th>{id}</th><td>{count}</td></tr>)}</tbody></table><ObservationLink uid={row.uid} from={data.from} to={data.to}>해당 구간 요청 기록</ObservationLink><SourceJSON inline value={row}/></details>},
      ]}/>
      <SourceJSON inline value={data}/><SourceExport filename="chill-load-selected-window.json" value={{source:"v1/observations/load",from:data.from,to:data.to,population:data.population,sourceData:data}}/>
    </>}</QueryGate>
  </section>
}

export function PowerInterval({from,to}:{from:string;to:string}) {
  const query=useChillQuery<OperatingPower>(`v1/observations/power?${new URLSearchParams({from,to})}`,{decoder:parseSchedulerJSON})
  const data=query.data,measuredEnergy=(value:number|null)=>value==null?"측정값 없음":joules(value),watts=(value:number|null)=>value==null?"측정값 없음":`${value.toLocaleString("ko-KR",{maximumFractionDigits:2})} W`
  return <QueryGate query={query}>{data&&<div className="space-y-4">
    <p>정확한 집계 구간 {data.from} ~ {data.to} / 범위 {data.scope}</p><StatusBadge tone={data.complete?"ok":"warn"}>{data.complete?"계측 집계 완료":"관측 공백 있음"}</StatusBadge><p className="text-sm text-muted-foreground">ChillServer가 같은 구간의 노드 벽전력을 한 번씩 적분한 실측값입니다. 현재 설정, 후보의 예상 에너지 또는 Serving 참여 여부만으로 소비를 계산하지 않습니다.</p>
    <dl className="grid gap-3 sm:grid-cols-2"><div><dt>전체 POOL (서빙 비참여 노드 포함)</dt><dd>{measuredEnergy(data.wholePoolJoules)} / 평균 {watts(data.wholePoolMeanWatts)}</dd></div><div><dt>활성 POOL (구간 중 실제 Serving 참여)</dt><dd>{measuredEnergy(data.activePoolJoules)} / 평균 {watts(data.activePoolMeanWatts)}</dd></div></dl>
    {data.reasons?.length>0&&<div role="status"><h3>측정 공백과 집계 사유</h3><Lines values={data.reasons}/></div>}{data.gapUntil&&<p role="status">{gapNotice(data.gapUntil)}</p>}
    <DataTable data={data.nodes??[]} columns={[
      {key:"node",label:"기대 노드",value:row=><span>{row.node.name}<small className="block break-all">{row.node.uid}</small></span>},
      {key:"scope",label:"계측기와 범위",value:row=><span>{row.meter||"계측기 없음"}<small className="block">{row.scope}</small></span>},
      {key:"energy",label:"실측 에너지",value:row=>measuredEnergy(row.joules)},
      {key:"power",label:"평균 전력",value:row=>watts(row.meanWatts)},
      {key:"samples",label:"표본 수",value:row=>row.samples},
      {key:"coverage",label:"관측 상태와 사유",value:row=><><p>{row.sourceTimestampAbsent?"원본 시각 없음 (수집 시각 사용; 신선도 증거 아님)":"원본 시각 부재 보고 없음"}</p>{row.reasons?.length>0?<Lines values={row.reasons}/>:<p>보고된 공백 사유 없음</p>}</>},
    ]}/>{!data.nodes?.length&&<p>기대 노드와 계측 범위가 제공되지 않았습니다. 전체 소비를 0으로 대체하지 않습니다.</p>}
    <SourceExport filename="chill-operating-power.json" value={{source:"v1/observations/power",conditions:{from,to},result:data}}/><SourceJSON title="정확한 구간의 집계 원본" value={data}/>
  </div>}</QueryGate>
}
