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
