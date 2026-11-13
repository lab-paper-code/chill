"use client"

import * as React from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Button, ConfirmDialog, DataTable, EmptyState, Fields, QueryBoundary, Section, StatusBadge, TimeText, useToast, type BoundaryQuery } from "@/components/kit"
import { useChillMutation } from "@/lib/client"
import { evaluationActions, evaluationReleaseBody } from "@/lib/evaluation-request"
import type { Experiment, OutcomeAxis } from "@/lib/experiments"
import { condition, reasonText, runPath, type Run } from "@/lib/experiments-run"
import { errorCodeLabel, evaluationCleanup, evaluationPool } from "@/lib/status-labels"
import { Reason } from "./run-detail-progress"

type Residual = NonNullable<OutcomeAxis["residuals"]>[number]

/** The run still holds its pool and cleanup cannot finish by itself: an operator confirms the return. */
export function releasable(run: Run): boolean {
  const pool = condition(run.conditions, "PoolAcquired"), cleanup = condition(run.conditions, "CleanupComplete")
  return pool?.status === "True" && (evaluationActions(run.phase).release || cleanup?.status === "False")
}

/** Header action: ask the server to confirm a manual pool return. */
export function ReleaseAction({ run, onChanged }: { run: Run; onChanged: () => void }) {
  const [open, setOpen] = React.useState(false)
  const release = useChillMutation()
  const cache = useQueryClient()
  const { toast } = useToast()
  const confirm = (reason: string) => release.mutate(
    { path: `${runPath(run.name)}/release`, method: "POST", body: evaluationReleaseBody(run.uid, reason) },
    {
      onSuccess: () => { setOpen(false); toast({ title: "반환 확인 요청됨", type: "success" }) },
      onError: error => {
        setOpen(false)
        const code = (error.data as { code?: unknown } | null)?.code
        toast(error.unknownOutcome || error.status === 0 ? { title: "결과 미확인", type: "warning" } : { title: "반환 확인 요청 실패", description: typeof code === "string" ? errorCodeLabel(code) : `HTTP ${error.status}`, type: "error" })
      },
      onSettled: () => { onChanged(); void cache.invalidateQueries({ queryKey: ["chill", "v1/evaluations"] }) },
    },
  )
  return (
    <>
      <Button variant="default" onClick={() => setOpen(true)}>자원 반환 확인</Button>
      <ConfirmDialog
        open={open} onOpenChange={setOpen} title="자원 반환 확인 요청" target={run.name}
        changes={[{ label: "장치 풀", before: evaluationPool(condition(run.conditions, "PoolAcquired")).label, after: "반환 확인" }]}
        reason={{ label: "확인 사유", required: true }} confirmLabel="요청" pending={release.isPending} onConfirm={confirm}
      />
    </>
  )
}

/** Pool return and cleanup state; the residual resources of a published run. */
export function RunCleanup({ run, publication }: { run: Run; publication: BoundaryQuery<Experiment> | null }) {
  const pool = condition(run.conditions, "PoolAcquired")
  const cleanup = condition(run.conditions, "CleanupComplete")
  return (
    <div className="grid gap-6">
      <Section card>
        <Fields items={[
          { label: "장치 풀", value: pool ? <StatusBadge status={evaluationPool(pool)} reason={reasonText(pool.reason)} /> : null, reason: "미기록" },
          { label: "풀 변경 시각", value: pool?.lastTransitionTime ? <TimeText at={pool.lastTransitionTime} mode="detail" /> : null, reason: "미기록" },
          { label: "정리", value: cleanup ? <StatusBadge status={evaluationCleanup(cleanup)} reason={reasonText(cleanup.reason)} /> : null, reason: "정리 전" },
          { label: "정리 시각", value: cleanup?.lastTransitionTime ? <TimeText at={cleanup.lastTransitionTime} mode="detail" /> : null, reason: "정리 전" },
        ]} />
      </Section>

      {cleanup?.status === "False" && publication && (
        <QueryBoundary query={publication}>{data => (
          <DataTable<Residual>
            label="잔여 자원" title="잔여 자원" rows={data.outcome?.cleanup?.residuals ?? []} rowKey={row => `${row.kind}/${row.name}`} pageSize={0}
            empty={<EmptyState compact title="잔여 자원 없음" />}
            columns={[
              { key: "kind", header: "종류", cell: row => row.kind, width: "20%" },
              { key: "name", header: "이름", cell: row => row.name, width: "45%" },
              { key: "reason", header: "사유", cell: row => <Reason code={row.reason} /> },
            ]}
          />
        )}</QueryBoundary>
      )}
    </div>
  )
}
