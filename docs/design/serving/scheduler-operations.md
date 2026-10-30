# Scheduler 인스턴스 전달과 수동 원자 실행

physical instance transport와 수동 실행 계약이다. 배분/reset의 구현 목표·수용 조건을 포함하며 문서만으로 구현 완료를 선언하지 않는다. 자동 판단/DAG/장부·예약은 [구현 설계](scheduler-implementation.md), 측정 적격성은 [실행기와 측정](scheduler-runtime-measurement.md), 과거 관측은 [실행 결과](scheduler-execution-history.md)를 따른다. 수동 성공은 automatic 판단/power 측정/FULL 수용이 아니다.

## Why concealment forces routing

Power mode applies to a whole node; on a memory-constrained device the old model must leave before a new one fits. The [transition specification](../../spec/05_transition_control.md) therefore requires planning request handling during state changes and routing to other ready nodes when the changing node cannot serve.

### 목표 전환 중 Ready 부분집합 분배

final prepare/power/drain 의존성을 유지하되 매 tick 실제 snapshot/native Ready 목적지를 먼저 분배한다. current 공급이 있으면 **old 보존+검증된 독립 Node 순증**, current 없는 bootstrap은 목표 Ready weights를 합1000으로 재정규화한다. stopped/이번 tick stop 가능 경로를 되살리지 않는다. freshness·κ/weight·SLO/backlog gate와 실패 시 current 보존의 상세 원본은 [구현 설계](scheduler-implementation.md)다.
중간 route도 `Operations.ApplyTarget`의 measured profile/native ACK·owner/현재 UID·atomic runtime 분배를 거치며 accepted 계약/drain을 지킨다. 다른 prepare/power/drain/slow 박자를 기다리지 않고 같은 owner 예약 안에서 실행한다. 부분 용량이 전체 SLO를 보장하는 것은 아니다.
ID `<목표 ID>-ready-<부분집합 hash>`, `Action.from=ready-subset`, action/operation start/end·frozen `operation.target`·before/actual routing을 기록하고 campaign Run에도 보존한다. 후속 subset이 대체하며 final target/DAG 완료나 전역 판단 대기 근거가 아니다. owner handoff/제거 의무와 예약 밖 탐색은 유지한다.

## Where a request branches

EdgeMesh is a DaemonSet proxy: the client's node iptables intercepts and chooses an endpoint, not a shared global point. An external consumer enters the cluster at one entry point. A shared Service would delegate selection to EdgeMesh without weights; a direct Pod address cannot resolve across the tunnel. CHILL chooses the physical instance and EdgeMesh transports it.

## 현재 인스턴스 전달 경로

고정 Node table/직접 PodIP fallback 대신 physical Pod마다 Pod-owned ClusterIP Service `chill-executor-<Pod UID>`를 둔다. Kubernetes UID 확정 뒤 selector `edge.dacs.io/serving-pod-uid=<actual Pod UID>`만 설치한다. 단일 TCP port `http:8080`→isolated firstfloor container named `http`, label `service.edgemesh.kubeedge.io/service-proxy-name: ""`를 사용한다.
`entry point → physical instance 선택 → chill-executor-<Pod UID>:8080 → EdgeMesh tunnel → 그 Pod firstfloor`다. user-facing inference Service가 아니며 Workload 이름은 서비스/SLO, model 이름은 artifact다. 같은 Pod를 쓰는 Workload는 executor Service 이름을 공유한다.

| binding·관측 | 계약 |
|---|---|
| 생성/재사용 | actual UID label·owner·selector·port가 모두 맞는 Service만 허용하고 같은 이름의 다른 binding을 고치지 않고 거절한다. Pod UID와 container identity는 별개다. Observer/Capture/policy read-back/native drain은 ClusterIP만 사용한다. |
| UID label patch | 실제 UID+현재 label을 test하는 JSON Patch. metadata-only 변경을 unrelated CNI/kubelet status/resourceVersion으로 막지 않으며 다른 label 변경/Pod 교체는 거절, 재시도 없음. preparation CPU/memory는 `maps.EqualFunc(resource.Quantity.Equal)`로 값 비교하고 표기/cache 차이를 다른 allocation으로 보지 않는다. |
| fence | GET으로 owner/selector/proxy label/port, namespace Pod LIST로 현재 Pod/container1개만 선택함을 확인한다. observer probe 뒤 LIST는 actual UID label selector로 제한하되 terminal도 포함해 중복/교체를 잡고 다른 UID label은 제외한다(SCH-50). |
| readiness | `RestartPolicyNever` 필수. 같은 UID 뒤 container 재시작으로 executor를 바꾸지 않는다. native model/state·immutable profile/runtime evidence를 검증하며 Pod Ready/Service 생성만으로 추론 준비 완료 아님. prepare는 현재 저장 policy를 유지하고 stale annotation을 고치되 annotation write는 native ACK가 아니다. |

### 정책 반영 대기 표시 — SCH-48, AC-34~35

desired/native DecisionID만 다르면 probe는 오류 대신 `PolicyAckPending`을 반환하되 실제 physical ID/Node/profile/allocation/owner/slot/native Ready/policy/요청과 동일 UID/container·단일 executor 검증을 유지한다. scheduler 원본/compact/summary/기록은 instance `reasons`, POOL은 `InstanceState.reason` **반영 대기**로 표시하고 owned/bound/active POOL에 유지한다. ACK 일치 다음 probe에 지운다. desired로 actual을 덮거나 pending을 완료 proof로 쓰지 않는다.
`ApplyTarget`은 runtime resolve와 같이 `target.routing[workloadUID][instanceID]>0` 목적지만 policy write/ACK 대기한다. 0/생략된 퇴역 대상은 기존 policy/drain을 유지하여 dead executor ACK로 제거 분배를 막지 않는다. 양수 목적지 ACK·exact UID/container fence는 유지한다(SCH-AC-38). 실제 read/metadata/Pod/Service 오류는 관측 실패다.
SCH-49 실제 `InstanceUnavailable` Node는 같은 Name+UID의 새 prepare/power와 absence 기반 remove 완료를 보류한다. Node identity/이유를 후보/동작에 남기고 필요한 후속 route/제거도 대기한다. 실행 중 actual 상태를 조작하지 않고 정상 다른 Node 독립 동작과 기존 전역 자원 Pending은 유지한다.

### 자원 장부와 예약 표시 — SCH-50~52, AC-37~39

같은 Pod UID 항목에 physical/slot/allocation/owner·마지막 성공 시각·최근 실패를 함께 둔다. **실물 점유/관측 불명/정책 반영 대기/작업 예약·결과 확인 대기**는 서로 대용하지 않는다. 준비/종료 미확인 실물은 Ready가 아니어도 owner POOL에 남고 새 요청 가능/새 policy 완료는 별개다.
owner·Node/UID/실물 범위·고정 이유를 action/operation에 연결한다. 관측 불명/timeout/취소는 실물·예약 반환이 아니며 actual effect 또는 reset의 Pod 부재+power 비대기 재관측 뒤만 반환한다. 전역 장벽 없이 예약 밖 판단을 계속한다. 성공 cleanup의 routeOnly 제한/직렬 ACK·old Pod/physical/Node와 stop/drain/remove 의무는 [장부 설계](scheduler-implementation.md#54-자원-장부--sch-5052-ac-3739)를 따른다.

| terminal/실패 뒤 효과 확인 | SCH-AC-38 계약 |
|---|---|
| known effect | partial/failed 자율 전환도 fresh exact Pod UID/container/Node UID가 receipt 또는 실행 중 고정 identity와 일치하고 slot(TargetID)/profile/DecisionID/전체 policy ACK를 확인하면 late Ready를 현재 실물로 인수한다. prepare는 실행 중 exact slot/Node/profile/owner의 첫 Pod/container만 고정하고 마지막 Capture 취소에도 보존한다. 종료 뒤 처음 나타난 Pod/교체 container를 소급 고정하지 않는다. |
| 정책/시각 | route는 단일 workloadUID뿐 아니라 frozen workloadUIDs에도 검사한다. 독립 operation의 이전 정책 증명은 자기 Before.Native에만 한정한다. parent 연계 공유는 실제 목표 policy ACK가 원칙이며, 이전 정책 fallback은 부모 인가 시 고정한 `(Node UID, Pod UID, physical ID)`에 존재했던 executor만 허용한다. 형제 route의 Before.Native에 새 prepare가 보였다는 사실은 전환 이전 존재나 목표 ACK가 아니다. 관측은 child와 failed parent EndedAt 모두 이후다. 실패 receipt/DAG는 바꾸지 않고 확인된 책임만 반환한다. |
| Cycle 2 C1 sibling | terminal prepare child는 동일 frozen transition parent action 목록의 ID/kind/Node/instance가 맞는 terminal sibling exact receipt만 참조한다. owner 문자열만으로 공유 금지. 관측은 반환 child·evidence sibling·parent 종료 모두 뒤이고 UID/container/Node UID/TargetID/profile/전체 ACK 일치 필수. complete2+partial1 prepare의 신규 Pod3개가 모두 known이면 넓은 child 범위 반환. unknown/stale/restart/pending power/active·cancel sibling/다른 parent는 유지. 실패 receipt/DAG 불변, 새 timer/보상 선택 조건 없음. |
| cleanup route | 성공 handoff 뒤 cleanupRoutes의 UID별 최신 route는 planner supersession index다. 반환 proof는 같은 reserveRun에서 승인한 공통 routeReceipts의 frozen receipt를 검사하며, cleanup 실패 뒤 최신 ACK를 원래 정책으로 오인하지 않는다. 다른 실물 책임이 해소되기 전 owner 전체를 반환하지 않는다. |
| G1b 인가 receipt | route 종류·From과 무관하게 reserveRun이 parent가 인가한 기존 actionRun을 action ID로 등록한다. 새 journal/receipt 복사 없이 원래 DAG 밖 중간/후속 route의 목표 ACK도 반환 검사에 쓴다. UID별 최신 route가 바뀌어도 다른 executor를 증명하는 이전 목표 ACK receipt는 소실하지 않는다. 다른 owner/미등록/stale/active/목표 policy 불일치는 proof가 아니다. 중간 route를 final handoff/cleanup proof로 승격하지 않는다. |
| 인가 증거 수명 | 같은 owner에 reserved/running/cancel 책임이 하나라도 남으면 parent의 exact baseline/receipt index를 action-view 보존 창과 별도로 유지한다. parent 자신의 reserved가 먼저 내려가도 child 증명 권한은 사라지지 않는다. 책임 반환 뒤에는 기존 관측 창 GC가 parent/receipt를 정리한다. 별도 TTL·영속 journal은 없다. |
| R4b 보상 | P2 §3 R4의 보상 트랜잭션/idempotent cleanup을 기존 retire로 적용한다. failed parent와 같은 owner 실행이 모두 terminal 뒤, failed/partial prepare가 **직접 만든** 실행 중 고정 identity 또는 receipt의 Pod만 검사한다. fresh UID/container/Node/slot/profile/owner 일치, prepare 전 존재 아님, 현재 Workload claim/양수 route/routable/해당 policy 전체 ACK가 모두 없는 Ready executor만 같은 owner에서 기존 remove한다. |
| 보상 수명 | stop→drain→native outstanding 부재→actual container 종료→Pod/Service/config cleanup을 재사용한다. compensation ID는 prepare+exact executor로 고정, 중복 실행 없음. 원래 실패/DAG 불변, 완료 뒤 **새 Pod 부재**를 releaseObservedScopes가 확인해야 반환한다. serving/claimed/다른 owner/identity 불일치/unknown/pending power에는 보상하지 않으며 cleanup 실패도 예약 유지. 새 timer/상수/retry 정책 없이 기존 polling/config/identity 경계를 쓴다. |

제거는 native drain+actual container 종료 뒤 자기 Pod-owned Service를 RV/UID precondition으로 삭제하고 finalizer/config를 해제한다. cleanup은 namespace/name/Pod owner만 확인하므로 자기 Service selector/proxy label drift가 종료 Pod 정리를 막지 않는다. 다른 Pod Service는 삭제하지 않으며 owner reference GC/Workload cleanup도 같은 수명이다.
source finalizer는 immutable UID 포함 strategic-merge primitive-list delta로 자기 항목만 추가/해제한다. 다른 finalizer와 unrelated kubelet status/RV는 보존하며 UID 변경/API 실제 metadata 오류는 반환한다. exact UID/container 종료·Pod 종료 proof는 유지한다. EdgeMesh 설정/타 Workload는 변경하지 않는다. ChillServer Role은 Service GET/DELETE 기본, scheduler/native operation 활성 시 CREATE 추가다.

#### G2 소실과 관측 신선도 경계 — SCH-26, SCH-49~50, AC-45

`control-222-full/full-E5-7-3b/obs/20261029T23/scheduler.jsonl.gz:6767,6788`은 Smol 삭제 뒤 `UnconfirmedInstance`에서 exact 종료가 반영된 `stopped=true`로 바뀌지만 마지막 native 성공 시각은 `23:50:45.632241365Z`에 머무는 경로다. `:6788`의 `UnknownOrStaleInstance`는 전역 Pending을 만들어 대체 후보 탐색 전에 반환하며, 다음 날 `obs/20261030T00/scheduler.jsonl.gz:504`에도 지속된다.
논리적 신규 유입 정지와 kubelet의 exact UID/container 종료는 같은 증거가 아니다. 실물 장부는 실제 제거까지 allocation/owner를 보존하되 종료한 실행기에 새 native 성공 관측을 요구해서는 안 된다. 미확인 실물의 해당 Node 봉쇄·부재 추론 금지와 새 목적지 Ready/양수 policy ACK는 유지한다. R5의 양수 목적지만 ACK하는 경로는 목표 선택 뒤의 실행 경계이므로 그 앞의 Pending을 해소하지 않는다. 원시 카드와 검증 quote는 외부 `analysis/cards-r4/smol-recovery-01.*`이며, 로컬 수정은 배포·실측 복구 성공을 뜻하지 않는다.
`InstanceState`/scheduler snapshot의 `Terminated`는 exact physical ID의 단조 tombstone이다. 논리 `Stopped`와 구별하며 observer의 해당 container 종료 증거만 설정한다. outstanding 병합·늦은 Ready probe도 terminal을 되돌리지 못하고 `Terminated ⇒ !Ready, κ=0`이다. live-native freshness만 면제하며 옛 positive route/slot을 재사용하지 않는다. Pod 부재·예약 반환·할당 해제는 여전히 별도 실제 cleanup proof가 필요하고 `RetirePod`도 logical stop만으로 완료하지 않는다. TTL·새 설정값·관측자의 임의 삭제는 추가하지 않는다.

#### G2 자원 blocker의 ACK 선행 이관 — SCH-25, SCH-27~28, AC-45

Node-local `RequiresBridge`는 routed blocker의 조건부 후보이지 stop 허가가 아니다. 전역 후보의 **최종 goal에 이미 포함된** 독립 prepare만 사용해 `prepare → 중간 route/양수 native ACK → 원 physical blocker stop/drain/remove → 막힌 prepare → 최종 route` DAG를 컴파일한다. 독립 bridge를 증명하지 못하면 `RoutedInstanceStopsBeforeRoute`/window-capacity 거절을 유지한다. growing/slow-window/partial/held, placement/membership/control, dwell/payback 보호를 일괄 해제하지 않는다.
중간 route의 `Action.RouteTarget`은 Actions 없는 불변 정책이고 parent의 최종 goal을 덮어쓰지 않는다. 기존 run/동일 owner 예약과 child route receipt 경로를 그대로 사용하며 별도 단계 상태기계·retry/TTL은 없다. stop은 complete route run만으로 진행하지 않고 해당 원 physical source UID의 현재 route·Ready·전체 양수 native policy ACK를 다시 검사한다. 실패/지연 ACK 또는 effect drift는 stop을 막고, 다른 UID의 후속 bridge가 전역 route를 갱신했다는 이유만으로 이미 유지되는 source 이관을 무효화하지 않는다. 완료한 중간 route는 최종 goal 완료나 cleanup 완료로 간주하지 않는다.
각 단계는 측정 operation chain 최장 시간, 서로 다른 Node의 κ 합, window demand, queued ingress와 보수적 backlog, SLO 여유, 설정된 reserve를 공개한다(`capacity.bridgeStages`). 미래 prepare는 현재 공급/예약 반환 증거가 아니며 기존 실제 transition-reserve 검사는 유지한다. 실제 제거 전 old+new physical peak/replica 예산을 빌려 쓰지 않는다. 대기 중 before baseline과 단계별 routing request energy를 계산하고 raw catalog는 중간 target profile도 ID로 참조한다.
Replay도 최종 goal과 현재 적용 route를 분리한다. 각 중간 route에서 physical ID와 weight가 그대로인 credit만 유지하고, 이미 reset된 lazy credit를 최종 route에서 되살리지 않는다. retained/reset·lazy/initialized credit는 직전 native 정책과 replay 적용 정책을 함께 priming한 뒤 실제 Runtime 분배와 비교하는 회귀로 검증한다.
컴파일은 canonical 순서의 최종-goal bridge에 한정한다. 임시 variant 추가 탐색이나 모든 전환 경로 완전 탐색을 하지 않으므로 bridge 미발견은 전역 불가능성 증명이 아니다. 예산/수요/관측 조건이 맞지 않으면 안전한 전환 부재로 보고한다. 설정은 기존 comparison/observation window, transitionReserveFraction, maxInstances/maxReplicas를 사용하며 미래 campaign rate를 floor로 하드코딩하지 않는다.


### G1b 취소와 client limiter 진단 — SCH-51, SCH-AC-38

`Refresh`/`RefreshWithin`은 공유 관측 직렬화에 들어온 뒤 caller context를 먼저 검사한다. lock 대기 중 취소된 caller는 cached success나 canceled Kubernetes read를 실행하지 않으며 다른 Ready executor를 `refreshFailure`로 내리지 않는다. 진행 중 실제 probe/read 오류의 fail-closed·freshness 경계는 유지한다.
`control-226-rehearsal/api-collection/smoke-MIX/obs/20261030T04/scheduler.jsonl.gz:2215`의 `context canceled\nclient rate limiter Wait returned an error: context canceled`는 autonomousWorkloads 해제의 `run.cancel()`과 일치한다. elapsed 13.171873470s, cost trial 16.257206776s+관측창60s이므로 prepare deadline 초과가 아니다. 최종 Capture도 취소되어 actual 실물 증거가 비었다. limiter 문구 자체는 token 소진/HTTP429 증거가 아니며 QPS 확대를 이 취소의 해결로 주장하지 않는다.
같은 원시 `:2192`의 running prepare와 complete Ready-subset, `:2226`의 Ready·unclaimed 실물을 구분한다. 인가 시점의 exact 기준선+소유자별 효과 receipt라는 기존 transaction ledger 패턴을 사용한다. 부모 이후 생성된 executor는 형제 receipt의 이전 정책으로 반환하지 않고 실제 목표 전체 ACK 또는 기존 R4b remove→fresh Pod 부재가 필요하다. 관측된 사건의 어느 private predicate가 거절했는지는 native metadata 미노출로 미확정이다.
증거가 부족하면 예약을 보수적으로 유지하는 tradeoff가 있다. 기준선 identity 복사와 기존 receipt pointer index만 추가하며 새 timer/TTL/retry/무조건 삭제는 두지 않는다. 취소 admission, 신규 미ACK source의 R4b 우회 금지, preexisting identity 교체 거절, 연속 subset의 목표 ACK 보존·미확인 다른 실물 책임 유지 회귀로 동일 유형 재발을 막는다. 패널 판정에서 부모 실패만으로 예약 범위를 버리는 대안은 기존 R4b/cleanup ownership을 끊어 기각했다.


## 수동 원자 실행 계약

`POST /api/v1/scheduler/operations`는 측정과 별개의 서버 실행이다. prepare/remove/route/unroute/release는 autonomousWorkloads 밖 UID1개, reset은 지정 UID 자동제어를 먼저 해제한다(SCH-39, SCH-41). 기존 prepare/remove `{kind,workloadUID,profileID,node,instance,fixedBatchSize}`를 유지하고 모든 kind별 strict decode·필수 reason을 적용한다.

| kind | 입력/의미 |
|---|---|
| route | `{kind:"route",workloadUID,weights:{"<physical instance ID>":1000},reason}`. 해당 UID 선언만 교체, nonnegative int32·양수 대상≥1. 현재 UID/Ready/owner/model/native ACK 검증. |
| unroute | `{kind:"unroute",workloadUID,reason}`. UID 빈 map 유지→새 배분0. accepted 수명/drain 보존. |
| release | `{kind:"release",workloadUID,reason}`. UID 선언/runtime override만 삭제→현재/이후 Ready Pod 기본 seed. 타 UID 유지. |
| reset | `{kind:"reset",workloadUIDs:["<UID>"],reason}` 또는 `{kind:"reset",scope:"all-non-autonomous",reason}`. 목록/scope 정확히 하나, scope는 수락 시 비자율 UID 목록으로 동결. |

caller는 scheduler ConfigMap update와 `/chill/api/v1/scheduler/operations` POST 권한이 필요하다. ChillServer는 tickMu 안 허가 직전 Tick과 같은 capture로 Workload/Node/Pod/instance/routing/outstanding을 새로 읽는다. 마지막 Tick/선언만으로 허가하지 않아 unroute ACK 직후 remove·다른 UID route ACK 직후 route도 다음 Tick을 기다리지 않는다(SCH-39). snapshot/decision 발행/자율 판단은 하지 않는다.
reset은 현재 Workload로 scope를 고정하고 observer refresh/live 계획 관측을 유지한다. 충돌 확인/run 등록 뒤 lock 밖 기존 Operations를 실행하며 reservation/ownership/tickMu 경계는 같다. UID reset은 Node run/route 충돌도 검사하고 자율 해제 뒤 기존 관련 실행의 안전 완료 전 후속 단계 금지다.
응답은 기존 OperationResult+actual ACK/read-back이며 분배는 전후 선언/actual.routing을 포함한다. 저장만으로 complete 아님.

| 사전 조건 | 허가 직전 fresh capture 기준 |
|---|---|
| 공통 | 현재 Workload·자동제어 밖, Node 존재, measurement taint/실행 run/Node power label pending 없음(NodeBusy). |
| prepare | 그 Node의 derived profileID, owner=대상 UID. 양수 fixedBatchSize는 policy/instance에 함께 기록하고 measured/initialized B 검증([baseline](scheduler-runtime-measurement.md#strict-fixed-size-baseline-serving)). 선택 maxBatch도 initialized+measured 양수만 허용하여 policy cap 적용, 범위 밖 InvalidInput. SCH-AC-46 초기 복원 ACK는 snapshot.instances[].maxBatch이며 options[].instance planner template 불변. |
| capacity | ValidatePreparationCapacity가 fresh Node/all Pod request·inventory/예약을 검사. 설정 namespace 기존 serving Pod·미확인 GPU owner·CPU 예약 부족·memory 초과/MemoryPressure 거절. |
| seed | applied routing에 UID 있으면 빈 map/nil도 배분0 유지. UID 없으면 Pod owner별 annotation seed. manual/measurement 준비 seed는 Ready/owner/native ACK 확인한 instance만, planner seed는 빈 분배. |
| remove | snapshot instance, 타 UID 소유/binding 없음, 모든 UID route weight0·outstanding0. 먼저 unroute ACK 확인. snapshot physical ID로 planner serving-<hash> Pod도 선택 가능하며 이는 manual 증거일 뿐 automatic/power 측정 아님. |

### reset 순서·실패·멱등

SCH-41의 단일 작업 `steps[]`는 `{name,startedAt,endedAt,status,before,actual,error}`, 전체 OperationResult startedAt/endedAt/status/error/전후 상태도 유지한다.
1. 대상 UID만 autonomousWorkloads에서 제거→설정 actual readback.
2. 각 UID unroute→새 배분0 ACK/readback.
3. 해당 UID outstanding0을 기존 native drain proof로 대기.
4. 해당 UID **소유** 실물만 stop/remove. binding≠owner이며 타 UID binding/owner 충돌은 실패. exact UID/container 종료·자기 Service 해제·actual Pod 소멸 확인.
5. UID 선언/runtime override만 release→삭제 readback.
6. 제거로 빈 Node만 DeviceClass/actuation policy 최저 모드 ChangePower→actual readback/Ready. measurement/다른 owner/요청 Node 금지, 불필요/미지원 fixed mode는 현재 상태+skip 이유.
동일 slot 모든 owned exact Pod UID/container를 열거하고 remove 직전 재열거한다(AC-36). runtime/scheduler/native policy는 exact physical 우선→없을 때 유일 TargetID alias다. A→S,B→A에서 A는 A, B도 별도 정리하고 진짜 다중 alias는 오류다. 타 owner/binding/measurement/Node UID 보호는 유지한다.
실패/timeout/read 실패는 단계+actual partial을 기록하고 즉시 중단한다. client cancel도 이후 단계를 중단하나 시작한 physical 효과 예약은 cancel만으로 풀지 않는다(SCH-51, AC-38). 자동 retry/rollback/후속 실행 없고 실패 선언을 refresh가 자동 적용하지 않는다.
재호출은 현재 충족 단계 무변경 완료다. remove 뒤 release/lower-power 실패로 Pod가 없어도 현재 UID compatible preparation의 동일 Node UID를 재확인하여 최저 mode/Ready 미확인 빈 Node를 계속 처리한다. 제외/measurement/다른 serving Pod Node는 제외한다. 최저 actual readback/Ready 전 성공 아님. 두 번째 완료 reset은 새 Pod/선언/power 요청 없이 성공(SCH-AC-33), 별도 재개 state machine/hidden retry 없음.
설정 변경 전 관련 running/cancel 작업 충돌을 거절한다. terminal owner 예약은 그 **전체** Workload와 동일 UID Node가 실제 reset admission `operation.Nodes` 안에 있을 때만 원자 인계한다(SCH-51/SCH-AC-38). 과거 snapshot이 충돌 범위를 넓혀도 cleanup/admission Node 추가는 아니다. active 단계/measurement owner/범위 밖 UID·Node는 인계하지 않는다. 인계 책임은 단계 사이 계속 소유하며 reset effect 전 거절도 미확인 책임 반환이 아니다. actual reset 완료 proof 뒤 해제하고 단계별 action start/end/failure를 기록한다.

## serving-operation CLI

JSON 파일을 보내 응답 stdout, `--execute-config FILE`, `--chill-server URL`(HTTPS origin1개), `--chill-ca-file`, `--chill-token-file`, `--timeout`(기본30분)을 쓴다. 명시 CA/token만·redirect 금지·전송 전 strict decode·응답 error면 nonzero다. 서버 `--operation-timeout` 기본30분까지 기다린다.

## 정적 배치 사용법

SCH-35 비교는 자동제어 밖 UID임을 settings GET으로 확인하고 profiles에서 profileID를 골라 prepare한다. 최소 입력은 `{kind:"prepare",workloadUID,profileID,node:{name,uid},fixedBatchSize:1,reason}`이며 fixedBatchSize는 baseline만 준다. instance ID는 `/api/v1/pools` 또는 scheduler state에서 읽고 route weights를 명시하여 actual.routing/ACK/routable을 확인한다(SCH-36,SCH-39).
배분만 중단=unroute, Pod 기본 표시 복귀=release, 완전 정리=reset이다. staticRouting 설정 PUT이나 client drain/remove/power 조립은 하지 않는다. 수동 분배는 프로세스 재시작 뒤 운영자가 재명시해야 하며 저장 선언은 관측 근거이지 재시작/refresh 자동 실행 지시가 아니다.

## 원자 동작 측정의 격리

measurement POOL은 ChillServer가 객체에 따라 붙이고 떼는 `chill.dacs.io/measurement:NoSchedule` taint Node다([측정 admission](measurement-admission.md)). scheduler는 제외하며 일반 Workload prepare/ChangePower는 NodeInMeasurement다.
opcost는 Node 원자 순서를 한 번 승인, operation.id=`<CR UID>.<순번>.<단계 이름>`으로 순차 측정 후 hand-back한다. meter=Node 설정, store=ChillServer store, prepare=catalog 검증 profile, 측정 instance는 toleration과 같은 Runtime/Observer를 쓴다. taint가 일반 serving을 배제하므로 같은 model Workload가 있어도 측정한다.
종료 isolation witness는 Node/`Tainted`/그 model Workload 목록이다. Node 불일치/taint 부재/미요청 전력 변화(PowerChangedDuringMeasurement)는 Insufficient다. immutable key=`serving/operation-measurements/<Node UID>/<operation ID>.json`; 실패/non-Accepted에서 중단하며 미완 순서는 `serving/operation-sequences/<CR UID>.json`에 기록한다. CR 단계 `{name,key,verdict}`, phase Complete/Insufficient/Failed다.
power도 tainted Node에서 같은 ChangePower·DeviceClass/policy desired label 경로다. 빈 Node 최저→profile mode가 재편입 비용이며 before/after mode가 from/to, 양쪽 빈 State만 Accepted다. loaded 전환은 PowerModeTransitionCost 소유다. 구간은 Node annotation의 apply+독립 read-back+settlingDurationSeconds, 목표 settling 에너지도 포함한다. jetsonx 약1.5s apply도 E1 60s settling이면 coverage 통과하며 전체 구간<meter interval이면 Insufficient다.

## 운영: ChillServer pprof 수집

기본 비활성. 재현 창에만 Helm `servingEntrypoint.pprofAddress:"127.0.0.1:6060"`, `pprofMutexFraction:100`, `pprofBlockRate:"1us"`를 켠다. flags는 --pprof-address/--pprof-mutex-fraction/--pprof-block-rate다. 명시 loopback IP+port만(`[::1]:6060` 가능), wildcard/hostname/port-only 거절. 주소""·sampling0 기본, 주소 없는 sampling 거절이다.
별도 HTTP listener로 HTTPS API에 노출하지 않는다. Service/Ingress/containerPort를 추가하지 않고 권한 있는 운영자만 대상 Pod에 `kubectl -n "$NS" port-forward --address 127.0.0.1 "pod/$POD" 6060:6060`한다. 민감한 실행 정보이므로 capture 디렉터리는 mode700, 루트는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/perf/<UTC 시각>`이다.
같은140req/s 창에 `/debug/pprof/profile?seconds=30` CPU30초(curl max-time40), heap/allocs/mutex/block(curl max-time30)을 pb.gz로 보존한다. heap=live, allocs=누적, mutex/block=프로세스 시작 후 sampled 누적이므로 양쪽 경과/부하 창/sampling을 맞추고 overhead를 고려한다. `go tool pprof -top <cpu.pb.gz>`, `-sample_index=alloc_space -top <allocs.pb.gz>`로 본다.
image digest/Pod UID/부하 시작·종료/req/s/CPU·memory를 함께 보존한다. 수집 뒤 forward 종료, address""·mutex0/block0s로 복귀 재배포한다.

## 구현 위치와 검사

| 경계 | 구현·남은 이행/수용 조건(실행 결과 아님) |
|---|---|
| API | httpapi/scheduler.go는 settings PUT의 read-only 배분 거절, httpapi/operations.go·DecodeManualOperation은 kind strict decode/권한. |
| 실행 | static_routing.go는 UID 선언/persist/ACK/readback, measurement_sequence ExecuteManual은 admission/예약, manual_reset은 순서/partial/idempotence. Operations Execute/StopInstance/removeInstance/ChangePower/Capture의 기존 drain/종료/readback을 재사용. |
| release | ReleaseSchedulerRouting은 빈 override≠UID 삭제, UID key만 지워 최근 Pod seed/이후 Ready seed를 따른다. autonomous 적용·Pod seed persistence도 변경 자율 UID만 갱신하여 release한 비자율 override 재생성/타 UID 수동 override 고정화 금지. routable은 runtime이 계산해 snapshot/POOL 공유, 새 요청 선언+Ready/Stopped/owner/model 필터이며 accepted 전용 보존 route 제외. SCH-AC-32는 release 뒤 새 Ready에 실제 dispatch·타 UID 보존. |
| client cutover | hack/e5/e5.py CPO.clear_owned/finish의 unroute/drain/remove/Pod 대기/release/power 조립→reset+결과 보존, route_ack ConfigMap timestamp/manual-static-routing→명시 ACK, start/restore/factor·demo.py resume staticRouting={}→release ACK 후 자율 설정. CPO.put·e5-wire.py Prepare의 GET→PUT은 staticRouting/staticRoutingValidatedAt 제외. 부하/관측/입력 복원 유지, reset에 Workload 정의 삭제 없음. |
| 수동 수용 | route/unroute/release 선언+actual readback/타 UID 보존·invalid UID/target/autonomy 충돌 거절·SCH-AC-32·reset 순서/outstanding0/owned Pod 부재/빈 Node Ready·실패 후속0·SCH-AC-33 두 번째 무변경. |
| AC-34~36 | 기존 production Probe→Refresh→capture→Tick ACK 지연: physical/allocation/owner/slot/Ready/POOL/목표 유지·prepare/Create0·effect/replica/동일 meter whole/active joules 불변·compact/summary 이유. SCH-49 네 Node gate·이유/identity·복구·독립 Node. AC-35 pending+normal stop/drain 및 queued/running power0→빈 목록/drain 뒤1. AC-36 routed A→S/B→A 전체 native drain/exact 종료/Pod-Service-config 부재·타 owner 보존·재호출 writes0·목록 순서 무관 exact 우선/다중 alias 거절. |
