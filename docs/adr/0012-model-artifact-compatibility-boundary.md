# 0012 Model Artifact Compatibility Boundary

## Status

Accepted

## Context

CHILL must exclude execution states that cannot run before an enumerator or
optimizer constructs a cluster configuration. The research specification calls
the resulting per-device-class model set $M_t$ and identifies architecture,
accelerator, memory, and runtime support as hard compatibility constraints.

A logical model name is not a sufficient execution identity. The existing EEP
Profiler and S1 Capacity environments execute the same three logical models
through several distinct paths:

- canonical ONNX artifacts through ONNX Runtime CPU, CUDA, or TensorRT
  execution providers
- RK3588-targeted, batch-fixed RKNN artifacts through RKNNLite

The RKNN artifacts have their own digests and retain the source ONNX digest,
target platform, fixed batch size, and conversion metadata. The measurement
harness also validates the loaded model, selected execution provider, batch
size, and device operating mode before admitting a result. These facts show
that compatibility applies to an executable artifact variant and execution
path, not only to a logical model and CPU architecture.

Runtime availability is not an intrinsic hardware property. The current
testbed has different ONNX Runtime versions and provider sets across devices,
and one Orange Pi board supports both an ONNX Runtime CPU path and an RKNNLite
NPU path. In Kubernetes deployment, the serving image and its runtime
dependencies further determine the available execution path. Adding a generic
runtime string to `DeviceClass` would mix software substrate with stable class
capabilities and still would not prove that a particular artifact can load.

Execution compatibility, SLO feasibility, measurement coverage, and current
Kubernetes schedulability are separate questions. Existing measurements
contain combinations that load and execute successfully but cannot satisfy the
evaluation latency objective. Current Node readiness, taints, and allocatable
resources can also change without changing the artifact's static execution
contract.

## Decision

Use the following domain boundaries:

- A model is the logical workload identity and model-level semantics.
- An artifact variant is an immutable executable representation of that model,
  identified by content digest and carrying the requirements needed to select
  its execution path.
- A runtime path identifies the runtime family and backend or execution
  provider used to load the variant. Runtime availability is supplied by the
  serving workload or another explicit runtime declaration when that component
  exists; it is not inferred from accelerator names.
- A device profile is evidence for a measured execution state. It does not
  redefine the model or artifact.

Keep `DeviceClass` limited to relatively stable hardware and class
capabilities. Do not add installed runtime versions, execution providers,
serving images, or `supportedRuntimes` to `DeviceClass.spec` or status.

Design `ModelSpec` around a logical model with one or more artifact variants.
The first schema must represent only facts required by an existing execution or
profiling path. Candidate facts include artifact reference and digest, format,
architecture and accelerator requirements, runtime family, backend or
execution provider, target platform, and fixed or supported batch constraints.
The exact field shape is decided with the first consumer and samples; this ADR
does not reserve speculative fields.

Do not treat artifact file size as runtime memory requirement. Runtime memory
is admitted only when its meaning and provenance are explicit, for example a
conservative declared bound or a measured load-time observation. Likewise,
runtime, driver, ABI, and accelerator-version constraints are added only when
the deployment path can supply and compare them.

Derive compatibility at decision time with a shared pure evaluator. Its result
has three states and stable reason codes:

- `Compatible`: every declared static requirement has a matching declared
  capability or runtime path.
- `Incompatible`: at least one declared requirement has an explicit conflict.
- `Unknown`: a required fact is absent, unsupported by the evaluator, or lacks
  a source that CHILL can trust.

`Unknown` is distinct from `Incompatible` for diagnostics and evidence
accounting, but both are fail-closed for configuration enumeration.
`Compatible` means only that the declared static execution contract is
satisfied; it does not mean that a Pod is currently schedulable or that the
combination satisfies an SLO.

Do not materialize `compatibleClasses` in `ModelSpec.status` or the inverse
mapping in `DeviceClass.status`. Both would be derived indexes requiring
fan-out reconciliation and could become stale. At the current catalog size the
evaluator can compute the cross-product directly. A report or cached view may
be introduced later only with a concrete consumer, one owning controller, and
input identity and generation recorded in the output.

The optimizer's enumerable state set is stricter than static compatibility:

```text
enumerable = compatible artifact/runtime path
             AND admissible device profile
             AND current Kubernetes eligibility
```

Profile absence, profile failure, and SLO infeasibility remain explicit
reasons outside static compatibility. A failed measurement of one artifact and
runtime path does not make the logical model universally incompatible.

## Consequences

The next API work is not a flat `ModelSpec` containing one image and one
runtime string. It starts by defining the smallest artifact-variant schema that
can express the ONNX Runtime and RKNNLite paths already demonstrated by the
testbed.

Device discovery remains independent of serving-software installation and
model catalog changes. The same physical class may participate through more
than one runtime path without being duplicated solely to encode software
state.

Profilers, enumerators, and future schedulers share one compatibility
evaluator and reason vocabulary. Kubernetes remains authoritative for current
Node eligibility, while measurement resources remain authoritative for which
compatible states have decision-grade performance and energy evidence.

Some combinations will remain `Unknown` until CHILL has a deployable serving
image or explicit runtime declaration that can prove the required backend is
available. This is intentional: the system does not convert hardware naming
conventions into unsupported execution guarantees.

The concrete ONNX TensorRT and RKNNLite RK3588 examples, field ownership, and
minimum validation rules are maintained in the isolated
[`spikes/modelspec/`](../../spikes/modelspec/) workspace.
