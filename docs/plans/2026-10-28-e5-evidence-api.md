# E5 증거 API — 결과 요약

- 상태: completed(코드·전환 리허설). 현행 API·원시/stream 계약은 [관측 설계](../design/serving/observability-split.md), [ADR 0030](../adr/0030-observation-evidence-api-streams.md)가 권위다.
- publication은 불변이다. energyCoverage·gapErrorBoundJoules·측정 규칙 식별자(harness SHA/coverage/evidenceSource/gcMeaning)를 함께 게시하고 과거 기록을 현재 기본값으로 채우지 않는다. 비교 관문은 만들지 않는다.
- 구현: `8c6bef14`, `c355e3cd`, `2d18dcee`, `85a9cab3`, `94a7a4d4`; 하네스 API-only cutover `d39c9576`, collector 가공 GET RBAC `f75596f6`. 옛 CHILL SSH/crictl/host/S3 수집과 영수증 판정은 제거했다.
- CHILL 밖에는 edge-metrics 전력·producer/client 장비·STOP 감시·E5-7 운영자 명령만 남긴다. GC는 runtime-metrics-delta, recovery는 scheduler/cluster 출처로 바뀐다.
- raw는 하네스 전용이며 대시보드는 가공 조회만 쓴다. 열린 분은 내보내지 않고 local→archive 읽기를 재사용한다. raw와 과거 대시보드 조회는 측정 창 밖에서 수행한다.
- settings의 meters/requestTimeout은 읽기 전용이고 두 쓰기 클라이언트가 PUT에서 제거한다. podName은 실제 Pod identity에서 투영하며 별도 Kubernetes 읽기를 대체한다.
- rev106~110 리허설에서 옛/API KPI 및 #213 재판정 동일. 추가 CPU 몫 live2.5%·수신7.3%·raw1.1%·대시보드16.8%; agent1s 표본 rasp5 0.24%·nano4.5% of core. 단발 리허설이지 상시 성능 보장은 아니다.
- 결손 jetsonx identity·orin-nano(POOL 외) 수집0은 미완이다. 예약 잔류/rate limiter 재발도 [로드맵](2026-10-26-remaining-roadmap.md)에 유지한다.
- #213~#225는 rev114에서 새 ID 4건으로 첫 게시했으며 재게시가 아니다. 실제 archive SHA·host-s3·gctrace-cycles와 회차 조건을 보존했다. [대시보드 결과](2026-10-26-dashboard-live-stage.md).
- 근거: `O/campaign/v2/harness-cutover-20261030/`, `O/campaign/v2/dashboard-acceptance-20261030/`, `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`.
