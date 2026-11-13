"use client"

import * as React from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import { ChevronRight } from "lucide-react"
import { DataTable, Fields, Hint, Missing, QueryBoundary, Section, StatusBadge, StatusDot, TimeText, useNow } from "@/components/kit"
import { Progress } from "@/components/custom/blocks/ProgressBlock"
import { ToggleGroup, ToggleGroupItem } from "@/components/custom/blocks/ToggleGroupBlock"
import { useChillQuery } from "@/lib/client"
import { evaluationRemaining, type ActiveEvaluation, type ActiveEvaluations } from "@/lib/evaluation-active"
import { condition, injectionActions, isFinished, provisionalRows, runSteps, specStages, stageSteps, type LoadJob, type Run, type RunStage, type Step, type StepState } from "@/lib/experiments-run"
import { records } from "@/lib/evaluation-author"
import { DASH, formatKst, formatNumber, formatPercent } from "@/lib/format"
import { duration } from "@/lib/live-display"
import { evaluationReason, evaluationVerdict, injectionOutcome, loadJobStatus, type StatusTone } from "@/lib/status-labels"

const stateTone: Record<StepState, StatusTone> = { done: "ok", failed: "fail", running: "progress", stalled: "warn", waiting: "idle" }
const stateWord: Record<StepState, string> = { done: "완료", failed: "실패", running: "진행 중", stalled: "종료 미기록", waiting: "대기" }

/** Tooltip text of a reason code: its label, or the code when the label is unknown. */
export function reasonHint(code: string | null | undefined): string | undefined {
  if (!code) return undefined
  const reason = evaluationReason(code)
  return reason.code ? `코드 ${reason.code}` : reason.label
}
/** A reason code in a cell: its label, or "—" with the code in the tooltip. */
export function Reason({ code }: { code: string }) {
  const reason = evaluationReason(code)
  return reason.code ? <Missing reason={`코드 ${reason.code}`} /> : <>{reason.label}</>
}

/** Procedure steps in order: state icon, name, start time; the reason of a failed step in the tooltip. */
function StepStrip({ steps, label, finished }: { steps: Step[]; label: string; finished: boolean }) {
  return (
    <ol aria-label={label} className="flex flex-wrap items-start gap-x-2 gap-y-3">
      {steps.map((step, index) => {
        const word = step.state === "waiting" && finished ? "미기록" : stateWord[step.state]
        const reason = step.state === "failed" || step.state === "stalled" ? reasonHint(step.reason) : undefined
        return (
          <li key={step.key} className="flex min-w-0 items-start gap-2">
            {index > 0 && <ChevronRight className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />}
            <Hint text={reason ?? word} focusable>
              <span className="flex min-w-0 flex-col gap-0.5">
                <StatusDot tone={stateTone[step.state]} label={step.label} showLabel className="text-sm font-medium" />
                <span className="pl-5 text-xs text-muted-foreground tabular-nums">{step.at ? formatKst(step.at).slice(11) : word}</span>
                <span className="sr-only">{word}</span>
              </span>
            </Hint>
          </li>
        )
      })}
    </ol>
  )
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
      <Progress value={Math.min(elapsed, planned)} max={planned || 1} barSize="sm" aria-label="부하 진행" />
      <div className="flex justify-between gap-3 text-sm tabular-nums">
        <span><span className="text-muted-foreground">부하 경과 </span>{duration(elapsed)}</span>
        <span>{elapsed >= planned
          ? <><span className="text-muted-foreground">계획 종료 후 </span>{duration(elapsed - planned)}</>
          : <><span className="text-muted-foreground">남은 시간 </span>{remaining === null ? DASH : duration(remaining)}</>}</span>
      </div>
    </div>
  )
}

/** Elapsed time from `from` to `to`, ticking while `to` is absent. */
export function Elapsed({ from, to }: { from: string | null | undefined; to?: string | null }) {
  if (!from) return null
  return to ? <>{duration((Date.parse(to) - Date.parse(from)) / 1000)}</> : <Ticking from={from} />
}
function Ticking({ from }: { from: string }) {
  const now = useNow()
  return <>{now ? duration((now - Date.parse(from)) / 1000) : DASH}</>
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
              { label: "시작", value: stage?.startedAt ? <TimeText at={stage.startedAt} mode="absolute" /> : null, reason: "시작 전" },
              { label: "소요", value: stage?.startedAt ? <Elapsed from={stage.startedAt} to={stage.finishedAt ?? (finished ? run.finishedAt : null)} /> : null, reason: "시작 전" },
              { label: "판정", value: verdict ? <StatusBadge status={evaluationVerdict(verdict)} reason={reasonHint(verdict.reason)} /> : null, reason: "판정 전" },
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
            { key: "name", header: "이름", cell: job => job.name, width: "34%" },
            { key: "status", header: "상태", cell: job => <StatusBadge status={loadJobStatus(job.status)} /> },
            { key: "start", header: "시작", cell: job => job.startedAt ? <TimeText at={job.startedAt} mode="absolute" /> : null },
            { key: "end", header: "완료", cell: job => job.completedAt ? <TimeText at={job.completedAt} mode="absolute" /> : null, missingReason: "진행 중" },
            { key: "reason", header: "사유", cell: job => job.reason ? <Reason code={job.reason} /> : null },
          ]}
        />
      )}

      {injections.length > 0 && (
        <DataTable<InjectionRow>
          label="장애 주입" title="장애 주입" rows={injections} rowKey={row => row.id} pageSize={0}
          columns={[
            { key: "id", header: "주입", cell: row => row.id },
            { key: "action", header: "동작", cell: row => injectionActions[row.action] ?? <Hint text={`코드 ${row.action}`} focusable>{DASH}</Hint> },
            { key: "offset", header: "시작 후", unit: "s", numeric: true, cell: row => row.offset === null ? null : formatNumber(row.offset) },
            { key: "node", header: "노드", cell: row => row.node, missingReason: "미기록" },
            { key: "pod", header: "파드", cell: row => row.pod, missingReason: "미기록" },
            { key: "outcome", header: "결과", cell: row => row.outcome ? <StatusBadge status={injectionOutcome(row.outcome)} /> : null, missingReason: "결과 미기록" },
          ]}
        />
      )}

      {provisional && (
        <DataTable
          label="잠정 결과"
          title={<span className="flex items-center gap-2">잠정 결과{typeof provisional.asOf === "string" && <span className="text-xs font-normal text-muted-foreground"><TimeText at={provisional.asOf} /></span>}</span>}
          rows={provisionalRows(provisional)} rowKey={row => row.name} pageSize={0}
          columns={[
            { key: "name", header: "서비스", cell: row => row.name, width: "26%" },
            { key: "success", header: "기한 내 성공률", unit: "%", numeric: true, cell: row => row.successRate === null ? null : formatPercent(row.successRate, 2).replace("%", "") },
            { key: "goodput", header: "유효 처리량", unit: "req/s", numeric: true, cell: row => row.goodput === null ? null : formatNumber(row.goodput, 2) },
            { key: "latency", header: "목표 분위 지연", unit: "ms", numeric: true, cell: row => row.latencyMs === null ? null : formatNumber(row.latencyMs, 1), missingReason: "분위 미제공" },
            { key: "target", header: "목표 지연", unit: "ms", numeric: true, cell: row => row.targetMs === null ? null : `p${row.percentile} ${formatNumber(row.targetMs, 0)}` },
            { key: "offered", header: "보낸 요청", numeric: true, cell: row => row.offered === null ? null : formatNumber(row.offered) },
          ]}
        />
      )}
    </div>
  )
}
