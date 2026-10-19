# 워크로드별 POOL과 자동제어 구현 설계

## 1. 목적과 범위

[시스템 명세의 노드 POOL과 구성](../../spec/README.md#노드-pool과-구성),
[측정 POOL](../../spec/00_measurement.md#측정-pool),
[스케줄러 SRS](../../spec/scheduler/SRS.md)의 `SCH-02`, `SCH-08`, `SCH-09`, `SCH-13`, `SCH-15`, `SCH-21`,
`SCH-32`, `SCH-33`, `SCH-34`, [워크로드 SRS](../../spec/workloads/SRS.md)의 `WM-25`,
[Dashboard SRS](../../spec/dashboard/SRS.md)의 `DB-FR-10`, `DB-FR-33`, `DB-FR-34`에 대응하는 구현 계약이다.
경계 결정은 [ADR 0022](../../adr/0022-workload-pool-membership-boundary.md)에 기록한다.
기존 계약은 [워크로드 관리](workload-management.md), [스케줄러 구현 설계](scheduler-implementation.md),
[이기종 워크로드](heterogeneous-workloads.md)를 따르며 이 문서는 바뀌는 부분만 정한다.

### 핵심 결정

| 결정 | 이유 |
|---|---|
| 자동제어 스위치는 scheduler 설정 `settings.autonomousWorkloads`(Workload UID 목록) | 결정 기록마다 사용한 설정이 함께 저장되고 기존 설정 API를 그대로 쓴다 |
| 활성 POOL 소속은 저장하지 않고 CHILL serving 인스턴스에서 도출 | 여러 활성 POOL 소속을 별도 상태 없이 표현하고, 부분 완료도 실제 인스턴스로 드러난다 |
| 측정 POOL은 Node annotation `edge.dacs.io/measurement-pool` 하나, writer는 측정 실행 주체 | 측정의 시작, 종료, 실패를 아는 곳에서 이동과 반환을 한다 |
| 측정 POOL 점유는 annotation 하나로 표시하고, 점유, serving Pod 생성, 전력 변경 직전에 서로를 확인한다 | 겹침이 관측되기 전까지 별도 직렬화를 두지 않는다. 남는 경쟁 구간은 측정 결과의 Pod 목록으로 드러난다 |
| 전력모드 전환 비용 측정의 runner와 Service는 측정 POOL 점유 뒤 ChillServer가 측정 소유로 만들고 반환 전에 지운다 | serving Pod 생성, profile 발행, 자원 예산 검사를 소유한 경로를 그대로 쓴다 |
| 전체 POOL은 control-plane이 아닌 모든 Kubernetes Node, `cpuInventory`는 배치 가능 조건 | 명세의 전체 워커노드 범위와 가용 노드 전력 집계를 그대로 따른다 |
| 가용 노드는 비용 비교 없이 늘 가장 낮은 전력모드로 내린다. 가장 낮은 모드는 unloaded baseline 중 전력이 가장 작은 모드 | 후보 선택이 아닌 고정 규칙. DeviceClass에는 모드 순서가 없고, 이 baseline은 에너지 계산에 이미 필요하다 |
| baseline이 없는 노드는 남겨 두고 에너지를 평가 불가로 표시 | 전체 POOL 에너지에서 일부를 빼고 비교하지 않는다 |
| 후보와 비용 근거는 완료된 측정 전체에서 조건별 최신 성공으로 도출 | 운영자 채택과 `profileCampaigns`, `preparations`, `costReferences`, `powerCostSources` 선언을 없앤다 |
| 완료 여유는 `settings.completionMarginMS` 하나, firstfloor에는 요청마다 전달 | 설정 변경이 바로 모든 요청에 적용되고 인스턴스 policy를 다시 쓰지 않는다 |
| NodeActuator의 workload safety 검사 삭제, 측정 결과는 노드 Pod 목록과 CHILL 배치 여부를 기록 | CHILL은 자신이 배치한 파드만 관리한다 |
| 저장 계약은 이 branch에서 한 번씩 바꾸고, 같은 계약을 바꾸는 작업은 한 commit에 모은다 | [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md) |

## 2. 요구사항 대응

| 요구사항 | 설계 |
|---|---|
| 시스템 명세 노드 POOL과 구성(활성 POOL, 측정 POOL, 가용 노드, 자동제어) | 3.1, 3.2, 3.3, 3.4, 4 |
| 시스템 명세 외부 파드, 가용 노드 전력 | 3.4, 3.8 |
| 시스템 명세 흐름의 측정 POOL 이동과 반환, 00 측정 POOL | 3.3, 3.8 |
| `SCH-02`, `SCH-AC-22`, `WM-25` | 3.2, 5 |
| `SCH-08` | 3.4, 3.5, 4.2 |
| `SCH-09` | 3.5 |
| `SCH-13`, `SCH-21`, 03 라우팅의 설정 여유, 04 요청 지연 계산 | 3.6 |
| `SCH-15`, `SCH-16`의 전체 POOL 에너지 | 3.4, 4.3, 4.4 |
| `SCH-32` | 3.1, 3.6 |
| `SCH-33`, `SCH-AC-24`, `SCH-AC-25` | 3.1, 3.4, 3.7, 4 |
| `SCH-34`, `SCH-AC-26` | 3.3, 3.8 |
| `DB-FR-10`, `DB-FR-33`, `DB-FR-34` | 5 |

## 3. 표현 선택

### 3.1 워크로드별 자동제어

`SchedulerSettings`(`internal/serving/scheduler.go:14-29`)에 `autonomousWorkloads []string`을 더하고
`SchedulerControls.Enabled`(`:31`)를 삭제한다. 나머지 control(`:32-39`)은 시스템 전체의 제어요소 활성
설정으로 남는다. 판단 기록 `SchedulerDecision.Settings`(`:243`)가 그 시점의 목록을 함께 보존하므로
`SCH-32`의 사용한 설정 기록이 그대로 성립한다. 변경은 기존 `PUT /api/v1/scheduler/settings`
(`internal/serving/httpapi/handler.go:101`, `httpapi/scheduler.go:64-143`)로 한다.

목록은 Workload UID를 담는다. 같은 이름으로 다시 등록한 Workload는 새 UID라 꺼진 상태로 시작한다.
판단은 현재 Workload에 없는 UID를 무시하고, 설정 저장은 그런 UID를 지운 목록을 저장한다.
`Controls.Enabled`를 읽는 곳(`kube/scheduler.go:111-120,284,317`, `kube/static_routing.go:30`,
`scheduler_replay.go:162,172`, `scheduler_candidates.go:28`, `cmd/chill-server/server.go:105`,
`scheduler_service.go:389`, `profiles.go:348`, `cmd/serving-operation/main.go:450,460`)은 4장과 3.7절의
워크로드별 규칙으로 바꾼다.

기각: Workload spec 필드. Workload 정의는 모델 참조와 SLO만 담고(`api/v1alpha1/workload_types.go:13-23`,
[워크로드 관리](workload-management.md) 1장), spec 수정은 정의 변경 절차를 거친다.

### 3.2 활성 POOL 소속

Workload W의 활성 POOL은 W가 owner(`InstanceState.OwnerWorkloadUID`)이거나 binding으로 연결된 CHILL serving
인스턴스가 있는 노드 집합이다. 노드의 소속은 그 노드의 인스턴스에서 계산하며 저장하지 않는다.
전력 집계는 이미 인스턴스로 활성 노드를 정한다(`internal/serving/observation_log_power.go:429-437`).
[전환 명세](../../spec/05_transition_control.md)의 동작 목록에도 소속 변경 동작은 없다. 노드의 편입과 반환은
prepare와 remove로 이루어지고, 일부만 완료한 전환은 실제 인스턴스 상태로 바로 드러난다(`SCH-AC-22`).

삭제 대상은 `edge.dacs.io/serving-pool` label(`kube/observer.go:40`), 그 유일한 writer `MoveNodePool`
(`observer.go:875-953`), membership 동작(`kube/operations.go:46,259-263`, `scheduler_replay.go:26,1054`,
`scheduler_candidates.go:1048`, `kube/scheduler.go:1030-1033`), label 기반 POOL 산출(`observer.go:180-193`)과
배치 검사(`observer.go:317-318,372-378`, `operations.go:151,369-370,997,1039`, `kube/scheduler.go:533-536,734-737`)다.
`Pool` 필드는 `SchedulerNode`(`scheduler.go:51`), `SchedulerInstance`(`:67`), `InstanceState`(`types.go:202`),
`Assignment`(`types.go:185`), `Operation`과 `OperationState`(`operations.go:71,81`),
`firstfloor.Spec`(`serving/firstfloor/client.go:31`)에서 삭제한다. `Assignment.Pool`의 producer와 복원
(`runtime.go:128,348`), 후보 비교(`runtime.go:714`), Dashboard 요청 관측 타입도 함께 바꾼다.

한 노드의 모든 CHILL 인스턴스는 같은 ModelSpec(이름과 UID)을 참조한다. 이 조건은 후보 생성에서 확인한다(4.3절).
owner와 binding이 모두 없는 인스턴스는 어느 활성 POOL에도 속하지 않는다. 측정 POOL 밖의 이런 인스턴스는 다음
판단의 후보가 remove로 정리한다.

기각: label 값을 POOL 목록으로 확장. 인스턴스와 따로 맞춰야 하는 저장 상태가 남는다.

### 3.3 측정 POOL

Node annotation `edge.dacs.io/measurement-pool`의 값은 노드를 점유한 측정(owner)의 전체 식별자다. Kubernetes 측정은
`<kind>/<namespace>/<name>/<UID>`이고, `serving-operation`은 `serving-operation/<measurementOwner>`이며
`measurementOwner`는 부모 실행자가 측정 묶음(prepare 비용과 이어지는 remove 비용)마다 새로 만드는 UUID다. CLI는 UUID
형식만 받는다. 같은 이름으로 다시 만든 측정 객체는 UID가 달라 남은 annotation을 자기 것으로 보지 않는다. 새 package
`internal/measurementpool`이 이 상수와 `Acquire`, `Release`, `Check`를 제공한다. 모든 읽기는 cache를 거치지 않는다
(ChillServer의 uncached client, controller는 manager의 `APIReader`). 비교는 전체 문자열로 한다.

- `Acquire(node, owner)`: 같은 owner가 점유 중이면 성공한다. 다른 owner가 점유 중이면 `MeasurementPoolBusy`다. 비어
  있으면 serving namespace의 끝나지 않은 `app.kubernetes.io/component=serving-instance` Pod(`kube/operations.go:53,403`)가
  있을 때 `NodeServing`으로 거절하고, 그 노드의 끝나지 않은(Phase가 `Ready`, `Failed`가 아닌) NodeActuation 중
  controller owner가 이 owner가 아닌 것이 있으면 `MeasurementPoolBusy`다. 둘 다 없으면 Node UID와 resourceVersion
  조건으로 annotation을 patch한다(충돌하면 다시 읽는다). controller는 Busy인 동안 `RequeueAfter`
  (`measurementpool.RetryInterval`, 10초)를 반환한다. 세 controller는 Node를 watch하지 않으므로
  (`campaign/controller/setup.go:9-16`, `idlebaseline/controller/setup.go:9-16`, `transitioncost/controller/setup.go:87-96`)
  다른 owner의 반환은 이 재시도로 발견한다.
- `Release(node, owner)`: annotation이 자기 owner일 때 지운다.
- `Check(node, owner)`: 동작 직전에 Node를 읽어 annotation이 owner(Workload 동작은 빈 값)와 다르면
  `NodeInMeasurementPool`이다. serving Pod 생성(`kube/operations.go:403`, ChillServer와 `serving-operation`이 함께 쓰는
  prepare)은 Pod의 `edge.dacs.io/measurement-owner` annotation으로 확인한다. NodeActuator는 `ActionApplyTarget` 실행
  전(`internal/nodeactuator/controller/reconciler.go:302,369`)에 NodeActuation의 controller owner(측정 controller가
  붙인다, `campaign/controller/transition_builder.go:88`, `idlebaseline/controller/transition.go:108`,
  `transitioncost/controller/attempt_materializer.go:389`)로 확인하고 다르면 실패 reason `NodeInMeasurementPool`로
  끝낸다. 스케줄러의 NodeActuation(`kube/operations.go:740-769`)은 owner가 없으므로 측정 중인 노드에서 실패하고, 그
  action 실패로 다음 판단이 다시 계획한다. 점유 전 snapshot으로 계획한 prepare도 같은 사유로 실패한다.

ponytail: `Check`와 Pod 생성 또는 apply 사이, `Acquire`의 확인과 patch 사이에 상대 동작이 끼면 측정 중인 노드에
serving Pod나 전력 변경이 남을 수 있다. 각 구간은 API 호출 하나 길이다. 측정 결과는 노드의 Pod 목록(3.8절)과
NodeActuation을 기록하므로 겹침은 evidence에 드러난다. 겹침이 관측되면 Node resourceVersion을 쓰는 사후 확인으로
올린다.

기각: ChillServer를 annotation의 유일한 writer로 두고 프로세스 안에서 직렬화. `serving-operation`도 자기 프로세스에서
serving Pod를 만들고(`kube/operations.go`의 같은 prepare), 세 controller의 점유 요청을 ChillServer가 watch하고 승인하는
경로가 더 필요하다.

**실행 주체.** 각 주체는 Node에 대한 첫 동작(NodeActuation, Run, Job, 측정 인스턴스) 전에 점유하고, 자기 동작이 끝난
뒤 terminal 상태를 쓰기 전에 반환한다. 실패 경로도 같다.

| 실행 주체 | Acquire | Release |
|---|---|---|
| BatchEnergyOptimization controller | reconcile에서 `ActionCreateTransition`, `ActionCreateRun` 실행 전(`internal/profiling/campaign/controller/reconciler.go:153-183`) | Complete 또는 실패 전이(`:311-359`) |
| ModelLoadedIdleBaseline controller | `ensureRequiredState`의 NodeActuation 생성 전(`internal/profiling/idlebaseline/controller/transition.go:20-59`) | Complete 또는 Insufficient 전이(`reconciler.go:102-148`) |
| PowerModeTransitionCost controller | 준비(`internal/profiling/transitioncost/controller/preparer.go`) 전 | terminal status를 쓴 뒤 runner Pod와 Service가 모두 없어졌을 때(아래 runner 절) |
| `serving-operation --measurement-config` | 측정 시작 전 | 측정 인스턴스의 remove 완료 뒤. 측정이 실패해도 같은 순서 |

terminal 전이는 진행 중인 Run, Job, NodeActuation이 끝난 뒤에만 일어나고 Job Pod는 Run이 소유하므로, Release는 Job
Pod를 세지 않는다. `serving-operation`의 측정 인스턴스는 owner annotation을 갖고, remove 완료 확인(`kube/operations.go`의
기존 remove)이 반환 조건이다.

이동과 반환은 Node annotation으로 조회한다(5장 pools API). BatchEnergyOptimization은 Dashboard 측정 화면을 위해
`status.conditions`에 `MeasurementPool` 조건(Acquired, Busy, Refused, Released)을 더한다. `deriveStatus`
(`campaign/controller/reconciler.go:298-359`)는 status 전체를 새로 만들므로 기존 `MeasurementPool` 조건을 복사해 함께 쓴다.
ModelLoadedIdleBaseline과 PowerModeTransitionCost의 status는 바뀌지 않는다. `serving-operation`은 측정 결과의
`isolation`에 owner를 기록한다.

**terminal 사유 등록.** `NodeServing`은 Node에 아무 동작도 하지 않은 terminal 실패이고 `NodeNotInMeasurementPool`은
Run의 terminal 실패다. 두 사유를 기존 terminal 판정 지점마다 더하고 각 지점에 focused test를 둔다.

| 사유 | 판정 지점 |
|---|---|
| `NodeServing` | campaign lifecycle의 `ActionFail`(`internal/profiling/campaign/lifecycle.go:15-60`), `terminalCampaignStatusCurrent`(`campaign/controller/reconciler.go:393-410`), ChillServer `measurementTerminal`(`cmd/chill-server/measurements_evidence.go:34-47`), idle baseline Insufficient 전이(`idlebaseline/controller/reconciler.go:280-292`의 판정은 그대로). PowerModeTransitionCost는 Outcome `Failed`, Reason `NodeServing`, 빈 direction의 기존 result 기록(`transitioncost/controller/measurement_result.go:152-170`)을 써서 기존 `completeTerminalMeasurementStatus`(`reconciler.go:119-130`)가 그대로 terminal로 본다 |
| `NodeNotInMeasurementPool` | ProfilingRun `terminalPreJobFailureCurrent`(`profiler/controller/run_reconciler.go:369-384`), campaign `isTerminalReason`(`campaign/controller/projection.go:172-184`), idle baseline Run 판정(`idlebaseline/controller/reconciler.go:210-213`), ChillServer `terminalRunFailure`(`measurements_evidence.go:178-187`), `checkPreJobRunStopped`(`:233-243`) |

**Run과 Job 경계.** 점유자가 맞는지 확인한다. ProfilingRun reconciler는 `reconcileRequiredState`와
`ensureMeasurementJob` 전(`internal/profiling/profiler/controller/run_reconciler.go:113-120`)에 annotation이 Run의
controller owner(`campaign/controller/run_builder.go:98`, `idlebaseline/controller/run_builder.go:39`)와 같은지
확인한다. controller owner가 없거나 다르면 `NodeNotInMeasurementPool`로 실패한다. transition attempt Job 생성
(`transitioncost/controller/attempt_job.go:123`)은 annotation이 그 PowerModeTransitionCost인지 확인한다. 점유자는
자기 Release 전까지 바뀌지 않으므로 이 확인은 한 번 읽기로 충분하다. 현재 profiling 노드 해석은 소속을 확인하지
않는다(`internal/profiling/profiler/kube/node_resolver.go:33-73`).

**전력모드 전환 비용 측정의 runner.** 이 측정은 노드에서 모델을 서빙하는 native 인스턴스와 그 Service를 관측한다
(`api/v1alpha1/powermodetransitioncost_types.go:30-50`, `internal/profiling/transitioncost/native.go:45-61`,
`transitioncost/kube/observer.go:166-209`). ChillServer가 runner와 Service를 만든다.

- 이름과 표지. Service는 serving namespace의 `chill-transition-<PMTC UID>`, runner Pod는 `chill-transition-<PMTC UID>`이고
  둘 다 label `edge.dacs.io/measurement-uid=<PMTC UID>`, Pod는 `edge.dacs.io/workload-role=transition-runner`와 owner
  annotation을 갖는다. Service selector는 이 UID label이다. ChillServer는 이 label 값이 UID와 같은 객체만 고치고
  지운다. 같은 이름의 다른 객체가 있으면 `MeasurementRunnerNameTaken`으로 기다린다.
- 시작. ChillServer의 runner reconciler는 serving namespace의 이 label Pod와 profile namespace allowlist의
  PowerModeTransitionCost를 watch하고(기존 Pod watch 방식, `kube/observer.go:196`), 각 사건과 `RetryInterval`마다 한 번
  reconcile한다. annotation owner가 terminal이 아닌 PowerModeTransitionCost인 노드에 Service가 없으면, 3.5절 도출
  profile 중 Node UID, `spec.modelSpec`, `spec.executionPath`, `spec.modes[0].powerMode`에 맞는 최신 profile을 한 번
  고르고 그 ID를 Service annotation `edge.dacs.io/runner-profile`에 담아 Service를 만든다. profile이 없으면 scheduler
  상태에 `MeasurementRunnerEvidenceMissing:<owner>`를 남긴다.
- 교체. runner가 없거나 끝난(`Succeeded`, `Failed`) 상태면 끝난 Pod를 지우고 Service annotation의 profile로 다시
  준비한다. 재시작 전환 뒤에도 같다. 준비는 `measuredPreparation`과 기존 prepare(3.3절의 확인 포함)이고 PrepareSpec
  이름은 늘 `chill-transition-<PMTC UID>`다. profile이 같으므로 불변 실행 ConfigMap 검사(`kube/operations.go:495-524`)를
  그대로 통과하고, 모든 trial이 같은 runner 조건을 쓴다. 그 profile이 도출 목록에 없으면 다른 profile로 바꾸지 않고
  `MeasurementRunnerEvidenceMissing`이다. 측정 삭제 보호(`cmd/chill-server/measurements_evidence.go:575-592`)는 살아 있는
  runner Service가 가리키는 profile의 측정을 사용 중으로 본다.
- 측정. controller는 Service가 그 노드의 Ready Pod를 고를 때까지 `RetryInterval`로 requeue한다. 대기 시작은 불변
  preparation ConfigMap의 `creationTimestamp`이고(`transitioncost/controller/preparation_record.go:47-75`가 읽는 객체),
  Profiler 옵션 `--measurement-runner-timeout`을 넘기면 result Outcome `Failed`, Reason `MeasurementRunnerUnavailable`로
  끝낸다. 시작 시각이 저장된 객체에 있으므로 Profiler가 재시작해도 같은 기한이다. 대기 시간은 ChillServer 제어 주기와
  무관하다(watch 시작). preparation record는 Service 이름과 serving namespace를 담고 container는 상수
  `firstfloor`다(`observer.go:246`).
- 종료와 반환. controller가 terminal status를 쓰면 ChillServer는 runner를 기존 remove로, Service를 삭제로 지운다.
  controller는 terminal 뒤에도 annotation이 자기 owner인 동안 `RetryInterval`로 requeue해 runner Pod와 Service가 모두
  없어진 것을 확인하고 반환한다. 측정 객체가 지워지면 ChillServer가 같은 정리를 하고, annotation은 ADR 0022의 수동
  정리 대상이다.

**측정 인스턴스의 분리.** `edge.dacs.io/measurement-owner` annotation이 있는 serving Pod(전환 비용 runner와
`serving-operation` 측정 인스턴스)는 측정 인스턴스다. observer(`internal/serving/kube/observer.go:292-318`)는 같은 probe로
이 Pod를 관측하지만 `Runtime`의 인스턴스로 발행하지 않는다. 따라서 route 대상(`runtime_scheduler.go:39-43`,
`runtime.go:803-809`)과 활성 POOL 산출에 들어가지 않고, pools API의 측정 POOL 아래에 따로 보인다. prepare의 완료
확인은 측정 인스턴스에 대해 이 관측의 native Ready를 쓰고, Workload 인스턴스는 기존 runtime 편입을 쓴다.

**ChillServer와 스케줄러.** BatchEnergyOptimization 요청(`cmd/chill-server/measurement_service.go:131-152`)의 대상
노드가 활성 POOL에 있으면 `MeasurementNodeServing`과 사유를 반환한다(`DB-FR-34`). 최종 판정은 `Acquire`다.
스케줄러는 annotation 노드에 계획하지 않는다(3.4절). Profiler에는 `--serving-namespace`, `--measurement-runner-timeout`,
Node patch 권한과 serving namespace의 Service get, Pod list 권한(`charts/chill/templates/profiler_rbac.yaml:94-104`는 지금
Evidence Store Service만 허용)을, transition observer Job의 ServiceAccount
(`charts/chill/templates/profiler_transition_observer_rbac.yaml`)에는 같은 namespace의 Service get과 Pod list, watch를,
ChillServer에는 profile namespace의 PowerModeTransitionCost list와 watch, serving namespace의 Service 생성과 삭제
권한을 더한다. 두 Role은 설정한 serving namespace에 render한다.

`serving-operation`은 `measurement-*` POOL 규칙(`cmd/serving-operation/main.go:127-130,269-294`)을 `Acquire`로 바꾼다.
prepare 비용을 측정한 뒤 remove 비용을 측정할 때까지 노드는 같은 owner로 측정 POOL에 남는다. 격리 witness
(`main.go:308-332`)의 `MeasurementIsolation.Pool`(`kube/measurement.go:51-55`)에는 owner를 기록한다.

기각: ChillServer 단일 writer와 측정 객체 polling 반환. 실행 주체가 이미 아는 종료와 실패를 다시 관측해야 한다.
기각: Node별 Lease. 만료 뒤 늦게 도착한 요청을 막지 못하고, 겹침도 아직 관측되지 않았다.

### 3.4 전체 POOL, 가용 노드와 전력모드

전체 POOL은 `node-role.kubernetes.io/control-plane` label이 없는 모든 Kubernetes Node다. capture
(`kube/scheduler.go:531-556`)는 label로 거르지 않고 이 Node 전체를 snapshot에 넣는다. 측정 POOL 노드는 배치,
전력 변경, 제거 대상이 아닌 고정 노드로 들어간다. 가용 노드는 측정 POOL 밖에서 Ready이고 CHILL serving 인스턴스가
없는 노드다. 설정이 부족한 노드는 빼지 않고 노드 사유로 남긴다.

| 사유 | 조건 | 판단 |
|---|---|---|
| `PlacementInventoryMissing` | `cpuInventory`에 없음 | 배치 option에서 빠짐. 전력과 에너지는 계속 다룸 |
| `PlacementBudgetUnavailable` | CHILL이 배치하지 않은 Pod의 GPU 소유를 확인할 수 없거나 그 CPU 요청이 예약을 넘음 | 같음 |
| `PowerModeUnavailable` | DeviceClass나 `powerRequests` 항목 없음 | 현재 모드 유지 |

현재 CPU inventory 부족은 snapshot 사유가 되어 모든 후보를 평가 대기로 만들고(`kube/scheduler.go:543-546`),
`captureNodePods`는 CHILL이 배치하지 않은 Pod 때문에 `SchedulerNode.Ready=false`로 바꾼다(`kube/scheduler.go:699-722`).
둘 다 위 노드 사유로 바꾼다. 외부 Pod의 자원 요청은 기존처럼 `NonServing` 예산에 남고, 노드의 Ready는 Kubernetes
NodeReady만 따른다. 그래서 외부 Pod가 있는 노드도 가용 노드로 남아 전력모드를 내리고 에너지에 들어간다.

가장 낮은 전력모드는 그 노드의 unloaded baseline(State가 빈 `NodeBaseline`) 중 `Watts`가 가장 작은 모드이고,
같으면 이름순이다. DeviceClass의 `powerModes`는 순서와 전력 값을 갖지 않는다(`api/v1alpha1/deviceclass_types.go:8-42`,
`internal/deviceclass/powermode.go:11-42`).

가용 노드를 내리는 것은 탐색 축이 아닌 고정 규칙이고 모든 후보의 DAG에 공통으로 들어간다. `Controls.Power`가 켜져 있으면 모든 후보(유지 후보 포함)는
판단 시점의 모든 가용 노드를 시각 0부터 가장 낮은 모드에 둔 공통 상태에서 계산한다. 계속 비어 있는 노드의
에너지는 가장 낮은 모드의 unloaded baseline이다. 현재 모드가 다르면 후보의 DAG는 그 노드의 맨 앞에 power
동작을 두고 `Action.Fixed=true`로 표시한다. replay는 이 동작의 시간과 에너지를 세지 않고, 결정 기록의 assumption에
`AvailableNodeLoweringExcludedFromComparison`과 대상 노드를 남긴다. 판단에 target이 없으면 controller는 유지 후보의
`Fixed` 동작만 실행한다. 빈 노드 전환 비용 측정은 이 규칙에 필요하지 않다.

가용 노드를 활성 POOL로 다시 넣는 후보는 그 전환 비용을 비교에 포함한다. 노드 단위 순서(`scheduler_candidates.go:1009-1048`)에
따라 `Fixed` 내림 뒤에 power 동작(가장 낮은 모드에서 profile 모드로)과 prepare가 온다. 그 노드는 가장 낮은 모드에서
출발한 것으로 계산한다. 빈 노드의 power 동작 비용은 측정하지 않으므로, 같은 Node와 모드 쌍에서 이어서 준비할
인스턴스의 적재 상태로 측정한 전력모드 전환 비용(3.5절)을 쓴다. replay의 적재 상태 확인(`scheduler_replay.go:1219-1229`)은
이 경우에만 다음 prepare의 상태와 비교하고, 결정 기록의 assumption에 `ReentryPowerCostFromLoadedRunner`를 남긴다.
그 비용이 없으면 다시 넣는 후보는 `MissingOperationCost`로 평가할 수 없다.

후보 에너지는 측정 POOL 밖 노드를 합하고 아래 측정 POOL 항을 더한다. 노드의 계획 모드에 unloaded baseline이 없으면 그 후보의 에너지는
`MissingUnloadedNodeBaseline:<node>:<mode>`로 평가할 수 없다(`scheduler_replay.go:1712-1713`). 현재 모드의
baseline이 없으면 모든 후보가 그렇고, 판단은 기존 규칙대로 `NoFullyEvaluableCandidateWithinReportedBounds`와 함께
Pending이 된다(`scheduler_replay.go:122-160`).

측정 POOL 노드의 소비는 모든 후보에 같은 값으로 더한다. 값은 그 노드 whole-node meter의 최근 관측 구간 평균 전력에
비교 기간을 곱한 것이며, 기존 운영 전력 집계(`ObservationLog.OperatingPower`, `cmd/chill-server/scheduler_service.go:399-409`)를
그 노드의 meter로 읽는다. 값이 없으면 모든 후보의 에너지가 `MeasurementNodePowerUnobserved:<node>`로 평가 불가다.

### 3.5 후보 도출

`Catalog.Refresh`(`internal/serving/kube/catalog.go:86-128`)는 선택 목록 대신 profile namespace allowlist
(`measurement_service.go:59-71`)의 모든 BatchEnergyOptimization 중 terminal 성공(`cmd/chill-server/measurements_evidence.go:34-47`)을
읽는다. 조건별 최신 성공 선택은 기존 `latestConditionProfiles`(`catalog.go:154-181`)를 그대로 쓴다.
profile의 측정 노드가 그 Workload의 후보 노드가 된다(`kube/scheduler.go:560-585`의 `MatchesProfile`).

`PrepareSpec`은 refresh가 profile마다 기존 `checkProfileInventory`와 `measuredPreparation`
(`cmd/chill-server/profiles.go:465-490,516-615`)으로 만들고 저장하지 않는다. serving-pool 확인(`:533-536`)과
`Instance.Pool`(`:571`)은 삭제한다. bundle 발행과 binding
(`scheduler_service.go:315-352`)도 profile마다 수행한다. 한 profile의 실패는 그 profile의 `CatalogIssue`
(`PreparationUnavailable`)가 되고 다른 profile과 refresh는 계속한다(`SCH-08`). 현재는 하나의 실패가 refresh 전체를
멈춘다(`scheduler_service.go:322-331`). profile ID 집합이 바뀌면 다음 refresh가 새 구성을 적용한다(`:295-304`).

삭제 대상은 `profileCampaigns`, `preparations`(`scheduler_service.go:39,44,133-136`), 채택과 해제
(`profiles.go:65-464`, `httpapi/profiles.go`의 POST와 DELETE, `handler.go:104-105`)다. `readSelectedProfiles`(`profiles.go:41-64`)는
모든 측정을 읽는 경로가 된다. 측정 삭제 보호
(`measurement_service.go:245-275`, `measurements_evidence.go:461-491,575-582`)는 현재 인스턴스가 쓰는 profile의
측정만 보호하는 규칙으로 바꾼다.

**비용 근거.** prepare와 remove 비용, unloaded baseline, 전력모드 전환 비용도 같은 방식으로 도출한다. scheduler 설정의
`costReferences`, `powerCostSources`(`scheduler_service.go:45-46,305-314`)와 `PowerCostSource`(`kube/cost_power.go:24-27`)를
삭제한다.

| 근거 | 위치와 나열 | 받아들이는 기록 | 조건별 최신 |
|---|---|---|---|
| operation 측정(`serving-operation`) | ChillServer store의 고정 prefix `serving/operation-measurements/<Node UID>/<operation ID>.json`. `serving-operation`이 이 규칙으로 key를 만들고 manifest는 key를 받지 않는다 | Verdict `Accepted`, 기존 검사(`kube/cost.go:27-98`) 통과, 기록의 meter(ID, Node, scope, source)가 현재 scheduler 설정 `meters`의 항목과 모두 같음 | 비용은 (Node UID, kind, from, to, 노드 전력모드)마다 `Result.EndedAt`이 가장 늦은 것. idle 기록의 baseline은 (Node, 전력모드, State)마다 하나 |
| 전력모드 전환 비용 | profile namespace allowlist의 PowerModeTransitionCost 중 기존 `loadPowerCost` 조건(`kube/cost_power.go:75-130`)을 통과한 Complete. meter는 scheduler 설정 `meters`에서 같은 Node와 같은 source(`prepared.PowerSource`)를 가진 항목이다 | 같음 | (Node UID, from, to, 적재 상태)마다 받아들인 trial의 완료 시각 중 가장 늦은 값이 큰 측정, 같으면 이름순. 적재 상태는 runner의 profile과 할당을 담은 `BeforeState`(`cost_power.go:195-199`)이고 profile이 모델, 실행 경로, 장비를 정한다 |

두 경우 모두 그 시각을 새 필드 `OperationCost.MeasuredAt`에 담는다. 조건이 다른 비용은 서로 대신하지 않는다. replay는
이미 prepare와 remove의 노드 전력모드(`scheduler_replay.go:1230-1233`)와 전력모드 변경의 적재 상태(`:1219-1229`)를
확인하므로, planner의 비용 선택(`scheduler_candidates.go:882-886`)도 같은 열쇠로 고른다. 맞는 비용이 없으면 기존대로
`MissingOperationCost`다(`scheduler_replay.go:1205-1210`).

나열은 새 `evidencestore.Lister`(`List(ctx, prefix) ([]ObjectIdentity, error)`)로 한다. S3 구현은 `ListObjectsV2`를
page 끝까지 읽는다(`internal/evidencestore/s3/client.go:10-14`에 메서드 추가). store 자격 증명에 list 권한이
필요하다[INFERENCE: 현재 권한은 확인하지 않음]. 객체는 불변이므로 ChillServer는 identity별로 한 번만 읽어 결과를
보관하고, 더는 나열되지 않는 identity는 버린다. PowerModeTransitionCost는 UID와 result identity로 같은 보관을 한다.

한 기록이나 측정의 실패는 그 출처의 `CostIssue{Source, Code, Reason}`(`CostEvidenceInvalid`, `MeterUndeclared`)가 되어
scheduler 상태에 보이고 나머지는 계속 쓴다. 현재는 하나의 실패가 refresh 전체를 멈춘다(`scheduler_service.go:305-313`).
나열 자체가 실패하면 그 refresh가 실패하고 이전 구성이 유지된다(`:286-304`). refresh의 재사용 판단(`:301`)은 비용
identity 집합도 비교한다. 측정 삭제 보호의 비용 목록 항목(`measurements_evidence.go:584-592`)은 삭제한다.

기각: 운영자 채택과 비용 선언 유지. SRS의 운영자 역할에서 적용 대상 지정이 빠졌다.

### 3.6 완료 여유

`SchedulerSettings.CompletionMarginMS float64`(`json:"completionMarginMS"`)는 유한한 0 이상의 필수 값이며
`SchedulerSettings.Validate`(`scheduler.go:249-260`)가 확인한다. 생략과 0을 구분하도록 설정 decode 경계
(`cmd/chill-server/scheduler_service.go:110-138`, settings PUT `httpapi/scheduler.go:64-143`)는 이 필드를 `*float64`로
받아 없으면 `completionMarginMS required`로 거절하고, 확인한 값만 `SchedulerSettings`에 넣는다. 요청의 예상 완료는
firstfloor 처리 완료에 이 값을 더한 시각이다.

- replay: 반환 완료와 만료 계산(`scheduler_replay.go:566-575,829-832,988-1005,1107-1111`)이 instance의
  `CompletionOverhead` 대신 설정 값을 쓴다.
- firstfloor: `RequestContext`(`internal/firstfloor/types.go:54-62`)에 `completionMarginMS`를 더하고 ChillServer가
  dispatch 때 현재 설정 값을 넣는다(`internal/serving/firstfloor/client.go:178`). `requestSlack`은
  `RemainingSLOMS - completionMarginMS - serviceCost`이고(`internal/firstfloor/policy.go:22-27`), lifetime 수락도
  같은 값을 뺀다(`internal/firstfloor/server.go:215-220`). 여유는 sender 경과로 줄이지 않는다.
- `Policy.CompletionOverheadMS`, `Policy.EnvelopeSource`(`types.go:92-93`)는 삭제한다. 정책 ACK 비교
  (`kube/scheduler.go:1091-1121`)에서도 빠진다.
- 여유는 Pending 사유가 되지 않는다. `CompletionOverheadUnobserved`(`scheduler_replay.go:290-294,461-466`)와
  `completion calibration Pending`(`kube/scheduler.go:602-603,618-620`)을 삭제한다.

검증은 Serving 관측으로 한다. `ObservationCohort`(`internal/serving/observation_log.go:517-526`)에
`completionResidualNanos`를 더한다. 완료 요청마다 종단 경과(`RequestEvent.ElapsedNanos`)에서 전달 시점 경과와
firstfloor 경과(`NativeProgress.NativeElapsedMS`)를 뺀 값이며(`types.go:244-248`), 지연 표본과 같은 자리
(`observation_log.go:697-699`)에서 모은다. Dashboard Workload 관측은 이 분포와 설정 여유를 함께 보인다.

초기 값은 E1(9.2절)에서 정한다. 그 전까지 sample은 잠정 값을 쓰고 [설정과 관측 API](scheduler-api.md)의 설정 표에
잠정임을 적는다. E1의 `completionResidualNanos` 분포에서 값을 정해 설정과 sample에 반영하고, E2부터 그 값을 쓴다.

기각: `Policy` 필드로 전달. 설정을 바꿀 때마다 모든 인스턴스 policy를 다시 써야 하고 꺼진 Workload의
인스턴스도 바뀐다.

### 3.7 수동 변경 범위

수동 변경 하나는 꺼진 Workload 하나에 적용한다.

- `staticRouting`은 꺼진 Workload의 UID만 담는 부분 map이다. 담지 않은 Workload는 현재 분배를 유지하고, 켜진
  Workload의 항목은 거절한다. 전체 map 규칙(`internal/serving/static_routing.go:13-15`)은 삭제하고 항목별 검증
  (`:17-54`)은 유지한다. 적용 조건(`kube/static_routing.go:30`)은 항목의 Workload가 모두 꺼져 있는 것이다. 한
  Workload의 분배에서 인스턴스를 빼면 그 Workload의 binding만 풀린다(`static_routing.go:39-50`).
- `serving-operation --execute-config`의 manifest는 꺼진 Workload 하나의 `workloadUID`를 받는다. prepare는 준비한
  인스턴스의 owner로 그 Workload를 기록한다. remove는 그 Workload가 인스턴스의 유일한 owner이자 binding일 때만
  허용하고, 다른 Workload가 쓰는 인스턴스는 `InstanceShared`로 거절한다. 그 경우 위 `staticRouting`으로 그
  Workload의 분배와 binding만 뺀다. 전체 controls OFF 조건(`cmd/serving-operation/main.go:450-462`)과 membership
  동작(`:554-558,733-746`)은 삭제한다. 진행 중 action 조건(`:463-467`)과 staticRouting ACK 확인(`:468-483`)은 유지한다.

### 3.8 외부 파드

NodeActuator의 workload safety를 삭제한다: `internal/nodeactuator/controller/workload_safety.go` 전체,
`lifecycle.go:142-156`의 `WorkloadSafetyBlocked`와 CheckingSafety 단계, `controller/reconciler.go:279-288`,
CRD의 `CheckingSafety`, `Safety` 단계와 `status.safety`(`api/v1alpha1/nodeactuation_types.go:6,12,22,28,114-145,190-192`),
`cmd/node-actuator/options.go:41-44,92-93`, `controller_setup.go:25-45`, chart의 허용 namespace
(`charts/chill/values.yaml:422-423`, `templates/node_actuator_config.yaml:24-25`), idle baseline의 단계 참조
(`internal/profiling/idlebaseline/controller/transition.go:148`).

CHILL이 배치한 Pod는 label `edge.dacs.io/workload-role`(`profilingrun.MeasurementWorkloadLabelKey`,
`internal/profiling/run/workload.go:9`)을 가진 Pod다. profiling Job은 이미 값 `profiling-run`을 붙이고
(`profiler/controller/execution_job_spec.go:89-92`), workload observer는 이 key를 이미 보존한다(`run/workload.go:36-40`).
따라서 profiling evidence의 계약은 바뀌지 않는다. 나머지 생성 경로에 역할 값을 붙인다.

| 생성 경로 | 값 |
|---|---|
| serving 인스턴스(`kube/operations.go:403`) | `serving-instance`, 전환 비용 runner는 `transition-runner` |
| transition attempt Job(`transitioncost/controller/attempt_job.go:116-119`) | `transition-observer` |
| operator 생성 Pod(`internal/operator/{evidencestore/deployment.go,evidencestore/bootstrap_job.go,nodeactuator/daemonset.go,profiler/deployment.go,nodediscovery/daemonset.go}`) | `system` |
| chart Pod template(`charts/chill/templates/{operator,serving_entrypoint,console,dashboard,predelete_cleanup}.yaml`, selector는 그대로) | `system` |
| `hack/runtime-declarations.py:142-144`의 probe | `system` |

측정 결과는 노드 Pod를 다음과 같이 기록하고, Dashboard는 이 label이 없는 Pod를 CHILL이 배치하지 않은 파드로
표시한다. 이 branch 이전의 evidence에는 serving과 system Pod의 label이 없어 외부로 표시된다.

| 측정 | 기록 |
|---|---|
| profiling(BatchEnergyOptimization, idle) | 기존 workload observer 결과(`internal/workloadobserver/types.go:49-89`). console projection(`internal/operatorconsole/workload_view.go:25-37`)에 `chillPlaced`를 더한다 |
| 전력모드 전환 비용 | `NativeNodeObservation.Pods []string`(`transitioncost/native.go:37`)을 `[]workloadobserver.Pod`로 바꾼다 |
| `serving-operation` | `OperationState.Pods []string`(`kube/operations.go:85`)을 `[]workloadobserver.Pod`로 바꾼다 |

두 변환은 `projectPod`(`internal/workloadobserver/kube/projection.go:52-105`)를 export해 같은 label key로 쓴다.
`.go-arch-lint.yml`에 serving-kube와 transition-cost-kube의 workload 의존을 더한다. 에너지는 노드 전체 meter 값을 그대로 쓴다.

## 4. 스케줄러 판단

### 4.1 입력

snapshot의 노드는 3.4절의 전체 POOL이고 측정 POOL 노드는 고정 노드다. option은 3.5절의 최신 profile과 그 profile에
맞는 켜진 Workload의 조합이다. 인스턴스는 꺼진 Workload의 것을 포함해 모두 넣는다.

### 4.2 Workload 판단 상태

판단마다 Workload별 상태를 `SchedulerDecision.Workloads []SchedulerWorkloadState{UID, State, Reason}`로 기록한다.

| State | 조건 | 판단 |
|---|---|---|
| `Disabled` | 자동제어 꺼짐 | 마지막 구성 유지, 4.4절의 영향 평가 대상 |
| `MeasurementEvidenceMissing` | 켜졌고 option 없음 | 후보 생성에서 빠지고 다른 Workload의 판단은 계속 |
| `Controlled` | 켜졌고 option 있음 | 후보 생성 대상 |

### 4.3 후보 생성

- 열거(`scheduler_candidates.go:288-344`)는 `Controlled` Workload만 바꾼다. 꺼진 Workload의 분배는 현재 값을 복사한다.
- 꺼진 Workload가 owner이거나 binding된 인스턴스는 제거, 자원 변경, policy 변경 대상에서 빠진다. `Controlled`
  Workload는 그 인스턴스로 가는 자기 분배를 바꿀 수 있다.
- 한 노드의 인스턴스는 모두 같은 ModelSpec을 참조한다.
- `Controls.Membership`이 꺼져 있으면 `Controlled` Workload의 option은 그 Workload의 활성 POOL 노드에 한정한다
  (`scheduler_candidates.go:767`의 Pool 비교를 대체).
- `Controlled` Workload마다 분배 가중치가 양수인 인스턴스가 하나 이상 있어야 한다. 이를 어기는 후보는 유지 후보를
  포함해 `MinimumInstance:<uid>` 사유로 선택하지 않는다. 부하가 0이어도 같다.
- 3.2절의 연결 없는 인스턴스 정리를 포함한다. 가용 노드를 내리는 동작은 탐색 축이 아니며 모든 후보의 DAG에 공통
  `Fixed` 동작으로 들어간다(3.4절).
- 활성 POOL 사이의 직접 이동은 같은 노드의 remove와 prepare를 한 후보의 DAG로 만든다. 노드 단위 순서
  (`scheduler_candidates.go:1009-1048`)는 membership 동작 없이 유지하고, 비용은 각 동작 비용의 합이다.
- 첫 구성은 현재 인스턴스가 0인 `Controlled` Workload에 대해 option의 배치를 그대로 만든다. option 허용 규칙
  (`scheduler_candidates.go:772-787`)에 따라 placement, resources, batching control이 켜져 있어야 한다.

### 4.4 SLO 판정 대상과 에너지

후보의 SLO 판정 대상은 `Controlled` Workload 전체와, 인스턴스가 계획된 동작이 있는 노드에 있거나 인스턴스의
binding이 바뀌는 `Disabled` Workload다. 전력모드 변경은 노드의 동작이므로 이 규칙에 포함된다. 나머지 Workload의
예측도 기록한다. 에너지는 3.4절의 규칙으로 4.1절 노드 전체를 합한다.

### 4.5 적용과 기록

`Target`이 있으면 적용한다(`kube/scheduler.go:284`의 전체 활성 조건 삭제). 결정 기록(`cmd/chill-server/scheduler_service.go:411-416`)은
모든 후보와 snapshot을 담고 option은 완료된 측정 수만큼 늘어난다. 저장 기록의 `SchedulerInstance`는 profile 본문 대신
`profileID`만 담고, profile 본문은 catalog(`GET /api/v1/scheduler/profiles`)에서 조회한다. HTTP 조회의 기존 압축
(`internal/serving/httpapi/scheduler_presentation.go:17-73`)은 조회 경계에 그대로 둔다.

## 5. API와 Dashboard

| 표면 | 계약 | 요구사항 |
|---|---|---|
| `GET /api/v1/pools`(`handler.go:95`) | `workloads[]{workload, nodes, instances}`, `measurement[]{node, owner, instances}`, `available[]{node, ready, powerMode, reasons}`, 전체 POOL의 나머지 노드와 사유. 활성 POOL은 runtime 인스턴스, 측정 POOL은 Node annotation, 가용 노드는 최신 snapshot에서 만든다. `GET /api/v1/pools/{id}`(`:96`)와 `Runtime.Pools`(`runtime.go:859-919`), `PoolState`(`types.go:217-223`)는 이 형태로 바꾼다 | `DB-FR-10`, `WM-25` |
| `GET /api/v1/scheduler/state` | `decision.workloads`, 노드 사유, `CostIssue`, `MeasurementRunnerEvidenceMissing` | `DB-FR-33`, `SCH-08` |
| `PUT /api/v1/scheduler/settings` | `autonomousWorkloads`, `completionMarginMS` | `DB-FR-33`, `SCH-32` |
| `GET /api/v1/scheduler/profiles` | 도출된 profile과 `CatalogIssue` 조회 | `SCH-08`, `SCH-09` |
| BatchEnergyOptimization POST | `MeasurementNodeServing` 거절 | `DB-FR-34` |

Dashboard:

- `chill-dashboard/features/resources.tsx:54-79`, `service-hub.tsx:34`: Workload별 활성 POOL, 측정 POOL, 가용 노드와
  여러 활성 POOL이 함께 쓰는 노드, 노드 사유.
- `features/management.tsx:440` Workload 상세: 자동제어 스위치, 판단 상태와 사유, 꺼진 Workload의 마지막 구성 유지와
  수동 변경 결과, 완료 여유 검증 분포.
- `features/scheduler-settings.tsx:38,181-187`: `enabled` 항목 삭제, `completionMarginMS` 편집.
- `features/measurement-request.tsx:143-147,313-323`: 노드 소속 표시, 서빙 중인 노드 선택 시 거절 사유.
- `features/measurement-plans.tsx:69-76,122-125`: `MeasurementPool` 조건의 이동과 반환, CHILL이 배치하지 않은 파드 목록, 채택 동작 삭제.
- `features/scheduler-profiles.tsx:58-85,114-149`: 조회 전용.
- `features/scheduler.tsx:44-58,148-151`, `lib/read-types.ts:46-47,58-61`, `lib/scheduler.ts:15-20`: Pool과 completion 필드 삭제.

## 6. 삭제 목록

| 대상 | 위치 |
|---|---|
| completion calibration selector와 loader | `internal/serving/observation_log_calibration.go` 전체와 test |
| 관측 overhead 산출 | `observation_log_scheduler.go:281-430`, test `:81-203,503-541` |
| instance completion 필드 | `scheduler.go:77-84`, `scheduler_candidates.go:439-442,642,732-735,813-854,988-997` |
| calibration 연결 | `kube/scheduler.go:43-45,497-498,586-624,953-972`, `kube/operations.go:64`, `cmd/chill-server/scheduler_service.go:215-264,353-358`, `cmd/serving-operation/main.go:514-542`, `measurements_evidence.go:464-491`의 calibration 항목 |
| calibration 조회 | `httpapi/scheduler_presentation.go:20-23,49-73,87-110`의 completion 사전 |
| calibration test | `scheduler_test.go:740-813`, `kube/scheduler_test.go:559-646`, `cmd/serving-operation/main_test.go:297-309` |
| serving-pool label, membership, `Pool` 필드(`Assignment.Pool` 포함) | 3.2절 목록 |
| 채택, 저장 preparation, 비용 선언(`costReferences`, `powerCostSources`, `PowerCostSource`) | 3.5절 목록 |
| `Controls.Enabled` | 3.1절 목록 |
| NodeActuator workload safety | 3.8절 목록 |
| `measurement-*` 격리 POOL | `cmd/serving-operation/main.go:127-130,269-294` |

## 7. 저장 계약

| 계약 | 현재 | 이 branch의 값 | 바꾸는 commit | 정리 대상 |
|---|---|---|---|---|
| scheduler ConfigMap JSON(`scheduler_service.go:38-50`, version 없음, strict decode) | `profileCampaigns`, `preparations`, `costReferences`, `powerCostSources`, `controls.enabled`, 전체 `staticRouting` | 다섯 필드 삭제, `settings.autonomousWorkloads`, 필수 `settings.completionMarginMS`, 부분 `staticRouting` | 2, 3, 4 | 새 sample로 ConfigMap 재작성 |
| Node label `edge.dacs.io/serving-pool` | 노드 소속 | 삭제 | 3 | 모든 노드에서 label 제거 |
| Node annotation `edge.dacs.io/measurement-pool`, Pod annotation `edge.dacs.io/measurement-owner` | - | 신규 | 5 | - |
| `observationSchema`(`observation_log.go:21`) | `serving-observations.v1alpha3` | `v1alpha4`(`Pool`과 `Assignment.Pool`, completion 필드 삭제, `decision.workloads`, 노드 사유, 저장 기록의 `profileID`, `completionResidualNanos`, `OperationCost.MeasuredAt`, `Action.Fixed`) | 2(version), 3, 4 | PVC와 evidence store의 serving 관측 window |
| `OperationMeasurementSchema`(`kube/measurement.go:20`) | `serving-operation-measurement.v1alpha1`, manifest가 정한 key | `v1alpha2`(포함한 `Operation`, `OperationState`, `PrepareSpec`, `SchedulerInstance`의 필드 삭제, 구조화한 `Pods`, isolation owner), key는 3.5절의 고정 prefix. strict decode(`cost.go:77-85`, `cost_power.go:50-61`) | 2(version), 3, 4, 5 | 모든 operation 측정 기록(prepare, remove 비용과 unloaded baseline) |
| firstfloor policy와 요청(Go wire, version 없음) | `completionOverheadMS`, `envelopeSource` | `RequestContext.completionMarginMS` | 2 | 모든 serving 인스턴스 Pod와 instance ConfigMap |
| `RawRecordSchemaVersion`(`transitioncost/raw_record.go:11`) | `v1alpha5` | `v1alpha6`(native state의 firstfloor policy 형태, 구조화한 `Pods`) | 2(version), 5 | 모든 PowerModeTransitionCost와 그 evidence |
| `FinalizedRecordSchemaVersion`(`raw_record.go:12`) | `v1alpha3` | `v1alpha4`(`BeforeNative`, `AfterNative`, `finalized.go:32-33`) | 2(version), 5 | 위와 같음 |
| NodeActuation CRD | `CheckingSafety`, `Safety`, `status.safety` | 삭제. 측정 POOL 거절은 기존 실패 기록에 reason `NodeInMeasurementPool` | 1, 5 | 모든 NodeActuation 객체 |
| `PreparationRecordSchemaVersion`(`raw_record.go:14`), ChillServer의 `powerPreparationWire`(`kube/cost_power.go:220-225`) | `v1alpha4`, `servingService`, `servingContainer` | `v1alpha5`(생성 Service 이름과 serving namespace, container는 상수) | 5 | 모든 PowerModeTransitionCost와 그 evidence |
| transition attempt 요청(`attempt_job.go:28`, 전달용) | `v1alpha4` | `v1alpha5`(같은 필드) | 5 | - |
| PowerModeTransitionCost CRD | `spec.servingService`, `spec.servingContainer` 필수 | 두 필드 삭제 | 5 | 모든 PowerModeTransitionCost 객체 |
| BatchEnergyOptimization CRD | `MeasurementPool` 조건 없음 | `MeasurementPool` 조건(기존 `status.conditions`) | 5 | - |
| Pod label `edge.dacs.io/measurement-uid`, runner Service `chill-transition-<UID>`와 그 annotation `edge.dacs.io/runner-profile` | - | 신규 | 5 | - |
| node-actuator 설정, Profiler 옵션 | 허용 namespace | 허용 namespace 삭제, Profiler `--serving-namespace`, `--measurement-runner-timeout` | 1, 5 | Helm으로 다시 render |
| `serving-operation` manifest(전달용) | `isolatedPool`, `expectedPool`, membership | `measurementOwner`(UUID), `workloadUID` | 3, 5 | - |

외곽 형태가 그대로인 transition-cost `MeasurementResultSchemaVersion` `v1alpha3`은 유지하고, 위 정리 대상과 함께
삭제한다. BatchEnergyOptimization과 ProfilingRun, profiling evidence는 바뀌지 않으므로 기존 측정이 바로 후보가 된다.
CHILL 배치 판정은 이미 보존된 label을 읽는 규칙이라 profiling evidence에 새 version이 필요 없다. transition-cost
raw 기록은 구조화한 `Pods`를 계속 담는다(`SCH-34`).

merge 절차: 진행 중 campaign이 끝난 뒤 merge한다. serving 인스턴스 Pod와 instance ConfigMap, NodeActuation 객체,
PowerModeTransitionCost 객체, serving 관측 window, operation 측정 기록과 transition-cost evidence를 지우고 노드의
serving-pool label을 제거한다. Helm upgrade 뒤 새 sample로 scheduler ConfigMap을 쓰고 inventory, meter, powerRequests를
다시 넣는다. 측정 POOL에서 전체 POOL의 노드마다 모드별 unloaded baseline과 prepare, remove 비용, 전력모드 전환 비용을
다시 측정한다. 결과는 3.5절의 나열로 바로 비용 근거가 된다. baseline이 없는 노드가 있으면 3.4절에 따라 판단이
Pending이므로 이 측정이 E2E의 선행 조건이다.

## 8. 커밋 단위

모든 commit은 단독으로 build되고 `make lint`와 `make test`(envtest로 `test/integration`을 포함)를 통과한다. 아래 검사는
[AGENTS.md](../../../AGENTS.md)의 표면별 기준으로 그에 더한다. 저장 계약의 version은 그 계약을 처음 바꾸는 commit에서
한 번 올리고, 이후 commit은 같은 미배포 version의 형태를 이어서 바꾼다([이기종 워크로드 8장](heterogeneous-workloads.md#8-저장-계약)과
같은 규칙). 중간 commit은 배포하지 않고 merge 뒤 7장 절차로 한 번 배포하므로, 저장된 객체에는 계약마다 한 형태만 생긴다
([ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)). 각 commit은 producer와 모든 consumer를 함께 바꾼다.

| # | 제목 | 파일과 package | 선행 | 요구사항 | 추가 검사 |
|---|---|---|---|---|---|
| 1 | `feat(nodeactuator): actuate regardless of external pods` | 3.8절의 NodeActuator 목록과 남은 safety 참조 전체: `internal/profiling/campaign/controller/projection.go:75-79`, `internal/nodeactuator/controller/projection.go:70-81`, `internal/profiling/idlebaseline/controller/transition.go:148`. test: `internal/nodeactuator/lifecycle_test.go`, `controller/reconciler_test.go`, `controller/workload_safety_test.go`(삭제), `internal/profiling/transitioncost/kube/observer_test.go:174-177`, `test/integration/operator/nodeactuation_api_test.go:95-104`. 생성 CRD와 deepcopy, chart | 없음 | 시스템 명세 외부 파드, `SCH-34` | `make manifests generate`와 생성 drift 확인, `go test ./api/... ./internal/nodeactuator/... ./internal/profiling/campaign/... ./internal/profiling/idlebaseline/... ./internal/profiling/transitioncost/... ./cmd/node-actuator/...`, `make helm-lint helm-template`(default와 Profiler) |
| 2 | `refactor(serving): replace completion calibration with a configured margin` | 3.6절, 6장의 calibration 행, `internal/firstfloor`, scheduler 설정 decode의 필수 확인, observation과 operation 측정의 version, transition-cost raw와 finalized version, Dashboard 설정과 scheduler 조회, `config/samples/serving-scheduler.json`(잠정 `completionMarginMS`) | 없음 | `SCH-13`, `SCH-21`, 03, 04 | `go test ./internal/firstfloor/... ./internal/serving/... ./internal/profiling/transitioncost/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make dashboard-check` |
| 3 | `feat(serving): derive active pools and control autonomy per workload` | 3.1, 3.2, 3.4(`captureNodePods` 포함), 3.7절, 4장(4.5의 profile ID 제외), pools API와 Dashboard 자원, Workload 상세, 설정 화면, `serving-operation`의 membership 삭제와 `workloadUID` | 2 | `SCH-02`, `SCH-15`, `SCH-16`, `SCH-32`, `SCH-33`, `SCH-AC-22`, `SCH-AC-24`, `SCH-AC-25`, `WM-25`, `DB-FR-10`, `DB-FR-33` | `go test ./internal/serving/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make dashboard-check`, `make gopls-check` |
| 4 | `feat(serving): derive candidates and costs from completed measurements` | 3.5절(`evidencestore.Lister`, S3 `ListObjectsV2`, operation 측정의 고정 key 포함), 4.5절의 저장 profile ID, 측정 삭제 보호, Dashboard profile 화면과 채택 동작 삭제, `README.md:184-197` | 3 | `SCH-08`, `SCH-09`, `SCH-16` | `go test ./internal/serving/... ./internal/evidencestore/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make dashboard-check` |
| 5 | `feat(profiling): measure only in the measurement pool` | 3.3절 전체(`internal/measurementpool`, 세 controller의 점유와 반환, Busy requeue, `Check`의 serving 생성과 NodeActuator 적용, Run과 Job 경계, terminal 사유와 그 test, BatchEnergyOptimization 조건, transition-cost runner와 preparation, attempt `v1alpha5`, 측정 인스턴스 분리, `serving-operation` 점유), 3.8절의 역할 label, 구조화한 `Pods`, console `chillPlaced`, CRD, `cmd/profiler` 옵션, Profiler, observer, ChillServer RBAC, `.go-arch-lint.yml`과 `architecture-coverage.sh`, Dashboard 측정 화면 | 1, 4 | `SCH-34`, `SCH-AC-26`, `DB-FR-34`, 00 측정 POOL | `make arch-check`, `make manifests generate`, `go test ./api/... ./internal/measurementpool/... ./internal/nodeactuator/... ./internal/profiling/... ./internal/serving/... ./internal/workloadobserver/... ./internal/operator/... ./internal/operatorconsole/... ./cmd/profiler/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make helm-lint helm-template`(default와 Profiler), `make deployment-graph-check`, `make dashboard-check` |
| 6 | `docs(design): record workload pool verification` | 9.3절 기록, 10장의 문서, `docs/design/serving/README.md` | 5 | 전체 | 문서 링크 확인 |

1과 2는 동시에 진행한다. 3과 4는 순서대로 진행하고, 5는 1과 4 뒤에 진행한다. 생성 파일을 바꾸는 commit은 각 merge 뒤
`make manifests generate`를 다시 실행한다.

## 9. 검증 계획

### 9.1 단위와 계약 검사

| 대상 | 검사 |
|---|---|
| firstfloor | slack과 lifetime이 `completionMarginMS`를 sender 경과와 무관하게 한 번 뺀다 |
| replay | 완료 시각이 native 완료 + 여유, 관측 여부와 무관하게 Pending 사유 없음 |
| 소속 도출 | owner, binding, 둘 다 없는 인스턴스의 노드 분류, 같은 노드의 두 Workload 활성 POOL |
| 전체 POOL | inventory 없는 노드와 전력모드 수단 없는 노드가 사유와 함께 남음, baseline 없는 노드가 에너지 평가 불가와 Pending을 만듦, 측정 POOL 노드 항이 모든 후보에 같게 더해지고 관측이 없으면 평가 불가 |
| 후보 | 꺼진 Workload 인스턴스 고정, 최소 인스턴스, 같은 모델 조건, 직접 이동 DAG, 모든 후보가 가용 노드를 가장 낮은 모드에서 출발한 공통 상태로 계산하고 `Fixed` 내림이 그 노드의 다른 동작보다 앞서며 그 시간과 에너지가 빠지고 assumption이 남음, target이 없을 때 유지 후보의 `Fixed` 동작만 실행, 다시 넣는 후보의 power 비용이 다음 prepare 상태의 전환 비용으로 계산됨, 판정 대상 집합 |
| 도출 profile과 비용 | 같은 조건의 최신 성공 선택, 한 profile이나 비용 기록의 실패가 나머지를 막지 않음, 고정 prefix 나열과 identity별 한 번 읽기, `Accepted`만 받음, 현재 `meters`와 다른 meter의 operation 기록과 meter 없는 전환 비용의 `MeterUndeclared`, operation 비용은 노드 전력모드까지 같은 조건에서 최신, 전환 비용은 적재 상태가 다른 측정을 섞지 않고 trial 완료 시각으로 최신, planner가 같은 열쇠로 고름, 나열 실패 때 이전 구성 유지 |
| 수동 변경 | 부분 `staticRouting`, 켜진 Workload 항목 거절, 공유 인스턴스 remove의 `InstanceShared` |
| 측정 POOL | `Acquire` 거절(서빙 Pod), 다른 owner의 끝나지 않은 NodeActuation이 있으면 Busy, Busy의 `RequeueAfter`와 다른 owner 반환 뒤 점유, 점유 뒤 serving 생성과 스케줄러 NodeActuation의 `NodeInMeasurementPool` 거절과 측정 owner NodeActuation의 통과, 같은 이름으로 다시 만든 측정 객체가 UID로 구분됨, Run과 Job의 owner 불일치 거절, 첫 NodeActuation 전 점유, 실패 경로의 반환, 측정 인스턴스가 route 대상과 활성 POOL에 없고 pools API 측정 POOL 아래에 보임 |
| terminal 사유와 status | `NodeServing`과 `NodeNotInMeasurementPool`이 3.3절 표의 판정 지점마다 terminal로 처리되고 ChillServer 삭제가 막히지 않음, `NodeServing` 거절 result를 쓴 PowerModeTransitionCost가 다시 실행되지 않음, BatchEnergyOptimization status 갱신 뒤에도 `MeasurementPool` 조건이 남음 |
| 전환 비용 runner | 점유 뒤 runner와 Service 생성, 끝난 runner Pod 교체가 Service annotation의 같은 profile을 쓰고 새 profile이 생겨도 바뀌지 않음, 그 profile 측정의 삭제 거절, UID label이 다른 같은 이름 객체를 건드리지 않음, terminal 뒤 Pod와 Service가 모두 없어진 다음에만 반환, preparation ConfigMap 생성 시각 기준 timeout이 Profiler 재시작 뒤에도 같음, Profiler와 observer Role의 serving namespace render(`make helm-template`) |
| Pod 기록 | `edge.dacs.io/workload-role` 유무로 분류, 표의 생성 경로마다 label, transition-cost와 operation 측정의 구조화한 `Pods` |
| NodeActuator | 외부 Pod가 있어도 Ready까지 진행 |
| 외부 Pod와 스케줄러 | 외부 GPU Pod나 예약을 넘는 외부 CPU 요청이 있는 노드가 Ready로 남아 `PlacementBudgetUnavailable` 사유와 함께 가장 낮은 모드로 내려가고 에너지에 들어감 |
| 설정 | `completionMarginMS`가 없는 설정과 settings PUT 거절, 0은 받음 |

### 9.2 실클러스터 E2E

[AGENTS.md](../../../AGENTS.md)에 따라 testbed 변경은 사용자 요청이 있을 때 7장의 merge 절차 뒤에 수행한다.
각 실행은 관측 window, 결정 기록, 측정 객체 상태를 보존한다. ChillServer 메모리와 관측 PVC 사용량을 함께 기록한다.

| # | 시나리오 | 확인할 결과 |
|---|---|---|
| E1 | mobilenet Workload를 서빙한 적 없는 가용 노드에만 측정 근거가 있는 상태에서 인스턴스 0으로 자동제어를 켬, 잠정 `completionMarginMS`. 부하 0에서 시작해 평가 부하를 보냄 | 그 노드에 인스턴스 하나를 만들고 나머지 가용 노드를 가장 낮은 모드로 둠. 부하에 따라 다시 판단(`SCH-AC-24`). `completionResidualNanos` 분포를 기록하고 그 값으로 `completionMarginMS`를 정해 설정과 sample, [설정과 관측 API](scheduler-api.md)에 반영 |
| E2 | 꺼진 Workload(수동 인스턴스), 근거 없는 켜진 Workload, 근거 있는 켜진 Workload 공존. 꺼진 Workload와 노드를 공유하는 전력 변경 유도 | 꺼진 Workload 인스턴스 유지와 SLO 판정 포함, `MeasurementEvidenceMissing` 표시, 나머지 판단 계속(`SCH-AC-25`) |
| E3 | 가용 노드, 서빙 중인 노드, 외부 Pod가 있는 노드에 BatchEnergyOptimization 요청. 진행 중 Job 삭제로 실패 유도. PowerModeTransitionCost 측정(재시작 전환 포함)과 `serving-operation` prepare와 remove 비용 측정 | 이동 후 측정과 반환, 서빙 노드 거절 사유, CHILL 배치 여부가 표시된 외부 Pod 목록, 실패 기록과 반환, 측정 중 스케줄러가 그 노드에 계획하지 않음, runner 자동 생성과 재시작 뒤 교체, 반환 전 runner와 Service 삭제, 측정 결과가 다음 refresh에서 비용 근거로 나타남(`SCH-AC-26`) |
| E4 | 활성 POOL 사이 직접 이동이 선택되는 부하 전환 | 한 전환의 DAG와 비용, 단계별 소속(`SCH-AC-22`) |
| E5 | mobilenet과 SmolVLM 혼합 부하, 두 Workload 자동제어 | Workload별 SLO 달성, 가용 노드를 포함한 전체 에너지, `completionResidualNanos`와 설정 여유 비교 |

### 9.3 검증 기록

구현 후 이 절에 실행 경계별 결과를 기록한다.

## 10. 갱신할 문서

구현 commit과 함께 다음 절을 이 설계에 맞춘다.

| 문서 | 대상 |
|---|---|
| [설정과 관측 API](scheduler-api.md) | 설정 표와 controls 설명(`:10-24`), `Completion calibration selector와 발행 객체`(`:35-70`), compact completion 참조(`:76-85`), completion overhead 조회(`:114-143`) |
| [스케줄러 구현 설계](scheduler-implementation.md) | 관측 overhead와 Pending(`:47-50`), membership 동작(`:29,200,284`), controls OFF의 staticRouting(`:223,274,305,316`), 요구사항 표(`:340`) |
| [실행기와 측정 계약](scheduler-runtime-measurement.md) | calibration, 일치, Pending, sender 경과 차감, slack과 lifetime 예약(`:42-79`), membership 측정(`:203`) |
| [인스턴스 전달과 수동 실행](scheduler-operations.md) | `measurement-*` membership(`:97-100`), 수동 실행의 preparations, calibration, controls OFF, membership(`:102-192`), 원자 동작 측정의 격리(`:196-`) |
| [워크로드 관리](workload-management.md) | POOL 결정(`:20`), POOL 운영 상태와 label(`:107-133`), pools API(`:189`), 채택과 profileCampaigns(`:564,601`), POOL 재구성(`:459-471`), `WM-25` 대응(`:685`) |
| [README](../../../README.md) | profile 채택 안내(`:184-197`) |
| [Profiling 실행 지원 확장](../profiling/plans/06-execution-support-expansion.md) | workload safety(`:199-215,279-285`) |
| ADR 0015, 0017, 0018 | 본문은 유지하고 ADR 0022가 해당 부분을 대체 |
