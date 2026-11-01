"use client"

import * as React from "react"
import Link from "next/link"
import type { UseQueryResult } from "@tanstack/react-query"
import type { ApiError } from "@/lib/client"
import { useChillQuery } from "@/lib/client"
import type { ObservationResult, PoolsView, SchedulerView } from "@/lib/read-types"
import { resourceHref, type WorkloadView } from "@/lib/domain"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON } from "@/components/read-region"
import { Facts, RowDialog, Rows } from "@/components/screen"
import { DataTable } from "@/components/data-table"
import { StatusCell } from "@/components/status-cell"
import { statusLabel } from "@/lib/labels"

export function serviceListReturn(value: string | null) {
  if (!value || !value.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(value)) return "/workloads"
  try {
    const url = new URL(value, "https://dashboard.invalid")
    return url.origin === "https://dashboard.invalid" && url.pathname === "/workloads" ? value : "/workloads"
  } catch { return "/workloads" }
}
// Management reads remain independent of the operational WebSocket snapshot.
type ManagementQuery<T> = Pick<UseQueryResult<T, ApiError>, "data" | "error" | "isPending" | "isFetching" | "dataUpdatedAt" | "refetch">
export type ServiceSignals = {
  pools: ManagementQuery<PoolsView>
  scheduler: ManagementQuery<SchedulerView>
  observations: ManagementQuery<ObservationResult>
  instances: NonNullable<NonNullable<PoolsView["workloads"]>[number]["instances"]>
}
export function useServiceSignals(view?: WorkloadView): ServiceSignals {
  const uid = view?.desired.metadata.uid ?? ""
  const [range] = React.useState(() => { const to = new Date(); return { from: new Date(to.getTime() - 3600000).toISOString(), to: to.toISOString() } })
  const pools = useChillQuery<PoolsView>("v1/pools")
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler", { decoder: parseSchedulerJSON })
  const observations = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${new URLSearchParams({ ...range, limit: "20" })}`, { decoder: parseSchedulerJSON, enabled: !!uid })
  const instances = pools.data?.workloads?.find(pool => pool.workload.uid === uid)?.instances ?? []
  return { pools, scheduler, observations, instances }
}

export function ServiceAdmission({ view }: { view: WorkloadView }) {
  return <StatusCell axis="접수 허용" label={view.admission ? "허용" : "차단"} tone={view.admission ? "ok" : "warn"} />
}

export function ServiceAppliedFacts({ view }: { view: WorkloadView }) {
  const applied = view.appliedDefinition
  return <Facts items={[
    ["적용 상태", applied ? "적용됨" : "미적용"],
    ...(applied ? [
      ["적용 세대", String(applied.generation)], ["적용 모델", applied.model.name], ["적용 입력 처리", applied.preprocessor.name],
      ["적용 데이터", applied.dataset?.name ?? "미설정"], ["목표 비율 (%)", String(applied.slo.percentile)],
      ["목표 지연 (ms)", (applied.slo.targetLatencyNanos / 1e6).toLocaleString("ko-KR", { maximumFractionDigits: 3 })],
      ...applied.preprocessor.input.map((field): [string, string] => [`입력 ${field.name}`, `${field.format} · 최대 ${field.maxBytes.toLocaleString("ko-KR")} B`]),
    ] as [string, string][] : []),
    ["측정 실행 경로 (개)", String(view.supportedInputs.length)],
    ...view.supportedInputs.map((input): [string, string] => [`실행 경로 ${input.executionPath}`, `${input.artifact} · 프로파일 ${input.profiles.length}개`]),
  ]} />
}

export function ServiceDefinition({ view }: { view: WorkloadView }) {
  const { desired, appliedDefinition: applied } = view
  return <section id="definition" className="min-w-0 space-y-3 rounded-xl border p-4"><h2 className="font-semibold">정의·설정</h2>
    <Facts items={[["모델", desired.spec.modelRef.name], ["입력 처리", desired.spec.preprocessorRef.name], ["데이터", desired.spec.datasetRef?.name ?? "미설정"], ["목표 비율 (%)", desired.spec.slo.percentile], ["목표 지연", desired.spec.slo.targetLatency], ["측정 입력", `${view.supportedInputs.length}개`]]} />
    <div className="flex flex-wrap gap-4 text-sm">
      <Link href={applied ? resourceHref("models", applied.model.name, applied.model.uid) : `/models/${encodeURIComponent(desired.spec.modelRef.name)}`}>모델</Link>
      <Link href={applied ? resourceHref("preprocessors", applied.preprocessor.name, applied.preprocessor.uid) : `/preprocessors/${encodeURIComponent(desired.spec.preprocessorRef.name)}`}>입력 처리</Link>
      {desired.spec.datasetRef && <Link href={applied?.dataset ? resourceHref("datasets", applied.dataset.name, applied.dataset.uid) : `/datasets/${encodeURIComponent(desired.spec.datasetRef.name)}`}>데이터</Link>}
    </div>
    <Rows><RowDialog label="적용 정의·측정 입력" title="적용 정의·측정 입력"><ServiceAppliedFacts view={view} /><SourceJSON inline value={{ appliedDefinition: applied, supportedInputs: view.supportedInputs }} /></RowDialog></Rows>
  </section>
}

export function ServicePlacement({ view, signals: s }: { view: WorkloadView; signals: ServiceSignals }) {
  const uid = view.desired.metadata.uid
  // Instance rows and weights share one scheduler snapshot; pools are a separate observation.
  const snapshot = s.scheduler.data?.state.snapshot
  const weights = snapshot?.routing?.[uid]
  const instances = snapshot?.instances?.filter(row => row.workloadUID === uid || !!weights && Object.hasOwn(weights, row.id)) ?? []
  return <section id="placement" className="min-w-0 space-y-3 rounded-xl border p-4"><h2 className="font-semibold">배치·연결</h2>
    <Facts items={[["분배 기준 시각 (KST)", snapshot?.at ? new Date(snapshot.at).toLocaleString("ko-KR", { timeZone: "Asia/Seoul" }) : "미확인"]]} />
    <QueryGate query={s.scheduler}><DataTable searchable={false} data={instances} columns={[
      { key: "device", label: "장치", value: row => row.node.name },
      { key: "instance", label: "인스턴스", value: row => row.id },
      { key: "ready", label: "준비", value: row => <StatusCell axis="배치" label={row.ready ? "준비됨" : "준비 안 됨"} tone={row.ready ? "ok" : "warn"} /> },
      { key: "weight", label: "분배 가중치", numeric: true, value: row => weights?.[row.id] ?? "미제공" },
    ]} /></QueryGate>
    <nav className="flex flex-wrap gap-4 text-sm"><Link href={`/?${new URLSearchParams({ open: `service:${uid}` })}`}>현재 관측</Link><Link href={`/measurements?${new URLSearchParams({ uid, workload: view.desired.metadata.name })}`}>측정</Link><Link href={`/records?${new URLSearchParams({ kind: "requests", uid, workload: view.desired.metadata.name })}`}>기록</Link></nav>
    <Rows><RowDialog label="배치 관측" title="배치 관측">
      <Facts items={[["배치 관측 시각 (KST)", s.pools.data?.observedAt ? new Date(s.pools.data.observedAt).toLocaleString("ko-KR", { timeZone: "Asia/Seoul" }) : "미확인"]]} />
      <QueryGate query={s.pools}>{s.instances.length ? <DataTable searchable={false} data={s.instances} columns={[
        { key: "device", label: "장치", value: row => row.node.name },
        { key: "instance", label: "인스턴스", value: row => row.id },
        { key: "ready", label: "준비", value: row => <StatusCell axis="배치" label={row.ready ? "준비됨" : "준비 안 됨"} tone={row.ready ? "ok" : "warn"} reason={row.reason ? statusLabel(row.reason) : undefined} /> },
      ]} /> : <Facts items={[["인스턴스", "없음"]]} />}</QueryGate>
    </RowDialog></Rows>
    <Rows><RowDialog label="원본·내보내기" title="서비스 원본"><SourceJSON inline title="서비스" value={view} /><SourceJSON inline title="배치" value={s.pools.data} /><SourceJSON inline title="운영 판단·분배" value={s.scheduler.data} /><SourceExport value={view} filename={`workload-${view.desired.metadata.name}.json`} /></RowDialog></Rows>
  </section>
}
