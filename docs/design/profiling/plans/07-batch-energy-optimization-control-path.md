# Plan 7: Batch Energy Optimization Control Path

## 현재 계약 — 자동 batch discovery

- `BatchEnergyOptimization`은 exact model/device/path/power/protocol을 동결한다. 후보는 선택된 firstfloor의 실제 `MaxBatchSize` 이하인 `1, 2, 4, …`이며, finite artifact declaration이 있으면 교집합만 남긴다. batch 1은 필수다. public `batchSizes`는 제거하고 기존 `retryLimit`(0–10)은 유지한다. 동결한 `PlannedBatchSizes`가 실행 도메인이다.
- 기존 trial마다 독립 `ProfilingRun`과 Kubernetes Job을 유지한다. 같은 actor의 loaded-idle baseline 3개를 먼저 완료하고, 각 K를 오름차순으로 active trial 3개씩 완료한다. active trial은 기존 최소 elapsed와 known-G sample floor를 모두 충족해야 한다. startup은 serving/idle/active 공통 `[1]` recipe만 실행한다. 높은 K의 첫 cold execution은 해당 측정에 포함하며 별도 current-K warmup으로 숨기지 않는다.
- 첫 expected runtime-execution OOM 또는 기존 execution measurement budget 소진에서 재시도 없이 discovery를 끝낸다. OOM은 실제 runtime execution stage에서 발생하고 명시적 infrastructure disruption이 없어야 한다. typed stage로 shared-worker 준비/observer/evidence commit을 구분하며 exit 137만으로 OOM을 판정하지 않는다. budget 소진은 해당 실행 조건의 한계이며 영구 unsupported 판정이 아니다.
- 알려진 Node/Pod eviction과 transient infrastructure failure만 기존 `retryLimit` 내에서 같은 K를 재시도한다. 한도를 소진하면 `Failed`다. generic runtime crash/model error, integrity failure, 원인을 분류할 수 없는 실패는 fatal이며 `Complete`로 처리하지 않는다.
- 완료한 idle 3개와 K별 active 3개의 연속 prefix만 정상 discovery 결과의 energy/profile/capacity 도출에 사용한다. 중단 K의 이미 완료한 1~2개 trial과 실패 진단은 보존하되 prefix에는 넣지 않는다. 예상 밖 실패에서도 이전 trial raw는 보존한다. `Failed` campaign은 completed-prefix profile을 발행하지 않는다.
- terminal 결과는 `Complete`와 outcome `BatchDiscoveryComplete`다. detail `DiscoveryResult`는 `MeasuredBatchSizes`, `Stop`, 선택적 `Energy`를 기록한다(JSON: `measuredBatchSizes`, `stop`, `energy`). `Stop` reason은 `DomainExhausted`, `OOM`, `MeasurementBudgetExceeded`다. 실행 경계에는 `FailedMeasurement`의 정확한 K/mode, `Attempt`, Run reference와 diagnostic을 남긴다.
- `Complete`는 discovery 종료를 뜻한다. 모든 K의 성공이나 usable profile을 보장하지 않는다. batch 1 이전/도중 경계라면 prefix는 빈 배열이고 `Energy`는 없으며 usable profile도 없다. 일부 prefix만 완료했으면 그 prefix의 energy를 도출한다. `DomainExhausted`는 동결 도메인 전체 완료에만 쓴다.
- energy는 완료 prefix의 measured mean `incrementalWorkJoulesPerItem` 최소값이다. local minimum, non-improving neighbor, 느린 p95 또는 SLO-impossible batch 1은 조기 종료 조건이 아니다. SLO는 sample floor를 정하며 unmeasured K의 최적성·통계적 saturation·positive capacity를 보장하지 않는다.
- controller는 immutable spec/owned Runs와 durable Node power confirmation으로 상태를 재구성하고 terminal result/reference를 발행한다. 기존 trial identity, actor/evidence 검증과 [Node label/generation protocol](../../platform/node-agent.md#3-노드-전력모드-node-label과-generation)은 유지한다. `ProfilingRun`은 독립 제출 가능하다.
- 승인된 source 배포 후 새 campaign identity로 전환한다. 기존 campaign의 trial state, raw/finalized evidence, stored plan/result와 과거 측정 수치는 수정하지 않는다. 과거 `LocalMinimum`/full-sweep 결과는 당시 정책의 기록이며 현재 stop 계약이 아니다. 이 문서는 integration gate나 live 검증 완료를 주장하지 않는다.

[campaign](../../../../internal/profiling/campaign) · [ADR 0016](../../../adr/0016-declared-batch-size-artifact-capability.md) · [Profiling index](../README.md)
