# Minimal Profiler Control Path

> Navigation: [Profiling design index](../README.md)

## Status

Approved implementation plan. The operator-managed Profiler operand foundation
is implemented: `ChillSystem` configuration and conditions, the standalone
Profiler process, Operator-owned Deployment lifecycle, and Helm-owned runtime
identity and configuration. The foundation has passed API, envtest, Helm,
multi-architecture image, and live-cluster lifecycle verification.
The immutable, namespaced `ProfilingRun` API, preparation, Job execution,
worker-side raw evidence commit, controller-side terminal evidence
finalization, and compact execution/evidence status are implemented and
live-verified for the first CPU ONNX Runtime path. The Profiler now independently
reloads that retained evidence, applies the named `wall-power-v2` single-Run
policy, and records `Accepted`, `Rejected`, or `Insufficient` with typed reason
codes. Request, evidence-ownership, status/retry, and measurement-worker
checkpoints are fixed.

The internal intent is `v1alpha10`, and the Go and Python execution boundaries
accept that current schema only. It carries semantic source-object identities,
the typed host contract, and an optional typed actuation binding while
preserving the common worker CPU quota and RKNNLite's omission of ORT-only
thread settings. Each immutable intent record contains the structured intent,
candidate report, admission policy, and execution policy.
The public request schema, request-to-intent projection, bounded Job envelope,
idle CPU ORT execution, raw commit, mode-aware finalization/loading, and named
idle admission dispatch are implemented and live-verified. The accepted idle
Run retains distinct runtime/raw completeness while reusing the active control
lifecycle.

A focused Profiler envtest now covers the campaign-risk subset of checkpoint
12: duplicate reconciles, reconstruction of the reconciler over durable API
state, and disappearance of the recorded Job. It proves that one recorded Job
UID is never rematerialized and that loss is reported as stable
`ExecutionLost`. A second restart test proves that evidence finalized before a
Profiler restart proceeds to admission without repeating finalization, and that
a completed admission is a stable no-op. A third envtest proves that recreating
the recorded Job name with a different UID is rejected without
rematerialization or Run-status mutation; the same fail-closed result now covers
an unchanged Job UID whose controller owner drifts. Retained-frozen intent record,
immutable Run-spec, evidence-store binding, raw-object identity, and executed
container-image drift remain covered by their focused unit gates. The Operator
envtest also reconstructs its Profiler reconciler over the retained
`ChillSystem`, ConfigMap, and Deployment, then proves that it retains one
Deployment UID and returns to a stable no-op. Cancellation remains open;
the Profiler envtest now proves that a deletion timestamp is a no-write,
no-external-call fence while the recorded Job retains its Run owner reference.
Because envtest has no Kubernetes garbage-collection controller, actual Job
collection is covered by a guarded Kubernetes 1.31 kind e2e. Together these
tests complete checkpoint 12 without treating envtest as runtime-GC evidence.

## Purpose

CHILL has reusable internal functions for static compatibility, bounded power
observation, immutable Run validation, and profile-evidence admission. Its
managed Profiler process now coordinates Run preparation, one native Job, and
durable execution-evidence finalization, then invokes the existing single-Run
admission boundary without copying raw observations into Kubernetes status.

The first Profiler answers one bounded question:

> Can CHILL execute one exact, statically Compatible model path on one selected
> Node, retain trustworthy Run evidence, and report whether that single Run is
> admissible for later profile construction?

It does not search a batch curve or produce a `DeviceProfile`.

## Current flow

```text

ChillSystem desired state
  -> Helm installs Profiler runtime identity, permissions, and base configuration
  -> CHILL Operator manages the Profiler Deployment lifecycle

ProfilingRun desired state
  -> CHILL Profiler controller
  -> exact intent frozen
  -> fixed: owned Kubernetes Job
  -> non-fixed: owned NodeActuation handoff; Job withheld until convergence
  -> measurement Job executes runtime worker + MetricObserver helper
  -> immutable raw measurement object
  -> Profiler finalizes execution evidence from terminal Kubernetes facts
  -> single-Run admission result
  -> ProfilingRun status
```

The visible system changes are an operator-managed Profiler workload and the
`ProfilingRun` resources reconciled by that workload. `ChillSystem.status`
reports only whether the enabled Profiler component is available. It does not
duplicate individual Run state.

## First supported path

The first path deliberately fixes this boundary:

- one statically `Compatible` candidate;
- one selected Kubernetes Node;
- one explicitly selected, non-actuated `fixed` power mode;
- ONNX artifact with ONNX Runtime CPU Execution Provider;
- one fixed batch and one immutable measurement contract;
- one Job, one Pod attempt, and no retry;
- one edge-metrics Shelly source resolved from the selected Node;
- raw execution and power evidence; and
- `Accepted`, `Rejected`, or `Insufficient` single-Run admission.

Adding another runtime is not part of this implementation. The first adapter
must remain explicit instead of introducing an unproven generic plugin system.
Nodes with selectable power modes are rejected until CHILL can independently
observe and enforce their current mode; a manually asserted Jetson mode is not
accepted as Run evidence.

The follow-on model-loaded-idle path is another measurement mode of the same
CPU ORT Run, not another runtime and not a separate frozen request type. It uses
the same `profilingrun.Intent` family, so the existing preparation, one-Job ownership,
terminal finalization, and immutable evidence chain remain common. The runtime
result and raw-measurement schemas are mode-specific because active inference
and zero-inference idle have different completeness facts.

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
- the default Profiler Deployment configuration referenced by `ChillSystem`;
- the default trusted fact-source ConfigMap referenced by that Deployment
  configuration, including immutable OCI runtime and artifact workload images
  and the Node-to-edge-metrics map; and
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
  contract, and resolved metric-source identity into one immutable intent;
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
- reading and verifying the durable raw-measurement reference produced by the
  measurement worker;
- combining that verified object with terminal Job, Pod, Node, and image facts
  into one immutable execution-evidence record;
- applying compact status to the reconciled `ProfilingRun`; and
- running as a separate controller binary and operator-managed Deployment.

The controller is a time-bounded, idempotent control loop. Each reconciliation
reconstructs the Run from Kubernetes objects and durable evidence. It may make
compatibility, intent, Job, and admission decisions, but it does not poll power,
execute inference, hold a measurement window open, or retain authoritative Run
state in process memory.

### Runtime measurement worker owns

- running inside the one-shot `ProfilingRun` Job;
- executing the frozen runtime and artifact contract;
- coordinating the measurement window with the MetricObserver helper;
- validating and assembling only runtime and typed metric facts observable
  during execution without changing the frozen intent;
- writing one immutable raw-measurement object to the configured durable
  evidence sink;
- emitting a bounded receipt that identifies the committed object; and
- exiting with an execution result that Kubernetes records on the Job and Pod.

For model-loaded idle, the worker also owns the only authoritative window
declaration: after exact model load it completes the intent-bound settling
duration, opens a monotonic fixed-duration window without issuing inference,
and closes it while the model remains resident. Observer results never feed
back into start/end selection.

The Job is the execution plane. A Profiler controller restart or leader change
does not restart measurement in controller memory or create a second
measurement path.

### MetricObserver helper owns

- running beside the runtime worker in the same one-shot Job;
- invoking `internal/metricobserver` for the bounded frozen window;
- atomically handing one complete metric result to the runtime worker; and
- exiting without storage credentials or evidence-write authority.

### Existing leaf packages retain ownership

- `internal/modelcompat`: static artifact/runtime/DeviceClass compatibility;
- `internal/metricobserver`: bounded typed metric observation;
- `internal/profiling/run`: immutable intent and execution-evidence validation;
- `internal/profiling/derivation`: scientific admission and later derivation.

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
- exact trusted runtime declaration and immutable OCI image references;
- selected DeviceClass and target Node;
- selected power mode;
- fixed batch, warm-up, duration, repetitions, and CPU policy;
- metric-source selection by Node, not a caller-supplied endpoint; and
- admission-policy identity.

The Profiler resolves the selected Node to an edge-metrics source before it
freezes the Run intent. The resolved endpoint and source identity belong to the
intent and evidence, not to the stable DeviceClass or ModelSpec.

## Execution fact sources

The first path uses existing Kubernetes objects and one installation-time
Profiler fact-source ConfigMap. It does not introduce a runtime-declaration CRD
or put mutable runtime state in `DeviceClass` or `ModelSpec`.

| Fact | Authority | Frozen Run value |
|---|---|---|
| logical artifact and required execution path | referenced `ModelSpec` | object identity, artifact name, path, and immutable carrier image, format, runtime family, and backend |
| stable hardware class and declared power modes | referenced `DeviceClass` | object identity, architecture, and selected power-mode identity |
| trusted executable workload | fact-source ConfigMap referenced by Profiler Deployment configuration | declaration source object identity plus immutable OCI runtime and artifact workload images |
| selected machine | Kubernetes `Node` | name, UID, resourceVersion, architecture, and allocatable CPU |
| Shelly observation source | Node-keyed entry in the fact-source ConfigMap | resolved edge-metrics endpoint, source kind, metric, and configuration source object identity |
| single-Run admission policy | named entry in the fact-source ConfigMap | selected policy semantics and exact ConfigMap namespace, name, UID, resourceVersion, and key |
| worker execution envelope | DeviceClass, execution-path, and workload-mode entry in the fact-source ConfigMap | timeout margins, producer capacities, container resources, and exact ConfigMap namespace, name, UID, resourceVersion, and key |

The Profiler Deployment configuration contains only a local ConfigMap key
reference to these facts. The Operator passes that reference to the Deployment
but does not parse execution declarations or source entries. Helm installs the
default ConfigMap; an administrator may replace it only through installation
configuration, never through a Run request.

Changing fact-source content may affect new Runs but never rewrites an already
frozen Run. The Profiler retains the selected entries, ConfigMap namespace,
name, UID, resourceVersion, key, and all selected immutable image references in
the intent record.

Admission policy names are Run inputs, but thresholds are not. An administrator
defines the named policies in the installation-time fact-source ConfigMap. The
Profiler strict-decodes the catalog, selects the requested entry, verifies its
workload mode, and retains the selected policy plus exact source provenance in
the immutable intent record before creating a Job. ConfigMap changes therefore
affect only new Runs. Restart recovery reads the retained policy and never
re-resolves mutable policy configuration.

The admission-policy catalog selector and retained-record decoder return a
sealed policy value. Downstream admission and controller code receive detached
source/policy projections; they do not re-validate the same retained policy.

Before an intent record exists, a successfully read but malformed policy
catalog, an unconfigured selected name, or a selected policy whose workload
mode does not match the immutable Run is terminal for that Run. The controller
records `ExecutionComplete=False` with `AdmissionPolicyInvalid` instead of
returning a stateless reconcile error. A retry uses a new `ProfilingRun`; the
failed Run never starts a Job under policy semantics that were not frozen.

Worker timeout, capacity, and resource values are installation inputs, not Run
inputs. The Profiler strict-decodes the execution-policy catalog and selects
one entry by the already frozen DeviceClass, execution path, and workload mode.
An exact DeviceClass selector takes precedence over the explicit `*` fallback,
so device-specific envelopes do not require a new binary or a Run-level resource
override. The Profiler validates the envelope's resource and capacity
relationships, then retains its
selected policy and exact source provenance beside the intent before creating
a Job. Admission, execution, and optional actuation selections must come from
the same ConfigMap UID and resourceVersion; a concurrent catalog update
therefore fails closed instead of producing a mixed snapshot. The Job
annotation uses the selected policy's semantic identity; ConfigMap-only
revision changes do not create a new execution identity. Restart recovery never
consults mutable execution-policy configuration.

`ProfilingRun.spec.candidate.powerMode` is required. Candidate-report v1alpha7
verifies that it is declared by the selected `DeviceClass` and directly retains
the exact typed actuator target for a non-fixed mode. The Profiler selects a
configured actuation policy by DeviceClass, mode, and actuator kind; its
selected semantics and ConfigMap source enter the immutable intent record.
Fixed Runs continue directly. An
actuated Run records `ActuationHandoffPending` and creates no measurement Job,
because Lease ownership, read-back, settling, and restoration remain Plan 6
work. Thus target freezing is implemented without claiming selectable-mode
execution support.

The concrete adapters remain narrow:

- candidate resolver: `ModelSpec` + `DeviceClass` + trusted execution
  declaration -> pure static-compatibility evaluation and exact candidate
  report;
- Node resolver: exact API read and class-membership validation;
- metric-source resolver: selected Node -> configured edge-metrics source.

Caller-supplied image references, runtime declarations, and metric endpoints are
not accepted in `ProfilingRun`.

`ProfilingRun.spec.measurement.workloadMode` is the only execution-mode
discriminator. `idle-model-loaded` requires zero warm-up, a positive settling
duration, and `none` input/output protocols; CPU ORT keeps `batch=1`, while a
declared-batch-size artifact binds the first batch in its declared batch size set. Idle
is accepted only with `idle-wall-power-v1`. Active fields and
`wall-power-v2` remain unchanged. The controller projects these frozen facts
but does not implement runtime or raw-schema behavior itself.

Status remains compact and reconstructable:

- `observedGeneration`;
- Kubernetes-style conditions for execution completion and evidence admission;
- frozen intent record;
- owned Job reference and actual Node/Pod identities;
- evidence store/key reference, schema version, and byte size;
  and
- structured admission verdict and reason codes.

Raw latency arrays, power samples, full Pod snapshots, and large logs must not
be copied into CR status. No free-form `phase`, duplicated Node readiness, or
aggregate `message` field is introduced.

## Reconciliation flow

```text
1. Validate request, selected power mode, and referenced facts
2. Resolve the trusted execution declaration and evaluate exact static compatibility
3. Resolve the exact Node snapshot and Node -> edge-metrics source
4. Freeze all source identities, source object identities, and the Run intent
5. Create one deterministic, ProfilingRun-owned Job
6. Let its measurement worker execute and persist raw runtime + power evidence
7. Observe native Job/Pod completion and verify the raw-measurement receipt
8. Finalize immutable execution evidence with terminal Kubernetes facts
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
  -> internal/profiling/profiler/controller/    ProfilingRun reconciliation and status
  -> internal/profiling/profiler/               one-Run application orchestration
       +--> internal/modelcompat
       +--> internal/profiling/run
       +--> internal/profiling/derivation
       +--> narrow Kubernetes/source/evidence ports

runtime worker entrypoint
  -> internal/profiling/profiler/runner/        one-Job execution plane
       +--> explicit CPU ORT execution adapter
       +--> durable evidence sink

metric-observer entrypoint
  -> internal/metricobserver
       +--> edge-metrics adapter

api/v1alpha1/
  -> ChillSystem profiler configuration and availability
  -> ProfilingRun desired state and status

charts/chill/
  -> Profiler ServiceAccount and base ConfigMap
  -> namespaced Role / RoleBinding
  -> read-only ClusterRole / ClusterRoleBinding
```

`internal/operator/profiler` manages only the Profiler operand. It does not
import profiling-domain leaf packages. `internal/profiling/profiler` owns the application
sequence and narrow interfaces without Kubernetes objects. Native object
translation and controller-runtime behavior stay under
`internal/profiling/profiler/controller` and its adapters.

`internal/profiling/profiler/runner` does not import controller-runtime, the Operator, or
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

## Run-path checkpoints

### Checkpoint 1: request and immutability boundary

Decide the minimum `ChillSystem.spec.profiler` configuration, the minimum
`ProfilingRun.spec` references, and which resolved facts are frozen into the
intent. The APIs must avoid embedding Deployment details or adapter fixture
structure while preserving every identity required by current validation.

Recommended decision: one enabled Profiler operand per `ChillSystem` and one
Run in the CHILL release namespace with immutable execution inputs; no campaign
fields, namespace override, or mutable desired/current phase model.

### Checkpoint 2: Node-to-metric-source resolution

Define the narrow resolver contract and the exact Kubernetes facts that bind a
Node to the existing edge-metrics deployment. Resolution failure must be a
typed pre-execution outcome. The first implementation supports edge-metrics
only while keeping its endpoint out of caller-facing Run intent inputs.

### Checkpoint 3: durable evidence ownership

The raw measurement object and finalized execution-evidence record belong to
the CHILL-managed evidence-store operand, not to CR status, a Profiler
filesystem, or a Run Pod volume. The storage engine is a replaceable
S3-compatible dependency. The Operator manages its Kubernetes operating
resources; the Profiler consumes only the logical evidence-store contract. The
first ownership contract is:

- the measurement worker is the only raw-measurement writer;
- the Profiler controller is the only finalized execution-evidence writer and
  does not reconcile the storage operand;
- the raw object is authoritative for the exact retained runtime and power
  observations;
- Kubernetes remains authoritative for current Job, Pod, and Node state;
- their terminal snapshots become historical facts only in the finalized
  execution-evidence record;
- profile derivation consumes only evidence that the Profiler has retrieved and
  finalized, not an unverified worker result or container log; and
- human and research consumers read through the finalized reference without
  becoming writers to either object.

Each object reference contains the configured logical store, an opaque immutable
key, and a positive byte size. The finalized record contains the raw-measurement
reference. The compact finalized reference carried by Run status adds the schema
version. It contains no raw samples, endpoint credentials,
Secret data, or caller-selected storage URL. The selected store comes from
CHILL system configuration; a Run cannot redirect evidence to an arbitrary
endpoint.

The worker constructs the bounded raw-measurement object, records its size, and
performs an immutable create. It does not claim terminal Job or
Pod state, because those facts do not exist until the worker has exited. An
existing immutable key is reported without reopening and comparing its content;
the controller's semantic loader owns validation. The sink must not expose a
partial object as committed. After a successful create or existing-key result,
the worker emits only a small structured
receipt through the native container termination message. The receipt is a
handoff hint, not proof.

The controller trusts the handoff only after it:

1. observes the one owned Job and Pod attempt in a terminal state;
2. parses the bounded receipt from the terminated worker container;
3. retrieves the object through the configured evidence-store reader;
4. enforces the size bound, strict-decodes the schema, and verifies the Run and
   measurement relationships;
5. constructs an immutable execution-evidence record containing the verified
   raw reference and the observed terminal Job, Pod, Node, ownership, and image
   facts;
6. independently retrieves and verifies that finalized record; and
7. publishes its compact reference and evidence/admission Conditions.

Retrieval and verification produce an opaque evidence value. Scientific
admission consumes that value directly and does not repeat structural
validation inside the admission package.

Finalization uses a deterministic object key and a typed semantic record. Before
constructing a record, the controller checks that key and reuses an existing
valid record. When no record exists, it projects the required UIDs, ownership,
placement, requested and pulled image identities, termination states, native
completion timestamps, and bounded workload observation rather than
serializing entire live Kubernetes objects. Volatile `resourceVersion`, managed
fields, and unrelated metadata are not part of the record. A crash after the
immutable write therefore recovers the existing record instead of reconstructing
them from later cluster state.

Job success without a committed and verified raw object and a verified finalized
record is evidence failure. An upload failure or partial write terminates the
worker unsuccessfully and cannot produce an admitted result. Finalization is an
idempotent immutable write derived from already retained facts. Duplicate
reconcile events and Profiler restarts repeat retrieval, verification, and
finalization; they do not rerun measurement.

The first path does not cascade evidence deletion from `ProfilingRun` or
`ChillSystem` deletion and does not add evidence cleanup to the root finalizer.
Until a separately reviewed retention policy can prove that no live profile or
research lineage references an object, CHILL performs no automatic evidence
garbage collection. This conservative initial retention is acceptable for the
bounded single-Run path and avoids publishing references that silently expire.

This closes evidence responsibility and handoff semantics. The Evidence Store
contract supplies the immutable writer, independent reader,
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
Job/Pod, receipt, raw object, and finalized record without launching measurement
again.

Intent freezing distinguishes retryable infrastructure failures from terminal
input and installation-policy failures. Kubernetes API reads and writes,
including a fact-source ConfigMap lookup failure, remain retryable. A concurrent
fact-source revision observed between admission- and execution-policy reads also
remains retryable so the next attempt can select one coherent snapshot. Once a
catalog has been read successfully, malformed catalog data, an absent selected
entry, or a policy-to-intent mismatch is terminal and is published on
`ExecutionComplete=False` with `InvalidRunSpec`, `AdmissionPolicyInvalid`, or
`ExecutionPolicyInvalid`. The Condition and top-level `observedGeneration`
identify the processed immutable Run generation; no Job is created.

The public API must make these outcome boundaries observable without copying
native Job status, raw evidence, logs, credentials, or endpoints into the Run.
Exact Condition names, reason values, and whether a summary Condition is useful
are deferred to the reviewed `ProfilingRun` API schema. No free-form phase or
mutable cancel field is required by this checkpoint.

### Checkpoint 5: measurement worker contract

The concrete serialization, Job-local synchronization, raw-measurement write,
bounded termination receipt, and worker exit classes are fixed by the
[Measurement Worker contract](../components/measurement-worker.md). It retains
the validated runtime plus observer topology without adding an uploader
sidecar. Both durable writes use the same evidence-store contract and
distinct immutable object identities. Logs and controller memory are not
handoff paths.

The idle revision keeps that topology and finalization boundary. It advances
the frozen intent schema rather than adding a parallel intent type, and uses a
separate idle raw/runtime result so zero inference, settling completion, model
residency, and the runtime-declared window are explicit evidence rather than
sentinel values accepted by the active result validator.

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
5. Add `internal/profiling/profiler` ports and a deterministic one-Run orchestrator with
   fake adapters; do not add Kubernetes dependencies. Implemented for intent
   preparation, lifecycle decisions, and terminal evidence finalization.
6. Add the namespaced `ProfilingRun` API and schema tests. Implemented with an
   immutable spec, compact status, generated CRD, Helm CRD, and envtest gates.
7. Add the explicit power-mode input and Kubernetes/configuration fact adapters
   for the proven fixed-power CPU ORT path. Implemented.
8. Extend `cmd/profiler` with its `ProfilingRun` controller using the dedicated
   Helm-installed runtime identity and direct owner-based event mapping.
   Implemented through Job outcome and evidence status publication.
9. Add the one-shot measurement Job boundary, then integrate the validated CPU
   ORT runtime-worker and MetricObserver-helper behavior into it.
   Implemented and live-verified.
10. Add the selected durable evidence sink. Implemented and live-verified for
    raw and finalized execution evidence.
11. Sync CRDs and RBAC into Helm and verify independent Operator/Profiler
    permissions, configuration rollout, and upgrade behavior. Implemented for
    the execution/evidence path.
12. Verify failure, cancellation, identity drift, duplicate events, and both
    controller restarts with envtest and unit tests. Implemented and verified:
    duplicate reconcile, Profiler reconciler reconstruction, and recorded-Job
    loss now cross a real API server and status subresource. Restart between
    finalization and admission, plus the completed-admission no-op, are also
    covered. Recorded-Job replacement with a different UID is rejected without
    status mutation, as is controller-owner drift on the retained Job UID. The
    focused intent, evidence, and retained-loader drift gates remain green.
    Operator reconciler reconstruction over the retained Deployment is covered.
    The Run deletion fence and Job owner binding are covered in envtest, and
    native owner garbage collection is verified in the guarded Kubernetes 1.31
    kind e2e suite.
13. Run live Lattepanda active and model-loaded-idle measurements through the
    production evidence and admission path. Implemented and live-verified.
14. Demonstrate Profiler availability through `ChillSystem`, then the Run flow
    through `kubectl get/describe profilingrun`, its owned Job, and the evidence
    object reference. Implemented through the evidence object reference.
15. Expose and execute `idle-model-loaded` through the same Run/Job lifecycle,
    while retaining distinct runtime/raw completeness and idle admission.
    Implemented and live-verified on Lattepanda.

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
- the selected power mode is explicit and belongs to the selected DeviceClass;
- new Runs resolve only installation-time trusted execution declarations,
  while frozen Runs remain bound to the exact ConfigMap identity, declaration,
  and source-entry source object identities;
- Node-to-edge-metrics resolution is explicit and captured in the frozen intent;
- exactly one native Job/Pod attempt is attributable to the Run;
- strict evidence validation is covered by the current worker, evidence,
  restart, drift, and fail-closed tests;
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
- selectable power-mode observation or actuation
- caller-supplied runtime images, artifact images, or power endpoints
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
