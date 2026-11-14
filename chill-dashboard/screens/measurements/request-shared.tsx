"use client"

import * as React from "react"
import Link from "next/link"
import { Button, buttonClassName, Fields, Hint, Section, StatusBadge, useToast, type BoundaryQuery, type FieldItem } from "@/components/kit"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { cn } from "@/lib/utils"
import type { ApiError, RelayResponse } from "@/lib/client-transport"
import { failure, useMeasurementDocumentSubmission } from "@/hooks/use-measurement-submission"
import { requestRejectionGuidance } from "@/lib/measurement-request"

/** One loading/error gate over several queries; `data` is `true` once every query has data. */
export function allLoaded(queries: BoundaryQuery<unknown>[]): BoundaryQuery<true> {
  const failed = queries.find(query => query.error && query.data === undefined)
  return {
    data: queries.every(query => query.data !== undefined) ? true : undefined,
    error: failed?.error ?? null,
    isPending: queries.some(query => query.isPending),
    failureReason: queries.find(query => query.failureReason)?.failureReason,
    refetch: () => queries.forEach(query => { if (query.error) void query.refetch() }),
  }
}

export type ChoiceOption = { value: string; label: string; disabled?: boolean; reason?: string; title?: string }

/** Radio choices as a grid of bordered options; unavailable options stay visible, disabled, with the reason in a tooltip. */
export function ChoiceGroup({ label, value, onChange, options, error, wide = false, disabled = false }: {
  label: string; value: string; onChange: (value: string) => void; options: ChoiceOption[]; error?: string; wide?: boolean; disabled?: boolean
}) {
  const id = React.useId()
  return (
    <div className="grid gap-2">
      <span id={id} className={cn("text-sm font-medium", error && "text-fail")}>{label}</span>
      <RadioGroup aria-labelledby={id} aria-invalid={!!error || undefined} value={value} onValueChange={onChange} disabled={disabled}
        className={cn("grid gap-2", wide ? "grid-cols-1 sm:grid-cols-2 xl:grid-cols-4" : "grid-cols-2 sm:grid-cols-3 xl:grid-cols-5")}>
        {options.map(option => (
          <Hint key={option.value} text={option.reason ?? option.title} className="block min-w-0">
            <Label className={cn(
              "h-10 min-w-0 rounded-md border border-border bg-card px-3 font-normal leading-tight",
              option.disabled || disabled ? "cursor-not-allowed text-muted-foreground" : "cursor-pointer hover:bg-muted has-[[data-state=checked]]:border-primary",
            )}>
              <RadioGroupItem value={option.value} disabled={option.disabled} />
              <span className="truncate">{option.label}</span>
              {option.reason && <span className="sr-only">{`, ${option.reason}`}</span>}
            </Label>
          </Hint>
        ))}
      </RadioGroup>
      {error && <p className="text-xs font-medium text-fail">{error}</p>}
    </div>
  )
}

/** Unit shown inside the right edge of an input. */
export const suffix = (unit: string) => <span className="text-xs tabular-nums">{unit}</span>

type Submission = ReturnType<typeof useMeasurementDocumentSubmission>

function mutationBadge(state: { isPending: boolean; error: ApiError | null; data?: RelayResponse }, passed: boolean, rejected?: string) {
  if (state.isPending) return <StatusBadge tone="progress" label="검사 중" />
  if (state.error) return state.error.status ? <StatusBadge tone="fail" label="거부" reason={`HTTP ${state.error.status}: ${failure(state.error).message}`} /> : <StatusBadge tone="warn" label="응답 없음" />
  if (!state.data) return null
  return passed ? <StatusBadge tone="ok" label="통과" /> : rejected ? <StatusBadge tone="fail" label="거부" reason={rejected} /> : <StatusBadge tone="warn" label="확인 필요" reason="예상과 다른 응답" />
}

/** Optional dry run and preflight of a measurement request: one row per check, then the batch plan. */
export function PreflightSection({ submission, before = [], blocked, control }: {
  submission: Submission
  /** Rows shown first (e.g. 지원). */ before?: FieldItem[]
  blocked?: boolean
  /** Extra control next to the button (e.g. an acknowledgement checkbox). */ control?: React.ReactNode
}) {
  const { dry, pre, send, answer, admitted, pending, unknown } = submission
  const rejected = answer && !answer.resolves ? requestRejectionGuidance(answer.reason ?? "")?.message ?? answer.reason ?? "거부" : undefined
  const batch = answer?.batchPlan
  const list = (values: number[] | undefined) => values?.length ? values.join(", ") : values ? "없음" : null
  const sendReason = send.error && send.error.status ? requestRejectionGuidance(`${send.error.body} ${JSON.stringify(send.error.data)}`)?.message ?? failure(send.error).message : undefined
  const items: FieldItem[] = [
    ...before,
    { label: "접수 검사", value: mutationBadge(dry, admitted), reason: "검사 전" },
    { label: "실행 계획", value: mutationBadge(pre, answer?.resolves === true, rejected), reason: "검사 전" },
    ...(answer?.session ? [
      { label: "계획 배치", value: list(answer.session.plannedBatchSizes) },
      { label: "최소 표본 수", value: String(answer.session.minimumSamples), info: `수준마다 모을 처리 시간 기록 수, p${answer.session.sloPercentile} 추정에 필요한 최솟값` },
    ] : batch?.plannedBatches ? [
      { label: "측정된 배치", value: list(batch.measuredBatches) },
      { label: "측정할 배치", value: list(batch.missingBatches) },
      { label: "탐색 상한", value: batch.plannedBatches.length ? String(Math.max(...batch.plannedBatches)) : null, reason: "미확정" },
    ] : []),
    ...(sendReason ? [{ label: "제출", value: <StatusBadge tone="fail" label="거부" reason={sendReason} /> }] : []),
    ...(submission.error ? [{ label: "확인", value: <StatusBadge tone="warn" label="확인 필요" reason={submission.error} /> }] : []),
  ]
  return (
    <Section title="사전 검사" card actions={<>
      {control}
      <Button size="sm" variant="outline" loading={pending} disabled={blocked || pending || unknown || send.isPending} onClick={submission.verify}>사전 검사</Button>
    </>}>
      <Fields items={items} columns={3} />
    </Section>
  )
}

/** The response of a submit was lost: no resubmission, look the request up by name instead. */
export function UnknownOutcome({ name, onLookup, looking, checked, href }: { name: string; onLookup: () => void; looking: boolean; checked: boolean; href?: string }) {
  return (
    <Section title="결과 미확인" card actions={href
      ? <Link href={href} className={buttonClassName({ variant: "default", size: "sm" })}>요청 열기</Link>
      : <Button size="sm" variant="outline" loading={looking} onClick={onLookup}>요청 조회</Button>}>
      <div role="alert">
        <Fields items={[
          { label: "요청 이름", value: name },
          { label: "조회", value: href ? <StatusBadge tone="ok" label="접수됨" /> : checked ? <StatusBadge tone="idle" label="목록에 없음" /> : null, reason: "조회 전" },
        ]} />
      </div>
    </Section>
  )
}

/** Toasts the outcome of a submit once. Lost responses are "결과 미확인", never retried. */
export function useSubmitToast(send: { isSuccess: boolean; error: ApiError | null }, describe?: (error: ApiError) => string) {
  const { toast } = useToast()
  React.useEffect(() => {
    if (send.isSuccess) toast({ title: "측정 요청 접수", type: "success" })
  }, [send.isSuccess, toast])
  React.useEffect(() => {
    const error = send.error
    if (!error) return
    if (lostResponse(error)) toast({ title: "결과 미확인", type: "warning" })
    else toast({ title: "측정 요청 실패", description: describe?.(error) ?? `HTTP ${error.status}`, type: "error" })
    // Only a new error instance toasts again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [send.error])
}

/** Same rule the submission hook uses to lock resubmission. */
export const lostResponse = (error: ApiError) => error.unknownOutcome || error.status === 0 || failure(error).code === "MeasurementAuditIncomplete"
