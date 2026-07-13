# Plan 2: Immutable Profiling Run and Execution Evidence

## Status

Implemented for the first CPU ONNX Runtime path. This plan does not
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
- digest-pinned runtime, artifact-carrier, and PowerObserver images;
- selected batch, warm-up, bounded measurement duration, and repetitions;
- target Node name, UID, resourceVersion, architecture, and allocatable CPU;
- the CPU derivation policy and its material inputs and outputs; and
- the resolved power source and bounded observation request.

The intent identity is the SHA-256 of canonical JSON. It is not a Job, Pod, or
Kubernetes UID. A changed snapshot or decision creates a different intent.

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
- runs the runtime and PowerObserver in separate containers; and
- carries the Run intent digest as a label/annotation and environment input.

Plan 2 does not claim that selection intent guarantees scheduling. It records
Kubernetes admission, placement, image, and container outcomes as evidence.

There is no public ProfilingRun CRD, controller, generic runtime registry, or
DeviceProfile write in the first path. Those abstractions require more than one
proven runtime lifecycle.

## Checkpoint 3: read-only evidence collector

The collector waits for the bounded Job, requires exactly one owned Pod, and
preserves:

- Run intent JSON and digest;
- raw Job, Pod, and actual Node snapshots, including UIDs;
- container statuses and pulled image identities;
- the strict runtime result for the selected batch;
- artifact digest, runtime/provider, CPU/cgroup, warm-up, inference, timestamp,
  and output-validation facts; and
- the unmodified PowerObserver result and its source identity.

The bundle is written atomically and named by its content digest. Job completion
alone is not a valid Run. Missing, duplicate, malformed, mismatched, or failed
runtime evidence fails the Run evidence gate. Collection/I/O failure remains
distinct from an observed Run failure.

Power samples are raw Run evidence only. Receipt timestamps are not device
timestamps. Coverage, gaps, integration, repetitions, uncertainty, energy, and
`b_sat` belong to Plan 3.

## Code boundary

```text
internal/profilingrun/          plain intent and evidence validation
spikes/profilingrun/cmd/        concrete CPU ORT Kubernetes adapter
spikes/profilingrun/fixtures/   exact first-path input
```

`internal/profilingrun` imports no Kubernetes API, controller-runtime, client,
or CHILL API package. Kubernetes translation and collection remain in the
one-shot adapter.

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
PowerObserver retained 31 receipt-timestamp samples with no read failure and a
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
