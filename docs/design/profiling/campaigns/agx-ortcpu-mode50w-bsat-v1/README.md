# AGX Orin CPU ORT MODE_50W batch-saturation campaign v1

> Navigation: [Campaign records](../README.md)
>
> Method: [Plan 5](../../plans/05-batch-saturation-campaign.md)

## Status

Frozen before measurement. The Git commit that first contains this directory is
the freeze boundary. No Run with the campaign prefix may be submitted before
that commit, and no campaign result exists yet.

## Why this uses Plan 5

The ONNX artifact accepts a dynamic batch dimension, so batch three is not a
capability boundary. This campaign therefore does not use Plan 5a's typed
`CapabilityBoundary` stop. Plan 5 instead fixes the bounded claim domain
`1..2` before measurement and makes no statement about batch three or above.

## Frozen inputs

- family contract: [curve-family-contract.json](curve-family-contract.json)
- discovery policy: [discovery-policy.json](discovery-policy.json)
- family name: `agx-ortcpu-mode50w-bsat-v1`
- discovery-policy name: `agx-ortcpu-mode50w-bsat-v1`
- exact Node and mode: `jetsono` UID
  `52b04c16-4f4a-4b4c-938c-eac35e679f09`, `MODE_50W`
- configured actuation-policy name: `orin-mode-50w-v1`
- `Kmax=2`, `tau=0.05`, `n_disc=3`,
  `maximumConfirmatoryTrials=6`, `maximumActiveRunBudget=18`, point confidence
  `0.95`, and family-wise alpha `0.05`
- active protocol: batch is the only varying field; 20 warm-up iterations,
  75-second window, 2.5-second metric interval, and
  `wall-power-2p5s-source-v1`
- idle protocol: model loaded, 60-second settling, 30-second window, 1-second
  metric interval, and `idle-wall-power-v1`

Both members use the same dynamic ONNX artifact and therefore must realize one
execution-state, environment, active-protocol, and idle-protocol identity. Three
batch-one idle Runs form the one shared baseline and also supply the admitted
idle witness for both family members.

## Inclusion rule

Include every Run submitted after the freeze boundary whose name starts with
`jetsono-ortcpu-m50-bsat-v1-`, whose retained intent verifies against the family
contract and selected mode policies, and whose evidence is admitted. Exclude no
identity-compatible admitted Run from that prefix. A rejected, insufficient,
cancelled, or transport-failed attempt remains listed but contributes no trial;
the campaign stops instead of replacing or omitting it.

Run sequentially and fail-stop on any non-`Accepted` verdict:

1. batch-one model-loaded idle `i1..i3`;
2. discovery active batches one and two, `d1..d3` at each batch;
3. Loader verification, family verification, shared-baseline derivation,
   discovery derivation, and confirmatory sizing;
4. commit the canonical confirmatory policy before submitting confirmation
   Runs; and
5. only when sizing returns `ConfirmatoryTrialsSized`, execute exactly the
   frozen allocation and derive the final result.

Discovery and confirmation active Run sets are disjoint. Any family,
owner-bound environment, actuation-policy, or protocol drift ends this campaign.

## Frozen Run-name grammar

```text
jetsono-ortcpu-m50-bsat-v1-idle-bs1-i{1|2|3}
jetsono-ortcpu-m50-bsat-v1-discovery-bs{1|2}-d{1|2|3}
jetsono-ortcpu-m50-bsat-v1-confirm-bs{1|2}-c{1|2|3|4|5|6}
```

The confirmatory grammar reserves the configured cap but authorizes no
confirmation Run until a second committed policy fixes the realized allocation.
