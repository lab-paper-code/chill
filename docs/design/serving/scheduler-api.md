# Scheduler 설정과 관측 API

현재 설정·권한·조회 및 관측 기록 처리 계약이다. 판단·제어 불변식은
[스케줄러 구현 설계](scheduler-implementation.md), 실행기와 측정의
해석은 [실행기와 측정 계약](scheduler-runtime-measurement.md#시간과-수명)을 따른다.
외부 Workload HTTP·인증·리소스 변경은 [Workload 관리](workload-management.md#5-api와-권한),
기본 JSONL/S3 보존·집계 모집단은 [관측과 보존](workload-management.md#11-관측과-보존)이 원본이다.

## 구성 입력

HTTPS ChillServer는 `--serving-scheduler-configmap=<name>`과
`--serving-scheduler-config-key=scheduler.json`으로 Serving namespace의 구성을 읽는다.
빈 ConfigMap 이름은 기존 Workload 관리와 추론만 실행한다. scheduler는
`settings.autonomousWorkloads`의 Workload만 자동제어하고, 나머지 Workload는 동일 관측과 판단 기록 아래 현재 배치를 유지한다.

| 구성 필드 | 의미와 실제 근거 |
|---|---|
| `settings` | nanosecond duration의 `observationWindowNanos`, `controlIntervalNanos`, `comparisonPeriodNanos`(비교 기간 T의 하한, sample 10분, [용량표 계획기 4장](analytic-planner.md#4-노드-계획과-dp)), `maxObservationAgeNanos`, 자원 상한 `maxReplicas`, `maxInstances`, `maxCPUs`, `servingMemoryBytes`와 제어 활성 상태 `controls`(`placement`, `resources`, `replicas`, `routing`, `batching`, `power`, `membership`). 구성과 `PUT` 본문은 선언한 필드만 받는다 |
| `settings.servingMemoryBytes` | 서빙 Pod의 memory request와 limit은 profile의 측정 최대 메모리에 이 값을 더한 것이다. 절반(최대 64 MiB)이 firstfloor `maxQueueBytes`이고, Go의 기본 GOGC 100에서 heap이 대기 요청 바이트의 두 배까지 자라는 것을 나머지 절반이 받는다. 2 미만이면 서빙 준비가 `PreparationUnavailable`이다. sample의 128 MiB는 rasp5 q1(측정 198 MiB)이 포아송 40 req/s에서 대기 요청으로 OOMKilled된 m4 r2 사건의 대기열 64 MiB를 담는다 |
| `settings.completionMarginMS` | 필수, 유한한 0 이상의 ms. firstfloor 처리 완료에 요청마다 더하는 완료 여유이고 ChillServer가 dispatch 때 요청에 넣는다. sample의 `139`는 E1(rasp5 CPU, 2 req/s 60분, n=8312)의 `completionResidualNanos` p99 138.23 ms를 올림한 값이다([워크로드별 POOL](workload-pools.md#36-완료-여유)) |
| `settings.capacityWindowNanos`, `capacityWindows`, `capacityPassingWindows`, `capacityMinimumRequests` | 필수. 용량표 행의 창 길이(> 0), 요청률마다 재생하는 창 수(≥ 1), 충족 창 수(1 이상 창 수 이하), 창의 최소 요청 수(≥ 1). sample은 60 s, 20, 19, 1,000이다([용량표 계획기](analytic-planner.md#2-용량표)) |
| `settings.autonomousWorkloads` | 자동제어할 Workload UID 목록. decision은 Workload마다 `workloads[]`에 `Disabled`, `MeasurementEvidenceMissing`, `Controlled` 상태와 사유를 남긴다([워크로드별 자동제어](workload-pools.md#31-워크로드별-자동제어)) |
| `settings.excludedNodes` | 계획에서 뺀 Node 이름 목록(이름마다 하나, 빈 이름 불가, sample은 빈 목록). 그 Node는 측정 POOL Node처럼 그대로 두며 판단의 노드 계획과 snapshot 노드 사유에 `ExcludedNode`로 남는다([용량표 계획기 14장](analytic-planner.md#14-구현-규칙)). Dashboard 운영 설정의 "계획에서 뺀 장비"가 고친다 |
| `reserved` | Node별 비서빙 전용 예약 CPU IDs와 GPU UUID. quota는 별도 수량 예산. CPU inventory는 node agent가 Node annotation `edge.dacs.io/cpus`에 게시한 online CPU이고, GPU inventory는 그 Node에서 측정한 profile의 GPU UUID다([node agent](../platform/node-agent.md#21-필드별-출처)). inventory 밖의 예약은 Node를 `PlacementInventoryMissing`으로 만든다 |
| `staticRouting` | OPTIONAL 부분 map: 자동제어하지 않는 현재 Workload UID → physical instance ID → nonnegative int32 weight. positive weight가 없는 항목(빈 map 포함)은 그 UID의 routing을 모두 지운다. 선언하지 않은 UID는 현재 routing을 유지한다. 명시 config revision을 기존 typed ApplyTarget 경로로 적용하고 physical read-back하며 `manual-static-routing` action의 `sourceConfigRV`에 남김. 현재가 아닌 UID, `autonomousWorkloads`의 UID와 stale target은 오류 |
| wall meter | 구성 필드가 없다. Profiler facts `metricSources`(schema `v1alpha6`)의 항목마다 `{id: meterID, node: 그 이름의 live Node와 UID, scope: node-exclusive-wall, source: 전력 부분}`을 만든다. Node가 없는 항목은 meter가 없다 |
| 전력 요청 | 구성 필드가 없다. Node의 DeviceClass 모드 중 actuator가 `Fixed`가 아니고 Profiler facts의 actuation policy가 있는 모드가 요청 가능 모드다. 전력 동작이 시작할 때 NodeActuation을 새 이름, 그 시각의 `stageRequestedAt`, `maxActuationDurationSeconds` 뒤의 `deadline`으로 만든다([node agent](../platform/node-agent.md#21-필드별-출처)) |
| `powerObservation` | 기존 sampler의 `Interval/Duration/RequestTimeout/MaxEvents`와 적분 coverage의 `MaximumBoundaryDistance/MinimumCoverage/MaximumGap` |

profile 후보, profile ID별 `PrepareSpec`, 원자 동작 비용과 power mode 비용은 조건마다 최신
완료 측정에서 도출하며 구성 필드가 없다. 측정 기록의 store key는
`serving/operation-measurements/<Node UID>/<operation ID>.json`이다. firstfloor
`Policy.fixedBatchSize`의 0 또는 생략은 dynamic이고 양수 B는 비교 기준 strict fixed-size
serving이며 worker metadata ack로 확인한다([strict fixed-size baseline](scheduler-runtime-measurement.md#strict-fixed-size-baseline-serving)).

`staticRouting`의 configuration 수신과 실제 적용은 별개다. 물리 source readiness가
아직 없거나 UID/target 검증이 실패하면 ChillServer listener를 막지 않고 기존 refresh
cadence에서 미적용 revision을 다시 평가한다. 실패는 선언
원본, `sourceConfigRV`, 실제 routing과 오류를 가진 manual action으로 관측 기록과 scheduler
state에 남으며 적용 성공으로 표시하지 않는다. 전체 physical read-back이 일치한
revision은 다시 적용하지 않는다.

## API와 compact projection

| API | 동작 |
|---|---|
| `GET /api/v1/scheduler` | settings와 실제 snapshot, decision, actions, results, `costIssues` |
| `GET /api/v1/scheduler/settings`, `/state` | 운영 설정 또는 현재 판단·적용 상태 |
| `PUT /api/v1/scheduler` 또는 `/settings` | 직접 `SchedulerSettings`를 검증하고 named ConfigMap에 저장하고 적용 |
| `GET /api/v1/scheduler/profiles` | 읽기 전용. `configVersion`, 완료 측정에서 도출한 조건별 최신 `profiles`, profile ID별 `preparations`, profile을 만들지 못한 측정의 `insufficiencies[]`(CatalogIssue `source/code/reason`) |
| `GET /api/v1/scheduler/capacity` | 읽기 전용. 자동제어 Workload에 필요한 용량표 `rows[]`(`key`, `input`, `status`, `reason`, `mu`, `kappa`, 격자와 이분 탐색 판정, `builtAt`, `elapsedNanos`). 계산 중인 행은 `CapacityPending`, 단조가 아닌 행은 `CapacityRejected`와 `NonMonotone`이다. 표는 메모리에만 있고 행마다 `management` stream의 `capacity-row-built` 사건으로 남는다 |
| `GET /api/v1/pools` | 실제 CHILL 인스턴스의 owner와 binding에서 도출한 `workloads[]`(`workload`, 활성 POOL `nodes`, `instances`), 측정 taint Node의 `measurement[]`, `available[]`와 `other[]`(`node/ready/powerMode/reasons`), `observedAt`, observer `issues` |
| `GET /api/v1/pools/{uid}` | 해당 Workload의 활성 POOL 하나. 없으면 `PoolNotFound` |
| `GET /api/v1/observations/load` | 사건 구간의 모든 UID 유입·접수·배정·완료율과 instance assignment 집계 |
| `GET /api/v1/observations/scheduler` | 고정 `from/to`, bounded `limit`, append-order `cursor/nextCursor`의 원시 기록 |
| `GET /api/v1/observations/power` | 같은 고정 `from/to`의 raw 표본을 Node당 한 번 적분한 전체·활성 POOL J/평균 W; 누락은 null과 사유 |

`/scheduler/state`의 `costIssues[]`(CostIssue `source/code/reason`)는 비용에 기여하지 못한
측정이다. decision은 `keep`, `target`, `workloads[]`, `capacity`, snapshot Node의 `measurement`와 `reasons`,
`search.examined`, `search.comparedNodes`를 담는다. `capacity`는 비교 기간, 고른 route 시각, Workload별 κ 합과 판정,
가중치, 사용한 용량표 행, 모든 노드 계획과 제외 사유다([스케줄러 구현 설계 5.3절](scheduler-implementation.md#53-전환에너지선택)).
`comparedNodes`는 keep 밖의 적격 노드 계획이 있는 Node이며 `expectedJoules`는 이 Node만 합산한다.
관측 기록의 인스턴스는 profile을 `profileID`로, Workload를 `workloadUID` 하나로 보존한다.

현재 `/scheduler`, `/scheduler/state`와 scheduler history는 선택적으로
`view=compact`를 받는다. 생략하면 기존 full raw 응답이며 저장 관측 기록, archive와
page/cursor 선택은 바꾸지 않는다. 다른 `view` 값은 `InvalidQuery`로 거절한다.
compact 응답은 같은 envelope에 `profiles` dictionary를 추가하고, snapshot의 instances/options/
inProgressTarget 및 decision의 keep/target 안 인스턴스의 `profile`만
`{"$ref":"응답 내 key"}`로 치환한다. nil은 그대로다. dictionary에는 원본 profile 객체를
보존한다. profile ID가 key의 기준이며 같은 ID의 다른 보존 값은 별도 suffix key로 남긴다.
같은 값의 반복만 공유하며 equality는 admission/validation gate가 아니다. 소비자는 이
필드만 dictionary로 해석한 뒤 기존 화면 계약을 사용한다. SLO, 정책, 설정,
물리/선택 인스턴스 신원, 사유, 출처와 action acknowledgement 원본은 바꾸지 않는다.
응답 한 page의 얕은 DTO만 만들며 별도 history scan, raw evidence 복사, decoded cache
또는 fallback reader는 없다.

power 조회는 controller가 수락한 ChillServer configuration 하나를 atomic하게 읽어 meter
구성을 고정한다. 긴 refresh·control Tick의 scheduler mutex를 기다리지 않으며, 새 설정은
controller 적용 성공 뒤에만 게시한다. 거절된 refresh는 이전 설정을 그대로 유지한다.

## 관측 조회의 일관성·모집단·제외 규칙

ObservationLog 조회는 기존 writer mutex 아래에서 필요한 stream 경로·byte 길이와 window
상태를 한 번 함께 캡처하고, 파일 읽기·JSON decode·집계는 잠금을 푼 뒤 수행한다.
완료된 append 뒤의 길이를 고정하므로 새 append·rotation을 현재 조회에 섞거나
진행 중 record를 잘린 tail로 오인하지 않는다. 실제 read 오류·잘린 원본은 health와
incomplete 상태에 그대로 남는다. 기존 시작 시 전체 stream 검증·decode pass와 성공한
scheduler/request/definition append에서 window당 작은 in-memory census를 채우고, 동일 잠금 아래 값과
검증된 prefix byte 수를 캡처한다. 별도 저장 index나 decoded history cache는 없다.
실제 파일 길이가 census prefix와 다르거나 census를 신뢰할 수 없으면 해당 prefix
전체를 보수적으로 읽는다. window metadata 전체는 gap·완전성 판정용으로 유지한다.
scheduler history는 실제 `SchedulerObservation.At` min/max가 `[from,to)`와 만날 수
있는 window를 append 순서로 읽고, 사건 시각 envelope로 요청 page만 전체 decode한다.
다음 일치 record 하나로 continuation을 확인하며 matching-offset cursor 의미를 유지한다.
window wall time은 append 시각이지 사건 시각의 범위가 아니므로 필터에 쓰지 않는다.
power는 실제 기록 구간의 겹침(표본 0개 포함), coverage boundary 거리로 확장한 실제
sample 시각 범위, 모든 nil-policy window, 활성 POOL 인스턴스 전환과 최신 prior baseline의
후보 window를 합쳐 읽는다. baseline 동률·시각 범위를 가로지르는 window도 보존한다.
정확한 Node·meter·scope·source와 가장 가까운 유효 표본 선택은 기존 reducer가 담당하며,
서로 다른 series의 전역 최근접 표본으로 nil-policy window를 제거하지 않는다.
집계에 필요한 실제 표본, meter, coverage와 활성 POOL 인스턴스만 decode한다.
capture의 arrivals/load는 receipt 사건의 실제 `ReceivedAt`(없으면 `At`)와 나머지 사건의
`At` 범위로 request prefix를 선택한다. 선택될 receipt가 있을 수 있는 최초 prefix 뒤는
`ReceivedAt` 없는 늦은 definition/accepted/outcome 사실도 합치도록 끝까지 유지한다.
두 조회 모두 전체 definitions와 window metadata를
보존하고, 오래된 append window·역순 시각·알 수 없는 prefix를 빈 성공으로 바꾸지 않는다.
definition은 기존 validation/counting pass에서 기록 유무만 보존하며, prefix byte 수가
일치하고 신뢰되는 빈 stream만 열지 않는다. 사건 시각과 무관하게 모든 nonempty 과거
definition prefix는 유지한다. 정의 추가·알 수 없는/길이가 바뀐 prefix는 다시 읽으며,
Runtime의 retained-definition registry를 복제하거나 별도 정의 cache를 만들지 않는다.
두 작은 projection은 기존 jsoniter 의존성으로 읽되, 건너뛴 subtree의 잘못된
숫자·문자열·구분자도 표준 JSON 구문 검사로 거절한다. power·coverage의 작은 실제
측정 block과 선택된 history 원문은 계속 표준 decoder로 읽어 float·정수·duration·
시각 의미를 보존한다. 큰 decision·profile payload와 원본 기록은 변경하지 않는다.
각 stream 조회는 window가 바뀌어도 성장한 Scanner buffer 하나를 재사용하고 newline
경계를 유지한다. 같은 크기의 후속 record마다 fragment·전체 line·window별 큰 buffer를
새로 할당·복사하지 않는다.

전력 collector의 짧은 기록 구간별 적분 verdict는 원시 기록에 그대로 보존하되,
조회 구간의 coverage 판정으로 재사용하지 않는다. 조회 경계 밖의 실제 표본은 기록된
boundary 거리 안에서만 보조하며, 합친 표본의 원래 gap·값·meter identity·보관 완전성과
조회 구간의 coverage를 검사한다. 조회 중 실제 source 실패와 OOM/재시작 공백은
누락으로 남기며, 이전 구간의 오래된 표본 때문에 정상적인 닫힌 구간을 무효화하지 않는다.
닫힌 ObservationLog window의 겹침은 예정 `EndsAt`보다 이른 실제 `ClosedAt`에서 끝난다.
전력, 활성 POOL 전환과 arrival 조회가 같은 경계를 사용한다. boundary 표본은
현재 선언의 Node·meter·scope와 source kind/endpoint/metric/labels가 일치한 뒤에만
선택하며, source가 없거나 바뀐 과거 기록을 현재 meter의 실측으로 재해석하지 않는다.

## 설정 권한과 적용 기록

설정 변경은 검증된 identity의 groups/extra까지 사용하여 해당 namespace/name의
`configmaps/update` Resource SAR를 추가 확인한다. decision과 snapshot을 관측 기록에
보존한 뒤 physical action을 시작하고, 실제 시작·완료·실패와 부분 상태를 별도로 기록한다.
