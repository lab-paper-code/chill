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

## Roles and validation ownership

| Role | Contract |
|---|---|
| Artifact init | Copy declared supply-image path as a regular file to runtime-specific destination |
| Runtime worker | Sole raw writer: verify RunConfig/artifact, execute firstfloor, coordinate window, compose validated results, immutable-create raw object, write bounded receipt |
| MetricObserver | Wait for start; bounded observation; observe end, atomically publish only metric result, exit |
| WorkloadObserver | Target-Node list-watch; close after same-watch metric-container successful exit fence; atomically publish only workload result |
| Profiler | Materialize/observe native resources, independently verify immutable raw evidence and finalize with terminal Kubernetes facts; never inference/power polling |

No uploader container: it adds handoff/failure ownership without removing worker
responsibility. MetricObserver has no storage credentials. Runtime image is the
trusted complete worker/firstfloor actor/runner plus ORT/RKNNLite helpers; trusted
execution references that image, not the artifact init supply image. Observer
images remain separately pinned; only WorkloadObserver receives Kubernetes token.

All three regular processes strict-decode/validate the same canonical RunConfig
at their own boundary; runtime carries `profilingrun.VerifiedRunConfig`, not
parallel mutable inputs. Result-file readers validate once after atomic handoff;
runtime adapters strict-decode/check process results once. Coordinator passes
sealed, unmutated source-package verified values to raw composition, which checks
only new cross-record source/window relationships and canonicalizes without
revalidating nested values. Deploy the three images as one schema-compatible
set, each independently pinned to an exact OCI reference.

## RunConfig and delivery inputs

RunConfig is execution authority, delivered once as canonical JSON via
`--run-config` to each regular container; no auxiliary per-Run ConfigMap or
compatibility file. Resolve candidate compatibility before Job creation. Only
runtime execution material enters `RunConfig.delivery`; selected policy payloads
stay controller-side Job provenance.

Job recovery compares caller-selected fields (including Workload name) with the
requested ProfilingRun. Standalone SLO/sample-count/source facts resolved during
preparation are not caller-selected. Campaign children instead carry the plan's
frozen Workload UID, percentile, target and native sample count, so those selected
facts are retained and checked without rereading the Workload. ResourceVersion
is raw provenance only and never an admission/identity gate. A standalone Run
warms its one selected batch by default; campaign children carry the full frozen
measured warmup domain.

`profilingrun.ExecutionState.ArtifactPath` comes from the trusted artifact
declaration and is recorded in RunConfig. Adapter owns destination: `model.onnx`
for ORT, `model.rknn` for RKNNLite; callers cannot choose it. RKNNLite projects
only verified report's exact host-library, driver-version, platform-compatible and
device paths. File facts are read-only, devices writable; runtime privilege/host
mounts are runtime-only. Request extended resource only if report declares one.
Supplemental groups are necessarily Pod-level; observers get no device mount or
privilege.

Go verifies projected candidate report once before adapter construction.
`runtime/rknnlite/runner.py`/firstfloor own RKNNLite artifact, image-package,
host-library, driver, platform, character-device, core-mask, initialization and
inference checks, not ORT provider vocabulary. `runtime/firstfloor/measurement.py`
compares actual runtime facts to delivered host declarations before raw encoding.

Active/idle accept only matching current Go/Python RunConfig/request schemas.
Optional physical-host contract is semantic library/driver/platform/device/
allocation facts. Optional typed actuation carries non-fixed mode, exact actuator
target and configured policy name; fixed Runs omit it. Actuated Jobs wait for
durable Node power-mode request-generation/readback/transition handoff, not a
NodeActuation CRD or Lease. Settling duration uses the common measurement protocol
and execution provenance, not a parallel execution type.

### Sample-driven active stop

RunConfig v1alpha19 freezes the referenced Workload's name, UID, resourceVersion,
SLO percentile and target latency. Model, preprocessor and dataset must match the
measurement selection; no percentile is inferred or defaulted. The existing 95%
confidence convention gives `ceil(log(.05) / log(percentile/100))` completed native
service calls per trial (59 at p95). Percentile 100 cannot have finite coverage and
is rejected for this measurement contract.

Each of the three independently restarted active trials for every planned batch
runs until **both** elapsed time is at least 30 seconds (or the longer requested
minimum) and its derived service-call count is complete. Budget exhaustion and
the immutable safety timeout fail the trial; neither is successful completion.
Native/client sample alignment and actual counts/windows remain retained evidence.
Metric observation follows the runtime end fence, including sample-limited windows.

The sample minimum is not a successful upper bound: 59 calls at 2 seconds
complete at 118 seconds even with the sample YAML's 30-second minimum and
31-second nominal metric duration. The immutable selected execution policy's
`AttemptTimeout` (`Metrics.DurationSeconds + settling duration +
attemptTimeoutOverheadSeconds`) is the producer's total failure budget, applied
by `cmd/measurement-worker` to its root attempt context. The Job's longer
`ActiveDeadline` is the outer Kubernetes safety budget, not a completion rule.
Both observers' measurement-end waits use that producer failure bound; the metric
observer's `ObserveUntil` watchdog and pre-readiness capacity admission use the
same bound, including its final real power read. Interval/per-read timeout and
the producer's actual elapsed windows remain unchanged. Runtime failure still
interrupts both observers immediately; reaching a safety bound is never success.

The sweep does not stop at a local energy minimum or a slow p95. Only batch-one
evidence from all three current-condition, same-device trials whose fastest
observed native service call already exceeds the frozen SLO target can end the
cell at batch one. Its terminal `BatchServiceExceedsSLO` records negative capacity,
not a usable batch or invented positive κ. Old-regime evidence cannot select this
branch. The original planned warmup domain remains unchanged.

For iid continuous service samples, the pooled sample maximum covers population
q95 with probability `1-.95^59 ≈ .9515` at 59 samples. This minimal coverage is not
a precise p95 estimate, an independence/stationarity check or proof of κ safety;
59 is a count of calls, not a count of tail exceedances.

Shared CPU policy: RunConfig v1alpha19 has no CPU allocation or thread-tuning field. Jobs carry
no CPU requests/limits and the runner accepts no affinity or thread override.
ORT SessionOptions defaults are unchanged; session readback records 0 for automatic
thread selection, not a fixed single thread. Actual cgroup quota (v2 `max`, v1 `-1`
included), cpuset/affinity, usage and throttling remain runtime evidence. Active
runtime body v1alpha9 and idle runtime body v1alpha7 are unchanged. Active raw v1alpha10,
idle raw v1alpha8 and finalized v1alpha17 cut over together after dependent campaigns finish.
Old controlled evidence/publications are not rewritten or admitted through a compatibility reader. Memory/GPU/Node
identity, ownership and physical-return checks remain mandatory.


Store delivery is not scientific input: logical store, internal endpoint/bucket/
region, immutable credential Secret reference and deterministic raw key. Profiler
startup knows owning `ChillSystem`; Job creation exact-reads its enabled store
reference/configuration and system-owned Service, binds allocated ClusterIP/S3
port (hostNetwork Jobs cannot resolve cluster DNS), derives Secret reference
without reading bytes. Require current availability and completed Operator-owned
bootstrap Job attesting exact ConfigMap `resourceVersion`; no operand reconcile
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
| `measurement-end` | Runtime closes window after last active inference/idle duration |
| `metric-result.json` | Complete result via temp-file + rename |
| `workload-result.json` | Complete atomic result after causal status fence |
| `runtime-failed` | Runtime failure; observers end all waits/watch and exit failed |

Runtime: strict config validation; verify Pod UID, Node, artifact, runtime/
provider/output-validation capability and observe actual CPU environment; bounded wait for both preflight readiness
signals; active warm-up or loaded-model idle settling; request/await retained
workload window; emit start; one duration-based measurement; emit end; bounded
wait for both results; check new source/identity/summary/timestamp/coverage
relationships; encode mode-specific raw object and bounded size; one immutable
create; write validated receipt to `/dev/termination-log`; exit zero.

Idle start requires exact load/full observer-independent settling without
inference; end requires monotonic duration, zero inference and verified residency.
Non-RKNN idle measurement batch is always one; RKNN retains its first planned
capability batch. Campaign trial names retain the planned batch independently
of the measurement batch so retries and completed-trial accounting stay stable.
ORT model sessions load artifact files without a batch argument, but firstfloor
request validation still requires the selected batch in `initialized_batches`:
an ORT artifact declaring only `[4]` therefore cannot execute batch-one idle
without a separate runtime contract change. Runtime emits signals without
reading observer results; observers cannot choose a window from observed values.

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
reference boundary. No Job completion, terminal Pod/Node snapshot, credentials or
backend response: controller adds native facts independently during finalization.

Exit zero requires immutable commit and exactly one
`evidence-store-receipt.v1alpha1` JSON termination message within 4 KiB. Receipt
is a hint; controller independently retrieves/verifies. Failure codes:

| Code | Meaning |
|---:|---|
| 2 | Invalid/drifted RunConfig or delivery |
| 3 | Artifact/runtime/provider/inference/output validation |
| 4 | Observer readiness/execution/handoff/result validation |
| 5 | Raw encoding/immutable write/post-write verification |

Failure writes stderr, leaves `/dev/termination-log` empty. Runtime uses
`FallbackToLogsOnError` (Kubernetes last 80 lines or 2 KiB). Controller copies
runtime exit/message once to `ProfilingRun.status.runtimeTermination` on Job
failure, surviving Pod deletion; diagnostic only, never parsed. Kubernetes owns
native failure state.

Observer images enter measurement-protocol provenance; runtime/worker image enters
execution-state provenance. CurvePlan v1alpha11 freezes the common actor startup
recipe and runtime package versions separately from active/idle collection and
sample-stop policy. Active/idle pairing still checks the recorded recipe and
protocol, but latest-success performance identity excludes exact runtime image,
observers, windows and meter identity. Artifact/preprocessor recipes and images,
input population, initialized/warmup domains and CPU/thread/memory/power settings
remain semantic conditions. A collection-only change cannot revive a replaced
older sibling; latest failed selection and explicit reuse remain separate.

## Kubernetes execution and bounded capacity

One deterministic Run-owned Job: `parallelism=1`, `completions=1`,
`backoffLimit=0`, `restartPolicy=Never`, no replacement for same Run;
`automountServiceAccountToken=false`; Downward API Pod UID/scheduled Node; exact
Node placement/configured CPU request-limit; immutable artifact/runtime/observer
OCI references; restricted security contexts; bounded active deadline.
Job name derives from Run UID: losing/deleting recorded Job never permits repeat.

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
- Workspace fits private result plus one bounded evidence candidate, not image layer.
- Common deadline is established once before worker preparation and covers
  artifact/model/input preparation, observer synchronization, warm-up, measurement,
  both result handoffs and evidence commit. Metric/workload result and causal
  end-fence caps equal the frozen attempt timeout and inherit only its remaining
  context budget; runtime completion does not start a new global deadline or a
  nominal-window final-read allowance.

Derive capacities from resources/duration, never predicted throughput. Oversize
artifact shape or inability to retain full latency distribution fails explicitly;
no truncation, sampling or silent aggregation. Numeric reserves/serialized-byte
coefficients require measurements from exact pinned runtime image, become versioned
installation defaults, affect only new Runs and change content-derived execution
identity. Later runtimes may have their own policy; no generic planner framework.

## Dependencies and remaining boundary

Runtime worker -> firstfloor adapter -> `internal/profiling/profiler/evidence`
raw codec -> `internal/evidencestore`/S3. Metric helper -> `internal/metricobserver`
-> edge-metrics adapter. Workload helper -> `internal/workloadobserver` -> read-only
Pod list-watch adapter. No Job container imports controller-runtime or reconciles
objects. Controller trusts independently verified immutable evidence/native facts,
never terminated-Pod shared files. Current-schema live verification, idle Passport,
image rebuild/remeasurement and measured capacity defaults retain the limits above.
