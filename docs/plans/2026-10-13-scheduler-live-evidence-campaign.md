# CHILL 실클러스터 evidence 확보 및 FULL 제어 기능 검증

## 목표·FULL 범위

실측 evidence를 실제 스케줄러에 연결하고 판단 → 제어 완료 → 요청 처리·종단 지연·전체 POOL 전력을 실클러스터와 Dashboard에서 대조한다. 프로파일 생성·플래그 활성화만으로 완료하지 않으며, 기능 연결과 SLO 개선·에너지 절감의 연구적 입증은 구분한다.

- FULL 노드는 **genesis2·자비에·나노·오린나노·라떼판다·라파·오렌지파이**다. 그 밖의 노드는 완료 선행조건이 아니다.
- GPU·NPU 우선, CPU 후순위다. 필요한 노드·구성의 부분집합으로 검증하며 전 노드 동시 참여·자원/배치 조합 완전탐색은 요구하지 않는다. 실제 선택·제어할 후보의 프로파일·전환 근거를 확보한다.
- 모델 신규 개발·학습은 제외한다. 기존 실행 산출물을 사용하고 다른 세션의 작업·안전 제한·측정 중 stored evidence 계약을 보존한다. genesis1/3 신규 프로파일링·제어는 보류다.

운영은 기존 API·CLI·Dashboard를 우선한다. 큰 의존성 단계가 끝나면 `/tmp·Eval·Bash` 반복 기록을 함께 보고, 실제 운영 공백만 최소 API·UX 개선으로 정리한다. Dashboard 구현·배포는 별도 Main 세션이 맡고 계약·검증 결과는 사용자를 통해 전달한다.

## 완료 기준·계약

기준은 [시스템 명세](../spec/README.md), [스케줄러 SRS](../spec/scheduler/SRS.md), [구현·검증 경계](../design/serving/scheduler-implementation.md)다. 판단 알고리즘의 비교 근거는 [온라인 구성 전환 조사](../design/serving/online-switching-references.md)를 따른다. 다음 기준을 축소하지 않는다.

- FULL 노드의 실행 경로·자원·meter 대응과 미지원 이유/의존성을 남긴다. 서빙과 같은 firstfloor/backend 조건의 실행시간 원시 분포·배치별 증분 에너지·메모리·idle·원자 동작·전력모드 전환 비용을 보존한다. 다른 조건이나 임의 값으로 대체하지 않는다.
- evidence의 실제 후보 채택, 입력·후보·선택 이유·예상 비용을 추적한다. 자동 판단의 입력·유지/전환 판정식·시점·전환 중 재판단 규칙을 고정 기간 비교와 구분해 검증한다. 제어 성공만으로 알고리즘 완료를 선언하지 않는다.
- **placement, resources, replicas, sharing, routing, batching, power, membership**을 각각 검증한 뒤 함께 활성화한 FULL을 검증한다. 목표 선언과 실제 readiness·분배 반영·drain·제거·membership·전력 read-back 완료를 구분한다. 미지원·미검증을 숨기지 않는다.
- 일정 부하·증가/감소·버스트·전환 직후 반전·용량 초과·테스트 소유 대상 실패에서 판단/동작/요청/지연/전체 POOL 전력을 연결하고 동일 부하 OFF 비교를 보존한다. SLO 위반·실패·거절·취소·미완료·예측/실측 차이를 결과로 남긴다.
- 실제 `chill-system` Dashboard의 인증 → 측정/evidence → 판단·실제 전환 → 추론/지연/전력을 브라우저로 조작하고 API·클러스터·시각 증거와 대조한다. FULL 근거 → 판단 → 적용 완료 → 현재 서빙·지연·전력 연결이 필요하며 모의 데이터·FULL 표시·페이지 로딩만으로 통과하지 않는다. kind 전용 e2e와 이 검증은 별개다.

## 현재 판정

**Dashboard 기능 연결의 마지막 차단은 해소됐지만, 전체 FULL 완료는 주장하지 않는다.**

- ChillServer `dad2aec` / image `fb5348b8`, CPU2/1Gi·Go soft768MiB·API50/100, fixed-horizon·9OFF다. 상세 배포/요청/전력 증거는 [최신 서버 대조](2026-10-13-scheduler-live-evidence-campaign-06-dashboard-operations.md#chillserver-리팩토링-이미지-배포새-dashboard-e2e--2026-10-16)에 있다.
- Dashboard `7b43c7c` 뒤 offset24 단건 조회와 표시된 실제 Rpi native ACK를 독립 대조했다. 상세 링크 차단은 [최종 대조](2026-10-13-scheduler-live-evidence-campaign-06-dashboard-operations.md#상세-링크-수정-후-독립-최종-대조--2026-10-16)에서 닫혔다.
- CPU 폐루프·3 profile 통합·개별 제어 실적과 연구 결과는 구분한다. 변동 부하 완료에도 버스트 SLO는 실패했고 실제 장애는118완료·2오류였다. 이력 예측의 기한 미전파·완료 action 재실행은 수정됐지만 계산량 개선·전체 에너지 절감은 입증하지 않았다. [폐루프·실패 기록](2026-10-13-scheduler-live-evidence-campaign-05-closed-loop.md)
- **자동 전력 선택에는 `powerRequests`/비용과 모드별 paired evidence가 없다.** 수동 Jetson 왕복·고정 전력 profile 통합을 자동 전력모드 최적화나 전체 FULL 통과로 바꾸지 않는다. Orin의 최근 `hetero-*` 활동은 외부 작업과의 조율이 필요하다.

## 다음 단일 의존성

**Orin의 최근 `hetero-*` 작업 소유자와 전력 작업 가능 범위를 조율한다.** 그 선행조건 없이 전력모드를 변경하거나 부족한 자동 전력 선택 근거를 성공으로 간주하지 않는다. Dashboard 결함 재조사나 완료한 CPU 실험 반복은 현재 다음 작업이 아니다.

## 과거 기록 찾아보기

아래 파일은 당시 상태·실패·evidence의 기록이며 현재 실행 지시가 아니다.

1. [ARM64 실행 이미지 검증](2026-10-13-scheduler-live-evidence-campaign-01-runtime.md)
2. [실행 선언·상시 모델 등록·설정 지속성](2026-10-13-scheduler-live-evidence-campaign-02-declarations.md)
3. [정식 측정·노드 조사·반복 trial](2026-10-13-scheduler-live-evidence-campaign-03-profiling.md)
4. [제어·폐루프·배포/Dashboard 주제별 목록](2026-10-13-scheduler-live-evidence-campaign-04-control.md)
