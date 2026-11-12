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
| [request-control-interference-plan](request-control-interference-plan.md) | implementation: 요청·관측 잠금 결합 해소, capture·replay 경량화와 수용 기준 |
| [control-loop-cost-and-acceptance-plan](control-loop-cost-and-acceptance-plan.md) | implementation: exact 재생 경량화·전체 제어 시간·종료 Pod-only 정리와 실수용 |
| [control-decision-recovery-plan](control-decision-recovery-plan.md) | plan: 동일 decision 재현으로 확인한 탐색 처리율 불일치 수정, 제어 시간·측정 유효성 수용 |
| [control-loop-degradation-plan](control-loop-degradation-plan.md) | plan: control375 국소 실패의 제어 정지(Pod-only 검증, 실패 owner 예약, 잔여 시간 무효, 실패 action), Tick 반응 시간, Completed Pod 정리 |
| [control-apply-latency-plan](control-apply-latency-plan.md) | plan: control376 검증 route의 rebase 왜곡, 범위 밖 실행 중 batch, refresh 분해 |
| [system-reaction-and-validity-plan](system-reaction-and-validity-plan.md) | plan: control377 공통 원인(genesis1 DiskPressure: 서버 퇴거 gap·12k Failed Pod·edge 종료 정지), OnCluster 전체 재목록 회귀, 직렬 release, action 기록 투영, 시작 상태 |
| [control-reaction-plan](control-reaction-plan.md) | plan: control378 수요 증가 직후 실패의 불필요 비용 목록 — 과부하 중 기한 초과 요청 실행(executor `keep`), 매 round 측정 CR 6,567개 전체 List(Admit 1.9초), 평가에 직렬화된 진행, 요청 window 재읽기, 평가 계산, 판단 기록 1.2MB/행 |
| [overload-admission-plan](overload-admission-plan.md) | plan: 완료 가능성 기반 admission(gateway 예측·executor 재확인, Workload별 Reject·Delay·maxQueuedRequests), SLO 기한과 최종 기한 분리, 기한 내 성공률·goodput·결과 분류 지표 |
| [decision-fidelity-plan](decision-fidelity-plan.md) | plan: control379 남은 실패·비용의 공통 원인 — gateway 실행기 상태가 관측으로만 갱신(pre-IPC running 하나로 10초 무배정), 후보 간 초기 물리 상태 불일치(실패한 prepare 하나로 keep 평가 불가, route 139회), profile 대표성 미검증(예측 p95 213ms vs 실제 828ms), 판단 계산 회귀(Tick 191/282가 10초 초과), 원인 미기록 |
| [energy-objective-plan](energy-objective-plan.md) | plan: control380 정상 상태에서 에너지 최적화가 작동하지 않는 원인 — 탐색이 정밀 평가 충족·저에너지 후보를 넘기지 못함(keep 충족 중 prepare 0/301, fluid 근사의 전환 구간 지연·출발점 기아), 다른 모드·조건 측정을 옮긴 에너지 항(genesis2 예측 62W vs 실측 131W)과 전환 비용 셀, 예측-실측 대조 부재, 판정 경계; 전환 후보 정밀 평가로 늘어난 판단 비용(D1) |
| [profile-serving-equivalence-plan](profile-serving-equivalence-plan.md) | plan: profile과 serving의 능력 차이(genesis2 IC CUDA 1.8~3배, 이미지 14/14 불일치, 유휴 118W vs 69W) — 측정이 serving 운영점(유휴 간격·실행 중 유입)을 재현하지 않음, 이미지가 적용 조건에서 빠짐, 대조 부재; serving 경로·다른 노드 부하·운영점 단계 측정, 기존 profile 삭제·재측정, 측정을 막는 reset 예약 반환 결함 |
| [capacity-table-evidence](capacity-table-evidence.md) | completed: 당시 전체 격자 κ·탐색의 역사적 근거; 현행 수치 계약은 승인 M0 |
| [scheduler-operations](scheduler-operations.md) | active: Pod-bound Service/수동원자실행 |
| [scheduler-srs-audit](scheduler-srs-audit.md) | completed: 당시 SRS대조 결과·증거 경계 |
| [online-switching-references](online-switching-references.md) | active reference: 미채택 문헌과 적용한계 |
| [scheduler-execution-history](scheduler-execution-history.md) | completed: 배포/checkpoint 결과요약 |

- 요구사항 권위: [spec](../../spec/README.md), [scheduler SRS](../../spec/scheduler/SRS.md), [Workload SRS](../../spec/workloads/SRS.md). 설계는 명세를 대체하지 않는다.
- 현재 수용/미완료는 [실클러스터 검증 현황](../../plans/2026-10-13-scheduler-live-evidence-campaign.md), 원시입력 hardware미검증은 [이기종 설계](heterogeneous-workloads.md)를 따른다. 전체 FULL 통과를 주장하지 않는다.
- gateway split: removed @e8344b20; T5 live만 유지한다([ADR0029](../../adr/0029-gateway-control-data-plane-split.md)). 아키텍처 B1–B3 전체안의 승인대기는 [대안 요약](../architecture-rethink/README.md)과 구분한다.
