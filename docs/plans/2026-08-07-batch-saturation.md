# BatchSaturation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One declared `BatchSaturation` object drives a whole `b_sat` measurement — idle baseline, batch sweep, derivation, typed result — with no human step between Runs.

**Architecture:** A namespaced `BatchSaturation` CRD owns the `ProfilingRun` objects it creates. Each reconcile lists the owned Runs, projects them into a pure `Snapshot`, and calls `Decide`, which handles shared progress rules and delegates the measurement question to a named method module. Nothing authoritative lives in status or in process memory.

**Tech Stack:** Go, kubebuilder/controller-runtime, CEL validation, existing `internal/profilederivation` pure functions.

**Design authority:** `docs/design/profiling/plans/07-batch-saturation-control-path.md`

## Global Constraints

- Pure packages must not import `api/v1alpha1`, Kubernetes APIs, or controller-runtime. Verified by `make arch-check`.
- Values deciding what a Run measures belong in configuration, never Go constants (`AGENTS.md`).
- Do not give a struct a field that accepts only one value.
- Do not add digest, byte-equality, or canonical re-encoding comparison as a validation gate.
- Verify once at a boundary, return a type only that boundary can construct, do not re-validate downstream.
- Every new production package must map to exactly one `.go-arch-lint.yml` component.
- Before handoff run `make lint` and `make test`; run `make arch-check` when a package or internal import is added; run `make manifests generate` when API types change.
- Never run e2e against the testbed kubeconfig.
- Existing package conventions to copy verbatim: `internal/nodeactuator/types.go` (doc comment, const block, types with `Validate()` methods), `internal/nodeactuator/lifecycle.go` (`Decide(Snapshot) (Decision, error)` + table tests), `internal/nodeactuator/controller/projection.go` (`Project*` returning pure types, validating at the end), `internal/profiler/controller/intent_setup.go` (`SetupWithManager`).

---

### Task 1: Remove `Repetitions` and raise the evidence schemas

`ProfilingRunSpec.Repetitions` is pinned to exactly one value (`Minimum=1, Maximum=1`), which the repo's own rules forbid. Trial count moves to `BatchSaturation`. Doing this before any new measurement avoids invalidating evidence produced later.

**Files:**
- Modify: `api/v1alpha1/profilingrun_types.go:89-92`
- Modify: `internal/profilingrun/types.go:147`, `internal/profilingrun/evidence.go:40,173,176,179`, `internal/profilingrun/intent.go:82`
- Modify: `internal/profiler/preparation.go:433`, `internal/profiler/controller/projection.go:42`, `internal/profiler/admission/curve_family.go:154`
- Modify: `internal/profilederivation/curve_family.go:50,379`
- Modify: `internal/profilingrun/evidence.go` — `RuntimeResultSchemaVersion` to `v1alpha4`
- Modify: `internal/profiler/evidence/types.go:11` — `ExecutionEvidenceSchemaVersion` to `v1alpha4`

**Interfaces:**
- Consumes: nothing
- Produces: `ProfilingRunSpec` without `Repetitions`; `RuntimeResultSchemaVersion = "chill.dacs.io/batch-characterization.v1alpha4"`; `ExecutionEvidenceSchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha4"`

- [ ] **Step 1: Find every reference**

```bash
grep -rn "Repetitions\|repetitions" --include='*.go' api/ internal/ cmd/
```
Expected: 37 hits across the files listed above plus tests.

- [ ] **Step 2: Delete the API field**

Remove from `api/v1alpha1/profilingrun_types.go`:

```go
	// The first path treats each ProfilingRun as one independent trial.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	Repetitions int32 `json:"repetitions"`
```

- [ ] **Step 3: Delete the field from the internal intent and evidence records**

In `internal/profilingrun/types.go` remove `Repetitions int` from the measurement struct. In `internal/profilingrun/intent.go` remove the guard:

```go
	if i.Measurement.Repetitions != 1 {
```

In `internal/profiler/preparation.go` remove the same guard at line 433. In `internal/profiler/controller/projection.go:42`, `internal/profiler/admission/curve_family.go:154`, and `internal/profilederivation/curve_family.go:50,379` remove the field and the assignments that carry it.

- [ ] **Step 4: Keep the worker repetition results, drop the count cross-check**

`RepetitionResults` stays — it is the per-repetition latency carrier that `projectTrial` flattens. Only the declared-count comparison goes. In `internal/profilingrun/evidence.go` remove:

```go
		result.Repetitions != intent.Measurement.Repetitions {
```
```go
	minimumElapsed := float64(intent.Measurement.DurationSeconds*intent.Measurement.Repetitions) - 0.5
```
```go
		len(result.RepetitionResults) != intent.Measurement.Repetitions {
```

Replace the elapsed floor with the duration alone and require at least one repetition result:

```go
	minimumElapsed := float64(intent.Measurement.DurationSeconds) - 0.5
```
```go
	if len(result.RepetitionResults) < 1 {
		return errors.New("runtime result must contain at least one repetition result")
	}
```

- [ ] **Step 5: Raise both schema versions**

```go
// internal/profilingrun/evidence.go
	RuntimeResultSchemaVersion = "chill.dacs.io/batch-characterization.v1alpha4"
```
```go
// internal/profiler/evidence/types.go
	ExecutionEvidenceSchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha4"
```

- [ ] **Step 6: Update tests that set the field**

```bash
grep -rln "Repetitions" --include='*_test.go' .
```
Remove the field from every literal. Do not add a replacement assertion; the field no longer exists.

- [ ] **Step 7: Regenerate and verify**

```bash
make manifests generate
go build ./... && go test ./... 2>&1 | grep -v "^ok" | head -20
```
Expected: build succeeds, no failing packages. `config/crd/bases/edge.dacs.io_profilingruns.yaml` and `charts/chill/templates/crds/edge.dacs.io_profilingruns.yaml` no longer contain `repetitions`.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor: move trial count out of the Run spec"
```

---

### Task 2: Extract `MeasurementStateSpec` and add the `BatchSaturation` API

**Files:**
- Modify: `api/v1alpha1/profilingrun_types.go:23-46`
- Create: `api/v1alpha1/batchsaturation_types.go`
- Generated: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/`, `charts/chill/templates/crds/`

**Interfaces:**
- Consumes: Task 1's `ProfilingRunSpec`
- Produces: `MeasurementStateSpec`, `BatchSaturationSpec`, `ActiveProtocolSpec`, `IdleProtocolSpec`, `MeasurementMethodSpec`, `SequentialEarlyStopSpec`, `BatchSaturationStatus`, and the constants `MethodSequentialEarlyStop = "sequential-early-stop"`

- [ ] **Step 1: Extract the shared state, keeping the JSON shape identical**

```go
// MeasurementStateSpec fixes the execution state every trial of one
// measurement shares.
type MeasurementStateSpec struct {
	Candidate ProfilingRunCandidateSpec `json:"candidate"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	TargetNode string `json:"targetNode"`

	CPURequest resource.Quantity `json:"cpuRequest"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	AdmissionPolicy string `json:"admissionPolicy"`
}

type ProfilingRunSpec struct {
	MeasurementStateSpec `json:",inline"`

	Measurement       ProfilingRunMeasurementSpec       `json:"measurement"`
	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}
```

Keep all three existing `XValidation` markers on `ProfilingRunSpec` unchanged.

- [ ] **Step 2: Verify the generated CRD is byte-identical apart from `repetitions`**

```bash
make manifests generate
git diff config/crd/bases/edge.dacs.io_profilingruns.yaml
```
Expected: no diff. The inline embedding must not move or rename a field. If the diff is non-empty, the embedding is wrong.

- [ ] **Step 3: Write the new API types**

Create `api/v1alpha1/batchsaturation_types.go`:

```go
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const MethodSequentialEarlyStop = "sequential-early-stop"

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="BatchSaturation spec is immutable"
type BatchSaturationSpec struct {
	State          MeasurementStateSpec  `json:"state"`
	ActiveProtocol ActiveProtocolSpec    `json:"activeProtocol"`
	IdleProtocol   IdleProtocolSpec      `json:"idleProtocol"`
	Method         MeasurementMethodSpec `json:"method"`

	// RetryLimit bounds attempts per batch that did not reach an Accepted
	// verdict. It does not bound admitted trials.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	RetryLimit int32 `json:"retryLimit"`
}

// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type ActiveProtocolSpec struct {
	// +kubebuilder:validation:Minimum=0
	WarmupIterations int32 `json:"warmupIterations"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	InputGenerator string `json:"inputGenerator"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OutputValidation string `json:"outputValidation"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type IdleProtocolSpec struct {
	// +kubebuilder:validation:Minimum=1
	SettlingDurationSeconds int32 `json:"settlingDurationSeconds"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

// MeasurementMethodSpec selects one measurement method and carries exactly that
// method's parameters.
// +kubebuilder:validation:XValidation:rule="self.name == 'sequential-early-stop' ? has(self.sequentialEarlyStop) : !has(self.sequentialEarlyStop)",message="method parameters must match the selected method name"
type MeasurementMethodSpec struct {
	// +kubebuilder:validation:Enum=sequential-early-stop
	Name string `json:"name"`

	// +optional
	SequentialEarlyStop *SequentialEarlyStopSpec `json:"sequentialEarlyStop,omitempty"`
}

type SequentialEarlyStopSpec struct {
	// MinDelta is the relative improvement below which a batch does not count
	// as an improvement over the best batch so far.
	MinDelta string `json:"minDelta"`

	// +kubebuilder:validation:Minimum=1
	Patience int32 `json:"patience"`

	// +kubebuilder:validation:Minimum=3
	TrialsPerBatch int32 `json:"trialsPerBatch"`

	// DivergenceFactor is the multiple of the best value above which the sweep
	// stops early. Must exceed 1.
	DivergenceFactor string `json:"divergenceFactor"`
}

type BatchSaturationStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Measuring;Deriving;Complete;Failed
	Phase string `json:"phase,omitempty"`

	// +optional
	AcceptedTrials int32 `json:"acceptedTrials,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=128
	Outcome string `json:"outcome,omitempty"`

	// +optional
	Result *ProfilingRunEvidenceReference `json:"result,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=bsat
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Trials",type=integer,JSONPath=`.status.acceptedTrials`
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.status.outcome`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BatchSaturation drives one batch-saturation measurement by owning the
// ProfilingRun objects it creates.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 200",message="name must be at most 200 characters so generated Run names fit"
type BatchSaturation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BatchSaturationSpec   `json:"spec"`
	Status BatchSaturationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type BatchSaturationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BatchSaturation `json:"items"`
}

func init() { SchemeBuilder.Register(&BatchSaturation{}, &BatchSaturationList{}) }
```

`MinDelta` and `DivergenceFactor` are strings because CRD schemas have no float type with exact round-tripping; the controller parses them with `strconv.ParseFloat` and surfaces a typed error. `TrialsPerBatch` has `Minimum=3` because `validateSequentialPolicy` rejects fewer.

- [ ] **Step 4: Generate and verify**

```bash
make manifests generate
ls config/crd/bases/edge.dacs.io_batchsaturations.yaml
go build ./...
```
Expected: the new CRD exists and the build succeeds.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: add the BatchSaturation API"
```

---

### Task 3: Pure core — types and `Decide`

Mirror `internal/nodeactuator`: `types.go` holds the doc comment, constants, types, and their `Validate()` methods; `lifecycle.go` holds `Decide`.

**Files:**
- Create: `internal/batchsaturation/types.go`
- Create: `internal/batchsaturation/lifecycle.go`
- Create: `internal/batchsaturation/lifecycle_test.go`
- Modify: `.go-arch-lint.yml`

**Interfaces:**
- Consumes: nothing from earlier tasks (pure)
- Produces: `Snapshot`, `Trial`, `Decision`, `Action`, `Measurement`, `WorkloadMode`, `Verdict`, `Observed`, `Plan`, `Result`, `Method`, `Decide(Snapshot) (Decision, error)`

- [ ] **Step 1: Write the types**

```go
// Package batchsaturation contains the Kubernetes-independent progress rules
// that drive one batch-saturation measurement from its admitted trials.
package batchsaturation

import (
	"errors"
	"fmt"
)

const (
	WorkloadModeActive WorkloadMode = "active"
	WorkloadModeIdle   WorkloadMode = "idle"

	VerdictPending  Verdict = "Pending"
	VerdictAccepted Verdict = "Accepted"
	VerdictFailed   Verdict = "Failed"

	ActionCreateRun Action = "CreateRun"
	ActionWait      Action = "Wait"
	ActionFinish    Action = "Finish"
	ActionFail      Action = "Fail"
)

type (
	WorkloadMode string
	Verdict      string
	Action       string
)

// Measurement names one trial to run. Batch is meaningful for both modes: the
// idle baseline is measured at the batch whose state it belongs to.
type Measurement struct {
	WorkloadMode WorkloadMode
	Batch        int
}

// Trial is one owned ProfilingRun projected into the progress rules. A trial
// carries no measured value; the method receives values separately.
type Trial struct {
	Measurement        Measurement
	Attempt            int
	Verdict            Verdict
	CapabilityRejected bool
}

// Snapshot is built from the immutable spec and the owned Runs only. It has no
// status field, so a decision cannot depend on previously reported progress.
type Snapshot struct {
	RetryLimit int
	Trials     []Trial
}

type Decision struct {
	Action Action
	Run    *Measurement
	Result *Result
	Reason string
}

// Observed is everything a method may see. Failed, pending, and retried
// attempts are absent by construction.
type Observed struct {
	Accepted           []Measurement
	CapabilityBoundary *int
}

type Plan struct {
	Next   *Measurement
	Result *Result
}

// Result is the terminal typed outcome plus the method's own detail, which the
// controller writes to the evidence store without interpreting it.
type Result struct {
	Outcome string
	Detail  any
}

type Method interface {
	Plan(observed Observed) (Plan, error)
}

func (m Measurement) Validate() error {
	if m.WorkloadMode != WorkloadModeActive && m.WorkloadMode != WorkloadModeIdle {
		return fmt.Errorf("unsupported workload mode %q", m.WorkloadMode)
	}
	if m.Batch < 1 {
		return errors.New("batch must be positive")
	}
	return nil
}

func (s Snapshot) Validate() error {
	if s.RetryLimit < 0 {
		return errors.New("retry limit must not be negative")
	}
	seen := make(map[Measurement]map[int]struct{}, len(s.Trials))
	for _, trial := range s.Trials {
		if err := trial.Measurement.Validate(); err != nil {
			return fmt.Errorf("validate trial measurement: %w", err)
		}
		if trial.Attempt < 1 {
			return errors.New("trial attempt must be positive")
		}
		attempts, exists := seen[trial.Measurement]
		if !exists {
			attempts = make(map[int]struct{})
			seen[trial.Measurement] = attempts
		}
		if _, duplicate := attempts[trial.Attempt]; duplicate {
			return errors.New("one measurement attempt appears twice")
		}
		attempts[trial.Attempt] = struct{}{}
	}
	return nil
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/batchsaturation/lifecycle_test.go`:

```go
package batchsaturation

import "testing"

type stubMethod struct {
	plan Plan
	seen Observed
}

func (s *stubMethod) Plan(observed Observed) (Plan, error) {
	s.seen = observed
	return s.plan, nil
}

func active(batch int) Measurement { return Measurement{WorkloadMode: WorkloadModeActive, Batch: batch} }
func idle(batch int) Measurement   { return Measurement{WorkloadMode: WorkloadModeIdle, Batch: batch} }

func TestDecide(t *testing.T) {
	first := idle(1)
	for _, testCase := range []struct {
		name     string
		snapshot Snapshot
		plan     Plan
		want     Action
		wantRun  *Measurement
	}{
		{
			name:     "ask the method when nothing has run",
			snapshot: Snapshot{RetryLimit: 2},
			plan:     Plan{Next: &first},
			want:     ActionCreateRun, wantRun: &first,
		},
		{
			name: "wait while a trial is live",
			snapshot: Snapshot{RetryLimit: 2, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictPending},
			}},
			want: ActionWait,
		},
		{
			name: "retry a failed attempt within the limit",
			snapshot: Snapshot{RetryLimit: 2, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
			}},
			want: ActionCreateRun, wantRun: &first,
		},
		{
			name: "fail once the retry limit is exhausted",
			snapshot: Snapshot{RetryLimit: 1, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
				{Measurement: idle(1), Attempt: 2, Verdict: VerdictFailed},
			}},
			want: ActionFail,
		},
		{
			name: "finish when the method returns a result",
			snapshot: Snapshot{RetryLimit: 2, Trials: []Trial{
				{Measurement: idle(1), Attempt: 1, Verdict: VerdictAccepted},
			}},
			plan: Plan{Result: &Result{Outcome: "Stopped/NoFurtherImprovement"}},
			want: ActionFinish,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			decision, err := Decide(testCase.snapshot, &stubMethod{plan: testCase.plan})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision.Action != testCase.want {
				t.Fatalf("action = %q, want %q", decision.Action, testCase.want)
			}
			if testCase.wantRun != nil && (decision.Run == nil || *decision.Run != *testCase.wantRun) {
				t.Fatalf("run = %+v, want %+v", decision.Run, testCase.wantRun)
			}
		})
	}
}

func TestDecideHidesFailedTrialsFromTheMethod(t *testing.T) {
	method := &stubMethod{plan: Plan{Next: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}}}
	_, err := Decide(Snapshot{RetryLimit: 2, Trials: []Trial{
		{Measurement: idle(1), Attempt: 1, Verdict: VerdictFailed},
		{Measurement: idle(1), Attempt: 2, Verdict: VerdictAccepted},
		{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
	}}, method)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(method.seen.Accepted) != 2 {
		t.Fatalf("method saw %d measurements, want only the admitted 2", len(method.seen.Accepted))
	}
}

func TestDecideReportsCapabilityBoundaryToTheMethod(t *testing.T) {
	method := &stubMethod{plan: Plan{Result: &Result{Outcome: "Stopped/CapabilityBoundary"}}}
	_, err := Decide(Snapshot{RetryLimit: 2, Trials: []Trial{
		{Measurement: active(1), Attempt: 1, Verdict: VerdictAccepted},
		{Measurement: active(2), Attempt: 1, Verdict: VerdictFailed, CapabilityRejected: true},
	}}, method)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method.seen.CapabilityBoundary == nil || *method.seen.CapabilityBoundary != 2 {
		t.Fatalf("capability boundary = %v, want 2", method.seen.CapabilityBoundary)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

```bash
go test ./internal/batchsaturation/ -run TestDecide -v
```
Expected: FAIL — `undefined: Decide`.

- [ ] **Step 4: Implement `Decide`**

Create `internal/batchsaturation/lifecycle.go`:

```go
package batchsaturation

import (
	"errors"
	"fmt"
)

// Decide resolves the shared progress rules and delegates only the measurement
// question. A capability rejection is not a failure: it is reported to the
// method as an observed boundary and never retried.
func Decide(snapshot Snapshot, method Method) (Decision, error) {
	if method == nil {
		return Decision{}, errors.New("a measurement method is required")
	}
	if err := snapshot.Validate(); err != nil {
		return Decision{}, fmt.Errorf("validate snapshot: %w", err)
	}
	observed := Observed{}
	attempts := make(map[Measurement]int)
	accepted := make(map[Measurement]struct{})
	for _, trial := range snapshot.Trials {
		if trial.Verdict == VerdictPending {
			return Decision{Action: ActionWait, Reason: "TrialInFlight"}, nil
		}
		if trial.CapabilityRejected {
			batch := trial.Measurement.Batch
			if observed.CapabilityBoundary == nil || batch < *observed.CapabilityBoundary {
				observed.CapabilityBoundary = &batch
			}
			continue
		}
		if trial.Attempt > attempts[trial.Measurement] {
			attempts[trial.Measurement] = trial.Attempt
		}
		if trial.Verdict == VerdictAccepted {
			if _, exists := accepted[trial.Measurement]; !exists {
				accepted[trial.Measurement] = struct{}{}
			}
			observed.Accepted = append(observed.Accepted, trial.Measurement)
		}
	}

	for _, trial := range snapshot.Trials {
		if trial.Verdict != VerdictFailed || trial.CapabilityRejected {
			continue
		}
		if _, admitted := accepted[trial.Measurement]; admitted {
			continue
		}
		next := attempts[trial.Measurement] + 1
		if next > snapshot.RetryLimit+1 {
			return Decision{Action: ActionFail, Reason: "RetryLimitExhausted"}, nil
		}
		measurement := trial.Measurement
		return Decision{Action: ActionCreateRun, Run: &measurement, Reason: "RetryAfterFailure"}, nil
	}

	plan, err := method.Plan(observed)
	if err != nil {
		return Decision{}, fmt.Errorf("plan next measurement: %w", err)
	}
	switch {
	case plan.Result != nil:
		return Decision{Action: ActionFinish, Result: plan.Result}, nil
	case plan.Next != nil:
		if err := plan.Next.Validate(); err != nil {
			return Decision{}, fmt.Errorf("validate planned measurement: %w", err)
		}
		next := *plan.Next
		return Decision{Action: ActionCreateRun, Run: &next, Reason: "MethodRequestedMeasurement"}, nil
	}
	return Decision{}, errors.New("method returned neither a next measurement nor a result")
}
```

- [ ] **Step 5: Run to verify it passes**

```bash
go test ./internal/batchsaturation/ -v
```
Expected: PASS, all subtests.

- [ ] **Step 6: Register the architecture component**

In `.go-arch-lint.yml`, add under `components:`

```yaml
  batch-saturation-core: { in: internal/batchsaturation }
```

and under `deps:`

```yaml
  batch-saturation-core:
    mayDependOn:
      - profile-derivation
```

- [ ] **Step 7: Verify the boundary**

```bash
make arch-check
```
Expected: pass. If it reports an unmapped package, the component entry is missing.

- [ ] **Step 8: Commit**

```bash
git add internal/batchsaturation .go-arch-lint.yml
git commit -m "feat: add batch-saturation progress rules"
```

---

### Task 4: The `sequential-early-stop` method

**Files:**
- Create: `internal/batchsaturation/sequential.go`
- Create: `internal/batchsaturation/sequential_test.go`

**Interfaces:**
- Consumes: `Method`, `Observed`, `Plan`, `Result`, `Measurement` from Task 3
- Produces: `NewSequentialEarlyStop(config SequentialConfig, curve CurveSource) (Method, error)`, `SequentialConfig{MinDelta, Patience, TrialsPerBatch, DivergenceFactor float64/int}`, `CurveSource func(batches []int) (profilederivation.Curve, error)`

- [ ] **Step 1: Write the failing test**

```go
package batchsaturation

import "testing"

func stubCurve(batches []int) (CurveResult, error) { return CurveResult{}, nil }

func TestSequentialRequestsIdleBaselineFirst(t *testing.T) {
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	plan, err := method.Plan(Observed{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Next == nil || *plan.Next != (Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}) {
		t.Fatalf("first measurement = %+v, want idle batch 1", plan.Next)
	}
}

func TestSequentialFillsEachBatchBeforeAdvancing(t *testing.T) {
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	observed := Observed{Accepted: []Measurement{
		{WorkloadMode: WorkloadModeIdle, Batch: 1},
		{WorkloadMode: WorkloadModeIdle, Batch: 1},
		{WorkloadMode: WorkloadModeIdle, Batch: 1},
		{WorkloadMode: WorkloadModeActive, Batch: 1},
	}}
	plan, err := method.Plan(observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Next == nil || *plan.Next != (Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}) {
		t.Fatalf("next = %+v, want a second active trial at batch 1", plan.Next)
	}
}

func TestSequentialStopsAtACapabilityBoundary(t *testing.T) {
	boundary := 3
	method, err := NewSequentialEarlyStop(SequentialConfig{
		MinDelta: 0.05, Patience: 1, TrialsPerBatch: 3, DivergenceFactor: 1.1,
	}, func(batches []int) (CurveResult, error) {
		return CurveResult{SelectedBatch: 2, Outcome: "Stopped/CapabilityBoundary"}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	observed := Observed{CapabilityBoundary: &boundary, Accepted: acceptedThrough(2, 3)}
	plan, err := method.Plan(observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Result == nil || plan.Result.Outcome != "Stopped/CapabilityBoundary" {
		t.Fatalf("result = %+v, want a capability-boundary stop", plan.Result)
	}
}

func acceptedThrough(maxBatch, trials int) []Measurement {
	var accepted []Measurement
	for count := 0; count < trials; count++ {
		accepted = append(accepted, Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1})
	}
	for batch := 1; batch <= maxBatch; batch++ {
		for count := 0; count < trials; count++ {
			accepted = append(accepted, Measurement{WorkloadMode: WorkloadModeActive, Batch: batch})
		}
	}
	return accepted
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/batchsaturation/ -run TestSequential -v
```
Expected: FAIL — `undefined: NewSequentialEarlyStop`.

- [ ] **Step 3: Implement the method**

Create `internal/batchsaturation/sequential.go`:

```go
package batchsaturation

import (
	"errors"
	"fmt"
)

// CurveSource derives the sequential decision over the admitted active batches
// it is given. The controller supplies it because building a Curve needs
// evidence the progress rules never see.
type CurveSource func(batches []int) (CurveResult, error)

// CurveResult is the method's view of a derivation outcome. Only the fields the
// stopping rule consumes cross this boundary. It is exported because the
// controller supplies the CurveSource that constructs it.
type CurveResult struct {
	Continue      bool
	SelectedBatch int
	Outcome       string
	Detail        any
}

type SequentialConfig struct {
	MinDelta         float64
	Patience         int
	TrialsPerBatch   int
	DivergenceFactor float64
}

type sequentialEarlyStop struct {
	config SequentialConfig
	curve  CurveSource
}

// NewSequentialEarlyStop mirrors the bounds validateSequentialPolicy enforces
// in internal/profilederivation, so an invalid configuration fails here rather
// than after the first Run.
func NewSequentialEarlyStop(config SequentialConfig, curve CurveSource) (Method, error) {
	if config.MinDelta < 0 || config.MinDelta >= 1 ||
		config.Patience < 1 || config.TrialsPerBatch < 3 || config.DivergenceFactor <= 1 {
		return nil, errors.New("sequential configuration is outside the supported range")
	}
	return &sequentialEarlyStop{config: config, curve: curve}, nil
}

func (s *sequentialEarlyStop) Plan(observed Observed) (Plan, error) {
	counts := make(map[Measurement]int, len(observed.Accepted))
	highestActive := 0
	for _, measurement := range observed.Accepted {
		counts[measurement]++
		if measurement.WorkloadMode == WorkloadModeActive && measurement.Batch > highestActive {
			highestActive = measurement.Batch
		}
	}

	baseline := Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}
	if counts[baseline] < s.config.TrialsPerBatch {
		return Plan{Next: &baseline}, nil
	}

	for batch := 1; batch <= highestActive; batch++ {
		point := Measurement{WorkloadMode: WorkloadModeActive, Batch: batch}
		if counts[point] < s.config.TrialsPerBatch {
			return Plan{Next: &point}, nil
		}
	}
	if highestActive == 0 {
		first := Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}
		return Plan{Next: &first}, nil
	}

	batches := make([]int, 0, highestActive)
	for batch := 1; batch <= highestActive; batch++ {
		batches = append(batches, batch)
	}
	if s.curve == nil {
		return Plan{}, errors.New("a curve source is required once a batch is complete")
	}
	result, err := s.curve(batches)
	if err != nil {
		return Plan{}, fmt.Errorf("derive sequential decision: %w", err)
	}
	if !result.Continue {
		return Plan{Result: &Result{Outcome: result.Outcome, Detail: result.Detail}}, nil
	}
	if observed.CapabilityBoundary != nil && *observed.CapabilityBoundary <= highestActive+1 {
		return Plan{Result: &Result{Outcome: result.Outcome, Detail: result.Detail}}, nil
	}
	next := Measurement{WorkloadMode: WorkloadModeActive, Batch: highestActive + 1}
	return Plan{Next: &next}, nil
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/batchsaturation/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/batchsaturation
git commit -m "feat: add the sequential early-stop measurement method"
```

---

### Task 5: Expose the admitted active trial

`Evaluate` builds a `profilederivation.Trial` and discards it, while `EvaluateIdle` already returns its `*IdleTrial`. The aggregate layer needs the active trial. Remove the asymmetry rather than duplicating the projection.

**Files:**
- Modify: `internal/profiler/admission/admitter.go:29-54`
- Test: `internal/profiler/admission/admitter_test.go`

**Interfaces:**
- Consumes: existing `profilerevidence.VerifiedEvidence`, `FrozenPolicy`
- Produces: `func EvaluateTrial(evidence profilerevidence.VerifiedEvidence, frozenPolicy FrozenPolicy) (Result, *profilederivation.Trial, error)`; `Evaluate` keeps its current signature

- [ ] **Step 1: Write the failing test**

Add to `internal/profiler/admission/admitter_test.go`, reusing whatever fixture the existing `Evaluate` test builds:

```go
func TestEvaluateTrialReturnsTheTrialOnlyWhenAccepted(t *testing.T) {
	evidence, policy := acceptedActiveFixture(t)
	result, trial, err := EvaluateTrial(evidence, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictAccepted {
		t.Fatalf("verdict = %q, want Accepted", result.Verdict)
	}
	if trial == nil || trial.Batch < 1 || trial.TrialID == "" {
		t.Fatalf("accepted evaluation returned no usable trial: %+v", trial)
	}
}
```

Name `acceptedActiveFixture` after whatever helper the existing accepted-path test already uses; do not build a second fixture.

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/profiler/admission/ -run TestEvaluateTrial -v
```
Expected: FAIL — `undefined: EvaluateTrial`.

- [ ] **Step 3: Refactor `Evaluate` into `EvaluateTrial` plus a wrapper**

```go
func Evaluate(
	evidence profilerevidence.VerifiedEvidence,
	frozenPolicy FrozenPolicy,
) (Result, error) {
	result, _, err := EvaluateTrial(evidence, frozenPolicy)
	return result, err
}

// EvaluateTrial validates the immutable evidence chain, applies the named
// policy, and projects the derivation trial only for Accepted evidence.
func EvaluateTrial(
	evidence profilerevidence.VerifiedEvidence,
	frozenPolicy FrozenPolicy,
) (Result, *profilederivation.Trial, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return Result{}, nil, err
	}
	policy, err := frozenPolicy.AdmissionPolicy()
	if err != nil {
		return Result{}, nil, err
	}
	_, frozenRecord, err := frozenPolicy.Records()
	if err != nil {
		return Result{}, nil, err
	}
	if frozenRecord.WorkloadMode != execution.Intent.Measurement.WorkloadMode {
		return Result{}, nil, errors.New("admission policy workload mode does not match execution intent")
	}
	trial, err := projectTrial(execution, raw)
	if err != nil {
		return Result{}, nil, err
	}
	evaluated := profilederivation.Admit(trial, policy)
	compact, err := compactResult(evaluated.Verdict, evaluated.PolicyVersion, evaluated.Reasons)
	if err != nil {
		return Result{}, nil, err
	}
	if evaluated.Verdict != profilederivation.VerdictAccepted {
		return compact, nil, nil
	}
	return compact, &trial, nil
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./internal/profiler/... -v 2>&1 | tail -20
```
Expected: PASS, including every existing `Evaluate` caller.

- [ ] **Step 5: Commit**

```bash
git add internal/profiler/admission
git commit -m "refactor: expose the admitted active trial"
```

---

### Task 6: Controller — projection, Run builder, reconciler

**Files:**
- Create: `internal/batchsaturation/controller/projection.go`
- Create: `internal/batchsaturation/controller/projection_test.go`
- Create: `internal/batchsaturation/controller/run_builder.go`
- Create: `internal/batchsaturation/controller/run_builder_test.go`
- Create: `internal/batchsaturation/controller/reconciler.go`
- Create: `internal/batchsaturation/controller/setup.go`
- Modify: `.go-arch-lint.yml`

**Interfaces:**
- Consumes: `batchsaturation.Snapshot/Trial/Decide` (Task 3), `edgev1alpha1.BatchSaturation` (Task 2)
- Produces: `ProjectSnapshot(*edgev1alpha1.BatchSaturation, []edgev1alpha1.ProfilingRun) (batchsaturation.Snapshot, error)`, `BuildRun(*edgev1alpha1.BatchSaturation, batchsaturation.Measurement, int) (*edgev1alpha1.ProfilingRun, error)`, `RunName(parent string, m batchsaturation.Measurement, attempt int) string`, `Reconciler`, `(*Reconciler).SetupWithManager(ctrl.Manager) error`

- [ ] **Step 1: Write the failing Run-builder test**

```go
package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
)

func TestRunNameIsDeterministic(t *testing.T) {
	name := RunName("jetsono-ortcpu-m50", batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1)
	if name != "jetsono-ortcpu-m50-active-b2-a1" {
		t.Fatalf("name = %q", name)
	}
}

func TestBuildRunFillsTheIdleUnionFields(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle, Batch: 1,
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode != "idle-model-loaded" || measurement.WarmupIterations != 0 ||
		measurement.InputGenerator != "none" || measurement.OutputValidation != "none" ||
		measurement.SettlingDurationSeconds == nil {
		t.Fatalf("idle Run does not satisfy the ProfilingRun union rule: %+v", measurement)
	}
	if run.Spec.MeasurementStateSpec != parent.Spec.State {
		t.Fatalf("Run did not inherit the shared state")
	}
	owner := run.OwnerReferences
	if len(owner) != 1 || owner[0].Controller == nil || !*owner[0].Controller ||
		string(owner[0].UID) != string(parent.UID) {
		t.Fatalf("Run is not controller-owned by its parent: %+v", owner)
	}
}

func TestBuildRunUsesTheActiveProtocol(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode != "saturated-serial-fixed-batch" || measurement.Batch != 2 ||
		measurement.SettlingDurationSeconds != nil ||
		measurement.WarmupIterations != parent.Spec.ActiveProtocol.WarmupIterations {
		t.Fatalf("active Run does not match the active protocol: %+v", measurement)
	}
}

func testParent() *edgev1alpha1.BatchSaturation {
	return &edgev1alpha1.BatchSaturation{
		ObjectMeta: metav1.ObjectMeta{
			Name: "jetsono-ortcpu-m50", Namespace: "chill-system", UID: "parent-uid",
		},
		Spec: edgev1alpha1.BatchSaturationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: "mobilenet-v2-050", DeviceClass: "jetson-agx-orin-64g",
					ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
				},
				TargetNode: "jetsono", CPURequest: resource.MustParse("100m"),
				AdmissionPolicy: "wall-power-2p5s-source-v1",
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				WarmupIterations: 20, DurationSeconds: 75,
				InputGenerator: "numpy-default-rng-seed-0-fp32-v1",
				OutputValidation: "nonempty-shape-batch-v1",
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 2500, TimeoutMilliseconds: 2400, DurationSeconds: 78,
				},
			},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{
				SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 900, DurationSeconds: 31,
				},
			},
			Method: edgev1alpha1.MeasurementMethodSpec{
				Name: edgev1alpha1.MethodSequentialEarlyStop,
				SequentialEarlyStop: &edgev1alpha1.SequentialEarlyStopSpec{
					MinDelta: "0.05", Patience: 1, TrialsPerBatch: 3, DivergenceFactor: "1.10",
				},
			},
			RetryLimit: 2,
		},
	}
}
```

Add the `metav1` and `resource` imports the fixture needs.

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/batchsaturation/controller/ -v
```
Expected: FAIL — `undefined: RunName`, `undefined: BuildRun`.

- [ ] **Step 3: Implement the Run builder**

Create `internal/batchsaturation/controller/run_builder.go`:

```go
// Package controller adapts owned ProfilingRun objects to the pure
// batch-saturation progress rules. It performs no derivation of its own.
package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	activeWorkloadMode = "saturated-serial-fixed-batch"
	idleWorkloadMode   = "idle-model-loaded"
	idleInputGenerator = "none"
	idleOutputValidation = "none"

	labelBatchSaturation = "edge.dacs.io/batch-saturation"
	labelWorkloadMode    = "edge.dacs.io/workload-mode"
	labelBatch           = "edge.dacs.io/batch"
	labelAttempt         = "edge.dacs.io/attempt"
)

// RunName is deterministic so re-executing one decision collides instead of
// creating a duplicate trial.
func RunName(parent string, measurement batchsaturation.Measurement, attempt int) string {
	return fmt.Sprintf("%s-%s-b%d-a%d", parent, measurement.WorkloadMode, measurement.Batch, attempt)
}

func BuildRun(
	parent *edgev1alpha1.BatchSaturation,
	measurement batchsaturation.Measurement,
	attempt int,
) (*edgev1alpha1.ProfilingRun, error) {
	if parent == nil {
		return nil, errors.New("a parent BatchSaturation is required")
	}
	if err := measurement.Validate(); err != nil {
		return nil, fmt.Errorf("validate measurement: %w", err)
	}
	if attempt < 1 {
		return nil, errors.New("attempt must be positive")
	}
	spec := edgev1alpha1.ProfilingRunSpec{MeasurementStateSpec: parent.Spec.State}
	switch measurement.WorkloadMode {
	case batchsaturation.WorkloadModeActive:
		protocol := parent.Spec.ActiveProtocol
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:            int32(measurement.Batch),
			WarmupIterations: protocol.WarmupIterations,
			DurationSeconds:  protocol.DurationSeconds,
			WorkloadMode:     activeWorkloadMode,
			InputGenerator:   protocol.InputGenerator,
			OutputValidation: protocol.OutputValidation,
		}
		spec.MetricObservation = protocol.MetricObservation
	case batchsaturation.WorkloadModeIdle:
		protocol := parent.Spec.IdleProtocol
		settling := protocol.SettlingDurationSeconds
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:                   int32(measurement.Batch),
			WarmupIterations:        0,
			SettlingDurationSeconds: &settling,
			DurationSeconds:         protocol.DurationSeconds,
			WorkloadMode:            idleWorkloadMode,
			InputGenerator:          idleInputGenerator,
			OutputValidation:        idleOutputValidation,
		}
		spec.MetricObservation = protocol.MetricObservation
	}
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RunName(parent.Name, measurement, attempt),
			Namespace: parent.Namespace,
			Labels: map[string]string{
				labelBatchSaturation: parent.Name,
				labelWorkloadMode:    string(measurement.WorkloadMode),
				labelBatch:           fmt.Sprintf("%d", measurement.Batch),
				labelAttempt:         fmt.Sprintf("%d", attempt),
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				parent, edgev1alpha1.GroupVersion.WithKind("BatchSaturation"),
			)},
		},
		Spec: spec,
	}, nil
}
```

- [ ] **Step 4: Run to verify the builder passes**

```bash
go test ./internal/batchsaturation/controller/ -v
```
Expected: PASS.

- [ ] **Step 5: Write the failing projection test**

```go
func TestProjectSnapshotReadsValuesFromTheRunSpecNotLabels(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	run.Labels[labelBatch] = "99"
	run.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "v1",
	}
	snapshot, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshot.Trials) != 1 || snapshot.Trials[0].Measurement.Batch != 2 {
		t.Fatalf("projection trusted the label instead of the spec: %+v", snapshot.Trials)
	}
	if snapshot.Trials[0].Verdict != batchsaturation.VerdictAccepted {
		t.Fatalf("verdict = %q", snapshot.Trials[0].Verdict)
	}
}

func TestProjectSnapshotRejectsAForeignRun(t *testing.T) {
	parent := testParent()
	run, _ := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle, Batch: 1,
	}, 1)
	run.OwnerReferences[0].UID = "someone-else"
	if _, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}); err == nil {
		t.Fatal("a Run owned by another object was accepted")
	}
}
```

- [ ] **Step 6: Run to verify it fails**

```bash
go test ./internal/batchsaturation/controller/ -run TestProjectSnapshot -v
```
Expected: FAIL — `undefined: ProjectSnapshot`.

- [ ] **Step 7: Implement the projection**

Create `internal/batchsaturation/controller/projection.go`:

```go
package controller

import (
	"errors"
	"fmt"
	"strconv"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const capabilityReasonCode = "ArtifactBatchUnsupported"

// ProjectSnapshot verifies ownership and reads every value from the child spec.
// Labels select Runs; they are never the source of a projected value.
func ProjectSnapshot(
	parent *edgev1alpha1.BatchSaturation,
	runs []edgev1alpha1.ProfilingRun,
) (batchsaturation.Snapshot, error) {
	if parent == nil {
		return batchsaturation.Snapshot{}, errors.New("a parent BatchSaturation is required")
	}
	snapshot := batchsaturation.Snapshot{RetryLimit: int(parent.Spec.RetryLimit)}
	for index := range runs {
		run := &runs[index]
		owner := metav1.GetControllerOf(run)
		if owner == nil || owner.Kind != "BatchSaturation" ||
			owner.APIVersion != edgev1alpha1.GroupVersion.String() ||
			string(owner.UID) != string(parent.UID) {
			return batchsaturation.Snapshot{}, fmt.Errorf(
				"ProfilingRun %q is not controller-owned by this BatchSaturation", run.Name,
			)
		}
		trial, err := projectTrial(run)
		if err != nil {
			return batchsaturation.Snapshot{}, fmt.Errorf("project ProfilingRun %q: %w", run.Name, err)
		}
		snapshot.Trials = append(snapshot.Trials, trial)
	}
	if err := snapshot.Validate(); err != nil {
		return batchsaturation.Snapshot{}, fmt.Errorf("validate projected snapshot: %w", err)
	}
	return snapshot, nil
}

func projectTrial(run *edgev1alpha1.ProfilingRun) (batchsaturation.Trial, error) {
	mode := batchsaturation.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == idleWorkloadMode {
		mode = batchsaturation.WorkloadModeIdle
	}
	attempt, err := strconv.Atoi(run.Labels[labelAttempt])
	if err != nil || attempt < 1 {
		return batchsaturation.Trial{}, errors.New("owned Run has no usable attempt label")
	}
	trial := batchsaturation.Trial{
		Measurement: batchsaturation.Measurement{
			WorkloadMode: mode, Batch: int(run.Spec.Measurement.Batch),
		},
		Attempt: attempt,
		Verdict: batchsaturation.VerdictPending,
	}
	if run.Status.Admission != nil {
		if run.Status.Admission.Verdict == "Accepted" {
			trial.Verdict = batchsaturation.VerdictAccepted
			return trial, nil
		}
		trial.Verdict = batchsaturation.VerdictFailed
		return trial, nil
	}
	for _, condition := range run.Status.Conditions {
		if condition.Type != "IntentFrozen" && condition.Type != "ExecutionComplete" {
			continue
		}
		if condition.Status != metav1.ConditionFalse || condition.Reason == "" {
			continue
		}
		if isTerminalReason(condition.Reason) {
			trial.Verdict = batchsaturation.VerdictFailed
			trial.CapabilityRejected = condition.Reason == capabilityReasonCode
			return trial, nil
		}
	}
	return trial, nil
}

func isTerminalReason(reason string) bool {
	return reason == capabilityReasonCode || reason == "TerminalFreezeFailure" ||
		reason == "ExecutionFailed"
}
```

The attempt is the one value read from a label because it is not part of the Run spec. Confirm the exact terminal condition reasons the Profiler emits before finalizing `isTerminalReason`:

```bash
grep -rn "Reason:" internal/profiler/controller/freeze_failure.go internal/profiler/controller/execution_finalizer.go
```

- [ ] **Step 8: Run to verify projection passes**

```bash
go test ./internal/batchsaturation/controller/ -v
```
Expected: PASS.

- [ ] **Step 9: Implement the reconciler and setup**

Create `internal/batchsaturation/controller/reconciler.go` with a `Reconciler` holding a `client.Client`, `Scheme`, and an evidence loader. Its `Reconcile`:

```go
func (r *Reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	parent := &edgev1alpha1.BatchSaturation{}
	if err := r.Get(ctx, request.NamespacedName, parent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if parent.Status.Phase == phaseComplete || parent.Status.Phase == phaseFailed {
		return ctrl.Result{}, nil
	}
	var runs edgev1alpha1.ProfilingRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(parent.Namespace),
		client.MatchingLabels{labelBatchSaturation: parent.Name},
	); err != nil {
		return ctrl.Result{}, err
	}
	snapshot, err := ProjectSnapshot(parent, runs.Items)
	if err != nil {
		return ctrl.Result{}, err
	}
	method, err := r.methodFor(ctx, parent, runs.Items)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision, err := batchsaturation.Decide(snapshot, method)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.execute(ctx, parent, snapshot, decision)
}
```

`execute` handles the four actions:

```go
const (
	phaseMeasuring = "Measuring"
	phaseComplete  = "Complete"
	phaseFailed    = "Failed"
)

func (r *Reconciler) execute(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
	snapshot batchsaturation.Snapshot,
	decision batchsaturation.Decision,
) (ctrl.Result, error) {
	switch decision.Action {
	case batchsaturation.ActionCreateRun:
		attempt := 1
		for _, trial := range snapshot.Trials {
			if trial.Measurement == *decision.Run && trial.Attempt >= attempt {
				attempt = trial.Attempt + 1
			}
		}
		run, err := BuildRun(parent, *decision.Run, attempt)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, run); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.updateStatus(ctx, parent, phaseMeasuring, "", nil, snapshot)
	case batchsaturation.ActionWait:
		return ctrl.Result{RequeueAfter: r.RequeueInterval},
			r.updateStatus(ctx, parent, phaseMeasuring, "", nil, snapshot)
	case batchsaturation.ActionFinish:
		reference, err := r.writeResult(ctx, parent, decision.Result)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.updateStatus(
			ctx, parent, phaseComplete, decision.Result.Outcome, reference, snapshot,
		)
	case batchsaturation.ActionFail:
		return ctrl.Result{}, r.updateStatus(ctx, parent, phaseFailed, decision.Reason, nil, snapshot)
	}
	return ctrl.Result{}, fmt.Errorf("unsupported decision action %q", decision.Action)
}

func (r *Reconciler) updateStatus(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
	phase string,
	outcome string,
	result *edgev1alpha1.ProfilingRunEvidenceReference,
	snapshot batchsaturation.Snapshot,
) error {
	accepted := 0
	for _, trial := range snapshot.Trials {
		if trial.Verdict == batchsaturation.VerdictAccepted {
			accepted++
		}
	}
	parent.Status.ObservedGeneration = parent.Generation
	parent.Status.Phase = phase
	parent.Status.AcceptedTrials = int32(accepted)
	if outcome != "" {
		parent.Status.Outcome = outcome
	}
	if result != nil {
		parent.Status.Result = result
	}
	return r.Status().Update(ctx, parent)
}
```

`writeResult` marshals the method's `Result.Detail` and stores it under the aggregate key, mirroring `internal/profiler/controller/execution_evidence_keys.go`:

```go
func batchSaturationObjectKey(uid types.UID, kind string) string {
	return "batch-saturations/" + string(uid) + "/" + kind + ".json"
}
```

`methodFor` parses the string-typed parameters, builds the config, and supplies a `CurveSource` closure:

```go
func (r *Reconciler) methodFor(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
	runs []edgev1alpha1.ProfilingRun,
) (batchsaturation.Method, error) {
	if parent.Spec.Method.Name != edgev1alpha1.MethodSequentialEarlyStop {
		return nil, fmt.Errorf("unsupported measurement method %q", parent.Spec.Method.Name)
	}
	parameters := parent.Spec.Method.SequentialEarlyStop
	minDelta, err := strconv.ParseFloat(parameters.MinDelta, 64)
	if err != nil {
		return nil, fmt.Errorf("parse minDelta: %w", err)
	}
	divergence, err := strconv.ParseFloat(parameters.DivergenceFactor, 64)
	if err != nil {
		return nil, fmt.Errorf("parse divergenceFactor: %w", err)
	}
	return batchsaturation.NewSequentialEarlyStop(batchsaturation.SequentialConfig{
		MinDelta:         minDelta,
		Patience:         int(parameters.Patience),
		TrialsPerBatch:   int(parameters.TrialsPerBatch),
		DivergenceFactor: divergence,
	}, func(batches []int) (batchsaturation.CurveResult, error) {
		return r.deriveSequential(ctx, parent, runs, batches, parameters)
	})
}
```

`deriveSequential` is the only place that touches evidence. It loads each admitted Run's verified evidence through the same store client `RunReconciler` uses, calls `admission.EvaluateTrial` for active Runs and `admission.EvaluateIdle` for idle ones, verifies the family with `admission.ProjectVerifiedCurveFamilyMember`, builds one `profilederivation.Pass{Role: PassDiscovery}` whose `Batches` carry the admitted `CurveRun` values, calls `profilederivation.DeriveCurve`, then `profilederivation.DeriveSequentialBatchDecision`, and maps the returned `SequentialResult` into `batchsaturation.CurveResult`:

```go
	return batchsaturation.CurveResult{
		Continue:      sequential.Status == profilederivation.SequentialContinue,
		SelectedBatch: selectedBatch(sequential),
		Outcome:       "Stopped/" + string(sequential.StopReason),
		Detail:        sequential,
	}, nil
```

Cache loaded evidence by Run UID in a `map[types.UID]` field on the `Reconciler`. The objects are immutable, so a stale cache entry is impossible and losing the cache costs one reload.

- [ ] **Step 9b: Anchor the family on first admission**

Plan 7 says `family.json` is written before the first Run. The resolved catalog facts it records do not exist until the Profiler freezes a Run's intent, so write it when the first trial is admitted instead, from that Run's frozen intent, and check every later trial against it. Store it at `batchSaturationObjectKey(parent.UID, "family")`. Correct the one sentence in Plan 7 that claims it precedes the first Run.

Create `internal/batchsaturation/controller/setup.go`:

```go
package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers direct BatchSaturation events and owned Run
// transitions, so a child reaching a verdict wakes the parent.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("batchsaturation").
		For(&edgev1alpha1.BatchSaturation{}).
		Owns(&edgev1alpha1.ProfilingRun{}).
		Complete(r)
}
```

- [ ] **Step 10: Register the architecture component**

```yaml
  batch-saturation-controller: { in: internal/batchsaturation/controller }
```
```yaml
  batch-saturation-controller:
    mayDependOn:
      - api
      - batch-saturation-core
      - evidence-core
      - evidence-kube
      - profile-derivation
      - profiler-admission
      - profiler-evidence
      - profiler-evidence-kube
      - profiling-run
```

- [ ] **Step 11: Verify**

```bash
go build ./... && go test ./internal/batchsaturation/... -v && make arch-check
```
Expected: all pass.

- [ ] **Step 12: Commit**

```bash
git add internal/batchsaturation .go-arch-lint.yml
git commit -m "feat: reconcile BatchSaturation into owned Runs"
```

---

### Task 7: Wire it up and prove one measurement end to end

**Files:**
- Modify: `cmd/profiler/main.go:58`
- Modify: `config/rbac/role.yaml` and the Profiler role in `charts/chill/templates/`
- Create: `internal/batchsaturation/controller/reconciler_test.go` (envtest)

**Interfaces:**
- Consumes: everything above
- Produces: a running controller; no new exported symbols

- [ ] **Step 1: Register the controller**

In `cmd/profiler/main.go`, after `setupRunController`:

```go
	if err := setupBatchSaturationController(mgr, opts); err != nil {
		setupLog.Error(err, "unable to register reconciler", "resource", "BatchSaturation")
		os.Exit(1)
	}
```

Add `setupBatchSaturationController` beside the existing `setupRunController`, constructing the `Reconciler` with the same client, scheme, and evidence-store dependencies that `setupRunController` already resolves.

- [ ] **Step 2: Add RBAC markers**

On the `Reconciler`:

```go
// +kubebuilder:rbac:groups=edge.dacs.io,resources=batchsaturations,verbs=get;list;watch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=batchsaturations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=profilingruns,verbs=get;list;watch;create
```

```bash
make manifests
git diff config/rbac/role.yaml
```
Expected: the three rules appear. Mirror them into the Profiler role in `charts/chill/templates/`.

- [ ] **Step 3: Write the envtest**

```go
func TestReconcileCreatesTheFirstIdleTrial(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(testParent()).WithStatusSubresource(&edgev1alpha1.BatchSaturation{}).Build()
	reconciler := &Reconciler{Client: client, Scheme: testScheme(t)}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "jetsono-ortcpu-m50", Namespace: "chill-system"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var runs edgev1alpha1.ProfilingRunList
	if err := client.List(context.Background(), &runs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs.Items) != 1 || runs.Items[0].Name != "jetsono-ortcpu-m50-idle-b1-a1" {
		t.Fatalf("unexpected Runs: %+v", runs.Items)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(testParent()).WithStatusSubresource(&edgev1alpha1.BatchSaturation{}).Build()
	reconciler := &Reconciler{Client: client, Scheme: testScheme(t)}
	request := ctrl.Request{NamespacedName: types.NamespacedName{
		Name: "jetsono-ortcpu-m50", Namespace: "chill-system",
	}}
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatalf("reconcile %d: %v", attempt, err)
		}
	}
	var runs edgev1alpha1.ProfilingRunList
	if err := client.List(context.Background(), &runs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("repeated reconciles created %d Runs, want 1", len(runs.Items))
	}
}
```

- [ ] **Step 4: Run**

```bash
go test ./internal/batchsaturation/... -v
```
Expected: PASS. The idempotence test is the claim that deterministic names prevent duplicate trials.

- [ ] **Step 5: Full gates**

```bash
make lint && make test && make arch-check && make helm-lint helm-template
```
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: run BatchSaturation in the Profiler"
```

---

### Task 8: Record the architecture decision

**Files:**
- Create: `docs/adr/0016-aggregate-measurement-unit.md`
- Modify: `docs/design/profiling/plans/07-batch-saturation-control-path.md` — Status to implemented

- [ ] **Step 1: Write the ADR**

Follow the shape of `docs/adr/0015-node-power-actuation-boundary.md`: context (Run is the largest unit CHILL understands; composing Runs lived outside the system), decision (a namespaced `BatchSaturation` owns the Runs it creates; measurement methods are named modules behind one pure `Plan` interface; `ProfilingRun` stays independently submittable), consequences (evidence schemas moved to `v1alpha4`; measurements recorded only in documents must be run again; support level is read from status).

- [ ] **Step 2: Update the plan Status**

Change the Plan 7 `Status` first line from `Approved design, not yet implemented.` to a statement of what is implemented and what is not, and update the corresponding entry in `docs/design/profiling/README.md`.

- [ ] **Step 3: Commit**

```bash
git add docs
git commit -m "docs: record the aggregate measurement unit decision"
```

---

## Deferred, with the reason each is safe

These are named in Plan 7 as outside first scope. Do not implement them here.

| Deferred | Why absence costs waste, not correctness |
|---|---|
| Per-trial environment-identity pre-check | `VerifyCurveFamily` catches drift at derivation |
| No-progress timeout | A stalled measurement is visible and can be deleted |
| Deletion finalizer | A measurement is not deleted while running; restore the power mode by hand if it is |
| `two-pass-confirmatory` method | `sequential-early-stop` proves the module boundary; the second method adds a `Plan` implementation and one enum value, changing neither `Decide` nor the controller |
