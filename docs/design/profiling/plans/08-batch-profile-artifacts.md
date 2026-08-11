# Plan 8: Batch-Profile Artifacts

> Navigation: [Profiling design index](../README.md)
>
> Method: [Plan 5a](05a-sequential-batch-stop.md), [Plan 5](05-batch-saturation-campaign.md)
>
> Control path: [Plan 7](07-batch-saturation-control-path.md)

## Status

Approved design, under implementation. It replaces the premise beneath Plan 7
and retires ADR 0014. Plans 5 and 5a keep the estimand, acceptance rule, and
sizing method unchanged.

## Question

> CHILL assumes one immutable artifact encodes exactly one batch size. Is that a
> property of the runtime, or a property of CHILL's own conversion step?

It is CHILL's own conversion step. RKNN carries several batch shapes in one
artifact, and correcting the assumption removes the manual per-batch apparatus
built to accommodate it.

## What this plan does

One artifact declares the set of batch sizes it can execute. One execution path
serves a whole batch sweep. The capability boundary becomes a catalog fact known
before any Run is created.

The batch-invariant tensor truth already exists one layer down as
`CurveTensorContract.NonBatchShape`. This plan promotes it to the catalog and
deletes every place that restated it per batch.

## Measured evidence

Two independent results establish the premise.

**Conversion (workstation).** `mobilenet-v2-050.onnx` carries a symbolic batch
axis (`["batch", 3, 224, 224]`), satisfying `dynamic_input`'s documented
prerequisite. RKNN-Toolkit2 2.3.2 built one artifact holding profiles
`1, 2, 4, 8, 16`:

```text
artifact  = mobilenet-v2-050-rk3588-dynamic-b1-2-4-8-16.rknn
size      = 16,238,707 bytes
SHA-256   = 74328e5c931720e6ffc3ee5d0a1c6f3d71b40f84a730d8e7f36d8bbe8bd22f50
```

**Execution (RK3588 board `orangepi`).** A probe mirroring
`runtime/rknnlite/runner.py:928-938,973` — `RKNNLite()`, `load_rknn`,
`init_runtime(core_mask=NPU_CORE_0)`, `inference(inputs=[sample])` — ran against
that exact artifact:

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

Three facts follow, and each is load-bearing below:

1. The Lite API selects the profile from the input array shape. The runner's
   existing `inference(inputs=[sample])` call is sufficient as written.
2. `init_runtime(core_mask=NPU_CORE_0)` works against a dynamic artifact.
3. The runtime refuses an undeclared batch and enumerates the shapes it holds.
   The capability boundary is an observed runtime property.

The runtime reports its shapes as NHWC while the framework layout is NCHW. That
asymmetry already exists in CHILL and is preserved.

## The single declaration point

```go
type ModelArtifactDerivation struct {
	SourceArtifact string
	TargetPlatform string

	// BatchProfiles enumerates every batch size this artifact can execute.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=1
	BatchProfiles []int32 `json:"batchProfiles"`

	Input      *ModelArtifactInputContract
	Conversion ModelArtifactConversion
}

type ModelArtifactInputContract struct {
	NonBatchShape []int32 `json:"nonBatchShape"`
	Layout        string  `json:"layout"`
	DataType      string  `json:"dataType"`
}
```

An artifact without a `Derivation` declares no profiles and accepts any batch.
That covers the source ONNX and the ONNX Runtime CPU path, which consume batch
as a call argument.

The capability boundary is a requested batch outside a declared `BatchProfiles`.
It is resolved from the catalog, before a Run exists.

`ProfilingRunSpec` is unchanged: one Run measures one batch.
`BatchSaturationSpec.State` is unchanged: one candidate now covers a whole
sweep, which is what its shape already assumed.

## Verification collapses to execution evidence

Removing the batch axis from the declared shape makes a batch mismatch
unrepresentable. Five checks lose their subject:

| Deleted | Why it has nothing left to compare |
|---|---|
| `internal/profiler/controller/catalog_facts.go:143` | `Input.Shape[0]` and `Derivation.FixedBatch` merge into one field |
| `internal/profilingrun/candidate.go:447` | same |
| `internal/profiler/worker/rknn_lite.go:210-211` | same |
| `runtime/rknnlite/runner.py:397-398` | same |
| `internal/profilederivation/curve_family.go:339` | per-member tensor contract is gone |

Each compared two catalog declarations against each other, leaving execution
unobserved. Two checks that observe execution remain, and they are stronger than
the five they replace:

```python
# runtime/rknnlite/runner.py:665 — the runtime supplied no result for this batch
if not outputs:
    raise ContractError("inference produced no outputs")

# runtime/rknnlite/runner.py:670 — the executed batch was preserved
if output.size < 1 or not shape or shape[0] != batch:
    raise ContractError("inference output shape does not preserve the frozen batch")
```

Both already exist. RKNNLite swallows a refused profile internally and returns
`None`, which `validate_outputs` catches on its first line because `None` is
falsy, so the refusal surfaces as a typed `ContractError`.

What the probe justifies is naming the cause. "inference produced no outputs"
states the symptom; the refused profile is the cause, and a misbuilt artifact is
the one case that reaches this path. Naming it is a diagnosability change to an
existing check.

This satisfies `AGENTS.md` — "verify once at that boundary, and do not
re-validate it downstream" — by removing a standing violation of that rule.

## Derivation collapse

With one artifact per family, a "member" has nothing batch-varying left to hold.
`CurveFamilyMemberContract`'s seven fields resolve to: four now shared
(`ExecutionPath`, `ArtifactName`, `ArtifactPath`, `ArtifactImage`), two derived
(`FixedBatchCapability`, `InputShape`), one remaining (`Batch`).

Deleted from `internal/profilederivation/curve_family.go`:
`CurveFamilyMemberContract`, `VerifiedCurveFamilyMember`, `CurveFamily.members`,
`validateCurveFamilyMember`, `cloneCurveFamilyMembers`,
`cloneVerifiedCurveFamilyMember(s)`, the N:N contract match in
`VerifyCurveFamily`, and `CurveConversionContract.Derived`. Conversion
provenance (`Tool`, `ToolVersion`, `TargetPlatform`) is retained, and a family
whose artifact went through no converter leaves all three empty together.

`CurveFamilySharedContract` absorbs the four artifact-identity fields and gains
`EligibleBatches []int`. The hand-written members array becomes one integer
array. `internal/profiler/admission/curve_family.go` shrinks in proportion.

`VerifyCurveFamily` keeps its guard on the four derivation identities. It is the
boundary that mints a `CurveFamily`, so a blank state, environment, active
protocol, or idle protocol identity is rejected there.

The `CurveFamily` names are kept. They still read correctly as the family of
measurements composing one curve, and renaming them would be churn across
admission, derivation, documents, and tests. What is retired is the
fixed-artifact premise, retired by deleting ADR 0014.

ADR 0014 required one admitted idle baseline per fixed-batch member state and
recorded that a dynamic-batch artifact keeps one baseline. One artifact means
one state key, so `internal/batchsaturation/sequential.go:56` keeps planning a
single baseline. It takes the first declared batch, so the baseline Run carries
a batch the artifact declares.

## Capability boundary: source, freeze, and failure

The boundary moves from post-hoc inference to pre-Run resolution:

```text
first reconcile   resolve ModelSpec.BatchProfiles
                  freeze CurveFamilySharedContract, including EligibleBatches
                  write it to the evidence store
later reconciles  read the frozen record, which is now the authority
```

The declared profile set may be sparse. Which sizes an artifact carries is a
measurement decision that belongs in configuration, and a campaign is free to
declare a set that skips sizes it has no interest in.

The sweep therefore advances through the declared set in its own order, and the
capability boundary is reaching the end of that set. A gap inside the set is a
size the operator chose to skip, and the sweep steps over it.

`Observed` carries the eligible set, and the method asks whether an eligible
batch larger than the highest measured one exists. Deleted:
`capabilityReasonCode` and its uses
(`internal/batchsaturation/controller/projection.go:13-15,82,91`),
`Trial.CapabilityRejected` (`internal/batchsaturation/types.go:43`), the
boundary-inference loop (`internal/batchsaturation/lifecycle.go:25-31`), and
`Observed.CapabilityBoundary *int` itself.

Two consequences reach the derivation layer. A curve pass's batches follow the
eligible sequence (`internal/profilederivation/curve.go`), and a discovery
policy's `KMax` is compared against the largest eligible batch. Under a
contiguous domain starting at one those were the same number, which is why the
distinction stayed hidden.

A catalog that declares a profile its artifact was built without produces an
ordinary trial failure. The boundary is decided before the Run from the declared
set, so every Run that was created carried a batch that set contained; its
failure is retried and then terminal through the existing path at
`internal/batchsaturation/lifecycle.go:43-56`. The measurement ends as a
failure, which is what a configuration error is.

## Build pipeline

`build/model-artifacts/rknn/export_rknn.py:82` calls `set_fixed_batch()`, which
overwrites the ONNX symbolic batch axis with a constant immediately before
conversion. The converter itself destroyed the prerequisite `dynamic_input`
needs.

```python
# deleted: set_fixed_batch() (17-30), --batch-size, the temporary batch-fixed.onnx
parser.add_argument("--batch-profile", required=True, action="append", type=int)

non_batch = onnx_input_shape(model)[1:]      # read from the model
dynamic_input = [[[batch, *non_batch]] for batch in sorted(set(args.batch_profile))]
converter.config(target_platform=args.target_platform, dynamic_input=dynamic_input)
```

`load_onnx` receives the original ONNX. `Makefile`'s `RKNN_FIXED_BATCH` becomes
`RKNN_BATCH_PROFILES` and the target runs once per model.
`build/docker/derived-model-artifact.Dockerfile` keeps its single artifact copy,
which now covers every profile.

Per model, a sweep over N batches costs one conversion, one image build and
push, and one catalog execution path.

## Records

Every record is stored in the evidence store. Documents carry claims and
pointers only, continuing the direction of `30d7f5b` and `e787336`.

| Record | Key | Written |
|---|---|---|
| Conversion record | `model-artifacts/<modelSpec>/<artifact>/conversion.json` | first Run admission |
| Device probe | `model-artifacts/<modelSpec>/<artifact>/device-probe.json` | first Run admission |
| Frozen family contract | `batch-saturations/<uid>/family.json` | first reconcile |
| BatchSaturation result | `batch-saturations/<uid>/result.json` | measurement end |
| Run raw and execution evidence | `<uid>/raw`, `<uid>/execution` | unchanged |

The offline build runs outside the cluster, and the Profiler controller reads
Kubernetes objects rather than OCI image contents. The records therefore travel
inside the artifact image:

```dockerfile
COPY artifact /artifact/${ARTIFACT_NAME}
COPY conversion.json /artifact/conversion.json
COPY device-probe.json /artifact/device-probe.json
```

The device probe is a build step. Conversion declares the profiles; the probe
executes them once on the target device class and records which ones the runtime
accepted; the image is published after both records exist. The probe is the one
confirmation of a declared profile set available before measurement, and placing
it before publication means every artifact reaching the cluster carries a
checked claim. This plan's own probe, recorded above, is that step performed by
hand for the first such artifact.

The candidate report flows controller to worker: it is built by the controller
from the catalog (`candidate_resolver.go:111`), written to the intent record,
and projected into the Job as a file (`execution_job_spec.go:283`).

The runtime result is the path that flows worker to Profiler. The runner already
mounts the artifact directory, so it reads the two records from that mount and
emits them with its result, and the Profiler stores each under its
artifact-scoped key. `PutImmutable` returns `Existing` for an object already
present, so the record is written once per artifact however many Runs carry it.

This reuses the evidence path the Profiler already owns, and the record travels
bound to the artifact it describes. The cost is timing: the records are stored
at the artifact's first Run admission, the first moment the cluster can observe
them.

The conversion record's shape changes accordingly:

```json
"conversion": {
  "tool": "rknn-toolkit2", "version": "2.3.2", "targetPlatform": "rk3588",
  "batchProfiles": [ ... ],
  "nonBatchShape": [ ... ],
  "quantized": false
}
```

## Schema versions and preserved evidence

Removing `fixedBatch` from the artifact capability changes four wire contracts:

| Contract | From | To |
|---|---|---|
| `chill.dacs.io/modelcompat-report` | `v1alpha7` | `v1alpha8` |
| `chill.dacs.io/curve-family-contract` | `v1alpha2` | `v1alpha3` |
| `chill.dacs.io/rknnlite-runtime-request` | `v1alpha3` | `v1alpha4` |
| `chill.dacs.io/rknn-conversion-record` | `v1alpha2` | `v1alpha3` |

`internal/profilingrun/candidate.go:335` decodes on exact version match, so the
new code reads evidence produced under the new versions. The stored objects and
the recorded claims persist; re-derivation applies to evidence produced from
here on. This repository has accepted that trade before — the same reasoning
governed the `v1alpha3` to `v1alpha4` raise — and maintaining multi-version
decoders is the class of machinery this plan exists to remove. Where
re-derivation is genuinely needed, re-measurement produces better evidence.

The completed RKNN measurement is preserved. Its record in
`docs/design/profiling/README.md` is rewritten to attribute the result to the
premise under which it was obtained.

## What is deleted

**Code.** `FixedBatch` and `Shape` in `api/v1alpha1/modelspec_types.go:49-51,65`;
`catalog_facts.go:143`; `candidate.go:82,326,445,447`;
`worker/rknn_lite.go:210-211`; `runner.py:397-398`; the member machinery in
`internal/profilederivation/curve_family.go` and its adapter in
`internal/profiler/admission/curve_family.go`;
`batchsaturation/controller/projection.go:13-15,82,91`;
`batchsaturation/types.go:43`; `batchsaturation/lifecycle.go:25-31`;
`export_rknn.py:17-30,41,58-59,79-83` and its tests at
`test_export_rknn.py:42-69`; `Makefile:279,287`.

**Documents.** `docs/adr/0014-fixed-artifact-curve-composition.md` is deleted and
its number left vacant. The fixed-batch premise is removed from Plans 5, 5a, 6,
and 7. `docs/spec/` carries the estimand only and is untouched.

**Kept deliberately.** `SaturatedSerialFixedBatchWorkloadMode` describes the
workload — one Run holds its batch fixed and executes serially — which remains
true of the workload; renaming it would change the runtime contract string,
execution policies, and evidence.

## What survives

The six implemented commits of the BatchSaturation plan
(`766df85..92017c5`) are retained. Task 1 (`Repetitions` removal, schema raise),
Task 2 (the `BatchSaturation` API), Task 4 (`sequential-early-stop`), and Task 5
(admitted active trial) are untouched. Task 3 loses only the boundary-inference
loop; Task 6a loses only the three `capabilityReasonCode` sites.

The blocked reconciler task becomes implementable and smaller: the method's
capability plumbing disappears, and the frozen family write replaces it.
`batch-saturation-controller` gains `evidence-core` and `evidence-kube`
dependencies, updated in `.go-arch-lint.yml` in the same change per `AGENTS.md`.
No new production package is created.

## Verification

The two runtime unknowns are closed by measurement above, and check deletion
needs no test because the compared values merge into one field. What remains:

- Focused unit tests at changed sites only. The suite shrinks: the
  `set_fixed_batch` tests, the `CapabilityRejected` test at
  `internal/batchsaturation/lifecycle_test.go:98`, the curve-family member
  validation tests, and the candidate and worker `fixedBatch` tests are deleted;
  a `dynamic_input` construction test and an `EligibleBatches` boundary test are
  added. No new test category appears.
- Repository gates selected by changed surface, per `AGENTS.md`:
  `make manifests generate` for the API types, `make arch-check` for the
  component map, and `make lint` and `make test` before handoff. The Python
  runtime suite runs inside `make test`.
- e2e stays off the testbed kubeconfig.
- The first BatchSaturation measurement sweeps the declared profiles from one
  artifact, which is the end-to-end check, and its record is the validation
  record. No separate validation campaign is created.

## Consequences

A runtime that enumerates one batch per artifact would need a per-batch path
restored. Reintroducing it would be a new decision carrying its own evidence.

The declared profile set is a catalog claim about a built artifact. Execution
confirms it: the runtime refuses a batch its artifact was built without, and the
resulting failure is reported as a failure.
