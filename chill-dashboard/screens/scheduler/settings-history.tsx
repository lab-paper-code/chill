"use client"

import * as React from "react"
import { DataTable, DetailDrawer, EmptyState, QueryBoundary, Section, StatusBadge, TimeText } from "@/components/kit"
import { useChillQuery, useSession } from "@/lib/client"
import type { SchedulerObservationPage, SchedulerSettingsChange } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { changeRows, type ChangeRow } from "@/lib/scheduler-settings-form"

/** Recent settings changes (`?history=`, the selected record's `at`). */
export function SettingsHistory({ open, selected, onSelect, onClose, name }: {
  open: boolean; selected: string; onSelect: (at: string) => void; onClose: () => void; name: (uid: string) => string
}) {
  return (
    <DetailDrawer open={open} onOpenChange={next => { if (!next) onClose() }} title="변경 이력">
      {open && <HistoryBody selected={selected} onSelect={onSelect} name={name} />}
    </DetailDrawer>
  )
}

type Row = { at: string; change: SchedulerSettingsChange; rows: ChangeRow[] }

function HistoryBody({ selected, onSelect, name }: { selected: string; onSelect: (at: string) => void; name: (uid: string) => string }) {
  const { config } = useSession()
  // The server window is bounded; a linked older record gets a window around it.
  const [range] = React.useState(() => {
    const span = config.maxWindowHours * 3_600_000, now = Date.now(), at = Date.parse(selected)
    const [from, to] = Number.isFinite(at) && at < now - span + 60_000 ? [at - span / 2, at + span / 2] : [now - span, now]
    return { from: new Date(from).toISOString(), to: new Date(to).toISOString() }
  })
  const query = useChillQuery<SchedulerObservationPage>(`v1/observations/scheduler?${new URLSearchParams({ filter: "settings", order: "desc", ...range })}`, { decoder: parseSchedulerJSON })
  const rows = React.useMemo<Row[]>(() => (query.data?.events ?? []).flatMap(event => event.settings ? [{ at: event.at, change: event.settings, rows: changeRows(event.settings.before, event.settings.after, name) }] : []), [query.data, name])
  const current = rows.find(row => row.at === selected)
  return (
    <QueryBoundary query={query} isEmpty={() => rows.length === 0} empty={<EmptyState compact title="변경 기록 없음" />}>
      {() => (
        <div className="grid gap-5">
          <DataTable label="설정 변경" rows={rows} rowKey={row => row.at} pageSize={0} onRowClick={row => onSelect(row.at)} selectedKey={selected || undefined}
            columns={[
              { key: "at", header: "시각", width: "11.5rem", cell: row => <TimeText at={row.at} mode="list" /> },
              { key: "actor", header: "변경자", cell: row => row.change.actorUsername || null, missingReason: "기록 없음" },
              { key: "count", header: "항목 수", width: "4.5rem", numeric: true, cell: row => row.rows.length },
            ]} />
          {current && (
            <Section title="변경 내용" actions={current.change.appliedAt ? undefined : <StatusBadge tone="fail" label="적용 안 됨" reason={current.change.error} />}>
              <DataTable label="변경 내용" rows={current.rows} rowKey={row => row.id} pageSize={0} empty={<EmptyState compact title="바뀐 값 없음" />}
                columns={[
                  { key: "label", header: "항목", width: "52%", cell: row => row.label },
                  { key: "before", header: "변경 전", cell: row => row.before },
                  { key: "after", header: "변경 후", cell: row => row.after },
                ]} />
            </Section>
          )}
        </div>
      )}
    </QueryBoundary>
  )
}
