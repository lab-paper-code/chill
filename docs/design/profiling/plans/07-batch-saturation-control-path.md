# Plan 7: Batch-Saturation Control Path

> Navigation: [Profiling design index](../README.md)
>
> Method: [Plan 5a](05a-sequential-batch-stop.md), [Plan 5](05-batch-saturation-campaign.md)

## Status

Implemented for the `sequential-early-stop` control path. The
`two-pass-confirmatory` method remains design work. Plan 5a and Plan 5 own the
estimand, acceptance rule, and sizing method; this plan owns the control path
that executes them. It supersedes the document-driven measurement procedure
those plans described, including the committed contract and policy files and
the per-measurement record directory.

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

Every trial in one measurement shares an execution state. That state is
extracted so the parent and the child use one type instead of two declarations
that must be kept equal.

```go
type MeasurementStateSpec struct {
    Candidate  ProfilingRunCandidateSpec `json:"candidate"`
    TargetNode string                    `json:"targetNode"`
    CPURequest resource.Quantity         `json:"cpuRequest"`
}

type ProfilingRunSpec struct {
    MeasurementStateSpec `json:",inline"`
    AdmissionPolicy      string                                `json:"admissionPolicy"`
    Measurement          ProfilingRunMeasurementSpec       `json:"measurement"`
    MetricObservation    ProfilingRunMetricObservationSpec `json:"metricObservation"`
}
```

The execution state embedding is inline, and `ProfilingRunSpec` retains
`admissionPolicy` as its own top-level field, so its field positions, generated
CRD, and all three existing CEL rules are unchanged.

The earlier shared placement of `AdmissionPolicy` was wrong. An admission
policy is per workload mode, while the active and idle child Runs of one
measurement have different workload modes. Each protocol block therefore owns
the admission policy for the child Run it defines.

### The BatchSaturation resource

```go
// +kubebuilder:resource:scope=Namespaced,shortName=bsat
type BatchSaturationSpec struct {
    State          MeasurementStateSpec  `json:"state"`
    ActiveProtocol ActiveProtocolSpec    `json:"activeProtocol"`
    IdleProtocol   IdleProtocolSpec      `json:"idleProtocol"`
    Method         MeasurementMethodSpec `json:"method"`
    RetryLimit     int32                 `json:"retryLimit"`
}
```

`ActiveProtocolSpec` and `IdleProtocolSpec` are separate blocks rather than one
union discriminated by workload mode, and each requires its own
`admissionPolicy`. `IdleProtocolSpec` has no
`warmupIterations`, `inputGenerator`, or `outputValidation` field at all; the
controller supplies the required values when it assembles a Run. The union CEL
rule that `ProfilingRunSpec` needs for standalone submission therefore has no
counterpart here.

`MeasurementMethodSpec` is a discriminated union: a method name plus exactly one
parameter block, with CEL requiring the block to match the name.

`spec` is immutable under `self == oldSelf`, as `ProfilingRunSpec` already is.
Parent names are bounded at 200 characters so generated child names stay within
253.

[ADR 0016](../../../adr/0016-declared-batch-size-artifact-capability.md) requires the
control path to resolve the selected artifact's declared `DeclaredBatchSizes` at
intent freeze. One artifact and execution path serve the batch sizes recorded in
the resulting curve plan. Later decisions consume only that recorded plan.

### Status is not authority

```go
type BatchSaturationStatus struct {
    ObservedGeneration int64
    Conditions         []metav1.Condition
    Phase              string  // Measuring | Complete | Failed
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
func Decide(snapshot Snapshot) (Decision, error)

// What a method implements, and all it implements.
type Method interface {
    Plan(observed Observed) (Plan, error)
}

type Observed struct {
    PlannedBatchSizes []int
    Accepted        []Trial
}

type Plan struct {
    Next   *Measurement // {WorkloadMode, Batch}
    Result *Result
}
```

`Decide` resolves in order: a live Run yields `Wait`; an unfinished trial within
`RetryLimit` yields a retry at the same batch with the next attempt number;
exceeding the limit yields a typed failure; otherwise the method is asked what
to measure next. `Snapshot` and `Observed` both carry the batch sizes recorded in
the curve plan. A gap inside that set is skipped by advancing to the next declared
batch size.

`Plan` receives admitted trials only. A method has no way to see a failed,
pending, or retried attempt, so preferring one measurement over another on the
basis of its outcome is not a rule to follow but a value the signature cannot
express. Adding a method means implementing `Plan` and adding one enum name;
neither `Decide` nor the controller changes.

### The two modules

| Method name | Calls | Owns |
|---|---|---|
| `sequential-early-stop` | `DeriveSequentialBatchDecision` | Plan 5a walk through the batch sizes recorded in the curve plan, including sparse sets, and stop rule |
| `two-pass-confirmatory` | `DeriveBSatDiscovery`, sizing, `DeriveBSatConfirmatory` | Plan 5 discovery, consecutive candidate-to-`KMax` sizing, confirmation |

Both call existing pure functions in `internal/profilederivation`. This plan
adds no estimator and changes no acceptance rule.

`two-pass-confirmatory` counts its active-Run budget in admitted active trials.
Wasted attempts are bounded separately by `RetryLimit`, so the two numbers never
count the same thing.

## Checkpoint 3: Controller loop

```text
Reconcile(BatchSaturation):
  1. resolve the artifact facts and call NewCurvePlan
  2. list owned ProfilingRun objects; verify each ownerRef UID
  3. project spec, recorded curve plan, and Runs into a pure Snapshot
  4. Decide(snapshot)
  5. execute: CreateRun | Wait | Finish | Fail
  6. update status as derived display
```

`Owns(&ProfilingRun{})` wakes the parent on child transitions, so the requeue
after `Wait` is a backstop rather than the primary signal.

A completed campaign still re-derives its result and recovers the immutable
result write on every reconcile, so periodic resync causes safe but unbounded
repeated work. A terminal short-circuit needs its own change because status
cannot become decision authority and an early stored-result read would make the
object store a dependency before the first Run can be created.

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

The controller composes `MeasurementStateSpec` from the parent with the batch,
workload mode, and the admission policy and measurement settings from the
matching protocol block. Idle Runs receive
`warmupIterations: 0`, `inputGenerator: none`, and `outputValidation: none`.
One model-loaded-idle baseline is planned for the artifact state, using the
first declared batch. Active Runs advance through the batch sizes recorded in the curve plan.
The existing union CEL rule on `ProfilingRunSpec` validates the assembled
object, so a mis-assembled Run is rejected by the API server rather than
trusted.

## Checkpoint 4: Failure handling

### Two kinds

| Kind | Handling |
|---|---|
| Curve plan batch sizes exhausted | The method returns the capability-boundary stop before creating an outside-set Run |
| A created Run fails, including a declared batch size absent from the built artifact | Retry within `RetryLimit`; exceeding it ends the measurement with a typed failure |

One terminal-failure classification covers created-Run failures. A misconfigured
state exhausts `RetryLimit` and stops, which costs a small bounded number of
Runs and keeps the failure path uniform.

The one rule that cannot be relaxed is the first: while any owned Run is live,
the decision is `Wait`. Two concurrent measurements on one Node are rejected as
CHILL-owned interference, and an unnoticed overlap contaminates power.

### Deliberately outside the first scope

These are excluded because their absence costs waste or convenience, never
correctness.

| Excluded | Why it is safe |
|---|---|
| Pre-checking environment identity per trial | `VerifyCurvePlan` catches drift at derivation. Pre-checking only saves Runs that would be discarded anyway |
| No-progress timeout | A stalled measurement is visible and can be deleted |
| Deletion finalizer | A measurement is not deleted while running. If it is, the Node power mode is restored by hand |
| Runtime-call batch or a derived artifact without an input tensor contract | No finite declared batch set and tensor contract exists to freeze, so the source fails before creating a Run |

None of these changes the `Decide` signature when added later.

## Evidence store layout

```text
profiling-runs/<run-uid>/raw.json           unchanged
profiling-runs/<run-uid>/execution.json     unchanged
batch-saturations/<uid>/curve-plan.json     recorded curve plan
batch-saturations/<uid>/result.json         derived result
```

The aggregate layer writes nothing under `profiling-runs/`; the aggregate in
`result.json` is derived at this layer and the record references its input Runs.
A method's retained per-trial statistics are references-plus-values whose source
Run and raw evidence identity are identified, so the copy remains traceable and
falsifiable. No raw sample or execution record is copied; excluded trials are
listed by reference with their exclusion reason.

`curve-plan.json` is the output of `NewCurvePlan` and records the one
artifact execution state plus its planned batch sizes, written before the
first Run. This mirrors the Run's own `spec` to intent record to evidence
progression.

## Verification boundary

Pure decision and method logic is covered by table tests in the style of
`internal/nodeactuator/lifecycle_test.go`. One test reconstructs a snapshot from
an arbitrary set of child Runs and asserts the decision is unchanged, which is
the claim that makes owned-Run reconstruction preferable to accumulated status.
Controller coverage is two envtest scenarios: a completed measurement and an
exhausted retry limit.

Four invariants are enforced by construction and need no test: `Snapshot` has no
status field; `Observed` exposes admitted trials only; child names collide on
duplication; assembled Runs are validated by the API server.

## Code boundary

```text
internal/batchsaturation/            pure; no CHILL-internal imports
internal/batchsaturation/controller/ Kubernetes adapter; imports batch-saturation-core
                                      plus api, evidence-core, profile-derivation,
                                      profiler-app/admission/controller/evidence,
                                      and profiling-run
```

The control adapter owns the upward composition needed to resolve candidates,
freeze prepared intents, record or recover the plan, and derive a curve from
Loader-verified evidence. Those dependencies do not flow back into the pure
core. `.go-arch-lint.yml` records the allowed edges; an ADR records the
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
before confirmation, an offline driver requesting the next declared batch size, name
grammars, and inclusion rules — are replaced by this control path.

## Not authorized by this plan

- a new estimator, acceptance rule, or sizing method
- a `DeviceProfile` API or scheduler consumption
- measurement of anything other than batch saturation
- parallel Runs on one Node
