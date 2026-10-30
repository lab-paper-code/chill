# Runtime and Artifact Execution Boundary

> Navigation: [Foundations](../README.md) · [Profiling design index](../../README.md)

Active conclusion contract consolidating the completed runtime fact audit,
commonality, provenance, and compatibility work. This is a semantic boundary,
not a final API, CRD, enum, Go type, or shared execution-contract package.

## Current boundary

| Authority | Owns | Must not claim |
|---|---|---|
| Pure static compatibility (ADR 0004, ADR 0012) | Immutable artifact/carrier requirements, format/target/architecture/accelerator/batch constraints with trusted producers, stable DeviceClass capabilities, explicit immutable runtime workload/image/backend/ABI declarations | Live Node eligibility, resource requests, power mode, runtime observations, profile quality, energy, or SLO feasibility |
| Pre-run decision | Selected artifact/runtime/image/batch/input binding, campaign and runtime-specific policies, required current snapshots, immutable Run identity, desired configuration, native Pod intent, input/rule references | Observed enforcement, exclusive/free allocatable CPU, or guaranteed scheduling |
| Kubernetes | Current admission, Ready/taints/allocatable/capacity, scheduling/binding/resource accounting/device allocation | Durable CHILL current-status truth or artifact compatibility |
| Post-start Run validation | Exact Run/Pod/Node/container/pulled-image/artifact identities, effective resources/access/settings, components/providers, load/init/engine state, selected-batch output checks, desired/actual comparison | A static verdict or generalized artifact capability |
| Evidence and numeric derivation | Immutable raw/finalized evidence, measurement identity, coverage, samples, transient/baseline handling, repetitions/uncertainty, latency/throughput/power/energy and batch curves | Scientific accept/reject policy or automatic experiment suitability judgment |

Scientific admission was removed in `788bcf93` (`refactor: remove profiling
scientific admission`). Execution errors and structural evidence-boundary
validation remain; thermal/workload observations are retained without an
automatic scientific verdict. See [Plan 3](../../plans/03-profile-admission-derivation.md).

Node power actuation now uses Node labels/request generation and independent
read-back, bootID, generation, state, and last-transition annotations.
`PowerModeReconciler` is the sole host power-state mutator; desired power mode
and observed state remain separate. `NodeActuation` CRD and actuation Lease
were removed in `f4b0b685` (`refactor(api): delete NodeActuation`), not current
Run prerequisites. Source: `internal/nodeactuator/controller/power_mode.go`;
current request/observation contract: [Node agent section 3](../../../platform/node-agent.md#3-노드-전력모드-node-label과-generation).

## Compatibility and failure ownership

| Rule | Contract |
|---|---|
| `Compatible` | Every required static declaration matches a trusted declaration/capability; does not mean schedulable, runnable now, measured, or SLO-feasible. Runtime availability requires an explicit declaration, not accelerator naming or an old successful Run. |
| `Incompatible` | At least one pair of trusted static facts explicitly conflicts. |
| `Unknown` | A required fact is absent, stale/untrusted for this claim, or unsupported by the evaluator. Missing trusted backend declaration is Unknown; explicit architecture conflict is Incompatible. |
| Normal enumeration | Unknown and Incompatible both fail closed, retaining distinct reasons. Normal serving/Run intent is not built from either. An explicitly authorized diagnostic Run may probe Unknown without automatically promoting compatibility. |
| Purity/cache | Plain immutable inputs, no Kubernetes client/imports or runtime probe (ADR 0004). Cached results bind every input identity/generation and require recomputation on change. |
| Decision failure | Missing/stale inputs, unsupported policy, invalid selected batch or unresolved access block normal Run creation, not static compatibility. Selected batch must be inside trusted capability; campaign maximum is not capability. |
| Placement failure | Pending, Unschedulable, admission rejection, image pull or allocation failure are current execution outcomes, not static incompatibility. Preflight cannot remove scheduling races. Matching declarations remain Compatible when all Nodes are NotReady/tainted. |
| Run failure | Drift, admission mutation, pressure, image/host ABI mismatch, load/init/build or invalid output affect the frozen Run. Only a separately established trusted static conflict and evaluator rerun can change compatibility. |
| Apparently conflicting results | Compatible plus Run failure is possible; Unknown plus diagnostic success proves only that selected point. Incompatible plus success requires investigation of inputs/provenance/Run identity and reevaluation, not overwriting the verdict. |
| Reason composition | Keep static conflict/missing-fact, decision-policy/input, Kubernetes, Run mismatch/failure, and numeric/evidence reasons distinct. Expose the first blocked gate and prior results, not one generic compatible/eligible/reason string. |
| Staleness | Re-read current Kubernetes eligibility; material declaration/snapshot changes stale a pre-Run decision. Run evidence remains immutable history. Profile reuse requires the exact execution-state identity; image/provider/power drift prevents reuse without deleting history. |

## Shared nucleus, not a rich ExecutionContract

Shared meaning requires the same meaning, owner, creation phase, lifetime, and
comparable validation method—not matching JSON/Go/YAML names. Keep the chain:
identified catalog/artifact declaration → runtime-specific desired decision →
native Kubernetes workload intent → identified Run observation/evidence.

| Common concept | Required distinction |
|---|---|
| Run correlation | Immutable Run identity, logical model, selected artifact and source object, runtime-path reference, selected batch. Runtime path is common identity only; contents/validation stay runtime-specific. |
| Outcomes | Carrier-reference verification, load/init, selected-batch execution, output-validation outcome **and method/strength**. CPU inference success without shape checks must not equal RKNN/Jetson `(batch, 1000)` checks. |
| Resolved evidence | Actual Node, measurement start/end, runtime-specific attachments on the same Run. Node selector/affinity intent differs from actual `spec.nodeName`. |
| Batch | Artifact/runtime-path capability, one selected attempted batch, and campaign search range/strategy are three separate concepts. Only the selected batch is common Run correlation. |
| Desired vs observed | Requested provider vs registration/graph assignment; CPU limit/threads vs cgroup/affinity/throttling; requested GPU vs successful allocation; desired power vs read-back; expected component relation vs observed identities/load outcome. Observation exposes mismatch, never rewrites desired intent. |
| Runtime-specific desired state | CPU quota/thread/request/topology/co-location policy; RKNN direct backend/core mask/layout/shape evidence/toolkit-library-driver relation; Jetson provider options/engine-profile/cache lifecycle/ORT-TensorRT-CUDA-L4T-driver relation/graph fallback and precision. |

Do not flatten provider, runtimeVersion, precision, supportedBatches,
computeDevice/resourceCount, exclusive, cache, parallelism, powerMode, or node
into apparently equivalent fields. RKNNLite is not an ORT provider; CPU quota,
NPU exposure, and GPU allocation enforce different things. TensorRT engines
are runtime-derived/device-profile-bound, unlike predeployment RKNN files.
Native Pod requests/limits/selectors/affinity/tolerations/volumes/access belong
in RunConfig, not `deviceKind`, `deviceCount`, or `exclusive=true` equivalents.

## Provenance contract

Provenance constrains claim meaning; `Declared`, `Observed`, `Derived`, or
`Trusted` alone is insufficient and cannot merge lifecycle owners.

| Dimension | Required evidence/rule |
|---|---|
| Source kind | Authority/producer: immutable artifact or image/package declaration, mutable Kubernetes snapshot, host probe, runtime process/session, Kubernetes allocation, or CHILL derivation. Kind alone establishes no trust. |
| Source identity | Exact artifact name/path/immutable carrier, pulled immutable OCI image (not just tag), Node UID/resourceVersion, Pod/container/Run, and observable runtime component identity. Names/tags/endpoints/paths alone are locators. |
| Verification depth | Preserve declared-only, identified-image packaging, matched object identity, API observation, confirmed Pod allocation, availability, initialized/loaded, selected-batch execution, shape/semantic validation, and graph-assignment distinctions. |
| Scope | Artifact, runtime image, Node snapshot, Pod/Run, process/session, selected artifact+batch, or compiled engine/profile. Selected-batch success is not artifact-wide batch support; a session provider list is not future Node capability. |
| Mutable time/version | Snapshot/version context plus observation time; time does not replace resourceVersion/content identity. Distinguish producer/device time, observer receipt time, and no source timestamp. MetricObserver receipt time is never a Shelly device measurement time. Immutable content needs stable owner/reference, not redundant per-value timestamps. |
| Derivation | Rule/policy identity and version, all material input evidence/snapshots, and atomically produced output group. `derivedAt` alone is not reproducibility/currentness. Changed Node resourceVersion requires a new current CPU derivation; old results remain history. |
| Unknown | Result/epistemic state, not provenance kind. Name required claim and cause: absent source, self-declared/untrusted source, stale/mismatched identity, unsupported comparison, ambiguity/conflict, or verification failure. These are requirements, not final reason enums. Required unknown/unrecognized forms fail closed. |
| Grouping | One provenance header and typed values per uniform snapshot/rule/scope/lifecycle group. Split differing authorities/scopes; do not wrap every scalar or duplicate provenance on CPU limit/intra/inter siblings. |

Example grouping: Node UID/resourceVersion/observedAt + consumed allocatable
fields → CPU rule/version + snapshot reference + limit/thread outputs →
Run/Pod/container/image/window + quota/affinity/runtime/throttling evidence.
A runtime self-report is not supply-chain attestation; verifying the model
file does not verify adjacent metadata/image; hostPath presence does not
identify a library or prove ABI. Changed image resolution under the same tag
is a different runtime input and invalidates the frozen identity.

## Retained execution evidence and limits

These are selected characterization/diagnostic conclusions, not claims that
historical runner constants describe today's deployed substrate. Retained Run
observations live in the evidence store; current source/support matrix owns
implementation facts. No printed `runtimeContract`/`derivedExecutionContract`
is promoted wholesale into reusable truth.

| Path | Evidence retained | Limits and unresolved evidence |
|---|---|---|
| CPU ORT | Immutable carrier delivers a regular ONNX file; Node name and `status.allocatable.cpu` snapshot; `floor(allocatableCPU)` policy sets limit/intra-op, inter-op `1`; runner checks `cpu.max` and thread environment. Runtime reads `ort.__version__`, provider availability/session selection, affinity/usage/CFS throttling. Historical source: `derive-execution-contract.py:27-34,45-55`, `runner.py:145-158,169-182,252-263`. | `OnePodPerNodeFullCPU` overstates isolation: request `100m`, no co-tenant/exclusivity/cpuset enforcement. Allocatable is not free CPU, physical core count or NUMA topology. Inter-op `1` is policy; preinspection provider is a constant. Arbitrary ONNX batches and complete output shape/content semantics were unverified. Mutable image tags and absent imageID, Node UID/resourceVersion, carrier/semantic identity made the derived document incomplete. |
| RKNNLite RK3588 | Selected carrier file, `BATCH_SIZE`/shape-mode/`SUPPORTED_BATCHES` declaration checks (`rknn_runner.py:68-80`), checked load/init on NPU core `0` (`85-89`), selected-artifact NHWC buffer after reproduced layout failure (`91-104`), warm-up/measured `(batch, 1000)` checks (`34-44,103-120`). | Supplied batches are workload declarations, not artifact introspection; success proves only selected batch. RKNNLite/librknnrt ABI `2.3.2` was incomplete because Python package is image-owned but library host-injected. `RKNPUExecutionProvider` misnames direct RKNNLite. Privileged `/dev/dri`, host library mounts and `/dev/dri/renderD129` wiring do not prove extended-resource allocation/NPU isolation or portable capability. `do_quantization=false` does not prove FP16 operations. Source NCHW, artifact interface, and runtime NHWC have separate owners. |
| RKNN provenance audit-time limits | Diagnostic toolkit-lite observation and strict image-owned package declaration field existed; diagnostic host snapshot and separate expected host contract existed; semantic host fields entered new physical state identities. A non-root diagnostic proved four exact DRM devices under explicit privileged runtime-access contract. | At audit time, the production worker declaration, Run-bound host verifier and drift comparison were pending; this is not current outstanding work. Current production L3 support is recorded in [Plan 6](../../plans/06-execution-support-expansion.md). Diagnostic access alone is not retained Run/allocation evidence; successful init alone proves neither reusable ABI compatibility nor NPU allocation/isolation. |
| Jetson ORT TensorRT | EfficientNet-B4 ONNX opens before session. TensorRT available/first (`jetson_runner.py:72-90`); requested TensorRT→CUDA (`84-86`), FP16/INT8 disabled (`66-82`). `nvidia.com/gpu: 1` request+limit (`manifests/jetson-trt-smoke-job.yaml:48-56`) and completed Pod prove one-GPU allocation. Warm-up/measured output `(batch, 1000)` (`31-35,95-112`). Retained v2 `observations/jetson-xavier-nx-tensorrt-smoke.md` records TensorRT, CUDA, CPU chain; graph node counts `NotCaptured` (`125-136`). | Registered order does not prove graph placement/fallback or effective FP32 kernels. Retained Kubernetes smoke validates batch `1`, not every dynamic batch/profile. `MODE_15W_6CORE`, nvpmodel ID `2`, L4T `R35.4.1` were unqueried/unenforced constants, not current power/ABI observations. GPU allocation is not node/CPU exclusivity. Pod-local `emptyDir` lacks reusable engine/profile identity. `sessionBuildSeconds` excludes several additional minutes of first-warmup GPU work. Host-sourced ORT wheel provenance was not retained. |

| Cross-path acceptance rule | Consequence |
|---|---|
| Carrier, runtime image and mutable host ABI are three identities | A verified file cannot substitute for runtime/host identity. |
| Global EEP provider inventory is vocabulary only | It proves neither selected-Node nor selected-image availability. Provider request, registration and graph placement remain distinct. |
| CPU/GPU/NPU intent differs | CPU compressible quota+selector proves no exclusivity; RKNN privileged exposure is no allocation/isolation; GPU plugin count one proves only that allocation. |
| Power and software are mutable Node state | Pinned experiment constants are not stable DeviceClass metadata; manifest `powerMode=15W` is not live read-back. Missing power enforcement is an evidence mismatch, not silent success. |
| Run validation stays selected-point scoped | Provider mismatch, output-shape failure, or immutable-image drift invalidates that Run, not every logical-model path or the static verdict. TensorRT engine-ready evidence is separate from session construction. |
| Numeric evidence stays available | Insufficient power samples, coverage, repetition or uncertainty and SLO results do not become static incompatibility or a scientific admission verdict. |
