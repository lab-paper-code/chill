"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession, useWorkloadDefinitions } from "@/lib/client"
import { conditionRows } from "@/features/scheduler-profiles"
import type { LoadWindow, OperatingPower, SchedulerAllocation, SchedulerCandidate, SchedulerDecision, SchedulerDefinition, SchedulerEstimate, SchedulerInstance, SchedulerNode, SchedulerSnapshot, SchedulerView, WireInteger } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { capacityPath } from "@/lib/capacity"
import { excludedNodes } from "@/features/scheduler-settings"
import { safeSource } from "@/lib/source-export"
import { gapNotice, localDateTime, utcDateTime } from "@/lib/date-time"
import { DataTable } from "@/components/data-table"
import { QueryGate, SourceJSON, SourceExport, StatusBadge, statusLabel, type Tone } from "@/components/read-region"
import { Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { resolveWorkloadIdentities, type IdentityWorkload } from "@/lib/workload-identity"

export const judgmentLabels:Record<string,string>={Pending:"판단 대기",Keep:"현재 구성 유지",Feasible:"목표 충족",Overload:"예상 부하가 목표 초과",InfeasibleLifetime:"요청 유효기간 충족 어려움",InfeasibleResources:"자원 조건 충족 어려움",InfeasibleTransition:"변경 조건 충족 어려움",ExecutionError:"실행 오류 확인"}
export const judgmentTones:Record<string,Tone>={Keep:"ok",Feasible:"ok",Pending:"idle",Overload:"warn",InfeasibleLifetime:"warn",InfeasibleResources:"warn",InfeasibleTransition:"warn",ExecutionError:"fail"}
export const actionKinds:Record<string,string>={prepare:"실행 준비",route:"요청 분배 변경",stop:"요청 접수 중지",drain:"기존 요청 마무리",remove:"인스턴스 제거",power:"전력모드 변경",resize:"인스턴스 교체","manual-static-routing":"설정한 요청 분배 적용"}
// internal/serving/kube/static_routing.go: a configuration-sourced route has no single node (empty NodeRef).
export const actionNodeName=(node:{name:string;uid:string})=>node.uid?node.name||"이름 미제공":"전체 노드"
export const actionStatuses:Record<string,string>={planned:"계획됨",starting:"시작 기록",running:"실행 중",complete:"완료",failed:"실패",cancel:"취소",partial:"일부 변경 관측"}
export const actionTones:Record<string,Tone>={complete:"ok",failed:"fail",partial:"warn",running:"info",starting:"info"}
const observed=(at?:string)=>!!at&&!at.startsWith("0001-")
const joules=(value:number)=>value>=1000?`${(value/1000).toLocaleString("ko-KR",{maximumFractionDigits:2})} kJ`:`${value.toLocaleString("ko-KR",{maximumFractionDigits:1})} J`
const localTime = (at:string) => new Date(at).toLocaleString("ko-KR",{dateStyle:"medium",timeStyle:"short"})
const phaseLabels:Record<string,string>={pending:"미배정",assigned:"배정 후 실행 단계 확인 대기",queued:"실제 대기열",deferred:"SLO 위반 후 후순위 대기",running:"실행 중",returning:"응답 반환 중"}
const time=(value:string)=>!value||value.startsWith("0001-")?"관측 시각 없음":value
const ms=(value:WireInteger)=>`${nanosToMilliseconds(value)} ms`
const energy=(value:number|null)=>value==null?"계산값 없음":`${value} J`
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
  return <section className="space-y-2"><h3>판단에 참조한 용량 행</h3><p className="text-sm text-muted-foreground">저장된 key 그대로 현재 제공 여부를 확인해요. 없어진 행은 최신 행으로 바꾸지 않아요.</p>{decision.capacity?.workloads?.map(workload=><div key={workload.uid} className="space-y-1"><Link className="break-all" href={capacityPath(workload.uid)}>{workload.uid}의 현재 용량 행</Link>{workload.rows?.map(key=><p key={key}><Link className="break-all" href={capacityPath(workload.uid,key)}>용량 행 {key}</Link></p>)}</div>)}<Link href="/scheduler/capacity">전체 용량 계산 근거 보기</Link></section>
}
export function CandidateDetails({candidate,view}:{candidate:SchedulerCandidate;view:SchedulerView}) {
  const {snapshot,decision}=view.state
  return <><p>{candidate.keep?"현재 구성 유지안":"변경 후보"} / {judgmentLabels[candidate.status]??candidate.status} / {candidate.id}</p><Lines values={candidate.reasons}/><SLOs candidate={candidate} decision={decision} snapshot={snapshot}/><CapacityLinks decision={decision}/><h3>비교 조건</h3><p>입력 관측 구간 {time(decision.search.windowStart)} ~ {time(decision.search.windowEnd)}</p><p>에너지 비교 기간 {decision.capacity?ms(decision.capacity.periodNanos):"기록 없음"}</p><p>모집단 {decision.search.population}</p><p>수신 {decision.search.received}, 접수 {decision.search.accepted}, 계산 입력 {decision.search.computeInputs}, 검토 {decision.search.examined}</p><p>에너지를 비교한 노드 {decision.search.comparedNodes?.map(node=>node.name).join(", ")||"없음"}</p><details><summary>탐색 상한</summary><dl>{Object.entries(decision.search.bounds).filter(([key])=>key!=="controls").map(([key,value])=><React.Fragment key={key}><dt>{key}</dt><dd>{String(value)}</dd></React.Fragment>)}</dl><p>제어 조건 {Object.entries(decision.search.bounds.controls).map(([key,value])=>`${key}: ${value?"사용":"미사용"}`).join(", ")}</p></details><h3>서비스별 입력</h3>{decision.search.loads?.map(load=><p key={load.uid}><ServiceName uid={load.uid} snapshot={snapshot}/> 수신 {load.received}, 접수 {load.accepted}, 계산 입력 {load.computeInputs}, {load.inputsPerSecond} 입력/s</p>)}<h3>가정</h3><Lines values={candidate.assumptions}/><h3>출처</h3><Lines values={candidate.sources}/><h3>참조</h3><Lines values={decision.references}/><h3>자원 조건과 목표 구성</h3><p>자원 충족 여부는 ChillServer가 제공한 상태와 사유로 확인합니다.</p><Configuration nodes={candidate.nodes} instances={candidate.instances} routing={candidate.routing} snapshot={snapshot}/><h3>실행 계획</h3>{candidate.actions?.map(action=><p key={action.id}>{action.kind} / {action.node.name} / {action.instance} / {action.status} / 예상 경과 {ms(action.elapsedNanos)} / {action.reason} / 의존 {action.dependsOn?.join(", ")||"없음"}{action.fixed&&" / 모든 후보에 같은 동작"}</p>)}<h3>유휴 기준 전력</h3>{snapshot.baselines?.map(baseline=><details key={baseline.id}><summary>{baseline.node.name}: {baseline.watts} W ({baseline.powerMode})</summary><p>상태 {baseline.state?.join(", ")}, 계측기 {baseline.meter}, 범위 {baseline.scope}, 측정 {time(baseline.measuredAt)}</p><pre className="source-json">{stringifySchedulerJSON(safeSource(baseline.reference),2)}</pre></details>)}{!snapshot.baselines?.length&&<p>제공된 기준 전력 없음</p>}<h3>변경 비용 측정</h3>{snapshot.costs?.map(cost=><details key={cost.id}><summary>{cost.node.name} / {cost.kind} / {cost.from} → {cost.to}</summary><p>전력모드 {cost.powerMode}, 계측기 {cost.meter}, 범위 {cost.scope}, 에너지 범위 {cost.energyScope}, 차감 {cost.subtractedWatts??"제공 없음"} W, 이전 기준 {cost.beforeBaselineWatts??"제공 없음"} W</p><p>이전 상태 {cost.beforeState?.join(", ")}, 목표 상태 {cost.targetState?.join(", ")}</p>{cost.trials?.map((trial,index)=><p key={index}>측정 {index+1}: {ms(trial.durationNanos)}, {trial.joules} J</p>)}<pre className="source-json">{stringifySchedulerJSON(safeSource(cost.reference),2)}</pre></details>)}{!snapshot.costs?.length&&<p>제공된 비용 측정 없음</p>}<SourceJSON inline value={{candidate,decision,snapshot}}/></>
}

const headlines:Record<string,string>={Pending:"첫 판단을 기다리고 있어요",Keep:"지금 구성을 그대로 유지해요",Feasible:"지금 구성으로 목표를 지킬 수 있어요",Overload:"예상 부하가 목표를 넘어요",InfeasibleLifetime:"요청 유효기간을 지키기 어려워요",InfeasibleResources:"자원이 부족해 목표를 지키기 어려워요",InfeasibleTransition:"지금은 구성을 바꾸기 어려워요",ExecutionError:"변경 실행 중 오류가 있었어요"}
const back:[string,string]=["/scheduler","운영 판단"]

// Before the first judgment there is no keep/target pair to compare.
function judgment(view:SchedulerView) {
  const {decision}=view.state,first=!observed(decision.at)
  const target=!first&&decision.target&&decision.target.id!==decision.keep.id?decision.target:null
  const estimates=first?[]:(target??decision.keep).slo??[]
  return {decision,first,status:first?"Pending":decision.status,target,estimates,exceeding:estimates.filter(row=>row.normalizedExceedance>0).length}
}
function candidatesOf(decision:SchedulerDecision) {
  return observed(decision.at)?[decision.keep,...(decision.target?[decision.target]:[])].filter((candidate,index,all)=>candidate.id&&all.findIndex(item=>item.id===candidate.id)===index):[]
}

function EnergyCompare({view}:{view:SchedulerView}) {
  const {decision,target}=judgment(view)
  const rows:[string,SchedulerCandidate][]=[["현재 구성 유지",decision.keep],...(target?[["선택한 변경 후보",target] as [string,SchedulerCandidate]]:[])]
  const values=rows.map(([,candidate])=>candidate.expectedJoules).filter((value):value is number=>value!=null)
  const scale=Math.max(...values,0)
  return <div className="grid gap-3"><dl className="energy-rows" aria-label="예상 에너지">{rows.map(([label,candidate])=>{
    const value=candidate.expectedJoules
    const delta=target&&candidate===target&&value!=null&&decision.keep.expectedJoules?(value-decision.keep.expectedJoules)/decision.keep.expectedJoules*100:null
    return <div key={label} className="energy-row"><dt>{label}</dt><div className="energy-track" aria-hidden="true">{value!=null&&scale>0&&<div className="energy-fill" data-hot={value===scale&&values.length>1} style={{width:`${Math.max(value/scale*100,2)}%`}}/>}</div><dd>{value==null?<small>계산값 없음</small>:joules(value)}{delta!=null&&<small className={delta<0?"block text-ok":"block text-heat"}>유지 대비 {delta>0?"+":"−"}{Math.abs(delta).toFixed(1)}%</small>}</dd></div>
  })}</dl><p className="text-sm text-muted-foreground">에너지와 지연은 예상값이에요. 실측 전력이 아니에요.</p></div>
}

const controlKeys=["placement","resources","replicas","routing","batching","power","membership"] as const
const controlLabels={placement:"배치 위치",resources:"자원 할당",replicas:"복제 수",routing:"요청 분배",batching:"배칭",power:"전력모드",membership:"실행 그룹 소속"}
// From settings to actual serving: what is switched on, what the judgment used, and where the real observations are.
function ActualControl({view}:{view:SchedulerView}) {
  const {snapshot,decision}=view.state,controls=view.settings.controls
  const full=!!view.settings.autonomousWorkloads?.length&&controlKeys.every(key=>controls[key]===true)
  const range=observed(decision.search.windowStart)&&observed(decision.search.windowEnd)&&decision.search.windowStart!==decision.search.windowEnd?new URLSearchParams({from:decision.search.windowStart,to:decision.search.windowEnd}):null
  const chosen=decision.target??decision.keep
  const workloads=(snapshot.workloads??[]).filter((workload):workload is SchedulerDefinition=>!!workload)
  return <>
    <StatusBadge tone={full?"info":"idle"}>{full?"모든 제어 켜짐":"일부 제어만 켜졌거나 자동 제어 서비스 없음"}</StatusBadge>
    <p className="text-sm text-muted-foreground">모든 제어는 자동 제어 서비스가 하나 이상 있고 8가지 제어가 모두 켜진 설정이에요. 측정 근거, 준비 상태, 실제 변경 완료나 응답 목표 통과를 뜻하지는 않아요.</p>
    <Facts items={[["자동 제어 서비스",`${view.settings.autonomousWorkloads?.length??0}개`],...controlKeys.map(key=>[controlLabels[key],controls[key]?"사용":"미사용"] as [string,string])]}/>
    <Link href="/configuration">운영 설정 보기</Link>
    <h3>측정 근거와 판단</h3>
    <Facts items={[["판단",observed(decision.at)?`${localTime(decision.at)} · ${judgmentLabels[decision.status]??statusLabel(decision.status)}`:"판단 대기"],["입력 관측 구간",`${time(decision.search.windowStart)} ~ ${time(decision.search.windowEnd)}`],["에너지 비교 기간",decision.capacity?ms(decision.capacity.periodNanos):"기록 없음"],["계획에서 뺀 장비",excludedNodes(decision.settings)],["선택 목표",observed(decision.at)?decision.target?.id??"변경 목표 없음":"판단 대기"],["실제 구성 관측",time(snapshot.at)]]}/>
    <p className="text-sm text-muted-foreground">목표 선언은 실제 구성이 아니에요. 판단 시각과 관측 시각은 별개예요.</p>
    <Lines values={decision.reasons}/>
    <ul className="grid gap-1.5">
      <li><Link href="/scheduler/candidates">유지안·후보의 프로파일, 원시 근거와 전환 비용</Link></li>
      <li><Link href="/scheduler/candidates?view=snapshot">실제 노드 준비·인스턴스·할당·큐·라우팅과 후보 비교</Link></li>
      <li><Link href="/resources">현재 노드 자원과 준비 상태</Link></li>
      <li><Link href="/transitions">실행 동작과 실제 변경 후 상태</Link></li>
      {range&&<li><Link href={`/scheduler/history?${range}&filter=decision`}>입력 관측 구간의 판단 기록</Link></li>}
      {range&&<li><Link href={`/scheduler/power?${range}`}>같은 구간의 실측 전력</Link></li>}
    </ul>
    <h3>실제 요청 지연과 결과</h3>
    <p className="text-sm text-muted-foreground">판단의 입력 관측 구간이에요. 미래 예상, 변경 이후의 성과나 에너지 귀속이 아니에요. 다른 구간은 요청 기록에서 골라요.</p>
    {range?workloads.length?<ul className="grid gap-1.5">{workloads.map(workload=><li key={workload.uid}><ObservationLink uid={workload.uid} definition={chosen.slo?.find(row=>row.uid===workload.uid)?.definition} from={decision.search.windowStart} to={decision.search.windowEnd}>{workload.name} 실제 요청 보기</ObservationLink></li>)}</ul>:<p>실제 요청 관측을 연결할 서비스가 없어요.</p>:<p>입력 구간이 없어 요청·전력 관측을 연결할 수 없어요.</p>}
  </>
}

export function SchedulerPage() {
  const {token}=useSession(),[poll,setPoll]=React.useState(true)
  const query=useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll}),view=query.data,now=view&&judgment(view)
  const reasons=(now?.decision.reasons??[]).map(reason=>reasonLabels[reason]??statusLabel(reason))
  return <Screen status={now&&<StatusBadge tone={judgmentTones[now.status]??"idle"}>{judgmentLabels[now.status]??statusLabel(now.status)}</StatusBadge>}
    title={!now?"운영 판단":now.target?"다른 구성이 더 좋아요":headlines[now.status]??"판단을 확인해야 해요"}
    lede={!view||!now?"관측한 부하로 지금 구성과 다른 구성을 비교해요.":now.first?`구성 관측 ${observed(view.state.snapshot.at)?localTime(view.state.snapshot.at):"대기 중"} · 첫 판단 전이에요`:`${localTime(now.decision.at)} · 용량 합으로 판단 · 자동 제어 서비스 ${view.settings.autonomousWorkloads?.length??0}개`}
    action={<Button asChild><Link href="/scheduler/candidates">후보 비교하기</Link></Button>}>
    <QueryGate query={query}>{view&&now&&<>
      {!now.first&&<EnergyCompare view={view}/>}
      {reasons.length>0&&<Rows><RowDialog label="판단 이유" hint={reasons[0]} value={reasons.length>1?`${reasons.length}가지`:undefined} title="판단 이유"><ul className="list-inside list-disc">{reasons.map((reason,index)=><li key={index}>{reason}</li>)}</ul><SourceJSON inline title="원래 사유 코드 보기" value={now.decision.reasons}/></RowDialog></Rows>}
    </>}</QueryGate>
    <Rows label="자세히">
      <Row href="/operations/flow" label="운영 흐름 보드" hint="수신, 실행과 구성 변경의 실시간 흐름"/>
      <Row href="/scheduler/estimates" label="서비스별 응답 목표 예상" value={now?.estimates.length?`초과 ${now.exceeding} / ${now.estimates.length}`:undefined}/>
      <Row href="/scheduler/load" label="실제 부하" hint="구간마다 받은 요청과 처리"/>
      <Row href="/transitions" label="변경 진행" value={view?`${view.state.actions?.length??0}건`:undefined}/>
      <Row href="/scheduler/profiles" label="측정 프로파일" hint="운영 판단이 쓰는 측정 결과"/>
      <Row href="/scheduler/capacity" label="인스턴스 용량 계산 근거" hint="조건별 κ와 탐색 판정, 현재 설정 대조"/>
      <Row href="/resources" label="장비"/>
      <Row href="/pools" label="실행 그룹"/>
    </Rows>
    <Rows label="더 보기">
      {view&&<RowDialog label="설정부터 실제 운영까지" hint="자동 적용, 판단 근거와 실제 관측" title="설정부터 실제 운영까지"><ActualControl view={view}/></RowDialog>}
      <RowDialog label="자동 새로고침" value={poll?"켜짐":"꺼짐"} title="자동 새로고침"><label className="switch"><input type="checkbox" checked={poll} onChange={event=>setPoll(event.target.checked)}/>새 판단을 자동으로 확인하기</label><p className="text-sm text-muted-foreground">{query.dataUpdatedAt>0?`마지막 확인 ${new Date(query.dataUpdatedAt).toLocaleTimeString("ko-KR",{hour:"2-digit",minute:"2-digit"})}`:"아직 확인하지 않았어요"}</p><Button variant="outline" onClick={()=>void query.refetch()} disabled={!token||query.isFetching}>지금 새로고침</Button></RowDialog>
      {view&&<RowDialog label="원본 자료" title="현재 응답 원본"><SourceJSON inline title="현재 응답 원본" value={view}/><SourceExport value={{scope:"현재 응답",source:"v1/scheduler",view}} filename="chill-scheduler-current.json"/></RowDialog>}
    </Rows>
  </Screen>
}

export function SchedulerCandidatesPage() {
  const query=useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll:true}),params=useSearchParams(),view=query.data
  const initial=params.get("view")??"",count=view?candidatesOf(view.state.decision).length:0
  return <Screen back={back} title={!view?"후보 비교":count?`후보 ${count}개를 비교했어요`:"아직 비교한 후보가 없어요"} lede="하나를 누르면 그때의 근거를 보여줘요. 새 판단이 와도 보던 내용은 바뀌지 않아요.">
    <QueryGate query={query}>{view&&<CandidateList key={initial} view={view} initial={initial}/>}</QueryGate>
  </Screen>
}

type Selection={key:string;view:SchedulerView}
// Opening a row freezes the response it was opened on; polling never changes evidence (or the list) under the reader.
function CandidateList({view,initial}:{view:SchedulerView;initial:string}) {
  const [selected,setSelected]=React.useState<Selection|null>(()=>initial==="snapshot"?{key:"snapshot",view}:null)
  const {decision,snapshot}=(selected?.view??view).state
  let reviewed=0
  const choices:{key:string;label:string;hint:string;value?:string}[]=[{key:"snapshot",label:"현재 실제 구성",hint:observed(snapshot.at)?`${localTime(snapshot.at)} 관측`:"관측 시각 없음"},...candidatesOf(decision).map(candidate=>({key:candidate.id,label:candidate.keep?"현재 구성 유지":decision.target?.id===candidate.id?"선택한 변경 후보":`검토한 후보 ${++reviewed}`,hint:judgmentLabels[candidate.status]??statusLabel(candidate.status),value:candidate.expectedJoules==null?"계산값 없음":joules(candidate.expectedJoules)}))]
  if(selected&&!choices.some(choice=>choice.key===selected.key))choices.push({key:selected.key,label:"선택했던 후보",hint:"이 응답에는 없어요"})
  return <Rows label="볼 대상">{choices.map(choice=><RowDialog key={choice.key} label={choice.label} hint={choice.hint} value={choice.value} title={choice.label} open={selected?.key===choice.key} onOpenChange={open=>setSelected(open?{key:choice.key,view}:null)}>
    {selected?.key===choice.key&&<Frozen selected={selected} latest={view} onLatest={()=>setSelected({key:choice.key,view})}/>}
  </RowDialog>)}</Rows>
}
function Frozen({selected,latest,onLatest}:{selected:Selection;latest:SchedulerView;onLatest:()=>void}) {
  const frozen=selected.view.state
  const chosen=selected.key==="snapshot"?undefined:[frozen.decision.keep,...(frozen.decision.target?[frozen.decision.target]:[])].find(candidate=>candidate.id===selected.key)
  const changed=frozen.decision.at!==latest.state.decision.at||frozen.snapshot.at!==latest.state.snapshot.at
  return <>
    {changed&&<div role="status" className="notice flex flex-wrap items-center justify-between gap-3"><span>새 판단 {time(latest.state.decision.at)} 또는 구성 {time(latest.state.snapshot.at)}을 확인했어요.</span><Button size="sm" variant="outline" onClick={onLatest}>최신 응답으로 보기</Button></div>}
    {chosen?<CandidateDetails candidate={chosen} view={selected.view}/>:selected.key==="snapshot"?<SnapshotDetails snapshot={frozen.snapshot}/>:<p>선택한 후보가 이 응답에 없어요.</p>}
    <SourceExport filename="chill-scheduler-selected-detail.json" value={{scope:"선택 상세",source:"v1/scheduler",key:selected.key,view:selected.view}}/>
  </>
}

const latency=(row?:SchedulerEstimate)=>row?.expectedNanos==null?"계산값 없음":`${(Number(row.expectedNanos)/1e6).toLocaleString("ko-KR",{maximumFractionDigits:1})} ms`
// Keep and target estimates per service in the decision's snapshot; the detail opens per service.
export function SchedulerEstimatesPage() {
  const query=useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll:true}),view=query.data,now=view&&judgment(view)
  const services=(view?.state.snapshot.workloads??[]).filter((row):row is SchedulerDefinition=>!!row)
  const estimate=(candidate:SchedulerCandidate|null|undefined,uid:string)=>now&&!now.first?candidate?.slo?.find(row=>row.uid===uid):undefined
  const chosenOf=(uid:string)=>estimate(now?.target??now?.decision.keep,uid)
  const over=services.filter(row=>{const chosen=chosenOf(row.uid);return chosen?.expectedNanos!=null&&chosen.normalizedExceedance>0}).length
  const missing=services.filter(row=>chosenOf(row.uid)?.expectedNanos==null).length
  return <Screen back={back} title={!now?"서비스별 응답 목표 예상":!services.length?"판단에 포함된 서비스가 없어요":over?`${over}개 서비스가 응답 목표를 넘을 것 같아요`:missing===services.length?"아직 응답 시간 예상이 없어요":missing?"계산한 서비스는 모두 응답 목표 안이에요":"모든 서비스가 응답 목표 안에 들 것 같아요"}
    lede={now?.target?"현재 구성 유지와 선택한 변경 후보의 예상이에요.":"현재 구성을 유지할 때의 예상이에요."}>
    <QueryGate query={query}>{view&&now&&services.length>0&&<Rows label="서비스">{services.map(row=>{
      const chosen=chosenOf(row.uid),keep=estimate(now.decision.keep,row.uid),target=now.target&&estimate(now.target,row.uid)
      return <RowDialog key={row.uid} label={row.name} hint={`${row.slo.percentile}% 요청을 ${ms(row.slo.targetLatencyNanos)} 안에`} value={!chosen||chosen.expectedNanos==null?<StatusBadge tone="idle">계산값 없음</StatusBadge>:chosen.normalizedExceedance>0?<StatusBadge tone="warn">목표 초과 예상</StatusBadge>:<StatusBadge tone="ok">목표 안 예상</StatusBadge>} title={`${row.name} 응답 시간 예상`}>
        <Facts items={[["응답 목표",`${row.slo.percentile}% 요청을 ${ms(row.slo.targetLatencyNanos)} 안에`],["현재 구성 유지",latency(keep)],...(now.target?[["선택한 변경 후보",latency(target||undefined)] as [string,string]]:[]),["표본 수",chosen?String(chosen.samples):"제공 없음"],["목표 초과 정도",chosen?String(chosen.normalizedExceedance):"제공 없음"],["예상 결과",Object.entries(chosen?.predictedOutcomes??{}).map(([key,value])=>`${statusLabel(key)} ${value}`).join(", ")||"제공 없음"]]}/>
        <p className="text-sm text-muted-foreground">예상 지연은 반복 비교 결과예요. 실측과 달라요.</p>
        <ul className="grid gap-1.5"><li><Link href={`/workloads/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`}>서비스 보기</Link></li><li><ObservationLink uid={row.uid} definition={chosen?.definition} from={now.decision.search.windowStart} to={now.decision.search.windowEnd}>판단에 쓴 구간의 실제 요청 보기</ObservationLink></li></ul>
        <SourceJSON inline value={{definition:row,keep,target}}/>
      </RowDialog>
    })}</Rows>}</QueryGate>
  </Screen>
}

type WorkloadChoice=IdentityWorkload
const path="/scheduler/load"
const shortTime=(at:string)=>{const date=new Date(at);return Number.isFinite(date.getTime())?date.toLocaleString("ko-KR",{dateStyle:"short",timeStyle:"short"}):"시각 없음"}
// Event-time load for an explicit window. The window lives in the URL with its exact (nanosecond) text; only edited times are re-encoded.
export function SchedulerLoadPage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const [entry]=React.useState(()=>{const now=Date.now();return {from:new Date(now-3600000).toISOString(),to:new Date(now).toISOString()}})
  const submitted={from:params.get("from")??entry.from,to:params.get("to")??entry.to}
  const [draft,setDraft]=React.useState(submitted),[uid,setUID]=React.useState(params.get("uid")??""),[validation,setValidation]=React.useState(""),[open,setOpen]=React.useState(false)
  const address=params.toString(),[previousAddress,setPreviousAddress]=React.useState(address)
  if(previousAddress!==address){setPreviousAddress(address);setDraft(submitted);setUID(params.get("uid")??"")}
  React.useEffect(()=>{if(!params.has("from")||!params.has("to")){const next=new URLSearchParams(params.toString());if(!next.has("from"))next.set("from",entry.from);if(!next.has("to"))next.set("to",entry.to);router.replace(`${path}?${next}`,{scroll:false})}},[params,entry,router])
  const selectedUID=params.get("uid")??""
  const query=useChillQuery<LoadWindow>(`v1/observations/load?${new URLSearchParams(submitted)}`,{decoder:parseSchedulerJSON}),workloads=useChillQuery<WorkloadChoice[]>("v1/workloads"),data=query.data
  const recordUIDs=[...(data?.workloads?.map(row=>row.uid)??[]),uid,selectedUID].filter(Boolean)
  const definitions=useWorkloadDefinitions(recordUIDs,submitted)
  const choices=resolveWorkloadIdentities(workloads.data,definitions,[],recordUIDs)
  const name=(recordUID:string)=>choices.get(recordUID)!
  const rows=(data?.workloads??[]).filter(row=>!selectedUID||row.uid===selectedUID),total=rows.reduce((sum,row)=>sum+row.received,0)
  function apply() {
    const from=Date.parse(draft.from),to=Date.parse(draft.to)
    if(!Number.isFinite(from)||!Number.isFinite(to)||from>to||to-from>config.maxWindowHours*3600000){setValidation(`조회 구간을 최대 ${config.maxWindowHours}시간 안에서 골라 주세요.`);return}
    const next=new URLSearchParams(params.toString());next.set("from",draft.from);next.set("to",draft.to);if(uid)next.set("uid",uid);else next.delete("uid")
    setValidation("");setOpen(false);router.replace(`${path}?${next}`,{scroll:false})
  }
  return <Screen back={back} title={!data?"실제 부하":total?`요청 ${total.toLocaleString("ko-KR")}건을 받았어요`:"이 구간에 받은 요청이 없어요"} lede="사건이 일어난 시각으로 셌어요. 완료 수는 이 구간에 받은 요청의 완료율이 아니에요.">
    <Rows><RowDialog open={open} onOpenChange={setOpen} label="조회 조건" hint={`${selectedUID?name(selectedUID):"전체 서비스"} · ${shortTime(submitted.from)} ~ ${shortTime(submitted.to)}`} title="조회 조건" description="현재 기기의 시간대로 골라요. 바꾼 시각만 새 값으로 바뀌어요.">
      <form noValidate className="grid gap-4" onSubmit={event=>{event.preventDefault();apply()}}>
        <label className="grid gap-1.5">서비스<select value={uid} onChange={event=>setUID(event.target.value)}><option value="">전체 서비스</option>{Array.from(choices,([recordUID,label])=><option key={recordUID} value={recordUID}>{label}</option>)}</select></label>
        {(workloads.error||workloads.isPending)&&<><QueryGate query={workloads}>{null}</QueryGate><p className="text-sm text-muted-foreground">조회된 부하의 서비스는 계속 고를 수 있어요.</p></>}
        <label className="grid gap-1.5">시작<input type="datetime-local" value={localDateTime(draft.from)} onChange={event=>{const from=utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"");setDraft(current=>({...current,from}))}} required/></label>
        <label className="grid gap-1.5">종료<input type="datetime-local" value={localDateTime(draft.to)} onChange={event=>{const to=utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"");setDraft(current=>({...current,to}))}} required/></label>
        {validation&&<p role="alert">{validation}</p>}
        <Button type="submit">이 조건으로 보기</Button>
      </form>
    </RowDialog></Rows>
    <QueryGate query={query}>{data&&<>
      {data.gapUntil&&<p role="alert">{gapNotice(data.gapUntil)}. 실제보다 적게 셌을 수 있어요.</p>}
      {rows.length?<Rows label="서비스별">{rows.map((row,index)=><RowDialog key={`${row.uid}/${index}`} label={name(row.uid)} hint={`배정 ${row.assigned}건 · 완료 ${row.completed}건`} value={`${row.received}건`} title={`${name(row.uid)} 부하`}>
        <Facts items={[["받음",`${row.received}건 · ${row.receivedInputsPerSecond} 입력/s`],["접수",`${row.accepted}건 · ${row.acceptedInputsPerSecond} 입력/s`],["배정",`${row.assigned}건 · ${row.assignedInputsPerSecond} 입력/s`],["완료",`${row.completed}건 · ${row.completionEventsPerSecond} 건/s`]]}/>
        <h3>인스턴스별 배정</h3>{Object.entries(row.instanceAssignments??{}).map(([instance,count])=><p key={instance} className="break-all">{instance}: {count}건</p>)}{!Object.keys(row.instanceAssignments??{}).length&&<p>제공된 배정이 없어요.</p>}
        <p><ObservationLink uid={row.uid} from={data.from} to={data.to}>같은 구간의 요청 기록 보기</ObservationLink> (적용 설정 전체)</p>
        <p className="break-all text-sm text-muted-foreground">서비스 UID {row.uid||"제공 없음"}</p>
        <SourceJSON inline value={row}/><SourceExport filename="chill-load-selected-service.json" value={{scope:"선택 서비스의 구간 집계",source:"v1/observations/load",from:data.from,to:data.to,population:data.population,workload:row,arrivals:null}}/>
      </RowDialog>)}</Rows>:<p>선택한 서비스와 구간의 사건 집계가 없어요.</p>}
      <Rows><RowDialog label="원본 자료와 내보내기" title="선택 구간 원본"><Facts items={[["구간",<span key="range" className="break-all">{data.from} ~ {data.to}</span>],["길이",ms(data.durationNanos)],["모집단",data.population],["관측 시작",data.gapUntil?gapNotice(data.gapUntil):"구간 전체를 관측했어요"]]}/><SourceJSON inline title="선택 구간 원본" value={data}/><SourceExport filename="chill-load-selected-window.json" value={{scope:"선택 구간의 전체 응답",source:"v1/observations/load",from:data.from,to:data.to,population:data.population,sourceData:data}}/></RowDialog></Rows>
    </>}</QueryGate>
  </Screen>
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
