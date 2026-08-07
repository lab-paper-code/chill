# RKNN MobileNet V2 0.50 batch-saturation campaign v2

> Navigation: [Campaign records](../README.md)
>
> Method: [Plan 5](../../plans/05-batch-saturation-campaign.md)

## Status

Completed on the live testbed on 2026-08-06. The Git commit that first contains
this directory is the discovery freeze boundary, and the commit that first
contains `confirmatory-policy.json` predates all confirmation Runs. All 20
matching idle, discovery, and confirmation Runs were retained, Loader-verified,
admitted, and included. The final result is
`CandidateOnly: DomainNoImprovementNotConfirmed`; this campaign makes no
accepted `b_sat` claim.

## Why this is a new campaign

Campaign v1 correctly closed as
`Unavailable: ConfirmatoryBudgetInsufficient`; changing its cap after seeing
discovery would rewrite the pre-registration. This record starts a disjoint
campaign. It uses v1 only as a pilot for the Run budget and pools none of v1's
idle or active evidence into a v2 point or decision.

Using v1's retained sufficient statistics, a read-only sensitivity calculation
kept the implemented adaptive sizing rule and its global selection family. A
fixed `H=2` calculation first met the 5% precision target at four trials, but it
does not apply because this policy lets discovery select a trial count. Under
that actual rule, cap six is the first feasible cap: `H=8`, `n_conf=6`, projected
reference lower bound `0.0087128936 J/item`, projected maximum relative
half-width `0.0473297600`, and worst-case total active Runs `18`. These values
are a pilot-based sizing prior, not a v2 result or guarantee that new evidence
will have the same variance.

## Frozen inputs

- family contract: [the immutable two-member RKNN contract](../rknn-mobilenet-v2-050-sequential-v1/curve-family-contract.json)
- discovery policy: [discovery-policy.json](discovery-policy.json)
- post-discovery policy: [confirmatory-policy.json](confirmatory-policy.json)
- family name: `rknn-mobilenet-v2-050-sequential-v1`
- discovery-policy name: `rknn-mobilenet-v2-050-bsat-v2`
- eligible member batches: `1, 2`; there is no eligible batch-3 artifact
- `Kmax=2`, `tau=0.05`, `n_disc=3`, `maximumConfirmatoryTrials=6`, and
  `maximumActiveRunBudget=18`
- the `idle-baseline` and `incremental-point` stages each require three
  independent admitted Runs
- six fresh model-loaded idle Runs remain outside the active-Run budget

The tolerance, cap, and budget are fixed before every v2 Run. They are not a
retroactive amendment to v1. This campaign can establish saturation only inside
batches `1..2` and makes no claim about an unbuilt batch 3 or any larger batch.

## Inclusion rule

Include every Run submitted after this freeze boundary whose name starts with
`orangepi-rknnlite-bsat-v2-`, whose retained intent verifies against the linked
family contract and selected mode policy, and whose evidence is admitted.
Exclude no identity-compatible admitted Run from that prefix. A rejected,
insufficient, cancelled, or transport-failed attempt remains listed but
contributes no trial. Do not replace or silently omit it under the same ordinal.

Run sequentially and fail-stop on any non-`Accepted` verdict:

1. batch 1 and batch 2 model-loaded idle, three fresh Runs per exact state;
2. discovery active batches 1 and 2, three fresh Runs per batch;
3. Loader verification, idle-baseline and discovery derivation, and sizing;
4. commit the canonical confirmatory policy that records this policy name,
   candidate, and realized sizing outcome; and
5. only when sizing returns `ConfirmatoryTrialsSized`, run exactly the frozen
   allocation and derive the final result.

Discovery and confirmatory active Run sets must be disjoint. The fresh v2
exact-state idle baselines are shared by its two passes. Any realized-family or
owner-bound environment drift ends v2; it does not authorize pooling v1 or
creating a replacement under an omitted ordinal.

## Frozen Run-name grammar

```text
orangepi-rknnlite-bsat-v2-idle-bs{1|2}-i{1|2|3}
orangepi-rknnlite-bsat-v2-discovery-bs{1|2}-d{1|2|3}
orangepi-rknnlite-bsat-v2-confirm-bs{1|2}-c{1|2|3|4|5|6}
```

The confirmatory grammar reserves the maximum domain but authorizes no
confirmatory Run until the second policy artifact fixes the realized subset and
count.

## Evidence access

The Run-name prefix and inclusion rule above select the retained
`ProfilingRun` objects. Per-Run execution, verification, admission, and
measurement values remain in Run status and the referenced evidence object;
use `kubectl chill runs` and `kubectl chill runs show NAME` instead of
copying them into this record. The Status section keeps only the campaign-level
terminal outcome.
