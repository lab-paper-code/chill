# Plan 7: Batch Energy Optimization Control Path

## 결과 요약 — completed
- 결정: `BatchEnergyOptimization`은 exact model/device/path/power/protocol과 ordered batch set을 동결하고 measured mean `incrementalWorkJoulesPerItem`을 최소화한다. method selector·`minDelta`·`patience`·divergence threshold는 없다.
- Quick search는 same-state loaded-idle baseline과 첫 batch를 측정한 뒤 가장 최근 batch가 이전 모든 batch보다 strictly lower mean이면 다음 batch로 간다. 아무리 작은 감소도 개선이다. 첫 non-improving neighbor는 `MinimumFound/LocalMinimum`, 모든 batch 개선 후 소진은 `MinimumFound/SearchSpaceExhausted`다.
- 각 active batch와 idle baseline은 정확히 `trialsPerBatch` successful/completed trials를 요구한다. count 초과는 다른 batch/idle이 미완료여도 추가 scheduling/derivation 전에 실패하며 failed attempts는 count에 들지 않는다. scientific accept/reject는 [Plan 3](03-profile-admission-derivation.md)에서 제거됐다.
- policy 불일치는 terminal `Failed/EnergyObjectivePolicyMismatch`, `MeasurementComplete=False`/같은 reason과 diagnostic message를 발행한다. status/condition은 current generation을 기록하고 terminal replay는 no-op이다. 원 Runs/evidence를 보존하고 result reference를 만들거나 초과 trial을 버리지 않는다. Dashboard는 새 이름의 측정을 안내한다.
- immutable result: `objective=min-incremental-work-joules-per-item`, `bestBatch`, `minimumIncrementalWorkJoulesPerItem`, `measuredBatchSizes`, `incrementalWorkByBatch`, `completionReason`, exact-state idle baseline/measurement identity. 최솟값은 local search 결과이지 unmeasured batch의 최적성·통계적 `b_sat`·SLO 결정이 아니다.
- controller는 immutable spec/owned Runs와 durable Node power confirmation으로 진행을 재구성하고 completed evidence를 읽어 한 terminal record/reference를 발행한다. `ProfilingRun`은 독립 제출 가능하다. 과거 owned `NodeActuation`은 removed @`f4b0b685`; 현재 [Node label/generation protocol](../../platform/node-agent.md#3-노드-전력모드-node-label과-generation)을 따른다.
- 근거 커밋: `dd520844` energy-objective cutover, `1f4a60fb` terminal mismatch, `30bafe78` count 초과 scheduling 방지. [campaign](../../../../internal/profiling/campaign) · [ADR 0016](../../../adr/0016-declared-batch-size-artifact-capability.md).
- 남은 것: unmeasured batches의 global optimum·scientific saturation 보장·serving/scheduler behavior는 이 계획 밖이다.
- [Profiling index](../README.md)
