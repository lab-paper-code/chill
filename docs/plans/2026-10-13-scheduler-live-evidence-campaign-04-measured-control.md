# CHILL 캠페인 — 측정 채택과 제어 결과

- 상태: completed(2026-10-14~16 기록). 5노드 q1/q2 10 profile·90 trial을 production catalog에서 읽고 실제 prepare/remove·membership·resize 및 CUDA 비용 연결을 확인했다.
- thermal metadata 추가로 GPU idle meter 연결이 빠진 원인은 `6d9cc7cd`에서 동일 power-series 의미 비교로 수정했다. Node·scope 검증과 실패 원시는 보존했다.
- 동일 3 Ready 실행기·190건/150초 trace, 공통 lead-in 30초·tail 60초 비교:

| 조건 | 완료 | tight p95(목표 1000ms) | relaxed p90(목표 1500ms) | 전체 POOL wall J |
|---|---|---|---|---|
| OFF | 190/190 | 258.711ms | 243.321ms | 54123.251 |
| FULL | 190/190 | 425.123ms | 270.998ms | 57495.104 |

- FULL route/stop/drain/remove 8동작 완료와 gross energy 증가는 모두 결과다. CHILL 증분 에너지 절감·인과·통계 개선 또는 자동 전력모드 최적화를 주장하지 않는다.
- 근거: `/tmp/chill-completion-cpu-cost-assembly/integrated-three-profile-cpu2-rootfixed/`; Dashboard 크기 차단의 후속 해소는 [Dashboard 요약](2026-10-13-scheduler-live-evidence-campaign-06-dashboard-operations.md).
