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

## Results

The production retained-evidence Loader re-opened every final and raw evidence
object through its owned Job binding. A local read-only diagnostic used a
transport-only port forward to that same retained store, verified object
identity and schema, reproduced all 12 `Accepted` verdicts from the immutable
Run policies, and called the implemented family, baseline, curve, discovery,
and sizing boundaries. No campaign controller, API object, or derived profile
was created.

### Included idle Runs

| Member | Run | Run UID | Pod-derived Trial ID |
|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v2-idle-bs1-i1` | `390c214c-73c6-4528-9540-2cdf11eb8adb` | `0c5c7a60-d622-4659-9ea4-c28dca76da3c` |
| batch 1 | `orangepi-rknnlite-bsat-v2-idle-bs1-i2` | `d9f0d8be-fd6f-4a2e-934b-1874a1d93292` | `6354c0d1-a631-4374-97bb-f4bf00a45b8f` |
| batch 1 | `orangepi-rknnlite-bsat-v2-idle-bs1-i3` | `2cd89473-b7e7-457a-8b8c-975cde4c1e78` | `debdabe4-c619-49ad-8eb3-800a9c0b458e` |
| batch 2 | `orangepi-rknnlite-bsat-v2-idle-bs2-i1` | `e4c964c7-07e3-45c7-a721-11f132c8be30` | `78203b83-a2d5-4017-930b-26d9c5bdba95` |
| batch 2 | `orangepi-rknnlite-bsat-v2-idle-bs2-i2` | `e6d38564-6416-412d-a377-8e1c3209905a` | `0d3d1be5-d299-4afd-9ae9-15cad870096d` |
| batch 2 | `orangepi-rknnlite-bsat-v2-idle-bs2-i3` | `10046bc4-41b7-437b-be21-30a68dcda3c3` | `8c16a7ea-103d-46ed-9435-30b5c26e4296` |

### Included discovery Runs

| Member | Run | Run UID | Pod-derived Trial ID |
|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v2-discovery-bs1-d1` | `1abaad90-f27f-41cc-a118-39e87eb97166` | `873ea76f-b87b-44d8-9829-5e3fb1e7b381` |
| batch 1 | `orangepi-rknnlite-bsat-v2-discovery-bs1-d2` | `15f8639b-d691-41f9-9726-9b86a5fc713a` | `fe5d6a50-e903-498f-8b65-4f69ce95ccf5` |
| batch 1 | `orangepi-rknnlite-bsat-v2-discovery-bs1-d3` | `1ccb1579-895d-4171-8bc9-59362adfb47a` | `43a74a06-62de-493a-b9d6-3d43e700c00e` |
| batch 2 | `orangepi-rknnlite-bsat-v2-discovery-bs2-d1` | `f7a19ef9-ab77-4a74-b58e-cb6ce4f3aba2` | `bb0f10e3-5df8-4d26-917f-1dc9d9860068` |
| batch 2 | `orangepi-rknnlite-bsat-v2-discovery-bs2-d2` | `dd0e358b-c3d3-4d18-8e68-1b3bfef0d48b` | `60e0ffa3-d531-411e-9354-971eba3e86d8` |
| batch 2 | `orangepi-rknnlite-bsat-v2-discovery-bs2-d3` | `edcb31cf-ee86-4cae-bdb5-bc1d443b12d8` | `34324180-ce6a-448d-a9d2-bac88ac85392` |

Every idle Run used `idle-wall-power-v1`; every active Run used
`wall-power-v2`.

### Verified family and discovery points

- curve-family name: `rknn-mobilenet-v2-050-sequential-v1`
- owner-bound environment: shared by every included Run

| Batch | Model-loaded idle baseline | Incremental energy | Active p99 |
|---:|---:|---:|---:|
| 1 | `2.7599619241 +/- 0.0437986902 W` | `0.00907538872 +/- 0.00015848267 J/item` | `4.204029 ms` |
| 2 | `2.7544370886 +/- 0.0153458624 W` | `0.00877422534 +/- 0.00037302391 J/item` | `7.933522 ms` |

The intervals are the current 95% `incremental-point-welch-satterthwaite-v2`
construction. Discovery estimated the largest remaining improvement above batch
one as `0.0331846266` (3.31846266%), below the frozen 5% tolerance, and selected
batch one as a candidate. Discovery itself makes no confidence claim.

### Frozen confirmatory allocation

- confirmatory policy: [confirmatory-policy.json](confirmatory-policy.json)
- confirmatory-policy name: `rknn-mobilenet-v2-050-bsat-v2`
- sizing method: `bonferroni-welch-relative-precision-v1`
- global statement count: `H=8`; one-sided quantile: `0.99375`
- selected confirmatory trials: `4` at each of batches `1, 2`
- projected reference lower bound: `0.0087535347 J/item`
- projected maximum relative half-width: `0.0401913879`
- projected total active Runs: `14` (`6` discovery plus `8` confirmation)

The authorized confirmatory Run set is exactly:

```text
orangepi-rknnlite-bsat-v2-confirm-bs1-c1..c4
orangepi-rknnlite-bsat-v2-confirm-bs2-c1..c4
```

Runs `c5` and `c6` remain inside the pre-frozen cap but are not in the realized
allocation and were not created.

### Included confirmatory Runs

| Member | Run | Run UID | Pod-derived Trial ID |
|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c1` | `790b15bd-1b60-4ccf-8aea-fcd9e13072b7` | `b6dd15b0-431e-4aec-8213-f80c288002f9` |
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c2` | `13701296-f555-4222-9890-02f6aa4ea444` | `ebaa045d-2467-47a8-bfae-19a4430a747b` |
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c3` | `70dae83b-6189-40ab-af15-7cf793d45cbe` | `67d37009-796e-4673-92cd-644b64c0b372` |
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c4` | `cafff32b-6876-4069-9ada-46c96c70d6eb` | `13cbc22e-6e53-4845-8f58-e284a6dbf08d` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c1` | `ecc61335-10f0-4096-a9bc-8eb3bf6ad312` | `20043adb-7151-41e1-9fa3-e6c163f0db31` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c2` | `1bbdf78d-cd06-4c18-a0a6-9e4d0014d99f` | `4badbae9-562c-4337-bfc4-a3e6e204a0b8` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c3` | `a889cc0b-844c-4eba-9a03-ed5feb59f832` | `36aab16f-399b-4c0f-9be4-f4db6c09aa5d` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c4` | `2830e453-9617-4c8c-9b34-76cc4ae67f93` | `6be651f4-980a-46be-964e-4da015424fbb` |

Loader projection verified every retained idle and active intent against the
same realized family and owner-bound environment. `DeriveCurve` also verified
that the six discovery Trial IDs and eight confirmatory Trial IDs are disjoint.
The exact-state idle baselines above were intentionally shared across passes.

### Confirmatory points and terminal result

| Batch | Confirmatory incremental energy | Active p99 |
|---:|---:|---:|
| 1 | `0.00904314283 +/- 0.00019228681 J/item` | `4.183610 ms` |
| 2 | `0.00877071789 +/- 0.00015005883 J/item` | `7.988360 ms` |

The final joint estimator used `H=8`, one-sided quantile `0.99375`, and both
policy names. Batch one's reference lower bound was positive at
`0.00875444087 J/item`. The batch-one minus batch-two difference estimate was
`0.00027242494 J/item`; its upper bound was `0.00055987716 J/item`, or
`0.0639535032` relative to the positive reference lower bound. Because 6.395%
exceeds the frozen 5% tolerance, the result is:

```text
Status                              = CandidateOnly
Code                                = DomainNoImprovementNotConfirmed
DiscoverySelectedBatch              = 1
DomainNoImprovementConfirmed        = false
MinimalityConfidenceConfirmed       = false
DiscoveryMeasuredBatches            = [1, 2]
ConfirmatoryMeasuredBatches         = [1, 2]
MaximumRemainingImprovementUpperBound = 0.0639535032
```

This does not establish that batch two is materially better; it establishes
that this campaign could not bound every larger measured improvement below 5%.
Adding `c5` or `c6` after this result would violate the frozen allocation rather
than complete it.
