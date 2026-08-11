# Plan 5: Batch-Saturation Campaign

> Navigation: [Profiling design index](../README.md)

## Status

Approved implementation plan. The reusable frozen-family, verified-family, and
opaque exact-state curve boundaries are implemented under Plan 5a, and its live
sequential result supplies one discovery candidate. They do not implement Plan
5's joint estimator or statistical claim. The derivation path now retains each
active trial's wall-energy-per-item and `T_i / N_i`, plus each exact-state idle
baseline's mean variance and degrees of freedom. `Aggregate` now emits the
versioned Welch--Satterthwaite point interval and retains the previous interval
under its own version. The post-selection error allocation is also fixed and
regression-tested with exact baseline covariance, and the
deterministic domain-wide discovery screen is implemented. The joint contrast
estimator, global-`H` confirmatory sizing, and typed confirmatory result are
implemented. Both policy files have strict semantic decoders and configured
names; the confirmatory file repeats the discovery fields and is checked against
the realized sizing allocation. The final result binds both names. A concrete
RKNN campaign measured with the two separate artifacts then in use is also
closed. All 12 idle and discovery Runs were
retained, Loader-verified, admitted, and included. Discovery selected batch one,
but the frozen three-trial cap could not meet the 5% confirmatory precision rule.
The committed second policy and typed final result therefore record
`Unavailable: ConfirmatoryBudgetInsufficient` with no confirmatory Run
allocation. A disjoint second campaign is frozen with the first campaign used
only as a pilot for a six-trial cap and 18-active-Run budget; its evidence will
not be pooled. Its own discovery subsequently selected batch one and froze four
confirmatory Runs at each measured batch before executing any of them. All eight
were retained and admitted, but the final remaining-improvement upper bound was
6.395%, above the frozen 5% rule, so the typed result is
`CandidateOnly: DomainNoImprovementNotConfirmed`. This plan fixes the quantity
that decides `b_sat` — the largest
remaining energy improvement any larger measured batch still offers — gives curve derivation
ownership of the exact idle baseline, rebuilds the point-confidence
construction, and separates candidate discovery from confirmation. It does not
authorize a `DeviceProfile` API, a campaign CRD or controller, an intent schema
change, cross-model pooling, or scheduler consumption.

[ADR 0016](../../../adr/0016-batch-profile-artifact-capability.md) defines one
immutable artifact with a declared batch-profile set. Exact execution-state and
protocol identities remain unchanged. An opaque curve owns one pre-frozen
family contract, one Loader-verified family, and one admitted idle baseline for
the artifact's single state key.

Plan 3 requires a campaign driver to predeclare an evidence-inclusion rule. Plan
4 leaves batch selection and repeated trials to a later campaign planner. This
plan closes both by pre-registration and read-time enumeration of the frozen
Run-name prefix. The [Verification boundary](#verification-boundary) states
exactly what that guarantees.

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
second pass is generated from the first. It fixes a Run-name prefix and an
inclusion rule, not a copied result list. Every choice in both files therefore
predates the evidence it will judge.

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

The catalog freezes the artifact's eligible batch set before execution, and the
discovery pass follows that set in its own order. `Kmax >= 2` is fixed before
execution and must equal the largest eligible batch value. The profile set may
be sparse.

Plan 5's confirmatory estimand ranges over every integer batch above candidate
`c` through `Kmax`. Confirmatory sizing therefore requires the eligible span
`c..Kmax` to be consecutive and returns an explicit error for the first gap.
Plan 5a owns sequential decisions over sparse spans.

Candidate batches are eligible values below `Kmax`. `Kmax` is the required
final witness batch. A candidate equal to `Kmax` would have an empty comparison
set. If no candidate below `Kmax` satisfies the discovery rule, the typed
outcome is `Censored: NoSaturationObserved`.

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

At a fixed candidate `c`, the rule makes `Kmax - c` contrast statements plus one
positivity statement. Allocating 5% only across those statements is insufficient:
discovery selected `c` after seeing every point estimate, including the shared
idle-baseline error. The allocation must cover every statement that could enter
the result after that selection.

Across all possible candidates `c in 1..Kmax-1`, that family contains

```text
S = sum_c (Kmax - c + 1)
  = (Kmax - 1)(Kmax + 2) / 2
```

statements: one positivity bound for each candidate and one contrast bound for
each ordered pair `c < b`. `S` is sufficient only when the confirmatory trial
count was fixed before discovery. Plan 5 also chooses `n_conf` from discovery,
so that second selection belongs to the error family too.

Let `B_active` be the fixed maximum active-Run budget and `cap` the fixed maximum
confirmatory trials per batch. For each candidate, the feasible trial counts are

```text
nmax(c) = min(cap,
              floor((B_active - Kmax * n_disc) / (Kmax - c + 1)))
n_min   = incremental-point.minimumTrials
N_c     = {n_min, ..., nmax(c)}
```

with `n_min >= 3` and an empty set when `nmax(c) < n_min`. The policy decoder
also requires `n_disc >= n_min` and `cap >= n_min`, so neither pass can ask the
point estimator to aggregate fewer Runs than its stage-scoped policy permits.
The full number of selectable marginal statements is fixed before measurement as

```text
H = sum_c |N_c| * (Kmax - c + 1)
```

Every bound used for the selected `(c, n_conf)` is computed at one-sided
quantile `1 - 0.05/H`, not at a candidate- or realized-size-dependent quantile.
If `n_conf` was independently fixed before discovery, each `N_c` is the same
singleton; when it is feasible for every candidate, `H = S`. The [fixed sizing
method](#fixed-confirmatory-sizing-method) evaluates precision against the same
global allocation. Methods that model the relationships may use less error
budget, but adopting one is a new estimator version rather than a configured
option. A selected candidate with an empty `N_c`, or `H = 0`, returns
`Unavailable: ConfirmatoryBudgetInsufficient` without making a confidence claim.

The post-selection guarantee follows directly. Imagine the predeclared
confirmatory sample for every feasible candidate and trial count, and let
`F_c,n,j` be failure of one marginal bound. Regardless of how discovery selects
`C` and `n_conf`, or how baseline estimates are shared,

```text
P(selected claim is not covered)
    <= P(any F_c,n,j over every feasible c, n, and statement j)
    <= sum_c,n,j P(F_c,n,j)
    <= H * (0.05 / H)
    = 0.05
```

No independence between selection and the baseline is assumed by this union
bound. Active discovery and confirmation Runs remain disjoint; the exact-state
baseline is intentionally shared. The guarantee is nominal under the
independent-Gaussian Run model used by the Student-t and Welch--Satterthwaite
marginal bounds, not distribution-free.

`plan5_coverage_test.go` is the deterministic regression oracle for the fixed
`n_conf=3` special case, where `H = S`. It runs 20,000 fixed-seed `Kmax=4`
procedures under Gaussian active and idle errors. The deliberately
baseline-dominated shared case gives 5.360% whole-procedure miscoverage under
the rejected candidate-local allocation, so that allocation cannot silently
return. The global allocation gives:

| covariance regime | selected procedures | whole-procedure miscoverage |
|---|---:|---:|
| shared baseline dominates | 19,943 | 2.270% |
| balanced shared baseline | 19,762 | 0.975% |
| active variance dominates | 14,581 | 0.620% |
| distinct exact-state baselines | 18,245 | 1.625% |

The simulation checks the implemented marginal formulas at the smallest
allowed trial count. The distinct-state row is retained estimator regression
coverage; ADR 0016's current artifact family uses the shared-baseline rows. The
simulation is regression evidence for the union-bound derivation above.

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

An accepted result says: *under the declared independent-Gaussian Run model, at
batch `c`, no larger batch within `1..Kmax` improves energy per item by more than
`tau`, at the declared nominal confidence.*

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
| an explicit capability boundary lies at or below `Kmax` | retained case `Censored: CapabilityBoundary` |
| no candidate in `1..Kmax-1` satisfies the rule | `Censored: NoSaturationObserved` |
| `LCB(epsilon_c) <= 0` | `Unavailable: NonPositiveReferenceLowerBound` |
| the required trial count exceeds the declared cap | `Unavailable: ConfirmatoryBudgetInsufficient` |

`Censored` means the campaign ran correctly but cannot produce an answer, and the
reason is itself worth reporting. `Unavailable` means the inputs never allowed
the question to be asked.

`BSatCodeCapabilityBoundary` describes an explicit boundary at or below
`Kmax`. The frozen-set boundary is derived as one greater than the largest
eligible batch, while `Kmax` equals that largest eligible value. A Run failure
for a declared profile follows the campaign's ordinary trial-failure path.

`R_c` remains a maximum over every integer batch above `c`. A gap in the
eligible span `c..Kmax` leaves that estimand undefined, so confirmatory sizing
returns an error naming the first missing batch.

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

This file holds every value that decides what the campaign accepts. Its
configured name is recorded in the derived result, so a published number
identifies the rule that produced it.

```text
DiscoveryDerivationPolicy
    pointConfidenceLevel           two-sided point and idle-baseline level

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
    familyWiseAlpha                alpha for the global one-sided family
```

`stage` scopes a point policy the way `workloadMode` scopes an admission policy,
and the selected policy's stage is checked against its use.

The estimator, Bonferroni allocation, and confirmatory sizing methods are
versioned package constants because the implementation supports exactly one of
each. Their identities are recorded in the result. The point confidence level
and family-wise alpha accept their implemented numeric ranges and are frozen in
the current `v1alpha2` policy name; neither value has a compiled default. The
committed `v1alpha1` campaign files remain historical records of the earlier
fixed-method runs and are not accepted as current policy input. New policies
cannot omit either value.

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
confirmatory Run. It repeats the discovery fields and records the realized
`n_conf` and Run allocation. It changes nothing else. Both configured names
appear in the result.

### Fixed confirmatory sizing method

The only sizing method is
`bonferroni-welch-relative-precision-v1`. It chooses the smallest common
confirmatory trial count `n >= incremental-point.minimumTrials` that makes the
projected simultaneous bound precise enough at the
zero-remaining-improvement planning point. The configured minimum is itself at
least three. This is a precision target, not a forecast that confirmation will
accept; a real curve near `tau` may still return `CandidateOnly`.

For discovery trial `i` at batch `b`, retain

```text
x_b,i = total active wall joules / completed items
a_b,i = window seconds / completed items
z_b,i = x_b,i - a_b,i * Pbar_q
```

where `Pbar_q` is the artifact's exact-state idle-baseline mean. Let `s_z,b^2` be the
discovery sample variance of `z_b,i`, `abar_b` the mean coefficient, and
`V_P,q` the variance of the idle-baseline mean with its own degrees of freedom.
For a prospective common confirmatory count `n`, the projected point variance is

```text
V_b(n) = s_z,b^2 / n + abar_b^2 * V_P,q
```

and the projected contrast variance is

```text
V_D(c,b,n) = s_z,c^2 / n + s_z,b^2 / n
             + (abar_c - abar_b)^2 * V_P,q
```

The observed `a` coefficients are conditioned-on design quantities. Active Run
sets are independent, and every profile shares the artifact's idle baseline.
Each projected variance uses Welch--Satterthwaite degrees of freedom over its
nonzero active and baseline components: prospective active components use
`n-1`, and the retained baseline component uses its admitted degrees of freedom.
With the global
post-selection count `H` fixed from both policy caps and the configured
`familyWiseAlpha = alpha`, let
`h_c(n)` and `h_D(c,b,n)` be their Student-t half-widths at quantile
`1 - alpha/H`. Let `epsilon_hat_c` be the discovery point estimate and

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

```text
DeriveBSatConfirmatorySizing(discoveryCurve Curve, candidate int,
                             policy DiscoveryDerivationPolicy)
    (BSatSizingResult, error)
```

The implemented function re-derives and checks the discovery candidate, computes
every budget-feasible `N_c` and the global `H` with checked integer arithmetic,
and selects the first `n` satisfying both inequalities. Its result records the
sizing-method version, `H`, quantile, confirmatory batch allocation, projected
precision, and exact total active-Run count. A baseline floor or either cap
returns `Unavailable: ConfirmatoryBudgetInsufficient`; a statement family beyond
the supported floating-point range is an error, not an approximation.

### The campaign record

This file identifies the campaign and makes its evidence set discoverable from
retained `ProfilingRun` objects. Before measurement it fixes:

- the execution state under measurement;
- the derivation policy file it uses, by source object identity;
- the namespace and Run-name prefix;
- the evidence-inclusion rule, in prose: every identity-compatible admitted trial
  produced under this campaign is included, and none is excluded;
- the rule that generates confirmatory batches `c..Kmax` from whatever candidate
  discovery proposes, after the consecutive-span check; and
- whether idle-baseline Runs count against the policy file's Run budget.

The record is not amended with Run identities or measurement values. Readers
enumerate the frozen prefix, inspect Run admission status, and follow each
`status.evidence` reference at read time.

Fixing the *generating rule* rather than the confirmatory batch list is what lets
the campaign start without knowing the candidate, while keeping the range from
being narrowed after the results are in.

### How many Runs a campaign costs

```text
R_active = Kmax * n_disc + (Kmax - c + 1) * n_conf
R_max    = Kmax * (n_disc + n_conf)              when c = 1
```

`Kmax` in these sizing formulas is the largest eligible batch value. The sizing
method and its predeclared conservative budget remain fixed.

The cost depends on where the candidate lands, and the worst case is `c = 1`:
if batching turns out to help nothing at all, confirmation has to re-measure the
entire domain. The physically simplest outcome is the most expensive to confirm.
The budget cap in the policy file is set against this worst case.

## Checkpoint 3: exact idle baseline ownership

### Why it matters

`epsilon` is energy above idle, so every point on a curve is computed by
subtracting an idle-power estimate. That estimate belongs to an exact execution
state. One batch-profile artifact supplies every eligible point under one state
key, and its curve owns one shared idle baseline.

### How it is guaranteed

The curve family and curve are opaque types with one construction path:

```text
type Curve struct { /* unexported fields */ }
type CurveFamily struct { /* unexported fields */ }

type Pass struct {
    Role    discovery | confirmatory
    Batches admitted trials of that pass, grouped by batch
}

FreezeCurveFamilyContract(shared contract with eligible batches)
    (CurveFamilyContract, error)
VerifyCurveFamily(contract CurveFamilyContract,
                  verified frozen state) (CurveFamily, error)
DeriveCurve(passes []Pass, family CurveFamily,
            baselinesByState map[string]IdleBaseline,
            policy PointPolicy) (Curve, error)
Curve.Points() []Point                      // copies, for reporting
DeriveBSatConfirmatory(curve Curve, candidate int,
                       policy ConfirmatoryDerivationPolicy) BSatResult
```

`FreezeCurveFamilyContract` fixes ADR 0016's shared artifact contract and
eligible batch set before measurement. `VerifyCurveFamily` compares
Loader-verified frozen Run facts with that contract and binds their common
observed environment before constructing the verified family. `DeriveCurve`
resolves each trial to the frozen eligible sequence, selects the family's
single baseline with the same `StateIdentity`, and
performs the per-trial incremental step and per-batch aggregation internally.
Since `Curve` has no exported fields and no other constructor, every point that
reaches the decision boundary has the exact family state and baseline.
`Points()` hands out copies for reporting, and those copies cannot re-enter the
decision.

Both passes go into the same call:

```text
discoveryCurve = DeriveCurve([discovery], family, baselinesByState,
                             discoveryPolicy.PointPolicy(incremental-point))
candidate      = DeriveBSatDiscovery(discoveryCurve, discoveryPolicy)
sizing         = DeriveBSatConfirmatorySizing(discoveryCurve, candidate,
                                               discoveryPolicy)
confirmPolicy  = FreezeConfirmatoryDerivationPolicy(discoveryPolicy, sizing)

campaignCurve  = DeriveCurve([discovery, confirmatory], family,
                             baselinesByState,
                             discoveryPolicy.PointPolicy(incremental-point))
result         = DeriveBSatConfirmatory(campaignCurve, candidate, confirmPolicy)
```

The confirmatory decision needs all three at once: the confirmatory points to
compute `R_c`, the discovery points to establish that lower batches were
measured, and the same baseline to relate every occurrence of the exact state.
A single-pass curve cannot hold them together.

`DeriveCurve` rejects a `TrialID` that appears in more than one pass. This makes
the discovery/confirmatory separation mechanical, reusing the trial-uniqueness
check that aggregation already applies within a batch.

The constructor requires exactly one baseline keyed by the family's state and
rejects a state or environment mismatch or a different baseline object between
passes.

### The estimator

Use the trial-level `x_b,i`, `a_b,i`, and `z_b,i` defined by the sizing method.
With `n_b` active Runs at batch `b`, `abar_b` their mean observed coefficient,
and `q` the artifact's exact baseline key:

```text
epsilon_bar_b      = mean_i(z_b,i)

Var(epsilon_bar_b) = s_z,b^2 / n_b
                      + abar_b^2 * V_P,q
```

The point estimate is algebraically the same as subtracting the exact baseline
from each trial and then averaging, so published point values do not move. The
interval does. `s_z,b^2` retains the observed covariance between active wall
energy and seconds per item instead of pretending those trial quantities vary
independently. Combining active and baseline components uses the
Welch--Satterthwaite effective degrees of freedom; it is not taken from either
component alone.

For a contrast `D_j,k = epsilon_j - epsilon_k`, the active components are
independent across batches. Every point has the same baseline key, so the
baseline component is `(abar_j-abar_k)^2 * V_P,q`. Shared-state subtraction
retains partial baseline cancellation. Every coefficient comes from observed
`T_i/N_i`, and no throughput monotonicity is assumed.

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
baseline. This covariance model links the differences inside one acceptance
decision, discovery selection, and the two passes. Showing that the complete
procedure holds its stated confidence is what Checkpoint 1 requires.

Discovery and confirmation use separate active Runs and share the artifact's
one exact-state baseline. The baseline estimate is reused at every batch in
both passes.

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
versions so each remains reproducible. The rebuilt interval is
`incremental-point-welch-satterthwaite-v2`; the retained interval is
`incremental-point-rss-half-width-v1`.

## Checkpoint 4: discovery and confirmation

### Discovery

Discovery measures every batch in the frozen eligible sequence at `n_disc`
trials and proposes the smallest eligible `c < Kmax` whose point estimate
satisfies `R_c_hat <= tau`, using the `tau` already fixed in the discovery
policy version. `Kmax` remains in every eligible candidate's comparison set and
is the largest eligible batch value.

```text
DeriveBSatDiscovery(discoveryCurve Curve, policy DiscoveryDerivationPolicy)
    (BSatDiscoveryResult, error)
```

The implemented screen accepts only an opaque discovery-only `Curve`, requires
the largest batch of its frozen family to equal policy `Kmax`, and checks
exactly `n_disc` independent trials at every eligible batch. The retained typed
capability-boundary case describes an explicit boundary at or below `Kmax`;
silent truncation is an invalid input.

This is a screen. It makes no confidence statement, and it is the sole basis for
the minimality noted in Checkpoint 1. Its one requirement is determinism, so that
the confirmatory batch list follows from the campaign record rather than from a
judgment call.

Discovery also produces the per-batch variance that the sizing rule turns into
`n_conf`.

### Confirmation

```text
DeriveBSatConfirmatory(campaignCurve Curve, candidate int,
                       policy ConfirmatoryDerivationPolicy) BSatResult
```

Confirmation receives the candidate and evaluates the acceptance rule there. It
does not search for a candidate of its own; searching and testing on the same
evidence is what the two-pass split exists to prevent.

The implemented boundary internally reconstructs a discovery-only view of the
opaque two-pass `Curve`, rechecks the candidate and sizing allocation, verifies
the curve-family contract and both stage-scoped point-policy versions, requires
exactly `n_conf` new Runs at every batch `c..Kmax`, and evaluates the reference
lower bound and every contrast upper bound at the frozen global-`H` quantile.
`DeriveBSatConfirmatorySizing` first verifies that every integer batch in
`c..Kmax` belongs to the eligible set and returns an explicit gap error when the
span is sparse.
Its result retains both pass point sets, all exact-state idle baselines, bounds
for every later batch, estimator and sizing versions, both policy names, and
the family contract and realized identities. A later measured improvement above
tolerance returns `CandidateOnly: DomainNoImprovementNotConfirmed`; there is no
`Ambiguous` outcome in the Plan 5 boundary. A budget-insufficient sizing outcome
is frozen as a confirmatory policy without a Run allocation, so its typed final
result also retains both policy names without pretending confirmation ran.

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
| a published number identifies its planned and realized family and rule | curve-family name, observed environment, and both policy names recorded in the result |
| the tolerance was fixed before the evidence that judges it | **the two policy versions are separate artifacts fixed at separate points — recorded, not enforced** |
| every eligible trial was included | **enumerate the campaign's frozen Run-name prefix and compare the admitted set with the derivation input — read, not enforced** |

The last two rows are checked by reading rather than by code. Policy names state
*which* rule produced a result; the Git history establishes *when* it was
written. The retained discovery and confirmation Run sets must have no trial in
common, and together must account for exactly the trials that entered the
result. This is the standard of pre-registration used in empirical work. It is
not proof against an operator who deletes a Run before review.

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
  discovery policy version, and both policy names version the result;
- the complete two-pass procedure, including candidate selection and the
  exact-state baseline covariance, is shown by derivation or simulation to hold
  its stated confidence;
- the sizing rule selects the smallest trial count satisfying its simultaneous
  relative-precision inequalities and returns budget-insufficient when the
  baseline floor or either cap prevents one;
- synthetic golden curves close the accepted, candidate-only, no-saturation,
  non-positive-reference, and budget-insufficient cases; retained coverage
  represents the capability-boundary code as schema-compatibility coverage,
  with current-path reachability unspecified; and a
  later batch that improves too much fails acceptance through `R_c` rather than
  through a separate outcome;
- `Curve` has no exported field and no constructor besides `DeriveCurve`, so a
  hand-assembled `Point` cannot reach `DeriveBSatConfirmatory` and no curve mixes
  curve families or mismatches a point with its exact-state baseline;
- family freezing and verification reject drift in the shared artifact,
  execution-path, eligible-batch, tensor, and execution-state facts fixed by
  ADR 0016;
- `DeriveCurve` rejects a `TrialID` appearing in more than one pass;
- `DeriveBSatConfirmatory` rejects a candidate its curve does not span;
- confirmatory sizing rejects a gap from the candidate through `Kmax`;
- discovery and confirmation reject `Kmax < 2` and require an eligible candidate
  below `Kmax`, so the accepted result always has at least one larger witness
  batch;
- the rebuilt estimator reproduces the live batch-one point estimate and produces
  a separately versioned interval, with the earlier interval retained;
- a trial with low observed throughput is admitted, retained, and kept in the
  domain; and
- the frozen Run-name prefix can be enumerated, and every admitted matching Run
  appears exactly once in the appropriate derivation pass.

## Implementation sequence

1. Fix the candidate domain `1..Kmax-1`, `R_c`, Bonferroni allocation,
   positivity condition, conditional-coefficient variance model, confirmatory
   sizing method, minimality boundary, and the rule that observed throughput
   never removes evidence. Implemented in this plan.
2. Preserve `T_i / N_i` per trial and make the baseline's variance and degrees of
   freedom reachable from the derivation path. Implemented.
3. Extend the implemented opaque `CurveFamily`, `Curve`, and `DeriveCurve`
   boundary to retain one exact-state baseline across both passes. Rebuild
   aggregation and the point-confidence construction; the previous construction
   combined Student-t half-widths at differing degrees of freedom, so the live
   batch-one interval is recomputed and the earlier one retained under its own
   estimator version. Implemented.
4. Show, by derivation or simulation, that the two-pass procedure holds its
   stated confidence across every batch-to-batch difference the screen and the
   rule touch. Implemented with the global statement-family derivation and
   deterministic Gaussian working-model regression above.
5. Implement discovery screening, fixed confirmatory sizing, and the
   confirmatory boundary. Remove the
   `Ambiguous` outcome, which the domain-wide maximum subsumes.
   Implemented; the superseded adjacent estimator and its types are deleted.
6. Decode the policy file and record its configured name. Rename
   `RelativeEquivalenceTolerance` to `RelativeImprovementTolerance` and
   `AdjacentEquivalenceConfirmed` to `DomainNoImprovementConfirmed`, and add
   the retained `Censored: CapabilityBoundary` representation; confirm first
   that no `b_sat` result has been persisted to the evidence store, since these
   are a JSON field name and a reason-code value. Add golden tests. Implemented:
   both policy artifacts have
   strict decoding and source object identities, the confirmatory allocation is
   checked against sizing, and the final result records both policy names.
7. Before any new campaign Run, fix `Kmax`, `tau`, `n_disc`, both caps, the
   campaign record, eligible profile set, and `CurveFamilyName` in the
   discovery policy version. The verified `CurveFamily` follows Loader
   verification. [Plan 7](07-batch-saturation-control-path.md) carries these in
   the immutable `BatchSaturation` spec instead of a committed record.
8. Fix the confirmatory policy version recording the realized `n_conf` and Run
   allocation inside the caps set at item 7, before any confirmatory Run.
   Implemented for the first campaign: sizing was budget-insufficient, so the
   canonical policy records that typed outcome and an empty allocation. No
   confirmatory Run was submitted.

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
transition plan. They do not gate this campaign. ADR 0016 gives one
batch-profile artifact one state key and one admitted model-loaded-idle
baseline. Batch remains excluded from the exact state and protocol projections
as an axis value, while the artifact name, path, declared profiles, and supply
image remain bound. The verified `CurveFamily` is the composition boundary for
that state.
