"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQueries, useChillQuery } from "@/lib/client"
import { decodeExperiment, experimentPath, historicalPublicationIDs, type Experiment, type ExperimentStage } from "@/lib/experiments"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { baselineChoices, comparisonConditions, energyDelta, failedServices, recordedBaseline, resultName, stageKey, type StageRef } from "@/lib/experiments-results"
import { experimentVerdict, type StatusInfo } from "@/lib/status-labels"
import { buttonClassName, Fields, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, Section, StatusBadge } from "@/components/kit"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"
import { CONDITION_LABELS, ResultConditions, ResultEnergy, ResultParts, ResultRequests, ResultSummary, signed, useQueryUpdate } from "./result-tabs"

const TABS = [{ value: "summary", label: "요약" }, { value: "parts", label: "구간" }, { value: "requests", label: "요청 결과" }, { value: "energy", label: "에너지" }, { value: "conditions", label: "조건" }]

function download(run: Experiment) {
  const url = URL.createObjectURL(new Blob([stringifySchedulerJSON(run)], { type: "application/json" }))
  Object.assign(document.createElement("a"), { href: url, download: `${run.id}.json` }).click()
  URL.revokeObjectURL(url)
}

export function ExperimentResultDetailScreen({ id }: { id: string }) {
  const query = useChillQuery<Experiment>(experimentPath(id), { decoder: decodeExperiment })
  const [raw, setRaw] = React.useState(false)
  const run = query.data
  return (
    <Page>
      <PageHeader
        title={run ? resultName(run) : "결과"}
        status={run ? experimentVerdict(run.outcome?.measurement?.verdict ?? run.status) : undefined}
        actions={run?.definition && <Link href={`/experiments/runs/new?publication=${encodeURIComponent(run.id)}`} className={buttonClassName({ variant: "default" })}>다시 실행</Link>}
        more={run && [{ label: "원본 보기", onSelect: () => setRaw(true) }, { label: "내려받기", onSelect: () => download(run) }]}
      />
      <QueryBoundary query={query} skeleton="block">
        {data => <ResultBody run={data} />}
      </QueryBoundary>
      {run && <RawDialog open={raw} onOpenChange={setRaw} value={run} filename={`${run.id}.json`} />}
    </Page>
  )
}

function ResultBody({ run }: { run: Experiment }) {
  const search = useSearchParams(), update = useQueryUpdate()
  const historical = useChillQueries<Experiment>(historicalPublicationIDs(run).map(experimentPath), { decoder: decodeExperiment })
  const loaded = historical.flatMap(item => (item.data ? [item.data] : []))
  const runs = [run, ...loaded]
  const stages = [...run.stages].sort((a, b) => Date.parse(a.from) - Date.parse(b.from))
  const stage = stages.find(item => item.id === search.get("stage")) ?? stages[0]
  if (!stage) return <Section card><Fields items={[{ label: "단계", value: null, reason: "게시된 단계 없음" }]} columns={1} /></Section>
  const choices = baselineChoices(run, runs, stage)
  const baseParam = search.get("base")
  const base: StageRef | null = baseParam === "none" ? null : baseParam ? choices.find(item => stageKey(item) === baseParam) ?? null : recordedBaseline(run, runs, stage)
  const tab = TABS.some(item => item.value === search.get("tab")) ? search.get("tab")! : "summary"
  return (
    <Page>
      <div className="flex flex-wrap items-center gap-2">
        <SelectBlock
          className="w-full sm:w-72" inputSize="sm" icon={<span className="text-xs">단계</span>}
          options={stages.map(item => ({ value: item.id, label: item.id }))} value={stage.id}
          onChange={value => update({ stage: value, base: null })}
        />
        {choices.length > 0 && (
          <SelectBlock
            className="w-full sm:w-72" inputSize="sm" icon={<span className="text-xs">기준</span>}
            options={[{ value: "none", label: "없음" }, ...choices.map(item => ({ value: stageKey(item), label: item.label }))]}
            value={base ? stageKey(base) : "none"} onChange={value => update({ base: value })}
          />
        )}
      </div>
      <Conclusion run={run} stage={stage} base={base} />
      <PageTabs param="tab" defaultValue="summary" tabs={TABS} />
      {tab === "summary" && <ResultSummary run={run} stage={stage} base={base} />}
      {tab === "parts" && <ResultParts run={run} stage={stage} />}
      {tab === "requests" && <ResultRequests stage={stage} />}
      {tab === "energy" && <ResultEnergy stage={stage} />}
      {tab === "conditions" && <ResultConditions run={run} stage={stage} base={base} />}
    </Page>
  )
}

function Conclusion({ run, stage, base }: { run: Experiment; stage: ExperimentStage; base: StageRef | null }) {
  const conditions = base && comparisonConditions({ run, stage }, base)
  const status: StatusInfo = !conditions ? { label: "기록 없음", tone: "idle" } : conditions.limited ? { label: "참고", tone: "warn" } : { label: "동일 조건", tone: "ok" }
  const differing = conditions ? (Object.keys(CONDITION_LABELS) as (keyof typeof CONDITION_LABELS)[]).filter(key => conditions[key] !== "같음").map(key => `${CONDITION_LABELS[key]} ${conditions[key]}`).join(", ") : ""
  const delta = base && energyDelta(stage, base.stage)
  const failed = failedServices(stage)
  return (
    <Section card>
      <Fields columns={3} items={[
        { label: "비교 상태", value: <StatusBadge status={status} reason={differing || undefined} /> },
        { label: "에너지 차이", value: delta && `${signed(delta.joules / 1000, 1)} kJ${delta.percent === null ? "" : ` (${signed(delta.percent, 1)}%)`}`, reason: base ? "일부 계측" : "기준 없음" },
        { label: "목표 미달 서비스", value: failed.length ? failed.join(", ") : "없음" },
      ]} />
    </Section>
  )
}
