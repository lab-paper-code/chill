"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { RefreshCw } from "lucide-react"
import {
  Button, DataTable, DetailDrawer, EmptyState, Fields, HeadroomBar, IdText, QueryBoundary, RawDialog, StatRow, StatTile, StatusBadge, TimeRange, TimeText,
  resolveTimeRange, useTimeRangeParam, type Column, type TimePreset, type TimeRangeValue,
} from "@/components/kit"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"
import { useChillQuery, useSession } from "@/lib/client"
import type { WorkloadView } from "@/lib/domain"
import { formatDurationNanos, formatKst, formatMs, formatNumber } from "@/lib/format"
import type { ObservationResult, RequestRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { nanosToMs, outcomeCodes, outcomeLabel, requestMarks, requestResult, requestStages } from "@/lib/services-records"

export type ServiceTabProps = { name: string; uid: string | undefined; workload: WorkloadView | undefined }

const HOUR = 3_600_000
const outcomeOptions = outcomeCodes.map(code => ({ value: code, label: outcomeLabel(code) }))

/** Repeated URL parameter helpers; every write replaces the entry so Back and reload keep the filters. */
function useListParam(key: string): [string[], (values: string[]) => void] {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const values = search.getAll(key)
  const set = (next: string[]) => {
    const params = new URLSearchParams(search.toString())
    params.delete(key)
    next.forEach(value => params.append(key, value))
    const query = params.toString()
    router.replace(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }
  return [values, set]
}

export function ServiceRequestsTab({ uid }: ServiceTabProps) {
  const { config } = useSession()
  const maxSpanMs = config.maxWindowHours * HOUR
  const presets = (["15m", "1h", "24h"] as TimePreset[]).filter(preset => preset !== "24h" || config.maxWindowHours >= 24)
  const [range, setRange] = useTimeRangeParam(presets, "1h", maxSpanMs)
  const [outcomes, setOutcomes] = useListParam("routcome")
  const [definitions, setDefinitions] = useListParam("rdef")
  // A preset window is resolved once per choice or refresh, so the query key stays stable between renders.
  const [anchor, setAnchor] = React.useState(() => Date.now())
  const span = React.useMemo(() => resolveTimeRange(range, anchor), [range, anchor])
  if (!uid) return <EmptyState title="서비스 식별 정보 없음" />
  return (
    <RequestsView
      key={[span.from, span.to, anchor, outcomes.join(), definitions.join()].join("|")}
      uid={uid} from={span.from} to={span.to} limit={Math.min(config.pageSize, config.maxPageSize)}
      range={range} presets={presets} maxSpanMs={maxSpanMs} onRange={next => { setAnchor(Date.now()); setRange(next) }} onRefresh={() => setAnchor(Date.now())}
      outcomes={outcomes} onOutcomes={setOutcomes} definitions={definitions} onDefinitions={setDefinitions}
    />
  )
}

function RequestsView({ uid, from, to, limit, range, presets, maxSpanMs, onRange, onRefresh, outcomes, onOutcomes, definitions, onDefinitions }: {
  uid: string; from: number; to: number; limit: number
  range: TimeRangeValue; presets: TimePreset[]; maxSpanMs: number; onRange: (value: TimeRangeValue) => void; onRefresh: () => void
  outcomes: string[]; onOutcomes: (values: string[]) => void; definitions: string[]; onDefinitions: (values: string[]) => void
}) {
  // Cursors of the pages visited so far; the last entry is the page on screen. The view remounts when a filter changes.
  const [cursors, setCursors] = React.useState<string[]>([])
  const cursor = cursors[cursors.length - 1]
  const params = new URLSearchParams({ from: new Date(from).toISOString(), to: new Date(to).toISOString(), limit: String(limit) })
  if (cursor) params.set("cursor", cursor)
  outcomes.forEach(value => params.append("outcome", value))
  definitions.forEach(value => params.append("definition", value))
  const query = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${params}`, { decoder: parseSchedulerJSON })
  const data = query.data
  const definitionOptions = (data?.definitions ?? []).map((item, index) => ({ value: item.identity, label: `정의 ${index + 1}` }))
  const [selected, setSelected] = React.useState<RequestRow | null>(null)
  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <TimeRange value={range} onChange={onRange} presets={presets} custom={{ maxSpanMs }} />
        <div className="w-full sm:w-52"><SelectBlock multiple inputSize="sm" options={outcomeOptions} value={outcomes} onChange={onOutcomes} placeholder="결과 전체" /></div>
        {(definitionOptions.length > 1 || definitions.length > 0) && <div className="w-full sm:w-44"><SelectBlock multiple inputSize="sm" options={definitionOptions} value={definitions} onChange={onDefinitions} placeholder="정의 전체" /></div>}
        <div className="ml-auto flex items-center gap-2">
          {data?.gapUntil && <StatusBadge tone="warn" label="관측 공백" reason={`${formatKst(data.gapUntil)} KST까지`} />}
          <Button variant="outline" size="sm" iconLeft={<RefreshCw />} loading={query.isFetching} onClick={onRefresh}>새로고침</Button>
        </div>
      </div>
      <QueryBoundary query={query} skeleton="table" isEmpty={result => result.total === 0} empty={<EmptyState title="요청 없음" />}>
        {result => (
          <>
            <Summary result={result} />
            <DataTable
              label="요청" rows={result.requests ?? []} rowKey={row => row.requestID} columns={columns} onRowClick={setSelected} selectedKey={selected?.requestID}
              paging={{ mode: "cursor", hasPrevious: cursors.length > 0, hasNext: !!result.nextCursor, onPrevious: () => setCursors(list => list.slice(0, -1)), onNext: () => setCursors(list => [...list, result.nextCursor!]) }}
              download={{ filename: `requests-${formatKst(from, false).replace(/\D/g, "")}` }}
              empty={<EmptyState compact title="조건에 맞는 요청 없음" />}
            />
            <RequestDrawer row={selected} result={result} onClose={() => setSelected(null)} />
          </>
        )}
      </QueryBoundary>
    </div>
  )
}

function Summary({ result }: { result: ObservationResult }) {
  const counts = outcomeCodes.map(code => [code, result.outcomes[code] ?? 0] as const).filter(([, count]) => count > 0)
  return (
    <StatRow>
      <StatTile label="요청" value={formatNumber(result.total)} sub={result.selected !== result.total ? `조건에 맞는 요청 ${formatNumber(result.selected)}` : undefined} />
      {result.cohorts.map((cohort, index) => {
        const quantile = nanosToMs(cohort.quantileNanos), target = nanosToMs(cohort.targetLatencyNanos)
        return (
          <StatTile
            key={cohort.definition} label={`${result.cohorts.length > 1 ? `정의 ${index + 1} ` : ""}지연 ${cohort.percentile ?? "—"}%`}
            value={quantile == null ? null : formatNumber(quantile, 0)} unit="ms" reason="완료된 요청 표본 없음"
            info="완료된 요청만으로 서버가 집계한 분위수"
            sub={<span className="inline-flex items-center gap-2">{target != null && <>목표 {formatMs(target, 0)} <HeadroomBar observed={quantile} target={target} /></>} 표본 {formatNumber(cohort.sampleCount)}</span>}
          />
        )
      })}
      {counts.map(([code, count]) => <StatTile key={code} label={outcomeLabel(code)} value={formatNumber(count)} sub={result.total ? `${formatNumber((count / result.total) * 100, 1)}%` : undefined} />)}
    </StatRow>
  )
}

const columns: Column<RequestRow>[] = [
  { key: "at", header: "시각", cell: row => <TimeText at={row.receivedAt} mode="absolute" />, csv: row => row.receivedAt, width: "11rem" },
  { key: "result", header: "결과", cell: row => <StatusBadge status={requestResult(row)} />, csv: row => requestResult(row).label },
  { key: "elapsed", header: "소요 시간", unit: "ms", numeric: true, cell: row => (row.elapsedNanos === undefined ? null : formatNumber(nanosToMs(row.elapsedNanos), 1)), csv: row => nanosToMs(row.elapsedNanos), missingReason: "아직 끝나지 않음" },
  { key: "node", header: "장치", cell: row => row.node?.name, csv: row => row.node?.name, missingReason: "배정 전" },
  { key: "batch", header: "배치", numeric: true, cell: row => row.batchSize, csv: row => row.batchSize, missingReason: "실행 기록 없음" },
]

function RequestDrawer({ row, result, onClose }: { row: RequestRow | null; result: ObservationResult; onClose: () => void }) {
  const [raw, setRaw] = React.useState(false)
  // Keep the last row while the drawer closes so its content does not blank mid-animation.
  const [shown, setShown] = React.useState<RequestRow | null>(null)
  if (row && row !== shown) setShown(row)
  const current = row ?? shown
  const index = current ? result.definitions.findIndex(item => item.identity === current.definitionID) : -1
  const stages = current ? requestStages(current) : null
  // Bars are scaled to the total time so a lone stage does not look like the whole request.
  const total = Math.max(nanosToMs(current?.elapsedNanos) ?? 0, stages?.reduce((sum, stage) => sum + stage.ms, 0) ?? 0)
  return (
    <>
      <DetailDrawer
        open={row !== null} onOpenChange={open => { if (!open) onClose() }} title="요청" status={current ? requestResult(current) : undefined}
        actions={<Button size="sm" variant="outline" onClick={() => setRaw(true)}>원본 보기</Button>}
      >
        {current && (
          <div className="grid gap-5">
            <Fields columns={2} items={[
              { label: "시각", value: <TimeText at={current.receivedAt} mode="absolute" /> },
              { label: "소요 시간", value: current.elapsedNanos === undefined ? null : formatDurationNanos(current.elapsedNanos), reason: "아직 끝나지 않음" },
              { label: "장치", value: current.node?.name, reason: "배정 전" },
              { label: "배치", value: current.batchSize, reason: "실행 기록 없음" },
              ...(result.definitions.length > 1 ? [{ label: "정의", value: index < 0 ? null : `정의 ${index + 1}`, reason: "이전 정의" }] : []),
              { label: "요청", value: <IdText value={current.requestID} /> },
              ...(current.error ? [{ label: "오류", value: current.error }] : []),
            ]} />
            <section className="grid gap-2" aria-label="단계별 시간">
              <h3 className="text-sm font-semibold">단계별 시간</h3>
              {stages ? stages.map(stage => (
                <div key={stage.key} className="grid grid-cols-[4rem_minmax(0,1fr)_5rem] items-center gap-3 text-sm">
                  <span className="text-muted-foreground">{stage.label}</span>
                  <span className="h-2 overflow-hidden rounded-full bg-muted" role="img" aria-label={`${stage.label} ${formatMs(stage.ms)}`}>
                    <span className="block h-full rounded-full bg-primary" style={{ width: `${total > 0 ? Math.max((stage.ms / total) * 100, stage.ms > 0 ? 1 : 0) : 0}%` }} />
                  </span>
                  <span className="text-right tabular-nums">{formatMs(stage.ms)}</span>
                </div>
              )) : <EmptyState compact title="단계 기록 없음" />}
            </section>
            {requestMarks(current).length > 0 && (
              <section className="grid gap-2" aria-label="수신 후 시점">
                <h3 className="text-sm font-semibold">수신 후 시점</h3>
                <Fields columns={2} items={requestMarks(current).map(mark => ({ label: mark.label, value: formatDurationNanos(mark.nanos) }))} />
              </section>
            )}
          </div>
        )}
      </DetailDrawer>
      <RawDialog open={raw} onOpenChange={setRaw} value={current} filename={`request-${current?.requestID ?? "row"}`} title="요청 원본" />
    </>
  )
}
