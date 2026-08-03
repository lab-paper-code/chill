# Measurement Worker Contract

> Navigation: [Profiling design index](../README.md)

## Status

Implemented for the first CPU ONNX Runtime profiling path. The causal
metric/workload end-fence success path was live-verified with three sequential,
same-intent Runs admitted by `wall-power-v2`. This document does not authorize
batch campaigns, `b_sat` derivation, another runtime, or serving.

## Purpose

The Profiler controller has frozen one exact `ProfilingRun` intent. The
measurement Job must now answer one bounded question:

> Did the selected runtime execute the frozen measurement on the selected Pod
> while the selected metric source was observed, and were those raw facts
> committed immutably?

The Job produces raw facts only. The controller independently verifies and
finalizes them after Kubernetes reports the Job and Pod terminal state.

## First Job topology

The first Job contains four execution roles, not a generic plugin graph:

```text
artifact init container
  -> copy the declared artifact path
  -> verify the frozen artifact digest

runtime container (the measurement worker and sole raw writer)
  -> verify intent and artifact
  -> run the explicit CPU ONNX Runtime adapter
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
selected path. It contains the CHILL worker entrypoint and the explicit CPU ORT
adapter. The separately pinned `metricObserver` image contains the bounded
observer helper. A separately pinned WorkloadObserver image receives the only
Kubernetes API credential in the Job. The trusted execution declaration must
therefore reference a promoted worker/runtime image; the spike runtime image
is not itself a production contract. The artifact image remains an
init-container supply image.

## Frozen and delivery inputs

The immutable intent record remains the scientific execution authority. All
regular containers mount its canonical `intent.json` read-only and verify the
expected digest before doing work. The candidate report remains controller-side
provenance and is not a worker input.

One missing execution fact must be added before Job materialization:

- `profilingrun.ExecutionState.ArtifactPath`, the artifact source path inside
  the artifact supply image.

It is resolved from the already trusted artifact declaration and frozen in the
intent. The destination path inside the shared model volume belongs to the
versioned CPU ORT worker contract and is not caller-selectable.

The implemented active path uses internal intent schema `v1alpha5`; retained
earlier intents remain readable without changing their canonical bytes or
digests. The model-loaded-idle revision advances new intents to `v1alpha6` and
adds the frozen measurement-protocol settling duration. It reuses the same
intent type and common execution provenance rather than creating a parallel
frozen type.

The following values are delivery bindings, not scientific Run inputs:

- logical Evidence Store identity;
- internal Service endpoint, bucket, and region;
- immutable credentials Secret reference; and
- deterministic raw-object key.

The Profiler receives the owning `ChillSystem` name at startup. At Job creation
it exact-reads that system's enabled Evidence Store reference and configuration,
derives the internal Service FQDN and immutable credentials Secret reference,
and never reads the credential bytes. It requires the current ChillSystem
availability condition and a completed, operator-owned bootstrap Job attesting
the exact configuration digest. It does not reconcile or directly probe the
storage operand.

The resolved binding is retained in the immutable Job PodSpec through literal
store, endpoint, bucket, and region values plus Secret references. Finalization
reconstructs this binding from the recorded Job rather than silently switching
an in-flight Run to newer store configuration. These values never enter
`ProfilingRun.spec` or the frozen intent.
Endpoint, bucket, region, and Secret data remain outside status and container
logs; only the logical store and an opaque finalized-evidence key may later
appear in the compact status reference. A Run cannot supply or override them.

The initial object keys are fixed:

```text
profiling-runs/<Run UID>/raw/<intent SHA-256 hex>.json
profiling-runs/<Run UID>/execution/<intent SHA-256 hex>.json
```

The logical store identity is the CHILL Evidence Store Service name, not its
endpoint. Two Run objects therefore remain distinct trials even when their
scientific intent is otherwise identical.

## Job-local handoff

The Job uses five volumes:

- a read-only intent ConfigMap volume;
- an `emptyDir` model volume populated by the artifact init container;
- an `emptyDir` coordination volume shared by the three regular containers;
- a bounded runtime-workspace `emptyDir` mounted only by the runtime worker; and
- a projected Kubernetes token and CA volume mounted only by WorkloadObserver.

The runtime workspace holds the private CPU ORT result and bounded temporary
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
```

For `idle-model-loaded`, the same signals retain the same ownership but have
mode-specific preconditions: `measurement-start` means exact model load and the
full frozen settling interval completed with no inference; `measurement-end`
means the monotonic duration completed with no inference and the model still
resident. The runtime creates both signals without reading metric or workload
results. Observers can therefore prove coverage or fail closed, but cannot
derive a preferred window from the values they observe.

Logs are diagnostic only. They are never parsed as evidence or used as a
controller handoff. A partial metric-result file is never visible under the
final filename.

The runtime sequence is fixed:

1. strict-decode and validate the mounted intent and expected digest;
2. verify Pod UID, scheduled Node name, artifact bytes, CPU contract, runtime,
   provider, and output-validation capability;
3. wait with a bounded timeout for both observer readiness signals;
4. execute the declared warm-up iterations;
5. atomically create `measurement-start`;
6. execute the one duration-based measurement;
7. atomically create `measurement-end` after the final measured inference;
8. wait with bounded timeouts for both result files;
9. validate metric source and workload identities, summaries, timestamps, and
   coverage of the runtime measurement window;
10. canonicalize `RawMeasurement`, calculate its digest and size, and perform
   one immutable create; and
11. write the validated Evidence Store receipt to `/dev/termination-log` and
    exit successfully.

The MetricObserver sequence is fixed:

1. strict-decode and validate the same intent;
2. construct the edge-metrics source from the frozen power contract;
3. atomically create `observer-ready`;
4. wait with a bounded timeout for `measurement-start`;
5. observe for the frozen power duration;
6. wait with a bounded timeout for `measurement-end`; and
7. atomically publish `metric-result.json` and exit successfully. Therefore its
   successful Pod container status is causally after runtime closed the window.

The WorkloadObserver sequence is fixed:

1. strict-decode the intent and verify its own Pod and Job ownership;
2. establish the target-Node Pod list-watch and publish its readiness signal;
3. retain bounded membership changes until `measurement-end`;
4. continue the same watch until the Run Pod reports successful termination of
   `metric-observer`, the causal post-end status fence; and
5. atomically publish `workload-result.json` and exit.

## Raw evidence and receipt

The `profiling-raw-measurement.v1alpha3` object is the worker output.
It contains only facts available before the worker exits:

- intent digest and actual Pod UID;
- the existing validated runtime result;
- the complete MetricObserver result; and
- the complete bounded WorkloadObserver result.

That schema remains the retained active output. Model-loaded idle uses a
separate idle raw schema because its runtime completeness proof is exact model
load, settling completion, zero inference, retained residency, and a
runtime-declared duration rather than active latency and output-validation
evidence. Both raw schemas retain the same intent digest, actual Pod UID,
MetricObserver result, WorkloadObserver result, and immutable object-identity
verification boundary.

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
| 2 | invalid or drifted frozen/delivery contract |
| 3 | artifact, runtime, provider, inference, or output validation failure |
| 4 | observer readiness, execution, handoff, or result validation failure |
| 5 | raw evidence encoding, immutable write, or post-write verification failure |

Failures write diagnostics to stderr but never place credentials, raw samples,
or a success-shaped receipt in the termination message. Kubernetes remains the
authority for the native container and Job failure state.

## Kubernetes execution contract

The controller creates one deterministic Job owned by the `ProfilingRun`:

- `parallelism=1`, `completions=1`, and `backoffLimit=0`;
- `restartPolicy=Never` and no replacement Job for the same Run;
- `automountServiceAccountToken=false`;
- Pod UID and scheduled Node name injected from the Downward API;
- exact target-Node placement and frozen CPU request/limit;
- digest-pinned artifact, runtime, MetricObserver, and WorkloadObserver images;
- restricted container security contexts; and
- one bounded active deadline covering startup, warm-up, measurement, handoff,
  and evidence commit.

Deadline and handoff margins are versioned implementation constants for this
first worker, not new user knobs. The Job name is derived from the Run UID, so
deleting or losing a recorded Job cannot be mistaken for permission to repeat
the measurement.

Job composition supplies each process with its handoff wait budget from the
same bounded attempt timing contract. Runtime and MetricObserver entrypoints
have no hidden default wait duration, and `ProfilingRun.spec` cannot override
these delivery bindings.

Job composition also supplies bounded producer capacities. The MetricObserver
must admit the complete duration/interval polling schedule within one combined
sample-and-failure event cap before polling begins, then enforce that cap while
recording. The CPU ORT runtime must reject an input tensor before allocation if
its calculated bytes exceed the supplied input budget, cap retained latency
samples during measurement, and reject a serialized runtime result that
exceeds its result-byte budget. These capacities are versioned worker delivery
bindings derived from the bounded execution resources; they are not scientific
Run knobs and do not add fields to `ProfilingRun.spec`.

### Resource-capacity envelope

Job materialization must derive container resources, handoff deadlines, and
producer capacities through one versioned worker policy. It must not assemble
independent positive numbers that can only be discovered to conflict after a
large in-memory result has been built. The immutable Job retains the native
resource requirements and policy identity needed to reconstruct that delivery
decision; CR status does not copy these implementation details.

The first policy must enforce these relationships:

- the MetricObserver event capacity covers the complete frozen polling schedule;
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

Capacity is derived from resources and the frozen duration, never from an
expected model throughput: using a predicted inference rate would make faster
measurements fail for reasons derived from the profile they are supposed to
produce. When the artifact-derived input shape exceeds the admitted ceiling,
or a duration cannot retain its complete latency distribution, the worker
fails explicitly. It never truncates, samples, or silently aggregates the raw
distribution to fit a budget.

Numeric reserves and serialized-byte coefficients are not fixed by this
document. They require measurements from the exact promoted worker/runtime
image and become implementation constants of that versioned path. A later
runtime may define its own policy; this first contract does not introduce a
generic resource-planner framework.

## Responsibility and dependency boundary

```text
Profiler controller
  -> materializes and later observes native objects
  -> never executes inference or polls power

runtime worker
  -> explicit CPU ORT adapter
  -> internal/profiler/evidence raw codec
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

1. Freeze the artifact supply path in the internal intent and candidate
   projection.
2. Add a pure worker coordinator with fake runtime, observer-result, and store
   ports; keep Kubernetes out.
3. Promote the CPU ORT runtime entrypoint and file-based MetricObserver handoff
   from the spike without importing spike code.
4. Add the CHILL-managed Evidence Store delivery binding to Profiler startup
   configuration and Job projection.
5. Add deterministic Job materialization with its coherent resource-capacity
   envelope and status reference publication.
6. Connect terminal observation to the existing evidence finalizer.
7. Compare one live Lattepanda Run byte-for-byte and fact-for-fact with the
   retained spike evidence before removing any spike path.

Items 1 through 6 are implemented. The worker composes the frozen-intent
loader, runtime adapter, bounded observer handoffs, immutable Evidence Store
writer, stable failure exits, and bounded termination receipt. The final live
comparison remains pending and no public CRD field is added.
