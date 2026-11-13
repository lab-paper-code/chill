"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { sectionTabs } from "@/lib/routes"
import { useChillQuery, useSession } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { formatKst, formatNumber } from "@/lib/format"
import { decisionStatus, schedulerReason, type StatusInfo } from "@/lib/status-labels"
import { decisionListPath, decisionRecordPath, decisionResult, latestDecisionPath, type DecisionPage, type DecisionRow } from "@/lib/scheduler-decisions"
import type { SchedulerObservationPage, SchedulerSettings } from "@/lib/read-types"
import type { WorkloadView } from "@/lib/domain"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Button, DataTable, EmptyState, FilterBar, HeadroomBar, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, StatRow, StatTile, StatusDot,
  TimeRange, TimeText, resolveTimeRange, useTimeRangeParam, type Column, type TimeRangeValue,
} from "@/components/kit"
import { DecisionDetail, ReasonSummary } from "./decision-detail"
import { DecisionCompare } from "./decision-compare"

const PAGE = 20
const decoder = { decoder: parseSchedulerJSON }

const rowStatus = (row: DecisionRow): StatusInfo => decisionStatus(decisionResult(row.decision?.status, row.decision?.targetKeep))

export function DecisionsScreen() {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const { config } = useSession()
  const maxSpan = config.maxWindowHours * 3_600_000
  const [range] = useTimeRangeParam(["15m", "1h", "24h"], "1h", maxSpan)
  const [raw, setRaw] = React.useState(false)
  const at = search.get("at") ?? ""
  const recordPath = decisionRecordPath(at)
  // Same key as the detail pane, so this shares its response.
  const record = useChillQuery<SchedulerObservationPage>(recordPath ?? "", { enabled: !!recordPath, ...decoder })
  const event = record.data?.events?.find(row => row.decision?.at === at)

  const set = React.useCallback((patch: Record<string, string | null>) => {
    const params = new URLSearchParams(search.toString())
    for (const [key, value] of Object.entries(patch)) { if (value == null) params.delete(key); else params.set(key, value) }
    const text = params.toString()
    router.replace(`${pathname}${text ? `?${text}` : ""}`, { scroll: false })
  }, [search, router, pathname])

  return (
    <Page>
      <PageHeader title="스케줄러" more={[{ label: "원본 보기", onSelect: () => setRaw(true), disabled: !event }]} />
      <PageTabs tabs={sectionTabs.scheduler} />
      <Tiles />
      <DecisionsBody key={JSON.stringify(range)} range={range} maxSpan={maxSpan} set={set} />
      {event && <RawDialog open={raw} onOpenChange={setRaw} value={event} filename={`scheduler-decision-${formatKst(event.at).replace(/\D/g, "")}`} />}
    </Page>
  )
}

function Tiles() {
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", decoder)
  const [mountedAt] = React.useState(() => Date.now())
  const latest = useChillQuery<DecisionPage>(latestDecisionPath(mountedAt), { poll: true, ...decoder })
  const last = latest.data?.events?.find(row => row.decision)
  const interval = settings.data?.controlIntervalNanos
  const elapsed = last?.decision?.evaluationElapsedNanos
  const over = elapsed != null && interval != null ? Number(elapsed) > Number(interval) : null
  const none = latest.error ? "기록 조회 실패" : latest.data ? "판단 기록 없음" : "조회 중"
  return (
    <StatRow columns={3}>
      <StatTile label="마지막 판단" value={last ? <TimeText at={last.at} /> : null} reason={none}
        sub={last ? rowStatus(last).label : undefined} href={last ? `/scheduler/decisions?at=${encodeURIComponent(last.decision!.at)}` : undefined} />
      <StatTile label="판단 주기" value={interval == null ? null : formatNumber(Number(interval) / 1e9, Number(interval) % 1e9 ? 1 : 0)} unit="s"
        reason={settings.error ? "설정 조회 실패" : "조회 중"} href="/scheduler/settings" />
      <StatTile label="마지막 소요" value={elapsed == null ? null : formatNumber(Number(elapsed) / 1e6, 0)} unit="ms" reason={last ? "소요 기록 없음" : none}
        info="후보를 만들고 비교하는 데 걸린 시간"
        status={over == null ? undefined : over ? { label: "주기 초과", tone: "fail" } : { label: "주기 이내", tone: "ok" }}
        sub={over == null ? undefined : `주기의 ${formatNumber((Number(elapsed) / Number(interval)) * 100, 1)}%`} />
    </StatRow>
  )
}

function DecisionsBody({ range, maxSpan, set }: { range: TimeRangeValue; maxSpan: number; set: (patch: Record<string, string | null>) => void }) {
  const search = useSearchParams()
  // Fixed per range so the list keeps one query key; a new range remounts the body.
  const [now] = React.useState(() => Date.now())
  const span = resolveTimeRange(range, now)
  // "변경만" by default; a link to one decision (`?at=` without a filter) opens the full list, since it may be a keep.
  const filter = search.get("filter")
  const changes = filter ? filter !== "all" : !search.get("at")
  const uid = search.get("uid") ?? ""
  const cursor = Math.max(0, Number(search.get("cursor")) || 0)
  const picks = (search.get("compare") ?? "").split(",").filter(Boolean).slice(0, 2)
  const [comparing, setComparing] = React.useState(false)
  const detailRef = React.useRef<HTMLDivElement>(null)

  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", decoder)
  const workloads = useChillQuery<WorkloadView[]>("v1/workloads?view=summary")
  const names = React.useMemo(() => new Map((workloads.data ?? []).map(w => [w.desired.metadata.uid, w.desired.metadata.name])), [workloads.data])
  const list = useChillQuery<DecisionPage>(decisionListPath(span, changes, uid, PAGE, cursor), decoder)
  const rows = React.useMemo(() => (list.data?.events ?? []).filter(row => row.decision), [list.data])
  const interval = settings.data?.controlIntervalNanos
  const elapsedMs = (row: DecisionRow) => row.decision?.evaluationElapsedNanos == null ? null : Number(row.decision.evaluationElapsedNanos) / 1e6
  const intervalMs = interval == null ? null : Number(interval) / 1e6
  const selected = search.get("at") ?? rows[0]?.decision?.at ?? null

  const setRange = (value: TimeRangeValue) => set("preset" in value
    ? { range: value.preset === "1h" ? null : value.preset, from: null, to: null, cursor: null }
    : { range: null, from: String(value.from), to: String(value.to), cursor: null })
  const select = (row: DecisionRow) => {
    set({ at: row.decision!.at, filter: changes ? "changes" : "all" })
    if (window.matchMedia("(max-width: 1279px)").matches) requestAnimationFrame(() => detailRef.current?.scrollIntoView({ block: "start" }))
  }
  const togglePick = (at: string, on: boolean) => {
    const next = on ? [...picks, at].slice(-2) : picks.filter(value => value !== at)
    set({ compare: next.length ? next.join(",") : null })
  }

  const columns: Column<DecisionRow>[] = [
    {
      key: "at", header: "시각", width: "33%",
      cell: row => {
        const at = row.decision!.at, checked = picks.includes(at)
        return (
          <span className="flex items-center gap-2.5">
            <Checkbox checked={checked} disabled={!checked && picks.length >= 2} aria-label={`${formatKst(at)} 비교에 담기`} onCheckedChange={value => togglePick(at, value === true)} />
            <span className="tabular-nums"><span className="hidden sm:inline">{formatKst(at).slice(5, 11)}</span>{formatKst(at).slice(11)}</span>
          </span>
        )
      },
      csv: row => formatKst(row.decision!.at),
    },
    { key: "result", header: "결과", width: "17%", cell: row => <StatusDot status={rowStatus(row)} showLabel />, csv: row => rowStatus(row).label },
    {
      key: "reason", header: "사유", missingReason: "기록된 사유 없음",
      cell: row => row.decision?.reasons?.length ? <ReasonSummary codes={row.decision.reasons} /> : null,
      csv: row => (row.decision?.reasons ?? []).map(code => { const reason = schedulerReason(code); return reason.code ?? reason.label }).join(", "),
    },
    {
      key: "elapsed", header: "소요", unit: "ms", numeric: true, width: "21%", missingReason: "소요 기록 없음",
      cell: row => {
        const ms = elapsedMs(row)
        return ms == null ? null : (
          <span className="inline-flex items-center gap-2">
            {formatNumber(ms, 0)}
            <HeadroomBar observed={ms} target={intervalMs} className="w-10" />
          </span>
        )
      },
      csv: row => { const ms = elapsedMs(row); return ms == null ? null : Math.round(ms) },
    },
  ]

  return (
    <>
      <FilterBar
        selects={[{
          label: "서비스", value: uid || "all", searchable: true, onChange: value => set({ uid: value === "all" ? null : value, cursor: null }),
          options: [{ value: "all", label: "모든 서비스" }, ...[...names].sort((a, b) => a[1].localeCompare(b[1])).map(([value, label]) => ({ value, label }))],
        }]}
        end={<TimeRange value={range} onChange={setRange} presets={["15m", "1h", "24h"]} custom={{ maxSpanMs: maxSpan }} />}
      />
      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,34rem)_minmax(0,1fr)]">
        <QueryBoundary query={list}>
          {() => (
            <DataTable label="판단 기록" rows={rows} rowKey={row => row.decision!.at} columns={columns} pageSize={0}
              onRowClick={select} selectedKey={selected} download={{ filename: "scheduler-decisions" }}
              toolbar={<FilterBar
                segments={[{ label: "판단 종류", value: changes ? "changes" : "all", onChange: value => set({ filter: value, cursor: null }), options: [{ value: "changes", label: "변경만" }, { value: "all", label: "전체" }] }]} />}
              actions={<Button size="sm" variant="outline" disabled={picks.length !== 2} disabledReason="비교할 판단 두 개 선택" onClick={() => setComparing(true)}>비교 {picks.length}/2</Button>}
              paging={{
                mode: "cursor", hasPrevious: cursor > 0, hasNext: !!list.data?.nextCursor,
                onPrevious: () => set({ cursor: cursor > PAGE ? String(cursor - PAGE) : null }), onNext: () => set({ cursor: list.data?.nextCursor ?? null }),
              }}
              empty={<EmptyState compact title={changes ? "변경한 판단 없음" : "판단 없음"} action={changes ? <Button size="sm" variant="outline" onClick={() => set({ filter: "all", cursor: null })}>전체 보기</Button> : undefined} />} />
          )}
        </QueryBoundary>
        <div ref={detailRef} className="min-w-0 scroll-mt-4">
          {selected && <DecisionDetail key={selected} at={selected} names={names} intervalNanos={interval} />}
        </div>
      </div>
      {picks.length === 2 && <DecisionCompare open={comparing} onOpenChange={setComparing} picks={picks} names={names} />}
    </>
  )
}
