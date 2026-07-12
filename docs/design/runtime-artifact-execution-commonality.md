# Runtime and Artifact Execution Commonality

## Status

Step 2 complete. This document separates the minimum concepts shared by the
CPU, RKNN, and Jetson paths from runtime-specific semantics. It does not define
a provenance taxonomy, compatibility API, CRD, or Go package.

## Input

This step uses the decomposed evidence in
[`runtime-artifact-execution-fact-audit.md`](runtime-artifact-execution-fact-audit.md).
It does not normalize the existing `runtimeContract` or
`derivedExecutionContract` JSON documents.

## Commonality test

A value is a candidate for a shared semantic field only when all three paths
give it:

1. the same meaning
2. the same owner
3. the same creation phase
4. the same mutability/lifetime
5. a comparable validation method

Matching field names or Go/YAML shapes are insufficient.

## Principal finding

Current evidence supports a shared Run correlation envelope more strongly than
a single rich `ExecutionContract`.

Four different authorities and lifecycles must remain distinguishable:

```text
catalog and artifact requirements
        |
        v
runtime-specific desired execution decision
        |
        v
Kubernetes workload placement/allocation intent
        |
        v
post-start Run observation and evidence
```

Combining these in one document would recreate the desired/current,
static/dynamic, and declaration/observation ambiguity found in Step 1.

## Defensible common nucleus

The following concepts have the same minimum meaning across CPU, RKNN, and
Jetson. This is a conceptual list, not a field schema.

### Run correlation and selected state

- immutable Run identity
- logical model reference
- selected artifact reference and content digest
- selected runtime-path reference
- selected batch

The runtime-path reference is common only as identity. The contents and
validation of that path remain runtime-specific. The selected batch identifies
one attempted execution point; it does not carry an artifact-supported set or
the Profiler's campaign search range.

### Common lifecycle outcomes

- artifact digest verification outcome
- runtime load/initialization outcome
- selected-batch execution outcome
- output-validation outcome and validation method

Only the outcome envelope is common. Validation strength is not. CPU currently
observes successful inference without an output-shape check, while RKNN and
Jetson validate `(batch, 1000)`. A shared success value must not erase that
difference.

### Resolved Run correlation

- actual Kubernetes Node identity
- measurement start and end
- runtime-specific evidence attached to the same Run identity

Actual Node is common Run evidence, not a static artifact compatibility fact.
Node selection intent and actual `spec.nodeName` are not one value.

## Runtime-specific desired decisions

### ONNX Runtime CPU

- CPU quota/limit policy
- ORT intra-op and inter-op thread settings
- scheduling request policy
- any future cpuset, topology, or co-location policy

Post-start `cpu.max`, affinity, throttling, and topology observations must not
be written back into this desired input.

### RKNNLite RK3588

- direct RKNNLite backend identity; it is not an ONNX Runtime provider
- NPU core-mask selection
- caller buffer and runtime-native layout handling
- RKNN artifact shape capability and its evidence source
- RKNN toolkit, runtime library, and driver compatibility relation

The current privileged device path and host library mount are spike wiring,
not reusable runtime-path fields.

### ONNX Runtime TensorRT on Jetson

- requested ORT provider chain and TensorRT provider options
- TensorRT engine/profile construction and cache lifecycle
- ORT, TensorRT, CUDA, L4T, and driver compatibility relation
- graph partition/fallback evidence
- configured precision and any separately observed effective precision

TensorRT engine state is runtime-derived and device/profile-bound. It is not
the same lifecycle as an RKNN file compiled before deployment.

## Kubernetes workload intent stays native

Every path eventually produces a Pod, but this structural similarity does not
make placement and allocation part of one runtime compatibility contract.

| Path | Current Kubernetes mechanism | Actual guarantee |
|---|---|---|
| CPU | request, compressible CPU limit, node selector | quota and placement intent; no node or cpuset exclusivity |
| RKNN | privileged Pod, `/dev/dri` and host library mounts | host device/library exposure; no extended-resource allocation |
| Jetson | `nvidia.com/gpu: 1` request and limit | device-plugin allocation of one GPU; no node-wide exclusivity |

Native Pod resource requirements and placement can be retained as Run intent.
They must not be flattened into `deviceKind`, `deviceCount`, or
`exclusive=true`, because those values would imply equivalent guarantees that
do not exist.

## False common fields

| Tempting field | Why it fails the commonality test |
|---|---|
| `provider` | CPU/Jetson use ORT Execution Providers; RKNN calls RKNNLite directly. |
| `runtimeVersion` | CPU is largely image-owned; RKNN and Jetson cross image/host ABI boundaries with multiple components. |
| `precision` | artifact conversion, runtime configuration, input dtype, and effective kernel precision are different facts. |
| `supportedBatches` | CPU attempts dynamic shapes, RKNN embeds or declares shape sets, and TensorRT depends on engine/profile construction. |
| `computeDevice` | CPU quota, RKNN host-device exposure, and GPU device-plugin allocation have different enforcement semantics. |
| `resourceCount` | CPU cores are compressible quota/thread inputs; GPU count is an extended resource; RKNN has no resource count contract. |
| `exclusive` | CPU node co-tenancy, NPU sharing, and GPU allocation describe different isolation domains; none proves node exclusivity here. |
| `cache` | TensorRT engine cache has no CPU equivalent and differs from a precompiled RKNN artifact. |
| `parallelism` | ORT thread pools, RKNN core masks, and GPU allocation are controlled by different schedulers. |
| `powerMode` | mutable desired/current Node state, not a common artifact/runtime-path property. |
| `node` | placement intent, resolved target, and actual scheduled Node are different lifecycle values. |

## Batch separation

Three batch concepts must remain separate:

```text
artifact/runtime-path batch capability
selected batch for one Run
Profiler campaign search range and strategy
```

Only the selected batch belongs in the common Run correlation nucleus.

## Desired and observed separation

Examples of values that must not share one writable field:

| Desired or declared | Observed after start |
|---|---|
| requested ORT provider path | registered providers and graph assignment evidence |
| CPU limit and thread settings | effective cgroup quota, affinity, throttling |
| requested GPU resource | actual scheduled Node and successful device allocation |
| desired power mode | current mode read from the Node |
| expected runtime component relation | observed component identities and load/init outcome |

The observation side may report drift or mismatch. It does not mutate the
desired decision to make the run appear consistent.

## Adversarial review disposition

Independent CPU, accelerator, and boundary reviews were compared with the
current implementations.

Accepted:

- a rich shared `ExecutionContract` is premature
- provider, precision, resource, batch-capability, ABI, and cache semantics
  must not be flattened
- Kubernetes placement/allocation is a separate authority from static
  compatibility and runtime configuration
- the strongest common shape today is a Run correlation envelope plus
  runtime-specific desired and observed attachments

Narrowed:

- Pod placement/resource intent exists in every path and can be correlated by
  Run identity, but it is not part of the common semantic runtime nucleus
- load and output-validation outcomes are common lifecycle concepts, but each
  must retain its runtime-specific method and strength
- runtime path is a common reference concept, not a common `provider` object

Rejected:

- the strict claim that only identity can ever be common. Selected-point
  lifecycle outcomes are also common, provided their evidence method is not
  erased.

## Step 2 conclusion

Do not implement one large `internal/executioncontract.Contract` from the
current spike outputs.

The defensible decomposition is:

```text
small common Run correlation envelope
        + runtime-specific desired decision
        + native Kubernetes workload intent
        + common outcome envelope with runtime-specific evidence
```

Step 3 may define how declarations, observations, derived decisions, and
unknowns carry provenance across these boundaries. It must not use provenance
metadata as a way to recombine all four lifecycles into one object.
