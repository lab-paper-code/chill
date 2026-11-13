"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { RefreshCw } from "lucide-react"
import {
  DataTable, EmptyState, Feed, FilterBar, HeadroomBar, Hint, Page, PageHeader, QueryBoundary, Section, Sparkline, StatRow, StatTile, StatusDot, TimeText,
  Button, buttonClassName, useNow, type BoundaryQuery, type Column,
} from "@/components/kit"
import { useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { useLive } from "@/lib/use-live"
import type { LiveSnapshot } from "@/lib/live"
import type { ActiveEvaluations } from "@/lib/evaluation-active"
import type { SchedulerObservationSummaryPage, SchedulerStateSummary } from "@/lib/read-types"
import { autonomyEnabled, decisionStatus } from "@/lib/status-labels"
import { formatNumber } from "@/lib/format"
import { autonomyCell, changeItems, overviewRows, progressItems, serviceStates, TREND_SECONDS, type OverviewRow, type ServiceState } from "@/lib/overview"

const STATES = Object.keys(serviceStates) as ServiceState[]
const DEFAULT_STATES: ServiceState[] = ["attention", "active"]

/** `?status=attention,active,idle`; the default pair is not written. */
function useStateFilter(): [ServiceState[], (next: string[]) => void] {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const parsed = (search.get("status") ?? "").split(",").filter((value): value is ServiceState => (STATES as string[]).includes(value))
  const value = parsed.length ? parsed : DEFAULT_STATES
  const set = (next: string[]) => {
    if (!next.length) return
    const params = new URLSearchParams(search.toString())
    const ordered = STATES.filter(state => next.includes(state))
    if (ordered.join() === DEFAULT_STATES.join()) params.delete("status")
    else params.set("status", ordered.join(","))
    const query = params.toString()
    router.replace(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }
  return [value, set]
}

/** Keeps the last good page while a moved time window refetches, so the list does not blink each minute. */
function useKept<T>(data: T | undefined): T | undefined {
  const [kept, setKept] = React.useState(data)
  if (data !== undefined && data !== kept) setKept(data)
  return data ?? kept
}

export function OverviewScreen() {
  const live = useLive()
  const { state } = live
  const summary = useChillQuery<SchedulerStateSummary>("v1/scheduler/state?view=summary", { poll: true })
  const evaluations = useChillQuery<ActiveEvaluations>("v1/evaluations/active", { poll: true })
  const now = useNow()
  const minute = Math.floor(now / 60_000) * 60_000
  const settingsQuery = useChillQuery<SchedulerObservationSummaryPage>(
    `v1/observations/scheduler?${new URLSearchParams({ filter: "settings", view: "summary", from: new Date(minute - 3_600_000).toISOString(), to: new Date(minute).toISOString() })}`,
    { poll: true, enabled: now > 0 },
  )
  const settings = useKept(settingsQuery.data)
  const [filter, setFilter] = useStateFilter()

  const snapshot = state.snapshot
  const rows = React.useMemo(() => (snapshot ? overviewRows(snapshot, state.metrics) : []), [snapshot, state.metrics])
  const latest = state.metrics.at(-1)
  const autonomy = new Map((summary.data?.decision.workloads ?? []).map(row => [row.uid, row.state]))

  const liveQuery: BoundaryQuery<LiveSnapshot> = {
    data: snapshot ?? undefined,
    error: state.status === "unauthorized" ? new ApiError(401, "", null) : state.status === "forbidden" ? new ApiError(403, "", null)
      : state.status === "protocol-error" ? new ApiError(0, "", { code: "실시간 형식 오류" }) : null,
    isPending: !snapshot,
    refetch: live.reconnect,
  }

  const counts = Object.fromEntries(STATES.map(key => [key, rows.filter(row => row.state === key).length])) as Record<ServiceState, number>
  const visible = rows.filter(row => filter.includes(row.state))
  const judged = rows.filter(row => row.slo?.met != null)
  const missed = judged.filter(row => row.slo?.met === false).length
  const inflow = latest ? rows.reduce((sum, row) => sum + (row.received ?? 0), 0) : null
  const readyNodes = snapshot?.nodes.filter(node => node.ready).length
  const decision = snapshot?.decision
  const controlled = summary.data?.decision.workloads?.filter(row => row.state === "Controlled").length

  const columns: Column<OverviewRow>[] = [
    { key: "name", header: "서비스", width: "14rem", cell: row => row.name, sortValue: row => row.name },
    {
      key: "state", header: "상태", width: "7.5rem", csv: row => row.label,
      cell: row => <Hint text={row.reason ?? undefined} focusable={!!row.reason}><StatusDot tone={row.tone} label={row.label} showLabel /></Hint>,
    },
    {
      key: "received", header: "유입", unit: "req/s", numeric: true, width: "7.5rem", missingReason: "관측 없음", csv: row => row.received,
      cell: row => row.received == null ? null : (
        <span className="inline-flex items-center gap-2">
          <Sparkline values={row.trend} width={48} height={18} label={`최근 ${TREND_SECONDS}초 유입`} />
          <span className="min-w-8">{formatNumber(row.received)}</span>
        </span>
      ),
    },
    {
      key: "latency", header: "지연", unit: "ms", numeric: true, width: "8.5rem", csv: row => row.slo?.quantileMs,
      missing: row => !latest?.workloads[row.uid] ? "관측 없음" : !row.slo ? "서버 판정 없음" : row.slo.samples === 0 ? "표본 없음" : "표본 부족",
      cell: row => {
        const slo = row.slo
        if (slo?.quantileMs == null) return null
        return (
          <Hint text={`p${slo.percentile}, 목표 ${formatNumber(slo.targetMs)} ms, 표본 ${formatNumber(slo.samples)}`}>
            <span className="inline-flex items-center gap-2">
              <HeadroomBar observed={slo.quantileMs} target={slo.targetMs} />
              <span className="min-w-10">{formatNumber(slo.quantileMs, slo.quantileMs < 100 ? 1 : 0)}</span>
            </span>
          </Hint>
        )
      },
    },
    { key: "success", header: "성공률", unit: "%", numeric: true, width: "5.5rem", missingReason: `최근 ${TREND_SECONDS}초 끝난 요청 없음`, cell: row => row.success == null ? null : formatNumber(row.success * 100, 1) },
    {
      key: "devices", header: "배치", missingReason: "배치 없음",
      cell: row => row.devices.length === 0 ? null : row.devices.length <= 2 ? row.devices.join(", ") : `${row.devices.slice(0, 2).join(", ")} +${row.devices.length - 2}`,
    },
    {
      key: "autonomy", header: "자동 제어", width: "8rem",
      cell: row => {
        const scope = autonomy.get(row.uid)
        const autonomous = snapshot?.workloads.find(item => item.uid === row.uid)?.autonomous
        return <StatusDot status={scope ? autonomyCell(scope) : autonomyEnabled(autonomous)} showLabel />
      },
    },
  ]

  const progress = snapshot ? progressItems(snapshot, evaluations.data?.items ?? []) : []
  const changes = changeItems(state.events, settings?.events ?? [])
  const attention = counts.attention

  return (
    <Page>
      <PageHeader title="개요" />
      <StatRow>
        <StatTile
          label="목표 충족" href="/services" value={judged.length ? `${judged.length - missed}/${judged.length}` : null}
          reason={latest ? "판정된 서비스 없음" : "관측 대기"} info="서버가 지연을 판정한 서비스 중 목표를 지킨 수"
          status={snapshot ? { label: attention ? `확인 필요 ${attention}` : "정상", tone: missed ? "fail" : attention ? "warn" : "ok" } : undefined}
          sub={snapshot ? (attention ? `확인 필요 ${attention}` : "확인 필요 없음") : undefined}
        />
        <StatTile label="유입" value={inflow == null ? null : formatNumber(inflow)} unit="req/s" reason="관측 대기" sub={snapshot ? `가동 서비스 ${counts.attention + counts.active}` : undefined} />
        <StatTile
          label="전력" href="/devices" value={latest?.pool.activeWatts == null ? null : formatNumber(latest.pool.activeWatts)} unit="W"
          reason={latest ? "전력 관측 없음" : "관측 대기"} info="서비스를 실행 중인 장치의 전력"
          sub={snapshot ? `전체 ${latest?.pool.wholeWatts == null ? "—" : formatNumber(latest.pool.wholeWatts)} W, 준비 장치 ${readyNodes}/${snapshot.nodes.length}` : undefined}
        />
        <StatTile
          label="스케줄러" href={decision ? `/scheduler/decisions?${new URLSearchParams({ at: decision.at })}` : "/scheduler/decisions"}
          value={decision ? decisionStatus(decision.state).label : null} reason={snapshot ? "판단 기록 없음" : "관측 대기"}
          sub={decision || controlled != null ? <>{decision && <><TimeText at={decision.at} />, </>}자동 제어 {controlled ?? "—"}</> : undefined}
        />
      </StatRow>

      <div className="grid items-start gap-6 2xl:grid-cols-[minmax(0,1fr)_20rem]">
        <QueryBoundary
          query={liveQuery} isEmpty={data => data.workloads.length === 0}
          empty={<EmptyState title="등록된 서비스 없음" action={<Link href="/services/new" className={buttonClassName({ variant: "default", size: "sm" })}>서비스 등록</Link>} />}
        >
          {() => (
            <DataTable
              label="서비스" title="서비스" rows={visible} rowKey={row => row.uid} columns={columns} pageSize={0}
              rowHref={row => `/services/${encodeURIComponent(row.name)}?${new URLSearchParams({ uid: row.uid })}`}
              empty={<EmptyState compact title="해당 서비스 없음" action={!filter.includes("idle") && counts.idle > 0 ? <Button size="sm" variant="outline" onClick={() => setFilter([...filter, "idle"])}>유휴 {counts.idle} 보기</Button> : undefined} />}
              toolbar={<FilterBar segments={[{ label: "상태", multiple: true, value: filter, onChange: setFilter, options: STATES.map(key => ({ value: key, label: serviceStates[key], count: counts[key] })) }]} />}
            />
          )}
        </QueryBoundary>

        <aside className="grid min-w-0 items-start gap-6 md:grid-cols-2 2xl:grid-cols-1" aria-label="진행과 변경">
          <Section title="진행 중" card>
            <Feed label="진행 중" items={progress} empty={snapshot ? "진행 중인 작업 없음" : "관측 대기"} />
            <QueryLine query={evaluations} label="실험" />
          </Section>
          <Section
            title="최근 변경" card
            actions={changes.cluster > 0 ? <Link href="/scheduler/actions?kind=events" className={buttonClassName({ variant: "link", size: "sm", className: "h-auto px-0 text-xs text-muted-foreground" })}>클러스터 사건 {changes.cluster}건</Link> : undefined}
          >
            <Feed label="최근 변경" items={changes.items} empty="최근 변경 없음" />
            <QueryLine query={settingsQuery} label="설정 변경" kept={settings !== undefined} />
          </Section>
        </aside>
      </div>
    </Page>
  )
}

/** A failed side-list source as one line with retry; nothing while it loads or succeeds. */
function QueryLine({ query, label, kept = false }: { query: { error: ApiError | null; refetch: () => unknown }; label: string; kept?: boolean }) {
  if (!query.error || kept) return null
  const text = [401, 403].includes(query.error.status) ? `${label} 권한 없음` : `${label} 조회 실패`
  return (
    <div role="alert" className="mt-2 flex items-center gap-2 border-t border-border pt-2 text-sm">
      <StatusDot tone="fail" label={text} showLabel />
      <Button size="sm" variant="ghost" className="ml-auto" iconLeft={<RefreshCw />} onClick={() => void query.refetch()}>다시 시도</Button>
    </div>
  )
}
