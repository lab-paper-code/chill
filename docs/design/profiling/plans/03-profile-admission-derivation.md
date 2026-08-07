# Plan 3: Profile Evidence Admission and Layer-0 Derivation

> Navigation: [Profiling design index](../README.md)

## Status

Implemented as a pure, read-only path for active and model-loaded-idle
admission and derivation. The Job, worker, finalizer, verified loader, and
single-Run controller admission branch now produce and consume idle raw
evidence. Three sequential retained live idle Runs have passed
`idle-wall-power-v1`, projected to identity-compatible `IdleTrial` values, and
produced the first live `DeriveIdleBaseline` result. Three subsequent active
Runs have passed `wall-power-v2` and produced the first live batch-one
`IncrementalTrial` and `Aggregate` result. Production campaign orchestration,
persistence, and multi-batch curve derivation remain unwired. No `DeviceProfile`
API or status write is authorized by this plan.

## Question

> Is exact Run evidence scientifically usable, and what fixed-batch energy
> curve and `b_sat` conclusion follow without overstating the evidence?

## Checkpoint 1: quantity and estimator contract

Shelly reports total wall power, including idle consumption. To remain
consistent with the specification's later

```text
P_node(lambda) = P_idle + lambda * epsilon
```

this plan defines `epsilon` as incremental active energy per completed item:

```text
E_total(window)       = timestamp-aware integral of wall power
E_incremental(window) = E_total(window) - P_idle(state) * window duration
epsilon(batch)        = E_incremental(window) / completed items in that window
```

The research note's total-wall form `epsilon = P_act * T / b` is superseded
here. `P_node(lambda) = P_idle + lambda * epsilon` composes only with this
incremental definition; the total-wall form would count idle twice.

`P_idle(state)` must come from separately admitted idle evidence with the same
material execution-state and environment identity. Missing or unstable idle
baseline makes incremental energy and `b_sat` unavailable. Mean power times
mean call latency remains a diagnostic cross-check, never the accepted
estimator.

The specification defines this idle state as model-loaded idle: the exact model
is resident in the declared runtime and power mode, but no inference call is
issued during the retained measurement window. It is not an unloaded Node or
the separate `inactive`/standby state.

The idle window is runtime-declared and observer-independent. The runtime loads
the exact artifact, retains the session, waits the positive settling duration
frozen in the current intent, and then opens a fixed-duration window using its
monotonic clock. It issues no inference during settling or measurement and
records that the model remained resident through the end. Metric and workload
observers only establish coverage and contamination facts for those immutable
boundaries; power values must not be used to choose a quieter sub-window.

For idle, `SteadyWindow` means protocol-conditioned model-loaded idle: model
load and other preparation precede the declared settling interval, the full
settling interval completes before the window, no inference or preparation
occurs during settling or measurement, and the runtime remains resident. This
does not by itself claim thermodynamic equilibrium. Known non-throttled thermal
evidence remains a separate single-trial gate, while residual between-trial
variation remains visible in the idle-baseline confidence interval.

The active and idle admission paths share only gates whose evidence has the
same meaning:

| Gate family | Shared | Active only | Idle only |
|---|---|---|---|
| provenance and identity | immutable raw-object identity; Run, Pod, Node, state, environment, protocol, runtime image, CPU, and meter binding | completed-item denominator and latency-to-call binding | raw-object identity retained in `IdleTrial`; canonical idle protocol |
| declared window | valid runtime-declared interval; metric/workload coverage; no observer-derived boundary | warm-up and preparation precede the window | model load precedes settling; full settling interval completes; model remains resident |
| execution result | successful exact runtime/provider/artifact realization | positive inference count, complete latency distribution, and output validation | zero inference during settling and measurement; no input or output validation |
| quality and contamination | sample count, gaps, boundaries, failures, timestamp semantics, known non-throttled thermal state, workload continuity, and concurrent/external workload rejection | total wall joules plus completed items | total wall joules divided by declared duration gives mean idle wall watts |

The derivation boundary uses three independently versioned identities:

| Identity | Bound fields | Deliberately excluded |
|---|---|---|
| `StateIdentity` | execution state including model/artifact carrier reference and path, runtime image, optional normalized host contract, actual Node UID, CPU contract, and exact wall-meter source | workload mode, warm-up, duration, sampling schedule, helper images, batch |
| `EnvironmentIdentity` | stable external workload authorities, active phase, images, selected labels, and replica multiplicity | profiling Run Pod; owner-controlled Pod name and Pod UID |
| `MeasurementProtocolIdentity` | workload mode, input/output protocol, warm-up and duration, sampling bounds, artifact-supply and observer images, workload-observation contract | batch, because batch remains the explicit curve axis |

These remain exact single-Run and single-point identities. A batch curve that
uses different fixed-batch artifacts does not weaken or normalize them. It uses
the separate verified composition boundary and per-state baseline ownership fixed by
[ADR 0014](../../../adr/0014-fixed-artifact-curve-composition.md). That
ADR separates the pre-measurement `CurveFamilyName` from the verified
`CurveFamily`, which is constructed only after evidence crosses the Loader
boundary. Neither is stored in or recomputed from one Run.

Binding the actual Node UID intentionally makes this first derivation
node-instance-scoped; it does not assert within-type homogeneity or authorize
cross-node pooling.

For an owner-controlled external Pod, environment identity binds the observed
controller owner `kind`, `name`, and UID. Recreating a Pod under the same owner
between trials therefore does not manufacture environment drift. A changed
owner UID, image, phase, selected label, or replica count still changes the
identity. A Pod with no observed controller owner remains bound to its Pod name
and UID and therefore fails safe across replacement. Any membership change
during one measurement window remains a typed admission failure even when the
departing and replacement Pods share an owner.

Active evidence and its idle baseline must match `StateIdentity` and
`EnvironmentIdentity`. Their `MeasurementProtocolIdentity` values are expected
to differ because active and model-loaded idle use different workload modes;
both identities and both admission policies are retained in the estimator
identity. Idle trials may aggregate only when their three identities match.
Active trials and batch points likewise require matching identities, with batch
validated separately. Identity strings are opaque projections of semantic
fields. A host contract binds host library, driver, device visibility, and
allocation, so physical-state drift cannot pool. Actuated state additionally binds
the typed target and actuation-policy name; those facts do not enter the
measurement-protocol identity.

Consequently, a model-loaded-idle baseline is reusable only by active trials
with its exact state identity. Curves over one dynamic-batch artifact may have
one such baseline. Curves over several fixed-batch artifacts retain one
baseline per member state; curve-level covariance is owned by Plans 5 and 5a,
not by single-Run admission.

Observer timestamps are receipt timestamps. The first implementation uses a
declared nearest-boundary, piecewise-linear integration rule and rejects windows
whose boundary distance, maximum gap, failures, or coverage exceed explicit
policy. Raw samples are never silently dropped.

## Checkpoint 2: single-Run admission

Admission consumes an already valid Plan 2 bundle and checks only measurement
quality:

- exact state and trial identity;
- steady runtime window and completed-item denominator;
- raw latency distribution and output validation;
- metric source identity, power-sample boundary coverage, gaps, failures, and timestamp
  semantics;
- throttling and declared contamination evidence; and
- absence of warm-up, load, or engine preparation inside the steady window.

The result is `Accepted`, `Rejected`, or `Insufficient` with typed local reasons.
It does not repair evidence, alter static compatibility, or classify SLO.

## Checkpoint 3: repeated point aggregation

Runs aggregate only when state, owner-bound environment, measurement-protocol,
estimator, and admission-policy identities match. Independent trial values
remain visible. Minimum trial count is an explicit policy input; request-level
latency samples are not pooled to erase between-trial variance. A batch point
retains p99 latency, mean incremental joules per item, and a trial-level
confidence interval.

## Checkpoint 4: curve and `b_sat`

The measured batch domain remains explicit. Candidate search and scientific
acceptance are separate:

- a missing adjacent `b+1` cannot accept `b_sat`;
- a failed or unsupported next batch is a capability boundary, not saturation;
- no plateau in the measured range is `Censored`, not `b_sat=max`;
- a mean-only plateau with overlapping uncertainty is `CandidateOnly`;
- accepted saturation requires the declared equivalence tolerance to hold at
  trial-level confidence and no later measured contradiction; and
- a non-unimodal contradiction is `Ambiguous`.

The output status is `Accepted`, `CandidateOnly`, `Censored`, `Ambiguous`, or
`Unavailable` and remains bound to exact input and rule identities.

## Code boundary

```text
internal/profilederivation/       pure admission, integration, aggregation, b_sat
internal/profiler/admission/      verified evidence-to-trial projection
```

The pure package imports no Kubernetes, CHILL API, controller-runtime,
MetricObserver transport, or scheduler package.

## Explicitly out of scope

- Job creation or runtime validation
- source resolution or polling
- adaptive campaign orchestration
- SLO feasibility or open-loop serving
- transition, thermal-capacity, or observation-staleness experiments
- `DeviceProfile` persistence, controller, or status
- scheduler/enumerator consumption

## Acceptance boundary

Plan 3 is complete when synthetic golden curves close accepted, candidate,
censored, and ambiguous cases; malformed or insufficient real evidence fails
closed with precise reasons; estimator units and identity binding are tested;
and the retained Plan 2 Run produces no accepted energy curve or `b_sat` without
idle and repeated-trial evidence. An `IdleTrial` must carry the verified object
identity of the raw evidence that produced its mean wall power.

## Implementation result

The pure implementation now distinguishes execution-state, owner-bound
environment, measurement-protocol, and unique trial identities. It rejects
duplicate trials, binds every result to admission, estimator, point, confidence,
and `b_sat` policy versions, and uses a 95% Student-t interval with at least
three independent trials. It validates the complete measured batch domain from
batch one before searching for the first adjacent plateau. Missing lower or
intermediate batches cannot yield an accepted result.

Active and idle admission now share one power-window quality core for sample
count, failures, receipt-timestamp policy, boundaries, gaps, coverage, thermal
state, and workload contamination. Idle admission separately requires the
canonical idle protocol, exact model load before the full settling
interval, zero inference after model load, retained end residency, and a
runtime-declared window. Only an Accepted result projects to `IdleTrial`.
Each projected trial retains the verified raw object's logical store, opaque
key, and byte size; baseline aggregation rejects missing or reused raw object
references.

Power-window thresholds are installation-time configured policy data rather
than Go constants or caller-supplied Run fields. A Run selects a policy name;
the control path freezes that policy's semantic fields and ConfigMap source
identity before execution. Admission verifies that the selected policy workload
mode matches the frozen intent and uses the source object identity as its
version. This preserves replay and aggregation identity while allowing an
operator to add a meter-specific policy without a CRD or binary release.

The producer uses the public `ProfilingRun` mode discriminator and a separate
idle runtime/raw schema. Common worker orchestration and immutable evidence
finalization are shared with active execution; only runtime completeness, raw
projection, verified loading, and final admission dispatch are mode-specific.
A controller never infers idle from metric values.

The adapter strict-decodes and validates the Plan 2 bundle, including intent,
Job/Pod/Node ownership, runtime output, warm-up ordering, exact inference-to-
latency count, and immutable content filename. Thermal state is not silently
assumed healthy when no producer exists.

### Live model-loaded-idle baseline result

On 2026-08-03, three same-condition Runs were executed sequentially on the
live Lattepanda Node:

| Run | Node resourceVersion | admitted mean wall power |
|---|---:|---:|
| `idle-baseline-live-ad7b84f-a` | 275297891 | 4.929898 W |
| `idle-baseline-live-ad7b84f-b` | 275297891 | 4.893351 W |
| `idle-baseline-live-ad7b84f-c` | 275301868 | 4.897335 W |

All three reached `ExecutionComplete=True`, `EvidenceVerified=True`, and
`EvidenceAdmitted=True` with `idle-wall-power-v1`. Re-reading each retained raw
object through `RetainedLoader` and `EvaluateIdle` produced distinct Pod trial
IDs and distinct verified raw-object identities. The projections nevertheless
agreed exactly on:

- `StateIdentity`: `chill.dacs.io/execution-state-identity.v1alpha1@sha256:fdf063288ab6695ce2f2e2e93a9d43b60bfdc13ba37785a22cf4715c17b399e8`;
- `EnvironmentIdentity`: `chill.dacs.io/external-environment-identity.v1alpha1@sha256:4e7f69c7ab9450af5bc06f11e5e76c930ca3431dc8f0e5f9dd6097af38ba2d58`; and
- idle `MeasurementProtocolIdentity`: `chill.dacs.io/measurement-protocol-identity.v1alpha1@sha256:c7a36290499e36e445d709e61509c3d5112c07bfd61149a6407998facefe0d7d`.

The three semantic intent projections differed only in the third Run's
`targetNode.resourceVersion`, confirming on the live path that this causal
execution snapshot can change without changing any of the three aggregation
identities.

Calling `DeriveIdleBaseline` with those three independently admitted trials and
policy `idle-baseline-v1` produced 4.906861 W with a 95% Student-t confidence
half-width of 0.049809 W. The read-only diagnostic reconstructed each retained
Job and store binding, validated the loaded records semantically, and routed only the S3
transport through a local port-forward. It did not add a production caller or
persist a baseline. Production campaign ownership therefore remains open even
though the pure derivation and identity-stability assumptions now have live
evidence.

### Earlier idle-trial selection audit

The earlier `idle-model-loaded-live-ad7b84f-v2` Run was not rejected by the
identity scheme. Re-reading its retained records through the same loader and idle
projector produced 4.862052 W, and its state, owner-bound environment, and idle
protocol identities all matched the later three-trial baseline exactly. It was
absent only because that read-only diagnostic named the later A/B/C Runs as its
inputs; no production campaign selector or automatic exclusion was involved.

The earlier value also lies inside the later baseline's 95% interval of
4.857052--4.956671 W. Combining all four identity-compatible trials produces
4.895659 W with a 95% Student-t half-width of 0.044147 W. This audit therefore
does not establish time-separated environment drift. It does establish that a
future campaign driver must predeclare an evidence-inclusion or temporal-block
rule instead of allowing an ad-hoc Run-name list to define the baseline.

### First live incremental batch point

Using the already fixed three-trial idle baseline above, three active Runs with
the exact `active-regression-live-ad7b84f` spec were then executed sequentially:

| Run | total wall energy | completed items | incremental energy/item | trial p99 |
|---|---:|---:|---:|---:|
| `active-epsilon-live-69a83ad-a` | 519.935 J | 5,417 | 0.068805 J | 8.843 ms |
| `active-epsilon-live-69a83ad-b` | 492.160 J | 5,255 | 0.065637 J | 8.945 ms |
| `active-epsilon-live-69a83ad-c` | 485.927 J | 5,178 | 0.065410 J | 9.295 ms |

All three passed `wall-power-v2`, carried the same material intent, and
matched the idle baseline's state and environment identities. Their active
measurement-protocol identity also matched one another and intentionally
differed from the idle protocol identity. Re-loading the immutable evidence and
calling the existing projector, `Admit`, `IncrementalTrial`, and `Aggregate`
paths produced the first live batch-one estimate:

```text
epsilon(batch=1) = 0.066617 J/item
95% CI           = 0.066617 +/- 0.004723 J/item
interval         = [0.061894, 0.071341] J/item
point p99        = 9.295 ms
```

The confidence half-width combines active between-trial uncertainty with the
fixed idle-baseline uncertainty. The three incremental trial values have sample
standard deviation 0.001898 J/item and coefficient of variation 2.85%. With
only three trials this is an empirical campaign-sizing input, not yet a stable
variance model. As with the idle diagnostic, no production caller or derived
object was added or persisted.

### Earlier active-only evidence result

For the earlier retained evidence object, the timestamp-aware diagnostic total
is 543.686 J over 30.010 seconds and 5,541
items. Admission is insufficient because thermal state was not captured and a
post-run Pod snapshot cannot prove continuous workload membership across the
earlier measurement window. Incremental
energy is
`Unavailable: MissingIdleBaseline`; `b_sat` is
`Unavailable: MissingIdleBaselineAndRepeatedCurve`. No `DeviceProfile` is
created or updated.
