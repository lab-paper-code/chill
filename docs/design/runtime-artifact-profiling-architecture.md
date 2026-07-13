# Runtime, Artifact, and Profiling Roadmap

## Status

Roadmap with Plan 1 implemented. This document itself is not an implementation
plan and does not authorize later API or internal changes.

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
runtime evidence + power evidence
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

[runtime-artifact-01-catalog-compatibility.md](runtime-artifact-01-catalog-compatibility.md)

Answers only:

> Is this immutable model artifact and runtime path statically compatible
> with this stable DeviceClass?

The first read-only CPU ONNX Runtime path is implemented. Its output is a
static candidate report, not permission to execute profiling.

### 2. Profiling Run and Kubernetes execution — implemented first path

[runtime-artifact-02-profiling-run-execution.md](runtime-artifact-02-profiling-run-execution.md)

Answers only:

> Did Kubernetes and the selected runtime realize one immutable profiling
> decision, and what execution evidence was observed?

Its first CPU ONNX Runtime plan is approved and is implemented as isolated
plain-domain and one-shot Kubernetes boundaries before any public Run API.

### 3. Profile admission and derivation — implemented first read-only path

[runtime-artifact-03-profile-admission-derivation.md](runtime-artifact-03-profile-admission-derivation.md)

Answers only:

> Are the execution and power observations sufficient to admit a profile, and
> what energy curve and `b_sat` follow from that evidence?

Its first pure read-only plan is approved against the retained real Run
evidence. Persistence remains deferred until the output contract survives
adversarial and live-evidence review.

## Supporting records

The following documents are background analysis, not active implementation
plans or competing architecture authorities:

- `runtime-artifact-execution-fact-audit.md`
- `runtime-artifact-execution-commonality.md`
- `runtime-artifact-execution-provenance.md`
- `runtime-artifact-compatibility-execution-boundary.md`

## Stable ownership rules

- `DeviceClass` describes stable hardware-class facts, not live Node or
  runtime state.
- `ModelSpec` describes logical models, immutable artifacts, and stable
  execution-path requirements, not execution results.
- Kubernetes owns admission, placement, allocation, Job retry, and Pod
  lifecycle.
- CHILL owns its desired Run decision, runtime-specific validation, evidence
  admission, and derived scheduler facts.
- `internal/powerobserver` remains a bounded raw-observation library. It does
  not own profiling policy or `b_sat`.
- SLO classification consumes an admitted profile; it does not decide whether
  Layer-0 measurement evidence is scientifically valid.

## Dependency rule

```text
cmd / internal/operator
        |
        v
Kubernetes adapters and composition
        |
        v
plain domain or runtime-specific leaf packages
```

Pure packages do not import `api/v1alpha1`, Kubernetes APIs, or
controller-runtime. Kubernetes adapters may import pure packages, never the
reverse.

## Not authorized by this roadmap

- a shared `ExecutionContract` package
- a generic runtime plugin, registry, planner, or validator framework
- a public profiling Run API
- a compatibility controller or materialized compatibility status
- Node runtime/driver state in `DeviceClass`
- profile status or scheduler integration

Those decisions belong to the plan whose concrete input and consumer require
them.
