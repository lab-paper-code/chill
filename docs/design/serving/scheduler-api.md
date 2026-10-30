# Scheduler 설정과 관측 API

설정·권한·조회 및 관측 기록 처리 계약이다. 2026-10-25 배분 작업 개정은 구현 목표이며,
아래 코드 위치는 변경할 현재 구현을 가리킨다(`SCH-36`, `SCH-39`, `SCH-41`).
판단·제어 불변식은 [스케줄러 구현 설계](scheduler-implementation.md), 실행기와 측정의
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
| `settings` | ns duration의 `observationWindowNanos`, `controlIntervalNanos`, `comparisonPeriodNanos`(keep·모든 후보에 고정한 H, sample10분; 자동연장 없음, [계획기](analytic-planner.md#4-노드-계획과-탐색)), `maxObservationAgeNanos`, 자원 상한 `maxReplicas`, `maxInstances`, `maxCPUs`, `servingMemoryBytes`, 제어 `controls`(`placement`,`resources`,`replicas`,`routing`,`batching`,`power`,`membership`). 미선언 입력은 거절 |
| `settings.servingMemoryBytes` | 서빙 Pod의 memory request와 limit은 profile의 측정 최대 메모리에 이 값을 더한 것이다. 절반(최대 64 MiB)이 firstfloor `maxQueueBytes`이고, Go의 기본 GOGC 100에서 heap이 대기 요청 바이트의 두 배까지 자라는 것을 나머지 절반이 받는다. 2 미만이면 서빙 준비가 `PreparationUnavailable`이다. sample의 128 MiB는 rasp5 q1(측정 198 MiB)이 포아송 40 req/s에서 대기 요청으로 OOMKilled된 m4 r2 사건의 대기열 64 MiB를 담는다 |
| `settings.completionMarginMS` | 필수, 유한한 0 이상의 ms. firstfloor 처리 완료에 요청마다 더하는 완료 여유이고 ChillServer가 dispatch 때 요청에 넣는다. sample의 `29`는 m4 r3 포아송 40 req/s 포화에서 잰 m(종단 경과 − (`nativeElapsedMS` − ingress))의 p95 29.2 ms다([용량표 계획기 5장](analytic-planner.md#5-완료-여유-규칙)). E1의 p99 138.23 ms는 요청마다 걸리던 TokenReview 지연이 대부분이었고, 인증 결과 재사용(c829518)과 authz singleflight가 그 지연을 없앴다 |
| `settings.capacityWindowNanos`, `capacityWindows`, `capacityPassingWindows`, `capacityMinimumRequests` | 필수. 용량표 행의 창 길이(> 0), 요청률마다 재생하는 창 수(≥ 1), 충족 창 수(1 이상 창 수 이하), 창의 최소 요청 수(≥ 1). sample은 60 s, 20, 19, 1,000이다([용량표 계획기](analytic-planner.md#2-용량표)) |
| `settings.planSearchWorkers` | 필수 정수≥1. 판단시작부터 controlIntervalNanos 절반 예산, 종료시 capacity.search.budgetCut. sample3/서버6CPU에서 G≈220req/s 게이트웨이≈1.7+나머지Tick≈.5core와 합쳐≈.8core 여유였다([자원 상한](workload-management.md#e5의-chillserver-메모리와-cpu-상한),[계획기](analytic-planner.md#4-노드-계획과-탐색)). E5 G 실측 뒤 선택한다 |
| `settings.autonomousWorkloads` | 자동제어할 Workload UID 목록. decision은 Workload마다 `workloads[]`에 `Disabled`, `MeasurementEvidenceMissing`, `Controlled` 상태와 사유를 남긴다([워크로드별 자동제어](workload-pools.md#31-워크로드별-자동제어)) |
| `settings.excludedNodes` | 계획에서 뺀 Node 이름 목록(이름마다 하나, 빈 이름 불가, sample은 빈 목록). 그 Node는 측정 POOL Node처럼 그대로 두며 판단의 노드 계획과 snapshot 노드 사유에 `ExcludedNode`로 남는다([용량표 계획기 14장](analytic-planner.md#14-구현-규칙)). Dashboard 운영 설정의 "계획에서 뺀 장비"가 고친다 |
| `reserved` | Node별 비서빙 전용 예약 CPU IDs와 GPU UUID. quota는 별도 수량 예산. CPU inventory는 node agent가 Node annotation `edge.dacs.io/cpus`에 게시한 online CPU이고, GPU inventory는 그 Node에서 측정한 profile의 GPU UUID다([node agent](../platform/node-agent.md#21-필드별-출처)). inventory 밖의 예약은 Node를 `PlacementInventoryMissing`으로 만든다 |
| `staticRouting` | 읽기 전용 projection: Workload UID → physical instance ID → nonnegative int32 weight. UID의 빈 map은 명시적 배분 해제이고 UID 부재는 Pod 기본 표시를 따르는 상태다. 선언 변경은 `route`/`unroute`/`release` 작업만 받으며 설정 PUT은 이 필드를 받지 않는다(`SCH-39`) |
| `staticRoutingValidatedAt` | 읽기 전용 선언 변경 출처. 설정 PUT이 아니라 배분 작업의 검증 시각과 연결한다. 실제 성공 여부는 작업의 ACK와 `actual.routing` readback으로 확인한다 |
| wall meter | 구성 필드가 없다. Profiler facts `metricSources`(schema `v1alpha6`)의 항목마다 `{id: meterID, node: 그 이름의 live Node와 UID, scope: node-exclusive-wall, source: 전력 부분}`을 만든다. Node가 없는 항목은 meter가 없다 |
| 전력 요청 | 별도 구성 필드 없음. DeviceClass의 비Fixed actuator·Profiler actuation policy가 요청 가능 모드를 정한다. 동작 시작에 Node mode label과 requested-at을 같은 optimistic patch로 쓰고 policy 기한 내 success·독립readback을 확인한다([현행 Node 전력 계약](../platform/node-agent.md#3-노드-전력모드-node-label과-generation)). NodeActuation은 제거됨 |
| `powerObservation` | 기존 sampler의 `Interval/Duration/RequestTimeout/MaxEvents`와 적분 coverage의 `MaximumBoundaryDistance/MinimumCoverage/MaximumGap` |

profile 후보, profile ID별 `PrepareSpec`, 원자 동작 비용과 power mode 비용은 조건마다 최신
완료 측정에서 도출하며 구성 필드가 없다. 측정 기록의 store key는
`serving/operation-measurements/<Node UID>/<operation ID>.json`이다. firstfloor
`Policy.fixedBatchSize`의 0 또는 생략은 dynamic이고 양수 B는 비교 기준 strict fixed-size
serving이며 worker metadata ack로 확인한다([strict fixed-size baseline](scheduler-runtime-measurement.md#strict-fixed-size-baseline-serving)).

배분 작업은 ConfigMap 선언 저장과 물리 적용을 구분하여 기록한다. 기존 typed
`ApplyTarget`의 준비·소유권·native policy ACK와 전체 physical readback을 재사용하고,
다른 UID의 선언과 실제 분배는 보존한다. 실패 또는 부분 적용은 성공으로 표시하지 않으며
작업의 actor, reason, 전후 선언, `sourceConfigRV`, 시작·완료 시각과 실제 상태를 남긴다.
수동 단일 UID와 자동제어 UID 목록 모두 `ApplySchedulerWorkloadsTarget`을 사용한다.
선언의 전후 기록은 바깥 map만 분리하며 기존 내부 weight map은 수정하지 않는다.
설정 refresh는 이 선언을 실행하거나 실패한 수동 작업을 자동 재시도하지 않는다.
작업 입력은 HTTP와 CLI의 공유 typed strict decoder에서 중첩 `node`의 미선언 필드까지
거절한다. 종류별 허용 필드 검사는 값이 `null`이나 0이어도 필드의 존재를 기준으로 적용한다(`SCH-39`).
Dashboard의 `scheduler-settings.tsx`도 설정 PUT에서 두 읽기 전용 필드를 제외한다.
`hack/e5`의 E5/demo/D2 설정 쓰기도 같은 입력 계약을 따른다. `reason`은
operations POST 전용이며 settings PUT에는 넣지 않는다. 설정 PUT의 미선언
`reason` 필드는 strict decode에서 400 `InvalidBody`(`field=body`)로 거절한다.
배분 편집은 바뀐 UID만 route/unroute/release POST로 순서대로 보내고,
실패/부분 적용에서 멈추며 서버의 실제 `OperationResult`를 보여준다. UI 재시도나 되돌림은 없다.

## API와 compact projection

| API | 동작 |
|---|---|
| `GET /api/v1/scheduler` | settings와 실제 snapshot, decision, actions, results, `costIssues` |
| `GET /api/v1/scheduler/settings`, `/state` | 운영 설정 또는 현재 판단·적용 상태. settings 응답의 `staticRouting`은 읽기 전용 선언이다 |
| `PUT /api/v1/scheduler` 또는 `/settings` | `SchedulerSettings`만 검증·저장·적용한다. `staticRouting`/`staticRoutingValidatedAt` 입력은 400 `InvalidBody`와 해당 `field`로 거절하며, 생략된 기존 선언은 변경하지 않는다. 설정 변경 기록은 actor, before/after, validatedAt, affected, appliedAt을 유지한다 |
| `POST /api/v1/scheduler/operations` | 기존 prepare/remove와 명시적 route/unroute/release/reset. 본문·ACK·실패·멱등 계약은 [수동 원자 실행](scheduler-operations.md#수동-원자-실행-계약)을 따른다 |
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

인스턴스 출력은 `routable: {"<workloadUID>": true|false}`를 Ready와 별도로 제공한다
(`SCH-36`). snapshot·POOL의 같은 physical instance는 같은 서버 계산값을 쓴다.
현재 Workload별 새 요청 유효 분배에 포함되고 가중치가 양수일 때만 참이며,
Ready/Stopped·소유권·모델 적격성은 기존 runtime 분배 필터를 따른다. 과거 접수 요청의
drain용 보존 route는 새 요청의 routable로 세지 않는다.

현재 `/scheduler`, `/scheduler/state`와 scheduler history는 선택적으로
`view=compact`를 받는다. 생략하면 기존 full raw 응답이며 저장 관측 기록, archive와
page/cursor 선택은 바꾸지 않는다. 다른 `view` 값은 `InvalidQuery`로 거절한다.
compact 응답은 같은 envelope에 `profiles` dictionary를 추가하고, snapshot의 instances/options/
reservations[].target 및 decision의 keep/target 안 인스턴스의 `profile`만
`{"$ref":"응답 내 key"}`로 치환한다. nil은 그대로다. dictionary에는 원본 profile 객체를
보존한다. profile ID가 key의 기준이며 같은 ID의 다른 보존 값은 별도 suffix key로 남긴다.
같은 값의 반복만 공유하며 equality는 admission/validation gate가 아니다. 소비자는 이
필드만 dictionary로 해석한 뒤 기존 화면 계약을 사용한다. SLO, 정책, 설정,
물리/선택 인스턴스 신원, 사유, 출처와 action acknowledgement 원본은 바꾸지 않는다.
응답 한 page의 얕은 DTO만 만들며 별도 history scan, raw evidence 복사, decoded cache
또는 fallback reader는 없다.

snapshot의 `reservations`는 `{owner,nodes,workloadUIDs,podUIDs,instances,target?,reason?}` 목록이다.
`target`은 owner의 frozen target이며 실제 상태는 snapshot의 instances/routing에서 읽는다.
전역 `inProgressTarget` 필드는 제거되었고 호환 alias는 없다. 실행 종료 뒤에도 원격 효과가
미확인이면 예약이 남는다. 예약 밖 판단은 계속되므로 예약 존재 자체를 전역 대기로 해석하지 않는다.

power 조회는 controller가 수락한 ChillServer configuration 하나를 atomic하게 읽어 meter
구성을 고정한다. 긴 refresh·control Tick의 scheduler mutex를 기다리지 않으며, 새 설정은
controller 적용 성공 뒤에만 게시한다. 거절된 refresh는 이전 설정을 그대로 유지한다.

라이브 채널 메시지 계약 v1은 [Dashboard 설계 §3](../dashboard/README.md#3-라이브-api-계약-v1)을 따른다.
라이브 Hub는 `Publish`의 Delta와 명시적 Removed로만 상태 변경을 발행하며 전체 교체 API는 없다. Dashboard tooltip은 dot indicator와 기본 label/value만 표시한다.

## 관측 조회의 일관성·모집단·제외 규칙

현행 시간 세그먼트·catalog·로컬/archive 선택과 요청 reducer는 [관측 분리 §3~6](observability-split.md)이 권위다. 폐기된 window metadata/census/prefix reader 계약은 적용하지 않는다.
- writer mutex에서 stream fd·길이·시간을 고정하고 읽기/decode/집계는 밖에서 한다. 새 append·rotation을 섞지 않으며 실제 손상/read 오류는 health·incomplete로 남긴다. 별도 저장index·decoded cache는 없다.
- scheduler page는 `[from,to)` 사건시각을 append 순으로 읽고 다음 일치1건으로 continuation을 확인한다. cursor의 matching-offset 의미를 유지하며 append wall time을 사건시간 필터로 쓰지 않는다.
- power의 짧은 collector verdict는 조회 coverage의 대체물이 아니다. 실제 sample·원 gap·경계거리·meter/scope/source identity·보관 완전성·조회구간을 검사한다. 경계 밖은 기록된 boundary 거리 안에서만 보조하고 오래된 이전 표본으로 정상 닫힌 구간을 무효화하지 않는다. source 실패·OOM/재시작 공백은 결손이다.
- source kind/endpoint/metric/labels가 다른 과거 기록을 현재 meter 실측으로 재해석하지 않는다. 누락정의·역순시각·알수없는 원시를 빈성공으로 바꾸거나 legacy window reader로 fallback하지 않는다.
- JSON projection은 `encoding/json.Unmarshal` 한 경계에서 건너뛴 subtree까지 구문검사하고 실제 float/정수/duration/시각과 선택원문은 표준decoder 의미를 보존한다. 별도 구문 검증 pass는 없다. Scanner는 stream별 buffer를 재사용하고 newline 경계를 유지한다.

## 설정 권한과 적용 기록

설정 변경은 검증된 identity의 groups/extra까지 사용하여 해당 namespace/name의
`configmaps/update` Resource SAR를 추가 확인한다. decision과 snapshot을 관측 기록에
보존한 뒤 physical action을 시작하고, 실제 시작·완료·실패와 부분 상태를 별도로 기록한다.

## 구현 위치와 검사

- `internal/serving/httpapi/scheduler.go:Handler.scheduler`, `persistSchedulerSettings`:
  설정 PUT의 선언 입력을 거절하고 읽기 전용 GET은 유지한다.
- `internal/serving/kube/static_routing.go:manualRoutingOperationLocked`,
  `persistManualConfiguration`, `executeManualRouting`: 명시적 작업의 선언·ACK/readback을 실행한다.
- `internal/serving/runtime_scheduler.go:Runtime.routeWeightLocked`,
  `applySchedulerTargetLocked`, `schedulerRoutingLocked`: UID의 빈 선언과 부재를 구별한다.
  release는 전역 `DisableSchedulerRouting`이 아니라 해당 UID만 제어 map에서 제거하고
  Pod 기본 표시를 다시 읽어 rebuild/dispatch한다. 현재 ApplyTarget이 덮어쓰는
  `instance.Routing`과 기본 표시를 혼동하지 않도록 현재 Pod annotation을 원본으로 삼는다.
- 검사: 설정 PUT 선언 입력 거절·다른 UID 보존, route/unroute/release 각각의 ACK와
  전체 readback, Ready지만 unroute인 인스턴스의 routable=false,
  release 뒤 새 측정 인스턴스의 routable=true와 실제 요청 처리를 확인한다
  (`SCH-36`, `SCH-39`, `SCH-AC-32`).
