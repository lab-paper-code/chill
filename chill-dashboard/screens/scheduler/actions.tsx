"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { sectionTabs } from "@/lib/routes"
import { useChillQuery, useChillQueries } from "@/lib/client"
import { useLive } from "@/lib/use-live"
import { actionProgress, reservationReason } from "@/lib/live-display"
import { isLiveActionActive, liveActionKey, type LiveAction, type LiveEvent, type LiveReservation } from "@/lib/live"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { schedulerRecordMatches, schedulerRecordPath, schedulerRecordRef } from "@/lib/scheduler-history"
import { actionElapsedNanos, actionKindLabel, clusterRow, groupActions, phaseStatus, planVsActual, type ActionRow } from "@/lib/scheduler-actions"
import { formatDurationNanos, formatKst } from "@/lib/format"
import type { SchedulerObservation, SchedulerObservationSummaryPage } from "@/lib/read-types"
import {
  Button, DataTable, DetailDrawer, EmptyState, FilterBar, Fields, HeadroomBar, Page, PageHeader, PageTabs, QueryBoundary, RawDialog,
  Section, StatusBadge, TimeRange, TimeText, resolveTimeRange, useNow, useTimeRangeParam, type Column, type TimeRangeValue,
} from "@/components/kit"

const KINDS = [{ value: "actions", label: "작업" }, { value: "reservations", label: "예약" }, { value: "events", label: "사건" }]
const DAY = 86_400_000
const kindText = (kind: string) => actionKindLabel(kind)
const nodeText = (node: { uid: string; name: string }) => (node.uid ? node.name || null : null)

export function ActionsScreen() {
  const search = useSearchParams()
  const [range, setRange] = useTimeRangeParam(["15m", "1h", "24h"], "1h", DAY)
  const atText = search.get("at") ?? ""
  const at = Number.isFinite(Date.parse(atText)) ? Date.parse(atText) : null
  // The window is fixed per range so the query key stays stable between renders; a new range remounts.
  return (
    <Page>
      <PageHeader title="스케줄러" />
      <PageTabs tabs={sectionTabs.scheduler} />
      <ActionsBody key={JSON.stringify(range) + String(at)} range={range} setRange={setRange} at={at} />
    </Page>
  )
}

function ActionsBody({ range, setRange, at }: { range: TimeRangeValue; setRange: (value: TimeRangeValue) => void; at: number | null }) {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const kind = KINDS.some(k => k.value === search.get("kind")) ? search.get("kind")! : "actions"
  const status = search.get("status") ?? "all"
  const [now] = React.useState(() => Date.now())
  const window = at ? { from: at - 5 * 60_000, to: Math.min(at + 3_600_000, now) } : resolveTimeRange(range, now)

  const set = (patch: Record<string, string | null>) => {
    const params = new URLSearchParams(search.toString())
    for (const [key, value] of Object.entries(patch)) { if (value == null) params.delete(key); else params.set(key, value) }
    const text = params.toString()
    router.replace(`${pathname}${text ? `?${text}` : ""}`, { scroll: false })
  }

  return (
    <>
      <FilterBar
        segments={[{ label: "종류", value: kind, options: KINDS, onChange: value => set({ kind: value === "actions" ? null : value, status: null, action: null, at: null }) }]}
        end={kind !== "reservations" && <TimeRange value={at ? { from: window.from, to: window.to } : range} presets={["15m", "1h", "24h"]} custom={{ maxSpanMs: DAY }}
          onChange={value => { if (!at) setRange(value); else set("preset" in value ? { range: value.preset, from: null, to: null, at: null, action: null } : { range: null, from: String(value.from), to: String(value.to), at: null, action: null }) }} />}
      />
      {kind === "actions" && (
        <>
          <RunningActions />
          <ActionHistory window={window} status={status} onStatus={value => set({ status: value === "all" ? null : value })}
            selected={search.get("action")} onSelect={id => set({ action: id })} />
        </>
      )}
      {kind === "reservations" && <Reservations />}
      {kind === "events" && <ClusterEvents window={window} />}
    </>
  )
}

function useServiceNames() {
  const { state } = useLive()
  return React.useMemo(() => new Map((state.snapshot?.workloads ?? []).map(w => [w.uid, w.name])), [state.snapshot?.workloads])
}

function RunningActions() {
  const { state } = useLive(), names = useServiceNames(), now = useNow()
  const rows = React.useMemo(
    () => (state.snapshot?.actions ?? []).filter(isLiveActionActive).sort((a, b) => a.startedAt.localeCompare(b.startedAt) || liveActionKey(a).localeCompare(liveActionKey(b))),
    [state.snapshot?.actions],
  )
  const columns: Column<LiveAction>[] = [
    { key: "start", header: "시작", cell: r => <TimeText at={r.startedAt} />, csv: r => r.startedAt, width: "9rem" },
    { key: "kind", header: "동작", cell: r => kindText(r.kind), csv: r => r.kind },
    { key: "node", header: "장치", cell: r => nodeText(r.node), csv: r => r.node.name },
    { key: "service", header: "서비스", cell: r => names.get(r.workloadUID) ?? null, csv: r => names.get(r.workloadUID) },
    { key: "status", header: "상태", cell: r => <StatusBadge status={{ ...phaseStatus(r.status), ...(actionProgress(r, now) === "delayed" ? { label: "지연", tone: "warn" as const } : {}) }} />, csv: r => r.status },
    {
      key: "elapsed", header: "경과 / 예상", width: "14rem", csv: r => Math.round((now - Date.parse(r.startedAt)) / 1000),
      cell: r => {
        const seconds = Math.max(0, (now - Date.parse(r.startedAt)) / 1000)
        return (
          <div className="flex items-center gap-3">
            <span className="w-14 shrink-0 tabular-nums">{formatDurationNanos(seconds * 1e9)}</span>
            {r.expectedSeconds ? <HeadroomBar observed={seconds} target={r.expectedSeconds} unit="초" className="min-w-20 flex-1" /> : null}
          </div>
        )
      },
    },
  ]
  return (
    <Section title="진행 중">
      {rows.length ? <DataTable label="진행 중인 작업" rows={rows} rowKey={liveActionKey} columns={columns} />
        : <EmptyState compact title="진행 중인 작업 없음" className="rounded-xl border border-border bg-card" />}
    </Section>
  )
}

function ActionHistory({ window, status, onStatus, selected, onSelect }: {
  window: { from: number; to: number }; status: string; onStatus: (value: string) => void; selected: string | null; onSelect: (id: string | null) => void
}) {
  // ponytail: cursor stack lives in state, so reload returns to the first page; put the cursor in the URL if deep links to later pages matter.
  const [cursors, setCursors] = React.useState([""])
  const cursor = cursors[cursors.length - 1]
  const path = `v1/observations/scheduler?${new URLSearchParams({
    filter: "action", view: "summary", order: "desc", limit: "100", from: new Date(window.from).toISOString(), to: new Date(window.to).toISOString(), ...(cursor ? { cursor } : {}),
  })}`
  const query = useChillQuery<SchedulerObservationSummaryPage>(path, { decoder: parseSchedulerJSON })
  const [raw, setRaw] = React.useState(false)
  return (
    <Section title="기록">
      <QueryBoundary query={query} isEmpty={d => !d.events?.length} empty={<EmptyState title="기록 없음" />}>
        {page => {
          const all = groupActions(page.events ?? [])
          const tone = (r: ActionRow) => phaseStatus(r.phase).tone
          const bucket = (r: ActionRow) => (tone(r) === "ok" ? "ok" : tone(r) === "fail" ? "fail" : "other")
          const rows = status === "all" ? all : all.filter(r => bucket(r) === status)
          const count = (key: string) => all.filter(r => bucket(r) === key).length
          const columns: Column<ActionRow>[] = [
            { key: "start", header: "시작", cell: r => <TimeText at={r.startedAt} mode="absolute" />, csv: r => r.startedAt, width: "11rem" },
            { key: "kind", header: "동작", cell: r => kindText(r.kind), csv: r => r.kind },
            { key: "node", header: "장치", cell: r => nodeText(r.node), csv: r => r.node.name },
            { key: "status", header: "상태", cell: r => <StatusBadge status={phaseStatus(r.phase)} />, csv: r => r.phase },
            { key: "took", header: "소요", numeric: true, cell: r => { const n = actionElapsedNanos(r); return n == null ? null : formatDurationNanos(n) }, csv: r => actionElapsedNanos(r) },
          ]
          const row = all.find(r => r.id === selected)
          return (
            <>
              <DataTable
                label="작업 기록" rows={rows} rowKey={r => r.id} columns={columns}
                selectedKey={selected ?? undefined} onRowClick={r => onSelect(r.id)}
                download={{ filename: "scheduler-actions" }}
                empty={<EmptyState compact title="해당 기록 없음" />}
                toolbar={<FilterBar segments={[{ label: "상태", value: status, onChange: onStatus, options: [{ value: "all", label: "전체", count: all.length }, { value: "ok", label: "완료", count: count("ok") }, { value: "fail", label: "실패", count: count("fail") }, { value: "other", label: "그 외", count: count("other") }] }]} />}
                paging={{ mode: "cursor", hasPrevious: cursors.length > 1, hasNext: !!page.nextCursor, onPrevious: () => setCursors(c => c.slice(0, -1)), onNext: () => setCursors(c => [...c, page.nextCursor!]) }}
              />
              {row && <ActionDrawer row={row} page={page} onClose={() => onSelect(null)} onRaw={() => setRaw(true)} />}
              {row && <RawDialog open={raw} onOpenChange={setRaw} value={row.event} filename="scheduler-action" />}
            </>
          )
        }}
      </QueryBoundary>
    </Section>
  )
}

function ActionDrawer({ row, page, onClose, onRaw }: { row: ActionRow; page: SchedulerObservationSummaryPage; onClose: () => void; onRaw: () => void }) {
  const ref = schedulerRecordRef(row.event, page.from, page.to, String(row.event.offset ?? ""))
  const query = useChillQuery<{ events: SchedulerObservation[] | null }>(schedulerRecordPath(ref), { decoder: parseSchedulerJSON })
  const full = query.data?.events?.[0]
  const record = full?.action && schedulerRecordMatches(ref, { at: full.at, offset: full.offset, action: full.action }) ? full.action : null
  const took = actionElapsedNanos(row)
  const compare = record ? planVsActual(record) : []
  return (
    <DetailDrawer open onOpenChange={open => { if (!open) onClose() }} title={kindText(row.kind) ?? "작업"} status={phaseStatus(row.phase)} actions={<Button size="sm" variant="outline" onClick={onRaw}>원본 보기</Button>}>
      <div className="grid gap-6">
        <Fields columns={2} items={[
          { label: "장치", value: nodeText(row.node) },
          { label: "시작", value: formatKst(row.startedAt) },
          { label: "종료", value: took == null ? null : formatKst(row.at), reason: "진행 중" },
          { label: "소요", value: took == null ? null : formatDurationNanos(took) },
          ...(record?.instance ? [{ label: "인스턴스", value: record.instance }] : []),
        ]} />
        {record?.error && <Fields columns={1} items={[{ label: "오류", value: record.error }]} />}
        <Section title="계획과 실제">
          <QueryBoundary query={query} skeleton="block" isEmpty={() => !compare.length} empty={<EmptyState compact title="계획 기록 없음" />}>
            {() => (
              <DataTable label="계획과 실제" pageSize={0} rows={compare} rowKey={r => r.label}
                columns={[
                  { key: "label", header: "항목", cell: r => r.label },
                  { key: "plan", header: "계획", cell: r => r.plan },
                  { key: "actual", header: "실제", cell: r => r.actual },
                ]} />
            )}
          </QueryBoundary>
        </Section>
      </div>
    </DetailDrawer>
  )
}

function Reservations() {
  const { state } = useLive(), names = useServiceNames()
  const rows = React.useMemo(() => (state.snapshot?.reservations ?? []).map((r, i) => ({ ...r, key: `${r.owner}/${i}` })), [state.snapshot?.reservations])
  const columns: Column<LiveReservation & { key: string }>[] = [
    { key: "service", header: "서비스", cell: r => r.workloadUIDs.map(uid => names.get(uid)).filter(Boolean).join(", ") || (r.reason.startsWith("measurement-lease") ? "측정 장치 예약" : null), csv: r => r.workloadUIDs.map(uid => names.get(uid)).join(", "), width: "16rem" },
    { key: "nodes", header: "장치 수", numeric: true, cell: r => r.nodes.length, csv: r => r.nodes.length, width: "8rem" },
    { key: "reason", header: "사유", cell: r => (r.reason.endsWith("profiling run in progress") ? "측정 실행 중" : r.reason ? reservationReason(r.reason) : null), csv: r => r.reason },
  ]
  return (
    <Section title="예약">
      <DataTable label="예약" rows={rows} rowKey={r => r.key} columns={columns} empty={<EmptyState compact title="예약 없음" />} />
    </Section>
  )
}

type EventsPage = { events: LiveEvent[]; nextCursor?: string }

function ClusterEvents({ window }: { window: { from: number; to: number } }) {
  const [cursors, setCursors] = React.useState([""])
  const paths = cursors.map(cursor => `v1/observations/events?${new URLSearchParams({ from: new Date(window.from).toISOString(), to: new Date(window.to).toISOString(), limit: "100", ...(cursor ? { cursor } : {}) })}`)
  const pages = useChillQueries<EventsPage>(paths)
  const last = pages[pages.length - 1]
  const rows = React.useMemo(
    () => pages.flatMap(p => p.data?.events ?? []).flatMap(e => (e.kind === "cluster" ? [{ at: e.at, ...clusterRow(e.cluster) }] : []))
      .sort((a, b) => b.at.localeCompare(a.at)).map((r, i) => ({ ...r, key: `${r.at}/${i}` })),
    [pages],
  )
  const columns: Column<(typeof rows)[number]>[] = [
    { key: "at", header: "시각", cell: r => <TimeText at={r.at} mode="absolute" />, csv: r => r.at, width: "11rem" },
    { key: "kind", header: "종류", cell: r => r.kind, csv: r => r.kind, width: "6rem" },
    { key: "target", header: "대상", cell: r => r.target, csv: r => r.target },
    { key: "content", header: "내용", cell: r => r.content, csv: r => r.content },
  ]
  return (
    <Section title="사건">
      <QueryBoundary query={pages[0]} isEmpty={() => !rows.length} empty={<EmptyState title="사건 없음" />}>
        {() => (
          <div className="grid gap-3">
            <DataTable label="사건" rows={rows} rowKey={r => r.key} columns={columns} pageSize={0} download={{ filename: "cluster-events" }} />
            {pages.at(-1)?.data?.nextCursor && (
              <div className="flex justify-center">
                <Button variant="outline" loading={last.isFetching} onClick={() => setCursors(c => [...c, last.data!.nextCursor!])}>이전 기록 더 보기</Button>
              </div>
            )}
          </div>
        )}
      </QueryBoundary>
    </Section>
  )
}
