# 0032 EvaluationRun Execution Boundary

## Status

Accepted (2026-10-31, 사용자 승인).

## Context

E5의 조건·순서·비교 관계는 CHILL 밖 `.cache/chill-assets/ops/pools-deploy`의 round JSON·binding·`run-ab-*.sh`에 있다. CHILL에는 결과 요약만 게시되고 KPI는 의미를 해석하지 않는 JSON이다. 대시보드는 단계 이름과 요청률로 정책·비교 관계를 추측한다. 조건, 결과, 화면의 해석이 세 곳으로 갈려 같은 실험을 재현하기 어렵다.

조사 근거는 `exp-terrain`(지형 조사 §4~7, `local://exp-terrain.md`)과 `exp-usage`(운영 명령·STOP·입력 목록, `local://exp-usage.md`)다. 아래 운영 경로는 위 `.cache` 디렉터리 기준이다.

- 입력 파일이 사라지면 기록만으로 복원할 수 없다. 실제 `campaign/v2/d-20261027/control-214-factors/STOP.json`에는 `initial` 파일 부재가 남았다(`exp-usage` STOP 표). 파일이 없다는 사실과 삭제 원인 확인은 구별한다.
- 혼합 운영 STOP **17건**은 정리·반환 실패 **6**, 정리 Job 부재 **1**, 입력 부재·구계약 **2**, 증거 JSON 읽기 **2**, 수집기 기동 **1**, runtime/Service/Pod 준비 **3**, 완료 측정의 소비 투영 누락 **1**, 디스크 가드 **1**이다(`exp-usage` STOP 표, `exp-terrain` §7). 전부 E5 실행 실패는 아니며 배포 drift·digest 불일치 STOP은 이 표에서 관측되지 않았다.
- round·binding·환경 파일과 단계별 명령을 함께 알아야 하므로 운영 에이전트가 라운드마다 실행 절차를 다시 조합한다(`exp-usage` 라운드 설정·실제 argv 절). 선언된 정의와 실행 상태를 한곳에서 읽을 수 없다.
- E5-7은 외부 하네스가 대상을 선택해 Kubernetes 명령을 주입한다. 실제 round-26의 `E5-7m-injection-0-0.command.json`은 Pod 삭제 API 호출이다(`exp-terrain` §5). 운영자·하네스의 수동 절차를 CHILL의 선언된 실행 계약으로 옮길 필요가 있다.

검토한 대안과 기각 이유:

| 대안 | 기각 이유 |
|---|---|
| A. 파일을 저장소로 옮기기 | 보관 위치만 바뀌며 조건과 실행은 여전히 CHILL 밖이다. |
| B. API로 조건 등록 | 다른 측정의 CR 기반 정의·상태 계약과 일관되지 않는다. |
| B′. CRD만 추가 | 정의는 남지만 실행은 여전히 수동 하네스에 의존한다. |
| ChillServer 내부 실행기 | 측정 주체가 시험 대상 생명주기에 들어간다. `AGENTS.md`의 측정 분리 원칙에 어긋난다. |
| 새 독립 프로세스 | 기존 profiler가 이미 분리된 측정 주체이므로 별도 서비스는 불필요하다. |
| EvaluationRun 전용 Lease | 기존 leader election·단일 worker·캐시 아닌 목록 읽기로 현재 단일 풀을 직렬화한다. 추가 Lease도 퇴임 leader의 늦은 명령을 fencing하지 못한다. |
| 판정·단계 프로토콜을 Go로 이식 | 기존 계산식과 실행 절차를 다시 쓰면 판정·측정 의미가 바뀔 위험이 있다. |
| Katib | 탐색·튜닝용이며 이번 고정 evaluation 절차 실행에는 맞지 않는다. kind는 Katib Experiment와의 이름 충돌을 피해 EvaluationRun으로 정한다. |
| Tekton | CI 중심의 별도 실행 체계를 도입할 이유가 부족하다. |
| Argo Workflows | 이번 단순 순차 실행에서 새 의존성을 들일 만큼 이점이 크지 않다. |
| FaultInjection·ControlProfile·Snapshot CR | 독립 생명주기·소유자 근거 없음. |

## Decision

상세 필드·단계·실패 처리는 [실행 설계](../design/profiling/experiment-execution.md)가 소유한다. 요구사항은 [실험 SRS](../spec/experiments/SRS.md) EXP-01~15를 따른다.

1. namespaced 세 CR(개정 이력 참조)에 질문·조건·순서·비교·장애 주입·불변 입력 참조를 선언한다. spec 전체는 CEL `self == oldSelf`로 불변이며 정정은 새 CR이다. 실행 두 CR(EvaluationRun·EvaluationStage)의 status는 `observedGeneration`과 type-keyed `metav1.Condition`을 사용하고 실행 완료·측정 판정·정리 완료를 구분한다.
2. 기존 profiler manager에 reconciler를 둔다. controller는 단계 순서·Job 채택·compact status만 소유한다. 기존 다른 측정 CR 실행 위치는 이 결정으로 옮기지 않는다.
3. 배포·업그레이드는 하지 않는다. 기대 신원과 관측한 image digest·source SHA가 다르거나 신원을 확인할 수 없으면 시작하지 않는다. 이는 배포 신원 확인이며 새 digest 형식·재계산·바이트 동일성 검증 관문은 아니다.
4. 시험 대상 제어·관측에는 ChillServer 공개 API와 Kubernetes API만 쓴다. failure 주입도 선언된 계획에 따라 Kubernetes API로 수행하며 ChillServer 내부 호출이나 장애 주입 API를 만들지 않는다.
5. profiler leader election(Helm 기본 활성)·단일 worker·캐시 아닌 목록 읽기로 생성 순서에 따라 한 실험만 실행한다. leader election이 꺼지면 이 reconciler만 등록하지 않는다. 실험 시작 표시는 한 번 조건부 갱신하되 명령별 선점 장부는 만들지 않는다. 반환 미확인 실험은 다음 실행을 막으며 별도 Lease·우선순위 큐는 없다. 실제 노드 예약은 기존 ChillServer admission이 소유한다. 퇴임 leader의 늦은 명령 가능성은 알려진 한계이며 fencing을 보장한다고 주장하지 않는다.
6. 재개는 결정적 이름으로 기존 Job을 조회하거나 AlreadyExists 결과를 채택하는 것이다. 기록된 Job/Pod UID를 관측할 뿐 재발행·재측정하지 않는다. Job 소실은 `ExecutionLost`, 원격 효과 불명은 미확인 상태로 중단하며 원 사유·증거·예약을 보존한다. 자동 물리 복구하지 않는다.
7. 기존 `conductor.py` 단계 명령(`factor`/`stage`/`judge`/`cleanup-jobs`)을 결정적 이름의 단발 Job으로 실행하고 마지막에는 표·게시 Job을 둔다. 부하·drain·수집·판정 등 단계 프로토콜과 계산식을 Go로 옮기지 않는다. 각 Job은 Pod attempt 하나이며 자동 재시도하지 않고 Job 성공을 측정 PASS나 증거 완료로 간주하지 않는다. 단계 정리는 자기 Job 삭제만 하며 원 설정 복구·owned reset·반환 대기는 실험 끝에 한 번 수행한다. 오류 시 하네스의 자동 설정 복구는 끈다. 정리 실패도 측정 결과는 게시하고, 운영자가 실제 상태를 확인한 뒤 metadata annotation으로 해제하면 controller가 status에 기록하고 점유를 해제한다.
8. `ExperimentSummary`의 선택 필드 `definition`은 EvaluationRun spec + 해석된 LoadScenario spec snapshot을 담는 additive 확장이다. prepare가 해석한 Workload 적용 정의(SLO 정본)와 고정된 게시 형태는 [실행 설계 §8](../design/profiling/experiment-execution.md#8-증거게시조회)을 따른다. 실제 관측 신원·동결 운영 설정·완료 축·실행 증거 참조는 실행 기록에 둔다. 게시 ID는 EvaluationRun UID다. ChillServer는 불변 게시물만 읽고 CR은 읽지 않는다. reader(서버·대시보드)를 먼저 배포한 뒤 publisher를 전환한다. 대시보드는 완료 게시의 명시된 정책·비교 관계·SLO만 읽고 진행 중 조회는 kubectl로 한다. 옛 게시는 비교 관계 미기록으로 표시되며 추측 함수 삭제로 과거 비교가 사라지는 가시적 변화를 알린다.
9. 운영 상한 등 설정값은 profiler config에서 선택하고 실제값과 읽은 출처를 점유 시작 시 `status.frozenConfig`와 실행 증거에 동결한다. 실행 중 설정 재조회로 조건을 바꾸지 않는다.

## 개정 이력

### 2026-11-02 producer cgroup을 node-agent raw로(사용자 결정)

- evaluation의 SSH sampler·producer SSH Secret·호스트 계정/authorized_keys·sudo crictl 요구를 제거한다. node-agent가 producer cgroup을 수집하고 기존 resources raw의 닫힌 분을 폴링하여 정확한 Pod UID/load containerID로 GO 전·종료 counter·판정을 연결한다. 별도 API·폴백·local client 병합은 없다.
- 서버 namespace의 일반 계약 라벨 `edge.dacs.io/workload-role=load-generator`는 **자원 표본만** 선택한다. cluster observer 선택은 유지하고 서버는 evaluation 전용 라벨에 결합하지 않는다.
- 과거 “node-agent 대체 불가” 결정을 폐기한다. 닫힌 분 지연 ≤82초 + 기존 API 상한 129초를 기존 startup 예산 194초에 더해 chart setup 기본을 405초로 조정한다. 근거·상한·실패 의미는 [실행 설계 §12](../design/profiling/experiment-execution.md#12-결정2026-11-02-사용자-개정)를 따른다. raw 저장 형식/ADR 0019는 변경하지 않으며 D2 sampler 도구는 유지한다.

### 2026-10-31 (사용자 승인) 하위 데이터 모델 분해

- `LoadScenario` CR은 재사용 가능한 불변 부하 정의이며 실행 status나 consumer ownerReference를 두지 않는다.
- `EvaluationRun` CR은 질문·조립·비교·단일 풀 세션·실험 끝 정리·게시를 소유한다.
- `EvaluationStage` CR은 부모가 순차 생성하는 자식이며 한 행의 복원→부하→drain→수집→판정→단계 정리 Job, producer Job과 주입 기록을 소유한다.
- StageTemplate·ControlSettings·TargetIdentity·Judgment·Generator·PowerMeasurement·FaultInjectionPlan/Record는 값 타입이며 StartState/StageResult는 불변 증거다. 별도 CR을 만들지 않는다.
- 단독 EvaluationStage 실행은 금지한다. controller ownerReference가 부모의 정본이며 micro 단독 실행도 1단계 EvaluationRun으로 표현한다.
- 풀 점유·finish·release·게시는 부모만 수행한다. 자식은 Job 생성·주입 등 외부 효과 직전에 비캐시 APIReader로 부모의 점유와 자신이 active child인지 확인하며 fencing을 보장한다고 주장하지 않는다.
- background 삭제만 지원한다. foreground/orphan 삭제로 Job이 사라지면 `ExecutionLost`와 잔류를 기록하며 finalizer가 Job을 지킨다고 주장하지 않는다.
- LoadScenario·카탈로그는 prepare에서 1회 해석하여 부하 정의와 Workload 적용 정의를 snapshot으로 동결하고 자식은 snapshot만 사용한다. SLO는 고정된 Workload generation에서 얻으며 Judgment에 복사하지 않는다.

## Consequences

정의·실행·판정의 연결과 재현 가능한 입력 참조를 얻고, 화면의 비교 추측과 운영자의 명령 조합을 없앤다. 수행 오류·측정 불합격·반환 실패를 구별하며 불확실한 원격 효과를 성공으로 감추지 않는다.

비용은 새 하네스 이미지의 빌드·배포, EvaluationRun과 Job/Pod·주입 대상에 대한 범위 제한 RBAC 확장, profiler leader election 유지다. 조건·상태·증거 연결을 끝까지 구현해야 하며 반환 미확인은 운영자 확인·해제 전까지 후속 실행을 막는다.

현재 최적화 측정은 `round.py`로 계속한다. 새 경로 완성 후 기존 증거의 오프라인 judge 동일성과 controller 경로 **1회**의 `*.command.json`·표·정리 증거를 비교한다. 필요한 `Round.reports` 표 조립만 tables 경계로 옮기고 수용 뒤 `round.py`, `run-ab` 스크립트와 대시보드 비교 추측 함수를 삭제한다. 배포와의 flock 소실을 고려해 실행 중 신원 감시는 유지한다. 과거 필드 부재는 미기록이며 이름·요청률로 복원하지 않는다.

과거 게시·과학 원시·실행 기록은 수정·삭제·재게시하지 않는다. [ADR 0019](0019-durable-stored-contract-compatibility.md)의 단일 버전 원칙을 유지하고 구계약 reader·dual decoder·호환 shim은 두지 않는다. 영향받는 작업용 객체만 의존 캠페인이 없는 전환 시점에 운영자가 정리한다. 과거 과학 기록 보존과 혼동하지 않는다.

[ADR 0030](0030-observation-evidence-api-streams.md)은 관측 스트림 결정이다. 이 ADR은 이를 변경하지 않으며 기존 archive/publication 불변 계약을 유지한다.
