# Declared-Batch-Size Artifact Capability

## Status

Accepted

## Context

Batch discovery measures one immutable artifact and execution path. The selected firstfloor maximum and any finite artifact capability bound the candidate domain.

## Decision

RKNN artifacts declare `ModelArtifact.input.tensors`: unique runtime names, supported Triton datatypes and positive sample shapes without the batch axis. Other formats obtain model inputs through [Raw-Sample Preprocessing Boundary](0021-raw-sample-preprocessing-boundary.md). Missing or unsupported inputs fail explicitly.

Derived artifacts declare their conversion-fixed batch set in `ModelArtifact.derivation.declaredBatchSizes`. Canonical artifacts have no derivation or finite artifact batch set.

BatchEnergyOptimization selects powers of two up to the resolved firstfloor `MaxBatchSize`, intersected with finite artifact declarations when present. Batch one is required. The immutable curve plan freezes the ordered domain, input contract and conversion source. Current mutable facts do not reproject an existing plan.

Use one three-trial batch-one model-loaded-idle baseline, followed by three active trials at each batch in ascending order. Each trial owns its ProfilingRun and Job. Active trials satisfy their elapsed-time and SLO-derived sample floors.

Serving, active and idle share one actor startup recipe, warming only `[1]`. Higher-batch cold execution occurs in the measurement window. Workers validate runtime metadata against frozen delivery inputs.

Domain exhaustion, expected execution OOM and the measurement-budget boundary stop discovery without retry. OOM requires actual runtime execution without an explicit infrastructure disruption; exit 137 alone is insufficient. Preserve the contiguous prefix whose active trials and common idle baseline are complete.

Known transient Node/Pod infrastructure failures use `retryLimit` in the range 0–10 at the same batch. Exhausted infrastructure retries, generic crashes, model errors, invalid evidence and unknown failures produce `Failed`, retaining completed raw evidence without publishing a completed-prefix profile.

`DiscoveryResult` records measured batches, the stop and optional energy. Stops are `OOM`, `MeasurementBudgetExceeded` or `DomainExhausted`. A boundary records the failed batch, mode, attempt, Run and diagnostic. Partial trials at the boundary remain raw evidence without contributing a profile. A batch-one boundary leaves no usable prefix.

## Consequences

`Complete` reports discovery completion; consumers still check whether a usable profile exists. Budget exhaustion describes the frozen measurement budget.

Discovery continues through a local energy minimum and an SLO-infeasible batch one. Existing plans, raw trials and finalized results remain immutable under [Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md).

