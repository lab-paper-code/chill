# Evaluation 범위·원칙·소유 계약

상태: 초안(검토 전). `[사용자 결정]` 표시는 사용자가 정할 항목이다.
정본 요구사항은 [실험 SRS](../../spec/experiments/SRS.md) EXP-01~15이고, 실행 구조는
[EvaluationRun 실행](../profiling/experiment-execution.md)과 [ADR 0032](../../adr/0032-experiment-execution-boundary.md)가 정한다.
이 문서는 둘을 반복하지 않고 evaluation 영역이 무엇을 보장하고 무엇을 쓰지 않는지만 정한다.

## 1. 목표와 완료 기준

evaluation은 CHILL을 외부 평가자처럼 실행·판정·게시해 `docs/spec/` 측정 표를 채운다.
다음이 관찰 가능하게 성립하면 완료다.

| 보장 | 관찰 기준 |
|---|---|
| 같은 입력이면 같은 판정 | 판정은 동결된 binding·client 행·원시 증거만의 순수 함수다(`hack/evaluation/judgment.py` `reduce_requests`). 같은 증거로 다시 판정하면 같은 verdict·수치가 나온다. |
| client 행 = 계획 행 | 요청마다 평가자 client 행이 하나 있다. 수집이 계획보다 적으면 `collectionLoss`로 따로 세고 `unfinishedOrUnsent`에 섞지 않는다. 분모(frozen offered)와 D1 식은 바뀌지 않는다. |
| 사람 개입 없는 종결 | prepare → stage → finish → publish가 끝나면 Run이 `CleanupComplete=True`, `PoolAcquired=False/Returned`가 된다. 실제로 남은 자원이 없는데 pool을 잡는 것은 결함이다. 수동 `POST .../release`는 실제 잔여가 있을 때만 쓴다. |
| 재현 가능 | 게시물(`serving-experiment.v1alpha1`)에 정의·LoadScenario·seed·harness 이미지·sourceSHA·동결 설정·증거 참조가 있어 같은 Run을 다시 제출할 수 있다. 단일 seed 묶음은 반복 독립성을 주장하지 않는다(binding `limitations`). |

## 2. 블랙박스 경계

평가자는 CHILL을 시험 대상으로만 본다. 내부 상태를 판정·보정·시작 조건에 쓰지 않는다.

### 쓸 수 있는 입력

| 입력 | 경로 | 용도 |
|---|---|---|
| 평가자가 쓴 선언 | EvaluationRun, LoadScenario(절대 rate, inline seed, datasetRefs, traceRefs) | 부하·단계·판정 조건 |
| 평가자가 낸 부하와 그 응답 | load generator client JSONL(`requestIndex`, 송신·수신 시각, `requestID`, outcome) | 발행 품질, 요청 존재, 응답 결합 |
| 공개 워크로드 정의 | `GET /api/v1/workloads/<name>` | 신원(name·uid·generation)과 적용 SLO |
| 공개 운영 설정 | `GET/PUT /api/v1/scheduler/settings`(If-Match) | 단계 조건 patch와 종료 시 원복 |
| 요청 HTTP final | `GET /api/v1/observations/raw` | 아래 열린 항목 B1 참고 |
| 선언된 전력계 | `GET /api/v1/observations/power`(`powerMeasurement.meters[].sourceIdentity`) | 에너지 적분, 공백 기록 |
| Kubernetes API(평가자 소유분) | 자기 Job/Pod(UID 기록), 대상 Pod readiness·imageID, 장애 주입 대상 | 실행·정리·preflight·주입 |

### 쓰지 않는 입력

- κ, d, 계획기 결정·후보·Fixed/Pending 사유, scheduler state/snapshot, profile 값.
- 시작 상태 복원·내부 모드 주입(W1 cutover로 `mode`·`startState`·`control` 삭제, [measurement-api §W1](../serving/measurement-api.md#w1-외부-블랙박스-평가-cutover)).
- gateway·observer 관측으로 평가자 증거를 보충·보정하는 것.
  - 사례(2026-11-04): 한 단계에서 IC producer stdout 꼬리가 수집 중 끊겼다. 같은 요청 전체가 gateway raw에는 있었다. 결정은 gateway 행으로 client 행을 복원하지 않는 것이다. 손실은 `collectionLoss`로 드러내고, 분모와 판정식은 그대로 둔다. 원인은 conductor 수집 경로에서 고친다.

### 열린 항목

| # | 현재 사실 | 질문 |
|---|---|---|
| B1 `[사용자 결정]` | D1의 지연은 gateway HTTP final `elapsedNanos`(CHILL 수신 → final)다. client RTT는 참고값이다(`request_analysis.py` `request_timing`, judgment `latencyScope`). 즉 판정이 CHILL gateway 관측을 쓴다. | 이것을 시험 대상의 공개 응답 기록으로 허용할지, D1을 client 관측 지연으로 바꿀지(판정 규칙 변경) |
| B2 `[사용자 결정]` | 전력은 외부 계측기 값이지만 읽는 경로는 CHILL observation API다. | 판정용 독립 경로(계측기 직접 수집)가 필요한지 |
| B3 `[사용자 결정]` | 단계 시작 전 executor gate는 CHILL이 Ready·routed로 보는 인스턴스만 검사한다(운영 규칙, 판정 밖). | 시작 조건에 CHILL 판단을 쓰는 것을 허용 범위로 명시할지 |

## 3. 소유 계약

| 계약 | 식별자 | 위치 |
|---|---|---|
| 선언 CR | `edge.dacs.io/v1alpha1` EvaluationRun·LoadScenario·EvaluationStage(spec 불변) | `api/v1alpha1/evaluationrun_types.go`, `loadscenario_types.go`, `evaluationstage_types.go` |
| HTTP | `/api/v1/evaluations`(목록·상세·생성·삭제, `preflight`, `{name}/release`, `active`, `template`, `inputs`, `load-scenarios`(+`preflight`)), stage 읽기 projection | `internal/serving/httpapi/handler.go:162-170`, `evaluation_load.go`, `internal/evaluation/service` |
| 단계 결과 | `evaluation-step.v1alpha1` | `hack/evaluation/evaluation.py` `result_record`; controller `jobs.go` `validateResult` |
| 입력 동결 | `evaluation-snapshot.v2alpha1` / `evaluation-inputs.v2alpha1` | `hack/evaluation/input_snapshot.py` |
| binding | `e5-binding.v4` | `hack/evaluation/conductor.py` `bind`, `evidence_io.load_binding` |
| 판정 | `e5-judgment.v3` | `hack/evaluation/judgment.py` `judge` |
| 요약 | `e5-summary.v4` | `judgment.py`, `tables.py` |
| 실패 정책 | `failurePolicy: v2`(부분 관측 보존, 하나 이상하면 전체 폐기 금지) | binding·phase·archive·judgment 공통 |
| 게시 | `serving-experiment.v1alpha1`(정의 `evaluation-definition.v1alpha1`) | `hack/evaluation/publication.py` |

변경 절차는 [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)를 따른다.
- reader는 단일 버전이다. 옛 형식은 변환하지 않고 지운다.
- 저장 계약은 한 작업 단위에 한 번만 바꾸고, 캠페인이 의존하는 동안에는 바꾸지 않는다.
- additive optional 필드(예: stage `progress`·`lastProgressAt`·`loadJobs`, `collectionLoss`)는 버전을 올리지 않는다.
- 게시된 결과는 다시 해석하지 않는다(EXP-14). 정정은 새 ID로 게시한다.

## 4. 확장 방향 `[사용자 결정]`

권장 순서는 c → a → b → d다. 각 항목은 측정 표 하나를 채울 때만 착수한다.

| 순서 | 후보 | 현재 사실 | 필요한 것 |
|---|---|---|---|
| 1 | (c) 고정 배치 기준선 비교 | `spec.comparisons`(stage/historical 참조)는 선언·게시만 되고 harness가 비교를 계산하지 않는다. 에너지 상태는 MEASURED/PARTIAL/UNAVAILABLE 기록일 뿐 기준선 대비 판정이 없다. | 같은 LoadScenario에 고정 구성 단계(settingsPatch)를 두고 에너지·D1 차이를 게시. 에너지 판정의 전제 |
| 2 | (a) 반복 측정·편차 | binding이 단일 seed 묶음이라 반복 독립성 주장이 없다. | seed만 바꾼 반복 단계와 편차 표 |
| 3 | (b) ablation | 공개 설정 patch로 끌 수 있는 기능만 대상 | 기능 하나씩 끈 단계와 비교 |
| 4 | (d) 장애 주입 반복 | 단계당 주입 계획 실행은 구현됨(EXP-12) | 같은 주입의 반복·자동 집계 |

범위 밖:
- 빈 장비 실측, 큰 batch 측정 → profiling 영역([Profiling 색인](../profiling/README.md)).
- CHILL 배포·업그레이드, 다중 풀 → SRS §1.

## 5. 결정 권한

| eval이 정함 | 사용자 결정 |
|---|---|
| 판정식을 바꾸지 않는 harness·conductor·loadgen 결함 수정(수집, 정리, 진행 신호, 메모리) | D1 식·분모·percentile 의미, 판정 지연의 출처(B1) |
| additive 관측 필드·게시 진단(예: `collectionLoss`, stage `progress`) | 에너지 상태 규칙과 coverage 정책의 의미 변경 |
| 실행 실패 분류, residual 판정, release 조건 구현 | 블랙박스 경계 변경(§2 입력 추가·삭제, B1~B3) |
| Run spec 작성(단계, settingsPatch, timebox 값 선택) | 측정 방식 자체 변경, 노드 제외, 재측정 예외 |
| 증거·게시 포맷 내부 정리(버전 불변 범위) | 캠페인 중 저장 계약 버전 변경, 게시된 결과의 재해석 |
| §4 항목의 설계안 작성 | §4 착수 여부와 순서 |

## 6. 경계

| 상대 | eval → 상대 | 상대 → eval |
|---|---|---|
| advisor-2(serving·scheduler·profiling·kube) | 서버 기능이 필요하면 eval이 계약(요구사항·필드·검증 위치)을 정해 요청한다. 본체 코드는 직접 고치지 않는다. | 본체 변경이 공개 API·관측 의미를 바꾸면 eval에 알린다. |
| advisor-3(대시보드 실험 화면) | 화면에 필요한 데이터 계약(필드, 의미, 결측 처리)을 직접 맞춘다. | 표시 요구를 계약으로 요청한다. 판정·추론은 화면에서 하지 않는다. |
| main(실클러스터 실행·배포) | 수정 hash와 검증 결과를 전달한다. 배포는 단계 사이에만 한다. | 실행 결과·증거 경로·결함을 기록한다. |
