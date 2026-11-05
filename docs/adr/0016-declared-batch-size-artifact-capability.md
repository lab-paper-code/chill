# 0016 Declared-Batch-Size Artifact Capability

## Status

Accepted, updated for automatic batch discovery. [0021 Raw-Sample Preprocessing Boundary](0021-raw-sample-preprocessing-boundary.md) narrows the input-tensor declaration requirement to RKNN artifacts. The current decision below replaces the original campaign-selected batch list and retry/local-minimum behavior; retained measurements and historical results remain unchanged.

## Context

CHILL discovers executable batches against one immutable artifact and execution path. The selected firstfloor's actual maximum bounds the finite candidate domain; a finite artifact declaration further constrains it.

RKNN-Toolkit2 2.3.2 accepts `dynamic_input` during conversion and enumerates several batch shapes into one immutable artifact. An RK3588 probe loaded one such artifact and executed several declared batches through the same RKNNLite path. The runtime refused an undeclared batch and reported the shapes carried by the artifact. The converter and device probe provide the evidence for a finite artifact-declared batch-size set.

The canonical ONNX artifact retains its symbolic batch axis. ONNX Runtime takes the batch size as a call argument, so that artifact does not impose a finite batch-size set. A batch-energy-optimization campaign freezes its automatically derived finite domain before creating a Run.

The RKNN input tensor contract belongs to the artifact and is frozen into the comparable curve plan.

## Decision

RKNN artifacts declare their batch-invariant input tensor contract as `ModelArtifact.input.tensors`, as narrowed by ADR 0021. Each tensor records its exact runtime `name`, Triton `datatype` (for example `FP32` or `INT64`), and positive `sampleShape` without the batch axis. Names are unique. A selected RKNN artifact without a complete declaration returns an error naming the artifact.

Derived artifacts declare the finite batch set fixed by conversion as `ModelArtifact.derivation.declaredBatchSizes`. One artifact identity and runtime path serve that declaration. A canonical artifact has no derivation or finite artifact batch-size set.

BEO automatically selects powers of two `1, 2, 4, …` no greater than the selected firstfloor's actual `MaxBatchSize`. When finite `DeclaredBatchSizes` exist, the candidate domain is their intersection with those powers of two. Batch 1 is required. Public BEO `spec.batchSizes` and `retryLimit` are removed; users do not supply a second batch domain or retry policy.

The immutable curve plan records that ordered domain as `PlannedBatchSizes` and preserves the selected input contract. Its conversion contract records the artifact source, or remains all-empty for canonical artifacts. Once recorded, the plan is authoritative; mutable current facts do not reproject it.

Candidate reports and frozen RunConfig delivery preserve the complete typed inputs for canonical and derived artifacts. Active and model-loaded-idle runtime evidence records the same inputs and is matched against the frozen declaration. Workers validate actual runtime metadata before measurement; they never infer a datatype or substitute a shape for a missing declaration. An unsupported datatype or runtime input contract is an explicit error.

The sweep advances through the recorded domain in ascending order. Each existing trial keeps its independent ProfilingRun and Kubernetes Job. Three completed model-loaded-idle trials precede three completed active trials per K. Active trials must meet both the existing elapsed-time and known-G sample floors.

One artifact defines one execution-state key and one common actor startup recipe for serving, active and idle: warm only `[1]`. The full actor batch list is a capability declaration, not eager execution. The first higher-K cold execution occurs inside its measurement window.

Domain exhaustion or the first expected OOM, runtime execution failure or existing measurement-budget boundary ends discovery without retry. The BEO becomes `Complete` with outcome `BatchDiscoveryComplete`, preserving the contiguous prefix of K values with all three active trials and the three-trial idle baseline. Genuine infrastructure, observer and invalid-evidence failures remain `Failed`. Budget exhaustion describes the frozen measurement budget; it does not declare permanent runtime non-support.

`DiscoveryResult` carries `MeasuredBatchSizes`, `Stop` and optional `Energy`. A boundary stop records its exact failed K/mode, reason, attempt, Run and diagnostic; exhaustion records `DomainExhausted`. Any one or two completed trials at the interrupted K remain retained evidence but contribute neither profile nor capacity. A batch-one boundary produces an empty prefix and no usable profile. `Complete` does not imply all candidates succeeded or that usable energy/profile exists.

## Consequences

A runtime that enumerates one batch per artifact requires a per-batch execution path supported by its own conversion, device, state-identity, and baseline evidence.

A declared batch may still fail on the selected runtime. Discovery records that execution boundary and derives energy only from the completed prefix. It does not stop at a local energy minimum, non-improving neighbor or SLO-impossible batch one. SLO supplies the sample floor, not a discovery early-stop rule.

After approved source deployment, use new campaign identities. Existing trial state, completed raw/finalized evidence, stored plans and results remain immutable. Historical local-stop and full-sweep records retain their original interpretation and measurement numbers. This decision does not claim integration or live gates have passed.
