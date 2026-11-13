// The one place where server status codes become Korean labels and display tones.
// Tones are display only. Labels are terse words: no sentences, no internal names.
import { DASH } from "./format.ts"

export type StatusTone = "ok" | "warn" | "fail" | "progress" | "idle"
/** `code` is set when the server code is unknown; screens show it only in the tooltip. */
export type StatusInfo = { label: string; tone: StatusTone; code?: string }

type Table = Record<string, StatusInfo>
/** No code gives "—"; an unknown code gives "기타" and keeps the code for the tooltip. */
const unknown = (code: string | null | undefined): StatusInfo => (code ? { label: "기타", tone: "idle", code } : { label: DASH, tone: "idle" })
const lookup = (table: Table, code: string | null | undefined): StatusInfo => (code && Object.hasOwn(table, code) ? table[code] : unknown(code))

/** WorkloadView.readiness and LiveWorkload.readiness.reason. */
const readiness: Table = {
  Ready: { label: "준비됨", tone: "ok" },
  Waiting: { label: "준비 중", tone: "progress" },
  Preparing: { label: "실행 준비 중", tone: "progress" },
  Changing: { label: "변경 중", tone: "progress" },
  Deleting: { label: "삭제 중", tone: "progress" },
  NativeNotReady: { label: "실행 준비 중", tone: "progress" },
  Degraded: { label: "확인 필요", tone: "warn" },
  MissingEvidence: { label: "측정 결과 필요", tone: "warn" },
  MissingDataset: { label: "데이터셋 필요", tone: "warn" },
  MissingRuntime: { label: "실행 환경 필요", tone: "warn" },
  Failed: { label: "실패", tone: "fail" },
}
export const serviceReadiness = (code: string | null | undefined) => lookup(readiness, code)

/** WorkloadSpec.admission.policy. */
const admissionPolicies: Record<string, string> = { Reject: "거부", Delay: "지연 허용" }
export const admissionPolicy = (code: string | null | undefined): string => (code && Object.hasOwn(admissionPolicies, code) ? admissionPolicies[code] : unknown(code).label)

/** WorkloadView.admission / LiveWorkload.admission. */
export function admissionStatus(open: boolean | null | undefined): StatusInfo {
  return open == null ? unknown(undefined) : open ? { label: "접수 중", tone: "ok" } : { label: "접수 중지", tone: "warn" }
}

/** SchedulerWorkloadState.state from the scheduler decision summary. */
const autonomy: Table = {
  Controlled: { label: "켜짐", tone: "ok" },
  Fixed: { label: "고정", tone: "idle" },
  Disabled: { label: "꺼짐", tone: "idle" },
  MeasurementEvidenceMissing: { label: "측정 결과 없음", tone: "warn" },
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

/** MeasurementAssessment.displayStatus: one word per measurement state; a failed measurement is "실패". */
export const coverageMeasurementStates = { usable: "측정 있음", review: "검토 필요", insufficient: "결과 부족", problem: "실패", running: "측정 중", unmeasured: "미측정", unknown: "미확인", unsupported: "지원 안 함" }
const measurementTones: Record<keyof typeof coverageMeasurementStates, StatusTone> = {
  usable: "ok", review: "warn", insufficient: "warn", problem: "fail", running: "progress", unmeasured: "idle", unknown: "idle", unsupported: "idle",
}
const measurement: Table = Object.fromEntries(Object.entries(coverageMeasurementStates).map(([code, label]) => [code, { label, tone: measurementTones[code as keyof typeof measurementTones] }]))
export const measurementStatus = (code: string | null | undefined) => lookup(measurement, code)

/** Measurement request phase (BatchEnergyOptimization status.phase). */
const measurementPhase: Table = {
  Measuring: { label: "측정 중", tone: "progress" },
  Complete: { label: "완료", tone: "ok" },
  Failed: { label: "실패", tone: "fail" },
  Insufficient: { label: "결과 부족", tone: "warn" },
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
  PASS: { label: "목표 충족", tone: "ok" },
  OBSERVED_THRESHOLD_MET: { label: "목표 충족", tone: "ok" },
  FAIL: { label: "목표 미달", tone: "fail" },
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

/** A server-computed live latency window (LiveSLOWindow.met): a miss is the live problem "목표 초과". */
export const sloVerdict = (met: boolean | null | undefined): StatusInfo =>
  met == null ? unknown(undefined) : met ? { label: "목표 충족", tone: "ok" } : { label: "목표 초과", tone: "fail" }

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
/** Label for a scheduler reason code; unknown codes give "기타 사유" and keep the code for the tooltip. */
export function schedulerReason(code: string | null | undefined): { label: string; code?: string } {
  const key = code?.split(":", 1)[0] ?? ""
  return key && Object.hasOwn(reason, key) ? { label: reason[key] } : code ? { label: "기타 사유", code } : { label: DASH }
}

/** Evaluation run conditions ({type,status,reason}) and records. */
type EvaluationCondition = { status: string; reason?: string } | null | undefined
const evaluationReasons: Record<string, string> = {
  Complete: "완료", ProcedureComplete: "완료", MeasurementComplete: "측정 완료",
  ProcedureFailed: "절차 실패", ExecutionLost: "실행 유실", FrozenInputInvalid: "입력 정의 무효",
  TargetUnavailable: "평가 대상 없음", IdentityMismatch: "실행 대상 식별자 불일치",
  NoControlEffects: "복구할 제어 변경 없음", ResidualState: "잔여 자원 있음", CleanupFailed: "정리 실패",
  SLOFailed: "목표 미달", GeneratorInvalid: "부하 생성 품질 미달", ObservationUnavailable: "관측 자료 없음",
  NOT_EVALUATED: "미평가", Returned: "반환됨", OperatorReleased: "확인 후 반환", PublicationFailed: "결과 게시 실패",
}
/** Label for an evaluation reason code; unknown codes give "기타 사유" and keep the code for the tooltip. */
export function evaluationReason(code: string | null | undefined): { label: string; code?: string } {
  return code && Object.hasOwn(evaluationReasons, code) ? { label: evaluationReasons[code] } : code ? { label: "기타 사유", code } : { label: DASH }
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
  if (c.status === "False") return c.reason === "GeneratorInvalid" ? { label: "측정 무효", tone: "fail" } : c.reason === "SLOFailed" ? { label: "목표 미달", tone: "fail" } : { label: "판정 미통과", tone: "fail" }
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
const injection: Table = { Executed: { label: "실행됨", tone: "ok" }, Cancelled: { label: "취소", tone: "idle" }, Invalid: { label: "무효", tone: "fail" } }
export const injectionOutcome = (code: string | null | undefined) => lookup(injection, code)

/** Reason codes of coverage cells, compatibility checks and assessments; the server label wins. */
const coverageReasons: Record<string, string> = {
  ArtifactArchitectureMismatch: "아키텍처 불일치",
  DeclarationMismatch: "실행 경로 선언 불일치",
  RuntimeDeclarationMissing: "실행 환경 선언 없음",
  AcceptedEvidenceUnavailable: "검증된 측정 근거 없음",
  InvalidEvidence: "측정 근거 검증 실패",
  PlannedConditionUnconfirmed: "실행 조건 미확인",
  TargetBatchMissing: "목표 배치 부족",
}
export const coverageReason = (reason: { code: string; label?: string }): string => reason.label ?? coverageReasons[reason.code] ?? "기타 사유"

/**
 * Short phrase for a server code that explains a state or a failure: readiness reasons, API errors, measurement failures.
 * Sources: internal/serving (workload_view.go, runtime.go, httpapi, kube), internal/queryapi, lib/relay.ts, internal/profiling.
 */
const codeMessages: Record<string, string> = {
  // Workload readiness and reasons
  Waiting: "준비 중", Preparing: "실행 준비 중", Changing: "설정 변경 중", Failed: "마지막 작업 실패",
  AppliedDefinitionUnavailable: "서버 적용 전", MissingEvidence: "측정 결과 필요", MissingDataset: "데이터셋 필요", MissingRuntime: "실행 환경 필요",
  NativeNotReady: "실행기 준비 중", PolicyAckPending: "반영 대기", ObservationUnavailable: "요청 기록 저장 불가",
  RestartDrain: "서버 재시작, 이전 요청 마무리 중", RestartUnconfirmed: "서버 재시작 후 실행기 미확인", InstanceStopped: "실행기 멈춤",
  NodeDrained: "장치에서 실행기 비움", NoCurrentReadiness: "현재 구성에 없는 실행기", ConditionChangedBeforeDrain: "실행 조건 변경, 마무리 대기",
  ManagementOperationFailed: "설정 변경 실패", DrainTimeout: "요청 마무리 시간 초과", SharedUpdateFailed: "함께 바꾸던 서비스 변경 실패",
  // Serving errors
  Unauthenticated: "접속 정보 확인 실패", Forbidden: "권한 없음", AuthorizationUnavailable: "권한 확인 불가",
  NotFound: "항목 없음", WorkloadNotFound: "등록되지 않은 서비스", PoolNotFound: "실행 그룹 없음", AlreadyExists: "같은 이름 있음",
  InvalidBody: "요청 형식 오류", InvalidInput: "입력 값 오류", InvalidWorkload: "서비스 설정 오류", InvalidModel: "모델 설정 오류",
  InvalidModelReference: "모델이나 모델 파일 없음", InvalidPreprocessorReference: "전처리기 없음", InvalidDatasetReference: "데이터셋 없음",
  InvalidPreprocessor: "전처리기 설정 오류", InvalidDataset: "데이터셋 설정 오류", IncompatibleDataset: "데이터셋에 전처리기 입력 필드 없음",
  InvalidQuery: "조회 조건 오류", InvalidRequestTimeout: "요청 제한 시간 범위 초과",
  ResourceVersionRequired: "최신 상태 확인 필요", ResourceVersionConflict: "다른 변경이 먼저 저장됨", OperationConflict: "진행 중인 변경 있음",
  RegistrationConflict: "이전 등록 정리 전", CatalogIdentityConflict: "함께 바꾸던 대상이 바뀜",
  ModelReferenced: "사용 중인 모델", ModelArtifactReferenced: "전처리기가 쓰는 모델 파일", PreprocessorReferenced: "서비스가 쓰는 전처리기", DatasetReferenced: "서비스가 쓰는 데이터셋",
  ModelDeleting: "모델 삭제 중", PreprocessorDeleting: "전처리기 삭제 중", DatasetDeleting: "데이터셋 삭제 중",
  DefinitionChanged: "요청 중 정의 변경됨", WorkloadChanged: "요청 중 서비스 변경됨", IdentityUnavailable: "등록 정보 확인 불가",
  AppliedSnapshotUnavailable: "적용 기록 읽기 실패", AppliedStateUnconfirmed: "적용 상태 미확인", EntrypointRestarted: "서버 재시작으로 작업 중단",
  DrainIncomplete: "이전 요청 남음", StillAttached: "실행기 연결 남음", InvalidDistribution: "실행기 구성 오류", ManagementUnavailable: "서버 시작 중",
  ServiceUnavailable: "처리 불가", QueueFull: "대기열 가득 참", GatewayOverloaded: "서버 메모리 한도", RequestTimeout: "응답 시간 초과", RequestCanceled: "요청 취소",
  DeadlineInfeasible: "기한 내 처리 불가", PreprocessFailed: "전처리 실패", BackendError: "모델 실행 오류", BackendUnavailable: "실행기 연결 실패",
  BackendResponseInvalid: "실행기 응답 오류", BackendResponseTooLarge: "응답 크기 초과", BackendContextMismatch: "실행기 응답 불일치", ResponseDeliveryFailed: "응답 전달 실패",
  IncompleteObservation: "기간 내 기록 누락", InvalidObservation: "기록 시각 누락",
  SchedulerUnavailable: "스케줄러 조회 불가", MeasurementUnavailable: "측정 조회 불가",
  MeasurementPreflightUnavailable: "측정 사전 확인 실패", MeasurementPreflightRejected: "측정 계획 불가", MeasurementInUse: "사용 중인 측정",
  MeasurementAuditUnavailable: "측정 변경 기록 불가", MeasurementAuditIncomplete: "측정 변경 기록 미완료", MeasurementEvidenceUnavailable: "측정 기록 저장소 접근 불가",
  MeasurementEvidenceDeletionIncomplete: "측정 기록 일부만 삭제", MeasurementDeletionIncomplete: "측정 삭제 미확인",
  // Profile and cost issues
  PreparationUnavailable: "실행 준비 불가", CostEvidenceInvalid: "비용 측정 기록 검증 실패", MeterUndeclared: "운영 설정에 없는 계측기",
  InvalidEvidence: "측정 기록 검증 실패", MeasurementEvidenceMissing: "측정 기록 부족", MemoryEvidenceMissing: "메모리 기록 없음",
  // Query API errors
  SystemsUnavailable: "시스템 상태 읽기 실패", InventoryUnavailable: "장치 목록 읽기 실패", ProfilingRunsUnavailable: "측정 회차 목록 읽기 실패",
  EvidencePassportUnavailable: "검증 기록 읽기 실패", EvidencePassportProjectionFailed: "검증 기록 정리 실패",
  BatchEnergyOptimizationResultUnavailable: "측정 결과 읽기 실패", BatchEnergyOptimizationResultProjectionFailed: "측정 결과 정리 실패",
  BatchEnergyOptimizationPreflightRequestInvalid: "측정 요청 형식 오류", BatchEnergyOptimizationPreflightUnavailable: "측정 사전 확인 실패",
  BatchEnergyOptimizationPreflightProjectionFailed: "측정 계획 정리 실패",
  // Dashboard relay errors
  DashboardRouteNotAllowed: "열 수 없는 주소", DashboardMethodNotAllowed: "허용되지 않는 요청", DashboardQueryInvalid: "조회 조건 오류",
  DashboardRelayCanceled: "요청 취소", DashboardRequestTooLarge: "요청 크기 초과", DashboardResponseTooLarge: "응답 크기 초과",
  DashboardConfigInvalid: "배포 설정 오류", DashboardAuthenticationRequired: "접속 정보 없음", DashboardOriginDenied: "허용되지 않는 주소",
  DashboardHeaderInvalid: "요청 정보 오류", DashboardBodyNotAllowed: "본문을 보낼 수 없는 요청", DashboardRelayTimeout: "응답 지연", DashboardRelayUnavailable: "서버 연결 실패",
  // Measurement failures
  TransitionFailed: "전력 모드 전환 실패", RetryLimitExhausted: "재시도 한도 소진",
  EnergyObjectivePolicyMismatch: "성공 회차 수가 에너지 정책과 다름", CurvePlanResolutionFailed: "측정 계획 생성 실패",
  // Scheduler device reasons
  PlacementInventoryMissing: "CPU, GPU 목록 미확인", PlacementBudgetUnavailable: "자원 예산 미확인", PowerModeUnavailable: "전력 모드 미확인", ExcludedNode: "계획에서 뺀 장치",
  // Other typed errors of the serving and query handlers (internal/serving/httpapi/handler.go writeError, internal/queryapi/readapi)
  Conflict: "다른 변경과 충돌", SettingsConflict: "다른 설정 변경이 먼저 저장됨", SettingsPreconditionRequired: "설정 버전 확인 필요",
  NodeBusy: "측정이나 변경이 쓰는 장치", NodeServing: "서비스 실행 중인 장치", NodeMissing: "장치 없음",
  ObservationNotReady: "닫힌 관측 구간 없음", InvalidClientObservation: "관측 기록 형식 오류", InvalidCoverageQuery: "조회 조건 오류",
  CoverageWarming: "측정 범위 집계 중", CoverageUnavailable: "측정 범위 읽기 실패", ModelCompatibilityUnavailable: "지원 여부 확인 실패",
  ExperimentUnavailable: "실험 읽기 실패", EvaluationAuditUnavailable: "실험 변경 기록 불가", EvaluationAuditIncomplete: "실험 변경 기록 미완료",
  OccupancyPolicyUnavailable: "점유시간 측정 정책 없음", OccupancyInfeasible: "점유시간 측정 조건 불가", OccupancySelectionLimit: "선택 요청 수 초과",
  EvidencePassportPending: "검증 기록 작성 중",
  // Catalog insufficiencies (internal/serving/kube/catalog.go)
  ReplacementSelection: "교체 대상 선택 불가", NegativeIncrementalEnergy: "추가 에너지 음수", ProfileReuseRejected: "측정 재사용 불가",
}
/** Phrase for a known server code; undefined otherwise, so the caller picks its own fallback. */
export const codeMessage = (code: string | null | undefined): string | undefined => (code && Object.hasOwn(codeMessages, code) ? codeMessages[code] : undefined)
/** Label for a typed server error or reason code, with "기타 오류" for an unknown one. */
export const errorCodeLabel = (code: string | null | undefined): string => codeMessage(code) ?? (code ? "기타 오류" : DASH)
