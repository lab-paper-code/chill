# CHILL 스케줄러 구현 설계

이 문서는 제어와 물리 수명, 장부와 승인 경계를 정한다. 요구사항은 [스케줄러 명세](../../spec/scheduler/SRS.md), 수치 선택은 [계획기](analytic-planner.md), 실행 조건은 [실행기와 측정](scheduler-runtime-measurement.md), HTTP는 [설정 API](scheduler-api.md), 실제 작업은 [operations](scheduler-operations.md)를 따른다.

## 책임과 의존 방향

| 소유자 | 책임 |
|---|---|
| `internal/firstfloor` | UID별 bounded queue, frozen context, 배칭, 단일 runner와 progress |
| `internal/serving` | ingress, timing feedback, 분배, 수치와 물리 admission adapter |
| `internal/scheduler/protocol` | 중립 문제, 계획과 평가 port |
| `internal/scheduler/v1`, `internal/scheduler/v2` | protocol만 사용하는 탐색 순회 |
| `internal/scheduler` | 공식 전략 하나와 fallback 선택 |
| `internal/serving/firstfloor` | HTTP, metadata와 progress의 typed 경계 |
| `internal/serving/kube` | 실물 관측, capture, 제어 승인과 전환 진행 |
| `internal/serving/application` | 구성 수락, catalog와 cost refresh, preparation |
| `internal/serving/management` | 선언, lifecycle, status와 감사 |
| `internal/serving/state` | operation identity, frozen preparation, scope와 effect receipt |
| `internal/serving/operations` | prepare/remove/power/route와 metered primitive |
| Profiling과 metric owner | 같은 actor의 trial, raw, wall power와 immutable evidence |
| NodeActuator | 단일 host power writer와 독립 readback |

의존 방향은 `serving → scheduler → {v1,v2} → protocol`이다. firstfloor는 Serving과 Profiler를 역참조하지 않는다. Query는 Serving 내부 패키지를 import하지 않는다.

composition root는 하나의 ClusterState를 Observer, Manager, Operations와 SchedulerController에 주입한다. reservationState의 제어 mutex와 장부 mutex, observerState의 source mutex는 별개다. 잠금이나 map을 복사하지 않는다. Manager는 기존 deletion reservation을 사용하며 별도 장부를 만들지 않는다.

## 요청과 공통 선택

원시 표본은 UID와 Definition, 개별 SLO와 수명을 유지한다. 경로에 배정될 때 그 경로의 POST를 동결하며 native에 실제 sender elapsed를 전달한다. Tick의 TOTAL이나 PRE를 실제 요청에 중복 가산하지 않는다. 요청 시간, residual과 물리 완료는 [실행기 계약](scheduler-runtime-measurement.md#시간과-수명)에 둔다.

실제 actor와 직접 재생은 같은 순수 선택 함수를 사용한다. stable EDF에서 같은 UID와 불변 정의의 호환 요청만 묶고, 가능한 batch의 조건부 service 분위수와 요청당 증분 J를 비교한다. 더 큰 측정 batch의 엄격한 energy gain과 fill time이 있을 때만 bounded wait한다.

Serving의 Reject와 Delay는 요청 정책이다. Reject는 불가능한 head를 실행 없이 거절하고 Delay는 후순위 FIFO로 옮겨 normal 뒤 처리한다. actor의 측정 deadlinePolicy와 혼동하지 않는다. batch 확정 직전 monotonic 시각으로 cancel, 최종 수명과 SLO를 다시 확인한다.

모든 후보는 같은 outstanding과 arrival cohort, physical inventory와 replay exclusion scope를 사용한다. 모델링할 수 없는 action과 executor는 공급에서 공통 제외하되 allocation, owner와 예약, 요청과 cleanup 의무는 남긴다. 실패 시도의 identity를 이후 같은 logical target의 성공 실물에 적용하지 않는다. 실패한 prepare 시도의 Pod는 그 인스턴스만 제외하고 노드는 제외하지 않는다. 노드를 제외하면 그 Pod를 은퇴시킬 계획도 그 노드를 건너뛴다.

제외 범위가 재생에서 막는 action은 셋이다. 제외된 노드의 prepare, remove와 power, 제외된 인스턴스의 prepare, 제외된 인스턴스에 남은 요청을 기다리는 drain. 실패한 action과 막힌 action에 의존하는 action도 실행하지 못한다. 인스턴스의 제외는 그 인스턴스의 서비스 예측을 거두며, 그 인스턴스의 stop과 remove는 재생이 실행한다. owner ledger의 진행 중 action과 후보가 새로 내는 action에 같은 규칙을 쓴다.

후보의 SLO 판정은 재생이 그 후보의 action을 실행한 결과다. 재생이 실행하지 못하는 action을 가진 후보는 판정이 없으며 `PlannedActionNotReplayed:<kind>:<node>`로 제외한다. keep은 이미 승인된 의무만 이어 가는 비교의 기준이므로 이 검사를 받지 않는다.

## 판단 평가와 전환 진행

주기 판단과 완료 후 진행은 별도다. capture, 승인, 기록과 launch는 제어 mutex로 직렬화하며 수치 평가는 불변 capture 위에서 lock 밖으로 실행한다. 진행된 상태 위에 옛 snapshot을 다시 게시하지 않는다.

새 target의 승인은 최신 capture에서 묻는 질문 하나다. 그 capture에서 계획을 실행할 수 있고 여전히 나은지를 lock 밖 admission replay(`SchedulerTargetAdmission`)로 판정한다. 평가 중 다른 제어 호출의 게시나 물리 상태 변화는 평가를 버리는 사유가 되지 않는다. relock 뒤 제어 mutex 아래에서 읽은 action과 예약 장부가 admission에 쓴 capture의 장부와 다르면 버린다. 장부 비교는 실행 중 action의 경과 시간과 예약 범위의 순서를 제외한다.

Keep 판단은 새 target을 시작하지 않으므로 장부 변화로 버리지 않는다. 평가 중 게시가 있었거나 owner나 Keep action이 있으면 다시 capture해 게시하고, 이미 승인된 Fixed 작업만 이어 간다. 승인 구성이 평가 중 바뀌면 두 판단 모두 버린다.

current state는 승인 capture, decision은 원래 평가 입력을 기록한다. decision 기록이 수락된 뒤에만 physical action을 시작한다. 기록 뒤 취소는 기록을 지우지 않고 새 launch를 막는다.

`observationEnd`는 historical cutoff, `at`은 blocking I/O 뒤 Runtime lock에서 outstanding과 정의, routing과 credits를 함께 동결한 원점이다. source 시각을 at으로 다시 쓰지 않는다. 미상과 미래 시각, 실제 capture 실패는 사유로 남기며 고정 age TTL은 없다.

완료 signal은 buffer 1 nonblocking이다. Continue는 현재 owner의 capture, reconciliation과 후속 launch만 수행하며 신규 평가나 decision을 만들지 않는다. 주기 timer를 재설정하지 않는다. 후속 launch는 기록 상태와 parent, 현재 controls와 UID 자격을 다시 확인한다.

## 실물 장부

Pod UID가 유일한 실물 장부 key다. Runtime은 실행과 binding/routing projection, snapshot은 판단 복사다.

| 사실 | 보존 규칙 |
|---|---|
| identity | namespace/name, Pod UID, Node name/UID. container 생성 뒤 exact `PodUID/containerID` |
| TargetID와 slot | 계획 alias. 같은 slot의 여러 Pod를 축약하지 않음 |
| allocation | 실제 memory/GPU, 준비 소유와 예약. unknown이나 stop 계획으로0 처리하지 않음 |
| owner와 route | owner reference와 binding을 구분. 빈 route는 ownerlessness proof가 아님 |
| 관측 | 마지막 성공, 원래 시각과 최근 실패를 분리. 실패로 성공 시각을 갱신하지 않음 |
| 수명 | exact container 종료, finalizer와 삭제 확인. 논리 stop과 물리 반환은 별개 |

Create 응답, watch와 초기/복구 전체 List로 UID를 발견한다. container 이전 Pod도 보존한다. terminal, terminating과 label drift, profile/Service/read 오류에도 실물 장부를 유지한다.

실제 native 오류와 Pod unready는 dispatch를 닫는다. probe 진행 중에는 판정을 기다린다. 직전 읽기가 기한을 넘긴 인스턴스의 다음 읽기는 refresh가 기다리지 않는다. 그 인스턴스는 읽기가 답할 때까지 dispatch가 닫힌 채 장부에 남고, 답하면 그 읽기가 바로 게시한다. 복구는 native 성공과 동일 UID/container, 단일 Service 검증을 요구한다. 제한된 Service나 후속 Pod List의 첫 실패 때만 검증된 이전 Ready를 한 refresh 유지하고 issue를 남긴다.

desired/native DecisionID만 다르면 PolicyAckPending이다. 실제 identity와 Ready, allocation과 owner, 요청과 정책을 보존한다. desired로 actual을 덮거나 pending을 완료 ACK로 바꾸지 않는다.

Ready 공급은 Ready로 확인한 실물만 센다. unknown 실물은 해당 노드 새 physical 작업을 막는다. 독립 노드 판단은 계속한다. route-only의 양수 목적지는 정확한 executor 확인을 사용하므로 다른 co-resident의 unknown을 전체 목적지 거절로 확대하지 않는다.

종료가 확인된 source의 Pod가 namespace 목록에서 같은 이름과 UID로 존재하지 않으면 observer도 ledger 발행, runtime retirement, follow 중단과 source 삭제를 수행한다. 이후 remove의 중복 retirement는 성공하며 실제 retirement 오류는 유지한다. 목록 조회 실패는 Pod 부재로 취급하지 않는다.

retirement는 exact 종료 뒤 finalizer와 Service/config cleanup, 정확한 UID 삭제 확인이다. Pod 없음, GET 실패와 DeletionTimestamp만으로 종료를 추정하지 않는다. Pending Pod의 executor 흔적이 전혀 없는 경우 DELETE 요청은 가능해도 반환 proof는 별도다.

정확히 종료된 Pod-only `ID=PodUID`, Stopped와 Terminated는 executor stop/drain 없이 기존 remove를 쓴다. container가 있거나 종료 미확정이면 적용하지 않는다. late probe는 종료나 퇴역을 되돌릴 수 없다.

## 자원과 범위 예약

memory와 whole GPU UUID를 실제 실물과 create 전 target에서 owner/identity 중복 없이 센다. init, sidecar, overhead와 non-serving memory도 포함한다. 외부 GPU owner 불명은 해당 노드 배치를 닫는다. CPU request/quota/mask와 thread matching은 없다.

준비 중 실물과 미생성 예약은 maxReplicas/maxInstances와 bridge peak에 한 번 포함한다. Ready 공급은 Ready로 확인한 실물만 센다. unavailable 분기가 pending 점유를 지우지 않는다.

공통 admission은 `tickMu → mu` 순서로 영향 집합, 전체 충돌, 원자 등록, live 재확인과 시작을 연결한다. mutation 직전 route/outstanding, exact UID와 optimistic precondition을 검사한다. 범위 확대는 effect 전에 재승인한다.

Node와 physical scope는 배타다. local prepare나 retirement의 Workload UID는 소유 identity이며 같은 UID의 다른 노드 확장을 일괄 막지 않는다. 실제 Workload 전체 삭제와 reset은 UID 배타 범위다. route writer는 UID별로 직렬화한다.

route dispatch는 현재 routing과 touched 목적지를 사용해 rebase한다. 더 최신 admission writer의 실제 Ready 몫을 유지하고 자기 몫은 남은1000 안에서 배분한다. borrowed 목적지의 writer 권한을 얻지 않으며 철회된 source를 되살리지 않는다.

목적지별 writer watermark는 같은 장부 mutex에서 단조 갱신한다. 예약 반환과 receipt GC가 이를 지우지 않는다. 진짜 새 admission은 다시 분배할 수 있다. 목적지 실물이나 UID 소멸 때 정리한다.

## 효과와 책임 반환

goroutine 종료, timeout, 취소와 기록 실패 뒤에도 원격 effect 확인 책임을 유지한다. effect 전 확정 실패는 멱등 반환하며 첫 API write 결과 불명과 부분 성공은 unknown 책임을 유지한다.

prepare는 error와 함께 Create 또는 existing GET의 exact Pod/Node/Decision 증거를 반환한다. 마지막 capture 실패로 이 증거를 지우지 않으며 terminal 공개 전 owner에 편입한다. 같은 RestartPolicyNever UID의 첫 container만 결합하고 교체 container를 재결합하지 않는다.

책임 반환은 writer와 future cleanup이 모두 terminal인 뒤 더 최신 exact 관측으로 노드별 판단한다. Pod 부재와 settled power, 또는 다음 known-effect proof가 필요하다.

- 인가 prepare의 identity, profile와 owner, 전체 목표 ACK와 일치하는 late Ready.
- 같은 frozen parent의 정확한 terminal sibling receipt. 관측은 child와 sibling, parent 종료 뒤여야 한다.
- admission baseline에 존재하고 may-touch에 없는 executor의 fresh Ready와 이전 정책. dirty executor에는 적용하지 않는다.

may-touch는 reserveRun과 같은 mutex에서 Execute 전에 단조 기록한다. 실패와 child GC로 지우지 않으며 owner 책임 전체 반환까지 baseline과 receipt를 유지한다. untouched 반환 뒤에도 제거 성공과 allocation 반환은 각각 확인한다.

failed 또는 partial prepare가 만든 Pod는 그 동작의 알려진 결과다. 모든 관련 run이 끝나면 owner의 보상 단계가 생성 receipt의 Pod UID로 기존 remove를 보내 준비 여부와 관계없이 지운다. executor가 있으면 stop/drain/remove, 초기 컨테이너 실패처럼 executor가 없으면 종료 확인 뒤 Pod-only remove다. reused, serving, 다른 owner, 생성 receipt 없는 실물과 pending power는 보상 정리에서 제외한다. Pod 부재를 관측하면 owner 예약 전체를 반환한다. 원래 failed receipt를 성공으로 바꾸지 않는다.

transition의 action(예: prepare)이 failed 또는 partial로 끝나 transition이 실패하면 그 action에 직간접으로 의존하는 미시작 action(bridge, route, stop, drain)은 원래 실패 문장을 사유로 cancel로 끝난다. 실패한 owner는 그 action을 보내지 않으므로 Ready handoff 증거를 기다리지 않는다.

사후 상태 capture와 준비 실패 분류는 동작 기한과 분리된 context로 읽고, 그 상한은 동작이 시작할 때 남은 기한과 같다. 측정 경로의 물리 동작 context와 같은 방법이다. 서빙 Pod의 모든 초기 컨테이너는 실패 때 로그를 종료 메시지로 쓴다.

자기 생성 UID나 API receipt 없는 possibly-effectful Create는 이전 occupant가 전부 사라져도 unknown이다. reset도 이 책임을 인계하며 다른 노드의 cleanup으로 해소하지 않는다.

실제 dispatch 뒤 모든 미완료 frontier가 exact 미확인 precondition에 막히고 running/cancel child가 없을 때만 구조적 실패를 판정한다. 외부 dependency, runnable work, disabled control과 ACK 진행은 해당하지 않는다. failed continuation은 이미 stop한 실물의 drain/remove만 허가한다.

## 전환과 handoff

동시 자원이 있으면 prepare, 양수 정책 ACK와 route handoff 뒤 old stop/drain/remove한다. blocker를 먼저 비워야 하면 최종 goal의 독립 prepare로 검증 bridge를 만든다. 임시 variant와 모든 전환 경로를 완전 탐색하지 않는다.

stop은 completed 표시만 보지 않고 자기 source UID의 actual 철회, Ready 대체와 정책 ACK를 확인한다. ordinary final과 bridge 모두 같다. rebase 뒤 source 양수 weight가 남으면 stop 없이 parent를 실패로 닫는다.

최종 prepare가 남아도 old 보존과 독립 Ready 순증은 먼저 route할 수 있다. 빈 출발은 Ready subset을1000으로 정규화한다. stopped, retiring과 stop 예정 source는 제외한다. 부분 route도 현재 timing과 수요의 직접 replay와 accepted work 보호를 통과한다.

일반 route는 양수 선언을 empty/all-zero로 바꾸지 않는다. 명시 unroute만 철수 허가다. nil scope는 전체, non-nil empty는 변경 없음이다. launch할 수 없는 빈 route와 retirement만 있는 계획은 새 예약을 만들지 않고 다음 Tick에서 재계획한다.

생략 bridge는 frozen dependency와 실제 consumer의 exact 완료 proof로 판단한다. running과 failed receipt를 다른 ACK로 성공 승격하지 않는다. 지난 bridge의 완료에 뒤 stage가 바꾼 전역 routing을 요구하지 않는다.

## 실패 분류와 독립 판단

준비 시작 실패는 deterministic, transient와 unknown으로 분류한다. 자기 프로세스 종료와 cgroup OOM, 권위 있는 artifact/permission 거절은 deterministic이다. 네트워크와 API, Node 불가와 eviction은 transient다.

unknown은 transient로 시작하며 같은 서명과 reason이 다른 action/Pod UID에서 독립 재현되면 promoted다. 알려진 transient는 반복돼도 승격하지 않는다. 분류 이력은 기존 in-memory 장부이며 재시작 후 영속 복원하지 않는다.

서명은 Node identity, profile, memory limit과 measured peak, GPU UUID, image와 전체 RunnerCommand argv다. allocatable drift와 Ready 시각, 단순 관측 시각을 서명에 넣지 않는다.

예약 영역의 실제 상태와 frozen 의도만 고정하고 밖의 후보는 같은 단일 planner로 평가한다. 새 target이 기존 run과 ID/DAG를 덮지 않는다. Keep는 평가 뒤 현재 구성을 유지하는 판단이다. 물리 미확인과 공통 설정, API와 기록 장애는 실제 범위의 사유로 남긴다.

선택의 현재 수요, 전체 SLO 순위와 고정 H, 회복 추정과 에너지 결정 가능성, reserve와 payback은 [계획기](analytic-planner.md)의 공통 계약이다. 설계 연결을 실클러스터 SLO나 절감 검증 완료로 보고하지 않는다.
