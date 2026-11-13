# Measurement Worker Contract

> Navigation: [Component contracts](README.md) · [Profiling design index](../README.md)

## Status and evidence

Implemented for firstfloor active/model-loaded-idle execution, separate runtime/raw
schemas, shared immutable finalization/loading. Scientific admission was removed
(`788bcf93`): current contracts retain execution, raw facts and numeric derivation,
not scientific accept/reject policy. This contract authorizes neither serving,
batch campaigns/optimization nor another runtime; public mode/validation fields
belong to the request schema (Plan 4), not the worker.

Historical method evidence (predates current wire schemas): on 2026-08-03,
`idle-model-loaded-live-ad7b84f-v2` on Lattepanda recorded model load, 60-second
settling, 30-second monotonic window, zero inference and retained residency; 31
successful power samples, zero failed reads, continuous unchanged workloads;
`Accepted` by `idle-wall-power-v1`, with `ExecutionComplete=True`,
`EvidenceVerified=True`, `EvidenceAdmitted=True`. Finalized reference retained
configured raw store/key/bounded size. `active-regression-live-ad7b84f` completed
5,434 calls, 31 successful samples, no failed reads, continuous workload coverage,
and historical `wall-power-v2` acceptance. Causal metric/workload end fence was
live-verified with three sequential same-RunConfig Runs. These verify the earlier
Run/Job/immutable-evidence path, not current simplified semantic schemas, which
remain unverified live. Idle Operator Query API Evidence Passport remains separate
from its active-only projection.

Firstfloor's 1-second readiness timeout formerly leaked onto the kept-alive
measurement socket and failed calls slower than 1 second. Readiness now closes
that connection; measurement reconnects without timeout. Rebuild every runtime
variant and remeasure existing mobilenet profiles with one comparable image set;
pre-fix images cannot run slow calls.

Model-loaded idle also ends the HTTP connection's ownership before entering
settling/measurement. The worker may expire an unused keep-alive socket during
that request-free interval; final counters/metadata open a new connection.
The idle actor, window, zero-inference check and runtime observations are
unchanged. This is neither a timeout increase nor a retry/recovery path.

The local native catalog integration test always retains immutable raw/execution
objects and `native-catalog-evidence.json` under `/tmp/chill-native-catalog-*`;
`CHILL_FIRST_PROFILE_FIXTURE_OUTPUT` selects an explicit directory. Replay that
same directory with `go test ./internal/serving/kube -run
'^TestNativeCatalogReplayRetainedWitness$' -count=1` and the same environment
variable. Per-field catalog equivalence failures report actual and expected
values without weakening the checks or requiring another native measurement.

## Roles and validation ownership

| Role | Contract |
|---|---|
| Artifact init | Copy declared supply-image path as a regular file to runtime-specific destination |
| Runtime worker | Sole raw writer: validate executable RunConfig/artifact, execute firstfloor, coordinate window, compose boundary-validated results, immutable-create raw object, write bounded receipt |
| MetricObserver | Wait for start; bounded observation; observe end, atomically publish only metric result, exit |
| WorkloadObserver | Target-Node list-watch; close after same-watch metric-container successful exit fence; atomically publish only workload result |
| Profiler | Materialize/observe native resources, load immutable raw evidence through the authoritative reader, finalize with terminal Kubernetes facts; never inference/power polling |

No uploader container: it adds handoff/failure ownership without removing worker
responsibility. MetricObserver has no storage credentials. Runtime image is the
trusted complete worker/firstfloor actor/runner plus ORT/RKNNLite helpers; trusted
execution references that image, not the artifact init supply image. Observer
images are delivered separately; only WorkloadObserver receives Kubernetes token.

Each regular process validates its delivered RunConfig at its own execution
boundary; runtime carries `profilingrun.VerifiedRunConfig`, not parallel mutable
inputs. Atomic result readers and runtime adapters validate their process results
once. Coordinator consumes sealed values and checks only new source, target and
window relationships before raw composition. Retained loading validates the raw
record independently; later consumers do not repeat its spec/status/provenance
comparisons. Deploy schema-compatible runtime and observer images. Tags and digest
references are accepted; no digest-pin admission gate applies.

## RunConfig and delivery inputs

RunConfig is execution authority, delivered once as canonical JSON via
`--run-config` to each regular container; no auxiliary per-Run ConfigMap or
compatibility file. Resolve candidate compatibility before Job creation. Only
runtime execution material enters `RunConfig.delivery`; selected policy payloads
stay controller-side Job provenance.

Authenticated recorded-Job recovery uses its immutable executable RunConfig,
without re-admitting it against caller-selected fields or mutable Workload/SLO
copies. Campaign children retain the plan's frozen Workload UID, percentile,
target and native sample count. ResourceVersion remains provenance.
A standalone Run warms its one selected batch by default. BEO children share the serving/idle/active
actor recipe: startup warmup domain `[1]`, with the original active warmup count
in `StartupWarmupIterations` and `Measurement.WarmupIterations=0`.
The actor's full `BatchSizes` declares capability; it does not eagerly execute
higher K. The first current-K execution above 1 is cold and belongs to the active
measurement window, not an unrecorded current-K warmup.

`profilingrun.ExecutionState.ArtifactPath` comes from the trusted artifact
declaration and is recorded in RunConfig. Adapter owns destination: `model.onnx`
for ORT, `model.rknn` for RKNNLite; callers cannot choose it. RKNNLite projects
only verified report's exact host-library, driver-version, platform-compatible and
device paths. File facts are read-only, devices writable; runtime privilege/host
mounts are runtime-only. Request extended resource only if report declares one.
Supplemental groups are necessarily Pod-level; observers get no device mount or
privilege.

Go resolves the candidate report before adapter construction.
`runtime/rknnlite/runner.py`/firstfloor own actual artifact loading, accessible
host libraries/devices, platform, core-mask, initialization and inference checks.
Actual runtime/package/driver versions are retained observations, not equality
gates against declared versions. ORT/CUDA still require the actual selected
backend/provider/device, tensor metadata and initialized batches.

Active/idle accept only matching current Go/Python RunConfig/request schemas.
Optional physical-host contract is semantic library/driver/platform/device/
allocation facts. Optional typed actuation carries non-fixed mode, exact actuator
target and configured policy name; fixed Runs omit it. Actuated Jobs wait for
durable Node power-mode request-generation/readback/transition handoff, not a
NodeActuation CRD or Lease. Settling duration uses the common measurement protocol
and execution provenance, not a parallel execution type.

### Serving-path operating points

RunConfig v1alpha22 freezes the referenced Workload's name, UID, resourceVersion,
SLO percentile and target latency, plus `measurement.operatingPoints`:
`utilizations`, `sparseMeanGapMilliseconds`, `phaseDurationSeconds`,
`postLoadDurationSeconds`, `requestLifetimeSeconds`, `deliveryMarginMilliseconds`,
and `framedDecodedRequestUpperBytes`. `postLoadDurationSeconds` is a required
positive integer number of seconds, propagated unchanged to the native load plan.
`phaseDurationSeconds` also accepts any positive integer; the default remains
30 seconds. Each phase still requires its independent SLO-derived known-gap
sample floor. There is no separate hard-coded thirty-second phase minimum.
The selected execution policy freezes the
computed `queueBytesCap`; this is not a public input. Model, preprocessor and
dataset must match the measurement selection. The existing 95% confidence
convention gives `ceil(log(.05)/log(percentile/100))` known-gap native batch
samples **per phase** (59 at p95). Percentile 100 is rejected.

Each independently restarted active trial uses the same native `/infer` decode,
EDF queue, fixed-batch formation and IPC runner as serving, without a profile
bundle. There is no `/profile` endpoint or in-pod active request driver. A
Run-owned load Job on the gateway's actual node sends one raw input per native
request, matching DOM's ChillServer-origin mechanism. The computed
`runConfig.loadOriginNode` and `loadOriginGatewayPod` (`namespace`, `name`, `uid`)
freeze that observed origin; there is no public load-node setting.

Measurement Jobs use the same network-namespace isolation as native serving
Pods: `hostNetwork` is false and no `hostPort` is published. Serving's Kubernetes
adapter requires that isolation and routes each actor through its Pod-owned
ClusterIP Service; profiling's one off-node producer instead uses the actual
measurement Pod IP directly. The native actor keeps Pod-local TCP 8080 and the
active-only load control keeps Pod-local TCP 8081, so neither competes with
node-agent health or any other node listener. `FirstfloorSession` reaches the
same actor on the Pod's loopback port 8080 in active and loaded-idle modes.
The runtime container declares named `http` and active-only `load-control`
ports. The TCP readiness probe resolves `load-control` on the Pod IP, and the
load Job uses that same control port and actual Pod IP. The returned load plan
publishes that Pod IP's native port 8080. Idle starts no control listener or
off-node load Job. These are Job/runtime delivery choices; RunConfig, raw and
execution-evidence schemas stay unchanged, and retained Jobs keep their original
network shape during recovery and finalization.

The order is **sparse → saturation → utilization ladder**. Sparse Poisson load
uses the configured mean gap and supplies actual no-backlog batch-B service
observations. The largest such observed service supplies the existing K formula:
`K=min(64,floor(queueBytesCap/framedDecodedRequestUpperBytes),
floor((L-margin)/(B*maxObservedSparseServiceSeconds)))`. The shared native
`SaturationWorkers` calculation also checks whole-batch and queue feasibility.
K need not divide B: B=2, K=5 is valid. Total sent inputs end on a B boundary.
Admission slots remain held until the response completes, so K must not exceed
`maxQueueRequests`, including requests already executing.
Missing usable sparse observations or infeasible supply fails the Run. There is
no guessed service bound, old-profile bootstrap, rounding K to a multiple of B,
or fallback. The measured `supplySizing.serviceTimeSource` is
`run.sparse.maxObservedSeconds`.

Saturation uses closed-loop K outstanding requests and measures completed input
throughput as completed inputs divided by monotonic phase elapsed time. The
Evidence Passport divides total completed inputs by the full monotonic runtime window.
Each configured utilization then
produces open-loop Poisson arrivals at utilization × that throughput. The
`chill-server profiling-load` subcommand reuses the gateway's native request
encoder and pooled HTTP client. It runs in the validated gateway Pod's actual
`serving-entrypoint` image; the Python load-generator retains only gateway E5 load.
The producer schedules Poisson clients independently of previous responses.
Clients FIFO-wait for the frozen native admission slots, held until response
completion, just as serving gateway clients do. Temporary slot saturation does
not fail the phase or move a scheduled client arrival. Every scheduled client
must dispatch and complete before successful drain.
The scheduled `scheduledAt` point process is the arrival authority; `dispatchedAt`
records native HTTP dispatch after FIFO hold. Client latency includes that hold.
Target rates, dispatch lag and producer GC pauses remain diagnostics.
Derivation and analyzers use `scheduledInputs / issueElapsedSecondsMonotonic`,
including failed-phase diagnostic rates, never native dispatch rate or target rate.
Every phase runs until both its monotonic duration and known-gap sample floor
complete. The producer stops offering on a whole-batch issuance boundary and
drains every issued request. After the final drained phase, the runtime retains
the same actor and both observers for `postLoadDurationSeconds`, with no inference.
The nominal Run duration is the sum of all phase floors and this post-load window.
The immutable attempt/safety budget fails an unfinished phase; it never completes
evidence. Observer duration and capacity account for the complete lifecycle.
For BEO, runtime-execution memory exhaustion or an execution measurement-budget
boundary ends discovery as `Complete/BatchDiscoveryComplete`, preserving the
completed three-trial batch prefix. Memory exhaustion includes native `OOMKilled`
and ORT's BFCArena allocation failure, carried as typed `MemoryAllocationFailed`
through runner IPC, HTTP and worker termination. Generic ORT errors retain their
failure classification. Only the runtime container's proven Execution stage can
establish this boundary; preparation, observer and commit failures remain separate.
Explicit infrastructure disruption takes precedence. Exit 137 alone proves no OOM,
and budget exhaustion establishes no permanent runtime support limit.
Known Node/Pod eviction or transient infrastructure failures use bounded same-K
retries under the existing `retryLimit`. Generic runtime crashes, model errors,
integrity failures and unknown failures are fatal. All previously completed raw
evidence remains retained even when the campaign is `Failed`; that failure does
not publish a completed-prefix profile. See [Plan 7](../plans/07-batch-energy-optimization-control-path.md).
Native/client sample alignment and actual counts/windows remain retained evidence.
Metric observation follows the runtime end fence, including sample-limited windows.

The sample minimum is not a successful upper bound. The frozen execution budget
reserves `(minServiceSamples+1)*B/arrivalRate` seconds for sparse issuance,
or its configured phase floor if longer. The extra batch supplies the collector's
initial unknown-gap sample. It adds every other phase floor, one
`requestLifetimeSeconds + deliveryMarginMilliseconds/1000` HTTP drain allowance
per phase and `postLoadDurationSeconds`, then rounds the total up to whole seconds.
The configured metric-versus-measurement margin is retained. At p99, B=8 and
one sparse input/second, sparse issuance alone reserves 2,400 seconds.
`AttemptTimeout` adds settling and the existing `attemptTimeoutOverheadSeconds`;
the Job adds the existing longer `activeDeadlineOverheadSeconds` instead.
These bounds derive from immutable Run inputs and selected execution policy.
Poisson arrivals and sample-limited service have no deterministic finite
completion guarantee; the frozen bound remains a failure budget.
Both observers' measurement-end waits use that producer failure bound; the metric
observer's `ObserveUntil` watchdog and pre-readiness capacity admission use the
same bound, including its final real power read. Interval/per-read timeout and
the producer's actual elapsed windows remain unchanged. Runtime failure still
interrupts both observers immediately; reaching a safety bound is never success.

The sweep does not stop at a local energy minimum, slow p95 or SLO-impossible
batch-one result. SLO sets the sample floor; it does not supply an early-stop
branch or weaken evidence requirements.

For iid continuous service samples, the pooled sample maximum covers population
q95 with probability `1-.95^59 ≈ .9515` at 59 samples. This minimal coverage is not
a precise p95 estimate, an independence/stationarity check or proof of κ safety;
59 is a count of calls, not a count of tail exceedances.

Shared CPU policy: every CHILL operation runs without CPU reservation, quota or
pinning. RunConfig v1alpha22 has no CPU allocation or thread-tuning field.
Measurement and load Jobs carry no CPU requests/limits; the runner accepts no
affinity or thread override, and the Go producer uses default runtime scheduling.
Actual quota/cpuset/affinity/usage/throttling remain observed evidence. The
gateway-side load Job has bounded memory resources described below.

Active runtime and raw bodies are v1alpha14; idle runtime v1alpha7 and idle raw
v1alpha8 retain the existing loaded-idle protocol. Old active records are deleted
operationally after archival; no reader accepts their previous shapes. Memory,
GPU, Node identity, ownership and physical-return checks remain mandatory.


Store delivery is not scientific input: logical store, internal endpoint/bucket/
region, immutable credential Secret reference and deterministic raw key. Profiler
startup knows owning `ChillSystem`; Job creation exact-reads its enabled store
reference/configuration and system-owned Service, binds allocated ClusterIP/S3
port directly, and derives Secret reference without reading bytes. Require current
availability and completed Operator-owned bootstrap Job attesting exact ConfigMap
`resourceVersion`; no operand reconcile
or direct probe.

Immutable Job PodSpec retains literal store/endpoint/bucket/region and Secret
references. Finalization recovers this binding, never switches an in-flight Run
to newer configuration. None enter `ProfilingRun.spec` or RunConfig. Endpoint,
bucket, region and Secret data stay outside status/logs; compact status may expose
only logical store and opaque finalized key. No Run override. Logical store is
CHILL Evidence Store Service name, not endpoint. Fixed keys:
`profiling-runs/<Run UID>/raw.json`, `profiling-runs/<Run UID>/execution.json`.
Different Run objects are distinct trials even with identical scientific config.

## Job-local handoff

Four base volumes: artifact-populated model `emptyDir`; coordination `emptyDir`
shared by regular containers; bounded private runtime-workspace `emptyDir`; token/
CA projection only for WorkloadObserver. Accelerator adds exact `hostPath` per
verified host fact, never whole device directories or observer mounts. Workspace
holds private firstfloor result and bounded pre-upload evidence candidate, not a
cross-container protocol or retained store.

| Atomic bounded coordination path | Owner/meaning |
|---|---|
| `observer-ready` | Metric helper ready for start |
| `workload-observer-ready` | Workload preflight list-watch capability ready |
| `workload-window-start` | Runtime warm-up complete; request retained session |
| `workload-window-ready` | Workload retained list-watch established |
| `measurement-start` | Runtime opens window after warm-up/idle settling |
| `measurement-end` | Runtime closes the window after active post-load observation or idle duration |
| `metric-result.json` | Complete result via temp-file + rename |
| `workload-result.json` | Complete atomic result after causal status fence |
| `runtime-failed` | Runtime failure; observers end all waits/watch and exit failed |

Runtime: strict config validation; verify Pod UID, Node, artifact, runtime/
provider/output-validation capability and observe actual CPU environment; bounded wait for both preflight readiness
signals; actor startup warmup and, for loaded-idle, settling; request/await retained
workload window; emit start; measure until elapsed/sample floors and active post-load observation (idle: duration); emit end; bounded
wait for both results; check new source/target/window relationships; encode
mode-specific raw object within the byte cap; immutable-create without replacing
an existing winner; write bounded validated receipt to `/dev/termination-log`; exit zero.

Idle start requires exact load/common startup warmup and full observer-independent
settling; its measured window requires monotonic duration, zero inference and
verified residency. BEO requires batch 1 in its domain, so idle executes at batch
1 for both ORT and RKNN. Trial identity retains its planned logical batch and
attempt independently of the measurement batch. Runtime validates declared
capability before execution and initializes higher-K inputs lazily; an artifact
declaration does not prove that a future K will execute. Runtime emits signals
without reading observer results; observers cannot choose a window from observed
values.

Metric helper validates config, constructs frozen edge source, emits readiness,
bounded-waits start, observes until runtime end within the producer attempt
failure bound, publishes its final real read/result and exits. Workload helper
validates config/own Pod/Job, probes readiness,
establishes fresh retained list-watch after warm-up, retains changes through end
and same-watch successful `metric-observer` termination, then publishes. See
[workload contract](workload-observer.md) for causal coverage/ambiguous drain facts.
Runtime failure atomically emits `runtime-failed` before its failure exit; both
helpers observe from startup and stop within seconds. Logs are diagnostic, never
evidence/handoff; partial result files are never visible under final names.

## Raw evidence, receipt and failure

Active raw contains verified RunConfig, actual Pod UID, validated runtime result,
complete metric and bounded workload results. Idle has separate current schema:
exact load, settling completion, zero inference, residency and declared duration
instead of active latency/output-validation proof. Both share store/key/size
reference boundary. Job completion, terminal Pod/Node snapshots and credentials
are absent; the controller adds terminal Kubernetes facts during finalization.

### Active raw v1alpha14 field contract

`raw.json` has `schemaVersion`, `runConfig` (v1alpha22), `podUID`, `runtime`,
`metrics`, and `workload`. Raw evidence contains measured facts and boundaries.
`runtime.schemaVersion` is
`chill.dacs.io/batch-characterization.v1alpha14`. Existing actual runtime,
CPU/memory, startup and execution metadata remain. `runtime.phases` replaces
`repetitionResults`; there is no legacy reader.

Phases appear once in sparse, saturation, then configured utilization order.
Each object contains:

| Field | Meaning |
|---|---|
| `id`, `kind` | Unique phase ID; `sparse`, `saturation`, or `utilization` |
| `utilization` | Configured utilization, only for utilization phases |
| `arrivalRatePerSecond` | Target Poisson scheduling rate, only for sparse/utilization phases |
| `workers` | Measured sparse-derived K, only for saturation |
| `measurementStartedAt`, `measurementEndedAt` | Measurement-node UTC power-window boundaries |
| `measurementElapsedSecondsMonotonic` | Load-producer monotonic phase duration |
| `completedInputs` | Completed native requests, each carrying one input |
| `requests` | Every completed request; no failed request is silently omitted |
| `nativeSamples` | One physical batch per globally unique `batchID` |
| `latencyMs` | Maximum member scheduled-to-completed client latency, including FIFO hold, aligned with each batch sample |
| `offeringStartedAt`, `offeringEndedAt` | Load-node UTC offering start and last scheduled arrival, required in every phase |
| `drainEndedAt` | Load-node UTC last response completion, required in every phase |
| `issueElapsedSecondsMonotonic` | Offering duration through the last scheduled arrival, excluding response drain, required in every phase |
| `achievedArrivalRatePerSecond` | `scheduledInputs / issueElapsedSecondsMonotonic`, required in every phase |
| `scheduledInputs`, `sentInputs` | Required schedule and native dispatch counts; both equal `completedInputs` on success; saturation counts closed-loop issued clients |
| `arrivalSeed` | Poisson-only trial/phase seed identity, `podUID/phaseID` |
| `issueLagMs` | Poisson-only count, min, mean, p50, p95, p99 and max of monotonic scheduled-to-dispatch lag; no lag threshold |
| `driverGC` | Producer GC cycle count and total/maximum stop-the-world pause milliseconds |

Each request contains `requestID`, `recordID`, client arrival `scheduledAt`,
native HTTP dispatch `dispatchedAt`, `completedAt`, `latencyMs`, and native `/infer`
response identity, output and request-local progress. Requests are ordered by
`scheduledAt`. `latencyMs` covers scheduled arrival through response completion;
native response elapsed must nest within dispatch-to-completion, which nests
within client latency. `dispatchedAt` is the actual call into the shared native
encoder/exchange. The native Context keeps `receivedAt=scheduledAt` and
`senderElapsedMS=dispatchedAt−scheduledAt`, so queue hold consumes the existing
gateway request elapsed/lifetime budget. UTC client timestamps and offering/drain
boundaries belong to the **load node**; latency and dispatch lag use its monotonic
clock. Measurement-node phase boundaries remain separate power fences.
Stored UTC deltas are not required to equal monotonic latency exactly.
The recorded `arrivalSeed` is hashed with FNV-1a128; its first/last big-endian
64-bit words seed Go PCG. `response.progress.nativeHTTP` retains measurement-node
handler entry and receive/decode/enqueue/finish offsets. Cross-host subtraction
never defines latency.

Saturation retains the common counts, offering/drain boundaries, offering duration
and achieved arrival rate, but omits Poisson seed, issue lag and target rate.
Failed issue summaries retain these common fields as partial observations:
zero schedules end offering at its start; zero completed responses end drain at
its start. A failed drain may precede the last scheduled arrival.

Each native sample retains `batchID`, `batchSize`, ordered `requestIDs`,
per-member `recordIDs` and `units`; `serviceDurationNanos`, nullable
`precedingIdleNanos`, physical Pod `trialID`; `runnerServiceMs`, `preprocessMs`,
`backendCallMs`, `postprocessMs`, `nativeRequestMs`, and optional generative
`firstTokenMs`. Measurement-node UTC `startedAt`/`finishedAt` anchor the runner
exchange; `earliestEnqueuedAt`/`latestEnqueuedAt` bound native member enqueue times.
Only the first physical exchange may lack a preceding idle anchor. Repeated batch
membership, batch times and service/stage facts are removed from each retained
request response after validating the original native responses; samples own
these facts once per physical batch. The serving HTTP response itself is unchanged.

`runtime.supplySizing` contains `batch`, `maxBatchServiceSeconds`,
`serviceTimeSource`, `requestLifetimeSeconds`, `marginSeconds`, `workerCap`,
`queueBytesCap`, `framedDecodedRequestUpperBytes`, `workers`, and `limitingBound`.
The source is `run.sparse.maxObservedSeconds`; bound names and tie order remain
`queue-cap`, `bytes-cap`, `lifetime`. Every member of a no-backlog sparse batch
must enqueue after the preceding batch finishes. The initial batch qualifies
only because native starts with an empty queue.

Derivation publishes `phasePower` in each derived trial and catalog measurement
batch facts: `trialID`, `id`, `kind`, `windowStart`, `windowEnd`,
`completedItems`, `wallJoules`, and `meanWallWatts`. These integrate the
measurement node's retained `metrics.samples`, never the load node's power.
Raw evidence itself retains only the phase boundaries and raw meter samples.
The load producer obtains phase boundaries through worker-local
`POST /measurement/phase` start/end markers. `GET /measurement/load` supplies
the immutable plan; `POST /measurement/supply` derives K after sparse;
`POST /measurement/load-result` delivers success phases or explicit failure.
The final phase-end marker closes only that load phase and immediately snapshots
the actor counters and post-load start clocks. Load-result transfer/decoding
cannot hide the initial stabilization interval or restart this clock. The worker
keeps the same live loaded actor without requests until the frozen post-load
duration completes, then closes the measurement window.
`runtime.postLoad` contains measurement-node `startedAt`, `endedAt`, monotonic
`durationSecondsMonotonic`, `actorPID`, `modelResidentAtEnd`, `countersBefore`
and `countersAfter`. Both counter snapshots retain `acceptedRequests`,
`completedRequests` and `executions`; equality proves zero inference/requests.
The actor remains ready with no outstanding requests; metadata verifies model
residency before the final end fence. Timestamped `metrics.samples` within these
boundaries are the unaggregated post-load power stabilization series, including
their original thermal observations; `metrics.failures` retains read failures.
Post-load samples never enter utilization energy-per-input or service populations.
Only after this observation does the worker stop the native memory sampler or
read the kernel peak, freeze CPU/window observations and publish measurement-end.
The kernel peak retains its runtime-container-lifetime source.
The result-upload handler completes post-load observation and freezes memory,
CPU and the end fence before reading or decoding the large result body. Result
upload uses the immutable attempt deadline, so a post-load window longer than
the short control-call timeout remains valid. Evidence bytes and decoded request
objects do not contribute to the published memory observation.

Energy per input uses **only utilization phases**:
`(sum(wallJoules)-IdleBaselineWatts*sum(windowSeconds))/sum(completedInputs)`.
All phases supply scheduler `ServiceSamples` unchanged. Derived trial and
catalog batch facts retain `phasePower` and signed `dispatchOccupancyNanosPerInput`; saturation d
uses consecutive complete backlogged batches and the preceding batch's actual
service: `(nextStart-previousStart-previousServiceDurationNanos)/B`. A missing
anchor, member, actual service, or backlog proof contributes no d pair.

Exit zero requires successful immutable-create outcome and one
`evidence-store-receipt.v1alpha1` JSON termination message within 4 KiB. An existing
key keeps its original winner without candidate-byte comparison. The receipt is
a hint: finalization independently reads, bounds and validates retained evidence.
Failure codes:

| Code | Meaning |
|---:|---|
| 2 | Invalid/drifted RunConfig or delivery |
| 3 | Artifact/runtime/provider/inference/output validation |
| 4 | Observer readiness/execution/handoff/result validation |
| 5 | Raw encoding/byte bound/immutable write/receipt failure |

The worker records its versioned phase and typed failure in `/dev/termination-log`;
successful commit replaces that record with the receipt. Profiler retains the
first runtime termination and native Job reason/message, including early
`FailureTarget=True`; final `Failed=True` takes precedence when both are present.
An absent Pod or execution stage leaves classification unknown even when the Job
reports `DeadlineExceeded`. Unstructured logs remain diagnostic.

Observer images and package/driver readbacks remain provenance. CurvePlan
v1alpha14 retains the actor startup recipe and active/idle collection policy.
Profile applicability includes the measured runtime image identity and runtime
version; serving prepares that measured image and verifies the actual runtime
container image ID. Model/artifact/preprocessor/input population, actual
backend/device and memory/power conditions remain relevant. Initialized/warmup
domains, collection windows and meter identity remain measurement provenance.
Actual inference, shape, fixed-batch counts, native service-pair integrity and
selected-device checks still apply. Latest failed selection and operator-directed
replacement/reuse remain separate.

## Kubernetes execution and bounded capacity

One deterministic Run-owned measurement Job: `parallelism=1`, `completions=1`,
`backoffLimit=0`, `restartPolicy=Never`, no replacement for same Run;
`automountServiceAccountToken=false`; Downward API Pod UID/scheduled Node; exact
Node placement, shared CPU and frozen memory/accelerator resources; immutable
artifact/runtime/observer OCI references; restricted contexts; bounded deadline.
Job name derives from Run UID: losing/deleting recorded Job never permits repeat.

Active Runs also own one deterministic off-node load Job, pinned to the ready
serving-entrypoint Pod's actual node, exactly where DOM originates load. The
installation's existing system/release identity selects that Pod. Preparation
freezes its node and identity; Job creation rechecks the same Pod UID/node.
Missing/ambiguous gateway placement or equality with the measurement node fails
the Run. There is no arbitrary other-node fallback or new topology knob.
The profiler waits for the measurement Pod's control readiness before creating
load, waits for load completion/failure before collecting the uploaded result,
and removes load on normal completion, runtime failure and ExecutionLost.
Both active and model-loaded-idle measurement Pods have init `artifact` and
`preprocessor`, plus `runtime`, `metric-observer`, and `workload-observer`.
The active load Pod alone has init `dataset` and app `load`. Before publishing
verified evidence, finalization independently checks the load Job's Run owner,
frozen origin node, unique Job-owned successful Pod, frozen dataset image and
immutable Job load image, and both containers' successful terminal states.
Evidence v1alpha17 keeps its measurement-Pod-only terminal snapshot unchanged;
retained loading does not retrospectively assert a stored load-container snapshot.
Do not append load containers to the measurement Pod or manufacture missing facts.
Retained raw and successful measurement/load Pods can finalize after a verifier
fix without rewriting raw or RunConfig. API/store read failures remain retryable;
immutable semantic contradictions are terminal `EvidenceInvalid`.
Load-first failure waits for runtime termination evidence before cleanup, preserving
typed `MemoryAllocationFailed` discovery completion and the completed batch prefix.
Profiling load Jobs carry the existing target-Node measurement-load label so
terminal-root handback removes them across configured measurement namespaces.
The load command receives measurement Job start + ActiveDeadlineSeconds as one
absolute deadline; late load creation or Pod startup never extends that deadline.
All control HTTP exchanges have finite timeouts within the remaining attempt.

The load and dataset-init containers each request and limit **512 MiB**, with no
CPU reservation, quota or pinning. Their dataset `emptyDir` is limited to **1 GiB**.
These are installation delivery resources, not Run inputs or thread controls.
The driver retains complete evidence within the frozen result cap; it never
trims requests to fit.

The dataset record index is disk-backed. The producer retains one immutable
prepared-record cache, bounded by the existing framed-record cap; it does not
preload the whole dataset as wire payloads. Encoder/response buffers are reused.
The shared gateway client keeps its standard in-memory HTTP body path; buffers
with unfinished or unobserved transport writes are not recycled.
Raw request/sample/latency arrays stream to `/data/.profiling-evidence-*` during
collection and upload. Memory retains ordering indexes and unfinished-batch
metadata, rather than the complete evidence arrays.

One versioned installation worker policy derives resources, deadlines/handoff
margins and producer capacities coherently. Helm supplies default measured envelope;
Profiler selects by DeviceClass/execution path/workload mode before Job creation.
Immutable Run retains exact selected canonical policy/source ConfigMap provenance;
Job retains projected native resources and content-derived policy identity, not
CR status. These are delivery bindings, not Run knobs or spec overrides. Runtime/
metric entrypoints have no hidden default handoff wait.

Required relationships:
- Metric combined sample/failure event cap admits full duration/interval schedule
  before polling and remains enforced while recording.
- Independent handoff-byte limit rejects oversize before raw commit.
- Reject input tensor before allocation above calculated-byte budget; ceiling leaves
  measured reserve for model/session/outputs/bounded input-generation temp/worker.
- Retained latency cap and serialized-runtime-result cap fit memory and handoff.
- The producer enforces exact cumulative serialized result bytes plus retained
  FIFO allocation bytes incrementally against the same frozen result cap.
  FIFO capacity is reserved before allocation and released after phase drain.
  Exceeding the cap cancels outstanding requests immediately and reports the
  result-budget error, even when no native response has completed. There is no
  throughput/result-size projection admission gate, `6√N` reserve or per-phase
  4096-byte reserve.
- Workspace fits private result plus one bounded evidence candidate, not image layer.
- Common deadline is established once before worker preparation and covers
  artifact/model/input preparation, observer synchronization, warm-up, measurement,
  both result handoffs and evidence commit. Metric/workload result and causal
  end-fence caps equal the frozen attempt timeout and inherit only its remaining
  context budget; runtime completion does not start a new global deadline or a
  nominal-window final-read allowance.

Derive capacities from resources/duration, never predicted throughput. Oversize
artifact shape or inability to retain full latency distribution fails explicitly;
no truncation, sampling or silent aggregation. Installation envelopes affect only
new Runs and change content-derived execution identity. Later runtimes may have
their own policy; no generic planner framework.

## Dependencies and remaining boundary

Runtime worker -> firstfloor adapter -> `internal/profiling/profiler/evidence`
raw codec -> `internal/evidencestore`/S3. Metric helper -> `internal/metricobserver`
-> edge-metrics adapter. Workload helper -> `internal/workloadobserver` -> read-only
Pod list-watch adapter. No Job container imports controller-runtime or reconciles
objects. Controller trusts independently verified immutable evidence/native facts,
never terminated-Pod shared files. Current-schema live verification, idle Passport,
image rebuild/remeasurement and measured capacity defaults retain the limits above.

## Local native driver microbenchmark

Measured on genesis1, Intel i7-8700K, linux/amd64, Go 1.26.4. Each offering
window lasts 10 seconds. A separate local process serves the native HTTP wire
contract with a 16 KiB input and a small classification output. The producer
uses the real shared gateway encoder/transport. CPU, allocations and kernel
`VmHWM` below measure the driver process only. No CPU quota, reservation, affinity
or `GOMAXPROCS` override applies.

```sh
systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 \
  env GOTOOLCHAIN=go1.26.4 GOFLAGS=-p=1 \
  go test ./cmd/chill-server -run '^$' \
    -bench '^BenchmarkProfilingLoadNative$' -benchtime=1x -count=1 -timeout=3m
```

| Target / trial | Scheduled = sent = completed | Achieved inputs/s | Lag p50 ms | p95 ms | p99 ms | Maximum ms |
|---|---:|---:|---:|---:|---:|---:|
| 200 / 1 | 2,036 | 203.604 | 0.0516 | 0.5223 | 0.5945 | 4.158 |
| 500 / 1 | 4,992 | 499.243 | 0.0423 | 0.4805 | 0.5700 | 3.895 |
| 1000 / 1 | 10,021 | 1002.145 | 0.0659 | 0.4600 | 0.5731 | 7.457 |
| 1000 / 2 | 10,114 | 1011.383 | 0.0661 | 0.4696 | 0.6105 | 13.860 |
| 1000 / 3 | 10,010 | 1001.082 | 0.0671 | 0.4533 | 0.7995 | 13.430 |

| Target / trial | CPU cores | Peak RSS MiB | Allocated B/input | GC cycles | Total pause ms | Maximum pause ms | Raw result B/input |
|---|---:|---:|---:|---:|---:|---:|---:|
| 200 / 1 | 0.1386 | 61.35 | 37,702 | 14 | 1.029 | 0.131 | 1,330 |
| 500 / 1 | 0.2730 | 81.43 | 36,284 | 20 | 1.232 | 0.136 | 1,331 |
| 1000 / 1 | 0.4874 | 117.57 | 36,047 | 26 | 1.916 | 0.158 | 1,335 |
| 1000 / 2 | 0.4903 | 117.76 | 36,087 | 26 | 2.990 | 1.366 | 1,336 |
| 1000 / 3 | 0.7088 | 118.65 | 36,127 | 25 | 4.638 | 0.737 | 1,336 |

All five phases completed, including three independent 1000/s trials. Each
retains its `arrivalSeed`; the 1000/s seeds are
`pod/benchmark-1000-trial-{1,2,3}-0`. These historical dispatch observations include
the reported scheduling/GC gaps. Every scheduled input was sent; no response-driven
backpressure or count loss occurred. Scheduled client arrivals remain the current
measurement authority; dispatch lag is retained separately.

The measured peak describes this local fixture within the 512 MiB load-container
envelope. Its **1.30 KiB/input** serialized result coefficient is diagnostic,
not an admission gate. Current collection streams exact evidence bytes and uses
a bounded single-record cache instead of whole-dataset/evidence wire retention.

