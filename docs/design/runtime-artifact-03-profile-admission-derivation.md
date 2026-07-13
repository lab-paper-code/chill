# Plan 3: Profile Evidence Admission and Layer-0 Derivation

## Status

Implemented as a pure, read-only first path. No `DeviceProfile` API or
status write is authorized by this plan.

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

`P_idle(state)` must come from separately admitted idle evidence with the same
material execution-state and environment identity. Missing or unstable idle
baseline makes incremental energy and `b_sat` unavailable. Mean power times
mean call latency remains a diagnostic cross-check, never the accepted
estimator.

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
- power source identity, boundary coverage, gaps, failures, and timestamp
  semantics;
- throttling and declared contamination evidence; and
- absence of warm-up, load, or engine preparation inside the steady window.

The result is `Accepted`, `Rejected`, or `Insufficient` with typed local reasons.
It does not repair evidence, alter static compatibility, or classify SLO.

## Checkpoint 3: repeated point aggregation

Runs aggregate only when all material state identities and estimator policy
versions match. Independent trial values remain visible. Minimum trial count is
an explicit policy input; request-level latency samples are not pooled to erase
between-trial variance. A batch point retains p99 latency, mean incremental
joules per item, and a trial-level confidence interval.

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
spikes/profilederivation/cmd/     read-only Plan 2 bundle adapter
```

The pure package imports no Kubernetes, CHILL API, controller-runtime,
PowerObserver transport, or scheduler package.

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
idle and repeated-trial evidence.

## Implementation result

The pure implementation now distinguishes execution-state identity from unique
trial identity, rejects duplicate trials, binds every result to admission,
point, confidence, and `b_sat` policy versions, and uses a 95% Student-t interval
with at least three independent trials. It validates the complete measured batch
domain from batch one before searching for the first adjacent plateau. Missing
lower or intermediate batches cannot yield an accepted result.

The adapter strict-decodes and revalidates the Plan 2 bundle, including intent,
Job/Pod/Node ownership, runtime output, warm-up ordering, exact inference-to-
latency count, and immutable content filename. Thermal state is not silently
assumed healthy when no producer exists.

For retained evidence
`sha256:165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42`,
the timestamp-aware diagnostic total is 543.686 J over 30.010 seconds and 5,541
items. Admission is rejected because thermal state was not captured,
unexpected co-resident workloads were present, and a post-run Pod snapshot
cannot prove cleanliness across the earlier measurement window. Incremental
energy is
`Unavailable: MissingIdleBaseline`; `b_sat` is
`Unavailable: MissingIdleBaselineAndRepeatedCurve`. No `DeviceProfile` is
created or updated.
