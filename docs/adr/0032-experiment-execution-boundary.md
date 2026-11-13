# EvaluationRun Execution Boundary

## Status

Accepted

## Context

실험 정의, 실행 상태와 측정 판정은 같은 불변 입력을 참조해야 한다. 실행 주체는 시험 대상 ChillServer의 생명주기 밖에 둔다.

## Decision

namespaced `LoadScenario`, `EvaluationRun`, `EvaluationStage` 세 CR을 사용한다. spec 전체는 불변이며 정정에는 새 CR을 만든다.

- LoadScenario는 재사용 가능한 부하 정의다. 실행 status나 consumer ownerReference를 두지 않는다.
- EvaluationRun은 질문, 조건 조립, 비교, 단일 풀 점유, 최종 정리와 게시를 소유한다.
- EvaluationStage는 부모가 순차 생성하는 자식이며 한 행의 복원, 부하, drain, 수집, 판정과 단계 정리 Job을 소유한다. controller ownerReference가 부모의 정본이다. 단독 Stage 실행은 허용하지 않는다.

기존 profiler manager가 두 실행 reconciler를 소유한다. leader election이 켜진 경우에만 등록하고 단일 worker와 비캐시 목록 읽기로 한 실험을 실행한다. 반환 미확인 실험은 다음 실행을 막는다. 퇴임 leader의 늦은 원격 명령을 fencing한다고 보장하지 않는다.

풀 점유와 반환은 부모가 소유한다. 자식은 외부 효과 직전에 비캐시 APIReader로 부모의 점유와 active child 신원을 확인한다. 실제 Node 예약은 ChillServer admission이 소유한다.

시험 대상은 선언된 resource, Workload/Node UID와 소유권으로 고정한다. 공개 ChillServer API와 Kubernetes API로 제어하고 관측하며, 장애 주입도 선언한 계획에 따라 Kubernetes API로 수행한다. 배포나 업그레이드는 수행하지 않는다.

관측한 image digest, source 정보와 준비 상태는 실행 증거다. 이미지나 프로파일의 exact-match 및 토큰 TTL 비교를 접수 관문으로 추가하지 않는다. 실제 인증, 숫자 입력과 평가자 Job/Pod 소유권은 검증한다.

운영 상한과 선택한 설정의 실제값 및 출처를 점유 시작 때 `status.frozenConfig`와 실행 증거에 동결한다. Prepare는 LoadScenario와 카탈로그를 한 번 해석하고 부하 및 Workload 적용 정의를 snapshot으로 고정한다. SLO는 고정한 Workload generation에서 얻는다.

절대 요청률, seed와 Dataset carrier를 동결한다. Prepare가 도착 간격을 한 번 고정해 workspace PVC의 읽기 전용 파일로 producer에 전달한다. Producer가 다시 생성하지 않으며 SHA/count metadata를 접수 조건으로 삼지 않는다.

### Job과 증거 경계

결정적 이름의 단발 Job이 `evaluation.py`의 `prepare`, `factor`, `stage`, `judge`, `cleanup-jobs`, `finish`, `publish`를 실행한다. 단계 프로토콜과 계산식을 Go로 복제하지 않는다. Job은 Pod attempt 하나이며 자동 재시도하지 않는다.

재개는 기존 Job을 조회하거나 AlreadyExists 결과를 채택하는 것이다. 기록한 Job/Pod UID를 관측하며 재발행하거나 재측정하지 않는다. Job 소실은 `ExecutionLost`, 원격 효과 불명은 미확인 상태로 중단하고 원인, 증거와 예약을 보존한다. 자동 물리 복구는 하지 않는다.

Job 성공과 실행 완료, 측정 PASS, 증거 완료 및 정리 완료를 구분한다. 실행 CR status는 `observedGeneration`과 type-keyed conditions를 사용한다.

단계 정리는 자기 Job을 삭제한다. 원 설정 복원, owned reset과 반환 대기는 실험 끝에 한 번 수행한다. 선언한 `settingsPatch`만 공개 settings API로 적용하고 read-back 및 원복한다. 정리 실패도 측정 결과는 게시하며, 실제 상태를 확인한 운영자의 metadata annotation 해제를 status에 기록한 뒤 점유를 해제한다.

Background 삭제를 지원한다. Foreground/orphan 삭제로 Job이 사라지면 실행 소실과 잔류를 기록한다. finalizer가 Job 보존을 보장한다고 주장하지 않는다.

node-agent가 producer cgroup을 수집한다. 기존 `resources` raw의 닫힌 분에서 정확한 Pod UID와 load containerID로 시작 전후 counter 및 판정을 연결한다. 서버 namespace의 `edge.dacs.io/workload-role=load-generator`는 자원 표본만 선택한다. 별도 SSH 수집기나 폴백은 없다.

### 게시와 조회

게시 ID는 EvaluationRun UID다. `ExperimentSummary.definition`은 Run spec과 해석된 LoadScenario snapshot을 담는다. 실행 기록에는 관측 신원, 동결 설정, 완료 축과 증거 참조를 둔다. `tables.py`가 표를 조립한다.

ChillServer는 불변 게시물을 읽는다. 대시보드는 명시한 정책, 비교 관계와 SLO를 표시하며 이름이나 요청률로 누락된 의미를 추측하지 않는다. 진행 중 조회는 Kubernetes 실행 객체를 사용한다.

## Consequences

수행 오류, 측정 불합격과 반환 실패를 독립적으로 보고한다. 범위 제한 RBAC, 실제 인증, 유한 drain과 writer 소유권을 유지한다. 기존 과학 원시, 실행 기록과 게시물은 변경하거나 재게시하지 않는다.

저장 계약은 [Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md), 관측 스트림은 [Observation Evidence API Streams](0030-observation-evidence-api-streams.md)를 따른다. 상세 동작은 [실험 실행 설계](../design/profiling/experiment-execution.md)가 소유한다.

정의와 설정 요구사항은 [불변 실험 정의](../spec/experiments/SRS.md#불변-실험-정의), [실험 정의의 필수 내용](../spec/experiments/SRS.md#실험-정의의-필수-내용), [설정 선택과 동결](../spec/experiments/SRS.md#설정-선택과-동결)을 따른다.

실행 요구사항은 [독립 실행 경계](../spec/experiments/SRS.md#독립-실행-경계), [시험 대상 신원 확인](../spec/experiments/SRS.md#시험-대상-신원-확인), [자원 풀 상호 배제](../spec/experiments/SRS.md#자원-풀-상호-배제), [단계별 수행 순서](../spec/experiments/SRS.md#단계별-수행-순서), [수행 기록과 재시작](../spec/experiments/SRS.md#수행-기록과-재시작)을 따른다.

결과 요구사항은 [완료 상태의 분리](../spec/experiments/SRS.md#완료-상태의-분리), [기존 판정 규칙](../spec/experiments/SRS.md#기존-판정-규칙), [중단과 정리](../spec/experiments/SRS.md#중단과-정리), [장애 주입](../spec/experiments/SRS.md#장애-주입), [불변 증거와 참조 보호](../spec/experiments/SRS.md#불변-증거와-참조-보호), [실행 결과와 게시](../spec/experiments/SRS.md#실행-결과와-게시), [조회와 해석 경계](../spec/experiments/SRS.md#조회와-해석-경계)를 따른다.

