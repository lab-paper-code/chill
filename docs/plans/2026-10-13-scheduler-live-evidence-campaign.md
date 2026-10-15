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

**현재 우선:** 구 CUDA 실행기의 설정 계약 불일치를 최신 SDK로 정리하고 새 근거로 실제 GPU 서빙을 검증한다.
CPU OFF/`fixed-horizon`/`projected-work-history` 동일 150초 trace는 arm별 190건 전부 완료·SLO 통과다.
자동 Latte → Rpi prepare·route·stop·drain·remove와 이전 Pod 소멸, 비중첩 두 구간의
min-plus history 전진을 확인했다. 세부 수치·실패·경계는 [제어 기록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)에 둔다.
현재 제어는 모두 OFF이며 Rpi가 서빙한다. owner limit CPU1/512Mi·evidence-store PVC 8Gi를 유지한다.

1. **CPU 비교 완료의 경계:** 원래 150초 trace를 보존했다. projected 정리가 150초 뒤까지
   이어져 세 arm 모두 무부하 tail을 포함한 210초 공통 구간을 **사후 선언**했다.
   10개 meter의 전체 wall energy는 완전하지만 에너지 절감·통계적·인과적 주장은 하지 않는다.
   queue-reset projection은 실제 queue 이력이나 고전 WFA 보장이 아니다.
2. **GPU·Jetson:** GPU CLI는 리뷰·gate·커밋 완료. 실제 준비는 기존 CUDA 이미지의
   `completionOverheadMS` 미지원으로 실패해 생성 자원을 정리했다. 과거 측정은 수정하지 않는다.
   Jetson 3대의 현재 물리 모드는 확인했지만 단일 허용 모드·외부 Pod 안전 차단으로 전력 왕복은 미검증이다.
   별도 제어 OFF 16건 동시 요청은 모두 완료했으나 p90/p95 3325/3421ms로 SLO 초과했다.
   이는 앞선 trace 성공과 구분하며 자동 과부하 대응 통과로 세지 않는다.
3. **FULL·Dashboard:** 남은 제어의 실제 완료와 통합 시나리오를 검증한 뒤 실제 Dashboard
   e2e를 수행한다. Dashboard 수정·배포는 별도 Main 세션에 인계했으나 완료 통지는 받지 못했다.
   CPU 부분집합 성공은 FULL·Dashboard 완료가 아니다.

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

- CPU 세 arm의 동일 초기 Latte·동일 trace 비교와 실제 Rpi 전환을 완료했다. 모든 제어는 OFF다.
- placement/resources/replicas/routing/sharing/membership의 개별 수동 실적과 측정 운영 API·CLI,
  기존 GPU profile 채택 근거는 [제어 기록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)에 보존한다.
  GPU profile 채택은 실제 CUDA 준비·자동 선택 증거가 아니며 batch2 자동 선택도 미입증이다.
- 이전 통신 장애·OOM·freshness 정지·SLO 초과·전력 누락은 원시 기록과 함께 남긴다.
  최신 비교의 완전한 전력 구간으로 과거 실패를 덮지 않으며 연구적 개선을 주장하지 않는다.
- GPU CLI 리뷰·gate·real CUDA, Jetson 실제 제어와 남은 FULL 시나리오가 다음 의존성이다.
  단일 허용 power mode·Xavier 외부 Workload 안전 제한을 우회하지 않는다.
- `/pools` 오류 수정·브라우저 확인과 census owner 조회 개선은 완료했지만 최종 FULL Dashboard
  e2e는 미완료다. 별도 Main의 Dashboard 완료 통지는 아직 받지 못했다.

## 진행 기록 찾아보기

기록 당시 상태·실패·사용자 지시·evidence 경로는 아래에 그대로 보존한다.
새 결과는 해당 기록 파일에 남기고, 현재 상태와 다음 의존성은 이 진입 문서에 갱신한다.

1. [초기 작업 후보·ARM64 실행 이미지 검증](2026-10-13-scheduler-live-evidence-campaign-01-runtime.md)
2. [실행 선언·상시 모델 등록·설정 지속성](2026-10-13-scheduler-live-evidence-campaign-02-declarations.md)
3. [정식 측정·노드 조사·반복 trial](2026-10-13-scheduler-live-evidence-campaign-03-profiling.md)
4. [측정 근거 연결·원자 제어·실제 서비스 관측](2026-10-13-scheduler-live-evidence-campaign-04-control.md)
