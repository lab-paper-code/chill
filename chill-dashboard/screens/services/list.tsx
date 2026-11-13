"use client"

import { useState } from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import Link from "next/link"
import { Button, buttonClassName, DataTable, EmptyState, FilterBar, Page, PageHeader, QueryBoundary, StatusBadge, type Column } from "@/components/kit"
import { useChillQuery } from "@/lib/client"
import { type WorkloadSummary } from "@/lib/domain"
import { formatTarget, goDurationNanos } from "@/lib/format"
import { managementKind, managementPhase } from "@/lib/services-records"
import { parseSchedulerJSON } from "@/lib/scheduler"
import type { SchedulerSettings } from "@/lib/read-types"
import { admissionStatus, autonomyEnabled, errorCodeLabel, serviceReadiness, sloVerdict } from "@/lib/status-labels"
import { useLive } from "@/lib/use-live"

const statuses = ["all", "ready", "preparing", "blocked"] as const
type Status = (typeof statuses)[number]
const statusLabels: Record<Status, string> = { all: "전체", ready: "준비됨", preparing: "준비 중", blocked: "접수 중지" }

const matches: Record<Status, (w: WorkloadSummary) => boolean> = {
  all: () => true,
  ready: w => w.readiness === "Ready",
  preparing: w => serviceReadiness(w.readiness).tone === "progress",
  blocked: w => !w.admission,
}
const activeOperation = (w: WorkloadSummary) => (w.operation && !["Completed", "Failed"].includes(w.operation.phase) ? w.operation : undefined)

export function ServiceListScreen() {
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON })
  // Readiness is the registration's state; a missed live latency window is the operating problem 개요 shows, added beside it.
  const latest = useLive().state.metrics.at(-1)
  const missed = (uid: string) => latest?.workloads[uid]?.slo?.met === false
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const raw = params.get("status")
  const status: Status = statuses.find(s => s === raw) ?? "all"
  // The field keeps its own text: router.replace lands a tick later and would swallow keystrokes.
  const [search, setSearch] = useState(params.get("q") ?? "")

  const setParam = (key: string, value: string, fallback: string) => {
    const next = new URLSearchParams(params.toString())
    if (value === fallback) next.delete(key)
    else next.set(key, value)
    const query = next.toString()
    router.replace(query ? `${pathname}?${query}` : pathname, { scroll: false })
  }

  return (
    <Page>
      <PageHeader title="서비스" actions={<Link href="/services/new" className={buttonClassName({ variant: "default" })}>서비스 등록</Link>} />
      <QueryBoundary
        query={workloads}
        isEmpty={d => d.length === 0}
        empty={<EmptyState title="서비스 없음" action={<Link href="/services/new" className={buttonClassName({ variant: "default" })}>서비스 등록</Link>} />}
      >
        {all => {
          const needle = search.trim().toLowerCase()
          const rows = all
            .filter(matches[status])
            .filter(w => !needle || `${w.desired.metadata.name} ${w.desired.spec.modelRef.name}`.toLowerCase().includes(needle))
            .sort((a, b) => a.desired.metadata.name.localeCompare(b.desired.metadata.name))
          const autonomous = settings.data?.autonomousWorkloads ?? []
          const columns: Column<WorkloadSummary>[] = [
            { key: "name", header: "서비스", cell: w => w.desired.metadata.name, sortValue: w => w.desired.metadata.name, width: "16rem" },
            {
              key: "status", header: "준비 상태", width: "8rem", sortValue: w => serviceReadiness(w.readiness).label,
              cell: w => (
                <span className="inline-flex flex-wrap gap-1">
                  <StatusBadge status={serviceReadiness(w.readiness)} reason={w.reason && w.reason !== w.readiness ? errorCodeLabel(w.reason) : undefined} />
                  {missed(w.desired.metadata.uid) && <StatusBadge status={sloVerdict(false)} reason="최근 관측 구간 지연이 목표 초과" />}
                </span>
              ),
            },
            { key: "model", header: "모델", cell: w => w.desired.spec.modelRef.name, sortValue: w => w.desired.spec.modelRef.name, width: "16rem" },
            { key: "target", header: "목표", cell: w => formatTarget(w.desired.spec.slo.percentile, goDurationNanos(w.desired.spec.slo.targetLatency)), width: "8rem" },
            { key: "admission", header: "접수", cell: w => <StatusBadge status={admissionStatus(w.admission)} />, sortValue: w => (w.admission ? 1 : 0), width: "8rem" },
            {
              key: "autonomy",
              header: "자동 제어",
              cell: w => (settings.data ? <StatusBadge status={autonomyEnabled(autonomous.includes(w.desired.metadata.uid))} /> : null),
              missingReason: settings.error ? "설정 조회 실패" : "설정 확인 중",
              width: "8rem",
            },
            ...(all.some(activeOperation)
              ? [{ key: "operation", header: "변경 작업", cell: (w: WorkloadSummary) => { const op = activeOperation(w); return op ? `${managementKind(op.kind)} ${managementPhase(op.phase)}` : null } }]
              : []),
          ]
          return (
            <DataTable
              label="서비스"
              rows={rows}
              rowKey={w => w.desired.metadata.uid}
              rowHref={w => `/services/${encodeURIComponent(w.desired.metadata.name)}?uid=${encodeURIComponent(w.desired.metadata.uid)}`}
              columns={columns}
              pageSize={0}
              toolbar={
                <FilterBar
                  search={{ value: search, onChange: v => { setSearch(v); setParam("q", v, "") }, placeholder: "서비스, 모델 검색" }}
                  segments={[{ label: "준비 상태", value: status, onChange: v => setParam("status", v, "all"), options: statuses.map(s => ({ value: s, label: statusLabels[s], count: all.filter(matches[s]).length })) }]}
                />
              }
              empty={
                <EmptyState
                  title="조건에 맞는 서비스 없음"
                  compact
                  action={<Button variant="outline" onClick={() => { setSearch(""); router.replace(pathname, { scroll: false }) }}>필터 초기화</Button>}
                />
              }
            />
          )
        }}
      </QueryBoundary>
    </Page>
  )
}
