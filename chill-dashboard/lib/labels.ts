// Code → [plain sentence, next action]. Sources are the ChillServer files named per group (design §6.4).
// Screens show the sentence; the raw code stays in the 원본 view.
// Every failure or waiting code carries a next action.
type Guide = readonly [string, string]
// Display-only labels: settled states and component or runtime names that ask nothing of the reader.
type Label = readonly [string]

const guides: Record<string, Guide> = {
  // Workload readiness states: internal/serving/kube/workload_view.go
  Waiting: ["아직 요청을 받을 준비가 안 됐어요.", "아래 이유를 확인해 주세요."],
  Preparing: ["측정 결과는 있고, 실행을 준비하고 있어요.", "잠시 후 다시 확인해 주세요."],
  Changing: ["설정을 바꾸고 있어요. 바꾸는 동안 새 요청은 기다려요.", "끝날 때까지 기다려 주세요."],
  Failed: ["마지막 작업이 실패했어요.", "원본 보기에서 이유를 확인하고 다시 요청해 주세요."],

  // Readiness reasons: workload_view.go, internal/serving/runtime.go, earlier Dashboard reasons
  AppliedDefinitionUnavailable: ["ChillServer가 아직 이 설정을 적용하지 않았어요.", "잠시 후 다시 확인해 주세요."],
  MissingEvidence: ["이 서비스의 조건으로 잰 측정 결과가 없어요.", "측정을 요청하고 결과를 채택해 주세요."],
  MissingDataset: ["측정에 쓸 데이터셋이 지정되지 않았어요.", "서비스 설정에서 데이터셋을 골라 주세요."],
  MissingRuntime: ["실행 환경이 준비되지 않았어요.", "모델의 실행 방식을 확인해 주세요."],
  NativeNotReady: ["실행기가 아직 준비 중이에요.", "잠시 후 다시 확인해 주세요."],
  PolicyAckPending: ["반영 대기", "현재 실행 상태는 유지되며, 새 정책의 적용 확인을 기다려요."],
  ObservationUnavailable: ["요청 기록을 저장할 수 없어서 새 요청을 받지 않아요.", "ChillServer의 기록 저장소 상태를 확인해 주세요."],
  RestartDrain: ["ChillServer가 다시 시작해서, 이전 요청을 마무리할 때까지 새 요청을 멈췄어요.", "마무리될 때까지 기다려 주세요."],
  RestartUnconfirmed: ["ChillServer가 다시 시작한 뒤 이 실행기 상태를 아직 확인하지 못했어요.", "잠시 후 다시 확인해 주세요."],
  InstanceStopped: ["실행기가 멈춰서 이 실행기의 요청은 끝나지 못했어요.", "다른 실행기가 준비됐는지 확인해 주세요."],
  NodeDrained: ["장비에서 실행기를 비워서 이 실행기는 요청을 받지 않아요.", "다른 실행기가 준비됐는지 확인해 주세요."],
  NoCurrentReadiness: ["지금 구성에 이 실행기가 없어서 요청을 받지 않아요.", "운영 판단의 현재 구성을 확인해 주세요."],
  ConditionChangedBeforeDrain: ["처리 중인 요청이 남은 채 실행 조건이 바뀌어서, 마무리될 때까지 기다리고 있어요.", "마무리될 때까지 기다려 주세요."],
  ManagementOperationFailed: ["설정 변경을 끝내지 못했어요.", "원본 보기에서 이유를 확인하고 다시 요청해 주세요."],
  DrainTimeout: ["받은 요청이 제한 시간 안에 끝나지 않아 변경을 멈췄어요.", "처리 중인 요청을 확인한 뒤 다시 요청해 주세요."],
  SharedUpdateFailed: ["함께 바꾸던 다른 서비스의 변경이 실패해서 이 변경도 멈췄어요.", "실패한 서비스를 먼저 확인해 주세요."],

  // Serving errors: internal/serving/httpapi/handler.go, internal/serving/kube/manager.go, runtime.go, firstfloor/client.go
  Unauthenticated: ["접속 정보를 확인하지 못했어요.", "다시 접속해 주세요."],
  Forbidden: ["이 작업을 할 권한이 없어요.", "관리자에게 권한을 요청해 주세요."],
  AuthorizationUnavailable: ["지금은 권한을 확인할 수 없어요.", "잠시 후 다시 시도해 주세요."],
  NotFound: ["항목을 찾을 수 없어요.", "목록에서 다시 골라 주세요."],
  WorkloadNotFound: ["이 서비스가 등록되어 있지 않아요.", "서비스 목록을 확인해 주세요."],
  PoolNotFound: ["이 실행 그룹을 찾을 수 없어요.", "실행 그룹 목록을 확인해 주세요."],
  AlreadyExists: ["같은 이름이 이미 있어요.", "다른 이름을 골라 주세요."],
  InvalidBody: ["보낸 내용을 이해하지 못했어요.", "입력한 내용을 확인해 주세요."],
  InvalidInput: ["입력 값이 올바르지 않아요.", "입력 필드를 확인해 주세요."],
  InvalidWorkload: ["서비스 설정이 올바르지 않아요.", "표시된 항목을 확인해 주세요."],
  InvalidModel: ["모델 설정이 올바르지 않아요.", "모델 파일과 실행 방식을 확인해 주세요."],
  InvalidModelReference: ["대상 모델이나 모델 파일을 찾을 수 없어요.", "대상 모델과 모델 파일을 확인해 주세요."],
  InvalidPreprocessorReference: ["선택한 전처리기를 찾을 수 없어요.", "전처리기를 다시 골라 주세요."],
  InvalidDatasetReference: ["선택한 데이터셋을 찾을 수 없어요.", "데이터셋을 다시 골라 주세요."],
  InvalidPreprocessor: ["전처리기 설정이 올바르지 않아요.", "입력과 출력 필드를 확인해 주세요."],
  InvalidDataset: ["데이터셋 설정이 올바르지 않아요.", "데이터셋 필드를 확인해 주세요."],
  IncompatibleDataset: ["데이터셋이 전처리기 입력 필드를 담고 있지 않아요.", "전처리기 입력 필드와 데이터셋 필드를 맞춰 주세요."],
  InvalidQuery: ["조회 조건이 올바르지 않아요.", "기간과 조건을 다시 골라 주세요."],
  InvalidRequestTimeout: ["요청 제한 시간이 허용 범위를 벗어났어요.", "더 짧은 시간을 골라 주세요."],
  ResourceVersionRequired: ["최신 상태를 확인한 뒤에만 바꿀 수 있어요.", "최신 상태를 불러온 뒤 다시 시도해 주세요."],
  ResourceVersionConflict: ["다른 변경이 먼저 저장됐어요.", "최신 상태를 확인한 뒤 다시 시도해 주세요."],
  OperationConflict: ["이미 진행 중인 변경이 있어요.", "진행 중인 변경이 끝난 뒤 다시 시도해 주세요."],
  RegistrationConflict: ["같은 이름의 이전 등록이 아직 정리되지 않았어요.", "잠시 후 다시 시도해 주세요."],
  CatalogIdentityConflict: ["함께 바꾸던 대상이 중간에 바뀌었어요.", "최신 상태를 확인한 뒤 다시 요청해 주세요."],
  ModelReferenced: ["이 모델을 쓰는 곳이 있어서 지울 수 없어요.", "쓰는 곳을 먼저 바꾸거나 지워 주세요."],
  ModelArtifactReferenced: ["전처리기가 대상으로 삼은 모델 파일은 뺄 수 없어요.", "그 파일을 남기거나 전처리기의 대상에서 먼저 빼 주세요."],
  PreprocessorReferenced: ["이 전처리기를 쓰는 서비스가 있어서 지울 수 없어요.", "서비스를 먼저 바꾸거나 지워 주세요."],
  DatasetReferenced: ["이 데이터셋을 쓰는 서비스가 있어서 지울 수 없어요.", "서비스를 먼저 바꾸거나 지워 주세요."],
  ModelDeleting: ["모델을 지우고 있어서 바꿀 수 없어요.", "삭제가 끝난 뒤 목록을 다시 확인해 주세요."],
  PreprocessorDeleting: ["전처리기를 지우고 있어서 바꿀 수 없어요.", "삭제가 끝난 뒤 목록을 다시 확인해 주세요."],
  DatasetDeleting: ["데이터셋을 지우고 있어서 바꿀 수 없어요.", "삭제가 끝난 뒤 목록을 다시 확인해 주세요."],
  DefinitionChanged: ["요청을 받는 사이 모델이나 전처리 설정이 바뀌었어요.", "다시 보내 주세요."],
  WorkloadChanged: ["요청을 받는 사이 서비스 등록이 바뀌었어요.", "서비스 상태를 확인한 뒤 다시 보내 주세요."],
  IdentityUnavailable: ["등록 정보를 아직 확인할 수 없어요.", "잠시 후 다시 시도해 주세요."],
  AppliedSnapshotUnavailable: ["적용한 설정 기록을 읽지 못했어요.", "잠시 후 상태를 다시 확인해 주세요."],
  AppliedStateUnconfirmed: ["실제로 적용된 설정을 아직 확인하지 못했어요.", "잠시 후 상태를 다시 확인해 주세요."],
  EntrypointRestarted: ["ChillServer가 다시 시작해서 진행 중이던 작업이 끝나지 못했어요.", "상태를 확인한 뒤 다시 요청해 주세요."],
  DrainIncomplete: ["이전 요청이 아직 남아 있어요.", "마무리될 때까지 기다린 뒤 다시 시도해 주세요."],
  StillAttached: ["실행기 연결이 아직 남아 있어요.", "잠시 후 다시 시도해 주세요."],
  InvalidDistribution: ["실행기 구성이 올바르지 않아 적용하지 않았어요.", "원본 보기에서 자세한 내용을 확인해 주세요."],
  ManagementUnavailable: ["ChillServer가 아직 시작 중이에요.", "잠시 후 다시 시도해 주세요."],
  ServiceUnavailable: ["지금은 요청을 처리할 수 없어요.", "잠시 후 다시 시도해 주세요."],
  QueueFull: ["대기 중인 요청이 너무 많아요.", "잠시 후 다시 보내 주세요."],
  GatewayOverloaded: ["ChillServer 메모리가 한도에 가까워요.", "잠시 후 다시 보내 주세요."],
  RequestTimeout: ["정한 시간 안에 답을 받지 못했어요.", "잠시 후 다시 보내 주세요."],
  RequestCanceled: ["요청을 취소했어요.", "필요하면 다시 보내 주세요."],
  SLODeadlineDrop: ["응답 목표 시간을 넘길 것이 확실해서 처리하지 않았어요.", "부하가 줄어든 뒤 다시 보내 주세요."],
  PreprocessFailed: ["입력을 처리할 수 있는 형태로 바꾸지 못했어요.", "입력 형식이 전처리기 계약과 맞는지 확인해 주세요."],
  BackendError: ["모델 실행 중 오류가 났어요.", "원본 보기에서 자세한 내용을 확인해 주세요."],
  BackendUnavailable: ["실행기와 연결하지 못했어요.", "실행기 상태를 확인해 주세요."],
  BackendResponseInvalid: ["실행기의 응답을 이해하지 못했어요.", "원본 보기에서 자세한 내용을 확인해 주세요."],
  BackendResponseTooLarge: ["실행기의 응답이 너무 커요.", "서비스의 출력 크기 설정을 확인한 뒤 다시 보내 주세요."],
  BackendContextMismatch: ["실행기의 응답이 이 요청과 맞지 않아요.", "원본 보기에서 자세한 내용을 확인해 주세요."],
  ResponseDeliveryFailed: ["결과를 만들었지만 전달하지 못했어요.", "서비스 완료 기록을 확인한 뒤 필요하면 다시 보내 주세요."],
  IncompleteObservation: ["고른 기간에 빠진 기록이 있어요.", "다른 기간을 골라 주세요."],
  InvalidObservation: ["기록에 필요한 시각이 빠져 있어요.", "잠시 후 다시 조회해 주세요."],
  SchedulerUnavailable: ["운영 판단 기능이 이 배포에 연결되지 않았어요.", "배포 설정에서 스케줄러를 켜 주세요."],
  MeasurementUnavailable: ["지금은 측정 기능을 쓸 수 없어요.", "잠시 후 다시 시도해 주세요."],
  MeasurementPreflightUnavailable: ["측정 요청을 미리 확인하지 못했어요.", "잠시 후 다시 시도해 주세요."],
  MeasurementPreflightRejected: ["이 조건으로는 측정 계획을 만들 수 없어요.", "원본 보기에서 이유를 확인하고 조건을 바꿔 주세요."],
  MeasurementInUse: ["진행 중이거나 지금 실행 중인 서비스가 쓰고 있는 측정이라 지울 수 없어요.", "측정이 끝나거나 그 서비스 인스턴스가 정리된 뒤 지워 주세요."],
  MeasurementAuditUnavailable: ["측정 변경 기록을 저장할 수 없어서 요청하지 않았어요.", "잠시 후 다시 시도해 주세요."],
  MeasurementAuditIncomplete: ["측정 변경은 접수됐을 수 있지만 기록을 끝내지 못했어요.", "다시 보내기 전에 측정 목록을 확인해 주세요."],
  MeasurementEvidenceUnavailable: ["측정 기록 저장소에 접근할 수 없어서 지우지 않았어요.", "잠시 후 다시 시도해 주세요."],
  MeasurementEvidenceDeletionIncomplete: ["측정 기록 일부만 지웠어요. 측정 자체는 남아 있어요.", "측정을 다시 확인한 뒤 삭제를 다시 요청해 주세요."],
  MeasurementDeletionIncomplete: ["측정 기록은 지웠지만 측정 자체가 지워졌는지 확인하지 못했어요.", "측정을 다시 확인한 뒤 삭제를 다시 요청해 주세요."],

  // Profile and cost issues: cmd/chill-server/profiles.go, internal/serving/kube/catalog.go, internal/serving/kube/cost.go
  PreparationUnavailable: ["이 측정 결과로는 실행을 준비할 수 없어요.", "원본 보기에서 이유를 확인해 주세요."],
  CostEvidenceInvalid: ["비용 측정 기록을 검증하지 못했어요.", "원본 보기에서 이유를 확인해 주세요."],
  MeterUndeclared: ["운영 설정에 없는 계측기로 잰 비용이에요.", "운영 설정의 계측기와 같은 계측기로 다시 측정해 주세요."],
  InvalidEvidence: ["측정 기록을 검증하지 못했어요.", "원본 보기에서 이유를 확인해 주세요."],
  MeasurementEvidenceMissing: ["운영에 필요한 측정 기록이 모자라요.", "필요한 크기를 포함해 다시 측정해 주세요."],
  MemoryEvidenceMissing: ["메모리 사용량 기록이 없는 측정이에요.", "다시 측정해 주세요."],

  // Console errors: internal/operatorconsole/readapi/queries.go, handler.go
  SystemsUnavailable: ["시스템 상태를 읽지 못했어요.", "잠시 후 다시 시도해 주세요."],
  InventoryUnavailable: ["장비 목록을 읽지 못했어요.", "잠시 후 다시 시도해 주세요."],
  ProfilingRunsUnavailable: ["측정 회차 목록을 읽지 못했어요.", "잠시 후 다시 시도해 주세요."],
  EvidencePassportUnavailable: ["측정 회차의 검증 기록을 읽지 못했어요.", "잠시 후 다시 시도해 주세요."],
  EvidencePassportProjectionFailed: ["측정 회차의 검증 기록을 정리하지 못했어요.", "원본 보기에서 자세한 내용을 확인해 주세요."],
  BatchEnergyOptimizationResultUnavailable: ["측정 결과를 읽지 못했어요.", "잠시 후 다시 시도해 주세요."],
  BatchEnergyOptimizationResultProjectionFailed: ["측정 결과를 정리하지 못했어요.", "원본 보기에서 자세한 내용을 확인해 주세요."],
  BatchEnergyOptimizationPreflightRequestInvalid: ["확인할 측정 요청의 형식이 올바르지 않아요.", "측정 요청 내용을 확인해 주세요."],
  BatchEnergyOptimizationPreflightUnavailable: ["측정 요청을 미리 확인하지 못했어요.", "잠시 후 다시 시도해 주세요."],
  BatchEnergyOptimizationPreflightProjectionFailed: ["측정 계획을 정리하지 못했어요.", "원본 보기에서 자세한 내용을 확인해 주세요."],

  // Dashboard relay errors: lib/relay.ts, app/api/config/route.ts
  DashboardRouteNotAllowed: ["Dashboard가 열지 않는 주소예요.", "메뉴에서 다시 들어가 주세요."],
  DashboardMethodNotAllowed: ["Dashboard가 허용하지 않는 요청이에요.", "메뉴에서 다시 들어가 주세요."],
  DashboardQueryInvalid: ["조회 조건이 올바르지 않아요.", "기간과 조건을 다시 골라 주세요."],
  DashboardRelayCanceled: ["요청을 취소했어요.", "필요하면 다시 시도해 주세요."],
  DashboardRequestTooLarge: ["보낸 내용이 너무 커요.", "내용을 줄여 다시 보내 주세요."],
  DashboardResponseTooLarge: ["응답이 너무 커서 받지 않았어요.", "조회 범위를 줄여 주세요."],
  DashboardConfigInvalid: ["Dashboard 배포 설정이 올바르지 않아요.", "배포 설정을 확인해 주세요."],
  DashboardAuthenticationRequired: ["접속 정보가 없어요.", "먼저 접속해 주세요."],
  DashboardOriginDenied: ["이 주소에서 보낸 변경은 받을 수 없어요.", "Dashboard 주소로 다시 접속해 주세요."],
  DashboardHeaderInvalid: ["요청 정보가 올바르지 않아요.", "새로 고친 뒤 다시 시도해 주세요."],
  DashboardBodyNotAllowed: ["이 요청에는 내용을 함께 보낼 수 없어요.", "새로 고친 뒤 다시 시도해 주세요."],
  DashboardRelayTimeout: ["응답이 늦어지고 있어요.", "잠시 후 다시 시도해 주세요."],
  DashboardRelayUnavailable: ["ChillServer에 연결하지 못했어요.", "잠시 후 다시 시도해 주세요."],

  // Measurement failure reasons: internal/profiling/campaign/controller/reconciler.go
  TransitionFailed: ["측정할 상태로 장비를 바꾸지 못했어요.", "장비의 전력 모드 설정을 확인한 뒤 다시 요청해 주세요."],
  RetryLimitExhausted: ["다시 시도할 수 있는 횟수를 모두 써도 측정이 실패했어요.", "원본 보기에서 실패 이유를 확인한 뒤 다시 요청해 주세요."],
  EnergyObjectivePolicyMismatch: ["배치별 성공한 측정 회차 수가 에너지 정책과 달라 측정을 끝냈어요.", "새 이름의 측정으로 다시 측정해 주세요."],
  CurvePlanResolutionFailed: ["측정 계획을 만들지 못해 측정을 끝냈어요.", "원본 보기에서 이유를 확인하고 조건을 고친 뒤 다시 요청해 주세요."],

  // Scheduler node reasons: internal/serving SchedulerNode.Reasons
  PlacementInventoryMissing: ["CPU와 GPU 목록을 아직 확인하지 못했어요.", "배포 설정의 장비 목록을 확인해 주세요."],
  PlacementBudgetUnavailable: ["서비스에 쓸 수 있는 자원 예산을 확인하지 못했어요.", "배포 설정의 예약 자원을 확인해 주세요."],
  PowerModeUnavailable: ["지금 전력 모드를 확인하지 못했어요.", "전력 모드 제어기 상태를 확인해 주세요."],
  ExcludedNode: ["운영 설정에서 계획에서 뺀 장비예요. 운영 판단이 이 장비를 바꾸지 않아요.", "운영 설정의 '계획에서 뺀 장비'에서 빼면 다시 계획해요."],

  // ChillSystem condition reasons that need action: internal/operator/system/status.go
  ComponentsDisabled: ["관리하는 구성요소를 모두 꺼 두었어요.", "필요한 구성요소를 시스템 설정에서 켜 주세요."],
  ComponentsNotReady: ["준비되지 않은 구성요소가 있어요.", "아래 구성요소별 상태를 확인해 주세요."],
  ObservationFailed: ["구성요소 상태를 확인하지 못했어요.", "잠시 후 다시 확인해 주세요."],
  NodeAgentMissing: ["장비 탐색기가 설치되어 있지 않아요.", "설치 상태를 확인해 주세요."],
  NodeAgentPending: ["장비 탐색기를 실행할 장비가 아직 없어요.", "장비 연결 상태를 확인해 주세요."],
  NodeAgentProgressing: ["장비 탐색기를 배포하고 있어요.", "잠시 후 다시 확인해 주세요."],
  NodeAgentDegraded: ["장비 탐색기에 문제가 있어요.", "원본 보기에서 이유를 확인해 주세요."],
  ProfilerMissing: ["측정기가 설치되어 있지 않아요.", "설치 상태를 확인해 주세요."],
  ProfilerProgressing: ["측정기를 배포하고 있어요.", "잠시 후 다시 확인해 주세요."],
  ProfilerDegraded: ["측정기에 문제가 있어요.", "원본 보기에서 이유를 확인해 주세요."],
  NodeAgentActuationMissing: ["전력 모드 제어기가 설치되어 있지 않아요.", "설치 상태를 확인해 주세요."],
  NodeAgentActuationPending: ["전력 모드 제어기를 실행할 장비가 아직 없어요.", "장비 연결 상태를 확인해 주세요."],
  NodeAgentActuationProgressing: ["전력 모드 제어기를 배포하고 있어요.", "잠시 후 다시 확인해 주세요."],
  NodeAgentActuationDegraded: ["전력 모드 제어기에 문제가 있어요.", "원본 보기에서 이유를 확인해 주세요."],
  EvidenceStoreMissing: ["측정 기록 저장소가 설치되어 있지 않아요.", "설치 상태를 확인해 주세요."],
  EvidenceStoreProgressing: ["측정 기록 저장소를 준비하고 있어요.", "잠시 후 다시 확인해 주세요."],
  EvidenceStoreDegraded: ["측정 기록 저장소에 문제가 있어요.", "원본 보기에서 이유를 확인해 주세요."],
}

const labels: Record<string, Label> = {
  // Settled workload states: internal/serving/kube/workload_view.go
  Ready: ["요청을 받을 수 있어요."],
  Deleting: ["지우고 있어요. 받은 요청을 마무리한 뒤 사라져요."],

  // Measurement outcomes: internal/profiling/campaign/controller/curve_deriver.go
  "MinimumFound/LocalMinimum": ["크기를 더 키우면 에너지가 늘어서 탐색을 멈췄어요."],
  "MinimumFound/SearchSpaceExhausted": ["정한 크기를 모두 재고 가장 적은 것을 골랐어요."],

  // ChillSystem healthy reasons: internal/operator/system/status.go
  ComponentsAvailable: ["켜 둔 구성요소가 모두 정상이에요."],
  NodeAgentReady: ["장비 탐색기가 정상이에요."],
  ProfilerReady: ["측정기가 정상이에요."],
  NodeAgentActuationReady: ["전력 모드 제어기가 정상이에요."],
  EvidenceStoreReady: ["측정 기록 저장소가 정상이에요."],

  // ChillSystem condition types as component names: api/v1alpha1/chillsystem_types.go, used by status.go.
  // The overall "Ready" type shares its key with the workload Ready state above.
  NodeAgentAvailable: ["장비 탐색기"],
  ProfilerAvailable: ["측정기"],
  NodeAgentActuationAvailable: ["전력 모드 제어기"],
  EvidenceStoreAvailable: ["측정 기록 저장소"],

  // Runtime family, backends and providers: internal/modelcatalog/catalog.go, Passport measurement.runtime.provider
  firstfloor: ["CHILL 실행기"],
  "ort-cpu": ["CPU에서 ONNX 실행"],
  "ort-cuda": ["CUDA GPU에서 ONNX 실행"],
  rknnlite: ["NPU에서 RKNN 실행"],
  CPUExecutionProvider: ["CPU"],
  CUDAExecutionProvider: ["GPU (CUDA)"],
  TensorrtExecutionProvider: ["GPU (TensorRT)"],
}

export const codeText: Record<string, Guide | Label> = { ...labels, ...guides }
// A code outside the table gets the common sentence; the caller keeps the raw code in its 원본 view.
export const explain = (code: string): readonly [string, string?] =>
  codeText[code] ?? ["새 상태의 설명이 준비 중이에요.", "원본 보기에서 자세한 내용을 확인해 주세요."]
