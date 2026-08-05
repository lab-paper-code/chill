# Plan 5: Batch-Saturation Campaign

> Navigation: [Profiling design index](../README.md)

## Status

Approved implementation plan. The reusable frozen-family, verified-family, and
opaque exact-state curve boundaries are implemented under Plan 5a, and its live
sequential result supplies one discovery candidate. They do not implement Plan
5's joint estimator or statistical claim. The derivation path now retains each
active trial's wall-energy-per-item and `T_i / N_i`, plus each exact-state idle
baseline's mean variance and degrees of freedom. The complete two-pass
calibration, rebuilt estimator, confirmatory sizing, policy decoding, and
confirmatory result remain unimplemented. This plan fixes the quantity that
decides `b_sat` — the largest remaining energy improvement any larger measured
batch still offers — gives curve derivation ownership of the exact idle
baseline set, rebuilds the point-confidence construction, and separates
candidate discovery from confirmation. It does not authorize a `DeviceProfile`
API, a campaign CRD or controller, an intent schema change, cross-model pooling,
or scheduler consumption.

[ADR 0014](../../../adr/0014-fixed-artifact-curve-composition.md) amends the
original one-state assumption for runtimes with one immutable artifact per
fixed batch. Exact execution-state and protocol identities remain unchanged.
An opaque curve instead owns one pre-frozen family contract, one
Loader-verified realized family identity, and an admitted idle baseline for
every distinct member state. A dynamic-batch artifact remains the one-state,
one-baseline special case.

Plan 3 requires a campaign driver to predeclare an evidence-inclusion rule. Plan
4 leaves batch selection and repeated trials to a later campaign planner. This
plan closes both by pre-registration and a published Run list. The
[Verification boundary](#verification-boundary) states exactly what that
guarantees.

## Question

> Under what predeclared rule does a set of Runs establish batch saturation for
> one frozen curve family?

## What this plan does

Running inference in larger batches usually costs less energy per item, up to a
point. Past that point, larger batches stop helping while still costing latency
and memory. `b_sat` is the batch size at that point. This plan defines how a
campaign of profiling Runs decides where it is.

The whole procedure runs in two passes over the same frozen curve family.

**Before anything runs**, two files are written and fixed. One is the derivation
policy: how far up the batch domain we will measure, how much remaining
improvement we are willing to walk away from, and how many trials each pass gets.
The other is the campaign record: which Runs belong to this campaign and how the
second pass is generated from the first. Every choice in them therefore predates
the evidence it will judge.

**Pass one, discovery.** Measure every batch from 1 up to the declared maximum,
a few trials each. This is a cheap, low-precision sweep. Its job is to find the
approximate shape of the curve and propose a candidate batch — the smallest one
where the remaining improvement looks small enough. Nothing is claimed from this
pass. It also tells us how noisy the measurement is, which determines how many
trials the second pass needs.

**Pass two, confirmation.** Measure the candidate batch and everything above it
again, with fresh Runs and more trials. Then ask one question with a proper
confidence bound: *starting from the candidate, does any larger batch we
measured still offer more than the declared improvement?* If the bound says no,
the candidate is accepted.

The two passes are separated because choosing what to test and then testing it
with the same numbers makes the confidence statement wrong. Discovery chooses;
confirmation tests, on evidence discovery never saw.

## Checkpoint 1: the quantity, and when it is accepted

### The batch domain

The campaign measures batches `1` through `Kmax`, with `Kmax >= 2`. `Kmax` is
fixed before execution, and it is part of what gets claimed: the accepted
statement is that saturation occurs somewhere within `1..Kmax`. Nothing is
claimed about batch sizes above `Kmax`.

Candidate batches are restricted to `1..Kmax-1`. `Kmax` is the required final
witness batch, not a candidate. Allowing `c = Kmax` would make the set of larger
batches empty and would accept the upper endpoint without making a comparison.
If no candidate below `Kmax` satisfies the discovery rule, the typed outcome is
`Censored: NoSaturationObserved`.

Choosing `Kmax` is a real trade-off, described under
[typed outcomes](#typed-outcomes) below.

### The quantity

For a candidate batch `c` in `1..Kmax-1`, look at every larger batch in the
domain and ask how much better the best of them is, as a fraction:

```text
R_c = max over b in {c+1, ..., Kmax} of  (epsilon_c - epsilon_b) / epsilon_c
```

`epsilon_b` is the incremental energy per completed item at batch `b`, as Plan 3
defines it. `R_c` is therefore *the largest remaining improvement available above
`c`, expressed as a fraction of what `c` already costs*.

A worked example. Suppose a campaign with `Kmax = 10` measures:

| batch | 1 | 2 | 3 | 4 | 5 | ... | 10 |
|---|---:|---:|---:|---:|---:|---:|---:|
| `epsilon` (J/item) | 0.0666 | 0.0410 | 0.0344 | 0.0331 | 0.0327 | | 0.0325 |

At `c = 3` the best larger batch is 10, which costs `0.0325`. The remaining
improvement is `(0.0344 - 0.0325) / 0.0344 = 5.5%`. At `c = 4` the best larger
batch is again 10, and the remaining improvement is
`(0.0331 - 0.0325) / 0.0331 = 1.8%`.

With a declared tolerance of 5%, batch 3 leaves too much on the table and batch
4 does not. Batch 4 is the candidate.

### Why the quantity is a fraction

CHILL's output is a catalog of profiles, one per DeviceClass and model. A
tolerance expressed in joules per item would have to be chosen separately for
every entry, because a small model and a large one operate at completely
different energy scales. A tolerance expressed as a fraction means one number in
one policy file covers every state.

The fraction is a statement about curve shape only. Whether 5% of the energy is
worth the latency and memory that larger batches cost depends on the arrival
rate and the SLO, and belongs to a different layer:

| Layer | Owns |
|---|---|
| Plan 5 | a state-independent rule for when the energy curve has flattened |
| L1 / L4 | whether the remaining improvement is worth its latency and memory cost at a given arrival rate and SLO |

### The acceptance rule

Acceptance needs two things to be true together:

```text
UCB(R_c) <= tau            the remaining improvement is at most the tolerance
LCB(epsilon_c) > 0         the reference batch's own energy is positive
```

`UCB` is an upper confidence bound and `LCB` a lower one. Both must come from
**one construction that covers them together**. Two separately computed 95%
statements do not hold together at 95%: the chance that at least one of them
fails is roughly the sum of their individual failure chances.

The second condition exists because `R_c` divides by `epsilon_c`. If the
plausible range for `epsilon_c` reaches zero, the fraction has no upper bound and
no tolerance can be met.

The rule bounds improvement in one direction only. It asks whether a larger
batch can still do meaningfully better, and places no lower bound on the
difference. A larger batch that turns out *worse* is acceptable evidence that
nothing more is to be gained — provided no other measured batch in the domain
contradicts that, which the `max` in `R_c` already accounts for.

### How the bound is calibrated

At a fixed candidate `c`, the rule makes one statement about each larger batch —
`Kmax - c` of them — plus the one statement that `epsilon_c` is positive. All of
them have to hold at once, so the confidence has to be spread across all of them.

Plan 5 spreads it the simplest way. With `n` statements and a 5% total error
allowance, each individual bound is computed at `1 - 0.05/n` instead of
`1 - 0.05`. This is the Bonferroni correction. It needs no model of how the
statements are related to each other. The
[fixed sizing method](#fixed-confirmatory-sizing-method) evaluates its precision
against the same allocation. Methods that model the relationships may reach the
same guarantee with fewer trials; adopting one is a new estimator version rather
than a configured option.

That correction covers the confirmatory decision at a candidate that is already
fixed. It does not cover the fact that discovery picked the candidate, nor that
discovery and confirmation share one idle-power estimate. Plan 5 therefore also
requires showing — by derivation or by simulation — that the complete two-pass
procedure still holds its stated confidence. If it does not, the plan is
incomplete; a sizing calculation does not override this.

The fixed construction does not estimate a ratio interval directly. For
each larger batch `b`, define the linear contrast

```text
D_c,b = epsilon_c - epsilon_b
R_c,b = D_c,b / epsilon_c
```

and obtain a simultaneous upper bound `U_D(c,b)` for every contrast plus one
lower bound `L_c` for `epsilon_c`. If `L_c <= 0`, the result is unavailable. If
`U_D(c,b) <= 0`, then `R_c,b <= 0` and its conservative upper bound is zero.
Otherwise its upper bound is `U_D(c,b) / L_c`. The maximum of those bounds is
`UCB(R_c)`. This transformation is valid because the positivity statement and
all contrast statements belong to the same Bonferroni construction; it neither
divides by a point estimate nor silently applies a first-order ratio
approximation.

### What an accepted result claims

An accepted result says: *at batch `c`, no larger batch within `1..Kmax` improves
energy per item by more than `tau`, at the declared confidence.*

It does not say that `c` is the smallest batch for which that is true. The
candidate came from discovery's point estimates, which carry no confidence
statement. Proving minimality would mean separately showing that `c-1` fails the
rule — a second test with its own error allowance, which this plan does not
include.

The result keeps those two facts in separate fields, so the distinction survives
if the value is later promoted into a published profile:

```text
DiscoverySelectedBatch        = 4       the candidate discovery proposed
DomainNoImprovementConfirmed  = true    the acceptance rule held at that batch
MinimalityConfidenceConfirmed = false   no confidence statement about batch 3
```

### Typed outcomes

| Situation | Outcome |
|---|---|
| the acceptance rule holds at `c` | `Accepted: DomainNoImprovementConfirmed` |
| the point estimate plateaus but the bound is too wide | `CandidateOnly` |
| some batch produced no admissible evidence, or the runtime could not run it | `Censored: CapabilityBoundary` |
| no candidate in `1..Kmax-1` satisfies the rule | `Censored: NoSaturationObserved` |
| `LCB(epsilon_c) <= 0` | `Unavailable: NonPositiveReferenceLowerBound` |
| the required trial count exceeds the declared cap | `Unavailable: ConfirmatoryBudgetInsufficient` |

`Censored` means the campaign ran correctly but cannot produce an answer, and the
reason is itself worth reporting. `Unavailable` means the inputs never allowed
the question to be asked.

`CapabilityBoundary` has a consequence worth planning around. `R_c` is a maximum
over every batch above `c`, so if any one of them has no usable measurement, `R_c`
cannot be computed. That missing batch also sits inside the range of every
smaller candidate, so a single gap censors every candidate below it.

This is what makes `Kmax` a trade-off. A larger `Kmax` supports a stronger claim,
because the campaign has looked further before declaring the curve flat. It also
gives more opportunities for one unmeasurable batch to censor the entire result.

### Low throughput is a measurement, not a defect

At some batch size a runtime may deliver fewer items per second than at a
smaller one, because of memory pressure, kernel selection, or a changed execution
path. That is a real property of the configuration and one of the things a
saturation campaign exists to find.

Such a batch is admitted normally and stays in the energy domain. There is no
minimum-throughput gate anywhere in this plan, in admission or in derivation.

Low throughput is not assumed to persist at larger batches either. Throughput can
drop at one batch and recover at the next when the runtime picks a different
kernel. Ending the domain at the first drop would assume that throughput only
moves one way, which this plan does not assume anywhere else.

Low throughput does affect the numbers, through the ordinary path. Each trial
carries a coefficient

```text
a_i = window seconds / completed items
```

which is simply how many seconds of wall-clock each completed item occupied. The
node draws idle power for the whole window whether or not it is serving, so each
item is charged `a_i` seconds' worth of idle power, and subtracting that is what
makes `epsilon` incremental. A trial that completed 5,417 items in a 30.0-second
window has `a_i = 0.00554` seconds per item. A slower batch has a larger `a_i`,
so more of its measurement depends on the idle-power estimate, and its interval
comes out wider. Evidence is never discarded to make an interval narrower.

Finally, `b_sat` answers an energy question and `b*(lambda, q)` answers a
capacity-and-SLO question. Throughput curves, latency curves, and the
SLO-feasible batch range are separate profile outputs, consumed by L1 and L4.

## Checkpoint 2: what is fixed before measuring

Two files are written and fixed before the first Run.

### The derivation policy file

This file holds every value that decides what the campaign accepts. Its content
digest is recorded in the derived result, so a published number always identifies
the rule that produced it.

```text
PointPolicy
    name
    stage                          idle-baseline | incremental-point
    minimumTrials

BSatPolicy
    name
    kmax
    relativeImprovementTolerance   tau
    discoveryTrials                n_disc
    maximumConfirmatoryTrials      cap on n_conf
    maximumActiveRunBudget         cap on total active Runs
```

`stage` scopes a point policy the way `workloadMode` scopes an admission policy,
and the selected policy's stage is checked against its use.

The estimator, confidence level, Bonferroni allocation, and confirmatory sizing
method are versioned package constants rather than policy fields, because the
implementation supports exactly one of each. Their versioned estimator identity
is recorded in the result. A policy field accepting only that one method would
pretend to offer configuration that does not exist.

**`tau` is fixed in this file, before any measurement.** It expresses how much
remaining improvement is small enough to walk away from — a judgment about what
matters operationally, which the data cannot supply. It is also the threshold
discovery screens against, so it has to exist before discovery runs.

Discovery sufficient statistics determine exactly one thing: how many
confirmatory trials are needed, through the fixed sizing rule below, and only up
to the cap already written in this file. If the rule asks for more trials than
the cap or active-Run budget allows, the campaign ends as
`Unavailable: ConfirmatoryBudgetInsufficient`. The tolerance is never widened
to fit the budget.

A second version of this file is written after discovery and before the first
confirmatory Run. It records the discovery version's digest, the realized
`n_conf`, and the realized Run allocation. It changes nothing else. Both digests
appear in the result.

### Fixed confirmatory sizing method

The only sizing method is
`bonferroni-welch-relative-precision-v1`. It chooses the smallest common
confirmatory trial count `n >= 3` that makes the projected simultaneous bound
precise enough at the zero-remaining-improvement planning point. This is a
precision target, not a forecast that confirmation will accept; a real curve
near `tau` may still return `CandidateOnly`.

For discovery trial `i` at batch `b`, retain

```text
x_b,i = total active wall joules / completed items
a_b,i = window seconds / completed items
z_b,i = x_b,i - a_b,i * Pbar_q(b)
```

where `Pbar_q(b)` is the exact-state idle-baseline mean. Let `s_z,b^2` be the
discovery sample variance of `z_b,i`, `abar_b` the mean coefficient, and
`V_P,q` the variance of the idle-baseline mean with its own degrees of freedom.
For a prospective common confirmatory count `n`, the projected point variance is

```text
V_b(n) = s_z,b^2 / n + abar_b^2 * V_P,q(b)
```

and the projected contrast variance is

```text
V_D(c,b,n) = s_z,c^2 / n + s_z,b^2 / n
             + (abar_c - abar_b)^2 * V_P,q       when q(c) = q(b)

V_D(c,b,n) = s_z,c^2 / n + s_z,b^2 / n
             + abar_c^2 * V_P,q(c) + abar_b^2 * V_P,q(b)
                                                    otherwise
```

The observed `a` coefficients are conditioned-on design quantities. Active Run
sets and distinct idle-baseline sets are independent. Each projected variance
uses Welch--Satterthwaite degrees of freedom over its nonzero active and baseline
components: prospective active components use `n-1`, and retained baseline
components use their admitted baseline degrees of freedom. With
`M = Kmax - c + 1` simultaneous one-sided statements and `alpha = 0.05`, let
`h_c(n)` and `h_D(c,b,n)` be their Student-t half-widths at quantile
`1 - alpha/M`. Let `epsilon_hat_c` be the discovery point estimate and

```text
L_c(n) = epsilon_hat_c - h_c(n)
```

The first `n` is selected for which `L_c(n) > 0` and, for every `b > c`,

```text
h_D(c,b,n) / L_c(n) <= tau
```

The calculation then checks both declared caps using the exact Run-count formula
below. A fixed idle-baseline uncertainty creates a nonzero precision floor; if
no `n` through the cap satisfies the inequalities, the typed budget-insufficient
outcome is returned. No normal critical value replaces Student-t, and no
unimplemented extrapolation beyond the declared cap is reported.

### The campaign record

This file identifies the campaign and makes its evidence checkable by reading.
Before measurement it fixes:

- the execution state under measurement;
- the derivation policy file it uses, by content digest;
- the evidence-inclusion rule, in prose: every identity-compatible admitted trial
  produced under this campaign is included, and none is excluded;
- the rule that generates confirmatory batches `c..Kmax` from whatever candidate
  discovery proposes; and
- whether idle-baseline Runs count against the policy file's Run budget.

After the campaign it records every Run name and its retained intent digest, with
discovery and confirmatory Runs listed separately.

Fixing the *generating rule* rather than the confirmatory batch list is what lets
the campaign start without knowing the candidate, while keeping the range from
being narrowed after the results are in.

### How many Runs a campaign costs

```text
R_active = Kmax * n_disc + (Kmax - c + 1) * n_conf
R_max    = Kmax * (n_disc + n_conf)              when c = 1
```

The cost depends on where the candidate lands, and the worst case is `c = 1`:
if batching turns out to help nothing at all, confirmation has to re-measure the
entire domain. The physically simplest outcome is the most expensive to confirm.
The budget cap in the policy file is set against this worst case.

## Checkpoint 3: exact idle baseline ownership

### Why it matters

`epsilon` is energy above idle, so every point on a curve is computed by
subtracting an idle-power estimate. That estimate belongs to an exact execution
state. Reusing it for another fixed-batch engine would erase a material state
difference; treating several estimates as if they were one shared random
quantity would make the confidence bound wrong.

### How it is guaranteed

The curve family and curve are opaque types with one construction path:

```text
type Curve struct { /* unexported fields */ }
type CurveFamily struct { /* unexported fields */ }

type Pass struct {
    Role    discovery | confirmatory
    Batches admitted trials of that pass, grouped by batch
}

FreezeCurveFamilyContract(resolved member contracts) (CurveFamilyContract, error)
VerifyCurveFamily(contract CurveFamilyContract,
                  verified frozen members) (CurveFamily, error)
DeriveCurve(passes []Pass, family CurveFamily,
            baselinesByState map[string]IdleBaseline,
            policy PointPolicy) (Curve, error)
Curve.Points() []Point                      // copies, for reporting
DeriveBSatConfirmatory(curve Curve, candidate int, policy BSatPolicy) BSatResult
```

`FreezeCurveFamilyContract` fixes ADR 0014's shared anchor and exact
batch-member map before measurement. `VerifyCurveFamily` compares Loader-verified
frozen Run facts with that contract and binds their common observed environment
before producing the versioned family identity. `DeriveCurve` resolves each
trial to that map, selects the baseline with the same `StateIdentity`, and
performs the per-trial incremental step and per-batch aggregation internally.
Since `Curve` has no exported fields and no other constructor, every point that
reaches the decision boundary has an exact family member and baseline.
`Points()` hands out copies for reporting, and those copies cannot re-enter the
decision.

Both passes go into the same call:

```text
discoveryCurve = DeriveCurve([discovery], family, baselinesByState, pointPolicy)
candidate      = screen(discoveryCurve, bsatPolicy)

campaignCurve  = DeriveCurve([discovery, confirmatory], family,
                             baselinesByState, pointPolicy)
result         = DeriveBSatConfirmatory(campaignCurve, candidate, bsatPolicy)
```

The confirmatory decision needs all three at once: the confirmatory points to
compute `R_c`, the discovery points to establish that lower batches were
measured, and the same baseline set to relate every occurrence of each exact
state. A single-pass curve cannot hold them together.

`DeriveCurve` rejects a `TrialID` that appears in more than one pass. This makes
the discovery/confirmatory separation mechanical, reusing the trial-uniqueness
check that aggregation already applies within a batch.

The constructor rejects a missing member baseline, a state or environment
mismatch, duplicate idle raw evidence across distinct baseline keys, and a
different baseline object for the same state between passes. A dynamic-batch
family has one map entry. A fixed-artifact family normally has one entry per
batch. The distinction is determined by exact state identity, not runtime
naming.

### The estimator

Use the trial-level `x_b,i`, `a_b,i`, and `z_b,i` defined by the sizing method.
With `n_b` active Runs at batch `b`, `abar_b` their mean observed coefficient,
and `q(b)` the exact baseline key:

```text
epsilon_bar_b      = mean_i(z_b,i)

Var(epsilon_bar_b) = s_z,b^2 / n_b
                      + abar_b^2 * V_P,q(b)
```

The point estimate is algebraically the same as subtracting the exact baseline
from each trial and then averaging, so published point values do not move. The
interval does. `s_z,b^2` retains the observed covariance between active wall
energy and seconds per item instead of pretending those trial quantities vary
independently. Combining active and baseline components uses the
Welch--Satterthwaite effective degrees of freedom; it is not taken from either
component alone.

For a contrast `D_j,k = epsilon_j - epsilon_k`, the active components are
independent across batches. Its baseline component is
`(abar_j-abar_k)^2 * V_P,q` when the state is shared, or
`abar_j^2 * V_P,q(j) + abar_k^2 * V_P,q(k)` otherwise. Reuse of one raw idle
evidence object across distinct keys is rejected. Shared-state subtraction thus
retains partial baseline cancellation; distinct states retain both baseline
variances. Every coefficient comes from observed `T_i/N_i`, and no throughput
monotonicity is assumed.

Two contrasts can share both an active reference point and idle baselines. Let
`w_D,r` be `+1` or `-1` when point `r` enters contrast `D`, and zero otherwise.
Let `g_D,q` be the resulting coefficient on baseline mean `P_q`. Then

```text
Cov(D_1, D_2)
    = sum_r w_D1,r * w_D2,r * s_z,r^2 / n_r
      + sum_q g_D1,q * g_D2,q * V_P,q
```

Active terms are zero across discovery and confirmation because their Runs are
disjoint; baseline terms remain because both passes reuse the same exact-state
baseline set. This covariance model links the differences inside one acceptance
decision, discovery selection, and the two passes. Showing that the complete
procedure holds its stated confidence is what Checkpoint 1 requires.

So the design is best described as **discovery and confirmation measured on
separate Runs, sharing the same exact-state baseline set**. The active evidence
is disjoint; each baseline estimate is reused wherever its state recurs.

### What each trial must retain

The estimator needs the following preserved, per trial and per baseline:

- `BaselineCoefficientSecondsPerItem = T_i / N_i`;
- active energy per item; and
- the baseline's variance or standard error together with its degrees of freedom.

A half-width that has already been multiplied by a coefficient cannot be taken
apart again to recover the covariance structure, and cannot be taken apart at all
when it is zero.

### The already-published point

The live batch-one figure recorded in Plan 3 was produced by the previous
interval construction. Its point estimate is unaffected by this plan; its
interval is recomputed. Both results are retained under distinct estimator
versions so each remains reproducible.

## Checkpoint 4: discovery and confirmation

### Discovery

Discovery measures batches `1..Kmax` at `n_disc` trials each and proposes the
smallest `c` in `1..Kmax-1` whose point estimate satisfies
`R_c_hat <= tau`, using the `tau` already fixed in the discovery policy
version. `Kmax` remains in every eligible candidate's comparison set but can
never be returned as the candidate.

This is a screen. It makes no confidence statement, and it is the sole basis for
the minimality noted in Checkpoint 1. Its one requirement is determinism, so that
the confirmatory batch list follows from the campaign record rather than from a
judgment call.

Discovery also produces the per-batch variance that the sizing rule turns into
`n_conf`.

### Confirmation

```text
DeriveBSatConfirmatory(campaignCurve Curve, candidate int, policy BSatPolicy) BSatResult
```

Confirmation receives the candidate and evaluates the acceptance rule there. It
does not search for a candidate of its own; searching and testing on the same
evidence is what the two-pass split exists to prevent.

Confirmation still validates that the domain runs from batch 1. Accepting
saturation at `c` without evidence that batches below `c` were measured would not
establish that a smaller batch is not equally good. Those lower batches do not
need re-measuring in the confirmatory pass unless the candidate search is
repeated.

### Why there is no universal sizing table

A fixed table based on one batch's coefficient of variation would discard the
exact-state baseline floor, shared-baseline covariance, candidate-dependent
number of statements, and per-batch active variance. The sizing method therefore
evaluates every integer `n` against the frozen family and discovery sufficient
statistics. A concrete campaign policy records only the cap and budget; its
post-discovery policy records the resulting `n_conf` before confirmation starts.

## Verification boundary

Plan 5 does not add machine-enforced provenance for the campaign as a whole. The
freezing machinery in Plans 3 and 4 exists because a controller creates Runs
unattended and an operator can edit a policy ConfigMap between them. Plan 5 has
no controller: derivation is offline and read-only, and the campaign is run once
by hand.

What the code guarantees, and what a reader has to check:

| Guarantee | Mechanism |
|---|---|
| every point belongs to the frozen family and uses its exact-state idle baseline | opaque `CurveFamily` and `Curve`; `DeriveCurve` takes the family and baseline map |
| a hand-assembled `Point` cannot reach the decision | `DeriveBSatConfirmatory` accepts `Curve`, not `[]Point` |
| discovery and confirmatory evidence do not overlap | `DeriveCurve` rejects a `TrialID` present in more than one pass |
| the candidate was not chosen from the evidence that judges it | `DeriveBSatConfirmatory` takes the candidate as an input |
| a published number identifies its planned and realized family and rule | curve-family contract digest, realized identity, and both policy digests recorded in the result |
| the tolerance was fixed before the evidence that judges it | **the two policy versions are separate artifacts fixed at separate points — recorded, not enforced** |
| every eligible trial was included | **the campaign record's stated rule and its published Run list — read, not enforced** |

The last two rows are checked by reading rather than by code. The digests prove
*which* policy produced a result; nothing in the design proves *when* it was
written. The two published Run lists must have no Run in common, and together
must account for exactly the trials that entered the result. This is the standard
of pre-registration used in empirical work. It is not proof against an operator
who runs extra Runs and leaves them out of the record.

Machine-enforced equivalents — an assignment field inside the immutable intent, a
selection-scope identity, a closed eligible population checked against what was
selected, and a cryptographic baseline-evidence identity — belong to the plan
that introduces a campaign controller, where unattended Run creation is the
condition they exist for. An assignment field inside the intent would also force
an intent schema version bump across the Go codec, the Python worker's strict
version check, and the retained wire shapes, with no consumer for it yet.

## Code boundary

```text
internal/profilederivation/   the acceptance quantity and rule, curve derivation
                              owning exact-state baselines, the joint estimator, the
                              confirmatory boundary, policy file decoding
```

Nothing changes in `api/v1alpha1`, the intent schema, the Profiler controller,
the measurement worker, or single-Run admission. The pure package continues to
import no Kubernetes, CHILL API, controller-runtime, or transport package.

## Explicitly out of scope

- `DeviceProfile` or `ClusterEnergyModel` schema, persistence, or status
- a campaign CRD, controller, or orchestrator
- any intent schema change
- machine-enforced campaign assignment or eligible-population closure
- a per-pass baseline change for the same state, inferred per-engine idle
  equivalence, or comparison across two curve-family identities
- confirming at confidence that the accepted batch is the smallest one
- batch selection outside the campaign record's generating rule
- a doubling or otherwise non-consecutive batch sweep
- changing `tau` during a campaign
- cross-node or cross-model pooling
- transition-cost measurement, standby-versus-model-loaded idle separation, and
  any method that infers per-engine idle equivalence
- throughput, latency, or SLO feasibility derivation
- scheduler or enumerator consumption

## Acceptance boundary

Plan 5 is complete when:

- `Kmax`, `tau`, the acceptance quantity, and the acceptance rule live in the
  discovery policy version, and both policy digests version the result;
- the complete two-pass procedure, including candidate selection and the
  exact-state baseline covariance, is shown by derivation or simulation to hold
  its stated confidence;
- the sizing rule selects the smallest trial count satisfying its simultaneous
  relative-precision inequalities and returns budget-insufficient when the
  baseline floor or either cap prevents one;
- synthetic golden curves close the accepted, candidate-only, capability-boundary,
  no-saturation, non-positive-reference, and budget-insufficient cases, and a
  later batch that improves too much fails acceptance through `R_c` rather than
  through a separate outcome;
- `Curve` has no exported field and no constructor besides `DeriveCurve`, so a
  hand-assembled `Point` cannot reach `DeriveBSatConfirmatory` and no curve mixes
  curve families or mismatches a point with its exact-state baseline;
- family freezing and verification reject drift outside the explicitly member-specific
  artifact, execution-path, and batch fields fixed by ADR 0014;
- `DeriveCurve` rejects a `TrialID` appearing in more than one pass;
- `DeriveBSatConfirmatory` rejects a candidate its curve does not span;
- discovery and confirmation reject `Kmax < 2` and a candidate outside
  `1..Kmax-1`, so the accepted result always has at least one larger witness
  batch;
- the rebuilt estimator reproduces the live batch-one point estimate and produces
  a separately versioned interval, with the earlier interval retained;
- a trial with low observed throughput is admitted, retained, and kept in the
  domain; and
- the campaign record lists every Run, separated by pass, with retained intent
  digests.

## Implementation sequence

1. Fix the candidate domain `1..Kmax-1`, `R_c`, Bonferroni allocation,
   positivity condition, conditional-coefficient variance model, confirmatory
   sizing method, minimality boundary, and the rule that observed throughput
   never removes evidence. Implemented in this plan.
2. Preserve `T_i / N_i` per trial and make the baseline's variance and degrees of
   freedom reachable from the derivation path. Implemented.
3. Extend the implemented opaque `CurveFamily`, `Curve`, and `DeriveCurve`
   boundary to retain one exact-state baseline set across both passes. Rebuild
   aggregation and the point-confidence construction; the current construction
   combines Student-t half-widths at differing degrees of freedom, so the live
   batch-one interval is recomputed and the earlier one retained under its own
   estimator version.
4. Show, by derivation or simulation, that the two-pass procedure holds its
   stated confidence across every batch-to-batch difference the screen and the
   rule touch.
5. Implement discovery screening, fixed confirmatory sizing, and the
   confirmatory boundary. Remove the
   `Ambiguous` outcome, which the domain-wide maximum subsumes.
6. Decode the policy file and record its digests. Rename
   `RelativeEquivalenceTolerance` to `RelativeImprovementTolerance` and
   `AdjacentEquivalenceConfirmed` to `DomainNoImprovementConfirmed`, and add
   `Censored: CapabilityBoundary`; confirm first that no `b_sat` result has been
   persisted to the evidence store, since these are a JSON field name and a
   reason-code value. Add golden tests.
7. Before any new campaign Run, fix `Kmax`, `tau`, `n_disc`, both caps, the
   campaign record, eligible member map, and `CurveFamilyContractDigest` in the
   discovery policy version. The realized `CurveFamilyIdentity` follows Loader
   verification.
8. Fix the confirmatory policy version recording the realized `n_conf` and Run
   allocation inside the caps set at item 7, before any confirmatory Run.

Item 1 is the estimator decision and closes before code. Item 7 is the
campaign-specific freeze and closes before measurement. Item 8 records an
allocation inside limits already set at item 7 and introduces no new scientific
parameter.

## Follow-on boundary

A later plan owns publishing the derived curve, `b_sat`, and the idle baseline as
a `DeviceProfile`, and the controller that runs a campaign. That plan is where
the machine-enforced provenance described under
[Verification boundary](#verification-boundary) becomes load-bearing, because a
controller creating Runs unattended is the condition those mechanisms exist for.

Standby-versus-model-loaded idle and transition-cost decomposition belong to a
transition plan. They do not gate this campaign. Per-engine model-loaded idle is
not deferred: ADR 0014 requires an admitted baseline for every distinct member
state. Batch remains excluded from the exact state and protocol projections as
an axis value, but a fixed-batch artifact's digest, path, capability, and supply
image remain bound. `CurveFamilyIdentity` is the only boundary allowed to
compose those exact states.
