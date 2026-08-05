# RKNN MobileNet V2 0.50 batch-saturation campaign v1

> Navigation: [Campaign records](../README.md)
>
> Method: [Plan 5](../../plans/05-batch-saturation-campaign.md)

## Status

Discovery closed on the live testbed on 2026-08-06. The Git commit that first
contains this directory is the discovery freeze boundary. All 12 matching idle
and discovery Runs were retained, Loader-verified, admitted, and included. The
frozen cap could not meet the confirmatory precision rule, so the post-discovery
policy records `Unavailable: ConfirmatoryBudgetInsufficient` and allocates no
confirmatory Runs. This is a completed unavailable outcome, not an accepted
`b_sat` claim.

## Frozen inputs

- family contract: [the immutable two-member RKNN contract](../rknn-mobilenet-v2-050-sequential-v1/curve-family-contract.json)
- discovery policy: [discovery-policy.json](discovery-policy.json)
- canonical family-contract identity:
  `chill.dacs.io/curve-family-contract.v1alpha1@sha256:447c326bbdff27a3e6d750c70af714db739d27138c790c1ee9dbc5ffce4565d2`
- canonical discovery-policy digest:
  `sha256:548bd2b91da51bdb22b5a5a126b2df13e0fc7b150535d572ab19d01d13248631`
- committed discovery-policy file digest:
  `sha256:52ac345699534c706575c9bbbc5aab601d478f2f95ac2c0bed58b47bda91b4ee`
- eligible member batches: `1, 2`; there is no eligible batch-3 artifact
- `Kmax=2`, `tau=0.05`, `n_disc=3`, `maximumConfirmatoryTrials=3`, and
  `maximumActiveRunBudget=12`
- the `idle-baseline` and `incremental-point` stages each require three
  independent admitted Runs
- model-loaded idle Runs do not count against the active-Run budget

The 5% tolerance is fixed before every Run in this campaign. It is the same
operational margin used by the earlier sequential pilot, whose measurements are
not pooled into either pass here. Fixing `maximumConfirmatoryTrials=3` removes
trial-count selection: for `Kmax=2`, the global statement family is the one
positivity and one contrast statement at candidate batch 1 (`H=2`). If the
three-Run confirmation cannot meet the precision rule, the frozen outcome is
`Unavailable: ConfirmatoryBudgetInsufficient`; the tolerance and cap are not
changed after observing discovery.

This campaign can establish saturation only inside batches `1..2`. It makes no
claim about an unbuilt batch 3 or any larger batch and is not, by itself, a wide
batch-sweep figure.

## Inclusion rule

Include every Run submitted after this freeze boundary whose name starts with
`orangepi-rknnlite-bsat-v1-`, whose retained intent verifies against the linked
family contract and selected mode policy, and whose evidence is admitted.
Exclude no identity-compatible admitted Run from that prefix. A rejected,
insufficient, cancelled, or transport-failed attempt remains listed but
contributes no trial. Do not replace or silently omit it under the same ordinal.

Run sequentially and fail-stop on any non-`Accepted` verdict:

1. batch 1 and batch 2 model-loaded idle, three Runs per exact state;
2. discovery active batches 1 and 2, three Runs per batch;
3. Loader verification, idle-baseline and discovery derivation, and sizing;
4. commit the canonical confirmatory policy that records the discovery digest,
   candidate, and realized sizing outcome; and
5. only when sizing returns `ConfirmatoryTrialsSized`, run the frozen
   confirmatory allocation and derive the final result.

Discovery and confirmatory active Run sets must be disjoint. The exact-state
idle baselines are intentionally shared by both passes. A change in realized
family or owner-bound environment identity ends this campaign; it does not
authorize pooling across the drift.

## Frozen Run-name grammar

```text
orangepi-rknnlite-bsat-v1-idle-bs{1|2}-i{1|2|3}
orangepi-rknnlite-bsat-v1-discovery-bs{1|2}-d{1|2|3}
orangepi-rknnlite-bsat-v1-confirm-bs{1|2}-c{1|2|3}
```

The confirmatory grammar is reserved but does not authorize those Runs until
the second policy artifact is committed.

## Results

The production retained-evidence Loader re-opened every final and raw evidence
object through its owned Job binding. A local read-only diagnostic used a
transport-only port forward to that same retained store, verified object
identity and schema, reproduced every `Accepted` verdict from each immutable
Run policy, and then called the implemented family, baseline, curve, discovery,
sizing, and final-result boundaries. No campaign controller, API object, or
derived profile was created.

### Included idle Runs

| Member | Run | Run UID | Pod-derived Trial ID | Intent digest | Final evidence digest |
|---|---|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v1-idle-bs1-i1` | `9f65835f-58a7-4f28-ada4-52e124d2a037` | `4b2237eb-dfb4-417b-a31d-36fea6f0d2bd` | `sha256:748562fab88c3e2f50cca9c3afeb02dd1d57dd4921e5c3b089392ad21013ddb8` | `sha256:845fda67c5363071bbd29f28f7bbaff6b1ba8305dc5baaeac0b0162e2a8e3313` |
| batch 1 | `orangepi-rknnlite-bsat-v1-idle-bs1-i2` | `fa233e1e-4c57-48aa-b238-7321320b441a` | `e3749339-373f-4dd0-b200-980d8c9c04f5` | `sha256:b7e1516a6cc782272dbf29c570230f57dbec50639127307e66898e86fb9502ae` | `sha256:e45bc3db343be8613c56b253e193670cd1a39f6ffb730d8036d7601b456f918c` |
| batch 1 | `orangepi-rknnlite-bsat-v1-idle-bs1-i3` | `357fd111-8816-4f6a-88d8-51ee48786b49` | `26887676-ce71-4d0b-9bdd-886f0c26c259` | `sha256:b7e1516a6cc782272dbf29c570230f57dbec50639127307e66898e86fb9502ae` | `sha256:0594f81e71b025ea32d4bad4b97cc3934495a4089c67a822afac709b15b5cf96` |
| batch 2 | `orangepi-rknnlite-bsat-v1-idle-bs2-i1` | `dfbada7a-ae68-4fee-9b73-70ef4a25877c` | `82619820-df7e-442c-90dd-04e0ef9c9f5a` | `sha256:9937af84a27bb6b3a239e556d5bf4d8afbb4df8898a5793cbee3a6d5332b0d0e` | `sha256:869aaf87130576eddc900ec7895b6bf8fbf28cc96febab585ec2d2e17f632d64` |
| batch 2 | `orangepi-rknnlite-bsat-v1-idle-bs2-i2` | `01f3cc95-37d0-4571-ad2a-bd07c504fe44` | `53fee7d9-55ad-423c-8cc1-7d7735d6a2dc` | `sha256:cdf0f5d54e6ccddcc62cf0a2986f75dffefafad668037109763d834a6b27083b` | `sha256:86b55c3ba46bffcf814211dba76a295dfab45f194b8e8a103ed0ab734945fa76` |
| batch 2 | `orangepi-rknnlite-bsat-v1-idle-bs2-i3` | `07ab900c-516a-4ea7-9a97-748cb9511875` | `3fe18fe8-c156-4f21-bde0-fb56e1813621` | `sha256:cdf0f5d54e6ccddcc62cf0a2986f75dffefafad668037109763d834a6b27083b` | `sha256:1cb8aae318f89b8cdab07cccfe0a413f0a8ee32b92b92d88d47f71de69900214` |

### Included discovery Runs

| Member | Run | Run UID | Pod-derived Trial ID | Intent digest | Final evidence digest |
|---|---|---|---|---|---|
| batch 1 | `orangepi-rknnlite-bsat-v1-discovery-bs1-d1` | `f54d7f8b-fee0-4194-ba63-f9787eab3eab` | `701a6146-3903-4f67-ae40-5f2dfda68308` | `sha256:d0d4b1d89b1a21c56e0124613fffcc7ca33db8b114495b78153add7cee707872` | `sha256:772bb1839057a5dbd81a97dc23d7ca20d6169b323f47d2e331e942fac174c26d` |
| batch 1 | `orangepi-rknnlite-bsat-v1-discovery-bs1-d2` | `f9a34576-7484-4df6-b15e-6dc740e66bb1` | `687a2e79-1071-4a8e-8ba9-6c778d8d1a8e` | `sha256:d0d4b1d89b1a21c56e0124613fffcc7ca33db8b114495b78153add7cee707872` | `sha256:2a9e1fc6dfe8daf081611b7caa0f445eb4c66657064164f2322aa49e27c76a3b` |
| batch 1 | `orangepi-rknnlite-bsat-v1-discovery-bs1-d3` | `414ce550-36a0-47a9-a09a-e29bd4e1af93` | `bafc4edc-0587-471a-b8ca-9579f1823ae3` | `sha256:fffea44ba70a9fc7a9d22c23a7fac4fbb6a88938b03d3050bc88c635c3a14352` | `sha256:95d92a49bc6a141987db456ca07154bb4641bab68ac656b9f1a3c0cfede49fea` |
| batch 2 | `orangepi-rknnlite-bsat-v1-discovery-bs2-d1` | `1b23a851-641b-45f2-87ee-9ba1b3e9d607` | `27116cd9-7f59-4608-a313-84426650e230` | `sha256:df9cbe56e2b99f19d302e036c814aaad8322c76886731a0931b66505cfb0443e` | `sha256:2039fb147a2cb173d8dc0e45230959aa0fdc25b5f8fe58319f70ad89d86927ed` |
| batch 2 | `orangepi-rknnlite-bsat-v1-discovery-bs2-d2` | `f38b9775-c707-43fe-8b32-34ee84d7f5c8` | `4c4f26fa-a86c-4b79-9de2-8e52ade5af88` | `sha256:df9cbe56e2b99f19d302e036c814aaad8322c76886731a0931b66505cfb0443e` | `sha256:2bd8ac09bfef1bd918df31523b3c2ddcec57755247427108efd881eff45e8eaf` |
| batch 2 | `orangepi-rknnlite-bsat-v1-discovery-bs2-d3` | `83772594-d1da-42b4-ad71-7b8b81e5da82` | `902e947b-f7b8-4556-ac5e-18094fc5225e` | `sha256:df9cbe56e2b99f19d302e036c814aaad8322c76886731a0931b66505cfb0443e` | `sha256:340a4459342817a4528207739238a2061dcf5f185fa954dc39a7e57d365a8581` |

Every idle Run used admission policy
`sha256:958500162bcb71bccf2e92245c13651f74d570d79eae37b882e3577b8093e8ff`;
every active Run used
`sha256:9070c3923cebd39de730ba10d78ab83e2657c1249cd98985807f6e4412590fbb`.
The differing intent digests above reflect retained source snapshots; Loader
projection established the common scientific identities below rather than
assuming digest equality.

### Realized family and discovery points

- realized family identity:
  `chill.dacs.io/curve-family-identity.v1alpha1@sha256:5fa8ac8447016f259bf75e68549e73bcf8fa6974a958eaf28d1b126fe99cd60a`
- owner-bound environment identity:
  `chill.dacs.io/external-environment-identity.v1alpha1@sha256:1e462e5d2549ac2be0a98c2e74e8a8d07f9365766280cf052cc840227cb2ff31`

| Batch | Execution-state identity | Model-loaded idle baseline | Incremental energy | Active p99 |
|---:|---|---:|---:|---:|
| 1 | `chill.dacs.io/execution-state-identity.v1alpha2@sha256:0d0171da01907443a7aeef07380036291aa971e7c10abf4e522c8f89544977fe` | `2.7649485283 +/- 0.0161667034 W` | `0.00886307274 +/- 0.00022904371 J/item` | `4.135488 ms` |
| 2 | `chill.dacs.io/execution-state-identity.v1alpha2@sha256:941106399af8e860fd16b766f24166284172584442034d29e565e69419800501` | `2.7582822241 +/- 0.0180075745 W` | `0.00878000907 +/- 0.00067706196 J/item` | `8.025105 ms` |

The intervals are the current 95% `incremental-point-welch-satterthwaite-v2`
construction. Discovery estimated the largest remaining improvement above batch
one as `0.0093718814` (0.93718814%), below the frozen 5% tolerance, and selected
batch one as a candidate. Discovery makes no confidence claim.

### Post-discovery policy and terminal result

- confirmatory policy: [confirmatory-policy.json](confirmatory-policy.json)
- canonical confirmatory-policy digest:
  `sha256:7a914a05f41c62781fad14323f96614c32b92e1d3d6d3bf29f5dcce48ad062ee`
- committed confirmatory-policy file digest:
  `sha256:28113557c6289a0d0871cdc162aaeb73a767481744bbc88687c56d44534ee809`
- sizing method: `bonferroni-welch-relative-precision-v1`
- global statement count: `H=2`; one-sided quantile: `0.975`
- maximum feasible confirmatory trials: `3` at both batches
- allocated confirmatory Runs: none

At every allowed trial count, the projected simultaneous precision exceeded the
frozen 5% rule. The canonical second policy therefore records no Run allocation,
and the typed terminal result is:

```text
Status                         = Unavailable
Code                           = ConfirmatoryBudgetInsufficient
DiscoverySelectedBatch         = 1
DomainNoImprovementConfirmed   = false
MinimalityConfidenceConfirmed  = false
ConfirmatoryMeasuredBatches    = []
```

No confirmatory Run was submitted. Raising the cap now would define a new
campaign rather than repair this result.
