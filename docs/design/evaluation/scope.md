# Evaluation 범위와 소유 계약

Evaluation은 외부 평가자로 CHILL을 실행하고 [명세](../../spec/README.md)의 표를 채운다.
선언은 [실험 요구사항](../../spec/experiments/SRS.md), 수행 구조는
[EvaluationRun 실행](../profiling/experiment-execution.md)이 소유한다.

## 목표와 완료 기준

| 보장 | 기준 |
|---|---|
| 재현 가능한 판정 | 동결 binding, client 행과 raw 근거로 같은 판정/수치를 계산 |
| 발행과 손실 공개 | 계획보다 적은 client 행을 collectionLoss로 분리; offered 분모 유지 |
| 확인된 종결 | 소유 자원/writer와 설정 경로 정리 뒤 풀 반환; 미확인은 점유 유지 |
| 재현 가능한 정의 | LoadScenario, seed, 실행 도구, 설정과 evidence 참조를 게시에 보존 |

단일 seed 묶음은 반복 독립성을 주장하지 않는다.
상위 집계는 INVALID, NOT_EVALUATED, FAIL, PASS 순서와 원 사유를 보존한다.
Execution, measurement와 cleanup은 독립이다. 종료된 Blocked Run의 운영자 release는
확인된 복구 경로이며 실패 기록을 성공으로 바꾸지 않는다.

## 블랙박스 입력

| 입력 | 허용 용도 |
|---|---|
| EvaluationRun/LoadScenario | 부하, 단계, seed, dataset/trace와 판정 조건 |
| Client JSONL와 응답 | 발행 품질, 요청 존재, 완전 응답 지연과 결과 |
| 공개 Workload 정의 | Name/UID, 관측 generation과 적용 SLO |
| 공개 settings GET/PUT | 명시 patch와 조건부 변경/원복 |
| 요청 HTTP final | Native 완료, outcome/admission과 진단 시각 근거 |
| 선언 전력계의 공개 power 응답 | Source/node/meter/window 검증과 에너지 적분 |
| Kubernetes | 선언/외부 신원, readiness, 참여 환경, 자기 자원 정리와 typed 주입 |

현재 SLO 지연은 client의 full-body elapsed를 쓴다. 성공 client 응답과 native
executionComplete final이 함께 있어야 onTime/late다. Client timeout 뒤 native 완료도
timeout으로 남는다. Scheduled-arrival latency와 gateway elapsed는 진단으로 보존한다.

내부 κ/d/profile/계획기 후보/판단을 판정이나 결손 보정에 쓰지 않는다. 관측 이력으로
남길 수 있으나 수집에서 잃은 client 행을 gateway raw로 복원하지 않는다.
시작 상태 복원이나 내부 mode/control 입력을 만들지 않는다.

## 보류한 사용자 의도

아래 항목은 현재 구현과 구별하는 사용자 의도다. 기존 측정은 당시 의미로 보존한다.

| 의도 | 적용 경계 |
|---|---|
| 예정 도착부터 응답 완료까지의 지연 판정 | 발행 지연을 포함하도록 판정 변경 시 저장 계약을 한 번 교체; 과거 결과 재판정 없음 |
| 평가자의 전력계 직접 수집 | CHILL observation API를 거치지 않는 독립 경로 |
| 시작 전 실행기 점검 | 공개 Ready/routed는 점검 대상 선택에만 사용; 판정/보정에는 사용 금지 |

실행기 점검 의도는 현재 응답만 확인하고 CHILL을 바꾸지 않는다. 제외 사유를 남기며
참여 Node의 NotReady만 시작을 막는다. 점검 실패는 부하 없이 시작을 보류하고 새
관측으로 재확인한다. 현재 executor gate가 구현됐다고 주장하지 않는다.
현재 target check는 선언 Kubernetes resource UID/Pod readiness를 관측한다.

## 저장과 조회 소유권

| 계약 | 현재 형태 |
|---|---|
| 선언 | 불변 EvaluationRun, LoadScenario, EvaluationStage |
| 진행 API | `/api/v1/evaluations`, preflight/template/inputs/load-scenarios, active/release |
| 단계 result | `evaluation-step.v1alpha1` |
| 동결 입력 | `evaluation-snapshot.v2alpha1`, `evaluation-inputs.v2alpha1` |
| Binding | `e5-binding.v4` |
| 판정 | `e5-judgment.v4` |
| 요약 | `e5-summary.v5` |
| 게시 | `serving-experiment.v1alpha1`, `evaluation-definition.v1alpha1` |

저장 교체는
[Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md),
게시 정정은 [실행 결과와 게시](../../spec/experiments/SRS.md#실행-결과와-게시)를 따른다.
Optional 진행/진단 필드 추가는 기존 의미를 바꾸지 않는다.

전용 evaluation store 설정은 이름/endpoint/bucket/region과 credential 환경변수를
함께 설정하거나 모두 생략한다. 설정하면 평가 게시/입력 조회에 그 store를 사용하며
이전 store를 병행 조회하지 않는다.

## 요청 모집단과 관측 결손

OutcomeClasses는 offered 집단을 onTime, late, rejected, timeout, failed, incomplete로
분할한다. OnTimeSuccessRate는 offered 분모다. Goodput은 해당 Workload 부하 길이를
사용하고 그 cohort의 drain 완료를 포함한다. Drain 시간이나 다른 Workload의 전체
wall window로 분모를 늘리지 않는다. Completed latency 통계는 판정과 구별한다.

Rejection은 명시 policy/stage/code와 queue wait를 보존한다. 미기록 wait는 표본이
없으며 실제 영은 표본이다. 과거 admission metadata에 기본값을 소급하지 않는다.
회복 gap은 원래 half-open 범위와 source/hour provenance를 보존한다. Offered 구간과
겹치면 요청 판정은 NOT_EVALUATED/ObservationUnavailable이다. 살아남은 finals로
완전성을 주장하지 않는다. Zero-offer는 NOT_OFFERED이며 meter coverage는 독립이다.

Provisional은 닫힌 분 cutoff까지의 시작한 cohort만 쓴다. 미시작 zero-offer는 길이/
goodput 미가용이다. 전체 client 행은 마지막 capture에서 확정하고 손실을 숨기지 않는다.
Inventory/download capture 완료 여부와 관측 gap 유무는 독립이다.

모든 producer는 readiness 뒤 같은 미래 UTC release를 원자 기록받고 실제 wall/
monotonic 시작을 남긴다. Release instant를 놓친 쓰기는 BarrierTimeout이다.
Conductor GO와 producer 실제 시작을 구별하며 기존 skew 판정을 보존한다.

## 오프라인 진단

전환 예측 정합, serving/profile 분포 대조와 profile holdout은 judge Job 밖의 진단이다.
보존 scheduler/raw/profile/동결 settings만 읽으며 제품에 평가 전용 export나 수집
경로를 추가하지 않는다. Profile 값은 요청 지연/에너지 판정을 보정하지 않는다.

예측 분석은 실제 action 구간과 같은 meter/coverage/window를 사용한다. 선행 판단이나
실제 phase가 불명확하면 UNKNOWN이다. Gap은 겹치는 구간에만 영향을 주고 누락된
coverage 정보는 완전하다고 추정하지 않는다.

Holdout은 실제 batch/phase와 physical trial을 분리하고 독립 loaded-idle를 사용한다.
Service/preceding-idle 쌍을 유지하며 다른 Node/runtime/artifact 조건을 합치지 않는다.
Post-load는 같은 actor의 무추론/상주 진단이며 독립 유휴 기준선으로 섞지 않는다.
Client scheduled arrival와 측정 Node power clock을 혼합하지 않는다.
Bootstrap은 독립 trial 단위다. 단일 trial CI는 미제공이며 자동 배제/보정 임계값을
만들지 않는다. 실제 계산은 기존 derivation와 scheduler 공개 API를 재사용한다.

## 확장과 결정 권한

착수 순서는 고정 배치 기준선 비교, 반복/편차, ablation, 장애 주입 반복이다.
사용자가 평가 결과 안정화 뒤 착수를 지시한다. 현재 comparisons는 선언/게시 관계이며
그 자체로 비교 계산이나 에너지 우열 판정을 제공하지 않는다.
각 확장은 실제 명세 표를 채울 때 진행한다.

평가 구현자는 판정 의미를 바꾸지 않는 수집/정리/진행/메모리 결함을 고칠 수 있다.
판정 지연 출처, 분모/percentile 의미, 에너지 coverage 정책, 블랙박스 입력 변경,
노드 제외와 재측정 예외는 사용자 결정이다. 캠페인 저장 계약과 과거 게시 재해석도
임의로 바꾸지 않는다. 큰 batch/빈 장비 측정은 profiling 영역이고 배포/업그레이드와
다중 풀은 이 실행기의 범위 밖이다.
