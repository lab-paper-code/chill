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
remain unverified live. Idle Operator Console Evidence Passport remains separate
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

Runtime: strict config validation; verify Pod UID, Node, artifact, CPU/runtime/
provider/output-validation capability; bounded wait for both preflight readiness
signals; active warm-up or loaded-model idle settling; request/await retained
workload window; emit start; one duration-based measurement; emit end; bounded
wait for both results; check new source/identity/summary/timestamp/coverage
relationships; encode mode-specific raw object and bounded size; one immutable
create; write validated receipt to `/dev/termination-log`; exit zero.

Idle start requires exact load/full observer-independent settling without
inference; end requires monotonic duration, zero inference and verified residency.
CPU ORT canonical idle batch is one; batch-fixed artifacts require their capability
batch, identifying residency, not idle inference. Runtime emits signals without
reading observer results; observers cannot choose a window from observed values.

Metric helper validates config, constructs frozen edge source, emits readiness,
bounded-waits start, observes RunConfig power duration, bounded-waits end, publishes
result, exits. Workload helper validates config/own Pod/Job, probes readiness,
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

Observer images enter measurement-protocol identity; runtime/worker image enters
execution-state identity. Rebuild changes newly resolved identities, not validity
of retained earlier profiles. Curve active/idle pairing requires one runtime
image/protocol each; campaign and baseline use one image set.

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
- Common deadline covers artifact/model/input preparation, observer synchronization,
  warm-up, measurement, handoff and evidence commit.

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
