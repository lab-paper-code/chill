# Plan 3: Profile Evidence Admission and Layer-0 Derivation

> Navigation: [Profiling design index](../README.md)

## Status

Implemented as a pure, read-only path for active and model-loaded-idle
admission and derivation. The verified-evidence idle projector exists, but no
Job currently produces idle raw evidence. No `DeviceProfile` API or status
write is authorized by this plan.

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
frozen in the `v1alpha6` intent, and then opens a fixed-duration window using its
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
| `StateIdentity` | execution state including model/artifact digest and path, runtime image, actual Node UID, CPU contract, and exact wall-meter source | workload mode, warm-up, duration, sampling schedule, helper images, batch |
| `EnvironmentIdentity` | stable external workload authorities, active phase, images, selected labels, and replica multiplicity | profiling Run Pod; owner-controlled Pod name and Pod UID |
| `MeasurementProtocolIdentity` | workload mode, input/output protocol, warm-up and duration, sampling bounds, artifact-supply and observer images, workload-observation contract | batch, because batch remains the explicit curve axis |

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
validated separately. Identity strings include their projection schema version
and digest. This changes no retained intent bytes, intent schema, or intent
digest.

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
spikes/profilederivation/cmd/     read-only Plan 2 bundle adapter
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
`v1alpha6` canonical idle protocol, exact model load before the full settling
interval, zero inference after model load, retained end residency, and a
runtime-declared window. Only an Accepted result projects to `IdleTrial`.
Each projected trial retains the verified raw object's logical store, opaque
key, SHA-256 digest, and byte size; baseline aggregation rejects missing or
reused raw identities.

The adapter strict-decodes and revalidates the Plan 2 bundle, including intent,
Job/Pod/Node ownership, runtime output, warm-up ordering, exact inference-to-
latency count, and immutable content filename. Thermal state is not silently
assumed healthy when no producer exists.

For retained evidence
`sha256:165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42`,
the timestamp-aware diagnostic total is 543.686 J over 30.010 seconds and 5,541
items. Admission is insufficient because thermal state was not captured and a
post-run Pod snapshot cannot prove continuous workload membership across the
earlier measurement window. Incremental
energy is
`Unavailable: MissingIdleBaseline`; `b_sat` is
`Unavailable: MissingIdleBaselineAndRepeatedCurve`. No `DeviceProfile` is
created or updated.
