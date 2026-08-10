# Plan 7: Batch-Saturation Control Path

> Navigation: [Profiling design index](../README.md)
>
> Method: [Plan 5a](05a-sequential-batch-stop.md), [Plan 5](05-batch-saturation-campaign.md)

## Status

Approved design, under implementation. Plan 5a and Plan 5 own the estimand,
acceptance rule, and sizing method; this plan owns the control path that
executes them. It supersedes the document-driven measurement procedure those
plans described, including the committed contract and policy files and the
per-measurement record directory.

## Question

> Can one declared `BatchSaturation` object drive a whole `b_sat` measurement —
> idle baseline, batch sweep, derivation, and a typed result — without a human
> step between Runs?

## What this plan does

`ProfilingRun` is the largest unit CHILL currently understands. Deriving
`b_sat` needs many Runs whose composition is itself a decision, so that
composition has lived outside the system: in committed JSON, a record document,
and a person submitting Run YAML in order and reading each result before
writing the next one.

This plan promotes that composition to a first-class object. A `BatchSaturation`
owns the `ProfilingRun` objects it creates, decides the next measurement from
the ones already admitted, and publishes one typed outcome. The scientific
discipline that the documents carried does not relax; its enforcement moves from
human diligence to types, CEL, and object identity.

`ProfilingRun` remains independently submittable. It is not demoted to an
internal execution unit.

## Checkpoint 1: API surface

### Shared state extraction

A `ProfilingRun` declares its execution state as one block. That block is
extracted so the controller assembles it from one type instead of restating
field by field.

```go
type MeasurementStateSpec struct {
    Candidate       ProfilingRunCandidateSpec `json:"candidate"`
    TargetNode      string                    `json:"targetNode"`
    CPURequest      resource.Quantity         `json:"cpuRequest"`
    AdmissionPolicy string                    `json:"admissionPolicy"`
}

type ProfilingRunSpec struct {
    MeasurementStateSpec `json:",inline"`
    Measurement          ProfilingRunMeasurementSpec       `json:"measurement"`
    MetricObservation    ProfilingRunMetricObservationSpec `json:"metricObservation"`
}
```

The embedding is inline, so field positions, the generated CRD, and all three
existing CEL rules are unchanged. This is a refactor with no API shape change.

### What a measurement shares, and what varies with batch

Every trial in one measurement shares its model, device class, power mode,
node, CPU request, and admission policy. It does not share an execution path.
`ModelExecutionPath` names exactly one `ModelArtifact`, and a derived artifact
encodes exactly one `fixedBatch`, so a runtime that compiles the batch into the
artifact needs a different path for every batch it can measure. A runtime that
consumes batch as a call argument uses one path for all of them.

The parent therefore declares the batch-invariant state and the runtime that
selects among paths. It does not name an execution path, because that is the
one part of the state the sweep varies.

```go
type BatchSaturationStateSpec struct {
    Candidate       BatchSaturationCandidateSpec `json:"candidate"`
    TargetNode      string                       `json:"targetNode"`
    CPURequest      resource.Quantity            `json:"cpuRequest"`
    AdmissionPolicy string                       `json:"admissionPolicy"`
}

type BatchSaturationCandidateSpec struct {
    ModelSpec   string                  `json:"modelSpec"`
    DeviceClass string                  `json:"deviceClass"`
    PowerMode   string                  `json:"powerMode"`
    Runtime     ModelRuntimeRequirement `json:"runtime"`
}
```

`Runtime` is the existing `{family, backend}` pair. It is the one execution
choice a person still makes: a Node that supplies both an ONNX Runtime CPU path
and an RKNNLite NPU path for the same model offers two families to characterize,
and which one is being measured is an experimental decision, not a fact the
catalog can settle.

### The BatchSaturation resource

```go
// +kubebuilder:resource:scope=Namespaced,shortName=bsat
type BatchSaturationSpec struct {
    State          BatchSaturationStateSpec `json:"state"`
    MaxBatch       int32                    `json:"maxBatch"`
    ActiveProtocol ActiveProtocolSpec       `json:"activeProtocol"`
    IdleProtocol   IdleProtocolSpec         `json:"idleProtocol"`
    Method         MeasurementMethodSpec    `json:"method"`
    RetryLimit     int32                    `json:"retryLimit"`
}
```

`MaxBatch` bounds the sweep for both artifact kinds. A fixed-batch family also
ends where the catalog stops declaring members, which may be earlier. A
dynamic-batch family has no catalog end at all, so without this bound its only
stopping condition would be the method's own energy rule.

`ActiveProtocolSpec` and `IdleProtocolSpec` are separate blocks rather than one
union discriminated by workload mode. `IdleProtocolSpec` has no
`warmupIterations`, `inputGenerator`, or `outputValidation` field at all; the
controller supplies the required values when it assembles a Run. The union CEL
rule that `ProfilingRunSpec` needs for standalone submission therefore has no
counterpart here.

`MeasurementMethodSpec` is a discriminated union: a method name plus exactly one
parameter block, with CEL requiring the block to match the name.

`spec` is immutable under `self == oldSelf`, as `ProfilingRunSpec` already is.
Parent names are bounded at 200 characters so generated child names stay within
253.

### The resolved family map

The controller resolves which execution path measures which batch. Nobody
transcribes it, because every fact it needs is already declared in the catalog:

```text
for each ModelExecutionPath in the named ModelSpec
    keep it if its Runtime matches spec.state.candidate.runtime
    look up the ModelArtifact it names
        artifact has a Derivation  -> that path measures Derivation.FixedBatch
        artifact has no Derivation -> that path measures every batch
```

The result is a batch-ordered map from batch to execution path, bounded by
`spec.maxBatch`, resolved once and frozen before the first Run. A person
declares what to characterize; the map that follows is a calculation, so a batch
cannot be silently omitted or misspelled into a different artifact.

The frozen map records what the catalog decides — batch, execution path,
artifact name, fixed-batch capability, and input contract. It does not record
artifact supply. Which image delivers a path on a device class is installation
configuration the Profiler owns and already freezes into each Run's intent
record, so restating it here would duplicate a fact this layer is not
authoritative for and would make the aggregate layer read the Profiler's
fact source. `CurveFamilyContract` remains the planned contract and
`CurveFamily` the realized one, verified after the Loader boundary, exactly as
ADR 0014 splits them.

The map is also the whole definition of the capability boundary. The first
batch the map does not cover is the boundary, and the controller creates no Run
there — which is what `DeriveCurve` already requires of a boundary batch, an
empty final batch exactly one past the last frozen member. A dynamic-batch
family covers every batch up to `maxBatch` and so has no boundary.

This resolution is the only place the aggregate layer reads the catalog. Once a
path is chosen, the child Run carries it and the Profiler resolves artifact
name, image, path, and runtime compatibility exactly as it does for a Run
submitted by hand.

### Status is not authority

```go
type BatchSaturationStatus struct {
    ObservedGeneration int64
    Conditions         []metav1.Condition
    Phase              string  // Measuring | Deriving | Complete | Failed
    AcceptedTrials     int32
    Outcome            string  // typed terminal outcome
    Result             *ProfilingRunEvidenceReference
}
```

`Result` reuses the existing evidence reference type — store, key, schema
version, and size — because the full result is an evidence-store object and is
located exactly the way a Run locates its own evidence.

Status is a derived display for operators and `kubectl get`. The decision input
is `spec` plus the owned `ProfilingRun` objects, never status. The `Snapshot`
type carries no status field, so reading it back is not possible rather than
discouraged.

## Checkpoint 2: Method modules

### Where the split falls

Progress rules that every method shares — one live Run at a time, retry within
the declared limit, waiting — belong to one function. Only the measurement
question is pluggable.

```go
// Shared progress rules.
func Decide(snapshot Snapshot, method Method) (Decision, error)

// What a method implements, and all it implements.
type Method interface {
    Plan(observed Observed) (Plan, error)
}

type Observed struct {
    Accepted           []Measurement
    CapabilityBoundary *int
}

type Plan struct {
    Next   *Measurement // {WorkloadMode, Batch}
    Result *Result
}
```

`Decide` resolves in order: a live Run yields `Wait`; an unfinished trial within
`RetryLimit` yields a retry at the same batch with the next attempt number;
exceeding the limit yields a typed failure; otherwise the method is asked what
to measure next.

`CapabilityBoundary` is projected from the resolved family map, not from any
Run outcome. It is known before the first trial and does not change while the
measurement runs, so a method reading it is reading configuration rather than
inferring a limit from failures. This is why no condition reason means "the
artifact refused this batch": a batch outside the map never becomes a Run.

`Plan` receives admitted trials only. A method has no way to see a failed,
pending, or retried attempt, so preferring one measurement over another on the
basis of its outcome is not a rule to follow but a value the signature cannot
express. Adding a method means implementing `Plan` and adding one enum name;
neither `Decide` nor the controller changes.

### The two modules

| Method name | Calls | Owns |
|---|---|---|
| `sequential-early-stop` | `DeriveSequentialBatchDecision` | Plan 5a walk-up and stop rule |
| `two-pass-confirmatory` | `DeriveBSatDiscovery`, sizing, `DeriveBSatConfirmatory` | Plan 5 discovery, sizing, confirmation |

Both call existing pure functions in `internal/profilederivation`. This plan
adds no estimator and changes no acceptance rule.

`two-pass-confirmatory` counts its active-Run budget in admitted active trials.
Wasted attempts are bounded separately by `RetryLimit`, so the two numbers never
count the same thing.

## Checkpoint 3: Controller loop

```text
Reconcile(BatchSaturation):
  1. list owned ProfilingRun objects; verify each ownerRef UID
  2. project spec and Runs into a pure Snapshot
  3. Decide(snapshot)
  4. execute: CreateRun | Wait | Finish | Fail
  5. update status as derived display
```

`Owns(&ProfilingRun{})` wakes the parent on child transitions, so the requeue
after `Wait` is a backstop rather than the primary signal.

### Deterministic child names

```text
<parent>-<mode>-b<batch>-a<attempt>
```

Executing the same decision twice fails the second time with `AlreadyExists`.
A duplicate trial is therefore not a race to be detected but a name that is
already taken. This matters because the derivation rejects a batch whose trial
count differs from the declared one, which would discard the whole measurement.

### Evidence cache

A method needs projected measurements, not verdicts, so the controller loads
evidence for admitted Runs. Evidence objects are immutable, so caching them by
Run UID in process is safe: the cache holds copies of immutable objects, not
authoritative state, and losing it costs one reload.

### Run assembly

The controller composes `MeasurementStateSpec` from the parent's batch-invariant
state plus the execution path the resolved map gives for that batch, then adds
the batch, workload mode, and the matching protocol block. Idle Runs receive
`warmupIterations: 0`, `inputGenerator: none`, and `outputValidation: none`.
The existing union CEL rule on `ProfilingRunSpec` validates the assembled
object, so a mis-assembled Run is rejected by the API server rather than
trusted.

### One idle baseline per state, not per measurement

Incremental energy subtracts a model-loaded-idle baseline from an active trial
in the same exact execution state. When batches sit on different artifacts,
they sit in different states and each needs its own baseline: a compiled engine
reserves its own resources and draws its own idle power. Admission already
enforces this — a fixed-batch member is checked against an idle trial at the
member's own batch, and only a dynamic-batch member falls back to batch one.

The measurement therefore plans idle trials from the same resolved map that
plans active ones:

| Family kind | Idle trials |
|---|---|
| Dynamic batch — one path for every batch | One idle set at batch one, shared by every active batch |
| Fixed batch — one path per batch | One idle set per member, at that member's own batch |

A method that requests a single batch-one baseline can only measure the first
kind. The shared progress rules own this, not the method: the method asks for a
batch, and the trials that batch requires follow from the map.

How many trials a baseline needs is declared on `IdleProtocolSpec`, not taken
from the method's per-batch trial count. Derivation requires at least three, and
beyond that the count only narrows the baseline's confidence half-width — which
`sequential-early-stop` never reads, because its stop rule works on means. Tying
the two together would buy that method nothing and cost one idle Run per extra
active trial, at a settling-plus-duration each.

## Checkpoint 4: Failure handling

### One kind

A Run that does not reach an admitted verdict is retried at the same batch
within `RetryLimit`; exceeding the limit ends the measurement with a typed
failure. That is the whole taxonomy. A misconfigured state exhausts the limit
and stops, which costs a small bounded number of Runs and removes the need for
reason codes.

The capability boundary is not in this table because it is not a failure and
not an outcome. It is the edge of the resolved map, known before the first Run,
and the measurement simply has nothing to submit past it. Reading it back out
of a Run's failure condition would be inferring configuration from an accident.

The one rule that cannot be relaxed is the first: while any owned Run is live,
the decision is `Wait`. Two concurrent measurements on one Node are rejected as
CHILL-owned interference, and an unnoticed overlap contaminates power.

### Deliberately outside the first scope

These are excluded because their absence costs waste or convenience, never
correctness.

| Excluded | Why it is safe |
|---|---|
| Pre-checking environment identity per trial | `VerifyCurveFamily` catches drift at derivation. Pre-checking only saves Runs that would be discarded anyway |
| No-progress timeout | A stalled measurement is visible and can be deleted |
| Deletion finalizer | A measurement is not deleted while running. If it is, the Node power mode is restored by hand |

None of these changes the `Decide` signature when added later.

## Evidence store layout

```text
profiling-runs/<run-uid>/raw.json          unchanged
profiling-runs/<run-uid>/execution.json    unchanged
batch-saturations/<uid>/family.json        resolved family contract
batch-saturations/<uid>/result.json        derived result
```

The aggregate layer writes nothing under `profiling-runs/`. `result.json` holds
facts derived at this layer plus references to the input Runs; it never copies a
measured value, so each measurement exists in exactly one place. Excluded trials
are listed by reference with their exclusion reason.

`family.json` is the resolved map: which execution path and catalog artifact
measures each batch, and where the map ends. It is written before the first Run,
because it is resolved from the catalog rather than observed from results. This
mirrors the Run's own `spec` to intent record to evidence progression.

## Verification boundary

Pure decision and method logic is covered by table tests in the style of
`internal/nodeactuator/lifecycle_test.go`. One test reconstructs a snapshot from
an arbitrary set of child Runs and asserts the decision is unchanged, which is
the claim that makes owned-Run reconstruction preferable to accumulated status.

Map resolution is covered separately, because it is where a person's declaration
becomes the measurement's whole scope: one catalog with per-batch derived
artifacts resolves to distinct paths and a boundary where the members stop, and
one catalog with a single canonical artifact resolves to one path for every
batch and no boundary at all.

Controller coverage is two envtest scenarios: a completed measurement and an
exhausted retry limit.

Four invariants are enforced by construction and need no test: `Snapshot` has no
status field; `Observed` exposes admitted trials only; child names collide on
duplication; assembled Runs are validated by the API server.

## Code boundary

```text
internal/batchsaturation/            pure; imports nothing from this project
internal/batchsaturation/controller/ Kubernetes adapter
```

`.go-arch-lint.yml` gains this boundary in the same change. An ADR records the
aggregate measurement unit as an architectural boundary.

## What this plan removes

- `docs/design/profiling/campaigns/` and every file under it
- `internal/profilederivation/campaign_artifacts_test.go`, which validates a
  committed contract file that no longer exists
- `docs/design/profiling/foundations/runtime-execution/05-support-matrix.csv`.
  A measured row is read from `BatchSaturation` status. An unmeasured row then
  has no representation at all, which is accepted: a row nobody has declared a
  `BatchSaturation` for is a row nobody is measuring, and a hand-maintained
  matrix of such rows drifts from the cluster rather than describing it
- `ProfilingRunSpec.Repetitions` and the worker repetition structures behind it

Removing `Repetitions` raises `batch-characterization` and
`profiling-execution-evidence` to `v1alpha4`. Evidence written under the earlier
versions cannot be re-verified, so measurements already completed must be run
again to appear in the system. That is the intent: results reachable only
through a committed document are what this plan replaces.

Plan 5 and Plan 5a keep their estimand, acceptance rule, and sizing method.
Their procedural sections — freezing by commit, committing a second policy
before confirmation, an offline driver requesting the next member, name
grammars, and inclusion rules — are replaced by this control path.

## Not authorized by this plan

- a new estimator, acceptance rule, or sizing method
- a `DeviceProfile` API or scheduler consumption
- measurement of anything other than batch saturation
- parallel Runs on one Node
