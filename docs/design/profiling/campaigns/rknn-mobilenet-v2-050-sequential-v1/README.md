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
- family name: `rknn-mobilenet-v2-050-sequential-v1`
- sequential policy name: `rknn-mobilenet-v2-050-sequential-v1`
- point policy name: `rknn-mobilenet-v2-050-point-min3-v1`
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
object through its owned Job binding, validated its schema and semantic bindings, and
reproduced every `Accepted` verdict. Its terminal facts also verified the exact
batch-specific artifact image and the frozen runtime, MetricObserver, and
WorkloadObserver image IDs for all 12 Runs.

| Mode/member | Run | Run UID | Pod-derived Trial ID |
|---|---|---|---|
| idle / batch 1 | `orangepi-rknnlite-seq-v1-idle-bs1-i1` | `56e22b74-734c-4781-b574-cbedcd74439e` | `bb1bda9b-b6c4-4851-8daa-6fa87fb74b3c` |
| idle / batch 1 | `orangepi-rknnlite-seq-v1-idle-bs1-i2` | `8df9eff8-14aa-4f95-99cd-dd3ec6e63c8e` | `bfe3620d-66ed-4eef-a27f-0e84ccd00398` |
| idle / batch 1 | `orangepi-rknnlite-seq-v1-idle-bs1-i3` | `0cf47990-a77e-4b7b-9c5e-f49525421059` | `1862f5b4-c5f9-48fd-83f2-ed632a7b3b00` |
| active / batch 1 | `orangepi-rknnlite-seq-v1-active-bs1-a1` | `337e4e9b-ea95-4779-b548-82ee12067266` | `eb79a032-3e38-4f32-a739-c7565f7f00a1` |
| active / batch 1 | `orangepi-rknnlite-seq-v1-active-bs1-a2` | `213ba922-d7fa-407b-989c-55c2de06d7aa` | `36664f53-0caa-4850-80a5-70f851308f2d` |
| active / batch 1 | `orangepi-rknnlite-seq-v1-active-bs1-a3` | `774a9e7f-d2b7-4e18-9e45-2377bf4d979d` | `a903cc66-452a-44d7-b747-bd78061668cc` |
| idle / batch 2 | `orangepi-rknnlite-seq-v1-idle-bs2-i1` | `b8ce8837-b946-4c1f-b2e6-69e80657dcf0` | `ae1b35c2-5fef-4f48-8ca4-466a807cb1ba` |
| idle / batch 2 | `orangepi-rknnlite-seq-v1-idle-bs2-i2` | `f4d02d19-1ca4-4fb6-9fab-3478d218e377` | `12cedb9a-816c-48ef-b2ff-8c9d20f4e66c` |
| idle / batch 2 | `orangepi-rknnlite-seq-v1-idle-bs2-i3` | `3237cd2a-aac8-43d9-8d6a-3b801703b31e` | `5ff694e9-6e36-4359-826c-0c74359360b9` |
| active / batch 2 | `orangepi-rknnlite-seq-v1-active-bs2-a1` | `df2df5a6-7595-4ed2-ac5b-e9bd6021023d` | `9e993261-20cd-4a50-a143-5b2e66f4f1b0` |
| active / batch 2 | `orangepi-rknnlite-seq-v1-active-bs2-a2` | `ea01c67d-d298-43bc-a932-fc2c3ba5165c` | `5f6a0131-5e12-4bab-b030-1ba4f493fd56` |
| active / batch 2 | `orangepi-rknnlite-seq-v1-active-bs2-a3` | `94f3f0b2-5640-485f-ac32-d05fd5636e04` | `f801d88a-24ef-4348-a071-c3426123ec85` |

### Verified family and points

- curve-family name: `rknn-mobilenet-v2-050-sequential-v1`
- owner-bound environment: shared by every included Run
- active admission policy: `wall-power-v2`
- idle admission policy: `idle-wall-power-v1`

Loader verification established one exact state and one active/idle protocol
pair per member; those member-specific values are not reused as campaign IDs.

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
The deviation is procedural and does not change family membership, baseline
ownership, pooling, or the reported stop result.
