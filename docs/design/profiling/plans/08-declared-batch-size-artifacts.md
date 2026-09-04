# Plan 8: Declared-Batch-Size Artifacts

> Navigation: [Profiling design index](../README.md)
>
> Objective and control path: [Plan 7](07-batch-energy-optimization-control-path.md)

## Status

Implemented for two single-artifact paths. Every artifact declares its ordered
batch-invariant input dimensions. Derived RKNN artifacts
additionally declare the finite batch-size set fixed by conversion. Canonical
ONNX artifacts
with a symbolic batch axis leave the finite sweep set to
`BatchEnergyOptimization.spec.batchSizes`. Exactly one source supplies the batch
sizes in the recorded curve plan, and one execution path serves the selected
set. The Quick energy optimizer consumes that ordered search space.

## Question

> Which declaration owns the finite sweep when one immutable artifact executes
> several batch sizes?

A conversion that fixes a finite profile set makes that set an artifact
constraint. A runtime that takes batch as a call argument leaves the finite set
to the measurement campaign.

## Measured evidence

The canonical `mobilenet-v2-050.onnx` graph input `input` is
`["batch", 3, 224, 224]`, its graph output `output` is `["batch", 1000]`, and
its element type is `float32`. These facts were read from the artifact image
with `onnx.load`; ONNX tensor type metadata does not provide a portable layout
field. The symbolic first axis permits ONNX Runtime to receive batch at
execution. The artifact declaration records the ordered dimensions
`[3, 224, 224]` without that axis, and the worker builds its input from the
selected batch and the declared non-batch dimensions. The ORT worker validates
the loaded graph's element type and generates `float32` input as runtime
behavior; model registration does not repeat that fact.

RKNN-Toolkit2 2.3.2 built one artifact holding declared batch sizes
`1, 2, 4, 8, 16`:

```text
artifact  = mobilenet-v2-050-rk3588-dynamic-b1-2-4-8-16.rknn
size      = 16,238,707 bytes
SHA-256   = 74328e5c931720e6ffc3ee5d0a1c6f3d71b40f84a730d8e7f36d8bbe8bd22f50
```

An RK3588 execution probe using the same RKNNLite load, initialization, and
inference calls as the worker produced:

```text
load_rknn      -> 0
init_runtime   -> 0
batch  1 (declared  ) -> [[1, 1000]] batchPreserved=True finite=True
batch  2 (declared  ) -> [[2, 1000]] batchPreserved=True finite=True
batch  4 (declared  ) -> [[4, 1000]] batchPreserved=True finite=True
batch  3 (UNDECLARED) -> refused; runtime enumerated [16,8,4,2,1]x224x224x3 NHWC
```

Environment: `librknnrt 2.3.2 (429f97ae6b@2025-04-09)`, driver `0.9.6`, model
version 6, target `RKNPU v2 / rk3588`, `model inference type: dynamic_shape`.
The Lite API selects a declared profile from the input array shape, and the
runtime refuses an undeclared profile. The runtime reports its shapes as NHWC;
CHILL records that runtime-owned observation in execution evidence instead of
asking the registration object to predict it.

## Declaration contracts

Every artifact carries its ordered input dimensions, and a derived artifact
also carries the finite conversion constraint. Input data type remains a
runtime implementation and loaded-artifact fact rather than user configuration:

```go
type ModelArtifact struct {
    Name   string
    Format string
    Input  *ModelArtifactInputContract

    Derivation *ModelArtifactDerivation
}

type ModelArtifactDerivation struct {
    SourceArtifact     string
    TargetPlatform     string
    DeclaredBatchSizes []int32
    Conversion ModelArtifactConversion
}
```

A canonical artifact has no `Derivation`, and therefore no
`DeclaredBatchSizes`. Its campaign carries the finite measurement choice:

```go
type BatchEnergyOptimizationSpec struct {
    // Positive, unique, and strictly increasing. Optional only because a
    // derived artifact supplies its own set.
    BatchSizes []int32 `json:"batchSizes,omitempty"`
}
```

Exactly one source applies. `BatchSizes` is forbidden when the selected artifact
has `DeclaredBatchSizes`, and required when it has none. Supplying both sources
is a validation error, not a union or merge operation.

`ProfilingRunSpec` still represents one batch. The RKNNLite membership check in
Run admission consumes `DeclaredBatchSizes`; canonical ONNX Run admission has no
artifact-declared membership check.

## Recorded curve plan

The first reconcile resolves and records one immutable source:

```text
resolve selected artifact
  require artifact Input
  derived artifact   require campaign BatchSizes absent
                     use ModelSpec.DeclaredBatchSizes
  canonical artifact require campaign BatchSizes present
                     use campaign BatchSizes
prepare active and idle RunConfigs at the first selected batch
record curve-plan.json with PlannedBatchSizes
```

The plan copies the selected artifact's ordered input dimensions.
A missing declaration returns `ArtifactInputContractMissingError` naming that
artifact; the controller does not probe, introspect, infer, or default it.

The plan records the batch-set source through its conversion contract. The
artifact-declared source has `TargetPlatform`, `Tool`, and `ToolVersion`. The
campaign-declared canonical source records all three fields empty. A reader can
therefore distinguish an artifact constraint from a measurement choice while
both paths expose the same `PlannedBatchSizes` contract to the control and
derivation layers.

For the supported `mobilenet-v2-050` canonical ONNX path, the recorded tensor
contract comes from the `canonical-onnx` catalog declaration: `[3, 224, 224]`.
The controller does not open the artifact. The ONNX Runtime worker reads the
graph, requires `tensor(float)`, and generates `float32` input while executing
each Run.

The stored plan is authoritative on later reconciles. Semantic disagreement
between a recomputed plan and the immutable stored plan terminates resolution
instead of replacing the record.

## Sweep and capability boundary

Both selected sets may be sparse. The sweep advances through the recorded set
in ascending order, steps over omitted sizes, and reaches
`Stopped/CapabilityBoundary` when the set is exhausted. `KMax` is compared with
the largest planned batch size. Plan 5a and Plan 5 consume
`PlannedBatchSizes` without learning which source supplied it.

One artifact defines one execution-state key, so the curve owns one admitted
model-loaded-idle baseline. The baseline Run uses the first planned batch for
either source.

A derived-artifact catalog may declare a batch the artifact cannot execute. A
canonical campaign may select a batch its runtime cannot execute. Either refusal
is an ordinary trial failure followed by the existing retry and terminal-failure
path. The frozen capability boundary remains exhaustion of the recorded set.

## Build and evidence boundaries

`build/model-artifacts/rknn/export_rknn.py` reads the ONNX input sample shape,
builds `dynamic_input` from the configured declared batch sizes, and passes the
original ONNX model to the converter. One RKNN conversion, artifact image, and
catalog execution path serve the declared set.

The Profiler controller reads ModelSpec and candidate-report declarations. It
does not open the model, probe a runtime, discover capabilities, or merge a
campaign set with an artifact set. Runtime execution remains the observation
that confirms whether each selected batch actually runs.

Records remain separated by responsibility:

| Record | Key | Written |
|---|---|---|
| Conversion record | `model-artifacts/<modelSpec>/<artifact>/conversion.json` | first Run admission |
| Device probe | `model-artifacts/<modelSpec>/<artifact>/device-probe.json` | first Run admission |
| Recorded curve plan | `batch-energy-optimizations/<uid>/curve-plan.json` | first reconcile |
| BatchEnergyOptimization result | `batch-energy-optimizations/<uid>/result.json` | measurement end |
| Run raw and execution evidence | `<uid>/raw`, `<uid>/execution` | each Run |

The current strict readers accept only their current schema versions. Stored
objects retain their scientific value, while re-derivation under a later schema
requires new measurement evidence.

## Consequences

A runtime that enumerates one batch per artifact requires a per-batch execution
path supported by its own conversion, device, state-identity, and baseline
evidence.

A derived artifact's set is a catalog claim about a built artifact. A canonical
artifact's set is the campaign's measurement choice. Execution confirms each
selected batch, and a runtime refusal is reported as a trial failure.
