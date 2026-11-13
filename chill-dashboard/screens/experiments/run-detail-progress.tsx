"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { DataTable, Fields, HeadroomBar, Hint, IdText, MeterBar, Missing, QueryBoundary, Section, StatusBadge, Steps, TimeText, useNow, type StepItem } from "@/components/kit"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { useChillQuery } from "@/lib/client"
import { evaluationRemaining, type ActiveEvaluation, type ActiveEvaluations } from "@/lib/evaluation-active"
import { condition, injectionActions, isFinished, provisionalRows, reasonText, runSteps, specStages, stageSteps, type LoadJob, type Run, type RunStage, type Step, type StepState } from "@/lib/experiments-run"
import { records } from "@/lib/evaluation-author"
import { DASH, formatKst, formatNumber, formatSeconds, msNumber, percentNumber, rateNumber } from "@/lib/format"
import { evaluationReason, evaluationVerdict, injectionOutcome, loadJobStatus } from "@/lib/status-labels"

const stepStatus: Record<StepState, StepItem["status"]> = { done: "done", failed: "failed", running: "current", stalled: "failed", waiting: "pending" }

/** A reason code in a cell: its label, or "—" with the code in the tooltip. */
export function Reason({ code }: { code: string }) {
  const reason = evaluationReason(code)
  return reason.code ? <Missing reason={`코드 ${reason.code}`} /> : <>{reason.label}</>
}

/** Procedure steps in order; the start time, or why a step failed, in the tooltip. */
function StepStrip({ steps, label, finished }: { steps: Step[]; label: string; finished: boolean }) {
  const reason = (step: Step) =>
    step.state === "failed" ? reasonText(step.reason) ?? "실패"
    : step.state === "stalled" ? "종료 미기록"
    : step.at ? `시작 ${formatKst(step.at).slice(11)}`
    : finished ? "미기록" : undefined
  return <Steps label={label} items={steps.map(step => ({ label: step.label, status: stepStatus[step.state], reason: reason(step) }))} />
}

/** Load clock of the active stage; the only part that re-renders every second. */
export function LoadClock({ stage }: { stage: ActiveEvaluation["stage"] }) {
  const now = useNow()
  const goAt = stage?.goAt ? Date.parse(stage.goAt) : NaN
  const planned = stage?.plannedSeconds ?? 0
  if (!stage || !Number.isFinite(goAt) || !now) return <span className="text-sm text-muted-foreground">부하 시작 대기</span>
  const elapsed = Math.max(0, (now - goAt) / 1000)
  const remaining = evaluationRemaining(stage, now)
  return (
    <div className="grid gap-2">
      <MeterBar value={Math.min(elapsed, planned)} max={planned || 1} width="full" label={`부하 진행 ${formatSeconds(Math.floor(elapsed))}`} />
      <div className="flex justify-between gap-3 text-sm tabular-nums">
        <span><span className="text-muted-foreground">부하 경과 </span>{formatSeconds(Math.floor(elapsed))}</span>
        <span>{elapsed >= planned
          ? <><span className="text-muted-foreground">계획 종료 후 </span>{formatSeconds(Math.floor(elapsed - planned))}</>
          : <><span className="text-muted-foreground">남은 시간 </span>{remaining === null ? DASH : formatSeconds(remaining)}</>}</span>
      </div>
    </div>
  )
}

/** Elapsed time from `from` to `to`, ticking while `to` is absent. */
export function Elapsed({ from, to }: { from: string | null | undefined; to?: string | null }) {
  if (!from) return null
  return to ? <>{formatSeconds((Date.parse(to) - Date.parse(from)) / 1000)}</> : <Ticking from={from} />
}
function Ticking({ from }: { from: string }) {
  const now = useNow()
  return <>{now ? formatSeconds(Math.floor((now - Date.parse(from)) / 1000)) : DASH}</>
}

type InjectionRow = { id: string; action: string; offset: number | null; node: string | null; pod: string | null; outcome?: string }

export function RunProgress({ run }: { run: Run }) {
  const params = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const finished = isFinished(run)
  const active = useChillQuery<ActiveEvaluations>("v1/evaluations/active", { enabled: !finished, poll: true })
  const live = finished ? undefined : active.data?.items.find(item => item.name === run.name && item.uid === run.uid)
  const ids = specStages(run.spec)
  const current = typeof run.currentStage === "string" ? run.currentStage : undefined
  const requested = params.get("stage")
  const selected = requested && ids.includes(requested) ? requested : current && ids.includes(current) ? current : ids[0]
  const stage: RunStage | undefined = run.stages?.find(item => item.id === selected)
  const liveStage = live?.stage?.id === selected ? live.stage : undefined
  const select = (id: string) => {
    const next = new URLSearchParams(params.toString())
    if (id === (current && ids.includes(current) ? current : ids[0])) next.delete("stage")
    else next.set("stage", id)
    router.replace(`${pathname}?${next}`, { scroll: false })
  }
  const planned = records(run.spec.stages).find(item => item.id === selected)
  const injections: InjectionRow[] = liveStage
    ? liveStage.injections.map(i => ({ id: i.id, action: i.action, offset: i.offsetSeconds, node: i.nodeName ?? null, pod: i.podName ?? null, outcome: i.outcome }))
    : records(planned?.failureInjection).map(i => ({ id: String(i.id), action: String(i.action), offset: typeof i.offsetSeconds === "number" ? i.offsetSeconds : null, node: null, pod: null }))
  const provisional = stage && !stage.finishedAt && !finished && stage.provisionalD1 ? stage.provisionalD1 : undefined
  const verdict = condition(stage?.conditions, "MeasurementPassed")

  return (
    <div className="grid gap-6">
      <Section title="절차" card>
        <StepStrip steps={runSteps(run)} label="실행 절차" finished={finished} />
      </Section>

      {selected && (
        <Section title="단계" card>
          <div className="grid gap-5">
            {ids.length > 1 && (
              <div className="-mx-1 overflow-x-auto px-1">
                <ToggleGroup type="single" variant="segmented" size="sm" aria-label="단계 선택" value={selected} onValueChange={value => { if (value) select(value) }}>
                  {ids.map(id => <ToggleGroupItem key={id} value={id}>{id}</ToggleGroupItem>)}
                </ToggleGroup>
              </div>
            )}
            <Fields columns={3} items={[
              { label: "시작", value: stage?.startedAt ? <TimeText at={stage.startedAt} mode="detail" /> : null, reason: "시작 전" },
              { label: "소요", value: stage?.startedAt ? <Elapsed from={stage.startedAt} to={stage.finishedAt ?? (finished ? run.finishedAt : null)} /> : null, reason: "시작 전" },
              { label: "판정", value: verdict ? <StatusBadge status={evaluationVerdict(verdict)} reason={reasonText(verdict.reason)} /> : null, reason: "판정 전" },
            ]} />
            <StepStrip steps={stageSteps(stage, !finished)} label={`${selected} 진행`} finished={finished || !!stage?.finishedAt} />
            {liveStage && <LoadClock stage={liveStage} />}
            {!finished && active.error && <QueryBoundary query={active}>{() => null}</QueryBoundary>}
          </div>
        </Section>
      )}

      {!!stage?.loadJobs?.length && (
        <DataTable<LoadJob>
          label="부하 작업" title="부하 작업" rows={stage.loadJobs} rowKey={job => job.uid} pageSize={0}
          columns={[
            { key: "name", header: "이름", cell: job => <IdText value={job.name} head={12} tail={9} />, width: "34%" },
            { key: "status", header: "상태", cell: job => <StatusBadge status={loadJobStatus(job.status)} /> },
            { key: "start", header: "시작", cell: job => job.startedAt ? <TimeText at={job.startedAt} mode="list" /> : null },
            { key: "end", header: "완료", cell: job => job.completedAt ? <TimeText at={job.completedAt} mode="list" /> : null, missingReason: "진행 중" },
            { key: "reason", header: "사유", cell: job => job.reason ? <Reason code={job.reason} /> : null, missingReason: "없음" },
          ]}
        />
      )}

      {injections.length > 0 && (
        <DataTable<InjectionRow>
          label="장애 주입" title="장애 주입" rows={injections} rowKey={row => row.id} pageSize={0}
          columns={[
            { key: "id", header: "주입", cell: row => row.id },
            { key: "action", header: "동작", cell: row => injectionActions[row.action] ?? <Hint text={`코드 ${row.action}`} focusable>{DASH}</Hint> },
            { key: "offset", header: "시작 후", numeric: true, cell: row => row.offset === null ? null : formatSeconds(row.offset) },
            { key: "node", header: "장치", cell: row => row.node, missingReason: "미기록" },
            { key: "pod", header: "파드", cell: row => row.pod, missingReason: "미기록" },
            { key: "outcome", header: "결과", cell: row => row.outcome ? <StatusBadge status={injectionOutcome(row.outcome)} /> : null, missingReason: "결과 미기록" },
          ]}
        />
      )}

      {provisional && (
        <DataTable
          label="잠정 결과"
          title={<span className="flex items-center gap-2">잠정 결과{typeof provisional.asOf === "string" && <span className="text-xs font-normal text-muted-foreground"><TimeText at={provisional.asOf} mode="relative" /></span>}</span>}
          rows={provisionalRows(provisional)} rowKey={row => row.name} pageSize={0}
          columns={[
            { key: "name", header: "서비스", cell: row => row.name, width: "26%" },
            { key: "success", header: "기한 내 성공률", unit: "%", numeric: true, cell: row => row.successRate === null ? null : percentNumber(row.successRate), missingReason: "표본 없음" },
            { key: "goodput", header: "유효 처리량", unit: "req/s", numeric: true, cell: row => row.goodput === null ? null : rateNumber(row.goodput), missingReason: "표본 없음" },
            { key: "latency", header: "목표 분위 지연", unit: "ms", numeric: true, missingReason: "표본 없음",
              cell: row => row.latencyMs === null ? null : <span className="inline-flex items-center gap-2"><HeadroomBar observed={row.latencyMs} target={row.targetMs} className="hidden sm:inline-block" />{msNumber(row.latencyMs)}</span> },
            { key: "target", header: "목표 지연", unit: "ms", numeric: true, missingReason: "목표 미기록",
              cell: row => row.targetMs === null ? null : <Hint text={`p${row.percentile} 기준`} focusable>{msNumber(row.targetMs)}</Hint> },
            { key: "offered", header: "보낸 요청", numeric: true, cell: row => row.offered === null ? null : formatNumber(row.offered) },
          ]}
        />
      )}
    </div>
  )
}
