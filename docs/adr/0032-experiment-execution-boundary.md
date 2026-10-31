# 0032 Experiment Execution Boundary

## Status

Proposed — 사용자 승인 대기. 아래는 예정 대체 설계이며 CRD·실행기는 아직 구현되지 않았다. 현재 실행 수단은 `hack/e5/round.py`다.

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
| Experiment 전용 Lease | 기존 leader election과 실행 중·반환 미확인 실험 검사로 충분하다. |
| 판정기를 Go로 이식 | 기존 계산식을 다시 쓰면 판정 의미가 바뀔 위험이 있다. |

## Decision

상세 필드·단계·실패 처리는 [실행 설계](../design/profiling/experiment-execution.md)가 소유한다. 요구사항은 [실험 SRS](../spec/experiments/SRS.md) EXP-01~15를 따른다.

1. namespaced `Experiment` CRD에 질문·조건·순서·비교·장애 주입·불변 입력 참조를 선언한다. spec 전체는 CEL `self == oldSelf`로 불변이며 정정은 새 CR이다. status는 `observedGeneration`과 type-keyed `metav1.Condition`을 사용하고 실행 완료·측정 판정·정리 완료를 구분한다.
2. 기존 profiler manager에 reconciler를 둔다. controller는 순서·공개 명령·Job 생명주기·compact status만 소유한다. 기존 다른 측정 CR 실행 위치는 이 결정으로 옮기지 않는다.
3. 배포·업그레이드는 하지 않는다. 기대 신원과 관측한 image digest·source SHA가 다르거나 신원을 확인할 수 없으면 시작하지 않는다. 이는 배포 신원 확인이며 새 digest 형식·재계산·바이트 동일성 검증 관문은 아니다.
4. 시험 대상 제어·관측에는 ChillServer 공개 API와 Kubernetes API만 쓴다. E5-7 주입도 선언된 계획에 따라 Kubernetes API로 수행하며 ChillServer 내부 호출이나 장애 주입 API를 만들지 않는다.
5. profiler leader election 활성화를 전제로 단일 worker가 최신 실행 장부를 확인한다. 생성 순서로 한 실험만 실행하고 반환 미확인 실험도 다음 실행을 막는다. 별도 Lease·우선순위 큐는 없다. 실제 노드 예약은 기존 ChillServer admission이 계속 소유한다.
6. 기록된 단계 경계에서 재개한다. 시작한 부하는 기록된 Job/Pod UID를 관측할 뿐 재발행·재측정하지 않는다. 원격 효과 불명·실행 소실이면 중단하고 원 사유·증거·예약을 보존하며 자동 물리 복구하지 않는다.
7. 수집·기존 Python 판정·power reducer·표·게시를 새 하네스 이미지의 Job 하나에서 수행한다. Experiment당 Job·Pod attempt는 하나이고 자동 재시도하지 않는다. Go로 계산식을 복제하지 않으며 Job 성공을 측정 PASS나 증거 완료로 간주하지 않는다.
8. 게시물에 spec 스냅샷·CR 신원·동결 설정·실행/판정/정리 상태·증거 참조를 포함한다. ChillServer는 불변 게시물만 읽고 Experiment CR은 읽지 않는다. 대시보드는 명시된 정책·비교 관계·SLO를 사용한다.
9. 운영 상한 등 설정값은 profiler config에서 선택하고 실제값과 읽은 출처를 점유 시작 시 `status.frozenConfig`와 실행 증거에 동결한다. 실행 중 설정 재조회로 조건을 바꾸지 않는다.

## Consequences

정의·실행·판정의 연결과 재현 가능한 입력 참조를 얻고, 화면의 비교 추측과 운영자의 명령 조합을 없앤다. 수행 오류·측정 불합격·반환 실패를 구별하며 불확실한 원격 효과를 성공으로 감추지 않는다.

비용은 새 하네스 이미지의 빌드·배포, Experiment와 Job/Pod·주입 대상에 대한 범위 제한 RBAC 확장, profiler leader election 활성화다. 조건·상태·증거 연결을 끝까지 구현해야 하며 반환 미확인은 후속 실행을 막는다.

현재 최적화 측정은 `round.py`로 계속한다. 새 경로 완성 후 같은 정의를 양쪽에서 **1회씩** 실행해 조건·명령·판정·표·정리 증거를 비교한다. 수용 뒤 `round.py`, `run-ab` 스크립트와 대시보드 비교 추측 함수를 삭제하고 새 경로로 단일 전환한다. 과거 필드 부재는 미기록이며 이름·요청률로 복원하지 않는다.

과거 게시·과학 원시·실행 기록은 수정·삭제·재게시하지 않는다. [ADR 0019](0019-durable-stored-contract-compatibility.md)의 단일 버전 원칙을 유지하고 구계약 reader·dual decoder·호환 shim은 두지 않는다. 영향받는 작업용 객체만 의존 캠페인이 없는 전환 시점에 운영자가 정리한다. 과거 과학 기록 보존과 혼동하지 않는다.

[ADR 0030](0030-observation-evidence-api-streams.md)은 관측 스트림 결정이다. 이 ADR은 이를 변경하지 않으며 기존 archive/publication 불변 계약을 유지한다.
