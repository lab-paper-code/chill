# Plan 3: Profile Measurement Derivation

## Status

Implemented. Scientific admission is removed. CHILL records experiments and
calculates measurements; it does not decide whether an experiment is suitable
for a paper or a later analysis.

## Execution and calculation

A ProfilingRun completes when its execution evidence is stored. There is no
admission policy, admission status, or subsequent admission reconciliation.
Job failures, invalid boundary inputs, and storage errors remain errors.

`internal/profiling/profiler/projection` projects verified raw evidence into
active and idle measurements. `internal/profiling/derivation` computes:

- wall energy over the runtime measurement window;
- model-loaded idle mean power;
- incremental energy per completed item, subtracting the same-state idle baseline;
- repeated-trial summaries, confidence intervals, and batch comparisons.

The full declared window is used. Power is interpolated linearly between
samples; the first and last sample values extend to the window boundaries.
No minimum sample-count policy, coverage threshold, maximum gap threshold,
thermal verdict, or workload-contamination verdict selects or excludes trials.
At least two distinct power timestamps are needed to integrate. Invalid
numbers, invalid windows, missing denominators, mismatched execution states,
and duplicate trials remain calculation or structural errors. No value is
fabricated when a calculation cannot be performed. Negative incremental
estimates remain measurements rather than becoming rejection reasons.

Raw metric readings, read failures, thermal observations, and workload events
remain stored for inspection. Missing or adverse environmental observations do
not block a successful Run or its numeric derivation. Scientific interpretation
and exclusions belong to the experimenter.

Curve assembly binds every Loader-verified active and idle execution to the
frozen plan's Node, artifact, SDK image, backend, input, power, CPU allocation,
affinity, and mode-specific protocol facts before numeric derivation. Repeated
standalone idle trials bind to their first frozen execution condition.
State/protocol digests are retained provenance labels, not validation gates.
External-environment identities and changes remain per-trial observations;
different snapshots do not exclude trials or select a different baseline.
Aggregate environment labels describe the first retained trial, not a claim
that all environments were identical. Raw memory, duration, latency, power,
and energy remain measured quantities, not execution identity.

## Consumers and stored contracts

Idle-baseline aggregation and batch-energy search consume completed Runs with
stored evidence directly. They do not reload or repeat an admission policy.
`CompletedRuns` and `CompletedTrials` count completed measurements, not approved
experiments. The batch search still compares measured incremental energy.

Batch results use `batch-energy-optimization-result.v1alpha4`; idle-baseline
results use `model-loaded-idle-baseline-result.v1alpha2`. The old result readers
are not retained. Raw execution-evidence format is unchanged. These local
changes do not migrate or delete testbed objects and must not be deployed over
an active campaign; retire old requests and results before starting new ones.

## Serving energy below measurement resolution

Derivation and stored raw/derived measurement results retain signed incremental
energy; the batch search does not clamp or discard negative estimates. At the
serving catalog boundary only, a negative batch mean is evaluated with the
existing derivation `Aggregate` two-sided **95%** Student-t
Welch–Satterthwaite interval. It combines active trial variance of the mean with
the shared idle-baseline mean-power variance scaled by mean window seconds/item;
the shared baseline uncertainty is not divided by the active trial count.
This resolution interval uses 95% independently of the configured serving
confidence level used by other consumers.

If the interval upper endpoint is at least zero, serving publishes
`energyPerInputJoules: 0` and `energyBelowResolution: true`. This is an explicit
below-resolution representation, not a claim that measured energy was exactly
zero. The marker is retained in profile records, API responses, and the native
startup bundle (`energyPerItemJ: 0`). Positive and exact-zero estimates remain
unchanged with the marker false and omitted from JSON, preserving existing
immutable bundle bytes. If the upper endpoint is negative, the whole
profile is unavailable with `NegativeIncrementalEnergy`, including batch, signed
estimate and interval in J/item; independent remeasurement is required rather
than retrying until a positive estimate appears. Native admission and scheduler
replay continue to reject negative energy. Raw evidence and its existing schemas
are unchanged.

