"use client"

import * as React from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQueries, useChillQuery } from "@/lib/client"
import { decodeExperiment, experimentPath, historicalPublicationIDs, type Experiment, type ExperimentStage, type OutcomeAxis } from "@/lib/experiments"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { baselineChoices, comparisonConditions, energyDelta, failedServices, recordedBaseline, resultName, stageKey, type StageRef } from "@/lib/experiments-results"
import { reasonText } from "@/lib/experiments-run"
import { formatNumber } from "@/lib/format"
import { experimentStepStatus, experimentVerdict, type StatusInfo } from "@/lib/status-labels"
import { buttonClassName, EmptyState, Fields, Hint, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, Section, Select, StatusBadge } from "@/components/kit"
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
  if (!stage) return <NoStages run={run} />
  const choices = baselineChoices(run, runs, stage)
  const baseParam = search.get("base")
  const base: StageRef | null = baseParam === "none" ? null : baseParam ? choices.find(item => stageKey(item) === baseParam) ?? null : recordedBaseline(run, runs, stage)
  const tab = TABS.some(item => item.value === search.get("tab")) ? search.get("tab")! : "summary"
  return (
    <Page>
      <div className="flex flex-wrap items-center gap-2">
        <Select
          className="w-full sm:w-72" inputSize="sm" icon={<span className="text-xs">단계</span>}
          options={stages.map(item => ({ value: item.id, label: item.id }))} value={stage.id}
          onChange={value => update({ stage: value, base: null })}
        />
        {choices.length > 0 && (
          <Select
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

/** The answer row: which side was better when a baseline is chosen, else the stage's own verdict. */
function Conclusion({ run, stage, base }: { run: Experiment; stage: ExperimentStage; base: StageRef | null }) {
  const failed = failedServices(stage)
  const failedText = failed.length ? failed.join(", ") : "없음"
  if (!base) {
    const verdict = run.outcome?.stages?.find(item => item.id === stage.id)?.measurement
    return (
      <Section card>
        <Fields columns={3} items={[
          { label: "단계 판정", value: verdict?.verdict ? <StatusBadge status={experimentVerdict(verdict.verdict)} reason={reasonText(verdict.reason)} /> : null, reason: "판정 미게시" },
          { label: "목표 미달 서비스", value: <Hint text={failedText} focusable className="block min-w-0 truncate text-left">{failedText}</Hint> },
        ]} />
      </Section>
    )
  }
  const conditions = comparisonConditions({ run, stage }, base)
  const status: StatusInfo = conditions.limited ? { label: "참고", tone: "warn" } : { label: "동일 조건", tone: "ok" }
  const differing = (Object.keys(CONDITION_LABELS) as (keyof typeof CONDITION_LABELS)[]).filter(key => conditions[key] !== "같음").map(key => `${CONDITION_LABELS[key]} ${conditions[key]}`).join(", ")
  const delta = energyDelta(stage, base.stage)
  const baseFailed = failedServices(base.stage).length
  return (
    <Section card>
      <Fields columns={3} items={[
        { label: "비교 조건", value: <StatusBadge status={status} reason={differing || undefined} /> },
        { label: "에너지 (기준 대비)", reason: "일부 계측이라 비교 안 함",
          value: delta && (delta.joules === 0 ? "차이 없음" : `${formatNumber(Math.abs(delta.joules) / 1000, 1)} kJ ${delta.joules < 0 ? "절감" : "더 사용"}${delta.percent === null ? "" : ` (${signed(delta.percent, 1)}%)`}`) },
        { label: "목표 미달 서비스", value: <Hint text={failedText} focusable className="block min-w-0 truncate text-left">{`${failed.length}개`}<span className="text-muted-foreground">{` (기준 ${baseFailed}개)`}</span></Hint> },
      ]} />
    </Section>
  )
}

/** A publication without stages: why, from its three outcome axes. */
function NoStages({ run }: { run: Experiment }) {
  const axis = (value: OutcomeAxis | undefined, status: StatusInfo) => value ? <StatusBadge status={status} reason={reasonText(value.reason)} /> : null
  return (
    <>
      <Section card>
        <Fields columns={3} items={[
          { label: "실행", value: axis(run.outcome?.execution, experimentStepStatus(run.outcome?.execution?.status)), reason: "기록 없음" },
          { label: "측정", value: axis(run.outcome?.measurement, experimentVerdict(run.outcome?.measurement?.verdict)), reason: "기록 없음" },
          { label: "정리", value: axis(run.outcome?.cleanup, experimentStepStatus(run.outcome?.cleanup?.status)), reason: "기록 없음" },
        ]} />
      </Section>
      <EmptyState title="게시된 단계 없음" />
    </>
  )
}
