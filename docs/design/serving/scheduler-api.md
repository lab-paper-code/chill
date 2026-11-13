# Scheduler 설정과 관측 API

설정 수락, 권한과 조회 projection의 계약이다. 판단과 전환은 [구현 설계](scheduler-implementation.md), 수치 모델은 [계획기](analytic-planner.md), 요청 접수는 [워크로드 관리](workload-management.md), 저장과 원시 조회는 [관측 설계](observability-split.md)를 따른다.

## 구성 입력

`--serving-scheduler-configmap`과 `--serving-scheduler-config-key`로 Serving namespace의 구성을 읽는다. 기본 key는 `scheduler.json`이다. ConfigMap 이름이 비면 자동제어 없이 관리와 추론을 실행한다.

설정은 strict JSON이다. CPU 상한, quota나 mask 같은 미선언 입력은 거절한다. duration 필드는 정수 ns이며 생략과 명시0을 필드별 검증 규칙으로 구분한다.

| 필드 | 의미 |
|---|---|
| `settings.observationWindowNanos` | 부하와 피드백 관측창, 양수 |
| `settings.controlIntervalNanos` | 제어 주기, 양수 |
| `settings.comparisonPeriodNanos` | 모든 후보와 keep의 고정 에너지 기간, 양수 |
| `settings.maxReplicas`, `maxInstances` | UID별 복제와 전체 실물 및 미생성 예약 상한 |
| `settings.servingMemoryBytes` | frozen memory envelope 안 queue headroom의 상한 |
| `settings.completionMarginMS` | 필수 유한 비음수. 유효 TOTAL/POST 표본이 없을 때 초기값 |
| `settings.capacityWindowNanos`, `capacityWindows` | 양수 검사창과 rate당 창 수 |
| `settings.capacityPassingWindows`, `capacityMinimumRequests` | 필요한 충족창 수와 최소 요청 수. 충족창은 전체 이하 |
| `settings.capacityGridSteps`, `capacityBisectionWidth`, `capacityTailSamples` | 명시 offline 전체 rate 탐색 설정. grid와 tail은 양수, width는 유한 `(0,1]` |
| `settings.controls` | placement, resources, replicas, routing, batching, power, membership |
| `settings.autonomousWorkloads` | 현재 UID 중 자동제어 대상. PUT에서 미등록 UID 제거 |
| `settings.excludedNodes` | 중복과 빈 이름 없는 Node 이름. keep과 실물 점유 유지 |
| `settings.minimumDemand` | UID별 유한 비음수 req/s. 생략0, 미등록 UID도 보존 |
| `settings.sloPriorityStrategy` | `excess` 기본 또는 `workload` |
| `settings.workloadPriority` | 중복과 빈 값 없는 UID 순서. 미등록 UID 보존, 평가에서는 무시 |
| `settings.transitionReserveFraction`, `scaleDownHeadroomFraction` | 전환 실제 예비와 최종 하강 수요 여유 |
| `settings.defaultDispatchOccupancyNanos` | 모든 batch의 명시 scalar fallback |
| `settings.defaultOperationCost` | prepare/remove/power의 명시 seconds와 whole-wall joules 추정 |
| `settings.defaultIdlePower` | 명시 nodeWatts와 perInstanceWatts 추정 |
| `settings.planSearchStrategy` | `v1` 또는 기본 `v2` |
| `settings.planSearchWorkers` | 양수. 이웃 탐색과 정밀 funnel의 병렬 수 |
| `settings.planSearchEvaluations` | 증분 탐색 평가 수, 기본500000 |
| `settings.planExactFinalists`, `planPolishActive` | 기본16과8. active0은 전체 변형 |
| `settings.planPostFilterFinalists`, `planPricedStart` | 기본false와true |
| `reserved` | Node별 memoryBytes와 GPUUUID 예약. CPU 예약 없음 |
| `powerObservation` | sampler와 적분 coverage 설정 |
| `dispatchOccupancy` | rolling 집계와 native DOM 생성 정책 |
| `staticRouting`, `staticRoutingValidatedAt` | 읽기 전용 배분 선언과 검증 시각 |

sample의 observation60초, control10초, comparison600초, margin29ms는 설정 예시다. 전체 sample은 [scheduler 구성](../../../config/samples/serving-scheduler.json)을 따른다.

Serving Pod의 request와 limit은 frozen Actor memory를 유지한다. queue bytes는 `min(min(servingMemoryBytes,memoryLimit−measuredPeak)/2,native.MaxFrameBytes)`다. 양의 headroom이 필요하며 peak에 servingMemoryBytes를 더해 Pod limit을 새로 만들지 않는다.

profile 후보와 PrepareSpec, 비용과 wall meter는 검증 측정과 metricSources에서 도출한다. 수동 profile 목록이나 별도 NodeActuation 선언은 없다. 전력 요청 가능성은 DeviceClass와 actuation policy, 실제 readback을 따른다.

## 설정 수락과 동시성

GET의 settings와 staticRouting, powerObservation과 ETag는 같은 ConfigMap 응답이다. meters와 accepted catalog는 별도 수락 revision이다. 적용 시점은 각 수락 revision에서 확인한다.

PUT은 `SchedulerSettings`만 받는다. `staticRouting`과 `staticRoutingValidatedAt`은 null을 포함한 존재 자체를 `400 InvalidBody`로 거절한다. 미선언 reason도 거절한다. 배분은 [operations](scheduler-operations.md#수동-원자-실행)의 route/unroute/release만 쓴다.

`If-Match` 누락은428, stale과 충돌은409다. 검증 Principal의 groups/extra로 정확한 namespace/name ConfigMap update SAR를 추가 확인한다. 사용자 포괄 권한이나 owner SA 권한으로 대신하지 않는다.

최초 완전 refresh 뒤 PUT은 수락된 typed catalog와 preparation에 정책을 동기 적용한다. 200의 appliedAt은 정책 수락이며 다음 Tick이 소비한다. queue cap, 초기 margin과 occupancy fallback도 갱신한다. 미수락 desired의 reserved나 전력, static routing을 실행하지 않는다.

원격 preparation은 별도 직렬 잠금에서 만들고 게시 직전 ConfigMap RV를 확인한다. 실패, 취소와 구버전 준비는 기존 accepted를 보존한다. full acceptance의 configVersion은 PUT ETag로 바꾸지 않는다. profile ticker는 이후 외부 변경을 수락하고 control loop는 accepted 입력으로 계속한다.

설정 intent는 effect 전에 기록하고 완료 기록 실패로 이미 수락한 effect를 rollback하지 않는다. 응답 본문과 RV는 mutation lock 안에서 동결하고 느린 HTTP write는 lock 밖에서 수행한다.

## API

| 경로 | 의미 |
|---|---|
| `GET /api/v1/scheduler` | settings, snapshot, decision, actions, results와 costIssues |
| `GET /api/v1/scheduler/settings` | 운영 설정과 읽기 전용 배분 선언 |
| `GET /api/v1/scheduler/state` | 현재 판단과 적용 상태 |
| `PUT /api/v1/scheduler` 또는 `/api/v1/scheduler/settings` | 설정 검증, 저장과 정책 수락 |
| `POST /api/v1/scheduler/operations` | prepare/remove/route/unroute/release/reset |
| `GET /api/v1/scheduler/profiles` | Query 소유 profile, preparation과 insufficiency |
| `GET /api/v1/scheduler/capacity` | 현재 선택안의 tested-rate 근거, 없으면 `{"tests":[]}` |
| `GET /api/v1/scheduler/catalog-read` | Query owner 전용 private reporting와 accepted snapshot |
| `GET /api/v1/namespaces/{namespace}/measurement-coverage` | Query 소유 현재 측정 커버리지 |
| `GET /api/v1/pools[/{uid}]` | 실제 소속. 없는 활성 풀은 PoolNotFound |
| `GET /api/v1/observations/load` | 모든 UID 유입과 실행 rate, assignment |
| `GET /api/v1/observations/scheduler` | 판단과 제어 원시 이력 |
| `GET /api/v1/observations/power` | raw 적분과 coverage, 전체와 활성 풀 |
| `GET /api/v1/observations/occupancy` | bounded fresh native-pair 진단 |

측정 생성과 삭제, profile 재사용, 공개 Query 응답은 [측정 API](measurement-api.md)에 둔다. ChillServer는 공개 Query route의 인증과 proxy를 소유하며 Query는 Serving 패키지를 import하지 않는다.

capacity의 항목은 instanceID, scalar input, 별도 pathFeedback, kappaHat와 tests다. test는 inputKey, rate, met, passed와 requests를 제공한다. κ̂는 검사한 rate 중 최대 통과 근거다. 빈 tests나0은 통과 근거 부재를 나타낸다. 미검사 rate를 보간하지 않는다. 과거 decision 상세는 해당 decision에서 조회한다.

## 현재 상태와 catalog

decision은 keep, target, Workload 제어 상태, capacity, search와 제외 이유를 연결한다. capacity는 period, route 시각, UID별 rate와 queued, profile routingRate와 retainedRoutingRate, 정수 weights, 선택 tests와 keepTests를 제공한다.

`ComparedNodes`는 keep 외 적격 계획이 있는 Node다. expectedJoules도 이 집합을 합한다. 전체 풀 실측과 구분한다. `missingCells`는 decision 전체에서 중복 제거하며 energyComparison과 undecidableCandidates는 정수 `missingCellRefs`로 참조한다. keep 충족 상태의 비-exact 새 operation 후보는 `replaySkipped:true`일 수 있다.

snapshot reservations는 owner, nodes, workloadUIDs, podUIDs, instances, frozen target과 reason이다. target은 예약 목표다. 실행 종료 뒤에도 unknown 원격 effect는 예약에 남는다. State는 현재 장부에서 예약을 재투영하되 snapshot.at과 과거 decision을 새 관측으로 표시하지 않는다.

인스턴스 `routable`은 현재 새 요청의 양수 분배와 Ready, Stopped, owner와 모델 적격성으로 계산한다. accepted 요청의 drain용 보존 route는 새 요청 가능으로 세지 않는다. Pod Ready와 새 정책 ACK도 별도다.

private catalog-read의 reporting과 accepted는 독립 revision이다. source 일부 실패나 preparation 문제는 per-source issue이며 필수 read와 설정, meter, 저장 실패는 adoption을 중단한다. 이전 accepted가 남아도 현재 reporting에 수락을 추정하지 않는다.

`accepted.dispatchOccupancy`가 실제 선택된 d와 provenance의 권위다. qualified measured, legacy dom, default 순이다. `accepted.occupancyEstimates`는 완료 rolling reduction의 signed 진단이고 `accepted.occupancy`는 legacy evidence identity다. 이 셋을 같은 채택 표로 합치지 않는다.

## 제어 시간과 탐색 진단

round는 호출 시간과 오류, Tick은 판단 처분을 소유한다. `control.tickAt`으로 round와 Tick을, `control.decisionAt`으로 Tick과 decision을 연결한다. 전체 분모에는 오류, Keep, fallback과 중도 반환을 포함한다.

| scope 또는 필드 | 측정 경계 |
|---|---|
| round | application lock 대기, Admit, Tick, unlock. admitError와 tick error 분리 |
| tick | controller lock 대기, capture, evaluate, publish, record, launch와 unlock |
| continue | capture와 publish, launch. 신규 평가 처분 없음 |
| capture | window, refreshList, sourceProbe, rest |
| window | barrier, scan, cache, decode, join, sort의 배타 하위 시간 |
| refreshList | Node, Pod, source resource, DeviceClass와 Service List |
| decision evaluationTiming | preparation, search, payback, rates와 projection의 배타 단계 |

완료 행 자신의 제출, 비동기 영속 완료와 물리 전환 완료는 그 행 elapsed 밖이다. round의 Tick 시간에는 Tick 완료 행 제출이 포함된다. 하위 시간과 backend 부분집합을 전체에 중복 가산하지 않으며 병렬 worker CPU 시간의 합으로 해석하지 않는다.

`search.startedAt/finishedAt`은 실제 탐색 UTC 경계, durationNanos는 같은 구간 monotonic이다. examined는 node-plan 수다. 이웃 탐색 evaluated는 초기 keep/empty를 뺀 sweep 평가 수이며 증분 전략의 exactEvaluated는 keep seed와 replay 생략도 포함한 정밀 판정 수다.

증분 surrogateElapsedNanos와 exactElapsedNanos는 search 안의 부분집합이다. 후자는 keep seed, 정밀 funnel과 fallback, backend 후처리를 포함한다. nominal-rate 진단 취소 뒤에도 이미 수행한 시간은 남는다. 탐색하지 않은 기록이나 과거 raw에 시간을 합성하지 않는다.

## 이력과 compact 조회

scheduler history는 `[from,to)` 사건시각을 필터한다. 기본과 asc는 append 순, desc는 사건시각 내림차순과 append offset 내림차순이다. `filter`는 all, decision-action, decision, decision-change, action, snapshot, settings, power, other다. decision-change는 target이 있고 그 target이 keep 계획이 아닌 decision, 곧 구성을 바꾸는 판단만 선택한다. Keep, Pending과 keep target의 Overload는 제외된다. uid는 명시 Workload 관계만 선택한다.

미지원 값과 중복 key, 빈 UID와 잘못된 문자열은 InvalidQuery다. cursor는 같은 query의 선택 결과 offset이며 `event.offset`은 필터 전 원본 offset이다. 원본 상세는 filter/order/uid 없이 limit1과 원본 offset으로 읽는다.

full은 원본이다. compact는 profile dictionary와 ref를 제공하고 현재 상태의 options와 arrivals를 null, 정확한 count로 투영한다. raw duration 벡터를 생략해도 durationSampleCount를 유지한다. history dictionary는 원본 profile을 유지하며 compact plan의 생략은 원본 기록을 바꾸지 않는다.

summary의 decision은 `at`, `status`, `reasons`(원본 decision과 같은 문자열, 없으면 생략), `targetKeep`(target이 keep 계획이면 true, 다른 계획이면 false, target이 없으면 생략), `evaluationElapsedNanos`(원본 decision의 판단 소요 시간, ns, 0이면 생략)와 `workloads`만 담는다. summary의 workloads는 저장 원본 순서와 reason을 유지하고 catalog 재복원으로 바꾸지 않는다. decoded request/client cache는 설정 rolling span을 보유하며 cold scan은 cache lock 밖이다. 과거 문서의 고정 행 수 상한을 적용하지 않는다.

power 조회는 accepted meter 구성을 atomic하게 고정하고 긴 scheduler 잠금을 기다리지 않는다. 실제 source, raw gap과 coverage가 판정 근거다. 오래된 source를 현재 meter로 대입하거나 결손을0으로 표시하지 않는다.
