# Batch-Profile Artifacts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One immutable artifact declares the set of batch sizes it can execute, so one execution path serves a whole batch sweep and the capability boundary becomes a catalog fact resolved before any Run is created.

**Architecture:** `ModelArtifactDerivation.FixedBatch int32` becomes `BatchProfiles []int32`, and the declared input contract drops its batch axis. Checks that compared two catalog declarations of the same fact lose their subject and are deleted; the one surviving batch check becomes a set-membership test at intent freeze. The per-batch curve-family member contract collapses into the shared contract.

**Tech Stack:** Go, kubebuilder/controller-runtime, CEL validation, Python 3 (`runtime/rknnlite`, `build/model-artifacts/rknn`), RKNN-Toolkit2 2.3.2.

**Design authority:** `docs/design/profiling/plans/08-batch-profile-artifacts.md`

## Global Constraints

- Verify once at a boundary, return a type only that boundary can construct, and do not re-validate it downstream (`AGENTS.md`).
- Do not add digest, byte-equality, or canonical re-encoding comparison as a validation gate.
- Values deciding what a Run measures belong in configuration, never Go constants.
- Do not give a struct a field that accepts only one value.
- Pure packages (`internal/profilederivation`, `internal/batchsaturation`) must not import `api/v1alpha1`, Kubernetes APIs, or controller-runtime.
- Every new production package must map to exactly one `.go-arch-lint.yml` component. This plan adds none.
- `make manifests generate` when API types change; `make arch-check` when a package or internal import changes; `make lint` and `make test` before handoff.
- Never run e2e against the testbed kubeconfig.
- The RKNNLite input layout is `NHWC` and the batch axis is the first dimension. The ONNX side is `NCHW`. Both remain true; do not unify them.

## Out of scope

Two follow-on plans depend on this one and must not be started inside it:

- Carrying the conversion and device-probe records into the evidence store.
- The `BatchSaturation` reconciler and its frozen family write.

---

### Task 1: Delete the checks that compare two catalog declarations

Four sites assert `inputShape[0] == fixedBatch`, comparing two fields of the same catalog record against each other. None can observe whether the artifact executes the batch. Task 2 merges those two fields, so these checks lose their subject; deleting them first keeps Task 2 to a type change.

The batch-eligibility check at `internal/profilingrun/candidate.go:445` is **not** deleted here. It transforms into a set-membership test in Task 2.

**Files:**
- Modify: `internal/profiler/controller/catalog_facts.go:140-146`
- Modify: `internal/profilingrun/candidate.go:443-451`
- Modify: `internal/profiler/worker/rknn_lite.go:209-215`
- Modify: `runtime/rknnlite/runner.py:374-405`, `:664-666`, `:371`
- Modify: `runtime/rknnlite/test_runner.py`

**Interfaces:**
- Consumes: nothing
- Produces: `validate_candidate(value)` in `runtime/rknnlite/runner.py` loses its `batch` parameter

- [ ] **Step 1: Write the failing runtime test**

Add to `runtime/rknnlite/test_runner.py`, inside the existing test class:

```python
    def test_validate_outputs_names_a_refused_profile(self):
        with self.assertRaisesRegex(RUNNER.ContractError, "does not provide this profile"):
            RUNNER.validate_outputs(None, 1)
```

Use whatever module alias the file already binds `runner.py` to; do not introduce a second alias.

- [ ] **Step 2: Run it to verify it fails**

```bash
python3 -m unittest discover -s runtime/rknnlite -p 'test_*.py'
```
Expected: FAIL. `validate_outputs(None, 1)` currently raises `ContractError("inference produced no outputs")`, which does not match.

- [ ] **Step 3: Name the refusal in `validate_outputs`**

RKNNLite returns `None` when the runtime refuses a batch profile. `if not outputs` already catches it, so this is a message change, not a new guard. In `runtime/rknnlite/runner.py:664-666`, replace:

```python
def validate_outputs(outputs: list[Any], batch: int) -> list[list[int]]:
    if not outputs:
        raise ContractError("inference produced no outputs")
```

with:

```python
def validate_outputs(outputs: list[Any], batch: int) -> list[list[int]]:
    if outputs is None:
        raise ContractError(
            "runtime supplied no result for the frozen batch; "
            "the artifact does not provide this profile"
        )
    if not outputs:
        raise ContractError("inference produced no outputs")
```

- [ ] **Step 4: Run it to verify it passes**

```bash
python3 -m unittest discover -s runtime/rknnlite -p 'test_*.py'
```
Expected: PASS.

- [ ] **Step 5: Drop the runtime's duplicate batch checks**

Batch eligibility is decided at intent freeze, and execution is proven by the output batch axis. In `runtime/rknnlite/runner.py`, change the signature and body of `validate_candidate`:

```python
def validate_candidate(value: Any) -> None:
```

Remove these two lines from its comparison block:

```python
        or capability["fixedBatch"] != batch
        or normalized_shape[0] != batch
```

The remaining comparison keeps `targetPlatform`, `inputLayout`, and `inputDataType`. Keep the loop that rejects a non-positive dimension. Update the error message so it no longer claims a batch relationship:

```python
        raise ContractError("candidate artifact capability is unsupported")
```

Update the call site at `runtime/rknnlite/runner.py:371`:

```python
    validate_candidate(request["candidate"])
```

- [ ] **Step 6: Delete the three Go equality checks**

`internal/profiler/controller/catalog_facts.go` — delete only this line from the RKNNLite condition:

```go
			artifact.Derivation.Input.Shape[0] != artifact.Derivation.FixedBatch ||
```

`internal/profilingrun/candidate.go` — delete only these two lines from the RKNNLite condition:

```go
			len(report.ArtifactCapability.InputShape) == 0 ||
			report.ArtifactCapability.InputShape[0] != report.ArtifactCapability.FixedBatch ||
```

`internal/profiler/worker/rknn_lite.go` — replace:

```go
	if capability.FixedBatch < 1 || len(capability.InputShape) == 0 ||
		capability.InputShape[0] != capability.FixedBatch ||
		capability.InputLayout != profilingrun.RKNNLiteInputLayout ||
```

with:

```go
	if len(capability.InputShape) == 0 ||
		capability.InputLayout != profilingrun.RKNNLiteInputLayout ||
```

- [ ] **Step 7: Remove the assertions that covered the deleted checks**

Search the Go and Python suites for tests whose only subject was a shape-versus-fixed-batch mismatch and delete those cases. Leave every test that covers layout, data type, target platform, dimension positivity, or the surviving batch check.

```bash
grep -rn "FixedBatch\|fixedBatch" --include='*_test.go' --include='test_*.py' internal/ runtime/
```

- [ ] **Step 8: Run the full gates**

```bash
make lint && make test
```
Expected: both pass.

- [ ] **Step 9: Commit**

```bash
git add internal/ runtime/
git commit -m "refactor: drop artifact batch checks that compare two declarations"
```

---

### Task 2: Replace the fixed batch with a declared batch-profile set

**Files:**
- Modify: `api/v1alpha1/modelspec_types.go:49-51,62-70`
- Modify: `internal/profilingrun/candidate.go:15,79-87,323-334,443-451`
- Modify: `internal/profiler/controller/candidate_report.go:109-125`
- Modify: `internal/profiler/controller/catalog_facts.go:140-155`
- Modify: `internal/profiler/worker/rknn_lite.go:18,209-215,230-240`
- Modify: `runtime/rknnlite/runner.py:374-405,953-958`
- Generated: `config/crd/bases/`, `charts/chill/templates/crds/`, `api/v1alpha1/zz_generated.deepcopy.go`

**Interfaces:**
- Consumes: `validate_candidate(value)` from Task 1
- Produces: `ModelArtifactDerivation.BatchProfiles []int32`; `ModelArtifactInputContract.NonBatchShape []int32`; `profilingrun.CandidateArtifactCapability{TargetPlatform string, BatchProfiles []int32, NonBatchShape []int32, InputLayout string, InputDataType string}`; `CandidateReportSchemaVersion = "chill.dacs.io/modelcompat-report.v1alpha8"`; `rknnLiteRequestSchemaVersion = "chill.dacs.io/rknnlite-runtime-request.v1alpha4"`

- [ ] **Step 1: Write the failing eligibility test**

Add to `internal/profilingrun/candidate_test.go`. The helper mirrors the fixture already used by `TestCandidateReportValidatesHostContractDelivery` at `:196-223`, varying only the declared profiles and the measured batch:

```go
func TestVerifyCandidateReportRejectsABatchOutsideTheDeclaredProfiles(t *testing.T) {
	_, err := verifyRKNNCandidateAtBatch(t, []int32{1, 2, 4}, 3)
	if err == nil {
		t.Fatal("expected an undeclared batch to be rejected")
	}
	if !strings.Contains(err.Error(), "supported profile") {
		t.Fatalf("error = %v, want it to name the unsupported profile", err)
	}
}

func TestVerifyCandidateReportAcceptsADeclaredProfile(t *testing.T) {
	if _, err := verifyRKNNCandidateAtBatch(t, []int32{1, 2, 4}, 4); err != nil {
		t.Fatalf("declared profile rejected: %v", err)
	}
}

func verifyRKNNCandidateAtBatch(
	t *testing.T, profiles []int32, batch int,
) (VerifiedCandidateReport, error) {
	t.Helper()
	report := completeCandidateReportForTest()
	contract := rknnHostContractForTest()
	report.HostContract = &contract
	report.ArtifactCapability = rknnArtifactCapabilityForTest()
	report.ArtifactCapability.BatchProfiles = profiles
	report.RuntimeAccess = &CandidateReportRuntimeAccess{
		Privileged: true, SupplementalGroups: []int64{44, 110},
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{
		Candidate: CandidateIdentity{
			Inputs: report.Inputs, ExecutionPath: report.Selection.ExecutionPath, Verdict: report.Verdict,
		},
		State: ExecutionState{
			DeviceClass: report.Inputs.DeviceClass.Name, Model: report.Inputs.ModelSpec.Name,
			Artifact:     report.Selection.Artifact,
			ArtifactPath: report.ArtifactSupply.Path, PowerMode: report.Selection.PowerMode,
			RuntimeFamily: "rknnlite", HostContract: cloneCandidateHostContract(report.HostContract),
		},
		Images: Images{
			Runtime: report.Inputs.RuntimeDeclaration.Image, Artifact: report.ArtifactSupply.Image,
			MetricObserver: report.MetricObserverImage,
		},
		TargetNode:  NodeSnapshot{Architecture: report.DeviceClassFacts.Architecture},
		Measurement: MeasurementContract{Batch: batch},
	}
	return VerifyCandidateReport(payload, intent)
}
```

Add `"strings"` to the test file's imports if it is not already there.

- [ ] **Step 2: Run it to verify it fails**

```bash
go test ./internal/profilingrun/ -run TestVerifyCandidateReport -v
```
Expected: FAIL to compile — `BatchProfiles` is undefined.

- [ ] **Step 3: Change the catalog API**

In `api/v1alpha1/modelspec_types.go`, replace the `FixedBatch` field of `ModelArtifactDerivation`:

```go
	// BatchProfiles enumerates every batch size this artifact can execute.
	// A runtime that consumes batch as a call argument declares no derivation
	// and therefore no profiles.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=1
	BatchProfiles []int32 `json:"batchProfiles"`
```

and replace `Shape` in `ModelArtifactInputContract`:

```go
	// NonBatchShape is the input tensor shape without its batch axis. The batch
	// axis is supplied by the selected profile, so a batch mismatch is not
	// representable here.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=1
	NonBatchShape []int32 `json:"nonBatchShape"`
```

- [ ] **Step 4: Change the candidate capability contract**

In `internal/profilingrun/candidate.go`, raise the schema constant and replace the capability type:

```go
	CandidateReportSchemaVersion = "chill.dacs.io/modelcompat-report.v1alpha8"
```

```go
// CandidateArtifactCapability carries only runtime-consumed facts of one
// derived artifact. Full conversion provenance remains in ModelSpec content.
type CandidateArtifactCapability struct {
	TargetPlatform string  `json:"targetPlatform"`
	BatchProfiles  []int32 `json:"batchProfiles"`
	NonBatchShape  []int32 `json:"nonBatchShape,omitempty"`
	InputLayout    string  `json:"inputLayout,omitempty"`
	InputDataType  string  `json:"inputDataType,omitempty"`
}
```

In `validateInternalCandidateReport`, replace the artifact block:

```go
	if report.ArtifactCapability != nil {
		if strings.TrimSpace(report.ArtifactCapability.TargetPlatform) == "" ||
			strings.TrimSpace(report.ArtifactCapability.TargetPlatform) != report.ArtifactCapability.TargetPlatform ||
			len(report.ArtifactCapability.BatchProfiles) == 0 {
			return errors.New("candidate report artifact target platform and batch profiles are required")
		}
		for _, profile := range report.ArtifactCapability.BatchProfiles {
			if profile < 1 {
				return errors.New("candidate report artifact batch profiles must be positive")
			}
		}
		for _, dimension := range report.ArtifactCapability.NonBatchShape {
			if dimension < 1 {
				return errors.New("candidate report artifact input shape dimensions must be positive")
			}
		}
	}
```

Update the fixture at `internal/profilingrun/candidate_test.go:275` to the new field names, so every existing caller keeps compiling:

```go
func rknnArtifactCapabilityForTest() *CandidateArtifactCapability {
	return &CandidateArtifactCapability{
		TargetPlatform: "rk3588", BatchProfiles: []int32{1}, NonBatchShape: []int32{224, 224, 3},
		InputLayout: RKNNLiteInputLayout, InputDataType: RKNNLiteInputDataType,
	}
}
```

- [ ] **Step 5: Turn the surviving batch check into set membership**

This is the capability boundary. `internal/profiler/preparation.go:203` calls `ValidateCandidateReport` during intent freeze, so it rejects an undeclared batch before the Run executes. In the RKNNLite block of `VerifyCandidateReport`, replace:

```go
			report.ArtifactCapability.FixedBatch != int32(intent.Measurement.Batch) ||
```

with nothing, and add a dedicated check with its own message immediately after that condition closes:

```go
		if !slices.Contains(report.ArtifactCapability.BatchProfiles, int32(intent.Measurement.Batch)) {
			return VerifiedCandidateReport{}, errors.New(
				"candidate RKNN artifact does not declare the measurement batch as a supported profile",
			)
		}
```

Add `"slices"` to the file's imports.

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go test ./internal/profilingrun/ -run TestVerifyCandidateReport -v
```
Expected: PASS.

- [ ] **Step 7: Follow the compiler through the remaining Go sites**

```bash
go build ./... 2>&1 | head -40
```

Fix each break with the corresponding rename, changing nothing else:

- `internal/profiler/controller/candidate_report.go` — `reportArtifactCapability` sets `BatchProfiles: append([]int32(nil), derivation.BatchProfiles...)` and `capability.NonBatchShape = append([]int32(nil), derivation.Input.NonBatchShape...)`.
- `internal/profiler/controller/catalog_facts.go` — the RKNNLite condition reads `len(artifact.Derivation.Input.NonBatchShape) == 0` and its dimension loop iterates `NonBatchShape`. Add `len(artifact.Derivation.BatchProfiles) == 0` to that condition so a derived RKNNLite artifact must declare at least one profile.
- `internal/profiler/worker/rknn_lite.go` — raise `rknnLiteRequestSchemaVersion` to `"chill.dacs.io/rknnlite-runtime-request.v1alpha4"`; the delivery check reads `len(capability.NonBatchShape) == 0`; wherever the adapter previously passed `InputShape`, pass `append([]int32{int32(batch)}, capability.NonBatchShape...)`.

- [ ] **Step 8: Change the runtime contract**

In `runtime/rknnlite/runner.py`, `validate_candidate` requires the new field names:

```python
    require_fields(
        capability,
        {"targetPlatform", "batchProfiles", "nonBatchShape", "inputLayout", "inputDataType"},
        "candidate.artifactCapability",
    )
    profiles = capability["batchProfiles"]
    if not isinstance(profiles, list) or not profiles:
        raise ContractError("candidate batchProfiles must be a non-empty array")
    for profile in profiles:
        require_integer(profile, "candidate batchProfiles item", 1)
    shape = capability["nonBatchShape"]
    if not isinstance(shape, list) or not shape:
        raise ContractError("candidate nonBatchShape must be a non-empty array")
    normalized_shape = [
        require_integer(value, "candidate nonBatchShape item", 1) for value in shape
    ]
```

At `runtime/rknnlite/runner.py:957`, compose the full shape from the frozen batch:

```python
        shape = [measurement["batch"], *candidate["artifactCapability"]["nonBatchShape"]]
```

Update the corresponding fixtures in `runtime/rknnlite/test_runner.py`.

- [ ] **Step 9: Regenerate and inspect the drift**

```bash
make manifests generate
git diff --stat config/crd/bases charts/chill/templates/crds api/v1alpha1/zz_generated.deepcopy.go
```
Expected: `fixedBatch` and `shape` replaced by `batchProfiles` and `nonBatchShape` in both CRD copies, and the deepcopy functions gaining slice copies. Nothing else.

- [ ] **Step 10: Full gates**

```bash
make lint && make test && make arch-check
```
Expected: all pass.

- [ ] **Step 11: Commit**

```bash
git add api/ internal/ runtime/ config/ charts/
git commit -m "feat: declare a batch-profile set per artifact"
```

---

### Task 3: Collapse the curve-family member contract

One artifact per family leaves a member with nothing batch-varying to hold: four of its seven fields become shared, two become derived, one is the batch itself.

**Files:**
- Modify: `internal/profilederivation/curve_family.go`
- Modify: `internal/profilederivation/curve_family_test.go`
- Modify: `internal/profiler/admission/curve_family.go`
- Modify: `internal/profiler/admission/curve_family_test.go`
- Modify: `internal/profilederivation/curve.go` call sites the compiler reports

**Interfaces:**
- Consumes: nothing from Tasks 1-2
- Produces: `CurveFamilySharedContract` with `Artifact CurveArtifactContract` and `EligibleBatches []int`; `FreezeCurveFamilyContract(shared CurveFamilySharedContract) (CurveFamilyContract, error)`; `CurveFamilyContract.EligibleBatches() []int`; `VerifyCurveFamily(contract CurveFamilyContract, verified VerifiedCurveFamilyState) (CurveFamily, error)`

- [ ] **Step 1: Write the failing contract test**

Replace the member-oriented freeze test in `internal/profilederivation/curve_family_test.go` with:

```go
func TestFreezeCurveFamilyContractCarriesTheEligibleBatches(t *testing.T) {
	shared := validSharedContract()
	shared.EligibleBatches = []int{4, 1, 2}

	contract, err := FreezeCurveFamilyContract(shared)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}

	got := contract.EligibleBatches()
	if !reflect.DeepEqual(got, []int{1, 2, 4}) {
		t.Fatalf("eligible batches = %v, want sorted [1 2 4]", got)
	}
}

func TestFreezeCurveFamilyContractRejectsAnEmptyEligibleSet(t *testing.T) {
	shared := validSharedContract()
	shared.EligibleBatches = nil

	if _, err := FreezeCurveFamilyContract(shared); err == nil {
		t.Fatal("expected an empty eligible batch set to be rejected")
	}
}
```

Add `validSharedContract()` to that file, returning a fully populated `CurveFamilySharedContract` including `Artifact` and `Tensor`.

- [ ] **Step 2: Run it to verify it fails**

```bash
go test ./internal/profilederivation/ -run TestFreezeCurveFamilyContract -v
```
Expected: FAIL to compile — `EligibleBatches` is undefined and `FreezeCurveFamilyContract` takes two arguments.

- [ ] **Step 3: Move the artifact facts into the shared contract**

In `internal/profilederivation/curve_family.go`, add:

```go
// CurveArtifactContract binds the one immutable artifact every batch of this
// family executes.
type CurveArtifactContract struct {
	ExecutionPath string `json:"executionPath"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Image         string `json:"image"`
}
```

Add two fields to `CurveFamilySharedContract`:

```go
	Artifact        CurveArtifactContract `json:"artifact"`
	EligibleBatches []int                 `json:"eligibleBatches"`
```

Delete `CurveConversionContract.Derived`. Keep `TargetPlatform`, `Tool`, and `ToolVersion`, and make them unconditional rather than `omitempty`-on-derived.

Raise the schema constant:

```go
	curveFamilyContractSchemaVersion = "chill.dacs.io/curve-family-contract.v1alpha3"
```

- [ ] **Step 4: Delete the member machinery**

Delete from `internal/profilederivation/curve_family.go`:

`CurveFamilyMemberContract`, `curveFamilyContractRecord.Members`,
`CurveFamilyContract.Members`, `VerifiedCurveFamilyMember`,
`CurveFamily.members`, `CurveFamily.Members`, `validateCurveFamilyMember`,
`cloneCurveFamilyMembers`, `cloneVerifiedCurveFamilyMember`, and
`cloneVerifiedCurveFamilyMembers`.

Replace the freeze and verify entry points:

```go
func FreezeCurveFamilyContract(shared CurveFamilySharedContract) (CurveFamilyContract, error) {
	shared = cloneCurveFamilyShared(shared)
	shared.EligibleBatches = append([]int(nil), shared.EligibleBatches...)
	sort.Ints(shared.EligibleBatches)
	if err := validateCurveFamilyShared(shared); err != nil {
		return CurveFamilyContract{}, err
	}
	record := curveFamilyContractRecord{
		SchemaVersion: curveFamilyContractSchemaVersion,
		Shared:        shared,
	}
	return CurveFamilyContract{record: record}, nil
}

func (c CurveFamilyContract) EligibleBatches() []int {
	return append([]int(nil), c.record.Shared.EligibleBatches...)
}
```

```go
// VerifiedCurveFamilyState is the plain projection made only after active and
// idle frozen evidence have crossed their Loader boundaries. One artifact means
// one state, so there is one projection per family.
type VerifiedCurveFamilyState struct {
	Shared                 CurveFamilySharedContract `json:"shared"`
	StateIdentity          string                    `json:"stateIdentity"`
	EnvironmentIdentity    string                    `json:"environmentIdentity"`
	ActiveProtocolIdentity string                    `json:"activeProtocolIdentity"`
	IdleProtocolIdentity   string                    `json:"idleProtocolIdentity"`
}

func VerifyCurveFamily(
	contract CurveFamilyContract,
	verified VerifiedCurveFamilyState,
) (CurveFamily, error) {
	if err := contract.requireFrozen(); err != nil {
		return CurveFamily{}, err
	}
	if !reflect.DeepEqual(verified.Shared, contract.record.Shared) {
		return CurveFamily{}, errors.New("verified curve-family state does not match the contract")
	}
	return CurveFamily{contract: contract, state: verified}, nil
}
```

`CurveFamily` holds `contract CurveFamilyContract` and `state VerifiedCurveFamilyState`. Keep `Name()` and `EnvironmentIdentity()`, reading the latter from `state.EnvironmentIdentity`.

Extend `validateCurveFamilyShared` to reject an empty `EligibleBatches`, a non-positive or duplicate batch, and an `Artifact` with any empty field.

- [ ] **Step 5: Follow the compiler through the adapter and curve**

```bash
go build ./... 2>&1 | head -40
```

`internal/profiler/admission/curve_family.go` projects one verified state instead of a member list. `internal/profilederivation/curve.go` keys its admitted idle baselines by the single `StateIdentity` rather than per member. Make no behavior change beyond removing the per-member dimension.

- [ ] **Step 6: Run to verify the contract tests pass**

```bash
go test ./internal/profilederivation/ ./internal/profiler/admission/ -v
```
Expected: PASS.

- [ ] **Step 7: Full gates**

```bash
make lint && make test && make arch-check
```
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/
git commit -m "refactor: collapse the curve family onto one artifact"
```

---

### Task 4: Read the capability boundary from the eligible batch set

`Trial.CapabilityRejected` is set from a condition reason no component emits, so the boundary is unreachable. It becomes an input to the snapshot instead of an inference from trial outcomes.

**Files:**
- Modify: `internal/batchsaturation/types.go:43,64`
- Modify: `internal/batchsaturation/lifecycle.go:18-41`
- Modify: `internal/batchsaturation/lifecycle_test.go:98-109`
- Modify: `internal/batchsaturation/controller/projection.go:13-15,19-22,73-107`
- Modify: `internal/batchsaturation/controller/projection_test.go`

**Interfaces:**
- Consumes: nothing from Tasks 1-3
- Produces: `batchsaturation.Snapshot` with `EligibleBatches []int`; `controller.ProjectSnapshot(parent *edgev1alpha1.BatchSaturation, runs []edgev1alpha1.ProfilingRun, eligibleBatches []int) (batchsaturation.Snapshot, error)`

- [ ] **Step 1: Write the failing boundary test**

Replace `TestDecideReportsCapabilityBoundaryToTheMethod` in `internal/batchsaturation/lifecycle_test.go` with:

```go
func TestDecideReportsTheBoundaryFromTheEligibleBatches(t *testing.T) {
	method := &stubMethod{plan: Plan{Result: &Result{Outcome: "Stopped/CapabilityBoundary"}}}
	snapshot := Snapshot{
		RetryLimit:      1,
		EligibleBatches: []int{1, 2},
		Trials: []Trial{
			{Measurement: idle(1), Attempt: 1, Verdict: VerdictAccepted},
			{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
			{Measurement: active(2), Attempt: 1, Verdict: VerdictAccepted},
		},
	}

	if _, err := Decide(snapshot, method); err != nil {
		t.Fatalf("decide: %v", err)
	}

	if method.seen.CapabilityBoundary == nil || *method.seen.CapabilityBoundary != 3 {
		t.Fatalf("capability boundary = %v, want 3", method.seen.CapabilityBoundary)
	}
}
```

The expected value is 3: the smallest batch above the contiguous eligible prefix.

- [ ] **Step 2: Run it to verify it fails**

```bash
go test ./internal/batchsaturation/ -run TestDecideReports -v
```
Expected: FAIL to compile — `Snapshot.EligibleBatches` is undefined.

- [ ] **Step 3: Change the pure types**

In `internal/batchsaturation/types.go`, delete `Trial.CapabilityRejected` and add to `Snapshot`:

```go
	// EligibleBatches is the frozen set of batch sizes the selected artifact
	// declares. A batch outside it cannot be measured and is the capability
	// boundary.
	EligibleBatches []int
```

Extend `Snapshot.Validate()` to require a non-empty `EligibleBatches` with positive, strictly increasing values.

- [ ] **Step 4: Compute the boundary in `Decide`**

In `internal/batchsaturation/lifecycle.go`, delete the `trial.CapabilityRejected` branch from the first loop, and delete `|| trial.CapabilityRejected` from the retry loop's guard. Set the boundary from the eligible set before the loops:

```go
	boundary := firstIneligibleBatch(snapshot.EligibleBatches)
	observed := Observed{CapabilityBoundary: &boundary}
```

```go
// firstIneligibleBatch returns the smallest batch above the contiguous prefix
// of the eligible set. A sweep advances by one batch at a time, so that is the
// first batch the sweep can reach and the artifact cannot execute. The set is
// finite, so this batch always exists.
func firstIneligibleBatch(eligible []int) int {
	expected := 1
	for _, batch := range eligible {
		if batch != expected {
			break
		}
		expected++
	}
	return expected
}
```

- [ ] **Step 5: Run to verify it passes**

```bash
go test ./internal/batchsaturation/ -v
```
Expected: PASS.

- [ ] **Step 6: Delete the unreachable reason code from the projection**

In `internal/batchsaturation/controller/projection.go`, delete the `capabilityReasonCode` constant and its doc comment, delete the `trial.CapabilityRejected` assignment, and delete `capabilityReasonCode` from the `isTerminalReason` switch. Add the eligible set as a projection input:

```go
func ProjectSnapshot(
	parent *edgev1alpha1.BatchSaturation,
	runs []edgev1alpha1.ProfilingRun,
	eligibleBatches []int,
) (batchsaturation.Snapshot, error) {
	if parent == nil {
		return batchsaturation.Snapshot{}, errors.New("a parent BatchSaturation is required")
	}
	snapshot := batchsaturation.Snapshot{
		RetryLimit:      int(parent.Spec.RetryLimit),
		EligibleBatches: append([]int(nil), eligibleBatches...),
	}
```

Update `internal/batchsaturation/controller/projection_test.go` to pass an eligible set to every call.

- [ ] **Step 7: Run and gate**

```bash
go test ./internal/batchsaturation/... -v && make lint && make test
```
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/batchsaturation
git commit -m "refactor: resolve the capability boundary from the eligible batches"
```

---

### Task 5: Convert one artifact for every batch profile

`set_fixed_batch` overwrites the ONNX symbolic batch axis immediately before conversion, destroying the prerequisite `dynamic_input` needs.

**Files:**
- Modify: `build/model-artifacts/rknn/export_rknn.py`
- Modify: `build/model-artifacts/rknn/test_export_rknn.py`
- Modify: `Makefile:273-289`

**Interfaces:**
- Consumes: nothing
- Produces: `export_rknn.py --batch-profile` (repeatable); conversion record schema `chill.dacs.io/rknn-conversion-record.v1alpha3` with `batchProfiles` and `nonBatchShape`

- [ ] **Step 1: Write the failing build test**

Replace both `set_fixed_batch` tests in `build/model-artifacts/rknn/test_export_rknn.py` with:

```python
    def test_dynamic_input_enumerates_sorted_unique_profiles(self):
        self.assertEqual(
            EXPORT_RKNN.build_dynamic_input([3, 224, 224], [4, 1, 2, 1]),
            [[[1, 3, 224, 224]], [[2, 3, 224, 224]], [[4, 3, 224, 224]]],
        )

    def test_dynamic_input_rejects_a_non_positive_profile(self):
        with self.assertRaisesRegex(ValueError, "positive"):
            EXPORT_RKNN.build_dynamic_input([3, 224, 224], [1, 0])

    def test_non_batch_shape_drops_the_leading_axis(self):
        self.assertEqual(EXPORT_RKNN.non_batch_shape(["batch", 3, 224, 224]), [3, 224, 224])

    def test_non_batch_shape_rejects_a_concrete_batch_axis(self):
        with self.assertRaisesRegex(ValueError, "symbolic"):
            EXPORT_RKNN.non_batch_shape([1, 3, 224, 224])
```

The `FakeDimension`, `FakeTensorType`, and `fake_value_info` helpers exist only for `set_fixed_batch` and are deleted with it.

- [ ] **Step 2: Run it to verify it fails**

```bash
python3 -m unittest discover -s build/model-artifacts/rknn -p 'test_*.py'
```
Expected: FAIL — `build_dynamic_input` and `non_batch_shape` are undefined.

- [ ] **Step 3: Replace the conversion entry points**

In `build/model-artifacts/rknn/export_rknn.py`, delete `set_fixed_batch` and add:

```python
def non_batch_shape(shape):
    if not shape or len(shape) < 2:
        raise ValueError("input shape must carry a batch axis and at least one more")
    if isinstance(shape[0], int):
        raise ValueError("input batch axis must be symbolic, not a concrete size")
    for dimension in shape[1:]:
        if not isinstance(dimension, int) or dimension < 1:
            raise ValueError("non-batch dimensions must be positive integers")
    return list(shape[1:])


def build_dynamic_input(non_batch, profiles):
    if not profiles:
        raise ValueError("at least one batch profile is required")
    for profile in profiles:
        if profile <= 0:
            raise ValueError("batch profiles must be positive")
    return [[[profile, *non_batch]] for profile in sorted(set(profiles))]
```

Change the argument:

```python
    parser.add_argument("--batch-profile", required=True, action="append", type=int)
```

Delete the `args.batch_size <= 0` guard; `build_dynamic_input` owns that rejection.

- [ ] **Step 4: Convert from the original ONNX**

Replace the conversion block so it reads the shape from the model and never rewrites it:

```python
        model = onnx.load(str(source))
        graph_input = model.graph.input[0].type.tensor_type.shape.dim
        declared = [
            dimension.dim_param or dimension.dim_value for dimension in graph_input
        ]
        non_batch = non_batch_shape(declared)
        dynamic_input = build_dynamic_input(non_batch, args.batch_profile)

        converter = RKNN(verbose=False)
        try:
            if converter.config(
                target_platform=args.target_platform, dynamic_input=dynamic_input
            ) != 0:
                raise SystemExit("RKNN config failed")
            if converter.load_onnx(model=str(source)) != 0:
                raise SystemExit("RKNN ONNX load failed")
```

The temporary directory is still needed for `temporary_output`; the `batch-fixed.onnx` path inside it is not.

- [ ] **Step 5: Raise the conversion record**

```python
RECORD_SCHEMA = "chill.dacs.io/rknn-conversion-record.v1alpha3"
```

In the record's `conversion` object, replace `"fixedBatch": args.batch_size` with:

```python
            "batchProfiles": sorted(set(args.batch_profile)),
            "nonBatchShape": non_batch,
```

- [ ] **Step 6: Run to verify the tests pass**

```bash
python3 -m unittest discover -s build/model-artifacts/rknn -p 'test_*.py'
```
Expected: PASS.

- [ ] **Step 7: Change the Makefile target**

In `Makefile`, rename the help text to "Build one RKNN artifact covering every declared batch profile", replace `test -n "$(RKNN_FIXED_BATCH)"` with `test -n "$(RKNN_BATCH_PROFILES)"`, and replace the `--batch-size` argument with a repeated flag:

```make
		$(foreach profile,$(RKNN_BATCH_PROFILES),--batch-profile "$(profile)") \
```

- [ ] **Step 8: Full gates**

```bash
make lint && make test
```
Expected: both pass. `make test` runs the build-tool suite.

- [ ] **Step 9: Commit**

```bash
git add build/ Makefile
git commit -m "feat: convert one RKNN artifact for every batch profile"
```

---

### Task 6: Retire the fixed-artifact documents

**Files:**
- Delete: `docs/adr/0014-fixed-artifact-curve-composition.md`
- Create: `docs/adr/0016-batch-profile-artifact-capability.md`
- Modify: `docs/design/profiling/README.md:175-186`
- Modify: `docs/design/profiling/plans/05-batch-saturation-campaign.md`
- Modify: `docs/design/profiling/plans/05a-sequential-batch-stop.md`
- Modify: `docs/design/profiling/plans/06-execution-support-expansion.md`
- Modify: `docs/design/profiling/plans/07-batch-saturation-control-path.md`

**Interfaces:**
- Consumes: the decisions implemented in Tasks 1-5
- Produces: nothing consumed by code

- [ ] **Step 1: Write the replacement ADR**

Create `docs/adr/0016-batch-profile-artifact-capability.md` following the section order of the existing ADRs — `# 0016 Batch-Profile Artifact Capability`, then `## Status` (`Accepted. Supersedes and replaces 0014, which is deleted.`), `## Context`, `## Decision`, `## Consequences`.

Context states the measured facts: `export_rknn.py` overwrote the ONNX symbolic batch axis before conversion, RKNN-Toolkit2 2.3.2 enumerates several batch shapes into one artifact through `dynamic_input`, and an RK3588 probe executed batches 1, 2, and 4 from one artifact while the runtime refused an undeclared batch 3. Decision states that a derived artifact declares `BatchProfiles`, that one execution path serves every profile, that the capability boundary is resolved at intent freeze, and that one artifact means one state key and therefore one idle baseline. Consequences state that a runtime unable to enumerate profiles at conversion time would need a new decision with its own evidence, and that a declared profile set the artifact does not honour surfaces as a trial failure rather than a boundary.

- [ ] **Step 2: Delete ADR 0014 and leave its number vacant**

```bash
git rm docs/adr/0014-fixed-artifact-curve-composition.md
grep -rn "0014" docs/ --include='*.md'
```

Every hit must be rewritten to reference 0016 or dropped. Do not renumber 0015.

- [ ] **Step 3: Rewrite the recorded result without deleting it**

In `docs/design/profiling/README.md:175-186`, keep the completed measurement and its outcome. Replace "two immutable RKNN fixed-batch rows" with wording that attributes the rows to the premise they were obtained under, and note that the batch-three capability boundary was a property of the per-batch artifacts then in use, not of the RKNN runtime. Replace the `ADR 0014` reference with `ADR 0016`.

- [ ] **Step 4: Remove the premise from Plans 5, 5a, 6, and 7**

```bash
grep -rn "fixed-batch\|fixed artifact\|fixedBatch\|per-batch artifact\|member map" docs/design/profiling/plans/
```

Rewrite each hit so it describes one artifact declaring a profile set. In Plan 5a, the capability boundary is a batch outside the frozen eligible set, decided before the batch is measured. Do not change the estimand, the acceptance rule, or the sizing method in Plans 5 and 5a.

- [ ] **Step 5: Verify no stale reference survives**

```bash
grep -rn "fixedBatch\|FixedBatch\|fixed-artifact\|0014" docs/ api/ internal/ runtime/ build/ Makefile
```
Expected: no hits.

- [ ] **Step 6: Commit**

```bash
git add docs/
git commit -m "docs: retire the fixed-artifact curve composition decision"
```

---

## Deferred, with the reason each is safe

- **Conversion and device-probe records reaching the evidence store.** They are written as files today and continue to be. The carrier is the runtime result, which is a separate contract change with its own storage keys; folding it in here would mix a premise correction with an evidence-path change.
- **The `BatchSaturation` reconciler.** Task 4 leaves `ProjectSnapshot` taking the eligible set as a parameter, so the reconciler supplies it from the frozen family. Nothing in this plan wires the controller up, and nothing in this plan depends on it.
- **Removing `SaturatedSerialFixedBatchWorkloadMode`.** Its name describes the workload holding batch fixed within one Run, which stays true. Renaming it would change the runtime contract string, execution policies, and evidence for no behavioral gain.
