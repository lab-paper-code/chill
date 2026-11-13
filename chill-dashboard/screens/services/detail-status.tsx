"use client"

import * as React from "react"
import { Button, DataTable, EmptyState, HeadroomBar, QueryBoundary, Section, StatRow, StatTile, StatusBadge, StatusDot, TimeSeries, type Column, type SeriesPoint } from "@/components/kit"
import { SkeletonBlock } from "@/components/custom/blocks/SkeletonBlock"
import { useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import type { WorkloadView } from "@/lib/domain"
import { formatNumber } from "@/lib/format"
import type { LiveInstance, LiveSnapshot } from "@/lib/live"
import { belongsToPool, minuteMetrics, sloLatency, workloadQueue, routingShare } from "@/lib/live-display"
import type { SchedulerSettings } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { sloVerdict, type StatusInfo } from "@/lib/status-labels"
import { useLive } from "@/lib/use-live"
import { RoutingDialog, type RoutingTarget } from "./routing-dialog"

export type ServiceTabProps = { name: string; uid: string | undefined; workload: WorkloadView | undefined }

const WINDOW_MS = 300_000

type PlacementRow = RoutingTarget & { nodeName: string; powerMode: string; share: number | undefined; running: number; waiting: number; status: StatusInfo }

// Instance role for this service, from the live snapshot flags.
function instanceStatus(instance: LiveInstance, uid: string): StatusInfo {
  if (instance.routable[uid] === true) return { label: "분배 중", tone: "ok" }
  if ((instance.running[uid] ?? 0) > 0) return { label: "정리 중", tone: "warn" }
  return instance.ready ? { label: "미분배", tone: "idle" } : { label: "준비 중", tone: "progress" }
}

/** 현황: live tiles, the last five minutes since connect, and where the service runs. */
export function ServiceStatusTab({ name, uid, workload }: ServiceTabProps) {
  const { state, reconnect } = useLive()
  const blocked = state.status === "unauthorized" || state.status === "forbidden"
  const query = {
    data: state.snapshot ?? undefined,
    error: blocked ? new ApiError(state.status === "unauthorized" ? 401 : 403, "", null) : state.status === "protocol-error" ? new ApiError(0, "", { code: "LiveProtocolError" }) : null,
    isPending: !state.snapshot,
    refetch: reconnect,
  }
  const serviceUID = workload?.desired.metadata.uid ?? uid
  return (
    <QueryBoundary query={query} skeleton="tiles">
      {snapshot => {
        const live = snapshot.workloads.find(row => row.uid === serviceUID)
        return live ? <LiveStatus name={name} snapshot={snapshot} live={live} locked={!!uid && uid !== live.uid} /> : <EmptyState title="실시간 관측 없음" />
      }}
    </QueryBoundary>
  )
}

function LiveStatus({ name, snapshot, live, locked }: { name: string; snapshot: LiveSnapshot; live: LiveSnapshot["workloads"][number]; locked: boolean }) {
  const { state } = useLive()
  const settings = useChillQuery<SchedulerSettings>("v1/scheduler/settings", { decoder: parseSchedulerJSON })
  const [editing, setEditing] = React.useState(false)
  const uid = live.uid
  const frames = state.metrics
  const end = frames.length ? Math.floor(Date.parse(frames[frames.length - 1].bucketAt) / 1000) * 1000 : 0
  const latest = frames.at(-1)?.workloads[uid]
  const recent = end ? minuteMetrics(frames, uid, end + 1000) : []
  const completed = recent.reduce((sum, row) => sum + row.completed, 0)
  const failed = recent.reduce((sum, row) => sum + row.failed, 0)
  const slo = latest?.slo
  const percentile = slo?.percentile ?? live.slo.percentile
  const targetMs = slo?.targetMs ?? live.slo.targetLatencyNanos / 1e6

  const instances = snapshot.instances.filter(row => belongsToPool(row, uid) && !row.stopped)
    .sort((a, b) => a.node.name.localeCompare(b.node.name) || a.id.localeCompare(b.id))
  const weights = snapshot.routing[uid]
  const rows: PlacementRow[] = instances.map(instance => {
    const same = instances.filter(row => row.node.uid === instance.node.uid)
    return {
      id: instance.id,
      device: same.length > 1 ? `${instance.node.name} #${same.indexOf(instance) + 1}` : instance.node.name,
      nodeName: instance.node.name,
      weight: weights?.[instance.id],
      powerMode: snapshot.nodes.find(node => node.uid === instance.node.uid)?.powerMode ?? "",
      share: routingShare(weights, instance.id),
      running: instance.running[uid] ?? 0,
      waiting: instance.waiting[uid] ?? 0,
      status: instanceStatus(instance, uid),
    }
  })
  const running = rows.reduce((sum, row) => sum + row.running, 0)
  const declared = settings.data?.staticRouting?.[uid]

  // One point per second over the last five minutes; seconds without a bucket stay null (a gap).
  const buckets = new Map(frames.map(frame => [Math.floor(Date.parse(frame.bucketAt) / 1000) * 1000, frame.workloads[uid]]))
  const points: SeriesPoint[] = []
  for (let t = end - WINDOW_MS + 1000; end && t <= end; t += 1000) {
    const metric = buckets.get(t)
    const latency = metric ? sloLatency(live, metric) : null
    points.push({ t, received: metric ? metric.received : null, latency: latency === undefined ? metric?.p95ms ?? null : latency })
  }
  const shown = [50, 90, 95, 99].includes(percentile) ? percentile : 95

  const columns: Column<PlacementRow>[] = [
    { key: "device", header: "장치", cell: row => row.device, sortValue: row => row.device },
    { key: "mode", header: "전력 모드", cell: row => row.powerMode || null, missingReason: "관측 없음" },
    { key: "share", header: "분배 비율", unit: "%", numeric: true, cell: row => row.share == null ? null : formatNumber(row.share, 1), missingReason: "분배 없음" },
    { key: "running", header: "처리 중", numeric: true, cell: row => formatNumber(row.running) },
    { key: "waiting", header: "대기", numeric: true, cell: row => formatNumber(row.waiting) },
    { key: "status", header: "상태", cell: row => <StatusDot status={row.status} showLabel /> },
  ]
  const placementActions = <>
    {declared && <StatusBadge status={Object.keys(declared).length ? { label: "직접 지정", tone: "progress" } : { label: "새 요청 차단", tone: "warn" }} />}
    <Button size="sm" variant="outline" disabled={locked || (!rows.length && !declared)} onClick={() => setEditing(true)}>분배 수정</Button>
  </>

  return (
    <div className="flex flex-col gap-6">
      <StatRow className="grid-cols-2 xl:grid-cols-5">
        <StatTile label="유입" value={latest ? formatNumber(latest.received) : null} unit="req/s" reason="관측 없음" />
        <StatTile
          label="지연" unit="ms" info={slo ? `최근 ${slo.windowSeconds}초 완료 요청의 p${percentile}` : `완료 요청의 p${percentile}`}
          value={slo?.quantileMs != null ? formatNumber(slo.quantileMs, slo.quantileMs < 100 ? 1 : 0) : null}
          reason={!slo ? "관측 없음" : slo.samples === 0 ? "표본 없음" : "창이 아직 차지 않음"}
          status={slo?.met != null ? sloVerdict(slo.met) : undefined}
          sub={<span className="inline-flex items-center gap-2">{slo?.quantileMs != null && <HeadroomBar observed={slo.quantileMs} target={slo.targetMs} />}목표 {formatNumber(targetMs)} ms</span>}
        />
        <StatTile
          label="성공률" unit="%" info="최근 60초 완료와 실패 기준"
          value={completed + failed ? formatNumber(completed / (completed + failed) * 100, 1) : null} reason="완료 요청 없음"
          sub={completed + failed ? `${formatNumber(completed)} / ${formatNumber(completed + failed)}` : undefined}
        />
        <StatTile label="실패" unit="건" info="최근 60초" value={recent.length ? formatNumber(failed) : null} reason="관측 없음" />
        <StatTile label="대기" unit="건" value={formatNumber(workloadQueue(live, instances))} sub={`처리 중 ${formatNumber(running)}`} className="col-span-2 xl:col-span-1" />
      </StatRow>

      {!end ? <SkeletonBlock animation="pulse" height={250} radius={10} aria-hidden="true" /> : points.some(point => (point.received ?? 0) > 0) ? (
        <div className="grid gap-3 lg:grid-cols-2">
          <Section title="유입 추이" card>
            <TimeSeries data={points} series={[{ key: "received", label: "유입" }]} unit="req/s" digits={0} height={180} domain={[end - WINDOW_MS, end]} label="최근 5분 초당 유입" />
          </Section>
          <Section title={`지연 추이 (p${shown})`} card>
            <TimeSeries data={points} series={[{ key: "latency", label: `p${shown}`, color: 2 }]} unit="ms" height={180} domain={[end - WINDOW_MS, end]} reference={{ value: targetMs, label: "목표" }} label={`최근 5분 초당 p${shown} 지연`} />
          </Section>
        </div>
      ) : (
        <Section title="추이" card><EmptyState compact title="최근 5분 요청 없음" /></Section>
      )}

      {rows.length ? (
        <DataTable label="배치" title="배치" rows={rows} rowKey={row => row.id} columns={columns} pageSize={0} rowHref={row => `/devices/${encodeURIComponent(row.nodeName)}`} actions={placementActions} />
      ) : (
        <Section title="배치" card actions={placementActions}><EmptyState compact title="배치 없음" /></Section>
      )}
      <RoutingDialog open={editing} onOpenChange={setEditing} name={name} uid={uid} targets={rows} declared={declared} />
    </div>
  )
}
