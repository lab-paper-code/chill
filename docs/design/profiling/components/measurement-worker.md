# Measurement Worker Contract

> Navigation: [Profiling design index](../README.md)

## Status

Implemented and live-verified for the first CPU ONNX Runtime profiling path.
This document specifies the Job-local handoff required by Plan 4. It does not
authorize batch campaigns, `b_sat` derivation, another runtime, or serving.

## Purpose

The Profiler controller has frozen one exact `ProfilingRun` intent. The
measurement Job must now answer one bounded question:

> Did the selected runtime execute the frozen measurement on the selected Pod
> while the selected metric source was observed, and were those raw facts
> committed immutably?

The Job produces raw facts only. The controller independently verifies and
finalizes them after Kubernetes reports the Job and Pod terminal state.

## First Job topology

The first Job contains three execution roles, not a generic plugin graph:

```text
artifact init container
  -> copy the declared artifact path
  -> verify the frozen artifact digest

runtime container (the measurement worker and sole raw writer)
  -> verify intent and artifact
  -> run the explicit CPU ONNX Runtime adapter
  -> coordinate the measurement window
  -> validate the MetricObserver result
  -> assemble and immutably store RawMeasurement
  -> write the bounded success receipt

metric-observer container
  -> wait for the runtime measurement signal
  -> invoke internal/metricobserver for one bounded window
  -> atomically write only the metric result
```

There is no separate uploader container. It would introduce another image,
handoff, terminal state, and failure owner without removing responsibility from
the runtime worker. The MetricObserver does not receive storage credentials and
cannot publish Run evidence.

The `runtime` image is the complete trusted worker/runtime environment for the
selected path. It contains the CHILL worker entrypoint and the explicit CPU ORT
adapter. The separately pinned `metricObserver` image contains the bounded
observer helper. The trusted execution declaration must therefore reference a
promoted worker/runtime image; the spike runtime image is not itself a
production contract. The artifact image remains an init-container supply image.

## Frozen and delivery inputs

The immutable intent record remains the scientific execution authority. Both
regular containers mount its canonical `intent.json` read-only and verify the
expected digest before doing work. The candidate report remains controller-side
provenance and is not a worker input.

One missing execution fact must be added before Job materialization:

- `profilingrun.ExecutionState.ArtifactPath`, the artifact source path inside
  the artifact supply image.

It is resolved from the already trusted artifact declaration and frozen in the
intent. The destination path inside the shared model volume belongs to the
versioned CPU ORT worker contract and is not caller-selectable.

The current internal intent schema is `v1alpha2` and requires this path.
Retained `v1alpha1` intents remain readable without it so their canonical bytes
and digests do not change; new Runs are never emitted as `v1alpha1`.

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

The Job uses four volumes:

- a read-only intent ConfigMap volume;
- an `emptyDir` model volume populated by the artifact init container; and
- an `emptyDir` coordination volume shared by the two regular containers; and
- a bounded runtime-workspace `emptyDir` mounted only by the runtime worker.

The runtime workspace holds the private CPU ORT result and bounded temporary
file used to verify evidence before upload. It is not a cross-container
protocol or retained evidence location.

The coordination volume contains only this versioned, bounded protocol:

```text
observer-ready       metric-observer is ready to receive a start signal
measurement-start    runtime completed warm-up and opens the measurement window
metric-result.json    complete MetricObserver Result, written by temp-file + rename
```

Logs are diagnostic only. They are never parsed as evidence or used as a
controller handoff. A partial metric-result file is never visible under the
final filename.

The runtime sequence is fixed:

1. strict-decode and validate the mounted intent and expected digest;
2. verify Pod UID, scheduled Node name, artifact bytes, CPU contract, runtime,
   provider, and output-validation capability;
3. wait with a bounded timeout for `observer-ready`;
4. execute the declared warm-up iterations;
5. atomically create `measurement-start`;
6. execute the one duration-based measurement;
7. wait with a bounded timeout for `metric-result.json`;
8. validate source identity, polling summary, timestamps, and overlap with the
   runtime measurement window;
9. canonicalize `RawMeasurement`, calculate its digest and size, and perform
   one immutable create; and
10. write the validated Evidence Store receipt to `/dev/termination-log` and
    exit successfully.

The observer sequence is fixed:

1. strict-decode and validate the same intent;
2. construct the edge-metrics source from the frozen power contract;
3. atomically create `observer-ready`;
4. wait with a bounded timeout for `measurement-start`;
5. observe for the frozen power duration; and
6. atomically publish `metric-result.json` and exit.

## Raw evidence and receipt

The `profiling-raw-measurement.v1alpha2` object is the worker output.
It contains only facts available before the worker exits:

- intent digest and actual Pod UID;
- the existing validated runtime result; and
- the complete MetricObserver result.

It contains no Job completion, terminal Pod state, co-resident Pod snapshot,
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
| 4 | MetricObserver readiness, execution, handoff, or result validation failure |
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
- digest-pinned artifact, runtime, and MetricObserver images;
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
```

Neither Job container imports controller-runtime, reconciles Kubernetes
objects, or receives a Kubernetes API token. The controller does not trust
shared-volume files after Pod termination; it trusts only independently read
and verified immutable evidence plus native Kubernetes observations.

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

Items 1 through 3 are implemented through the fail-closed worker adapters, the
production CPU ORT subprocess, and the MetricObserver container entrypoint. The
Go adapter's exact runtime request is checked against the production Python
strict decoder as a cross-language contract test. The Operator now supplies
the owning `ChillSystem` name to each Profiler instance. A fail-closed
Kubernetes resolver can exact-read that system and its configured Evidence
Store into a non-secret delivery binding. Its Job projection and Job
materialization remain later steps. The production CPU ORT measurement-worker
image composes the frozen-intent loader, runtime adapter, file-based power
handoff, immutable S3 writer, coordinator, stable failure exits, bounded
termination receipt, and pinned Python runtime dependencies. This changes no
public CRD field and does not create a Job.
