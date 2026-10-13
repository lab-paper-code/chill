"use client"

import * as React from "react"
import Link from "next/link"
import { useChillQuery, useSession, type ApiError } from "@/lib/client"
import type { SchedulerView, SystemsView } from "@/lib/read-types"
import type { WorkloadView } from "@/lib/domain"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { ReadRegion, StatusBadge, statusLabel, toneOf, type Tone } from "@/components/read-region"
import { Button } from "@/components/ui/button"
import { Judgment } from "@/features/scheduler"

const observed = (at?:string) => !!at && !at.startsWith("0001-")
const serviceHref = (name:string, uid:string, section:string) => `/workloads/${encodeURIComponent(name)}?uid=${encodeURIComponent(uid)}#${section}`
type Attention = {key:string;tone:Tone;badge:string;title:string;cause:string;href:string;action:string}
const toneRank:Record<Tone,number> = {fail:0,warn:1,info:2,idle:3,ok:4}

// Every item is derived from an owner response; nothing is inferred beyond what it reports.
function attention(workloads?:WorkloadView[], scheduler?:SchedulerView):Attention[] {
  const items:Attention[] = []
  for (const row of workloads ?? []) {
    const {name, uid} = row.desired.metadata
    const phase = row.operation?.phase
    if (phase === "Failed") items.push({key:`op/${uid}`,tone:"fail",badge:"변경 실패",title:`${name} 설정 변경이 실패했어요`,cause:row.operation?.error || statusLabel(row.operation?.kind),href:serviceHref(name,uid,"definition"),action:"변경 내용 확인"})
    else if (row.operation && phase !== "Completed") items.push({key:`op/${uid}`,tone:"info",badge:statusLabel(phase),title:`${name} 설정을 적용하고 있어요`,cause:statusLabel(row.operation.kind),href:serviceHref(name,uid,"definition"),action:"진행 보기"})
    if (!row.admission) {
      const reason = row.reason || row.readiness
      items.push({key:`admit/${uid}`,tone:toneOf(reason) === "fail" ? "fail" : "warn",badge:"접수 대기",title:`${name} 서비스가 아직 요청을 받지 못해요`,cause:statusLabel(reason),href:serviceHref(name,uid,reason === "MissingEvidence" ? "evidence" : "definition"),action:reason === "MissingEvidence" ? "필요한 측정 보기" : "준비 상태 보기"})
    }
  }
  if (scheduler) {
    const {snapshot, decision, actions, results} = scheduler.state
    for (const node of snapshot.nodes ?? []) if (!node.ready) items.push({key:`node/${node.node.uid}`,tone:"warn",badge:"노드 준비 안 됨",title:`${node.node.name} 노드가 준비되지 않았어요`,cause:node.reason || "준비 확인 대기",href:node.pool ? `/pools/${encodeURIComponent(node.pool)}` : "/resources",action:node.pool ? "실행 그룹 보기" : "장비 보기"})
    if (observed(decision.at)) {
      const chosen = decision.target && decision.target.id !== decision.keep.id ? decision.target : decision.keep
      for (const estimate of chosen.slo ?? []) if (estimate.normalizedExceedance > 0) {
        const name = snapshot.workloads?.find(row => row?.uid === estimate.uid)?.name ?? "이전 등록 서비스"
        items.push({key:`slo/${estimate.uid}`,tone:"warn",badge:"목표 초과 예상",title:`${name}의 응답 목표 초과가 예상돼요`,cause:chosen.keep ? "현재 구성 유지 기준 예상" : "선택한 변경 후보 기준 예상",href:"/scheduler#services",action:"예상 비교 보기"})
      }
    }
    const failed = (results ?? []).filter(result => result.status === "failed").length
    if (failed) items.push({key:"results/failed",tone:"fail",badge:"변경 실패",title:`실행 결과 ${failed}건이 실패했어요`,cause:"구성 변경의 실제 결과",href:"/transitions",action:"변경 진행 보기"})
    const open = (actions ?? []).filter(action => !["complete","failed","cancel"].includes(action.status)).length
    if (open) items.push({key:"actions/open",tone:"info",badge:"변경 진행",title:`구성 변경 ${open}건이 계획되거나 진행 중이에요`,cause:"운영 판단이 고른 목표로 옮기는 중",href:"/transitions",action:"변경 진행 보기"})
  }
  return items.sort((a, b) => toneRank[a.tone] - toneRank[b.tone])
}

export function OverviewPage() {
  const [poll,setPoll] = React.useState(false)
  const {config} = useSession()
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON,poll})
  const systems = useChillQuery<SystemsView>(`v1/systems?limit=${config.pageSize}&offset=0`,{poll})
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads",{poll})
  const items = attention(workloads.data, scheduler.data)
  const missing = [workloads.error && "서비스", scheduler.error && "운영 판단"].filter(Boolean)
  const loading = workloads.isPending || scheduler.isPending
  return <div className="page">
    <header className="flex flex-wrap items-end justify-between gap-4">
      <div className="grid gap-1.5"><h1 className="page-title">운영 현황</h1><p className="text-muted-foreground">{config.systemName}에서 확인할 일부터 보고, 운영 판단과 주요 상태를 이어서 확인해요.</p></div>
      <label className="switch"><input type="checkbox" checked={poll} onChange={event => setPoll(event.target.checked)} />자동 새로고침</label>
    </header>
    <section className="read-region" aria-label="확인할 일">
      <div className="read-region-heading"><h2>확인할 일</h2><span className="read-region-meta" role="status">{loading ? "확인하고 있어요" : `${items.length}건`}</span></div>
      {loading && !items.length ? <div className="skeleton"><span /><span /></div> : items.length ? <ul className="attention-list">{items.map(item => <li key={item.key} className="attention-item">
        <StatusBadge tone={item.tone}>{item.badge}</StatusBadge>
        <div className="min-w-0"><p className="font-semibold">{item.title}</p><p className="text-sm text-muted-foreground">{item.cause}</p></div>
        <Link href={item.href} className="attention-link">{item.action}</Link>
      </li>)}</ul> : !missing.length && <p className="flex items-center gap-2"><StatusBadge tone="ok">모두 정상</StatusBadge>지금 확인할 일이 없어요.</p>}
      {missing.length > 0 && <p className="text-sm text-muted-foreground">{missing.join(", ")} 정보를 확인하지 못해 그 부분의 할 일은 빠져 있을 수 있어요.</p>}
    </section>
    <ReadRegion hero title="운영 판단" query={scheduler}>{scheduler.data && <Judgment view={scheduler.data} actions={<><Button asChild variant="outline" size="sm"><Link href="/scheduler#candidates">후보 근거</Link></Button><Button asChild size="sm"><Link href="/scheduler">판단 작업 공간</Link></Button></>} />}</ReadRegion>
    <section className="vitals" aria-label="주요 상태">
      <Vital title="시스템" query={systems} href={null}>{data => {
        const ready = data.systems.filter(row => row.status.conditions?.find(condition => condition.type === "Ready")?.status === "True").length
        return <><p className="vital-value">{ready}<small>/ {data.systems.length} 준비</small></p>{data.systems.slice(0,3).map(row => <p key={row.uid} className="flex flex-wrap items-center gap-2"><span className="font-medium text-foreground">{row.name}</span><StatusBadge value={row.status.conditions?.find(condition => condition.type === "Ready")?.status ?? "Unknown"} /></p>)}</>
      }}</Vital>
      <Vital title="서비스" query={workloads} href="/workloads">{data => <><p className="vital-value">{data.filter(row => row.admission).length}<small>/ {data.length} 요청 접수 중</small></p><p>{data.some(row => !row.admission) ? `${data.filter(row => !row.admission).length}개는 준비를 기다려요` : data.length ? "모든 서비스가 요청을 받고 있어요" : "등록된 서비스가 없어요"}</p></>}</Vital>
      <Vital title="노드" query={scheduler} href="/pools">{data => {
        const nodes = data.state.snapshot.nodes ?? []
        return <><p className="vital-value">{nodes.filter(node => node.ready).length}<small>/ {nodes.length} 준비</small></p><p>{observed(data.state.snapshot.at) ? `실행 그룹 ${new Set(nodes.map(node => node.pool).filter(Boolean)).size}개에 속해 있어요` : "첫 구성 관측을 기다려요"}</p></>
      }}</Vital>
      <Vital title="처리 중 요청" query={scheduler} href="/scheduler?view=snapshot">{data => <><p className="vital-value">{data.state.snapshot.outstanding?.length ?? 0}<small>건</small></p><p><Link href="/transitions">변경 동작 {data.state.actions?.length ?? 0}건 보기</Link></p></>}</Vital>
    </section>
  </div>
}

function Vital<T>({title,query,href,children}:{title:string;query:{data?:T;error:ApiError|null;isPending:boolean};href:string|null;children:(data:T)=>React.ReactNode}) {
  return <section className="vital" aria-label={title}>
    <h2>{href ? <Link href={href} className="text-inherit no-underline hover:text-foreground">{title}</Link> : title}</h2>
    {query.data !== undefined ? children(query.data) : query.error ? <p>{[401,403].includes(query.error.status) ? "볼 권한이 없어요" : "지금은 확인할 수 없어요"}</p> : <div className="skeleton" role="status"><span className="sr-only">불러오고 있어요.</span><span /><span /></div>}
  </section>
}
