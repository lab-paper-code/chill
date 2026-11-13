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
  INVALID: { label: "무효", tone: "fail" },
  OVERLOAD_DESCRIPTIVE: { label: "과부하", tone: "warn" },
}
export const experimentVerdict = (code: string | null | undefined) => lookup(verdict, code)

/** Published experiment execution and cleanup axes (outcome.execution.status, outcome.cleanup.status). */
const experimentStep: Table = {
  Complete: { label: "완료", tone: "ok" },
  Failed: { label: "실패", tone: "fail" },
}
export const experimentStepStatus = (code: string | null | undefined) => lookup(experimentStep, code)

/** Published stage energy (kpis.power.energyStatus). */
const energy: Table = {
  MEASURED: { label: "계측 완료", tone: "ok" },
  PARTIAL: { label: "일부 계측", tone: "warn" },
  UNAVAILABLE: { label: "계측 없음", tone: "fail" },
}
export const energyStatus = (code: string | null | undefined) => lookup(energy, code)

/** A server-computed SLO verdict (LiveSLOWindow.met). */
export const sloVerdict = (met: boolean | null | undefined): StatusInfo =>
  met == null ? unknown(undefined) : met ? { label: "충족", tone: "ok" } : { label: "초과", tone: "fail" }

/** Scheduler decision status (internal/serving: Keep, Feasible, Pending, Overload). */
const decision: Table = {
  Keep: { label: "유지", tone: "idle" },
  Feasible: { label: "변경", tone: "progress" },
  Pending: { label: "보류", tone: "idle" },
  Overload: { label: "과부하", tone: "fail" },
  CapacityPending: { label: "용량 확인 중", tone: "progress" },
  CapacityRejected: { label: "용량 부족", tone: "fail" },
}
export const decisionStatus = (code: string | null | undefined) => lookup(decision, code)

/** SchedulerCandidate.status: whether a candidate plan can serve the demand. */
const candidate: Table = {
  Feasible: { label: "가능", tone: "ok" },
  Pending: { label: "보류", tone: "idle" },
  Overload: { label: "과부하", tone: "fail" },
}
export const candidateStatus = (code: string | null | undefined) => lookup(candidate, code)

/** Scheduler reason codes on decisions, candidates and per-service states. Codes arrive as "Code" or "Code:detail". */
const reason: Record<string, string> = {
  AutonomousControlOff: "자동 제어 꺼짐",
  NoCompletedMeasurementMatches: "측정 결과 없음",
  MissingEvidence: "측정 결과 없음",
  CapacityPending: "용량 확인 중",
  CapacityRejected: "용량 부족",
  NoPlanMeetsSLO: "목표 충족 구성 없음",
  RouteWeightExceedsCapacity: "분배가 용량 초과",
  TransitionGainWithinUncertainty: "절감량이 오차 범위 안",
  ReplacementUnpaid: "교체 비용 미회수",
  PartialObservationWindow: "관측 구간 부족",
  HeldAfterOverload: "과부하 뒤 구성 유지",
  InfeasibleResources: "자원 부족",
  FixedWorkload: "고정 서비스",
  PreparationFailed: "준비 실패",
  ExcludedNode: "제외 장치",
  PowerModeUnavailable: "전력 모드 미확인",
  InstanceUnavailable: "인스턴스 없음",
  MissingPoolInventory: "장치 목록 없음",
  UnknownOrFutureObservationWindow: "관측 구간 오류",
  ReservedScope: "예약 중",
}
/** Label for a scheduler reason code; unknown codes give "—" and keep the code for the tooltip. */
export function schedulerReason(code: string | null | undefined): { label: string; code?: string } {
  const key = code?.split(":", 1)[0] ?? ""
  return key && Object.hasOwn(reason, key) ? { label: reason[key] } : { label: DASH, code: code || undefined }
}

/** Evaluation run conditions ({type,status,reason}) and records. */
type EvaluationCondition = { status: string; reason?: string } | null | undefined
const evaluationReasons: Record<string, string> = {
  Complete: "완료", ProcedureComplete: "완료", MeasurementComplete: "측정 완료",
  ProcedureFailed: "절차 실패", ExecutionLost: "실행 유실", FrozenInputInvalid: "입력 정의 무효",
  TargetUnavailable: "평가 대상 없음", IdentityMismatch: "실행 대상 식별자 불일치",
  NoControlEffects: "복구할 제어 변경 없음", ResidualState: "잔여 자원 있음", CleanupFailed: "정리 실패",
  SLOFailed: "목표 미충족", GeneratorInvalid: "부하 생성 품질 미달", ObservationUnavailable: "관측 자료 없음",
  NOT_EVALUATED: "미평가", Returned: "반환됨", OperatorReleased: "운영자 확인 반환", PublicationFailed: "결과 게시 실패",
}
/** Label for an evaluation reason code; unknown codes give "—" and keep the code for the tooltip. */
export function evaluationReason(code: string | null | undefined): { label: string; code?: string } {
  return code && Object.hasOwn(evaluationReasons, code) ? { label: evaluationReasons[code] } : { label: DASH, code: code || undefined }
}
/** PoolAcquired: whether the run still holds its device pool. */
export function evaluationPool(c: EvaluationCondition): StatusInfo {
  if (!c) return unknown(undefined)
  if (c.status === "True") return { label: "점유 중", tone: "progress" }
  if (c.status === "False") return c.reason === "Returned" ? { label: "반환됨", tone: "ok" } : c.reason === "OperatorReleased" ? { label: "확인 후 반환", tone: "ok" } : { label: "미확보", tone: "idle" }
  return { label: "미확인", tone: "warn" }
}
/** MeasurementPassed: the server's measurement verdict of a run or stage. */
export function evaluationVerdict(c: EvaluationCondition): StatusInfo {
  if (!c) return unknown(undefined)
  if (c.status === "True") return { label: "목표 충족", tone: "ok" }
  if (c.status === "False") return c.reason === "GeneratorInvalid" ? { label: "측정 무효", tone: "fail" } : c.reason === "SLOFailed" ? { label: "목표 미충족", tone: "fail" } : { label: "판정 미통과", tone: "fail" }
  return c.reason === "NOT_EVALUATED" ? { label: "미평가", tone: "idle" } : { label: "판정 불가", tone: "warn" }
}
/** ExecutionComplete. */
export function evaluationExecution(c: EvaluationCondition): StatusInfo {
  if (!c) return unknown(undefined)
  return c.status === "True" ? { label: "실행 완료", tone: "ok" } : c.status === "False" ? { label: "실행 실패", tone: "fail" } : { label: "실행 중", tone: "progress" }
}
/** CleanupComplete. */
export function evaluationCleanup(c: EvaluationCondition): StatusInfo {
  if (!c) return unknown(undefined)
  if (c.status === "True") return { label: "완료", tone: "ok" }
  return c.status === "False" ? (c.reason === "ResidualState" ? { label: "잔여 자원", tone: "fail" } : { label: "미완료", tone: "warn" }) : { label: "미확인", tone: "idle" }
}
const loadJob: Table = { Active: { label: "진행 중", tone: "progress" }, Succeeded: { label: "완료", tone: "ok" }, Failed: { label: "실패", tone: "fail" } }
export const loadJobStatus = (code: string | null | undefined) => lookup(loadJob, code)
const injection: Table = { Executed: { label: "실행됨", tone: "ok" }, Cancelled: { label: "취소됨", tone: "idle" }, Invalid: { label: "무효", tone: "fail" } }
export const injectionOutcome = (code: string | null | undefined) => lookup(injection, code)
