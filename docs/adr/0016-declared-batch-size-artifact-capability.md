# 0016 Declared-Batch-Size Artifact Capability

## Status

Accepted.

## Context

CHILL measures one finite batch sweep against one immutable artifact and one
execution path. The source of that finite set depends on how the artifact and
runtime represent batch.

RKNN-Toolkit2 2.3.2 accepts `dynamic_input` during conversion and enumerates
several batch shapes into one immutable artifact. An RK3588 probe loaded one
such artifact and executed several declared batches through the same RKNNLite
path. The runtime refused an undeclared batch and reported the shapes carried by
the artifact. The converter and device probe provide the evidence for a finite
artifact-declared batch-size set.

The canonical ONNX artifact retains its symbolic batch axis. ONNX Runtime takes
the batch size as a call argument, so that artifact does not impose a finite
batch-size set. A batch-energy-optimization campaign must still freeze the finite set it
will measure before it creates a Run.

The input tensor contract is a property of each artifact. It is required to
freeze comparable curve plans regardless of whether a conversion produced the
artifact.

## Decision

Every artifact declares its batch-invariant input tensor contract as
`ModelArtifact.input`. A selected artifact without that declaration cannot
produce a curve plan and returns a typed error naming the artifact.

Every derived artifact additionally declares the finite batch set fixed by its
conversion as `ModelArtifact.derivation.declaredBatchSizes`. One execution
path, artifact identity, and runtime path serve every declared batch size. A
canonical artifact declares no derivation and no finite batch-size set;
`BatchEnergyOptimization.spec.batchSizes` supplies its measurement-selected sweep.

Exactly one source applies to a campaign. `spec.batchSizes` must be absent when
the selected artifact has `DeclaredBatchSizes`, and it is required, positive,
unique, and strictly increasing when the selected artifact has none. Supplying
both sources is a validation error.

The recorded curve plan carries the selected artifact's input tensor contract
unchanged and the selected batch set as `PlannedBatchSizes`. Its conversion
contract is populated for the artifact-declared source and all-empty for the
campaign-declared canonical source, so the immutable plan records whether its
boundary is an artifact constraint or a measurement choice.

The capability boundary is resolved when the campaign records its curve plan. The
sweep advances through the recorded set in ascending order, steps over omitted
sizes, and reaches the capability boundary when the set is exhausted. `KMax` is
compared with the largest planned batch size.

One artifact defines one execution-state key. The curve therefore owns one
admitted model-loaded-idle baseline for that state. The baseline Run uses the
first planned batch.

## Consequences

A runtime that enumerates one batch per artifact requires a per-batch execution
path supported by its own conversion, device, state-identity, and baseline
evidence.

A derived-artifact catalog entry may declare a batch size that the selected
artifact was built without. A canonical-artifact campaign may likewise select
a batch that its runtime cannot execute. A Run created for either selected size
follows the ordinary trial failure and retry path. The recorded capability
boundary remains exhaustion of the recorded set.

Plan 7's Quick search follows the recorded batch-size set directly. It stops at
the first non-improving neighbor or completes with `SearchSpaceExhausted`; it
does not require a consecutive integer span or a confirmatory range through
`KMax`.
