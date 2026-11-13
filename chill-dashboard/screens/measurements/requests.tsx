"use client"

import { useState } from "react"
import Link from "next/link"
import { X } from "lucide-react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { Button, buttonClassName, DataTable, EmptyState, FilterBar, Hint, Page, PageHeader, PageTabs, QueryBoundary, StatusBadge, TimeText, type Column } from "@/components/kit"
import { useSession } from "@/lib/client"
import { useMeasurementPlans } from "@/hooks/use-measurement-data"
import { sectionTabs } from "@/lib/routes"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { requestPhase } from "@/lib/measurements-coverage-matrix"
import type { MeasurementPlan } from "@/lib/read-types"
import { Wrapping } from "./coverage-matrix"

const statuses = ["all", "Measuring", "Complete", "Failed"] as const
type Status = (typeof statuses)[number]
const statusLabels: Record<Status, string> = { all: "전체", Measuring: "측정 중", Complete: "완료", Failed: "실패" }
const matches = (status: Status, plan: MeasurementPlan) => status === "all" || plan.status?.phase === status
const condition = (plan: MeasurementPlan) => plan.spec.state.candidate
// The coverage drawer links here with the cell's condition; every given key must match.
const CONDITION_KEYS = ["model", "deviceClass", "path", "powerMode"] as const
const conditionValue = (plan: MeasurementPlan, key: (typeof CONDITION_KEYS)[number]) => {
  const c = condition(plan)
  return key === "model" ? c.modelSpec : key === "deviceClass" ? c.deviceClass : key === "path" ? c.executionPath : c.powerMode
}

const columns: Column<MeasurementPlan>[] = [
  { key: "name", header: "요청", cell: p => p.metadata.name, sortValue: p => p.metadata.name, width: "15%" },
  { key: "model", header: "모델", cell: p => condition(p).modelSpec, sortValue: p => condition(p).modelSpec },
  { key: "device", header: "장치와 전력 모드", cell: p => <Wrapping text={`${deviceClassLabel(condition(p).deviceClass)} ${powerModeLabel(condition(p).powerMode)}`} />, csv: p => `${deviceClassLabel(condition(p).deviceClass)} ${powerModeLabel(condition(p).powerMode)}`, sortValue: p => `${condition(p).deviceClass} ${condition(p).powerMode}`, width: "20%" },
  { key: "path", header: "실행 경로", cell: p => executionPathLabel(condition(p).executionPath), sortValue: p => executionPathLabel(condition(p).executionPath), width: "12.5rem" },
  { key: "status", header: "상태", cell: p => <StatusBadge status={requestPhase(p.status?.phase)} />, sortValue: p => requestPhase(p.status?.phase).label, width: "6.5rem" },
  { key: "trials", header: "완료 회차", unit: "개", numeric: true, cell: p => p.status?.completedTrials, sortValue: p => p.status?.completedTrials, missingReason: "아직 집계되지 않음", width: "8rem" },
  { key: "created", header: "생성", cell: p => <TimeText at={p.metadata.creationTimestamp} mode="list" />, sortValue: p => Date.parse(p.metadata.creationTimestamp), width: "8.5rem" },
]

export function MeasurementRequestsScreen() {
  const { config } = useSession()
  const plans = useMeasurementPlans(config.namespace)
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const status: Status = statuses.find(s => s === params.get("status")) ?? "all"
  const given = CONDITION_KEYS.flatMap(key => { const value = params.get(key); return value ? [[key, value] as const] : [] })
  // The field keeps its own text: router.replace lands a tick later and would swallow keystrokes.
  const [search, setSearch] = useState(params.get("q") ?? "")
  const replace = (next: URLSearchParams) => { const query = next.toString(); router.replace(query ? `${pathname}?${query}` : pathname, { scroll: false }) }
  const setParam = (key: string, value: string, fallback: string) => {
    const next = new URLSearchParams(params.toString())
    if (value === fallback) next.delete(key)
    else next.set(key, value)
    replace(next)
  }
  const clearCondition = () => { const next = new URLSearchParams(params.toString()); CONDITION_KEYS.forEach(key => next.delete(key)); replace(next) }
  const create = <Link href="/measurements/requests/new" className={buttonClassName({ variant: "default" })}>새 측정 요청</Link>
  const conditionLabel = given.map(([key, value]) => key === "deviceClass" ? deviceClassLabel(value) : key === "path" ? executionPathLabel(value) : key === "powerMode" ? powerModeLabel(value) : value).join(", ")

  return (
    <Page>
      <PageHeader title="측정" actions={create} />
      <PageTabs tabs={sectionTabs.measurements} />
      <QueryBoundary query={plans} isEmpty={d => (d.items ?? []).length === 0} empty={<EmptyState title="측정 요청 없음" action={create} />}>
        {data => {
          const all = (data.items ?? []).filter(p => given.every(([key, value]) => conditionValue(p, key) === value))
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
                  end={given.length > 0 && (
                    <Hint text={conditionLabel}>
                      <Button size="sm" variant="secondary" iconRight={<X />} aria-label={`조건 해제: ${conditionLabel}`} onClick={clearCondition}>
                        <span className="max-w-64 truncate">조건 {conditionLabel}</span>
                      </Button>
                    </Hint>
                  )}
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
