"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { buttonClassName, ConfirmDialog, DataTable, EmptyState, Fields, IdText, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, Section, StatusBadge, TimeText, useToast, type MoreAction } from "@/components/kit"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { evaluationActions, type EvaluationLiveTarget } from "@/lib/evaluation-request"
import { decodeExperiment, experimentPath, type Experiment } from "@/lib/experiments"
import { condition, isFinished, reasonText, runHref, runPath, type Run } from "@/lib/experiments-run"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { errorCodeLabel, evaluationExecution, evaluationPhase, evaluationVerdict } from "@/lib/status-labels"
import { releasable, ReleaseAction, RunCleanup } from "./run-detail-cleanup"
import { RunPlan } from "./run-detail-plan"
import { Elapsed, RunProgress } from "./run-detail-progress"

const tabs = [{ value: "progress", label: "진행" }, { value: "plan", label: "계획" }, { value: "cleanup", label: "정리" }, { value: "target", label: "대상" }]

export function ExperimentRunDetailScreen({ name }: { name: string }) {
  const params = useSearchParams()
  const tab = params.get("tab")
  const detail = useChillQuery<Run>(runPath(name), { poll: data => !!data && !isFinished(data), decoder: parseSchedulerJSON })
  const run = detail.data
  const urlUID = params.get("uid")

  if (detail.error?.status === 404) return (
    <Page>
      <PageHeader title={name} />
      <EmptyState title="실행 없음" action={<Link href="/experiments/runs" className={buttonClassName({ variant: "outline" })}>실행 목록</Link>} />
    </Page>
  )
  if (run && urlUID && run.uid !== urlUID) return (
    <Page>
      <PageHeader title={name} />
      <EmptyState title="실행 교체됨" action={<Link href={runHref(run)} className={buttonClassName({ variant: "outline" })}>현재 실행 보기</Link>} />
    </Page>
  )
  if (!run) return (
    <Page>
      <PageHeader title={name} />
      <QueryBoundary query={detail} skeleton="block">{() => null}</QueryBoundary>
    </Page>
  )
  return <RunScreen key={run.uid} run={run} tab={tab ?? defaultTab(run)} refetch={() => void detail.refetch()} />
}

// A run that has not started has no progress yet: open on its plan.
const defaultTab = (run: Run) => run.phase === "Pending" ? "plan" : "progress"

function RunScreen({ run, tab, refetch }: { run: Run; tab: string; refetch: () => void }) {
  const router = useRouter()
  const cache = useQueryClient()
  const { toast } = useToast()
  const [raw, setRaw] = React.useState(false)
  const [removing, setRemoving] = React.useState(false)
  const remove = useChillMutation()
  const finished = isFinished(run)
  const actions = evaluationActions(run.phase)
  const publication = useChillQuery<Experiment>(experimentPath(run.publicationID ?? ""), { enabled: !!run.publicationID && (tab === "plan" || tab === "cleanup"), decoder: decodeExperiment })
  const verdict = condition(run.conditions, "MeasurementPassed")
  const execution = condition(run.conditions, "ExecutionComplete")
  const edit = `/experiments/runs/${encodeURIComponent(run.name)}`

  const editHref = `${edit}/edit?uid=${encodeURIComponent(run.uid)}`
  const more: MoreAction[] = [
    { label: "다시 실행", onSelect: () => router.push(`/experiments/runs/new?copy=${encodeURIComponent(run.name)}`) },
    { label: "원본 보기", onSelect: () => setRaw(true) },
    { label: actions.remove, destructive: true, disabled: actions.remove === "중지" && !!run.stopRequested, disabledReason: "중지 요청됨", onSelect: () => setRemoving(true) },
  ]
  const confirmRemove = () => remove.mutate(
    { path: `${runPath(run.name)}?uid=${encodeURIComponent(run.uid)}`, method: "DELETE" },
    {
      onSuccess: () => {
        setRemoving(false)
        toast({ title: actions.remove === "삭제" ? "삭제 요청됨" : "중지 요청됨", type: "success" })
        void cache.invalidateQueries({ queryKey: ["chill", "v1/evaluations"] })
        if (actions.remove === "삭제") router.push("/experiments/runs")
        else refetch()
      },
      onError: error => {
        setRemoving(false)
        const code = (error.data as { code?: unknown } | null)?.code
        toast(error.unknownOutcome || error.status === 0 ? { title: "결과 미확인", type: "warning" } : { title: `${actions.remove} 실패`, description: typeof code === "string" ? errorCodeLabel(code) : `HTTP ${error.status}`, type: "error" })
        refetch()
      },
    },
  )

  return (
    <Page>
      <PageHeader
        title={run.name}
        status={evaluationPhase(run.phase)}
        badges={<>
          {run.stopRequested && !finished && <StatusBadge tone="warn" label="중지 요청됨" reason={run.stopRequested.by || undefined} />}
          {execution?.status === "False" && <StatusBadge status={evaluationExecution(execution)} reason={reasonText(execution.reason)} />}
          {verdict && <StatusBadge status={evaluationVerdict(verdict)} reason={reasonText(verdict.reason)} />}
        </>}
        actions={run.publicationID
          ? <Link href={`/experiments/results/${encodeURIComponent(run.publicationID)}`} className={buttonClassName({ variant: "default" })}>결과 보기</Link>
          : actions.edit ? <Link href={editHref} className={buttonClassName({ variant: "default" })}>수정</Link>
          : releasable(run) && <ReleaseAction run={run} onChanged={refetch} />}
        more={more}
      />
      {run.startedAt && (
        <Section card>
          <Fields columns={3} items={[
            { label: "시작", value: <TimeText at={run.startedAt} mode="detail" /> },
            { label: "종료", value: run.finishedAt ? <TimeText at={run.finishedAt} mode="detail" /> : null, reason: finished ? "미기록" : "진행 중" },
            { label: "소요", value: <Elapsed from={run.startedAt} to={run.finishedAt} /> },
          ]} />
        </Section>
      )}
      <PageTabs param="tab" defaultValue={defaultTab(run)} tabs={tabs} />
      {tab === "plan" ? <RunPlan run={run} publication={publication.data} />
        : tab === "cleanup" ? <RunCleanup run={run} publication={run.publicationID ? publication : null} />
        : tab === "target" ? <RunTargets targets={run.liveTarget} />
        : <RunProgress run={run} />}
      <RawDialog open={raw} onOpenChange={setRaw} value={run} filename={run.name} />
      <ConfirmDialog
        open={removing} onOpenChange={setRemoving} tone="destructive"
        title={actions.remove === "삭제" ? "실험 삭제" : "실험 중지"} target={run.name} confirmLabel={actions.remove}
        changes={[{ label: "상태", before: evaluationPhase(run.phase).label, after: actions.remove === "삭제" ? "삭제됨" : "중지 요청" }]}
        pending={remove.isPending} onConfirm={confirmRemove}
      />
    </Page>
  )
}

type TargetRow = { key: string; component: string; resource: string; image: string | null; digest: string | null; ready: boolean }
function RunTargets({ targets }: { targets: EvaluationLiveTarget[] | null | undefined }) {
  const rows: TargetRow[] = (targets ?? []).flatMap((target, index) => (target.liveImageIDs.length ? target.liveImageIDs : [""]).map((id, image) => {
    const [ref, digest] = id.split("@")
    return { key: `${index}/${image}`, component: target.component, resource: target.resource.name, image: ref ? ref.slice(ref.lastIndexOf("/") + 1) : null, digest: digest ?? null, ready: target.ready }
  }))
  return (
    <DataTable<TargetRow>
      label="평가 대상" rows={rows} rowKey={row => row.key} pageSize={0}
      empty={<EmptyState compact title="평가 대상 없음" />}
      columns={[
        { key: "component", header: "구성요소", cell: row => row.component },
        { key: "resource", header: "자원", cell: row => row.resource, width: "26%" },
        { key: "image", header: "이미지", cell: row => row.image, missingReason: "미관측" },
        { key: "digest", header: "다이제스트", cell: row => row.digest && <IdText value={row.digest} head={15} tail={4} />, missingReason: "미관측" },
        { key: "ready", header: "준비", cell: row => <StatusBadge tone={row.ready ? "ok" : "warn"} label={row.ready ? "준비됨" : "준비 안 됨"} />, width: "8rem" },
      ]}
    />
  )
}
