# Runtime and Artifact Execution Boundary

[Foundations](../README.md) and [Profiling design index](../../README.md)

## Authorities

| Authority | Owns | Claim boundary |
|---|---|---|
| Static compatibility | Trusted artifact/carrier, architecture, accelerator, batch and runtime declarations | Does not establish placement, power, execution, quality or SLO feasibility |
| Pre-run decision | Selected artifact/runtime/image/batch/input, policies, snapshots and frozen actor | Placement remains subject to Kubernetes admission and races |
| Kubernetes | Readiness, taints, capacity, scheduling, binding and device allocation | Does not establish artifact compatibility |
| Actual execution | Run/Pod/Node/artifact identity, effective resources/settings, providers and inference | Selected-point evidence |
| Evidence and derivation | Immutable records, coverage, samples, repetitions and numeric profiles | No automatic scientific suitability verdict |

Static compatibility follows
[Derivation Purity Boundary](../../../../adr/0004-derivation-purity-boundary.md)
and [Model Artifact Compatibility Boundary](../../../../adr/0012-model-artifact-compatibility-boundary.md).
Its evaluator uses plain immutable inputs and has no Kubernetes client/imports.

## Compatibility and failure

`Compatible` means required trusted declarations match. `Incompatible` means
trusted static facts conflict. `Unknown` names missing, untrusted, stale,
ambiguous or unsupported facts. Normal enumeration fails closed for Unknown
and Incompatible with distinct reasons. An authorized diagnostic Run can probe
Unknown without promoting reusable capability.

Missing policy, unresolved access or invalid batch blocks Run creation.
Pending/Unschedulable, image pull and allocation failures are execution outcomes.
Drift, ABI/load/init/inference/output failures belong to the frozen Run.
Compatibility changes require trusted static facts and evaluator results.

Keep static, preparation, Kubernetes, execution and numeric/evidence reasons
distinct. Re-read placement eligibility. Selected-batch success does not prove
every future batch.

## Frozen execution and provenance

The chain is declaration, frozen runtime-specific actor, native workload and
immutable observed evidence. Shared names do not merge owners or phases.

Runtime image/version remain measured performance identity. Collection windows,
observers, meters and initialization/warmup domains are normalized separately
by `profiling/derivation.PerformanceCondition`. Serving launches the measured
frozen actor; recovery does not substitute a newly selected image.

Retain producer/source identity, verification depth, scope, mutable snapshot
context and derivation rule. Artifact, runtime image and host ABI are distinct.
A self-report is not supply-chain attestation; library presence does not prove
ABI compatibility.

ORT provider availability, selection and graph placement are different facts.
RKNNLite is a direct backend with its own core-mask, layout and host/device
access contract. GPU allocation, privileged exposure and CPU sharing provide
different enforcement.

CPU has no reservation, quota, pinning or tuning override. Actual cgroup,
cpuset, affinity, threads, usage and throttling are observations. Memory,
accelerators, selectors, tolerations and mounts remain frozen delivery facts.

## Power and retained evidence

Node labels request power mode. The node agent alone mutates host power and
publishes independent read-back, boot identity, generation and transition
markers. See [Node agent](../../../platform/node-agent.md).

Finalization validates raw once and binds actual execution. Retained readers
bound bytes and decode. Numeric insufficiency, uncertain coverage or an SLO
result does not become static incompatibility.
Contract replacement follows
[Durable Stored-Contract Compatibility](../../../../adr/0019-durable-stored-contract-compatibility.md).
