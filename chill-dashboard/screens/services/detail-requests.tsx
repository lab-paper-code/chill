"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { RefreshCw } from "lucide-react"
import {
  Button, DataTable, DetailDrawer, EmptyState, Fields, HeadroomBar, IdText, QueryBoundary, RawDialog, Select, StatRow, StatTile, StatusBadge, TimeRange, TimeText,
  resolveTimeRange, useTimeRangeParam, type Column, type TimePreset, type TimeRangeValue,
} from "@/components/kit"
import { useChillQuery, useSession } from "@/lib/client"
import type { WorkloadView } from "@/lib/domain"
import { formatKst, formatMs, formatNumber, msNumber } from "@/lib/format"
import type { ObservationResult, RequestRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { nanosToMs, outcomeCodes, requestMs, outcomeLabel, requestError, requestMarks, requestResult } from "@/lib/services-records"
import { StageBars } from "./stage-bars"

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
  // A preset window is resolved once per choice or refresh: `to` stays fixed while the user pages towards older rows,
  // so rows arriving meanwhile never shift the pages, and the query key stays stable between renders.
  const [anchor, setAnchor] = React.useState(() => Date.now())
  const span = React.useMemo(() => resolveTimeRange(range, anchor), [range, anchor])
  if (!uid) return <EmptyState title="서비스 식별 정보 없음" />
  return (
    <RequestsView
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
  // Newest first; cursors of the pages visited so far, each towards older rows; the last entry is the page on screen.
  // A new window, refresh or filter starts at the newest page. The tiles come from the same response as the rows.
  // The view stays mounted, so an open filter popover stays open while the user ticks several outcomes.
  const [cursors, setCursors] = React.useState<string[]>([])
  const [selected, setSelected] = React.useState<RequestRow | null>(null)
  const filterKey = [from, to, outcomes.join(), definitions.join()].join("|")
  const [seenKey, setSeenKey] = React.useState(filterKey)
  if (seenKey !== filterKey) { setSeenKey(filterKey); setCursors([]); setSelected(null) }
  const cursor = cursors[cursors.length - 1]
  const params = new URLSearchParams({ from: new Date(from).toISOString(), to: new Date(to).toISOString(), limit: String(limit), order: "desc" })
  if (cursor) params.set("cursor", cursor)
  outcomes.forEach(value => params.append("outcome", value))
  definitions.forEach(value => params.append("definition", value))
  const query = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${params}`, { decoder: parseSchedulerJSON })
  const data = query.data
  const definitionOptions = (data?.definitions ?? []).map((item, index) => ({ value: item.identity, label: `정의 ${index + 1}` }))
  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <TimeRange value={range} onChange={onRange} presets={presets} custom={{ maxSpanMs }} />
        <div className="w-full sm:w-52"><Select multiple aria-label="결과" inputSize="sm" options={outcomeOptions} value={outcomes} onChange={onOutcomes} placeholder="결과 전체" /></div>
        {(definitionOptions.length > 1 || definitions.length > 0) && <div className="w-full sm:w-44"><Select multiple aria-label="적용 정의" inputSize="sm" options={definitionOptions} value={definitions} onChange={onDefinitions} placeholder="정의 전체" /></div>}
        <div className="ml-auto flex items-center gap-2">
          {data?.gapUntil && <StatusBadge tone="warn" label="관측 공백" reason={`${formatKst(data.gapUntil)} KST까지`} />}
          <Button variant="outline" size="sm" iconLeft={<RefreshCw />} loading={query.isFetching} onClick={() => "preset" in range ? onRefresh() : void query.refetch()}>새로고침</Button>
        </div>
      </div>
      <QueryBoundary query={query} skeleton="table" isEmpty={result => result.total === 0} empty={<EmptyState title="요청 없음" />}>
        {result => (
          <>
            <Summary result={result} />
            <DataTable
              label="요청" title="요청 기록" narrow="scroll" rows={result.requests ?? []} rowKey={row => row.requestID} columns={columns} onRowClick={setSelected} selectedKey={selected?.requestID}
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
  // One row of tiles: request count, a latency tile per definition, then the outcomes the server counted.
  const tiles = Math.min(Math.max(1 + result.cohorts.length + counts.length, 2), 6) as 2 | 3 | 4 | 5 | 6
  return (
    <StatRow columns={tiles}>
      <StatTile label="요청" value={formatNumber(result.total)} sub={result.selected !== result.total ? `조건에 맞는 요청 ${formatNumber(result.selected)}` : undefined} />
      {result.cohorts.map((cohort, index) => {
        const quantile = nanosToMs(cohort.quantileNanos), target = nanosToMs(cohort.targetLatencyNanos)
        return (
          <StatTile
            key={cohort.definition} label={`${result.cohorts.length > 1 ? `정의 ${index + 1} ` : ""}지연 p${cohort.percentile ?? "—"}`}
            value={quantile == null ? null : formatNumber(quantile, 0)} unit="ms" reason="완료된 요청 표본 없음"
            info="완료된 요청만으로 서버가 집계한 분위수"
            sub={<span className="flex flex-wrap items-center gap-x-2 gap-y-1">{target != null && <><span>목표 {formatMs(target)}</span>{quantile != null && <HeadroomBar observed={quantile} target={target} />}</>}<span>표본 {formatNumber(cohort.sampleCount)}</span></span>}
          />
        )
      })}
      {counts.map(([code, count]) => <StatTile key={code} label={outcomeLabel(code)} value={formatNumber(count)} sub={result.total ? `${formatNumber((count / result.total) * 100, 1)}%` : undefined} />)}
    </StatRow>
  )
}

const columns: Column<RequestRow>[] = [
  { key: "at", header: "시각", cell: row => <TimeText at={row.receivedAt} mode="list" />, csv: row => row.receivedAt, width: "11rem" },
  { key: "result", header: "결과", cell: row => <StatusBadge status={requestResult(row)} />, csv: row => requestResult(row).label },
  { key: "elapsed", header: "소요 시간", unit: "ms", numeric: true, cell: row => (row.elapsedNanos === undefined ? null : msNumber(nanosToMs(row.elapsedNanos))), csv: row => nanosToMs(row.elapsedNanos), missingReason: "아직 끝나지 않음" },
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
  return (
    <>
      <DetailDrawer
        open={row !== null} onOpenChange={open => { if (!open) onClose() }} title="요청" status={current ? requestResult(current) : undefined}
        actions={<Button size="sm" variant="outline" onClick={() => setRaw(true)}>원본 보기</Button>}
      >
        {current && (
          <div className="grid gap-5">
            <Fields columns={2} items={[
              { label: "시각", value: <TimeText at={current.receivedAt} mode="detail" /> },
              { label: "소요 시간", value: current.elapsedNanos === undefined ? null : requestMs(current.elapsedNanos), reason: "아직 끝나지 않음" },
              { label: "장치", value: current.node?.name, reason: "배정 전" },
              { label: "배치", value: current.batchSize, reason: "실행 기록 없음" },
              ...(result.definitions.length > 1 ? [{ label: "정의", value: index < 0 ? null : `정의 ${index + 1}`, reason: "이전 정의" }] : []),
              { label: "요청", value: <IdText value={current.requestID} /> },
              ...(current.error ? [{ label: "오류", value: requestError(current.error), reason: `코드 ${current.error}` }] : []),
            ]} />
            <section className="grid gap-2" aria-label="단계별 시간">
              <h3 className="text-sm font-semibold">단계별 시간</h3>
              <StageBars row={current} />
            </section>
            {requestMarks(current).length > 0 && (
              <section className="grid gap-2" aria-label="수신 후 시점">
                <h3 className="text-sm font-semibold">수신 후 시점</h3>
                <Fields columns={2} items={requestMarks(current).map(mark => ({ label: mark.label, value: requestMs(mark.nanos) }))} />
              </section>
            )}
          </div>
        )}
      </DetailDrawer>
      <RawDialog open={raw} onOpenChange={setRaw} value={current} filename={`request-${current?.requestID ?? "row"}`} title="요청 원본" />
    </>
  )
}
