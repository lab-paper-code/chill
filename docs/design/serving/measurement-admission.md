# 측정 admission과 단일 writer (M3)

[채택한 이행 단계](../architecture-rethink/05-recommendation-migration.md)의 M3 설계다. 명세 변경은 없다(`SCH-34`,
`SCH-AC-26`, `DB-FR-34`의 의미 그대로). [워크로드별 POOL 3.3절](workload-pools.md#33-측정-pool)의 측정 taint
`chill.dacs.io/measurement:NoSchedule`을 ChillServer 하나가 측정 객체에 따라 붙이고 뗀다. 원래 제안은
[B2](../architecture-rethink/03-b2-single-writer-node-state.md), 노드 쪽 준비는 [M2](../platform/node-agent.md)다.

코드 근거는 `e3867f5`다. live 근거는 2026-10-21 `kubectl get` snapshot과 M2, M4 live test에서 사용자가 확인한 사실이다.
실행으로 확인하지 않은 판단은 `[추론]`으로 표시한다.

## 1. 측정한 문제

| # | 문제 | 근거 |
|---|---|---|
| 1 | taint를 운영자가 손으로 붙이고 뗀다. 서빙 인스턴스가 없는지도 운영자가 pools API로 확인한다 | [3.3절](workload-pools.md#33-측정-pool), `scheduler-operations.md:203-210` |
| 2 | 측정 controller는 taint를 보지 않고 시작한다. BEO, MLIB는 시작 전에 NodeActuation도 만든다 | `InMeasurement` 호출이 serving 쪽에만 있음, `campaign/controller/transition_builder.go:92`, `idlebaseline/controller/transition.go:112` |
| 3 | ChillServer의 확인과 전력 변경 사이에 경쟁 구간이 있다 | `kube/operations.go:788`의 주석, 확인(`:789`)과 NodeActuation 생성(`:792`)이 별도 API 호출 |
| 4 | `serving-operation`이 host에서 돌지 못한다 | live: S3 endpoint가 cluster 안 이름이고(`cmd/serving-operation/main.go:136-141`), 인스턴스 호출이 `chill-executor-<pod UID>` ClusterIP로 간다(`kube/native_service.go:25-43`) |
| 5 | `serving-operation --execute-config`가 계획기 Pod를 지우지 못한다 | 제거 대상 이름을 `measured-<hash>`(`cmd/chill-server/profiles.go:191-192`)와 비교하고(`main.go:559`), 계획기 Pod는 `serving-<hash>`(`kube/scheduler.go:639-642`) |
| 6 | 전환 비용 runner와 Service를 운영자가 준비한다 | snapshot: `chill-system`의 PMTC 5개가 모두 `servingService: chill-executor-<pod UID>`(손으로 준비한 runner), 모두 `Failed`, 수용 시도 0 |
| 7 | 재부팅 뒤 확인되지 않은 전력 상태가 모든 판단을 `Pending`으로 만든다 | live 사실. capture는 최신 NodeActuation이 Ready일 때만 모드를 쓴다(`kube/scheduler.go:478-503`) |

`powerRequests`의 만료된 deadline과 같은 모드 전환의 Ready 재사용은 M2 commit 6(동작 시각에 새 NodeActuation)이 푼다.
M3는 그 위에 선다.

## 2. 결정

### 2.1 writer와 요청

- writer는 ChillServer scheduler loop(`cmd/chill-server/scheduler_service.go:403-431`)다. 매 제어 주기의 `Tick` 앞에
  `SchedulerController.Admit`를 부른다. `Admit`는 `ApplyStaticRouting`(`kube/static_routing.go:17-29`)처럼 `tickMu`와 `mu`를
  잡고 새 capture를 읽는다. 잠금은 taint 추가에만 쓴다. runner 준비와 반환 작업은 taint가 있는 동안 goroutine에서 하므로
  `Tick`을 막지 않는다. operation 측정은 요청을 받을 때 같은 `Admit` 경로로 승인을 받는다.
- 측정 요청은 측정 루트 객체다. 대상 노드는 이름 필드에서 읽는다.

| 루트 | 대상 | terminal |
|---|---|---|
| BatchEnergyOptimization | `spec.state.targetNode` | `Complete`, `Failed`(`CurvePlanResolutionFailed` 제외, `campaign/controller/reconciler.go:108-112`) |
| ModelLoadedIdleBaseline | `spec.state.targetNode` | `Complete`, `Insufficient` |
| ProfilingRun(BEO, MLIB가 소유하지 않은 것) | `spec.targetNode` | `ExecutionComplete`가 `True` 또는 `False` |
| PowerModeTransitionCost | `spec.targetNode` | `Complete`, `Failed` |
| operation 측정(2.5절) | 요청의 `Operation.Node` | ChillServer 안의 측정이 끝남 |

- BEO와 MLIB의 자식 ProfilingRun은 루트가 승인된 뒤에 만들어지므로 따로 요청하지 않는다.

### 2.2 승인, 거절, 반환

한 노드에 승인은 하나다. 루트마다 `Admit`는 다음 하나를 한다.

| 상황 | 동작 |
|---|---|
| 노드가 비었고 대기 루트 중 가장 먼저 만들어짐(`creationTimestamp`, 이름) | taint `chill.dacs.io/measurement=<루트 UID>:NoSchedule`, 루트 annotation `chill.dacs.io/admission: Granted` |
| 노드에 CHILL serving Pod가 있음 | annotation `Refused: NodeServing <인스턴스 ID 목록>`. taint는 붙이지 않는다 |
| 노드가 없음 | `Refused: NodeMissing` |
| 다른 루트가 노드를 가졌거나 그 노드에서 scheduler 동작이 실행 중(`conflictingActionRunning`, `kube/scheduler.go:644-653`) | 다음 주기에 다시 본다 |
| 승인된 루트가 terminal이거나 삭제됨, 또는 taint 값이 살아 있는 루트가 아님 | 반환: 2.4절 runner 제거, 2.3절 낮은 모드 확인, taint 제거 |

- "비었다"는 `edge.dacs.io/workload-role` serving Pod가 없다는 뜻이다(ADR 0022). 소유 Workload가 자동제어인지와 관계없이
  활성 POOL 노드를 거절한다(`SCH-34`). CHILL이 배치하지 않은 Pod는 보지 않는다.
- 거절은 terminal이다. 기각: drain 뒤 측정. `SCH-34`는 거절을 요구하고, drain은 서빙 중인 Workload의 SLO를 깬다.
- 경쟁 구간이 없다. `Tick`은 `tickMu` 안에서 capture부터 `launch`(`kube/scheduler.go:655-750`)까지 하므로 승인이 그 사이에
  끼지 못하고, 실행 중인 동작이 있는 노드는 승인을 미룬다. 승인 뒤 capture는 taint를 보고 노드를 뺀다
  ([워크로드별 POOL 3.4절](workload-pools.md#34-전체-pool-가용-노드와-전력모드)).
  `admittedNode`(`kube/operations.go:843-865`)는 그대로 남는다.
- taint는 resourceVersion을 실은 merge patch로 쓴다. `spec.taints`는 atomic list이고 node lifecycle controller도 쓰므로
  Server-Side Apply field manager로 소유를 나누지 못한다. 충돌이면 다음 주기에 다시 한다.
- 재시작은 level-based다. taint 값과 루트 목록이 API 서버에 있다. 운영자가 붙인 `=true` taint는 살아 있는 루트가 아니므로
  첫 주기에 반환된다.
- pools API `measurement[]`(`workload-pools.md:344`)에 `owner{kind, namespace, name}`를 더한다(`DB-FR-34`의 이동과 반환 진행).

### 2.3 fencing과 반환 전력

- fencing token은 taint 값인 루트 UID다. 루트는 한 번만 승인되므로 값이 되풀이되지 않고 별도 counter가 필요 없다.
- NodeActuation에 `spec.grant`(문자열)를 더한다. 측정 controller의 세 생성 위치(`campaign/controller/transition_builder.go:92`,
  `idlebaseline/controller/transition.go:112`, `transitioncost/controller/attempt_materializer.go:377`)는 루트 UID를, ChillServer
  serving `ChangePower`는 빈 값을 쓴다.
- NodeActuator `Decide`(`internal/nodeactuator/lifecycle.go:112-127`)는 deadline 검사 옆에서 Node의 taint 값을 snapshot으로 받아,
  grant가 다르면 적용 전에 `Failed`, 사유 `GrantMismatch`로 끝낸다. 빈 grant는 taint 없는 노드에서만 적용된다.
- 반환 뒤 늦게 도착한 측정 요청은 taint가 없어 거절된다. 승인 전에 만든 serving 요청은 그 `ChangePower`가 Ready나 `Failed`
  (deadline 만료 포함)까지 기다리며(`kube/operations.go:795-813`) 노드 동작으로 남으므로 승인과 겹치지 않는다. ChillServer
  재시작으로 대기가 끊긴 요청은 승인 뒤에 도착해도 빈 grant라 거절된다.
- 반환 때 writer는 taint를 떼기 전에 grant를 실은 `ChangePower`로 노드를 가장 낮은 전력모드에 놓고 독립 read-back을 확인한다.
  측정이 재부팅이나 실패로 남긴 미확인 상태가 가용 노드로 넘어가지 않는다(문제 7). 확인이 실패하면 taint를 유지하고 사유를
  남긴다. 측정 밖 노드의 미확인 상태는 범위 밖이다(8장 질문 3).

### 2.4 전환 비용 runner

- PMTC 승인은 runner가 준비된 뒤에 `Granted`가 된다. writer는 taint를 붙이고, 측정 모드 Operations(2.5절)로 runner를
  준비한다. profile은 catalog의 준비 가능한 profile 중 대상 Node UID, `spec.modelSpec`, `spec.executionPath`,
  `spec.modes[0].powerMode`가 같은 것이다.
- runner Pod는 PMTC를 owner로 갖는다. writer는 `spec.servingService` 이름으로 Service를 만들고 selector는 runner Pod의
  `edge.dacs.io/serving-pod-uid`(`kube/native_service.go:20,39`)다. observer는 PMTC namespace에서 이 Service와 Pod를
  읽으므로(`transitioncost/kube/observer.go:166-210`) PMTC는 serving namespace에 둔다.
- 거절 사유: `RunnerProfileMissing`(맞는 profile 없음), `RunnerContainerUnsupported`(`spec.servingContainer`가 `firstfloor`가
  아님), `RunnerNamespace`(PMTC namespace가 serving namespace와 다름).
- 반환 때 runner를 remove하고 Service를 지운다. PMTC spec과 evidence 형식(`kube/cost_power.go:202-205`)은 그대로다.

### 2.5 serving-operation은 ChillServer 클라이언트

| route | 몸체 | 동작 |
|---|---|---|
| `POST /api/v1/measurements/operations` | `MeasurementRequest`(`kube/measurement.go:31-39`) | 202와 측정 ID. ChillServer가 ID를 루트로 승인을 받고, 관측 PVC 아래 측정 전용 directory와 Runtime으로 지금 `runMeasurement`(`main.go:104-224`)의 몸체를 돌린다. S3는 ChillServer binding, 전력원은 M2가 넣은 Profiler facts resolver |
| `GET /api/v1/measurements/operations/{id}` | - | 진행, 기록, receipt. 메모리에 둔다. 기록 원본은 S3 |
| `POST /api/v1/scheduler/operations` | `{kind: prepare 또는 remove, workloadUID, profileID, node, instance, fixedBatchSize}` | `SchedulerController.ExecuteManual`. `tickMu` 안에서 Workload가 자동제어 밖인지, 그 노드에 실행 중인 동작이 없는지, remove 대상이 `removableBy`(`main.go:507-520`)를 통과하고 route 가중치 0인지 보고, 같은 `Operations`와 `OnAction` 기록으로 실행 |

- 인증과 권한은 기존 TokenReview와 SAR이다. CLI는 manifest를 읽어 보내고 결과를 stdout에 쓴다.
- ChillServer 재시작은 실행 중인 operation 측정을 끝낸다. 그 ID는 살아 있는 루트가 아니므로 다음 주기에 반환된다.
- 인스턴스는 scheduler snapshot의 ID로 고르므로 `serving-<hash>` Pod도 지운다(문제 5).

### 2.6 측정 controller의 대기

- 네 루트 reconciler는 child, Job, NodeActuation을 만들기 전에 annotation을 본다. 없으면 `RequeueAfter` 5초,
  `Refused: <사유>`면 terminal로 끝낸다: BEO `Failed`, MLIB `Insufficient`, ProfilingRun `ExecutionComplete=False`, PMTC
  `Failed`, 사유는 annotation의 첫 단어다.
- Profiler RBAC는 그대로다(Node `get`, `charts/chill/templates/profiler_rbac.yaml:170-175`). ChillServer에 네 kind의
  `list`, `patch`와 serving namespace Service `create`, `delete`를 더한다. Node `patch`는 이미 있다
  (`serving_entrypoint_rbac.yaml:34-36`).

## 3. 저장 계약 (ADR 0019)

| 대상 | 변경 | 처리 |
|---|---|---|
| Node taint `chill.dacs.io/measurement` | 값이 루트 UID | 저장 evidence가 아니다 |
| 루트 annotation `chill.dacs.io/admission` | 새 key | 같음 |
| NodeActuation CRD | `spec.grant` 추가(선택, 불변) | 기존 객체는 빈 grant로 읽힌다. M5에서 CRD가 사라진다 |
| operation 측정 `v1alpha2`, `MeasurementIsolation.Tainted` | 그대로 | - |
| PMTC spec, transition-cost evidence | 그대로 | snapshot의 `Failed` PMTC 5개는 지운다 |

## 4. 커밋 단위

| # | commit | 내용 | 선행 |
|---|---|---|---|
| 1 | `feat(serving): admit measurement nodes in ChillServer` | `kube/admission.go`(`Admit`, 승인, 거절, 대기, taint 반환), `cmd/chill-server`의 루트 목록과 terminal 판정, loop 연결, pools `owner`, RBAC | - |
| 2 | `feat(profiling): wait for measurement admission` | 네 루트 reconciler의 annotation 대기와 거절 terminal | 1과 함께 배포 |
| 3 | `feat(nodeactuator): fence power changes by measurement grant` | `spec.grant`, `make manifests generate`, `Decide`의 `GrantMismatch`, 네 생성 위치 | 2, M2 commit 6 |
| 4 | `feat(serving): return measured nodes at a confirmed lowest mode` | 반환의 grant `ChangePower`와 read-back | 1, 3 |
| 5 | `feat(serving): run operation measurements and manual operations in ChillServer` | 2.5절 route 셋, `ExecuteManual`, `serving-operation` client화. 지우는 것: `executionOwnerClient`, `readExecutionOwner`, `readExecutionConfiguration`, `validateExecutionOwner`, `validateExecutionNode`, `validateExecutionRemoval`, `readExecutionAcknowledgement`, `validateMeasurementNode`, `readMeasurementIsolation`의 CLI 사본 | 1 |
| 6 | `feat(serving): create transition-cost runners on admission` | 2.4절 | 2, 5 |
| 7 | `test(serving): race measurement admission against prepares` | 5장의 1000 동시 test(`test/integration/serving`, envtest) | 5 |

각 commit은 자기가 바꾼 문서를 같이 고친다: [3.3절](workload-pools.md#33-측정-pool), `workload-management.md:122-125`,
`scheduler-operations.md:203-212`와 수동 실행 절, `scheduler-runtime-measurement.md:210-211`, ADR 0022의 M3 문장,
`MeasurementTaintKey` 주석(`internal/profiling/run/measurement_taint.go:5-6`). 1, 2는 배포 하나, 3, 4는 M2 배포 뒤 하나,
5, 6, 7은 하나다.

## 5. 검사

| commit | 검사 |
|---|---|
| 1 | 빈 노드 승인(taint 값 UID, `Granted`). serving Pod 노드 `Refused: NodeServing`, taint 없음. 외부 Pod만 있는 노드 승인. 두 루트가 한 노드면 먼저 만든 것 승인, 뒤 것 대기. 실행 중 prepare가 있는 노드 대기. terminal과 삭제와 `=true` taint가 반환. merge patch 충돌이 taint를 바꾸지 않음. 같은 주기의 `Tick`이 승인 노드를 후보에서 뺌 |
| 2 | 루트마다 annotation 없으면 child, Job, NodeActuation 0. `Refused`면 kind별 terminal과 사유 |
| 3 | `Decide`: taint 노드의 빈 grant, 다른 UID grant, taint 없는 노드의 측정 grant가 `GrantMismatch`. 같은 UID는 적용. 생성 위치 넷의 grant 값 |
| 4 | 반환 순서가 runner 제거, Ready read-back, taint 제거. read-back 실패면 taint 유지와 사유 |
| 5 | operation 측정이 ChillServer binding으로 S3에 쓰고 기록이 지금 CLI 결과와 같은 형식. `serving-<hash>` 인스턴스 remove 성공, 공유 인스턴스와 자동제어 Workload와 가중치 남은 대상 거절, route SAR 거절 |
| 6 | runner Pod owner PMTC, Service selector가 Pod UID, profile 선택 표, 세 거절 사유, 반환 뒤 runner와 Service 0 |
| 7 | 노드 하나에 goroutine 1000개: 500개는 서로 다른 루트의 승인과 terminal, 500개는 `ExecuteManual` prepare. Node와 Pod watch의 resourceVersion 순서로 taint 구간 안 serving Pod 생성과 serving Pod가 있는 동안의 taint 추가를 센다. 겹침 0, 승인과 prepare 각각 1회 이상, `-race` |
| 전체 | `make lint test`, `make arch-check`, `make helm-lint helm-template`(default와 `values-profiler.yaml`) |

## 6. 실클러스터 확인

M4 live test와 M2 배포가 끝난 뒤 한다.

| # | 확인 | 통과 기준 |
|---|---|---|
| E-A1 | 가용 노드에 MLIB 하나 | 한 제어 주기 안에 taint(UID)와 `Granted`, `Complete` 뒤 가장 낮은 모드 Ready read-back, taint 제거. 승인과 반환 지연 기록 |
| E-A2 | 자동제어 Workload가 서빙 중인 노드에 BEO | `Refused: NodeServing`, taint 없음, 그 Workload의 SLO 위반 수가 직전 같은 길이 구간과 같음 |
| E-A3 | runner 없이 PMTC(jetsonx, 두 모드) | runner와 Service 자동 생성, 방향마다 수용 시도가 목표 수, 반환 뒤 runner와 Service 0 |
| E-A4 | host에서 `serving-operation --measurement-config`(prepare 비용) | 기록이 S3에 있고 비용 근거로 묶임 |
| E-A5 | host에서 자동제어 밖 Workload의 `serving-<hash>` remove | Pod 제거, 관측 기록에 manual 동작 |
| E-A6 | 측정 중 루트 삭제, 측정 중 ChillServer 재시작 | 삭제는 다음 주기에 반환. 재시작 뒤 CR 루트 taint 유지, operation 측정 taint 반환 |
| E-A7 | 위 전체 동안 NodeActuation 이력과 taint 구간 | taint 구간 안에서 Ready가 된 serving 요청, 반환 뒤 Ready가 된 측정 요청이 모두 0. commit 3 뒤에는 `GrantMismatch` 수도 기록 |

## 7. 범위 밖

- Measurement CRD 통합과 NodeActuation 삭제는 M5다. 루트 표(2.1절)는 그때 한 kind가 된다.
- watch 기반 승인. 제어 주기 polling으로 측정 진입이 한 주기 늦어지는 것을 E-A1이 재고, 문제가 되면 더한다.
- Dashboard 측정 요청 화면의 노드 선택 불가 표시(`DB-FR-34`)는 pools `owner`와 거절 annotation을 읽는 Dashboard 변경이다.

## 8. 사용자에게 묻는 것

1. 서빙 중인 노드의 측정을 terminal 거절로 끝낸다. 운영자는 Workload를 옮긴 뒤 다시 제출한다. 자동제어를 끈 Workload와
   소유자 없는 인스턴스의 노드도 같이 거절한다. 이대로 가는가?
2. AGENTS.md는 측정 근거 없는 fence를 금한다. commit 3은 05가 채택했고 ChillServer 재시작과 측정 실패 뒤의 늦은 요청을
   막지만, 늦은 적용이 실제로 일어난 기록은 없다. 승인 순서(commit 1)만으로 ChillServer 안의 구간은 닫힌다. commit 3을
   지금 넣을까, E-A7이 늦은 적용을 셀 때까지 미룰까?
3. 반환 때 가장 낮은 모드를 확인한다(2.3절). 측정 밖에서 재부팅한 노드의 미확인 상태가 모든 판단을 `Pending`으로 만드는
   문제는 계획기가 그 노드만 빼도록 M4 쪽에서 다룰까?
4. runner profile이 여럿 맞으면(배치 설정이나 artifact가 다른 profile) 가장 최근 측정을 고른다. PMTC에 profile ID를 지정하는
   필드가 필요한가?
5. operation 측정 API는 비동기(202와 조회)다. 기록은 ChillServer 재시작에 살아남지 않고 S3 원본만 남는다. 괜찮은가?
