"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { LoadWindow, SchedulerAllocation, SchedulerCandidate, SchedulerDecision, SchedulerDefinition, SchedulerEstimate, SchedulerInstance, SchedulerNode, SchedulerSnapshot, SchedulerView, WireInteger } from "@/lib/read-types"
import { nanosToMilliseconds, parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { safeSource } from "@/lib/source-export"
import { localDateTime, utcDateTime } from "@/lib/date-time"
import { DataTable } from "@/components/data-table"
import { ReadRegion, DetailsDialog, SourceJSON, SourceExport, StatusBadge, type Tone } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { SubNav } from "@/components/sub-nav"

export const judgmentLabels:Record<string,string>={Pending:"판단 대기",Keep:"현재 구성 유지",Feasible:"목표 충족",Overload:"예상 부하가 목표 초과",InfeasibleLifetime:"요청 유효기간 충족 어려움",InfeasibleResources:"자원 조건 충족 어려움",InfeasibleTransition:"변경 조건 충족 어려움",ExecutionError:"실행 오류 확인"}
export const judgmentTones:Record<string,Tone>={Keep:"ok",Feasible:"ok",Pending:"idle",Overload:"warn",InfeasibleLifetime:"warn",InfeasibleResources:"warn",InfeasibleTransition:"warn",ExecutionError:"fail"}
export const actionKinds:Record<string,string>={prepare:"실행 준비",route:"요청 분배 변경",stop:"요청 접수 중지",drain:"기존 요청 마무리",remove:"인스턴스 제거",power:"전력모드 변경",membership:"실행 그룹 소속 변경",resize:"인스턴스 교체"}
export const actionStatuses:Record<string,string>={planned:"계획됨",starting:"시작 기록",running:"실행 중",complete:"완료",failed:"실패",cancel:"취소",partial:"일부 변경 관측"}
const actionTones:Record<string,Tone>={complete:"ok",failed:"fail",partial:"warn",running:"info",starting:"info"}
const observed=(at?:string)=>!!at&&!at.startsWith("0001-")
const joules=(value:number)=>value>=1000?`${(value/1000).toLocaleString("ko-KR",{maximumFractionDigits:2})} kJ`:`${value.toLocaleString("ko-KR",{maximumFractionDigits:1})} J`
const utc=(at:string)=>`${new Date(at).toLocaleString("ko-KR",{timeZone:"UTC",dateStyle:"medium",timeStyle:"short"})} UTC`
const phaseLabels:Record<string,string>={pending:"미배정",assigned:"배정 후 실행 단계 확인 대기",queued:"실제 대기열",deferred:"SLO 위반 후 후순위 대기",running:"실행 중",returning:"응답 반환 중"}
const time=(value:string)=>!value||value.startsWith("0001-")?"관측 시각 없음":value
const ms=(value:WireInteger)=>`${nanosToMilliseconds(value)} ms`
const energy=(value:number|null)=>value==null?"계산값 없음":`${value} J`
const reasonLabels:Record<string,string>={MissingPoolInventory:"실행 그룹의 노드 관측이 필요합니다.",MissingImmutableDefinition:"요청의 고정 서비스 정의가 필요합니다.",NoFullyEvaluableCandidateWithinReportedBounds:"제공된 탐색 범위에서 전체 계산을 마친 후보를 기다립니다.",NoResourceFeasibleCandidateWithinReportedBounds:"제공된 탐색 범위에서 자원 조건을 충족하는 후보가 필요합니다.",NoReadyTransitionWithinReportedBounds:"제공된 탐색 범위에서 실행 가능한 변경을 기다립니다.",EvaluatedCandidatesExceedSLOWithinReportedSearchBounds:"계산한 후보의 예상 지연이 서비스 목표를 초과합니다.",UnevaluableCandidatesRemainPending:"추가 근거를 기다리는 후보가 있습니다.",ExplicitRequestLifetimeCapacityExceeded:"요청 유효기간 안의 처리 용량을 확인합니다.",ObservedOperationExecutionError:"관측된 변경 실행 오류를 확인합니다."}

function Lines({values}:{values:string[]|null}) {return values?.length?<ul className="list-inside list-disc">{values.map((value,index)=><li key={index}>{reasonLabels[value] ?? value}</li>)}</ul>:<p>제공된 항목 없음</p>}
function Allocation({value}:{value:SchedulerAllocation}) {return <span>CPU {value.cpus?.join(", ")||"목록 없음"} / {value.cpuCount}개, 스레드 {value.threads}, CPU 예약 {value.cpuReserved?"사용":"미사용"}, GPU {value.gpuUUID||"없음"}, 메모리 {String(value.memoryBytes)} bytes</span>}
function RelatedLinks() {return <SubNav label="운영 판단 관련 화면" links={[["/scheduler","운영 판단"],["/scheduler/history","판단 기록"],["/scheduler/power","전력 기록"],["/transitions","변경 진행"],["/configuration","운영 설정"]]} />}
function ObservationLink({uid,definition,from,to,children}:{uid:string;definition?:string;from:string;to:string;children:React.ReactNode}) {
  if(!uid||!from||!to||from.startsWith("0001-")||to.startsWith("0001-"))return <span>{children}</span>
  const query=new URLSearchParams({uid,from,to});if(definition)query.set("definition",definition)
  return <Link href={`/observations?${query}`}>{children}</Link>
}
function ServiceName({uid,snapshot}:{uid:string;snapshot:SchedulerSnapshot}) {return <span>{snapshot.workloads?.find(item=>item?.uid===uid)?.name??"이전 등록 서비스"} <small className="break-all">({uid})</small></span>}
function SLOs({candidate,decision,snapshot}:{candidate:SchedulerCandidate;decision:SchedulerDecision;snapshot:SchedulerSnapshot}) {
  return <><p>예상 에너지 {energy(candidate.expectedJoules)}. 예상 지연은 반복 비교 결과이며 실측과 구분합니다.</p>{candidate.expectedJoules==null&&<Lines values={candidate.reasons}/>}<div className="overflow-x-auto"><table className="w-full text-sm"><thead><tr><th>서비스</th><th>예상 지연</th><th>목표</th><th>표본 수</th><th>목표 초과 비율</th><th>예상 결과</th></tr></thead><tbody>{candidate.slo?.map((row,index)=><tr key={index}><td><ObservationLink uid={row.uid} definition={row.definition} from={decision.search.windowStart} to={decision.search.windowEnd}><ServiceName uid={row.uid} snapshot={snapshot}/></ObservationLink><small className="block break-all">{row.definition}</small></td><td>{row.expectedNanos==null?"계산값 없음":`${Number(row.expectedNanos) / 1e6} ms`} ({row.percentile}% 기준)</td><td>{ms(row.targetNanos)}</td><td>{row.samples}</td><td>{row.normalizedExceedance}</td><td>{Object.entries(row.predictedOutcomes??{}).map(([key,value])=>`${key}: ${value}`).join(", ")||"제공 없음"}</td></tr>)}</tbody></table></div>{!candidate.slo?.length&&<p>서비스별 예상값 제공 없음</p>}</>
}
function Configuration({nodes,instances,routing,snapshot}:{nodes:SchedulerNode[]|null;instances:SchedulerInstance[]|null;routing:SchedulerCandidate["routing"];snapshot:SchedulerSnapshot}) {
  return <><h3>노드와 실행 그룹</h3>{nodes?.length?nodes.map(node=><section key={node.node.uid} className="space-y-2 rounded border p-3"><h4>{node.node.name} / {node.pool||"실행 그룹 없음"}</h4><p className="break-all">노드 UID {node.node.uid}</p><p>준비 {node.ready?"완료":"확인 대기"} / 전력모드 {node.powerMode||"제공 없음"} / 관측 {time(node.observedAt)}</p>{node.reason&&<p>{node.reason}</p>}<p>전체 CPU {node.cpus?.join(", ")||"없음"}, GPU {node.gpuUUIDs?.join(", ")||"없음"}, 메모리 {String(node.memoryBytes)} bytes</p><p>예약: <Allocation value={node.reserved}/></p><h5>서비스 외 할당</h5>{node.nonServing?.map((allocation,index)=><p key={index}><Allocation value={allocation}/></p>)}{!node.nonServing?.length&&<p>제공된 할당 없음</p>}</section>):<p>제공된 노드 없음</p>}
    <h3>인스턴스와 서비스 연결</h3>{instances?.map(instance=><Instance key={instance.id} instance={instance} snapshot={snapshot}/>)}{!instances?.length&&<p>제공된 인스턴스 없음</p>}
    <h3>요청 분배</h3><p>원래 가중치이며 확률이 아닙니다.</p>{Object.entries(routing??{}).map(([uid,weights])=><div key={uid}><ServiceName uid={uid} snapshot={snapshot}/><ul>{Object.entries(weights).map(([id,weight])=><li key={id}>{id}: {weight}</li>)}</ul></div>)}{!Object.keys(routing??{}).length&&<p>제공된 요청 분배 없음</p>}</>
}
function Instance({instance,snapshot}:{instance:SchedulerInstance;snapshot:SchedulerSnapshot}) {
  const profile=instance.profile
  return <section className="space-y-2 rounded border p-3"><h4 className="break-all">{instance.id}</h4><p>{instance.node.name} / {instance.pool} / 준비 {instance.ready?"완료":"확인 대기"} / 처리 중지 {instance.stopped?"사용":"미사용"}</p><p><Allocation value={instance.allocation}/></p><p>처리 용량 {instance.capacity}, 원래 가중치 {instance.weight}, 최대 배치 {instance.maxBatch}, 최대 대기 {ms(instance.maxWaitNanos)}</p><p>완료 오버헤드(왕복 교환 잔여+응답) {ms(instance.completionOverheadNanos)} / {instance.completionObserved?"측정됨":"미측정"}, 정책 출처 {instance.envelopeSource}, 관측 {time(instance.observedAt)}</p><p>유휴 계측기 {instance.idleMeter} / 범위 {instance.idleScope}</p><ul>{instance.workloadUIDs?.map(uid=><li key={uid}><ServiceName uid={uid} snapshot={snapshot}/></li>)}</ul><details><summary>도착 경과 시간</summary>{Object.entries(instance.arrivalAgesNanos??{}).map(([uid,ages])=><p key={uid}>{uid}: {ages.map(ms).join(", ")}</p>)}</details>{profile?<details><summary>프로파일 {profile.id}</summary><p>노드 {profile.nodeName}, 측정 {time(profile.measuredAt)}, 신뢰 수준 {profile.confidenceLevel}, 유휴 {profile.idleBaselineWatts} W</p><p>조건</p><pre className="source-json">{stringifySchedulerJSON(safeSource(profile.condition),2)}</pre>{profile.batches?.map((batch,index)=><p key={index}>배치 {batch.batch}: 지연 표본 {batch.durationNanos?.map(value=>`${Number(value) / 1e6} ms`).join(", ")||"없음"}, 입력당 {batch.energyPerInputJoules} J, 메모리 {String(batch.memoryBytes)} bytes</p>)}<p>참조와 측정 근거</p><pre className="source-json">{stringifySchedulerJSON(safeSource({reference:profile.reference,evidence:profile.evidence,runtimeMetadata:profile.runtimeMetadata,metricSource:profile.metricSource}),2)}</pre></details>:<p>프로파일 제공 없음</p>}</section>
}
export function SnapshotDetails({snapshot}:{snapshot:SchedulerSnapshot}) {
  return <><p>구성 관측 {time(snapshot.at)}</p><Lines values={snapshot.reasons}/><Configuration nodes={snapshot.nodes} instances={snapshot.instances} routing={snapshot.routing} snapshot={snapshot}/><h3>처리 중 요청</h3><p>배정 후 실행 단계 확인 대기는 실제 대기열과 구분합니다.</p>{snapshot.outstanding?.map((request,index)=><details key={index}><summary>{phaseLabels[request.phase]??request.phase} / {request.definition?.name??"정의 제공 없음"} / {request.id}</summary><p>서비스 UID {request.definition?.uid}, 인스턴스 {request.instance||"미배정"}, 배치 {request.batchID||"없음"}, 입력 경로 {request.inputPath}, 큐 순서 {request.queueOrder}</p><p>수신 {time(request.receivedAt)}, 전체 경과 {ms(request.elapsedNanos)}, 대기 {ms(request.queueAgeNanos)}, 실행 {ms(request.runningElapsedNanos)}, 반환 {ms(request.returnElapsedNanos)}</p>{request.phase==="returning"&&<p>왕복 교환 잔여 {request.exchangeResidualNanos==null?"미확보":ms(request.exchangeResidualNanos)}{request.nativeOutcome&&<> / native 결과 {request.nativeOutcome}</>}</p>}<p>남은 유효기간 {request.remainingLifetimeNanos==null?"제공 없음":ms(request.remainingLifetimeNanos)}, 도착률 {request.arrivalRate}, native sender 경과 {request.nativeSenderElapsedMS??"제공 없음"} ms</p></details>)}{!snapshot.outstanding?.length&&<p>제공된 처리 중 요청 없음</p>}<h3>참조</h3><Lines values={snapshot.references}/><SourceJSON inline value={snapshot}/></>
}
export function CandidateDetails({candidate,view}:{candidate:SchedulerCandidate;view:SchedulerView}) {
  const {snapshot,decision}=view.state
  return <><p>{candidate.keep?"현재 구성 유지안":"변경 후보"} / {judgmentLabels[candidate.status]??candidate.status} / {candidate.id}</p><Lines values={candidate.reasons}/><SLOs candidate={candidate} decision={decision} snapshot={snapshot}/><h3>비교 조건</h3><p>입력 관측 구간 {time(decision.search.windowStart)} ~ {time(decision.search.windowEnd)}</p><p>미래 예상 기간 {ms(decision.settings.horizonNanos)}</p><p>모집단 {decision.search.population}</p><p>수신 {decision.search.received}, 접수 {decision.search.accepted}, 계산 입력 {decision.search.computeInputs}, 검토 {decision.search.examined}, 탐색 상한 도달 {decision.search.truncated?"예":"아니요"}</p><details><summary>탐색 상한</summary><dl>{Object.entries(decision.search.bounds).filter(([key])=>key!=="controls").map(([key,value])=><React.Fragment key={key}><dt>{key}</dt><dd>{String(value)}</dd></React.Fragment>)}</dl><p>제어 조건 {Object.entries(decision.search.bounds.controls).map(([key,value])=>`${key}: ${value?"사용":"미사용"}`).join(", ")}</p></details><h3>서비스별 입력</h3>{decision.search.loads?.map(load=><p key={load.uid}><ServiceName uid={load.uid} snapshot={snapshot}/> 수신 {load.received}, 접수 {load.accepted}, 계산 입력 {load.computeInputs}, {load.inputsPerSecond} 입력/s</p>)}<h3>가정</h3><Lines values={candidate.assumptions}/><h3>출처</h3><Lines values={candidate.sources}/><h3>참조</h3><Lines values={decision.references}/><h3>자원 조건과 목표 구성</h3><p>자원 충족 여부는 owner가 제공한 상태와 사유로 확인합니다.</p><Configuration nodes={candidate.nodes} instances={candidate.instances} routing={candidate.routing} snapshot={snapshot}/><h3>실행 계획</h3>{candidate.actions?.map(action=><p key={action.id}>{action.kind} / {action.node.name} / {action.instance} / {action.status} / 예상 경과 {ms(action.elapsedNanos)} / {action.reason} / 의존 {action.dependsOn?.join(", ")||"없음"}</p>)}<h3>유휴 기준 전력</h3>{snapshot.baselines?.map(baseline=><details key={baseline.id}><summary>{baseline.node.name}: {baseline.watts} W ({baseline.powerMode})</summary><p>상태 {baseline.state?.join(", ")}, 계측기 {baseline.meter}, 범위 {baseline.scope}, 측정 {time(baseline.measuredAt)}</p><pre className="source-json">{stringifySchedulerJSON(safeSource(baseline.reference),2)}</pre></details>)}{!snapshot.baselines?.length&&<p>제공된 기준 전력 없음</p>}<h3>변경 비용 측정</h3>{snapshot.costs?.map(cost=><details key={cost.id}><summary>{cost.node.name} / {cost.kind} / {cost.from} → {cost.to}</summary><p>전력모드 {cost.powerMode}, 계측기 {cost.meter}, 범위 {cost.scope}, 에너지 범위 {cost.energyScope}, 차감 {cost.subtractedWatts??"제공 없음"} W, 이전 기준 {cost.beforeBaselineWatts??"제공 없음"} W</p><p>이전 상태 {cost.beforeState?.join(", ")}, 목표 상태 {cost.targetState?.join(", ")}</p>{cost.trials?.map((trial,index)=><p key={index}>측정 {index+1}: {ms(trial.durationNanos)}, {trial.joules} J</p>)}<pre className="source-json">{stringifySchedulerJSON(safeSource(cost.reference),2)}</pre></details>)}{!snapshot.costs?.length&&<p>제공된 비용 측정 없음</p>}<SourceJSON inline value={{candidate,decision,snapshot}}/></>
}
export function Judgment({view,actions}:{view:SchedulerView;actions?:React.ReactNode}) {
  const {decision,snapshot}=view.state
  const first=!observed(decision.at)
  const status=first?"Pending":decision.status
  const target=decision.target&&decision.target.id!==decision.keep.id?decision.target:null
  const rows:[string,SchedulerCandidate][]=first?[]:[["현재 구성 유지",decision.keep],...(target?[["선택한 변경 후보",target] as [string,SchedulerCandidate]]:[])]
  const values=rows.map(([,candidate])=>candidate.expectedJoules).filter((value):value is number=>value!=null)
  const scale=Math.max(...values,0)
  const estimates=(target??decision.keep).slo??[]
  const exceeding=estimates.filter(row=>row.normalizedExceedance>0).length
  return <div className="grid gap-5">
    <div className="judgment-head">
      <div className="grid gap-1"><p className="judgment-title">{first?"첫 판단을 기다리고 있어요":judgmentLabels[status]??status}</p><p className="text-sm text-muted-foreground">{first?`구성 관측 ${observed(snapshot.at)?utc(snapshot.at):"대기 중"}`:`판단 ${utc(decision.at)}`} · 자동 적용 {view.settings.controls.enabled?"사용":"미사용"}</p></div>
      <StatusBadge tone={judgmentTones[status]??"idle"}>{first?"판단 대기":status==="Keep"?"유지":target?"변경 후보 있음":"후보 없음"}</StatusBadge>
    </div>
    {rows.length>0&&<dl className="energy-rows" aria-label="예상 에너지">{rows.map(([label,candidate])=>{
      const value=candidate.expectedJoules
      const delta=target&&candidate===target&&value!=null&&decision.keep.expectedJoules?(value-decision.keep.expectedJoules)/decision.keep.expectedJoules*100:null
      return <div key={label} className="energy-row"><dt>{label}</dt><div className="energy-track" aria-hidden="true">{value!=null&&scale>0&&<div className="energy-fill" data-hot={value===scale&&values.length>1} style={{width:`${Math.max(value/scale*100,2)}%`}}/>}</div><dd>{value==null?<small>계산값 없음</small>:joules(value)}{delta!=null&&<small className={delta<0?"block text-ok":"block text-heat"}>유지 대비 {delta>0?"+":"−"}{Math.abs(delta).toFixed(1)}%</small>}</dd></div>
    })}</dl>}
    {decision.reasons?.length?<Lines values={decision.reasons}/>:null}
    <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
      <p className="text-sm text-muted-foreground">{estimates.length?`응답 목표 초과 예상 서비스 ${exceeding} / ${estimates.length}`:"서비스별 지연 예상이 아직 없어요"} · 에너지와 지연은 예상값이며 실측 전력이 아니에요.</p>
      {actions&&<div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  </div>
}

const latency=(row?:SchedulerEstimate)=>row?.expectedNanos==null?"계산값 없음":`${(Number(row.expectedNanos)/1e6).toLocaleString("ko-KR",{maximumFractionDigits:1})} ms`
// Keep and target estimates side by side, one row per service in the decision's snapshot.
function ServiceEstimates({view}:{view:SchedulerView}) {
  const {decision,snapshot}=view.state
  const target=observed(decision.at)&&decision.target&&decision.target.id!==decision.keep.id?decision.target:null
  const rows=(snapshot.workloads??[]).filter((row):row is SchedulerDefinition=>!!row)
  const estimate=(candidate:SchedulerCandidate|null,uid:string)=>observed(decision.at)?candidate?.slo?.find(row=>row.uid===uid):undefined
  return <section className="read-region" id="services" aria-label="서비스별 응답 목표 예상">
    <div className="read-region-heading"><h2>서비스별 응답 목표 예상</h2><span className="read-region-meta">{target?"현재 구성 유지와 선택한 변경 후보의 예상":"현재 구성 유지의 예상"}</span></div>
    {rows.length?<div className="overflow-x-auto rounded-xl border"><table><thead><tr><th>서비스</th><th>응답 목표</th><th>현재 구성 유지</th>{target&&<th>선택한 변경 후보</th>}<th>판정</th></tr></thead><tbody>{rows.map(row=>{
      const chosen=estimate(target??decision.keep,row.uid)
      return <tr key={row.uid}><td><Link href={`/workloads/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`}>{row.name}</Link></td><td>{row.slo.percentile}% · {ms(row.slo.targetLatencyNanos)}</td><td>{latency(estimate(decision.keep,row.uid))}</td>{target&&<td>{latency(estimate(target,row.uid))}</td>}<td>{!chosen||chosen.expectedNanos==null?<StatusBadge tone="idle">계산값 없음</StatusBadge>:chosen.normalizedExceedance>0?<StatusBadge tone="warn">목표 초과 예상</StatusBadge>:<StatusBadge tone="ok">목표 안 예상</StatusBadge>}</td></tr>
    })}</tbody></table></div>:<p>판단에 포함된 서비스가 없어요.</p>}
  </section>
}

type Choice={key:string;label:string;candidate?:SchedulerCandidate}
// Selection freezes the response it was made on; polling never changes evidence under the reader.
function CandidateWorkbench({view,initial,pool}:{view:SchedulerView;initial:string;pool:string}) {
  const {decision}=view.state
  const first=!observed(decision.at)
  const candidates=first?[]:[decision.keep,...(decision.candidates??[]),...(decision.target?[decision.target]:[])].filter((candidate,index,all)=>candidate.id&&all.findIndex(item=>item.id===candidate.id)===index)
  const choices:Choice[]=[{key:"snapshot",label:"현재 실제 구성"},...candidates.map(candidate=>({key:candidate.id,label:candidate.keep?"현재 구성 유지":decision.target?.id===candidate.id?"선택한 변경 후보":"검토한 후보",candidate}))]
  const [selected,setSelected]=React.useState(()=>({key:initial==="snapshot"||first?"snapshot":decision.target?.id||decision.keep.id||"snapshot",view}))
  const frozen=selected.view.state
  const chosen=selected.key==="snapshot"?undefined:[frozen.decision.keep,...(frozen.decision.candidates??[]),...(frozen.decision.target?[frozen.decision.target]:[])].find(candidate=>candidate.id===selected.key)
  const changed=frozen.decision.at!==decision.at||frozen.snapshot.at!==view.state.snapshot.at
  return <section className="read-region" id="candidates" aria-label="후보와 근거">
    <div className="read-region-heading"><h2>후보와 근거</h2><span className="read-region-meta">고를 때 확인한 응답을 유지해요</span></div>
    {initial==="snapshot"&&<p>선택한 실행 그룹 {pool||"전체"}의 실제 구성을 열었어요. 상세는 전체 응답을 함께 보여줘요.</p>}
    <div className="workbench">
      <div className="workbench-list" role="group" aria-label="볼 대상">{choices.map(choice=><button key={choice.key} type="button" className="workbench-option" aria-pressed={choice.key===selected.key} onClick={()=>setSelected({key:choice.key,view})}>
        <span className="font-semibold">{choice.label}</span>
        {choice.candidate?<><StatusBadge tone={judgmentTones[choice.candidate.status]??"idle"}>{judgmentLabels[choice.candidate.status]??choice.candidate.status}</StatusBadge><span className="text-sm text-muted-foreground">예상 {energy(choice.candidate.expectedJoules)}</span></>:<span className="text-sm text-muted-foreground">관측 {time(view.state.snapshot.at)}</span>}
      </button>)}{first&&<p className="text-sm text-muted-foreground">첫 후보 평가를 기다립니다.</p>}</div>
      <div className="workbench-detail min-w-0 space-y-4">
        {changed&&<div role="status" className="notice flex flex-wrap items-center justify-between gap-3"><span>새 판단 {time(decision.at)} 또는 구성 {time(view.state.snapshot.at)}을 확인했어요.</span><Button size="sm" variant="outline" onClick={()=>setSelected({key:selected.key,view})}>최신 응답으로 보기</Button></div>}
        {chosen?<CandidateDetails candidate={chosen} view={selected.view}/>:selected.key==="snapshot"?<SnapshotDetails snapshot={frozen.snapshot}/>:<p>선택한 후보가 이 응답에 없어요.</p>}
        <SourceExport filename="chill-scheduler-selected-detail.json" value={{scope:"선택 상세",source:"v1/scheduler",key:selected.key,view:selected.view}}/>
      </div>
    </div>
  </section>
}

function ActionsSummary({view}:{view:SchedulerView}) {
  const actions=view.state.actions??[]
  return <section className="read-region" id="actions" aria-label="변경 진행">
    <div className="read-region-heading"><h2>변경 진행</h2><Link href="/transitions" className="text-sm">변경 진행 전체 보기</Link></div>
    {actions.length?<ul className="grid gap-2">{actions.map(action=><li key={`${action.id}/${action.node.uid}`} className="flex flex-wrap items-center gap-2"><StatusBadge tone={actionTones[action.status]??"idle"}>{actionStatuses[action.status]??action.status}</StatusBadge><span className="font-medium">{actionKinds[action.kind]??action.kind}</span><span className="text-muted-foreground">{action.node.name}{action.from||action.to?` · ${action.from||"제공 없음"} → ${action.to||"제공 없음"}`:""}</span></li>)}</ul>:<p>진행 중이거나 계획된 변경이 없어요.</p>}
    <p className="text-sm text-muted-foreground">실행 결과 {view.state.results?.length??0}건. 계획과 실제 결과는 변경 진행 화면에서 확인해요.</p>
  </section>
}

export function SchedulerPage() {
  const query=useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll:true}),params=useSearchParams(),view=query.data
  const initial=params.get("view")??""
  const loaded=!!view
  // Sections mount with the data, after Next has already handled the hash.
  React.useEffect(()=>{const id=window.location.hash.slice(1)||(initial==="snapshot"?"candidates":"");if(loaded&&id)document.getElementById(id)?.scrollIntoView()},[loaded,initial])
  return <div className="page">
    <header className="grid gap-1.5"><h1 className="page-title">운영 판단</h1><p className="text-muted-foreground">관측한 부하로 현재 구성과 후보를 비교하고, 고른 목표가 실제로 적용되는지 이어서 확인해요.</p></header>
    <RelatedLinks/>
    <ReadRegion hero title="지금 판단" query={query}>{view&&<Judgment view={view} actions={<><Button asChild size="sm" variant="outline"><a href="#load">실제 부하</a></Button><Button asChild size="sm"><a href="#candidates">후보 근거 보기</a></Button></>}/>}</ReadRegion>
    {view&&<><ServiceEstimates view={view}/><ActionsSummary view={view}/></>}
    <LoadSection path="/scheduler"/>
    {view&&<><CandidateWorkbench view={view} initial={initial} pool={params.get("pool")??""}/><div className="flex flex-wrap gap-2"><SourceJSON title="현재 응답 원본" value={view}/><SourceExport value={{scope:"현재 응답",source:"v1/scheduler",view}} filename="chill-scheduler-current.json"/></div></>}
  </div>
}

type WorkloadChoice={desired:{metadata:{name:string;uid:string}}}
// Event-time load for an explicit window; the window lives in the hosting page's URL.
function LoadSection({path}:{path:string}) {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const [entry]=React.useState(()=>{const now=Date.now();return {from:new Date(now-3600000).toISOString(),to:new Date(now).toISOString()}})
  const submitted={from:params.get("from")??entry.from,to:params.get("to")??entry.to}
  const [draft,setDraft]=React.useState(submitted),[uid,setUID]=React.useState(params.get("uid")??""),[validation,setValidation]=React.useState("")
  const address=params.toString(),[previousAddress,setPreviousAddress]=React.useState(address)
  if(previousAddress!==address){setPreviousAddress(address);setDraft(submitted);setUID(params.get("uid")??"")}
  React.useEffect(()=>{if(!params.has("from")||!params.has("to")){const next=new URLSearchParams(params.toString());if(!next.has("from"))next.set("from",entry.from);if(!next.has("to"))next.set("to",entry.to);router.replace(`${path}?${next}`,{scroll:false})}},[params,entry,router,path])
  const selectedUID=params.get("uid")??""
  const queryText=new URLSearchParams(submitted)
  const query=useChillQuery<LoadWindow>(`v1/observations/load?${queryText}`,{decoder:parseSchedulerJSON}),workloads=useChillQuery<WorkloadChoice[]>("v1/workloads"),data=query.data
  const choices=new Map((workloads.data??[]).map(row=>[row.desired.metadata.uid,row.desired.metadata.name]))
  for(const recordUID of [...(data?.workloads?.map(row=>row.uid)??[]),uid,selectedUID])if(recordUID&&!choices.has(recordUID))choices.set(recordUID,"이전 등록 서비스")
  const rows=(data?.workloads??[]).filter(row=>!selectedUID||row.uid===selectedUID)
  function apply() {
    const from=Date.parse(draft.from),to=Date.parse(draft.to)
    if(!Number.isFinite(from)||!Number.isFinite(to)||from>to||to-from>config.maxWindowHours*3600000){setValidation(`조회 구간을 최대 ${config.maxWindowHours}시간 안에서 선택하세요.`);return}
    const next=new URLSearchParams(params.toString());next.set("from",draft.from);next.set("to",draft.to);if(uid)next.set("uid",uid);else next.delete("uid")
    setValidation("");router.replace(`${path}?${next}`,{scroll:false})
  }
  return <section id="load" className="grid gap-4" aria-label="실제 부하"><h2 className="text-lg font-bold">실제 부하</h2><form noValidate className="toolbar" onSubmit={event=>{event.preventDefault();apply()}}><label>서비스<select value={uid} onChange={event=>setUID(event.target.value)}><option value="">전체 서비스</option>{Array.from(choices,([recordUID,name])=><option key={recordUID} value={recordUID}>{name} ({recordUID})</option>)}</select></label><label>시작<input type="datetime-local" value={localDateTime(draft.from)} onChange={event=>{const from=utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"");setDraft(current=>({...current,from}))}} required/></label><label>종료<input type="datetime-local" value={localDateTime(draft.to)} onChange={event=>{const to=utcDateTime(event.currentTarget.validity.valid?event.currentTarget.value:"");setDraft(current=>({...current,to}))}} required/></label><Button type="submit">구간 적용</Button></form><p className="text-sm text-muted-foreground">현재 기기의 시간대로 입력합니다. 직접 수정한 시각만 새 시각으로 변환합니다.</p>{validation&&<p role="alert">{validation}</p>}{(workloads.error||workloads.isPending)&&<ReadRegion title="등록 서비스 목록" query={workloads}><p>조회된 부하의 이전 등록 서비스도 선택할 수 있습니다.</p></ReadRegion>}<ReadRegion title="선택 구간의 사건 집계" query={query}>{data&&<><p className="break-all">{data.from} ~ {data.to} / {ms(data.durationNanos)}</p><p>모집단: {data.population}</p><p>수신, 접수, 배정과 완료는 각 사건의 시각을 기준으로 셉니다. 완료는 이 구간에 수신한 요청의 완료율이 아닙니다.</p><DataTable data={rows} columns={[{key:"service",label:"서비스",value:row=><span>{choices.get(row.uid)??"등록 UID 없음"}<small className="block break-all">{row.uid||"UID 제공 없음"}</small></span>},{key:"received",label:"수신 / 입력률",value:row=>`${row.received}건 / ${row.receivedInputsPerSecond} 입력/s`},{key:"accepted",label:"접수 / 입력률",value:row=>`${row.accepted}건 / ${row.acceptedInputsPerSecond} 입력/s`},{key:"assigned",label:"배정 / 입력률",value:row=>`${row.assigned}건 / ${row.assignedInputsPerSecond} 입력/s`},{key:"completed",label:"완료 / 사건률",value:row=>`${row.completed}건 / ${row.completionEventsPerSecond} 건/s`},{key:"detail",label:"할당과 요청 결과",value:row=><DetailsDialog label="서비스 부하 자세히" title={`${choices.get(row.uid)??"등록 UID 없음"} 부하`}><p className="break-all">서비스 UID {row.uid||"제공 없음"}</p><h3>인스턴스별 배정 사건</h3>{Object.entries(row.instanceAssignments??{}).map(([instance,count])=><p key={instance} className="break-all">{instance}: {count}건</p>)}{!Object.keys(row.instanceAssignments??{}).length&&<p>제공된 배정 없음</p>}<h3>같은 구간의 서비스 요청 기록</h3>{<p><ObservationLink uid={row.uid} from={data.from} to={data.to}>같은 서비스 UID의 전체 적용 설정 조회</ObservationLink> (수신 정의 제공 없음)</p>}<SourceJSON inline value={row}/><SourceExport filename="chill-load-selected-service.json" value={{scope:"선택 서비스의 구간 집계",source:"v1/observations/load",from:data.from,to:data.to,population:data.population,workload:row,arrivals:null}}/></DetailsDialog>}]}/>{!rows.length&&<p>선택한 서비스와 구간의 사건 집계가 없습니다.</p>}<h3>관측 공백</h3>{data.incompleteWindows?.length?<><p role="alert">다음 기록 구간의 관측이 불완전합니다.</p><Lines values={data.incompleteWindows}/></>:<p>보고된 불완전 구간 없음</p>}<SourceJSON title="선택 구간 원본" value={data}/><SourceExport filename="chill-load-selected-window.json" value={{scope:"선택 구간의 전체 응답",source:"v1/observations/load",from:data.from,to:data.to,population:data.population,sourceData:data}}/></>}</ReadRegion></section>
}
