"use client"

import { useState } from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { DataTable, EmptyState, Fields, Page, PageHeader, QueryBoundary, RawDialog, Section, StatRow, StatTile, TimeText, type Column } from "@/components/kit"
import { useChillQuery, useSession } from "@/lib/client"
import { currentRunCondition, runSpec } from "@/lib/measurement-coverage"
import { exactRun, runFailure } from "@/lib/measurements"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { formatNumber, msNumber, wattsNumber } from "@/lib/format"
import { measurementStatus } from "@/lib/status-labels"
import type { NativeSample, Passport, ProfilingRun, RunsView } from "@/lib/read-types"

type PassportBody = Passport | { code: "EvidencePassportPending" }

const sampleColumns: Column<NativeSample>[] = [
  { key: "id", header: "배치 실행", numeric: true, cell: s => String(s.batchID), sortValue: s => Number(s.batchID) },
  { key: "size", header: "배치", unit: "건", numeric: true, cell: s => s.batchSize, sortValue: s => s.batchSize },
  { key: "pre", header: "전처리", unit: "ms", numeric: true, cell: s => formatNumber(s.preprocessMs, 2), sortValue: s => s.preprocessMs },
  { key: "backend", header: "추론", unit: "ms", numeric: true, cell: s => formatNumber(s.backendCallMs, 2), sortValue: s => s.backendCallMs },
  { key: "post", header: "후처리", unit: "ms", numeric: true, cell: s => formatNumber(s.postprocessMs, 2), sortValue: s => s.postprocessMs },
  { key: "service", header: "실행기", unit: "ms", numeric: true, cell: s => formatNumber(s.runnerServiceMs, 2), sortValue: s => s.runnerServiceMs },
  { key: "request", header: "요청", unit: "ms", numeric: true, cell: s => formatNumber(s.nativeRequestMs, 2), sortValue: s => s.nativeRequestMs },
]

export function MeasurementRunScreen({ name }: { name: string }) {
  const { config } = useSession()
  const uid = useSearchParams().get("uid")
  const [raw, setRaw] = useState(false)
  const lookup = useChillQuery<RunsView>(`v1/namespaces/${encodeURIComponent(config.namespace)}/profiling-runs?${new URLSearchParams({ q: name, limit: "200" })}`)
  const run = exactRun(lookup.data?.runs ?? [], config.namespace, name, uid)
  const idle = run ? runSpec(run).measurement?.workloadMode === "idle-model-loaded" : false
  const request = run?.ownerReferences?.find(owner => owner.kind === "BatchEnergyOptimization")
  const passport = useChillQuery<PassportBody>(`v1/namespaces/${encodeURIComponent(config.namespace)}/profiling-runs/${encodeURIComponent(name)}/evidence-passport`, { enabled: !!run && !idle && !!run.status.evidence })
  const body = passport.data && "measurement" in passport.data ? passport.data : undefined

  return (
    <Page>
      <PageHeader
        title={name}
        status={run ? measurementStatus(run.assessment?.displayStatus) : undefined}
        more={run ? [{ label: "원본 보기", onSelect: () => setRaw(true) }] : undefined}
        parents={request ? [{ label: "요청", href: "/measurements/requests" }, { label: request.name, href: `/measurements/requests/${encodeURIComponent(request.name)}?uid=${encodeURIComponent(request.uid)}` }] : undefined}
      />
      <QueryBoundary query={lookup} skeleton="tiles" isEmpty={() => !run} empty={<EmptyState title="회차를 찾을 수 없음" />}>
        {() => run && (
          <>
            {currentRunCondition(run, "ExecutionComplete")?.status === "False" && <Failure run={run} />}
            {idle ? (
              <EmptyState title="유휴 측정은 처리 성능이 없음" compact />
            ) : !run.status.evidence ? (
              !currentRunCondition(run, "ExecutionComplete") ? <EmptyState title="측정 중" compact />
              : currentRunCondition(run, "ExecutionComplete")?.status === "False" ? null : <EmptyState title="근거 없음" compact />
            ) : (
              <QueryBoundary query={passport} skeleton="tiles">
                {data => {
                  if (!("measurement" in data)) return <EmptyState title="근거 발행 대기" compact />
                  const { runtime, power } = data.measurement
                  return (
                    <StatRow columns={4}>
                      <StatTile label="처리량" value={formatNumber(runtime.throughputItemsPerSecond, 1)} unit="건/s" />
                      <StatTile label="지연 p50" value={msNumber(runtime.latencyMilliseconds.p50)} unit="ms" />
                      <StatTile label="지연 p95" value={msNumber(runtime.latencyMilliseconds.p95)} unit="ms" sub={`p99 ${msNumber(runtime.latencyMilliseconds.p99)} ms`} />
                      <StatTile label="평균 전력" value={power.watts ? wattsNumber(power.watts.mean) : null} unit="W" reason="전력 기록 없음" />
                    </StatRow>
                  )
                }}
              </QueryBoundary>
            )}
            <Conditions run={run} />
            {!!body?.measurement.runtime.nativeSamples?.length && (
              <DataTable label="배치 실행" title="배치 실행" rows={body.measurement.runtime.nativeSamples} rowKey={s => String(s.batchID)} columns={sampleColumns} defaultSort={{ key: "id", desc: false }} />
            )}
          </>
        )}
      </QueryBoundary>
      <RawDialog open={raw} onOpenChange={setRaw} value={{ run, passport: body }} filename={`run-${name}`} />
    </Page>
  )
}

function Conditions({ run }: { run: ProfilingRun }) {
  const spec = runSpec(run)
  const ended = currentRunCondition(run, "ExecutionComplete")?.lastTransitionTime
  const parent = run.ownerReferences?.find(owner => owner.kind === "BatchEnergyOptimization")
  const attempt = run.labels?.["edge.dacs.io/attempt"]
  return (
    <Section title="조건" card>
      <Fields columns={3} items={[
        { label: "측정 요청", value: parent && <Link className="text-primary hover:underline" href={`/measurements/requests/${encodeURIComponent(parent.name)}?uid=${encodeURIComponent(parent.uid)}`}>{parent.name}</Link>, reason: "연결된 요청 없음" },
        { label: "모델", value: spec.candidate?.modelSpec },
        { label: "장치 종류", value: spec.candidate && deviceClassLabel(spec.candidate.deviceClass) },
        { label: "장치", value: spec.targetNode },
        { label: "전력 모드", value: spec.candidate && powerModeLabel(spec.candidate.powerMode) },
        { label: "실행 경로", value: spec.candidate && executionPathLabel(spec.candidate.executionPath) },
        { label: "배치", value: spec.measurement?.batch == null ? null : `${spec.measurement.batch}건` },
        { label: "방식", value: spec.measurement?.workloadMode === "idle-model-loaded" ? "유휴" : "부하" },
        { label: "반복", value: attempt ? `${attempt}번째` : null, reason: "기록 없음" },
        { label: "생성", value: <TimeText at={run.creationTimestamp} mode="detail" /> },
        { label: "실행 종료", value: ended ? <TimeText at={ended} mode="detail" /> : null, reason: "아직 끝나지 않음" },
      ]} />
    </Section>
  )
}

function Failure({ run }: { run: ProfilingRun }) {
  const failure = runFailure(run)
  const lines = failure.log.split("\n").map(l => l.trimEnd()).filter(Boolean).slice(-6)
  return (
    <Section title="실패" card>
      <Fields columns={3} items={[
        { label: "원인", value: failure.title },
        { label: "종료 코드", value: failure.exitCode == null ? null : String(failure.exitCode), reason: "종료 코드 없음" },
      ]} />
      {lines.length > 0 && <pre tabIndex={0} aria-label="마지막 오류 줄" className="mt-3 max-h-48 overflow-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs leading-5">{lines.join("\n")}</pre>}
    </Section>
  )
}
