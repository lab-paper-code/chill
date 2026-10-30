# 0031 Configuration Ownership and Freezing Paths

## Status

Accepted — 2026-10-30. 문서 결정이며 구현·측정 완료를 뜻하지 않는다.

## Context

코드의 숫자를 모두 설정으로 풀면 프로토콜까지 운영 정책이 되고, 하나의 설정 계층에 모으면
serving·측정·하네스의 책임이 섞인다. 반대로 측정 조건을 상수로 고정하면 Run을 재현하기 어렵다.
AGENTS.md는 측정·판정 값의 설정화, 선택값과 출처의 불변 실행 기록, 단일값 정책 필드 금지를 요구한다.
현재 settings API와 관측 scheduler stream, 측정 CR/frozen Run, E5 config/binding이 이미 있다.

## Decision

1. A는 값의 소유자에게, B는 환경 overlay/JSON에게 옮긴다. C는 이름 있는 상수로 두며 D는 유지한다.
   저장 증거나 판정에 영향을 주지 않는 화면 분위수·chart 집계 창은 C/D다.
2. 실행 중 조정할 serving 정책은 기존 SchedulerSettings GET/PUT에 넣고 초기 적용과 변경을
   기존 scheduler stream에 기록한다. before/after·actor·validatedAt·appliedAt/error로 시도와
   성공 적용을 구분한다. 이는 serving 시간선의 동결이며 immutable ProfilingRun과 동일하지 않다.
3. request/drain timeout, source/profile/directory interval 등 기동 옵션은 기존 flags/values에 둔다.
   프로세스가 실제 적용한 값을 기존 scheduler stream의 startup 한 행에 기록한다. 새 stream은 없다.
4. profiler 측정·수용 정책은 기존 측정 CR spec와 frozen Run/attempt 입력이 소유한다.
   하네스 정책은 CHILL_EXPERIMENT_CONFIG를 이용한 실험 config로 받고 정상화한 effective config를
   기존 binding JSON에 보존한다. 하네스 정책을 서버 settings에 넣지 않는다.
5. 기본값은 현재값이다. 단일값만 받는 새 필드는 금지하고 구조·의미 검증은 신뢰 경계에서 한 번 한다.
   새 의존성, 전역 설정 서비스, 다중 버전 reader, compatibility shim은 추가하지 않는다.
6. 저장 계약 변경은 [ADR0019](0019-durable-stored-contract-compatibility.md)에 따라 캠페인 사이
   한 작업 단위에 한 번 모아서 적용한다. 필요한 구형 객체는 운영자가 정리한다.
   바이트·hash equality/recomputation을 새 validation gate로 만들지 않는다.

## Evidence and Consequences

`internal/serving/httpapi/scheduler.go:140-170`의 기존 PUT은 ConfigMap 변경 전 의도를 기록하고
full refresh 뒤 적용/오류를 기록한다. `observation_log_scheduler.go`가 이를 scheduler 행으로 저장한다.
그러나 기동값이나 외부 ConfigMap 수락 기록은 이 경로만으로 완전하지 않아 보완해야 한다.
`hack/e5/e5.py:1294`는 config/schedulerSettings/sources/schedule을 binding에 보존한다.
파일 overwrite가 가능한 현재 binding을 물리적 write-once 저장소라고 부르지 않는다.
완결 binding은 해당 실험에서 변경하지 않고 다음 실행은 새 binding을 만드는 계약으로 동결한다.

기존 소유자를 재사용하므로 추가 설정 계층은 없다. 대신 모든 값의 전달과 기록을 끝까지 연결해야 한다.
기존 CLI와 Helm 기본값이 다른 경우 양쪽을 그대로 보존하며 값이 같다고 꾸미지 않는다.
역사 스크립트는 재설계하지 않고 삭제 후보로만 남긴다. 실제 삭제에는 별도 사용자 승인이 필요하다.
필드·기동 행·병렬 slice와 회귀 기준은 [상세 설계](../design/serving/configuration-hardcoding.md),
범위·열린 점은 [계획](../plans/2026-10-30-configuration-hardcoding.md)이 소유한다.
