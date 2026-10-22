# Measurement Worker Contract

> Navigation: [Profiling design index](../README.md)

## Status

Implemented for the firstfloor profiling path. The causal metric/workload
end-fence success path was live-verified with three sequential, same-RunConfig
Runs admitted by `wall-power-v2`. This document does not authorize batch
campaigns, batch-energy optimization, another runtime, or serving.

The current RunConfig and firstfloor request schemas, the firstfloor runtime
adapter, separate idle runtime/raw schemas, shared immutable finalization/loading, and
idle admission dispatch are implemented. The earlier active and idle paths were
live-verified; the current simplified semantic contracts have not yet been
re-run on the live cluster.

The firstfloor readiness probe connects with a 1-second timeout. Before the
fix, that timeout stayed on the kept-alive socket, so any measured call slower
than 1 second failed with a socket timeout. The probe connection now closes
after readiness so measurement reconnects without a timeout. Every firstfloor
runtime variant is rebuilt with this fix and the existing mobilenet profiles
are re-measured: images built before it can't run slow calls, and one image
set across all variants keeps the profiles comparable.

## Model-loaded-idle live verification

On 2026-08-03, `idle-model-loaded-live-ad7b84f-v2` completed on Lattepanda and
was `Accepted` by `idle-wall-power-v1`. The finalized evidence retained the raw
object's configured store, opaque key, and bounded size. This historical Run
predates the current schema and is evidence for the method, not live proof of
the current wire contract.

The runtime evidence records model load before a 60-second settling interval,
a 30-second monotonic measurement window, zero inference after model load, and
model residency through the window. The metric result retained 31 successful
power samples with no failed reads, and workload observation remained
continuous with no entered, updated, or exited Pod. The Run reached
`ExecutionComplete=True`, `EvidenceVerified=True`, and
`EvidenceAdmitted=True`.

All three regular containers strictly decode the same RunConfig supplied by the
Job. Each process validates that input at its boundary; the runtime worker then
carries `profilingrun.VerifiedRunConfig` instead of accepting parallel mutable
configuration inputs.
Raw-result composition remains the separate boundary that correlates runtime
and observer facts. Delivery
therefore deploys runtime-worker, MetricObserver, and WorkloadObserver images
as one tested schema-compatible set. Each image remains an independently pinned
OCI reference; this rule does not merge their responsibilities.

The metric and workload result-file readers validate their respective records
once after the atomic file boundary and return sealed values. Runtime adapters
do the same after strict process-result decoding and runtime-specific contract
checks. The coordinator passes those sealed values into raw composition; it
does not repeat their boundary validations or mutate them.
The evidence composer accepts only the verified RunConfig, runtime, metric, and
workload value types owned by their source packages. It checks only new
cross-record relationships such as source identity and window coverage, then
canonicalizes the raw record without re-validating the nested values.

The validated image set ran `active-regression-live-ad7b84f` through the active
protocol. It
completed 5,434 inference calls, retained 31 successful power samples with no
failed reads and continuous workload observation, and was `Accepted` by
`wall-power-v2`. This verifies that the idle-compatible delivery set preserves
the active producer, finalization, and admission path.

This verification covers the Profiler Run, Job, immutable evidence, and
admission lifecycle. It does not extend the active-only Operator Console
Evidence Passport projection; requesting a passport for this idle Run remains
a separate follow-on boundary.

## Purpose

The Profiler controller has resolved one exact RunConfig for the `ProfilingRun`. The
measurement Job must now answer one bounded question:

> Did the selected runtime execute the RunConfig-defined measurement on the selected Pod
> while the selected metric source was observed, and were those raw facts
> committed immutably?

The Job produces raw facts only. The controller independently verifies and
finalizes them after Kubernetes reports the Job and Pod terminal state.

## First Job topology

The first Job contains four execution roles, not a generic plugin graph:

```text
artifact init container
  -> copy the declared artifact path
  -> expose it as a regular file at the runtime-specific destination

runtime container (the measurement worker and sole raw writer)
  -> verify RunConfig and artifact
  -> run the firstfloor runtime adapter
  -> coordinate the measurement window
  -> validate MetricObserver and WorkloadObserver results
  -> assemble and immutably store RawMeasurement
  -> write the bounded success receipt

metric-observer container
  -> wait for the runtime measurement signal
  -> invoke internal/metricobserver for one bounded window
  -> observe measurement-end, atomically write only the metric result, and exit

workload-observer container
  -> establish a continuous target-Node Pod list-watch
  -> close only after the same watch observes the metric-observer exit fence
  -> atomically write only the workload result
```

There is no separate uploader container. It would introduce another image,
handoff, terminal state, and failure owner without removing responsibility from
the runtime worker. The MetricObserver does not receive storage credentials and
cannot publish Run evidence.

The `runtime` image is the complete trusted worker/runtime environment for the
selected path. It contains the CHILL worker entrypoint, the firstfloor actor and
runner, and their ORT and RKNNLite contract helpers. The separately pinned
`metricObserver` image contains the bounded observer helper. A separately
pinned WorkloadObserver image receives the only
Kubernetes API credential in the Job. The trusted execution declaration must
therefore reference the worker/runtime image selected for the Run. The artifact
image remains an init-container supply image.

## RunConfig and delivery inputs

The exact RunConfig remains the scientific execution authority. The Job passes
the same canonical JSON once to each regular container through `--run-config`;
there is no auxiliary per-Run ConfigMap or mounted compatibility file.
Candidate compatibility is resolved before Job creation, and only the
runtime material needed for execution is copied into `RunConfig.delivery`.
Selected policy payloads remain controller-side Job provenance and are not
worker inputs.

The Job materializer consumes this resolved execution fact:

- `profilingrun.ExecutionState.ArtifactPath`, the artifact source path inside
  the artifact supply image.

It is resolved from the already trusted artifact declaration and recorded in the
RunConfig. The destination path inside the shared model volume belongs to the
runtime adapter (`model.onnx` for ORT backends and `model.rknn` for the RKNNLite
backend) and is not caller-selectable. RKNNLite additionally projects only the
verified report's exact host library, driver-version, platform-compatible, and
device paths. File facts are read-only, device nodes remain writable, runtime
container privilege and host mounts apply only to that container, and an
extended resource is requested only when the report declares one. Kubernetes
supplemental groups are necessarily Pod-level, but observers receive no device
mount or privilege.

The Go worker verifies the projected candidate report once before constructing
the runtime adapter. The firstfloor runner and the RKNNLite helpers in
`runtime/rknnlite/runner.py` then own RKNNLite-specific artifact,
image-package, host-library, driver, platform, character-device, core-mask,
initialization, and inference checks; they do not reuse ONNX Runtime provider
vocabulary. `runtime/firstfloor/measurement.py` compares the actual runtime
facts with the delivered host declarations before raw evidence construction.

Active and idle Runs use only the current RunConfig schema. The optional
physical host contract is represented as semantic library, driver, platform,
device, and allocation facts. The optional typed actuation contract contains
the non-fixed mode, exact actuator target, and configured policy name; fixed
Runs omit it. An actuated Run is withheld before Job creation until the durable
actuation handoff exists. The measurement protocol includes settling duration
and reuses the same RunConfig type and common execution provenance rather than
creating a parallel execution type. Go and Python accept only their matching
current request and RunConfig schemas.

The runtime worker and both observers consume that canonical RunConfig through
strict decoders. Delivery configuration must therefore select images that
support the same current RunConfig schema. Compatibility is established at
image deployment time, while each image remains an exact OCI reference in the
RunConfig and each process keeps the responsibility described below.

The following values are delivery bindings, not scientific Run inputs:

- logical Evidence Store identity;
- internal Service endpoint, bucket, and region;
- immutable credentials Secret reference; and
- deterministic raw-object key.

The Profiler receives the owning `ChillSystem` name at startup. At Job creation
it exact-reads that system's enabled Evidence Store reference and configuration,
exact-reads the system-owned Evidence Store Service and binds its allocated
ClusterIP and S3 port (not cluster DNS, which hostNetwork Jobs cannot resolve),
derives the immutable credentials Secret reference, and never reads the
credential bytes. It requires the current ChillSystem
availability condition and a completed, operator-owned bootstrap Job attesting
the exact configuration ConfigMap `resourceVersion`. It does not reconcile or
directly probe the storage operand.

The resolved binding is retained in the immutable Job PodSpec through literal
store, endpoint, bucket, and region values plus Secret references. Finalization
reconstructs this binding from the recorded Job rather than silently switching
an in-flight Run to newer store configuration. These values never enter
`ProfilingRun.spec` or the resolved RunConfig.
Endpoint, bucket, region, and Secret data remain outside status and container
logs; only the logical store and an opaque finalized-evidence key may later
appear in the compact status reference. A Run cannot supply or override them.

The initial object keys are fixed:

```text
profiling-runs/<Run UID>/raw.json
profiling-runs/<Run UID>/execution.json
```

The logical store identity is the CHILL Evidence Store Service name, not its
endpoint. Two Run objects therefore remain distinct trials even when their
scientific RunConfig is otherwise identical.

## Job-local handoff

The Job uses four base volumes:

- an `emptyDir` model volume populated by the artifact init container;
- an `emptyDir` coordination volume shared by the three regular containers;
- a bounded runtime-workspace `emptyDir` mounted only by the runtime worker; and
- a projected Kubernetes token and CA volume mounted only by WorkloadObserver.

An accelerator path appends one exact `hostPath` volume per verified host fact;
it never mounts a whole device directory or exposes those volumes to either
observer.

The runtime workspace holds the private firstfloor result and bounded temporary
file used to verify evidence before upload. It is not a cross-container
protocol or retained evidence location.

The coordination volume contains only this versioned, bounded protocol:

```text
observer-ready       metric-observer is ready to receive a start signal
workload-observer-ready  target-Node list-watch continuity is established
measurement-start    runtime completed warm-up and opens the measurement window
measurement-end      runtime completed its final measured inference
metric-result.json    complete MetricObserver Result, written by temp-file + rename
workload-result.json  complete WorkloadObserver Result, written atomically
runtime-failed       runtime failed and is exiting; observers stop and exit failed
```

For `idle-model-loaded`, the same signals retain the same ownership but have
mode-specific preconditions: `measurement-start` means exact model load and the
full RunConfig settling interval completed with no inference; `measurement-end`
means the monotonic duration completed with no inference and the model still
resident. CPU ORT keeps the canonical idle batch of one. A runtime whose
artifact is batch-fixed instead requires the idle RunConfig batch to equal that
artifact capability; the value identifies what is resident and does not imply
an idle-window inference. The runtime creates both signals without reading
metric or workload results. Observers can therefore prove coverage or fail
closed, but cannot derive a preferred window from the values they observe.

Logs are diagnostic only. They are never parsed as evidence or used as a
controller handoff. A partial metric-result file is never visible under the
final filename.

The runtime sequence is fixed:

1. strict-decode and validate the supplied RunConfig;
2. verify Pod UID, scheduled Node name, artifact file, CPU contract, runtime,
   provider, and output-validation capability;
3. wait with a bounded timeout for both observer readiness signals;
4. execute active warm-up, or for idle retain the loaded model for the full
   observer-independent settling duration without inference;
5. atomically create `measurement-start`;
6. execute the one duration-based measurement;
7. atomically create `measurement-end` after the final active inference, or
   after the idle duration completes with zero inference and verified session
   residency;
8. wait with bounded timeouts for both result files;
9. validate metric source and workload identities, summaries, timestamps, and
   coverage of the runtime measurement window;
10. encode the mode-specific active or idle raw measurement, record its bounded
    size, and perform one immutable create; and
11. write the validated Evidence Store receipt to `/dev/termination-log` and
    exit successfully.

On any failure the runtime atomically creates `runtime-failed` before it exits
with its failure class. Both observers watch for that signal from startup and
end every wait at once, so a failed attempt releases the Node within seconds.

The MetricObserver sequence is fixed:

1. strict-decode and validate the same RunConfig;
2. construct the edge-metrics source from the RunConfig power contract;
3. atomically create `observer-ready`;
4. wait with a bounded timeout for `measurement-start`;
5. observe for the RunConfig power duration;
6. wait with a bounded timeout for `measurement-end`; and
7. atomically publish `metric-result.json` and exit successfully. Therefore its
   successful Pod container status is causally after runtime closed the window.

The WorkloadObserver sequence is fixed:

1. strict-decode the RunConfig and verify its own Pod and Job ownership;
2. establish the target-Node Pod list-watch and publish its readiness signal;
3. retain bounded membership changes until `measurement-end`;
4. continue the same watch until the Run Pod reports successful termination of
   `metric-observer`, the causal post-end status fence; and
5. atomically publish `workload-result.json` and exit.

## Raw evidence and receipt

The current active raw-measurement object contains only facts available before
the worker exits:

- the validated RunConfig and actual Pod UID;
- the existing validated runtime result;
- the complete MetricObserver result; and
- the complete bounded WorkloadObserver result.

Model-loaded idle uses its own current raw schema because its runtime
completeness proof is exact model load, settling
completion, zero inference, retained residency, and a
runtime-declared duration rather than active latency and output-validation
evidence. Both raw schemas retain the same validated RunConfig, actual Pod UID,
MetricObserver result, WorkloadObserver result, and immutable store/key/size
reference boundary.

It contains no Job completion, terminal Pod state, or post-run Node snapshot,
credentials, or backend response. Those Kubernetes facts are added only by the
Profiler controller during independent finalization.

Exit code zero is permitted only after immutable storage succeeds and the
receipt is written. The success termination message contains exactly one
`evidence-store-receipt.v1alpha1` JSON object and stays within Kubernetes' 4 KiB
termination-message bound. A receipt is a handoff hint, not proof; the
controller retrieves and verifies the object independently.

The first worker uses stable failure classes:

| Exit code | Meaning |
|---:|---|
| 2 | invalid or drifted RunConfig/delivery contract |
| 3 | artifact, runtime, provider, inference, or output validation failure |
| 4 | observer readiness, execution, handoff, or result validation failure |
| 5 | raw evidence encoding, immutable write, or post-write verification failure |

Failures write diagnostics to stderr and leave `/dev/termination-log` empty.
The runtime container uses `FallbackToLogsOnError`, so Kubernetes fills its
termination message with the last 80 lines or 2 KiB of the container log. When
the Job fails, the controller copies the runtime exit code and that message
once into `ProfilingRun.status.runtimeTermination`, which keeps the failure
cause after the Pod is deleted. The copy is a diagnostic; the controller never
parses it. Kubernetes remains the authority for the native container and Job
failure state.

Observer images belong to the measurement-protocol identity, and the runtime
image, which carries the worker, belongs to the execution-state identity. A Run
resolved with a rebuilt observer or worker image therefore receives new
identities, while profiles derived from earlier evidence keep their recorded
identities and stay valid. A curve plan pairs active and idle Runs only under
one runtime image and one protocol each, so a campaign and its idle baseline
use one image set.

## Kubernetes execution contract

The controller creates one deterministic Job owned by the `ProfilingRun`:

- `parallelism=1`, `completions=1`, and `backoffLimit=0`;
- `restartPolicy=Never` and no replacement Job for the same Run;
- `automountServiceAccountToken=false`;
- Pod UID and scheduled Node name injected from the Downward API;
- exact target-Node placement and configured CPU request/limit;
- immutable OCI references for artifact, runtime, MetricObserver, and WorkloadObserver images;
- restricted container security contexts; and
- one bounded active deadline covering startup, warm-up, measurement, handoff,
  and evidence commit.

Deadline and handoff margins are versioned installation policy for this worker,
not Run knobs. Helm provides the default measured envelope. Before Job creation,
the Profiler selects it by RunConfig DeviceClass, execution path, and workload
mode and copies the exact canonical policy and ConfigMap provenance into the
owned Job. The Job name is derived from the Run UID, so deleting or
losing a recorded Job cannot be mistaken for permission to repeat the
measurement.

Job composition supplies each process with its handoff wait budget from the
same bounded attempt timing contract. Runtime and MetricObserver entrypoints
have no hidden default wait duration, and `ProfilingRun.spec` cannot override
these delivery bindings.

Job composition also supplies bounded producer capacities. The MetricObserver
must admit the complete duration/interval polling schedule within one combined
sample-and-failure event cap before polling begins, then enforce that cap while
recording. The firstfloor runtime must reject an input tensor before allocation if
its calculated bytes exceed the supplied input budget, cap retained latency
samples during measurement, and reject a serialized runtime result that
exceeds its result-byte budget. These capacities are versioned worker delivery
bindings derived from the bounded execution resources; they are not scientific
Run knobs and do not add fields to `ProfilingRun.spec`.

### Resource-capacity envelope

Job materialization must derive container resources, handoff deadlines, and
producer capacities through one versioned worker policy. It must not assemble
independent positive numbers that can only be discovered to conflict after a
large in-memory result has been built. The immutable Run record retains the
exact selected policy and source, while the immutable Job retains the projected
native resource requirements and content-derived policy identity. CR status
does not copy these implementation details.

The first policy must enforce these relationships:

- the MetricObserver event capacity covers the complete configured polling schedule;
- the independent handoff byte limit rejects an oversized result before raw
  evidence is committed;
- the runtime input ceiling leaves a separately measured reserve for the model,
  runtime session, outputs, bounded input-generation temporary, and worker;
- the latency-sample capacity and runtime-result byte capacity fit both the
  runtime container memory limit and the immutable handoff; and
- the runtime workspace size covers the private runtime result and one bounded
  evidence candidate without relying on a writable image layer; and
- the common attempt deadline covers artifact preparation, model and input
  preparation, observer synchronization, warm-up, measurement, handoff, and
  evidence commit.

Capacity is derived from resources and the configured duration, never from an
expected model throughput: using a predicted inference rate would make faster
measurements fail for reasons derived from the profile they are supposed to
produce. When the artifact-derived input shape exceeds the admitted ceiling,
or a duration cannot retain its complete latency distribution, the worker
fails explicitly. It never truncates, samples, or silently aggregates the raw
distribution to fit a budget.

Numeric reserves and serialized-byte coefficients are not fixed by this
document. They require measurements from the exact pinned worker/runtime
image and become defaults in a versioned installation policy. Changing those
values affects only newly resolved Runs and changes the content-derived execution
identity. A later runtime may define its own policy; this first contract does
not introduce a generic resource-planner framework.

## Responsibility and dependency boundary

```text
Profiler controller
  -> materializes and later observes native objects
  -> never executes inference or polls power

runtime worker
  -> firstfloor runtime adapter
  -> internal/profiling/profiler/evidence raw codec
  -> internal/evidencestore + S3 adapter

metric-observer helper
  -> internal/metricobserver
  -> edge-metrics adapter

workload-observer helper
  -> internal/workloadobserver
  -> read-only Kubernetes Pod list-watch adapter
```

No Job container imports controller-runtime or reconciles Kubernetes objects.
Only WorkloadObserver receives the projected read-only Kubernetes API token.
The controller does not trust shared-volume files after Pod termination; it
trusts only independently read and verified immutable evidence plus native
Kubernetes observations.

## Implementation order

1. Record the artifact supply path in RunConfig from the candidate projection.
2. Add a pure worker coordinator with fake runtime, observer-result, and store
   ports; keep Kubernetes out.
3. Implement the CPU ORT runtime entrypoint and file-based MetricObserver
   handoff within the worker and adapter package boundaries.
4. Add the CHILL-managed Evidence Store delivery binding to Profiler startup
   configuration and Job projection.
5. Add deterministic Job materialization with its coherent resource-capacity
   envelope and status reference publication.
6. Connect terminal observation to the existing evidence finalizer.
7. Verify active and model-loaded-idle Lattepanda Runs through the worker,
   immutable store, finalizer, loader, and admission path.

Items 1 through 7 are implemented. The worker composes RunConfig validation,
the runtime adapter, bounded observer handoffs, immutable Evidence Store
writer, stable failure exits, and bounded termination receipt. The final live
active and idle paths are verified. This worker contract does not independently
own the public request schema; Plan 4 owns the exposed mode and validation
fields.
