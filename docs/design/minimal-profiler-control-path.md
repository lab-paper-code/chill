# Minimal Profiler Control Path

## Status

Approved implementation plan. The operator-managed Profiler operand foundation
is implemented: `ChillSystem` configuration and conditions, the standalone
Profiler process, Operator-owned Deployment lifecycle, and Helm-owned runtime
identity and configuration. The foundation has passed API, envtest, Helm,
multi-architecture image, and live-cluster lifecycle verification.
`ProfilingRun` reconciliation and measurement execution remain unimplemented
and are gated below. Request, source-resolution, evidence-ownership, and
status/retry checkpoints are fixed; the measurement-worker contract remains.

## Purpose

CHILL has reusable internal functions for static compatibility, bounded power
observation, immutable Run validation, and profile-evidence admission. It also
has a managed Profiler process, but that process does not yet coordinate a Run.

The first Profiler answers one bounded question:

> Can CHILL execute one exact, statically Compatible model path on one selected
> Node, retain trustworthy Run evidence, and report whether that single Run is
> admissible for later profile construction?

It does not search a batch curve or produce a `DeviceProfile`.

## Before and after

```text
Before

human / spike CLI
  -> materialize one Job
  -> wait and collect evidence
  -> call admission logic

CHILL Operator does not observe or own this lifecycle.
```

```text
After

ChillSystem desired state
  -> Helm installs Profiler runtime identity, permissions, and base configuration
  -> CHILL Operator manages the Profiler Deployment lifecycle

ProfilingRun desired state
  -> CHILL Profiler controller
  -> exact intent frozen
  -> owned Kubernetes Job
  -> measurement worker executes runtime + PowerObserver observation
  -> immutable evidence record
  -> single-Run admission result
  -> ProfilingRun status
```

The visible system changes are an operator-managed Profiler workload and the
`ProfilingRun` resources reconciled by that workload. `ChillSystem.status`
reports only whether the enabled Profiler component is available. It does not
duplicate individual Run state.

## First supported path

The first path deliberately preserves the boundary already proven by the
spike:

- one statically `Compatible` candidate;
- one selected Kubernetes Node;
- ONNX artifact with ONNX Runtime CPU Execution Provider;
- one fixed batch and one immutable measurement contract;
- one Job, one Pod attempt, and no retry;
- one edge-metrics Shelly source resolved from the selected Node;
- raw execution and power evidence; and
- `Accepted`, `Rejected`, or `Insufficient` single-Run admission.

Adding another runtime is not part of this implementation. The first adapter
must remain explicit instead of introducing an unproven generic plugin system.

## Responsibility boundary

### CHILL Operator owns

- enabling, updating, and removing the Profiler component from `ChillSystem`
  desired state;
- reconciling the Profiler Deployment and its `ChillSystem` owner relationship
  in the CHILL release namespace;
- observing only the referenced Profiler ConfigMap identity and content version
  needed to roll the Deployment when startup configuration changes;
- observing Profiler workload availability; and
- reporting component availability as part of `ChillSystem` readiness.

The Operator does not watch `ProfilingRun`, create measurement Jobs, inspect
runtime evidence, grant Profiler permissions, or make profile-admission
decisions. Profiler cleanup is not added to the root `ChillSystem` finalizer;
native owner relationships and the Profiler controllers retain that lifecycle.

### Helm release owns

- the Profiler ServiceAccount in the CHILL release namespace;
- a namespace-scoped Role and RoleBinding for `ProfilingRun`, Job, Pod,
  configuration, and any selected namespaced evidence-object access;
- a read-only ClusterRole and ClusterRoleBinding for the exact cluster-scoped
  Node and CHILL catalog facts required to prepare a Run;
- the default Profiler configuration referenced by `ChillSystem`; and
- the install, upgrade, and removal lifecycle of those authorization and
  configuration objects.

The Operator is not granted RBAC `bind` or `escalate` privileges. Managing the
Profiler Deployment makes the Profiler an operator-managed operand without
making runtime authorization part of controller reconciliation. Cluster-scoped
permissions start with `get` and expand to `list` or `watch` only when a
concrete controller watch requires them. Exact `get`-only reads use the
manager's uncached API reader so they do not implicitly require informer
`list` and `watch` permissions.

### Profiler application layer owns

- coordinating one Run from accepted request through terminal admission;
- using static compatibility before execution;
- freezing the selected candidate, Node snapshot, runtime images, measurement
  contract, and resolved power-source identity into one immutable intent;
- driving the Run state machine idempotently;
- preserving typed failure reasons without converting them into profile facts;
- invoking existing Run validation and admission packages; and
- publishing bounded status and references to durable evidence.

### Profiler controller owns

- watching `ProfilingRun` resources in the CHILL release namespace;
- materializing the exact native Job and support objects;
- expressing placement, resources, security, and ownership with native APIs;
- observing directly owned Job lifecycle without replacing Kubernetes status;
- resolving the actual Pod identity through the Job ownership chain when
  evidence validation requires it;
- reading the durable evidence reference and object identities produced by the
  measurement worker;
- applying compact status to the reconciled `ProfilingRun`; and
- running as a separate controller binary and operator-managed Deployment.

The controller is a time-bounded, idempotent control loop. Each reconciliation
reconstructs the Run from Kubernetes objects and durable evidence. It may make
compatibility, intent, Job, and admission decisions, but it does not poll power,
execute inference, hold a measurement window open, or retain authoritative Run
state in process memory.

### Measurement worker owns

- running inside the one-shot `ProfilingRun` Job;
- executing the frozen runtime and artifact contract;
- invoking `internal/powerobserver` for the bounded measurement window;
- assembling runtime and power evidence without changing the frozen intent;
- writing the immutable raw bundle to the configured durable evidence sink;
  and
- exiting with an execution result that Kubernetes records on the Job and Pod.

The worker is the execution plane. A Profiler controller restart or leader
change does not restart measurement in controller memory or create a second
measurement path.

### Existing leaf packages retain ownership

- `internal/modelcompat`: static artifact/runtime/DeviceClass compatibility;
- `internal/powerobserver`: bounded raw power observation;
- `internal/profilingrun`: immutable intent and execution-evidence validation;
- `internal/profilederivation`: scientific admission and later derivation.

None of these packages imports the Profiler, Kubernetes APIs, controller-runtime,
or CHILL API types.

### Other systems retain ownership

- Kubernetes owns Job admission, placement, allocation, Pod lifecycle, and
  native failure state;
- Helm owns installation-time authorization and base configuration;
- edge-metrics owns Shelly communication and metric exposition;
- a later campaign planner owns batch selection and repeated trials;
- `DeviceProfile` owns only admitted, aggregated profile facts; and
- the scheduler consumes profiles and never runs measurements itself.

## Kubernetes API surface

`ChillSystem` configures whether its Profiler operand is present. The first
configuration surface stays small:

```yaml
spec:
  profiler:
    enabled: true
    configRef:
      name: chill-profiler-config
```

When enabled, the Operator reconciles one Profiler Deployment. The Helm release
supplies its least-privilege runtime identity and default configuration.
`ChillSystem.status` exposes `NodeDiscoveryAvailable` and
`ProfilerAvailable` component conditions and derives the aggregate `Ready`
condition from the enabled components. `ProfilerAvailable` means only that the
controller workload can accept work; it says nothing about any Run succeeding
or being scientifically admitted. Native replica counts and individual Run
state are not duplicated into `ChillSystem.status`.

Profiler operating resources are colocated in the CHILL release namespace,
which is `chill-system` in the current deployment. The Profiler API does not
introduce an independent namespace setting; its Deployment, configuration,
runtime identity, Runs, and Jobs share this namespace.

The Profiler watches `ProfilingRun` only in this namespace, and each Run owns
its Job there. Read-only access to referenced cluster-scoped Nodes,
DeviceClasses, ModelSpecs, and the owning ChillSystem does not broaden the Run
watch scope. A Run is a request for one measurement, not a campaign or profile.

Its desired state should contain references and policy inputs sufficient to
derive one immutable intent:

- exact model artifact and execution-path identity;
- exact trusted runtime declaration and digest-pinned images;
- selected DeviceClass and target Node;
- fixed batch, warm-up, duration, repetitions, and CPU policy;
- power-source selection by Node, not a caller-supplied endpoint; and
- admission-policy identity.

The Profiler resolves the selected Node to an edge-metrics source before it
freezes the Run intent. The resolved endpoint and source identity belong to the
intent and evidence, not to the stable DeviceClass or ModelSpec.

Status remains compact and reconstructable:

- `observedGeneration`;
- Kubernetes-style conditions for execution completion and evidence admission;
- intent digest;
- owned Job reference and actual Node/Pod identities;
- evidence store/key reference, content digest, schema version, and byte size;
  and
- structured admission verdict and reason codes.

Raw latency arrays, power samples, full Pod snapshots, and large logs must not
be copied into CR status. No free-form `phase`, duplicated Node readiness, or
aggregate `message` field is introduced.

## Reconciliation flow

```text
1. Validate request and referenced facts
2. Evaluate exact static compatibility
3. Resolve Node -> edge-metrics source
4. Freeze immutable Run intent and digest
5. Create one deterministic, ProfilingRun-owned Job
6. Let its measurement worker execute and persist runtime + power evidence
7. Observe native Job/Pod completion and strictly validate retained evidence
8. Verify the retained evidence reference and content digest
9. Apply single-Run admission policy
10. Patch compact ProfilingRun status
```

Every step is idempotent. Once execution starts, mutable upstream objects do
not silently change the frozen intent. A materially different request creates
a new Run rather than rewriting the identity of an existing measurement.

Job failure, evidence-collection failure, and scientific rejection remain
different outcomes:

- Job failure means the requested execution did not complete;
- evidence failure means execution cannot be proven from retained facts; and
- admission rejection means valid Run evidence exists but is scientifically
  unsuitable for profile construction.

## Package and dependency shape

```text
cmd/main.go
  -> internal/operator/profiler/      Profiler operand lifecycle only
  -> Profiler Deployment

cmd/profiler/main.go
  -> internal/profiler/controller/    ProfilingRun reconciliation and status
  -> internal/profiler/               one-Run application orchestration
       +--> internal/modelcompat
       +--> internal/profilingrun
       +--> internal/profilederivation
       +--> narrow Kubernetes/source/evidence ports

measurement worker entrypoint
  -> internal/profiler/runner/        one-Job execution plane
       +--> internal/powerobserver
       +--> explicit CPU ORT execution adapter
       +--> durable evidence sink

api/v1alpha1/
  -> ChillSystem profiler configuration and availability
  -> ProfilingRun desired state and status

charts/chill/
  -> Profiler ServiceAccount and base ConfigMap
  -> namespaced Role / RoleBinding
  -> read-only ClusterRole / ClusterRoleBinding
```

`internal/operator/profiler` manages only the Profiler operand. It does not
import profiling-domain leaf packages. `internal/profiler` owns the application
sequence and narrow interfaces without Kubernetes objects. Native object
translation and controller-runtime behavior stay under
`internal/profiler/controller` and its adapters.

`internal/profiler/runner` does not import controller-runtime, the Operator, or
CHILL controller packages. It performs the bounded execution requested by the
Job and terminates. The controller observes durable Kubernetes and evidence-
sink state instead of depending on runner process memory.

The Operator and Profiler are separate binaries and runtime identities while
remaining in the same repository and release. The Profiler never imports the
Operator, and neither process reconciles resources owned by the other.

Controller event mapping follows direct ownership and exact references. The
Operator maps an owned Profiler Deployment and the referenced ConfigMap only to
their `ChillSystem`; the Profiler maps an owned Job only to its `ProfilingRun`.
Neither controller lists and enqueues every root resource for an unrelated
event. A Pod remains owned by its Job, so Pod identity is discovered through
the Job UID rather than by introducing a second controller owner.

The existing spike remains a characterization harness and regression oracle
until live equivalence is demonstrated. It is not imported by production code.

## Required checkpoints before ProfilingRun implementation

### Checkpoint 1: request and immutability boundary

Decide the minimum `ChillSystem.spec.profiler` configuration, the minimum
`ProfilingRun.spec` references, and which resolved facts are frozen into the
intent. The APIs must avoid embedding Deployment details or spike fixture
structure while preserving every identity required by current validation.

Recommended decision: one enabled Profiler operand per `ChillSystem` and one
Run in the CHILL release namespace with immutable execution inputs; no campaign
fields, namespace override, or mutable desired/current phase model.

### Checkpoint 2: Node-to-power-source resolution

Define the narrow resolver contract and the exact Kubernetes facts that bind a
Node to the existing edge-metrics deployment. Resolution failure must be a
typed pre-execution outcome. The first implementation supports edge-metrics
only while keeping its endpoint out of caller-facing Run intent inputs.

### Checkpoint 3: durable evidence ownership

The raw immutable bundle belongs to the CHILL-managed evidence-store operand,
not to CR status, a Profiler filesystem, or a Run Pod volume. The storage
engine is a replaceable S3-compatible dependency. The Operator manages its
Kubernetes operating resources; the Profiler consumes only the logical
evidence-store contract. The first ownership contract is:

- the measurement worker is the only raw-bundle writer;
- the Profiler controller verifies evidence for the Run lifecycle, but does not
  reconcile the storage operand;
- the evidence object is authoritative for the exact retained Run evidence;
- Kubernetes remains authoritative for current Job, Pod, and Node state, while
  their copies in the bundle are historical Run snapshots;
- profile derivation consumes only evidence that the Profiler has retrieved and
  verified, not an unverified worker result or container log; and
- human and research consumers read through the retained reference without
  becoming writers to that object.

Evidence identity is the digest of the exact stored bytes. A location alone is
not identity. The compact reference carried by Run status contains a logical
store identity and opaque object key, content digest, schema version, and byte
size. It contains no raw samples, endpoint credentials, Secret data, or
caller-selected storage URL. The selected store comes from CHILL system
configuration; a Run cannot redirect evidence to an arbitrary endpoint.

The worker constructs the complete bounded bundle, calculates its digest and
size, and performs an immutable create. A repeated write of the same bytes may
succeed idempotently; existing different bytes at the same key must fail. The
sink must not expose a partial object as committed. After a successful commit,
the worker emits only a small structured receipt through the native container
termination message. The receipt is a handoff hint, not proof.

The controller trusts the handoff only after it:

1. observes the one owned Job and Pod attempt in a terminal state;
2. parses the bounded receipt from the terminated worker container;
3. retrieves the object through the configured evidence-store reader;
4. verifies byte size, content digest, schema, Run intent digest, and relevant
   Kubernetes object identities; and
5. publishes the verified reference and evidence/admission Conditions.

Job success without a committed and verified object is evidence failure. An
upload failure or partial write terminates the worker unsuccessfully and cannot
produce an admitted result. Duplicate reconcile events and Profiler restarts
repeat the read and verification; they do not rerun measurement.

The first path does not cascade evidence deletion from `ProfilingRun` or
`ChillSystem` deletion and does not add evidence cleanup to the root finalizer.
Until a separately reviewed retention policy can prove that no live profile or
research lineage references an object, CHILL performs no automatic evidence
garbage collection. This conservative initial retention is acceptable for the
bounded single-Run path and avoids publishing references that silently expire.

This closes evidence responsibility and handoff semantics. The promoted
Evidence Store contract now supplies the immutable writer, independent reader,
compact receipt identity, retained credentials, and operator-managed lifecycle
required by this path.

### Checkpoint 4: status and retry semantics

The Run path preserves three different facts: whether the Kubernetes execution
completed, whether its retained evidence was independently verified, and
whether that verified evidence was scientifically admitted. Job success alone
does not imply either evidence validity or admission. Admission `Rejected` or
`Insufficient` is not rewritten as an execution failure.

The first Job keeps `parallelism=1`, `completions=1`, `backoffLimit=0`, and
`restartPolicy=Never`. A terminal Run never launches another measurement under
the same identity. If its recorded Job disappears, the controller reports that
the execution record was lost rather than recreating the Job. A retry is a new
`ProfilingRun` and therefore a new Run and trial identity.

Deleting a `ProfilingRun` cancels active work through native owner garbage
collection of its Job. It does not delete committed evidence. Profiler restart,
transient observation errors, and duplicate events re-observe the same Run,
Job/Pod, receipt, and retained object without launching measurement again.

The public API must make these outcome boundaries observable without copying
native Job status, raw evidence, logs, credentials, or endpoints into the Run.
Exact Condition names, reason values, and whether a summary Condition is useful
are deferred to the reviewed `ProfilingRun` API schema. No free-form phase or
mutable cancel field is required by this checkpoint.

### Checkpoint 5: measurement worker contract

Fix the concrete serialization and delivery of frozen intent to the one-shot
Job worker, the evidence-store writer interface, the bounded termination
receipt schema, and worker exit codes. This checkpoint implements the ownership
and verification sequence fixed by Checkpoint 3; it does not introduce a second
handoff through logs or controller memory. Reconciliation must remain bounded
and reconstructable without running a measurement loop.

## Implementation sequence

1. Add the minimum `ChillSystem` Profiler configuration and component
   conditions, plus Helm-owned Profiler identity and base configuration.
   Implemented.
2. Add `internal/operator/profiler` so the CHILL Operator reconciles only the
   Profiler Deployment and availability. Implemented.
3. Verify enable, update, disable, ownership, component readiness, API schema,
   image construction, and Helm rendering. Implemented and verified locally and
   on the live cluster.
4. Close the five Run-path checkpoints and record the reviewed API/state
   contracts.
5. Add `internal/profiler` ports and a deterministic one-Run orchestrator with
   fake adapters; do not add Kubernetes dependencies.
6. Add the namespaced `ProfilingRun` API and schema tests.
7. Extend `cmd/profiler` with its `ProfilingRun` controller using the dedicated
   Helm-installed runtime identity and direct owner-based event mapping.
8. Add the one-shot measurement worker boundary, then promote only the proven
   CPU ORT execution and PowerObserver behavior from the spike into it.
9. Add the edge-metrics Node resolver and selected durable evidence sink.
10. Sync CRDs and RBAC into Helm and verify independent Operator/Profiler
    permissions, configuration rollout, and upgrade behavior.
11. Verify failure, cancellation, identity drift, duplicate events, and both
    controller restarts with envtest and unit tests.
12. Run one live Lattepanda measurement and compare the new evidence and
    admission output with the spike path.
13. Demonstrate Profiler availability through `ChillSystem`, then the Run flow
    through `kubectl get/describe profilingrun`, its owned Job, and the evidence
    digest.
14. Remove no spike path until equivalent evidence and fail-closed behavior are
    confirmed.

## Acceptance gates

The first Profiler is complete only when:

- enabling the Profiler in `ChillSystem` causes the CHILL Operator to maintain
  an available Profiler Deployment and only that component lifecycle;
- the Helm release, not the Operator, owns the Profiler ServiceAccount,
  namespace-scoped Role and RoleBinding, read-only ClusterRole and
  ClusterRoleBinding, and default configuration;
- creating one `ProfilingRun` causes the Profiler controller, not the CHILL
  Operator or a human CLI, to own the complete one-Run lifecycle;
- the Operator and Profiler use separate runtime identities and least-privilege
  RBAC without overlapping reconciliation ownership;
- the Profiler has no independent namespace setting, watches Runs in the CHILL
  release namespace, currently `chill-system`, and keeps each owned Job there;
- Deployment and Job events map through direct owners, ConfigMap events map
  through their exact reference, and unrelated events do not enqueue every
  root resource;
- the root `ChillSystem` finalizer does not acquire Profiler Run, Job, or
  evidence cleanup responsibilities;
- `ChillSystem` reports component availability conditions and derives `Ready`
  only from enabled components without duplicating native workload status;
- each controller remains idempotent across duplicate events and its own
  process restart;
- power polling and inference execute in the one-shot Job worker, never inside
  a controller reconcile or controller-memory background loop;
- the Run uses exact candidate, Node, artifact, image, CPU, and power identities;
- Node-to-edge-metrics resolution is explicit and captured in the frozen intent;
- exactly one native Job/Pod attempt is attributable to the Run;
- strict evidence validation is unchanged or stronger than the spike path;
- raw evidence is durable, content-addressed, and referenced rather than copied
  into status;
- execution failure, evidence failure, and admission rejection remain distinct;
- a real live Run produces an observable terminal `ProfilingRun` status; and
- no `DeviceProfile`, batch curve, `b_sat`, SLO, serving, or scheduler claim is
  produced by this first path.

## Explicit non-goals

- adaptive coarse/binary batch search
- idle-baseline campaign orchestration
- repeated-trial aggregation or `b_sat` acceptance
- GPU, TensorRT, RKNN, or generic runtime plugin architecture
- automatic candidate enumeration
- `DeviceProfile` creation or update
- SLO classification or 24-hour serving
- scheduler placement decisions
- changing Prometheus scrape configuration
- representing Kubernetes Node readiness in CHILL status

## Follow-on boundary

Only after this single-Run control path is operationally proven should a later
campaign plan compose multiple `ProfilingRun` objects, admitted idle trials,
batch-point aggregation, and `b_sat` derivation. Only admitted campaign output
may then justify defining and publishing a real `DeviceProfile` contract.
