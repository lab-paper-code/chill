"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { canDeleteLoad, object, type LoadScenario } from "@/lib/evaluation-author"
import { arrivalLabels, scenarioParts, scenarioSeconds, scenarioServices } from "@/lib/experiments-scenario"
import { formatBytes, formatSeconds, rateNumber } from "@/lib/format"
import { Button, buttonClassName, ConfirmDialog, DataTable, EmptyState, Fields, Missing, Page, PageHeader, QueryBoundary, RawDialog, Section, TimeText, useToast } from "@/components/kit"
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
      <QueryBoundary query={query} skeleton="block">{item => <Page><Body item={item} /></Page>}</QueryBoundary>
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
        if (error instanceof ApiError && error.unknownOutcome) toast({ title: "결과 미확인", description: "목록에서 삭제 여부 확인 필요", type: "warning" })
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
          ? <Button variant="default" disabled disabledReason="사용 중인 실험이 있어 수정할 수 없음">수정</Button>
          : <Link href={`/experiments/scenarios/${encodeURIComponent(item.name)}/edit${search({ uid: item.uid })}`} className={buttonClassName({ variant: "default" })}>수정</Link>}
        more={[
          { label: "복사", onSelect: () => router.push(`/experiments/scenarios/new${search({ copy: item.name })}`) },
          { label: "원본 보기", onSelect: () => setRaw(true) },
          { label: "삭제", destructive: true, disabled: inUse, disabledReason: "사용 중인 실험이 있어 삭제할 수 없음", onSelect: () => setRemoving(true) },
        ]}
      />
      <Section card>
        <Fields columns={3} items={[
          { label: "전체 길이", value: `${formatSeconds(scenarioSeconds(item.spec))} (구간 ${parts.length}개)` },
          { label: "생성", value: item.createdAt ? <TimeText at={item.createdAt} mode="detail" /> : null, reason: "미기록" },
          { label: "사용 중 실험", value: item.inUse.length
            ? <span className="flex min-w-0 flex-wrap gap-x-3">{item.inUse.map(run => <Link key={run} href={`/experiments/runs/${encodeURIComponent(run)}`} className="truncate text-primary hover:underline">{run}</Link>)}</span>
            : "없음" },
        ]} />
      </Section>
      <Section title="요청률" card><RateChart spec={item.spec} /></Section>
      <DataTable
        label="구간" title="구간 (요청률 req/s)" rows={parts.map((part, index) => ({ ...part, index }))} rowKey={row => String(row.index)} pageSize={0}
        columns={[
          { key: "id", header: "구간", cell: row => row.id },
          { key: "seconds", header: "길이", numeric: true, cell: row => row.seconds == null ? null : formatSeconds(row.seconds) },
          ...shown.map((service, i) => ({ key: `r${i}`, header: service, numeric: true, cell: (row: { rates: Record<string, number | null> }) => row.rates[service] == null ? null : rateNumber(row.rates[service]) })),
        ]}
      />
      <DataTable
        label="서비스 입력" title="서비스 입력" rows={services.map(service => ({ service }))} rowKey={row => row.service} pageSize={0}
        columns={[
          { key: "service", header: "서비스", cell: ({ service }) => service },
          { key: "arrival", header: "도착 분포", cell: ({ service }) => { const code = String(arrivals[service] ?? ""); return code ? arrivalLabels[code] ?? <Missing reason={`코드 ${code}`} /> : null } },
          { key: "dataset", header: "데이터셋", cell: ({ service }) => String(object(datasets[service]).name ?? "") || null },
          { key: "seed", header: "난수 씨앗", numeric: true, cell: ({ service }) => String(seeds[service] ?? "") || null },
          { key: "trace", header: "기록 입력", missingReason: "사용 안 함",
            cell: ({ service }) => traces[service] ? `${String(object(traces[service]).key).split("/").pop()} (${formatBytes(Number(object(traces[service]).size))})` : null },
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
