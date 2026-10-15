# CHILL 실클러스터 evidence 확보 및 FULL 제어 기능 검증

## 궁극적 목표

아래 FULL 범위의 노드를 활용하여 CHILL 스케줄러가 실제 판단·제어에
소비할 수 있는 실측 evidence를 확보한다. 그 근거를 실제 스케줄러에 연결하고,
실클러스터에서 판단 → 제어 실행 → 실제 상태 변화 → 요청 처리 및 지연·전력
관측까지 종단으로 검증한다.

모델 신규 개발·학습은 이번 세션의 범위가 아니다. 기존 실행 산출물로 검증한다.
프로파일 생성이나 제어 플래그 활성화만으로 목표를 달성했다고 보지 않는다.
기능 검증 결과와 연구적 SLO 개선·에너지 절감의 입증은 구분한다.

FULL의 노드 범위는 genesis2·자비에·나노·오린나노·라떼판다·라파·오렌지파이로 한정한다.
그 밖의 노드는 완료의 선행조건이 아니다. 실제 제어·판단·Dashboard 종단 검증 기준은 유지한다.
측정은 GPU·NPU 가속기를 우선하고 CPU는 후순위로 둔다. 실행 그룹은 검증에 필요한
노드·구성의 부분집합으로 구성하며, 전 노드 동시 참여나 자원·배치 조합의 완전탐색은
요구하지 않는다. 실제 선택·제어할 후보에 필요한 프로파일·전환 근거만 선별 확보한다.

기준 계약:
- [시스템 명세](../spec/README.md)
- [스케줄러 SRS](../spec/scheduler/SRS.md)
- [스케줄러 구현 및 검증 경계](../design/serving/scheduler-implementation.md)
- [온라인 구성 전환 알고리즘 조사](../design/serving/online-switching-references.md)

## 완료 기준

- 위 FULL 범위 노드의 지원 실행 경로·자원·meter
  대응을 확인한다. 실제 사용하지 못한 노드·경로는 이유와 미충족 의존성을 남긴다.
- 서빙과 동일한 firstfloor/backend 조건에서 실행시간 원시 분포, 배치별 증분
  에너지, 메모리 및 실행 조건을 확보하고, 필요한 idle·원자 동작·전력모드
  전환 비용 evidence를 보존한다. 임의 값이나 다른 실행 조건으로 대체하지 않는다.
- evidence가 실제 후보 평가에 채택되는 것을 확인하고 판단의 입력·후보·선택
  이유·예상 비용을 추적할 수 있다.
- 자동 판단의 입력·유지/전환 판정식·제어 시점·전환 중 재판단 규칙을 확정하고,
  기존 고정 기간 비교와 구분해 구현·검증한다. 제어 실행 성공만으로 판단 알고리즘의
  완료를 선언하지 않는다.
- placement, resources, replicas, sharing, routing, batching, power, membership을
  각각 검증한 뒤 함께 활성화한 FULL 제어를 검증한다. 지원되지 않거나 미검증인
  제어를 숨기고 FULL 통과라고 보고하지 않는다.
- 목표 선언과 실제 적용을 구분하고, readiness·분배 반영·drain·제거·membership·
  전력 read-back 등 해당 동작의 실제 완료를 확인한다.
- 일정 부하, 증가·감소, 버스트, 전환 직후 부하 반전, 용량 초과 및 테스트 전용
  대상의 실패 상황에서 판단·동작·요청 결과·종단 지연·전체 POOL 전력을 연결한다.
- 동일 부하의 제어 OFF 비교 기록을 확보한다. SLO 위반·실패·거절·취소·미완료와
  예측/실측 차이를 숨기지 않고 결과로 남긴다.
- 실제 `chill-system`의 CHILL Dashboard를 브라우저로 조작하여 인증,
  측정/evidence 조회, 스케줄러 판단·실제 전환, 추론·관측의 e2e 흐름을 검증한다.
  화면 표시를 실제 API 및 클러스터 상태와 대조하고 시각 증거를 남긴다.
  최종 화면에는 FULL 제어의 실측 근거 → 판단 → 실제 적용/전환 완료 →
  현재 서빙 상태·지연·전력이 명세의 의미대로 연결되어 보여야 한다.
  모의 데이터, 단순 FULL 활성화 표시나 페이지 로딩만으로 통과시키지 않는다.

## 작업 순서

**현재 우선:** 검증된 Rpi CPU2↔Latte CPU1의 최신 Serving 보정값을 채택하고 실제 폐루프 비교를 진행한다.
공유 통신·owner HTTPS는 복구됐고 owner CPU limit 1코어에서 두 실행기 각각 24건이 모두 완료됐다.
실제 CPU 제어와 판단 알고리즘 구현은 완료했지만 폐루프 비교·FULL 통과는 미완료다.
자동제어는 아직 모두 OFF이며 지속 부하 입력 freshness·SLO·전체 POOL 전력을 함께 확인한다.
Dashboard 수정·배포는 별도 Main 세션에 인계했으며 이 작업의 완료로 세지 않는다.

1. **판단 알고리즘 — 구현·배포 완료, 폐루프 비교 미완료:** 검증된 Latte/Rpi CPU 노드·동작으로 구현했다.
   전 노드나 외부 조율 중인 power/GPU 완료를 기다리지 않으며 미검증 동작은 후보에서 제외한다.
   조사 문헌과 현재 구현을 대조해 워크로드/SLO·부하/큐·evidence·가용 자원·실제 전환 상태
   입력에서 유지/목표 구성·제어 동작·실행 시점과 계속/취소/재선택 규칙을 확정한다.
   비중첩 receipt interval의 실제 방향별 measured DAG kernel과 min-plus history를
   `projected-work-history`로 연결했다. 고정 H는 `fixed-horizon` 비교 기준으로 유지하며,
   queue-reset projection은 실제 queue 이력/고전 WFA 보장이 아니다. 구현과 live 비교 통과를 구분한다.
2. **자동 실행 gate:** decision origin에서 실제 source freshness ≤5초는 live 한 iteration을
   통과했다. 지속 부하 중 freshness·SLO feasibility와 fresh observed energy는 계속 검증해야 한다.
   무부하 KeepFeasible·부분 전력 누락을 통과로 대체하지 않으며, gate 전까지 자동 제어는 OFF다.
   복구 후 owner CPU 1코어에서 Rpi p90/p95는 560/316ms, Latte는 335/357ms로 각각
   원래 1500/1000ms SLO를 충족했다. 이전 실패·장애와 구분하며 미래 지연 보장으로 해석하지 않는다.
3. **폐루프 검증:** 짧은 부하·지속 부하·반복 변동에서 판단 → 실제 실행 → 피드백과
   SLO·전환 포함 전체 POOL 에너지를 확인한다. 평가기·배처 검증은 판단 정책 설계를 대신하지 않는다.
4. **FULL·Dashboard:** 남은 개별 제어를 검증·통합하고 완료 기준의 실제 시나리오를 수행한다.

### 단계 경계의 운영 API·UX 고도화

큰 의존성 단위가 끝날 때, `/tmp` 파일/스크립트·Eval Tool 실행 기록·Bash 명령어 기록과
직접 조작을 함께 검토해 다음 단계의 반복 흐름과 필요한 고도화를 판단한다.
계약이 검증됐고 구현·검증 비용보다 후속 반복 비용이 클 때만
최소 기능으로 정리한다. 기존 API·CLI·화면을 우선하고 적합한 경계에서 실검증한다.
효과가 없으면 건너뛰며 임시 명령 전체의 API화나 미확정 판단 정책의 API 고정은 하지 않는다.
종단 검증 후에는 단계별로 놓친 흐름만 종합 회고한다.

## 실행 원칙

- 의존성상 다음 핵심 작업 하나를 정해 추가 승인 없이 진행한다. 명세·실측 증거로 판정한다.
- 부모는 의존성·통합·완료 판정을 맡고 큰 조사·구현·리뷰는 작고 명확한 단위로 위임한다.
- 기존 API를 우선한다. 명세 불일치·누락과 위 단계 경계에서 입증된 필요만 구현한다.
  변경은 작은 커밋·ponytail 리뷰·필요 리팩토링·실행 검증으로 마무리한다.
- 최신 계약만 지원하며 호환 reader·alias·fallback을 추가하지 않는다. 관련 생산자·소비자를 함께 갱신한다.
- 이 문서는 현행 목표·순서·차단만 유지한다. 세부 실행 조건·결과·경로는 아래 기록에 보존하며
  과거 상태·실패 당시의 다음 작업을 현재 지시로 재사용하지 않는다.

## 실행 경계

- 모델 생성·학습은 제외한다. genesis2는 위 FULL 범위에 포함하며 genesis1/3 신규 프로파일링·제어는 보류한다.
- 다른 세션의 변경·실제 노드 상태를 확인하고 외부 작업을 보존한다. 안전 제한을 우회하지 않는다.
- 실행 중인 측정의 stored evidence 계약을 변경하지 않는다.
- 실제 Dashboard 브라우저 검증과 kind 전용 e2e suite를 구분한다. testbed에서 후자를 실행하지 않는다.
- Dashboard 수정·배포는 별도 Main 세션이 담당한다. 연결 계약과 검증 시나리오는 사용자 전달용 프롬프트로 인계한다.

## 현재 상태·다음 작업

- owner 측정 목록·preflight/server dry-run·프로파일 채택 해제·귀속 근거 영구 삭제를 실검증했다.
  폐기 측정의 owned key 21개와 native BEO·소유 Run 9개가 제거됐고 현재 5개 profile·Rpi CPU2 서빙은 유지됐다.
  신규 실측·정상 제출 `202`는 실행하지 않았다. 세부 근거는 [운영 기록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)에 있다.
- placement는 실제 요청 30건과 Latte → Rpi 이동·이전 Pod 제거·Dashboard 연결을 확인했다.
- resources 제어 실행은 기존 API·원자 동작의 remove → prepare로 Rpi CPU1 → CPU2를 실제 적용했고,
  같은 모델·SLO로 복원한 두 Workload의 요청 4건이 완료됐다. 자동 선택·무중단 resize 성공은 아니다.
- replicas/routing/sharing/membership 수동 검증을 완료했다. 원래 Rpi CPU2와 두 Workload UID·모델·SLO를
  유지한 채 Latte replica로 relaxed만 분배했고, 전체 경로 원복·정확한 replica 제거·membership
  detach/join 후 두 UID가 같은 원래 Rpi CPU2 instance를 공유하며 요청 4건을 완료했다.
  세부 근거는 [제어 기록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)에 있다.
- firstfloor의 실제 in-flight SLO/원래 receivedAt/lifetime 문맥과 현재 적용 calibration policy를
  확인했다. batch2 실행·자동 선택·SLO 개선·에너지 절감은 입증하지 않았다. 자동 제어는 모두 OFF다.
- 다음은 검증된 CPU 부분집합의 알고리즘 설계·구현이다. 이전 minimum-loop gate의 최근 60초
  도착 0건 KeepFeasible은 부하 증거가 아니다. 시간 계약 수정 후 live 한 iteration의 source
  freshness는 통과했고 기존 두 UID/Rpi CPU2 요청 4건도 완료됐다. 지속 부하 freshness·SLO·
  fresh energy gate는 남아 있으며, 마지막 whole-power 검증의 Jetsonx timeout은 숨기지 않는다.
- genesis2 GPU 배치 1/2의 기존 9회 결과를 새 `profiles adopt`로 채택했고 기존 4개와 모두 로드됐다.
  미허용 GPU 거절·설정 무변경, 명시적 허용 후 채택, 반복 채택 무변경과 측정 dry-run 미생성을 확인했다.
  실제 GPU 인스턴스 준비·자동 선택·SLO/에너지 개선의 증거는 아니다.
- scheduler census 배포(`63521317b5bb`) 후 같은 과거 60초 전력 조회는 28.997초→0.307초,
  이전 기본 1시간 이력 축은 owner 직접 조회 200·0.181초·50건/nextCursor로 개선됐다.
  전력은 실제 gap 사유·10개 노드 각 79표본·`complete=false`·전체/활성 합계 null을 유지한다.
  BFF·브라우저 재검증은 별도 Main 완료를 기다리며, 원시 전력 품질·FULL 성공으로 세지 않는다.
- 기존 AGX Orin 차단은 FULL 범위 밖이다. 대상 Jetson의 전력 안전 조건은 별도 확인한다.
  power와 자동 판단·FULL 종단은 미완료이나 알고리즘 착수의 전 노드 대기 조건은 아니다.
- `/pools` 오류는 수정·배포·실제 브라우저 검증을 마쳤다. 이것은 FULL Dashboard 완료가 아니다.

## 진행 기록 찾아보기

기록 당시 상태·실패·사용자 지시·evidence 경로는 아래에 그대로 보존한다.
새 결과는 해당 기록 파일에 남기고, 현재 상태와 다음 의존성은 이 진입 문서에 갱신한다.

1. [초기 작업 후보·ARM64 실행 이미지 검증](2026-10-13-scheduler-live-evidence-campaign-01-runtime.md)
2. [실행 선언·상시 모델 등록·설정 지속성](2026-10-13-scheduler-live-evidence-campaign-02-declarations.md)
3. [정식 측정·노드 조사·반복 trial](2026-10-13-scheduler-live-evidence-campaign-03-profiling.md)
4. [측정 근거 연결·원자 제어·실제 서비스 관측](2026-10-13-scheduler-live-evidence-campaign-04-control.md)
