# 측정 커버리지·실험 대시보드 API 한계 해소 — B0

- 날짜: 2026-10-30. **B0 설계 확정; B1~B3 구현·배포, F4 소비·수용 전**.
- 사용자 요청: 백엔드 조치도 워크플로우 의존성을 고려하여 대시보드 작업 사이에 넣는다.
- 화면 출발점: [측정 UX 계획](2026-10-30-dashboard-measurement-ux.md),
  [Dashboard 설계 §8](../design/dashboard/README.md#8-측정실험-ux와-커버리지-2026-10-30),
  [Dashboard SRS](../spec/dashboard/SRS.md) DB-FR-56~65.
  이 문서는 그 화면/판정 기준을 대체하거나 선행 UI 작업을 막지 않는다.
- 새 서버/게시자 요구: [catalog SRS §9](../spec/catalog/SRS.md#9-측정-커버리지-조회-api) PC-07~13.
- API 정본: [측정 API §12](../design/serving/measurement-api.md#12-대시보드-측정-api-보강-계약-b0-2026-10-30),
  [관측 설계 §18](../design/serving/observability-split.md#18-실험-부하-단계기록-당시-identity-공개-b0b3-2026-10-30).
- B0는 문서만 변경한다. 코드·CR·저장 객체·클러스터·이미지·publication을 변경하지 않는다.

## 1. 건별 결론

‘저장됨’은 CR status, evidence raw/result, 관측/동결 입력, publication을 구분한다.
**완성된 summary 필드가 없다는 이유로 이미 있는 원시를 다시 측정하지 않는다.**

| 한계 | 이미 저장된 사실 / 미기록 | 최소 변경 | 저장 계약 변경 | 슬라이스·요구 |
|---|---|---|---|---|
| 1. runtime 지원 판정 | ModelSpec/path, DeviceClass, Node 및 trusted execution-facts ConfigMap은 존재. inventory가 조인 판정을 하지 않음. 실제 runtime 가동 보장은 미측정 | 정확 model/path/class/Node 1건의 bounded GET 정적 호환성. 기존 resolver/modelcompat 재사용 | 없음. 새 지원 registry·snapshot 저장 없음 | B1, PC-07 |
| 2. 실패 BEO→Run 소유 연결 | Run metadata.controller owner UID와 mode/batch/attempt labels는 생성 때 저장됨. public Run projection에서 누락. 실패 BEO result:null은 정상 | 기존 Run 목록에 ownerReferences와 기존 4 labels 공개. BEO UID exact join | 없음. CRD/owner/result writer 불변 | B1, PC-08 |
| 3. 배치 CI·통합 p95 | raw의 client latency vector, active/idle trial, frozen plan과 정책 보존. CI는 기존 유도 중 계산되지만 sealed result.detail에는 평균만 남음. pooled p95 summary는 미생성 | result 응답 형제 statistics read-projection. 기존 verified loader와 curve 유도·Gonum quantile로 계산, sealed detail은 불변 | 없음. **생성·result schema 변경을 택하지 않음** | B2, PC-09~10 |
| 4. OpCost 실측 시간/J | immutable operation record에 actual startedAt/endedAt, wallJoules·수용/사유·identity 존재. CR status/public projection에는 name/key/verdict뿐 | CR 정확 step key를 읽어 list/detail status.steps에 실제 ns/J·operationID·reason 노출 | 없음. operation/raw/status 계약 불변 | B2, PC-11 |
| 5. 실험 α·historical identity | E5/demo의 frozen binding/schedule/phase/captured definitions에는 대체로 존재하나 publication에서 누락. direct-rate α, 일부 producer anchor/혼합 identity는 애초에 없거나 단일값 확정 불가 | **두 publisher**가 미래 publication의 기존 KPI 행에 동일 metadata를 투영. GET는 기존 opaque JSON 전달 그대로 | 신규 optional KPI 의미 추가만 캠페인 사이 1회. envelope/prefix/version/관측/raw는 불변; 과거 재게시 없음 | B3, PC-12~13 |

### 1.1 정직하게 남기는 한계

- runtime Compatible은 **선언에 근거한 정적 지원**이지 실제 실행/Ready/CPU·mode admission/전처리·dataset/근거 완비 인증이 아니다.
  누락·모호한 선언은 ‘지원 확인 불가 — 이유’. 현재 전 범위 지원 registry를 새로 만들지 않는다.
- Run owner/attempt가 보존되지 않은 과거 객체는 ‘소유 연결 기록 없음’/‘회차 기록 없음’이다.
  이름 유사성으로 복구하거나 실패 result를 합성하지 않는다.
- 보존 raw/Run이 없으면 ‘통계 원근거 기록 없음’, 통계 입력이 없는 방법은 ‘CI·통합 p95 기록 없음’이다.
  store/권한/invalid evidence 오류는 그 문구로 숨기지 않는다. CI는 독립 trial 추정이고 지연 표본 수와 다르다.
- OpCost 불수용 단계의 J는 null/‘수용된 에너지 없음’, 유효 actual interval 부재는 ‘실측 시간 기록 없음’이다.
  단계의 실측 수치와 전체 sequence 실패는 함께 표시한다.
- 과거 실험 α/identity는 ‘α 미제공’/‘기록 당시 identity 미기록’; 직접 rate 입력은 ‘직접 요청률 설정 — α 없음’이다.
  현재 Workload나 offered/기간으로 채우지 않는다. 예정 substage 경계를 관측된 사건이라고 쓰지 않는다.

## 2. 소스 근거와 현재 GET 단면

### 2.1 저장→투영 근거

| 건 | 권위 소스와 확인한 흐름 |
|---|---|
| 1 | `internal/operatorconsole/inventory_view.go:10-25,70-74`는 별도 LIST RV와 호환성 미계산을 명시. `internal/profiling/profiler/kube/candidate_resolver.go:66-139`는 ModelSpec/DeviceClass/ConfigMap 선택→modelcompat→artifact architecture 검사. `execution_facts.go:125-213`은 class/Node/family/backend별 유일 선언 선택. `internal/modelcompat/evaluate.go:15-72`는 Compatible/Incompatible/Unknown과 사유를 이미 구현 |
| 2 | `internal/profiling/campaign/controller/run_builder.go:20-23,90-104`가 네 labels와 BEO controller owner를 생성. `internal/operatorconsole/profiling_runs_view.go:30-38,50-56`는 metadata 소유 정보만 빼고 spec/status를 공개. `batch_energy_optimization_result_view.go:56-88`는 reference 없는 result:null 유지 |
| 3 | `internal/profiling/profiler/projection/projection.go:48-92`의 trial client latency vector, `internal/profiling/derivation/derive.go:118-208`의 active J/item·CI, `statistics.go:12-69`의 Gonum empirical quantile·Student-t/shared-idle variance. `types.go:118-143`의 PointConfidence, `energy_optimization.go:36-53,87-100`의 평균만 남기는 detail. `internal/serving/kube/catalog.go:292-409`는 completed references→verified active/idle→기존 projection/유도의 선례. 새 API는 현재 catalog latest/reuse 선택을 통계 원본으로 쓰지 않음 |
| 4 | `internal/serving/kube/measurement.go:23-59,139-195,419-453`의 저장 operation record와 actual-window 비용. `opcost.go:155-199`의 CR UID/index/name operation ID와 step key/status. `internal/serving/httpapi/measurement_projections.go:58-68,93-110` 및 `cmd/chill-server/measurement_service.go:89-117`은 현재 CR-only projection. 기존 accepted cost/idle 계약은 유지 |
| 5 | `internal/serving/httpapi/experiments.go:19-39,59-77`, `cmd/chill-server/experiment_service.go:89-122`는 bounded immutable summary와 RawMessage KPIs만 읽음. `hack/e5/e5.py:1341-1354,1420-1448,2399-2412,2654-2678`은 frozen UID/definitionID·α/part와 publisher allowlist 누락. `hack/e5/demo.py:365-398,795-855,911-948`는 captured historical definitions·producer-relative part 창과 두 번째 publisher. `internal/serving/observation_log.go:53-65,124-130`도 request workloadUID/definitionID와 definitions를 이미 보존 |

Go 핵심 정의는 repository-pinned `bin/gopls-v0.23.0 definition`으로 찾고 해당 소스를 확인했다
(BuildProfilingRuns, selectExecutionDeclaration, modelcompat.Evaluate, PointConfidence,
OperationMeasurement, ExperimentStage). Graphify query는 초기 탐색용으로만 사용했다.
controller event 전달/실제 package edge/배포 topology를 이 조사로 검증했다고 주장하지 않는다.

### 2.2 GET-only 관측

2026-10-30 **07:40 UTC부터** Python urllib HTTPS GET으로 관측했다. 지정 CA를 검증하고
지정 token 파일로 Authorization을 전송했으며 token/CA 내용은 출력·문서화하지 않았다.
POST·PUT·DELETE·kubectl 변경·raw export·inference·측정 재실행은 하지 않았다.
여러 조회는 atomic snapshot이 아니다.

| 조회 범위 | 관측 |
|---|---|
| `/api/v1/inventory?limit=200` | models 3, classes 8; 양쪽 page 전부 수집 |
| `/api/v1/namespaces/chill-system/batch-energy-optimizations` | 88개: Complete 80, Failed 8 |
| `/api/v1/namespaces/chill-system/profiling-runs?limit=200&offset=...` | 총 1155행/서로 다른 UID 1155개, 6 page 끝까지 읽음; 모든 public Run에서 owner 연결 필드 부재 |
| 실패 `genesis2-010-smolvlm-cuda-q1/result` | result:null, campaign에는 owner Run 참조 없음 |
| 완료 `genesis2-006-mnv2-cpu-q1/result` | detail.incrementalWorkByBatch 행은 batch/평균 J/item뿐, CI/통합 p95 없음 |
| `/api/v1/namespaces/chill-system/operation-cost-measurements` | 35개. 첫 Complete 항목 단계 status는 name/key/verdict뿐 |
| `/api/v1/experiments` | 6개. E5 requests shape 4개, demo workloads shape 2개. 첫 stage KPI와 rules에서 공통 α·historical UID/definition metadata 미제공 확인 |

이 단면은 public projection의 누락을 확인한다. 모든 원시 객체를 다운로드하거나 현재 runtime 지원,
전체 35개 비용의 수치 유효성·모든 과거 실험의 원근거 존재를 검증한 것은 아니다.
저장 사실의 판정은 위 writer/reader source에 근거하며 실제 잔존 여부는 B1/B2 exact read에서 구분한다.

## 3. 최소 API·경계 계약

전체 필드·단위·null 규칙은 설계 정본을 따른다. 구현 분담용 요약은 다음과 같다.

| 슬라이스 | 외부 계약 | 입력/원근거를 검증하는 경계 |
|---|---|---|
| B1 호환성 | 새 GET `/api/v1/model-compatibility` 필수 query modelSpec/executionPath/deviceClass/node. `{readAt,selection,inputs,verdict,reasons}`. 기존 verdict/reason 재사용 | query 이름/중복·unknown key와 API identity·class membership을 Console 읽기 경계에서 1회; trusted declaration 해석은 기존 adapter. callback으로 core/transport 방향 유지 |
| B1 owner | 기존 Run 목록 runs[].ownerReferences와 labels(기존 네 key), 원 spec/status/page 유지 | 저장 metadata 원문 투영. 소유 join은 namespace+controller apiVersion/kind+**UID**, label은 소유 증거 아님 |
| B2 통계 | 기존 BEO result 응답의 형제 statistics/statisticsReason; batches의 mean J/item·PointConfidence·trial 수·latencyP95Milliseconds/표본 수·sources | completed result references와 stored curve plan을 기존 loader/projection으로 verified 타입으로 변환. downstream 재검증·새 hash/bytes 관문 없음 |
| B2 비용 | 기존 list/detail status.steps의 operationID, nullable durationNanos/wallJoules, reason | serving/kube의 exact-key loader가 CR UID/index/kind/Node·request/result·actual window를 확인. Accepted cost/idle와 failure history 구분 |
| B3 실험 | 기존 두 shape KPI workload 행의 workloadUID/definitionID/identitySource, substages[].id/alpha/startOffsetNanos/endOffsetNanos/from/to/source와 missingReasons | frozen binding/schedule→publication 조립 때 finite α≥0·이름/alias·offset/window/source를 1회 확인. 기존 Go envelope reader는 opaque pass-through |

BEO statistics 없는 경우 NoPublishedResult/RetainedEvidenceMissing/StatisticsNotRecorded를 구분한다.
권한/transport/깨진 객체는 원래 실패 응답이다. raw 부족을 재측정 자동 요청으로 바꾸지 않는다.
기존 byte count 기반 bounded I/O는 유지하되 재인코딩 byte equality·digest 검증 gate를 새로 만들지 않는다.
BEO의 선택 결론/bestBatch와 통계는 별도이며 검색/admission/accepted 판정을 다시 결정하지 않는다.

## 4. 저장 계약·ADR 판단

- **B1/B2: 저장 계약 변경 0.** CRD/status, raw 실행 증거, BEO result v1alpha4/detail,
  operation v1alpha2, frozen bundle은 그대로다. 보존 자료를 읽을 때만 계산·투영한다.
  result writer에 CI/p95를 영구 추가하는 더 작은 writer diff도 가능하지만, 이미 존재하는 원시를
  노출하라는 이번 범위와 과거 결과 보존을 위해 선택하지 않는다. 새 cache/통계 저장소도 없다.
- **B3: publication의 신규 optional KPI metadata 의미만 추가.** 기존 envelope가 opaque KPI를
  허용하므로 schema/prefix/version은 유지하며 strict single reader로 신/구 내용을 읽는다.
  구형 decoder 분기를 만드는 것이 아니라 optional metadata의 부재를 ‘기록 없음’으로 처리한다.
- ADR 0019: 측정 캠페인 중 저장 의미 변경 금지. 두 publisher의 metadata cutover를 캠페인 사이
  한 작업 단위에 합친다. 과거 working object 정리/재측정은 B0의 요구가 아니며, 다른 이유로
  형식을 바꿔야 한다면 영향 CR/파생 객체를 별도 열거하고 운영자가 캠페인 종료 뒤 정리한다.
- ADR 0030: observation v1alpha5·기존 네 stream·추가 resources/cluster 계약 불변.
  **기존 publication #213~#225를 수정/재게시하지 않는다.** 새 publication은 새 experiment ID,
  해당 revision의 동결 입력·원 출처를 사용한다. 과거 원시의 존재는 과거 summary 덮어쓰기 허가가 아니다.
- ADR 0031: 현재 default/UID/RV를 과거에 소급 적용하지 않으며 기존 frozen binding/definition을 쓴다.
- **ADR 0032는 만들지 않는다.** 새 영속 schema/version, 계측 lifecycle, package 방향,
  보관 정책이나 publication-read architecture를 도입하지 않는다. 현 ADR의 적용 판단은 이 절에 남긴다.

## 5. 워크플로우·의존성·파일 소유권

```text
기존 S1/S2/S3 UI 구현 ───────────────┐
B0 문서 확정 ─┬─ B1 호환성·owner ──┼─ 공통 배선/회귀·승인 배포 ─ F4 측정 소비
              ├─ B2 CI/p95·비용 ───┘
              └─ B3 publisher metadata ─ 신규 게시 수용 ─ F4 실험 소비
```

B1/B2/B3는 서로의 계산·생성을 기다릴 이유가 없으므로 B0 뒤 병렬 착수한다.
기존 UI는 현재 한계를 정직하게 표시한 채 먼저 완료할 수 있다. F4는 각 backend 계약/배포가
도착한 부분부터 이어 붙이고 없는 optional 필드는 계속 미제공으로 둔다. B3를 위해 새 실험을
자동 실행하지 않으며 실제 신규 게시 수용은 운영자의 다음 승인 캠페인/게시 일정에 연결한다.
F4 전체 수용은 B1/B2 배포와 B3 새 publication 검증이 모두 있어야 완료다.

공유 파일은 **통합 소유자 한 명**만 수정한다. worker는 자기 파일과 그 인접 테스트만 수정하고
중간 build/lint/test/formatter를 실행하지 않는다. 모든 변경 합류 후 통합 소유자가 한 번 검증한다.

| 소유 | 구현 파일 범위 | 다른 슬라이스에 제공할 계약 |
|---|---|---|
| B1 | `internal/profiling/profiler/kube/candidate_resolver.go`, `execution_facts.go`의 좁은 공통 읽기·호환성 메서드; `internal/operatorconsole/profiling_runs_view.go`; 새 `internal/operatorconsole/model_compatibility_view.go`, `internal/operatorconsole/kube/model_compatibility_query.go`, `internal/operatorconsole/readapi/model_compatibility.go`; 인접 테스트 | compatibility typed selection→snapshot callback, 기존 RunView metadata. 새 handler는 기존 QueryHandler에 합칠 등록 함수를 제공; controller import 금지 |
| B2 | `internal/operatorconsole/batch_energy_optimization_result_view.go`, `kube/batch_energy_optimization_result_query.go`; 새 `cmd/console/measurement_statistics.go`의 저장 자료→통계 callback; `internal/profiling/derivation`의 좁은 statistics summary(기존 수학 재사용); `internal/serving/httpapi/measurement_projections.go`, `internal/serving/kube`의 exact operation projection loader; `cmd/chill-server/measurement_service.go`; 인접 테스트 | validated result/campaign→statistics·reason·error callback, CR+exact operation record→safe step DTO. campaign controller/result writer/raw producer는 편집하지 않음 |
| B3 | `hack/e5/e5.py`, `hack/e5/demo.py`와 기존 recorded-data/self-check; `cmd/chill-server/experiment_service_test.go`의 metadata pass-through 회귀 | 두 기존 KPI shape 안 동일 metadata/null/source. Go experiments.go·uploader envelope 코드는 변경 불필요 |
| 통합 소유자 | `cmd/console/run.go`, `internal/operatorconsole/readapi/queries.go`(callback wiring), `cmd/chill-server/http_routes.go`, `charts/chill/templates/serving_entrypoint_rbac.yaml`, 필요한 Console read RBAC/차트, 공통 route/auth 테스트 | B1 GET 등록·프록시·narrow GET grant와 B2 result statistics callback 연결. 정의된 실제 callback만 연결하고 stub/가짜 fallback 없이 B1/B2를 함께 완결 |
| F4 | 기존 UI S1/S2/S3가 끝난 뒤 `chill-dashboard/lib/read-types.ts`, `lib/measurements.ts`, `lib/measurement-coverage.ts`, `lib/experiments.ts`와 측정/실험 feature·인접 테스트; 기존 담당과 이동 파일 사전 조율 | 서버 판정을 정적 표시와 증거 상태로 분리, exact owner join, CI/p95/ns/J와 null, 두 실험 metadata 소비. 새 독자적 호환성 표/통계 구현 없음 |

위 새 파일 이름은 구현 영역 지정이며 현재 존재/구현 완료 주장이 아니다. 인터페이스는 기존
Console snapshot→BuildView→HTTP 패턴과 composition-root port/callback으로 충분하다.
일반화된 coverage 엔진·registry·generic evidence service·두 번째 logger/측정 lifecycle은 만들지 않는다.

## 6. 완료 기준·회귀 검사

| 슬라이스 | 완료 기준 / 반드시 남길 회귀 |
|---|---|
| B1 | trusted 충돌만 Incompatible, 선언 부재/모호함 Unknown, class/Node/family/backend 유일 선택과 artifact architecture, Node-bound GPU/동일 class 다른 Node, query 중복·누락·unknown·403/404/503. 기존 preflight 성공/거절 불변. owner UID exact join·동명 재생성·owner 없음·누락/잘못된 attempt·active/idle/batch별 원 회차·result:null·전체 page 미조회 |
| B2 통계 | 기존 CI의 비영분산/영분산·비기본 level·shared idle variance·signed 음수 하한 유지. unequal trial vector로 pooled p95≠trial 분위 평균을 입증; 호출/Run/item 수 구분. stored completed references만 포함, 다른 batch/조건·excluded/reuse 확인 섞지 않음. 원근거 404와 권한/store/invalid 구분, 원 result/detail 불변, 요청 취소·유한 read·JSON serialization |
| B2 비용 | 실제 end-start, accepted 0 J, failed에 시간만 제공, insufficient/isolation null J, idle 별도 분기, 부분 완료 sequence, 미실행 후속 없음. foreign CR UID/step/kind/Node/key·missing/corrupt evidence·권한 오류. 원 phase/verdict/reason·evidence 위치 보존, raw/비밀 유출 없음 |
| B3 | E5/demo 두 shape, workload마다 다른 α와 demo alias, direct-rate null α, 한/여러 part, staggered producer starts·0부하 anchor 없음, mixed/missing definition, historical UID/출처. 기존 metric 모집단·판정·에너지/지연 단위와 opaque GET 보존. 과거 #213 읽기에 새 raw open/현재 Workload 조회가 없고 신규 publication collision/불변 순서 유지 |
| F4 | Compatible≠측정 완료/admission, backend Unknown·오류 상태의 정직한 표시. 실패 BEO의 owner UID 일치 회차만 연결. CI 부재/0폭·음수 CI·call count·p95 범위·ns lossless 처리·수용 J 0/누락/오류 구분. 두 publisher identity·α·예정 구간과 과거 미기록. 기존 화면 regression 및 DB-FR-56~65 수용 |

통합 소유자가 구현 뒤 실행할 최소 명령군(이 B0에서 실행하지 않음):

- focused `go test ./internal/operatorconsole/... ./internal/profiling/profiler/kube ./internal/profiling/derivation ./internal/serving/kube ./internal/serving/httpapi ./cmd/console ./cmd/chill-server`.
- B3의 기존 `python3 hack/e5/e5.py self-check`(기존 CHILL_EXPERIMENT_CONFIG 전제) /
  `python3 hack/e5/demo.py --self-check`와 recorded publication 검사, 필요 시
  `go test ./hack/e5/publish`. mock/self-check는 실험·게시 성공의 증거가 아니다.
- 변경된 Go의 repository gate `make lint`, `make test`; 허용 package edge가 실제로 바뀔 때만
  `.go-arch-lint.yml`/설계와 `make arch-check`를 함께 다룬다. 새 package는 이번 설계에 필요 없다.
- chart/RBAC가 바뀌는 B1 통합은 `make helm-lint helm-template`와 별도 default/profiler render,
  exact SAR GET grant·기존 broad grant를 확인한다. CRD 변경이 없으므로 `make manifests generate`는 필요 없다.
- F4의 기존 dashboard native tests/typecheck/lint/build 및 browser smoke, 기존 SUS/과업 수용.
  실측/역사 조회는 측정 창 밖에서 하며 GET만으로 수용할 수 없는 게시/배포는 별도 승인 뒤 수행한다.

## 7. 배포 필요물과 순서

| 범위 | 필요물 | 불필요한 배포 |
|---|---|---|
| B1 | Console 이미지, 새 route를 proxy/auth 등록한 ChillServer 이미지, 정확 `/chill/api/v1/model-compatibility` GET grant 차트. Console는 기존 fact-source options/read 권한 재사용, 실제 배포 RBAC 부족 시 그 GET 권한만 보강 | CRD, profiler controller/worker, runtime, node-agent 이미지 변경 없음 |
| B2 | statistics callback/query를 담은 Console 이미지와 OpCost exact reader/DTO를 담은 ChillServer 이미지. B1과 함께 같은 승인 Helm release에 묶을 수 있음 | CRD, campaign/result/raw writer, measurement worker/runtime/node-agent 이미지 변경 없음 |
| B3 | 두 publisher를 함께 바꾼 harness revision, revision과 동결 입력이 연결된 새 experiment ID/publication, 기존 uploader | metadata pass-through용 서버/Console/agent 이미지·CRD 배포 없음 |
| F4 | 백엔드가 준비된 계약을 소비하는 Dashboard 이미지 | 측정 대상 runtime image 변경 없음 |

순서: 구현 합류→통합 검증→캠페인 측정 창 종료/운영 승인→B1/B2 이미지·narrow RBAC 배포→
GET 실패/성공 경로 확인→F4 측정 소비 배포. B3는 캠페인 사이 publisher cutover→다음 승인된
새 publication→F4 실험 소비 수용이다. 이전 optional metadata 부재는 전 기간 내내 기록 없음으로
남는다. 이 B0 커밋은 배포/클러스터 변경을 승인하거나 실행한 것이 아니다.

## 8. B0 수행 기록

- 수정: 이 계획, catalog SRS PC-07~13, serving 측정 API §12, 관측 설계 §18.
  다른 세션 소유 Dashboard 설계·UX 계획/SRS와 `chill-dashboard/**`는 편집하지 않았다.
- 조사: 기존 저장/읽기 소스, pinned gopls 정의 조회, Graphify 초기 탐색, 지정 인증의 GET 단면.
  gopls 첫 다중 인자 호출은 사용법 오류였고 단일 정의 호출로 고쳤다. source/GET 외의 동작 검증 주장은 없다.
- 미실행: 코드 테스트·lint·build·formatter·Helm render·CRD 생성·배포·측정·새 publication·브라우저/SUS.
  문서 단계이고 코드 변경이 없으므로 구현 검증은 위 슬라이스 통합 소유자의 다음 단계다.
- 기존 worktree의 Dashboard 구현 변경은 보존하고 이 문서 네 경로만 커밋한다.
