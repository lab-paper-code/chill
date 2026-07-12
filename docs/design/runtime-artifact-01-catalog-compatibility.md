# Plan 1: Catalog and Static Compatibility

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

The first supported path is CPU ONNX Runtime. Jetson and RKNN comparisons stay
`Unknown` where an authoritative ABI/component producer does not yet exist.

## Checkpoint 1 decision: minimum ModelSpec

Accepted for the first CPU ORT path:

```text
Model identity              metadata.name
Artifact                    local name + format + SHA-256 digest
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
the selected campaign batches do not establish a digest-bound artifact batch
capability. Those fields remain absent rather than being guessed from the
current CPU spike or added for future runtimes.

## Checkpoint 2 decision: runtime declaration producer

The first declaration is generated in the isolated batch-characterization
spike after the CPU ORT image is pushed:

```text
buildx push metadata
  -> select the single runnable linux/amd64 child manifest
  -> run the exact repository@child-digest image
  -> inspect architecture, imported ORT identity, and CPU backend
  -> atomically write a digest-named declaration
```

The authoritative identity is the runnable child manifest digest, not a
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

Generated declarations live under the spike results directory and are named
by digest. A failed producer emits no new declaration; a consumer treats a
missing or untrusted declaration as `Unknown` and never rebinds an older file
to a new image.

## Checkpoint 3 decision: first read-only consumer

The first consumer is a one-shot offline CLI in the root Go module:

```text
spikes/modelcompat/cmd/candidate-report/
  main.go
  main_test.go
```

It requires exactly one ModelSpec file, one DeviceClass file, one runtime
declaration file, and one execution-path local name. It selects the referenced
artifact through that path, translates the three declarations into plain
`internal/modelcompat` values, invokes the evaluator once, and exits. It has no
Kubernetes client, hidden fixture defaults, discovery, enumeration, status
write, image inspection, or workload execution.

The adapter preserves exact input identity with the content SHA-256 of each
input, the selected path/artifact identity, and the declaration's immutable
image reference. It writes one deterministic JSON report to stdout and a
short `StaticCompatibility` explanation to stderr. The report never describes
the result as runnable, schedulable, executable, ready, or admitted.

`Compatible`, `Incompatible`, and `Unknown` are all successfully computed
domain results and therefore exit zero. I/O, strict decoding, structural
reference, identity-integrity, or usage failure is nonzero and does not emit a
successful report. A future caller gates explicitly on the JSON verdict rather
than treating process success as compatibility.

The pure evaluator, not the CLI adapter, owns the first narrow known relation
between artifact format `onnx` and required runtime family `onnxruntime`. This
does not claim that a selected artifact loads or executes; unsupported format
and runtime relations remain `Unknown` until a concrete rule is implemented.

## Output

A read-only result for one exact input identity:

```text
Compatible | Incompatible | Unknown
+ zero or more local static reasons
```

The first consumer is a read-only CPU profiling-candidate report used by the
profiling spike. The result is not written into `ModelSpec.status` or
`DeviceClass.status`.

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
- bind the declaration to an immutable image digest
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
- PowerObserver synchronization
- profile admission, energy, `b_sat`, or SLO
- generic planner/evaluator interfaces, registries, factories, or plugin maps

## Implementation sequence

The three prerequisites above are approved. Implementation proceeds as small
review boundaries:

1. Add the accepted CPU-only ModelSpec fixture/validator in an isolated path;
   retain the old multi-runtime spike as background evidence, not an API
   source.
2. Implement and exercise the exact-image runtime-declaration producer in the
   batch-characterization spike.
3. Evolve the existing ModelSpec API with only the accepted fields, generate
   artifacts, and verify structural validation.
4. Implement plain `internal/modelcompat` inputs, tri-state evaluation, local
   reasons, and deterministic unit tests.
5. Implement the offline candidate-report adapter and its input/output tests.
6. Run the report against the exact CPU fixtures, then run repository tests,
   targeted race tests, lint, generated-diff checks, and import audit.

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
- the isolated producer binds observed CPU runtime facts to the exact runnable
  image manifest digest;
- `internal/modelcompat` returns deterministic `Compatible`, `Incompatible`,
  or `Unknown` results without Kubernetes dependencies; and
- the offline candidate report consumes exact files, writes no cluster state,
  and creates no workload.

The three verdicts were exercised against exact fixture identities, and the
live `cpu-v1` registry tag currently resolves to the same runnable child digest
as the retained runtime declaration. Repository tests, targeted race tests,
lint, generated-artifact drift checks, Helm rendering, and the dependency audit
form the completion evidence. The declaration verification label remains a
documented producer trust boundary, not a cryptographic signature or runtime
admission proof.
