"use client"

import { useState } from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { Button, buttonClassName, DataTable, EmptyState, FilterBar, Hint, Page, PageHeader, PageTabs, QueryBoundary, StatusBadge, TimeText, type Column } from "@/components/kit"
import { useSession } from "@/lib/client"
import { useMeasurementPlans } from "@/hooks/use-measurement-data"
import { sectionTabs } from "@/lib/routes"
import { measurementPhaseStatus } from "@/lib/status-labels"
import { executionPathLabel } from "@/lib/measurements"
import type { MeasurementPlan } from "@/lib/read-types"

const statuses = ["all", "Measuring", "Complete", "Failed"] as const
type Status = (typeof statuses)[number]
const statusLabels: Record<Status, string> = { all: "전체", Measuring: "측정 중", Complete: "완료", Failed: "실패" }
const matches = (status: Status, plan: MeasurementPlan) => status === "all" || plan.status?.phase === status
const condition = (plan: MeasurementPlan) => plan.spec.state.candidate

const columns: Column<MeasurementPlan>[] = [
  { key: "name", header: "요청", cell: p => p.metadata.name, sortValue: p => p.metadata.name, width: "22rem" },
  { key: "model", header: "모델", cell: p => condition(p).modelSpec, sortValue: p => condition(p).modelSpec },
  { key: "device", header: "장치와 전력 모드", cell: p => `${condition(p).deviceClass} ${condition(p).powerMode}`, sortValue: p => `${condition(p).deviceClass} ${condition(p).powerMode}` },
  { key: "path", header: "실행 경로", cell: p => <Hint text={condition(p).executionPath}>{executionPathLabel(condition(p).executionPath).replace(/ \(.*\)$/, "")}</Hint>, sortValue: p => condition(p).executionPath },
  { key: "status", header: "상태", cell: p => <StatusBadge status={measurementPhaseStatus(p.status?.phase)} />, sortValue: p => measurementPhaseStatus(p.status?.phase).label, width: "7rem" },
  { key: "trials", header: "완료 회차", unit: "회", numeric: true, cell: p => p.status?.completedTrials, sortValue: p => p.status?.completedTrials, missingReason: "아직 집계되지 않음", width: "8rem" },
  { key: "created", header: "생성", cell: p => <TimeText at={p.metadata.creationTimestamp} mode="absolute" seconds={false} />, sortValue: p => Date.parse(p.metadata.creationTimestamp), width: "11rem" },
]

export function MeasurementRequestsScreen() {
  const { config } = useSession()
  const plans = useMeasurementPlans(config.namespace)
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const status: Status = statuses.find(s => s === params.get("status")) ?? "all"
  // The field keeps its own text: router.replace lands a tick later and would swallow keystrokes.
  const [search, setSearch] = useState(params.get("q") ?? "")
  const setParam = (key: string, value: string, fallback: string) => {
    const next = new URLSearchParams(params.toString())
    if (value === fallback) next.delete(key)
    else next.set(key, value)
    const query = next.toString()
    router.replace(query ? `${pathname}?${query}` : pathname, { scroll: false })
  }
  const create = <Link href="/measurements/requests/new" className={buttonClassName({ variant: "default" })}>측정 요청</Link>

  return (
    <Page>
      <PageHeader title="측정" actions={create} />
      <PageTabs tabs={sectionTabs.measurements} />
      <QueryBoundary query={plans} isEmpty={d => (d.items ?? []).length === 0} empty={<EmptyState title="측정 요청 없음" action={create} />}>
        {data => {
          const all = data.items ?? []
          const needle = search.trim().toLowerCase()
          const rows = all.filter(p => matches(status, p) && (!needle || `${p.metadata.name} ${condition(p).modelSpec} ${condition(p).deviceClass} ${condition(p).executionPath}`.toLowerCase().includes(needle)))
          return (
            <DataTable
              label="측정 요청"
              rows={rows}
              rowKey={p => p.metadata.uid}
              rowHref={p => `/measurements/requests/${encodeURIComponent(p.metadata.name)}?uid=${encodeURIComponent(p.metadata.uid)}`}
              columns={columns}
              defaultSort={{ key: "created", desc: true }}
              toolbar={
                <FilterBar
                  search={{ value: search, onChange: v => { setSearch(v); setParam("q", v, "") }, placeholder: "요청, 모델, 장치 검색" }}
                  segments={[{ label: "상태", value: status, onChange: v => setParam("status", v, "all"), options: statuses.map(s => ({ value: s, label: statusLabels[s], count: all.filter(p => matches(s, p)).length })) }]}
                />
              }
              empty={<EmptyState title="조건에 맞는 요청 없음" compact action={<Button variant="outline" onClick={() => { setSearch(""); router.replace(pathname, { scroll: false }) }}>필터 초기화</Button>} />}
            />
          )
        }}
      </QueryBoundary>
    </Page>
  )
}
