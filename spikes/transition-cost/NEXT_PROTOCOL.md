# Next transition protocol checkpoint

## Decision

The repeated cold/warm family will attach an external request path. The v1
pair's `servingDuring=none` remains a diagnostic family and is not pooled with
future repetitions. This decision precedes variance and confidence-interval
work because changing `servingDuring` changes the protocol identity.

The exact endpoint, offered rate, request trace schema, latency SLO, and load
generator implementation remain execution inputs that must be frozen before a
v2 intent exists. Until they are fixed, no repeated pair is authorized and no
v2 intent is labeled frozen.

## Required baseline sequence

Each next pair uses the following order on the same bound Node and power mode:

1. Measure `nodeStandby` for 30 seconds with no experiment model-service Pod.
2. Start the source runtime and realize the exact source model, runtime,
   provider, artifact, and engine state.
3. After source Ready, keep the internal inference loop stopped for a positive
   settling duration frozen before execution.
4. Measure source-state `modelLoadedIdle` for 30 seconds with the session
   resident and no inference, input generation, or output validation.
5. Start the frozen external offered load, establish the source-serving
   baseline, and retain request timestamps, outcomes, and latency.
6. Execute the cold or warm transition while maintaining that external load.

Steps 3 and 4 use
[Plan 03's `IdleModelLoadedWorkloadMode`](../../docs/design/profiling/plans/03-profile-admission-derivation.md)
meaning. Power values must not choose either boundary. `nodeStandby` yields
$P^{standby}_t$ for the bound device/power mode; `modelLoadedIdle` yields
$P^{idle}_{s_{from}}$ only for the exact bound source state. Neither name
aliases the other.

The v1 pair already contains two valid 30-second `nodeStandby` observations,
10.217 W before cold and 10.226 W before warm. It contains no
`modelLoadedIdle` window. Its reported node-standby-referenced energy deltas
therefore remain sensitivity results, not model-loaded incremental-energy
estimates.

## Availability outputs

The external path must retain enough information to report separately:

- measured service-capacity gap and recovery timing;
- offered, completed, failed, and timed-out request counts;
- latency distribution and the frozen SLO violation count; and
- the demand/opportunity evidence needed to make `E_forgone` identified or to
  leave it explicitly unavailable.

Seconds, request counts, latency, and joules remain separate outputs.
