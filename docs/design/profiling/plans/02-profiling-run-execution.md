# Plan 2: Immutable Profiling Run and Execution Evidence

> Navigation: [Profiling design index](../README.md)

## Status

Implemented and live-verified for the first CPU ONNX Runtime active path and
for the same Job's model-loaded-idle producer branch. This plan does not
authorize profile admission, energy derivation, `b_sat`, or `DeviceProfile`
status writes.

## Question

> Did Kubernetes and the selected runtime realize one exact profiling decision,
> and what raw execution and power evidence was observed?

One Run is one selected batch, one Job, and exactly one Pod attempt. A campaign
is a collection of Runs and is not introduced as an API by this plan.

## Checkpoint 1: immutable Run intent

The first Run intent freezes:

- the static candidate report input identities and selected model path;
- logical model, artifact digest, runtime family, and backend;
- digest-pinned runtime, artifact-carrier, and MetricObserver images;
- selected batch, warm-up, bounded measurement duration, and repetitions;
- target Node name, UID, resourceVersion, architecture, and allocatable CPU;
- the CPU derivation policy and its material inputs and outputs; and
- the resolved metric source and bounded observation request.

The intent identity is the SHA-256 of canonical JSON. It is not a Job, Pod, or
Kubernetes UID. A changed snapshot or decision creates a different intent.

The model-loaded-idle extension remains one `profilingrun.Intent`; it does not
introduce a second frozen-intent type. Candidate, execution state, images, Node,
CPU, metric source, and workload-observation provenance are common execution
facts, and duplicating them would create two validation and finalization paths.
The intent schema is `v1alpha8`. It retains the measurement-protocol
`settlingDurationSeconds` field and the optional digest of only the
order-normalized physical host contract. CPU ORT omits that state field.
The common CPU quota remains frozen for all workers, while RKNNLite omits the
ORT-only intra/inter-op settings. Retained v1alpha7 and v1alpha6 CPU intent
bytes remain readable and are protected by their original golden digests;
v1alpha6 cannot carry the host-contract field and RKNNLite is current-only.

Intent validation is mode-aware. The active mode retains a positive batch,
declared warm-up, input generation, output validation, and measured inference
calls. Model-loaded idle uses `warmupIterations=0`,
`workloadMode=idle-model-loaded`, `inputGenerator=none`, and
`outputValidation=none`, plus a positive settling duration. CPU ORT retains the
canonical `batch=1`; a fixed-batch derived runtime instead freezes the loaded
artifact's positive batch capability even though the idle window issues no
inference. These are frozen protocol facts, not delivery defaults.

The first CPU policy is deliberately concrete: under the existing one-serving-
Pod-per-Node profiling assumption, integer allocatable CPU becomes the runtime
limit and ORT intra-op thread count; inter-op is one. CPU scheduling request is
a separate explicit Run input because existing system Pods prevent full-node
reservation; it does not imply exclusivity. The policy name, version, input
snapshot, and result are all frozen so this is not a hidden magic number.

## Checkpoint 2: Kubernetes materialization

A one-shot CPU ORT adapter translates the intent into native objects. The Job:

- has `parallelism=1`, `completions=1`, `backoffLimit=0`, and `restartPolicy=Never`;
- disables service-account token mounting;
- uses digest-pinned images and `IfNotPresent` pull policy;
- expresses placement as a hostname selector and records actual binding later;
- requests and limits the frozen CPU amount for the runtime container;
- copies the digest-pinned artifact into an `emptyDir` and verifies its bytes;
- runs the runtime and MetricObserver in separate containers; and
- carries the Run intent digest as a label/annotation and environment input.

Plan 2 does not claim that selection intent guarantees scheduling. It records
Kubernetes admission, placement, image, and container outcomes as evidence.

The public `ProfilingRun` CRD and controller that expose this execution mode
belong to Plan 4. This Plan 2 boundary still introduces no generic runtime
registry or `DeviceProfile` write.

## Checkpoint 3: read-only evidence collector

The collector waits for the bounded Job, requires exactly one owned Pod, and
preserves:

- Run intent JSON and digest;
- raw Job, Pod, and actual Node snapshots, including UIDs;
- container statuses and pulled image identities;
- the strict runtime result for the selected batch;
- artifact digest, runtime/provider, CPU/cgroup, warm-up, inference, timestamp,
  and output-validation facts; and
- the unmodified MetricObserver result and its source identity.

The bundle is written atomically and named by its content digest. Job completion
alone is not a valid Run. Missing, duplicate, malformed, mismatched, or failed
runtime evidence fails the Run evidence gate. Collection/I/O failure remains
distinct from an observed Run failure.

The controller-side finalizer observes the completed Job and can therefore own
terminal Kubernetes facts. The in-Job measurement worker cannot author its own
terminal Pod or Job state.

Power samples are raw Run evidence only. Receipt timestamps are not device
timestamps. Coverage, gaps, integration, repetitions, uncertainty, energy, and
`b_sat` belong to Plan 3.

For either active or idle execution, the runtime declares the scientific
measurement window from its own monotonic clock and retains the corresponding
wall-clock timestamps. MetricObserver and WorkloadObserver results may prove or
fail coverage of that already declared window; they must never select, trim, or
move its boundaries from observed power or workload values.

## Code boundary

```text
api/v1alpha1/                   public active/idle Run request schema
internal/profilingrun/          plain intent and runtime-result validation
internal/profiler/worker/       Job-local orchestration and raw projection
runtime/ortcpu/                 explicit active/idle CPU ORT execution adapter
internal/profiler/evidence/     raw/final record verification and loading
```

`internal/profilingrun` imports no Kubernetes API, controller-runtime, client,
or CHILL API package. Kubernetes projection remains in the Plan 4 controller;
the runtime adapter and worker remain Kubernetes-free.

## Acceptance boundary

Plan 2 is complete when the same exact intent produces deterministic native
objects, one real Kubernetes execution yields a strict immutable evidence
bundle, intentional identity drift is rejected, and repository tests, race,
lint, import, and manifest audits pass.

Completion permits Plan 3 to inspect this bundle. It does not make the evidence
scientifically admissible and does not authorize a profile.

## Implementation result

One exact Lattepanda Run was materialized and collected under intent digest
`sha256:526040beceecf15f145e50372822280d9a3a0962809f1a4cc812aa64d4a9794c`.
The single Pod completed without retry or restart. Runtime evidence matched the
digest-pinned image, Node UID, artifact bytes, ORT CPU backend, four-CPU quota,
four/one ORT threads, effective cpuset and affinity, selected batch, and output
shape. Warm-up completion precedes the measurement window, and the 5,541
reported calls exactly match 5,541 retained raw latency samples over 30 seconds.
The observer retained 31 receipt-timestamp samples with no read failure and a
1.059-second maximum gap.

Ten co-resident Pods were visible in the post-run Node snapshot, six still
Running. Plan 2 does
not silently call their wall-power contribution negligible. The Run is valid
execution evidence, while power attribution and scientific admission remain a
Plan 3 decision. The init-container `imageID` was not comparable to the requested
artifact manifest on this KubeEdge/containerd path; exact artifact bytes were
instead verified inside the runtime and both raw identities are retained.
The final bundle is named by the SHA-256 of the exact bytes written, embeds the
original Plan 1 candidate report, and revalidates Job-to-Pod ownership, terminal
Job success, full Run annotations, and all requested Pod-spec image references.
