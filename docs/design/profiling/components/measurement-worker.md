# Measurement Worker Contract

[Component contracts](README.md) and [Profiling design index](../README.md)

The worker executes the model-loaded idle measurement, coordinates observers and
commits immutable raw evidence. The idle baseline controller creates the
ProfilingRun that runs it; the power-mode transition cost reads its result.
Batch profiles are measured by the session in
[profile-session.md](../../serving/profile-session.md), which does not use this
worker. Consumers own numeric derivation and publication policy.

## Roles and validation ownership

| Role | Contract |
|---|---|
| Artifact/preprocessor init | Copy declared supplies into execution mounts |
| Runtime worker | Validate executable inputs, load the model, coordinate the window, compose raw, commit and return receipt |
| MetricObserver | Observe start through final real power read, atomically publish result and exit |
| WorkloadObserver | Retain target-Node LIST/WATCH through causal metric-container exit fence |
| Profiler | Create/observe resources, validate raw once and bind external execution facts |

Each process validates its delivered input at its execution boundary. Runtime
carries `profilingrun.VerifiedRunConfig`; result adapters return validated
process results. The coordinator checks new source/target/window relationships.
Finalization validates raw once. Retained readers only bound bytes and decode;
they do not revalidate mutable specs/status or duplicated provenance.

Runtime image contains the worker and persistent actor/runner. Artifact supply
is a separate image. Metric/workload observers have separate images; only
WorkloadObserver receives Kubernetes credentials. No uploader container is used.
Tags and digest references are accepted without a digest-pin admission gate.

## RunConfig and delivery inputs

RunConfig v1alpha22 is executable authority, delivered as canonical JSON through
`--run-config`. There is no auxiliary per-Run ConfigMap or compatibility file.
Candidate compatibility is resolved before creation. Selected policy payloads
remain Job provenance; only runtime delivery enters RunConfig.

Authenticated recorded-Job recovery consumes its immutable executable record.
It does not re-admit mutable copies. ResourceVersion remains provenance.

Loaded-idle uses `profiling/run.IdleExecutionBatch`: ORT uses batch one;
RKNN retains its fixed artifact batch. The idle Run has no dataset, workload,
control listener or load Job.

The artifact declaration fixes source paths. Adapters own runtime destinations
(`model.onnx` or `model.rknn`). Verified RKNN delivery projects exact host
library, driver, platform and device paths. Files are read-only; device mounts
are writable and runtime-only. Extended resources require a declaration.
Supplemental groups are Pod-level; observers receive no device mounts/privilege.

Actual backend/provider/device, tensor metadata and initialized batches remain
checked. Package/driver versions are observed provenance; declaration-version
differences do not reject successful real execution.

CPU has no request, limit, pinning, affinity or thread-tuning override.
Actual quota/cpuset/affinity/usage/throttling and runtime-default threads remain
observed. Memory and accelerator resources are frozen actor delivery.

Storage delivery includes logical store, internal endpoint/bucket/region,
immutable Secret reference and deterministic raw key. Job creation requires an
enabled store with current availability and completed bootstrap attesting the
configuration revision. Workers receive delivery without Kubernetes discovery.
Finalization recovers the original Job binding.

Endpoint, bucket, region and credentials stay outside public spec/status/logs.
Compact evidence references contain logical store and opaque key. Keys are
`profiling-runs/<Run UID>/raw.json` and `profiling-runs/<Run UID>/execution.json`.

## Window and failure budget

Loaded-idle requires complete startup/settling, empty queue, declared monotonic
duration, zero inference and resident model. Runtime freezes memory/CPU
observations and measurement-end before reading any large body.

The original context covers preparation, startup, window, observer fences,
handoffs and evidence commit. Both observers use the full producer failure
bound; capacity admits its complete schedule before execution. Ending runtime
does not restart a deadline. Runtime failure signals both helpers immediately.

OOMKilled or typed ORT BFCArena `MemoryAllocationFailed` during runtime
Execution can establish a memory boundary; exit 137 alone cannot.
Preparation/observer/commit failures remain separate. Infrastructure disruption
takes precedence.

## Job-local handoff

Artifact, preprocessor, coordination and private runtime-workspace volumes are
separate. Runtime workspace holds private results and a bounded evidence
candidate. Accelerator paths come only from verified delivery. The projected
token/CA is mounted only in WorkloadObserver.

| Atomic path | Owner/meaning |
|---|---|
| `observer-ready` | Metric helper ready |
| `workload-observer-ready` | Workload capability ready |
| `workload-window-start`, `workload-window-ready` | Open/acknowledge retained membership |
| `measurement-start`, `measurement-end` | Exact runtime window |
| `metric-result.json`, `workload-result.json` | Bounded temp-file/rename results |
| `runtime-failed` | Failure cancellation signal |

Runtime emits signals without reading observer values. WorkloadObserver drains
the same watch through successful metric-container termination. Logs are
diagnostic.

Readiness and idle HTTP exchanges release connection ownership before a long
measurement/request-free interval; subsequent metadata/counter exchanges open
a new connection. A readiness timeout must not leak onto the idle interval.

## Idle raw field contract

Idle runtime is v1alpha7 and idle raw v1alpha8. Raw contains `schemaVersion`,
verified `runConfig`, actual `podUID`, `runtime`, `metrics` and `workload`.
Finalization adds execution facts separately.

Derivation integrates measurement-node metrics into mean watts over the window.
Runtime image/version remain performance identity. Initialized domains,
collection policy, observers and meter identity retain provenance and are
normalized separately by `PerformanceCondition`. CPU observations do not become
allocation identity.

## Commit, resources and lifecycle

Success requires immutable-create outcome and an
`evidence-store-receipt.v1alpha1` termination message within 4 KiB. Existing keys
retain their winner. Finalization independently reads and validates raw once.

| Exit | Meaning |
|---:|---|
| 2 | Invalid/drifted executable input or delivery |
| 3 | Artifact/runtime/provider/inference/output failure |
| 4 | Observer/readiness/handoff validation failure |
| 5 | Encoding, byte bound, store or receipt failure |

Versioned execution stage/typed failure precedes a successful receipt in
termination-log. Native Job reasons remain evidence; unstructured logs cannot
prove OOM or execution stage.

The Run owns one deterministic measurement Job. It uses backoff zero, restart
Never and a bounded deadline. Losing a recorded Job does not authorize
re-execution. Cleanup handles completion, failure and ExecutionLost.

Policy coherently freezes memory/resources, deadlines, observer capacity,
handoff bounds and workspace. Input tensors are checked before allocation.

Execution evidence v1alpha18 contains Job/Pod/Node and actuation facts without
terminal-container arrays or Node workload snapshots. Job status determines
execution completion. Finalization does not replay raw workload
self-consistency. Contradictions are `EvidenceInvalid`; API/store failures retry
finalization against the recorded Job.

Contract replacement follows
[Durable Stored-Contract Compatibility](../../../adr/0019-durable-stored-contract-compatibility.md).
Old working objects are replaced deliberately. There is no dual reader or
rewrite of archived scientific raw.
