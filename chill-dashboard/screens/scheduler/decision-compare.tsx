"use client"

import { useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { formatKst, formatNumber } from "@/lib/format"
import { decisionStatus } from "@/lib/status-labels"
import { compareRows, decisionRecordPath, type CompareRow } from "@/lib/scheduler-decisions"
import type { SchedulerObservation, SchedulerObservationPage } from "@/lib/read-types"
import { DataTable, Dialog, EmptyState, QueryBoundary } from "@/components/kit"

const find = (page: SchedulerObservationPage | undefined, at: string) => page?.events?.find(row => row.decision?.at === at)

function useRecord(at: string) {
  const path = decisionRecordPath(at)
  return useChillQuery<SchedulerObservationPage>(path ?? "", { enabled: !!path, decoder: parseSchedulerJSON })
}

/** Two decisions side by side, the earlier one as A. */
export function DecisionCompare({ open, onOpenChange, picks, names }: { open: boolean; onOpenChange: (open: boolean) => void; picks: string[]; names: Map<string, string> }) {
  const [first, second] = [...picks].sort((x, y) => Date.parse(x) - Date.parse(y))
  const a = useRecord(first), b = useRecord(second)
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="판단 비교" size="xl">
      <QueryBoundary query={a} skeleton="table" isEmpty={data => !find(data, first)} empty={<EmptyState compact title="판단 기록 없음" />}>
        {pageA => (
          <QueryBoundary query={b} skeleton="table" isEmpty={data => !find(data, second)} empty={<EmptyState compact title="판단 기록 없음" />}>
            {pageB => <Table a={find(pageA, first)!} b={find(pageB, second)!} names={names} />}
          </QueryBoundary>
        )}
      </QueryBoundary>
    </Dialog>
  )
}

const show = (value: number | string | null, digits = 0) => typeof value === "number" ? formatNumber(value, digits) : value

function Table({ a, b, names }: { a: SchedulerObservation; b: SchedulerObservation; names: Map<string, string> }) {
  const recorded = new Map([...(a.snapshot?.workloads ?? []), ...(b.snapshot?.workloads ?? [])].flatMap(w => (w ? [[w.uid, w.name] as const] : [])))
  const rows = compareRows(a.decision!, b.decision!, uid => recorded.get(uid) ?? names.get(uid) ?? uid, code => decisionStatus(code).label)
  return (
    <div className="max-h-[65dvh] overflow-y-auto">
      <DataTable<CompareRow> label="판단 비교" rows={rows} rowKey={row => row.key} pageSize={0} columns={[
        { key: "label", header: "항목", width: "40%", cell: row => row.unit ? `${row.label} (${row.unit})` : row.label },
        { key: "a", header: `A ${formatKst(a.at).slice(5)}`, numeric: true, missingReason: "기록 없음", cell: row => show(row.a, row.digits) },
        { key: "b", header: `B ${formatKst(b.at).slice(5)}`, numeric: true, missingReason: "기록 없음", cell: row => show(row.b, row.digits) },
        {
          key: "diff", header: "차이", numeric: true, missingReason: "비교할 값 없음",
          cell: row => {
            if (typeof row.a === "number" && typeof row.b === "number") {
              const diff = row.b - row.a
              return `${diff > 0 ? "+" : ""}${formatNumber(diff, row.digits ?? 0)}`
            }
            return row.a == null || row.b == null ? null : row.a === row.b ? "같음" : "다름"
          },
        },
      ]} />
    </div>
  )
}
