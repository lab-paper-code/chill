"use client"

import * as React from "react"
import { useChillQuery, useSession } from "@/lib/client"
import { planHref } from "@/features/measurement-plans"
import type { MeasurementPlanList, SchedulerView } from "@/lib/read-types"
import { resourceHref, type WorkloadView } from "@/lib/domain"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, StatusBadge, statusLabel, toneOf, type Tone } from "@/components/read-region"
import { Row, RowDialog, Rows, Screen } from "@/components/screen"

type Attention = {key:string;tone:Tone;badge:string;title:string;hint:string;href:string}
const toneRank:Record<Tone,number> = {fail:0,warn:1,info:2,idle:3,ok:4}

// Every item is derived from an owner response and links to the one screen that resolves it.
function attention(workloads?:WorkloadView[], scheduler?:SchedulerView):Attention[] {
  const items:Attention[] = []
  for (const row of workloads ?? []) {
    const {name, uid} = row.desired.metadata
    const phase = row.operation?.phase
    if (phase === "Failed") items.push({key:`op/${uid}`,tone:"fail",badge:"변경 실패",title:`${name} 설정 변경이 실패했어요`,hint:"변경 작업 기록에서 이유를 확인해 주세요",href:resourceHref("workloads",name,uid,"definition")})
    else if (row.operation && phase !== "Completed") items.push({key:`op/${uid}`,tone:"info",badge:statusLabel(phase),title:`${name} 설정을 적용하고 있어요`,hint:`${statusLabel(row.operation.kind)} 진행 중`,href:resourceHref("workloads",name,uid,"definition")})
    if (!row.admission) {
      const reason = row.reason || row.readiness
      const section = reason === "MissingEvidence" ? "evidence" : ["MissingRuntime","NativeNotReady"].includes(reason) ? "placement" : "definition"
      items.push({key:`admit/${uid}`,tone:toneOf(reason) === "fail" ? "fail" : "warn",badge:"접수 대기",title:`${name} 서비스가 아직 요청을 받지 못해요`,hint:statusLabel(reason),href:resourceHref("workloads",name,uid,section)})
    }
  }
  if (scheduler) {
    const {snapshot, decision, actions, results} = scheduler.state
    for (const node of snapshot.nodes ?? []) if (!node.ready) items.push({key:`node/${node.node.uid}`,tone:"warn",badge:"노드 준비 안 됨",title:`${node.node.name} 노드가 준비되지 않았어요`,hint:node.reason ? statusLabel(node.reason) : "준비 확인을 기다려요",href:node.pool ? `/pools/${encodeURIComponent(node.pool)}` : "/resources"})
    if (decision.at && !decision.at.startsWith("0001-")) {
      const chosen = decision.target && decision.target.id !== decision.keep.id ? decision.target : decision.keep
      for (const estimate of chosen.slo ?? []) if (estimate.normalizedExceedance > 0) {
        const name = snapshot.workloads?.find(row => row?.uid === estimate.uid)?.name ?? "이전 등록 서비스"
        items.push({key:`slo/${estimate.uid}`,tone:"warn",badge:"목표 초과 예상",title:`${name}의 응답 목표 초과가 예상돼요`,hint:chosen.keep ? "지금 구성을 유지할 때의 예상" : "고른 변경 후보를 따를 때의 예상",href:"/scheduler/estimates"})
      }
    }
    const failed = (results ?? []).filter(result => result.status === "failed").length
    if (failed) items.push({key:"results/failed",tone:"fail",badge:"변경 실패",title:`구성 변경 ${failed}건이 실패했어요`,hint:"실제 결과를 확인해 주세요",href:"/transitions"})
    const open = (actions ?? []).filter(action => !["complete","failed","cancel"].includes(action.status)).length
    if (open) items.push({key:"actions/open",tone:"info",badge:"변경 진행",title:`구성 변경 ${open}건이 계획되거나 진행 중이에요`,hint:"운영 판단이 고른 목표로 옮기는 중이에요",href:"/transitions"})
  }
  return items.sort((a, b) => toneRank[a.tone] - toneRank[b.tone])
}

// Home answers one question: what needs my attention now.
export function OverviewPage() {
  const [poll,setPoll] = React.useState(false)
  const {token, config} = useSession()
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll})
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads",{poll})
  const plans = useChillQuery<MeasurementPlanList>(`v1/namespaces/${encodeURIComponent(config.namespace)}/batch-energy-optimizations`,{poll})
  const measuring = (plans.data?.items ?? []).filter(plan => plan.status?.phase === "Measuring")
  const items = [...attention(workloads.data, scheduler.data), ...measuring.map((plan):Attention => ({key:`measure/${plan.metadata.uid}`,tone:"info",badge:"측정 중",title:`${plan.spec.state.targetNode}에서 측정하고 있어요`,hint:`${plan.spec.state.candidate.modelSpec} · ${plan.status?.completedTrials ?? 0}번 쟀어요`,href:planHref(plan.metadata.namespace, plan.metadata.name, plan.metadata.uid)}))]
  const failed = ([[workloads,"서비스"],[scheduler,"운영 판단"]] as const).filter(([query]) => query.error)
  const loading = workloads.isPending || scheduler.isPending
  const title = !token ? "먼저 접속해 주세요" : items.length ? `확인할 일이 ${items.length}개 있어요` : loading ? "확인하고 있어요" : failed.length ? "일부 정보를 확인하지 못했어요" : "지금 확인할 일이 없어요"
  return <Screen status={!loading && !items.length && !failed.length && token ? <StatusBadge tone="ok">모두 정상</StatusBadge> : undefined} title={title} lede={`${config.systemName}에서 지금 손봐야 할 일만 모았어요.`}>
    {loading && !items.length && token && <div className="skeleton" role="status"><span className="sr-only">불러오고 있어요.</span><span /><span /></div>}
    {(items.length > 0 || failed.length > 0) && <Rows>
      {items.map(item => <Row key={item.key} href={item.href} label={item.title} hint={item.hint} value={<StatusBadge tone={item.tone}>{item.badge}</StatusBadge>} />)}
      {failed.map(([query, label]) => <RowDialog key={label} label={`${label} 정보를 확인하지 못했어요`} hint="이 부분의 할 일은 빠져 있을 수 있어요" title={`${label} 정보`}><QueryGate query={query}>{null}</QueryGate></RowDialog>)}
    </Rows>}
    <Rows><Row label="자동 새로고침" hint="켜 두면 새 할 일을 계속 확인해요" value={poll ? "켜짐" : "꺼짐"} onClick={() => setPoll(!poll)} /></Rows>
  </Screen>
}
