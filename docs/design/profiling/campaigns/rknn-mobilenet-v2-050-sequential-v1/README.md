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

## Evidence access

The Run-name prefix and inclusion rule above select the retained
`ProfilingRun` objects. Per-Run execution, verification, admission, and
measurement values remain in Run status and the referenced evidence object;
use `kubectl chill runs` and `kubectl chill runs show NAME` instead of
copying them into this record. The Status section keeps only the campaign-level
terminal outcome.
