"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useState } from "react"
import { useChillQuery } from "@/lib/client"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { statusLabel } from "@/lib/labels"
import { Facts, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { admissionPolicies, definitionIdentity, resourceHref as href, type WorkloadSummary, type WorkloadView } from "@/lib/domain"
import type { ObservationCohort, SchedulerSettings, SchedulerWorkloadState } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { ServiceAdmission, ServiceReadiness, ServiceAppliedFacts, ServiceDefinition, ServicePlacement, serviceListReturn, useServiceSignals, type ServiceSignals } from "@/features/service-hub"
import { DataTable } from "@/components/data-table"
import { useResource } from "@/hooks/use-catalog-resource"
import { Deleted, DeleteConfirm, Mismatch, Problem, select } from "@/features/catalog-editor"

// Readiness and connection summary for dialogs outside the service screens (inference uses it).
export function WorkloadState({ view }: { view: WorkloadView }) {
  const { name, uid } = view.desired.metadata
  return <>
    <Facts items={[
      ["모델", view.desired.spec.modelRef.name], ["모델 입출력 처리", view.desired.spec.preprocessorRef.name], ["데이터", view.desired.spec.datasetRef?.name ?? "미설정"], ["목표 비율 (%)", view.desired.spec.slo.percentile], ["목표 지연", view.desired.spec.slo.targetLatency],
      ["과부하 정책", admissionPolicies[view.desired.spec.admission?.policy] ?? "미기록"], ["대기 상한", view.desired.spec.admission ? `${view.desired.spec.admission.maxQueuedRequests}건` : "미기록"], ["최종 기한", view.desired.spec.requestLifetime ?? "서버 기본값"],
      ["적용된 모델", view.appliedDefinition?.model.name ?? "미적용"],
      ["준비", statusLabel(view.readiness)], ...(view.reason && statusLabel(view.reason) !== statusLabel(view.readiness) ? [["사유", statusLabel(view.reason)] as [string, string]] : []), ["접수 허용", view.admission ? "허용" : "차단"],
      ["배정 대기", `${view.pending}건`], ["배정됨", `${view.assigned}건`], ["측정 입력", `${view.supportedInputs.length}개`],
    ]} />
    <Link href={`${href("workloads", name, uid)}#definition`}>측정 입력</Link>
    <SourceJSON inline title="전체 상태와 관측 시각" value={view} /><SourceExport value={view} filename={`workload-${name}.json`} />
  </>
}
export function ServicesPage() {
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON })
  const params = useSearchParams(), router = useRouter()
  const filter = params.get("filter") ?? "all", returnTo = `/workloads?${params}`
  const list = (workloads.data ?? []).filter(w => filter === "all" || (filter === "allowed" ? w.admission : filter === "blocked" ? !w.admission : !!w.operation && !["Completed", "Failed"].includes(w.operation.phase)))
  return <Screen width="data" title="서비스" action={<Button asChild><Link href="/workloads/new">등록</Link></Button>}>
    <QueryGate query={workloads}>
      <label className="flex items-center gap-3 text-sm">상태<select className={select} value={filter} onChange={event => { const next = new URLSearchParams(params.toString()); next.set("filter", event.target.value); next.delete("page"); router.replace(`/workloads?${next}`, { scroll: false }) }}><option value="all">전체</option><option value="allowed">접수 허용</option><option value="blocked">접수 차단</option><option value="changing">변경 작업 진행</option></select></label>
      <DataTable urlState data={list} emptyAction={<Button variant="outline" onClick={() => { const next = new URLSearchParams(params.toString()); for (const key of ["filter", "search", "page"]) next.delete(key); router.replace(`/workloads?${next}`, { scroll: false }) }}>검색·필터·페이지 초기화</Button>} columns={[
        {key:"service",label:"서비스",value:w=><Link href={`${href("workloads",w.desired.metadata.name,w.desired.metadata.uid)}&${new URLSearchParams({returnTo})}`}>{w.desired.metadata.name}</Link>,sortValue:w=>w.desired.metadata.name},
        {key:"model",label:"모델",value:w=>w.desired.spec.modelRef.name,sortValue:w=>w.desired.spec.modelRef.name},
        {key:"ready",label:"준비",value:w=><ServiceReadiness view={w} />,sortValue:w=>w.readiness},
        {key:"admission",label:"접수 허용",value:w=><ServiceAdmission view={w} />},
        {key:"autonomy",label:"CHILL 자동 운영",value:w=><Link href={`/settings?section=targets&edit=autonomousWorkloads&workload=${encodeURIComponent(w.desired.metadata.uid)}`}>{settings.error?"조회 실패":!settings.data?"미확인":settings.data.autonomousWorkloads?.includes(w.desired.metadata.uid)?"켜짐":"꺼짐"}</Link>},
        ...(list.some(w=>w.operation&&!["Completed","Failed"].includes(w.operation.phase))?[{key:"operation",label:"변경 작업 진행",value:(w:WorkloadSummary)=>w.operation&&!["Completed","Failed"].includes(w.operation.phase)?`${statusLabel(w.operation.kind)} · ${statusLabel(w.operation.phase)}`:<span className="text-muted-foreground">해당 없음</span>}]:[]),
        {key:"actions",label:"작업",value:w=><Link href={href("workloads",w.desired.metadata.name,w.desired.metadata.uid,"edit")}>수정</Link>},
      ]} />
      <Rows><RowDialog label="목록 원본" title="서비스 목록 원본"><SourceJSON inline value={workloads.data} /></RowDialog></Rows>
    </QueryGate>
  </Screen>
}


const workloadStates: Record<string, string> = { Disabled: "CHILL 자동 운영을 꺼 두었어요", MeasurementEvidenceMissing: "측정 근거가 생기면 CHILL 자동 운영을 시작해요", Controlled: "운영 판단이 구성을 정하고 있어요" }
function Autonomy({ uid, state, scheduler, cohorts, current }: { uid: string; state?: SchedulerWorkloadState; scheduler: ServiceSignals["scheduler"]; cohorts: ObservationCohort[]; current?: string }) {
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON })
  const on = settings.data?.autonomousWorkloads?.includes(uid) ?? false, margin = settings.data?.completionMarginMS
  const stateText = scheduler.error ? "운영 판단 조회 실패" : settings.error ? "CHILL 자동 운영 설정 조회 실패" : !settings.data ? "CHILL 자동 운영 설정 미확인" : !on ? "CHILL 자동 운영 꺼짐" : scheduler.isPending ? "운영 판단 조회 중" : state ? workloadStates[state.state] ?? statusLabel(state.state) : "이 서비스의 판단 기록 없음"
  return <RowDialog label={settings.error ? "조회 실패" : !settings.data ? "미확인" : on ? "켜짐" : "꺼짐"} title="CHILL 자동 운영">
      <Link href={`/settings?section=targets&edit=autonomousWorkloads&workload=${encodeURIComponent(uid)}`}>CHILL 자동 운영 설정 변경</Link>
      <Facts items={[["판단 상태", stateText], ["사유", scheduler.error ? scheduler.error.message : state?.reason || "미제공"]]} />
      <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>CHILL 자동 운영을 끈 서비스는 마지막 구성을 그대로 유지해요. 설정한 요청 분배와 직접 실행한 변경은 그대로 적용돼요.</details>
    <details><summary>완료 여유 · {margin === undefined ? "미확인" : `${margin} ms`}</summary>
      {cohorts.map(row => {
        // ChillServer sorts completionResidualNanos ascending; ranks are nearest-rank observed values.
        const sorted = row.completionResidualNanos ?? [], at = (p: number) => `${(sorted[Math.max(0, Math.ceil(p * sorted.length) - 1)] / 1e6).toLocaleString("ko-KR", { maximumFractionDigits: 3 })}밀리초`
        return <Facts key={row.definition} label={row.definition === current ? "현재 적용 설정" : "이전 적용 설정"} items={sorted.length ? [
          ["표본", `${sorted.length}건`], ["중앙값", at(0.5)], ["95번째 백분위", at(0.95)], ["최대", at(1)],
          ["설정 여유보다 길었던 요청", margin === undefined ? "확인 중" : `${sorted.filter(nanos => nanos > margin * 1e6).length}건`],
        ] : [["표본", "아직 없어요"]]} />
      })}
      {!cohorts.length && <p>표본 없음 · 최근 1시간</p>}
      <details><summary className="min-h-11 cursor-pointer py-2">도움말</summary>요청마다 종단 경과에서 전달 시점 경과와 실행기 처리 시간을 뺀 값이에요. 운영 설정에서 완료 여유를 바꿀 수 있어요.</details>
    </details>
  </RowDialog>
}

export function ServicePage({ name }: { name: string }) {
  const r = useResource("workloads", name)
  const view = r.detail.data && "desired" in r.detail.data && !r.denied ? r.detail.data : undefined
  const s = useServiceSignals(view)
  const params = useSearchParams()
  const [deleting, setDeleting] = useState(false)
  if (r.deletedUID) return <Deleted kind="workloads" name={name} uid={r.deletedUID} r={r} />
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind="workloads" name={name} uid={r.current.metadata.uid} />
  const uid = view?.desired.metadata.uid ?? ""
  return <Screen back={[serviceListReturn(params.get("returnTo")), "서비스"]} title={name} action={view && <div className="flex gap-2"><Button asChild variant="outline"><Link href={href("workloads",name,uid,"edit")}>수정</Link></Button><Button asChild><Link href={href("workloads",name,uid,"infer")}>단일 요청</Link></Button></div>}>
    <QueryGate query={r.detail}>{view && <>
      <Problem r={r} kind="workloads" />
      <section id="application" className="space-y-3 rounded-xl border p-4"><h2 className="font-semibold">적용 상태</h2>
        <Facts items={[
          ["준비", <ServiceReadiness key="ready" view={view} />],
          ["접수 허용", <ServiceAdmission key="admission" view={view} />],
          ["CHILL 자동 운영", <Rows key="autonomy"><Autonomy uid={uid} scheduler={s.scheduler} state={s.scheduler.data?.state.decision.workloads?.find(row => row.uid === uid)} cohorts={s.observations.data?.cohorts ?? []} current={view.appliedDefinition && definitionIdentity(view.appliedDefinition)} /></Rows>],
          ["적용 정의", view.appliedDefinition ? <Rows><RowDialog label="적용됨" title="적용 정의"><ServiceAppliedFacts view={view} /><SourceJSON inline value={view.appliedDefinition} /></RowDialog></Rows> : "미적용"],
          ["변경 작업 진행", view.operation ? <Rows><RowDialog label={`${statusLabel(view.operation.kind)} · ${statusLabel(view.operation.phase)}`} title="변경 작업 진행">{view.operation.error && <p role="alert">{view.operation.error}</p>}<SourceJSON inline value={view.operation} /></RowDialog></Rows> : <span className="text-muted-foreground">해당 없음</span>],
        ]} />
      </section>
      <div className="grid items-start gap-4 xl:grid-cols-2"><ServiceDefinition view={view} /><ServicePlacement view={view} signals={s} /></div>
      <Button variant="destructive" disabled={r.blocked || !!r.conflict} onClick={() => setDeleting(true)}>삭제</Button>
      {deleting && <DeleteConfirm kind="workloads" name={name} r={r} close={() => setDeleting(false)} />}
    </>}</QueryGate>
  </Screen>
}
