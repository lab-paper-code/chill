# CHILL 스케줄러 구현 설계

## 1. 기준과 적용 범위

[스케줄러 SRS](../../spec/scheduler/SRS.md)의 SCH-01~32와
[요구사항·구현 대조](scheduler-srs-audit.md)에 대응하는 현재 판단·제어 구조다.
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
실행 중인 목표에 Fixed가 아닌 동작이 남아 있으면 판단은 그 목표를 keep으로 이어 가고 상태는 `Keep`, 사유는
`TransitionInProgress`이며 새 계획을 만들지 않는다. 가용 노드 최저 모드 낮춤만 남은 목표는 계획을 막지 않는다.
prepare는 cost의 측정 trial 최대 시간에 관측 창 하나를 더한 timeout으로 실행된다. timeout을 넘긴 prepare는
`failed`와 사유 `ActionDeadlineExceeded`로 끝나고, 실패한 동작처럼 controller가 그 목표를 버린다. 다음 판단은
실제 상태에서 다시 계획한다. 다른 동작은 끝날 때까지 돈다.
관측된 `TargetID`가 같은 실제 physical slot은 중복 prepare하지 않는다. 새 완료 proof와
소비 수치는 현재 capture를 사용하고, 원래 실행의 frozen target을 덮어쓰거나 사라진
proof를 복원하지 않는다.

#### 중간 배분

`SchedulerController.intermediateActions`는 최종 route의 의존성이 남아 있으면
`readySubset`으로 같은 목표의 양수 가중치 중 실제 snapshot과 native 상태가
모두 Ready인 목적지만 고른다. 목표 가중치 비율을 워크로드별 합계 1000으로
재정규화하며, 아직 목적지가 없는 워크로드는 현재 Ready 분배를 유지한다.
Stopped이거나 이번 tick에 stop할 수 있는 인스턴스에는 새 가중치를 주지 않는다.
이 route는 다른 prepare, power, drain의 완료와 무관하게 기존 global route
직렬화·native policy ACK·원자적 `Runtime.ApplySchedulerTarget` 경로로 실행한다.

중간 분배는 `<goal>-ready-<subset hash>` ID와 `Action.from=ready-subset`으로
구분하며, 기존 OnAction 기록의 시작/완료와 frozen `operation.target`,
`before.routing`/`actual.routing`을 보존한다. 부분집합은 최종 목표를 덮거나
최종 DAG 완료 조건에 들어가지 않는다. 후속 부분집합으로 이전 분배가
대체되어도 완료 효과 소실로 목표를 실패시키지 않으며, 전체 목표는 계속
`TransitionInProgress`로 유지한다. 기존 요청의 실행 계약·drain과 요청별
지연/SLO admission은 유지한다. 실제 반영은 Ready를 캡처한 제어 tick이며
이 경로가 전체 부하의 중간 용량 충족을 보장하지는 않는다.

#### 분배 적용 증거

global route는 같은 policy ConfigMap/ACK 경로를 쓰므로 기존 running run을 통해
직렬화한다. source release는 이전 global route 완료를 기다리되 독립 Node의 합법적
준비를 전역 lock으로 막지 않는다. 진행 중 route는 자신의 frozen target으로 표시하며, native ACK를 관측할 때까지
판단은 `TransitionInProgress`로 그 목표를 이어 간다. running/native noncancelable 작업은 끝날 때까지 둔다.
자율 제어 대상이 아닌 Workload의 `staticRouting`도 기존 running route가 있으면 덮어쓰지 않고
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
