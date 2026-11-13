"use client"

import * as React from "react"
import { MoreVertical } from "lucide-react"
import { Button, ConfirmDialog, DataTable, DetailDrawer, EmptyState, Fields, FilterBar, Missing, QueryBoundary, RawDialog, Section, StatusBadge, TimeText, useToast, type Column, type Segment } from "@/components/kit"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { ApiError } from "@/lib/client-transport"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { occupancyDirect, occupancyDisplayReason, occupancyRunning } from "@/lib/dispatch-occupancy"
import { formatNumber } from "@/lib/format"
import { measurementPath } from "@/lib/measurements"
import type { DispatchOccupancyMeasurement, MeasurementProjectionList } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { measurementPhaseStatus } from "@/lib/status-labels"
import { distinct, filterChoice, useUrlState } from "./results-shared"

const ms = (nanos: number | bigint | null | undefined, digits = 2) => nanos == null ? null : formatNumber(Number(nanos) / 1e6, digits)
const statusOf = (item: DispatchOccupancyMeasurement) => item.phase ? measurementPhaseStatus(item.phase) : { label: "대기", tone: "idle" as const }

/** Server's occupancy per request in ns: the observed median, or the older single result. */
function occupancyNanos(item: DispatchOccupancyMeasurement) {
  if (item.phase !== "Complete") return undefined
  return occupancyDirect(item) ? item.estimate?.statistics?.median : item.d?.perRequestNanos
}

export function MeasurementOccupancyResults({ lead, namespace }: { lead: Segment; namespace: string }) {
  const { search, set } = useUrlState()
  const { toast } = useToast()
  const mutation = useChillMutation()
  const list = useChillQuery<MeasurementProjectionList>(measurementPath("dispatch-occupancy-measurements", namespace), {
    decoder: parseSchedulerJSON,
    poll: data => !!data?.items?.some(item => item.kind === "DispatchOccupancyMeasurement" && occupancyRunning(item)),
  })
  const [raw, setRaw] = React.useState(false)
  const [removing, setRemoving] = React.useState<DispatchOccupancyMeasurement | null>(null)
  const all = React.useMemo(() => (list.data?.items ?? []).filter((item): item is DispatchOccupancyMeasurement => item.kind === "DispatchOccupancyMeasurement"), [list.data])
  const node = search.get("node"), workload = search.get("workload"), status = search.get("status")
  const rows = all.filter(item => (!node || item.node === node) && (!workload || item.conditions.workload === workload) && (!status || (item.phase ?? "") === status))
  const selectedUid = search.get("uid")
  const selected = all.find(item => item.metadata.uid === selectedUid)
  const reset = { measurement: null, uid: null }

  function remove(item: DispatchOccupancyMeasurement) {
    const { namespace: ns, name, uid, resourceVersion } = item.metadata
    mutation.mutate(
      { path: `${measurementPath("dispatch-occupancy-measurements", ns, name)}?${new URLSearchParams({ uid })}`, method: "DELETE", version: JSON.stringify(resourceVersion) },
      {
        onSuccess: () => {
          setRemoving(null)
          if (selectedUid === uid) set(reset)
          toast({ title: "삭제됨", description: name, type: "success" })
          void list.refetch()
        },
        onError: error => {
          setRemoving(null)
          toast({ title: error instanceof ApiError && error.unknownOutcome ? "결과 미확인" : "삭제 실패", description: name, type: error instanceof ApiError && error.unknownOutcome ? "warning" : "error" })
          void list.refetch()
        },
      },
    )
  }

  const columns: Column<DispatchOccupancyMeasurement>[] = [
    { width: "30%", key: "name", header: "측정", cell: item => item.metadata.name, sortValue: item => item.metadata.name },
    { width: "14%", key: "node", header: "장치", cell: item => item.node, sortValue: item => item.node },
    { width: "24%", key: "workload", header: "서비스", cell: item => item.conditions.workload, sortValue: item => item.conditions.workload },
    { width: "8%", key: "batch", header: "배치", unit: "건", numeric: true, cell: item => item.conditions.batch, sortValue: item => item.conditions.batch },
    {
      width: "12%", key: "occupancy", header: "점유시간", unit: "ms", numeric: true, sortValue: item => occupancyNanos(item) == null ? undefined : Number(occupancyNanos(item)),
      cell: item => ms(occupancyNanos(item)) ?? <Missing reason={item.phase === "Complete" ? "결과 없음" : occupancyDisplayReason(item)} />,
    },
    { width: "12%", key: "status", header: "상태", cell: item => <StatusBadge status={statusOf(item)} reason={item.phase === "Complete" ? undefined : occupancyDisplayReason(item)} /> },
    {
      key: "menu", header: "", width: "3.5rem",
      cell: item => (
        <DropdownMenu>
          <DropdownMenuTrigger className="inline-flex size-8 items-center justify-center rounded-md hover:bg-muted" aria-label={`${item.metadata.name} 메뉴`}><MoreVertical className="size-4" aria-hidden="true" /></DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-32" onClick={event => event.stopPropagation()}>
            <DropdownMenuItem variant="destructive" disabled={item.phase === "Measuring"} onSelect={() => setRemoving(item)}>삭제</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
    },
  ]
  const stats = selected?.estimate?.statistics
  return (
    <>
      <FilterBar
        segments={[lead]}
        selects={[
          filterChoice("장치", node, distinct(all.map(item => item.node)), value => set({ node: value, ...reset })),
          filterChoice("서비스", workload, distinct(all.map(item => item.conditions.workload)), value => set({ workload: value, ...reset })),
          filterChoice("상태", status, distinct(all.map(item => item.phase ?? "")), value => set({ status: value, ...reset }), code => measurementPhaseStatus(code).label),
        ]}
      />
      <QueryBoundary query={list} isEmpty={() => !all.length} empty={<EmptyState title="분배 점유시간 결과 없음" />}>
        {() => (
          <DataTable
            label="분배 점유시간"
            rows={rows}
            rowKey={item => item.metadata.uid}
            columns={columns}
            onRowClick={item => set({ measurement: item.metadata.name, uid: item.metadata.uid })}
            selectedKey={selectedUid}
            busy={list.isFetching}
            download={{ filename: "dispatch-occupancy" }}
            empty={<EmptyState compact title="조건에 맞는 결과 없음" />}
          />
        )}
      </QueryBoundary>
      <DetailDrawer
        open={!!selectedUid}
        onOpenChange={open => { if (!open) set(reset) }}
        title={selected?.metadata.name ?? "분배 점유시간"}
        status={selected ? statusOf(selected) : undefined}
        actions={selected && <Button size="sm" variant="ghost" onClick={() => setRaw(true)}>원본 보기</Button>}
      >
        {selected ? (
          <>
            {stats && (
              <Section title="분포">
                <Fields columns={2} items={[
                  { label: "중앙값", value: `${ms(stats.median)} ms` },
                  { label: "사분위 범위", value: `${ms(stats.iqr)} ms` },
                  { label: "하위 10%", value: `${ms(stats.p10)} ms` },
                  { label: "상위 10%", value: `${ms(stats.p90)} ms` },
                  { label: "음수 비율", value: `${formatNumber(stats.negativeFraction * 100, 1)} %` },
                  { label: "인접 쌍", value: selected.estimate?.pairs == null ? null : `${formatNumber(selected.estimate.pairs)} 쌍` },
                ]} />
              </Section>
            )}
            <Section title="조건">
              <Fields columns={2} items={[
                { label: "장치", value: selected.node },
                { label: "서비스", value: selected.conditions.workload },
                { label: "배치", value: `${selected.conditions.batch} 건` },
                { label: "전력 모드", value: selected.status.powerMode },
                { label: "시작", value: <TimeText at={selected.measurementWindow?.start} mode="absolute" /> },
                { label: "종료", value: <TimeText at={selected.measurementWindow?.end} mode="absolute" /> },
                { label: "예열", value: `${selected.conditions.warmupSeconds} s` },
                { label: "측정", value: `${selected.conditions.durationSeconds} s` },
                { label: "송신자", value: `${selected.conditions.workers} 명` },
                ...(selected.phase === "Complete" ? [] : [{ label: "사유", value: occupancyDisplayReason(selected) }]),
              ]} />
            </Section>
            <RawDialog open={raw} onOpenChange={setRaw} value={selected} filename={`${selected.metadata.name}.json`} title="분배 점유시간 원본" />
          </>
        ) : <EmptyState compact title="목록에 없음" />}
      </DetailDrawer>
      <ConfirmDialog
        open={!!removing}
        onOpenChange={open => { if (!open && !mutation.isPending) setRemoving(null) }}
        title="분배 점유시간 삭제"
        target={removing?.metadata.name}
        changes={[{ label: "측정 기록", before: "있음", after: "삭제" }]}
        tone="destructive"
        confirmLabel="삭제"
        pending={mutation.isPending}
        onConfirm={() => removing && remove(removing)}
      >
        근거 파일도 함께 삭제됩니다
      </ConfirmDialog>
    </>
  )
}
