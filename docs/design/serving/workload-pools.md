# 워크로드별 POOL과 자동제어 구현 설계

## 1. 목적과 범위

[시스템 명세의 노드 POOL과 구성](../../spec/README.md#노드-pool과-구성),
[측정 POOL](../../spec/00_measurement.md#측정-pool),
[스케줄러 SRS](../../spec/scheduler/SRS.md)의 `SCH-02`, `SCH-08`, `SCH-09`, `SCH-13`, `SCH-15`, `SCH-21`,
`SCH-32`, `SCH-33`, `SCH-34`, [워크로드 SRS](../../spec/workloads/SRS.md)의 `WM-25`,
[Dashboard SRS](../../spec/dashboard/SRS.md)의 `DB-FR-10`, `DB-FR-33`, `DB-FR-34`에 대응하는 구현 계약이다.
경계 결정은 [ADR 0022](../../adr/0022-workload-pool-membership-boundary.md)에 기록한다.
측정 POOL의 진입과 반환은 [측정 admission](measurement-admission.md)이 정한다.
[워크로드 관리](workload-management.md), [스케줄러 구현 설계](scheduler-implementation.md)와
[이기종 워크로드](heterogeneous-workloads.md)의 공통 계약을 따르고, 본문은 POOL과 자동제어의 계약을 정한다.

### 핵심 결정

| 결정 | 이유 |
|---|---|
| 자동제어 스위치는 scheduler 설정 `settings.autonomousWorkloads`(Workload UID 목록) | 결정 기록마다 사용한 설정이 함께 저장되고 기존 설정 API를 그대로 쓴다 |
| 활성 POOL 소속은 저장하지 않고 CHILL serving 인스턴스에서 도출 | 여러 활성 POOL 소속을 별도 상태 없이 표현하고, 부분 완료도 실제 인스턴스로 드러난다 |
| 측정 POOL은 taint `chill.dacs.io/measurement:NoSchedule`으로 표시하고 ChillServer가 측정 객체에 따라 관리 | 서빙 계획은 이 노드의 배치, 내림, 제거와 전력 변경을 격리한다 |
| 전체 POOL은 control-plane이 아닌 모든 Kubernetes Node, node agent가 게시한 online CPU 목록은 배치 가능 조건 | 명세의 전체 워커노드 범위와 가용 노드 전력 집계를 그대로 따른다 |
| 가용 노드는 비용 비교 없이 늘 가장 낮은 전력모드로 내린다. 가장 낮은 모드는 unloaded baseline 중 전력이 가장 작은 모드 | 후보 선택이 아닌 고정 규칙. DeviceClass에는 모드 순서가 없고, 이 baseline은 에너지 계산에 이미 필요하다 |
| 후보는 상태가 바뀌는 노드의 차이로 비교하고, 전체 POOL 에너지는 실측으로 보고한다 | `SCH-16`의 공통 항 소거. 바뀌지 않는 노드의 baseline이 없어도 판단이 멈추지 않는다 |
| 후보와 비용 근거는 완료된 측정 전체에서 조건별 최신 성공으로 도출 | 운영자 채택과 `profileCampaigns`, `preparations`, `costReferences`, `powerCostSources` 선언을 없앤다 |
| 완료 여유는 `settings.completionMarginMS` 하나, firstfloor에는 요청마다 전달 | 설정 변경이 바로 모든 요청에 적용되고 인스턴스 policy를 다시 쓰지 않는다 |
| 측정 결과는 노드 Pod 목록과 CHILL 배치 여부를 기록 | 외부 파드의 운영 책임과 CHILL의 자원 예산을 구분한다 |
| 저장 계약은 작업 단위마다 한 형태를 사용하고 producer와 consumer를 함께 맞춘다 | [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 reader 원칙 |

## 2. 요구사항 대응

| 요구사항 | 설계 |
|---|---|
| 시스템 명세 노드 POOL과 구성(활성 POOL, 측정 POOL, 가용 노드, 자동제어) | 3.1, 3.2, 3.3, 3.4, 4 |
| 시스템 명세 외부 파드, 가용 노드 전력 | 3.4, 3.8 |
| 시스템 명세 흐름의 측정 POOL 이동과 반환, 00 측정 POOL | 3.3과 측정 admission |
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

자동제어 스위치는 scheduler의 `settings.autonomousWorkloads`에 담긴 Workload UID 목록이다.
제어요소별 control 설정은 시스템 전체의 활성 설정으로 유지한다.
판단 기록은 그 시점의 목록을 보존하고 변경은 기존 `PUT /api/v1/scheduler/settings`를 사용한다.

같은 이름으로 다시 등록한 Workload는 새 UID이므로 꺼진 상태로 시작한다.
판단은 현재 Workload에 없는 UID를 무시하고 설정 저장은 그런 UID를 제거한다.
Workload 정의는 모델 참조와 SLO를 담고 자동제어 상태는 설정에서 관리한다.

### 3.2 활성 POOL 소속

Workload W의 활성 POOL은 W가 owner(`InstanceState.OwnerWorkloadUID`)이거나 binding으로 연결된 CHILL serving
인스턴스가 있는 노드 집합이다. 노드의 소속은 그 노드의 인스턴스에서 계산하며 저장하지 않는다.
전력 집계는 이미 인스턴스로 활성 노드를 정한다(`internal/serving/observation_log_power.go:429-437`).
[전환 명세](../../spec/05_transition_control.md)의 동작 목록에도 소속 변경 동작은 없다. 노드의 편입과 반환은
prepare와 remove로 이루어지고, 일부만 완료한 전환은 실제 인스턴스 상태로 바로 드러난다(`SCH-AC-22`).

prepare와 remove가 실제 인스턴스를 바꾸면 활성 POOL 소속도 함께 바뀐다.
별도의 POOL label이나 membership 동작은 사용하지 않는다.
측정 격리는 3.3절의 taint와 admission이 맡는다.

노드마다 서빙 인스턴스는 하나다. 이 조건은 후보 생성에서 확인한다(4.3절).
owner와 binding이 모두 없는 인스턴스는 어느 활성 POOL에도 속하지 않는다. 측정 POOL 밖의 이런 인스턴스는 다음
판단의 후보가 remove로 정리한다.

같은 모델의 Workload도 각자 인스턴스를 두고 측정 profile은 실행 조건이 같을 때 재사용한다.

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

외부 Pod의 자원 요청은 `NonServing` 예산에 남고 Node의 Ready는 Kubernetes NodeReady를 따른다.
외부 Pod가 있는 노드도 가용 노드의 전력 집계에 포함하고, 자원 예산 문제는 노드 사유로 표시한다.

가장 낮은 전력모드는 그 노드의 unloaded baseline(State가 빈 `NodeBaseline`) 중 `Watts`가 가장 작은 모드이고,
같으면 이름순이다. DeviceClass의 `powerModes`는 순서와 전력 값을 갖지 않는다(`api/v1alpha1/deviceclass_types.go:8-42`,
`internal/deviceclass/powermode.go:11-42`).

가용 노드를 내리는 것은 고정 규칙이고 keep을 포함한 모든 계획의 DAG에 공통으로 들어간다. `Controls.Power`가 켜져 있으면
모든 노드 계획은 판단 시점의 모든 가용 노드를 시각 0부터 가장 낮은 모드에 둔 공통 상태에서 계산한다. 계속 비어 있는 노드의
에너지는 가장 낮은 모드의 unloaded baseline이다. 현재 모드가 다르면 DAG는 그 노드의 맨 앞에 power 동작을 두고
`Action.Fixed=true`로 표시한다. 이 동작은 모든 계획에 같으므로 그 시간과 에너지는 공통 항으로 비교에서 빠진다. 판단에
target이 없으면 controller는 keep의 `Fixed` 동작만 실행한다. 빈 노드 전환 비용 측정은 이 규칙에 필요하지 않다.
target이 사용하지 않고 실제 인스턴스도 없는 빈 노드의 공통 절전은 다른 노드의 route와 독립적으로 실행한다.
실제 사용할 노드에서는 `Fixed` 내림부터 전력 변경과 prepare까지 같은 노드의 순서를 지키고,
실제 Ready, 소유권, policy ACK와 기존 요청의 인계를 확인한 뒤 분배한다.
공통 절전의 소비와 전환 중 결과는 전체 POOL 측정에 포함한다.

가용 노드를 활성 POOL로 다시 넣는 노드 계획은 그 전환 비용을 비교에 포함한다. 노드 단위 순서(`schedulerPlan`)에 따라
`Fixed` 내림 뒤에 power 동작(가장 낮은 모드에서 profile 모드로)과 prepare가 온다. 그 노드는 가장 낮은 모드에서
출발한 것으로 계산한다. 다시 넣는 비용은 빈 노드에서 측정한 전력모드 전환 비용과 profile 모드에서 측정한 prepare 비용의
합이다. `schedulerPlan`의 비용 선택은 power 동작에 그 시점의 적재 상태, 곧 빈 상태를 `BeforeState`로 가진 전환 비용(3.5절)을,
prepare에 노드 전력모드가 같은 비용을 고른다. 빈 노드 전환은
측정 POOL에서 `serving-operation`의 power 동작으로 측정하고([원자 동작 측정의 격리](scheduler-operations.md#원자-동작-측정의-격리)),
모드 변경이 노드를 재부팅하는 장비(jetsono)도 같다. 측정 구간은 Node annotation의 적용과 독립 read-back 확인에 settling
(`settlingDurationSeconds`, E1에서 60 s)을 더한 구간이다. 그래서 적용이 약 1.5 s인 jetsonx 전환도 63.5 s 구간으로
coverage 검사를 통과해 `Accepted`이고, 비용에는 목표 모드에서 보낸 settling 에너지가 들어간다. 그 비용이 없으면 다시
넣는 노드 계획은 `MissingOperationCost`로 비교에서 빠진다.

비교 기간은 판단마다 하나인 `T = max(comparisonPeriodNanos, 최장 route 전 사슬 + 최장 route 뒤 remove)`이고 keep과 모든 계획에
같다([용량표 계획기 4장](analytic-planner.md#4-노드-계획과-탐색)). 사슬은 동작마다 측정 trial의 최대 시간으로 재므로 다시
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

`PrepareSpec`은 profile의 측정 inventory와 preparation에서 도출한다.
bundle 발행과 binding도 profile마다 수행한다.
한 profile의 실패는 `CatalogIssue`의 `PreparationUnavailable`로 표시하고 다른 profile의 refresh는 계속한다.
profile ID 집합이 바뀌면 다음 refresh에 적용한다.
현재 인스턴스가 사용하는 profile의 측정은 삭제 보호 대상이다.

**비용 근거.** prepare/remove 비용, unloaded baseline과 전력모드 전환 비용은 완료된 측정에서 도출한다.
동일 조건의 최신 성공을 선택하고, 정확 profile/runtime/NodeUID/mode와 적재 상태를 맞춘다.
profile을 바꾸는 변경에는 영향받는 d, 비용과 baseline의 목록을 함께 남긴다.

| 근거 | 위치와 나열 | 받아들이는 기록 | 조건별 최신 |
|---|---|---|---|
| operation 측정(`serving-operation`) | ChillServer store의 고정 prefix `serving/operation-measurements/<Node UID>/<operation ID>.json`. `serving-operation`이 이 규칙으로 key를 만들고 manifest는 key를 받지 않는다 | Verdict `Accepted`, 기존 검사(`kube/cost.go:27-98`) 통과, 기록의 meter(ID, Node, scope, source)가 현재 scheduler 설정 `meters`의 항목과 모두 같음 | 비용은 (Node UID, kind, from, to, 노드 전력모드)마다 `Result.EndedAt`이 가장 늦은 것. power 기록은 측정 전후 모드가 from과 to이고 (Node UID, from, to, 적재 상태)마다 고른다. idle 기록의 baseline은 (Node, 전력모드, State)마다 하나 |
| 전력모드 전환 비용 | profile namespace allowlist의 PowerModeTransitionCost 중 기존 `loadPowerCost` 조건(`kube/cost_power.go:75-130`)을 통과한 Complete. meter는 scheduler 설정 `meters`에서 같은 Node와 같은 source(`prepared.PowerSource`)를 가진 항목이다 | 같음 | (Node UID, from, to, 적재 상태)마다 받아들인 trial의 완료 시각 중 가장 늦은 값이 큰 측정, 같으면 이름순. 적재 상태는 runner의 profile과 할당을 담은 `BeforeState`(`cost_power.go:195-199`)이고 profile이 모델, 실행 경로, 장비를 정한다 |

두 경우 모두 그 시각을 새 필드 `OperationCost.MeasuredAt`에 담는다. 조건이 다른 비용은 서로 대신하지 않는다. planner의
비용 선택(`schedulerPlan`의 `selectCosts`)은 prepare와 remove에 노드 전력모드를, 전력모드 변경에 적재 상태(`BeforeState`)를
열쇠로 쓴다. 맞는 비용이 없으면 그 노드 계획은 `MissingOperationCost`로 비교에서 빠진다.

객체 나열은 `evidencestore.Lister`의 고정 prefix 목록과 S3 `ListObjectsV2` 페이지를 사용한다.
객체는 불변이므로 identity별로 한 번 읽어 보관하고, 목록에서 사라진 identity는 보관 목록에서 뺀다.
PowerModeTransitionCost는 UID와 result identity로 같은 규칙을 사용한다.

한 기록이나 측정의 실패는 그 출처의 `CostIssue{Source, Code, Reason}`(`CostEvidenceInvalid`, `MeterUndeclared`)가 되어
scheduler 상태에 보이고 나머지는 계속 쓴다. Verdict가 Accepted가 아닌 기록(실패한 시도)과 Complete가 아닌 측정은 issue 없이 건너뛴다.
나열 자체가 실패하면 이전 구성을 유지한다. refresh의 재사용 판단은 비용 identity 집합도 비교한다.

### 3.6 완료 여유

`SchedulerSettings.CompletionMarginMS float64`(`json:"completionMarginMS"`)는 유한한 0 이상의 필수 값이며
`SchedulerSettings.Validate`(`scheduler.go:249-260`)가 확인한다. 생략과 0을 구분하도록 설정 decode 경계
(`cmd/chill-server/scheduler_service.go:110-138`, settings PUT `httpapi/scheduler.go:64-143`)는 이 필드를 `*float64`로
받아 없으면 `completionMarginMS required`로 거절하고, 확인한 값만 `SchedulerSettings`에 넣는다. 요청의 예상 완료는
firstfloor 처리 완료에 이 값을 더한 시각이다.

- replay: 반환 완료와 만료 계산(`scheduler_replay.go:566-575,829-832,988-1005,1107-1111`)이 instance의
  `CompletionOverhead` 대신 설정 값을 쓴다.
- firstfloor는 요청별 `completionMarginMS`를 사용한다.
  `requestSlack`은 `RemainingSLOMS−completionMarginMS−serviceCost`이며 lifetime 수락도 같은 여유를 한 번 뺀다.
  senderElapsed는 원래 transport lifetime에서 별도로 차감한다.
- 완료 여유는 설정 입력이며 관측 overhead나 calibration의 Pending 상태를 요구하지 않는다.

검증은 Serving 관측으로 한다. `ObservationCohort`(`internal/serving/observation_log.go:517-526`)에
`completionResidualNanos`를 더한다. 완료 요청마다 종단 경과(`RequestEvent.ElapsedNanos`)에서 전달 시점 경과와
firstfloor 경과(`NativeProgress.NativeElapsedMS`)를 뺀 값이며(`types.go:244-248`), 지연 표본과 같은 자리
(`observation_log.go:697-699`)에서 모은다. Dashboard Workload 관측은 이 분포와 설정 여유를 함께 보인다.

MNV의 29 ms는 p95 관측에 근거한 값이다.
D2 뒤 Workload별 선택 SLO 백분위의 완료 잔여를 재고, 단일 m은 그 추정값 중 최댓값으로 정한다.
PLC/Smol의 최종 SLO와 함께 실제 입력을 고정하며 측정용 relaxed SLO를 자동 채택하지 않는다.

### 3.7 수동 변경 범위

수동 변경 하나는 꺼진 Workload 하나에 적용한다.

- `staticRouting`은 꺼진 Workload UID만 담는 부분 map이다. 생략한 Workload는 현재 분배를 유지한다.
  켜진 Workload의 항목은 거절하고, 한 Workload의 분배에서 인스턴스를 빼면 그 binding만 해제한다.
- `serving-operation --execute-config`의 manifest는 꺼진 Workload 하나의 `workloadUID`를 받는다. prepare는 준비한
  인스턴스의 owner로 그 Workload를 기록한다. remove는 그 Workload가 인스턴스의 유일한 owner이자 binding일 때만
  허용하고 다른 Workload가 사용하는 인스턴스는 거절한다.
  그 경우 `staticRouting`으로 해당 Workload의 분배와 binding만 해제한다.
  진행 중 action 조건과 staticRouting ACK를 확인한다.

### 3.8 외부 파드

NodeActuator는 외부 Pod의 운영을 맡지 않는다.
스케줄러는 외부 자원 사용을 예산과 사유로 기록하고 측정은 실제 노드 전체의 전력을 기록한다.

CHILL이 배치한 Pod는 label `edge.dacs.io/workload-role`로 구분한다.
profiling workload observer도 같은 key를 보존한다.

| 생성 경로 | 값 |
|---|---|
| serving 인스턴스(`kube/operations.go:403`) | `serving-instance` |
| transition attempt Job(`transitioncost/controller/attempt_job.go:116-119`) | `transition-observer` |
| operator 생성 Pod(`internal/operator/{evidencestore/deployment.go,evidencestore/bootstrap_job.go,nodeactuator/daemonset.go,profiler/deployment.go,nodediscovery/daemonset.go}`) | `system` |
| chart Pod template(`charts/chill/templates/{operator,serving_entrypoint,console,dashboard,predelete_cleanup}.yaml`, selector는 그대로) | `system` |
| `hack/runtime-declarations.py:142-144`의 probe | `system` |

측정 결과는 Node의 Pod 목록과 구조화한 `workloadobserver.Pod` 정보를 보존한다.
Dashboard는 역할 label이 없는 Pod를 CHILL이 배치하지 않은 파드로 표시한다.
profiling, 전력모드 전환과 operation 측정은 같은 Pod 분류를 사용한다.
에너지는 노드 전체 meter 값을 사용한다.

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

노드 계획과 판정은 [용량표 계획기 4장](analytic-planner.md#4-노드-계획과-탐색)과 [14장](analytic-planner.md#14-구현-규칙)이
원본이다. Workload별 규칙은 다음과 같다.

- 계획은 `Controlled` Workload의 인스턴스와 분배만 바꾼다. 꺼진 Workload의 분배는 현재 값을 그대로 둔다.
- 꺼진 Workload가 owner이거나 꺼진 Workload를 맡은 인스턴스, strict fixed-size baseline 인스턴스가 있는 노드는
  keep뿐이다(`FixedWorkload`).
- 노드마다 서빙 인스턴스는 하나다. 같은 ModelSpec을 참조하는 Workload들에도 같다.
- `Controls.Membership`이 꺼져 있으면 `Controlled` Workload의 option은 그 Workload의 인스턴스가 있는 노드에 한정한다.
- `Controlled` Workload는 부하가 0이어도 인스턴스가 하나 이상 있어야 최종 충족이다.
- 3.2절의 연결 없는 인스턴스 정리를 포함한다. 가용 노드를 내리는 동작은 모든 계획의 DAG에 공통 `Fixed` 동작으로
  들어간다(3.4절).
- 활성 POOL 사이의 직접 이동은 같은 노드의 remove와 prepare를 한 DAG로 만든다. 노드 단위 순서(`schedulerPlan`)는
  membership 동작 없이 유지하고, 비용은 각 동작 비용의 합이다.
- 첫 구성은 현재 인스턴스가 0인 `Controlled` Workload에 대해 option의 배치를 그대로 만든다. 설정 gate에 따라
  placement, resources, batching, membership control이 켜져 있어야 한다.
- 수요 λ는 기존 장기 관측과 최근 한 제어 주기의 계산 도착률 중 큰 값에 기존 미완료 기여를 한 번 더한다.
  두 구간은 실제 관측 범위로 한정하고 용량, 지연, 분배와 요청 에너지가 같은 λ를 쓴다.
  최근 관측률이 더 큰 Workload는 이미 서빙하는 인스턴스의 제거, 교체와 분배 0을 보호한다.
  해당 Workload의 확장과 다른 Workload의 변경은 계속하며 진행 중 목표의 유지 규칙을 따른다.
  두 구간과 관측값, 최종 λ는 [용량표 계획기의 판단 기록](analytic-planner.md#4-노드-계획과-탐색)에서 확인한다.

### 4.4 SLO 판정 대상과 에너지

판정 대상은 `Controlled` Workload 전체다. `Disabled` Workload의 인스턴스가 있는 노드는 keep뿐이므로 계획 동작은 그 노드의
전력모드와 인스턴스를 바꾸지 않고, 꺼진 Workload의 SLO는 계획의 영향을 받지 않는다. 에너지는 3.4절의 비교 집합 노드를 합한다.

### 4.5 적용과 기록

`Target`이 있으면 적용한다. 결정 기록은
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
- 설정 화면은 `autonomousWorkloads`와 `completionMarginMS`, 제어요소 설정을 편집한다.
- 측정 화면은 CHILL이 배치하지 않은 Pod 목록, 측정 POOL 소속과 거절 사유를 표시한다.
- profile 화면은 도출된 측정 근거와 문제 사유를 조회한다.

## 6. 측정과 증거 책임

대량 측정은 대표 한 건에서 조건 생성, 실행, raw, projection, 계획기 선택과 실물 반환을 확인한 뒤 진행한다.
profile, runtime 또는 측정 계약을 바꾸는 변경에는 영향받는 d, 비용, loaded/unloaded baseline과
전력 전환 비용 목록을 함께 작성한다. 운영 묶음 완료와 E5 입력 충족은 각 분모로 보고한다.

조건 생성은 실측 profile/SLO와 승인 계수에서 출발한다.
N/K/L과 준비/본창/drain/수집 예산, 사용한 계수와 exact profile ID를 Run에 동결한다.
generator가 계산을 맡고 M5에서 측정 controller로 옮긴다.
물리 d, signed raw와 max0 정책, 비용 `E±B`와 `B=S+G`, 보고용 10%는
[측정 API](measurement-api.md)의 계약을 따른다.
minimumGain은 실제 선택 근거의 B만 signed 상쇄해 전파하며 profile/BEO idle의 통계 CI는 포함하지 않는다.

증거는 조건 생성기, 승인 원문, manifest, profile/runtime/NodeUID/source SHA와 원시 위치를 선별한다.
`.token`, Secret 스냅샷과 전체 캐시 백업은 저장소와 증거 인덱스에서 제외한다.
재현 링크는 [handoff](../../handoff.md)의 선별 근거 표와 연결한다.

## 7. 저장 계약

저장 reader는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)의 단일 계약을 사용한다.
설정, 관측, operation과 전환 비용의 producer/consumer를 같은 작업 단위에서 맞춘다.
미기록 B는 unknown으로 표시한다. Timing이 없는 기록에 B=0 근사를 부여하지 않는다.
원 raw와 값, 판정은 외부 과학 증거에 보존한다.

G2의 [실제 소비자 목록](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/g2-current-consumer-inventory.json)은
catalog legacy 49개(비용 35, baseline 14)와 E5 노드의 ranking baseline 9개를 확인했다.
동결된 E5 후보 에너지 입력은 legacy 0개이며 전체 저장 prefix 정리는 별도다.
ranking9의 불확실성 unknown과 fresh10 입구 조건은 유지한다.
G2의 ranking9 재측정은 2 roots, 18 steps로 승인됐으며 실제 최저선정의 조건부 직접 전환을 포함한 상한은 4 roots, 22 steps다.
각 root의 raw, 계획기 소비와 정상 반환을 확인한 뒤 다음 root로 진행한다.
9개 대체 확인과 검증된 외부 백업의 실제 절대경로 및 개수 대조 보고 뒤 raw 80개 키와 terminal OCM 6개를 정리한다.
old ranking9는 80개에 포함되며 같은 승인 조건의 대체와 옛 기록 정리는 연속 실행한다.
strict reader의 로컬 소스 cutover는 수집과 병렬로 완결한다. 새 소스의 검증과 적용은 대체 확인, 검증 백업과 정리 및 Timing 없는 소비 기록 0건 뒤에 진행하고 첫 BEFORE 전에 공통 입력을 동결한다.
삭제와 reader 제거 완료, 원 raw/status 보존은 [측정 API 저장 계약](measurement-api.md#7-저장-계약-adr-0019)의 실제 근거로 기록한다.

## 8. 최소 D2 범위

| 항목 | 계약과 경계 |
|---|---|
| gateway tail | 관측된 offending holder/work 경계를 보수하며 durability, physical ownership과 HTTP 성공 경계를 보존한다 |
| G/CPU/κ/Tick | 통합의 cache/transport/payload/search 경로를 실제 병목 증거에 필요한 범위에서 보수하고 BEFORE/AFTER를 보존한다 |
| lifetime/socket | E5 caller가 SLO, 실제 gateway cap, L, delivery와 socket을 명시한다. L≥SLO, cap≥L, socket>L+delivery와 native 남은 수명을 일치시킨다 |
| 조건 설정 | warmup 60 s, 본창 360 s, cap 3600 s의 승인값을 한 설정 원천에서 읽고 Run에 동결한다. 실측 preparation 예산을 별도로 두고 generator/API/admission/controller가 같은 입력을 소비한다 |
| minimumGain | pre-D2 B-only를 적용하며 nominal 평균, 선택 근거 상쇄, 사슬 합성과 keep infeasible 회복을 유지한다 |
| admission | 요청 수명은 실제 `HandlerConfig.RequestTimeout` 상한과 대조해 초과 CR을 시작 전에 거절한다 |
| source 검증 | original architecture 경계를 보수하고 production mapping과 negative fixture를 보존한다. repo-local kind의 SCH-AC24/25/26은 조율된 검증 범위다 |
| 저장 비용 | G2 실제 소비 legacy 목록과 단일 계약 정리는 7절을 따른다 |

Runtime은 물리 인스턴스의 capacity를 예약한 뒤 소유권 잠금 밖에서 assignment를 저장하고 native 실행을 시작한다.
final row도 같은 호출자의 잠금 밖에서 저장하며 shutdown은 진행 중인 final 저장의 종료를 기다린다.
요청 행의 메모리 fold는 디스크 writer 잠금과 분리한다. 완전한 행의 rollback과 저장 실패의 fail-closed 처리를 유지한다.
동일한 불변 profile pointer의 시간별 catalog 직렬화 생략을 재사용한다.

runtime 7종, 기존 자원과 evidence store를 유지한다.
알려진 원인의 수리를 완료한 뒤 fresh D2 실행 승인을 한 번 받고 최종 D2 rollout을 최대 1회 수행한다.
같은 부하의 AFTER tail/G가 PLC SLO를 충족하면 진단은 0회다.
잔여 tail이 PLC SLO를 막을 때만 준비된 유한 진단 제어 rollout을 최대 1회 사용한다.
진단은 같은 PRIMARY native8과 실제 조건의 420 s 창, 유한 ring/trace 예산과 drop 수를 보존하며 근거 부족은 inconclusive로 남긴다.

진단은 제어 프로세스의 `--diagnostic-directory`에 새 절대 경로를 명시해 한 번 활성화한다.
Helm은 `servingEntrypoint.diagnostic.directory/startDelay/traceOffset`을 같은 세 flag에 연결한다.
기존 `/data` writable mount를 재사용하며 기본 directory는 빈 값이다.
빈 값은 비활성 경로이며 요청별 clock, ID 변환과 버퍼 할당을 수행하지 않는다.
`--diagnostic-start-delay`는 listener bind부터 420 s numeric 창까지의 지연이고,
`--diagnostic-trace-offset`은 그 창 안의 Go trace 시작 위치(0부터390 s)다.
실행 manifest는 실제 mounted private 디렉터리, listener/readiness와 부하 경계, 두 offset을 연결한다.
numeric ring은16 MiB/64-byte 고정 이벤트, Go trace 저장 버퍼는32 MiB/30 s다.
native 반환, mutex의 attempt/acquire/release와 owner/request, reply/Wait/Write/Flush/Response 및
관측 marshal/write/Sync/query/archive 경계를 실제 잠금 범위에서 기록한다.
두 캡처가 끝난 뒤 잠금 밖에서0700 디렉터리의0600 `events.bin`, `runtime.trace`, `metadata.json`을 저장하고 버퍼를 해제한다.
metadata는 overwrite/drop, 원래 offset 범위, trace cap/중단/절단과 실제 파일 오류를 보존한다.
창 경계와 유실로 연결되지 않은 edge는 inconclusive이며 trace 관측 오버헤드를 원인이나 수정 효과로 단정하지 않는다.
Go runtime 자체의 trace 버퍼와 캡처 오버헤드는 위 저장 버퍼 예산과 별도로 보고한다.
source 변경의 검증, kind, build와 offline κ는 측정/load와 겹치지 않게 조율한다.
통합 source, 검증된 source와 live 배포는 각 실제 SHA와 결과로 구분한다.

CRD 통합, NodeActuation 정리, 일반 이미지 분리, 추가 planner/판정 변경,
정합 통계 CI와 known bias/d 추정정책 대안, 지속 tracing과 관련 없는 cleanup은 M5 범위다.
승인된 단계 사이에는 재승인 대기를 두지 않으며 실제 새 조건, 판정과 범위 변경만 승인받는다.


## 9. 검증 계획

### 9.1 단위와 계약 검사

| 대상 | 검사 |
|---|---|
| firstfloor | 원래 lifetime에서 senderElapsed를 차감하고 slack과 native remaining에서 완료 여유 m을 한 번 차감한다 |
| replay | 완료 시각이 native 완료 + 여유, 관측 여부와 무관하게 Pending 사유 없음 |
| 소속 도출 | owner, binding, 둘 다 없는 인스턴스의 노드 분류, 같은 노드의 두 Workload 활성 POOL |
| 전체 POOL | inventory 없는 노드와 전력모드 수단 없는 노드가 사유와 함께 남음, 비교 집합이 상태가 바뀌는 노드만 담고 모든 후보에 같음, 비교 집합 밖 노드의 baseline이 없어도 판단이 진행됨, 비교 집합 노드의 baseline이 없으면 그 후보만 평가 불가 |
| 후보 | 꺼진 Workload 인스턴스 고정, 최소 인스턴스, 노드마다 서빙 인스턴스 하나, 직접 이동 DAG, 모든 후보가 가용 노드를 가장 낮은 모드에서 출발한 공통 상태로 계산하고 `Fixed` 내림이 그 노드의 다른 동작보다 앞서며 공통 항으로 비교에서 빠짐, target이 없을 때 유지 후보의 `Fixed` 동작만 실행, 다시 넣는 후보의 power 비용이 빈 노드 전환 비용으로 계산되고 그 비용이 없으면 `MissingOperationCost`, 판정 대상 집합 |
| 도출 profile과 비용 | 같은 조건의 최신 성공 선택, 한 profile이나 비용 기록의 실패가 나머지를 막지 않음, 고정 prefix 나열과 identity별 한 번 읽기, `Accepted`만 받음, 현재 `meters`와 다른 meter의 operation 기록과 meter 없는 전환 비용의 `MeterUndeclared`, operation 비용은 노드 전력모드까지 같은 조건에서 최신, 전환 비용은 적재 상태가 다른 측정을 섞지 않고 trial 완료 시각으로 최신, planner가 같은 열쇠로 고름, 나열 실패 때 이전 구성 유지 |
| 수동 변경 | 부분 `staticRouting`, 켜진 Workload 항목 거절, 다른 Workload 인스턴스의 remove 거절 |
| 측정 POOL | taint 노드가 배치, 전력 변경, `Fixed` 내림, 제거 대상에서 빠지고 pools API에 측정 POOL로 보임, 판단 뒤 taint가 붙은 노드에서 Workload prepare와 `ChangePower`가 `NodeInMeasurement`로 실패, 그 노드의 측정 인스턴스가 정리되지 않음, profiling과 transition attempt Job에 toleration, `serving-operation` 측정이 taint 없는 노드를 거절 |
| Pod 기록 | `edge.dacs.io/workload-role` 유무로 분류, 표의 생성 경로마다 label, transition-cost와 operation 측정의 구조화한 `Pods` |
| NodeActuator | 외부 Pod가 있어도 Ready까지 진행 |
| 외부 Pod와 스케줄러 | 외부 GPU Pod나 예약을 넘는 외부 CPU 요청이 있는 노드가 Ready로 남아 `PlacementBudgetUnavailable` 사유와 함께 가장 낮은 모드로 내려가고 에너지에 들어감 |
| 설정 | `completionMarginMS`가 없는 설정과 settings PUT 거절, 0은 받음 |

### 9.2 실클러스터 E2E

[AGENTS.md](../../../AGENTS.md)에 따라 live 변경은 승인 범위와 실제 실행 조건에 맞춰 수행한다.
각 실행은 관측 window, 결정 기록과 측정 객체 상태, ChillServer 메모리와 관측 PVC 사용량을 보존한다.

| # | 시나리오 | 확인할 결과 |
|---|---|---|
| E1 | mobilenet Workload를 서빙한 적 없는 가용 노드에만 측정 근거가 있는 상태에서 인스턴스 0으로 자동제어를 켬, 잠정 `completionMarginMS`. 부하 0에서 시작해 평가 부하를 보냄 | 그 노드에 인스턴스 하나를 만들고 나머지 가용 노드를 가장 낮은 모드로 둠. 부하에 따라 다시 판단(`SCH-AC-24`). `completionResidualNanos` 분포를 기록하고 그 값으로 `completionMarginMS`를 정해 설정과 sample, [설정과 관측 API](scheduler-api.md)에 반영 |
| E2 | 꺼진 Workload(수동 인스턴스), 근거 없는 켜진 Workload, 근거 있는 켜진 Workload 공존. 켜진 Workload의 부하를 바꿔 꺼진 Workload 인스턴스가 있는 노드의 전력 변경이 유리한 상황을 만듦 | 꺼진 Workload 인스턴스가 있는 노드는 전력모드와 인스턴스가 그대로이고 결정 기록에 `FixedWorkload`로 남음, `MeasurementEvidenceMissing` 표시, 나머지 판단 계속(`SCH-AC-25`) |
| E3 | 측정 taint 노드의 BatchEnergyOptimization, 전환 비용과 operation 측정 | 서빙 격리, CHILL 배치 여부를 포함한 Pod 목록, 측정 결과의 후보/비용 연결과 자연 반환을 확인한다(`SCH-AC-26`) |
| E4 | 활성 POOL 사이 직접 이동이 선택되는 부하 전환 | 한 전환의 DAG와 비용, 단계별 소속(`SCH-AC-22`) |
| E5 | mobilenet tight, PLC, SmolVLM 혼합 부하, 세 Workload 자동제어, Jetson 포함(아래 단계) | Workload별 SLO 달성, 비교 집합 에너지와 실측 전체 POOL 에너지, `completionResidualNanos`와 설정 여유 비교 |

빠른 수요와 빈 노드 공통 절전의 영향 측정은 E1의 첫 유입, E4의 이동,
E5-0부터 E5-7 및 E5-S의 구간별 두 관측률, 최종 λ, 보호 용량, 실제 분배 시각과 전력 사슬을 연결한다.
모든 유효 offered 결과와 전환 포함 전체 POOL 에너지를 유지한다.
runtime 7종 digest, profile, 측정 입력과 판정 기준은 고정한다.

**E5 혼합 부하.** 측정 캠페인이 끝나고 모든 Jetson이 가장 낮은 모드로 돌아온 뒤 세 Workload를 함께 자동제어한다. 사람은 부하만 바꾸고 구성은 판단이 정한다.

- 노드: E5 노드 여덟(genesis2, genesis3, lattepanda, rasp5, orangepi, jetsono, jetsonx, jetson-nano)과 측정한 전력모드가 후보다. `excludedNodes`는 `genesis1`(ChillServer와 게이트웨이의 Node)과 `orin-nano`(DNS를 고칠 때까지)다. 노드마다 서빙 인스턴스는 하나다(4.3절).
- Workload는 `image-classification-tight-slo`(p95 1 s), `plc-anomaly`(`plc-extruder-01-windows`), `smolvlm2-vqa`(VQAv2 `vqav2-val-subset`)다.
  세 canonical Workload는 인스턴스 0에서 자동제어를 시작한다.
  측정 전용 Workload의 비활성/정리 상태와 실제 UID를 실행 전에 확인하고 실제 완료 여유 설정값을 기록한다.
- PLC와 SmolVLM SLO는 D2 뒤 실제 d/m과 마지막 scheduler dump로 제안한다.
  노드별 가장 빠른 option의 b1 지연을 구하고 가장 넓은 지연 간격에서 목표를 고르는 방향을 따른다.
  κ>0인 노드가 절반 이상이고 가장 느린 두 노드는 κ0인지 p95/p99 표와 계산시간으로 확인한다.
  최종 percentile/SLO의 숫자는 실측 제안으로 고정한다.
- 기준 요청률 `R_w`: 최종 Workload SLO의 Node별 최대 적격 κ 합이다(`hack/kappa -mode slo`). **Node당 서빙 인스턴스 하나**이므로 세 고립 R은 같은 Node를 중복 사용하며 동시에 달성할 수 없다. 단계률은 **`rate_w=α_w×s_w×R_w`**다. `poolShares={MNV:1/3,PLC:1/3,Smol:1/3}`가 기본값이고 각 s는 유한한 [0,1], 합 1(오차 1e-9 이내)이다. α∈{0.3,0.5,0.7,0.9,1.2}는 POOL 사용률 단계이며 혼합 feasible 보장이 아니다. R·s·α·최종 rate/trace를 binding에 동결한다. 아래 단계 표의 수치는 R 직접 배수가 아니라 α이며 burst에서는 Smol의 α만 바뀐다.
- 부하는 `chill-loadgen replay --arrivals poisson`으로 Workload마다 동시에 보낸다.
  seeds101/202/303 배정, 도착 trace와 payload ID는 사전에 manifest로 고정하고 비교 쌍에서 재사용한다.
  모든 E5 호출은 실제 Workload UID/generation/SLO와 gateway cap을 캡처하고
  `--workload-slo`, `--gateway-timeout`, `--request-lifetime`, `--delivery-margin`, `--timeout`을 명시한다.
  L≥SLO, socket>L+delivery, cap≥L을 확인한다.
  tight MNV 예는 `--workload-slo 1 --gateway-timeout 129 --request-lifetime 2 --delivery-margin 0.010 --timeout 2.02`다.
  PLC/Smol 값은 D2 뒤 실측 SLO/m/G/κ/R 입력으로 고정한다.
  세 요청률 합이 실측 G 범위인지 고정 배치 혼합 계단으로 확인한다.
  G 초과 시 자동 clipping하지 않는다. native/client가 먼저 한계이면 gateway bound와 원인을 기록한다.
- 원래 L과 senderElapsed를 전달하고 `native remaining=L−senderElapsed−m`으로 계산한다.
  SLO와 transport L을 구분하고 m은 한 번 차감한다. signed d와 계획기의 `max(d,0)`를 각각 보존한다.
  D04/D09/D14/D23/D33의 profile 포함 95% 구간은 0 아래인 known systematic profile-versus-serving bias다.
  E5 해석에 이 한계를 포함하며 원인별 기여는 미확정으로 둔다.
  [정확 수치와 identity](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.json)와
  [관측/가설 구분](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-systematic-bias-20261024.txt)을 연결한다.
  signed raw, 원 Accepted와 max0는 유지하고 bias 보정과 d 추정정책 대안은 M5 후보로 둔다.

**E5 입력과 입구 조건.** 입력 보수는 rev72/e18ac에서 exact profile 13개의 prepare/remove 26단계,
empty 10창과 native up 2단계를 합친 8 roots/38 steps다.
fresh empty는 기준 모드 8개와 Jetsono/Jetsonx의 lowest 2개다.
Sampling 1 s, 요청 timeout 800 ms, 최대 원 gap 3 s, physical watchdog 2250 s와 empty 300 s를 사용한다.
loaded idle 13개나 전 19모드 확장은 이 범위에 포함하지 않는다.

남은 legacy empty ranking은 Jetsono MODE_30W/MODE_50W와 Jetsonx
MODE_10W_4CORE/MODE_10W_DESKTOP/MODE_15W_2CORE/MODE_15W_4CORE/MODE_20W_2CORE/MODE_20W_4CORE/MODE_20W_6CORE의 9개다.
이 기록은 ranking에 참여하며 불확실성은 unknown이다.
실제 lowest와 실제 소비되는 empty-mode energy baseline이 fresh10에 속하는지 소비 결과로 확인한다.
밖의 실제 소비나 예상 밖 handback mode는 보고하고 E5 입구에서 멈춘다.
ranking filter, 자동 추가 측정이나 수동 mode 강제는 적용하지 않는다.
loaded baseline과 profile idle의 실제 ref/시점은 별도로 기록한다.

| 입구 | 확인할 근거 |
|---|---|
| 정확 비용 | 최종 profile ID/NodeUID/mode/runtime/allocation의 prepare/remove와 필요한 power 비용 연결 |
| d | Smol9와 D2 뒤 CPU8/GPU5/PLC12의 새 규칙 근거. 실제 대체 Accepted 전 legacy CPU8/GPU2는 미재인증으로 표시하고 소비를 유지한다 |
| baseline | fresh10 안의 실제 lowest/empty 소비, loaded/profile idle 출처와 legacy9의 unknown 한계 |
| SLO와 수명 | 최종 Workload percentile/SLO와 m, L/socket/delivery/실제 gateway cap을 실행 전에 고정 |
| 용량과 전환 | 실제 G/κ/R로 초기/확장/축소/swap witness를 계산하고 모든 Workload의 전환 중 SLO와 자원을 확인 |
| 실행과 반환 | 필요한 측정의 terminal, actual execution/action/handback 0, 반환 readback과 원본017 비실행 보존 예외 |

각 witness는 `B_w=q+max(0,λ−κ_ret)×r`, `W_w=B_w/κ_new≤SLO−m`,
keep/best nominal J와 signed 상쇄 B를 공개한다.
keep과 best가 모두 feasible이면 gain>문턱일 때 전환하고 같으면 keep한다.
keep infeasible 회복은 유지한다. 자연스러운 swap witness가 없으면 차단 부등식을 보고한다.
정량 SLO/m/G/κ/R/N과 최종 제어요소 집합은 D2 뒤 실측으로 정한다.

**실행 순서.**

1. rev72 입력 보수를 끝내고 비용/profile/baseline catalog를 동결한다.
2. MNV만의 자율 BEFORE를 logical initial8, seed1, `λ=min(50,0.3Σκ)`, 660 s(준비 60+관측 600)로 수행하고 복구한다.
3. 정확한 8 Node의 profile/mode/runtime/batch/allocation을 고정한 PRIMARY native8을 준비한다.
   동일 old loadgen의 tight MNV, seed1, Poisson workers320, L2 s/delivery0.010 s/socket2.02 s로
   0부터 50 req/s씩 300 s 단계를 관측한다. 마지막은 `min(next50,Σκ)`이며 첫 60 s를 제외한다.
   throttle, 미완료 요청 또는 frozen κ 합에 닿으면 단계를 멈추고 부분 근거를 보존한다.
4. producer, HTTP/native outstanding, pending, measurement, action과 handback 0을 확인한다.
   사전 고정한 Ready IDLE native8만 rollout 예외로 보존한다. 수리와 새 실행 승인을 확인한 뒤 최종 D2를 진행한다.
5. startup re-adoption과 route ACK, 실제 PodUID/containerID/profile/mode/allocation/route의 동일성을 확인하고 PRIMARY paired AFTER를 수행한다.
   보존 실패는 physical-match unknown/mismatch로 보고하고 paired 주장을 중단한다.
   과거 all-instances0 배포는 이 물리 보존의 실증으로 사용하지 않는다.
6. PRIMARY AFTER를 정리한 뒤 같은 logical initial8로 자율 AFTER를 수행하고 복구한다.
7. D2 뒤 d25(CPU8+GPU5+PLC12), G와 m을 측정하고 정량 SLO/κ/R/N을 제안한다.
   U3 D04/D09의 실제 소비 source 교체와 옛 raw/status를 보존한다.
8. E5를 수행하고 M5, P11 main/spec 이관, main 제어 image(runtime7 고정), 최종 배포/P10 순서로 진행한다.

각 source의 실제 SHA와 image digest, 입력 dump, UID와 자원을 manifest에 남긴다.
BEFORE의 MNV 자율 Tick, 고정 경로의 no-control Tick과 최종 혼합3 search를 각각 보고한다.
G는 모든 요청이 완료되고 throttle이 없는 마지막 단계이며 native/client/capacity가 먼저 한계에 닿으면 bound와 원인을 표시한다.
요청 경로 CPU는 `(stage cores−idle cores)/rate`로 계산하고 지연, 거절, 만료와 미완료를 함께 보고한다.
`GatewayOverloaded`는 rejected 관측의 error와 client 503을 함께 대조한다. 같은 503의 다른 사유는 별도로 센다.
ChillServer와 genesis1 edgemesh의 cgroup은 1 s, restartCount는 15 s, thermal은 5 s 주기로 전후 동일하게 기록한다.
일반 Flush와 scraper cadence, 실제 server 6 CPU/4 GiB/GOMEMLIMIT3072MiB/gctrace1/timeout129 s/drain164 s/grace194 s를 보존한다.
단계 중 observation query는 경계에서만 수행하고 drain 뒤 같은 archive/Prometheus 집합을 조회한다.
최종 동일 scheduler dump의 source별 κ 계산은 측정과 live 부하가 없을 때 순차 수행한다.

**결과 진도.** 새 규칙 d 9/34, 정확 비용 69/69, 평가 시나리오 0/7이다.
정확 비용은 2026-10-24T12:49:27.350175648Z snapshot의 key join으로
[69 options의 prepare/remove 연결](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/cost-coverage-after-055.json)을 확인한 값이다.
G2 계약 세대 감사와 E5 실행 결과는 각각 별도다.
아래 E5 계획 9행의 결과 연결은 0/9이며 Run ID와 원시 경로가 확보된 결과만 기입한다.
7개 명세 평가 시나리오와 9개 E5 계획 행(E5-0~7+S)은 각 분모를 유지한다.

| 단계 | 시간 | 요청률 (mobilenet, PLC, SmolVLM) | 재는 것 | 채우는 칸 | 근거(Run ID, 원시 경로) |
|---|---|---|---|---|---|
| E5-0 시작 | 10분 | 모두 0 | Workload마다 측정 근거가 있는 노드의 첫 인스턴스, 나머지 가용 노드의 가장 낮은 모드, 무부하 전체 POOL 전력 | `SCH-AC-24`(세 Workload) | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-1 일정 | 30분 | 모두 α=0.3 | Workload별 지연 분포와 처리 결과 수(완료, 실패, 거절, 취소, 미완료), 전체 POOL과 활성 POOL 에너지, 창별 예측 백분위와 관측 백분위 | [명세 평가 표](../../spec/README.md#실클러스터-검증과-평가) 일정 부하와 이기종 워크로드 동시 혼합 부하 행, `SCH-AC-23`, [07 검증](../architecture-rethink/07-validation.md) 2번 | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-2 버스트 | 15분 | Smol α=0.3→0.9, 나머지 α=0.3 | 대응 시작 시간, 측정 가능한 GPU 장착 Node의 Workload 소유권 재배치, 전환 중 모든 Workload SLO와 Jetson 모드 변경 지연 | 평가 표 갑작스러운 버스트 행, `SCH-AC-21` | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-3 증가 | 30분 | 모두 α=0.5,0.7,0.9 각10분 | 확장 순서, 전환 구간 SLO, 판단의 `κ_new`, `B`, `W`와 관측 큐 | 평가 표 점진적인 증가와 감소 행(증가), `SCH-AC-15` | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-S 정적 기준선 | 170분 | E5-1부터 E5-6까지의 요청률과 seed | E5-1부터 E5-6까지와 같은 항목, 고정 구성의 인스턴스와 전력모드 | [명세](../../spec/README.md#실클러스터-검증과-평가)의 정적 배포 비교, `SCH-AC-20` | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-4 한계 | 15분 | 모두 α=1.2 | `Overload` 판단과 최대 초과량 순서, Workload별 실제 지연과 처리 결과, 노드별 인스턴스와 전력모드 | 평가 표 전체 POOL의 한계를 넘는 부하 행, `SCH-AC-16` | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-5 급감과 재급증 | 20분 | 모두 α=0.3 10분, α=0.9 10분 | 축소와 유휴 제거, 반복 전환의 J와 시간, U2 최소안 Jetson 모드 변경/KEEP 근거와 read-back | 평가 표 전환 직후 급감과 재급증 행, `SCH-AC-15`; 점진 감소는 미평가 | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-6 soak | 60분 | 모두 α=0.3 | 1시간 예측 에너지 합과 비교 집합 실측, 전체 POOL 실측, `completionResidualNanos` 분포와 설정 여유 | 위 표 E5 행, [용량표 계획기](analytic-planner.md#11-확인) 에너지(실측) | 미측정. Run ID 없음, 원시 경로 없음 |
| E5-7 실패 | 15분 | 모두 α=0.3 | 180초 인스턴스 강제 종료, 360초 Node 하나 cordon+서빙 Pod 삭제, 600초 uncordon; 격리·대체·실제 복구와 실패 동작 원인 | 평가 표 노드·서빙·제어 동작 실패 행, `SCH-AC-45` | 미측정. Run ID 없음, 원시 경로 없음 |

**7행 커버리지와 실패 주입 계약:** [E5 계획의 커버리지 표](../../plans/2026-10-26-e5-evaluation.md#명세-7행-커버리지와-한계)가 정본이다. 일정=1/6, 증가=3(점진 감소 없음), 버스트=2(Smol만), 급감/재급증=5, 이기종 혼합=1~7, 한계=4, 실패=7이다. 독립 반복은 없으며 완전 커버리지로 주장하지 않는다. E5-7은 기존 kubectl의 Pod 강제 삭제와 cordon/uncordon으로 운영 담당만 직렬 실행한다. cordon은 네트워크 단절이나 기존 Pod 종료가 아니므로 삭제를 함께 사용한 배치 차단 대리 실험으로 보고한다. 대상 UID·명령 UTC·응답·격리·대체 결정·Ready/ACK·route 복구를 보존하고 실패 시에도 uncordon read-back을 남긴다. 제어 실패는 이 주입에서 실제 관찰한 유형에 한정한다.

**U2 전력 모드 장면:** 최소안은 jetsono MAXN↔MODE_15W, jetsonx MODE_15W_6CORE↔MODE_15W_4CORE, jetson-nano MAXN↔5W의 세 모델 CPU q1/B1이다. [모드 커버리지 계획](../../plans/2026-10-27-power-mode-coverage.md)의 근거 5종이 Accepted인 조합만 E5-5 후보로 연결한다. 실측 부족을 κ0으로 바꾸거나 근거 없이 모드를 강제하지 않는다. 비용상 KEEP이면 차단 근거를 남기며 전력 절감 성공으로 세지 않는다.

**E5-2 A.** genesis2/genesis3의 SmolCUDA는 OOM 제외를 유지한다.
해당 GPU 장착 Node는 측정된 MNV/PLC CUDA 또는 Smol CPU를 후보로 사용하고,
Jetsono SmolCUDA도 실제 후보로 비교한다. CPU 후보를 유지하며 SmolCUDA 채택이나 GPU 승자를 성공 조건으로 두지 않는다.

**E5-S 정적 기준선.** E5-3 끝에서 구성을 고정해 같은 부하를 다시 보내고, 끝나면 자동제어를 되돌려 E5-4로 간다. 고정 구성은 α=0.9(`rate_w=0.9×s_w×R_w`)에서 판단이 고른 구성, 곧 최대 정상 단계에 맞춘 배치다.

1. 고정: `/api/v1/scheduler/state` snapshot에서 세 Workload의 인스턴스와 routing을 읽어, 세 UID를 `autonomousWorkloads`에서 빼고 그 routing을 `staticRouting`으로 담은 PUT 하나를 `reason`(위 선택 이유)과 함께 보낸다([정적 배치 사용법](scheduler-operations.md#정적-배치-사용법) 2단계). 꺼진 Workload의 인스턴스가 있는 노드는 인스턴스와 전력모드를 그대로 둔다(`FixedWorkload`). PUT 응답의 `validatedAt`, ConfigMap 구성의 `staticRoutingValidatedAt`, `manual-static-routing` 동작의 `operation.settingsValidatedAt`이 같은지 실행 기록에 남긴다.
2. 부하: E5-1부터 E5-6까지의 요청률을 같은 seed로 차례로 보낸다.
3. 되돌리기: 세 UID를 `autonomousWorkloads`에 다시 넣고 `"staticRouting": {}`(선언 삭제)와 이유를 담은 PUT 하나를 보낸다. 선언 삭제는 동작을 만들지 않으므로 분배는 그대로이고 다음 판단이 이어받는다. 그 구성에서 E5-4를 시작한다.

- 모든 단계에서 Tick p50/p99, ChillServer CPU/RSS/GC CPU, 관측 PVC 증가와 edgemesh CPU/memory/throttle/restart를 기록한다.
  `GatewayOverloaded`와 노드당 인스턴스 하나 조건을 확인한다.
- 계획 탐색과 게이트웨이의 CPU 나눔(탐색 규모의 오프라인 근거는 [용량표 근거 7장](capacity-table-evidence.md#7-계획-탐색)). 판단마다 다음을 남긴다.
  - 탐색: `decision.capacity.search`(`neighborhood`, `evaluated`, `durationNanos`, `budgetCut`)와 `evaluationElapsedNanos`. 시간 예산은 `controlIntervalNanos`의 절반(제어 주기 10 s에서 5 s)이다
  - Tick CPU: 판단 시작과 끝 사이 `cgmon.sh` 표본의 ChillServer CPU(cgroup v1 `cpuacct.usage` 차)와 `cpu.stat`의 `nr_throttled`, `throttled_time` 차
  - 게이트웨이 지연: 요청 기록의 ChillServer 안 시간(`elapsedNanos`에서 firstfloor `nativeElapsedMS`를 뺀 값) p99를 탐색 구간과 그 밖 구간으로 나눠 단계마다
  - SLO: 1분 구간마다 Workload별 SLO 충족 비율, 그 구간에 탐색이 있었는지
- `planSearchWorkers`와 시간 예산이 gateway 지연과 SLO에 주는 영향을 실제 G와 CPU 관측으로 보고한다.
  sample 3과 과거 G 약 220 req/s의 CPU 배분은 선행 근거다.
  최종 E5 G와 구분하며 `budgetCut`의 수와 선택 구성, `TransitionGainWithinUncertainty` keep 수도 기록한다.
- 평가 표의 분석 값은 Workload별 지연 분포와 처리 결과, 전환 구간을 포함한 전체 POOL 에너지, 활성 POOL 에너지다. 단계 경계와 seed, 단계별 `R_w`와 G, 사용한 profile과 설정이 실행 기록에 남는다.
- 평가 분모는 모든 유효 offered 요청이다. 전환을 포함한 정상 단계/하위 단계의 SLO와 1분 관측을 남긴다.
  완료, 실패, 만료, drain과 censoring, 미완료와 표본 수를 함께 보고한다.
  thermal은 5 s 주기와 active/latched/unknown, 사전 온도 bin(<60/60–70/70–80/≥80°C)으로 기록하고 미관측 bin의 일반화를 제한한다.
  E5-S는 E5-3 끝 α=0.9의 실제 구성/모드/route를 고정한다.
  제어요소 비교는 N=2 권고안(membership-off, replicas-off)이며 각각 하나씩 끄고 같은 E5-2/E5-5 arrival trace와 실제 효과 witness를 보존한다.
  placement/자원은 배치·profile과 결합, routing은 no-op/안전 전달 경로, batching은 고정 B와 deadline 의미 차이, power는 U2 최소안 장면만 관찰하므로 별도 off 비교에서 제외한다. 전체 제어요소의 독립 기여를 증명하지 않는다.
  seeds101/202/303, payload ID와 profiling/evaluation 부분집합을 사전에 고정한다.
  한 paired trace의 case study와 독립 seed 반복을 구분하며 사후 threshold/seed/집단 선택을 하지 않는다.
  정량 SLO/m/G/κ/R/N과 나머지 평가 threshold는 D2 뒤 실측에 근거해 고정한다.
- E5 full 195분과 static 170분은 합계 365분이다(E5-S는 실패 주입 제외).
  제어요소 N=2의 E5-2/E5-5 비교는 70분을 더해 435분이다. reset, settling, warmup, drain, flush와 추가 seed 시간은 별도다.
  캠페인 중 다중 Workload 시험은 선행 근거로 보존하고 E5 결과 칸은 해당 Run ID가 있을 때 채운다.
- 첫 준비(부하 시작→첫 분배 ACK) 요청은 전체 offered 분모를 유지하고 별도 집단으로 표시한다. Node 차단 명령 시작→uncordon 뒤 실제 Ready·관측·route 회복 확인 구간도 전체 결과를 유지하며 표시하고, 해당 구간 제외 집계를 분모·실패 수와 함께 병기한다. 미복구는 단계 끝까지다. 사후 유리한 창을 고르지 않는다.
- 실행 설정 `hack/e5/demo-config.e5.example.json`의 capacityR는 최종 숫자 승인 뒤 채운다. 실패 주입 runbook은 운영자용이며 자동 실행하지 않는다. 짧은 preflight는 E5-0 Ready·혼합 부하·토큰·전력·정리 경로를 검사하며 연구 장애 실증을 대체하지 않는다.

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
| D-4 | PMTC는 `modes[0]` 상태에서 시작해야 하며, 첫 시도는 `SourceStateMismatch`로 실패했다. Admission은 선택 runner 프로파일의 DeviceClass 기준 Node read-back이 `modes[0]`과 다르면 taint·runner Pod 생성 전에 `Refused: RunnerSourceStateMismatch`를 기록한다. |
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

## 10. 결정과 재현 근거

본문의 측정, 최소 이득, legacy와 실행 순서는 다음 승인 자료의 결정을 담는다.
캐시 자료는 승인 원문과 상세 실행 manifest의 근거로 연결한다.

- [d 결정](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/d-measurement-decision.md)
- [D2와 E5 승인](/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/deployment2-e5-proposal-20261024.md)
- [정확 8 roots/38 steps](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/e5-input-repair-plan.json)
- [empty19와 legacy9 목록](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/empty-baseline-plan.json)
- [BEFORE/AFTER와 native8 조건](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/deployment2-before-plan.json)
- [Smol source 교체와 반환](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/smol-d-followthrough.json)
- [legacy MNV GPU2 provenance](/home/genesis1/.cache/chill-assets/ops/pools-deploy/audit-20261024/legacy-mnv-gpu2-provenance.json)
