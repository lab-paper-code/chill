# Runtime, Artifact, and Profiling Roadmap

## Status

Roadmap with the first three leaf plans and the `ProfilingRun` path through
durable execution-evidence verification and single-Run admission implemented.
Repeated-trial aggregation, curve derivation, and profile publication remain
incomplete. This document itself does not authorize later API or internal
changes.

## How to use this directory

This file is the entry point for the profiling design. Do not read every file
as one flat specification.

```text
README.md
├── plans/         ordered delivery boundaries and current scope
├── components/    reusable subsystem contracts consumed by plans
└── foundations/   fact audits and reasoning records; no implementation authority
```

Use this reading order:

1. Read this roadmap to locate the current end-to-end boundary.
2. Read only the relevant document under `plans/` for authorized scope and
   current implementation status.
3. Follow a `components/` document when that plan depends on a reusable
   subsystem contract.
4. Consult `foundations/` only when claim provenance, rejected assumptions, or
   boundary rationale is needed.

The `Status` section in each document is authoritative for whether its scope is
implemented, approved but incomplete, or analysis only. Repository code,
tests, and live verification remain the source of truth for implementation
facts; a stale document status must be corrected rather than inferred around.

## Purpose

Keep the end-to-end direction visible without assigning the whole system to
one package or one implementation change.

```text
catalog facts
    |
    v
static model-path compatibility
    |
    v
one immutable profiling Run on Kubernetes
    |
    v
runtime evidence + metric evidence
    |
    v
profile admission and b_sat derivation
    |
    v
future enumerator / scheduler
```

Each boundary is planned and approved separately. A later plan consumes the
previous plan's output without taking over its responsibility. If live
evidence exposes a bad earlier assumption, that earlier boundary is revised
separately.

## Split plans

### 1. Catalog and static compatibility

[plans/01-catalog-static-compatibility.md](plans/01-catalog-static-compatibility.md)

Answers only:

> Is this immutable model artifact and runtime path statically compatible
> with this stable DeviceClass?

The first read-only CPU ONNX Runtime path is implemented. Its output is a
static candidate report, not permission to execute profiling.

### 2. Profiling Run and Kubernetes execution — implemented first path

[plans/02-profiling-run-execution.md](plans/02-profiling-run-execution.md)

Answers only:

> Did Kubernetes and the selected runtime realize one immutable profiling
> decision, and what execution evidence was observed?

Its first CPU ONNX Runtime path is implemented through an immutable public Run,
one native Job attempt, raw evidence persistence, and finalized execution
evidence. Active and model-loaded idle are live-verified through the same
lifecycle.

### 3. Profile admission and derivation — implemented first read-only path

[plans/03-profile-admission-derivation.md](plans/03-profile-admission-derivation.md)

Answers only:

> Are the execution and metric observations sufficient to admit a profile, and
> what energy curve and `b_sat` follow from that evidence?

Its first pure read-only plan is approved against the retained real Run
evidence. Persistence remains deferred until the output contract survives
adversarial and live-evidence review.

### 4. Minimal Profiler control path — first bounded path implemented

[plans/04-minimal-profiler-control-path.md](plans/04-minimal-profiler-control-path.md)

Answers only:

> Can an operator-managed CHILL Profiler own one exact ProfilingRun from a
> compatible request through durable evidence and single-Run admission?

The Operator manages a separately deployed Profiler operand and reports its
component availability. The Profiler resolves trusted facts, freezes one Run,
creates one owned Job, verifies the worker's durable raw object, finalizes
terminal Kubernetes evidence, independently reloads it, applies the named
single-Run admission policy, and publishes compact execution, evidence, and
admission status. Model-loaded-idle request, evidence, and admission dispatch
are live-verified. The scope does not orchestrate a batch
campaign, derive `b_sat`, publish a `DeviceProfile`, or connect to the
scheduler.

### 5a. Sequential batch sweep with early stopping — approved, not implemented

[plans/05a-sequential-batch-stop.md](plans/05a-sequential-batch-stop.md)

Answers only:

> Walking batch sizes upward, when has energy per item stopped improving enough
> to keep going?

Approved scope, not implemented. It takes the early-stopping rule as implemented
in Keras, PyTorch Lightning, scikit-learn, Hugging Face, and Optuna, adapts its
margin to a relative form, and walks batch sizes until they stop paying off. The
batch domain is discovered rather than declared, and the cost is a fraction of
Plan 5's. It produces a decision with no confidence statement attached, and its
selected batch is the candidate Plan 5's confirmation pass takes as input.

### 5. Batch-saturation campaign — approved, not implemented

[plans/05-batch-saturation-campaign.md](plans/05-batch-saturation-campaign.md)

Answers only:

> Under what predeclared rule does a set of Runs establish batch saturation for
> one execution state?

Approved scope, not implemented. It fixes a domain-wide relative saturation
estimand with simultaneous calibration, rebuilds the point-confidence
construction, and separates candidate discovery from confirmation. Campaign
provenance is pre-registered in a committed record rather than machine-enforced;
that boundary is stated in the plan. It does not authorize a `DeviceProfile`
API, a campaign controller, an intent schema change, or scheduler consumption.

### 6. Execution-support expansion — active

[plans/06-execution-support-expansion.md](plans/06-execution-support-expansion.md)

Answers only:

> Which exact Node, model, runtime/backend, power-mode, and workload-mode rows
> does production support, and which smallest vertical slice raises the next
> row to measurement-ready?

P0 records the L0--L4 vocabulary and exact support matrix. Raspberry Pi 5 CPU
ORT at fixed power mode is the current implementation slice. Runtime-specific
adapters and mutable power-mode ownership remain explicit; catalog presence and
historical standalone smoke are not treated as production execution evidence.

## Component contracts

These documents define reusable internal subsystem boundaries. They support the
plans but do not expand a plan's authorization:

- [MetricObserver](components/metric-observer.md) owns bounded typed power and
  thermal observation, not evidence-admission policy or profile derivation.
- [WorkloadObserver](components/workload-observer.md) defines bounded
  measurement-window Pod-membership evidence, external-environment stability,
  and concurrent CHILL measurement detection.
- [Evidence Store](components/evidence-store.md) owns immutable evidence object
  storage and verification boundaries. Its Profiler-consumption plan remains
  separately review-gated.
- [Measurement Worker](components/measurement-worker.md) fixes the first
  CPU-ORT Job-local execution, synchronization, raw-write, and receipt contract.

## Supporting records

The following documents are background analysis, not active implementation
plans or competing architecture authorities:

- [Step 1: runtime execution fact audit](foundations/runtime-execution/01-fact-audit.md)
- [Step 2: runtime execution commonality](foundations/runtime-execution/02-commonality.md)
- [Step 3: runtime execution provenance](foundations/runtime-execution/03-provenance.md)
- [Step 4: compatibility and execution boundary](foundations/runtime-execution/04-compatibility-execution-boundary.md)
- [Step 5: execution support matrix](foundations/runtime-execution/05-support-matrix.csv)

## Stable ownership rules

- `DeviceClass` describes stable hardware-class facts, not live Node or
  runtime state.
- `ModelSpec` describes logical models, immutable artifacts, and stable
  execution-path requirements, not execution results.
- Kubernetes owns admission, placement, allocation, Job retry, and Pod
  lifecycle.
- Helm owns installation-time identities, permissions, and base configuration;
  controllers are not granted authority to create or bind their own RBAC.
- CHILL owns its desired Run decision, runtime-specific validation, evidence
  admission, and derived scheduler facts.
- The Profiler controller is a time-bounded, reconstructable control plane.
  The one-shot profiling Job is the execution plane that runs inference,
  receives typed metric observations, and persists raw evidence.
- `internal/metricobserver` remains a bounded raw-observation library. It does
  not own profiling policy or `b_sat`.
- SLO classification consumes an admitted profile; it does not decide whether
  Layer-0 measurement evidence is scientifically valid.

## Dependency rule

```text
operator / Profiler controller entrypoints
        -> Kubernetes adapters
        -> application orchestration
        -> plain domain leaf packages

one-shot runtime worker
        +-> runtime-specific execution adapter
        +-> durable evidence sink

bounded MetricObserver helper
        -> internal/metricobserver
        -> edge-metrics adapter
```

Pure packages do not import `api/v1alpha1`, Kubernetes APIs, or
controller-runtime. Kubernetes adapters and execution entrypoints may import
pure packages, never the reverse. Reconciliation derives its next action from
durable objects and does not perform measurement or retain authoritative Run
state in process memory.

## Not authorized by this roadmap

- a shared `ExecutionContract` package
- a generic runtime plugin, registry, planner, or validator framework
- a compatibility controller or materialized compatibility status
- Node runtime/driver state in `DeviceClass`
- profile status or scheduler integration

Those decisions belong to the plan whose concrete input and consumer require
them.
