# Plan 7: Batch Energy Optimization Control Path

> Status: Implemented. The former `BatchSaturation` and
> `sequential-early-stop` contracts are removed.

## Question

> Can one object find the batch with the lowest incremental wall energy per
> completed item without a human step between measurements?

## Contract

`BatchEnergyOptimization` freezes one exact model, device, execution path,
power mode, protocol, and ordered batch search space. It owns the optional
`NodeActuation` that establishes that state and every `ProfilingRun` used by the
search.

The objective is fixed by the implementation:

```text
minimize incrementalWorkJoulesPerItem
```

The user configures only `trialsPerBatch`; there is no method selector,
`minDelta`, `patience`, or divergence threshold.

## One-dimensional Quick search

1. Measure the exact-state model-loaded idle baseline.
2. Measure the first planned batch.
3. If the newest batch has strictly lower mean incremental energy per item than
   every previously measured batch, measure the next planned batch.
4. Otherwise finish with `MinimumFound/LocalMinimum` and report the best
   measured batch.
5. If every planned batch improved the objective, finish with
   `MinimumFound/SearchSpaceExhausted` and report the final batch.

An arbitrarily small reduction is an improvement. Early stopping no longer
defines a saturation point; it only closes a local search when the next
one-dimensional neighbor is not better. Search-space exhaustion is a normal
completion, not evidence of saturation.

Each measured active batch and the idle baseline must contain exactly
`trialsPerBatch` successful trials. Evidence inconsistent with the recorded
energy-objective policy terminates as `Failed/EnergyObjectivePolicyMismatch`,
with `MeasurementComplete=False`, the same condition reason, and a diagnostic
message. The current generation is recorded on status and condition; replay of
that terminal generation performs no further derivation or measurement.
The original Runs and evidence are retained, no successful result reference is
created, and trials are never silently discarded to satisfy the policy.
The scheduling method counts all successful observations before requesting
another trial or deriving a curve. Any measurement with more than
`trialsPerBatch` successes fails with the same policy-mismatch reason, even if
an earlier batch or the idle baseline is incomplete. Failed attempts do not
count toward this bound. Deterministic Run names and the existing
`AlreadyExists` reconciliation path are unchanged.
The Dashboard explains this failure as a successful-trial-count policy mismatch
and directs the operator to repeat measurement under a new name.

## Result

The immutable result detail records:

```text
objective = min-incremental-work-joules-per-item
bestBatch
minimumIncrementalWorkJoulesPerItem
measuredBatchSizes
incrementalWorkByBatch
completionReason
exact-state idle baseline and measurement identity
```

The result is the minimum found by this local search. It is not a statistical
`b_sat`, a proof about unmeasured batches, or an SLO-aware serving decision.

## Control boundary

Controller-runtime delivers direct `BatchEnergyOptimization` events and
framework-derived events from owned `NodeActuation` and `ProfilingRun` objects.
The reconciler reconstructs progress from immutable spec and owned children,
loads completed Run evidence, derives the objective result, writes one immutable
terminal record, and publishes only its reference in status.

`ProfilingRun` remains independently submittable. No serving or scheduler
behavior is added by this plan.
