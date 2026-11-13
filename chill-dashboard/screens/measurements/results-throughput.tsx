"use client"

import * as React from "react"
import { DataTable, DetailDrawer, EmptyState, FilterBar, Fields, Hint, QueryBoundary, RawDialog, Section, StatusBadge, TimeText, Button, type Column, type Segment } from "@/components/kit"
import { executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { useChillQuery } from "@/lib/client"
import { formatNumber, formatWatts, msNumber, wattsNumber } from "@/lib/format"
import { measurementQuality, measurementSchedulerUsed } from "@/lib/measurement-assessment"
import { parseMeasurementJSON } from "@/lib/measurement-summary"
import type { ProfilesView, ResultRow } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { coverageReason, measurementStatus } from "@/lib/status-labels"
import { filterChoice, useUrlState } from "./results-shared"

type Profile = NonNullable<ProfilesView["profiles"]>[number]
const LIMIT = 25
const FILTERS = ["model", "node", "status"] as const

const representative = (profile: Profile): ResultRow | undefined => profile.resultRows?.find(row => row.rowKind === "direct") ?? profile.resultRows?.[0]

function Usage({ profile }: { profile: Profile }) {
  const used = measurementSchedulerUsed(profile.assessment)
  if (used == null) return null
  return <StatusBadge tone={used ? "ok" : "idle"} label={used ? "사용 중" : "미사용"} />
}
function Quality({ profile }: { profile: Profile }) {
  const quality = measurementQuality([profile.assessment])
  return <StatusBadge tone={quality.state === "pass" ? "ok" : quality.state === "fail" ? "fail" : "idle"} label={quality.label} />
}

const columns: Column<Profile>[] = [
  { width: "20%", key: "model", header: "모델", cell: p => p.condition.logicalModel },
  { width: "28%", key: "condition", header: "장치와 조건", cell: p => `${p.nodeName} / ${powerModeLabel(p.condition.powerMode)} / ${executionPathLabel(p.condition.artifact.executionPath)}` },
  { width: "9%", key: "batch", header: "대표 배치", unit: "건", numeric: true, cell: p => representative(p)?.batch, missingReason: "배치 결과 없음" },
  { width: "9%", key: "latency", header: "지연 p95", unit: "ms", numeric: true, cell: p => { const v = representative(p)?.p95Milliseconds; return v == null ? null : msNumber(v) }, missingReason: "지연 기록 없음" },
  { width: "12%", key: "energy", header: "건당 에너지", unit: "J", numeric: true, cell: p => { const v = representative(p)?.incrementalJoulesPerItem; return v == null ? null : formatNumber(v, 3) }, missingReason: "에너지 기록 없음" },
  { width: "10%", key: "use", header: "사용 여부", cell: p => <Usage profile={p} />, missingReason: "사용 여부 미확인" },
  { key: "quality", header: "품질", cell: p => <Quality profile={p} /> },
]

const batchTable = (title: string, tail: Column<ResultRow>[]) => ({ title, columns: [{ key: "batch", header: "배치", unit: "건", numeric: true, cell: (r: ResultRow) => r.batch }, ...tail] })
const batchTables = [
  batchTable("지연", [
    { key: "latency", header: "지연 p95", unit: "ms", numeric: true, cell: r => r.p95Milliseconds == null ? null : msNumber(r.p95Milliseconds) },
    { key: "samples", header: "표본", unit: "건", numeric: true, cell: r => r.latencySampleCount == null ? null : formatNumber(r.latencySampleCount) },
  ]),
  batchTable("에너지", [
    { key: "total", header: "총", unit: "J/건", numeric: true, cell: r => r.totalJoulesPerItem == null ? null : formatNumber(r.totalJoulesPerItem, 3) },
    { key: "extra", header: "추가", unit: "J/건", numeric: true, cell: r => r.incrementalJoulesPerItem == null ? null : formatNumber(r.incrementalJoulesPerItem, 3) },
  ]),
  batchTable("전력과 오차", [
    { key: "active", header: "실행 전력", unit: "W", numeric: true, cell: r => r.activeWatts == null ? null : wattsNumber(r.activeWatts) },
    { key: "ci", header: "신뢰 구간 ±", unit: "J/건", numeric: true, cell: r => r.confidence ? formatNumber(r.confidence.halfWidth, 3) : null, missingReason: "신뢰 구간 미제공" },
  ]),
]

export function MeasurementThroughputResults({ lead }: { lead: Segment }) {
  const { search, set } = useUrlState()
  const offset = Math.max(0, Number(search.get("offset")) || 0)
  const selectedId = search.get("profile")
  const query = new URLSearchParams({ view: "summary", summary: "v2", limit: String(LIMIT), offset: String(offset) })
  for (const key of FILTERS) { const value = search.get(key); if (value) query.set(key, value) }
  const list = useChillQuery<ProfilesView>(`v1/scheduler/profiles?${query}`, { decoder: parseMeasurementJSON })
  const inPage = list.data?.profiles?.find(profile => profile.id === selectedId)
  // A reloaded link can point at a profile outside the current page: read it alone.
  const single = useChillQuery<ProfilesView>(`v1/scheduler/profiles?${new URLSearchParams({ profile: selectedId ?? "", limit: "1" })}`, { enabled: !!selectedId && !inPage && !list.isPending, decoder: parseSchedulerJSON })
  const selected = inPage ?? single.data?.profiles?.find(profile => profile.id === selectedId)
  const [raw, setRaw] = React.useState(false)
  const filters = list.data?.filters
  const total = list.data?.page?.total
  // A link past the last page (filters changed server-side) returns to the first page.
  React.useEffect(() => { if (total != null && offset > 0 && offset >= total) set({ offset: null }) }, [total, offset, set])
    return (
    <>
      <FilterBar
        segments={[lead]}
        selects={[
          filterChoice("모델", search.get("model"), filters?.models ?? [], value => set({ model: value, offset: null, profile: null })),
          filterChoice("장치", search.get("node"), filters?.nodes ?? [], value => set({ node: value, offset: null, profile: null })),
          filterChoice("상태", search.get("status"), filters?.statuses ?? [], value => set({ status: value, offset: null, profile: null }), code => measurementStatus(code).label),
        ]}
      />
      <QueryBoundary query={list} isEmpty={data => !data.profiles?.length && offset === 0}
        empty={FILTERS.some(key => search.get(key))
          ? <EmptyState title="조건에 맞는 결과 없음" action={<Button variant="outline" onClick={() => set({ model: null, node: null, status: null, profile: null })}>필터 해제</Button>} />
          : <EmptyState title="처리 성능 결과 없음" />}>
        {data => (
          <DataTable
            label="처리 성능"
            rows={data.profiles ?? []}
            rowKey={p => p.id}
            columns={columns}
            onRowClick={p => set({ profile: p.id })}
            selectedKey={selectedId}
            busy={list.isFetching}
            paging={{ mode: "offset", offset, limit: LIMIT, total: data.page?.total ?? null, onOffsetChange: next => set({ offset: next ? String(next) : null }) }}
            download={{ filename: "throughput" }}
          />
        )}
      </QueryBoundary>
      <DetailDrawer
        open={!!selectedId}
        onOpenChange={open => { if (!open) set({ profile: null }) }}
        title={selected?.condition.logicalModel ?? "처리 성능"}
        status={selected ? measurementStatus(selected.assessment?.displayStatus) : undefined}
        actions={selected && <Button size="sm" variant="ghost" onClick={() => setRaw(true)}>원본 보기</Button>}
      >
        {inPage
          ? <ProfileBody profile={inPage} raw={raw} setRaw={setRaw} />
          : (
            <QueryBoundary query={single} skeleton="block" isEmpty={data => !data.profiles?.length} empty={<EmptyState compact title="목록에 없음" />}>
              {data => <ProfileBody profile={data.profiles![0]} raw={raw} setRaw={setRaw} />}
            </QueryBoundary>
          )}
      </DetailDrawer>
    </>
  )
}

function ProfileBody({ profile, raw, setRaw }: { profile: Profile; raw: boolean; setRaw: (open: boolean) => void }) {
  const reasons = [...new Set((profile.assessment?.reasons ?? []).map(coverageReason))]
  return (
    <>
      <Fields columns={2} items={[
        { label: "사용 여부", value: measurementSchedulerUsed(profile.assessment) == null ? null : <Usage profile={profile} />, reason: "확인 불가", info: "스케줄러가 지금 이 측정을 쓰는지" },
        { label: "품질", value: <Quality profile={profile} /> },
      ]} />
      {reasons.length > 0 && <Fields columns={1} items={[{ label: "사유", value: reasons.join(", ") }]} />}
      <Fields columns={2} items={[
        { label: "장치", value: profile.nodeName },
        { label: "전력 모드", value: powerModeLabel(profile.condition.powerMode) },
        { label: "실행 경로", value: <Hint text={profile.condition.artifact.executionPath}>{executionPathLabel(profile.condition.artifact.executionPath)}</Hint> },
        { label: "전처리기", value: profile.condition.preprocessor.name, reason: "전처리기 없음" },
        { label: "데이터셋", value: profile.condition.activeProtocol.dataset?.name, reason: "기록 없음" },
        { label: "유휴 전력", value: profile.idleBaselineWatts == null ? null : formatWatts(profile.idleBaselineWatts) },
        { label: "측정일", value: <TimeText at={profile.measuredAt} mode="detail" /> },
      ]} />
      {batchTables.map(({ title, columns }) => (
        <Section key={title} title={title}>
          <DataTable label={title} density="compact" rows={[...(profile.resultRows ?? [])].sort((a, b) => a.batch - b.batch)} rowKey={r => r.id} columns={columns} pageSize={0} empty={<EmptyState compact title="배치 결과 없음" />} />
        </Section>
      ))}
      <RawDialog open={raw} onOpenChange={setRaw} value={profile} filename="profile.json" title="처리 성능 원본" />
    </>
  )
}
