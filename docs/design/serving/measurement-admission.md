# 측정 admission과 단일 writer (M3)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M3 설계다. 명세 변경은 없다(`SCH-34`,
`SCH-AC-26`, `DB-FR-34`의 의미 그대로). [워크로드별 POOL 3.3절](workload-pools.md#33-측정-pool)의 측정 taint
`chill.dacs.io/measurement:NoSchedule`을 ChillServer 하나가 측정 객체에 따라 붙이고 뗀다. 원래 제안은
[B2](../architecture-rethink/03-b2-single-writer-node-state.md), 노드 쪽 준비는 [M2](../platform/node-agent.md)다.

코드 근거는 `3dd5902`다. live 근거는 2026-10-21 `kubectl get` snapshot과 M2, M4 live test에서 사용자가 확인한 사실이다.
실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 1. 측정한 문제

| # | 문제 | 근거 |
|---|---|---|
| 1 | taint를 운영자가 손으로 붙이고 뗀다. 서빙 인스턴스가 없는지도 운영자가 pools API로 확인한다 | [3.3절](workload-pools.md#33-측정-pool), `scheduler-operations.md:203-210` |
| 2 | 측정 controller는 taint를 보지 않고 시작한다. BEO, MLIB는 시작 전에 NodeActuation도 만든다 | `InMeasurement` 호출이 serving 쪽에만 있음, `campaign/controller/transition_builder.go:92`, `idlebaseline/controller/transition.go:112` |
| 3 | serving NodeActuation이 그것을 만든 동작보다 오래 남는다 | `ChangePower`의 대기는 첫 `Get` 오류나 target 차이에서 끝나고(`kube/operations.go:132-140,796-803`), 실행은 `Failed`로 끝나도 NodeActuation은 Pending이나 Applying으로 남는다 |
| 4 | `serving-operation`이 host에서 돌지 못한다 | live: S3 endpoint가 cluster 안 이름이고(`cmd/serving-operation/main.go:136-141`), 인스턴스 호출이 `chill-executor-<pod UID>` ClusterIP로 간다(`kube/native_service.go:25-43`) |
| 5 | `serving-operation --execute-config`가 계획기 Pod를 지우지 못한다 | 제거 대상 이름을 `measured-<hash>`(`cmd/chill-server/profiles.go:191-192`)와 비교하고(`main.go:559`), 계획기 Pod는 `serving-<hash>`(`kube/scheduler.go:639-642`) |
| 6 | 전환 비용 runner와 Service를 운영자가 준비한다 | snapshot: `chill-system`의 PMTC 5개가 모두 `servingService: chill-executor-<pod UID>`(손으로 준비한 runner), 모두 `Failed`, 수용 시도 0 |
| 7 | 재부팅 뒤 확인되지 않은 전력 상태가 모든 판단을 `Pending`으로 만든다 | live 사실. capture는 최신 NodeActuation이 Ready일 때만 모드를 쓴다(`kube/scheduler.go:478-503`) |

`powerRequests`의 만료된 deadline과 같은 모드 전환의 Ready 재사용은 M2 commit 6(동작 시각에 새 NodeActuation)이 푼다.
M3는 그 위에 선다.

## 2. 결정

### 2.1 writer와 요청

- writer는 ChillServer `SchedulerController`다. scheduler loop(`cmd/chill-server/scheduler_service.go:403-431`)가 매 제어
  주기의 `Tick` 앞에 `Admit`를 부르고, operation 측정 요청(2.5절)도 같은 `Admit`를 부른다. `Admit`는 `tickMu`를 잡고
  Node, `edge.dacs.io/workload-role=serving-instance` Pod, 대상 노드의 Pod, NodeActuation(`kube/scheduler.go:405-408`과 같은
  목록), 측정 루트를 읽는다. `c.mu`는 `c.runs` 확인과 등록에만 잡는다. 전체 capture는 읽지 않는다.
- 노드에 시간이 걸리는 일(runner 준비, 반환, operation 측정, 수동 동작)은 `launch`(`kube/scheduler.go:705-747`)처럼
  `tickMu` 안에서 그 노드의 `actionRun`으로 `c.runs`에 등록하고 잠금 밖 goroutine에서 실행한다. 결과는 기존 `OnAction`
  기록으로 남는다. 등록된 run이 있는 노드에는 `Tick`(`conflictingActionRunning`, `:644-653`)도 `Admit`도 손대지 않는다.
- 측정 요청은 측정 루트다. 대상 노드는 이름 필드에서 읽는다.

| 루트 | 대상 | terminal |
|---|---|---|
| BatchEnergyOptimization | `spec.state.targetNode` | `Complete`, `Failed`(`CurvePlanResolutionFailed` 제외, `campaign/controller/reconciler.go:108-112`) |
| ModelLoadedIdleBaseline | `spec.state.targetNode` | `Complete`, `Insufficient` |
| ProfilingRun(BEO, MLIB가 소유하지 않은 것) | `spec.targetNode` | `ExecutionComplete`가 `True` 또는 `False` |
| PowerModeTransitionCost | `spec.targetNode` | `Complete`, `Failed` |
| operation 측정 순서(2.5절) | 요청의 `Operation.Node` | 그 run이 끝남 |

BEO와 MLIB의 자식 ProfilingRun은 루트가 승인된 뒤에 만들어지므로 따로 요청하지 않는다.

### 2.2 승인과 거절

한 노드에 승인은 하나다. `Admit`는 대기 루트를 `creationTimestamp`, 이름 순으로 보고 위에서부터 처음 맞는 줄을 따른다.

| # | 상황 | 동작 |
|---|---|---|
| 1 | 노드에 taint가 있음, 그 노드의 run이 실행 중, 또는 Ready나 `Failed`가 아닌 NodeActuation이 있음 | 기다린다(다음 주기) |
| 2 | 노드가 없음 | annotation `chill.dacs.io/admission: Refused: NodeMissing` |
| 3 | 노드에 CHILL serving Pod가 있음 | `Refused: NodeServing <namespace/name 목록>`. taint는 붙이지 않는다 |
| 4 | PMTC이고 runner 조건이 맞지 않음(2.4절) | `Refused: <사유>` |
| 5 | 그 밖 | taint `chill.dacs.io/measurement=<루트 UID>:NoSchedule`, `Granted <RFC3339 시각>`. PMTC는 runner 준비 뒤에 `Granted`(2.4절) |

- 줄 1이 먼저라서 다른 측정이 가진 노드의 대기 루트는 그 측정의 serving Pod(runner나 operation 측정 인스턴스) 때문에
  거절되지 않는다. 줄 3은 taint 없는 노드에만 적용된다.
- 줄 1의 NodeActuation 조건은 문제 3을 닫는다. 대기는 M2의 `deadline = now + maxActuationDurationSeconds`로 끝이 있다.
- 줄 3은 Workload 소유와 자동제어 여부와 관계없이 CHILL serving Pod 하나로 거절한다(`SCH-34`). 거절은 terminal이다.
  기각: drain 뒤 측정. `SCH-34`는 거절을 요구하고, drain은 서빙 중인 Workload의 SLO를 깬다.
- CHILL이 배치하지 않은 Pod는 승인 판단에 쓰지 않는다. 그 노드는 가용 노드로 다뤄져 전력모드가 바뀌고 측정이 진행되며,
  측정 결과가 그 Pod 목록을 기록한다(`SCH-34`, `SCH-AC-26`, workload-pools commit 5). 그 Pod는 지우지 않는다.
- taint는 resourceVersion을 실은 merge patch로 쓴다. `spec.taints`는 atomic list이고 node lifecycle controller도 쓰므로
  Server-Side Apply field manager로 소유를 나누지 못한다. 충돌이면 다음 주기에 다시 한다.
- 승인 뒤 capture는 taint를 보고 노드를 뺀다([워크로드별 POOL 3.4절](workload-pools.md#34-전체-pool-가용-노드와-전력모드)).
  serving의 `admittedNode`(`kube/operations.go:843-865`)는 그대로 남는다.

### 2.3 반환과 측정 무효

반환은 그 노드의 `actionRun`이고 measurement 모드 `Operations`(`OperationConfig.Measurement`, `kube/operations.go:856-865`)로
돈다. 시작 조건은 셋이다: 승인된 루트가 terminal이거나 삭제됨, taint 값이 살아 있는 루트도 실행 중인 run도 아님(운영자의
`=true`, ChillServer 재시작으로 끊긴 operation 측정과 runner 준비), operation 측정 순서가 끝남. 순서는 다음과 같다.

1. Ready나 `Failed`가 아닌 NodeActuation이 없어질 때까지 기다린다.
2. 측정 무효 판정: 그 노드의 NodeActuation 중 루트가 만들지 않았고(owner UID가 루트 UID가 아님) `applyStartedAt`이 승인
   시각과 반환 시작 사이에 있는 것이 있으면, 루트에 annotation `chill.dacs.io/measurement-invalid: PowerChangedDuringMeasurement
   <NodeActuation 이름>`을 쓴다. taint 값이 삭제된 루트이면 반환 run 결과에 `Interrupted <taint 값>`을
   기록한다. operation 측정 순서는 2.5절의 witness와 순서 기록이 판정한다.
3. 노드의 CHILL serving Pod를 모두 remove한다. taint 노드의 serving Pod는 승인 뒤 그 측정이 만든 것뿐이다(줄 3). PMTC
   runner Service도 지운다.
4. 노드에 요청 가능한 전력모드가 있고 `SchedulerLowestMode`(`internal/serving/scheduler_plan.go:34`)가 빈 값이 아니면
   그 모드로 `ChangePower`하고 독립 read-back을 확인한다. 둘 중 하나가 없으면(`Fixed` actuator, unloaded baseline 없음)
   이 단계를 건너뛴다.
5. taint를 뗀다.

- 단계가 실패하면 run이 `Failed`와 사유를 기록하고 taint는 남는다. 다음 주기에 같은 조건으로 반환을 다시 시작한다.
- 단계 4는 측정이 재부팅이나 실패로 남긴 미확인 전력 상태가 가용 노드로 넘어가지 않게 한다(문제 7).
- 무효는 세기만 한다. 측정 근거를 읽는 곳은 annotation이 있는 루트를 쓰지 않는다: catalog(`kube/catalog.go:96-99`)와
  비용(`kube/cost.go:146-150`)은 `CatalogIssue`, `CostIssue` 코드 `MeasurementInvalid`로 보이고, profiler의 baseline과
  curve plan 출처(`transitioncost/controller/baseline_source.go`, `campaign/controller/curve_plan_source.go:314`)는 건너뛴다.
- ChillServer 재시작은 level-based다. 살아 있고 terminal이 아닌 CR 루트의 taint는 그대로 두고, 나머지는 첫 주기에 위 순서로
  반환한다. runner 준비 중 끊긴 PMTC는 `Granted`가 없으므로 반환 뒤 다시 대기 루트가 된다.

### 2.4 전환 비용 runner

- 승인 전 확인(2.2절 줄 4): `RunnerProfileMissing`(catalog의 준비 가능한 profile 중 대상 Node UID, `spec.modelSpec`,
  `spec.executionPath`, `spec.modes[0].powerMode`가 같은 것이 없음), `RunnerContainerUnsupported`(`spec.servingContainer`가
  `firstfloor`가 아님), `RunnerNamespace`(PMTC namespace가 serving namespace와 다름). 맞는 profile이 여럿이면 가장 최근 측정이다.
- taint를 붙이고 runner 준비 run을 등록한다. run은 measurement 모드로 runner Pod `runner-<PMTC UID hash>`를 준비하고,
  `spec.servingService` 이름의 Service를 label `chill.dacs.io/measurement-runner: <Node UID>`로 만든다. 반환이 그 Node의
  serving Pod와 이 label의 Service를 지우므로 PMTC가 삭제되어도 runner가 남지 않는다. selector는 runner Pod의 `edge.dacs.io/serving-pod-uid`
  (`kube/native_service.go:20,39`)다. observer는 PMTC namespace에서 이 Service와 Pod를 읽는다(`transitioncost/kube/observer.go:166-210`).
- 성공하면 PMTC annotation `chill.dacs.io/runner-profile: <profile ID>`와 `Granted <시각>`을 쓴다. preparer가 profile ID를 준비
  기록에 고정하고 result가 `runnerProfileID`로 싣는다(`transition-cost-measurement-result.v1alpha3`에서 `v1alpha4`,
  `transitioncost/raw_record.go:13`).
- 실패하면 `Refused: RunnerPrepareFailed <오류>`를 쓰고 같은 run에서 반환한다.

### 2.5 serving-operation은 ChillServer 클라이언트

| route | 몸체 | 동작 |
|---|---|---|
| `POST /api/v1/measurements/operations` | 같은 노드의 순서 있는 `[]MeasurementRequest`(`kube/measurement.go:31-39`), 예: prepare, idle, remove | 동기. `Admit`로 순서 전체를 한 번 승인받고(taint 값은 ChillServer가 만든 ID), 줄 1이면 409 `NodeBusy`, 줄 2, 3이면 409와 거절 사유. 승인되면 run이 ChillServer의 runtime과 observer를 쓰는 measurement 모드 `Operations`로 요청마다 `Measure`를 돌리고 반환까지 한다. 측정 인스턴스는 owner와 route가 없어 serving 요청을 받지 않는다. 응답은 지금 CLI stdout과 같은 `{record, receipt}` 목록 |
| `POST /api/v1/scheduler/operations` | `{kind: prepare 또는 remove, workloadUID, profileID, node, instance, fixedBatchSize}` | `ExecuteManual`. `tickMu` 안에서 Workload가 자동제어 밖인지, 노드에 run이 없는지, prepare는 `ValidatePreparationCapacity`(`kube/scheduler.go:591-634`)를 마지막 snapshot의 노드와 live Node, Pod로, remove는 `removableBy`(`main.go:507-520`)와 route 가중치 0을 확인하고 run을 등록한 뒤 잠금 밖에서 같은 `Operations`로 실행 |

- S3는 ChillServer binding, 전력원은 M2가 넣은 Profiler facts resolver다. 인증과 권한은 기존 TokenReview와 SAR이다.
  두 route는 요청 한도 대신 `--operation-timeout`(기본 30분)을 쓴다. CLI는 manifest를 읽어 보내고 응답을 stdout에 쓴다.
- 순서 ID는 taint 값이다. ChillServer는 각 요청의 `Operation.ID`를 `<순서 ID>.<순번>.<요청 ID>`로 바꿔 저장 key
  (`kube/cost.go:31-33`)와 기록이 순서를 가리키게 한다(`/`만 금지, `kube/measurement.go:172-173`).
- 요청이 실패하거나 client 연결이 끊기면 순서가 멈추고 반환이 돈다. `Measure`는 진행 중이던 요청의 기록을 `WithoutCancel`로
  쓰며(`kube/measurement.go:107-109`), 취소가 오류로 판정에 들어가 Verdict가 `Insufficient`다 `[추론: :229의 취소 경로]`.
- 모든 요청을 마치지 못한 순서의 반환은 불변 object `serving/operation-sequences/<순서 ID>.json`
  (`serving-operation-sequence.v1alpha1`, `{sequenceID, node, requested, saved, reason}`)을 쓴다. 저장된 기록은 각자 판정을
  유지하고, 비용(`kube/cost.go:94`)은 그 순서의 기록을 비용으로 쓰면서 `CostIssue` `SequenceIncomplete <순서 ID>`로 표시한다.
- ChillServer 재시작은 실행 중인 순서를 끊는다. 재시작 순간에 진행 중이던 측정은 process가 죽으면 저장되지 않고, 종료
  처리로 run context가 취소되면 위처럼 `Insufficient`로 저장된다. 첫 주기의 반환이 taint 값으로 순서 ID를 알고,
  `saved`를 그 ID로 시작하는 기록 key에서 채워 reason `ChillServerRestart`로 순서 기록을 쓴다.
- 최종 isolation witness(지금 `main.go:287-313`)는 2.3절 단계 2와 같은 NodeActuation 검사를 더한다. 그 측정의 `operationPower`
  가 만든 NodeActuation(`kube/measurement.go:90`)은 제외한다. 걸리면 `Isolation.Error`가
  `PowerChangedDuringMeasurement`이고 기존 경로대로 Verdict `Insufficient`다. `kube/cost.go:190`은 이 기록을 조용히 건너뛰는
  대신 `CostIssue` `MeasurementInvalid`로 센다. 저장 형식은 그대로다.
- 인스턴스는 scheduler snapshot의 ID로 고르므로 `serving-<hash>` Pod도 지운다(문제 5).

### 2.6 측정 controller의 대기

- 네 루트 reconciler는 child, Job, NodeActuation을 만들기 전에 `Granted`를 본다. 없으면 `RequeueAfter` 5초, `Refused`면
  terminal로 끝낸다: BEO `Failed`, MLIB `Insufficient`, ProfilingRun `ExecutionComplete=False`, PMTC `Failed`, 사유는
  annotation의 첫 단어다.
- RBAC: ChillServer에 네 kind의 `patch`만 더한다. `list`(`serving_entrypoint_rbac.yaml:16-18`), Node `patch`(`:34-36`),
  serving namespace Service `create`, `delete`(`:83-89`)는 이미 있다. Profiler는 그대로다.

## 3. 저장 계약 (ADR 0019)

| 대상 | 변경 | 처리 |
|---|---|---|
| Node taint `chill.dacs.io/measurement` | 값이 루트 UID나 operation 측정 ID | 저장 evidence가 아니다 |
| 루트 annotation `chill.dacs.io/admission`, `chill.dacs.io/measurement-invalid`, `chill.dacs.io/runner-profile` | 새 key | 같음 |
| transition-cost 준비 기록과 result | `runnerProfileID`, result `v1alpha4` | snapshot의 `Failed` PMTC 5개와 그 evidence를 지운다 |
| operation 측정 `v1alpha2` | 그대로(`Isolation.Error`에 새 사유, `Operation.ID`에 순서 ID) | - |
| `serving/operation-sequences/<순서 ID>.json` | 새 object `serving-operation-sequence.v1alpha1` | 끝나지 않은 순서만 쓴다 |
| NodeActuation CRD | 그대로 | - |

## 4. 커밋 단위

| # | commit | 내용 | 선행 |
|---|---|---|---|
| 1 | `feat(serving): admit and hand back measurement nodes in ChillServer` | `kube/admission.go`(`Admit`, 2.2절 표, 2.3절 반환 run과 무효 판정, 재시작), `cmd/chill-server`의 루트 목록과 terminal 판정, loop 연결, catalog와 비용의 `MeasurementInvalid`, RBAC | - |
| 2 | `feat(profiling): wait for measurement admission` | 네 루트 reconciler의 대기와 거절 terminal, baseline과 curve plan 출처의 무효 건너뛰기 | 1 |
| 3 | `feat(serving): run operation measurements and manual operations in ChillServer` | 2.5절 route 둘, `ExecuteManual`, witness의 전력 검사, 순서 ID와 순서 기록, `SequenceIncomplete`, `serving-operation` client화. CLI에서 지우는 것: `executionOwnerClient`, `readExecutionOwner`, `readExecutionConfiguration`, `validateExecutionOwner`, `validateExecutionNode`, `validateExecutionRemoval`, `readExecutionAcknowledgement`, `validateMeasurementNode`, `readMeasurementIsolation` | 1 |
| 4 | `feat(serving): create transition-cost runners on admission` | 2.4절, result `v1alpha4` | 1, 3 |
| 5 | `feat(serving): return measured nodes at the lowest mode` | 반환 단계 4 | 1, 3, M2 commit 6 |

1-4는 배포 하나다. 1이 운영자 taint를 반환하므로 3(CLI가 taint를 요구하지 않음)과 4(손 runner가 `NodeServing`이 됨)가
같이 들어가야 한다. 5는 M2 배포 뒤 같이 넣거나 따로 넣는다. 각 commit은 자기가 바꾼 문서를 같이 고친다:
[3.3절](workload-pools.md#33-측정-pool), `workload-management.md:122-125`, `scheduler-operations.md:203-212`와 수동 실행 절,
`scheduler-runtime-measurement.md:210-211`, `MeasurementTaintKey` 주석(`internal/profiling/run/measurement_taint.go:5-6`).

## 5. 검사

| commit | 검사 |
|---|---|
| 1 | 빈 노드 승인(taint 값 UID, `Granted <시각>`). serving Pod 노드 `Refused: NodeServing`과 Pod 이름, taint 없음. CHILL이 배치하지 않은 Pod만 있는 노드 승인, 그 Pod 그대로. 한 노드의 두 루트는 먼저 만든 것 승인, 뒤 것은 앞 측정의 serving Pod가 있어도 대기. 등록된 run이 있으면 대기. Pending NodeActuation이 있으면 승인과 반환 단계 1 모두 대기. 반환 순서와 serving Pod 0, 단계 실패 때 taint 유지와 다음 주기 재시도, 반환 run 중복 없음. 승인 구간 안 외부 NodeActuation이면 `measurement-invalid`, 루트가 소유한 것은 무효 아님. 재시작: 살아 있는 CR 루트 taint 유지, `=true`와 삭제된 루트와 끊긴 순서 ID는 serving Pod 제거, 가장 낮은 모드, taint 제거, 끊긴 PMTC runner 준비는 반환 뒤 다시 대기. 무효 BEO와 PMTC가 `MeasurementInvalid`. merge patch 충돌이 taint를 바꾸지 않음 |
| 2 | 루트마다 `Granted` 없으면 child, Job, NodeActuation 0. `Refused`면 kind별 terminal과 사유. 무효 MLIB와 ProfilingRun을 출처가 쓰지 않음 |
| 3 | prepare, idle, remove 순서가 한 번 승인되고 각 기록이 순서 ID를 가진 key로 S3에, 응답이 CLI 형식, 순서 기록 없음. 바쁜 노드 409. 두 번째 요청 실패와 연결 끊김이 순서 기록(`saved` 1개)과 `SequenceIncomplete`를 만들고 첫 기록은 비용으로 쓰임. 재시작 뒤 반환이 reason `ChillServerRestart` 순서 기록, 진행 중이던 요청은 기록 없음이나 `Insufficient`. 순서 중 외부 NodeActuation이면 그 기록 `Insufficient`와 `MeasurementInvalid`. `ExecuteManual`: `serving-<hash>` remove 성공, 공유와 자동제어와 가중치 남은 대상 거절, 용량 초과 prepare 거절, taint 노드 거절(`admittedNode`), 실행 중 run을 `Admit`와 `Tick`이 건너뜀. route SAR 거절 |
| 4 | 세 확인 사유가 taint 전, 반환 뒤 runner Pod와 Service 0, 가장 최근 profile 선택, `runner-profile`과 result `runnerProfileID`, 준비 실패 `RunnerPrepareFailed`와 반환 |
| 5 | 요청 가능 모드와 가장 낮은 모드가 있으면 그 모드로 Ready read-back 뒤 taint 제거. `Fixed` 노드와 baseline 없는 노드는 전력 단계 없이 반환 |
| 전체 | `make lint test`, `make manifests generate`(RBAC), `make helm-lint helm-template`(default와 `values-profiler.yaml`) |

단일 writer는 순서마다 결정적 test 하나와 작은 동시 test 하나로 확인한다. commit 3에 넣는다(`internal/serving/kube`, fake client).

| 순서 | test |
|---|---|
| 전력 변경 ↔ 측정 승인 | Pending serving NodeActuation이 있으면 `Admit`가 기다리고 Ready 뒤 승인. 승인 뒤 serving `ChangePower`는 `NodeInMeasurement` |
| 측정 중 Tick | taint 노드가 배치, 내림, 제거 후보에서 빠짐. run이 등록된 노드에 `launch` 없음. 그 노드의 `ExecuteManual` 거절 |
| 측정 끝 ↔ 전력 변경 | 반환 단계 1이 Pending NodeActuation을 기다림. taint 제거 전 serving `ChangePower` 거절, 제거 뒤 허용. 승인 구간 안에 적용된 외부 NodeActuation이 측정 무효 |
| 동시(`-race`) | goroutine 수십 개가 `Admit`와 반환, `Tick`, `ExecuteManual` prepare, operation 측정 요청, serving `ChangePower`를 섞어 부름. fake client의 write 순서 기록에서 taint 구간 안 serving Pod와 serving NodeActuation 생성 0, serving Pod가 있는 동안 taint 추가 0, taint write는 `Admit`와 반환에서만 |

## 6. 실클러스터 확인

M4 live test와 M2 배포가 끝난 뒤 한다.

| # | 확인 | 통과 기준 |
|---|---|---|
| E-A1 | 가용 노드에 MLIB 하나 | 한 제어 주기 안에 taint(UID)와 `Granted`, `Complete` 뒤 가장 낮은 모드 Ready read-back, taint 제거. 승인과 반환 지연 기록 |
| E-A2 | 자동제어 Workload가 서빙 중인 노드에 BEO | `Refused: NodeServing`과 Pod 이름, taint 없음, 그 Workload의 SLO 위반 수가 직전 같은 길이 구간과 같음 |
| E-A3 | CHILL이 배치하지 않은 Pod(예: `plan10-serving-jetsono`)가 있는 jetsono에 MLIB | 승인과 측정 진행, 그 Pod 그대로, 결과 Pod 목록에 그 Pod |
| E-A4 | runner 없이 PMTC(jetsonx, 두 모드) | runner와 Service 자동 생성, result `runnerProfileID`, 방향마다 수용 시도가 목표 수, 반환 뒤 runner와 Service 0 |
| E-A5 | host에서 `serving-operation --measurement-config`(prepare, idle, remove) | 승인 한 번, 기록 셋이 S3에 있고 비용 근거로 묶임, 반환 뒤 serving Pod 0 |
| E-A6 | host에서 자동제어 밖 Workload의 `serving-<hash>` remove | Pod 제거, 관측 기록에 manual 동작 |
| E-A7 | 측정 중 루트 삭제, operation 측정 순서 중 ChillServer 재시작 | 다음 주기에 반환. 재시작 쪽은 serving Pod 0, 가장 낮은 모드, 순서 기록, 앞 기록에 `SequenceIncomplete` |
| E-A8 | 위 전체 동안 `measurement-invalid` 수와 `MeasurementInvalid` 수 | 기록. 0이 아니면 그 NodeActuation 출처를 확인 |

## 7. 범위 밖

- Measurement CRD 통합과 NodeActuation 삭제는 M5다. 루트 표(2.1절)는 그때 한 kind가 된다.
- watch 기반 승인. 제어 주기 polling으로 측정 진입이 한 주기 늦어지는 것을 E-A1이 재고, 문제가 되면 더한다.
- pools API의 측정 소유자 표시와 Dashboard 측정 요청 화면의 선택 불가 표시(`DB-FR-34`). 루트 annotation이 상태를 갖고 있어
  Dashboard 변경 때 함께 더한다.
- 측정 밖 노드의 미확인 전력 상태(문제 7의 나머지)는 M4 계획기 쪽이다(8장 질문 1).

## 8. 사용자에게 묻는 것

1. 측정 밖에서 재부팅한 노드의 미확인 상태가 모든 판단을 `Pending`으로 만드는 문제는 계획기가 그 노드만 빼도록 M4 쪽에서
   다룰까?
