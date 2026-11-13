# EvaluationRun 실행

[실험 요구사항](../../spec/experiments/SRS.md)과
[평가 범위](../evaluation/scope.md)를 따른다. Profiler의 EvaluationRun/Stage controller는
순서와 소유권을, 독립 evaluator Job은 단계 프로토콜과 판정을 소유한다.
ChillServer는 시험 대상이며 배포/업그레이드나 상태 복원은 평가 절차에 넣지 않는다.

## 객체와 실행 소유권

| 객체 | 소유 내용 |
|---|---|
| LoadScenario | 불변 공유 부하, Workload identity, parts, 도착 분포, seed와 dataset/trace 참조 |
| EvaluationRun | 질문, 외부 target, 공통 실행/판정 정의, 순서 있는 stage와 비교, 단일 풀 점유 |
| EvaluationStage | 부모가 조립한 한 행의 불변 spec, 단계 Jobs/producer와 주입 기록 |
| Harness Job | 공개 settings patch, load/drain/collection/judgment/cleanup |
| Publish Job | 기록된 최종 축과 근거의 불변 게시 |

세 CR spec은 생성 이후 불변이다. 정정은 새 신원으로 제출한다. 생성 경계는 구조,
범위, ID 유일성과 참조 관계를 검사하고 prepare가 실제 신원/가용성을 확인한다.
Workload name/UID를 확인하며 generation은 요청/관측 출처로 따로 보존한다.
공유 LoadScenario에 consumer ownerReference를 두지 않는다.

Run은 prepare/finish/publish Job, 작업 PVC와 immutable input/config CM을 소유한다.
Stage는 factor/stage/judge/cleanup와 producer Job을 소유한다. Job/Pod 이름은 결정적
lookup이며 UID, owner, 역할과 동결 executable body를 확인한 뒤 채택한다.
backoff zero, restart Never, parallelism/completions one만으로 exactly-once를 보장하지
않는다. GO는 선택한 정확한 producer Pod UID에만 보낸다.

Stage patch가 있으면 Run patch 전체를 대체한다. 생략은 상속이며 숨은 merge가 없다.
동일 LoadScenario UID는 동일 선언이고 실제 발행 품질/성공은 별도 근거다.
SLO와 적용 Workload 정의는 prepare snapshot에 동결한다.

## 설정과 입력 동결

이미지/source, generator/node/resources, 선언 전력계/source, judgment와 logical
evidence store는 명시 입력이다. 운영 endpoint, CA/token, timeout, Job deadline,
collection bounds, workspace/SA/Secret은 `chill-evaluation-config`에서 선택한다.
Controller는 시작 시 direct GET한 값과 ConfigMap name/UID/resourceVersion을 동결한다.
[Configuration Ownership and Freezing Paths](../../adr/0031-configuration-ownership-and-freezing.md)를 따른다.

Prepare는 LoadScenario와 Workload 적용 정의, Dataset carrier를 한 번 해석한다.
Seed는 workload별 uint64 십진 문자열이고 datasetRefs는 name/UID다. Producer init은
carrier의 evaluation 입력을 복사하고 실제 record ID/order/bytes를 Ready 근거에 남긴다.
Child는 snapshot을 사용하며 동명 재생성이 실행 입력을 바꾸지 않는다.

작업 경로는 실행별 RWX PVC의 `/work/campaign/run`이다. PVC는 가변 작업 상태,
store는 불변 정본이다. 입력 CM은 큰 snapshot 대신 snapshotRef만 전달한다.
PVC 소실은 자동 재측정이나 새 Job 복원을 허용하지 않는다.

## 절차

| 단계 | 공개 경계와 산출 |
|---|---|
| prepare | 선언 target/Node/Workload/Dataset, 실행 도구, 용량과 인증 확인; snapshot 동결 |
| factor | Settings GET/ETag, RFC7396 patch, PUT If-Match, readback |
| stage | Producer 생성, readiness, GO, 부하, drain과 collection |
| judge | 보존 client/request/meter와 동결 SLO로 순수 판정 |
| cleanup-jobs | 정확한 소유 Job/Pod 종료와 settings 변경 경로 원복 |
| finish | 평가 소유 writer/자원과 남은 설정 경로 정리 |
| publish | 최종 실행, 측정, 정리 축과 불변 근거 게시 |

Settings 적용은 응답 소실/409에서 자동 재시도하지 않는다. 원복은 새 GET에서 아직
우리 적용값과 같은 leaf만 복구하고 외부 변경은 사유를 남겨 생략한다. Array/scalar는
leaf, null은 RFC7396 삭제다. 원복의 확인된 409만 새 GET으로 한 번 재판단하고
두 번째 충돌은 원 응답을 보존하며 Returned를 막는다.

실행 중 변경한 image나 내부 scheduler 상태를 맞추지 않는다. Target resource
UID/readiness를 관측하고 evaluator 자신의 image/source/Job/Pod 신원을 유지한다.
CHILL instances/routes/reservations가 비었다는 조건을 정리/반환 predicate로 쓰지 않는다.

Producer 자원 관측은 namespace와 `edge.dacs.io/workload-role=load-generator`로
선택한 node-agent raw를 사용한다. 자신의 Pod UID와 load containerID의 완전한 시작/
종료 counter를 기다린다. 다른 Pod/container, SSH/local sampler로 결손을 보충하지 않는다.
닫힌 분 API의 지연을 기존 setup/drain/collect bounds 안에서 기다리고 초과는 실패다.

Client log follower는 바이트를 append/flush하고 해석/잠정 reducer와 분리한다.
공유 reserve 검사는 다음 행까지 반영하며 여유 부족을 capture 오류로 기록한다.
종료는 writer join, 남은 완전한 행 처리, phase 저장 순서다.
재연결/dedup 없는 follow 중단의 누락은 collectionLoss로 드러난다.

## 상태와 판정

| 부모 condition | 의미 |
|---|---|
| PoolAcquired | 해당 Run이 풀을 점유 |
| ExecutionComplete | 절차 완료/실패; 측정 PASS와 별개 |
| MeasurementPassed | 보존 판정의 결과 |
| CleanupComplete | 평가 소유 자원/writer와 변경 경로 정리 확인 |
| Published | 최종 게시 저장 완료 |

Stage는 ExecutionComplete, MeasurementPassed와 StageCleanupComplete를 소유한다.
Procedures 상세는 Stage가 정본이며 부모에 복제하지 않는다.
측정 집계는 INVALID, NOT_EVALUATED, FAIL, PASS 순서로 원 사유를 보존한다.
SLO FAIL은 판정 완료된 측정 불합격이고 JSON/수집/절차 실패는 수행 오류다.
Job 성공이나 exit code만으로 PASS를 만들지 않는다.

요청 지연 판정은 현재 client의 완전 본문 수신 elapsed와 native completion 근거를
사용한다. 예정 도착 지연은 별도 진단이며 deferred 변경은 평가 범위가 소유한다.
전체 offered 분모를 보존하고 collectionLoss를 unfinishedOrUnsent나 성공에 섞지 않는다.
Gateway raw로 유실 client 행을 재구성하지 않는다.

Stage `provisionalD1`은 닫힌 분과 실제 producer start/예정 offset의 현재 cohort를
사용한다. 최종 요청 reducer와 issuance fidelity 계산을 공유하되 drain 전 최종 PASS를
주장하지 않는다. 부분 collection/분 경계 변경은 직전 snapshot과 오류를 유지한다.
Writer join 후 손실을 확정하고 추가 종료 오류는 원래 primary failure를 덮지 않는다.

## 풀 점유와 재시작

평가 controller는 기존 Profiler leader election이 켜져 있을 때만 등록한다.
부모만 풀/finish/release/publication을 소유한다. Worker는 각 controller 하나이고
점유 목록은 APIReader로 읽는다. 시작은 resourceVersion 조건 status 갱신으로 기록한다.
이는 전역 fencing이 아니며 퇴임 leader의 늦은 외부 명령까지 차단하지 않는다.

반환 미확인 Run이 있으면 다음 Run을 시작하지 않는다. 대기는 생성 시각과 name/UID로
결정한다. 자식은 외부 효과 전에 uncached 부모를 읽어 자신이 active child인지 확인한다.
단독 Stage는 ParentRequired다. 기록 UID 소실/교체는 ExecutionLost이며 재측정하지 않는다.

완료 result를 확인한 경계에서만 다음 절차로 간다. 실패한 Job 뒤 다음 stage는 없다.
부분 GO, 불명확 PUT/주입 효과, Pod 교체는 원 근거를 보존하고 중단한다.
관측 재조회/기존 Job 채택은 측정 재시도와 구별한다.

부모 삭제 뒤 새 child를 만들지 않고 기존 child는 정리 경계까지 간다.
Background 삭제만 지원한다. Foreground/orphan으로 Job이 사라지면 finalizer가
보존했다고 주장하지 않고 ExecutionLost/잔류를 기록한다. TTL 자동 GC는 없다.

종료된 Blocked Run에서만 운영자 release를 받는다. 운영자는 원복/잔류와 이전 발행
주체의 효과 종료를 확인한다. Annotation의 reason/at/by/evidenceRef를 감사로 보존하고
controller가 실제 반환을 확인한 뒤 점유를 해제한다. 실패한 cleanup/measurement를
성공으로 고치지 않는다. 정리 실패도 세 축을 분리해 게시할 수 있다.

## 장애 주입

Stage의 typed plan과 compact status record가 정본이다. GO 근거+offset과 동결
허용 창/관측 유효기간을 사용한다. 창을 놓치거나 단계가 완료됐으면 늦게 주입하지
않고 InjectionInvalid를 기록한다. 현재 공개 Pod/Node UID/readiness로 대상을 고르고
선택/탈락 사유와 요청/응답/시각을 보존한다.

Controller가 UID precondition Pod 삭제와 Node UID 확인 cordon/uncordon을 수행한다.
Cordon 응답을 확인한 뒤 같은 Pod를 삭제하고, uncordon은 앞서 선택한 같은 Node만
대상이다. 읽기 오류는 requeue, 발송 뒤 효과 불명은 RemoteEffectUnknown이다.
자동 재주입/물리 복구나 선언 없는 baseline fault를 추가하지 않는다.

## 증거와 게시

단계 evidence는 `experiments/<Run UID>/...`에 conditional create로 저장한다.
Job argv는 `evaluation.py`의 prepare/factor/stage/judge/cleanup-jobs/finish/publish다.
결과 `evaluation-step.v1alpha1`은 실행 신원, 단계, 시각, 독립 상태와 artifact 참조를
보존한다. Exit 0은 필수 업로드 완료, exit 1은 수행/효과 불명, exit 2는 명시 INVALID
등의 판정 완료다. 로컬 결과 사본만으로 불변 결과 저장을 주장하지 않는다.

Binding은 `e5-binding.v4`, inputs/snapshot은 v2alpha1, judgment는 `e5-judgment.v4`,
summary는 `e5-summary.v5`다. Failure policy v2는 SLO, 발행 품질, 전력 coverage,
주입, drain, cleanup/publication을 독립 결과로 보존한다. 일부 전력만 있으면 observed
J/coverage이며 완전한 whole-pool 에너지로 표시하지 않는다.

게시 ID는 Run UID다. Bridge는 `serving/experiments.v1alpha1/<UID>/`의 작은 execution/
evidence-index와 summary를 쓰고 summary를 마지막에 commit한다. 별도의 중간
evidence writer와 구분한다. Sources는 게시 prefix 객체 또는 같은 Run의 명시된
기존 evidence 참조이며 후자는 stat/size로 확인한다. Snapshot을 재업로드하거나 digest
gate를 추가하지 않는다.

Definition은 Run spec, 해석한 LoadScenario/Workload snapshot이다. 현재 설정이나
동명 Workload로 과거 결손을 채우지 않는다. 비교는 선언된 stage/historical
publicationID 관계만 사용한다. 단계 이름/요청률로 정책이나 대응을 추정하지 않는다.

목록은 큰 definition 대신 이름/질문, stage row/load ref/settingsPatchKeys, SLO와
부하 출처 facts를 투영한다. Patch 값은 상세 definition에 남는다. Unknown/null과
빈 변경 키를 구별한다. 게시 outcome은 실행/측정/정리 축이며 미게시 진행과 분리한다.

Controller/harness 평가 계정에는 DeleteObject를 주지 않는다. CR 삭제가 저장 evidence
삭제로 이어지지 않는다. Conflict는 새 Run UID가 필요하며 덮어쓰기나 동일-byte
비교로 복구하지 않는다. Reader 준비 뒤 publisher를 활성화한다.
저장 교체는
[Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md),
화면 해석은
[실험 정의 기반 정책과 SLO](../../spec/dashboard/SRS.md#실험-정의-기반-정책과-slo)를 따른다.

## 권한 경계

Profiler는 평가 CR, 소유 Job/PVC/CM과 선언된 주입 리소스를 관리한다.
ChillServer의 evaluation application은 자기 API에 필요한 CR 조회/변경 권한을 가진다.
Harness action SA는 공개 settings/Workload/raw/power와 자기 producer/GO/cleanup을
사용한다. Judge/publish는 evidence read/immutable write만 사용한다.
Node field 범위/UID 제한은 코드가 집행하며 RBAC 자체의 필드 제한으로 설명하지 않는다.
