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
- canonical family-contract identity:
  `chill.dacs.io/curve-family-contract.v1alpha1@sha256:447c326bbdff27a3e6d750c70af714db739d27138c790c1ee9dbc5ffce4565d2`
- canonical discovery-policy digest:
  `sha256:965d093a62b3ca2134da859a50c4566e4e0d5a465ea86b48dc626c6863b987f0`
- committed discovery-policy file digest:
  `sha256:785dc92638fccb7122d43590d9d4bb3dfddf1b385673b5d2cc0145ccb701d795`
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
4. commit the canonical confirmatory policy that records this policy digest,
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

| Member | Run | Run UID | Pod-derived Trial ID | Intent digest | Final evidence digest |
|---|---|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v2-idle-bs1-i1` | `390c214c-73c6-4528-9540-2cdf11eb8adb` | `0c5c7a60-d622-4659-9ea4-c28dca76da3c` | `sha256:23ab54ba561be80bd4debc1cf8a47fa57bf6527328efbe20b7b8d2acf09c1096` | `sha256:a22ced0fddb4c3481df569d6b029d11bf5d5e0fcec4927fcb24cdc815cf95f07` |
| batch 1 | `orangepi-rknnlite-bsat-v2-idle-bs1-i2` | `d9f0d8be-fd6f-4a2e-934b-1874a1d93292` | `6354c0d1-a631-4374-97bb-f4bf00a45b8f` | `sha256:6cdfd10920e4b9fbff1814074d0e98514d3a7d3c5038f62c2c730106bd291bda` | `sha256:409f055c0f1b417784f16a934ffd6819d872435a04847912bfd5296284d5228e` |
| batch 1 | `orangepi-rknnlite-bsat-v2-idle-bs1-i3` | `2cd89473-b7e7-457a-8b8c-975cde4c1e78` | `debdabe4-c619-49ad-8eb3-800a9c0b458e` | `sha256:6cdfd10920e4b9fbff1814074d0e98514d3a7d3c5038f62c2c730106bd291bda` | `sha256:7543d1796e67d6f2b92706d7c69dc7e728409f05a124cf4452998c7b45ad40e9` |
| batch 2 | `orangepi-rknnlite-bsat-v2-idle-bs2-i1` | `e4c964c7-07e3-45c7-a721-11f132c8be30` | `78203b83-a2d5-4017-930b-26d9c5bdba95` | `sha256:284cc3b3e9ccad26c02a67542231d0f0dca22991827cac62d3231a8f7a0c3ae0` | `sha256:304f601620e5df7012a0366368faf0b2fbac53129273612e276fd06638742713` |
| batch 2 | `orangepi-rknnlite-bsat-v2-idle-bs2-i2` | `e6d38564-6416-412d-a377-8e1c3209905a` | `0d3d1be5-d299-4afd-9ae9-15cad870096d` | `sha256:eb0e6ae935fb276c150a2510fe9d378ba6482bafeb7e76fa6613053dd5711fd2` | `sha256:717f014857138c8cd7e8b559c30b024100a9bfd32e72cd4e44ad6c4253a4c868` |
| batch 2 | `orangepi-rknnlite-bsat-v2-idle-bs2-i3` | `10046bc4-41b7-437b-be21-30a68dcda3c3` | `8c16a7ea-103d-46ed-9435-30b5c26e4296` | `sha256:eb0e6ae935fb276c150a2510fe9d378ba6482bafeb7e76fa6613053dd5711fd2` | `sha256:5d7d8dae514a8894b7e9930f9c382c3293397330dcf5899b4df75d6bc3cf89e3` |

### Included discovery Runs

| Member | Run | Run UID | Pod-derived Trial ID | Intent digest | Final evidence digest |
|---|---|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v2-discovery-bs1-d1` | `1abaad90-f27f-41cc-a118-39e87eb97166` | `873ea76f-b87b-44d8-9829-5e3fb1e7b381` | `sha256:a7dd3c05f5c67b385c2bb33d4ce0d7bb3b3970038ac6690f5fbba1b63355e3c1` | `sha256:b32a95ad622dde39c3ffa00ad52d317f47f40b3539bb8721ef258f0b38d5fa15` |
| batch 1 | `orangepi-rknnlite-bsat-v2-discovery-bs1-d2` | `15f8639b-d691-41f9-9726-9b86a5fc713a` | `fe5d6a50-e903-498f-8b65-4f69ce95ccf5` | `sha256:a7dd3c05f5c67b385c2bb33d4ce0d7bb3b3970038ac6690f5fbba1b63355e3c1` | `sha256:a3d5eb798f90ffc846af0b833d38f6460987ab3751b7c87b43816114aee88a07` |
| batch 1 | `orangepi-rknnlite-bsat-v2-discovery-bs1-d3` | `1ccb1579-895d-4171-8bc9-59362adfb47a` | `43a74a06-62de-493a-b9d6-3d43e700c00e` | `sha256:a7dd3c05f5c67b385c2bb33d4ce0d7bb3b3970038ac6690f5fbba1b63355e3c1` | `sha256:6567418163a59c66705797e4592e134d0c51ef1d1ef95d0cfbc3f66d199376c4` |
| batch 2 | `orangepi-rknnlite-bsat-v2-discovery-bs2-d1` | `f7a19ef9-ab77-4a74-b58e-cb6ce4f3aba2` | `bb0f10e3-5df8-4d26-917f-1dc9d9860068` | `sha256:8317b0015524ec57e5502ef35d909729f7759310b80bf908c8d3526ffb95c0a3` | `sha256:447e7d29ddc14a01b63430c5c3128c08bd50c86ccbc76e2609439267f4b933ad` |
| batch 2 | `orangepi-rknnlite-bsat-v2-discovery-bs2-d2` | `dd0e358b-c3d3-4d18-8e68-1b3bfef0d48b` | `60e0ffa3-d531-411e-9354-971eba3e86d8` | `sha256:8317b0015524ec57e5502ef35d909729f7759310b80bf908c8d3526ffb95c0a3` | `sha256:b6cdedacc1d5ece50cd8301c09d9a5b97e2b4b515798f3722ed4f304e3b85cb4` |
| batch 2 | `orangepi-rknnlite-bsat-v2-discovery-bs2-d3` | `edcb31cf-ee86-4cae-bdb5-bc1d443b12d8` | `34324180-ce6a-448d-a9d2-bac88ac85392` | `sha256:8317b0015524ec57e5502ef35d909729f7759310b80bf908c8d3526ffb95c0a3` | `sha256:e95299eea4f39dd49e4dd372d627db2351ddb62123769777282ea8865b02b26d` |

Every idle Run used admission policy
`sha256:958500162bcb71bccf2e92245c13651f74d570d79eae37b882e3577b8093e8ff`;
every active Run used
`sha256:9070c3923cebd39de730ba10d78ab83e2657c1249cd98985807f6e4412590fbb`.

### Realized family and discovery points

- realized family identity:
  `chill.dacs.io/curve-family-identity.v1alpha1@sha256:5fa8ac8447016f259bf75e68549e73bcf8fa6974a958eaf28d1b126fe99cd60a`
- owner-bound environment identity:
  `chill.dacs.io/external-environment-identity.v1alpha1@sha256:1e462e5d2549ac2be0a98c2e74e8a8d07f9365766280cf052cc840227cb2ff31`

| Batch | Execution-state identity | Model-loaded idle baseline | Incremental energy | Active p99 |
|---:|---|---:|---:|---:|
| 1 | `chill.dacs.io/execution-state-identity.v1alpha2@sha256:0d0171da01907443a7aeef07380036291aa971e7c10abf4e522c8f89544977fe` | `2.7599619241 +/- 0.0437986902 W` | `0.00907538872 +/- 0.00015848267 J/item` | `4.204029 ms` |
| 2 | `chill.dacs.io/execution-state-identity.v1alpha2@sha256:941106399af8e860fd16b766f24166284172584442034d29e565e69419800501` | `2.7544370886 +/- 0.0153458624 W` | `0.00877422534 +/- 0.00037302391 J/item` | `7.933522 ms` |

The intervals are the current 95% `incremental-point-welch-satterthwaite-v2`
construction. Discovery estimated the largest remaining improvement above batch
one as `0.0331846266` (3.31846266%), below the frozen 5% tolerance, and selected
batch one as a candidate. Discovery itself makes no confidence claim.

### Frozen confirmatory allocation

- confirmatory policy: [confirmatory-policy.json](confirmatory-policy.json)
- canonical confirmatory-policy digest:
  `sha256:0ce98350240620ed5b4202e30ce499688e28f7d749b9ecc3c2b3e54d04f34ebc`
- committed confirmatory-policy file digest:
  `sha256:ad9a45bc2f9bfa4d42b5415c43a6ea3fcfb84b863ae316ae06ae3e38a28af260`
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

| Member | Run | Run UID | Pod-derived Trial ID | Intent digest | Final evidence digest |
|---|---|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c1` | `790b15bd-1b60-4ccf-8aea-fcd9e13072b7` | `b6dd15b0-431e-4aec-8213-f80c288002f9` | `sha256:2beb9b2e0faeee41c675fe027bcb3f2fb72db996968e085fb94d8af92ac40450` | `sha256:c7c4635ea0c8d75bb75fe9ed55eb2a8480f0b34b281d759495d62ba9a3dbce73` |
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c2` | `13701296-f555-4222-9890-02f6aa4ea444` | `ebaa045d-2467-47a8-bfae-19a4430a747b` | `sha256:66ce6dce2345490b9bfce9a5c4bc47c9f5f400fb0d719c56f911aa82a6b30141` | `sha256:302f8e9780fc4f42160fb9b47edc11183e1d4f828fbf2254f3260d7872739488` |
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c3` | `70dae83b-6189-40ab-af15-7cf793d45cbe` | `67d37009-796e-4673-92cd-644b64c0b372` | `sha256:66ce6dce2345490b9bfce9a5c4bc47c9f5f400fb0d719c56f911aa82a6b30141` | `sha256:7e5b9ac02fb5b239704bb75ff78e552c5f4871ceb76a8c66e004d52b30514eb2` |
| batch 1 | `orangepi-rknnlite-bsat-v2-confirm-bs1-c4` | `cafff32b-6876-4069-9ada-46c96c70d6eb` | `13cbc22e-6e53-4845-8f58-e284a6dbf08d` | `sha256:66ce6dce2345490b9bfce9a5c4bc47c9f5f400fb0d719c56f911aa82a6b30141` | `sha256:69e3e1c06b8fa252777bf963edc7a5508a28402b9ee318f7f460c664ba7c29b7` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c1` | `ecc61335-10f0-4096-a9bc-8eb3bf6ad312` | `20043adb-7151-41e1-9fa3-e6c163f0db31` | `sha256:53743fee42db288fd7ac24a4333dd2e5c33dbc4992766e7ac19991aadc66ff5b` | `sha256:b20d70a6c86521cd89e9a08a98187af9a735c37a1fb01681573db18937c40e91` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c2` | `1bbdf78d-cd06-4c18-a0a6-9e4d0014d99f` | `4badbae9-562c-4337-bfc4-a3e6e204a0b8` | `sha256:53743fee42db288fd7ac24a4333dd2e5c33dbc4992766e7ac19991aadc66ff5b` | `sha256:62cd1f5504bb2c271d49224f16b98625fc5f6af177f2f35f5723ae9036f7a8c0` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c3` | `a889cc0b-844c-4eba-9a03-ed5feb59f832` | `36aab16f-399b-4c0f-9be4-f4db6c09aa5d` | `sha256:53743fee42db288fd7ac24a4333dd2e5c33dbc4992766e7ac19991aadc66ff5b` | `sha256:f9fe5ff24f9b366211c5444446f463148c0e3b5f507ee91f7a5cc0996131cfc5` |
| batch 2 | `orangepi-rknnlite-bsat-v2-confirm-bs2-c4` | `2830e453-9617-4c8c-9b34-76cc4ae67f93` | `6be651f4-980a-46be-964e-4da015424fbb` | `sha256:b09081a48168515b1bd08b5848ba8c164f37c80b703edf8845db275c52dc0665` | `sha256:dcf97d0c82b2b9eb84d1db0aea3d10d047691b82ded1b1623408e57b769f2de1` |

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
policy digests. Batch one's reference lower bound was positive at
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
