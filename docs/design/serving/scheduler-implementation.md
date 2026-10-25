# CHILL 스케줄러 구현 설계

## 1. 기준과 적용 범위

[스케줄러 SRS](../../spec/scheduler/SRS.md)의 SCH-01~32, SCH-48~52와
[요구사항·구현 대조](scheduler-srs-audit.md)에 대응하는 현재 구조와 확정 변경 설계다.
요구사항과 수용 기준의 원본은 `docs/spec/`이며 이 문서는 구현 책임과 불변식을 정리한다.

firstfloor의 Go 큐·배처·persistent Python IPC runner를 `internal/firstfloor`,
`cmd/serving-worker`, `runtime/firstfloor`에 연결했다. 동료의
`/home/genesis1/ETRI/donghwan/firstfloor`는 보존한다. 실제 actor와 용량표 replay는
같은 순수 선택 함수를 사용하고 ingress Runtime은 Workload 수용·분배·drain을 맡는다.

| 세부 계약 | 원본 문서 |
|---|---|
| 실행기·시간·CPU·fixed-batch·baseline·catalog | [실행기와 측정](scheduler-runtime-measurement.md) |
| 설정·completion margin·API·관측 조회 | [설정과 관측 API](scheduler-api.md) |
| Pod-bound 전달·수동 원자 실행 예시 | [인스턴스 전달과 수동 실행](scheduler-operations.md) |
| 과거 build·smoke·배포 결과 | [실행·배포 이력](scheduler-execution-history.md) |
| 최신 live evidence·수용 범위 | [라이브 증거 캠페인](../../plans/2026-10-13-scheduler-live-evidence-campaign.md) |

## 2. 책임과 패키지 방향

| 소유자 | 입력과 책임 | 실제 완료 근거 |
|---|---|---|
| `internal/firstfloor` | bounded per-UID queue, frozen request SLO, 같은 UID의 호환 batch, energy-gain 대기, 단일 runner 실행 | 요청별 progress·실제 batch·runner 종료·동기 응답 |
| `internal/serving` | 정의·receipt·수명·ingress 대기, 운영 부하, 용량표와 κ 합 판정, 노드 계획 선택, 분배·판단 관측 기록 | 실제 native 상태와 반환 결과·분배 적용 기록 |
| `internal/serving/firstfloor` | HTTP/metadata/progress의 typed 경계, native span과 client span | prepared metadata, 정확한 request/UID/definition/physical instance 상관 |
| `internal/serving/kube` | 실제 자원 snapshot, profile/cost catalog, Pod·policy·power 원자 동작, 전환 의존성 | API 반환에 이어 실제 Pod/native readiness·drain·삭제·Node 상태 확인 |
| 기존 Profiling owner | 동일 firstfloor 경로의 fixed-batch 측정·idle·raw evidence·curve | 원래 Run/Job/Node/container/image/actuation와 raw trial binding |
| 기존 NodeActuator | 유일한 호스트 전력모드 변경 | 독립 read-back·settling·Ready/Failed |
| 기존 metric/evidence owner | Node별 wall-power·sample/failure·적분·immutable raw 보존 | 조건·구간·coverage가 확인된 watts/joules |

`firstfloor`는 Serving/Profiler 도메인에 역참조하지 않는 실행기 leaf다.
Serving core는 leaf를 사용하고 Kubernetes/API 호출은 kube adapter에 둔다.
프로파일러는 자체 runtime evidence 계약과 firstfloor 실행 경로를 연결한다.
`.go-arch-lint.yml`에 이 방향과 실제 생산 패키지를 함께 반영한다.

## 3. 실행기·요청 계약

외부 요청은 Workload별 독립 입력 1개와 불변 UID·definition·개별 SLO를 유지한다.
[Workload 요청 경로](workload-management.md#7-요청-경로와-큐)를 재사용하며,
[실행기·시간 계약](scheduler-runtime-measurement.md#요청과-실제-모델)에 원시 입력 필드,
IPC, deadline policy, 원래 monotonic elapsed와 physical release 조건을 둔다.

판단은 고정 historical `observationEnd`와 현재 outstanding의 decision origin `at`를
구분한다. source 시각을 재표시하지 않고 미래·오래된 물리 관측은 Pending으로 둔다.
capture는 observer refresh 뒤 observer 상태(profile)를 먼저 읽고 Node 목록을 읽으므로, Node 관측 시각과
판단 시각 사이에 observer lock을 기다리지 않는다. DeviceClass 모드도 한 번 읽은 목록에서 찾는다. prepare의 준비 대기는 poll 간격 안에 끝난 observer
refresh(서버의 source refresh, 또는 lock을 기다리는 동안 끝난 refresh)를 다시 쓴다. operation capture는 대상 Node의
Pod만 `spec.nodeName` field selector로 읽는다. 근거: m4 r7의 prepare 동안 판단이 11~28 s 걸렸고 모든 Node가
목록 뒤 약 11.5 s에 `UnknownOrStaleNode`였다. 실행 중인 prepare마다 1 s마다 observer 전체 refresh를 돌렸고,
capture는 Node 목록 뒤 observer lock을 기다렸다.
완료 여유는 설정의 `completionMarginMS`를 요청마다 한 번 반영하며 이미 소모한 sender
elapsed는 원래 receipt 경과에서만 뺀다. 상세 window·margin·반환 milestone 규칙은
[시간과 수명](scheduler-runtime-measurement.md#시간과-수명)이 원본이다.

### 3.3 같은 선택 함수의 실행과 replay

순수 batch 선택 함수의 입력은 검증된 batch별 service duration sample·incremental
energy/input, 현재 per-UID 요청의 원래 경과 시간/SLO, queue age, 반환 envelope,
max wait, 관측 요청률이다. 출력은 선택 UID·batch size·대기 시간과 판단 이유다.
actual actor와 discrete-event replay가 이 함수를 공유하고, replay는 용량표 행의 κ 판정(5.2절)에 쓴다.

1. normal 요청은 stable EDF로 선택하며 같은 UID/불변 정의의 호환 요청만 배치한다.
2. 요청별 p-quantile service 비용과 원래 elapsed·반환 비용으로 여유를 계산한다.
3. SLO 여유를 만족하는 현재 batch 중 energy/input을 비교한다.
4. 더 큰 measured batch의 **엄격한 energy gain**과 관측 요청률로 예상 fill time을
   확인한 경우에만 bounded wait를 선택한다.
5. feasible batch가 없으면 keep은 minimum-C fillable batch(동률은 큰 batch)를 즉시
   실행한다. drop은 head를 제거하고 defer는 head를 후순위 FIFO로 옮긴다.
6. normal 요청이 없으면 deferred FIFO의 호환 prefix를 minimum-C로 실행한다.
7. drop/defer/timeout은 성공 지연 표본과 구분하며 caller lifetime/cancel은 별도로 적용한다.

실제 적용되는 설정·profile과 진행 중 batch의 snapshot을 기록한다. accepted
요청의 정의·SLO를 config 변경 때문에 다시 해석하는 대신 기존 context를 유지한다.

## 4. 측정 계약과 catalog

Serving과 Profiling은 같은 firstfloor actor/runner·initialized batch 조건을 사용한다.
고정-K 측정은 fake profile 없이 실행하고 raw client/service/backend sample,
warmup 제외·자원·장치·image와 원래 trial binding을 보존한다. 실행 domain과 policy cap은
각각 초기화·실측 domain을 따른다.

profile은 incremental energy가 차감한 idle과 raw evidence를 보존한다. baseline은
실제 Node·mode·준비 집합·meter에 연결하며 공존 측정 전 additive 예상은 실측과 구별한다.
원자 동작 측정은 production primitive를 호출하고 실제 readiness/drain/read-back,
wall joules·부분 실패·불충분한 표본을 보존한다. 세부 실행 조건·catalog lifecycle·
baseline과 추론 제외 기준은 [측정 계약](scheduler-runtime-measurement.md#같은-firstfloor-fixed-batch-경로)에 둔다.

## 5. 판단 입력과 계획

### 5.1 한 운영 snapshot

운영 observation window·control interval·comparison horizon을 독립 설정한다.
관측 기록의 보존/발행 구간과 지정 구간의 분석 필터는 운영 부하와 구분한다.
모든 UID의 received/accepted/assigned/completion 사건을 한 번 수집해 해당 사건
구간의 inputs/s를 구하고, 실제 malformed/거절 결과도 전체 유입에 남긴다.
계산 가능한 compute demand와 total inflow는 모집단으로 구별한다.

Runtime의 pending/assigned와 firstfloor 현재 queued/running·running elapsed·batch를
physical instance/UID/definition에 연결한다. assignment와 실제 queue entry를
구별한다. 계획기는 Workload의 처리 중 요청 수를 요청률 λ와 밀린 요청 B의 대기 수 q로 센다
([용량표 계획기 3장](analytic-planner.md#3-판정과-순서)).
unknown instance·관측 공백·오래된 자료는 판단의 입력 상태에 표시한다.
현재와 목표의 Node UID·단일 POOL 소속·ready/capacity/resource 상태를 함께 캡처한다.
완료/실패 Pod는 이미 제외하던 자원 예산 캡처와 steady source refresh에서
Kubernetes field selector로 제외하여 종료된 profiling 이력의 반복 전송·해석을 피한다.
source refresh의 최초/재시작 watch 복구는 전체 Pod를 읽는다. source refresh는 instance 수와
관계없이 공유 client로 네 번 읽는다. Node 목록, Pod 목록, Service 목록, 그리고 native probe 뒤의
namespace 전체 Pod 목록이다. 마지막 목록이 probe한 executor가 그 Service가 고르는 유일한 실행
중 container임을 확인하고, 빠진 retained executor의 UID/container 종료도 확인한다. native probe
하나는 2초 안에 끝나므로 느린 executor가 source lock을 refresh 기한까지 쥐지 않는다.
근거: d9 병렬 단계에서 instance마다 Service와 Pod를 세 번 읽어(instance 6개면 20회) refresh가
review가 밀린 limiter 안에서 30초 기한을 모두 썼다.
Service 목록이나 probe 뒤 Pod 목록 읽기가 실패하면, profile과 소유 검사를 통과하고 owner와 profile이
그대로인 executor는 직전 refresh에서 probe로 확인한 준비 상태를 그 refresh 한 번 동안 유지하고, 그 실패를
observer issue와 로그에 남긴다. 다음 refresh도 읽기에 실패하면 instance는 준비되지 않은 상태가 된다.
executor가 바뀌거나 준비를 잃으면 Pod watch와 log follow가 그 instance를 끈다. Node 목록이나 Pod 목록
읽기가 실패하면 refresh는 할당을 닫는다.

#### 정책 ACK 대기와 실제 관측 실패의 분리 — SCH-48~49

2026-10-26 확정 설계다. 아래 C·A·B는 **한 배포에서 함께 적용할 변경 계약**이며,
이 문서 수정은 구현·회귀 통과를 뜻하지 않는다. ACK 대기로 물리 점유가 사라지는
원인을 C로 없애고, 진짜 읽기 실패는 A로 막으며, 이미 생긴 중복 실물은 B로 정리한다.
`ExclusiveCPUInfeasible` 등 현재 전체 자원 검증 실패의 **전역 `Pending`은 유지**한다.
Node 하나의 `InstanceUnavailable`을 새 전역 중지 사유로 확대하지 않는다.

**C — ACK pending은 오류가 아닌 반영 대기 상태다(SCH-48, AC-34~35).**

- 현재 `internal/serving/firstfloor/client.go:105-132`의 `validateMetadata`는
  `:122-123`에서 desired/actual `DecisionID` 불일치를 오류로 반환하고,
  `Probe(:156-173)`가 정상 native state까지 버린다. 이 불일치만 검증 오류에서 빼고,
  나머지 profile·runtime/provider/device/affinity·입출력·준비 상태 검증은 모두 수행한다.
  모든 검증이 통과한 `ReadyInstance.State.Reason`에 `PolicyAckPending`을 넣어
  `(ReadyInstance, nil)`로 반환한다. 새 pending bool·캐시·오류 sentinel은 만들지 않는다.
- 마지막으로 **실제로 확인한** physical ID·Node·profile·allocation·owner·TargetID,
  native readiness·정책·요청 목록을 보존한다. 성공한 이번 state read가 있으면 그 실제
  값과 원래 관측 시각을 사용한다. desired 정책으로 Native를 덮거나, ACK 대기만으로
  `Ready=false`/`Stopped=true`/`Native=nil`로 바꾸지 않는다. 읽기 실패 때 오래된 상태를
  무기한 되살리는 계약이 아니며, 교체된 executor/profile의 상태를 재사용하지 않는다.
- `kube/observer.go:394-405`는 이 반환을 성공 probe로 받아 `verifySource(:410-439)`의
  동일 Pod UID/container·Service 단일 executor 검증을 거친다. pending은
  `sourceUnavailable(:442-447)`나 실패 fallback `sourceReadFailed(:453-459)`로
  보내지 않는다. 진짜 HTTP/디코딩/metadata/Pod·Service 검증 실패는 기존 오류 경로다.
- `Runtime.InstallInstances`는 정상 instance로 유지한다. `kube/scheduler.go:695-708`의
  `captureNodePods`에서 `Native != nil`이므로 실제 allocation을 snapshot에 포함하고,
  `State.Reason`을 `SchedulerInstance.Reasons=["PolicyAckPending"]`로 전달한다.
  pending만으로 Node에 `InstanceUnavailable`을 붙이지 않는다.
- scheduler 원본·compact·summary와 관측 기록은 instance의 기존 `reasons`에
  `PolicyAckPending`을 노출한다(`httpapi/scheduler_summary.go:26-35,74-79` 포함).
  POOL은 `BuildPools(runtime.go:1109-1163)`의 기존 `InstanceState.reason`을 그대로
  내보내고 소비자는 **“반영 대기”**로 표시한다. 소유/바인딩된 Workload POOL에서
  사라지거나 available POOL로 이동하지 않으며, ACK 확인 뒤 다음 정상 probe에서 표시가 지워진다.
- 물리 존재/현재 readiness와 **새 정책 적용 완료**는 별개다. `operations.go:1109-1137`의
  native `DecisionID`·정책 ACK 확인, `scheduler.go:1186-1214`의 실제 target/policy 확인은
  유지한다. pending을 route 완료 증거로 쓰거나 `Actual.Native`에 목표 ACK를 만들어 넣지 않는다.
  진행 중 route는 owner의 예약 안에서 frozen target과 native ACK 대기를 유지하며 예약 밖 판단을 막지 않는다.

**A — 실제 `InstanceUnavailable` Node의 부재 추론을 금지한다(SCH-49, AC-34~35).**

`SchedulerNode.Reasons`에서 기존 `NodeInstanceUnavailable`을 **NodeReference(Name, UID)가
일치하는 Node별로** 검사한다. 이미 있는 `schedulerAvailable(:56-59)`와
`capacityPlanner.nodePlans(scheduler_capacity.go:566-575)`의 keep-only gate만으로는
진행 중 목표의 `schedulerPlan` 재구성이 보호되지 않으므로 다음 네 위치에도 적용한다.

| `internal/serving/scheduler_plan.go` 위치 | 검사 시점과 거절 결과 |
|---|---|
| `prepareTargets:568-585` | wanted Node를 고른 즉시, `runningPreparation`·존재 비교·`freePreparationResources`·새 prepare 추가 전에 검사한다. 해당 Node의 새 prepare를 거절하고 이유 `InstanceUnavailable`을 기록한다. |
| `preparationFits:637-664` | 같은 Node를 찾은 직후, allocation 조립/`ValidateNodeBudget` 전에 `false`다. “빈 Node여서 fits”가 아니다. 앞선 prepare gate가 막으므로 이 false를 근거로 blocker stop/remove를 생성하지 않는다. |
| `changeNodes:513-558` | oldNode와 target Node가 일치한 뒤, Fixed lowering 재사용이나 stop/drain/power/remove 사슬을 추가하기 전에 검사한다. 해당 Node의 새 전력 변경을 거절하고 이유 `InstanceUnavailable`을 기록한다. |
| `completedEffectPresent:465-468` remove 분기 | action Node를 먼저 검사하여 `false`를 반환한다. snapshot에 instance가 없다는 이유로 remove 완료를 재사용하거나 후속 prepare/power 의존성을 해제하지 않는다. |

거절은 기존 Node 계획의 `reason=InstanceUnavailable`, snapshot Node 이유와 해당
후보/동작의 이유에 연결하고 Node identity를 함께 남긴다. **prepare/power만 조용히
생략하고 target을 실행 가능하다고 반환해서는 안 된다.** 해당 Node 변경이 필요한
후보/진행 중 목표의 후속 동작은 보류하고 이유를 전달한다. 이미 실행 중인 물리 동작을
실패·취소·완료로 조작하거나 같은 동작을 재전송하지 않는다. 정상 Node의 독립 계획은
기존 전역 검증을 통과하는 한 계속 가능하다. unknown과 실제 제거/종료 proof를 구분한다.

**C 뒤 확인할 기존 소비자(별도 의미 변경 없음).**

| 소비자 | 확인 내용 |
|---|---|
| `scheduler_plan.go:475-489` `routeEffectPresent` | 동일 physical ID/분배/소비 policy 수치가 실제로 유지되면 ACK 표시만으로 완료 효과가 소실되거나 route가 재전송되지 않는다. 실제 분배·batch/wait/capacity 변경은 여전히 효과 불일치다. running route의 신규 ACK 대기는 완료로 단축하지 않는다. |
| `scheduler_capacity.go:402-405` `w.current` | pending 전후 같은 Workload의 `!Stopped` instance 수가 그대로다. snapshot 누락 때문에 0으로 세거나 replica 보충을 계획하지 않는다. |
| `observation_log_power.go:587-591` active membership | `Ready && !Stopped && WorkloadUID != ""`인 같은 instance가 ACK 대기에도 남아 active POOL 구간이 끊기지 않는다. 실제 stop/remove 때만 기존 membership 판정대로 빠진다. raw 전력 coverage·whole-POOL 합산 규칙은 바꾸지 않는다. |


### 5.2 용량표와 노드 계획

Tick은 요청을 재생하지 않는다. 용량표 worker가 Tick 밖에서 켜진 Workload의 SLO와 측정 option 변형(option과
배치 상한)마다 SLO를 지키는 최대 요청률 κ를 계산하고, Tick은 이 표의 κ 합으로 판정한다. 행의 키, 포아송 창 판정,
격자와 이분 탐색, `NonMonotone`, 계산 시점과 이전 행 사용은 [용량표 계획기 2장](analytic-planner.md#2-용량표)이
원본이고 설정은 `capacityWindowNanos`, `capacityWindows`, `capacityPassingWindows`, `capacityMinimumRequests`다.
디스패치 점유는 snapshot `dispatchOccupancy`의 측정 record이고, 노드, 전력모드, runtime image, model의 측정이 없는 option
행은 `MissingDispatchOccupancy`로 κ가 없다.
켜진 Workload에 κ가 있는 행이 하나도 없으면 판단은 `CapacityPending`이고 사유는 `CapacityPending:<Workload UID>`다.

창 하나는 option 인스턴스 하나만 있는 keep snapshot에 seed로 정한 포아송 도착을 넣고 3.3절의 선택 함수로 재생한다.
실행 시간은 empirical service sample에 배치 크기 × 디스패치 점유를 더한 값이다. 각 요청의 queue, 실행, 전달과 반환이 반영된 종료 시각에서
**전체 end-to-end 분포**를 만들고 gonum/stat로 Workload percentile을 계산한다. percentile 지연(완료 여유 포함)이
SLO 목표 이하이고 timeout과 만료가 0이면 그 창은 충족이다.

노드마다 keep과, 그 노드에서 측정한 option 변형 하나 중 그 전력모드에서 노드 예산(`ValidateNodeBudget`)을 만족하는
노드 계획을 만든다. 노드마다 서빙 인스턴스는 하나다. 계획마다 Workload별 `κ_new`와 `κ_ret`,
route 전후 동작 사슬과 전력을 둔다. 꺼진 Workload의 인스턴스가 있는 노드(`FixedWorkload`), 측정 POOL 노드
(`MeasurementNode`), 준비되지 않은 노드(`NodeUnavailable`)와 배치 설정이 부족한 노드는 keep뿐이다. 비용, 용량표 행,
자원이나 기준 전력이 없는 계획은 사유와 함께 기록에 남고 비교에서 빠진다. 노드 계획 규칙과 제외 사유는
[4장](analytic-planner.md#4-노드-계획과-탐색)과 [14장](analytic-planner.md#14-구현-규칙)이 원본이다. 꺼진 제어 축
(placement, resources, replicas, routing, batching, membership)의 규칙과 `maxReplicas`, `maxInstances`, `maxCPUs`
상한은 14장의 설정 gate를 따른다.

인스턴스는 Workload 하나를 맡고, snapshot과 기록의 인스턴스는 `workloadUID` 하나를 가진다.

ingress Runtime과 replay는 같은 deterministic smooth weighted rule로 요청을 분배한다. 판단의 분배는 켜진 Workload마다
합 1,000의 정수 가중치이고, 꺼진 Workload의 분배는 현재 값을 그대로 둔다. 적용할 때 active UID마다 분배가 있어야 하고
빠지면 오류다. 준비 slot은 실제 Ready physical ID로 해결한다. UID별 smooth-weighted credit은 같은 physical ID의
weight가 그대로인 경우에 유지하고, weight 변경과 새 route entry는 0에서 시작한다.

CPU/GPU/memory는 전체 allocations와 system/비서빙 예약분을 포함한 예산으로
검토한다. `CPUCount`는 측정/실제 Pod request의 올림값이며 quota·ORT threads·
허용 affinity 폭과 구분한다. 측정한 threads와 전체 affinity를 그대로 재현하고,
affinity 폭이 threads보다 큰 구성도 허용한다. 서빙 mask 사이의 중복과 예약 침범은
계속 차단한다.
GPU UUID와 whole-device exclusive allocation을 확인한다. measured peak memory와
Serving 부가 예산은 feasible 제약이다. Pod마다 한 prepared runner와 순차 batch를
사용하며 replica는 Pod 수로 나타낸다.

비서빙 Pod의 effective CPU request는 Node별 millicore 합계를 먼저 구한 뒤
한 번만 올림하여 quota 수량 예산으로 기록하고, effective memory request도 합산한다.
이 quota 합계는 OS affinity/exclusivity 관측의 증거가 아니다. 명시적 exclusive CPU
예약이 전체 millicore 합계를 덮지 못하거나 CHILL이 배치하지 않은 Pod가 물리 GPU를 요청하면 그 Node는
`PlacementBudgetUnavailable` 사유를 받고 노드 계획은 keep뿐이다. 현재 native 서빙 인스턴스는 이 합계에서 제외한다.
사유가 있는 Node도 전체 POOL에 남아 다른 Node의 계획은 계속된다.

### 5.3 전환·에너지·선택

프로파일 유휴 전력과 물리 meter는 같은 Node와 node-exclusive-wall 범위,
power source kind·NodeName·endpoint·metric·label selector로 연결한다.
동반 thermal endpoint/metric은 별도 관측 identity로 보존하며 같은 벽전력량의
연결 조건으로 사용하지 않는다. 다른 power series나 Node·범위는 계속 거절한다.

전환은 Workload마다 최종 상태와 전환 중 상태를 함께 판정한다([용량표 계획기 3장](analytic-planner.md#3-판정과-순서)).
node mode 변경·제거는 해당 노드의 모든 instance와 UID에 영향을 준다. 새 allocation을
동시에 수용할 수 있으면 prepare→route 뒤 기존 source를 stop/drain/remove한다.
exclusive CPU/GPU 또는 실제 예산 때문에 동시 점유가 불가능하면 최종 target에 없는
blocking source만 기존 stop→drain→remove→prepare→route 의존성으로 교체한다.
accepted ingress는 미래 target의 호환성만으로 포기하지 않는다. 현재 Ready이며 실제
routing된 대체 source가 없으면 기존 source에서 먼저 dispatch하고 원래 identity로
drain한다. route 전까지 남는 인스턴스의 κ 합 `κ_ret`와 route 시각 r까지 밀린 요청 B로 전환 중 대기 W를 판정하므로
합법적 DAG도 전환 중 SLO에서 탈락할 수 있다. route 전에 인스턴스를 지우는 모드 변경 계획은 그만큼 `κ_ret`가 줄어든다.
독립 Node의 안전한 동작은 병렬로, 같은 Node의 비용 구간은 의존 순서대로 반영한다.

기존 logical cohort의 routing origin은 weight 보존 근거이며 새 physical slot을
강제하는 fence가 아니다. 같은 measured profile·allocation·Node의 Ready/unbound
executor가 있으면 검증된 scheduler slot의 ownerless executor 또는 정확히 같은
Workload owner UID에 한해 그 physical ID를 재사용하고 중복 prepare를 추가하지
않는다. 빈 routing bindings로 ownerlessness를 추정하지 않고 Runtime의 기존 owner
fence와 관측된 slot identity를 snapshot에 보존한다. Route 동작은 candidate 순번이나
rolling witness 문자열 대신 physical/logical target ID·UID별 weight와 소비되는
completion bound/capacity/batch/wait/deadline 조건으로 식별한다.
complete 표시만으로 동작을 재사용하지 않는다. 현재 snapshot에서 prepare의 Ready
identity/state, route의 실제 분배·대상 조건, stop의 실제 중지·분배 차단, drain의
실제 중지·분배 차단과 outstanding 부재, remove의 실제 부재·drain, power의 실제 Node 상태를
확인하며 효과가 사라졌으면 새로운 실행 가능한 동작을 생성한다. 검증된 run으로 다음
stop→drain→remove 의존성을 진행하며, 실제 target·소비 수치 변경은 다시 적용한다.
새 policy 수치 적용 중에도 이미 traffic이 옮겨진 이전 source의 정리는 굶기지 않는다.
outstanding은 native 완료 기록이 올 때까지 남는다. native 교환이 완료 증거 없이 끝났고(502, 504, 취소)
그 요청의 native 진행 기록이 하나도 없으면, 교환이 끝난 뒤 시작한 native state 읽기에 그 요청이 없을 때
`backend_finished`(`NativeRequestAbsent`)로 끝낸다. native가 받지 않은 요청은 완료 기록을 내지 않으므로,
이 규칙이 없으면 drain이 끝나지 않는다(m4 r6).
단 gateway routing map 일치만으로 handoff를 인정하지 않는다. 기존 complete route의
Ready·profile·allocation·native policy ACK와 새 target의 typed completion proof 및
actual routing이 모두 일치할 때만 기존 handoff로 stop 의존성을 충족한다. 처음
handoff와 대상 변경은 원래 route 완료를 기다리고 accepted-context stop/drain/removal
검사는 그대로 유지한다.

break-before-make에서 routing/bindings가 잠시 비어도 기존 실행의 frozen
`Operation.Target`이 가진 UID cohort·replica 수·UID별 weight를 전환 의도의 근거로
사용한다. actual 점유·ownership·Ready는 계속 현재 snapshot에서만 가져온다.
실행 중 owner가 예약한 Node·Workload·Pod·physical 범위만 고정한다. 전역 계획기는
예약 밖의 현재 부하·상태로 새 후보를 평가하고 독립 DAG를 실행한다.
prepare는 cost의 측정 trial 최대 시간에 관측 창 하나를 더한 timeout으로 실행된다.
timeout은 `failed`/`ActionDeadlineExceeded`로 실행을 끝내지만 원격 효과가 미확인이면
예약은 유지한다. 이후 효과 확인 또는 reset의 실제 정리 proof 뒤에만 반환한다.
관측된 `TargetID`가 같은 실제 physical slot은 중복 prepare하지 않는다. 새 완료 proof와
소비 수치는 현재 capture를 사용하고, 원래 실행의 frozen target을 덮어쓰거나 사라진
proof를 복원하지 않는다.

#### 중간 배분

`SchedulerController.intermediateActions`는 최종 route의 의존성이 남아 있으면
`readySubset`으로 같은 목표의 양수 가중치 중 실제 snapshot과 native 상태가
모두 Ready인 목적지만 고른다. 목표 가중치 비율을 워크로드별 합계 1000으로
재정규화하며, 아직 목적지가 없는 워크로드는 현재 Ready 분배를 유지한다.
Stopped이거나 이번 tick에 stop할 수 있는 인스턴스에는 새 가중치를 주지 않는다.
이 route는 다른 prepare, power, drain의 완료와 무관하게 같은 owner의 예약 안에서
native policy ACK와 UID 한정 `Runtime.ApplySchedulerWorkloadsTarget` 경로로 실행한다.

중간 분배는 `<goal>-ready-<subset hash>` ID와 `Action.from=ready-subset`으로
구분하며, 기존 OnAction 기록의 시작/완료와 frozen `operation.target`,
`before.routing`/`actual.routing`을 보존한다. 부분집합은 최종 목표를 덮거나
최종 DAG 완료 조건에 들어가지 않는다. 후속 부분집합으로 이전 분배가
대체되어도 완료 효과 소실로 목표를 실패시키지 않는다. 최종 DAG와 예약의 수명은
owner별로 유지하며 다른 Workload의 새 판단을 막지 않는다.
기존 요청의 실행 계약·drain과 요청별 지연/SLO admission은 유지한다. 실제 반영은 Ready를 캡처한 제어 tick이며
이 경로가 전체 부하의 중간 용량 충족을 보장하지는 않는다.

#### 분배 적용 증거

route와 source release는 공통 Node·Workload·실물 예약으로 충돌을 검사한다.
같은 owner의 의존성은 native ACK까지 유지하지만 독립 범위는 동시에 진행한다.
진행 중 route는 자신의 frozen target을 표시하고 running/native noncancelable 작업은 끝날 때까지 둔다.
자율 제어 대상이 아닌 Workload의 `staticRouting`도 같은 공통 예약 검사를 사용하며
같은 관측 기록에 sourceConfigRV·대기 이유·actual readback을 남긴다.
route의 ConfigMap·annotation 저장은 desired 진행으로, `Actual.Native`에는 정확한
native ACK만 남긴다. 뒤 인스턴스의 적용이 실패하면 이미 받은 ACK와 실제 routing을
보존해 `partial`로 기록하며, 미확인 인스턴스를 적용 완료로 채우지 않는다.
runtime에 적용된 routing은 각 인스턴스 Pod의 `edge.dacs.io/serving-routing` annotation에
Workload UID별 가중치로 기록한다. 기록하는 값은 판단이 선언한 분배이고, 이미 받은 요청을 처리하려고
memory에 남긴 이전 route는 기록하지 않는다. prepare는 스케줄러 Pod를 `{}`로, owner가 있는 수동 Pod를
owner UID의 가중치로 만든다. route는 모든 인스턴스의 값을, stop과 drain은 해당 인스턴스의 값을
갱신한다. 자율 제어 중 적용된 routing은 인스턴스의 정적 분배가 되어 자율 제어를 끈 뒤에도 유지된다.
ChillServer 재시작은 이 값을 다시 읽어 마지막 분배를 그대로 복구하므로 꺼진 Workload도 마지막
구성을 유지한다. 서빙 source는 이 annotation이 있고 Workload를 하나 이하로 가리키는 Pod다.

비교 기간은 판단마다 하나인 `T = max(comparisonPeriodNanos, 최장 route 전 사슬 + 최장 route 뒤 remove)`이고 keep과 모든 계획에
같다. 사슬은 동작마다 측정 trial의 최대 시간으로 재므로 모든 계획 동작이 T 안에 끝나고, 비교는 동작 에너지 전체와
전환 뒤 구성을 함께 담는다. 노드 계획의 [0, T] 노드 에너지는 `schedulerBaseline`의 적재 상태 전력과 원자 동작의
평균 wall joules로 계산한다. 원자 동작 구간의 wall joules는 그 구간의 baseline 소비를 **대체**하고,
`target-baseline-delta` 비용은 차감한 전력에 시간을 곱해 더한 wall joules로 쓴다. 요청 에너지는 profile의 입력당
incremental energy를 route 시각에서 나눠 더한다. 비교 집합(`search.comparedNodes`) 밖의 노드는 모든 계획에 같아
소거한다. 식은 [용량표 계획기 4장](analytic-planner.md#4-노드-계획과-탐색)이 원본이다.

선택 순서는 [3장](analytic-planner.md#3-판정과-순서)이 정한다. 켜진 Workload마다 최종 충족(`κ_new ≥ λ`)과 전환 중
충족(`λ ≤ κ_ret` 또는 `W ≤ L − m`)을 정확한 κ 합으로 판정하고, 모든 Workload에서 둘 다 충족하는 해 중 에너지가 작은 것,
keep, 논리 구성 key 순으로 고른다. 그런 해가 없으면 최대 초과량이 작은 해, 에너지 순으로 고르고 상태는 `Overload`,
사유는 `NoPlanMeetsSLO`다. 켜진 Workload는 부하가 0이어도 인스턴스가 하나 이상 있어야 최종 충족이다. 해는 keep과 모든
노드를 비운 집합 두 출발점에서 노드 2개, 이어서 3개 이하의 계획을 바꾸는 이웃 탐색으로 찾는다. 동시 평가 수는
`planSearchWorkers`이고 탐색은 제어 주기의 절반 안에 끝난다. 고른 해의 nominal 이득이 operation/선택 baseline의
조건부 B 민감도 문턱 이하이면 keep이다. 공유 immutable 근거는 부호 상쇄하며 profile 통계오차와 BEO idle fallback CI는
이 문턱 밖에 남는다([4장](analytic-planner.md#4-노드-계획과-탐색), [14장](analytic-planner.md#14-구현-규칙)).
snapshot의 목록은 정렬해 쓰므로 판단은 입력 순서와 무관하다.

router 가중치는 고른 해의 인스턴스 κ에 입력당 에너지가 작은 인스턴스부터 λ를 채운 배정을 합 1,000의 정수로 반올림한
값이다. 반올림 뒤 한 인스턴스의 몫이 κ를 넘고 옮길 곳이 없으면 그 해는 최종 미충족이고 사유 `RouteWeightExceedsCapacity`가
붙는다. keep도 같은 규칙으로 가중치를 다시 정하고, 분배만 바뀌면 target은 route 동작 하나다.

판단 상태는 `Feasible`, `Overload`, `Keep`(지금 구성과 분배를 그대로 두는 충족 판단과 진행 중 전환을 이어 가는 판단),
`Pending`(snapshot 검증 사유), `CapacityPending`이다. keep과 target은 `expectedJoules`와 `worstExceedance`를
담는다. `expectedJoules`는 고른 해를 정확한 배정으로 다시 계산한 [0, T]의 분석 에너지이고 비교 집합 노드만 더한다.
초과량이 정해지지 않으면 `worstExceedance`는 float 최댓값이다. `decision.capacity`는 비교 기간 `periodNanos`, 고른 route
시각 `routeNanos`, 탐색 범위 `search`(`neighborhood`, `evaluated`, `durationNanos`, `budgetCut`), Workload별 `workloads[]`(`uid`, `rate`, `queued`, `kappaNew`, `kappaRetained`, `backlog`,
`waitSeconds`, `drainSeconds`, `finalExceedance`, `transitionExceedance`, `weights`, `rows`)와 모든 노드 계획 `plans[]`
(`node`, `key`, `eligible`, `reason`, `kappa`, `kappaRetained`, `preNanos`, `postNanos`, `joules`)를 담는다. 값이 정해지지
않는 대기, 비우는 시간, 초과량 필드는 기록에서 빠진다. `search.examined`는 만든 노드 계획 수이고, `search.comparedNodes`는
keep 밖의 적격 계획이 있는 노드다.

실제 요청 완료와 whole-POOL 전력 기록을 같은 관측 구간, UID, decision에 연결하여 분석자가 예측과 실측 차이를 계산한다.
자율 제어는 `settings.autonomousWorkloads`에 나열한 Workload UID에만 적용한다.

### 5.4 자원 장부 — SCH-50~52, AC-37~39

**2026-10-26 설계 및 구현 경계.** Pod UID 장부와 runtime/snapshot 투영은 구현되었다.
① 실물 장부 → ② 영향 범위 예약 → ③ 예약 밖 재계획 순서로 한 묶음을 적용한다.
전역 `InProgressTarget`/`TransitionInProgress`, Node 단위 guard와 global route
대기는 제거되었다. 현재 구현과 API는 아래 장부·owner별 예약 계약을 사용한다.
전역 계획기는 하나를 유지한다. 새 서비스·저장소·CRD·범용 workflow 엔진은 없다.
④ 두 박자 제어(안정화·예비 용량·고정 H)는 승인 질문만 남기며 여기서 구현하지 않는다.
⑤ 정책 ConfigMap 단일화와 finalizer로 뒤처리 책임을 옮기는 작업은 E5 뒤다.

근거는 운영 자산의 `intent/structure-redesign-20261026.md` 안 **나**와
`audit-20261024/structure-20261026/`의 `StructTruth.md`, `StructFailure.md`,
`StructControl.md`, `StructActions2.md`, `clusters.md` K1~K5 및 `Research-K1~K5.md`다.
운영 자산 루트는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy`다.
Research의 권고 전체를 이번 범위로 가져오지 않는다. 증거 저장소 복구, 전력 관측 프로토콜,
부하/κ 수식 변경은 이 세 요구사항의 완료 조건으로 섞지 않는다.

#### ① 실물 장부와 관측의 분리 — SCH-50

**`kube/observer.go`의 기존 `o.sources`를 유일한 실물 장부로 승격한다.**
현재 physical ID 키의 종료 추적 자료를 **Pod UID 키**로 이관한다. Runtime의
`instances`는 요청 실행·바인딩·배분을 위한 투영, `SchedulerSnapshot.Instances`는
한 판단에 복사한 장부 투영이다. 셋을 독립 writer/registry로 두지 않는다.
기존 `o.verified`는 짧은 readiness 검증 보조값일 뿐 존재·할당의 권위가 아니다.

| 장부 항목 | 의미와 권위 |
|---|---|
| Pod identity | namespace/name + 불변 Pod UID. 컨테이너가 아직 없어도 항목을 만든다. Node는 Name과 UID를 함께 고정한다. |
| 물리 ID | 컨테이너가 생긴 뒤 기존 `Pod UID/container ID`. 생성 전에는 비어 있다. 이전 컨테이너의 종료 추적도 같은 Pod 항목 안에 보존하고 다른 executor의 성공 관측으로 덮지 않는다. |
| slot | 기존 `ServingSlotAnnotation`/`TargetID`. 생성·계획 주소이지 실행원 신분이나 유일성 proof가 아니다. 같은 slot의 여러 Pod는 모두 남긴다. |
| 할당 | Pod의 실제 request/limit, 준비 계약의 CPU affinity/GPU·threads·profile binding 및 마지막 검증한 실제 native 할당. 준비 중에도 예약된 할당을 점유로 센다. 둘의 불일치/미확인은 해당 Node의 변경 자격을 닫지, 점유를 0으로 만들지 않는다. |
| owner·배분 | 기존 owner reference를 해석한 Workload UID와 runtime의 현재 binding/routing을 구별한다. route가 비었다고 owner를 지우지 않는다. |
| 최근 관측 | 마지막 성공 native state·실제 applied DecisionID·Ready/Stopped·요청 목록, 그 조회 시작 시각/마지막 성공 확인 시각, 최근 시도 시각과 실패 사유. 실패는 성공값과 성공 시각을 갱신하지 않는다. |
| 수명 증거 | Pod 생성/삭제 표식, 정확한 컨테이너 종료 확인, 기존 source finalizer 및 제거 결과. 실행 종료·자원 반환·API 삭제/뒤처리 완료를 서로 대용하지 않는다. |

필드는 개념 계약이다. 기존 `nativeSource`, `InstanceState`, `SchedulerInstance`에 필요한
정보만 더하고 중복된 관측 enum/정책 DSL/새 영속 이력 저장소를 만들지 않는다.
Pod UID와 물리 ID를 별도로 운반하므로 init 실패 Pod도 장부에서 사라지지 않는다.
`RestartPolicyNever` 및 정확 physical ID 우선/유일 alias 해석은 보존한다.
slot 해석이 필요한 prepare 완료에서 한 번 정확 신분으로 해결한 뒤 mutation은 그 신분에
고정한다. 예전 UID의 허가를 같은 이름·slot으로 재생성한 Pod에 넘기지 않는다.

**생성·삭제는 Pod 수명과 기존 finalizer 경계만 따른다.**

1. Create 반환/Pod Added·Modified 사건과 최초·watch 복구의 성공한 전체 Pod LIST로
   UID 항목을 발견·복구한다. native probe/profile/Service 검증 전에도 기록한다.
   create 전 의도는 ②의 예약이지 존재하는 Pod를 가장한 장부 항목이 아니다.
2. Pod watch와 기존 controller-runtime 재조정이 수명·종료 상태를 갱신한다.
   terminal/terminating Pod를 steady Ready 목록에서 제외해도 장부에서는 제외하지 않는다.
   relabel·owner 해석 실패·native/Service/LIST/log 실패·ACK pending은 항목 삭제 사유가 아니다.
   watch가 Ready 해제나 삭제 시작을 확인하면 runtime뿐 아니라 장부의 dispatch 자격도
   닫는다. 다른 source의 완료 게시가 성공 재검증 없이 해당 항목을 다시 Ready로 만들지 않는다.
3. 동일 UID/container의 실제 종료를 확인하고, 기존 finalizer/제거 절차의 필요한 정리와
   해당 UID의 삭제를 확인한 뒤에만 항목을 퇴역시킨다. 실행원이 한 번도 생기지 않은
   init 실패 Pod는 `kube/observer.go:podTerminated`의 기존 판정(terminal Pod이며
   init/main container에 실행 중·종료 불명 실행원이 없음)을 종료 증거로 사용한다.
   container ID 부재만으로 종료를 추정하지 않는다. `DeletionTimestamp`, 실패한 GET,
   필터 목록 누락이나 강제 삭제 사건 하나는 실제 종료 proof가 아니다.
   먼저 종료를 확인한 뒤 watch 삭제 사건을 놓친 경우는 정확 UID의 성공한 재조회로 수렴한다.
   이를 위해 `kube/manual_reset.go:resetOwnedTargets`의 container 미생성 거절(:158-160)과
   `kube/operations.go:removeInstance`의 physical ID 필수 경계(:638-642)를 함께 이관한다.
   reset/remove의 내부 대상은 Pod UID와 선택적 physical ID를 운반하며, 실행원이 있으면
   기존 exact container 종료·drain 검사를, 없으면 같은 UID의 `podTerminated` 검사를 쓴다.
   fake container ID나 slot fallback을 만들지 않고 기존 finalizer/정리 순서를 유지한다.
4. 관측 실패 시 항목·owner·할당·마지막 성공 상태는 보존하지만 현재 dispatch 자격은
   별도 freshness/identity 검사를 통과해야 한다. 늦은 probe는 먼저 확인한 종료/UID 변경을
   되돌리지 못한다. 전역 snapshot 시각으로 각 source의 성공 시각을 새로 찍지 않는다.
5. 재시작은 기존 Pod/finalizer/owner/정책 annotation에서 장부를 복원한 뒤 새 변경을
   허가한다. `c.runs`의 메모리 수명이 원격 작업의 취소/부재를 보장한다고 주장하지 않는다.
   복원할 identity/효과가 불명인 범위는 재관측까지 고정한다.

**소비자 전환표 — 아래는 모두 이관 대상이며 Ready 목록 fallback을 남기지 않는다.**
파일 경로는 별도 표시가 없으면 `internal/serving/` 아래다.

| 파일:함수 | 장부 방식의 기준 |
|---|---|
| `kube/observer.go:refreshLocked`, `restoreSources`, `probeSource`, `reconcileSources`, `watchPods`, `refreshFailure`, `observeTermination` | Pod 수명 갱신과 관측 갱신을 분리한다. 실패 때 `InstallInstances(nil)`로 재고를 비우지 않는다. 완료한 source 결과를 identity 확인 후 짧게 게시하고 느린 probe I/O가 다른 source 게시/장부 복사를 붙들지 않게 한다. 성공한 native 읽기는 Ready/metadata 검증 실패도 실제 state와 원래 조회 시작 시각을 남긴다. |
| `runtime.go:InstallInstances`, `Instances`, `BuildPools` | 전체 장부 투영을 한 집계자가 설치한다. 누락된 Ready 응답을 삭제로 해석하는 `!seen && outstanding==0` 제거를 없앤다. 요청/route 상태는 runtime 소유 그대로이며, 실행원 없는 Pod도 POOL 점유 투영에서 빠지지 않는다. |
| `kube/scheduler.go:capture`, `captureNodePods` | runtime의 `Native != nil` 분기로 서빙 실물 포함 여부를 정하지 않고 장부 항목 전부를 복사한다. Pod별 관측 실패를 이유로 붙여 해당 Node만 고정한다. CHILL 항목과 non-serving request를 UID로 구별해 중복 합산하지 않는다. non-serving 실제 예산 및 Node UID 검증은 유지한다. |
| `scheduler_plan.go:prepareTargets`, `runningPreparation`, `schedulerSamePhysicalTarget` | 동일 slot/할당의 준비 중·관측 불명·종료 미확인 Pod와 예약을 먼저 찾는다. 관측이 안 된 기존 항목은 “없어서 prepare”가 아니라 보류다. 여러 실물을 하나로 축약하지 않는다. |
| `scheduler_plan.go:preparationFits`, `freePreparationResources`, `retirePreparationBlocker` | 기존 실물+준비 예약의 전체 할당과 non-serving/system 예약을 센다. 중복 반영은 UID/예약 owner로 제거한다. stop을 계획했다는 이유로 이미 CPU가 반환됐다고 보지 않고 실제 remove/반환을 새 prepare의 의존성으로 둔다. |
| `scheduler_plan.go:changeNodes`, `retireOld`; `kube/observer.go:detachNodeLocked` | Node의 모든 미종료 실물·준비 잔재를 대상으로 stop/drain/remove 또는 기존 종료 proof를 요구한다. 상태 불명 대상은 power 선행 조건을 충족하지 못한다. |
| `scheduler_plan.go:completedEffectPresent`; `kube/scheduler.go:actionEffectPresent`, `failGoalLocked`, `reconcileSnapshot` | remove/stop 완료는 목록 부재가 아니라 정확 UID/container 종료·route 차단·outstanding 부재와 기존 제거 결과로 확인한다. 항목 퇴역 뒤에도 필요한 증거는 기존 `OperationResult`에서 읽고 별도 tombstone 저장소는 만들지 않는다. |
| `scheduler_plan.go:routeEffectPresent`, `schedulerSnapshotTargetMatches`; `kube/scheduler.go:routingTargetPresent`, `schedulerTargetReady`, `completedHandoffPresent` | 효과 확인을 확인됨/확정 반증/현재 확인 불능으로 구별한다. 확인 불능은 기존 run/예약을 유지하며 재관측하고 effect-lost/route 재전송을 만들지 않는다. pending은 존재 proof일 수 있으나 새 정책 ACK proof는 아니다. |
| `scheduler_capacity.go:newCapacityPlanner`의 `w.current`, `nodePlans`, `fixed` | 실물 수(종료 미확인·준비 중 포함), 실제 서빙 가능 수/κ, 전환 의도 수를 구별한다. replica/전체 instance 상한과 점유는 장부+create 전 예약을 중복 없이 세며 stale Ready에 κ를 부여하지 않는다. 관측 실패로 current가 0이 되어 보충 prepare를 만들지 않는다. |
| `observation_log_power.go:operatingMembership`, `operatingActiveJoules`; `runtime.go:BuildPools` | active POOL 귀속을 `Ready` 필터가 아니라 장부의 미반환 물리 점유·owner/현재 배분으로 판정한다. 준비·drain/정리 중 에너지도 귀속하고 단순 관측 실패로 구간을 끊지 않는다. 귀속 자체가 불명하면 이유와 미확인 구간을 남겨 active 합계를 완전하다고 하지 않는다. whole-POOL raw meter coverage·적분 규칙은 유지하며 stale 관측을 0 W로 만들지 않는다. |

#### ② 범위 예약과 단일 허가 경로 — SCH-51

기존 `c.runs`/`actionRun`에 **예약 owner(기존 operation ID/DecisionID), Node 집합,
Workload UID 집합, 정확한 Pod UID/물리 ID 집합**을 기록한다. 보수적으로 Node 전체를
예약하며 CPU별 세밀 락은 만들지 않는다. create 전에는 Node·Workload·slot과 예정
할당을 예약하고, Pod 생성 뒤 그 UID/물리 ID를 같은 owner에 결속한다.
Pod별 관측 메모와 run 예약은 같은 장부 snapshot에서 합쳐 보되 예약용 별도 저장소는 없다.

공통 허가·충돌·등록·해제 책임은 기존 `kube/admission.go`에 모은다.
`tickMu` 안에서 현재 장부/runtime/Node identity를 읽고 **영향집합 산정 → 전체 충돌 검사
→ 한 번에 예약 등록 → 현재 안전 조건 재확인 → 실행 시작**을 수행한다.
실패하면 아무 키도 부분 예약하지 않거나 물리 효과 전 등록을 되돌린다.
`c.mu`/runtime 잠금은 짧은 게시·조건 전이에만 사용한다. observer의 기존 Kubernetes
수명·finalizer 직렬화는 유지하며 신규 watch UID는 현재 Pod GET과 게시를 직렬화해 늦은
사건의 퇴역 UID 복원을 막는다. 병렬 native probe I/O는 observer 잠금 밖에서 실행한다.
runtime route/outstanding 조건은 실제 변경 직전 runtime 잠금 안에서도 검사하고,
Pod mutation은 정확 UID/기존 optimistic-lock 조건을 쓴다. 저장된
`c.state.Snapshot`의 과거 허가를 파괴적 동작에 재사용하지 않는다.

| 작업/현재 진입점 | 예약해야 하는 범위와 이관 |
|---|---|
| 자동 `kube/scheduler.go:launch`, `scheduledOperation` | 새 변경 DAG의 실제 영향 Node·Workload·실물 전체를 첫 effect 전에 확보한다. `Operation.WorkloadUIDs`에 모든 autonomous UID를 넣지 말고 실제 바꾸는 UID만 전달한다. |
| 수동 `measurement_sequence.go:ExecuteManual`, `manualOperationLocked`, `admitNodeLocked`, `manualRemovalAllowed` | prepare/remove도 같은 허가 경로를 쓴다. remove는 현재 양수 route/outstanding/소유·UID를 재검사하며, 상위 허가 없이 원자 실행기를 우회 호출하지 않는다. |
| 배분 `static_routing.go:manualRoutingOperationLocked`, `validateManualRoutingNodes`, `executeManualRouting`; `operations.go:ApplyTarget`, `persistRouting` | 철회 source와 새 destination의 Node/실물 **합집합**과 변경 Workload를 잡는다. 정책 ACK·runtime 적용·분배 readback까지 유지한다. 공유 설정 쓰기는 기존 resourceVersion 조건으로 직렬 승인하고 타 UID 선언을 보존한다. |
| reset `manual_reset.go:planManualResetLocked`, `reserveResetNodes`, `releaseResetNodes`, `executeManualReset` | reset 전용 Node별 가짜 run을 공통 집합 예약 하나로 흡수한다. 선택 Workload의 같은 slot 모든 실물과 반환할 Node를 잡고 disable→unroute→drain→remove→release→power 전 구간을 같은 owner로 실행한다. 자식 원자 동작은 자기 예약과 충돌하지 않는다. |
| 측정 `admission.go:Admit`, `rootRefusal`, `startNodeRun`, `startHandBack`; `occupancy.go:occupancyRefusal`, `startOccupancy` | measurement Node뿐 아니라 대상 Workload·실물, 실제 사용하는 load Node/공유 endpoint 범위도 잡는다. 같은 Workload의 다른 Node 측정도 같은 승인 주기부터 충돌한다. 외부 측정 root의 기존 taint/holder를 같은 예약 근거로 복원하며 종료·load Job 제거·hand-back까지 소유한다. |

Node/Workload/실물 중 **하나라도 다른 owner와 겹치면** 거절/대기한다.
같은 owner의 DAG 단계만 내부 의존성 순서로 실행한다. 승인 뒤 범위가 넓어지면 키를
하나씩 더 잡으며 진행하지 않고 effect 전에 재승인한다. 같은 이름의 새 Node/Pod는
옛 UID 예약을 물려받지 않는다. 정확한 독립 범위를 증명하지 못하면 관련 범위를 넓히되
“한 route이므로 모든 Workload”라는 전역 충돌 규칙으로 되돌리지 않는다.
물리 identity·route weight가 같아도 MaxBatch/MaxWait가 바뀐 Workload는 변경 UID에 포함해
동일 예약·UID 한정 적용 경로에서 native policy ACK와 runtime 반영까지 수행한다.

**해제는 goroutine 종료와 같지 않다.** effect 전 거절은 즉시 멱등 해제한다.
reset 첫 disable-autonomy의 ConfigMap conflict/확정 거절과 쓰기 전 실패는 이에 포함한다.
첫 쓰기의 timeout은 효과 불명이고, 앞 단계가 성공한 뒤의 거절도 전체 예약 반환 근거가 아니다.
시작 뒤 성공은 필요한 실제 효과/후속 정리 proof를 확인한 범위만 해제하며, DAG의
다음 단계가 쓸 자원은 단계 사이에도 계속 예약한다. 실패·취소·timeout·기록 실패 뒤
원격 효과가 불명하면 run의 실행 상태만 종료하고 해당 예약은 확인 대기로 남긴다.
기존 Pod/finalizer/Node 상태를 재관측해 실제 현재 상태와 남은 책임을 확인한 뒤 해제한다.
관측 결과 불명이라는 이유로 자원을 비우거나 전역 모든 작업을 멈추지 않는다.
이는 기존 뒤처리의 소유 범위를 명시하는 것이지 ⑤의 finalizer 책임 이동을 선행하는 것이 아니다.

#### ③ 예약 범위만 고정한 단일 계획기 — SCH-52

`scheduler_capacity.go:EvaluateScheduler`의 `transition` 조기 반환을 제거한다.
진입 시 장부+예약을 먼저 복사하고, 예약 Node의 실물·할당·기존 배분과 해당 owner의
진행 중 변경 의도를 **고정 제약**으로 둔 채 같은 전역 capacity planner를 실행한다.
불확실/계산 대기 Workload도 점유·현재 route를 고정하고 독립 후보를 평가한다.
준비 중 용량을 현재 가용 κ로 더하거나, 고정된 Workload의 관측 실패를 새 여유 자원으로
계산하지 않는다. 전체 자원 상한/공유 Node 비용과 SLO·에너지 선택 규칙은 유지한다.
확정된 CPU 독점 위반 등 기존 실제 공통 안전 검증을 이번 변경으로 면제하지 않는다.
`InstanceUnavailable` Node의 미준비 실물에는 실행 후보의 queue/batch/profile 관측 검증을
요구하지 않는다. Capacity/MaxBatch가 아직 0이어도 할당은 공통 Node 예산 검증에 남긴다.

| 파일:함수 | 새 판단·실행 계약 |
|---|---|
| `scheduler_capacity.go:EvaluateScheduler`, `newCapacityPlanner`, `pendingWorkloads`, `fixed`, `nodePlans` | 예약된 Node에는 고정 계획만 두고 나머지는 원래 후보를 만든다. κ가 없는 UID는 이유를 가진 고정 대상으로 보존하며 다른 UID의 판정을 막지 않는다. 고정 영역의 κ/에너지가 불명이면 전체 충족·전체 에너지도 미확인으로 표시하고 독립 변경의 증명과 구별한다. |
| `scheduler_plan.go:schedulerCurrentTarget`, `schedulerPlan`, `add`, `addRunningPrerequisites`, `prepareTargets`, `changeNodes`, `retireOld` | 새 완전 목표에는 고정 부분도 포함한다. 실행 중 run의 frozen target/ID·미완료 DAG는 변경하지 않고 새 독립 변경 DAG만 붙인다. 완료 action 재사용은 ①의 실제 효과 판정으로 한다. 같은 Node 및 route/stop의 전역 종류 비교를 ②의 owner/영향집합 의존성으로 교체한다. |
| `kube/scheduler.go:reconcileSnapshot`, `failGoalLocked`, `activeTargetLocked`, `launch`, `conflictingActionRunning`, `dependenciesReady` | 가장 오래된 global goal 하나를 전체 진실로 고르지 않는다. 각 run의 기존 frozen target과 예약을 재조정하고 실패/효과 불명도 그 범위에만 반영한다. `launch`는 공통 허가 경로로 비충돌 DAG를 등록하며 다른 목표 발행이 실행 중 run을 취소·덮어쓰지 않는다. |
| `kube/operations.go:ApplyTarget`, `persistRouting`; `runtime_scheduler.go:ApplySchedulerWorkloadsTarget`, `applySchedulerTargetLocked`, `resolveSchedulerRoutingLocked`, `validateSchedulerRoutingLocked` | 변경 UID만 policy/route/annotation에 적용하고 타 UID override·accepted context를 보존한다. 현재 선택적 적용도 내부에서 전체 target readiness를 검사하므로 UID 인자만 좁히면 끝난 것이 아니다. 보존 UID는 현재 분배와 동일함을, 변경 UID는 현재 identity/Ready/owner/정책을 검증하도록 같은 경로를 고친다. 공유 ConfigMap optimistic lock은 유지한다. |

새 snapshot/decision에는 고정 Node·Workload·실물 집합, 예약 owner와 대기 이유를
기존 action/operation 관측과 연결한다. `Keep`는 “새 변경 없음”이지 “누군가 끝날 때까지
전역 판단 생략”이 아니다. 공통 설정 무효·전체 API/journal 장애 등 실제 전역 차단은
그 원인으로 남기며 `TransitionInProgress`로 뭉개지 않는다.
예약 밖에도 후보가 없으면 정상 keep이며, 긴 run은 독립 판단의 생략 사유가 아니다.

**`34b9a40d` 중간 분배는 필요한 실행 동작으로 유지한다.**
그 커밋의 `intermediateActions`/`readySubset`은 한 목표 내부에서 일부 Ready 목적지에
먼저 traffic을 보내는 기능이다. ③은 다른 예약 범위의 새 판단을 여는 기능이므로
서로 대체하지 않는다. 같은 Workload의 예약은 계속 고정되어 있어, 전역 장벽만 없애고
중간 분배까지 삭제하면 해당 전환의 부분 Ready 활용을 잃는다.
따라서 중간 route를 **그 전환 owner의 같은 예약 안의 자식 동작**으로 실행하고
최종 DAG 완료/다른 Workload의 재계획과 분리한다. `<goal>-ready-<hash>`·frozen subset·
native ACK·actual routing 기록과 stopped 대상 제외는 보존한다.
최종 목표를 덮거나 다른 owner의 예약에 끼어들 수 없으며, 중간 route 완료를 전역
`TransitionInProgress` 유지 근거로 쓰지 않는다. 기존 비율 재정규화의 κ/SLO 한계는
여전하며 이를 새 안전 용량 보장으로 부르지 않는다. 그 판단 규칙 개선은 ④와 별도 승인 대상이다.

#### 삭제·흡수 대상과 작은 구현 커밋

| 없앨 코드/관례 | 대체·보존 경계 |
|---|---|
| `scheduler.go:SchedulerSnapshot.InProgressTarget`, `scheduler_capacity.go:schedulerTransitionInProgress` 및 transition 조기 반환 | 장부/예약 고정 입력. API·compact·로그·fixture의 해당 필드/사유 소비자도 함께 이관하고 호환 alias를 남기지 않는다. 과거 원시 기록은 다시 쓰지 않는다. |
| `scheduler_replay.go:schedulerReplay`, `schedulerReplayState.initializeInstances`의 global 목표/배분 적용 분기 | 같은 장부·예약 고정 입력을 replay에 전달한다. production만 장벽을 없애고 replay/test helper는 전역 목표를 재현하는 이중 의미를 남기지 않는다. |
| `observation_log_scheduler.go` snapshot의 목표 dictionary projection, `httpapi/scheduler_presentation.go:schedulerReadSnapshot`/`schedulerReadEvidence.snapshot`, 관련 scheduler·POOL·HTTP fixture | `inProgressTarget` 대신 예약 범위/owner와 run별 frozen target을 투영한다. 구현 cutover 때 `analytic-planner.md`의 진행 중 전환 규칙과 API/관측 설명도 함께 갱신한다. |
| `kube/scheduler.go:SchedulerController.goal`, `activeTargetLocked`의 global 선택, `reconcileSnapshot`/`failGoalLocked`의 전체 목표 폐기·전역 대기 | 각 기존 run의 frozen `Operation.Target`과 영향 범위 단위 재조정. 필요한 effect 검사와 terminal 우선 규칙은 유지한다. |
| `Operation.Node` 한 개를 작업의 전체 소유권으로 해석하는 규칙, `nodeBusy`/`conflictingActionRunning`의 별도 충돌 검사 | 공통 예약 집합. 단일 Node 원자 동작의 **명령 목적지** `Operation.Node`/`Action.Node`는 유지하되 권한·busy 판정의 대용으로 쓰지 않는다. |
| `manual_reset.go:reserveResetNodes`/`releaseResetNodes` 특례, 수동 route/reset/occupancy의 종류별 전역 상호배제 | 같은 집합을 승인·등록·해제하는 경로. Node taint, measurement holder 및 원래 reset 단계는 유지한다. |
| `6120aaa7`의 `instanceUnavailable`/`deferNode`, `prepareTargets`/`preparationFits`/`changeNodes`/remove-effect 및 `dependenciesReady`의 중복 임시 guard, transition 전용 이유 전파 | 장부의 점유/관측 품질과 공통 예약·실제 proof 검사로 흡수한 뒤 삭제한다. 준비/전력/제거 완료를 안전하게 막는 **행동**과 `InstanceUnavailable` 진단은 유지하며 guard만 먼저 지우지 않는다. |
| Ready 응답 누락을 runtime 삭제·remove/stop 성공·effect lost로 바꾸는 분기 | Pod/finalizer 수명 + 관측 불명 판정. `6545c800`의 ACK pending/실제 관측 보존은 장부 관측 계약으로 흡수하고 native ACK 검사는 유지한다. |
| slot별 단일 실물 가정 | `dc9c90bf`/`73f1ec79`의 exact 우선·중복 reset을 장부 전체 열거로 유지한다. 여러 Pod가 보이면 숨기지 않는다. |

**구현 커밋 경계**는 다음과 같다. 각 구현 커밋은 해당 회귀와 `go build ./...`,
`go vet ./...`, `make lint`가 통과 가능한 완결 경계로 나눈다. 선언만 추가하거나 호출자를
절반 이관한 상태를 커밋 완료로 삼지 않는다. source별 완료 게시를 위해 native probe는
병렬로 실행하고 각 완료 결과를 별도 Pod LIST로 검증한다. 느린 probe 동안 장부 잠금은 풀린다.

1. **Pod 장부 수명 cutover:** `o.sources` UID 키·항목과 watch/LIST/finalizer 경계를
   연결하고 기존 physical lookup 호출자·runtime 설치·snapshot/POOL 투영을 함께 이관한다.
   이 커밋부터 native 실패로 항목이 사라지지 않는다. 기존 전역 장벽은 아직 유지한다.
2. **장부 소비자 cutover:** prepare/budget/power/remove/effect/instance 수/active 에너지
   소비자를 위 표대로 바꾸고 확인 불능을 effect-lost와 분리한다. AC-37 및 AC-34~36의
   기존 동작이 이 단계의 회귀다. 장부로 대체된 6120aaa7 guard만 이때 제거한다.
   실제 Tick의 native 읽기 실패·ACK 대기와 UID 기반 init 실패 reset 회귀를 포함한다.
   `SchedulerRouting`은 Ready dispatch 목록 대신 보존된 선언을 읽으며, dispatch 자격은
   runtime의 Ready 검사로 별도 닫힌다. active 에너지는 stop/prepare의 미반환 점유를 센다.
3. **공통 범위 예약 cutover:** `c.runs` 범위와 공통 허가·해제를 만들면서 자동/수동/
   reset/측정/hand-back의 모든 진입점을 같은 커밋에서 이관한다. reset 전용 run과
   종류별 충돌 검사를 제거한다. 전역 목표 장벽은 아직 유지하고 AC-38을 통과시킨다.
   공통 예약은 `actionRun`에 Node UID/Workload UID/Pod UID/physical ID 집합과 owner를
   보관한다. 실행 상태와 예약 반환은 분리하며, 종료 후 Pod 부재·해당 Node의 전력
   비대기 재관측은 빈 범위의 반환 proof로 사용한다. reset은 부모 run 하나가 모든
   Node를 원자 예약하며 자식 remove는 그 부모 안에서 실행한다.
4. **UID 한정 적용·DAG 준비:** runtime 전체/선택적 검증을 하나의 경로에서 구분하고
   `scheduledOperation`·policy·persist/readback을 실제 변경 UID에 한정한다. 중간 route도
   같은 owner 예약의 자식으로 이관한다. 아직 장벽은 유지하므로 안전하게 독립 검증한다.
   선택적 runtime 검증은 변경 UID만 Ready/현재 owner와 대조하고, 입력에 함께 실린
   보존 UID는 현재 physical 분배와 동일한지만 검사한다. 보존 UID의 override·accepted
   request 문맥은 쓰지 않는다. 자동 route는 등록 때 실제 route/실행 설정 차이의 UID를
   고정하고 policy/Pod annotation/readback과 예약 proof에 같은 UID 집합을 쓴다.
5. **예약 밖 계획 활성화:** capacity 진입/고정 후보/κ pending 격리와 목표·DAG·launch를
   함께 바꿔 global `InProgressTarget`/`c.goal` 장벽을 삭제한다. 해당 API/기록/문서·테스트
   소비자도 동시에 이관한다. AC-39 및 앞 단계 전체 회귀 뒤 시연 3회차 진입 조건을 판정한다.

5번 cutover는 owner별 부모 DAG·UID 한정 적용, 예약/관측 불명/κ 대기 Workload 고정,
API·compact·기록·dashboard·자동화 소비자의 `reservations` 전환을 포함한다.
AC-38은 timeout/취소 실행 종료 후 예약 보존 및 Pod 부재·전력 비대기 재관측 후 반환을 검사한다.
AC-39는 slow-power/unconfirmed-source/pending-kappa 동안 독립 Workload의 Tick→prepare→route→native 요청 완료를 검사한다.

#### 회귀 설계와 검사 항목 — AC-37~39

아래는 **추가할 검사 설계이지 실행 결과가 아니다.** 기존 fake Kubernetes client,
native HTTP fixture와 production 함수 경로를 재사용하며 별도 장부 모형만 검증하지 않는다.

- **AC-37 / SCH-50 — 관측 실패가 실물을 지우지 않음.**
  `kube/observer_test.go`, `kube/scheduler_transition_test.go`, `scheduler_transition_test.go`,
  `scheduler_pools_test.go`, `observation_log_power_test.go`에서 동일 Pod A/slot S/CPU를
  정상 관측한 뒤 ACK 지연·native 실패·Service/LIST 실패·watch 재연결을 표로 주입한다.
  UID/물리 ID/owner/할당은 보존되어야 한다. 실제 읽기 실패 때만 마지막 성공 관측 시각을
  그대로 두고 실패 시도/이유를 갱신한다. ACK 지연은 성공한 native 관측이므로 이번 실제
  state와 원래 조회 시각으로 성공 관측 시각을 갱신하되 identity/할당을 보존하고,
  `PolicyAckPending`을 새 정책 적용 완료 proof로 사용하지 않아야 한다.
  prepare/Pod Create 0, fits의 점유 보존, 모든 실물의 power 선행 stop/drain,
  unknown remove/effect 완료 0·재전송 0, 수 상한·POOL 귀속·meter 구간 보존을 확인한다.
  stale 상태로 새 dispatch/κ를 허용해서는 안 된다. container 없는 init 실패 Pod와
  같은 slot 두 Pod도 각각 남겨 reset으로 제거한다. init 실패 Pod는 정확 UID에 대해
  `podTerminated`의 terminal·실행원 미생성 증거를 확인하며 physical ID를 요구하지 않는다.
  실행원이 있는 Pod의 실제 종료 또는 미생성 Pod의 이 증거와 finalizer·삭제/정리 proof
  뒤에만 항목/예약이 퇴역해야 한다. nonterminal/종료 불명 Pod는 거절되고, 늦은 probe가
  삭제를 되살리거나 새 UID를 대리하지 못해야 한다.
- **AC-38 / SCH-51 — 모든 작업이 같은 범위를 소유.**
  `kube/admission_test.go`, `kube/static_routing_test.go`, `kube/measurement_sequence_test.go`,
  `kube/reset_test.go`, `kube/occupancy_test.go`에서 수동/자동 route ACK를 멈춘다.
  옛 source와 새 destination 양쪽 Node의 remove/power/reset은 충돌하고, Node가 달라도
  같은 Workload의 occupancy는 한 승인 주기에서 하나만 시작해야 한다.
  Node·Workload·실물이 모두 다른 작업은 실제로 시작되어야 한다.
  예약 일부만 잡힌 실패 0, effect 전 거절의 멱등 해제, effect 후 timeout/취소의 불명
  예약 유지와 재관측 후 해제, reset 자식의 자기 충돌 없음/모든 중복 실물 정리를 확인한다.
  snapshot 이후 새 양수 route/outstanding을 넣은 수동 remove는 현재 경계에서 거절되어야 한다.
  같은 이름의 새 UID는 예전 승인으로 변경/삭제되지 않아야 한다.
- **AC-39 / SCH-52 — 긴 전환 중 비충돌 영역의 실제 진행.**
  `scheduler_capacity_test.go`, `scheduler_transition_test.go`,
  `kube/scheduler_transition_test.go`, `runtime_scheduler_test.go`에서 W1/N1의
  power→prepare/ACK를 여러 tick 미완료로 둔다. W2 수요를 늘리고 독립 N2/N3를 제공하면
  capacity 탐색·새 decision·prepare·W2 한정 route가 W1 완료 전에 실제 실행되어야 한다.
  W2 목적지의 실제 Ready를 관측한 tick을 기준으로 늦어도 다음 제어 주기 안에 분배에
  편입하고, W1이 아직 미완료인 동안 W2 요청이 그 정확한 physical ID에 assignment되어
  실제 native 실행되는지 요청·native 증거로 확인한다. route 선언/ACK만으로 통과하지 않는다.
  W2의 결정/적용이 W1의 frozen target/ID·점유·route·outstanding을 덮지 않고,
  중복 prepare/동작 취소가 없어야 한다. W1 자체의 정상 진행은 자기 owner의 DAG만 따른다.
  W1의 probe 불명/κ pending도 같은 형태로 반복하고, 보존 W1 readiness 불명이 W2의
  선택적 runtime 검증을 막지 않는지 확인한다. 같은 Node/Workload를 건드리는 후보는 거절된다.
  W1 내부에서는 기존 중간 Ready subset route가 자기 예약으로 실행되고 최종 DAG 완료로
  오인되지 않아야 한다. 실패/완료가 교차해도 terminal run이 stale snapshot보다 우선한다.

검수자는 AC-34~39와 함께 다음을 확인한다: Pod UID 없는 가짜 실물 없음;
물리 ID/slot·owner/routing·관측/존재의 혼동 없음; 미확인=0 용량/0 W/제거 성공 처리 없음;
모든 생산 mutation 진입점의 공통 예약 참여; 예약 owner와 반환 proof의 일치;
고정 영역을 뺀 것처럼 전체 SLO/에너지를 완전하다고 보고하지 않음;
전역 대기 필드/상수/consumer/문서의 clean cutover; 실제 전역 안전 gate 보존.
빌드·vet·focused 회귀 및 시연의 OnDecision/OnAction·Pod CRUD·native ACK·실제 routing
근거는 구현 담당자가 별도로 남긴다. 이 문서 갱신만으로 어느 AC도 통과 처리하지 않는다.


## 6. 실제 적용과 관리 연동

`serving/kube` 제어 구현이 measured target별 worker profile bundle과 작은 mutable policy config를
제공한다. 큰 raw 분포는 immutable store/volume에서 startup에 한 번 로드하고
request마다 복사하거나 ConfigMap에 반복 전송하는 대신 공유한다. actual native
metadata/progress에서 profile·policy 적용과 readiness를 확인한다.

전환 기록은 desired/start/progress/complete/failed/partial과 prerequisite를 가진다.
실제 준비 Pod, NodeActuation, routing 적용, drain·remove 완료를 확인하여
다음 동작을 시작한다. resource 변경 계획은 같은 기존 원자 동작을 조합하되 실제
동시 allocation 예산에 따라 prepare-first 또는 drain/remove-first DAG를 사용한다.
기존 독립 writer를 중복 생성하는 대신 NodeActuation을 생성해 sole host actuator를 사용한다.

부하·Workload 관리 상태가 바뀌면 같은 snapshot과 계획기로 재판단한다.
이미 수행된 결과는 실제 현재 상태이며, 진행 중 동일 동작은 재사용한다. cancel은
아직 시작 전/중단 가능한 동작에 적용하고 physical effect가 발생한 뒤에는 관측된
상태에서 다시 선택한다. 실패 Node·instance는 route에서 격리하고 실제 recovery
proof에 따라 재사용한다. 기존 관리 admission gate·accepted definition·공유 UID
보호·actual drain과 동일한 Workload 관리·요청 처리 구현을 사용한다.

평가 중 완료된 동작의 동일 ID·동일 계약은 live run의 terminal 결과가 stale snapshot의
running/cancel보다 우선한다. physical identity로 갱신된 목표에서 같은 prepare를 다시
구성·실행하지 않는다. 실제 effect가 소실되면 기존 reconciliation이 기록을 퇴역시키고
새 판단의 새 action identity로 준비하므로 logical slot을 영구 차단하는 규칙은 아니다.

### 중복 slot의 수동 reset — AC-36

`kube/manual_reset.go:132-179`의 `resetOwnedTargets`는 이미 Pod 목록에서 선택
Workload 소유의 **모든 정확한 `Pod UID/container ID`**를 모은다. 이 목록을 slot별
하나로 축약하지 않는다. `planManualResetLocked(:57-93)`의 Node 예약과
`executeManualReset(:223-282)`의 disable-autonomy→unroute→drain→remove→release→lower-power
순서를 그대로 쓰고, remove 직전 재열거(`:260-278`)로 소유 실물을 각각 제거한다.

현재 unroute가 호출하는 runtime 해석기는 exact ID와 TargetID alias를 한 번에
비교한다(`runtime_scheduler.go:233-244`). 따라서 `ID=A, TargetID=S`인 원본과
`ID=B, TargetID=A`인 중복 실물이 있으면 정확한 물리 A도 다중 slot 오류가 된다.
이를 **exact physical ID 우선, exact가 없을 때만 유일한 TargetID alias**로 해석하도록
바꾼다. 실제로 여러 실물이 같은 논리 alias를 가지면 계속 명시적으로 거절한다.
같은 선택 규칙을 `kube/scheduler.go:1173-1184`의 `schedulerPhysicalState`와
`kube/operations.go:1056-1068`의 `applyPolicy`에도 적용해 목록 순서에 따른 첫 Pod 선택을 없앤다.
정확한 A 선택은 B를 숨기는 중복 제거가 아니며 reset은 A와 B 모두 처리해야 한다.

소유는 routing 비어 있음/slot 일치로 추정하지 않고 기존 Workload owner와 바인딩 검사를
유지한다. 다른 소유분·measurement Node·다른 Node UID는 기존 보호를 따른다.
각 실물은 native outstanding/drain과 실제 container 종료·Pod/Service/config 제거 proof를
기존 원자 remove(`operations.go:638-746`)로 확인한다. 삭제 후 release/power에서 실패하면
다음 명시적 reset이 현 상태를 다시 열거하며, 완료 후 재호출은 성공하고 불필요한 Pod 쓰기가 없다.


### 초기화와 판단 발행 경계

시작 시 profile catalog와 scheduler 설정을 Observer에 제공하지만 생성자는 routing을
freeze하지 않는다. 첫 `observer.Refresh`·`manager.Initialize`가 성공하여 ready instance와
Workload 정의를 복원한 뒤 `settings.autonomousWorkloads`가 비어 있지 않을 때
`EnableSchedulerRouting`으로 실제 분포를 freeze한다. 첫 Tick·HTTPS serving보다 앞선
경계이며 live 설정에서 첫 자율 Workload가 추가될 때도 같은 freeze를 적용한다.

ChillServer의 기존 `--request-timeout`(기본 30초)은 한 control 호출의 refresh와 계획기 평가에 같은 `callCtx`로
적용된다. 노드 계획 생성과 route 시각별 DP는 취소를 확인하고 판단 대신 context 오류를 반환한다. 용량표 행은 Tick 밖에서
controller context로 계산하고 Tick은 그때까지의 행을 읽는다. `o.lock`과 `tickMu`는
기존대로 snapshot 평가·설정 수락·publication을 직렬화한다. CPU 계산 도중 확인한 취소는
snapshot/decision 발행 전에 오류로 반환한다. publication/관측 기록 직전과 `OnDecision` 이후에도
취소를 확인하며, callback 중/직후 취소는 이미 발행·기록한 유효 판단을 보존하되 context 오류를
반환하고 새 physical action을 시작하지 않는다. 이미 시작한 실제 동작을
소급해 취소한 것으로 바꾸지 않는다. 호출이 잠금을 반환하면 다음 full refresh가 canonical
settings와 나머지 구성을 함께 수락하며, 이전 계산을 비동기로 재사용하지 않는다.

## 7. 설정·권한·관측 API

`/api/v1/scheduler`에서 settings/current snapshot/decision/target/actions와 적용
상태를 제공한다. settings 변경은 기존 TokenReview·SubjectAccessReview 경계를
통과해 scheduler ConfigMap write 권한으로 검증하고 before/after·actor·검증 결과·
영향 범위·실제 적용 시간을 기록한다. observation/control/horizon, resource bounds,
용량표 설정, completion margin, 자율 Workload 목록과 비자율 Workload의
`staticRouting` 부분 map을 설정·기록한다. 정적 구성 실험도 같은 관측을 사용한다.

ObservationLog는 request/management와 더불어 decision/settings/action/power 원시 기록을
보존한다. 수신 모집단과 completion-event rate를 구별하고 unknown/missing과 0을
각각 표현한다. node watts·whole energy·incremental energy·단위·meter/구간/coverage를
연결한다. 모든 오류·거절·timeout/cancel·미완료·late와 요청/batch 수를 각각 제공한다.

설정 필드·compact projection·관측 조회의 window/coverage·제외 규칙은
[설정과 관측 API](scheduler-api.md), manual CLI·manifest는
[인스턴스 전달과 수동 실행](scheduler-operations.md)이 원본이다.

## 8. SRS와 구현 확인 경로

| SRS | 구현 경로 | 확인할 소비자 행동 |
|---|---|---|
| SCH-01~02, 19, 28 | capacity planner + decision record + actual CHILL serving instances | keep/change·SLO-first·energy 비교·overload·Pending 구별 |
| SCH-03~08, 29, 31 | Runtime/ObservationLog/window/native state | UID·frozen context·전체 유입·0/unknown·window 경계·관리 중 drain |
| SCH-09~12 | model/runtime/profile catalog + resource snapshot | latest same-condition·같은 실행기·분포/memory·전체 할당·exclusive resources |
| SCH-13~15 | shared selector + capacity table replay + κ 합 판정 | E2E percentile 기반 κ, 전환 중과 최종 판정, node-wide impact |
| SCH-16~18 | idle/action/transition measurement + whole-pool integration | inactive/temp Node·wall/delta 의미·중복 제외·같은 비교 기간 T·실제 readiness |
| SCH-20~23 | Runtime routing + firstfloor actor | ready 즉시 전달·원래 elapsed·UID compatible batch·energy-gain wait·명시적 수명 |
| SCH-24~27, 30 | kube atomic actions + operation ledger | prepare-route-drain-remove·actual effect·partial failure·in-progress reuse·appliedAt |
| SCH-32 | authenticated settings + per-Workload autonomy + frozen record | 권한·변경 결과/영향/적용·동일 조건의 static 비교 |
| SCH-48, AC-34~35 | firstfloor Probe → observer → snapshot/POOL → 기존 route ACK | pending 표시·물리 점유/현재 상태 보존·중복 준비 없음·전력 전 stop/drain 의무 |
| SCH-49, AC-34~35 | schedulerPlan의 Node별 guard + 기존 자원 검증 | 읽기 실패 Node의 준비/전력/부재 기반 제거 완료 금지·기존 전역 Pending 유지 |
| AC-36 | exact/alias 해석 + manual reset의 소유 실물 전체 열거 | 같은 slot의 소유 실물 전체 제거·타 소유분 보존·재호출 멱등 |
| SCH-50, AC-37 | observer Pod UID 장부 → runtime/snapshot 투영 → 모든 점유/효과/POOL 소비자 | 관측 실패에도 실물·할당·소유 보존, 정확한 종료·삭제 proof로만 퇴역 |
| SCH-51, AC-38 | admission 공통 허가 + `c.runs` 영향집합 예약 | 수동/자동/reset/측정의 Node·Workload·실물 충돌과 멱등 해제 |
| SCH-52, AC-39 | 단일 capacity planner + 예약 고정 입력 + UID 한정 DAG/적용 | 긴 전환/관측 불명 동안 독립 대상 재판단·실제 실행, 전역 목표 장벽 없음 |

### ACK 관측·불확실 Node·reset 회귀 설계

아래는 **구현 시 추가할 세 회귀 시나리오**이며 실행 결과가 아니다. 기존
`firstfloor/client_test.go`, `kube/observer_test.go`, `kube/scheduler_transition_test.go`,
`scheduler_transition_test.go`, `scheduler_pools_test.go`, `observation_log_power_test.go`,
`kube/reset_test.go`의 fixture/가짜 client와 실제 production 경로를 재사용한다.

1. **AC-34 — route ACK 지연 중 중복 준비 방지 및 읽기 실패 방어.**
   정상 물리 A/CPU `[2]`를 probe한 뒤 desired annotation의 DecisionID만 바꾸고
   native는 이전 ACK를 반환한다. `Probe→Refresh→capture→진행 중 목표 재계획`을
   여러 tick 진행해 nil error·Native/ID/TargetID/owner/allocation/Ready 유지,
   snapshot `PolicyAckPending`·POOL “반영 대기”, 같은 slot prepare 0·Pod Create 0을
   확인한다. ACK 후 같은 container로 표시가 지워져야 한다. 효과 확인/route 재전송,
   `w.current`, active membership 및 동일 meter 구간 joules가 pending 때문에 변하지
   않는지도 확인한다. 하위 경우로 실제 native 읽기 실패를 주어 Node에
   `InstanceUnavailable`이 남게 하고 네 guard의 준비/전력 거절·fits=false·remove
   완료=false를 확인한다. 정상 다른 Node의 계획과 기존 `ExclusiveCPUInfeasible`
   전역 Pending은 각각 보존되어야 한다.
2. **AC-35 — pending instance도 전력 변경 전 실제 stop/drain.**
   한 Node의 A는 ACK pending이고 요청이 queued/running, B는 정상인 snapshot에서
   전력 변경을 계획한다. A/B 모두 stop→drain이 power의 선행 조건이어야 하며
   outstanding이 남아 있는 동안 실제 power 호출은 0회다. native 요청 완료/빈 요청 목록과
   실제 분배 차단을 확인한 뒤에만 power를 실행한다. pending을 완료 proof로 쓰지 않고,
   진짜 읽기 실패로 바꾸면 해당 Node power를 시작하지 않아야 한다.
3. **AC-36 — 동일 slot 다중 실물 reset.**
   같은 선택 Workload 소유인 `A→S`, `B→A` 두 Pod와 다른 Workload의 C를 넣는다.
   A만 실제 routed여도 unroute가 ambiguity로 중단하지 않고 A/B 모두 drain/remove하고,
   각각의 Pod·Service·config가 없어지며 C의 소유·분배·자원은 불변이어야 한다.
   두 번째 reset은 성공·추가 Pod 쓰기 0회다. 목록 순서를 바꿔 exact A가 항상 A로
   해석되는지, exact가 없는 진짜 다중 alias는 여전히 오류인지 함께 확인한다.

위 변경 위치와 호출 관계는 2026-10-26 저장소의 pinned `gopls` references/definition과
반환된 실제 소스를 대조했다. line 번호는 변경 전 소스 기준의 탐색 앵커이며,
구현 후 이 세 시나리오의 focused test 및 해당 패키지 검증은 별도로 실행해야 한다.


### Ponytail 검수 반영

- firstfloor 배처의 실제 선택 함수는 actor와 replay의 두 소비자가 재사용한다.
- 공존 조합별 선행 campaign은 제거하고 측정/명시적 additive 예상과 실제 오차를 구분한다.
- 측정은 기존 sampler/store와 원자 동작을 호출하는 command로 구성한다. 별도 측정 CRD·범용 campaign/recovery engine은 추가하지 않는다.
- mutable policy는 작게 유지하고 raw profile은 startup에 로드하여 공유한다. 입력별 profile 복사와 별도 broker/DB는 추가하지 않는다.

## 9. 실행 증거와 현재 수용 범위

2026-10-08 local gate·ORT CPU smoke와 2026-10-09 배포 결과는
[실행·배포 이력](scheduler-execution-history.md)에 날짜별로 보존한다.
그때의 synthetic·fixture·wall-power 미측정·MissingEvidence 결과는 현재 검증 상태나
다음 실행 지시가 아니다. 최신 결과는 [실클러스터 검증 현황](../../plans/2026-10-13-scheduler-live-evidence-campaign.md)을 따른다.

기능 연결 검증과 연구적 SLO·에너지 개선 입증을 구분한다. 배포 버전·현재 차단·
다음 의존성은 라이브 증거 진입 문서 한 곳에서 관리하며, 여기서 전체 FULL 통과를 선언하지 않는다.
