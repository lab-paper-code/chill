# Minimal Profiler Control Path

## Status

Proposed high-level implementation plan. This document defines the first
operator-managed Profiler and its use of the existing profiling foundations;
it does not yet authorize API or controller implementation.

## Purpose

CHILL currently has reusable internal functions for static compatibility,
bounded power observation, immutable Run validation, and profile-evidence
admission. It does not have a managed Profiler component that coordinates them.

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
- reconciling only the Profiler Deployment and its owner relationship;
- observing Profiler workload availability; and
- reporting component availability as part of `ChillSystem` readiness.

The Operator does not watch `ProfilingRun`, create measurement Jobs, inspect
runtime evidence, grant Profiler permissions, or make profile-admission
decisions.

### Helm release owns

- the Profiler ServiceAccount;
- the least-privilege Role and RoleBinding used by the Profiler;
- the default Profiler configuration referenced by `ChillSystem`; and
- the install, upgrade, and removal lifecycle of those authorization and
  configuration objects.

The Operator is not granted RBAC `bind` or `escalate` privileges. Managing the
Profiler Deployment makes the Profiler an operator-managed operand without
making runtime authorization part of controller reconciliation.

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

- watching `ProfilingRun` resources;
- materializing the exact native Job and support objects;
- expressing placement, resources, security, and ownership with native APIs;
- observing Job and Pod lifecycle without replacing Kubernetes status; and
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

Profiler operating resources are colocated in the CHILL management namespace,
which is `chill-system` in the current deployment. The Profiler watches only
this namespace, and each namespaced `ProfilingRun` owns its Job there. This
keeps ownership and permissions namespace-scoped. A Run is a request for one
measurement, not a campaign or profile.

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
- evidence reference and content digest; and
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
  -> Profiler ServiceAccount, Role, RoleBinding, and base ConfigMap
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

The existing spike remains a characterization harness and regression oracle
until live equivalence is demonstrated. It is not imported by production code.

## Required checkpoints before implementation

### Checkpoint 1: request and immutability boundary

Decide the minimum `ChillSystem.spec.profiler` configuration, the minimum
`ProfilingRun.spec` references, and which resolved facts are frozen into the
intent. The APIs must avoid embedding Deployment details or spike fixture
structure while preserving every identity required by current validation.

Recommended decision: one enabled Profiler operand per `ChillSystem` and one
Run in the CHILL management namespace with immutable execution inputs; no
campaign fields and no mutable desired/current phase model.

### Checkpoint 2: Node-to-power-source resolution

Define the narrow resolver contract and the exact Kubernetes facts that bind a
Node to the existing edge-metrics deployment. Resolution failure must be a
typed pre-execution outcome. The first implementation supports edge-metrics
only while keeping its endpoint out of caller-facing Run intent inputs.

### Checkpoint 3: durable evidence sink

Select where the raw immutable bundle is stored before admission status can be
trusted. Profiler-local files are not durable, and CR status is not a raw-data
store. The sink must return a stable reference and content digest and must
enforce a bounded object size.

This checkpoint is blocking. The first implementation must not publish an
admission result whose source evidence disappears with a Profiler or Run Pod.

### Checkpoint 4: status and retry semantics

Fix condition types, reason codes, terminal behavior, cancellation, and the
boundary between Kubernetes Job failure and CHILL evidence/admission failure.
The first Run keeps `backoffLimit=0`; a retry is a new Run and therefore a new
trial identity.

### Checkpoint 5: measurement worker contract

Fix the narrow handoff from frozen intent to the one-shot Job worker and from
the worker to the durable evidence sink. Reconciliation must remain bounded and
reconstructable without running a measurement loop. Worker termination,
partial uploads, controller restart, and duplicate reconcile events must have
deterministic observable outcomes.

## Implementation sequence

1. Close the five checkpoints and record the reviewed API/state contracts.
2. Add `internal/profiler` ports and a deterministic one-Run orchestrator with
   fake adapters; do not add Kubernetes dependencies.
3. Add the `ChillSystem` Profiler configuration, `ProfilingRun` API, and schema
   tests.
4. Add `cmd/profiler` and its `ProfilingRun` controller with a dedicated
   Helm-installed ServiceAccount and namespace-scoped least-privilege RBAC.
5. Add the one-shot measurement worker boundary, then promote only the proven
   CPU ORT execution and PowerObserver behavior from the spike into it.
6. Add the edge-metrics Node resolver and selected durable evidence sink.
7. Add `internal/operator/profiler` so the CHILL Operator reconciles the
   Profiler Deployment and availability only.
8. Sync CRDs and RBAC into Helm and verify independent Operator/Profiler
   permissions and upgrade behavior.
9. Verify failure, cancellation, identity drift, duplicate events, and both
   controller restarts with envtest and unit tests.
10. Run one live Lattepanda measurement and compare the new evidence and
   admission output with the spike path.
11. Demonstrate Profiler availability through `ChillSystem`, then the Run flow
    through `kubectl get/describe profilingrun`, its owned Job, and the evidence
    digest.
12. Remove no spike path until equivalent evidence and fail-closed behavior are
    confirmed.

## Acceptance gates

The first Profiler is complete only when:

- enabling the Profiler in `ChillSystem` causes the CHILL Operator to maintain
  an available Profiler Deployment and only that component lifecycle;
- the Helm release, not the Operator, owns the Profiler ServiceAccount,
  Role, RoleBinding, and default configuration;
- creating one `ProfilingRun` causes the Profiler controller, not the CHILL
  Operator or a human CLI, to own the complete one-Run lifecycle;
- the Operator and Profiler use separate runtime identities and least-privilege
  RBAC without overlapping reconciliation ownership;
- the first Profiler watches only the CHILL management namespace, currently
  `chill-system`, and each Run owns a Job in that same namespace;
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
