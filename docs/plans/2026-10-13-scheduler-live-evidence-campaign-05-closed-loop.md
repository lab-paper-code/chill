# CHILL 캠페인 — 폐루프·실패·복구 결과

- 상태: completed(2026-10-15~16 기록). CPU 폐루프와 실제 제어를 연결했지만 변동 부하·버스트의 SLO 실패를 성공으로 바꾸지 않았다.
- OFF 30건 완료 구간의 tight p95 2.359148466s는 목표 1s 초과다. wall energy는 다른 작업도 포함하므로 CHILL 증분 소비가 아니다.
- 실제 실행기 SIGTERM 구간: 118건 완료·2건 BackendUnavailable, 이후 두 Workload 각 53건 Rpi 완료. 주입 순간 in-flight 0이라 실행 중 요청 중단은 미검증이다.
- `20e5a759`: 동일 action ID·계약의 live terminal receipt를 오래된 running/cancel snapshot보다 우선해 완료 action 재실행을 수정했다.
- 배포 후 40초 smoke: 요청 4/4 완료, route/stop/drain/remove 각각 시작1·완료1·partial0·중복0. 결정적 race는 회귀 근거와 구분하며 전력 비교 주장은 없다.
- 근거: `terminal-receipt-replacement-smoke-20e5a759-20261016T033504Z/`, `owned-latte-fault-protocol/new-declared-single-controller-20261016T022127Z/integrated-final-report.json`.
- 당시 Dashboard 차단의 해소는 [Dashboard 결과](2026-10-13-scheduler-live-evidence-campaign-06-dashboard-operations.md), 현행 연구 결과는 [E5 보고서](../reports/e5-evaluation-20261030.md).
