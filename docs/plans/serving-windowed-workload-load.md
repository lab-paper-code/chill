# Workload 시간창 부하 집계 — 통합 결과

- 상태: superseded. 지정 구간 부하 분석은 [스케줄러 구현 설계](../design/serving/scheduler-implementation.md)의 운영 관측과 후보 입력에 통합했다.
- `received`(수신), `accepted`(접수), `assigned`(배정)의 요청 수·requests/s·시간 구간·인스턴스별 Workload 배정량을 구분한다. assigned는 backend 호출 전 CHILL 사건이며 native 수신·실행 증거가 아니다.
- 원시 기록·명시적 시간 구간·결측 의미를 유지한다. 임의 고정 lookback·새 캐시·복구 상태기계·Dashboard 부하 화면을 추가하는 계획이 아니다.
- 계약 권위: [라우팅 명세](../spec/03_routing.md), [스케줄러 SRS](../spec/scheduler/SRS.md). 운영 제어 전체 완료를 이 조회 하나로 주장하지 않는다.
