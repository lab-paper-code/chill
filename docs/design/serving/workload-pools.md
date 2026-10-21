# 워크로드별 POOL과 자동제어 구현 설계

## 1. 목적과 범위

[시스템 명세의 노드 POOL과 구성](../../spec/README.md#노드-pool과-구성),
[측정 POOL](../../spec/00_measurement.md#측정-pool),
[스케줄러 SRS](../../spec/scheduler/SRS.md)의 `SCH-02`, `SCH-08`, `SCH-09`, `SCH-13`, `SCH-15`, `SCH-21`,
`SCH-32`, `SCH-33`, `SCH-34`, [워크로드 SRS](../../spec/workloads/SRS.md)의 `WM-25`,
[Dashboard SRS](../../spec/dashboard/SRS.md)의 `DB-FR-10`, `DB-FR-33`, `DB-FR-34`에 대응하는 구현 계약이다.
경계 결정은 [ADR 0022](../../adr/0022-workload-pool-membership-boundary.md)에 기록한다. 측정 POOL의 진입과 반환(admission)은
채택한 [아키텍처 재검토](../architecture-rethink/README.md)의 이행 단계 M3([단일 writer 노드 상태](../architecture-rethink/03-b2-single-writer-node-state.md))가
정하며, 그 전까지는 지금의 측정 격리를 유지한다(3.3절).
기존 계약은 [워크로드 관리](workload-management.md), [스케줄러 구현 설계](scheduler-implementation.md),
[이기종 워크로드](heterogeneous-workloads.md)를 따르며 이 문서는 바뀌는 부분만 정한다.

### 핵심 결정

| 결정 | 이유 |
|---|---|
| 자동제어 스위치는 scheduler 설정 `settings.autonomousWorkloads`(Workload UID 목록) | 결정 기록마다 사용한 설정이 함께 저장되고 기존 설정 API를 그대로 쓴다 |
| 활성 POOL 소속은 저장하지 않고 CHILL serving 인스턴스에서 도출 | 여러 활성 POOL 소속을 별도 상태 없이 표현하고, 부분 완료도 실제 인스턴스로 드러난다 |
| M3 전까지 측정 POOL은 운영자가 붙이는 taint `chill.dacs.io/measurement:NoSchedule`이고, ChillServer는 이 노드에 배치, 내림, 전력 변경을 하지 않는다 | taint는 toleration이 없는 Pod를 기본으로 거부한다(cordon과 같은 방식). M3는 같은 taint의 추가와 제거를 자동화한다 |
| 전체 POOL은 control-plane이 아닌 모든 Kubernetes Node, node agent가 게시한 online CPU 목록은 배치 가능 조건 | 명세의 전체 워커노드 범위와 가용 노드 전력 집계를 그대로 따른다 |
| 가용 노드는 비용 비교 없이 늘 가장 낮은 전력모드로 내린다. 가장 낮은 모드는 unloaded baseline 중 전력이 가장 작은 모드 | 후보 선택이 아닌 고정 규칙. DeviceClass에는 모드 순서가 없고, 이 baseline은 에너지 계산에 이미 필요하다 |
| 후보는 상태가 바뀌는 노드의 차이로 비교하고, 전체 POOL 에너지는 실측으로 보고한다 | `SCH-16`의 공통 항 소거. 바뀌지 않는 노드의 baseline이 없어도 판단이 멈추지 않는다 |
| 후보와 비용 근거는 완료된 측정 전체에서 조건별 최신 성공으로 도출 | 운영자 채택과 `profileCampaigns`, `preparations`, `costReferences`, `powerCostSources` 선언을 없앤다 |
| 완료 여유는 `settings.completionMarginMS` 하나, firstfloor에는 요청마다 전달 | 설정 변경이 바로 모든 요청에 적용되고 인스턴스 policy를 다시 쓰지 않는다 |
| NodeActuator의 workload safety 검사 삭제, 측정 결과는 노드 Pod 목록과 CHILL 배치 여부를 기록 | CHILL은 자신이 배치한 파드만 관리한다 |
| 저장 계약은 이 branch에서 한 번씩 바꾸고, 같은 계약을 바꾸는 작업은 한 commit에 모은다 | [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md) |

## 2. 요구사항 대응

| 요구사항 | 설계 |
|---|---|
| 시스템 명세 노드 POOL과 구성(활성 POOL, 측정 POOL, 가용 노드, 자동제어) | 3.1, 3.2, 3.3, 3.4, 4 |
| 시스템 명세 외부 파드, 가용 노드 전력 | 3.4, 3.8 |
| 시스템 명세 흐름의 측정 POOL 이동과 반환, 00 측정 POOL | 3.3(지금의 격리 유지), M3(이동과 반환) |
| `SCH-02`, `SCH-AC-22`, `WM-25` | 3.2, 5 |
| `SCH-08` | 3.4, 3.5, 4.2 |
| `SCH-09` | 3.5 |
| `SCH-13`, `SCH-21`, 03 라우팅의 설정 여유, 04 요청 지연 계산 | 3.6 |
| `SCH-15`, `SCH-16` | 3.4, 4.3, 4.4 |
| `SCH-32` | 3.1, 3.6 |
| `SCH-33`, `SCH-AC-24`, `SCH-AC-25` | 3.1, 3.4, 3.7, 4 |
| `SCH-34`, `SCH-AC-26` | 3.8(외부 Pod 목록), M3(측정 POOL 이동, 반환, 서빙 노드 거절) |
| `DB-FR-10`, `DB-FR-33` | 5 |
| `DB-FR-34` | 3.8과 5(외부 Pod 목록), M3(소속 표시와 거절 사유, 이동과 반환 진행) |

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

`edge.dacs.io/serving-pool` label(`kube/observer.go:40`), 그 유일한 writer `MoveNodePool`(`observer.go:875-953`)과 membership
동작(스케줄러 `scheduler_replay.go:26,1054`, `scheduler_candidates.go:1048`, `kube/scheduler.go:1030-1033`, operation
`kube/operations.go:46,259-263`, `serving-operation` `cmd/serving-operation/main.go:554-558,733-746`), label 기반 POOL 산출
(`observer.go:180-193`)과 label을 요구하는 배치 검사(`observer.go:317-318,372-378`, `operations.go:151,369-370,997,1039`,
`kube/scheduler.go:533-536,734-737`)를 삭제한다. membership은 노드를 POOL label에 붙이고 떼는 동작이라 label이 없으면
할 일이 없다. prepare는 label 없이 노드를 고르고, `measurement-*` 이동이 맡던 측정 격리는 3.3절의 taint가 맡는다.
`Pool` 필드는 `SchedulerNode`(`scheduler.go:51`), `SchedulerInstance`(`:67`), `InstanceState`(`types.go:202`),
`Assignment`(`types.go:185`), `Operation`과 `OperationState`(`operations.go:71,81`), `firstfloor.Spec`(`serving/firstfloor/client.go:31`)에서
삭제한다. `Assignment.Pool`의 producer와 복원(`runtime.go:128,348`), 후보 비교(`runtime.go:714`), Dashboard 요청 관측 타입도 함께 바꾼다.

한 노드의 모든 CHILL 인스턴스는 같은 ModelSpec(이름과 UID)을 참조한다. 이 조건은 후보 생성에서 확인한다(4.3절).
owner와 binding이 모두 없는 인스턴스는 어느 활성 POOL에도 속하지 않는다. 측정 POOL 밖의 이런 인스턴스는 다음
판단의 후보가 remove로 정리한다.

기각: label 값을 POOL 목록으로 확장. 인스턴스와 따로 맞춰야 하는 저장 상태가 남는다.

### 3.3 측정 POOL

측정 POOL은 taint `chill.dacs.io/measurement:NoSchedule`이 있는 노드다. ChillServer가 측정 객체에 따라 이 taint를
붙이고 떼는 유일한 writer이고, taint 값은 노드를 받은 측정이다. 승인, 거절, 반환은
[측정 admission](measurement-admission.md)이 정한다.

- 측정 Pod는 이 key의 toleration(`operator: Exists`, `effect: NoSchedule`)을 갖는다: profiling Job(measurement-worker,
  `profiler/controller/execution_job_spec.go:110-136`, idle baseline도 같은 경로), transition attempt Job
  (`transitioncost/controller/attempt_job.go:123-136`), operation 측정 인스턴스, ChillServer가 승인 때 준비하는 전환 비용
  runner([측정 admission 2.4절](measurement-admission.md#24-전환-비용-runner)).
- CHILL serving Pod는 `spec.nodeName`으로 만들어 kube-scheduler의 NoSchedule 검사를 거치지 않는다(`kube/operations.go:403`).
  그래서 ChillServer가 직접 뺀다. 스케줄러 capture는 이 노드를 배치, 전력 변경, `Fixed` 내림, 제거 대상에 넣지 않고(3.4절),
  Workload 인스턴스의 prepare와 `ChangePower`(`kube/operations.go:740-769`)는 실행 직전에 Node를 읽어 taint가 있으면
  `NodeInMeasurement`로 실패한다. pools API는 이 노드를 측정 POOL로 보인다(5장).
- operation 측정은 ChillServer가 승인한 노드에서만 측정 인스턴스를 준비하고, 격리 witness는 측정 끝의 taint 유무
  (`Tainted`)를 기록한다([원자 동작 측정의 격리](scheduler-operations.md#원자-동작-측정의-격리)).

### 3.4 전체 POOL, 가용 노드와 전력모드

전체 POOL은 `node-role.kubernetes.io/control-plane` label이 없는 모든 Kubernetes Node다. capture
(`kube/scheduler.go:531-556`)는 label로 거르지 않고 이 Node 전체를 snapshot에 넣는다. 측정 POOL 노드는 배치,
전력 변경, 제거 대상이 아닌 고정 노드로 들어간다. 가용 노드는 측정 POOL 밖에서 Ready이고 CHILL serving 인스턴스가
없는 노드다. 설정이 부족한 노드는 빼지 않고 노드 사유로 남긴다.

| 사유 | 조건 | 판단 |
|---|---|---|
| `PlacementInventoryMissing` | online CPU 목록이 없거나 읽을 수 없음, 측정 GPU가 allocatable보다 많음, 예약이 inventory 밖 | 배치 option에서 빠짐. 전력과 에너지는 계속 다룸 |
| `PlacementBudgetUnavailable` | CHILL이 배치하지 않은 Pod의 GPU 소유를 확인할 수 없거나 그 CPU 요청이 예약을 넘음 | 같음 |
| `PowerModeUnavailable` | 현재 모드를 모르거나, 요청 가능 모드(DeviceClass의 비고정 모드 중 actuation policy가 있는 모드)가 없거나, 가장 낮은 모드가 요청 가능 모드가 아님 | 현재 모드 유지 |
| `InstanceUnavailable` | 노드의 CHILL serving 인스턴스의 native나 profile 상태를 읽지 못함 | 그 노드는 배치, 내림, 전력 변경에서 빠지고 다른 노드는 계속 계획한다 |

현재 CPU inventory 부족은 snapshot 사유가 되어 모든 후보를 평가 대기로 만들고(`kube/scheduler.go:543-546`),
`captureNodePods`는 CHILL이 배치하지 않은 Pod 때문에 `SchedulerNode.Ready=false`로 바꾼다(`kube/scheduler.go:699-722`).
둘 다 위 노드 사유로 바꾼다. 외부 Pod의 자원 요청은 기존처럼 `NonServing` 예산에 남고, 노드의 Ready는 Kubernetes
NodeReady만 따른다. 그래서 외부 Pod가 있는 노드도 가용 노드로 남아 전력모드를 내리고 에너지에 들어간다.

가장 낮은 전력모드는 그 노드의 unloaded baseline(State가 빈 `NodeBaseline`) 중 `Watts`가 가장 작은 모드이고,
같으면 이름순이다. DeviceClass의 `powerModes`는 순서와 전력 값을 갖지 않는다(`api/v1alpha1/deviceclass_types.go:8-42`,
`internal/deviceclass/powermode.go:11-42`).

가용 노드를 내리는 것은 고정 규칙이고 keep을 포함한 모든 계획의 DAG에 공통으로 들어간다. `Controls.Power`가 켜져 있으면
모든 노드 계획은 판단 시점의 모든 가용 노드를 시각 0부터 가장 낮은 모드에 둔 공통 상태에서 계산한다. 계속 비어 있는 노드의
에너지는 가장 낮은 모드의 unloaded baseline이다. 현재 모드가 다르면 DAG는 그 노드의 맨 앞에 power 동작을 두고
`Action.Fixed=true`로 표시한다. 이 동작은 모든 계획에 같으므로 그 시간과 에너지는 공통 항으로 비교에서 빠진다. 판단에
target이 없으면 controller는 keep의 `Fixed` 동작만 실행한다. 빈 노드 전환 비용 측정은 이 규칙에 필요하지 않다.

가용 노드를 활성 POOL로 다시 넣는 노드 계획은 그 전환 비용을 비교에 포함한다. 노드 단위 순서(`schedulerPlan`)에 따라
`Fixed` 내림 뒤에 power 동작(가장 낮은 모드에서 profile 모드로)과 prepare가 온다. 그 노드는 가장 낮은 모드에서
출발한 것으로 계산한다. 다시 넣는 비용은 빈 노드에서 측정한 전력모드 전환 비용과 profile 모드에서 측정한 prepare 비용의
합이다. `schedulerPlan`의 비용 선택은 power 동작에 그 시점의 적재 상태, 곧 빈 상태를 `BeforeState`로 가진 전환 비용(3.5절)을,
prepare에 노드 전력모드가 같은 비용을 고른다. 빈 노드 전환은
측정 POOL에서 `serving-operation`의 power 동작으로 측정하고([원자 동작 측정의 격리](scheduler-operations.md#원자-동작-측정의-격리)),
모드 변경이 노드를 재부팅하는 장비(jetsono)도 같다. 측정 구간은 NodeActuation의 적용과 확인에 settling
(`settlingDurationSeconds`, E1에서 60 s)을 더한 구간이다. 그래서 적용이 약 1.5 s인 jetsonx 전환도 63.5 s 구간으로
coverage 검사를 통과해 `Accepted`이고, 비용에는 목표 모드에서 보낸 settling 에너지가 들어간다. 그 비용이 없으면 다시
넣는 노드 계획은 `MissingOperationCost`로 비교에서 빠진다.

비교 기간은 판단마다 하나인 `T = max(comparisonPeriodNanos, 최장 route 전 사슬 + 최장 route 뒤 remove)`이고 keep과 모든 계획에
같다([용량표 계획기 4장](analytic-planner.md#4-노드-계획과-dp)). 사슬은 동작마다 측정 trial의 최대 시간으로 재므로 다시
넣는 계획의 power, prepare, route 사슬도 T 안에 끝나고, 모든 계획은 power와 prepare의 측정 에너지 전체와 다시 넣은 뒤의
적재 baseline을 같은 T에서 비교한다. E1에서 측정한 빈 노드 전환은 jetsonx 63.5 s, jetsono 134.5 s, orin-nano 197.1 s이고,
이런 사슬이 있는 판단의 T는 E1의 비교 기간 하한 60 s보다 길었다.

노드 계획은 차이로 비교한다(`SCH-16`). 비교 집합은 keep 밖의 적격 노드 계획이 있는 노드이고, keep과 target의
`ExpectedJoules`는 이 집합의 노드만 합한다. 집합 밖 노드(측정 POOL 노드와 계획이 keep뿐인 노드 포함)는 모든 계획에 같아
소거한다. 결정 기록은 `SchedulerSearch.ComparedNodes`에 이 집합을 남긴다. 노드 계획의 모드에 unloaded baseline이 없으면 그
계획은 `MissingUnloadedNodeBaseline:<node>:<mode>` 사유로 비교에서 빠진다. 집합 밖 노드의 baseline은 판단에 쓰지 않는다.
전체 POOL 에너지는 기존 운영 전력 집계(`ObservationLog.OperatingPower`, `cmd/chill-server/scheduler_service.go:399-409`)로
실측해 보고한다.

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
| operation 측정(`serving-operation`) | ChillServer store의 고정 prefix `serving/operation-measurements/<Node UID>/<operation ID>.json`. `serving-operation`이 이 규칙으로 key를 만들고 manifest는 key를 받지 않는다 | Verdict `Accepted`, 기존 검사(`kube/cost.go:27-98`) 통과, 기록의 meter(ID, Node, scope, source)가 현재 scheduler 설정 `meters`의 항목과 모두 같음 | 비용은 (Node UID, kind, from, to, 노드 전력모드)마다 `Result.EndedAt`이 가장 늦은 것. power 기록은 측정 전후 모드가 from과 to이고 (Node UID, from, to, 적재 상태)마다 고른다. idle 기록의 baseline은 (Node, 전력모드, State)마다 하나 |
| 전력모드 전환 비용 | profile namespace allowlist의 PowerModeTransitionCost 중 기존 `loadPowerCost` 조건(`kube/cost_power.go:75-130`)을 통과한 Complete. meter는 scheduler 설정 `meters`에서 같은 Node와 같은 source(`prepared.PowerSource`)를 가진 항목이다 | 같음 | (Node UID, from, to, 적재 상태)마다 받아들인 trial의 완료 시각 중 가장 늦은 값이 큰 측정, 같으면 이름순. 적재 상태는 runner의 profile과 할당을 담은 `BeforeState`(`cost_power.go:195-199`)이고 profile이 모델, 실행 경로, 장비를 정한다 |

두 경우 모두 그 시각을 새 필드 `OperationCost.MeasuredAt`에 담는다. 조건이 다른 비용은 서로 대신하지 않는다. planner의
비용 선택(`schedulerPlan`의 `selectCosts`)은 prepare와 remove에 노드 전력모드를, 전력모드 변경에 적재 상태(`BeforeState`)를
열쇠로 쓴다. 맞는 비용이 없으면 그 노드 계획은 `MissingOperationCost`로 비교에서 빠진다.

나열은 새 `evidencestore.Lister`(`List(ctx, prefix) ([]ObjectIdentity, error)`)로 한다. S3 구현은 `ListObjectsV2`를
page 끝까지 읽는다(`internal/evidencestore/s3/client.go:10-14`에 메서드 추가). store 자격 증명에 list 권한이
필요하다[INFERENCE: 현재 권한은 확인하지 않음]. 객체는 불변이므로 ChillServer는 identity별로 한 번만 읽어 결과를
보관하고, 더는 나열되지 않는 identity는 버린다. PowerModeTransitionCost는 UID와 result identity로 같은 보관을 한다.

한 기록이나 측정의 실패는 그 출처의 `CostIssue{Source, Code, Reason}`(`CostEvidenceInvalid`, `MeterUndeclared`)가 되어
scheduler 상태에 보이고 나머지는 계속 쓴다. Verdict가 Accepted가 아닌 기록(실패한 시도)과 Complete가 아닌 측정은 issue 없이 건너뛴다.
현재는 하나의 실패가 refresh 전체를 멈춘다(`scheduler_service.go:305-313`).
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
  Workload의 분배와 binding만 뺀다. 전체 controls OFF 조건(`cmd/serving-operation/main.go:450-462`)과 membership 동작은
  삭제한다(3.2절). 진행 중 action 조건(`:463-467`)과 staticRouting ACK 확인(`:468-483`)은 유지한다.

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
| serving 인스턴스(`kube/operations.go:403`) | `serving-instance` |
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

snapshot의 노드는 3.4절의 전체 POOL이고 측정 POOL 노드(3.3절)는 고정 노드다. option은 3.5절의 최신 profile과 그 profile에
맞는 켜진 Workload의 조합이다. 인스턴스는 꺼진 Workload의 것을 포함해 모두 넣는다.

### 4.2 Workload 판단 상태

판단마다 Workload별 상태를 `SchedulerDecision.Workloads []SchedulerWorkloadState{UID, State, Reason}`로 기록한다.

| State | 조건 | 판단 |
|---|---|---|
| `Disabled` | 자동제어 꺼짐 | 마지막 구성 유지, 인스턴스가 있는 노드는 keep뿐(4.4절) |
| `MeasurementEvidenceMissing` | 켜졌고 option 없음 | 계획에서 빠지고 다른 Workload의 판단은 계속 |
| `Controlled` | 켜졌고 option 있음 | 노드 계획과 판정 대상 |

### 4.3 노드 계획 규칙

노드 계획과 판정은 [용량표 계획기 4장](analytic-planner.md#4-노드-계획과-dp)과 [14장](analytic-planner.md#14-구현-규칙)이
원본이다. Workload별 규칙은 다음과 같다.

- 계획은 `Controlled` Workload의 인스턴스와 분배만 바꾼다. 꺼진 Workload의 분배는 현재 값을 그대로 둔다.
- 꺼진 Workload가 owner이거나 꺼진 Workload를 맡은 인스턴스, strict fixed-size baseline 인스턴스가 있는 노드는
  keep뿐이다(`FixedWorkload`).
- 한 노드의 인스턴스는 모두 같은 ModelSpec을 참조한다.
- `Controls.Membership`이 꺼져 있으면 `Controlled` Workload의 option은 그 Workload의 인스턴스가 있는 노드에 한정한다.
- `Controlled` Workload는 부하가 0이어도 인스턴스가 하나 이상 있어야 최종 충족이다.
- 3.2절의 연결 없는 인스턴스 정리를 포함한다. 가용 노드를 내리는 동작은 모든 계획의 DAG에 공통 `Fixed` 동작으로
  들어간다(3.4절).
- 활성 POOL 사이의 직접 이동은 같은 노드의 remove와 prepare를 한 DAG로 만든다. 노드 단위 순서(`schedulerPlan`)는
  membership 동작 없이 유지하고, 비용은 각 동작 비용의 합이다.
- 첫 구성은 현재 인스턴스가 0인 `Controlled` Workload에 대해 option의 배치를 그대로 만든다. 설정 gate에 따라
  placement, resources, batching, membership control이 켜져 있어야 한다.

### 4.4 SLO 판정 대상과 에너지

판정 대상은 `Controlled` Workload 전체다. `Disabled` Workload의 인스턴스가 있는 노드는 keep뿐이므로 계획 동작은 그 노드의
전력모드와 인스턴스를 바꾸지 않고, 꺼진 Workload의 SLO는 계획의 영향을 받지 않는다. 에너지는 3.4절의 비교 집합 노드를 합한다.

### 4.5 적용과 기록

`Target`이 있으면 적용한다(`kube/scheduler.go:284`의 전체 활성 조건 삭제). 결정 기록(`cmd/chill-server/scheduler_service.go:411-416`)은
keep, target, `decision.capacity`와 snapshot을 담고 option은 완료된 측정 수만큼 늘어난다. 저장 기록의 `SchedulerInstance`는 profile 본문 대신
`profileID`만 담고, profile 본문은 catalog(`GET /api/v1/scheduler/profiles`)에서 조회한다. HTTP 조회의 기존 압축
(`internal/serving/httpapi/scheduler_presentation.go:17-73`)은 조회 경계에 그대로 둔다. 이 변경은 결정 기록 축소 M0
([05](../architecture-rethink/05-recommendation-migration.md))의 일부이며, M0의 나머지(snapshot 축소)는
[판단 기록 다이어트](decision-record-diet.md)가 정한다.

## 5. API와 Dashboard

| 표면 | 계약 | 요구사항 |
|---|---|---|
| `GET /api/v1/pools`(`handler.go:95`) | `workloads[]{workload, nodes, instances}`, `measurement[]{node, instances}`, `available[]{node, ready, powerMode, reasons}`, 전체 POOL의 나머지 노드와 사유. 활성 POOL은 runtime 인스턴스, 측정 POOL은 `chill.dacs.io/measurement` taint, 가용 노드는 최신 snapshot에서 만든다. `GET /api/v1/pools/{id}`(`:96`)와 `Runtime.Pools`(`runtime.go:859-919`), `PoolState`(`types.go:217-223`)는 이 형태로 바꾼다 | `DB-FR-10`, `WM-25` |
| `GET /api/v1/scheduler/state` | `decision.workloads`, 노드 사유, `CostIssue`, 비교 집합 | `DB-FR-33`, `SCH-08` |
| `PUT /api/v1/scheduler/settings` | `autonomousWorkloads`, `completionMarginMS` | `DB-FR-33`, `SCH-32` |
| `GET /api/v1/scheduler/profiles` | 도출된 profile과 `CatalogIssue` 조회 | `SCH-08`, `SCH-09` |

Dashboard:

- `chill-dashboard/features/resources.tsx:54-79`, `service-hub.tsx:34`: Workload별 활성 POOL, 측정 POOL, 가용 노드와
  여러 활성 POOL이 함께 쓰는 노드, 노드 사유.
- `features/management.tsx:440` Workload 상세: 자동제어 스위치, 판단 상태와 사유, 꺼진 Workload의 마지막 구성 유지와
  수동 변경 결과, 완료 여유 검증 분포.
- `features/scheduler-settings.tsx:38,181-187`: `enabled` 항목 삭제, `completionMarginMS` 편집.
- `features/measurement-plans.tsx:69-76,122-125`: CHILL이 배치하지 않은 파드 목록, 채택 동작 삭제. 측정 요청의 노드 소속 표시와 거절 사유는 M3.
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
| serving-pool label, `MoveNodePool`, membership 동작, label 기반 POOL 산출, `Pool` 필드(`Assignment.Pool` 포함), `measurement-*` 격리 조건 | 3.2절과 3.3절 목록 |
| 채택, 저장 preparation, 비용 선언(`costReferences`, `powerCostSources`, `PowerCostSource`) | 3.5절 목록 |
| `Controls.Enabled` | 3.1절 목록 |
| NodeActuator workload safety | 3.8절 목록 |

## 7. 저장 계약

| 계약 | 현재 | 이 branch의 값 | 바꾸는 commit | 정리 대상 |
|---|---|---|---|---|
| scheduler ConfigMap JSON(`scheduler_service.go:38-50`, version 없음, strict decode) | `profileCampaigns`, `preparations`, `costReferences`, `powerCostSources`, `controls.enabled`, 전체 `staticRouting` | 다섯 필드 삭제, `settings.autonomousWorkloads`, 필수 `settings.completionMarginMS`, 부분 `staticRouting` | 2, 3, 4 | 새 sample로 ConfigMap 재작성 |
| Node label `edge.dacs.io/serving-pool` | 노드 소속 | 삭제 | 3 | 모든 노드에서 label 제거 |
| Node taint `chill.dacs.io/measurement:NoSchedule` | - | 측정 POOL 표시(운영자가 붙이고 뗌, M3에서 자동) | 3 | - |
| `observationSchema`(`observation_log.go:21`) | `serving-observations.v1alpha3` | `v1alpha4`(`Pool`과 `Assignment.Pool`, completion 필드 삭제, `decision.workloads`, 노드 사유, 비교 집합 `ComparedNodes`, 저장 기록의 `profileID`, `completionResidualNanos`, `OperationCost.MeasuredAt`, `Action.Fixed`) | 2(version), 3, 4 | PVC와 evidence store의 serving 관측 window |
| `OperationMeasurementSchema`(`kube/measurement.go:20`) | `serving-operation-measurement.v1alpha1`, manifest가 정한 key | `v1alpha2`(포함한 `Operation`, `OperationState`, `PrepareSpec`, `SchedulerInstance`의 completion과 `Pool` 필드 삭제, `MeasurementIsolation.Pool`을 `Tainted`로 교체, 구조화한 `Pods`), key는 3.5절의 고정 prefix. strict decode(`cost.go:77-85`, `cost_power.go:50-61`) | 2(version), 3, 4, 5 | 모든 operation 측정 기록(prepare, remove 비용과 unloaded baseline) |
| firstfloor policy와 요청(Go wire, version 없음) | `completionOverheadMS`, `envelopeSource` | `RequestContext.completionMarginMS` | 2 | 모든 serving 인스턴스 Pod와 instance ConfigMap |
| `RawRecordSchemaVersion`(`transitioncost/raw_record.go:11`) | `v1alpha5` | `v1alpha6`(native state의 firstfloor policy 형태, 구조화한 `Pods`) | 2(version), 5 | 모든 PowerModeTransitionCost와 그 evidence |
| `FinalizedRecordSchemaVersion`(`raw_record.go:12`) | `v1alpha3` | `v1alpha4`(`BeforeNative`, `AfterNative`, `finalized.go:32-33`) | 2(version), 5 | 위와 같음 |
| NodeActuation CRD | `CheckingSafety`, `Safety`, `status.safety` | 삭제 | 1 | 모든 NodeActuation 객체 |
| node-actuator 설정 | 허용 namespace | 삭제 | 1 | Helm으로 다시 render |
| `serving-operation` manifest(전달용) | `isolatedPool`, `expectedPool`, membership | 두 필드와 membership 삭제, `workloadUID` 추가 | 3 | - |

외곽 형태가 그대로인 transition-cost `MeasurementResultSchemaVersion` `v1alpha3`, `PreparationRecordSchemaVersion` `v1alpha4`와
attempt 요청 `v1alpha4`는 유지하고, 위 정리 대상과 함께 삭제한다. PowerModeTransitionCost CRD는 바뀌지 않는다.
BatchEnergyOptimization과 ProfilingRun, profiling evidence는 바뀌지 않으므로 기존 측정이 바로 후보가 된다.
CHILL 배치 판정은 이미 보존된 label을 읽는 규칙이라 profiling evidence에 새 version이 필요 없다. transition-cost
raw 기록은 구조화한 `Pods`를 계속 담는다(`SCH-34`).

merge 절차: 진행 중 campaign이 끝난 뒤 merge한다. serving 인스턴스 Pod와 instance ConfigMap, NodeActuation 객체,
PowerModeTransitionCost 객체, serving 관측 window, operation 측정 기록과 transition-cost evidence를 지우고 모든 노드의
serving-pool label을 제거한다. Helm upgrade 뒤 새 sample로 scheduler ConfigMap을 쓴다.
측정 POOL에서 전체 POOL의 노드마다 모드별 unloaded baseline과 prepare, remove 비용, 전력모드 전환 비용을
다시 측정한다. 결과는 3.5절의 나열로 바로 비용 근거가 된다. 후보가 상태를 바꾸는 노드에 baseline이 없으면 그 후보는
평가할 수 없으므로(3.4절), E2E에 쓰는 노드의 이 측정이 선행 조건이다.

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
| 3 | `feat(serving): derive active pools and control autonomy per workload` | 3.1, 3.2(label, `MoveNodePool`, membership 삭제), 3.3(taint 제외, prepare와 `ChangePower`의 taint 확인, profiling과 transition attempt Job의 toleration, `serving-operation` 전제와 witness), 3.4(`captureNodePods`, 비교 집합 포함), 3.7절, 4장(4.5의 profile ID 제외), pools API와 Dashboard 자원, Workload 상세, 설정 화면, `serving-operation`의 `workloadUID` | 2 | `SCH-02`, `SCH-15`, `SCH-16`, `SCH-32`, `SCH-33`, `SCH-AC-22`, `SCH-AC-24`, `SCH-AC-25`, `WM-25`, `DB-FR-10`, `DB-FR-33` | `go test ./internal/serving/... ./internal/profiling/profiler/... ./internal/profiling/transitioncost/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make dashboard-check`, `make gopls-check` |
| 4 | `feat(serving): derive candidates and costs from completed measurements` | 3.5절(`evidencestore.Lister`, S3 `ListObjectsV2`, operation 측정의 고정 key 포함), 4.5절의 저장 profile ID, 측정 삭제 보호, Dashboard profile 화면과 채택 동작 삭제, `README.md:184-197` | 3 | `SCH-08`, `SCH-09`, `SCH-16` | `go test ./internal/serving/... ./internal/evidencestore/... ./cmd/chill-server/... ./cmd/serving-operation/...`, `make dashboard-check` |
| 5 | `feat(measurement): record CHILL-placed pods in measurement results` | 3.8절의 역할 label 전체, transition-cost와 operation 측정의 구조화한 `Pods`(`projectPod` export), console `chillPlaced`, Dashboard 측정 화면의 외부 Pod 목록, `.go-arch-lint.yml`의 workload 의존 | 2 | `SCH-34`, `SCH-AC-26`과 `DB-FR-34`의 외부 Pod 목록 | `make arch-check`, `go test ./internal/workloadobserver/... ./internal/profiling/... ./internal/serving/... ./internal/operator/... ./internal/operatorconsole/... ./cmd/serving-operation/...`, `make helm-lint helm-template`(default와 Profiler), `make dashboard-check` |
| 6 | `docs(design): record workload pool verification` | 9.3절 기록, 10장의 문서, `docs/design/serving/README.md` | 4, 5 | 전체 | 문서 링크 확인 |

1과 2는 동시에 진행한다. 3과 4는 순서대로 진행하고, 5는 2 뒤에 3, 4와 동시에 진행한다. taint의 자동 추가와 제거, 서빙 노드 거절,
전환 비용 runner 자동 생성은 이 branch 뒤의 M3([05](../architecture-rethink/05-recommendation-migration.md))가 맡는다. 생성 파일을 바꾸는
commit은 각 merge 뒤 `make manifests generate`를 다시 실행한다.

## 9. 검증 계획

### 9.1 단위와 계약 검사

| 대상 | 검사 |
|---|---|
| firstfloor | slack과 lifetime이 `completionMarginMS`를 sender 경과와 무관하게 한 번 뺀다 |
| replay | 완료 시각이 native 완료 + 여유, 관측 여부와 무관하게 Pending 사유 없음 |
| 소속 도출 | owner, binding, 둘 다 없는 인스턴스의 노드 분류, 같은 노드의 두 Workload 활성 POOL |
| 전체 POOL | inventory 없는 노드와 전력모드 수단 없는 노드가 사유와 함께 남음, 비교 집합이 상태가 바뀌는 노드만 담고 모든 후보에 같음, 비교 집합 밖 노드의 baseline이 없어도 판단이 진행됨, 비교 집합 노드의 baseline이 없으면 그 후보만 평가 불가 |
| 후보 | 꺼진 Workload 인스턴스 고정, 최소 인스턴스, 같은 모델 조건, 직접 이동 DAG, 모든 후보가 가용 노드를 가장 낮은 모드에서 출발한 공통 상태로 계산하고 `Fixed` 내림이 그 노드의 다른 동작보다 앞서며 공통 항으로 비교에서 빠짐, target이 없을 때 유지 후보의 `Fixed` 동작만 실행, 다시 넣는 후보의 power 비용이 빈 노드 전환 비용으로 계산되고 그 비용이 없으면 `MissingOperationCost`, 판정 대상 집합 |
| 도출 profile과 비용 | 같은 조건의 최신 성공 선택, 한 profile이나 비용 기록의 실패가 나머지를 막지 않음, 고정 prefix 나열과 identity별 한 번 읽기, `Accepted`만 받음, 현재 `meters`와 다른 meter의 operation 기록과 meter 없는 전환 비용의 `MeterUndeclared`, operation 비용은 노드 전력모드까지 같은 조건에서 최신, 전환 비용은 적재 상태가 다른 측정을 섞지 않고 trial 완료 시각으로 최신, planner가 같은 열쇠로 고름, 나열 실패 때 이전 구성 유지 |
| 수동 변경 | 부분 `staticRouting`, 켜진 Workload 항목 거절, 공유 인스턴스 remove의 `InstanceShared` |
| 측정 POOL | taint 노드가 배치, 전력 변경, `Fixed` 내림, 제거 대상에서 빠지고 pools API에 측정 POOL로 보임, 판단 뒤 taint가 붙은 노드에서 Workload prepare와 `ChangePower`가 `NodeInMeasurement`로 실패, 그 노드의 측정 인스턴스가 정리되지 않음, profiling과 transition attempt Job에 toleration, `serving-operation` 측정이 taint 없는 노드를 거절 |
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
| E2 | 꺼진 Workload(수동 인스턴스), 근거 없는 켜진 Workload, 근거 있는 켜진 Workload 공존. 켜진 Workload의 부하를 바꿔 꺼진 Workload 인스턴스가 있는 노드의 전력 변경이 유리한 상황을 만듦 | 꺼진 Workload 인스턴스가 있는 노드는 전력모드와 인스턴스가 그대로이고 결정 기록에 `FixedWorkload`로 남음, `MeasurementEvidenceMissing` 표시, 나머지 판단 계속(`SCH-AC-25`) |
| E3 | taint를 붙인 노드(외부 Pod 있음)에서 BatchEnergyOptimization 측정과 진행 중 Job 삭제로 실패 유도, 운영자 runner를 둔 노드의 PowerModeTransitionCost 측정, `serving-operation` prepare와 remove 비용 측정 | 측정 중 스케줄러가 그 노드를 쓰지 않음, CHILL 배치 여부가 표시된 외부 Pod 목록, 측정 결과가 다음 refresh에서 후보와 비용 근거로 나타남(`SCH-AC-26`의 외부 Pod 부분). taint 자동화와 서빙 노드 거절은 M3 뒤 M3의 검증([07](../architecture-rethink/07-validation.md))에서 확인한다 |
| E4 | 활성 POOL 사이 직접 이동이 선택되는 부하 전환 | 한 전환의 DAG와 비용, 단계별 소속(`SCH-AC-22`) |
| E5 | mobilenet tight, PLC, SmolVLM 혼합 부하, 세 Workload 자동제어, Jetson 포함(아래 단계) | Workload별 SLO 달성, 비교 집합 에너지와 실측 전체 POOL 에너지, `completionResidualNanos`와 설정 여유 비교 |

**E5 혼합 부하.** 측정 캠페인이 끝나고 모든 Jetson이 가장 낮은 모드로 돌아온 뒤 세 Workload를 함께 자동제어한다. 사람은 부하만 바꾸고 구성은 판단이 정한다.

- 노드: `excludedNodes`는 빈 목록이다(orin-nano DNS가 고장이면 orin-nano만 둔다). Jetson을 포함한 가용 노드 전체와 측정한 전력모드가 후보다.
- Workload: `image-classification-tight-slo`(p95 1 s), `plc-anomaly`(`plc-extruder-01-windows`), `smolvlm2-vqa`(VQAv2 `vqav2-val-subset`). 측정 전용 Workload는 지운 뒤이고, 세 Workload 모두 인스턴스 0에서 `autonomousWorkloads`에 들어간다. `completionMarginMS`는 그때 설정값이고 실행 기록에 남는다.
- PLC와 SmolVLM SLO: 마지막 `/api/v1/scheduler` dump에서 `hack/kappa -mode slo`가 제안하고 사용자가 승인한 값이다. 노드마다 가장 빠른 option의 b1 지연(실행시간 p99 + d + m)을 구하고, κ > 0인 노드가 절반 이상이며 가장 느린 두 노드는 κ 0인 목표 L을 가장 넓은 지연 간격에서 고른다. 그 L에서 p95와 p99 κ 표를 계산해 규칙이 κ로도 성립하는지와 두 백분위의 κ와 계산 시간을 비교한다.
- 기준 요청률 `R_w`: Workload SLO 행에서 노드별 최대 κ의 합이다(`hack/kappa -mode slo`의 κ 합). GPU 하나는 인스턴스 하나가 쓰므로 세 Workload가 함께 쓰는 용량은 세 `R_w`의 합보다 작다. 단계 요청률은 Workload마다 `R_w`의 비율이고, 같은 비율에서 판단이 `Overload`인지가 결과에 들어간다.
- 부하: `chill_loadgen replay --arrivals poisson`을 Workload마다 다른 seed로 동시에 보낸다. 세 요청률의 합은 캠페인 기록에서 ChillServer가 CPU limit 아래로 처리한 가장 큰 총 요청률 G 이하다. PLC 몫이 이 상한을 넘으면 G에서 다른 두 몫을 뺀 값으로 두고 그 값을 기록한다. 그래서 한계 단계의 초과는 POOL 용량에서 생긴다.

| 단계 | 시간 | 요청률 (mobilenet, PLC, SmolVLM) | 재는 것 | 채우는 칸 |
|---|---|---|---|---|
| E5-0 시작 | 10분 | 모두 0 | Workload마다 측정 근거가 있는 노드의 첫 인스턴스, 나머지 가용 노드의 가장 낮은 모드, 무부하 전체 POOL 전력 | `SCH-AC-24`(세 Workload) |
| E5-1 일정 | 30분 | 모두 0.3 `R_w` | Workload별 지연 분포와 처리 결과 수(완료, 실패, 거절, 취소, 미완료), 전체 POOL과 활성 POOL 에너지, 창별 예측 백분위와 관측 백분위 | [명세 평가 표](../../spec/README.md#실클러스터-검증과-평가) 일정 부하와 이기종 워크로드 동시 혼합 부하 행, `SCH-AC-23`, [07 검증](../architecture-rethink/07-validation.md) 2번 |
| E5-2 버스트 | 15분 | SmolVLM만 0.3에서 0.9 `R_w`, 나머지 0.3 `R_w` | 대응 시작까지 시간, GPU 노드(genesis2, genesis3, jetsono)에서 SmolVLM CUDA와 다른 Workload 인스턴스의 교체, Jetson 전력모드 변경이 같은 노드의 다른 Workload에 준 전환 중 지연 | 평가 표 갑작스러운 버스트 행, `SCH-AC-21` |
| E5-3 증가 | 30분 | 모두 0.5, 0.7, 0.9 `R_w` 각 10분 | 확장 순서, 전환 구간 SLO, 판단의 `κ_new`, `B`, `W`와 관측 큐 | 평가 표 점진적인 증가와 감소 행(증가), `SCH-AC-15` |
| E5-4 한계 | 15분 | 모두 1.2 `R_w` | `Overload` 판단과 최대 초과량 순서, Workload별 실제 지연과 처리 결과, 노드별 인스턴스와 전력모드 | 평가 표 전체 POOL의 한계를 넘는 부하 행, `SCH-AC-16` |
| E5-5 급감과 재급증 | 20분 | 모두 0.3 `R_w` 10분, 0.9 `R_w` 10분 | 축소와 유휴 제거, 다시 확장, 반복 전환의 J와 시간 | 평가 표 전환 직후 급감과 재급증 행, 점진적인 증가와 감소 행(감소), `SCH-AC-15` |
| E5-6 soak | 60분 | 모두 0.3 `R_w` | 1시간 예측 에너지 합과 비교 집합 실측, 전체 POOL 실측, `completionResidualNanos` 분포와 설정 여유 | 위 표 E5 행, [용량표 계획기](analytic-planner.md#11-확인) 에너지(실측) |

- 모든 단계에서 Tick p50과 p99, ChillServer CPU와 RSS, `decision.capacity.cells`, 관측 PVC 증가를 기록한다. 세 Workload의 칸 크기 재검토(용량표 계획기 13장)가 이 값을 쓴다.
- 평가 표의 분석 값은 Workload별 지연 분포와 처리 결과, 전환 구간을 포함한 전체 POOL 에너지, 활성 POOL 에너지다. 단계 경계와 seed, 단계별 `R_w`와 G, 사용한 profile과 설정이 실행 기록에 남는다.
- 전체 약 3시간이다. 캠페인 중 비Jetson 노드에서 mobilenet과 PLC로 E5-3, E5-4, soak를 미리 도는 다중 Workload 시험은 E5 준비 근거이고 표 값에는 넣지 않는다.

### 9.3 검증 기록

E1은 2026-10-19 UTC에 수행했다. 첫 실행과 rev 35 재실행 결과를 기록한다.

| 실행 | 배포와 이미지 | 설정 | 첫 구성 |
|---|---|---|---|
| 첫 실행 (E1 #1) | 2026-10-19 UTC | — | 30 s timeout으로 Tick 133회 실패, 첫 구성 없음, 1,660건 중 성공 0건, p95 30.08 s |
| 재실행 | 2026-10-19 UTC / rev 35; `chill-serving-entrypoint:pools-e669615`, `chill-profiler:pools-e669615` | `maxCandidates=32`, `replays=10`, `completionMarginMS=139` (초기 50) | 활성화 6 s 뒤 rasp5 `mnv2-rasp5-cpu-q1-ed628c8` 한 인스턴스(CPU [3], Fixed); 24 s 뒤 Ready |

재실행의 단계별 Tick과 SLO 결과다.

| 부하 | Tick p50 / p99 / max (ms) | 요청 p95 (ms) | 1 s 이내 완료 | SLO |
|---|---:|---:|---:|---|
| 0.633 req/s | 913 / 1,749 / 1,749 | 77.5 | 62 / 62 | 충족 |
| 2 req/s | 3,475 / 6,166 / 6,166 | 140.1 | 300 / 300 | 충족 |
| 5 req/s | 7,495 / 22,575 / 22,575 | 200.2 | 750 / 750 | 충족 |
| soak 2 req/s | 4,769 / 6,601 / 6,948 | 161.4 | 7,139 / 7,199 (99.17%) | 충족 |
| E1 전체 | 4,574 / 12,655 / 22,575 | 164.5 | 8,252 / 8,312 (99.28%) | 충족 |

| 측정 | 결과 |
|---|---|
| `completionResidualNanos` 전체 E1 | p50 3.29 ms / p99 138.23 ms / max 405.73 ms (n=8,312); `completionMarginMS=139` |
| 잔여 지연 산출 | D-11 쿼리 실패로 PVC 사본에서 계산; API 결과와 20:10:20–20:35 구간(n=1,070) 및 0.633 req/s(n=62)에서 일치 |
| 비교 집합 에너지 예측 | 499,333 J / 138.7 W (1 h) |
| 비교 집합 에너지 실측 | 495,741 J / 137.7 W (1 h) |
| 전체 POOL 에너지 실측 | 521,589 J / 144.9 W (1 h, control-plane genesis1 제외) |

확인한 결함과 환경 제한이다.

| ID | 확인 내용 |
|---|---|
| D-1 | 첫 실행의 Tick이 30 s deadline으로 133회 실패해 첫 구성이 없었고 요청 1,660건 중 성공이 없었다. |
| D-2 | 60 s 이상 settling이 필요한 MLIB 실행에서 runner의 60 s `IdleTimeout`이 연결을 닫았다. |
| D-3 | PMTC observer의 runner PodIP 연결이 edge tunnel에서 실패했다; Service 경유 관측은 재실행에서 성공했다. |
| D-4 | PMTC는 `modes[0]` 상태에서 시작해야 하며, 첫 시도는 `SourceStateMismatch`로 실패했다. |
| D-5 환경 | orin-nano Pod DNS의 `127.0.0.53` 질의가 거부되어 해당 노드의 serving prepare를 진행하지 못했다. |
| D-6 환경 | jetson-nano MAXN→5W NodeActuation이 `Applying`에 머물렀고 SSH 연결이 시간 초과됐다. |
| D-7 | 빈 노드의 `Fixed` 전력 내림에 power cost가 요구됐으며, rev 35 재실행에서는 측정 비용 없이 낮은 모드로 전환했다. |
| D-8 | `powerRequests`가 고정 이름 NodeActuation을 재사용해 반복 전환에 stale Ready 상태가 남을 수 있어 `e1r-*` 이름을 사용했다. |
| D-9 | measurement toleration이 없는 ChillServer Pod가 genesis1 taint 상태에서 Pending이었다. |
| D-10 | 실패한 measurement prepare가 finalizer가 남은 Pod와 ConfigMap을 남겨 수동 정리가 필요했다. |
| D-11 | workload observation query가 전체 요청 stream을 읽고 범위 필터를 적용해 ChillServer를 2회 OOM-kill했다. |
| D-12 | jetsono의 MAXN↔MODE_15W 전환이 노드를 재부팅해 적재된 runner가 종료되고 재진입 전력 비용을 얻지 못했다. |
| D-13 | jetsonx 전환은 약 1.5 s로 meter 약 1 s 간격보다 짧아 PMTC 15회 모두 `PowerWindowInsufficient`였다. |
| D-14 | serving-operation이 상태 변화가 증명되지 않은 remove를 Accepted로 저장해 CostIssue가 남았다. |
| D-15 | decision record 중앙값은 185,359 B로 ≤100 KB 목표를 넘었다; candidates 96,890 B, snapshot 70,394 B였다. |
| D-16 | CPU 500 m 제한에서 Tick p50은 2 req/s 3,475 ms, 5 req/s 7,495 ms였고, 5 req/s 최대는 22,575 ms였다. |
| D-17 | 요청 하나가 `assigned` 단계일 때 전체 Tick이 Pending이 됐다; soak 37/360회, 5 req/s 3/17회였다. |

## 10. 갱신할 문서

구현 commit과 함께 다음 절을 이 설계에 맞춘다.

| 문서 | 대상 |
|---|---|
| [설정과 관측 API](scheduler-api.md) | 설정 표와 controls 설명(`:10-24`), `Completion calibration selector와 발행 객체`(`:35-70`), compact completion 참조(`:76-85`), completion overhead 조회(`:114-143`) |
| [스케줄러 구현 설계](scheduler-implementation.md) | 관측 overhead와 Pending(`:47-50`), membership 동작(`:29,200,284`), controls OFF의 staticRouting(`:223,274,305,316`), 요구사항 표(`:340`) |
| [실행기와 측정 계약](scheduler-runtime-measurement.md) | calibration, 일치, Pending, sender 경과 차감, slack과 lifetime 예약(`:42-79`), membership 측정(`:203`) |
| [인스턴스 전달과 수동 실행](scheduler-operations.md) | `measurement-*` membership(`:97-100`), 수동 실행의 preparations, calibration, controls OFF, membership(`:102-192`), 원자 동작 측정의 격리(`:196-`)를 taint로 |
| [워크로드 관리](workload-management.md) | POOL 결정(`:20`), POOL 운영 상태와 label(`:107-133`), pools API(`:189`), 채택과 profileCampaigns(`:564,601`), POOL 재구성(`:459-471`), `WM-25` 대응(`:685`) |
| [README](../../../README.md) | profile 채택 안내(`:184-197`) |
| [Profiling 실행 지원 확장](../profiling/plans/06-execution-support-expansion.md) | workload safety(`:199-215,279-285`) |
| ADR 0015, 0017 | 본문은 유지하고 ADR 0022가 해당 부분을 대체 |
