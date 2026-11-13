# Raw-Sample Preprocessing Boundary

## Status

Accepted

## Context

Preprocessing and generated-output cost depend on sample content. A measured execution therefore identifies its model artifact, runtime path, preprocessor artifact and dataset.

## Decision

A request carries one raw sample of named BYTES fields. The gateway validates field names, datatype, nonempty bounded lengths, JPEG magic/header and UTF-8 once against the preprocessor contract. Later transport boundaries check framing.

`Preprocessor` is a cluster-scoped catalog kind with raw input/output contracts, optional `maxNewTokens` and artifacts. Each `{model, artifact}` pair has one preprocessor artifact naming an immutable carrier, architecture and directory. Artifact-copy delivery places its module beside the model files. The module provides `preprocess`, `infer`, `postprocess` and `WARMUP_SAMPLE`; missing runtime libraries fail startup.

`Dataset` names an immutable carrier and record-field declaration. Profiling and evaluation records live in separate directories. Carrier production owns their disjoint split. Dataset/preprocessor compatibility follows declared fields.

A Workload references a model, preprocessor and optional dataset. Preprocessor changes use the model-change barrier. Referenced catalog deletion and removal of a preprocessor-targeted model artifact are refused.

A model artifact carries one or more files from one carrier; an execution path selects one artifact and loads its files together. RKNN alone declares fixed input tensors under [Declared-Batch-Size Artifact Capability](0016-declared-batch-size-artifact-capability.md). Other inputs come from preprocessing.

The persistent runner executes preprocessing, inference and postprocessing once per batch of requests. BYTES payload elements use little-endian `uint32` lengths. Completion reports per-request positive unit counts or failure reasons, plus batch stage and first-token times.

An item failure ends that request. Invalid report counts, nonpositive successful unit counts, invalid generative first-token callback counts or module exceptions fail the batch while retaining the runner. The runner validates module results. Generation uses greedy static batching and frozen `maxNewTokens`.

Profiling and serving use firstfloor. Active executions draw fresh profiling-directory records using the frozen seed. Raw trials retain record IDs, unit counts and stage times.

## Consequences

Shared execution/profile identity includes model artifact, runtime, preprocessor artifact and dataset. Dataset matching uses name and UID. Existing profiles remain bound to their measured inputs. Unit counts are observations; batching and cost estimation count requests.

Stored changes follow [Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md). The [heterogeneous workload design](../design/serving/heterogeneous-workloads.md) owns detailed delivery and runtime behavior.

