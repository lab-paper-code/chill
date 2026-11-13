"use client"

import Link from "next/link"
import { catalogKinds, sectionTabs, type CatalogKind } from "@/lib/routes"
import { useChillQuery } from "@/lib/client"
import type { WorkloadSummary } from "@/lib/domain"
import { catalogSummaryColumns, servicesUsing, type CatalogResource } from "@/lib/catalog-read"
import { buttonClassName, DataTable, EmptyState, Page, PageHeader, PageTabs, QueryBoundary, type Column } from "@/components/kit"

export function CatalogListScreen({ kind }: { kind: CatalogKind }) {
  const list = useChillQuery<{ items: CatalogResource[] }>(`v1/${kind}`)
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const label = catalogKinds[kind]
  const register = <Link href={`/catalog/${kind}/new`} className={buttonClassName({ variant: "default" })}>{label} 등록</Link>
  const users = (r: CatalogResource) => workloads.data ? servicesUsing(workloads.data, kind, r.metadata.name, r.metadata.uid).length : null
  const columns: Column<CatalogResource>[] = [
    { key: "name", header: "이름", cell: r => r.metadata.name, sortValue: r => r.metadata.name },
    ...catalogSummaryColumns(kind).map(column => ({ key: column.key, header: column.header, numeric: column.numeric, cell: column.value, sortValue: column.value, missingReason: "없음" })),
    { key: "services", header: "사용 서비스", numeric: true, cell: users, sortValue: users, missingReason: "서비스 목록 조회 실패" },
  ]
  return (
    <Page>
      <PageHeader title="카탈로그" actions={register} />
      <PageTabs tabs={sectionTabs.catalog} />
      <QueryBoundary query={list} skeleton="table" isEmpty={d => d.items.length === 0} empty={<EmptyState title={`등록된 ${label} 없음`} action={register} />}>
        {data => (
          <DataTable
            label={label} rows={data.items} rowKey={r => r.metadata.uid}
            rowHref={r => `/catalog/${kind}/${encodeURIComponent(r.metadata.name)}`}
            defaultSort={{ key: "name", desc: false }} columns={columns}
          />
        )}
      </QueryBoundary>
    </Page>
  )
}
