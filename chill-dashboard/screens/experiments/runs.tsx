"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { buttonClassName, DataTable, EmptyState, FilterBar, Page, PageHeader, PageTabs, QueryBoundary, Section, StatusBadge, TimeText, type Column } from "@/components/kit"
import { useChillQuery } from "@/lib/client"
import type { ActiveEvaluation, ActiveEvaluations } from "@/lib/evaluation-active"
import { condition, isFinished, runHref, type Run } from "@/lib/experiments-run"
import { sectionTabs } from "@/lib/routes"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { evaluationPhase, evaluationPool } from "@/lib/status-labels"
import { LoadClock, reasonHint } from "./run-detail-progress"

const newRun = <Link href="/experiments/runs/new" className={buttonClassName({ variant: "default" })}>새 실험</Link>
const time = (at: string | null | undefined) => at ? Date.parse(at) : undefined

const columns: Column<Run>[] = [
  { key: "name", header: "이름", cell: run => run.name, sortValue: run => run.name, width: "30%" },
  {
    key: "phase", header: "상태", width: "8rem", sortValue: run => evaluationPhase(run.phase).label,
    cell: run => run.stopRequested && !isFinished(run) ? <StatusBadge tone="warn" label="중지 요청됨" /> : <StatusBadge status={evaluationPhase(run.phase)} />,
  },
  { key: "stage", header: "단계", cell: run => typeof run.currentStage === "string" ? run.currentStage : null, missingReason: "시작 전" },
  { key: "start", header: "시작", cell: run => run.startedAt && <TimeText at={run.startedAt} mode="absolute" seconds={false} />, sortValue: run => time(run.startedAt), missingReason: "시작 전", width: "9.5rem" },
  { key: "end", header: "종료", cell: run => run.finishedAt && <TimeText at={run.finishedAt} mode="absolute" seconds={false} />, sortValue: run => time(run.finishedAt), missingReason: "진행 중", width: "9.5rem" },
  {
    key: "pool", header: "정리", width: "8rem",
    cell: run => { const pool = condition(run.conditions, "PoolAcquired"); return pool ? <StatusBadge status={evaluationPool(pool)} reason={reasonHint(pool.reason)} /> : null },
    missingReason: "미기록",
  },
  {
    key: "result", header: "결과", width: "6rem",
    cell: run => run.publicationID && <Link href={`/experiments/results/${encodeURIComponent(run.publicationID)}`} className="text-primary hover:underline">결과</Link>,
    missingReason: "미게시",
  },
]

export function ExperimentRunsScreen() {
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const [search, setSearch] = React.useState(params.get("q") ?? "")
  const list = useChillQuery<{ items: Run[] }>("v1/evaluations", { poll: data => !!data?.items.some(run => !isFinished(run)), decoder: parseSchedulerJSON })
  const active = useChillQuery<ActiveEvaluations>("v1/evaluations/active", { poll: true })
  const setQuery = (value: string) => {
    setSearch(value)
    const next = new URLSearchParams(params.toString())
    if (value) next.set("q", value)
    else next.delete("q")
    router.replace(next.size ? `${pathname}?${next}` : pathname, { scroll: false })
  }
  return (
    <Page>
      <PageHeader title="실험" actions={newRun} />
      <PageTabs tabs={sectionTabs.experiments} />
      {(active.error || !!active.data?.items.length) && (
        <QueryBoundary query={active}>
          {data => <div className="grid gap-3">{data.items.map(run => <ActiveCard key={run.uid} run={run} />)}</div>}
        </QueryBoundary>
      )}
      <QueryBoundary query={list} isEmpty={data => !data.items.length} empty={<EmptyState title="실험 없음" action={newRun} />}>
        {data => {
          const needle = search.trim().toLowerCase()
          return (
            <DataTable
              label="실험 실행" rows={needle ? data.items.filter(run => run.name.toLowerCase().includes(needle)) : data.items}
              rowKey={run => run.uid} rowHref={runHref} columns={columns} defaultSort={{ key: "start", desc: true }}
              toolbar={<FilterBar search={{ value: search, onChange: setQuery, placeholder: "이름 검색", label: "실험 이름 검색" }} />}
              empty={<EmptyState compact title="검색 결과 없음" />}
            />
          )
        }}
      </QueryBoundary>
    </Page>
  )
}

function ActiveCard({ run }: { run: ActiveEvaluation }) {
  return (
    <Section card>
      <div className="grid gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
          <StatusBadge status={evaluationPhase("Running")} />
          <Link href={runHref(run)} className="min-w-0 truncate font-medium hover:underline">{run.name}</Link>
          <span className="ml-auto text-sm tabular-nums"><span className="text-muted-foreground">단계 {run.stageIndex}/{run.stagesTotal} </span>{run.stage?.id}</span>
        </div>
        <LoadClock stage={run.stage} />
      </div>
    </Section>
  )
}
