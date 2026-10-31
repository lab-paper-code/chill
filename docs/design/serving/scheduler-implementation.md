# CHILL 스케줄러 구현 설계

## 1. 기준과 적용 범위

[스케줄러 SRS](../../spec/scheduler/SRS.md)의 SCH-01~32, SCH-48~59와 PR-01..04에 대응하는 구조·확정 변경 계약이다. 요구사항·수용 기준의 원본은 `docs/spec/`이며 문서 갱신은 구현·회귀 통과를 뜻하지 않는다.
firstfloor Go 큐·배처·persistent Python IPC runner는 `internal/firstfloor`, `cmd/serving-worker`, `runtime/firstfloor`에 연결한다. 동료 `/home/genesis1/ETRI/donghwan/firstfloor`는 보존한다. 실제 actor와 용량표 replay는 같은 순수 선택 함수를 사용하며 ingress Runtime은 Workload 수용·분배·drain을 맡는다.

| 세부 계약 | 원본 |
|---|---|
| 실행기·시간·CPU·device·fixed-batch·baseline·catalog | [실행기와 측정](scheduler-runtime-measurement.md) |
| 설정·completion margin·API·관측 조회 | [설정과 관측 API](scheduler-api.md) |
| Pod-bound 전달·수동 원자 실행·reset | [인스턴스 전달과 수동 실행](scheduler-operations.md) |
| 초기 감사·실행 결과의 경계 | [SRS 감사 결과](scheduler-srs-audit.md), [실행 결과](scheduler-execution-history.md) |
| 최신 live evidence·수용 범위 | [라이브 증거 캠페인](../../plans/2026-10-13-scheduler-live-evidence-campaign.md) |

## 2. 책임과 패키지 방향

| 소유자 | 책임·실제 완료 근거 |
|---|---|
| `internal/firstfloor` | bounded per-UID queue, frozen SLO, 같은 UID 호환 batch, energy-gain 대기, 단일 runner; 요청별 progress·실제 batch·runner 종료·동기 응답 |
| `internal/serving` | 정의·receipt·수명·ingress 대기, 운영 부하, 용량표/κ 합, 노드 계획, 분배·판단 기록; 실제 native 상태·반환·적용 기록 |
| `internal/serving/firstfloor` | HTTP/metadata/progress typed 경계, native/client span; request/UID/definition/physical instance 상관 |
| `internal/serving/kube` | 실제 자원 snapshot·catalog·Pod/policy/power 원자 동작·DAG; API 뒤 실제 readiness/drain/삭제/Node 상태 |
| 기존 Profiling owner | 같은 firstfloor fixed-batch·idle·raw evidence·curve; 원래 Run/Job/Node/container/image/actuation와 trial binding |
| 기존 NodeActuator | 유일한 host power writer; 독립 read-back·settling·Ready/Failed |
| 기존 metric/evidence owner | Node wall-power·sample/failure·적분·immutable raw; 조건·구간·coverage가 확인된 watts/joules |

`firstfloor`는 Serving/Profiler를 역참조하지 않는 leaf다. Serving core가 leaf를 사용하고 Kubernetes/API는 kube adapter에 둔다. Profiler는 자체 evidence 계약에 같은 실행 경로를 연결하며 `.go-arch-lint.yml`도 이 방향을 따른다.

## 3. 실행기·요청 계약

외부 요청은 Workload별 입력 1개, 불변 UID·definition·개별 SLO를 유지한다. [Workload 관리](workload-management.md)와 [실행기·시간 계약](scheduler-runtime-measurement.md)이 원시 입력·IPC·deadline policy·monotonic elapsed·physical release의 원본이다.
판단은 고정 historical `observationEnd`와 현재 outstanding origin `at`를 구분한다. 미래·stale source는 Pending이며 source 시각을 재표시하지 않는다. `completionMarginMS`는 요청마다 한 번 반영하고 sender elapsed는 원래 receipt 경과에서만 뺀다.

### 3.3 같은 선택 함수의 실행과 replay

입력은 검증된 batch별 service duration sample·incremental energy/input, per-UID 원래 elapsed/SLO·queue age·반환 envelope·max wait·관측 요청률이다. 출력은 UID·batch size·대기·이유이며 actor와 discrete-event replay가 공유한다.
1. normal은 stable EDF, 같은 UID/불변 정의의 호환 요청만 배치한다.
2. 요청별 p-quantile service 비용·원래 elapsed·반환 비용으로 여유를 계산하고 feasible batch의 energy/input을 비교한다.
3. 더 큰 measured batch의 **엄격한 energy gain**과 관측률 기반 fill time을 확인할 때만 bounded wait한다.
4. feasible batch가 없으면 keep은 minimum-C fillable batch(동률 큰 batch)를 즉시 실행한다. drop은 head 제거, defer는 후순위 FIFO로 옮긴다.
5. normal이 없으면 deferred FIFO 호환 prefix를 minimum-C로 실행한다. drop/defer/timeout과 성공 지연 표본, caller lifetime/cancel은 구별한다.
실제 config/profile·진행 batch snapshot을 기록하고 accepted 정의·SLO는 config 변경으로 재해석하지 않는다.
가상/전환 Node는 `SchedulerNode.AtMode`의 원자 projection으로 PowerMode와 CPUs를 함께 갱신한다. capture가 snapshot 소유 `ModeCPUs[current]`에 실제 published CPUs를 넣고, 다른 mode는 DeviceClass 선언을 쓴다. projection은 이 정규화된 map만 조회하며 라벨-only fast path나 CPU mask 복제를 하지 않는다. capacity·fixed/common·idle·chain·최종 target·power 완료 replay가 같은 경로를 사용하고, 왕복 투영도 실제 현재 inventory를 보존한다. unknown target은 후보 거부/명시적 미확인으로 남기며 reserved/non-serving 예산·측정 affinity 검증과 실제 power 완료 전 공급 경계는 완화하지 않는다.

## 4. 측정 계약과 catalog

Serving/Profiling은 같은 actor/runner·initialized batch 조건을 쓴다. 고정-K는 fake profile 없이 client/service/backend sample·warmup 제외·자원/device/image·원래 trial binding을 보존하며 초기화 domain과 measured policy cap을 구별한다.
profile은 incremental energy에서 차감한 idle과 raw evidence를 보존한다. baseline은 실제 Node/mode/준비 집합/meter에 연결하고 additive 예상과 공존 실측을 구별한다. 원자 동작은 production primitive·actual readiness/drain/read-back·wall joules·부분 실패·Insufficient를 보존한다. 상세 원본은 [측정 계약](scheduler-runtime-measurement.md)이다.

## 5. 판단 입력과 계획

### 5.1 한 운영 snapshot

observation window·control interval·comparison horizon은 독립 설정이다. 기록 보존/발행 구간·분석 필터와 운영 부하를 구별한다. 모든 UID의 received/accepted/assigned/completion을 한 번 수집하여 사건 구간 inputs/s를 구하고 malformed/거절도 전체 유입에 남긴다. compute demand와 total inflow의 모집단은 다르다.
Runtime pending/assigned와 native queued/running·elapsed·batch를 physical instance/UID/definition에 연결하되 assignment와 queue entry를 구별한다. λ와 밀린 요청 B의 q 의미는 [용량표 계획기](analytic-planner.md)를 따른다. unknown/공백/stale 상태, Node Name/UID·단일 POOL·Ready/capacity/resource를 함께 캡처한다.

| capture/refresh 경계 | 계약·결정 근거 |
|---|---|
| 순서 | observer refresh 뒤 profile 상태를 먼저 읽고 Node 목록을 읽는다. Node 관측 뒤 observer lock을 기다리지 않으며 DeviceClass mode도 한 목록에서 찾는다. prepare poll은 그 사이 완료한 refresh를 재사용한다. operation capture는 `spec.nodeName` selector로 대상 Node Pod만 읽는다. m4 r7의 prepare 판단 11~28 s·Node 목록 약 11.5 s 뒤 `UnknownOrStaleNode`가 근거다. |
| steady source | 완료/실패 Pod는 자원 캡처·steady refresh field selector로 제외하되 최초/재시작 watch 복구는 전체 Pod를 읽는다. 공유 client로 Node·Pod·Service·probe 뒤 namespace Pod 목록 네 번을 읽어 단일 executor와 retained UID/container 종료를 확인한다. native probe는 2초 이내다. d9에서 6 instances/20 reads가 limiter의 30초를 소진한 것이 근거다. source별 병렬 probe는 잠금 밖에서 수행하고 각 완료 결과는 별도 Pod LIST로 검증·짧게 게시한다. |
| 제한된 read 실패 | Service 또는 probe 뒤 Pod LIST 실패 때 owner/profile 불변·profile/소유 검사를 통과한 executor의 직전 probe Ready를 그 refresh 한 번만 유지하고 issue/log를 남긴다. 연속 실패는 unready다. 교체/준비 소실은 watch/log follow로 닫으며 Node/Pod 목록 실패는 할당을 닫는다. |

#### 정책 ACK 대기와 실제 관측 실패의 분리 — SCH-48~49

ACK pending·실제 읽기 실패·중복 실물 정리는 함께 적용할 계약이다. 기존 `ExclusiveCPUInfeasible` 등 전체 자원 실패의 전역 Pending은 유지하되 Node 하나의 `InstanceUnavailable`을 새 전역 중지로 확대하지 않는다.
- **SCH-48 / AC-34~35:** `validateMetadata`는 desired/actual DecisionID 불일치만 오류에서 빼고 profile/runtime/provider/device/affinity/입출력/Ready는 모두 검증한다. `(ReadyInstance,nil)`과 기존 `State.Reason=PolicyAckPending`을 쓰며 새 bool/cache/sentinel은 없다.
- 성공 read의 실제 physical ID·Node·profile·allocation·owner·TargetID·Ready·정책·요청·원래 시각을 유지한다. desired로 Native를 덮거나 ACK 때문에 `Ready=false`/`Stopped=true`/`Native=nil`로 바꾸지 않는다. 실패/교체 executor의 stale 상태를 무기한 살리지 않는다.
- observer는 성공 probe로 받아 동일 Pod UID/container·Service 단일 executor를 검증한다. pending은 unavailable/read-failed fallback이 아니다. 실제 HTTP/디코딩/metadata/Pod/Service 오류는 기존 실패 경로다.
- Runtime·snapshot의 실물/할당·`!Stopped` replica·POOL 귀속을 유지하고 scheduler 원본/compact/summary/기록 `reasons`, POOL `InstanceState.reason`에 **반영 대기**를 표시한다. ACK 후 다음 probe에서 지우며 available POOL로 이동하지 않는다.
- 존재/현재 Ready와 새 정책 완료는 별개다. native DecisionID·정책 ACK·실제 target 검사는 유지하고 pending을 route 완료나 목표 `Actual.Native`로 만들지 않는다. route는 owner 예약의 frozen target/ACK 대기를 유지하며 예약 밖 판단을 막지 않는다.
- **SCH-49 / AC-34~35:** Name+UID가 일치하는 unavailable Node에서 `prepareTargets`는 존재 비교/자원 해제/prepare 전에 거절한다. `preparationFits`는 allocation/budget 전 false이며 이를 blocker stop/remove 근거로 쓰지 않는다. `changeNodes`는 Fixed lowering 재사용/stop/drain/power/remove 추가 전에 거절하고 remove의 `completedEffectPresent`도 false다.
- Node/후보/동작에 `InstanceUnavailable`과 identity를 남기고 필요한 후속 변경을 보류한다. 조용히 생략하여 executable로 반환하지 않는다. 실행 중 동작을 조작·재전송하지 않으며 정상 Node의 독립 계획은 기존 전역 검증 아래 계속한다. 이 guard는 장부/공통 proof로 흡수한 뒤만 제거한다.
- 완료 route의 실제 physical/분배/소비 policy가 같으면 ACK 표시만으로 효과 소실·재전송을 만들지 않는다. 실제 batch/wait/capacity/분배 변경은 불일치이며 running route의 신규 ACK 대기는 단축하지 않는다. active membership의 최종 권위는 아래 미반환 점유 장부다.

### 5.2 용량표와 노드 계획

Tick은 replay하지 않는다. 외부 worker가 켜진 Workload SLO와 measured option/배치 상한별 최대 충족률 κ를 계산하고 Tick은 합계를 쓴다. row key·포아송 창·격자/이분 탐색·`NonMonotone`·계산/이전 행 사용은 [용량표 계획기](analytic-planner.md), 설정은 `capacityWindowNanos`, `capacityWindows`, `capacityPassingWindows`, `capacityMinimumRequests`가 원본이다.
`dispatchOccupancy`는 node/mode/runtime image/model의 측정 record다. 없는 option은 `MissingDispatchOccupancy`로 κ가 없고 켜진 UID에 κ 행이 전혀 없으면 `CapacityPending:<UID>`다. κ 불명 UID를 고정하여 독립 UID 진행을 허용하는 규칙은 5.4절을 따른다.
창은 option 하나인 keep snapshot에 seeded Poisson arrival을 넣고 공유 selector로 재생한다. 실행은 empirical service sample + batch size×dispatch occupancy이며 queue/실행/전달/반환의 **전체 E2E 분포**를 gonum/stat로 percentile 계산한다. 완료 여유를 포함해 SLO 이하이고 timeout/만료 0인 창만 충족이다.
Node마다 keep과 해당 Node/mode의 measured option 변형 하나 중 `ValidateNodeBudget` 충족 계획을 만든다. 계획당 κ_new/κ_ret·route 전후 사슬·전력을 두며 현재 계획의 Node당 serving instance는 하나다. `FixedWorkload`, `MeasurementNode`, `NodeUnavailable`, 배치 설정 부족은 keep-only다. 비용/행/자원/baseline 누락은 이유를 남기고 비교에서 뺀다. placement/resources/replicas/routing/batching/membership gate와 `maxReplicas`/`maxInstances`/`maxCPUs`는 계획기 원본을 따른다.
instance는 Workload 하나를 맡고 snapshot/기록에 `workloadUID` 하나를 가진다. Runtime/replay는 같은 deterministic smooth-weighted 분배를 사용한다. active UID 가중치 합 1000, 비자율 UID는 현재 값 유지, active UID 누락은 오류다. 준비 slot은 실제 Ready physical ID로 해결한다. 같은 physical ID/weight의 credit만 유지하고 변경/new entry는 0이다.
CPU/GPU/memory 예산은 전체 allocation+system/non-serving 예약이다. `CPUCount`는 measured/actual Pod request 올림값으로 quota/ORT threads/affinity 폭과 다르다. measured threads와 전체 affinity를 재현하며 폭>threads를 허용하되 mask 중복·예약 침범은 차단한다. whole GPU UUID exclusive, measured peak memory+Serving 부가 예산을 검사한다. Pod당 runner 하나·순차 batch이고 replica는 Pod 수다.
non-serving effective CPU는 Node millicore 합 후 한 번 올림, memory도 합한다. quota는 affinity proof가 아니다. explicit exclusive 예약이 합계를 덮지 못하거나 CHILL 외 Pod가 물리 GPU를 요청하면 `PlacementBudgetUnavailable`로 그 Node만 keep이다. 현재 native serving은 합계에서 제외하며 이유 있는 Node도 POOL에 남는다.

### 5.3 전환·에너지·선택

profile idle/meter는 같은 Node·node-exclusive-wall·source kind/NodeName/endpoint/metric/label selector로 연결한다. thermal endpoint/metric은 별도 관측 identity이며 wall-power 연결 조건이 아니다. 다른 series/Node/범위는 거절한다.
전환은 영향받는 모든 UID의 최종/전환 상태를 평가한다. 동시 allocation 가능 시 prepare→route→old stop/drain/remove, 불가 시 최종 target에 없는 blocker만 stop→drain→remove→prepare→route한다. accepted ingress는 미래 호환성으로 포기하지 않고 현재 Ready/routed 대체가 없으면 old source에 dispatch하여 원래 identity로 drain한다. route까지의 κ_ret와 backlog B로 W를 판정하므로 합법적 DAG도 전환 SLO에서 탈락한다. 독립 Node는 병렬, 같은 Node 비용은 의존 순서다.
같은 profile/allocation/Node의 Ready/unbound executor는 검증된 scheduler slot의 ownerless 또는 정확한 Workload owner UID일 때 재사용한다. 빈 routing을 ownerlessness proof로 쓰지 않는다. route identity는 physical/logical target·UID weights·소비 completion bound/capacity/batch/wait/deadline이지 후보 순번/rolling witness가 아니다.
완료 표시는 proof가 아니다. prepare Ready identity/state, route actual 분배/조건, stop의 중지/분배 차단, drain의 추가 outstanding 부재, remove의 actual 부재/drain, power Node read-back을 확인한다. 효과 소실은 새 action으로 재실행하되 확인 불능은 run/예약 유지·재관측이며 effect-lost/재전송이 아니다. 같은 ID/계약의 live terminal 결과는 stale running/cancel snapshot보다 우선한다.
native 교환이 완료 proof 없이 502/504/취소로 끝나고 native progress가 전혀 없으면, 교환 종료 뒤 시작한 state read에서 요청 부재를 확인하여 `backend_finished/NativeRequestAbsent`로 끝낸다(m4 r6의 drain 영구 대기 근거). routing map만으로 handoff하지 않고 completed route의 Ready/profile/allocation/native ACK와 새 typed completion proof/actual routing이 모두 같아야 stop 의존성을 재사용한다. 첫 handoff/변경 target은 원래 route 완료를 기다린다.
break-before-make의 빈 routing/bindings 중 의도는 frozen `Operation.Target`의 UID cohort/replicas/weights에서 읽되 actual 점유/ownership/Ready는 현재 snapshot만 쓴다. observed TargetID의 physical slot을 중복 prepare하지 않고 frozen target/proof를 덮거나 복원하지 않는다. prepare timeout은 cost trial 최대+관측 창 하나이며 `failed/ActionDeadlineExceeded` 뒤 원격 효과 불명 예약은 유지한다.

**완료 기반 전진 (Cycle 6 / SCH-20·26·50).** action의 결과와 기록 실패 상태를 저장한 뒤 버퍼1 non-blocking 완료 신호를 보낸다. 서비스는 주기 timer와 별도 case에서 `Continue`를 실행하며 기존 주기 deadline을 재설정하지 않는다. `Continue`는 Tick/설정 변경과 같은 직렬화 경계에서 capture·동일 freshness/resource 검증·owner/effect reconciliation·running parent launch만 재사용한다. EvaluateScheduler·capacity search·신규 admission/decision 발행은 하지 않는다. 관측/기록 실패·취소 parent·실패/partial 결과는 후속 허가가 아니며 중복 신호와 late ACK도 기존 exact effect/owner proof를 우회하지 않는다. 현재 controls가 admission 시점과 다르거나 UID 자동제어가 해제된 parent는 완료 경로에서 전진하지 않는다. 완료마다 관측 I/O는 늘지만 전체 탐색을 반복하지 않고, 실제 cold prepare 시간·자원 반환·ACK-before-live-stop·peak/reserve/ReturnProof는 그대로 남는다.

#### 중간 배분

`intermediateActions/readySubset`은 최종 route 의존성이 남아도 같은 owner의 자식 route를 실행한다(`34b9a40d` 유지). snapshot/native Ready·미정지·routable인 old 공급이 있고 target 일부가 미준비면 **old 보존+검증된 독립 Node 순증**만 허용한다. `snapshotNodeHasPositiveRoute`는 모든 UID를 보아 양수 serving Node를 순증에서 제외한다.
`SchedulerReadyExpansion`은 최종 계획과 같은 observation freshness/MaxObservationAge gate, 현재 settings/snapshot/정확한 capacity row key·λ(최근 수요,q/창,최소 수요)·q·L−m·목적지 κ를 쓴다. Ready 합집합은 r=0,B=q,W=B/κ_new이며 기존 `capacityJudge/capacityWeights`를 쓴다. 모든 old/new 양수 정수 weight·rate/κ를 검증하고 같은 Node κ 중복/old 총κ 감소/새 solver/전체 재귀는 없다. stale/unknown/future·row/ACK/identity/용량 부족이면 current와 weights를 그대로 보존한다.
bootstrap처럼 current serving이 없으면 target Ready 부분집합을 합 1000으로 재정규화한다. stopped/고장/unroutable/이번 tick stop 가능 경로는 되살리지 않는다. 다른 prepare/power/drain이나 slow 박자를 기다리지 않으며 UID 한정 atomic runtime route와 ACK를 쓴다. launch의 `dependenciesReady`는 부분집합 target, final route는 전체 target/DAG를 검사한다.
ID `<goal>-ready-<subset hash>`, `Action.from=ready-subset`, OnAction start/end·frozen `operation.target`·before/actual routing을 기록한다. 부분집합은 final target/DAG 완료 조건이 아니고 후속 subset 대체를 effect-lost로 만들지 않는다. 반영은 Ready를 캡처한 tick이며 부족한 current/bootstrap의 κ/SLO 보장은 아니다.
생략된 bridge의 stop 소비도 완료 bridge와 동일하게 중단 source UID의 ACK/actual handoff만 검사한다. 보존된 다른 UID의 terminal route는 이 소비 범위에 포함하지 않는다(SCH-51).
다음 bridge도 부모 DAG에서 생략된 선행 bridge를 찾아, 그 bridge에 의존하는 stop들의 source UID 범위와 기존 완료 ACK를 같은 handoff 검사로 소비한다. 부모 전체 UID에 포함된 보존 terminal route는 검사하지 않는다. 현재 snapshot 대비 변경 UID는 이미 생략된 bridge에서 빈 집합이므로 이 handoff 범위를 대신하지 못한다. 생략을 성공 run으로 만들지는 않는다.
compiler는 해당 UID의 blocked stop이 있을 때만 bridge를 만들고 그 stop에 의존성을 붙이므로 stop 없는 bridge는 생성하지 않는다. 소비 범위는 빈 non-nil UID 집합에서 시작해 source UID만 모으며 부모 범위로 fallback하지 않는다. 빈 범위는 양수 route가 없어 handoff 증명이 될 수 없다.
bridge stage는 기존 routing을 복제하고 현재 UID만 변경한다. 목적지 선택(`peers`)과 prepare 의존성/종료시각 수집을 재사용하며 stage 전체를 중복 검사하지 않는다.
고유 회귀 근거: full-E5-3 `02:45:39.328560181Z`의 495:126:379(κ=103.37311385470142/s, 입력 74.58333333333333/s)를 준비 중 genesis3 단독 1000(κ=62.46166324284952/s)으로 줄이지 않고 완료 후 765:235를 적용한다. E5-1의 old+Ready genesis3 순증은 nano를 기다리지 않는다.

#### 분배 적용 증거

route/source release는 Node·UID·실물 합집합 예약을 쓴다. 같은 owner 의존성은 ACK까지 유지하고 독립 범위는 병행한다. running/native noncancelable 작업은 종료까지 보존한다. 비자율 `staticRouting`도 같은 예약/기록(sourceConfigRV·대기 이유·actual readback)을 쓴다.
ConfigMap/annotation은 desired이고 `Actual.Native`는 실제 ACK만 담는다. 뒤 destination 실패는 앞 ACK/actual routing을 보존해 partial이며 미확인을 완료로 채우지 않는다. `edge.dacs.io/serving-routing`은 UID별 **선언한** weights만 기록하고 accepted 요청용 old memory route는 제외한다. scheduler prepare는 `{}`, owned manual prepare는 owner seed, route는 전체 instance·stop/drain은 해당 instance annotation을 갱신한다. 자율 종료/재시작 뒤도 마지막 Pod 정적 분배를 복구하며 serving source는 annotation이 있고 UID를 하나 이하 가리키는 Pod다.
최종 handoff ACK·actual effect·모든 prepare 완료 후 `reservations[].routeOnly`는 정확한 현재 Ready 목적지에 한해 cleanup 중 weights 변경을 허용한다. 퇴역 source는 κ/route에서 제외하며 새 prepare/power/batch/resource/정책 변경은 금지한다. route는 원래 owner의 직렬 자식이고 old Pod/physical/Node·stop/drain/remove 의무·frozen target·성공 receipt를 보존한다. 최신 receipt는 제어한 UID 효과만 대체하고 타 UID/이미 갱신 UID의 최신 proof는 각각 검사한다. 실패/효과 소실은 owner 실패·미확인 책임 예약이다.
cleanup UID route와 독립 prepare/power/retire가 한 전역 target에 있으면 UID route는 원래 owner로, 나머지는 독립 DAG로 분리한다. cleanup에 다른 UID prepare 대기를 물리지 않는다(SCH-20/AC-38).
모든 계획은 같은 고정 H=`comparisonPeriodNanos`로 비교한다. 각 action duration은 measured trial 최대, 공통 route+최장 post>H는 `TransitionExceedsHorizon`(=H 허용)이다. [0,H] 에너지는 loaded baseline+평균 atomic wall joules이며 atomic 구간은 baseline을 **대체**한다. `target-baseline-delta`는 차감 전력×시간을 더해 wall joules로 바꾼다. profile incremental/input은 route 시각 전후 배정으로 더하고 `search.comparedNodes` 밖 동일 Node는 소거한다.
선택은 켜진 UID별 κ_new≥λ, λ≤κ_ret 또는 W≤L−m을 정확한 합으로 판정한다. 전체 충족 해는 에너지/keep/logical key 순이며 미충족의 현행 leximin 순서는 [계획기](analytic-planner.md)가 원본이다. 충족 해가 없으면 `Overload/NoPlanMeetsSLO`다. 0 부하 active UID도 instance≥1이어야 한다. keep/전체 빈 집합에서 Node 2개, 이어 3개 이하 변경 이웃 탐색을 `planSearchWorkers`로 제어 주기 절반 안에 수행하며 목록 정렬로 입력 순서에 독립적이다.
nominal 이득이 operation/선택 baseline의 조건부 B 민감도 문턱 이하면 keep이다. 공유 immutable 근거는 부호 상쇄하되 profile 통계오차/BEO idle fallback CI는 문턱 밖이다. weights는 낮은 incremental energy부터 λ를 채운 κ 배정을 합1000 정수로 반올림한다. 몫>κ를 옮길 수 없으면 `RouteWeightExceedsCapacity`로 최종 미충족이다. keep도 weights를 재계산하고 weight-only target은 route 하나다.
상태는 `Feasible`, `Overload`, `Keep`(새 변경 없음/진행 전환 계속), `Pending`(snapshot 검증), `CapacityPending`이다. keep/target의 `expectedJoules`는 비교 Node의 정확한 배정 [0,T] 분석값, `worstExceedance` 미정은 float 최댓값이다.
`decision.capacity`는 `periodNanos`, `routeNanos`, `search`(`neighborhood,evaluated,durationNanos,budgetCut,examined,comparedNodes`), `workloads[]`(`uid,rate,queued,kappaNew,kappaRetained,backlog,waitSeconds,drainSeconds,finalExceedance,transitionExceedance,weights,rows`), `plans[]`(`node,key,eligible,reason,kappa,kappaRetained,preNanos,postNanos,joules`)를 담는다. 미정 대기/drain/초과량은 생략한다. examined는 만든 Node 계획 수, comparedNodes는 keep 외 적격 계획이 있는 Node다. 실제 요청/whole-POOL power는 같은 구간·UID·decision에 연결하며 자율 제어는 `autonomousWorkloads` UID만이다.

### 5.4 자원 장부 — SCH-50~52, AC-37~39

Pod UID 장부/runtime/snapshot 투영과 owner별 예약이 현행 구조이며 전역 `InProgressTarget`/`TransitionInProgress`·global goal/route 장벽·종류별 별도 guard는 제거 대상/대체된 계약이다. 단일 전역 계획기·기존 저장소/CRD/서비스만 사용한다. 정책 ConfigMap 단일화·finalizer 책임 이동은 E5 뒤 미해결이다.
결정 근거는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy`의 `intent/structure-redesign-20261026.md` **나**, `audit-20261024/structure-20261026/{StructTruth,StructFailure,StructControl,StructActions2,clusters}.md` K1~K5·`Research-K1~K5.md`다. 권고 전부/증거 저장소 복구/전력 프로토콜/λ·κ 변경을 완료 조건으로 섞지 않는다.
장부 cutover의 결정 근거: `6120aaa7`의 unavailable/deferNode 임시 guard는 안전 행동·진단을 장부/공통 proof로 흡수한 뒤 삭제한다. `6545c800`의 ACK pending/actual 관측 보존은 장부 계약으로 유지하고, `dc9c90bf`/`73f1ec79`의 exact 우선·중복 reset은 전체 Pod 열거로 유지한다. Ready 누락=삭제/제거 성공/effect-lost, slot별 단일 실물, reset 전용 예약·global replay 목표 분기는 남기지 않는다.

#### ① 실물 장부와 관측의 분리 — SCH-50

`kube/observer.go:o.sources`를 **Pod UID 키의 유일한 실물 장부**로 쓰며 Runtime.instances는 실행/binding/routing 투영, snapshot은 판단용 복사다. 별도 registry/`o.verified`/disable wrapper/새 영속 이력·enum/DSL은 없다.
| 항목 | 권위·보존 규칙 |
|---|---|
| identity | namespace/name+Pod UID, Node Name+UID. container 전에도 항목을 만들고 physical ID는 생성 뒤 `Pod UID/container ID`; 이전 container 종료 추적을 덮지 않는다. |
| slot | ServingSlotAnnotation/TargetID는 계획 주소이지 executor 신분/유일성 proof가 아니다. 같은 slot의 여러 Pod를 모두 보존한다. |
| allocation | 실제 request/limit·준비 CPU affinity/GPU/threads/profile·마지막 검증 native allocation. 준비 중 예약도 점유다. 불일치/미확인은 Node 변경을 닫지 점유를 0으로 만들지 않는다. |
| owner/route | Workload owner reference와 현재 binding/routing을 구별하고 빈 route로 owner를 지우지 않는다. |
| observation | 마지막 성공 state/applied DecisionID/Ready/Stopped/요청, 조회 시작·성공 확인·최근 시도 시각/실패 이유. 실패는 성공값/시각을 갱신하지 않는다. |
| lifetime | 생성/삭제 표식·exact container 종료·source finalizer/제거 결과. 실행 종료/자원 반환/API 삭제/cleanup 완료는 다르다. |

Create 반환·watch·최초/복구 전체 LIST로 UID를 발견하며 probe/profile/Service 검증 전에도 기록한다. create 전 의도는 예약이지 가짜 Pod가 아니다. terminal/terminating·relabel·owner 오류·native/Service/LIST/log 오류·ACK pending은 삭제 사유가 아니다. inventory는 physical ID 없으면 UID 키로 pending/init 실패 Pod를 각각 보존한다.
refresh 시작/진행 probe는 실패가 아니며 마지막 Ready를 지우지 않는다. 실제 오류/serving label 불일치·Pod watch unready/삭제는 즉시 dispatch를 닫는다. Pod Ready만으로 복구하지 않고 성공 native probe+Pod 재검증을 요구한다. native not-ready/metadata 실패는 후속 LIST 전에 Ready/runtime dispatch를 닫고 실제 native state는 identity 검증 후 원래 조회 시각으로 게시한다.
퇴역은 동일 UID/container 실제 종료→기존 finalizer/정리→정확 UID 삭제 확인 뒤다. init 실패는 terminal이며 init/main에 실행 중/종료 불명 executor가 없다는 `podTerminated` proof를 사용한다. container ID 없음/DeletionTimestamp/GET 실패/필터 누락/강제 삭제 사건만으로 종료를 추정하지 않는다. 종료 proof 뒤 watch 삭제를 놓치면 정확 UID 성공 재조회로 수렴한다.
reset/remove는 UID+선택 physical ID를 운반한다. executor가 있으면 exact 종료/drain, 없으면 같은 UID `podTerminated`를 쓰며 fake container/slot fallback은 없다. clearNode도 같은 proof/finalizer를 쓴다. `podAwaitingExecutor`는 container ID·실행/과거 종료 흔적·장부 미확인 executor가 전혀 없는 Pending Pod에 **DELETE 요청만** 허용하고 finalizer/예약/taint는 실제 종료+삭제까지 유지한다.
실패 때 항목/owner/allocation/성공 상태는 유지하되 fresh/identity 검사 없이 dispatch/κ를 주지 않는다. late probe/LIST는 퇴역/UID 변경을 되돌리지 못한다. `restoreSources`만 생성하고 `startLogs`는 기존 UID/container·!stopped/!deleted를 요구한다. 재시작은 Pod/finalizer/owner/policy annotation에서 복원 뒤 변경을 허가하며 메모리 run 소멸을 원격 취소/부재로 보지 않는다.
소비자는 전체 장부를 쓴다: `InstallInstances`는 Ready 누락/`!seen && outstanding==0`로 삭제하지 않고 하나의 집계자가 투영한다. capture는 `Native!=nil`/Ready로 실물을 필터하지 않고 CHILL/non-serving을 UID로 중복 제거한다. prepare는 동일 slot/할당의 unknown/준비/미종료 항목·예약을 먼저 찾고 unknown을 absence로 보충하지 않는다.
budget은 실제 실물+create 전 예약 전체를 owner/UID 중복 없이 세며 stop 계획만으로 자원 반환하지 않는다. power/retire는 Node 모든 잔재의 stop/drain/remove proof, remove/effect는 exact 종료·route 차단·outstanding 부재·기존 OperationResult를 요구한다(별도 tombstone 없음). physical 수/Ready κ/전환 의도 수를 구별하고 replica/instance 상한에 준비·미종료도 센다.
active POOL은 Ready 필터가 아닌 **미반환 물리 점유·owner/배분**으로 귀속하여 prepare/drain/cleanup 에너지도 포함한다. 단순 관측 실패로 구간을 끊거나 stale=0 W로 만들지 않는다. 귀속 불명은 이유/구간을 남기며 합계를 완전하다고 하지 않는다. whole-POOL raw coverage/적분은 그대로다.

#### ② 범위 예약과 단일 허가 경로 — SCH-51

`c.runs/actionRun`에 owner(operation ID/DecisionID), Node UID·Workload UID·Pod UID/physical ID 집합을 둔다. Node 전체를 보수적으로 예약하고 CPU별 lock/별도 예약 저장소는 없다. create 전 Node/UID/slot/allocation, 생성 뒤 exact UID/physical을 같은 owner에 묶는다.

Cycle 7 / SCH-AC-38: 부모 reserveRun 전에 주 target의 route scope를 정규화한다. nil은 전체, non-nil empty는 변경 없음이며 no-op은 owner의 별도 omission bookkeeping이지 성공 route receipt가 아니다. prepare/power/retire와 그 의존성은 보존한다. 순수 빈 실행 DAG만 부모 admission을 생략하며 기존 빈 책임 orphan도 명시적 no-effect로 수렴한다. 동적 ready-subset/bridge/cleanup route도 빈 scope를 기록하고 stop의 completedHandoffPresent/exact Ready-policy 검사를 우회하지 않는다.

PrepareInstance는 `(Pod UID, Node UID, 인가 DecisionID, created/reused)` 증거와 error를 함께 반환한다. Execute prepare/resize와 occupancy 호출은 같은 계약을 사용한다. 마지막 Capture 실패가 증거를 지우지 않으며 terminal 공개 전 동일 mutex에서 owner scope에 편입한다. 동일 API-confirmed RestartPolicyNever UID/Node/slot/profile/owner의 첫 container만 terminal 뒤에도 결합하며 returnObserved를 무효화한다. 재결합과 baseline 변경은 금지한다. created=false/생성 결과 불명은 새 생성 보상 proof가 아니며 검색으로 출처를 꾸미지 않는다. late 정상 ACK는 원 실패 receipt를 보존하고 기존 fresh exact proof·terminal sibling·power 경계에서만 반환한다. 증거는 owner 책임의 기존 GC 참조 수명을 따른다.

예약 reason은 blocking action/Pod UID·부족 proof·다음 해소 경로를 action ID 순으로 모아 기존 raw/compact/API 투영에 전달한다. 빈 DAG, identity-awaiting-container, creation-outcome-unknown과 fresh ACK/absence/power 대기를 구별한다. 새 controller/journal/TTL은 없으며 API 결과 불명은 자동 해소를 주장하지 않는다.
공통 `kube/admission.go`가 `tickMu` 안에서 **영향집합→전체 충돌→원자 등록→현재 안전 재확인→시작**을 맡는다. effect 전 실패는 부분 키 없이 멱등 반환하며 범위 확대는 effect 전에 재승인한다. `c.mu`/runtime lock은 짧게, probe I/O는 밖에서, watch 신규 UID GET/게시는 수명 lock 안에서 처리한다. mutation 직전 runtime route/outstanding과 exact Pod UID/optimistic lock을 재검사하며 과거 snapshot 허가를 재사용하지 않는다.
| 진입점 | 예약 범위·수명 |
|---|---|
| 자동 launch/scheduledOperation | 실제 변경 DAG의 Node/UID/실물 전체를 첫 effect 전 확보하며 WorkloadUIDs는 실제 변경 UID만이다. |
| 수동 prepare/remove | 같은 admission. remove는 현재 양수 route/outstanding/owner/UID를 재검사하고 상위 허가 우회 금지. |
| route/static/manual/ApplyTarget | 철회 source+새 destination의 Node/실물 합집합과 변경 UID; ACK/runtime/readback까지 유지. 같은 physical/weight여도 MaxBatch/MaxWait 변경 UID를 포함한다. ConfigMap RV 직렬화·타 UID 선언 보존. |
| reset | 전용 Node fake run 대신 한 부모 집합으로 disable→unroute→drain→remove→release→power 전체 예약. 같은 slot 모든 실물·반환 Node 포함, 자식은 자기 충돌 없음. |
| measurement/occupancy/hand-back | measurement Node+Workload/실물+실제 load Node/공유 endpoint. 같은 UID 다른 Node 측정도 같은 승인 주기 충돌. 외부 root taint/holder를 복원하고 종료/load Job 삭제/hand-back까지 소유. |
| Workload 삭제 | `reserveDeletion→reserveRun`에서 API 삭제 전 UID·소유 Pod/physical/Node 확보; 빠진 대상은 현재 owned Pod로 보강. 실패/cleanup 대기는 유지하고 finalizer 제거가 아닌 `confirmDeletion` exact UID 부재+runtime Forget 뒤 반환. |

Node/UID/실물 하나라도 타 owner와 겹치면 거절/대기한다. 같은 owner는 DAG 의존 순서다. 새 동명 UID는 옛 예약을 상속하지 않는다. 독립 범위 불명이면 관련 범위를 넓히되 모든 route/UID 전역 lock으로 돌아가지 않는다.
해제는 goroutine 종료가 아니다. reset 첫 disable ConfigMap conflict/확정 거절/쓰기 전 실패는 effect 전 반환이지만 첫 write timeout은 unknown이고 앞 단계 성공 뒤 거절은 전체 반환 근거가 아니다. 시작 뒤 실제 효과/cleanup 확인 범위만 반환하고 후속 DAG 자원은 계속 예약한다. 실패/취소/timeout/기록 실패의 원격 불명은 실행만 terminal로 하고 재관측까지 예약한다.
terminal failed/partial manual route/unroute/release는 종료 이후 fresh capture로 exact UID/container·Pod/native Ready와 요청 ACK 또는 exact 이전 정책을 확인하면 Pod 유지한 채 반환한다. mismatch/unready/stale/교체는 안 되며 Pod 부재+해당 Node power 비대기 proof도 유지한다. sibling receipt/보상 정리는 [수동 실행 계약](scheduler-operations.md)의 SCH-AC-38을 따른다.
`routeOnly`는 해제가 아니라 같은 owner의 제한적 weights 변경이다. route 실행 중 다음 route, 퇴역 source/새 physical/정책 변경/reset은 금지한다. unknown은 빈 자원도 전역 중지도 아니며 finalizer 책임 이동을 선행하지 않는다.

#### ③ 예약 범위만 고정한 단일 계획기 — SCH-52

장부+예약을 복사하고 예약 Node의 실물/할당/배분/frozen 의도를 고정하여 같은 capacity planner를 실행한다. unknown/κ pending UID는 점유·route 고정이며 준비 κ/생성 전 의도 에너지는 현재 공급에 더하지 않는다. Node/Workload fixed 바깥 후보는 계속 평가하고 공통 자원 안전 gate는 면제하지 않는다.
unavailable Node 미준비 실물은 queue/batch/profile 실행 후보 검증을 요구하지 않으나 Capacity/MaxBatch=0이어도 allocation budget에 남는다. 예약 frozen target의 미생성 instance도 keep count에 넣고 physical/slot/UID 중복을 제거한다. 고정 영역 κ/에너지 불명은 전체 SLO/에너지 미확인으로 표시하며 독립 변경 proof와 구별한다.
새 완전 target은 고정 부분을 포함하되 기존 run frozen target/ID/DAG를 덮거나 취소하지 않고 독립 DAG만 붙인다. 가장 오래된 global goal을 진실로 삼지 않는다. terminal 결과는 stale보다 우선하며 실패도 owner 범위에만 반영한다. UID 한정 runtime/policy/annotation/readback은 변경 UID의 Ready/identity/owner/ACK를 검증하고 보존 UID는 현재 physical 분배 동일성만 검사한다. 타 UID override/accepted context를 쓰지 않는다.
snapshot/decision은 고정 Node/UID/실물·owner/대기 이유를 action/operation에 연결한다. Keep는 새 변경 없음이지 판단 생략이 아니다. 공통 설정 무효/API/journal 장애는 실제 전역 원인으로 남긴다. replay/API/compact/log/dashboard/fixture도 `reservations`로 함께 이행하며 옛 global 필드/alias를 남기거나 과거 raw를 다시 쓰지 않는다.

#### ④ 빈 출발 κ 대기 — SCH-54, AC-41

실물과 예약이 모두 없는 출발만 첫 판단 `bootstrapStartedAt`을 유지한다. 설정 갱신/restart는 새 출발이다. `bootstrap`에 시작/기한/pending row keys/`timedOut`, settings에 `bootstrapCapacityWaitNanos`를 고정한다(기본 10분, 0~1시간, 0은 대기 끔). 기본은 첫 Smol 20:07:12→MNV 20:09:24 행 간격+후보 계산을 기다리는 운영값이지 보장이 아니다.
`CapacityTable.Ensure` compatible option/batch 중 실제 selectable Node/resource/measured option 행이 모두 Built/rejected면 함께 배치한다. rejected κ는 기다리지 않고 missing/pending만 기다린다. 기한 전 target/절전/예약 없음, 이후 현재 근거로 평가하고 pending/timeout을 남긴다. 실물/예약 생성 뒤는 SCH-52다.
알려진 A안 한계: 기한 뒤 κ pending UID의 유일 Node를 다른 UID가 먼저 예약할 수 있다. 정상 운전의 선점 방지 B안은 미구현이며 사전식 비교는 다른 UID 개선을 보존하되 Node 회수/frozen DAG 변경은 하지 않는다.

## 6. 실제 적용과 관리 연동

kube는 measured profile bundle+작은 mutable policy를 제공한다. 큰 raw 분포는 immutable store/volume에서 startup 한 번 로드·공유하며 request 복사/ConfigMap 반복 전송은 없다. actual metadata/progress로 profile/policy/readiness를 확인한다. transition은 desired/start/progress/complete/failed/partial·prerequisite를 기록하고 actual Pod/NodeActuation/route/drain/remove proof 뒤 후속 동작을 실행한다. host writer는 NodeActuator 하나다.
부하/관리 변화는 같은 snapshot/계획기로 재판단한다. 동일 진행 동작을 재사용하고 cancel은 시작 전/중단 가능한 동작만, physical effect 뒤는 현재 관측에서 다시 선택한다. failed source는 route 격리 후 recovery proof로 복귀하며 기존 관리 gate/accepted definition/공유 UID/drain을 재사용한다.

### 중복 slot의 수동 reset — AC-36

reset은 선택 owner의 **모든** exact Pod UID/container를 열거·remove 직전 재열거하고 slot으로 축약하지 않는다. runtime resolve/schedulerPhysicalState/applyPolicy 모두 exact physical ID 우선, exact 없을 때 유일 TargetID alias다. `A→S,B→A`에서 A는 A이며 B도 정리한다. 다중 alias는 오류이고 owner를 빈 routing/slot으로 추정하지 않는다. 타 owner/measurement Node/다른 Node UID 보호·각 실물 drain/종료/Pod-Service-config 삭제·재호출 무변경 성공은 [operations](scheduler-operations.md)이 원본이다.

### 초기화와 판단 발행 경계

생성자는 routing freeze하지 않는다. 첫 observer.Refresh+manager.Initialize 성공 뒤 autonomous UID가 있으면 `EnableSchedulerRouting`으로 첫 Tick/HTTPS 전 실제 분포를 freeze하며 live 설정 첫 UID 추가도 같다.
기존 `--request-timeout` 기본30초의 같은 callCtx가 refresh+평가를 감싼다. Node 계획/route DP는 취소를 검사하고 context 오류를 반환한다. capacity 행은 controller context의 Tick 밖 계산이다. o.lock/tickMu는 snapshot 평가·설정 수락·발행을 직렬화한다. 계산 중/발행 직전 취소는 발행 전 오류, OnDecision 중/뒤 취소는 이미 유효한 기록은 보존하되 오류 반환·새 physical action 금지다. 시작한 동작을 소급 취소하지 않는다. 다음 full refresh가 canonical settings/구성을 함께 수락하며 이전 계산을 비동기 재사용하지 않는다.

## 7. 설정·권한·관측 API

[설정과 관측 API](scheduler-api.md)가 `/api/v1/scheduler` settings/snapshot/decision/target/actions·TokenReview/SAR/ConfigMap write·before/after/actor/검증/영향/actual appliedAt의 원본이다. ObservationLog는 request/management/decision/settings/action/power, 모든 오류/거절/timeout/cancel/미완료/late·request/batch 수를 보존한다. received와 completion-event rate, unknown/missing과0, wall/whole/incremental energy·단위·meter/구간/coverage를 구별한다. 정적 구성도 같은 관측을 쓴다.

## 8. SRS와 구현 확인 경로

| SRS/AC | 소비자·확인 계약(통과 선언 아님) |
|---|---|
| SCH-01~02,19,28 | capacity/decision/actual instances: keep/change·SLO-first·energy·overload/Pending |
| SCH-03~08,29,31 | Runtime/ObservationLog/native: UID/frozen context·전체 유입·0/unknown·window·관리 drain |
| SCH-09~12 | catalog/resource: 최신 same-condition·같은 실행기·분포/memory·전체 allocation·exclusive |
| SCH-13~15 | selector/capacity replay: E2E percentile κ·전환/최종·Node 전체 영향 |
| SCH-16~18 | idle/action/transition/POOL: inactive/temp Node·wall/delta·중복 없음·같은 T·actual Ready |
| SCH-20~23 | Runtime/actor: Ready 전달·원래 elapsed·UID batch·energy-gain wait·명시 수명 |
| SCH-24~27,30 | atomic/operation ledger: prepare-route-drain-remove·effect·partial·재사용·appliedAt |
| SCH-32 | authenticated settings·UID autonomy·frozen static 비교 |
| SCH-48~49,AC-34~35 | ACK 지연에도 동일 물리/할당/owner/slot·Native/Ready/POOL/replica/meter joules 보존, prepare/Create0, ACK 후 표시 해제; actual read 실패 Node 네 gate·정상 Node 진행·기존 전역 CPU gate 유지. pending/normal 모두 stop/drain·queued/running 동안 power0, 빈 native/drain 뒤만 power 허가 |
| AC-36 | exact 우선/다중 alias 거절·같은 slot 모든 owned Pod 제거·타 owner 보존·두 번째 reset Pod write0 |
| SCH-50,AC-37 | ACK/읽기/Service/LIST/watch 장애에도 UID/점유/owner 보존·unknown effect 재전송0·freshness 없이 κ/dispatch 금지·init 실패/중복 Pod 각각 exact 종료/삭제 뒤 퇴역·late probe 부활 금지 |
| SCH-51,AC-38 | 모든 mutation 공통 예약, source+destination 충돌·동일 UID 측정 한 승인·독립 범위 실제 시작·부분 예약0·effect 전 멱등 반환/effect 후 unknown 유지·fresh proof 해제·snapshot 뒤 새 route/outstanding remove 거절·새 UID 옛 승인 금지 |
| SCH-52,AC-39 | slow power/unconfirmed source/pending κ의 W1/N1 중 독립 W2/N2,N3 Tick→prepare→Ready 캡처 뒤 늦어도 다음 주기 route→정확 physical native 요청 완료. W1 frozen ID/target/route/outstanding 보존·중복/취소 없음·부분 Ready 자식 실행·terminal 우선 |

회귀 설계는 기존 fake client/native HTTP fixture와 production 함수 경로를 사용한다. 별도 장부 모형만으로 통과하지 않으며 focused 회귀/빌드/실연 근거는 구현 담당자가 남긴다. 문서/요구사항 추적성만으로 AC를 통과 처리하지 않는다.

### 두 박자·예비·고정 비교 기간 — SCH-55~58, AC-42~44

현행 정정 결정은 **SCH-56 최대 수요 창 + SCH-59 Fixed 본전 통일**이다. 이전 timer 구현/검증은 이 정정의 완료 근거가 아니다. 4회차 `demo-20261027-4/report.md` §C·ADR 0025가 근거이며 [용량표 계획기](analytic-planner.md)가 정책 원본이다. 장부/공통 예약/부분 Ready를 대체하지 않으며 클러스터/이미지 변경이나 E5 수용을 주장하지 않는다.
| 경계 | 현행 계약 |
|---|---|
| strict 설정 | `scaleDownStabilizationNanos`: 정수 ns≥0, 기본300초/시연120초. `transitionReserveFraction`: 유한[0,1], 기본0. `comparisonPeriodNanos`: 정수 ns>0, H 제안600초. 표현형 초과 거절. energyHorizonNanos alias/두 번째 horizon 없음; API/ConfigMap/Dashboard/sample/binding 동일 검증. |
| 수요 창 | `SchedulerTempo.Observe`가 기존 rows·snapshot.At·정상/gap 판정·w.rate로 실제 Ready/ACK 공급을 계산한다. Selected/선택 후 재기준·baselineRate/previousRate/downSince/globalSince 대신 UID별 (시각,유효 수요) 이력+연속 유효 시작을 쓴다. W 밖 만료, D=max d(s), t−W<s≤t. 완전 W와 D<현재 공급이 slow gate, 후보도 D 감당. W=0은 현재 표본만/기간 대기만 끔. gap/역행/restart/설정·UID 변경은 새 창, 선택은 이력 초기화 안 함. invalid를0으로 채우지 않고 새 timer/서비스/저장소 없음. |
| fast/slow | fast=keep/분배/안전한 순증/물리 정지·제거 없는 κ 증가 정책(회복). slow=정지·제거/κ 비증가·joules 감소의 에너지 교체/추가+제거 혼합; 겹치면 slow. 안정 영향 범위만 full 집합. 현 전체 충족→최종 충족→leximin→energy/keep/key·자원/κ/관측 창/DemandIncreasing/minimumGain 유지. |
| Cycle 5 / SCH-55·56 | `scaleDownHeadroomFraction` 유한[0,1], 기본0. 최종 에너지 하강·idle 후처리만 `max(rate,WindowMax)×(1+h)`를 양수 정수 weight/각 κ로 검사한다. weight0은 공급 아님. bridge/reserve·실제 J는 할증하지 않는다. |
| 조건부 회복 | Node-local slow 후보를 `requiresRecovery` UID로 전역 조합까지 보존한다. 같은 관측 keep 미충족+해당 UID 목표 완전 충족·backlog 처리만 timing 예외. 타 UID 하강은 독립 판정하고 증명 전 공개 ineligible. partial/held/unknown·예약·소유권·Ready/정확 ACK·old+new peak·bridge/reserve/H 유지. 중단형 power 변경은 handoff가 아니다. Observe.Allowed·에너지/leximin/payback/dwell 불변. |
| 빈 Node/Fixed | idle gate·탐색/후처리·commonNodes/changeNodes·target 없는 Keep.Actions 모두 같은 창. 빈 Node는 전 제어 UID 완전 창+D≤현재 공급, 하나 이상 D<공급; 벡터 상쇄/별도 전역 timer 없음. UID 변경 뒤 재충전, 제어 UID 없으면 새 절전 보류. 이미 실행 중 Fixed/허가 owner DAG 완료 의무 유지. |
| 예비 | 예약/unknown Node 제약 우선, 밖 fast/full 계속. f>0이면 구간별 영향 UID actual Ready/ACK κ≥λ(1+f), 같은 공급 중복 담보 금지. 부족은 후보 `TransitionReserveInsufficient`이지 전역 대기 아님. f는 요청률/요청 에너지 분모를 바꾸지 않는다. |
| 중간 route | 느린 박자/전체 준비와 무관하게 다음 tick 실제 Ready 부분집합을 UID atomic route로 편입하되 기존 공급/ACK/stop-drain-power/인계 보호 유지. |
| 고정 H | p.period 확장 폐기. keep/payback/minimumGain/decision.capacity.periodNanos와 모든 후보가 같은 H. 공통 route+선택 최장 post>H 제외, =H 허용. reserve prepare/load/Serving/remove 한 번 집계. Fixed 새 절전 선반영/에너지 면제 삭제, 일반 payback/minimumGain·원상태 keep으로 비교. H 안 nominal gain>0+minimumGain+payback 모두 필요. |
| 기록 | settings/search.bounds/H·Run binding/summary 동결. workloads[].scaleDown=`rate,windowMax,windowNanos,supply,allowed,reason`+선택 `resetReason`. rate=현재 유효 수요,D=windowMax,W=windowNanos,supply=actual Ready/ACK. 탐색 전 반환도 창/공급/보류 이유·후보 slow/제외·energy/gain/minimumGain/payback/reserve κ 연결. 구 baselineRate/previousRate/downSince/elapsedNanos reader/alias 병행 금지; 과거 raw·게시 증거는 보존하며 typed reader는 windowMax/windowNanos/supply 누락을 오류로 거절한다. |

H=600초 근거는 로컬 `campaign/v2/demo-*/summary.json`의 `stages[].actions[].action` 성공 완료: jetsono MAXN150.939초, lattepanda Smol prepare268.867초, genesis3 Smol prepare292.163초, jetsono 하강196.180초; 보수적 직렬 약488초 포함이지 미래 상한 보장 아님. 285초/Ready와 완료 시각 차이는 계획기를 따른다.
필수 수용: AC-42 일정100 req/s+잡음에서 W가 지나도 공급≤D면 축소/절전 없음(국소 고점 timer로39초 허가한 4회차 결함). 실제 하강은 높은 표본 W 만료·완전 창·D<공급·기존 이득/SLO 뒤 허가하며 gap/설정/restart/UID/W=0/역행/벡터 상쇄를 검사한다. D110·rate20·공급160이면 후보60 거절,120은 전환/SLO/본전 충족 시 허용한다.
AC-43은 f=0 기존 SLO 보존, f>0 미준비/중복 공급 거절·충분한 예비 허가·native ACK/actual SLO/예비 포함 E5 joules다. AC-44/SCH-59는 H600초 jetsono MAXN→15W −699J/손익 약52분 및 양수이나 문턱 미달 후보 미실행, 다른 Node 이득에 손해 Fixed를 묻지 않음, 빈 Node 후보/비교 집합/payback 포함, 미선택 긴 후보 추가/삭제에도 H/energy 불변, 조합>H 제외/=H 허용이다.
SCH-54 빈 출발/최소 instance1/증설/부분 Ready/예약 밖 진행은 창 대기 없이 유지한다. 3회차 B의 G/L15~18초 Ready 뒤 N32초까지 대기한 fake 시계 사례에서 G/L이 다음 주기 먼저 배분되어야 한다. 무한 초과량 leximin·예약 해제·accepted drain·H 독립 action timeout·typed API/UI 구 기록 경계·Dashboard 최대 수요 창 도움말도 수용 범위다. 실클러스터 재시연/E5는 별도 승인 후 같은 H/binding/부하로 수행한다.

## 9. 실행 증거와 현재 수용 범위

[초기 실행 결과](scheduler-execution-history.md)는 local/synthetic/fixture/wall-power 미측정과 live MissingEvidence 경계를 보존한다. 현재 배포·차단·다음 의존성·FULL 수용은 [라이브 증거 캠페인](../../plans/2026-10-13-scheduler-live-evidence-campaign.md) 한 곳이 원본이다. 기능 연결과 연구적 SLO/총에너지/공존 오차 개선은 별도 증명이다.

## 10. 같은 기종 profile 재사용 — PR-01..04

검증된 직접 profile 선택(`kube/catalog.go`)→`catalog_reuse.go` 판정/대상 materialize→기존 `cmd/chill-server/profiles.go` preparations→observer/option 경로다. 새 CRD/UID alias registry 없이 [측정 API](measurement-api.md)의 Node UID 소비자/CampaignOps 계약을 따른다.
기존 capacity/observer/operations/runner/cost_power/firstfloor UID guard를 유지하고 대상 Condition/RuntimeMetadata/RuntimeDelivery를 쓴다. catalog `reuseChecks`, profile `reuse`, 기존 BEO immutable result/raw가 근거다. 수락/거절을 기존 store에 content-addressed immutable 기록 후 `record` 참조를 발행하고 refresh signature에 판정 본문도 넣는다. memory는 양쪽 최대, idle baseline/physical UUID/meter는 대상 것이며 raw sample은 합치지 않는다.
