"use client"

import Link from "next/link"
import { DataTable, EmptyState, Fields, Hint, Missing, QueryBoundary, Section, type Column } from "@/components/kit"
import { useChillQuery } from "@/lib/client"
import type { LoadScenario } from "@/lib/evaluation-author"
import { object, records } from "@/lib/evaluation-author"
import type { Experiment } from "@/lib/experiments"
import { planRows, serviceNames, settingRows, specStages, type PlanRow, type Run, type ScenarioSource, type SettingRow } from "@/lib/experiments-run"
import { formatNumber } from "@/lib/format"
import { duration } from "@/lib/live-display"
import { parseSchedulerJSON } from "@/lib/scheduler"

/** Plan of the run: question, stage and load table, settings changes. Frozen scenarios of the publication when available. */
export function RunPlan({ run, publication }: { run: Run; publication: Experiment | undefined }) {
  const frozen = publication?.definition?.run.uid === run.uid ? publication.definition : undefined
  const live = useChillQuery<{ items: LoadScenario[] }>("v1/evaluations/load-scenarios", { enabled: !frozen, decoder: parseSchedulerJSON })
  const generator = object(run.spec.loadGenerator)
  const question = typeof run.spec.question === "string" ? run.spec.question : null
  const body = (scenarios: readonly ScenarioSource[]) => <PlanTables run={run} scenarios={scenarios} extra={frozen?.workloads ?? []} />
  return (
    <div className="grid gap-6">
      <Section card>
        <Fields columns={3} items={[
          { label: "질문", value: question && <Hint text={question} focusable className="block min-w-0 truncate text-left">{question}</Hint>, reason: "미기록" },
          { label: "부하 발생 장치", value: typeof generator.nodeName === "string" ? generator.nodeName : null, reason: "미기록" },
          { label: "단계 수", value: `${specStages(run.spec).length}` },
        ]} />
      </Section>
      {frozen ? body(frozen.loadScenarios) : <QueryBoundary query={live}>{data => body(data.items)}</QueryBoundary>}
    </div>
  )
}

function PlanTables({ run, scenarios, extra }: { run: Run; scenarios: readonly ScenarioSource[]; extra: readonly { name: string; uid: string }[] }) {
  const { rows, services } = planRows(run.spec, scenarios)
  const names = serviceNames(scenarios, extra)
  const stageIDs = specStages(run.spec)
  const settings = settingRows([object(run.spec.settingsPatch), ...records(run.spec.stages).map(stage => object(stage.settingsPatch))], uid => names.get(uid) ?? "미확인 서비스")
  const total = rows.every(row => row.seconds !== null) ? rows.reduce((sum, row) => sum + row.seconds!, 0) : null
  // Seven columns at most: per-service rates up to three services, the summed rate beyond that.
  // Service names are long, so per-service headers carry the name only and the title carries the unit.
  const perService = services.length <= 3
  const rateColumns: Column<PlanRow>[] = perService
    ? services.map(service => ({ key: `rate:${service}`, header: service, numeric: true, cell: row => row.rates[service] === undefined ? null : formatNumber(row.rates[service], 2), missingReason: "요청 없음" }))
    : [{ key: "rate", header: `요청률 합계 (${services.length}개 서비스)`, unit: "req/s", numeric: true, cell: row => row.part === null ? null : formatNumber(Object.values(row.rates).reduce((a, b) => a + b, 0), 2) }]
  return (
    <div className="grid gap-6">
      <DataTable<PlanRow>
        label="단계와 부하"
        title={<span className="flex items-baseline gap-3">단계와 부하<span className="text-xs font-normal tabular-nums text-muted-foreground">{total !== null && `전체 ${duration(total)}`}{perService && services.length > 0 && <span className="ml-3">요청률 req/s</span>}</span></span>}
        rows={rows} rowKey={row => row.key} pageSize={0}
        columns={[
          { key: "stage", header: "단계", cell: row => row.stage, width: "15%" },
          { key: "scenario", header: "부하 시나리오", cell: row => row.scenario && <Link className="hover:underline" href={`/experiments/scenarios/${encodeURIComponent(row.scenario.name)}?uid=${encodeURIComponent(row.scenario.uid)}`}>{row.scenario.name}</Link>, missingReason: "미기록", width: "17%" },
          { key: "part", header: "구간", cell: row => row.part, missingReason: "시나리오 없음", width: "8%" },
          { key: "seconds", header: "길이", unit: "s", numeric: true, cell: row => row.seconds === null ? null : formatNumber(row.seconds), width: "8%" },
          ...rateColumns,
        ]}
      />
      {/* ponytail: one column per stage that changes settings; past six such stages this breaks the 7-column rule, switch to a stage select then. */}
      <DataTable<SettingRow>
        label="설정 변경" title="설정 변경" rows={settings} rowKey={row => row.key} pageSize={0}
        empty={<EmptyState compact title="설정 변경 없음" />}
        columns={[
          { key: "label", header: "항목", cell: row => row.label ?? <Missing reason={`설정 ${row.key}`} />, width: "28%" },
          ...["실험 전체", ...stageIDs].flatMap((scope, index): Column<SettingRow>[] => settings.some(row => row.values[index] !== null) ? [{ key: `scope:${index}`, header: scope, cell: row => row.values[index], missingReason: "변경 없음" }] : []),
        ]}
      />
    </div>
  )
}
