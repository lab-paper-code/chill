# BatchSaturation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One declared `BatchSaturation` object drives a whole `b_sat` measurement — idle baselines, batch sweep, derivation, typed result — with no human step between Runs, for both artifact kinds.

**Architecture:** A namespaced `BatchSaturation` CRD owns the `ProfilingRun` objects it creates. A person declares what to characterize; the controller resolves which execution path measures which batch from the catalog, freezes that map, and never asks again. Each reconcile lists the owned Runs, projects them into a pure `Snapshot` carrying the frozen map, and calls `Decide`, which handles shared progress rules and delegates the measurement question to a named method module. Nothing authoritative lives in status or in process memory.

**Tech Stack:** Go, kubebuilder/controller-runtime, CEL validation, existing `internal/profilederivation` pure functions.

**Design authority:** `docs/design/profiling/plans/07-batch-saturation-control-path.md`

## Starting point

These exist in the tree and are not re-implemented here:

- `ProfilingRunSpec` without `Repetitions`; both evidence schemas at `v1alpha4`.
- `MeasurementStateSpec`, extracted and embedded inline in `ProfilingRunSpec`.
- The `BatchSaturation` CRD, its status, and the `sequential-early-stop` method union.
- `internal/batchsaturation`: `types.go`, `lifecycle.go` (`Decide`), `sequential.go`.
- `internal/batchsaturation/controller`: `run_builder.go`, `projection.go`.
- `admission.EvaluateTrial`, returning the admitted active trial.

Three defects in that code are corrected by Tasks 1-4. They share one cause: the parent declares a single execution path for the whole sweep, which is only true when one artifact serves every batch.

- The parent pins one `Candidate.ExecutionPath`, so a family whose artifacts encode a fixed batch cannot be swept past its first member.
- `sequential.go` requests one idle baseline at batch one, but `internal/profiler/admission/curve_family.go:85-87` requires a fixed-batch member's idle trial at that member's own batch.
- `projection.go` reads the capability boundary from a Run condition reason that nothing emits. The boundary is the edge of the resolved map, and `internal/profilederivation/curve.go:113-119` already requires it to be an empty batch one past the last frozen member.

## Global Constraints

- Pure packages must not import `api/v1alpha1`, Kubernetes APIs, or controller-runtime. Verified by `make arch-check`.
- Values deciding what a Run measures belong in configuration, never Go constants (`AGENTS.md`).
- Do not give a struct a field that accepts only one value.
- Do not add digest, byte-equality, or canonical re-encoding comparison as a validation gate.
- Verify once at a boundary, return a type only that boundary can construct, do not re-validate downstream.
- Every new production package must map to exactly one `.go-arch-lint.yml` component, and `hack/architecture-coverage.sh` must stay synchronized with it.
- Before handoff run `make lint` and `make test`; run `make arch-check` when a package or internal import is added; run `make manifests generate` when API types change.
- Changes to a runtime contract must be carried into `runtime/*/runner.py`, whose results are decoded with `DisallowUnknownFields()`.
- Never run e2e against the testbed kubeconfig.
- Existing package conventions to copy verbatim: `internal/nodeactuator/types.go`, `internal/nodeactuator/lifecycle.go`, `internal/nodeactuator/controller/projection.go`, `internal/profiler/controller/intent_setup.go`.

---

### Task 1: Declare the runtime, not the execution path

The parent must not name an execution path, because that is the one part of the execution state the sweep varies. It names the runtime that selects among paths, and the batch bound.

**Files:**
- Modify: `api/v1alpha1/batchsaturation_types.go`
- Modify: `internal/batchsaturation/controller/run_builder_test.go`, `projection_test.go` (parent literals)
- Modify: any `cmd/` or test literal constructing a `BatchSaturationSpec`

**Interfaces:**
- Consumes: `ModelRuntimeRequirement` from `api/v1alpha1/modelspec_types.go`
- Produces: `BatchSaturationSpec` whose state carries no execution path

- [ ] **Step 1: Replace the state and candidate types**

```go
// BatchSaturationStateSpec fixes what every trial shares. It omits the
// execution path: a derived artifact encodes one batch, so the path that
// measures a batch is resolved per batch rather than declared once.
type BatchSaturationStateSpec struct {
	Candidate BatchSaturationCandidateSpec `json:"candidate"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	TargetNode string `json:"targetNode"`

	CPURequest resource.Quantity `json:"cpuRequest"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	AdmissionPolicy string `json:"admissionPolicy"`
}

// BatchSaturationCandidateSpec selects one catalog execution family. Runtime
// selects among the paths a ModelSpec declares; the batch decides which of
// them measures a given trial.
type BatchSaturationCandidateSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ModelSpec string `json:"modelSpec"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DeviceClass string `json:"deviceClass"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PowerMode string `json:"powerMode"`

	Runtime ModelRuntimeRequirement `json:"runtime"`
}
```

- [ ] **Step 2: Point the spec at it and bound the sweep**

In `BatchSaturationSpec`, replace `State MeasurementStateSpec` with `State BatchSaturationStateSpec` and add:

```go
	// MaxBatch bounds the sweep. A fixed-batch family may end earlier, where
	// the catalog stops declaring members; a dynamic-batch family has no
	// catalog end and stops here or at the method's own rule.
	// +kubebuilder:validation:Minimum=1
	MaxBatch int32 `json:"maxBatch"`
```

- [ ] **Step 3: Declare the baseline trial count**

Add to `IdleProtocolSpec`. It is not the method's `trialsPerBatch`: derivation requires at least three (`internal/profilederivation/derive.go:62`), and beyond that the count only narrows the baseline's confidence half-width, which `sequential-early-stop` never reads. A method whose rule does read it can raise this without changing the active trial count.

```go
	// +kubebuilder:validation:Minimum=3
	Trials int32 `json:"trials"`
```

- [ ] **Step 4: Generate and verify**

```bash
make manifests generate
git diff --stat config/crd/bases charts/chill/templates/crds
```
Expected: only the `BatchSaturation` CRD changes in both locations. `ProfilingRun`'s CRD is untouched — `MeasurementStateSpec` and `ProfilingRunSpec` are not edited by this task.

- [ ] **Step 5: Build and commit**

```bash
go build ./... && go test ./internal/batchsaturation/... && make lint
git add api config charts internal
git commit -m "feat: select a runtime family instead of one execution path"
```

---

### Task 2: Resolve and freeze the family map

**Files:**
- Create: `internal/batchsaturation/family.go`, `family_test.go` (pure map type)
- Create: `internal/batchsaturation/controller/family_resolver.go`, `family_resolver_test.go`

**Interfaces:**
- Consumes: `ModelSpec`, `spec.state.candidate`, `spec.maxBatch`
- Produces: a pure `Family`, and a frozen planned map written to the evidence store

This task must not extend `batch-saturation-controller`'s dependencies beyond `api` and `batch-saturation-core`. `.go-arch-lint.yml:75` scopes these packages away from profiler orchestration, and the Profiler's fact-source decoding is unexported inside `internal/profiler/controller`. The map is resolved from the catalog alone; artifact supply is resolved per Run by the Profiler, which already freezes it into the Run's intent record.

- [ ] **Step 1: Write the pure family type**

In `internal/batchsaturation/family.go`. It carries no Kubernetes type and no artifact identity — only what the progress rules need.

```go
// Family is the frozen batch-to-state map the measurement was resolved to.
type Family struct {
	// Batches the map covers, ascending and contiguous from one.
	Batches []int

	// SharedIdleBatch is set when one artifact serves every batch, so a
	// single idle baseline at that batch pairs with every active trial.
	// It is unset when each batch has its own artifact and therefore its
	// own idle baseline.
	SharedIdleBatch *int
}

// CapabilityBoundary is the first batch the map does not cover. No trial is
// ever submitted there.
func (f Family) CapabilityBoundary() int

// IdleBatchFor returns the batch whose idle baseline pairs with an active
// trial at batch b.
func (f Family) IdleBatchFor(batch int) int

func (f Family) Validate() error
```

`Validate` rejects an empty map, a map not starting at one, a non-contiguous map, and a `SharedIdleBatch` outside `Batches`.

- [ ] **Step 2: Write the failing resolver test**

In `internal/batchsaturation/controller/family_resolver_test.go`, two cases built from `ModelSpec` fixtures:

- `TestResolveFamilyMapsEachDerivedArtifactToItsBatch` — a ModelSpec with paths `rknnlite` (artifact `fixedBatch: 1`) and `rknnlite-bs2` (artifact `fixedBatch: 2`), both with runtime `{rknnlite, npu}`, plus an unrelated `ort-cpu` path. With `maxBatch: 4`, the resolved family covers `[1, 2]`, has no `SharedIdleBatch`, and reports boundary 3.
- `TestResolveFamilyGivesOnePathToEveryBatch` — a ModelSpec with one `ort-cpu` path whose artifact has no `Derivation`. With `maxBatch: 4`, the family covers `[1, 2, 3, 4]` and `SharedIdleBatch` is 1.

- [ ] **Step 3: Implement the resolver**

In `internal/batchsaturation/controller/family_resolver.go`:

```text
for each ModelExecutionPath in the named ModelSpec
    skip unless Runtime.Family and Runtime.Backend equal the candidate's
    look up the ModelArtifact it names
        Derivation != nil -> that path measures Derivation.FixedBatch
        Derivation == nil -> that path measures every batch up to maxBatch
```

Two paths claiming one batch is an error; so is a derived batch above `maxBatch`, which is skipped rather than an error. Mixing a canonical artifact and derived artifacts within one runtime selection is an error — the family is one kind or the other.

Return both the pure `Family` and, for each covered batch, the resolved `ExecutionPath` and `ModelArtifact`.

- [ ] **Step 4: Freeze the map**

Record per covered batch: batch, execution path, artifact name, fixed-batch capability, and the artifact's input contract. Store it at `batchSaturationObjectKey(parent.UID, "family")` before creating the first Run, under its own schema version.

It does not carry an artifact image or path. Those are installation configuration keyed by `(modelSpec, deviceClass, executionPath)`, resolved and frozen per Run by the Profiler; the realized `CurveFamily` carries them after Loader verification, which is ADR 0014's planned-versus-realized split. Reaching for them here would duplicate a fact this layer does not own and would cross the dependency boundary named above.

A frozen map already present is read back and used, never recomputed — re-resolving after a catalog edit mid-measurement would change the scope of a running experiment.

- [ ] **Step 5: Verify and commit**

```bash
go test ./internal/batchsaturation/... -v && make arch-check && make lint
git add internal
git commit -m "feat: resolve and freeze the batch-saturation family map"
```

`make arch-check` must still report `batch-saturation-controller` depending only on `api` and `batch-saturation-core`. A new edge here means the resolver reached for something it should not.

---

### Task 3: Pair every active batch with the idle baseline its state needs

Idle pairing is a system invariant that admission already enforces, not a method preference. The method asks for a batch; the trials that batch requires follow from the map.

**Files:**
- Modify: `internal/batchsaturation/types.go`, `lifecycle.go`, `lifecycle_test.go`
- Modify: `internal/batchsaturation/sequential.go`, `sequential_test.go`

- [ ] **Step 1: Carry the map and both trial counts in the snapshot**

Add to `Snapshot`: `Family Family`, `TrialsPerBatch int`, and `BaselineTrials int`. They are separate values because they answer different questions — how many active trials a batch needs is the method's, how many idle trials a baseline needs is the protocol's. `Snapshot.Validate` rejects an invalid family, a non-positive `TrialsPerBatch`, and a `BaselineTrials` below three.

- [ ] **Step 2: Stop the method from planning idle trials**

Delete the batch-one baseline block at the top of `sequential.go`'s `Plan`. The method plans active measurements only. Set `Observed.CapabilityBoundary` from `Family.CapabilityBoundary()` in `Decide` rather than from trial flags.

- [ ] **Step 3: Make `Decide` supply the baseline**

When the method returns an active `Plan.Next` at batch `b`, `Decide` first checks the idle measurement at `Family.IdleBatchFor(b)`. If fewer than `BaselineTrials` of its trials are accepted, `Decide` returns that idle measurement instead. The method is not told this happened; the next reconcile asks it the same question.

A baseline is measured once per state and reused by every active trial of that state, including retried ones, because the check counts accepted trials rather than attempts. A dynamic family therefore measures one baseline for the whole sweep.

- [ ] **Step 4: Test both kinds**

- `TestDecideMeasuresOneSharedBaselineForADynamicFamily` — active batch 2 requested with `SharedIdleBatch = 1` and a complete batch-one idle set yields the active measurement, not another baseline.
- `TestDecideMeasuresAPerMemberBaselineForAFixedFamily` — active batch 2 requested with no `SharedIdleBatch` and no batch-two idle trials yields the idle measurement at batch two.
- `TestDecideReportsTheMapBoundaryToTheMethod` — a family covering `[1, 2]` reports boundary 3 in `Observed`.

- [ ] **Step 5: Verify and commit**

```bash
go test ./internal/batchsaturation/... -v && make lint
git add internal
git commit -m "feat: pair each active batch with its own idle baseline"
```

---

### Task 4: Project and build from the map

**Files:**
- Modify: `internal/batchsaturation/controller/projection.go`, `projection_test.go`
- Modify: `internal/batchsaturation/controller/run_builder.go`, `run_builder_test.go`

- [ ] **Step 1: Delete the unreachable reason code**

Remove `capabilityReasonCode`, its entry in `isTerminalReason`, and `Trial.CapabilityRejected` along with every use. A batch outside the map never becomes a Run, so no Run outcome can report a boundary.

- [ ] **Step 2: Take the family into the projection**

`ProjectSnapshot(parent, family, runs)` fills `Snapshot.Family` and reads both trial counts from the parent spec — `TrialsPerBatch` from the method block, `BaselineTrials` from `spec.idleProtocol`. Everything else is unchanged: values still come from `run.Spec.Measurement`, and only `attempt` comes from a label.

- [ ] **Step 3: Build the Run with the resolved path**

`BuildRun(parent, member, measurement, attempt)` takes the resolved member for that batch and assembles `MeasurementStateSpec` from the parent's batch-invariant state plus the member's `ExecutionPath`. Add a test asserting two batches of a fixed-batch family produce Runs with different execution paths.

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/batchsaturation/... -v && make lint
git add internal
git commit -m "refactor: project and build trials from the frozen map"
```

---

### Task 5: Reconciler and setup

**Files:**
- Create: `internal/batchsaturation/controller/reconciler.go`, `setup.go`
- Modify: `.go-arch-lint.yml`, `hack/architecture-coverage.sh`

- [ ] **Step 1: Implement `Reconcile`**

Get the parent; return early on a terminal phase; load or resolve-and-freeze the family; list owned Runs by `client.MatchingLabels{labelBatchSaturation: parent.Name}`; `ProjectSnapshot`; `methodFor`; `Decide`; `execute`.

- [ ] **Step 2: Implement `execute`**

Four actions. `ActionCreateRun` computes the next attempt from the snapshot, builds the Run from the resolved member, and tolerates `IsAlreadyExists`. `ActionWait` requeues. `ActionFinish` writes the result and completes. `ActionFail` records the reason. Phases are `Measuring`, `Complete`, `Failed`.

- [ ] **Step 3: Implement `methodFor` and `deriveSequential`**

`methodFor` parses the string-typed parameters into `SequentialConfig` and supplies a `CurveSource` closure. `deriveSequential` is the only place that touches evidence: it loads each admitted Run's verified evidence through the store client `RunReconciler` uses, calls `admission.EvaluateTrial` for active Runs and `admission.EvaluateIdle` for idle ones, builds the `CurveFamilyContract` by pairing each frozen map entry with the artifact supply its admitted Runs recorded, verifies members with `admission.ProjectVerifiedCurveFamilyMember`, builds one `profilederivation.Pass{Role: PassDiscovery}`, calls `DeriveCurve` then `DeriveSequentialBatchDecision`, and maps the result into `batchsaturation.CurveResult`. Cache loaded evidence by Run UID; the objects are immutable.

This is where the planned map becomes a realized family. A Run whose recorded execution path or fixed-batch capability disagrees with its frozen map entry fails verification here rather than being silently composed in.

- [ ] **Step 4: Register the controller**

`SetupWithManager` in `setup.go`, named `batchsaturation`, `For(&BatchSaturation{}).Owns(&ProfilingRun{})`, following `internal/profiler/controller/intent_setup.go`.

- [ ] **Step 5: Register the architecture component**

Extend `batch-saturation-controller`'s `mayDependOn` with the components its production imports actually name after this task. Let `go list` decide, not this plan.

- [ ] **Step 6: Verify and commit**

```bash
go build ./... && go test ./internal/batchsaturation/... -v && make arch-check && make lint
git add internal .go-arch-lint.yml hack
git commit -m "feat: reconcile BatchSaturation into owned Runs"
```

---

### Task 6: Wire it up and prove one measurement end to end

**Files:**
- Modify: `cmd/profiler/main.go`
- Modify: `config/rbac/role.yaml` and the Profiler role in `charts/chill/templates/`
- Create: `internal/batchsaturation/controller/reconciler_test.go` (envtest)

- [ ] **Step 1: Register the reconciler** in the Profiler binary, alongside the Run reconcilers, reusing the existing manager, scheme, and evidence-store client. It needs no fact-source option of its own.
- [ ] **Step 2: Add RBAC markers** for `batchsaturations` and `batchsaturations/status`, then `make manifests` and sync the chart.
- [ ] **Step 3: Write two envtest scenarios** — a completed measurement and an exhausted retry limit. Cover both artifact kinds in the completed case, since the idle-pairing rule differs between them.
- [ ] **Step 4: Full gates** — `make lint`, `make test`, `make arch-check`.
- [ ] **Step 5: Commit.**

---

### Task 7: Record the architecture decision

- [ ] **Step 1: Write the ADR** recording the aggregate measurement unit as an architectural boundary, and that the batch-to-path map is resolved from the catalog and frozen before the first Run rather than declared per measurement.
- [ ] **Step 2: Update the Plan 7 status** to implemented.
- [ ] **Step 3: Commit.**

## Deferred, with the reason each is safe

- Pre-checking environment identity per trial. `VerifyCurveFamily` catches drift at derivation; pre-checking only saves Runs that would be discarded anyway.
- A no-progress timeout. A stalled measurement is visible and can be deleted.
- A deletion finalizer. A measurement is not deleted while running.
- The `two-pass-confirmatory` method. `sequential-early-stop` proves the control path; adding a method is implementing `Plan` and one enum name.
- Generating a missing artifact so the sweep can continue past the map. The map makes the gap explicit, which is the precondition for filling it. Doing so needs a component that can run a converter and publish an image without sharing the measured Node's power envelope, which is its own boundary.
