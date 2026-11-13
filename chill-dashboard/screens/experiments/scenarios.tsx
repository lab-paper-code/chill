"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { sectionTabs } from "@/lib/routes"
import { useChillQuery } from "@/lib/client"
import { parseSchedulerJSON } from "@/lib/scheduler"
import type { LoadScenario } from "@/lib/evaluation-author"
import { scenarioParts, scenarioSeconds, scenarioServices } from "@/lib/experiments-scenario"
import { duration } from "@/lib/live-display"
import { Button, DataTable, EmptyState, FilterBar, Hint, Page, PageHeader, PageTabs, QueryBoundary, TimeText, buttonClassName } from "@/components/kit"

export const SCENARIOS_PATH = "v1/evaluations/load-scenarios"
export const scenarioHref = (name: string) => `/experiments/scenarios/${encodeURIComponent(name)}`

export function ScenariosScreen() {
  const query = useChillQuery<{ items: LoadScenario[] }>(SCENARIOS_PATH, { decoder: parseSchedulerJSON })
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  const q = search.get("q") ?? ""
  const setQ = (value: string) => router.replace(value ? `${pathname}?q=${encodeURIComponent(value)}` : pathname, { scroll: false })
  return (
    <Page>
      <PageHeader title="실험" actions={<Link href="/experiments/scenarios/new" className={buttonClassName({ variant: "default" })}>새 시나리오</Link>} />
      <PageTabs tabs={sectionTabs.experiments} />
      <QueryBoundary query={query} isEmpty={data => data.items.length === 0} empty={<EmptyState title="부하 시나리오 없음" action={<Link href="/experiments/scenarios/new" className={buttonClassName({ variant: "default" })}>새 시나리오</Link>} />}>
        {data => {
          const rows = data.items.filter(item => item.name.includes(q.trim()))
          return (
            <DataTable
              label="부하 시나리오" rows={rows} rowKey={row => row.uid} rowHref={row => scenarioHref(row.name)}
              defaultSort={{ key: "created", desc: true }} download={{ filename: "scenarios" }}
              toolbar={<FilterBar search={{ value: q, onChange: setQ, placeholder: "이름 검색" }} />}
              empty={<EmptyState compact title="일치하는 시나리오 없음" action={<Button variant="outline" size="sm" onClick={() => setQ("")}>검색 지우기</Button>} />}
              columns={[
                { key: "name", header: "이름", cell: row => row.name, sortValue: row => row.name },
                { key: "parts", header: "구간 수", numeric: true, cell: row => scenarioParts(row.spec).length, sortValue: row => scenarioParts(row.spec).length },
                { key: "length", header: "전체 길이", numeric: true, cell: row => duration(scenarioSeconds(row.spec)), sortValue: row => scenarioSeconds(row.spec), csv: row => scenarioSeconds(row.spec) },
                { key: "services", header: "서비스", numeric: true, cell: row => <Hint text={scenarioServices(row.spec).join(", ")} focusable>{scenarioServices(row.spec).length}</Hint>, sortValue: row => scenarioServices(row.spec).length },
                { key: "inUse", header: "사용 중", numeric: true, cell: row => row.inUse.length, sortValue: row => row.inUse.length },
                { key: "created", header: "생성", cell: row => row.createdAt ? <TimeText at={row.createdAt} mode="absolute" seconds={false} /> : null, sortValue: row => row.createdAt ? Date.parse(row.createdAt) : null, csv: row => row.createdAt ?? "" },
              ]}
            />
          )
        }}
      </QueryBoundary>
    </Page>
  )
}
