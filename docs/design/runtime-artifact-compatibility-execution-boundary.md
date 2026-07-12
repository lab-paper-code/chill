# Runtime and Artifact Compatibility and Execution Boundary

## Status

Step 4 complete. This document separates static compatibility, pre-run
execution decisions, Kubernetes placement/allocation, post-start Run
validation, and profile admission. It does not define final APIs, CRDs, Go
types, controllers, or reason constants.

## Inputs

This step builds on the fact audit, commonality, and provenance documents in
this directory and on ADR 0004 and ADR 0012.

ADR 0004 requires pure derivation code with plain Go inputs and no Kubernetes
imports. ADR 0012 defines static compatibility as `Compatible`,
`Incompatible`, or `Unknown` and keeps current Kubernetes eligibility, profile
admission, and SLO feasibility outside that result.

## Principal boundary

Five authorities compose the eventual path:

```text
1. Static compatibility evaluator
        |
2. Pre-run execution decision builder
        |
3. Kubernetes admission, scheduling, and device allocation
        |
4. Post-start Run validator
        |
5. Profile admission and scientific derivation
```

They may be orchestrated sequentially, but they do not share one verdict,
reason vocabulary, writer, or lifecycle.

## 1. Static compatibility evaluator

### Question

Does a declared immutable artifact/runtime path have an explicit static match,
conflict, or missing requirement against trusted stable capabilities?

### Inputs

- immutable artifact identity and digest-bound requirements
- artifact format, target, architecture, accelerator, and batch constraints
  only where their producer and provenance are trustworthy
- stable DeviceClass capabilities
- immutable runtime workload/image declaration describing the runtime family,
  backend capability, architecture, and comparable ABI requirements

Runtime availability must come from an explicit runtime declaration. It is not
inferred from an accelerator name or from an old successful Run.

### Excluded inputs

- individual live Nodes
- Ready, taints, allocatable, current free capacity, or Pod status
- requested CPU/GPU/NPU resources and placement
- selected Run power mode
- cgroup, provider-session, load, engine-build, or inference observations
- profile quality, SLO, or energy evidence

### Output semantics

- `Compatible`: every required static declaration has a matching trusted
  declaration/capability
- `Incompatible`: at least one pair of trusted static facts explicitly
  conflicts
- `Unknown`: a required fact is absent, stale/untrusted for the claim, or
  unsupported by the evaluator

`Unknown` and `Incompatible` both fail closed for normal enumeration, but their
reasons remain distinct. `Compatible` does not mean schedulable, runnable now,
measured, or SLO-feasible.

### Purity

The evaluator consumes plain immutable inputs and has no Kubernetes client or
runtime probe. Any cached/report view is bound to all input identities and
generations; changed inputs require recomputation.

## 2. Pre-run execution decision builder

### Question

For one selected artifact/runtime path and campaign request, what concrete
execution should CHILL ask Kubernetes to run?

### Inputs

- static compatibility result and its exact input identities
- selected artifact and immutable runtime image identity
- selected batch and profiling campaign policy
- runtime-specific decision policy
- current Node or cluster snapshot only where needed to resolve a Run decision
- desired placement, resource, cache, and synchronization policy

### Outputs

- immutable Run correlation identity
- runtime-specific desired configuration
- selected batch and input binding
- native Kubernetes workload intent: image digest, requests/limits,
  selectors/affinity/tolerations, volumes, and runtime-specific access
- references to material snapshots and policy/rule identities used to derive
  the decision

This is desired execution intent, not an observed contract.

### CPU example

- Node allocatable CPU: volatile decision input, not static compatibility
- `floor(allocatable)`: CHILL policy calculation
- CPU limit and ORT intra/inter threads: desired decision outputs
- 100m request and placement selector: Kubernetes workload intent

The builder must not infer that allocatable CPU is currently free, exclusively
available, or schedulable.

### Accelerator examples

- Jetson `nvidia.com/gpu: 1`: requested Pod resource, not allocation evidence
- requested TensorRT/CUDA path and provider options: desired runtime decision
- RKNN hostPath/device exposure: current spike intent, not a Kubernetes NPU
  allocation contract
- TensorRT cache/build policy: desired lifecycle policy; engine existence and
  identity are not yet observed

### Decision failure semantics

Missing/stale decision inputs, unsupported runtime-specific policy, invalid
selected batch, or unresolved access mechanism block creation of a normal Run.
They do not rewrite static compatibility as `Incompatible`.

The normal enumeration/serving path does not build runnable intent from static
`Unknown` or `Incompatible`. An explicitly authorized diagnostic profiling Run
may probe an `Unknown` combination to acquire missing evidence. Its success is
selected-Run evidence and does not automatically mutate the static result.

## 3. Kubernetes admission, scheduling, and allocation

### Authority

Kubernetes owns current Pod admission, placement, binding, Node eligibility,
resource accounting, and device-plugin allocation.

CHILL may perform a point-in-time preflight, but it cannot guarantee the
scheduler outcome or eliminate the race between preflight and admission.

### Required distinctions

- nodeSelector/affinity is placement intent; `spec.nodeName` is actual binding
- `nvidia.com/gpu: 1` request is intent; an admitted/running Pod supplies
  allocation evidence
- CPU allocatable and request do not prove free exclusive CPU or cpuset
- privileged RKNN hostPath access is device visibility, not extended-resource
  allocation or isolation

Pending, Unschedulable, admission rejection, image pull failure, and allocation
failure are current execution/placement outcomes. They do not change the
artifact's static compatibility.

## 4. Post-start Run validator

### Question

Did the concrete Pod and runtime instance realize the desired decision and
execute the selected point with the required validation?

### Inputs and observations

- Run, Pod, actual Node, container, and pulled image identities
- actual artifact bytes and digest
- effective resource/cgroup/device-access observations
- actual runtime component/version observations
- provider availability, registration, selection, and graph assignment where
  the path can observe them
- model load/init and engine build/reuse outcomes
- selected batch inference and output-validation results
- desired-versus-observed comparisons

### CPU example

- actual `cpu.max`, affinity/cpuset, ORT settings, and throttling
- actual provider availability and session selection
- artifact digest, warm-up, and selected-batch inference outcome

### Accelerator examples

- successful Kubernetes GPU allocation and runtime device visibility
- RKNN mounted component identities and `init_runtime` result
- TensorRT engine-ready state, not session construction alone
- registered provider chain and separately captured graph/fallback evidence
- selected-batch output validation

### Output semantics

Run validation reports the outcome and evidence for that exact frozen Run. It
does not own static `Compatible/Incompatible/Unknown` and does not own profile
admission.

A Run failure may indicate materialization drift, admission mutation, resource
pressure, runtime/image mismatch, host ABI drift, load/init/build failure, or
invalid output. It is not promoted to static incompatibility until a separate
trusted static conflict is established and the static evaluator is rerun.

## 5. Profile admission and scientific derivation

### Question

Does a valid Run contain decision-grade performance and energy evidence for
the exact execution-state identity?

This stage owns:

- measurement identity matching
- PowerObserver coverage and sample-quality policy
- transient/baseline handling
- repetitions and uncertainty
- latency, throughput, power, and energy derivation
- SLO feasibility
- candidate versus accepted `b_sat`
- DeviceProfile admission/status

A loadable valid Run can still yield an inadmissible profile. Insufficient
power samples or SLO failure does not make the artifact statically
incompatible or the runtime execution invalid.

## Result combinations that are not contradictions

### Compatible plus Run failure

Static declarations match, but the current Run can fail because of image pull,
resource pressure, host drift, engine-build OOM, or an effective-setting
mismatch.

### Unknown plus diagnostic Run success

Static declaration evidence was insufficient, but one explicitly authorized
probe succeeded. That evidence is scoped to the selected artifact, image,
Node, batch, and Run. It does not resolve unobserved ABI ranges or all batches.

### Incompatible plus Run success

This indicates stale, incorrect, or mismatched evaluator inputs or Run
identity. Success does not overwrite the incompatibility; the inputs and
provenance must be investigated and the evaluator rerun.

### Valid Run plus inadmissible profile

Execution was correct, but measurement coverage, repetition, uncertainty, or
SLO policy did not admit decision-grade evidence.

## Reason ownership

Each stage requires its own reason vocabulary:

| Stage | Reason meaning |
|---|---|
| static compatibility | explicit static conflict or missing/untrusted comparable fact |
| execution decision | unresolved/stale input or unsupported desired policy |
| Kubernetes | admission, placement, image, resource, and allocation outcome |
| Run validation | desired/actual mismatch or execution-stage failure |
| profile admission | evidence quality, coverage, uncertainty, energy, or SLO result |

Composition may expose the first blocked gate plus all prior results. It must
not flatten them into one `compatible`, `eligible`, or generic reason string.

## Staleness

- static results bind all declared input identities/generations
- current Kubernetes eligibility is re-read and remains race-prone; it is not
  durable CHILL truth
- execution decisions become stale when a material declaration or selected
  snapshot changes before Run creation
- Run evidence is immutable historical evidence for one instance
- profiles bind the exact execution-state identity and admission policy; drift
  prevents reuse but does not delete history

## Acceptance examples

### Static compatibility

1. Matching declarations remain `Compatible` even when every matching Node is
   NotReady or tainted.
2. Missing trusted runtime/backend declaration produces `Unknown`.
3. An explicit architecture conflict produces `Incompatible`.
4. Changed input generation prevents reuse of a cached result.

### Execution decision

1. Normal Run creation refuses upstream `Unknown`/`Incompatible` while
   preserving the upstream reason.
2. Missing RKNN allocation/access decision blocks the Run without changing the
   static verdict.
3. A selected batch outside trusted capability is rejected; campaign maximum
   is not treated as capability.

### Kubernetes

1. Static `Compatible` plus insufficient resources is currently ineligible,
   not statically incompatible.
2. A successful preflight followed by resource contention may still fail to
   schedule.
3. A GPU request is not allocation evidence until the Pod is admitted/bound
   and the runtime obtains the allocated device.

### Run validation

1. Requested TensorRT with an effective provider mismatch invalidates the Run
   without rewriting static compatibility.
2. Missing power-mode enforcement is an evidence mismatch, not silent success.
3. Output-shape failure invalidates the selected Run, not every logical-model
   path.
4. Runtime image digest drift invalidates the frozen Run identity even when the
   tag is unchanged.

### Profile admission

1. Valid execution with insufficient power samples rejects the profile only.
2. Valid measurements that fail an SLO remain an explicit profile/SLO result.
3. A profile from a different image digest, provider state, or power mode is
   not reused for the new execution state.

## Adversarial review disposition

Accepted:

- static evaluator, decision builder, Kubernetes, Run validator, and profile
  admission are separate authorities
- only static compatibility owns its tri-state verdict
- current eligibility and observed execution must not be folded into static
  compatibility
- a successful smoke is selected-Run evidence, not reusable class capability
- stage-specific reasons must not share one generic namespace

Narrowed:

- a decision builder may use current Node snapshots to resolve desired intent,
  but it does not predict or replace scheduler admission
- normal enumeration blocks static `Unknown`, while an explicit diagnostic
  profiling path may probe it without automatic promotion
- Run validation can reveal evidence that motivates a new static declaration
  or reevaluation, but it does not mutate the static verdict directly

Rejected:

- the idea that `enumerable = compatible AND profile AND eligibility` permits
  one evaluator to own all three. It is a composition expression only.

## Step 4 conclusion

The future architecture composes gates rather than producing one universal
execution verdict:

```text
pure static compatibility
        -> desired runtime-specific Run decision
        -> Kubernetes-native placement/allocation
        -> actual Run validation
        -> profile admission and derivation
```

Step 5 may now synthesize the high-level design. It must preserve these
authorities, identities, and stage-specific failure meanings rather than
introducing a monolithic `executioncontract` API.
