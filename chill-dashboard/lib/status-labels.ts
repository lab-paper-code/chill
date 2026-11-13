// The one place where server status codes become Korean labels and display tones.
// Korean words come from the existing tables (labels.ts statusLabel, measurement-coverage.ts); tones are display only.
import { statusLabel } from "./labels.ts"
import { coverageMeasurementStates } from "./measurement-coverage.ts"
import { DASH } from "./format.ts"

export type StatusTone = "ok" | "warn" | "fail" | "progress" | "idle"
/** `code` is set when the server code is unknown; screens show it only in the tooltip. */
export type StatusInfo = { label: string; tone: StatusTone; code?: string }

type Table = Record<string, StatusInfo>
const unknown = (code: string | null | undefined): StatusInfo => ({ label: DASH, tone: "idle", code: code || undefined })
const lookup = (table: Table, code: string | null | undefined): StatusInfo => (code && Object.hasOwn(table, code) ? table[code] : unknown(code))
const word = (code: string, tone: StatusTone): StatusInfo => ({ label: statusLabel(code), tone })

/** WorkloadView.readiness and LiveWorkload.readiness.reason. */
const readiness: Table = {
  Ready: { label: "준비됨", tone: "ok" },
  Waiting: word("Waiting", "progress"),
  Preparing: word("Preparing", "progress"),
  Changing: word("Changing", "progress"),
  Deleting: word("Deleting", "progress"),
  NativeNotReady: word("NativeNotReady", "progress"),
  Degraded: word("Degraded", "warn"),
  MissingEvidence: { label: "측정 결과 필요", tone: "warn" },
  MissingDataset: { label: "데이터셋 필요", tone: "warn" },
  MissingRuntime: { label: "실행 환경 필요", tone: "warn" },
  Failed: word("Failed", "fail"),
}
export const serviceReadiness = (code: string | null | undefined) => lookup(readiness, code)

/** WorkloadView.admission / LiveWorkload.admission. */
export function admissionStatus(open: boolean | null | undefined): StatusInfo {
  return open == null ? unknown(undefined) : open ? { label: "접수 중", tone: "ok" } : { label: "접수 중지", tone: "warn" }
}

/** SchedulerWorkloadState.state from the scheduler decision summary. */
const autonomy: Table = {
  Controlled: { label: "자율 제어", tone: "ok" },
  Fixed: { label: "고정", tone: "idle" },
  Disabled: { label: "꺼짐", tone: "idle" },
  MeasurementEvidenceMissing: { label: "측정 근거 부족", tone: "warn" },
}
export const autonomyState = (code: string | null | undefined) => lookup(autonomy, code)
/** LiveWorkload.autonomous (settings membership). */
export const autonomyEnabled = (on: boolean | null | undefined): StatusInfo =>
  on == null ? unknown(undefined) : on ? { label: "켜짐", tone: "ok" } : { label: "꺼짐", tone: "idle" }

/** LiveAction.status and scheduler action records. */
const action: Table = {
  planned: { label: "계획됨", tone: "idle" },
  starting: { label: "시작 중", tone: "progress" },
  running: { label: "진행 중", tone: "progress" },
  complete: { label: "완료", tone: "ok" },
  completed: { label: "완료", tone: "ok" },
  partial: { label: "일부 완료", tone: "warn" },
  failed: { label: "실패", tone: "fail" },
  cancel: { label: "취소", tone: "idle" },
  canceled: { label: "취소", tone: "idle" },
}
export const actionStatus = (code: string | null | undefined) => lookup(action, code)

/** MeasurementAssessment.displayStatus. */
const measurementTones: Record<keyof typeof coverageMeasurementStates, StatusTone> = {
  usable: "ok", review: "warn", insufficient: "warn", problem: "fail", running: "progress", unmeasured: "idle", unknown: "idle", unsupported: "idle",
}
const measurement: Table = Object.fromEntries(Object.entries(coverageMeasurementStates).map(([code, label]) => [code, { label, tone: measurementTones[code as keyof typeof measurementTones] }]))
export const measurementStatus = (code: string | null | undefined) => lookup(measurement, code)

/** Measurement request phase (BatchEnergyOptimization status.phase). */
const measurementPhase: Table = {
  Measuring: { label: "측정 중", tone: "progress" },
  Complete: word("Complete", "ok"),
  Failed: word("Failed", "fail"),
  Insufficient: { label: "근거 부족", tone: "warn" },
}
export const measurementPhaseStatus = (code: string | null | undefined) => lookup(measurementPhase, code)

/** Evaluation run phase. */
const evaluation: Table = {
  Pending: { label: "시작 전", tone: "idle" },
  Running: { label: "실행 중", tone: "progress" },
  Finishing: { label: "정리 중", tone: "progress" },
  Done: { label: "종료", tone: "ok" },
  Blocked: { label: "정리 실패", tone: "fail" },
}
export const evaluationPhase = (code: string | null | undefined) => lookup(evaluation, code)

/** Published experiment verdicts (outcome.measurement.verdict, per-service D1). */
const verdict: Table = {
  PASS: { label: "충족", tone: "ok" },
  OBSERVED_THRESHOLD_MET: { label: "충족", tone: "ok" },
  FAIL: { label: "미충족", tone: "fail" },
  NOT_EVALUATED: { label: "미평가", tone: "idle" },
}
export const experimentVerdict = (code: string | null | undefined) => lookup(verdict, code)

/** A server-computed SLO verdict (LiveSLOWindow.met). */
export const sloVerdict = (met: boolean | null | undefined): StatusInfo =>
  met == null ? unknown(undefined) : met ? { label: "충족", tone: "ok" } : { label: "초과", tone: "fail" }
