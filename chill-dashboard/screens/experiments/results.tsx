"use client"

import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { sectionTabs } from "@/lib/routes"
import { useChillQuery } from "@/lib/client"
import { decodeExperimentPage, type ExperimentPage, type OutcomeAxis } from "@/lib/experiments"
import { resultName } from "@/lib/experiments-results"
import { duration } from "@/lib/live-display"
import { experimentStepStatus, experimentVerdict, type StatusInfo } from "@/lib/status-labels"
import { DataTable, EmptyState, Page, PageHeader, PageTabs, QueryBoundary, StatusBadge, StatusDot, TimeText } from "@/components/kit"

const LIMIT = 5

const axis = (value: OutcomeAxis | undefined, label: (code: string | undefined) => StatusInfo, key: "status" | "verdict") =>
  value ? <StatusDot status={label(value[key])} showLabel /> : null

export function ExperimentResultsScreen() {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const raw = Number(search.get("offset"))
  const offset = Number.isSafeInteger(raw) && raw > 0 ? raw : 0
  const list = useChillQuery<ExperimentPage>(`v1/experiments?limit=${LIMIT}&offset=${offset}`, { decoder: decodeExperimentPage })
  const setOffset = (next: number) => {
    const params = new URLSearchParams(search.toString())
    if (next > 0) params.set("offset", String(next))
    else params.delete("offset")
    const query = params.toString()
    router.push(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }
  return (
    <Page>
      <PageHeader title="실험" />
      <PageTabs tabs={sectionTabs.experiments} />
      <QueryBoundary query={list} isEmpty={d => d.items.length === 0 && offset === 0 && !d.unreadable?.length} empty={<EmptyState title="게시된 결과 없음" />}>
        {data => {
          const unreadable = data.unreadable?.length ?? 0
          return (
            <DataTable
              label="게시된 결과"
              rows={data.items}
              rowKey={r => r.id}
              rowHref={r => `/experiments/results/${encodeURIComponent(r.id)}`}
              pageSize={0}
              busy={list.isFetching}
              actions={unreadable > 0 && <StatusBadge tone="warn" label={`읽을 수 없음 ${unreadable}건`} reason="게시 요약이 있지만 서버가 읽지 못한 결과" />}
              paging={{ mode: "offset", offset, limit: LIMIT, total: data.page?.total, onOffsetChange: setOffset }}
              columns={[
                { key: "name", header: "실험", width: "30%", cell: r => resultName(r) },
                { key: "from", header: "시작", width: "9.5rem", cell: r => <TimeText at={r.from} mode="absolute" seconds={false} /> },
                { key: "length", header: "길이", numeric: true, cell: r => {
                  const seconds = (Date.parse(r.to) - Date.parse(r.from)) / 1000
                  return Number.isFinite(seconds) && seconds >= 0 ? duration(seconds) : null
                } },
                { key: "execution", header: "실행", cell: r => axis(r.outcome?.execution, experimentStepStatus, "status"), missingReason: "기록 없음" },
                { key: "measurement", header: "측정", cell: r => axis(r.outcome?.measurement, experimentVerdict, "verdict"), missingReason: "기록 없음" },
                { key: "cleanup", header: "정리", cell: r => axis(r.outcome?.cleanup, experimentStepStatus, "status"), missingReason: "기록 없음" },
                { key: "stages", header: "단계", numeric: true, width: "4.5rem", cell: r => r.stages.length },
              ]}
            />
          )
        }}
      </QueryBoundary>
    </Page>
  )
}
