# Runtime, Artifact, and Profiling

Profiling records immutable execution evidence and derives numeric profiles.
Finalization validates raw evidence once; retained readers bound bytes and decode.
Raw RunConfig is execution authority. Thermal and workload observations remain
facts without automatic scientific accept/reject.

## Contracts

| Contract | Owner |
|---|---|
| [Measurement Worker](components/measurement-worker.md) | Native execution, off-node load, observers, raw commit, and receipt |
| [Evidence Store](components/evidence-store.md) | Immutable storage, finalization, retained reads, and operand lifecycle |
| [MetricObserver](components/metric-observer.md) | Exact-source power and auxiliary thermal observations |
| [WorkloadObserver](components/workload-observer.md) | Target-Node membership and continuous-window evidence |
| [Runtime boundary](foundations/runtime-execution/README.md) | Static compatibility, actual execution, and provenance |
| [Experiment execution](experiment-execution.md) | EvaluationRun/Stage controllers and external evaluator Jobs |

## Execution and campaign boundary

Current contracts: RunConfig v1alpha22, CurvePlan v1alpha14, active runtime/raw
v1alpha14, idle runtime v1alpha7, idle raw v1alpha8, execution evidence v1alpha18.
Execution evidence binds Job, Pod, Node, actuation and finalization facts.
It contains no container-terminal arrays or Node workload snapshot. Raw load
counts prove load success; Job status determines execution completion.
Immutable contradictions are `EvidenceInvalid`; API/store failures are retryable.

An active Run owns a Pod-network measurement Job and a separate load Job on the
frozen gateway Node. Sparse observations size saturation supply; saturation
throughput determines Poisson utilization rates. Scheduled arrivals define
client latency and offered rate. The same actor and observers remain alive
through the frozen request-free post-load window.

Batch discovery freezes powers of two through actual runtime capability,
intersected with artifact declarations; batch one is required. Startup warms
batch one. Three completed loaded-idle trials precede three active trials per
batch. Active phases meet elapsed-time and known-gap sample floors.
Domain exhaustion, proven runtime memory exhaustion, or execution measurement
budget ends discovery with its completed prefix. Generic runtime, model,
integrity and unknown failures are fatal. Only known infrastructure disruptions
use bounded same-batch retries. Energy minima and SLO impossibility do not stop
discovery.

Campaign identity is explicit in `spec.campaignTrial`; labels filter candidates
and controller owner UID establishes ownership. Recorded plans and already-owned
Jobs freeze execution authority. Recovery does not re-admit mutable Workload/SLO
copies or reconstruct trial identity from names.

The preparer freezes actor memory before recording a plan or RunConfig:
the lesser of configured limit and Node allocatable memory times measurement
fraction. Defaults are 0.8, a 16 GiB ceiling and 128 MiB request. Measurement and
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
