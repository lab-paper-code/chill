# ModelSpec Concrete Schema Spike

## Purpose

This document tests the boundary in ADR 0012 against two execution paths that
already exist in the EEP Profiler and S1 Capacity environments. It is an input
to the `ModelSpec` API implementation, not an installed CRD schema or a valid
`config/samples` resource yet.

Everything in this directory is isolated from the operator, generated CRDs,
Helm chart runtime, and `internal/` packages. Run the offline check with:

```sh
python3 spikes/modelspec/validate.py
```

The two cases use the same logical model:

- canonical ONNX through ONNX Runtime TensorRT Execution Provider
- a batch-fixed RKNN artifact through RKNNLite on RK3588 NPU

The spike must preserve artifact identity and lineage without treating runtime
installation, measurement results, or current Node state as model metadata.

## Observed facts

The following values are verified from the existing artifacts and measurement
harness.

| Fact | Canonical ONNX | RKNN bs=1 |
|---|---|---|
| Logical model | `mobilenet-v2-050` | `mobilenet-v2-050` |
| Format | `onnx` | `rknn` |
| Digest | `sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2` | `sha256:ba3224391d45e19eff09cdfce96040a3614cc8cfde5319f919f40630a99f8c26` |
| Runtime family | `onnxruntime` | `rknnlite` |
| Backend/provider | `TensorrtExecutionProvider` | `RKNPUExecutionProvider` |
| Target | NVIDIA Jetson execution path | `rk3588` |
| Batch constraint | dynamic model batch dimension | fixed at `1` during RKNN export |
| Lineage | canonical source | derived from the canonical ONNX digest |

Artifact file size is deliberately excluded from the compatibility contract.
It is observable metadata, but it neither proves runtime memory use nor changes
whether the artifact can execute.

## Candidate shape

The candidate separates executable files from the runtime paths that consume
them. The standalone candidate is
[`fixtures/mobilenet-v2-050.yaml`](fixtures/mobilenet-v2-050.yaml); it is the
single source for offline validation and is not applied to the current CRD.

This is one `ModelSpec`, not two logical models. The two execution paths are
shown separately below to make their compatibility inputs explicit.

## Case A: ONNX Runtime TensorRT path

```yaml
name: ort-tensorrt-arm64
artifact: canonical-onnx
runtime:
  family: onnxruntime
  provider: TensorrtExecutionProvider
requirements:
  architectures: [arm64]
  accelerators:
    - nvidia-jetson-agx-orin
    - nvidia-jetson-orin-nano
    - nvidia-jetson-xavier-nx
    - nvidia-jetson-nano
batching:
  mode: dynamic
```

The accelerator list is intentionally exact for the current catalog. CHILL
does not yet have a stable accelerator taxonomy with vendor/family matching,
and a naming convention such as `nvidia-*` must not silently become an API
matching rule. A future DeviceClass capability vocabulary may replace the
explicit list when a real consumer requires broader matching.

This declaration alone does not prove that ONNX Runtime TensorRT EP is present
on a Node or in a serving image. The existing profiler configuration can supply
that fact for the measurement adapter. A future Kubernetes runtime or serving
workload declaration must supply it for deployment. Without such a source, the
compatibility evaluator returns `Unknown`, not `Compatible`.

## Case B: RKNNLite RK3588 path

```yaml
name: rknnlite-rk3588-bs1
artifact: rk3588-bs1
runtime:
  family: rknnlite
  provider: RKNPUExecutionProvider
requirements:
  architectures: [arm64]
  accelerators: [rk3588-npu]
  targetPlatform: rk3588
batching:
  mode: fixed
  size: 1
```

This path cannot reuse the canonical ONNX artifact directly. The profiler
exports a separate RKNN file for each supported batch size, so batch is both a
build property of the artifact and an execution constraint. The duplicate
appearance is intentional: `build.batchSize` records provenance, while
`batching.size` is the constraint consumed by compatibility evaluation.

## Ownership boundaries

| Field or fact | Owner | Reason |
|---|---|---|
| logical model identity | `ModelSpec` | stable catalog identity |
| artifact name, format, digest | `ModelSpec.spec.artifacts` | immutable executable identity |
| artifact lineage and build target | artifact build metadata | reproducibility of derived artifacts |
| runtime family and provider requirement | `ModelSpec.spec.executionPaths` | requirement of one execution path |
| CPU architecture and accelerator requirement | execution path | requirement compared with `DeviceClass.spec` |
| stable hardware architecture, accelerator, memory, power modes | `DeviceClass.spec` | class capability |
| installed runtime/provider versions | runtime or serving environment | mutable software substrate, not hardware class |
| artifact delivery URI | future artifact resolver or serving binding | deployment concern; no current Kubernetes consumer |
| serving image digest | future runtime or workload declaration | packages the software substrate |
| actual model-load success | `DeviceProfile` or workload status | observed evidence |
| measured power, latency, throughput, capacity | `DeviceProfile` | decision-grade profile evidence |
| SLO feasibility | profile plus workload objective | depends on the requested SLO |
| current Node readiness, taints, allocatable resources | Kubernetes Node and scheduler | dynamic cluster state |

## Minimum validation implied by the examples

The first API implementation can validate these structural invariants without
a controller:

- `artifacts` contains at least one item.
- artifact names are unique within a `ModelSpec`.
- artifact format and digest are non-empty.
- digest uses an explicit algorithm prefix and a valid value for that
  algorithm; the initial implementation may support only `sha256`.
- `derivedFrom.artifact`, when present, references another artifact in the
  same `ModelSpec` and does not reference itself.
- `executionPaths` contains at least one item.
- execution-path names are unique.
- every execution path references an artifact in the same `ModelSpec`.
- runtime family and provider are non-empty.
- architectures and accelerators contain no empty or duplicate values.
- batching mode is one of `dynamic` or `fixed`.
- fixed batching requires a positive size; dynamic batching must not set one.
- an RKNN artifact with a fixed exported batch has a matching fixed execution
  constraint.

Cross-field reference checks and digest syntax checks that cannot be expressed
reliably through OpenAPI markers belong in a pure validation package and an
admission webhook only if API-server-time rejection becomes operationally
necessary. The initial CRD should use schema validation for local constraints
and unit-test the complete validator without adding a webhook.

## Compatibility mapping for the current catalog

These examples produce only static hardware matches. Runtime availability and
profile admission are subsequent gates.

| Execution path | Static DeviceClass matches |
|---|---|
| `ort-tensorrt-arm64` | `jetson-agx-orin-64g`, `jetson-orin-nano-8g`, `jetson-xavier-nx-8g`, `jetson-nano-4g` |
| `rknnlite-rk3588-bs1` | `orangepi-5-plus-16g` |

For example, the ONNX TensorRT path and `jetson-orin-nano-8g` have matching
architecture and accelerator declarations. The final static result is still
`Unknown` until a runtime source declares TensorRT EP availability. Once the
EEP adapter supplies the measured runtime path, the pair can become
`Compatible`; it becomes enumerable only when an admissible matching profile
and an eligible Kubernetes Node also exist.

## EEP and S1 profile key mapping

The existing measurement key is:

```text
(device, model, batch_size, dvfs_mode)
```

Runtime provider and artifact identity are currently implicit in device
configuration and deployment provenance. The CHILL adapter must resolve them
before admitting a profile:

```text
EEP device        -> DeviceClass
EEP model         -> ModelSpec
model SHA256      -> artifact
measured EP       -> executionPath.runtime.provider
batch_size        -> executionPath.batching
dvfs_mode         -> DeviceClass powerMode
```

The resulting CHILL profile key is conceptually:

```text
(deviceClass, modelSpec, executionPath, powerMode, batchSize)
```

Rows lacking artifact digest or measured provider provenance remain
unresolved; they are not silently joined by model filename alone.

## Deferred fields and decisions

The following are intentionally absent from the first schema:

- `status.compatibleClasses`: derived index with no owner or consumer
- `memoryRequired`: not measured by the existing harness and not equivalent to
  artifact size
- artifact URI: the Kubernetes artifact delivery mechanism is not selected
- serving image: no deployable serving workload owns it yet
- runtime, CUDA, TensorRT, driver, and ABI version constraints: current
  provenance is incomplete and no resolver compares them
- load time and engine-build time: profile data, not artifact identity
- model quality and SLO: model/profile/workload concerns outside static
  compatibility
- generic accelerator-family matching: current `DeviceClass.spec.accelerator`
  is an opaque exact value, not a hierarchy

These fields are added only with a producer, a consumer, and a validation rule.

## Result

The examples support a minimal `ModelSpec` with `artifacts` and
`executionPaths`. They also show that the first implementation should stop at
catalog schema, structural validation, and samples. It should not add a
compatibility controller, status fields, artifact downloader, serving image,
or webhook.

The next implementation PR can translate this candidate into Go API types and
CRD validation, using these two paths as acceptance fixtures. The pure
compatibility evaluator follows once the API types are stable and an explicit
runtime-capability input type has been defined.
