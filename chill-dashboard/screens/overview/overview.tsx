"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { RefreshCw } from "lucide-react"
import {
  DataTable, EmptyState, HeadroomBar, Hint, Missing, Page, PageHeader, QueryBoundary, Section, Sparkline, StatRow, StatTile, StatusDot,
  Button, buttonClassName, useNow, type BoundaryQuery, type Column,
} from "@/components/kit"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { useLive } from "@/lib/use-live"
import type { LiveSnapshot } from "@/lib/live"
import type { ActiveEvaluations } from "@/lib/evaluation-active"
import type { SchedulerObservationSummaryPage, SchedulerStateSummary } from "@/lib/read-types"
import { autonomyState, decisionStatus } from "@/lib/status-labels"
import { formatKst, formatNumber, formatRelative } from "@/lib/format"
import { changeItems, overviewRows, progressItems, serviceStates, TREND_SECONDS, type FeedItem, type OverviewRow, type ServiceState } from "@/lib/overview"

const STATES = Object.keys(serviceStates) as ServiceState[]
const DEFAULT_STATES: ServiceState[] = ["attention", "active"]

/** `?status=attention,active,idle`; the default pair is not written. */
function useStateFilter(): [ServiceState[], (next: ServiceState[]) => void] {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const parsed = (search.get("status") ?? "").split(",").filter((value): value is ServiceState => (STATES as string[]).includes(value))
  const value = parsed.length ? parsed : DEFAULT_STATES
  const set = (next: ServiceState[]) => {
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
  const now = useNow()
  const summary = useChillQuery<SchedulerStateSummary>("v1/scheduler/state?view=summary", { poll: true })
  const evaluations = useChillQuery<ActiveEvaluations>("v1/evaluations/active", { poll: true })
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
  const autonomy = new Map((summary.data?.decision.workloads ?? []).map(row => [row.uid, row]))

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
  const inflow = latest ? rows.reduce((sum, row) => sum + (row.received ?? 0), 0) : null
  const readyNodes = snapshot?.nodes.filter(node => node.ready).length
  const decision = snapshot?.decision
  const decisionInfo = decisionStatus(decision?.state)
  const controlled = summary.data?.decision.workloads?.filter(row => row.state === "Controlled").length

  const columns: Column<OverviewRow>[] = [
    { key: "name", header: "서비스", cell: row => row.name, sortValue: row => row.name },
    {
      key: "state", header: "상태", width: "6rem",
      cell: row => <Hint text={row.reason ?? undefined}><StatusDot tone={row.tone} label={serviceStates[row.state]} showLabel /></Hint>,
    },
    {
      key: "received", header: "유입", unit: "req/s", numeric: true, width: "7.5rem", missingReason: "관측 없음",
      cell: row => row.received == null ? null : (
        <span className="inline-flex items-center gap-2">
          <Sparkline values={row.trend} width={48} height={18} label={`최근 ${TREND_SECONDS}초 유입`} />
          <span className="min-w-8">{formatNumber(row.received)}</span>
        </span>
      ),
    },
    {
      key: "latency", header: "지연", unit: "ms", numeric: true, width: "8.5rem",
      cell: row => {
        const slo = row.slo
        if (slo?.quantileMs == null) return <Missing reason={!latest?.workloads[row.uid] ? "관측 없음" : !slo ? "서버 판정 없음" : slo.samples === 0 ? "표본 없음" : "창이 아직 차지 않음"} />
        return (
          <Hint text={`p${slo.percentile} 목표 ${formatNumber(slo.targetMs, 0)} ms, 표본 ${formatNumber(slo.samples)}`}>
            <span className="inline-flex items-center gap-2">
              <HeadroomBar observed={slo.quantileMs} target={slo.targetMs} />
              <span className="min-w-10">{formatNumber(slo.quantileMs, slo.quantileMs < 100 ? 1 : 0)}</span>
            </span>
          </Hint>
        )
      },
    },
    { key: "success", header: "성공률", unit: "%", numeric: true, width: "5.5rem", missingReason: `최근 ${TREND_SECONDS}초 종료된 요청 없음`, cell: row => row.success == null ? null : formatNumber(row.success * 100, 1) },
    {
      key: "devices", header: "배치", missingReason: "배치 없음",
      cell: row => row.devices.length === 0 ? null : row.devices.length <= 2 ? row.devices.join(", ") : `${row.devices.slice(0, 2).join(", ")} +${row.devices.length - 2}`,
    },
    {
      key: "autonomy", header: "자동 제어", width: "8.5rem",
      missingReason: summary.error ? "조회 실패" : summary.data ? "판단 기록 없음" : "조회 중",
      cell: row => {
        const scope = autonomy.get(row.uid)
        return scope ? <StatusDot status={autonomyState(scope.state)} showLabel /> : null
      },
    },
  ]

  const progress = snapshot ? progressItems(snapshot, evaluations.data?.items ?? []) : []
  const changes = changeItems(state.events, settings?.events ?? [])

  return (
    <Page>
      <PageHeader title="개요" />
      <StatRow>
        <StatTile
          label="서비스" href="/services" value={judged.length ? `${judged.filter(row => row.slo?.met).length}/${judged.length}` : null}
          reason={latest ? "판정된 서비스 없음" : "관측 대기"} info="목표 지연을 충족한 서비스 수와 판정된 서비스 수"
          sub={snapshot ? `확인 필요 ${counts.attention}` : undefined}
        />
        <StatTile label="유입" value={inflow == null ? null : formatNumber(inflow)} unit="req/s" reason="관측 대기" sub={snapshot ? `가동 서비스 ${counts.attention + counts.active}` : undefined} />
        <StatTile
          label="전력" href="/devices" value={latest?.pool.activeWatts == null ? null : formatNumber(latest.pool.activeWatts)} unit="W"
          reason={latest ? "전력 관측 없음" : "관측 대기"} info="서비스를 실행 중인 장치의 전력"
          sub={snapshot ? `전체 ${latest?.pool.wholeWatts == null ? "—" : formatNumber(latest.pool.wholeWatts)} W, 준비 장치 ${readyNodes}/${snapshot.nodes.length}` : undefined}
        />
        <StatTile
          label="스케줄러" href={decision ? `/scheduler/decisions?${new URLSearchParams({ at: decision.at })}` : "/scheduler/decisions"}
          value={decision ? decisionInfo.label : null} reason={snapshot ? "판단 기록 없음" : "관측 대기"}
          sub={decision || controlled != null ? `${decision && now ? `${formatRelative(decision.at, now)}, ` : ""}자동 제어 ${controlled ?? "—"}` : undefined}
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
              toolbar={
                <ToggleGroup type="multiple" variant="segmented" size="sm" aria-label="상태" value={filter} onValueChange={next => setFilter(next as ServiceState[])}>
                  {STATES.map(key => (
                    <ToggleGroupItem key={key} value={key}>
                      {serviceStates[key]}<span className="ml-1 tabular-nums text-muted-foreground">{counts[key]}</span>
                    </ToggleGroupItem>
                  ))}
                </ToggleGroup>
              }
            />
          )}
        </QueryBoundary>

        <aside className="grid min-w-0 items-start gap-6 md:grid-cols-2 2xl:grid-cols-1" aria-label="진행과 변경">
          <Section title="진행 중" card>
            {!snapshot ? <Feed items={[]} empty="관측 대기" /> : <Feed items={progress} empty="진행 중인 작업 없음" now={now} />}
            <QueryLine query={evaluations} label="실험" />
          </Section>
          <Section
            title="최근 변경" card
            actions={changes.cluster > 0 ? <Link href="/scheduler/actions?kind=events" className="text-xs text-muted-foreground hover:text-foreground hover:underline">클러스터 사건 {changes.cluster}건</Link> : undefined}
          >
            <Feed items={changes.items} empty="최근 변경 없음" now={now} />
            <QueryLine query={settingsQuery} label="설정 변경" kept={settings !== undefined} />
          </Section>
        </aside>
      </div>
    </Page>
  )
}

/** Linked one-line items of the side column. */
function Feed({ items, empty, now = 0 }: { items: FeedItem[]; empty: string; now?: number }) {
  if (!items.length) return <p className="py-2 text-sm text-muted-foreground">{empty}</p>
  return (
    <ul className="-mx-2 flex flex-col">
      {items.map(item => (
        <li key={item.key}>
          <Link href={item.href} className="flex h-9 min-w-0 items-center gap-2 rounded-md px-2 text-sm hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring">
            <StatusDot tone={item.tone} label="" />
            <span className="shrink-0 font-medium text-foreground">{item.label}</span>
            {item.target && <span className="min-w-0 truncate text-muted-foreground">{item.target}</span>}
            <span className="ml-auto shrink-0 pl-2 text-xs tabular-nums text-muted-foreground">
              {item.meta ?? (item.at && now ? <Hint text={`${formatKst(item.at)} KST`}><time dateTime={item.at}>{formatRelative(item.at, now)}</time></Hint> : null)}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

/** A failed side-list source as one line with retry; nothing while it loads or succeeds. */
function QueryLine({ query, label, kept = false }: { query: { error: ApiError | null; refetch: () => unknown }; label: string; kept?: boolean }) {
  if (!query.error || kept) return null
  const text = [401, 403].includes(query.error.status) ? `${label} 권한 없음` : `${label} 조회 실패`
  return (
    <div role="alert" className="mt-2 flex items-center gap-2 border-t border-border pt-2 text-sm">
      <StatusDot tone="fail" label={text} showLabel />
      <button type="button" onClick={() => void query.refetch()} className="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <RefreshCw className="size-3.5" aria-hidden="true" />다시 시도
      </button>
    </div>
  )
}
