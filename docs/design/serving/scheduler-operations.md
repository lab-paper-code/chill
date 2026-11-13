# Scheduler 인스턴스 전달과 수동 원자 실행

이 문서는 물리 전달과 수동 작업, 운영 수용을 정한다. 자동 판단과 장부는 [구현 설계](scheduler-implementation.md), 수치 비교는 [계획기](analytic-planner.md), 측정은 [측정 API](measurement-api.md)를 따른다.

## 동작과 idle 추정

SLO 회복에는 명시 추정을 사용할 수 있다. 비용은 첫 유효 tier의 최신 완료 근거를 고르며 같은 완료시각은 작은 cost ID 순이다.

1. 같은 Node와 정확한 모델, 실행 경로와 power 조건.
2. 같은 Node와 operation.
3. 같은 DeviceClass와 operation.
4. `settings.defaultOperationCost.{prepare,remove,power}`.

exact prepare/remove는 같은 Node와 profile 조건, exact power는 같은 Node의 from/to와 loaded state, 인스턴스 수를 맞춘다. 측정 runtime image 문자열과 관측 version을 조건에 사용하며 tag도 허용한다. imageID와 digest pinning gate는 없다.

`costProvenance.source`는 exact, node, deviceClass 또는 default이며 costRef나 configured value를 남긴다. 전이 추정과 default로 에너지 이동을 허가하지 않는다.

각 before/mid/after는 자기 mode와 loaded state의 idle을 쓴다. 정확한 node-state wall baseline, 또는 단일 인스턴스의 같은 Node/mode/meter profile idle이 exact다. 독립 profile idle 여러 개의 합은 exact 공존 실측이 아니다.

defaultIdlePower와 defaultOperationCost는 명시 필수다. 같은 고정 `comparisonPeriodNanos`를 keep과 후보에 적용한다. sample600초를 상수로 고정하지 않는다. 긴 사슬의 완결 operation J와 H 안 baseline/Serving을 구분한다.

signed 차이의 동일 unknown 노출은 상쇄한다. 남은 필수 미지 항은 undecidable이며 keep 충족 시 에너지 선택에서 제외한다. SLO 회복의 순서와 실제 자원, 요청 소유와 drain, reserve 보호는 유지한다.

keep와 선택 계획에는 phase별 idle, requestWatts와 profile references, action seconds/joules를 기록한다. 미선택 inventory에는 예측값을 채우지 않는다. 실제0은 명시0이다. missingCells와 missingCellRefs는 같은 decision의 중복 제거 표를 사용한다.

## 전력 방향과 실제 cleanup

실제 Node의 비어 있지 않은 boot ID가 바뀌면 `(Node UID,from,to)` 방향을 process-local 사실로 남긴다. 그 방향만 old actor 전체 stop/drain/remove 뒤 power/settling, prepare, route를 사용한다.

unknown과 in-place 방향은 stop/drain, loaded-process witness로 power, remove, prepare, route 순이다. unknown은 재부팅 없음의 증거가 아니다. 사실은 재시작이나 대체 Node UID로 이월하지 않는다. 장비별 hardcoded mode 표는 없다.

예상 밖 reboot라도 mode outcome이 성공이면 power를 완료하고 기존 DAG가 cleanup과 재구성을 이어 간다. actuator/readback과 capture, non-reboot loaded-state 무결성 실패는 그대로 실패다.

이미 인가된 정확한 stopped/terminated 실물의 정상 remove는 mode 변경으로 profile Ready가 사라져도 기존 cleanup proof로 진행한다. Node 자체 Ready와 다른 unknown occupant, owner와 measurement 보호는 유지한다.

`ID=PodUID`이며 Stopped와 Terminated인 Pod-only 재고는 executor stop/drain 없이 exact remove한다. blocker, obsolete와 power cleanup 모두 같고 power는 remove 뒤다. container identity가 있거나 종료 미확정이면 적용하지 않는다.

## 인스턴스 전달 경로

CHILL이 physical instance를 고르고 EdgeMesh가 전달한다. Pod마다 Pod-owned ClusterIP Service `chill-executor-<Pod UID>`를 만든다. 직접 PodIP fallback과 고정 Node table은 없다.

`entry point → physical instance 선택 → chill-executor-<Pod UID>:8080 → EdgeMesh → 해당 Pod`

| 요소 | 검증 |
|---|---|
| selector | 실제 UID label `edge.dacs.io/serving-pod-uid` 하나 |
| proxy label | `service.edgemesh.kubeedge.io/service-proxy-name: ""` |
| port | TCP `http:8080`에서 container named `http` |
| Service | actual Pod owner, selector와 port가 같아야 재사용 |
| fence | Service GET과 namespace Pod LIST로 동일 UID/container 단일 executor 확인 |
| lifecycle | RestartPolicyNever. 같은 Pod UID의 새 container를 동일 executor로 교체하지 않음 |
| Ready | native model/profile/runtime, 실제 policy ACK와 exact identity. Service 생성과 Pod Ready만으로 완료하지 않음 |

UID label은 UID와 현재 label을 test하는 JSON Patch다. unrelated status/RV 변동으로 UID proof를 대체하지 않는다. 다른 binding을 임의 수정하지 않는다.

옛 profile이 현재 catalog에서 제외돼도 물리 관측은 mounted startup Config와 exact identity로 계속한다. 검증된 빈 요청 관측은 executor-local recovery fence를 해제할 수 있지만 새 routing과 Execute 적격성을 부여하지 않는다. 과거 요청 영향 구간과 현재 fence의 종료도 구분한다.

native DecisionID만 다르면 PolicyAckPending으로 실제 Ready와 정책을 보존한다. route는 양수 목적지에만 policy write와 ACK를 요구한다. 철회된 dead executor에 새 ACK를 요청해 정상 목적지의 handoff를 막지 않는다.

remove는 native drain과 실제 종료 뒤 자기 Service를 UID/RV precondition으로 삭제하고 finalizer/config를 정리한다. selector drift를 이유로 타 Pod Service를 삭제하지 않는다. finalizer는 자기 primitive-list delta만 변경하고 unrelated 항목을 유지한다.

## 수동 원자 실행

`POST /api/v1/scheduler/operations`는 측정과 별개의 실행 API다. 모든 kind는 strict typed decode와 필수 reason을 쓴다. 종류별 허용 필드는 null과0도 존재 여부로 검증한다.

prepare/remove/route/unroute/release는 비자율 Workload UID 하나다. reset은 대상 UID 자동제어를 먼저 해제한다.

| kind | 입력과 의미 |
|---|---|
| prepare | `workloadUID`, `profileID`, exact `node`, 선택 `fixedBatchSize`. 대상 UID owner로 준비 |
| remove | `workloadUID`, exact `instance`. 모든 UID의 양수 route와 outstanding이 없어야 함 |
| route | `workloadUID`, `weights`, `reason`. nonnegative int32, 양수 목적지 하나 이상 |
| unroute | `workloadUID`, `reason`. 명시 빈 선언과 신규 분배0. accepted 수명은 유지 |
| release | `workloadUID`, `reason`. 해당 override만 제거하고 현재/이후 Ready Pod seed를 따름 |
| reset | `workloadUIDs` 또는 `scope:"all-non-autonomous"` 중 정확히 하나와 reason |

caller는 operations POST nonresource SAR와 지정 scheduler ConfigMap update SAR가 필요하다. owner SA로 대신하지 않는다. 허가 직전 fresh capture로 Workload와 Node, 전체 실물과 routing/outstanding을 읽는다. 마지막 Tick을 재사용하지 않으며 수동 작업이 새 자동 decision을 발행하지 않는다.

prepare는 Node UID와 readiness, MemoryPressure, 현재 memory와 physical GPU 예산을 검사한다. 측정 Node, 다른 owner 예약과 pending power는 거절한다. batch cap과 fixed B는 initialized와 measured 범위 안이어야 한다.

prepared seed는 실제 Ready, owner와 policy ACK를 확인한다. 적용 routing에 UID의 빈 선언이 있으면 분배0이며, UID가 없을 때만 Pod 기본 seed를 따른다. planner prepare는 빈 seed다.

route는 해당 UID 선언만 바꾸고 양수 목적지의 identity, owner와 모델, Ready와 native ACK를 확인한다. 일반 route의 양수→empty/all-zero 전이는 거절하며 unroute만 철수 인가다. release는 다른 UID를 바꾸지 않는다.

선언 저장과 실제 ACK/readback은 별도다. 결과는 actor와 reason, sourceConfigRV, before와 actual.routing, 시작과 완료, partial을 보존한다. refresh는 실패한 선언을 자동 재실행하지 않는다.

## reset과 책임 인계

reset은 하나의 예약과 `steps[]` 기록으로 실행한다.

1. 대상 UID만 자동제어에서 제거하고 actual 설정 확인.
2. UID별 unroute와 신규 배분0 ACK.
3. 해당 outstanding0과 실제 native drain 대기.
4. 대상 UID 소유 실물 전체 stop/remove, exact 종료와 Pod/Service/config 부재 확인.
5. UID 선언과 override release, 삭제 readback.
6. 빈 노드의 요청 가능한 최신 unloaded baseline 중 최소 W mode로 변경하고 Ready/readback 확인.

mode 동률은 이름순이다. policy가 없으면 현재가 측정 최저이며 Ready일 때만 무변경 완료한다. fixed-only는 skip 사유를 남긴다. 측정, 다른 serving과 Node UID 보호를 유지한다.

같은 slot의 모든 owned Pod UID/container를 열거하고 remove 직전 재열거한다. exact physical ID를 먼저 해석하고 exact가 없을 때만 유일 TargetID alias를 쓴다. 다중 alias는 오류다.

실패와 timeout, 취소는 실제 단계와 partial을 남기고 후속을 중단한다. 시작한 원격 effect 예약은 취소로 풀지 않는다. 자동 retry와 rollback은 없다. 재호출은 현재 proof로 완료 단계를 무변경 통과하며 새 Pod나 불필요한 power 요청을 만들지 않는다.

terminal owner의 전체 UID와 책임 Node가 reset의 실제 cleanup scope 안일 때만 책임을 원자 인계한다. active/cancel, measurement와 범위 밖 책임은 인계하지 않는다. 인계한 identity 연결과 미확정 Create 의무는 이전 receipt GC로 지워지지 않는다.

관련 writer 종료 뒤 fresh 전체 Pod List의 실제 부재와 power non-pending으로 노드별 반환한다. terminal Pod도 포함한다. 현재 desired mode와 requested-at에 맞는 transition이 필요하며 오래된 success는 proof가 아니다.

최저 mode 측정 부재로 reset이 partial이어도 독립적으로 입증된 빈 노드는 반환할 수 있다. 생성 UID와 receipt를 못 얻은 possibly-effectful Create의 노드는 old Pod 부재만으로 반환하지 않는다.

## CLI와 정적 배치

`serving-operation --execute-config FILE`은 strict JSON을 HTTPS origin 하나로 보내고 결과를 stdout으로 낸다. `--chill-server`, `--chill-ca-file`, `--chill-token-file`, `--timeout`을 쓴다. 명시 CA와 token만 사용하고 redirect를 금지한다. 오류는 nonzero이며 timeout 기본30분이다.

정적 비교는 비자율 UID를 확인하고 profile을 골라 prepare한 뒤 actual physical instance를 state/pools에서 읽어 route한다. fixed-size 비교만 fixedBatchSize를 지정한다. 배분 중단은 unroute, seed 복귀는 release, 완전 정리는 reset이다. settings PUT으로 배분하거나 client에서 drain/remove/power를 조립하지 않는다.

## 측정 격리

opcost는 승인 Node에서 production primitive를 순차 Measure한다. ID는 `<CR UID>.<순번>.<단계이름>`이며 같은 Runtime과 Observer, meter와 store를 사용한다. 일반 serving은 taint로 제외한다.

종료 witness는 exact Node와 taint, Workload census와 예상 power generation이다. prepare/remove는 실제 한 state 차이와 무추론 proof, 빈 power 전환은 실제 from/to와 양쪽 empty state를 요구한다. 실패와 Insufficient를0J 성공으로 바꾸지 않는다.

## 실클러스터 수용 기준

에너지 비교는 같은 trace, seed, SLO, 최소 수요와 시작 배치를 사용한다. 자율 제어를 끈 정적 stage와 CHILL stage의 전환 포함 전체 풀 에너지를 비교한다. 전체 풀에는 genesis1의 제품 제어 비용을 포함하고 비제품 작업을 섞지 않는다.

CHILL이 서비스별 설정 분위수 SLO를 지키며 meter 오차보다 크게 에너지를 줄이는지 판정한다. 배치 효과의 비교 노드 집합과 전체 풀은 따로 보고한다. 전체 풀이 UNKNOWN이면 에너지 우열을 판정하지 않는다.

에너지 이유의 각 전환은 같은 구간 비교 노드의 예측과 실측 에너지 차이의 절댓값 합이 양수 예측 절감량보다 작은지 확인한다. keep의 노드별 오차와 action별 예측 시간/J 오차도 따로 보고한다.

초기60초 뒤 non-on-time은 동결한 기준 실행과 비교한다. 서비스별 percentile, 전체 Tick과 round의 제어 주기 충족, 판단부터 적용까지 지연을 각각 판정한다. 주기를 넘긴 호출을 평균으로 숨기지 않는다. 결정 적용과 비동기 준비 완료를 구분한다.

keep가 가능한 상태에서 prepare한 대상 수와 각각의 예측/실측 절감을 보고한다. offline 동결 입력에서는 측정 에너지와 동일 H의 순위, 정밀 평가 진입과 oracle 비교를 확인하되 실측 통과로 표시하지 않는다.

추가 측정은 현재 판단의 missingCells 합집합에서 정한다. 비교를 바꾸는 unknown이 남으면 해당 수용을 보류한다. 공통 미지 항이 상쇄되는 비교와 SLO 복구는 기존 계약을 따른다.

## 운영 pprof

기본 비활성이다. 재현 창에만 `servingEntrypoint.pprofAddress:"127.0.0.1:6060"`와 sampling을 켠다. flags는 `--pprof-address`, `--pprof-mutex-fraction`, `--pprof-block-rate`다. loopback IP와 port만 허용하며 주소 없는 sampling은 거절한다.

별도 HTTP listener이며 Service, Ingress와 containerPort를 노출하지 않는다. 권한 있는 운영자가 loopback port-forward로 CPU, heap, allocs, mutex와 block을 보존한다. 민감한 실행 정보이므로 private mode700 디렉터리를 사용한다.

image와 Pod UID, 부하 창, CPU/memory와 sampling을 함께 보존한다. allocs와 mutex/block은 누적임을 고려한다. 수집 뒤 forward를 종료하고 주소와 sampling을 기본 비활성으로 복구한다.
