# RKNN MobileNet V2 0.50 sequential campaign v1

> Navigation: [Campaign records](../README.md)
>
> Method: [Plan 5a](../../plans/05a-sequential-batch-stop.md)

## Status

Completed on the live testbed on 2026-08-05. The Git commit that first contains
this directory is the freeze boundary. All 12 post-freeze Runs matching the
frozen prefix were retained, Loader-verified, admitted, and included; the
earlier `orangepi-rknnlite-family-v1-*` diagnostic Runs remain excluded because
they predate it.

## Frozen inputs

- family contract: [curve-family-contract.json](curve-family-contract.json)
- sequential policy: [sequential-policy.json](sequential-policy.json)
- point policy: [point-policy.json](point-policy.json)
- canonical family-contract identity:
  `chill.dacs.io/curve-family-contract.v1alpha1@sha256:447c326bbdff27a3e6d750c70af714db739d27138c790c1ee9dbc5ffce4565d2`
- committed file digests: family contract
  `sha256:8a2c5d6b4ce4e046152dbf67a67c3e16235e1f7ba44d3acf6ac95d8ba0344892`;
  sequential policy
  `sha256:393cf6d880541e542f5182c6f0b2c395d99d050adfb725dd6e2c84d4e06a1267`;
  point policy
  `sha256:a7a1f2eeb3d0190f91bde81dec32ebf2b4d4753defff265a6126051d048e1aed`
- eligible member batches: `1, 2`
- point policy `rknn-mobilenet-v2-050-point-min3-v1`: minimum 3 independent
  admitted Runs per active point and per exact-state idle baseline
- capability rule: the absence of an eligible batch-3 member is reported as
  `CapabilityBoundary` after batch 2; it is not a non-improving point
- idle baselines count outside the active-Run count, but all baseline evidence
  is reported in the result

`minDelta=0.05`, `patience=2`, and three trials use the approved Plan 5a worked
rule and the existing minimum independent-trial construction. A value more than
`1.5x` the running best is the pre-fixed energy-divergence exit. These are
screening decisions, not confidence or error-rate claims.

## Inclusion rule

Include every Run submitted after this freeze boundary whose name starts with
`orangepi-rknnlite-seq-v1-`, whose retained intent verifies against the family
contract and selected mode policy, and whose evidence is admitted. Exclude no
identity-compatible admitted Run from that prefix. A rejected, insufficient,
or transport-failed attempt remains listed but contributes no point; a batch is
declared a capability boundary only by the frozen eligible-member rule above,
not by silently dropping failed trials.

Run sequentially and fail-stop on any non-`Accepted` verdict:

1. batch 1 model-loaded idle `i1..i3`, then active `a1..a3`;
2. derive the batch-1 point;
3. batch 2 model-loaded idle `i1..i3`, then active `a1..a3`;
4. derive the batch-2 point and apply the frozen rule; and
5. if no earlier rule fired, record batch 3 as `CapabilityBoundary` because it
   is outside the frozen eligible map.

## Frozen Run-name grammar

```text
orangepi-rknnlite-seq-v1-idle-bs{1|2}-i{1|2|3}
orangepi-rknnlite-seq-v1-active-bs{1|2}-a{1|2|3}
```

## Results

The production retained-evidence Loader re-opened every final and raw evidence
object through its owned Job binding, verified its digest and schema, and
reproduced every `Accepted` verdict. Its terminal facts also verified the exact
batch-specific artifact image and the frozen runtime, MetricObserver, and
WorkloadObserver image IDs for all 12 Runs.

| Mode/member | Run | Run UID | Pod-derived Trial ID | Intent digest | Final evidence digest |
|---|---|---|---|---|---|
| idle / batch 1 | `orangepi-rknnlite-seq-v1-idle-bs1-i1` | `56e22b74-734c-4781-b574-cbedcd74439e` | `bb1bda9b-b6c4-4851-8daa-6fa87fb74b3c` | `sha256:5c51f6cf8de2d20fe055473be9c186235ee23ec4067bec880e83f08e153d6440` | `sha256:d8a153a65d18e48c38f27b38f8c88a2e6b93b52ad6ab542d101345f1a3d5a8fb` |
| idle / batch 1 | `orangepi-rknnlite-seq-v1-idle-bs1-i2` | `8df9eff8-14aa-4f95-99cd-dd3ec6e63c8e` | `bfe3620d-66ed-4eef-a27f-0e84ccd00398` | `sha256:5c51f6cf8de2d20fe055473be9c186235ee23ec4067bec880e83f08e153d6440` | `sha256:7dbbc9ebb14971a17bc485169e9accbaf657be3fbd1c38c5d39466434bc4f524` |
| idle / batch 1 | `orangepi-rknnlite-seq-v1-idle-bs1-i3` | `0cf47990-a77e-4b7b-9c5e-f49525421059` | `1862f5b4-c5f9-48fd-83f2-ed632a7b3b00` | `sha256:e2b1f6ca2d1fd0019b30afc1c30ccb18dca1c437b0a8448ec4bcf7aa11fc8240` | `sha256:daca0dde5737b6fa7dfc23128bc931323c73267ab20d935d089512b2de18a96f` |
| active / batch 1 | `orangepi-rknnlite-seq-v1-active-bs1-a1` | `337e4e9b-ea95-4779-b548-82ee12067266` | `eb79a032-3e38-4f32-a739-c7565f7f00a1` | `sha256:37472d1999832c9864b6cb9d88f92475226040e84433f4ed4a469e76393aa27e` | `sha256:fdae42755a30ff97438de27644d5efbe700325ee5cbcac2327b58ae8ea3164c2` |
| active / batch 1 | `orangepi-rknnlite-seq-v1-active-bs1-a2` | `213ba922-d7fa-407b-989c-55c2de06d7aa` | `36664f53-0caa-4850-80a5-70f851308f2d` | `sha256:37472d1999832c9864b6cb9d88f92475226040e84433f4ed4a469e76393aa27e` | `sha256:7ed4c0698dab00ae2e6ffe9fe518742f8f441a295c33f85da474226d2f79cdab` |
| active / batch 1 | `orangepi-rknnlite-seq-v1-active-bs1-a3` | `774a9e7f-d2b7-4e18-9e45-2377bf4d979d` | `a903cc66-452a-44d7-b747-bd78061668cc` | `sha256:37472d1999832c9864b6cb9d88f92475226040e84433f4ed4a469e76393aa27e` | `sha256:8545e41e35e20ff36064ce8e0c3146ccb4ac1515200284747b098d85bb2368da` |
| idle / batch 2 | `orangepi-rknnlite-seq-v1-idle-bs2-i1` | `b8ce8837-b946-4c1f-b2e6-69e80657dcf0` | `ae1b35c2-5fef-4f48-8ca4-466a807cb1ba` | `sha256:041009060b8d5f039387bc49cd2c57347fb4941dfa15f5728852171815541d58` | `sha256:a8f30a78ac63dd8239cb8283047ad5d73449e45366e49b912dfa6ca32411854b` |
| idle / batch 2 | `orangepi-rknnlite-seq-v1-idle-bs2-i2` | `f4d02d19-1ca4-4fb6-9fab-3478d218e377` | `12cedb9a-816c-48ef-b2ff-8c9d20f4e66c` | `sha256:618a6475277e523ebbf41467cc46090657818c6da1a9cacf2c8b533a999516e8` | `sha256:b74f4e8c638b2aaf663e73a231f3a7f87ab6f6141c08021dbcd2f0d63765d882` |
| idle / batch 2 | `orangepi-rknnlite-seq-v1-idle-bs2-i3` | `3237cd2a-aac8-43d9-8d6a-3b801703b31e` | `5ff694e9-6e36-4359-826c-0c74359360b9` | `sha256:618a6475277e523ebbf41467cc46090657818c6da1a9cacf2c8b533a999516e8` | `sha256:95e1be0ccdfff03328cfe88ac32eba3def12483beee4f34156b0d4b28c1b582c` |
| active / batch 2 | `orangepi-rknnlite-seq-v1-active-bs2-a1` | `df2df5a6-7595-4ed2-ac5b-e9bd6021023d` | `9e993261-20cd-4a50-a143-5b2e66f4f1b0` | `sha256:548db2d035323d2094179c113e3e2c5849841c0b993bb08c1ae06861edd4db21` | `sha256:d1116659a0fd28aa432e745ff4d0cc5e83cd314ffd92e65b6b27dab1c41dbc73` |
| active / batch 2 | `orangepi-rknnlite-seq-v1-active-bs2-a2` | `ea01c67d-d298-43bc-a932-fc2c3ba5165c` | `5f6a0131-5e12-4bab-b030-1ba4f493fd56` | `sha256:ce4857088c354fff3b5ce4c63ec3873349fb312ed568710ae57a6b5ebf8f8b97` | `sha256:1527e6b1e11c65d4ead8d0b8af95103d4d883a39c93055d084a6c304fa56c642` |
| active / batch 2 | `orangepi-rknnlite-seq-v1-active-bs2-a3` | `94f3f0b2-5640-485f-ac32-d05fd5636e04` | `f801d88a-24ef-4348-a071-c3426123ec85` | `sha256:ce4857088c354fff3b5ce4c63ec3873349fb312ed568710ae57a6b5ebf8f8b97` | `sha256:532af8e8097d20ec1403d2573feb16592806e53b04fd3f0a9c4f9d33291dd0ee` |

### Realized family and points

- realized family identity:
  `chill.dacs.io/curve-family-identity.v1alpha1@sha256:5fa8ac8447016f259bf75e68549e73bcf8fa6974a958eaf28d1b126fe99cd60a`
- owner-bound environment identity:
  `chill.dacs.io/external-environment-identity.v1alpha1@sha256:1e462e5d2549ac2be0a98c2e74e8a8d07f9365766280cf052cc840227cb2ff31`
- active admission policy:
  `sha256:9070c3923cebd39de730ba10d78ab83e2657c1249cd98985807f6e4412590fbb`
- idle admission policy:
  `sha256:958500162bcb71bccf2e92245c13651f74d570d79eae37b882e3577b8093e8ff`

| Member | Execution-state identity | Active protocol identity | Idle protocol identity |
|---|---|---|---|
| batch 1 | `chill.dacs.io/execution-state-identity.v1alpha2@sha256:0d0171da01907443a7aeef07380036291aa971e7c10abf4e522c8f89544977fe` | `chill.dacs.io/measurement-protocol-identity.v1alpha1@sha256:26f42452a853dafb7debb58e801e205d50b7358c17e09a27e484c147bb8b3576` | `chill.dacs.io/measurement-protocol-identity.v1alpha1@sha256:d39decf40316d3bb0df17c228303a81c4cc0fd8575080d3bd372e50e157508ad` |
| batch 2 | `chill.dacs.io/execution-state-identity.v1alpha2@sha256:941106399af8e860fd16b766f24166284172584442034d29e565e69419800501` | `chill.dacs.io/measurement-protocol-identity.v1alpha1@sha256:5812007de5098d3f393f109b0e1006d4a0173ba70d5e54f26c7eef22710d41ad` | `chill.dacs.io/measurement-protocol-identity.v1alpha1@sha256:edec97456323f32fb3367821a88d7b2692c2a0f77fd4ef1d57775dd3569c35a3` |

| Batch | Model-loaded idle baseline | Incremental energy | Active p99 |
|---:|---:|---:|---:|
| 1 | `2.7554709701 +/- 0.0286900010 W` | `0.00892543797 +/- 0.00031289634 J/item` | `4.111284 ms` |
| 2 | `2.7521968470 +/- 0.0254398341 W` | `0.00885848038 +/- 0.00017857514 J/item` | `7.923903 ms` |

The intervals above are the current 95% Student-t point construction. They are
reported as point evidence only; the Plan 5a selection itself carries no
confidence statement.

### Frozen-rule decision

`batch 2` was 0.750188% below the batch-one mean, which did not exceed the
pre-fixed 5% improvement margin. The result is:

```text
Status          = Stopped
SelectedBatch   = 1
StopReason      = CapabilityBoundary
BoundaryBatch   = 3
MeasuredBatches = [1, 2]
```

The `CapabilityBoundary` is the frozen absence of a batch-three family member,
not an admission failure and not statistical saturation.

### Recorded procedure deviation

The first batch-two idle Run was submitted after all batch-one Runs completed
but before the batch-one derivation call returned. It was retained under the
pre-fixed inclusion rule. This changed no decision: a one-point discovery
prefix always returns `Continue`, the Run was sequential rather than
overlapping, and no batch-two active point was measured before that return.
The deviation is procedural and does not change family identity, baseline
ownership, pooling, or the reported stop result.
