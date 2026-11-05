# 0016 Declared-Batch-Size Artifact Capability

## Status

Accepted, updated for automatic batch discovery. [0021 Raw-Sample Preprocessing Boundary](0021-raw-sample-preprocessing-boundary.md) narrows the input-tensor declaration requirement to RKNN artifacts. The current decision replaces the original campaign-selected batch list and local-minimum behavior while retaining bounded infrastructure retries; retained measurements and historical results remain unchanged.

## Context

CHILL discovers executable batches against one immutable artifact and execution path. The selected firstfloor's actual maximum bounds the finite candidate domain; a finite artifact declaration further constrains it.

RKNN-Toolkit2 2.3.2 accepts `dynamic_input` during conversion and enumerates several batch shapes into one immutable artifact. An RK3588 probe loaded one such artifact and executed several declared batches through the same RKNNLite path. The runtime refused an undeclared batch and reported the shapes carried by the artifact. The converter and device probe provide the evidence for a finite artifact-declared batch-size set.

The canonical ONNX artifact retains its symbolic batch axis. ONNX Runtime takes the batch size as a call argument, so that artifact does not impose a finite batch-size set. A batch-energy-optimization campaign freezes its automatically derived finite domain before creating a Run.

The RKNN input tensor contract belongs to the artifact and is frozen into the comparable curve plan.

## Decision

RKNN artifacts declare their batch-invariant input tensor contract as `ModelArtifact.input.tensors`, as narrowed by ADR 0021. Each tensor records its exact runtime `name`, Triton `datatype` (for example `FP32` or `INT64`), and positive `sampleShape` without the batch axis. Names are unique. A selected RKNN artifact without a complete declaration returns an error naming the artifact.

Derived artifacts declare the finite batch set fixed by conversion as `ModelArtifact.derivation.declaredBatchSizes`. One artifact identity and runtime path serve that declaration. A canonical artifact has no derivation or finite artifact batch-size set.

BEO automatically selects powers of two `1, 2, 4, …` no greater than the selected firstfloor's actual `MaxBatchSize`. When finite `DeclaredBatchSizes` exist, the candidate domain is their intersection with those powers of two. Batch 1 is required. Public BEO `spec.batchSizes` is removed. The existing `retryLimit` (0–10) remains for known transient infrastructure failures at the same K.

The immutable curve plan records that ordered domain as `PlannedBatchSizes` and preserves the selected input contract. Its conversion contract records the artifact source, or remains all-empty for canonical artifacts. Once recorded, the plan is authoritative; mutable current facts do not reproject it.

Candidate reports and frozen RunConfig delivery preserve the complete typed inputs for canonical and derived artifacts. Active and model-loaded-idle runtime evidence records the same inputs and is matched against the frozen declaration. Workers validate actual runtime metadata before measurement; they never infer a datatype or substitute a shape for a missing declaration. An unsupported datatype or runtime input contract is an explicit error.

The sweep advances through the recorded domain in ascending order. Each existing trial keeps its independent ProfilingRun and Kubernetes Job. Three completed model-loaded-idle trials precede three completed active trials per K. Active trials must meet both the existing elapsed-time and known-G sample floors.

One artifact defines one execution-state key and one common actor startup recipe for serving, active and idle: warm only `[1]`. The full actor batch list is a capability declaration, not eager execution. The first higher-K cold execution occurs inside its measurement window.

Domain exhaustion, expected runtime-execution OOM or the existing execution measurement-budget boundary ends discovery without retry. Expected OOM requires actual runtime execution with no explicit infrastructure disruption; typed stage separates it from shared-worker preparation and evidence commit. Exit 137 alone is insufficient. The BEO becomes `Complete` with outcome `BatchDiscoveryComplete`, preserving the contiguous prefix of K values with all three active trials and the three-trial idle baseline. Budget exhaustion describes the frozen measurement budget, not permanent runtime non-support.

Known Node/Pod eviction and transient infrastructure failures use bounded same-K retries; exhaustion is `Failed`. Generic runtime crashes, model errors, integrity failures and unknown failures are fatal. Previously completed trial raw remains retained on failure; a `Failed` campaign does not publish a completed-prefix profile.

`DiscoveryResult` carries `MeasuredBatchSizes`, `Stop` and optional `Energy`. Stop reasons are `OOM`, `MeasurementBudgetExceeded` and `DomainExhausted`. A boundary stop records its exact failed K/mode, attempt, Run and diagnostic. Any one or two completed trials at the interrupted K remain retained evidence but contribute neither profile nor capacity. A batch-one boundary produces an empty prefix and no usable profile. `Complete` does not imply all candidates succeeded or that usable energy/profile exists.

## Consequences

A runtime that enumerates one batch per artifact requires a per-batch execution path supported by its own conversion, device, state-identity, and baseline evidence.

A declared batch may still fail on the selected runtime. Expected OOM/budget boundaries preserve the completed prefix; generic execution errors remain fatal under [Plan 7](../design/profiling/plans/07-batch-energy-optimization-control-path.md). Discovery does not stop at a local energy minimum, non-improving neighbor or SLO-impossible batch one. SLO supplies the sample floor, not a discovery early-stop rule.

After approved source deployment, use new campaign identities. Existing trial state, completed raw/finalized evidence, stored plans and results remain immutable. Historical local-stop and full-sweep records retain their original interpretation and measurement numbers. This decision does not claim integration or live gates have passed.
