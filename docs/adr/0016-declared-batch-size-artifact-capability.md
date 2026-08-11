# 0016 Declared-Batch-Size Artifact Capability

## Status

Accepted. Supersedes and replaces 0014, which is deleted.

## Context

CHILL's RKNN conversion pipeline overwrote the source ONNX model's symbolic
batch axis with one constant immediately before conversion. The resulting
one-batch artifact supply was a property of that conversion step.

RKNN-Toolkit2 2.3.2 accepts `dynamic_input` during conversion and enumerates
several batch shapes into one immutable artifact. An RK3588 probe loaded one
such artifact and executed several declared batches through the same RKNNLite
path. The runtime refused an undeclared batch and reported the shapes carried by
the artifact. The converter and device probe together provide the evidence for
a finite declared batch size set.

## Decision

Every derived artifact declares its executable batch set as `DeclaredBatchSizes`.
One execution path, artifact identity, and runtime path serve every declared
profile. The profile set may be sparse. Its contents are catalog configuration;
family freeze canonicalizes those contents into strictly increasing batch order.

The capability boundary is resolved when measurement intent is frozen. The
selected artifact's declared batch size set becomes the frozen planned batch size set.
The sweep advances through that set in ascending order, steps over sizes the
operator omitted, and reaches the capability boundary when the set is
exhausted. `KMax` is compared with the largest planned batch size.

One artifact defines one execution-state key. The curve therefore owns one
admitted model-loaded-idle baseline for that state. The baseline Run uses the
first declared batch.

## Consequences

A runtime that enumerates one batch per artifact requires a per-batch execution
path introduced by a new architectural decision and supported by its own
conversion, device, state-identity, and baseline evidence.

A catalog entry may declare a profile that the selected artifact was built
without. A Run created for that declared batch size follows the ordinary trial
failure and retry path. The frozen capability boundary remains derived from the
declared set.

Plan 5's confirmatory estimand ranges over every batch above its candidate
through `KMax`. Its confirmatory sizing boundary therefore accepts only a
consecutive planned batch size span from the candidate through `KMax` and returns an
explicit error for a gap. Plan 5a follows sparse planned batch size sets directly.
