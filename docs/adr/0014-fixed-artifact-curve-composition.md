# 0014 Fixed-Artifact Curve Composition Boundary

## Status

Accepted

## Context

CHILL derives incremental energy only after an active trial is paired with a
model-loaded-idle baseline from the same exact execution state. The execution
state deliberately binds the artifact name, path, and immutable artifact-image
reference. That binding is material: two compiled engines can reserve different
resources and draw different idle power even when they came from the same
logical model.

A runtime may nevertheless require one immutable artifact per supported batch.
The live RKNNLite batch-one and batch-two rows demonstrate this case. They are
independently valid L3 rows, but their execution-state identities and
artifact-supply images differ. Plans 5 and 5a originally required every batch
point to share one execution state, one measurement-protocol identity, and one
idle baseline. Excluding artifact facts from those identities would make the
points composable by erasing the exact state that admission must protect.
Reusing one engine's idle baseline for another engine would make the energy
subtraction unsupported.

This decision crosses the model catalog, single-Run admission and derivation,
and batch-campaign plans. It therefore belongs in an ADR rather than one leaf
plan.

## Decision

Keep `StateIdentity`, `EnvironmentIdentity`, and
`MeasurementProtocolIdentity` as exact single-Run and single-point identities.
Do not remove artifact or artifact-supply facts from them, and do not change
their semantic projections for curve composition.

Add one named, planned contract and a Loader-verified family at the
derivation/campaign boundary:

- `CurveFamilyContract` is the batch-ordered eligible member map resolved from
  the catalog and execution configuration before any campaign Run starts. Its
  configured `name` is the stable policy-facing reference.
- `CurveFamily` is constructed only after retained evidence crosses the Loader
  boundary and every frozen Run is checked semantically against that contract.
  It carries the exact realized member identities and the one observed
  environment identity.

The latter is a verified composition boundary, not a fourth Run identity and
not a claim that its member execution states are physically identical. CHILL
does not derive another digest or validate serialized bytes at this boundary.

A family contract fails closed unless all planned members share:

- the logical model and the configured family name;
- DeviceClass, actual Node UID, runtime family and backend, runtime image,
  power mode, CPU contract, and wall-meter source;
- within each workload mode, the active or idle protocol fields other than
  batch and the member's exact artifact-supply image; and
- for derived fixed-batch artifacts, target platform, converter tool, converter
  version, and the non-batch tensor contract.

Each member explicitly binds its batch, execution path, artifact name, path,
immutable artifact-image reference, fixed-batch capability, and full input
shape. The fixed-batch capability and leading input dimension must equal the
member batch. Artifact names, runtime-family labels, or execution-path naming
alone never establish membership.

The realized projection additionally requires every verified member Run to
match its planned contract entry and all member evidence to carry one common
`EnvironmentIdentity`. Environment is observed evidence, not a catalog fact.

The offline campaign record freezes the configured family name and eligible
member map before measurement. The observed environment cannot be truthfully
frozen before the WorkloadObserver records it, so a verified `CurveFamily` is
created only after Loader verification. Plan 5 freezes the complete `1..Kmax`
member map. Plan 5a may stop before exhausting that map, but it reports the
family name, observed environment, and measured contiguous prefix. This adds no
campaign CRD, controller, or Run intent field.

An opaque curve owns a set of admitted idle baselines keyed by exact
`StateIdentity`:

```text
DeriveCurve(passes, family, baselinesByState, pointPolicy) -> Curve
```

Every active trial uses the baseline whose state and environment identities
match it exactly. The same baseline object is reused across discovery and
confirmation for every occurrence of that state. A dynamic-batch artifact has
one state key and therefore retains the original one-baseline behavior. A
fixed-artifact family normally has one state and one independently admitted
baseline per batch. Derivation rejects a missing member baseline, a baseline
for the wrong state or environment, duplicate active evidence across passes,
and reused idle raw evidence across distinct baseline keys.

The joint estimator retains the baseline key for every point. If

```text
epsilon_b = A_b - a_b * P_q(b)
```

then a contrast uses the coefficient of every baseline estimate it touches.
For independently admitted baseline sets, its baseline covariance is the sum
over baseline keys rather than an assumed single shared term. When two batches
share a state, their coefficients combine and the existing shared-baseline
partial cancellation is recovered exactly. Plan 5 must verify the resulting
joint coverage, including covariance from any active point reused by several
contrasts, before accepting a confidence claim. Plan 5a uses only the derived
means but still records the family name, observed environment, and every
baseline used.

## Consequences

Single-Run admission, `IncrementalTrial`, and exact per-batch aggregation remain
unchanged. Existing L3 evidence and identity strings remain valid.

The current retained RKNNLite batch-one and batch-two rows are not yet one
campaign family: they were frozen from different catalog snapshots and use
different generations of helper/runtime images. Both rows must be remeasured
under one frozen family contract before Plan 5a can consume them.

Fixed-artifact campaigns cost an admitted model-loaded-idle baseline set per
member state. CHILL does not infer per-engine idle equivalence to reduce that
cost. A future equivalence method may supersede this consequence only with its
own estimand and evidence contract.

The pure derivation package owns the opaque family and curve constructors.
Catalog and verified-evidence adapters may project plain inputs into that
boundary, but Kubernetes and API packages do not enter the mathematical core.
