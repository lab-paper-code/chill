# Measurement Worker Contract

[Component contracts](README.md) and [Profiling design index](../README.md)

The worker executes firstfloor active/model-loaded-idle measurement, coordinates
observers and commits immutable raw evidence. Controllers own orchestration;
consumers own numeric derivation and publication policy.

## Roles and validation ownership

| Role | Contract |
|---|---|
| Artifact/preprocessor init | Copy declared supplies into execution mounts |
| Runtime worker | Validate executable inputs, execute actor, coordinate window, compose raw, commit and return receipt |
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
It does not re-admit mutable Workload/SLO copies. Campaign children retain
frozen Workload identity, SLO/sample floor and explicit logical batch/attempt.
ResourceVersion remains provenance.

A standalone Run defaults to its selected warmup batch. Campaign actors share
the frozen startup recipe with warmup domain `[1]` and zero measurement warmup.
Higher batches initialize lazily; their first execution belongs to the measured
window. Declared capability does not prove that a future batch will execute.
Loaded-idle uses `profiling/run.IdleExecutionBatch`: ORT uses batch one;
RKNN retains its fixed artifact batch.

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
Different Runs remain distinct trials.

## Serving-path operating points

The frozen Workload name/UID/SLO and selected model/preprocessor/dataset define
execution inputs. `measurement.operatingPoints` contains:

- `utilizations`, `sparseMeanGapMilliseconds`, `phaseDurationSeconds`;
- `postLoadDurationSeconds`, `requestLifetimeSeconds`;
- `deliveryMarginMilliseconds`, `framedDecodedRequestUpperBytes`;
- computed `queueBytesCap`, selected from execution policy.

Phase/post-load durations are positive integer seconds. The phase default is
30 seconds without a separate hard-coded minimum. Each phase also needs
`ceil(log(.05)/log(percentile/100))` known-gap service samples: 59 at p95.
Percentile 100 is rejected. The sample maximum's coverage does not establish
independence, stationarity, precision or scheduler safety.

Each active trial restarts and uses serving's raw `/infer` decode, EDF queue,
fixed batching and IPC runner without a profile bundle. A separate load Job
freezes the actual ready gateway Pod identity and Node. That Node must differ
from the measurement Node. There is no public arbitrary load-node setting,
in-Pod active driver or `/profile` endpoint.

Measurement Pods use Pod networking: no hostNetwork or hostPort. The actor
listens at Pod-local TCP 8080; active control uses TCP 8081. Runtime declares
named `http` and `load-control` ports; readiness probes `load-control` on Pod IP.
The producer calls the actual measurement Pod IP and control port, and receives
the same Pod IP's native endpoint. Runtime reaches its actor on loopback 8080.
Idle has no control listener or off-node load Job. Serving instead uses each
Pod's ClusterIP Service. Recovery preserves recorded delivery shape.

Phases run sparse, saturation, then utilization ladder:

1. Sparse Poisson load retains no-backlog service observations.
2. Their largest service observation supplies saturation workers:
   `min(64, floor(queueBytesCap/framedDecodedRequestUpperBytes),
   floor((L-margin)/(B*maxObservedSparseServiceSeconds)))`.
   Native slots and whole-batch/queue feasibility also constrain supply.
3. Saturation holds measured workers outstanding and obtains completed-input
   throughput over monotonic elapsed time.
4. Each utilization schedules independent Poisson arrivals at utilization times
   saturation throughput.

Workers need not divide batch size. Issuance ends on a whole-batch boundary;
slots remain held through response completion. Missing sparse service or
infeasible supply fails explicitly without guessing or old-profile bootstrap.
`supplySizing.serviceTimeSource` is `run.sparse.maxObservedSeconds`.

The gateway-image `profiling-load` command reuses native encoding/pooled HTTP
transport. Clients FIFO-wait for frozen slots. Slot saturation does not move
scheduled arrivals or invalidate a phase. Every issued client must dispatch and
complete before successful drain. Queue hold consumes the existing request
elapsed/lifetime budget.

Scheduled arrivals define latency and offered rate:
`scheduledInputs / issueElapsedSecondsMonotonic`. Dispatch lag, target rate and
producer GC remain diagnostics. All phases meet elapsed and known-gap floors.
Only utilization phases enter incremental energy; all supply service samples.

## Window, post-load and failure budget

After final drain, the same actor and observers stay alive without requests for
`postLoadDurationSeconds`. Final phase-end snapshots counters and post-load
start clocks before result transfer. Transfer/decoding cannot reset or hide
stabilization. Runtime freezes memory/CPU observations and measurement-end before
reading the large uploaded result body.

`runtime.postLoad` retains start/end, monotonic duration, actor PID, residency
and before/after accepted/completed/execution counters. Equality proves no
requests/inference during the observed interval. Metrics retain original
power/thermal samples and failures. Post-load samples do not enter utilization
energy or service populations.

The frozen failure budget reserves sparse issuance for its sample floor plus
initial unknown-gap batch, every phase floor, per-phase request drain and the
post-load window. AttemptTimeout also covers settling and configured overhead;
Job deadline adds its own overhead. Poisson/sample-limited execution has no
finite successful-completion guarantee.

The original context covers preparation, warmup, window, observer fences,
handoffs and evidence commit. Both observers use the full producer failure
bound; capacity admits its complete schedule before execution. Ending runtime
does not restart a deadline. Runtime failure signals both helpers immediately.

Batch discovery ends with its completed prefix on domain exhaustion, proven
runtime memory exhaustion or execution measurement budget. OOMKilled or typed
ORT BFCArena `MemoryAllocationFailed` during runtime Execution can establish
the memory boundary; exit 137 alone cannot. Preparation/observer/commit failures
remain separate. Infrastructure disruption takes precedence. Known transient
failures alone use bounded same-batch retries; generic/model/integrity/unknown
failures are fatal. Energy minima or SLO-impossible batches do not stop discovery.

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

Loaded-idle requires complete startup/settling, empty queue, declared monotonic
duration, zero inference and resident model. Runtime emits signals without
reading observer values. WorkloadObserver drains the same watch through
successful metric-container termination. Logs are diagnostic.

Readiness and idle HTTP exchanges release connection ownership before a long
measurement/request-free interval; subsequent metadata/counter exchanges open
a new connection. A readiness timeout must not leak onto measured inference.

## Active raw v1alpha14 field contract

Active raw/runtime are v1alpha14; idle runtime is v1alpha7 and idle raw v1alpha8.
Raw contains `schemaVersion`, verified `runConfig`, actual `podUID`, `runtime`,
`metrics` and `workload`. Finalization adds execution facts separately.

| Phase fields | Meaning |
|---|---|
| `id`, `kind`, `utilization` | Unique sparse/saturation/utilization identity; utilization only where configured |
| `arrivalRatePerSecond`, `workers` | Poisson target rate or saturation workers |
| `measurementStartedAt`, `measurementEndedAt` | Measurement-node power fences |
| `measurementElapsedSecondsMonotonic` | Producer monotonic phase duration |
| `offeringStartedAt`, `offeringEndedAt`, `drainEndedAt` | Load-node offering/drain UTC |
| `issueElapsedSecondsMonotonic` | Through last scheduled arrival, excluding drain |
| `scheduledInputs`, `sentInputs`, `completedInputs` | Equal on successful drain |
| `achievedArrivalRatePerSecond` | Scheduled count over offering duration |
| `arrivalSeed`, `issueLagMs`, `driverGC` | Poisson seed/lag and producer GC diagnostics |
| `requests`, `nativeSamples`, `latencyMs` | Completed client records, physical batches and aligned maximum member latency |

Request identity includes request/record IDs, scheduled/dispatch/completed times,
latency and native response progress. Records are scheduled-order. Native
elapsed nests in dispatch-to-completion, which nests in client latency.
Client UTC belongs to the load Node; latency/lag use its monotonic clock.
Measurement-node fences remain separate. Cross-host subtraction never defines
latency; stored UTC deltas need not exactly equal monotonic durations.

Poisson seed identity is Pod UID/phase ID, hashed with FNV-1a128 into Go PCG.
Saturation omits Poisson-only fields while retaining common counts/windows.
Failed issue summaries preserve partial observations.

Physical samples own batch ID/size/member IDs/units, service duration, nullable
preceding idle, physical trial ID, execution/stage timing and optional first-token
time. Only the first exchange may lack an idle anchor. Validated membership and
batch timing are compacted once per batch without changing serving responses.

Derivation integrates measurement-node metrics into per-phase joules/mean W.
Incremental energy uses utilization phases only:
`(sum(wallJoules)-IdleBaselineWatts*sum(windowSeconds))/sum(completedInputs)`.
Saturation dispatch residual uses consecutive complete backlogged batches:
`(nextStart-previousStart-previousServiceDurationNanos)/B`. Missing anchor,
membership, actual service or backlog proof supplies no pair.

Runtime image/version remain performance identity. Warmup/initialized domains,
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

The Run owns one deterministic measurement Job, plus one off-node active load
Job. Jobs use backoff zero, restart Never and bounded deadlines. Losing a
recorded Job does not authorize re-execution. Load creation rechecks frozen
gateway UID/Node and waits for control readiness. Cleanup handles completion,
failure and ExecutionLost. Load-first failure waits for runtime classification
before cleanup. Load receives the measurement Job's absolute deadline.

Load and dataset-init each request/limit 512 MiB; dataset emptyDir is 1 GiB.
The producer uses disk-backed indexing and bounded record cache, streams raw
arrays, retains ordering/incomplete-batch metadata and reuses safe buffers.
Exact serialized bytes plus FIFO allocations consume the frozen result cap.
Overflow cancels work explicitly; no truncation, sampling or size projection
admits incomplete evidence.

Policy coherently freezes memory/resources, deadlines, observer capacity,
handoff bounds and workspace. Input tensors are checked before allocation.
Serving and measurement use the same frozen actor memory envelope. Missing
kernel memory peak uses retained window samples whose maximum can miss spikes.

Execution evidence v1alpha18 contains Job/Pod/Node and actuation facts without
terminal-container arrays or Node workload snapshots. Raw counts prove load
success; Job status determines execution completion. Finalization does not
replay raw workload self-consistency. Contradictions are `EvidenceInvalid`;
API/store failures retry finalization against the recorded Job.

Contract replacement follows
[Durable Stored-Contract Compatibility](../../../adr/0019-durable-stored-contract-compatibility.md).
Old working objects are replaced deliberately. There is no dual reader or
rewrite of archived scientific raw.
