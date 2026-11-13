# Identified Idle Acceptance for Retained Dispatch Occupancy Evidence

## Status

Accepted for retained `v1alpha1` occupancy records.

## Decision

Retain the signed `perRequestNanos`, original conditions, verdict and identified-idle diagnostics of accepted occupancy evidence. The stored acceptance rationale requires complete physical continuity and execution evidence; unknown rows, holes or unidentified idle cannot establish acceptance.

The retained policy uses the frozen `maximumIdleFraction` and independent 1 ms/request idle bound. Its approved idle-fraction value is 0.05 with a permitted range `[0,1]`. `maximumIdleBaseFraction` records the policy ceiling. Do not infer a tighter bound from profile timing or reinterpret retained verdicts.

When idle is identified, `dRange` records `[perRequestNanos - idleNanosPerRequest, perRequestNanos]` and `kappaErrorBound` records `idleFraction`. These express idle-only uncertainty. They are not confidence intervals or bounds on model/profile error. Unidentified idle has no such range or bound.

The retained reader selects the latest stored Accepted record per complete execution condition and consumes `max(perRequestNanos, 0)`. It preserves signed raw values without subtracting idle and does not rerun the original acceptance gate.

### Current factual measurements

New burst records use `v1alpha2` and the shared backlogged-pair estimator. Each eligible pair measures signed `(cycleNanos - serviceDurationNanos) / batchSize` using actual runner service. Its factual outcome is Observed or Insufficient; it does not replace retained Accepted evidence in scheduler admission.

## Consequences

Keep immutable records and frozen inputs unchanged. Offline rejudgment remains a report and cannot create a new Accepted record or silently modify planner inputs. Do not automatically tune supply, extend the window or rerun a measurement to manufacture acceptance.

Current measurement behavior is specified in the [measurement contract](../spec/00_measurement.md) and [measurement API design](../design/serving/measurement-api.md).

