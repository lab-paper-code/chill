"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { buttonClassName, ConfirmDialog, DataTable, EmptyState, Fields, Page, PageHeader, PageTabs, QueryBoundary, RawDialog, Section, StatusBadge, TimeText, useToast, type Column } from "@/components/kit"
import { Checkbox } from "@/components/ui/checkbox"
import { useChillMutation, useChillQuery, useSession } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { useMeasurementPlans } from "@/hooks/use-measurement-data"
import { currentRunCondition, runSpec } from "@/lib/measurement-coverage"
import { requestPhase } from "@/lib/measurements-coverage-matrix"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { parseSchedulerJSON, stringifySchedulerJSON } from "@/lib/scheduler"
import { formatNumber, formatSeconds, msNumber } from "@/lib/format"
import { measurementStatus } from "@/lib/status-labels"
import type { CampaignResult, MeasurementFailure, MeasurementPlanSpec, ProfilingRun, ResultRow, RunsView } from "@/lib/read-types"

const plansPath = (namespace: string) => `v1/namespaces/${encodeURIComponent(namespace)}/batch-energy-optimizations`

type Campaign = CampaignResult["campaign"] & { spec: MeasurementPlanSpec }

const resultColumns: Column<ResultRow>[] = [
  { key: "batch", header: "배치", unit: "건", numeric: true, cell: r => r.batch, sortValue: r => r.batch, csv: r => r.batch },
  { key: "p95", header: "지연 p95", unit: "ms", numeric: true, cell: r => msNumber(r.p95Milliseconds), sortValue: r => r.p95Milliseconds, missingReason: "지연 표본 없음" },
  { key: "total", header: "총 에너지", unit: "J/건", numeric: true, cell: r => formatNumber(r.totalJoulesPerItem, 3), sortValue: r => r.totalJoulesPerItem },
  { key: "incremental", header: "추가 에너지", unit: "J/건", numeric: true, cell: r => formatNumber(r.incrementalJoulesPerItem, 3), sortValue: r => r.incrementalJoulesPerItem },
  { key: "half", header: "신뢰 구간 ±", unit: "J/건", numeric: true, cell: r => formatNumber(r.confidence?.halfWidth, 3), sortValue: r => r.confidence?.halfWidth, missingReason: "신뢰 구간 없음" },
  { key: "active", header: "부하 반복", unit: "회", numeric: true, cell: r => r.activeTrialCount, sortValue: r => r.activeTrialCount },
  { key: "samples", header: "지연 표본", unit: "개", numeric: true, cell: r => formatNumber(r.latencySampleCount), sortValue: r => r.latencySampleCount },
]

const runColumns: Column<ProfilingRun>[] = [
  { key: "name", header: "회차", cell: r => r.name, sortValue: r => r.name, width: "28rem" },
  { key: "mode", header: "방식", cell: r => (runSpec(r).measurement?.workloadMode === "idle-model-loaded" ? "유휴" : "부하"), sortValue: r => runSpec(r).measurement?.workloadMode },
  { key: "batch", header: "배치", unit: "건", numeric: true, cell: r => runSpec(r).measurement?.batch, sortValue: r => runSpec(r).measurement?.batch },
  { key: "attempt", header: "반복", numeric: true, cell: r => r.labels?.["edge.dacs.io/attempt"] ?? null, sortValue: r => Number(r.labels?.["edge.dacs.io/attempt"]), missingReason: "기록 없음" },
  { key: "status", header: "상태", cell: r => <StatusBadge status={measurementStatus(r.assessment?.displayStatus)} />, sortValue: r => measurementStatus(r.assessment?.displayStatus).label },
  { key: "ended", header: "실행 종료", cell: r => { const at = currentRunCondition(r, "ExecutionComplete")?.lastTransitionTime; return at ? <TimeText at={at} mode="list" /> : null }, sortValue: r => currentRunCondition(r, "ExecutionComplete")?.lastTransitionTime, missingReason: "아직 끝나지 않음", width: "12rem" },
]

export function MeasurementRequestDetailScreen({ name }: { name: string }) {
  const { config } = useSession()
  const namespace = config.namespace
  const params = useSearchParams()
  const result = useChillQuery<CampaignResult>(`${plansPath(namespace)}/${encodeURIComponent(name)}/result`, { decoder: parseSchedulerJSON })
  // The result file can be unreadable while the request itself is fine: fall back to the stored request.
  const denied = [401, 403].includes(result.error?.status ?? 0)
  const errorBody = !denied ? (result.error?.data as Partial<CampaignResult> | null | undefined) : undefined
  const stored = useMeasurementPlans(namespace, !!result.error && !denied && !errorBody?.campaign)
  const plan = stored.data?.items?.find(p => p.metadata.name === name && (!params.get("uid") || p.metadata.uid === params.get("uid")))
  const data: Partial<CampaignResult> | undefined = result.data ?? (errorBody?.campaign ? errorBody : undefined)
  const campaign = (data?.campaign ?? (plan ? { ...plan.metadata, ...plan.status, spec: plan.spec } : undefined)) as Campaign | undefined
  const assessment = data?.assessment ?? plan?.assessment
  const rows = data?.resultRows ?? []
  const failures: MeasurementFailure[] = assessment?.failureSummary ?? []
  const failed = campaign?.phase === "Failed"

  const router = useRouter()
  const client = useQueryClient()
  const { toast } = useToast()
  const remove = useChillMutation()
  const [raw, setRaw] = useState(false)
  const [confirm, setConfirm] = useState(false)

  if (!campaign) {
    return (
      <Page>
        <PageHeader title={name} />
        <QueryBoundary query={result} skeleton="block">{() => null}</QueryBoundary>
      </Page>
    )
  }

  const spec = campaign.spec
  const candidate = spec.state.candidate
  const uid = campaign.uid
  const trials = campaign.completedTrials ?? 0
  const download = () => {
    const text = stringifySchedulerJSON({ apiVersion: "edge.dacs.io/v1alpha1", kind: "BatchEnergyOptimization", metadata: { name, namespace }, spec }, 2)
    const url = URL.createObjectURL(new Blob([text], { type: "application/json" }))
    Object.assign(document.createElement("a"), { href: url, download: `${name}-request.json` }).click()
    URL.revokeObjectURL(url)
  }
  const confirmDelete = () => remove.mutate(
    { path: `${plansPath(namespace)}/${encodeURIComponent(name)}?uid=${encodeURIComponent(uid)}`, method: "DELETE", version: campaign.resourceVersion },
    {
      onSuccess: () => {
        setConfirm(false)
        void client.invalidateQueries({ predicate: q => typeof q.queryKey[1] === "string" && q.queryKey[1].startsWith(plansPath(namespace)) })
        toast({ title: "측정 요청 삭제됨", description: name, type: "success" })
        router.push("/measurements/requests")
      },
      onError: error => {
        setConfirm(false)
        const code = error instanceof ApiError ? (error.data as { code?: unknown } | null)?.code : undefined
        const text = error.unknownOutcome || error.status === 0 ? "결과 미확인" : error.status === 403 ? "삭제 권한 없음" : code === "MeasurementInUse" ? "운영 중인 측정이라 삭제 불가" : error.status === 409 ? "그 사이 요청이 바뀜" : "삭제 실패"
        toast({ title: text, description: error.unknownOutcome || error.status === 0 ? "목록에서 상태 확인 필요" : name, type: "error" })
      },
    },
  )

  const tabs = [
    ...(failed ? [{ value: "failure", label: "실패" }] : []),
    { value: "results", label: "결과", count: rows.length },
    { value: "runs", label: "회차" },
    { value: "conditions", label: "조건" },
  ]
  const defaultTab = failed ? "failure" : "results"
  const requested = params.get("tab")
  const tab = tabs.find(t => t.value === requested)?.value ?? defaultTab
  const cause = failures[0]
  const sameTarget = `/measurements/results?${new URLSearchParams({ kind: "throughput", model: candidate.modelSpec, node: spec.state.targetNode })}`

  return (
    <Page>
      <PageHeader
        title={name}
        status={requestPhase(campaign.phase)}
        actions={<Link href={`/measurements/requests/new?${new URLSearchParams({ from: name, fromUID: uid })}`} className={buttonClassName({ variant: "default" })}>같은 조건으로 요청</Link>}
        more={[
          { label: "요청 문서 내려받기", onSelect: download },
          { label: "원본 보기", onSelect: () => setRaw(true) },
          { label: "삭제", destructive: true, disabled: campaign.phase === "Measuring", disabledReason: campaign.phase === "Measuring" ? "측정 중" : undefined, onSelect: () => setConfirm(true) },
        ]}
      />
      {failed && cause && <p className="text-sm text-muted-foreground">{cause.label ?? cause.message}{cause.firstErrorLine ? `: ${cause.firstErrorLine.slice((cause.firstErrorTime ?? "").length).trim()}` : ""}</p>}
      <PageTabs param="tab" defaultValue={defaultTab} tabs={tabs} />

      {tab === "results" && (
        <Section title="배치별 결과" actions={<Link href={sameTarget} className={buttonClassName({ variant: "outline", size: "sm" })}>같은 대상 결과</Link>}>
          {rows.length === 0 ? (
            <EmptyState title={result.error && !result.data ? "결과 파일을 읽지 못함" : campaign.phase === "Measuring" ? "측정 중" : failed ? "결과 없음" : "저장된 결과 없음"} compact />
          ) : (
            <DataTable
              label="배치별 결과"
              rows={rows}
              rowKey={r => r.id}
              columns={resultColumns}
              defaultSort={{ key: "batch", desc: false }}
              pageSize={0}
              narrow="scroll"
              download={{ filename: `${name}-results` }}
            />
          )}
        </Section>
      )}

      {tab === "runs" && <RunsTab namespace={namespace} uid={uid} />}

      {tab === "conditions" && (
        <Section title="측정 조건" card>
          <Fields columns={3} items={[
            { label: "모델", value: candidate.modelSpec },
            { label: "전처리기", value: candidate.preprocessor },
            { label: "데이터셋", value: spec.activeProtocol.dataset as string | undefined },
            { label: "장치 종류", value: deviceClassLabel(candidate.deviceClass) },
            { label: "장치", value: spec.state.targetNode },
            { label: "전력 모드", value: powerModeLabel(candidate.powerMode) },
            { label: "실행 경로", value: executionPathLabel(candidate.executionPath) },
            { label: "배치별 반복", value: `${spec.trialsPerBatch}회` },
            { label: "재시도 한도", value: `${spec.retryLimit}회` },
            { label: "실행 측정", value: formatSeconds(spec.activeProtocol.durationSeconds) },
            { label: "예열", value: `${spec.activeProtocol.warmupIterations}회` },
            { label: "유휴 측정", value: formatSeconds(spec.idleProtocol.durationSeconds) },
            { label: "신뢰 수준", value: spec.pointPolicy?.confidenceLevel ? `${Number(spec.pointPolicy.confidenceLevel) * 100}%` : null },
            ...(plan ? [{ label: "생성", value: <TimeText at={plan.metadata.creationTimestamp} mode="detail" /> }] : []),
          ]} />
        </Section>
      )}

      {tab === "failure" && failed && (
        <Section title="실패 원인">
          <DataTable
            label="실패 원인"
            rows={failures}
            rowKey={f => f.runId || `${f.batch}-${f.code}`}
            pageSize={0}
            columns={[
              { key: "run", header: "연결 회차", cell: f => (f.runName ? <Link className="text-primary hover:underline" href={`/measurements/runs/${encodeURIComponent(f.runName)}?uid=${encodeURIComponent(f.runId)}`}>{f.runName}</Link> : null), missingReason: "연결된 회차 없음", width: "24rem" },
              { key: "batch", header: "배치", unit: "건", numeric: true, cell: f => f.batch },
              { key: "code", header: "분류", cell: f => f.label },
              { key: "exit", header: "종료 코드", numeric: true, cell: f => f.exitCode, missingReason: "종료 코드 없음" },
              // The summary carries the first error line only for some failures; otherwise the linked run detail holds the log.
              ...(failures.some(f => f.firstErrorLine) ? [{ key: "line", header: "첫 오류", cell: (f: MeasurementFailure) => f.firstErrorLine?.slice((f.firstErrorTime ?? "").length).trim(), missing: (f: MeasurementFailure) => f.runName ? "연결 회차에서 확인" : "기록 없음" }] : []),
            ] satisfies Column<MeasurementFailure>[]}
            empty={<EmptyState title="실패 기록 없음" compact />}
          />
        </Section>
      )}

      <RawDialog open={raw} onOpenChange={setRaw} value={data ?? plan} filename={`measurement-${name}`} />
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title="측정 요청 삭제"
        target={name}
        confirmLabel="영구 삭제"
        tone="destructive"
        changes={[{ label: "측정 요청", before: "있음", after: "삭제" }, { label: "완료 회차", before: `${trials}개`, after: "삭제" }, { label: "결과와 근거 파일", before: "있음", after: "영구 삭제" }]}
        pending={remove.isPending}
        onConfirm={confirmDelete}
      >
        <label className="flex items-center gap-2 text-sm">
          <Checkbox required aria-required="true" />
          근거 파일 영구 삭제 확인
        </label>
      </ConfirmDialog>
    </Page>
  )
}

function RunsTab({ namespace, uid }: { namespace: string; uid: string }) {
  const runs = useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(namespace)}/profiling-runs?${new URLSearchParams({ parentUID: uid, limit: "200" })}`)
  return (
    <QueryBoundary query={runs} isEmpty={d => d.runs.length === 0} empty={<EmptyState title="회차 없음" />}>
      {data => (
        <DataTable
          label="회차"
          rows={data.runs}
          rowKey={r => r.uid}
          rowHref={r => `/measurements/runs/${encodeURIComponent(r.name)}?uid=${encodeURIComponent(r.uid)}`}
          columns={runColumns}
          defaultSort={{ key: "ended", desc: false }}
          pageSize={0}
        />
      )}
    </QueryBoundary>
  )
}
