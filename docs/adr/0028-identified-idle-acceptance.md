# 0028 Identified Idle Acceptance for Dispatch Occupancy

## Status

Accepted on 2026-10-26. This refines d acceptance in the
[measurement specification](../spec/00_measurement.md) and
[measurement API contract](../design/serving/measurement-api.md), following the
[decision log](../intent/decision-log.md).
ADR acceptance is a policy decision, not newly Accepted measurement evidence.

## Context

The previous 1% idle gate rejected measurements even when physical evidence
identified the idle contribution. Measured dispatch occupancy is the signed raw
quantity `d = native batch-start span / (physical cycles × B) − profileMean / B`.
Identified supply idle increases this value: leaving it in d lowers inferred
capacity κ rather than making capacity optimistic. The relative κ error due to
this identified contribution is bounded by the idle fraction. This is not a
bound on profile uncertainty, model mismatch or all SLO prediction error.

## Decision

Require complete physical batch continuity and execution evidence before
allowing nonzero identified idle. Unknown evidence, holes, missing native
records and incomplete execution remain insufficient. A partial sum over known
pairs when `idleIdentified=false` cannot establish acceptance.

Compare identified idle fraction with the frozen `maximumIdleFraction` setting.
Its approved value is `0.05`: one fifth of the 25% planning margin represented
by the existing factor 1.25. Validate this setting over the inclusive range
`[0,1]`, not by equality with 0.05. The frozen policy coefficient
`maximumIdleBaseFraction` has approved value 0.05; profile validation respects
that configured upper bound rather than deriving a tighter idle fraction from
profile timing. The independent **1 ms/request** idle limit remains unchanged.
Both idle limits, minimum physical batches and all existing evidence gates must
pass; report every applicable insufficiency reason.

Keep signed measured d unchanged, including negative values. Do not subtract
idle or substitute the lower endpoint into planner consumption. Only when idle
is identified, record:

- `dRange = [perRequestNanos − idleNanosPerRequest, perRequestNanos]`, in nanoseconds;
- `kappaErrorBound = idleFraction`, a dimensionless relative capacity-error bound.

Omit both fields when idle is unidentified. These fields describe the idle-only
uncertainty and are not a 95% confidence interval. Existing signed-raw storage
and nonnegative planner consumption remain unchanged.

## Consequences

Exceeding the configured fraction or 1 ms/request still reports a producer or
request-path limitation. Do not tune K, lifetime, margins or caps, extend the
window or automatically rerun to obtain acceptance. Supply sizing, finite drain,
planning precision and time budgets remain unchanged.

Existing immutable records, frozen inputs and Accepted evidence remain intact.
Offline raw rejudgment of Insufficient D05/D11/D15 is a report, not a mutation or
a new Accepted record. This decision authorizes no live deployment, measurement,
or change to another ongoing campaign.
