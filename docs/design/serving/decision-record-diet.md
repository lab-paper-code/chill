# 판단 기록 다이어트 (M0)

상태: completed — `SCH-30` 기록 축소; 현행 보존·조회는 [관측 분리](observability-split.md), 계획은 [용량표 계획기](analytic-planner.md)가 권위다.
- 원인: 후보×인스턴스 및 arrival×Definition 중복으로 E1 규모 합성 기록 18.8 MB(10초 주기 6.8 GB/h), 실제 약10 MB·128 MiB OOM·PVC8Gi 포화.
- 저장 직전 투영만 적용하며 메모리 판단/live full API는 유지한다. keep/target 인스턴스는 profileID, 동일 target은 `{id,keep}`; capacity와 evaluationElapsedNanos는 보존한다.
- snapshot은 매 판단 저장하되 arrivals를 빼고 search의 windowStart/windowEnd/received/accepted/computeInputs/loads를 남긴다. outstanding은 definitionID, 본문은 workloads에 둔다.
- observedAt, workloadUID, action elapsedNanos, routingCredits는 원값을 보존한다. 이력·Dashboard·A/B는 같은 저장 route를 읽는다.
- 합성 compact 결과 약68 KB(약25 MB/h); `TestSchedulerRecordStoresCompactDecision`의 수용 상한은135 KiB이며 replay에는 참조 profile/catalog/evidence가 필요하다.
- E1 재실행(2026-10-19 UTC rev35): 498건 중앙185,359 B(목표≤100 KB 미달), 2 req/s 66.3 MB/h, RSS111.4 MiB/limit512 MiB·재시작0, PVC273 MiB/h.
- 같은 실행 Tick p99: 2 req/s 6,166 ms; 5 req/s p99/max22,575 ms. 이 수치는 M1/M4 이후 성능 보장이 아니다.
- 로컬은2시간 세그먼트와 미업로드분을 보존한다. 무한 보존·현재 source의 live 수용 완료는 주장하지 않는다.
