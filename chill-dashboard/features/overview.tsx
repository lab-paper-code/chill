"use client"

import * as React from "react"
import { useChillQuery, useSession } from "@/lib/client"
import { planHref } from "@/features/measurement-plans"
import type { LoadWindow, MeasurementPlanList, OperatingPower, SchedulerView, SystemsView } from "@/lib/read-types"
import { resourceHref, sloTarget, type WorkloadView } from "@/lib/domain"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { explain } from "@/lib/labels"
import { localDateTime } from "@/lib/date-time"
import { QueryGate, SourceJSON, StatusBadge, statusLabel, toneOf, type Tone } from "@/components/read-region"
import { Row, RowDialog, Rows, Screen } from "@/components/screen"

type Attention = {key:string;tone:Tone;badge:string;title:string;hint:string;href:string}
type Query = Parameters<typeof QueryGate>[0]["query"]
const toneRank:Record<Tone,number> = {fail:0,warn:1,info:2,idle:3,ok:4}

// Each summary is its own query: a failure shows the error and last good time in that section only.
function Section({label,query,children}:{label:string;query:Query;children:React.ReactNode}) {
  return <section aria-label={label}>
    <QueryGate query={query}><Rows label={label}>{children}</Rows></QueryGate>
    {query.error && query.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">마지막 관측 {localDateTime(new Date(query.dataUpdatedAt).toISOString())}</p>}
  </section>
}

// Things to look at now: services not accepting requests, unready nodes, expected SLO overruns.
function attention(workloads?:WorkloadView[], scheduler?:SchedulerView):Attention[] {
  const items:Attention[] = []
  for (const row of workloads ?? []) if (!row.admission) {
    const {name, uid} = row.desired.metadata
    const reason = row.reason || row.readiness
    const section = reason === "MissingEvidence" ? "evidence" : ["MissingRuntime","NativeNotReady"].includes(reason) ? "placement" : "definition"
    items.push({key:`admit/${uid}`,tone:toneOf(reason) === "fail" ? "fail" : "warn",badge:"접수 대기",title:`${name} 서비스가 아직 요청을 받지 못해요`,hint:explain(reason)[0],href:resourceHref("workloads",name,uid,section)})
  }
  if (scheduler) {
    const {snapshot, decision} = scheduler.state
    for (const node of snapshot.nodes ?? []) if (!node.ready) items.push({key:`node/${node.node.uid}`,tone:"warn",badge:"노드 준비 안 됨",title:`${node.node.name} 노드가 준비되지 않았어요`,hint:node.reason ? explain(node.reason)[0] : "준비 확인을 기다려요",href:node.pool ? `/pools/${encodeURIComponent(node.pool)}` : "/resources"})
    if (decision.at && !decision.at.startsWith("0001-")) {
      const chosen = decision.target && decision.target.id !== decision.keep.id ? decision.target : decision.keep
      for (const estimate of chosen.slo ?? []) if (estimate.normalizedExceedance > 0) {
        const name = snapshot.workloads?.find(row => row?.uid === estimate.uid)?.name ?? "이전 등록 서비스"
        items.push({key:`slo/${estimate.uid}`,tone:"warn",badge:"목표 초과 예상",title:`${name}의 응답 목표 초과가 예상돼요`,hint:chosen.keep ? "지금 구성을 유지할 때의 예상" : "고른 변경 후보를 따를 때의 예상",href:"/scheduler/estimates"})
      }
    }
  }
  return items
}

// Work in progress: changes and measurements running or failed, most severe first.
function work(workloads?:WorkloadView[], scheduler?:SchedulerView, plans?:MeasurementPlanList):Attention[] {
  const items:Attention[] = []
  for (const row of workloads ?? []) {
    const {name, uid} = row.desired.metadata
    const phase = row.operation?.phase
    if (phase === "Failed") items.push({key:`op/${uid}`,tone:"fail",badge:"변경 실패",title:`${name} 설정 변경이 실패했어요`,hint:"변경 작업 기록에서 이유를 확인해 주세요",href:resourceHref("workloads",name,uid,"definition")})
    else if (row.operation && phase !== "Completed") items.push({key:`op/${uid}`,tone:"info",badge:statusLabel(phase),title:`${name} 설정을 적용하고 있어요`,hint:`${statusLabel(row.operation.kind)} 진행 중`,href:resourceHref("workloads",name,uid,"definition")})
  }
  if (scheduler) {
    const {actions, results} = scheduler.state
    const failed = (results ?? []).filter(result => result.status === "failed").length
    if (failed) items.push({key:"results/failed",tone:"fail",badge:"변경 실패",title:`구성 변경 ${failed}건이 실패했어요`,hint:"실제 결과를 확인해 주세요",href:"/transitions"})
    const open = (actions ?? []).filter(action => !["complete","failed","cancel"].includes(action.status)).length
    if (open) items.push({key:"actions/open",tone:"info",badge:"변경 진행",title:`구성 변경 ${open}건이 계획되거나 진행 중이에요`,hint:"운영 판단이 고른 목표로 옮기는 중이에요",href:"/transitions"})
  }
  for (const plan of plans?.items ?? []) if (plan.status?.phase === "Measuring" || plan.status?.phase === "Failed") items.push({key:`measure/${plan.metadata.uid}`,tone:plan.status.phase === "Failed" ? "fail" : "info",badge:plan.status.phase === "Failed" ? "측정 실패" : "측정 중",title:`${plan.spec.state.targetNode}에서 ${plan.status.phase === "Failed" ? "측정이 실패했어요" : "측정하고 있어요"}`,hint:`${plan.spec.state.candidate.modelSpec} · ${plan.status?.completedTrials ?? 0}번 쟀어요`,href:planHref(plan.metadata.namespace, plan.metadata.name, plan.metadata.uid)})
  return items.sort((a, b) => toneRank[a.tone] - toneRank[b.tone])
}

function SystemRows({data}:{data?:SystemsView}) {
  return <>{(data?.systems ?? []).map(system => {
    const conditions = system.status.conditions ?? []
    const ready = conditions.find(row => row.type === "Ready")
    const disabled = ready?.status === "True" && ready.reason === "ComponentsDisabled"
    const [sentence, next] = !ready ? ["준비 상태를 아직 보고하지 않았어요."] : disabled ? ["관리 구성요소를 모두 꺼 둔 상태예요."] : explain(ready.reason ?? "")
    return <RowDialog key={system.uid} label={sentence} hint={next} value={<StatusBadge tone={disabled ? "idle" : toneOf(ready?.status)}>{statusLabel(ready?.status)}</StatusBadge>} title={`${system.name} 구성요소`}>
      <Rows>{conditions.filter(row => row !== ready).map(row => {
        const [text, action] = explain(row.reason ?? "")
        return <Row key={row.type} label={explain(row.type)[0]} hint={[text, row.status === "True" ? undefined : action, row.lastTransitionTime && localDateTime(row.lastTransitionTime)].filter(Boolean).join(" ")} value={<StatusBadge value={row.status} />} />
      })}</Rows>
      <SourceJSON inline title="원본 condition 보기" value={conditions} />
    </RowDialog>
  })}</>
}

function PowerRow({label,joules,watts}:{label:string;joules:number|null;watts:number|null}) {
  return <Row href="/scheduler/power" label={label} hint={joules == null ? "이 구간은 전력 값이 비어 있어요" : `${joules.toFixed(1)} J${watts == null ? "" : ` · 평균 ${watts.toFixed(1)} W`}`} />
}

// Home: CHILL now over the last 5 minutes. Each summary is an independent existing read and links to its detail screen.
// ponytail: ChillServer integrates power per request (live: 5 min ≈ 1 s, 15 min ≈ 11 s > query timeout); widen when that read gets cheaper.
export function OverviewPage() {
  const [range] = React.useState(() => { const to = new Date(); return {from:new Date(to.getTime() - 300000).toISOString(), to:to.toISOString()} })
  const {token, config} = useSession()
  const systems = useChillQuery<SystemsView>("v1/systems")
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads")
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON})
  const load = useChillQuery<LoadWindow>(`v1/observations/load?${new URLSearchParams(range)}`,{decoder:parseSchedulerJSON})
  const power = useChillQuery<OperatingPower>(`v1/observations/power?${new URLSearchParams(range)}`,{decoder:parseSchedulerJSON})
  const plans = useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(config.namespace)}/batch-energy-optimizations`)
  const reads = [systems, workloads, scheduler, load, power, plans]
  const names = new Map((workloads.data ?? []).map(row => [row.desired.metadata.uid, row.desired.metadata.name]))
  const items = attention(workloads.data, scheduler.data)
  const jobs = work(workloads.data, scheduler.data, plans.data)
  const count = items.length + jobs.filter(job => job.tone === "fail").length
  const title = !token ? "먼저 접속해 주세요" : count ? `확인할 일이 ${count}개 있어요` : reads.some(query => query.error) ? "일부 정보를 확인하지 못했어요" : reads.some(query => query.isPending) ? "확인하고 있어요" : "지금 확인할 일이 없어요"
  const snapshot = scheduler.data?.state.snapshot
  const gap = power.data && !power.data.complete
  return <Screen title={title} lede={`${config.systemName} · 기준 구간 ${localDateTime(range.from)} ~ ${localDateTime(range.to)}`}>
    {items.length > 0 && <Rows label="확인할 일">{items.map(item => <Row key={item.key} href={item.href} label={item.title} hint={item.hint} value={<StatusBadge tone={item.tone}>{item.badge}</StatusBadge>} />)}</Rows>}
    <Section label="시스템 상태" query={systems}><SystemRows data={systems.data} /></Section>
    <Section label="서비스" query={workloads}>
      {(workloads.data ?? []).map(row => {
        const {name, uid} = row.desired.metadata
        return <Row key={uid} href={resourceHref("workloads",name,uid)} label={name} hint={`${row.admission ? "요청을 받고 있어요" : explain(row.reason || row.readiness)[0]} · ${sloTarget(row.desired.spec)}`} value={<StatusBadge tone={row.admission ? "ok" : "warn"}>{row.admission ? "접수 중" : "접수 대기"}</StatusBadge>} />
      })}
      {workloads.data && !workloads.data.length && <Row href="/workloads" label="등록된 서비스가 없어요" />}
    </Section>
    <Section label="부하" query={load}>
      {(load.data?.workloads ?? []).map(row => <Row key={row.uid} href="/scheduler/load" label={names.get(row.uid) ?? "이전 등록 서비스"} hint={`수신 ${row.received} · 접수 ${row.accepted} · 완료 ${row.completed}`} />)}
      {load.data && !load.data.workloads?.length && <Row href="/scheduler/load" label="이 구간에 들어온 요청이 없어요" />}
      {!!load.data?.incompleteWindows?.length && <RowDialog label="관측이 빠진 구간이 있어요" hint="이 구간의 수는 실제보다 적을 수 있어요" value={<StatusBadge tone="warn">관측 공백</StatusBadge>} title="관측이 빠진 구간"><SourceJSON inline title="원본" value={{incompleteWindows:load.data.incompleteWindows}} /></RowDialog>}
    </Section>
    <Section label="대기열" query={scheduler}>
      {snapshot && <Row href="/scheduler/load" label={`대기 요청 ${snapshot.outstanding?.length ?? 0}건`} hint={`관측 ${localDateTime(snapshot.at)}`} />}
    </Section>
    <Section label="전력과 에너지" query={power}>
      {power.data && <>
        <PowerRow label="전체 POOL" joules={power.data.wholePoolJoules} watts={power.data.wholePoolMeanWatts} />
        <PowerRow label="활성 POOL" joules={power.data.activePoolJoules} watts={power.data.activePoolMeanWatts} />
        <Row href="/scheduler/power" label="집계 구간" hint={`${localDateTime(power.data.from)} ~ ${localDateTime(power.data.to)}`} value={gap ? undefined : <StatusBadge tone="ok">빠짐 없음</StatusBadge>} />
        {gap && <RowDialog label="관측이 빠진 구간이 있어요" hint="원본에서 이유를 확인해 주세요" value={<StatusBadge tone="warn">관측 공백</StatusBadge>} title="전력 관측 공백"><SourceJSON inline title="원본" value={{reasons:power.data.reasons,incompleteWindows:power.data.incompleteWindows}} /></RowDialog>}
      </>}
    </Section>
    <Section label="진행 작업" query={scheduler}>
      {jobs.map(item => <Row key={item.key} href={item.href} label={item.title} hint={item.hint} value={<StatusBadge tone={item.tone}>{item.badge}</StatusBadge>} />)}
      {scheduler.data && !jobs.length && <Row href="/transitions" label="진행 중이거나 실패한 작업이 없어요" />}
    </Section>
  </Screen>
}
