# Profiling component contracts

[Profiling design index](../README.md)

Batch profiles are measured by the session in
[profile-session.md](../../serving/profile-session.md). The components below run
the model-loaded idle baseline measurement (a ProfilingRun) that the power-mode
transition cost reads, and store evaluation evidence.

| Contract | Status and ownership |
|---|---|
| [Evidence Store](evidence-store.md) | active — immutable bounded storage; Operator-managed operand, finalize-only validation and decode-only execution readers |
| [Measurement Worker](measurement-worker.md) | active — one-shot model-loaded idle runtime/observer coordination, raw commit and bounded receipt |
| [WorkloadObserver](workload-observer.md) | active — read-only continuous target-Node Pod membership evidence |
| [MetricObserver](metric-observer.md) | active — bounded exact-source power and typed thermal facts |
