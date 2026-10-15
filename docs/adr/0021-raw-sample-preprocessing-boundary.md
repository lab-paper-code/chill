# 0021 Raw-Sample Preprocessing Boundary

## Status

Accepted. Supersedes the parts of
[0012 Model Artifact Compatibility Boundary](0012-model-artifact-compatibility-boundary.md)
and [0016 Declared-Batch-Size Artifact Capability](0016-declared-batch-size-artifact-capability.md)
named under Decision.

## Context

CHILL serves heterogeneous workloads through one path. A mobilenet classifier
and SmolVLM2-500M-Video-Instruct answering VQAv2 questions share a POOL, and
each request carries one raw sample: a JPEG, optionally with a UTF-8 question.

Three facts of the current boundary block that path:

- Clients send model tensors. The model input contract of each artifact is the
  client contract, so preprocessing lives outside CHILL and outside its
  measurements.
- An artifact is one file. SmolVLM executes three ONNX files on one runtime:
  `vision_encoder`, `embed_tokens`, and `decoder_model_merged`. Their inputs
  carry dynamic tile and sequence axes that a fixed `sampleShape` cannot state.
- Active profiling feeds one seeded random tensor batch to every execution.
  The measurement specification now requires each execution to draw fresh
  dataset records and run preprocessing through postprocessing on the same
  firstfloor path that serves.

Preprocessing cost depends on the sample (image size, tile count, question
length), and decode cost depends on the generated length. A profile therefore
describes an execution only together with the preprocessing code and the data
it was measured on.

## Decision

A client request is one raw sample of named BYTES fields. The global gateway
validates it once against the referenced preprocessor's raw input contract:
field names, BYTES datatype, per-field size bound, JPEG magic bytes and header,
UTF-8 validity. Every later hop checks framing only.

`Preprocessor` is a cluster-scoped catalog kind holding the raw input contract,
the raw output contract, an optional `maxNewTokens`, and preprocessor artifacts.
Each preprocessor artifact targets one `{model, artifact}` pair, exactly one per
pair, and names an immutable carrier image, architecture, and directory. The
existing artifact-copy init container delivers that directory beside the model
files. The directory holds one Python module providing `preprocess`, `infer`,
`postprocess`, and `WARMUP_SAMPLE`. The runtime image supplies its libraries,
and an import failure is a runner startup error.

`Dataset` is a cluster-scoped catalog kind naming an immutable carrier image
and its record field declaration. Records live in two fixed directories,
`profiling` and `evaluation`. The asset build that produces the carrier splits
records between them and tests that the split is disjoint. Dataset and
preprocessor compatibility is decided from the two declarations alone.

A workload references one model, one preprocessor, and optionally one dataset.
A preprocessor change follows the shared model-change barrier. Deleting a
referenced preprocessor or dataset is refused, and so is removing a model
artifact that a preprocessor artifact targets.

One model artifact carries one or more model files from one carrier image.
An execution path still references exactly one artifact, so execution identity
and preprocessor targeting stay singular. The runtime loads every file of the
artifact together. This supersedes ADR 0012's implicit one-file artifact and
extends its execution identity: a measured execution state is the model
artifact, runtime path, preprocessor artifact, and dataset.

The per-artifact input tensor contract becomes an RKNN-only declaration that
feeds RKNN delivery and runtime checks. Preprocessor artifacts define the model
inputs of every other artifact. This supersedes ADR 0016's requirement that
every artifact declare `input.tensors` and that the curve plan compare that
contract. ADR 0016's declared batch sizes and batch-set rules are unchanged.

The persistent runner executes preprocess, inference, and postprocess as one
execution unit per batch of `n` requests, and `n` remains the batching unit.
IPC carries each BYTES element as a little-endian `uint32` length followed by
its bytes. The completion frame returns, per request, either a unit count of at
least one or a failure reason, plus batch-level preprocess, inference,
postprocess, and first-token elapsed times. A failed item ends only its own
request. A report count different from `n`, a successful unit count below one,
a generative batch whose first-token callback ran zero times or more than once,
or an exception from the module ends that batch and keeps the runner. The
runner is the only boundary that checks the module's results. Generation is
greedy with static batching, and `maxNewTokens` is configuration frozen with the
preprocessor artifact into every Run.

Profiling and serving use only the firstfloor path. Each measured execution
draws `n` records from the dataset's profiling directory with a fixed seed, and
raw trials retain record IDs, unit counts, and stage times.
The direct ORT and RKNNLite measurement paths and random input generation are
removed.

## Consequences

Two workloads share an instance and a profile exactly when model artifact,
runtime, preprocessor artifact, and dataset all match. Dataset matching uses its
name and UID, so a dataset edit applies from the next measurement while existing
profiles remain usable. Unit counts are analysis observations; batch selection
and cost estimation keep counting requests.

Every stored contract that embeds model inputs, artifact paths, or the profiling
protocol changes once in the same work unit. Under
[ADR 0019](0019-durable-stored-contract-compatibility.md), ModelSpecs, Workloads,
measurement objects, their Evidence Store objects, and serving journal windows
written before the change are deleted and recreated.

A new model joins by adding a carrier image and a preprocessor module. Its
pipeline glue lives in that versioned module; CHILL keeps one fixed three-call
interface and one execution unit.

Implementation design:
[heterogeneous workloads](../design/serving/heterogeneous-workloads.md).
