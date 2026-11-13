"use client"

import Link from "next/link"
import { catalogKinds, sectionTabs, type CatalogKind } from "@/lib/routes"
import { useChillQuery } from "@/lib/client"
import type { WorkloadSummary } from "@/lib/domain"
import { catalogSummary, servicesUsing, type CatalogResource } from "@/lib/catalog-read"
import { buttonClassName, DataTable, EmptyState, Page, PageHeader, PageTabs, QueryBoundary } from "@/components/kit"

export function CatalogListScreen({ kind }: { kind: CatalogKind }) {
  const list = useChillQuery<{ items: CatalogResource[] }>(`v1/${kind}`)
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const label = catalogKinds[kind]
  const register = <Link href={`/catalog/${kind}/new`} className={buttonClassName({ variant: "default" })}>{label} 등록</Link>
  return (
    <Page>
      <PageHeader title="카탈로그" actions={register} />
      <PageTabs tabs={sectionTabs.catalog} />
      <QueryBoundary query={list} skeleton="table" isEmpty={d => d.items.length === 0} empty={<EmptyState title={`등록된 ${label} 없음`} action={register} />}>
        {data => (
          <DataTable
            label={label}
            rows={data.items}
            rowKey={r => r.metadata.uid}
            rowHref={r => `/catalog/${kind}/${encodeURIComponent(r.metadata.name)}`}
            defaultSort={{ key: "name", desc: false }}
            columns={[
              { key: "name", header: "이름", cell: r => r.metadata.name, sortValue: r => r.metadata.name },
              { key: "summary", header: "요약", cell: r => catalogSummary(kind, r) },
              {
                key: "services", header: "사용하는 서비스", numeric: true,
                cell: r => workloads.data ? servicesUsing(workloads.data, kind, r.metadata.name, r.metadata.uid).length : null,
                sortValue: r => workloads.data ? servicesUsing(workloads.data, kind, r.metadata.name, r.metadata.uid).length : null,
                missingReason: "서비스 목록을 불러오지 못했어요",
              },
            ]}
          />
        )}
      </QueryBoundary>
    </Page>
  )
}
