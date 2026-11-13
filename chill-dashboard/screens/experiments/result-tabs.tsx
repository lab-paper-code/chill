"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { definitionStage, finite, object, type Experiment, type ExperimentStage } from "@/lib/experiments"
import { definitionPolicy, experimentRows, publishedEnergy, stageSeconds } from "@/lib/experiment-comparison"
import {
  comparisonConditions, energyScopes, latencyDigits, nodeEnergyRows, OUTCOME_CLASSES, partRows, rejectionReasons, rejectionRows,
  requestRows, serviceSummaries, stageEnergy, waitLocations, type ConditionWord, type StageRef,
} from "@/lib/experiments-results"
import { formatKst, formatNumber } from "@/lib/format"
import { duration } from "@/lib/live-display"
import { energyStatus, experimentVerdict, type StatusInfo } from "@/lib/status-labels"
import { buttonClassName, DataTable, EmptyState, Fields, HeadroomBar, Hint, Missing, Section, StatRow, StatTile, StatusBadge, StatusDot, TimeText } from "@/components/kit"
import { SelectBlock } from "@/components/custom/blocks/SelectBlock"

export const CONDITION_LABELS = { load: "부하", identity: "서비스 정의", rules: "측정 규칙", duration: "구간 길이" } as const

/** `?key=value` updates that keep the rest of the query; null removes the key. */
export function useQueryUpdate() {
  const search = useSearchParams(), router = useRouter(), pathname = usePathname()
  return React.useCallback((changes: Record<string, string | null>) => {
    const params = new URLSearchParams(search.toString())
    for (const [key, value] of Object.entries(changes)) {
      if (value === null) params.delete(key)
      else params.set(key, value)
    }
    const query = params.toString()
    router.replace(`${pathname}${query ? `?${query}` : ""}`, { scroll: false })
  }, [search, router, pathname])
}

export const signed = (value: number, digits: number) => `${value > 0 ? "+" : ""}${formatNumber(value, digits)}`
const kilo = (joules: number | null) => (joules === null ? null : formatNumber(joules / 1000, 1))
const latency = (value: number | null) => (value === null ? null : formatNumber(value, latencyDigits(value)))

/** Candidate minus baseline, after the value; nothing when unpaired. */
function Delta({ value, digits, unit = "" }: { value: number | null | undefined; digits: number; unit?: string }) {
  if (value == null) return null
  return <span className="ml-1.5 text-xs text-muted-foreground tabular-nums"><span className="sr-only">기준 대비 </span>{signed(value, digits)}{unit}</span>
}

/** Value with a bar scaled to the largest value in its column. */
function Metered({ value, max, text }: { value: number | null; max: number; text: string | null }) {
  if (value === null || text === null) return null
  return (
    <span className="inline-flex items-center gap-2">
      <span aria-hidden="true" className="relative hidden h-1.5 w-12 shrink-0 overflow-hidden rounded-full bg-muted sm:inline-block">
        <span className="absolute inset-y-0 left-0 rounded-full bg-chart-1" style={{ width: `${max > 0 ? (value / max) * 100 : 0}%` }} />
      </span>
      {text}
    </span>
  )
}

const conditionTone = (word: ConditionWord): StatusInfo => ({ label: word, tone: word === "같음" ? "ok" : word === "다름" ? "warn" : "idle" })

export function ResultSummary({ run, stage, base }: { run: Experiment; stage: ExperimentStage; base: StageRef | null }) {
  const energy = stageEnergy(stage), baseEnergy = base ? stageEnergy(base.stage) : null
  const services = serviceSummaries(stage, run), baseline = base ? serviceSummaries(base.stage, base.run) : []
  const seconds = stageSeconds(stage)
  const met = services.filter(item => experimentVerdict(item.verdict).tone === "ok").length
  return (
    <>
      <StatRow>
        <StatTile label={energy.partial ? "관측 에너지" : "에너지"} unit="kJ" value={kilo(energy.joules)} status={energyStatus(energy.status)} reason="게시 없음"
          sub={baseEnergy?.joules != null && !baseEnergy.partial ? `기준 ${kilo(baseEnergy.joules)} kJ` : undefined} />
        <StatTile label="평균 전력" unit="W" value={energy.watts === null ? null : formatNumber(energy.watts, 1)} reason="게시 없음"
          sub={baseEnergy?.watts != null && !baseEnergy.partial ? `기준 ${formatNumber(baseEnergy.watts, 1)} W` : undefined} />
        <StatTile label="길이" value={seconds === null ? null : duration(seconds)} sub={`${formatKst(stage.from, false)} 시작`} />
        <StatTile label="목표 충족 서비스" value={services.length ? `${met} / ${services.length}` : null} reason="서비스 기록 없음" />
      </StatRow>
      {services.length === 0 ? <EmptyState title="서비스 기록 없음" /> : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {services.map(item => {
            const other = baseline.find(entry => entry.name === item.name)
            const diff = (a: number | null, b: number | null | undefined, scale = 1) => (a === null || b == null ? null : (a - b) * scale)
            return (
              <Section key={item.name} title={item.name} card actions={<StatusBadge status={experimentVerdict(item.verdict)} />}>
                <Fields columns={2} items={[
                  { label: "기한 내 성공률 (%)", info: "목표 지연 안에 성공한 요청의 비율", reason: "게시 없음",
                    value: item.success === null ? null : <>{formatNumber(item.success * 100, 1)}<Delta value={diff(item.success, other?.success, 100)} digits={1} unit="%p" /></> },
                  { label: "유효 처리량 (req/s)", info: "목표 지연 안에 성공한 초당 요청 수", reason: "게시 없음",
                    value: item.goodput === null ? null : <>{formatNumber(item.goodput, 2)}<Delta value={diff(item.goodput, other?.goodput)} digits={2} /></> },
                  { label: `지연 p${item.target?.percentile ?? 95} (ms)`, reason: "게시 없음",
                    value: item.targetQuantile === null ? null : (
                      <span className="inline-flex items-center gap-2">
                        <HeadroomBar observed={item.targetQuantile} target={item.target?.ms} />
                        <span>{latency(item.targetQuantile)}<Delta value={diff(item.targetQuantile, other?.targetQuantile)} digits={latencyDigits(item.targetQuantile)} /></span>
                      </span>
                    ) },
                  { label: "목표 지연 (ms)", value: item.target && formatNumber(item.target.ms, latencyDigits(item.target.ms)), reason: "게시 없음" },
                  { label: "지연 p50 (ms)", value: latency(item.latency.p50), reason: "게시 없음" },
                  { label: "지연 p99 (ms)", value: latency(item.latency.p99), reason: "게시 없음" },
                ]} />
              </Section>
            )
          })}
        </div>
      )}
    </>
  )
}

export function ResultParts({ run, stage }: { run: Experiment; stage: ExperimentStage }) {
  const search = useSearchParams(), update = useQueryUpdate()
  const services = [...new Set(run.stages.flatMap(item => experimentRows(item).map(row => row.name)))]
  const service = services.find(name => name === search.get("svc")) ?? services[0]
  if (!service) return <EmptyState title="서비스 기록 없음" />
  const rows = partRows(run, service)
  const maxRate = Math.max(0, ...rows.map(row => row.rate ?? 0)), maxEnergy = Math.max(0, ...rows.map(row => row.joules ?? 0))
  const successes = rows.flatMap(row => (row.success === null ? [] : [row.success]))
  const worst = successes.length > 1 && Math.min(...successes) < Math.max(...successes) ? Math.min(...successes) : null
  return (
    <DataTable
      label="구간"
      rows={rows}
      rowKey={row => row.key}
      pageSize={0}
      selectedKey={rows.find(row => row.stage === stage.id)?.key ?? null}
      toolbar={services.length > 1 && (
        <SelectBlock className="w-full sm:w-72" inputSize="sm" icon={<span className="text-xs">서비스</span>}
          options={services.map(name => ({ value: name, label: name }))} value={service} onChange={value => update({ svc: value })} />
      )}
      columns={[
        { key: "stage", header: "단계", width: "18%", cell: row => row.stage },
        { key: "part", header: "부하 구간", width: "10%", cell: row => row.part || null, missingReason: "구간 기록 없음" },
        { key: "rate", header: "요청률", unit: "req/s", numeric: true, cell: row => <Metered value={row.rate} max={maxRate} text={row.rate === null ? null : formatNumber(row.rate, row.rate < 10 ? 2 : 1)} />, missingReason: "계획 없음" },
        { key: "p95", header: "지연 p95", unit: "ms", numeric: true, missingReason: "게시 없음",
          cell: row => row.p95 === null ? null : <span className="inline-flex items-center gap-2"><HeadroomBar observed={row.p95} target={row.target} className="hidden sm:inline-block" />{latency(row.p95)}</span> },
        { key: "success", header: "기한 내 성공", unit: "%", numeric: true, missingReason: "게시 없음",
          cell: row => row.success === null ? null : <span className="inline-flex items-center gap-1.5">{row.success === worst && <StatusDot tone="warn" label="최저" />}{formatNumber(row.success * 100, 1)}</span> },
        { key: "energy", header: "에너지", unit: "kJ", numeric: true, missingReason: "단계 단위로만 게시", cell: row => <Metered value={row.joules} max={maxEnergy} text={kilo(row.joules)} /> },
        { key: "log", header: "스케줄러", width: "7rem", cell: row => <Link href={`/scheduler/decisions?from=${row.from}&to=${row.to}`} className={buttonClassName({ variant: "link", size: "sm" })}>판단 기록</Link> },
      ]}
    />
  )
}

export function ResultRequests({ stage }: { stage: ExperimentStage }) {
  const rows = requestRows(stage), rejections = rejectionRows(stage)
  const waits = rows.flatMap(row => (["gateway", "executor"] as const).map(at => ({ key: `${row.name}/${at}`, name: row.name, at, ...row[at] })))
  if (!rows.length) return <EmptyState title="서비스 기록 없음" />
  return (
    <>
      <DataTable
        label="결과 분류" title="결과 분류" rows={rows} rowKey={row => row.name} pageSize={0}
        columns={[
          { key: "name", header: "서비스", width: "24%", cell: row => row.name },
          ...OUTCOME_CLASSES.map(([key, header]) => ({ key, header, numeric: true, missingReason: "게시 없음", cell: (row: (typeof rows)[number]) => row.outcomes && finite(row.outcomes[key]) !== null ? formatNumber(finite(row.outcomes[key]), 0) : null })),
        ]}
      />
      <DataTable
        label="대기 시간" title="대기 시간" rows={waits} rowKey={row => row.key} pageSize={0}
        columns={[
          { key: "name", header: "서비스", width: "28%", cell: row => row.name },
          { key: "at", header: "위치", cell: row => waitLocations[row.at] },
          { key: "p50", header: "p50", unit: "ms", numeric: true, missingReason: "표본 없음", cell: row => latency(row.p50) },
          { key: "p95", header: "p95", unit: "ms", numeric: true, missingReason: "표본 없음", cell: row => latency(row.p95) },
          { key: "p99", header: "p99", unit: "ms", numeric: true, missingReason: "표본 없음", cell: row => latency(row.p99) },
        ]}
      />
      <DataTable
        label="거절" title="거절" rows={rejections} rowKey={row => row.key} pageSize={0}
        empty={<EmptyState compact title="거절 없음" />}
        columns={[
          { key: "service", header: "서비스", width: "24%", cell: row => row.service },
          { key: "code", header: "사유", cell: row => rejectionReasons[row.code] ?? <Missing reason={`코드 ${row.code}`} /> },
          { key: "at", header: "위치", cell: row => waitLocations[row.at] ?? <Missing reason={`코드 ${row.at}`} /> },
          { key: "count", header: "건수", numeric: true, cell: row => row.count === null ? null : formatNumber(row.count, 0) },
          { key: "gateway", header: "게이트웨이 대기 p95", unit: "ms", numeric: true, missingReason: "표본 없음", cell: row => latency(row.gateway) },
          { key: "executor", header: "실행기 대기 p95", unit: "ms", numeric: true, missingReason: "표본 없음", cell: row => latency(row.executor) },
        ]}
      />
    </>
  )
}

export function ResultEnergy({ stage }: { stage: ExperimentStage }) {
  const energy = stageEnergy(stage), drained = publishedEnergy(object(stage.kpis.powerDrainInclusive))
  const coverage = object(object(stage.kpis.power).energyCoverage), limit = finite(object(coverage.policy).maxSingleGapSeconds), gap = finite(coverage.maximumGapSeconds)
  const nodes = nodeEnergyRows(stage)
  const nodeStatus = (row: (typeof nodes)[number]): StatusInfo | null =>
    row.complete === false ? { label: "불완전", tone: "warn" } : row.policyMet === false ? { label: "기준 미달", tone: "warn" } : row.complete && row.policyMet ? { label: "충족", tone: "ok" } : null
  return (
    <>
      <StatRow>
        <StatTile label={energy.partial ? "관측 에너지" : "풀 전체 에너지"} unit="kJ" value={kilo(energy.joules)} status={energyStatus(energy.status)} reason="게시 없음"
          sub={energy.watts === null ? undefined : `평균 ${formatNumber(energy.watts, 1)} W`} />
        <StatTile label="배출 포함 에너지" unit="kJ" value={drained.partial ? null : kilo(drained.joules)} reason={drained.partial ? "일부 계측" : "게시 없음"} info="부하가 끝난 뒤 남은 요청을 처리한 시간까지 포함한 에너지" />
        <StatTile label="최대 계측 공백" unit="s" value={gap === null ? null : formatNumber(gap, 1)} reason="게시 없음" sub={limit === null ? undefined : `상한 ${formatNumber(limit, 0)} s`} />
        <StatTile label="요청 배출" value={energy.drainComplete === null ? null : energy.drainComplete ? "완료" : "미완료"} reason="게시 없음"
          status={energy.drainComplete === null ? undefined : energy.drainComplete ? { label: "완료", tone: "ok" } : { label: "미완료", tone: "warn" }} />
      </StatRow>
      <DataTable
        label="노드별 계측" title="노드별 계측" rows={nodes} rowKey={row => row.name} pageSize={0} download={{ filename: `energy-${stage.id}` }}
        empty={<EmptyState compact title="계측 기록 없음" />}
        columns={[
          { key: "name", header: "노드", cell: row => row.name, sortValue: row => row.name },
          { key: "joules", header: "에너지", unit: "kJ", numeric: true, cell: row => kilo(row.joules), sortValue: row => row.joules },
          { key: "watts", header: "평균 전력", unit: "W", numeric: true, cell: row => row.watts === null ? null : formatNumber(row.watts, 1), sortValue: row => row.watts },
          { key: "gap", header: "공백", unit: "%", numeric: true, cell: row => row.gapFraction === null ? null : formatNumber(row.gapFraction * 100, 2), sortValue: row => row.gapFraction },
          { key: "maxGap", header: "최대 공백", unit: "s", numeric: true, cell: row => row.maxGapSeconds === null ? null : formatNumber(row.maxGapSeconds, 1), sortValue: row => row.maxGapSeconds },
          { key: "failures", header: "읽기 실패", numeric: true, cell: row => row.readFailures === null ? null : formatNumber(row.readFailures, 0), sortValue: row => row.readFailures },
          { key: "status", header: "상태", cell: row => { const status = nodeStatus(row); return status && <StatusDot status={status} showLabel /> } },
        ]}
      />
    </>
  )
}

export function ResultConditions({ run, stage, base }: { run: Experiment; stage: ExperimentStage; base: StageRef | null }) {
  const conditions = base && comparisonConditions({ run, stage }, base)
  const contract = object(stage.kpis.measurementContract), coverage = object(contract.energyCoverage)
  const text = (value: unknown) => (typeof value === "string" && value ? value : null)
  const spec = definitionStage(run, stage), generator = object(run.definition?.spec.loadGenerator)
  const policy = definitionPolicy(stage, run), question = run.definition?.spec.question ?? run.facts?.question
  const runName = run.definition?.run.name, runUID = run.definition?.run.uid
  const gapFraction = finite(coverage.maxGapFraction), singleGap = finite(coverage.maxSingleGapSeconds)
  return (
    <>
      {base && conditions && (
        <Section title="비교 조건" card>
          <Fields columns={3} items={[
            { label: "기준", value: base.run === run ? base.stage.id : `${base.run.definition?.run.name ?? base.run.id} / ${base.stage.id}` },
            ...(Object.keys(CONDITION_LABELS) as (keyof typeof CONDITION_LABELS)[]).map(key => ({ label: CONDITION_LABELS[key], value: <StatusDot status={conditionTone(conditions[key])} showLabel /> })),
          ]} />
        </Section>
      )}
      <Section title="측정 규칙" card>
        <Fields columns={3} items={[
          { label: "판정 규칙", value: text(contract.judgment), reason: "기록 없음" },
          { label: "결합 규칙", value: text(contract.binding), reason: "기록 없음" },
          { label: "실패 처리", value: text(contract.failurePolicy), reason: "기록 없음" },
          { label: "에너지 범위", value: energyScopes[String(contract.energyScope)] ?? null, reason: contract.energyScope ? `코드 ${String(contract.energyScope)}` : "기록 없음" },
          { label: "공백 비율 상한 (%)", value: gapFraction === null ? null : formatNumber(gapFraction * 100, 1), reason: "기록 없음" },
          { label: "단일 공백 상한 (s)", value: singleGap === null ? null : formatNumber(singleGap, 0), reason: "기록 없음" },
        ]} />
      </Section>
      <Section title="정의 출처" card>
        <Fields columns={3} items={[
          { label: "실행", reason: "기록 없음",
            value: runName && <Link className="hover:underline" href={`/experiments/runs/${encodeURIComponent(runName)}${runUID ? `?uid=${encodeURIComponent(runUID)}` : ""}`}>{runName}</Link> },
          { label: "질문", value: question && <Hint text={question} focusable className="block max-w-full truncate text-left">{question}</Hint>, reason: "기록 없음" },
          { label: "게시", value: <TimeText at={run.to} mode="absolute" seconds={false} /> },
          { label: "부하 시나리오", value: spec?.loadScenarioRef.name, reason: "기록 없음" },
          { label: "부하 발생 노드", value: text(generator.nodeName), reason: "기록 없음" },
          { label: "설정 변경", value: policy === "미기록" ? null : <Hint text={policy} focusable className="block max-w-full truncate text-left">{policy}</Hint>, reason: "기록 없음" },
        ]} />
      </Section>
    </>
  )
}
