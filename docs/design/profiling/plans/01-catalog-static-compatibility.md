# Plan 1: Catalog and Static Compatibility

> Navigation: [Profiling design index](../README.md)

## Status

Implemented for the first CPU ONNX Runtime static-compatibility path. The
implementation remains a read-only preflight and does not authorize profiling
execution or Plan 2 API work.

## Question

> Is one immutable model artifact and runtime path statically compatible with
> one stable DeviceClass?

This plan does not execute a workload or inspect a live Node.

## Inputs

- stable `DeviceClass` capabilities
- logical model and immutable artifact identity
- one artifact execution-path requirement
- one immutable runtime workload declaration with a known producer

The first supported path is CPU ONNX Runtime. Plan 6 later added the narrow
RKNN artifact relation described below; live RKNN compatibility still stays
`Unknown` until an authoritative runtime and host-component producer exists.

## Checkpoint 1 decision: minimum ModelSpec

Accepted for the first CPU ORT path:

```text
Model identity              metadata.name
Artifact                    local name + format
Execution path              local name + artifact reference
Runtime requirement         family + backend
```

The runtime family and backend are requirements. They do not claim that a
selected image supplies or actually uses them.

Not included in the first schema:

- architecture or accelerator requirements
- artifact batch capability
- runtime version or image
- artifact retrieval location
- CPU quota, thread, placement, or other Run decisions
- campaign, power-observation, execution-result, profile, or status fields

The demonstrated canonical ONNX artifact is not architecture-specific, and
the selected campaign batches do not establish an artifact batch
capability. Those fields remain absent rather than being guessed from one CPU
Run or added for future runtimes.

### Plan 6 extension: one batch-profile RKNN artifact

The first Orange Pi RKNN slice exercises one optional artifact derivation
group:

```text
source artifact reference     canonical ONNX artifact in the same ModelSpec
target platform               rk3588
batch profiles                finite declared set of positive batches
runtime input tensor          non-batch shape + layout + data type
conversion identity           tool + version
```

The derived artifact is selected by its catalog name and immutable carrier
image. One execution path serves every declared profile. Conversion fields
describe how it was produced; they are not a second content-verification
system.

The source reference is resolved semantically by the candidate adapter. The
OpenAPI schema validates only structure. Runtime image,
host `librknnrt`, driver, device visibility, initialization, and NPU allocation
remain outside `ModelSpec` and must come from their owning execution sources.

## Checkpoint 2 decision: runtime declaration producer

The first declaration is trusted exact-image inspection output for the CPU ORT
image:

```text
buildx push metadata
  -> select the single runnable linux/amd64 child manifest
  -> run its immutable repository@sha256 OCI reference
  -> inspect architecture, imported ORT identity, and CPU backend
  -> publish the declaration under the configured fact-source key
```

The authoritative runtime reference is the runnable child OCI reference, not a
mutable tag or an image index attestation descriptor. The producer normalizes
the observed `x86_64` machine vocabulary to Kubernetes `amd64` and fails
closed on missing or ambiguous platform data, failed ORT import, unexpected
ORT version, or absent `CPUExecutionProvider`.

The first declaration contains only:

```text
schema/producer version
exact repository@sha256 image identity
canonical architecture
supplied runtime family
supplied CPU backend
```

ORT version and the raw provider inventory are checked and retained in
producer output, but are not declaration fields because the first consumer
does not compare them. No CRD, registry controller, signing, attestation, or
generic provenance schema is introduced.

Configured declarations now live in the trusted Profiler fact source and are
bound by exact source and content identity when a Run is frozen. A consumer
treats a missing or untrusted declaration as `Unknown` and never rebinds an
older declaration to a new image.

Plan 6 extends this configuration envelope without changing the CPU producer:
runtime-declaration v1alpha2 can retain image-owned package versions, while
execution-facts v1alpha4 separately retains an expected host-library, driver,
platform-compatible token, device-path, and Kubernetes allocation contract.
It also keeps privileged mode and supplemental groups in a distinct runtime-
access contract because those are delivery requirements, not physical state.
Candidate-report v1alpha8 freezes these groups plus the selected derived
artifact's target platform, batch profiles, runtime input tensor contract, and
the exact typed actuator target for a non-fixed selection into the immutable
Run input. Intent v1alpha9 retains the order-normalized physical
host contract in state and defines the optional typed actuation binding
consumed by Plan 6. Existing v1alpha1 runtime declarations, v1alpha2/v1alpha3 execution
facts, and v1alpha2-v1alpha5 candidate reports remain readable but cannot carry
the later fields. These are expected contracts, not proof that a particular
Job observed or satisfied them.

## Checkpoint 3 decision: first read-only consumer

The first consumer is split across a pure evaluator and trusted fact adapter:

```text
internal/modelcompat/                         pure compatibility evaluator
internal/profiler/controller/candidate_resolver.go
                                              trusted fact adapter
```

The adapter reads one `ModelSpec`, one `DeviceClass`, and one matching runtime
declaration from the trusted fact source. It selects the referenced artifact,
translates the facts into plain `internal/modelcompat` values, and invokes the
evaluator once. The pure evaluator has no Kubernetes client, hidden defaults,
discovery, enumeration, status write, image inspection, or workload execution.

The adapter preserves exact Kubernetes object and content identity, the
selected path/artifact identity, and the declaration's immutable image
reference. The deterministic candidate report never describes the result as
runnable, schedulable, executable, ready, or admitted.

`Compatible`, `Incompatible`, and `Unknown` are computed domain results.
Configuration I/O, strict decoding, structural reference, and identity-
integrity failures do not produce a candidate. Callers gate explicitly on the
verdict rather than treating successful evaluation as compatibility.

The pure evaluator, not the CLI adapter, owns the narrow known relations
between artifact format `onnx` and runtime family `onnxruntime`, and between
`rknn` and `rknnlite`. These relations do not claim that a selected artifact
loads or executes; unsupported format and runtime relations remain `Unknown`
until a concrete rule is implemented.

## Output

A read-only result for one exact input identity:

```text
Compatible | Incompatible | Unknown
+ zero or more local static reasons
```

The first consumer produces a read-only CPU profiling-candidate report. The
result is not written into `ModelSpec.status` or `DeviceClass.status`.

## Responsibilities

### API catalog boundary

- evolve the existing `ModelSpec` Kind with only fields exercised by the first
  CPU ORT path
- represent immutable artifact identity and stable execution-path
  requirements
- keep artifact batch capability separate from runtime engine capability and
  selected-Run success
- use kubebuilder markers as the source of CRD validation

### Runtime declaration producer

- produce CPU architecture, ONNX Runtime family, and CPU backend facts by
  inspecting the exact image from the runtime-image build/publish flow
- bind the declaration to an immutable immutable OCI image reference
- retain how each declared fact was verified

These are supplied runtime-image facts. They are independent from the
requirements declared by `ModelSpec`; the evaluator compares the two sources
rather than copying one into the other.

### Pure compatibility logic

- compare only facts supplied by known producers
- return `Unknown` for missing, untrusted, or unsupported comparisons
- keep verdict reasons local to this domain
- preserve deterministic output and reason ordering

### Read-only consumer

- translate API/catalog objects into plain compatibility inputs
- invoke the pure evaluator
- report the result without updating cluster status

## Code boundary

The first pure package should remain small:

```text
internal/modelcompat/
  types.go
  evaluate.go
  evaluate_test.go
```

`internal/modelcompat` must not import:

- `api/v1alpha1`
- `k8s.io/*`
- controller-runtime
- `metav1.Condition`
- Pod, Node, or `resource.Quantity` types

The API-to-domain conversion belongs to the concrete read-only consumer. No
generic adapter framework is introduced.

## Kubernetes-native use

- edit the existing `api/v1alpha1/modelspec_types.go`; do not scaffold a
  duplicate Kind
- generate deepcopy, CRD, and RBAC artifacts through the existing Makefile
  flow
- do not hand-edit generated CRDs or Helm CRD copies
- use API-server structural validation for serialization-level constraints
- keep semantic compatibility in plain Go rather than admission webhooks or a
  controller

## Explicitly out of scope

- live Node readiness, allocatable resources, or scheduler eligibility
- Jetson L4T/CUDA/TensorRT readiness
- RKNN host library or driver readiness
- Kubernetes Job or Pod generation
- runtime execution validation
- MetricObserver synchronization
- profile admission, energy, `b_sat`, or SLO
- generic planner/evaluator interfaces, registries, factories, or plugin maps

## Implementation sequence

The three prerequisites above are approved. Implementation proceeds as small
review boundaries:

1. Define the CPU-only `ModelSpec` fields and structural validation.
2. Define and verify the exact-image runtime-declaration contract.
3. Evolve the existing ModelSpec API with only the accepted fields, generate
   artifacts, and verify structural validation.
4. Implement plain `internal/modelcompat` inputs, tri-state evaluation, local
   reasons, and deterministic unit tests.
5. Integrate the evaluator with the trusted candidate resolver and its focused
   tests.
6. Run repository tests, targeted race tests, lint, generated-diff checks, and
   import audit.

Each boundary is reviewed before the next begins. API/generated-schema changes
remain separate from pure evaluator changes so neither hides the other.

## Acceptance boundary

This plan is complete when the CPU ORT consumer can reproducibly explain, for
one exact catalog/input identity, why a path is `Compatible`, `Incompatible`,
or `Unknown` without creating a workload or writing status.

Completion of this plan authorizes review of Plan 2. It does not by itself
authorize profiling execution.

## Implementation result

The first CPU ONNX Runtime path now closes this plan's acceptance boundary:

- `ModelSpec` carries immutable artifacts and stable execution-path
  requirements without compatibility-result or Run status fields.
- the isolated producer binds observed CPU runtime facts to the immutable
  runnable OCI image reference;
- `internal/modelcompat` returns deterministic `Compatible`, `Incompatible`,
  or `Unknown` results without Kubernetes dependencies; and
- the offline candidate report consumes exact files, writes no cluster state,
  and creates no workload.

The three verdicts were exercised against exact fixture identities, and the
live `cpu-v1` registry tag currently resolves to the same runnable child reference
as the retained runtime declaration. Repository tests, targeted race tests,
lint, generated-artifact drift checks, Helm rendering, and the dependency audit
form the completion evidence. The declaration verification label remains a
documented producer trust boundary, not a cryptographic signature or runtime
admission proof.
