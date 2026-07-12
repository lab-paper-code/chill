# Runtime and Artifact Execution Fact Audit

## Status

Step 1 complete. This document inventories the current CPU, RKNN, and Jetson
spike evidence. It does not define a shared schema or authorize an internal
implementation.

## Audit rule

The audit does not treat a value as trusted merely because a runner prints it
under `CHILL DERIVED EXECUTION CONTRACT`. Each value is classified here only
by what the current code and retained execution evidence establish:

- verified declaration: immutable or versioned input checked before execution
- run observation: read or validated from the running process, Pod, or Node
- derived decision: a CHILL rule applied to input facts
- spike constant: a manifest, environment, image, or runner value that is not
  independently verified at the point where it is reported
- unknown: evidence required for a broader claim is absent

These names are audit labels, not the final provenance API proposed in a later
step.

## Governing boundary from ADR 0012

ADR 0012 separates logical model, immutable artifact variant, runtime path,
device profile evidence, and current Kubernetes eligibility. Static
compatibility is tri-state. A missing or untrusted required fact is `Unknown`,
and both `Unknown` and `Incompatible` fail closed for enumeration.

A successful smoke run proves that one artifact/runtime/Pod combination loaded
and produced valid output at that time. It does not by itself prove reusable
static compatibility, current schedulability, SLO feasibility, or support for
unmeasured batches.

## CPU ONNX Runtime path

### What is established

| Value | Audit classification | Current evidence |
|---|---|---|
| ONNX artifact bytes and digest | verified declaration and run observation | init container checks the digest; runner recomputes it before load (`manifests/batch-sweep-job.yaml:35-42,62-67`, `runner.py:162-167`) |
| selected Node name and allocatable CPU | Node snapshot observation before Job creation | derivation reads `metadata.name` and `status.allocatable.cpu` (`derive-execution-contract.py:27-32`) |
| integer CPU budget | derived decision | `floor(allocatableCPU)` (`derive-execution-contract.py:32-34`) |
| container CPU limit equals budget | derived decision plus post-start observation | renderer applies the value; runner checks `cpu.max` (`derive-execution-contract.py:45-55`, `runner.py:145-158`) |
| ORT intra-op equals budget, inter-op equals one | derived decision plus post-start validation | derivation and runner environment checks (`derive-execution-contract.py:52-55`, `runner.py:145-158`) |
| ORT version and selected CPU provider | run observation | runner reads `ort.__version__`, checks provider availability, constructs the session, and records the selected provider (`runner.py:169-182,252-254`) |
| affinity, CPU usage, CFS throttling | run observation | captured after execution (`runner.py:255-263`) |

### Values that are not established facts

| Current value or claim | Audit result |
|---|---|
| `OnePodPerNodeFullCPU` | spike policy name only. The Pod requests 100m, has no node-exclusivity mechanism, and does not validate absence of co-tenants. |
| allocatable CPU is an available exclusive execution budget | false generalization. Allocatable is scheduling capacity, not current free capacity, physical-core count, NUMA topology, or an exclusive cpuset. |
| `interOpThreads=1` | CHILL spike decision, not a Node, artifact, or runtime fact. |
| runtime/provider in the derived contract | hardcoded before the runtime image is inspected. Availability is observed only after the Pod starts. |
| arbitrary ONNX batch capability | unknown. The runner creates the selected input and observes success, but the contract has no artifact-declared supported set. |
| complete model semantic validation | unknown. The CPU runner does not validate output shape or content. |
| immutable runtime substrate | unknown. Runtime images are referenced by mutable tags and their pulled digest is not part of the derived contract. |
| contract input identity | incomplete. Node UID/resourceVersion, artifact digest, runtime image digest, and a contract digest are absent from the derived document. |

### CPU verdict

The CPU spike proves one executable state and verifies quota/thread equality.
It does not enforce the policy implied by `OnePodPerNodeFullCPU`. That name and
the derived CPU limit cannot be promoted as an effective isolation contract
without either enforcement evidence or a narrower claim.

## RKNNLite RK3588 path

### What is established

| Value | Audit classification | Current evidence |
|---|---|---|
| selected RKNN artifact bytes and digest | verified declaration and run observation | runner recomputes the digest (`rknn_runner.py:68-83`) |
| selected batch belongs to the supplied batch declaration | declaration validation | runner checks `BATCH_SIZE`, shape mode, and `SUPPORTED_BATCHES` (`rknn_runner.py:68-80`) |
| selected artifact loads and runtime initializes on NPU core 0 | run observation for one Pod | return codes from `load_rknn` and `init_runtime` are checked (`rknn_runner.py:85-89`) |
| NHWC runtime input works for the selected artifact | run observation and spike decision | steady buffer uses NHWC after the reproduced layout failure (`rknn_runner.py:91-104`) |
| every measured call returns `(batch, 1000)` | run observation | checked during warm-up and measurement (`rknn_runner.py:34-44,103-120`) |
| artifact target and fixed batch lineage in the ModelSpec fixture | verified offline declaration for the fixture | artifact build metadata and execution-path constraint are cross-checked (`spikes/modelspec/fixtures/mobilenet-v2-050.yaml:11-19,37-48`) |

### Values that are not established facts

| Current value or claim | Audit result |
|---|---|
| `SUPPORTED_BATCHES` | supplied by manifest/render arguments, not introspected from the RKNN artifact. A successful run proves only the selected batch. |
| RKNNLite/librknnrt ABI 2.3.2 | incomplete. The Python package is image-owned, while `librknnrt.so` is injected from the host; driver, library digest, and ABI comparison are not recorded. |
| `RKNPUExecutionProvider` | misleading vocabulary. This path calls RKNNLite directly and is not an ONNX Runtime Execution Provider. |
| `OnePodPerNodeSingleNPUCore` | unresolved policy. Privileged `/dev/dri` host access is not a Kubernetes extended-resource allocation and does not enforce NPU sharing or exclusivity. |
| `/dev/dri/renderD129` | spike wiring tied to one host, not a stable DeviceClass capability or portable runtime contract. |
| FP16 execution | unknown. `do_quantization=false` or an observation note does not prove the effective precision of every executed operation. |
| source NCHW and runtime NHWC ownership | mixed. Source layout, compiled artifact interface, and runtime buffer policy require separate sources rather than one runtime-owned value. |

### RKNN verdict

The RKNN spike strongly proves load, initialization, selected-batch execution,
and output shape for one Pod. Static reusable compatibility remains `Unknown`
for runtime/driver ABI and device allocation until the host substrate has an
identifiable producer and the NPU has a Kubernetes resource contract.

## Jetson ONNX Runtime TensorRT path

### What is established

| Value | Audit classification | Current evidence |
|---|---|---|
| EfficientNet-B4 ONNX bytes and digest | verified declaration and run observation | runner recomputes the digest before session creation (`jetson_runner.py:60-73`) |
| TensorRT provider is available and first in the registered chain | run observation | availability and `session.get_providers()` are checked (`jetson_runner.py:72-90`) |
| requested provider chain | derived execution decision | session requests TensorRT followed by CUDA (`jetson_runner.py:84-86`) |
| FP16 and INT8 disabled | runtime configuration decision | TensorRT provider options disable both (`jetson_runner.py:66-82`) |
| one Kubernetes GPU allocated | Pod declaration and successful allocation evidence | request and limit both use `nvidia.com/gpu: 1`; completed Pod proves allocation (`manifests/jetson-trt-smoke-job.yaml:48-56`) |
| every measured output is `(batch, 1000)` | run observation | checked for warm-up and measurement (`jetson_runner.py:31-35,95-112`) |
| registered provider chain TensorRT, CUDA, CPU | run observation | retained v2 execution evidence and result (`observations/jetson-xavier-nx-tensorrt-smoke.md`) |
| graph partition evidence absent | explicit unknown | TensorRT, CUDA, and CPU node counts are recorded as `NotCaptured` (`jetson_runner.py:125-136`) |

### Values that are not established facts

| Current value or claim | Audit result |
|---|---|
| entire graph executed in TensorRT | unknown. Provider availability and ordering do not identify subgraph assignment or fallback. |
| effective kernel precision is FP32 | unknown. Disabling TensorRT FP16/INT8 is a configuration fact, not per-operation execution evidence. |
| arbitrary dynamic batch support | unknown. The retained Kubernetes smoke validates batch 1 only; a dynamic ONNX dimension does not prove TensorRT profile construction for every batch. |
| `MODE_15W_6CORE`, nvpmodel ID 2 | runner constant based on an earlier observation; it is not queried or enforced when the run begins. |
| L4T R35.4.1 as current Node/runtime compatibility | runner constant and image-selection assumption; Node driver/L4T is not compared at start. |
| `OnePodPerNodeExclusiveGPU` | overbroad. The device-plugin allocation supports one-GPU allocation, not node-wide exclusivity, CPU isolation, or absence of other workloads. |
| reusable TensorRT engine identity | unknown. Cache is Pod-local `emptyDir`; no engine digest, optimization profile, or artifact linkage is retained. |
| `sessionBuildSeconds` is total preparation time | false. First warm-up performed several additional minutes of GPU work in the retained run. |
| immutable ORT/native runtime provenance | incomplete. The image imports a host-sourced ORT wheel without recording its source/digest in the execution result. |

### Jetson verdict

The Jetson smoke proves one GPU-allocated Pod can load the artifact, select a
TensorRT-first registered provider chain, and execute batch 1 with valid
outputs. It does not prove full TensorRT graph placement, reusable batch
capability, effective operation precision, current power mode, or a portable
L4T/TensorRT ABI match.

## Cross-cutting facts and ownership hazards

1. Artifact content digest, runtime image identity, and mutable host ABI are
   three different identities. The current spikes do not consistently retain
   all three.
2. A provider observed anywhere in the EEP inventory is vocabulary evidence,
   not proof that it exists on the selected Node or in the selected image. The
   ModelSpec validator currently unions provider names across devices
   (`spikes/modelspec/validate.py:107-125`).
3. Provider request, provider registration, and graph assignment are distinct.
4. Artifact-supported batches, a selected successful batch, and the Profiler's
   campaign search range are distinct.
5. `nvidia.com/gpu` capacity is reported by Kubernetes/device-plugin state;
   requesting count one is a CHILL decision; successful allocation is Run
   evidence.
6. Node selector, tolerations, requests, limits, and cache volumes are Run
   placement decisions. Node Ready, taints, and allocatable values remain
   current Kubernetes eligibility facts, not ModelSpec compatibility facts.
7. A mutable power mode or installed driver/runtime must not become stable
   DeviceClass metadata merely because a spike pins it.

## Adversarial review disposition

Three independent reviews challenged the CPU path, accelerator paths, and
cross-cutting ownership boundary. Their findings were checked against the
current files rather than accepted by default.

Accepted findings:

- the CPU policy name overstates unenforced isolation
- RKNN supported-batch and ABI claims are self-declared or incomplete
- Jetson provider ordering is not graph-placement evidence
- mutable Node software/power state is mixed into runner constants
- runtime image identity is missing from all derived contracts
- the ModelSpec spike's global provider union cannot prove per-Node runtime
  availability

Narrowed findings:

- `nvidia.com/gpu: 1` is not dismissed as a mere constant: the completed Pod is
  real allocation evidence. The broader node-exclusive policy remains
  unproven.
- RKNN NHWC input is retained as selected-artifact execution evidence, not
  promoted to a universal RKNN artifact fact.
- FP32 is retained as a Jetson configuration decision, while effective
  per-operation precision remains unknown.

## Step 1 conclusion

No current printed `runtimeContract` or `derivedExecutionContract` can be
promoted wholesale. The reusable input to later design work is the decomposed
set of verified declarations, run observations, derived decisions, spike
constants, and explicit unknowns above.

Step 2 may now examine which of these decomposed values are genuinely common
across runtimes and which must remain runtime-specific. It must not begin by
normalizing the existing output JSON structures.
