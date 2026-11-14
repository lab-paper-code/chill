# Runtime, Artifact, and Profiling

Batch profiles are measured by the session that ChillServer runs per
`BatchEnergyOptimization`, described in
[profile-session.md](../serving/profile-session.md). The profiling components
here run the model-loaded idle baseline measurement, a ProfilingRun that the
power-mode transition cost reads, and store evaluation evidence.
Finalization validates raw evidence once; retained readers bound bytes and
decode. Raw RunConfig is execution authority. Thermal and workload observations
remain facts without automatic scientific accept/reject.

## Contracts

| Contract | Owner |
|---|---|
| [Measurement Worker](components/measurement-worker.md) | Native model-loaded idle execution, observers, raw commit, and receipt |
| [Evidence Store](components/evidence-store.md) | Immutable storage, finalization, retained reads, and operand lifecycle |
| [MetricObserver](components/metric-observer.md) | Exact-source power and auxiliary thermal observations |
| [WorkloadObserver](components/workload-observer.md) | Target-Node membership and continuous-window evidence |
| [Runtime boundary](foundations/runtime-execution/README.md) | Static compatibility, actual execution, and provenance |
| [Experiment execution](experiment-execution.md) | EvaluationRun/Stage controllers and external evaluator Jobs |
| [Profile session](../serving/profile-session.md) | Batch profile measurement run by ChillServer |

## Idle baseline execution

Current contracts: RunConfig v1alpha22, idle runtime v1alpha7, idle raw
v1alpha8, execution evidence v1alpha18. Execution evidence binds Job, Pod, Node,
actuation and finalization facts. It contains no container-terminal arrays or
Node workload snapshot. Job status determines execution completion.
Immutable contradictions are `EvidenceInvalid`; API/store failures are retryable.

A model-loaded idle Run owns one measurement Job on the target Node. The model
is resident, the queue is empty and no request arrives for the declared
duration; the observers stay alive through that window. The idle baseline
controller creates the Run and derives the baseline watts; the power-mode
transition cost reads it.

The preparer freezes actor memory before recording a RunConfig: the lesser of
configured limit and Node allocatable memory times measurement fraction.
Defaults are 0.8, a 16 GiB ceiling and 128 MiB request. Measurement and
serving reuse `firstfloor.RuntimeContainer` with the frozen actor. CPU has no
request, limit, pinning or thread override; actual cgroup, affinity, usage and
throttling remain observations.

## Dependency rule

DeviceClass owns stable hardware declarations; ModelSpec owns artifact/path
requirements; Kubernetes owns placement and lifecycle. CHILL owns frozen
decisions, evidence and derivation. `modelcompat` and the `evidencestore` root
have no Kubernetes imports. Derivation has no direct Kubernetes imports;
firstfloor carries Kubernetes value projections. `profiling/run` contains
Kubernetes projections and a Node grant reader. Adapters depend inward.

Stored-contract cutovers follow
[Durable Stored-Contract Compatibility](../../adr/0019-durable-stored-contract-compatibility.md).
Affected working objects are replaced deliberately; archived scientific raw
and publications are not rewritten. Execution cleanup and failed-Job retention
do not garbage-collect evidence.
