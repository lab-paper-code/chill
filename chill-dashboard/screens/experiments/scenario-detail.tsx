"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { canDeleteLoad, object, type LoadScenario } from "@/lib/evaluation-author"
import { scenarioParts, scenarioSeconds, scenarioServices } from "@/lib/experiments-scenario"
import { duration } from "@/lib/live-display"
import { formatBytes, formatNumber } from "@/lib/format"
import { buttonClassName, ConfirmDialog, DataTable, EmptyState, Hint, Page, PageHeader, QueryBoundary, RawDialog, Section, StatRow, StatTile, useToast } from "@/components/kit"
import Button from "@/components/custom/blocks/Button"
import { RateChart } from "./scenario-chart"
import { SCENARIOS_PATH } from "./scenarios"

export const scenarioPath = (name: string) => `${SCENARIOS_PATH}/${encodeURIComponent(name)}`
export const scenarioQuery = (item: { name: string; uid: string }) => `${scenarioPath(item.name)}?${new URLSearchParams({ uid: item.uid })}`
// ponytail: a part table shows at most this many service columns (7 column limit); the chart and the service table show all.
const RATE_COLUMNS = 5

export function ScenarioDetailScreen({ name }: { name: string }) {
  const query = useChillQuery<LoadScenario>(scenarioPath(name), { decoder: parseSchedulerJSON })
  return (
    <Page>
      <QueryBoundary query={query} skeleton="block">{item => <Body item={item} />}</QueryBoundary>
    </Page>
  )
}

function Body({ item }: { item: LoadScenario }) {
  const wantedUid = useSearchParams().get("uid")
  const router = useRouter(), queryClient = useQueryClient(), mutation = useChillMutation(), { toast } = useToast()
  const [raw, setRaw] = React.useState(false), [removing, setRemoving] = React.useState(false)
  if (wantedUid && wantedUid !== item.uid) {
    return <><PageHeader title={item.name} /><EmptyState title="다른 정의로 교체됨" action={<Link href={`/experiments/scenarios/${encodeURIComponent(item.name)}`} className={buttonClassName({ variant: "outline" })}>현재 정의 보기</Link>} /></>
  }
  const services = scenarioServices(item.spec), parts = scenarioParts(item.spec), inUse = !canDeleteLoad(item)
  const arrivals = object(item.spec.arrivalDistribution), seeds = object(item.spec.seeds), datasets = object(item.spec.datasetRefs), traces = object(item.spec.traceRefs)
  const shown = services.slice(0, RATE_COLUMNS)
  const search = (extra: Record<string, string>) => `?${new URLSearchParams(extra)}`

  function remove() {
    mutation.mutate({ path: scenarioQuery(item), method: "DELETE" }, {
      onSuccess: () => {
        setRemoving(false)
        toast({ title: "삭제됨", type: "success" })
        void queryClient.invalidateQueries({ predicate: q => String(q.queryKey[1]).startsWith(SCENARIOS_PATH) })
        router.replace("/experiments/scenarios")
      },
      onError: error => {
        setRemoving(false)
        if (error instanceof ApiError && error.unknownOutcome) toast({ title: "결과 미확인", description: "목록에서 삭제 여부를 확인하세요", type: "warning" })
        else if (error instanceof ApiError && error.status === 409) toast({ title: "사용 중이라 삭제할 수 없음", type: "error" })
        else toast({ title: "삭제 실패", type: "error" })
      },
    })
  }

  return (
    <>
      <PageHeader
        title={item.name}
        actions={inUse
          ? <Hint text="사용 중인 실험이 있어 수정할 수 없음"><Button variant="default" disabled>수정</Button></Hint>
          : <Link href={`/experiments/scenarios/${encodeURIComponent(item.name)}/edit${search({ uid: item.uid })}`} className={buttonClassName({ variant: "default" })}>수정</Link>}
        more={[
          { label: "복사", onSelect: () => router.push(`/experiments/scenarios/new${search({ copy: item.name })}`) },
          { label: "원본 보기", onSelect: () => setRaw(true) },
          { label: "삭제", destructive: true, disabled: inUse, onSelect: () => setRemoving(true) },
        ]}
      />
      <StatRow>
        <StatTile label="구간 수" value={formatNumber(parts.length)} />
        <StatTile label="전체 길이" value={duration(scenarioSeconds(item.spec))} />
        <StatTile label="서비스" value={formatNumber(services.length)} />
        <StatTile label="사용 중 실험" value={formatNumber(item.inUse.length)} />
      </StatRow>
      {item.inUse.length > 0 && (
        <Section title="사용 중 실험">
          <ul className="flex flex-wrap gap-2">
            {item.inUse.map(run => <li key={run}><Link href={`/experiments/runs/${encodeURIComponent(run)}`} className={buttonClassName({ variant: "outline", size: "sm" })}>{run}</Link></li>)}
          </ul>
        </Section>
      )}
      <Section title="요청률" card><RateChart spec={item.spec} /></Section>
      <DataTable
        label="구간" title="구간 (요청률 req/s)" rows={parts.map((part, index) => ({ ...part, index }))} rowKey={row => String(row.index)} pageSize={0}
        columns={[
          { key: "id", header: "구간", cell: row => row.id },
          { key: "seconds", header: "길이", unit: "초", numeric: true, cell: row => row.seconds == null ? null : formatNumber(row.seconds, row.seconds % 1 ? 1 : 0) },
          ...shown.map((service, i) => ({ key: `r${i}`, header: service, numeric: true, cell: (row: { rates: Record<string, number | null> }) => row.rates[service] == null ? null : formatNumber(row.rates[service], 2) })),
        ]}
      />
      <DataTable
        label="서비스 입력" title="서비스 입력" rows={services.map(service => ({ service }))} rowKey={row => row.service} pageSize={0}
        columns={[
          { key: "service", header: "서비스", cell: ({ service }) => service },
          { key: "arrival", header: "도착 분포", cell: ({ service }) => String(arrivals[service] ?? "") || null },
          { key: "dataset", header: "데이터셋", cell: ({ service }) => String(object(datasets[service]).name ?? "") || null },
          { key: "seed", header: "난수 씨앗", numeric: true, cell: ({ service }) => String(seeds[service] ?? "") || null },
          { key: "trace", header: "기록 입력", cell: ({ service }) => traces[service] ? `${String(object(traces[service]).key)} (${formatBytes(Number(object(traces[service]).size))})` : null },
        ]}
      />
      <RawDialog open={raw} onOpenChange={setRaw} value={item} filename={`scenario-${item.name}.json`} title={item.name} />
      <ConfirmDialog
        open={removing} onOpenChange={setRemoving} title="시나리오 삭제" target={item.name} tone="destructive" confirmLabel="삭제"
        pending={mutation.isPending} onConfirm={remove}
      />
    </>
  )
}
