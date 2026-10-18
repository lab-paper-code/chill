"use client"

import * as React from "react"
import { useChillQuery, useSession } from "@/lib/client"
import { planHref } from "@/features/measurement-plans"
import type { LoadWindow, MeasurementPlanList, ObservationResult, OperatingPower, SchedulerView, SystemsView } from "@/lib/read-types"
import { definitionIdentity, resourceHref, sloTarget, type WorkloadView } from "@/lib/domain"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { explain } from "@/lib/labels"
import { localDateTime } from "@/lib/date-time"
import { QueryGate, SourceJSON, StatusBadge, statusLabel, toneOf, type Tone } from "@/components/read-region"
import { Row, Rows, Screen } from "@/components/screen"

type Attention = {key:string;tone:Tone;badge:string;title:string;hint:string;href:string}
type Query = Parameters<typeof QueryGate>[0]["query"]
const toneRank:Record<Tone,number> = {fail:0,warn:1,info:2,idle:3,ok:4}
const num = (value:number|null) => value == null ? "값 없음" : value.toFixed(1)

// Each summary is its own query: a failure shows the error and last good time in that section only.
function Section({label,query,children}:{label:string;query:Query;children:React.ReactNode}) {
  return <section aria-label={label}>
    <QueryGate query={query}><Rows label={label}>{children}</Rows></QueryGate>
    {query.error && query.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">마지막 관측 {localDateTime(new Date(query.dataUpdatedAt).toISOString())}</p>}
  </section>
}

function SystemSection() {
  const query = useChillQuery<SystemsView>("v1/systems")
  return <Section label="시스템 상태" query={query}>
    {(query.data?.systems ?? []).map(system => {
      const conditions = system.status.conditions ?? []
      const ready = conditions.find(row => row.type === "Ready")
      const disabled = ready?.status === "True" && ready.reason === "ComponentsDisabled"
      const [sentence, next] = ready ? explain(ready.reason ?? "") : ["준비 상태를 아직 보고하지 않았어요."]
      return <React.Fragment key={system.uid}>
        <Row label={`${system.name}: ${disabled ? "관리 구성요소를 모두 꺼 둔 상태예요" : sentence}`} hint={disabled ? undefined : next} value={<StatusBadge tone={disabled ? "idle" : toneOf(ready?.status)}>{statusLabel(ready?.status)}</StatusBadge>} />
        {conditions.filter(row => row !== ready).map(row => {
          const [text, action] = explain(row.reason ?? "")
          return <Row key={row.type} label={row.type} hint={[text, row.status === "True" ? undefined : action, row.lastTransitionTime && localDateTime(row.lastTransitionTime)].filter(Boolean).join(" ")} value={<StatusBadge value={row.status} />} />
        })}
        <li><SourceJSON title="원본 condition 보기" value={conditions} /></li>
      </React.Fragment>
    })}
  </Section>
}

function ServiceRow({row,range}:{row:WorkloadView;range:{from:string;to:string}}) {
  const {name, uid} = row.desired.metadata
  const query = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${new URLSearchParams({...range,limit:"1"})}`,{decoder:parseSchedulerJSON})
  const cohort = row.appliedDefinition ? query.data?.cohorts?.find(c => c.definition === definitionIdentity(row.appliedDefinition!)) : undefined
  const met = !cohort?.sampleCount || cohort.quantileNanos == null || cohort.targetLatencyNanos == null ? undefined : Number(cohort.quantileNanos) <= Number(cohort.targetLatencyNanos)
  const actual = query.error ? "요청 결과를 확인하지 못했어요" : !query.data ? "요청 결과 확인 중" : cohort?.quantileNanos == null ? "적용 설정의 요청 표본 없음" : `실제 ${cohort.percentile ?? "?"}% 응답 ${nanosToMilliseconds(BigInt(cohort.quantileNanos))} ms`
  const outcomes = Object.entries(query.data?.outcomes ?? {}).map(([k, v]) => `${statusLabel(k)} ${v}`).join(", ")
  return <Row href={resourceHref("workloads",name,uid,"operation")} label={`${name}: ${row.admission ? "요청을 받고 있어요" : explain(row.reason || row.readiness)[0]}`}
    hint={[sloTarget(row.desired.spec), actual, outcomes].filter(Boolean).join(" · ")}
    value={<StatusBadge tone={met === undefined ? "idle" : met ? "ok" : "warn"}>{met === undefined ? "표본 없음" : met ? "목표 안" : "목표 초과"}</StatusBadge>} />
}

function LoadSection({range,names,scheduler}:{range:{from:string;to:string};names:Map<string,string>;scheduler:Query & {data?:SchedulerView}}) {
  const query = useChillQuery<LoadWindow>(`v1/observations/load?${new URLSearchParams(range)}`,{decoder:parseSchedulerJSON})
  const snapshot = scheduler.data?.state.snapshot
  return <>
    <Section label="부하" query={query}>
      {(query.data?.workloads ?? []).map(row => <Row key={row.uid} href="/scheduler/load" label={names.get(row.uid) ?? row.uid} hint={`수신 ${row.received} · 접수 ${row.accepted} · 완료 ${row.completed}`} />)}
      {query.data && !query.data.workloads?.length && <Row href="/scheduler/load" label="이 구간에 들어온 요청이 없어요" />}
      {!!query.data?.incompleteWindows?.length && <Row href="/scheduler/load" label="관측이 빠진 구간이 있어요" hint={query.data.incompleteWindows.join(", ")} value={<StatusBadge tone="warn">관측 공백</StatusBadge>} />}
    </Section>
    <Section label="대기열" query={scheduler}>
      {snapshot && <Row href="/scheduler/load" label={`대기 요청 ${snapshot.outstanding?.length ?? 0}건`} hint={`관측 ${localDateTime(snapshot.at)}`} />}
    </Section>
  </>
}

function PowerSection({range}:{range:{from:string;to:string}}) {
  const query = useChillQuery<OperatingPower>(`v1/observations/power?${new URLSearchParams(range)}`,{decoder:parseSchedulerJSON})
  const data = query.data
  return <Section label="전력과 에너지" query={query}>
    {data && <>
      <Row href="/scheduler/power" label="전체 POOL" hint={`${num(data.wholePoolJoules)} J · 평균 ${num(data.wholePoolMeanWatts)} W`} />
      <Row href="/scheduler/power" label="활성 POOL" hint={`${num(data.activePoolJoules)} J · 평균 ${num(data.activePoolMeanWatts)} W`} />
      <Row href="/scheduler/power" label={`집계 구간 ${localDateTime(data.from)} ~ ${localDateTime(data.to)}`} hint={data.complete ? undefined : [...data.reasons, ...data.incompleteWindows].join(", ")} value={<StatusBadge tone={data.complete ? "ok" : "warn"}>{data.complete ? "빠짐 없음" : "관측 공백"}</StatusBadge>} />
    </>}
  </Section>
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

// Home: CHILL now, over the last hour, one independent summary per existing read.
export function OverviewPage() {
  const [poll,setPoll] = React.useState(false)
  const [range] = React.useState(() => { const to = new Date(); return {from:new Date(to.getTime() - 3600000).toISOString(), to:to.toISOString()} })
  const {token, config} = useSession()
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll})
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads",{poll})
  const plans = useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(config.namespace)}/batch-energy-optimizations`,{poll})
  const names = new Map((workloads.data ?? []).map(row => [row.desired.metadata.uid, row.desired.metadata.name]))
  const items = work(workloads.data, scheduler.data, plans.data)
  return <Screen title={token ? "지금 CHILL 상태" : "먼저 접속해 주세요"} lede={`${config.systemName} · 기준 구간 ${localDateTime(range.from)} ~ ${localDateTime(range.to)}`}>
    <SystemSection />
    <Section label="서비스" query={workloads}>{(workloads.data ?? []).map(row => <ServiceRow key={row.desired.metadata.uid} row={row} range={range} />)}</Section>
    <LoadSection range={range} names={names} scheduler={scheduler} />
    <PowerSection range={range} />
    <Section label="진행 작업" query={scheduler}>
      {items.map(item => <Row key={item.key} href={item.href} label={item.title} hint={item.hint} value={<StatusBadge tone={item.tone}>{item.badge}</StatusBadge>} />)}
      {scheduler.data && !items.length && <Row href="/transitions" label="진행 중이거나 실패한 작업이 없어요" />}
    </Section>
    <Rows><Row label="자동 새로고침" hint="켜 두면 요약을 계속 새로 읽어요" value={poll ? "켜짐" : "꺼짐"} onClick={() => setPoll(!poll)} /></Rows>
  </Screen>
}
