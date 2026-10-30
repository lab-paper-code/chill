# Dashboard 측정·용량 API coverage — 결과 요약

- 상태: completed(화면 구현). 요구사항 권위는 [Dashboard SRS](../spec/dashboard/SRS.md) DB-FR-45·53~55, 현행 화면/API 소유는 [설계](../design/dashboard/README.md).
- 기존 GET 동작 비용 측정·분배 점유시간 d 목록/상세와 `/scheduler/capacity` 행 근거를 연결했다. UI query는 선택·필터·identity 전용이며 upstream에 전달하지 않는다.
- OperationCostMeasurement는 power/prepare/idle/remove의 순차 측정이다. 양방향 반복 PowerModeTransitionCost의 시간/J/Serving 지속 계약을 대신 제공하지 않는다.
- 실제 API는 두 측정 모두 `status.reason`만 제공한다. 원 reason을 표시하고 등록된 코드에만 안내를 붙인다. d는 ms/request 주표기와 정확한 ns/request를 보존한다.
- Main 실데이터 조회에서 동작 비용35개·d149개를 확인했고, Failed/Insufficient 사유가 가려지는 표시를 고쳤다. 용량 Dialog는 제목에 초기 focus·닫을 때 행 focus 복원한다.
- 근거 `b3630301`: 기존 통합 검사 60개·typecheck/lint/build 통과 기록. Asta Sans fallback 경고는 유지된다. 이 문서 정리에서 재검사는 하지 않았다.
- 실클러스터 RBAC·deadline·실시간 갱신 전 범위를 수용 완료로 확대하지 않는다. 배포·라이브 미확인은 [라이브 무대 계획](2026-10-26-dashboard-live-stage.md)에 남긴다.
