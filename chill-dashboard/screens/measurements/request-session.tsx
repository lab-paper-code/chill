"use client"

import { useState } from "react"
import { DataTable, DetailDrawer, EmptyState, Fields, InfoHint, Section, StatusBadge, TimeSeries, type Column } from "@/components/kit"
import { deviceClassLabel, executionPathLabel, powerModeLabel } from "@/lib/display-labels"
import { formatBytes, formatDurationNanos, formatNumber, formatSeconds, rateNumber } from "@/lib/format"
import { sessionLevelEndStatus, sessionLevelKind, sessionOutcomeStatus, sessionParameterStatus } from "@/lib/status-labels"
import type { MeasurementSessionSpec, SessionBatchView, SessionLevel, SessionParameter, SessionResultView } from "@/lib/read-types"

type Parameter = { key: string; label: string; info: string; value: SessionParameter }

const valueText = (p: SessionParameter, digits: number) => (p.value === undefined ? null : formatNumber(p.value, digits))
const rangeText = (p: SessionParameter, digits: number) => (p.range ? `${formatNumber(p.range[0], digits)} ~ ${formatNumber(p.range[1], digits)}` : null)
const parameterCell = (p: SessionParameter) => <StatusBadge status={sessionParameterStatus(p.status)} reason={p.reason || undefined} />

const parameterColumns: Column<Parameter>[] = [
  { key: "label", header: "항목", cell: r => <span className="inline-flex items-center gap-1">{r.label}<InfoHint text={r.info} /></span> },
  { key: "value", header: "값", numeric: true, cell: r => valueText(r.value, 2), missingReason: "구분 안 됨" },
  { key: "range", header: "범위", numeric: true, cell: r => rangeText(r.value, 2), missingReason: "범위 없음" },
  { key: "unit", header: "단위", cell: r => r.value.unit },
  { key: "status", header: "구분", cell: r => parameterCell(r.value) },
]

const batchColumns: Column<SessionBatchView>[] = [
  { key: "batch", header: "배치", unit: "건", numeric: true, cell: b => b.batch, sortValue: b => b.batch },
  { key: "records", header: "처리 시간 기록 수", unit: "개", numeric: true, cell: b => formatNumber(b.serviceRecordCount), sortValue: b => b.serviceRecordCount },
  { key: "rate", header: "포화 처리율", unit: "req/s", numeric: true, cell: b => (b.saturationInputRate === undefined ? null : rateNumber(b.saturationInputRate)), missingReason: "포화 수준 없음" },
  { key: "energy", header: "요청당 에너지", unit: "J", numeric: true, cell: b => valueText(b.joulesPerInput, 3), missing: b => b.joulesPerInput.range ? `구분 안 됨, 범위 ${rangeText(b.joulesPerInput, 3)} J` : "구분 안 됨" },
  { key: "energyStatus", header: "에너지 구분", cell: b => parameterCell(b.joulesPerInput) },
  { key: "memory", header: "메모리 최댓값", numeric: true, cell: b => formatBytes(b.peakMemoryBytes), sortValue: b => b.peakMemoryBytes },
]

const levelColumns: Column<SessionLevel>[] = [
  { key: "kind", header: "수준", cell: l => sessionLevelKind(l.kind).label },
  { key: "rate", header: "요청률", unit: "req/s", numeric: true, cell: l => rateNumber(l.inputRate) },
  { key: "watts", header: "평균 전력", unit: "W", numeric: true, cell: l => (l.meanWatts === undefined ? null : `${formatNumber(l.meanWatts, 1)} ± ${formatNumber(l.meanWattsStandardError, 2)}`), missingReason: "전력 관측이 최소 시간 미만" },
  { key: "samples", header: "전력 표본 수", unit: "개", numeric: true, cell: l => l.powerSamples },
  { key: "inputs", header: "완료 요청", unit: "건", numeric: true, cell: l => formatNumber(l.completedInputs) },
  {
    key: "end", header: "종료 사유",
    cell: l => <StatusBadge status={sessionLevelEndStatus(l.endReason)} reason={l.predictedSeconds !== undefined ? `예상 ${formatSeconds(l.predictedSeconds)}, 한도 ${formatSeconds(l.limitSeconds)}` : undefined} />,
  },
]

/** Conclusion, power parameters, batches, post-load power and conditions of one session result. */
export function SessionResultSection({ session, phase, outcome, reason }: { session: SessionResultView; phase?: string; outcome?: string; reason?: string }) {
  const [selected, setSelected] = useState<number | null>(null)
  const batch = session.batches.find(b => b.batch === selected)
  const power = session.power
  const parameters: Parameter[] = [
    { key: "idle", label: "유휴 전력", info: "모델을 올린 채 요청이 없고 전력이 가라앉은 노드 전력", value: power.idleWatts },
    { key: "awake", label: "깨어 있는 전력 증가", info: "실행 중이거나 실행 뒤 유지 시간 안에 더해지는 전력", value: power.awakeStepWatts },
    { key: "hold", label: "유지 시간", info: "실행이 끝난 뒤 장치가 깨어 있는 시간", value: power.holdSeconds },
  ]
  const idle = power.idleWatts.value
  const condition = session.condition
  const settings = session.settings
  return (
    <>
      <SessionConclusion batches={session.batches.map(b => b.batch)} stop={session.stop} phase={phase} outcome={outcome} reason={reason} />
      <Section title="전력 매개변수">
        <DataTable label="전력 매개변수" rows={parameters} rowKey={r => r.key} columns={parameterColumns} pageSize={0} narrow="scroll" />
      </Section>
      <Section title="배치별 결과">
        <DataTable label="배치별 결과" rows={session.batches} rowKey={b => String(b.batch)} columns={batchColumns} pageSize={0} narrow="scroll"
          defaultSort={{ key: "batch", desc: false }} onRowClick={b => setSelected(b.batch)} selectedKey={selected === null ? undefined : String(selected)} />
      </Section>
      <Section title="부하 뒤 전력" card>
        {power.postLoad.length === 0 ? <EmptyState title="부하 뒤 관측 없음" compact /> : (
          <TimeSeries label="부하 뒤 노드 전력" x="seconds" unit="W" digits={1} data={power.postLoad.map(([t, w]) => ({ t, w }))}
            series={[{ key: "w", label: "노드 전력" }]} reference={idle === undefined ? undefined : { value: idle, label: "유휴 전력" }} />
        )}
      </Section>
      <Section title="측정 조건" card>
        <Fields columns={3} items={[
          { label: "모델", value: condition.logicalModel },
          { label: "장치 종류", value: deviceClassLabel(condition.deviceClass) },
          { label: "장치", value: condition.nodeName },
          { label: "전력 모드", value: powerModeLabel(condition.powerMode) },
          { label: "실행 경로", value: condition.artifact.executionPath ? executionPathLabel(condition.artifact.executionPath) : null, reason: "기록 없음" },
          { label: "실행 이미지", value: condition.runtimeImage },
          { label: "전력계 지연", value: formatSeconds(settings.meterLagSeconds) },
          { label: "최소 전력 관측", value: formatSeconds(settings.minimumPowerWindowSeconds) },
          { label: "중간 수준 비율", value: formatNumber(settings.midLevelRatio, 2) },
          { label: "저부하 평균 간격", value: formatSeconds(settings.sparseMeanGapSeconds) },
          { label: "수준 시간 한도", value: formatSeconds(settings.levelDurationLimitSeconds) },
          { label: "부하 뒤 관측", value: formatSeconds(settings.postLoadWindowSeconds) },
          { label: "최소 표본 수", value: `${settings.minimumSamples}개`, info: "수준마다 모은 처리 시간 기록 수" },
          { label: "신뢰 수준", value: String(settings.confidenceLevel) },
        ]} />
      </Section>
      <DetailDrawer open={!!batch} onOpenChange={open => { if (!open) setSelected(null) }} title={batch ? `배치 ${batch.batch} 수준` : ""}>
        {batch && <>
          <DataTable label={`배치 ${batch.batch} 수준`} rows={batch.levels} rowKey={l => l.id} columns={levelColumns} pageSize={0} narrow="scroll" empty={<EmptyState title="수준 없음" compact />} />
          <Fields columns={2} items={[
            { label: "점유 표본 수", value: `${batch.dispatchOccupancy.count}개` },
            { label: "점유 중앙값", value: batch.dispatchOccupancy.medianNanos === undefined ? null : formatDurationNanos(batch.dispatchOccupancy.medianNanos), reason: "점유 표본 없음", info: "요청 하나가 실행기를 차지한 시간의 중앙값" },
            { label: "메모리 기준", value: batch.memoryKind === "peak" ? "최댓값" : batch.memoryKind === "current-sample-maximum" ? "관측 최댓값" : null, reason: batch.memoryKind ? "기타" : "기록 없음" },
          ]} />
        </>}
      </DetailDrawer>
    </>
  )
}

function SessionConclusion({ batches, stop, phase, outcome, reason }: { batches: number[]; stop?: SessionResultView["stop"]; phase?: string; outcome?: string; reason?: string }) {
  const failed = phase === "Failed"
  return (
    <Section title="결론" card>
      <Fields columns={3} items={[
        { label: "결과", value: <StatusBadge status={failed ? sessionOutcomeStatus(outcome) : phase === "Complete" ? { label: "완료", tone: "ok" } : { label: "측정 중", tone: "progress" }} /> },
        ...(failed ? [{ label: "사유", value: reason || null, reason: "기록 없음" }] : [
          { label: "측정 배치", value: batches.length ? batches.join(", ") : null, reason: "측정한 배치 없음" },
          { label: "중단", value: stop ? <StatusBadge status={sessionOutcomeStatus(stop.class)} label={`배치 ${stop.batch} ${sessionOutcomeStatus(stop.class).label}`} reason={stop.reason} /> : null, reason: "최대 배치까지 측정" },
        ]),
      ]} />
    </Section>
  )
}

/** A session request that has no readable result: its status and the reason sentence. */
export function SessionStatusSection({ phase, outcome, reason, previousFormat }: { phase?: string; outcome?: string; reason?: string; previousFormat?: boolean }) {
  if (previousFormat) return (
    <Section title="결론" card>
      <Fields columns={2} items={[
        { label: "결과", value: <StatusBadge tone="warn" label="이전 형식의 측정 결과" /> },
        { label: "조치", value: "삭제 후 다시 측정 필요", info: "이전 측정 방식이 저장한 결과는 읽지 않습니다" },
      ]} />
    </Section>
  )
  return <SessionConclusion batches={[]} phase={phase} outcome={outcome} reason={reason} />
}

/** Session settings of a request, read from its spec. */
export function sessionSettingFields(session: MeasurementSessionSpec) {
  return [
    { label: "전력계 지연", value: formatSeconds(session.meterLagSeconds) },
    { label: "최소 전력 관측", value: formatSeconds(session.minimumPowerWindowSeconds) },
    { label: "중간 수준 비율", value: formatNumber(session.midLevelRatio, 2) },
    { label: "저부하 평균 간격", value: formatSeconds(session.sparseMeanGapSeconds) },
    { label: "수준 시간 한도", value: formatSeconds(session.levelDurationLimitSeconds) },
    { label: "부하 뒤 관측", value: formatSeconds(session.postLoadWindowSeconds) },
    { label: "신뢰 수준", value: session.confidenceLevel },
    { label: "요청 수명", value: formatSeconds(session.requestLifetimeSeconds) },
  ]
}
