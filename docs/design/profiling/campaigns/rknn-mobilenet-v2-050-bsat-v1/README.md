# RKNN MobileNet V2 0.50 batch-saturation campaign v1

> Navigation: [Campaign records](../README.md)
>
> Method: [Plan 5](../../plans/05-batch-saturation-campaign.md)

## Status

Frozen before measurement; no campaign Run has been submitted. The Git commit
that first contains this directory is the discovery freeze boundary. A
confirmatory policy does not exist yet and must be committed after discovery
sizing and before the first confirmatory Run.

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

No Runs or derived results are recorded yet.
