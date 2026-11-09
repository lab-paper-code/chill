# Serving Design

단일 HTTPS `cmd/chill-server`가 TokenReview/SAR·CRUD·추론과 불변 snapshot/접수 gate/bounded Workload 큐/physical drain·관측을 소유한다. CHILL은 physical instance를 고르고 EdgeMesh는 그 목적지로 전달한다. 노드당 서빙인스턴스1개·인스턴스당 Workload1개이며 실제 실행·측정 근거가 없으면 Ready 추론대상이 아니다.
원시 JPEG/UTF8 입력은 runner의 전처리 module이 모델 입력으로 바꾼다. firstfloor actor·persistent ORT CPU/CUDA·RKNNLite와 동일경로 profiling을 사용한다. 로컬smoke·qualified hardware/live evidence는 서로 다른 확인수준이다.
Observer readiness probes reject responses returned after their probe context is cancelled or expires, even when the native client reports success.

Cold start는 TLS·인증·HTTP 구성을 완료한 뒤 listener를 먼저 열고 첫 scheduler refresh와 Workload 복원을 백그라운드에서 수행한다. 그동안 `/api/v1` 아래의 조회·변경·추론·live 요청은 모두 `503 CoverageWarming` (`Retry-After: 2`, 기존 JSON 오류 형식)이며, 완전한 handler snapshot만 한 번 게시한다. dashboard와 `/healthz`는 먼저 응답한다. readinessProbe는 `/healthz`로 listener 도달 가능성만 검사하고 warm-up 완료를 뜻하지 않는다. `/readyz`는 기존 observation log 건강 상태만 유지한다. accept는 deadline 안에서 API의 503을 기다린다. 초기화 실패는 서버를 종료하며 부분 상태로 요청이나 scheduler Tick/Workload Advance를 실행하지 않는다.

| 파일 | 내용·상태 |
|---|---|
| [workload-management](workload-management.md) | active: 입력/CRUD/권한/접수/드레인 |
| [scheduler-implementation](scheduler-implementation.md) | active: 판단입력·전환DAG·제어불변식 |
| [scheduler-runtime-measurement](scheduler-runtime-measurement.md) | active: typed요청/IPC/시간/수명/CPU·장치/fixed-batch/catalog |
| [scheduler-api](scheduler-api.md) | active: strict설정·읽기projection·권한 |
| [configuration-hardcoding](configuration-hardcoding.md) | active: 설정소유권·동결·하네스 선택값; 구현offline과live수용 구분 |
| [heterogeneous-workloads](heterogeneous-workloads.md) | active: 원시표본/전처리·데이터셋/다중파일/생성형 |
| [workload-pools](workload-pools.md) | active: Workload별POOL/자동제어/측정노드/완료여유 |
| [measurement-admission](measurement-admission.md) | active: 노드승인/거절/반환·단일writer(M3) |
| [measurement-api](measurement-api.md) | active: operation비용/디스패치점유/재사용·과학판정 |
| [decision-record-diet](decision-record-diet.md) | completed: M0 결과·저장projection 요약 |
| [observability-split](observability-split.md) | active: 요청/catalog/시간세그먼트·evaluation raw/resources/cluster API(ADR0030) |
| [analytic-planner](analytic-planner.md) | active: 공통 adapter·v1/v2 경계·profile routing·저비용 탐색/직접 후보 평가·노드계획·고정 H |
| [path-aware-problem-statement](path-aware-problem-statement.md) | E2E 관측·제어 범위와 SLO 개선 목표 |
| [path-aware-design-answers-draft](path-aware-design-answers-draft.md) | 사용자 의견을 반영한 2차 설계 답안 |
| [path-aware-implementation-plan](path-aware-implementation-plan.md) | implemented: E2E 피드백·요청별 POST 동결·현재 tested-rate κ̂·두 SLO 우선순위; 실배포 수용은 별도 |
| [capacity-table-evidence](capacity-table-evidence.md) | completed: 당시 전체 격자 κ·탐색의 역사적 근거; 현행 수치 계약은 승인 M0 |
| [scheduler-operations](scheduler-operations.md) | active: Pod-bound Service/수동원자실행 |
| [scheduler-srs-audit](scheduler-srs-audit.md) | completed: 당시 SRS대조 결과·증거 경계 |
| [online-switching-references](online-switching-references.md) | active reference: 미채택 문헌과 적용한계 |
| [scheduler-execution-history](scheduler-execution-history.md) | completed: 배포/checkpoint 결과요약 |

- 요구사항 권위: [spec](../../spec/README.md), [scheduler SRS](../../spec/scheduler/SRS.md), [Workload SRS](../../spec/workloads/SRS.md). 설계는 명세를 대체하지 않는다.
- 현재 수용/미완료는 [실클러스터 검증 현황](../../plans/2026-10-13-scheduler-live-evidence-campaign.md), 원시입력 hardware미검증은 [이기종 설계](heterogeneous-workloads.md)를 따른다. 전체 FULL 통과를 주장하지 않는다.
- gateway split: removed @e8344b20; T5 live만 유지한다([ADR0029](../../adr/0029-gateway-control-data-plane-split.md)). 아키텍처 B1–B3 전체안의 승인대기는 [대안 요약](../architecture-rethink/README.md)과 구분한다.
