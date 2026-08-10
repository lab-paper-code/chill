# Plan 8: Batch-Profile Artifacts

> Navigation: [Profiling design index](../README.md)
>
> Method: [Plan 5a](05a-sequential-batch-stop.md), [Plan 5](05-batch-saturation-campaign.md)
>
> Control path: [Plan 7](07-batch-saturation-control-path.md)

## Status

Approved design, not yet implemented. It replaces the premise beneath Plan 7 and
retires ADR 0014. Plans 5 and 5a keep the estimand, acceptance rule, and sizing
method unchanged.

## Question

> CHILL assumes one immutable artifact encodes exactly one batch size. Is that a
> property of the runtime, or a property of CHILL's own conversion step?

It is CHILL's own. The assumption is false for RKNN, and correcting it removes
the manual per-batch apparatus built to accommodate it.

## What this plan does

One artifact declares the set of batch sizes it can execute. One execution path
serves a whole batch sweep. The capability boundary becomes a catalog fact known
before any Run is created, rather than a runtime condition inferred after one
fails.

Nothing new is introduced. The batch-invariant tensor truth already exists one
layer down as `CurveTensorContract.NonBatchShape`; this plan promotes it to the
catalog and deletes every place that restated it per batch.

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

1. The Lite API selects the profile from the input array shape. No
   `data_format` argument and no `set_input_shapes` call are needed, so the
   runner's inference calls need no change.
2. `init_runtime(core_mask=NPU_CORE_0)` works against a dynamic artifact.
3. An undeclared batch cannot execute. The capability boundary is an observed
   runtime property, not an assumption.

The runtime reports its shapes as NHWC while the framework layout is NCHW. That
asymmetry already exists in CHILL and is preserved, not introduced.

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

The capability boundary is `BatchProfiles` being present and not containing the
requested batch. It is resolved from the catalog, before a Run exists.

`ProfilingRunSpec` is unchanged: one Run measures one batch.
`BatchSaturationSpec.State` is unchanged: one candidate now covers a whole
sweep, which is what its shape already assumed.

## Verification collapses to execution evidence

Removing the batch axis from the declared shape makes a batch mismatch
unrepresentable rather than merely detected. Five checks lose their subject:

| Deleted | Why it has nothing left to compare |
|---|---|
| `internal/profiler/controller/catalog_facts.go:143` | `Input.Shape[0]` and `Derivation.FixedBatch` merge into one field |
| `internal/profilingrun/candidate.go:447` | same |
| `internal/profiler/worker/rknn_lite.go:210-211` | same |
| `runtime/rknnlite/runner.py:397-398` | same |
| `internal/profilederivation/curve_family.go:339` | per-member tensor contract is gone |

Each compared two catalog declarations against each other. None could observe
whether the artifact actually executes the batch. Two checks that observe
execution remain, and they are stronger than the five they replace:

```python
# runtime/rknnlite/runner.py:665 — the runtime supplied no result for this batch
if not outputs:
    raise ContractError("inference produced no outputs")

# runtime/rknnlite/runner.py:670 — the executed batch was preserved
if output.size < 1 or not shape or shape[0] != batch:
    raise ContractError("inference output shape does not preserve the frozen batch")
```

Both already exist, and no new guard is required. RKNNLite swallows a refused
profile internally and returns `None`, which `validate_outputs` catches on its
first line because `None` is falsy. The refusal therefore already surfaces as a
typed `ContractError` rather than a crash.

What the probe does justify is naming the cause. "inference produced no outputs"
describes the symptom of a refused profile, not the refusal, and a misbuilt
artifact is the one case that reaches this path. Distinguishing the two is a
diagnosability change to an existing check, not a new validation layer.

This satisfies `AGENTS.md` — "verify once at that boundary, and do not
re-validate it downstream" — by removing a standing violation rather than by
adding a rule.

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
provenance (`Tool`, `ToolVersion`, `TargetPlatform`) is retained.

`CurveFamilySharedContract` absorbs the four artifact-identity fields and gains
`EligibleBatches []int`. The hand-written members array becomes one integer
array. `internal/profiler/admission/curve_family.go` shrinks in proportion.

The `CurveFamily` names are kept. They still read correctly as the family of
measurements composing one curve, and renaming them would be churn across
admission, derivation, documents, and tests. What is retired is the
fixed-artifact premise, and that is retired by deleting ADR 0014.

ADR 0014 required one admitted idle baseline per fixed-batch member state and
recorded that a dynamic-batch artifact keeps one baseline. One artifact means
one state key, so the single baseline is correct and
`internal/batchsaturation/sequential.go:56` needs no change.

## Capability boundary: source, freeze, and failure

The boundary moves from post-hoc inference to pre-Run resolution:

```text
first reconcile   resolve ModelSpec.BatchProfiles
                  freeze CurveFamilySharedContract, including EligibleBatches
                  write it to the evidence store
later reconciles  read the frozen record; never re-read the catalog
```

`Observed.CapabilityBoundary` and the stop decision at
`internal/batchsaturation/sequential.go:86` are unchanged; only the value's
source changes. Deleted: `capabilityReasonCode` and its uses
(`internal/batchsaturation/controller/projection.go:13-15,82,91`),
`Trial.CapabilityRejected` (`internal/batchsaturation/types.go:43`), and the
boundary-inference loop (`internal/batchsaturation/lifecycle.go:25-31`).

A declared profile set that the artifact does not honour — declared
`[1,2,4,8,16]`, built `[1,2]` — is **not** a capability boundary. The boundary is
decided before the Run; a Run that was created had an eligible batch. Its
failure is an ordinary trial failure, retried and then terminal via the existing
path at `internal/batchsaturation/lifecycle.go:43-56`. Promoting it to a
boundary would let a misbuilt artifact end a measurement as a clean stop.

## Build pipeline

`build/model-artifacts/rknn/export_rknn.py:82` calls `set_fixed_batch()`, which
overwrites the ONNX symbolic batch axis with a constant immediately before
conversion. The prerequisite for `dynamic_input` was being destroyed by the
converter itself.

```python
# deleted: set_fixed_batch() (17-30), --batch-size, the temporary batch-fixed.onnx
parser.add_argument("--batch-profile", required=True, action="append", type=int)

non_batch = onnx_input_shape(model)[1:]      # read from the model, not declared by hand
dynamic_input = [[[batch, *non_batch]] for batch in sorted(set(args.batch_profile))]
converter.config(target_platform=args.target_platform, dynamic_input=dynamic_input)
```

`load_onnx` receives the original ONNX. `Makefile`'s `RKNN_FIXED_BATCH` becomes
`RKNN_BATCH_PROFILES` and the target runs once per model.
`build/docker/derived-model-artifact.Dockerfile` needs no change to its artifact
copy: one file still, now covering every profile.

Per model, the cost of a five-batch sweep falls from five conversions, five
image builds and pushes, and five catalog execution paths to one of each.

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

The offline build cannot reach the store, and the Profiler controller cannot
read an OCI image. The record therefore travels inside the artifact image:

```dockerfile
COPY artifact /artifact/${ARTIFACT_NAME}
COPY conversion.json /artifact/conversion.json
COPY device-probe.json /artifact/device-probe.json
```

The device probe is a build step, not a separate campaign. Conversion declares
the profiles; the probe executes them once on the target device class and
records which ones the runtime actually accepted; the image is published only
after both records exist. The probe is the only confirmation of a declared
profile set that is possible before measurement, and placing it before
publication means an artifact cannot reach the cluster carrying an unchecked
claim. This plan's own probe, recorded above, is that step performed by hand for
the first such artifact.

The measurement worker already mounts the artifact and already reports
`CandidateReportArtifactSupply`. Carrying the record in that report lets the
Profiler store it through the evidence path it already owns — no ModelSpec
controller, no new API, no credentials in the build path. The record cannot be
separated from the artifact it describes. The cost is timing: it is stored at
the artifact's first Run admission rather than at ModelSpec admission, which is
the first moment the cluster can observe it.

The conversion record's shape changes accordingly:

```json
"conversion": {
  "tool": "rknn-toolkit2", "version": "2.3.2", "targetPlatform": "rk3588",
  "batchProfiles": [1, 2, 4, 8, 16],
  "nonBatchShape": [3, 224, 224],
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

`internal/profilingrun/candidate.go:335` decodes on exact version match, so
evidence produced under the current versions cannot be re-derived by the new
code. The stored objects and the recorded claims persist; re-derivation does
not. This repository has accepted that trade before — the same reasoning
governed the `v1alpha3` to `v1alpha4` raise — and maintaining multi-version
decoders is the class of machinery this plan exists to remove. Where
re-derivation is genuinely needed, re-measurement produces better evidence.

The completed RKNN measurement is preserved. Its record in
`docs/design/profiling/README.md` is rewritten to attribute the result to the
premise under which it was obtained, not deleted.

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
and 7. `docs/spec/` contains no fixed-batch premise and is untouched.

**Kept deliberately.** `SaturatedSerialFixedBatchWorkloadMode` describes the
workload — one Run holds its batch fixed and executes serially — which remains
true and is unrelated to artifact capability; renaming it would change the
runtime contract string, execution policies, and evidence for no gain.

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
- No e2e against the testbed kubeconfig.
- No separate validation campaign. The first BatchSaturation measurement sweeps
  batch 1, 2, 4 from one artifact, which is the end-to-end check; its record is
  the validation record.

## Consequences

A runtime that genuinely cannot enumerate batch profiles at conversion time
would need a per-batch path restored. No such runtime is in CHILL's support
matrix, and reintroducing it would be a new decision with its own evidence
rather than a regression to this one.

The declared profile set is a catalog claim about a built artifact. Nothing
before execution can confirm it. That gap is closed at the only place it can be
closed — the runtime refusing the batch — and the resulting failure is reported
as a failure rather than absorbed as a boundary.
