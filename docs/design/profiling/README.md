# Runtime, Artifact, and Profiling Roadmap

## Status

Scientific admission has been removed. The current boundary is execution,
raw evidence storage, and numeric derivation; thermal/workload observations
are retained without an automatic accept/reject decision. See
[Plan 3](plans/03-profile-admission-derivation.md) for the current contract and
stored-result versions. The historical experiment descriptions below retain
the terminology and outcomes recorded at the time.


Roadmap for the implemented `ProfilingRun` path through durable
execution-evidence verification and measurement derivation. Execution evidence
uses `chill.dacs.io/profiling-execution-evidence.v1alpha15`; actuated evidence
retains the confirming transition identity, its target read-back, and its
completion time, and checks the target against the RunConfig. Permanently
invalid retained evidence produces the terminal `EvidenceInvalid` outcome.
Repeated active and idle trials, exact-state curve derivation, and the
`BatchEnergyOptimization` Quick-search control path are implemented. The
recorded Orange Pi RKNN campaign
used one artifact at batches one, two, and four, completed 12 accepted trials,
and produced `Stopped/NoFurtherImprovement`; production reconstruction matched
the retained incremental-work values exactly. Its execution evidence uses
v1alpha4, and the campaign and Run objects are archived outside the testbed
Evidence Store and cannot be reloaded by the current readers. The L4 result is
an archival scientific record, not current live or re-verifiable evidence, and
does not establish a universal `b_sat=1` or statistical confirmation. Its old
saturation outcome is not the contract of the current optimizer.
Campaign curve-plan resolution failures are exposed as `Failed` with
`MeasurementComplete=False`, reason `CurvePlanResolutionFailed`, and the actual
resolver error. The controller still returns the error for retry; a later
successful resolution resumes measurement rather than making this status sticky.
Recorded curve plans are read and decoded before consulting current facts;
configuration changes do not re-project or compare a campaign's frozen plan.
Transition recovery uses exact campaign-owned immutable operations and binds
their Node UID to the recorded plan. Before its first operation exists, only
that operation's active preparation is resolved, not a replacement curve plan.
Plan 9 is implemented: `NodeActuation` is a one-way transition independent of
every Run, a `BatchEnergyOptimization` creates and owns the one transition that
establishes its declared state, and a non-fixed Run executes only when the
newest successful transition on its target Node confirms its declared state.
The host-actuation DaemonSet uses host networking with `ClusterFirstWithHostNet`
DNS so its verified Kubernetes API client does not depend on edge Pod-CIDR routing.
Plan 10 is implemented and live-verified for the exact `jetsono` /
`mobilenet-v2-050` / `MAXN` and `MODE_15W` state pair. Two independent current
idle-baseline results provide the target-state `P_idle` values. A bounded Gate
0 restart trace retained 104 power samples without a failed read, and one
Complete `PowerModeTransitionCost` retained five Accepted observations in each
direction and its terminal two-row `tau`/`eta` table. The registered Profiler
controller owns one off-target observer Job and sequential `NodeActuation`
objects per attempt, with at most one attempt active, and writes the immutable
raw, finalized, and terminal result evidence.
The result is evidence only for that exact device, model, state pair, actuator,
meter, readiness witness, and protocol. All ten observations recorded
`servingSurvived=false`; the measurement does not establish a target-only cost,
another device's result, or scheduler behavior.
This document itself does not authorize later API or internal changes.

## How to use this directory

This file is the entry point for the profiling design. Do not read every file
as one flat specification.

```text
README.md
├── plans/         ordered delivery boundaries and current scope
├── components/    reusable subsystem contracts consumed by plans
└── foundations/   fact audits and reasoning records; no implementation authority
```

Use this reading order:

1. Read this roadmap to locate the current end-to-end boundary.
2. Read only the relevant document under `plans/` for authorized scope and
   current implementation status.
3. Follow a `components/` document when that plan depends on a reusable
   subsystem contract.
4. Consult `foundations/` only when claim provenance, rejected assumptions, or
   boundary rationale is needed.

The `Status` section in each document is authoritative for whether its scope is
implemented, approved but incomplete, or analysis only. Repository code,
tests, and live verification remain the source of truth for implementation
facts; a stale document status must be corrected rather than inferred around.

## Purpose

Keep the end-to-end direction visible without assigning the whole system to
one package or one implementation change.

```text
catalog facts
    |
    v
static model-path compatibility
    |
    v
one immutable profiling Run on Kubernetes
    |
    v
runtime evidence + metric evidence
    |
    v
measurement derivation and batch-energy optimization
    |
    v
future enumerator / scheduler
```

Each boundary is planned and approved separately. A later plan consumes the
previous plan's output without taking over its responsibility. If live
evidence exposes a bad earlier assumption, that earlier boundary is revised
separately.

## Profiling runtime input contracts

Every frozen RunConfig carries `delivery.artifactCapability.tensors`, including
canonical ONNX artifacts with no target platform or artifact-declared batch set.
Each tensor declares `name`, uppercase `datatype`, and batch-free
`sampleShape`; the selected measurement batch is added only at invocation.
The standalone ORT/RKNNLite request schemas are v1alpha15/v1alpha11; the native
firstfloor request is v1alpha1. RunConfig is v1alpha16, active runtime results are
v1alpha7 and model-loaded idle results are v1alpha5. Results record typed,
batch-free `inputs` and native results retain actual firstfloor runtime facts.
Readers consume the current schema and its explicit input declarations.

[CPU ORT](../../../runtime/ortcpu/runner.py) validates every declared input name,
datatype, rank, fixed sample dimension, and selected batch against session
metadata before either active or idle measurement. A symbolic runtime batch
axis accepts the selected batch; a fixed axis must equal it. Named multi-input
feeds support BOOL, UINT8/16/32/64, INT8/16/32/64, and FP16/32/64. Unsupported
datatypes fail explicitly. The `numpy-default-rng-seed-0-inputs-v1` generator
fills floating tensors with seeded standard-normal values cast to the declared
dtype and integer/boolean tensors with seeded zero/one values; the combined
input byte budget is admitted before allocating any feed.

[RKNNLite](../../../runtime/rknnlite/runner.py) supports one FP32 tensor with
rank-three NHWC sample dimensions. It validates the artifact input name,
datatype, shape, and selected static or dynamic batch through the delivered
`librknnrt` library before initializing the measured Lite2 context. This
temporary metadata context performs no inference and is destroyed before
measurement. Official
[RKNN C API metadata](https://github.com/airockchip/rknn-toolkit2/blob/master/rknpu2/runtime/Linux/librknn_api/include/rknn_api.h)
defines the queried structures and NCHW/NHWC layouts; NCHW metadata is explicitly
reordered to the NHWC invocation shape. Inference explicitly requests
`float32`/`nhwc`; unsupported contracts or unavailable metadata fail, including
for model-loaded idle runs.

The [RKNN exporter](../../../build/model-artifacts/rknn/export_rknn.py) accepts
one FP32 rank-four NCHW ONNX input with symbolic batch and positive fixed sample
dimensions. Conversion profiles retain that ONNX ordering, while conversion
record v1alpha4 preserves the source tensor name and FP32 datatype in
`conversion.tensors` with NHWC `sampleShape`. It does not emit the old anonymous
`inputSampleShape` field.

## Serving execution

**Status:** Implemented native runner and campaign/catalog integration.

Profiling Jobs explicitly invoke `/measurement-worker`; the shared runtime
image's serving entrypoint is not the measurement command.

Serving profiles follow the
[shared execution contract](../../spec/00_measurement.md#측정의-단위).
Reuse the Run and campaign controllers, repeated trials, power observation,
raw evidence storage, and numeric derivation. Runtime invocation and frozen
execution conditions use the same firstfloor actor, initialized batches and
actual ORT CPU/CUDA or RKNN backend as Serving. Fixed-K ACTIVE measurements
retain raw service/backend/native samples and actual execution IDs; IDLE keeps
the model resident with zero executed requests. Available SDK/provider/device,
physical GPU identity and RKNN ABI facts determine actual backend support.

## Serving resource allocation

The [measurement specification](../../spec/00_measurement.md) requires profiles
for resource allocations actually used by Serving. The scheduler selects only
measured execution configurations. CPU inference thread settings follow the CPU
allocation, and each serving instance executes one batch at a time; threads and
execution concurrency are not independent search dimensions.
Memory usage, including observed peaks, supplies placement-feasibility evidence,
not a memory-limit performance sweep. Account separately for system reservations
and serving memory not represented by the profiling workload.
The current implementation and the required alignment are distinct:

| Condition | Current implementation |
|---|---|
| CPU scheduling request | `ProfilingRun.spec.cpuRequest`, applied to the runtime container |
| CPU quota | Optional `spec.measurement.cpuQuota`, a positive whole-core count; omission uses Node allocatable CPU |
| CPU ORT threads | Intra-op threads fit the selected frozen affinity/quota; inter-op threads are one |
| CPU observations | Explicit `spec.measurement.cpuAffinity` freezes physical IDs; actual affinity and cgroup quota are separately read back |
| Memory | Runtime requests/limits come from the selected execution policy, not a per-Run measurement parameter |
| Accelerator execution | firstfloor ORT CUDA pins a physical GPU UUID; RKNN retains actual queried ABI/core capabilities; standalone ORT CPU/RKNN remain reference paths |

Sources: [public Run fields](../../../api/v1alpha1/profilingrun_types.go),
[CPU freezing](../../../internal/profiling/profiler/preparation.go),
[runtime resources](../../../internal/profiling/profiler/controller/execution_job_spec.go),
[CPU read-back](../../../runtime/ortcpu/runner.py),
[execution policy](../../../internal/profiling/profiler/execution_policy_types.go),
and [runtime Job projection](../../../internal/profiling/profiler/controller/runtime_job_projection.go).

The CPU contract already participates in
[execution-state identity](../../../internal/profiling/profiler/projection/identity.go)
and [curve conditions](../../../internal/profiling/profiler/projection/curve_plan.go).
Different quotas and physical CPU masks are distinct measurements.
[BatchEnergyOptimization Run construction](../../../internal/profiling/campaign/controller/run_builder.go)
passes its optional `spec.cpuQuota` and explicit `spec.cpuAffinity` to ACTIVE
Runs; matching idle baseline requests carry the same selected physical mask.
Default omission keeps the existing Node-derived quota behavior.

Quota controls CPU time; affinity identifies physical CPUs. Exclusive allocation
also requires compatible native Pod/Node policy and observed assignment.
The stored CPU contract records `ExclusivityClaimed=false` for an affinity-only
binding. GPU/RKNN hardware campaign verification remains a separate device run.

## Split plans

### 1. Catalog and static compatibility

[plans/01-catalog-static-compatibility.md](plans/01-catalog-static-compatibility.md)

Answers only:

> Is this immutable model artifact and runtime path statically compatible
> with this stable DeviceClass?

The first read-only CPU ONNX Runtime path is implemented. Its output is a
static candidate report, not permission to execute profiling.

### 2. Profiling Run and Kubernetes execution — implemented first path

[plans/02-profiling-run-execution.md](plans/02-profiling-run-execution.md)

Answers only:

> Did Kubernetes and the selected runtime realize one immutable profiling
> decision, and what execution evidence was observed?

Its first CPU ONNX Runtime path is implemented through an immutable public Run,
one native Job attempt, raw evidence persistence, and finalized execution
evidence. Active and model-loaded idle are live-verified through the same
lifecycle.

```text
ProfilingRun.spec
  -> resolve exact RunConfig and selected policies
  -> owned Job records and executes them
  -> raw measurement {RunConfig, measurements}
  -> final evidence {RunConfig, raw reference, terminal facts}
```

The Job is the single recoverable execution record. CHILL creates no separate
Run-record ConfigMap and exposes no intermediate resolution status phase.

### 3. Profile measurement derivation — implemented

[plans/03-profile-admission-derivation.md](plans/03-profile-admission-derivation.md)

Completed Run evidence is projected into measurements and used to calculate
idle power, incremental energy, repeated-trial statistics, and batch curves.
There is no scientific admission policy or accept/reject stage. Raw readings
and environmental observations remain available for the experimenter.

### 4. Minimal Profiler control path — implemented

[plans/04-minimal-profiler-control-path.md](plans/04-minimal-profiler-control-path.md)

The Operator manages the Profiler operand. A Run resolves its execution
configuration, executes one owned Job, and completes after raw and finalized
evidence are stored. Single-Run scientific admission in the historical Plan 4
is superseded by Plan 3. Execution errors and structural evidence-boundary
checks remain; there is no extra scientific verdict in Run status.

### 5 and 5a. Saturation designs — superseded by the energy objective

[Plan 5a](plans/05a-sequential-batch-stop.md) and
[Plan 5](plans/05-batch-saturation-campaign.md) preserve the former saturation
research design. They do not describe the current API or production control
path. Plan 7 now minimizes the measured energy objective directly and makes no
`b_sat` claim.

### 6. Execution-support expansion — active

[plans/06-execution-support-expansion.md](plans/06-execution-support-expansion.md)

Answers only:

> Which exact Node, model, runtime/backend, power-mode, and workload-mode rows
> does production support, and which smallest vertical slice raises the next
> row to measurement-ready?

P0 defines the L0--L4 vocabulary and exact support matrix. Raspberry Pi 5 CPU
ORT is paused at L2 on a physical electrical blocker. Orange Pi CPU ORT has an
L3 result. The recorded Orange Pi RKNN campaign used one artifact for three
active Runs at each of batches one, two, and four plus three batch-one
model-loaded-idle Runs. All 12 trials were accepted, and the campaign produced
`Stopped/NoFurtherImprovement`; production reconstruction matched every stored
incremental-work value exactly. This is a recorded L4 result, but its execution
evidence uses v1alpha4, and the campaign and Run objects are outside the testbed
Evidence Store. The strict current readers cannot reload them. It does not
establish a universal `b_sat=1` or statistical confirmation.
[ADR 0016](../../adr/0016-declared-batch-size-artifact-capability.md)
governs the declared-batch-size artifact capability used by future campaigns.

Runtime-specific adapters remain explicit. [ADR 0015](../../adr/0015-node-power-actuation-boundary.md)
governs the actuation safety boundary,
[ADR 0018](../../adr/0018-transition-ownership-boundary.md) separates transition
ownership from the Run, and
[ADR 0017](../../adr/0017-power-mode-reachability-boundary.md) makes every
catalogued mode reachable: the actuator applies a mode the device takes in place
and restarts the Node into one the device establishes at boot. Execution evidence v1alpha9
binds the confirming transition identity, target observation, and completion
time to the Run, checks the target facts, and gives permanently invalid
evidence the terminal `EvidenceInvalid` outcome.

One `jetson-agx-orin-64g` CPU ORT comparison measured
`mobilenet-v2-050`, batch one, for 75 seconds, with three admitted active and
three admitted model-loaded-idle Runs in each state. The exact aggregates are
recorded in [plans/06](plans/06-execution-support-expansion.md). From those, `MAXN` provides
1.326x the throughput at 1.714x the incremental energy per item, while mean
and p99 latency fall to 0.754x and 0.763x. The two idle means differ by 0.0334
W, so idle observations alone do not characterize the knob. Both power-mode
slices have recorded L3 results. Their source Runs use superseded v1alpha4 and
are archival rather than current-code-reloadable proof. They contain Run
measurement evidence, not separate transition evidence. The experiment does
not establish another Node, model, execution path, batch size, RKNN path, GPU
path, or L4 campaign result. Catalog presence and standalone smoke are not
production execution evidence.

### 7. Batch-energy optimization control path — implemented

[plans/07-batch-energy-optimization-control-path.md](plans/07-batch-energy-optimization-control-path.md)

Answers only:

> Can one declared `BatchEnergyOptimization` find the measured batch with the
> lowest incremental energy per item without a human step between Runs?

The one-dimensional Quick search advances on every strictly lower mean
`incrementalWorkJoulesPerItem`, however small the gain. The first non-improving
neighbor closes a local minimum; exhausting the declared search space is a
separate successful completion. The API exposes only `trialsPerBatch`, not a
method selector or saturation thresholds. `ProfilingRun` remains independently
submittable, and the plan adds no serving or scheduler behavior.

### 8. Declared-batch-size artifacts — implemented

[plans/08-declared-batch-size-artifacts.md](plans/08-declared-batch-size-artifacts.md)

Answers only:

> Is one batch size per immutable artifact a property of the runtime, or of
> CHILL's own conversion step?

It is CHILL's own. RKNN-Toolkit2 enumerates several batch shapes into one
artifact, and an RK3588 probe confirmed one artifact executing batches 1, 2, and
4 while refusing an undeclared batch. Every artifact declares its ordered
non-batch input shape. Data type is owned by the runtime implementation and
checked against the loaded artifact instead of copied from model registration.
A derived artifact additionally supplies the finite set fixed by its conversion
as an artifact constraint. A canonical ONNX artifact with a symbolic
batch axis leaves the search set to `BatchEnergyOptimization.spec.batchSizes` because
ONNX Runtime takes batch as a call argument. Exactly one source supplies the
recorded plan. Its exhaustion is a successful `SearchSpaceExhausted` optimizer
result. One artifact defines one state key and one model-loaded-idle baseline.
ADR 0016 records this decision.

### 9. Transition extraction — implemented

[plans/09-transition-extraction.md](plans/09-transition-extraction.md)

Answers only:

> How can a separately owned transition establish a state that many Runs
> consume without placing the transition inside any measurement window?

`NodeActuation` is a one-way operation toward a declared target, with an
immutable deadline bounding the transition through verified settling. `Ready`
is terminal and releases the Node Lease while leaving the established state in
place. One `BatchEnergyOptimization` owns one `NodeActuation` and emits its
creation as one optimizer step before creating any Run. A non-fixed Run
selects the most recent successfully completed
`NodeActuation` on its exact target Node and records that transition before it
creates a Job. If no completed transition exists, or the newest one established
a different mode, the Run ends with `RequiredStateNotConfirmed` and creates no
Job. Fixed Runs execute directly. The Run never creates, owns, or repairs a
transition.

Confirmation is intentionally allowed to be stale. Nothing else changes Node
power state on this testbed, the operating procedure permits no transition
during a measurement sweep, and only one campaign operates on a Node at a time.
No epoch, fence, claim, re-observation, concurrent-campaign guard, or tenure
protocol enforces those assumptions.
[ADR 0018](../../adr/0018-transition-ownership-boundary.md) owns the boundary.
The plan excludes a new resource kind, epochs, fences, tenure, successor
protocols, recovery state machines, compatibility readers, and the
transition-cost measurement that later consumes the extracted operation.

### 10. Power-mode transition-cost measurement — approved for implementation

[plans/10-transition-cost-measurement.md](plans/10-transition-cost-measurement.md)

Answers only:

> How can one real state transition produce `tau_(s->s')` and
> `eta_(s->s')` from the exact same observation window, including across a
> Node restart?

The first scope fixes one device type and model and measures one selected
pair of power modes. One `PowerModeTransitionCost` directly owns a bounded
off-target observer and creates one `NodeActuation` at a time; no additional
per-transition resource exists. Each `transitionWindow` closes when independent
target read-back and the frozen workload-readiness witness have both succeeded.
The same boundaries produce duration, wall joules, and signed eta, while exact
workload identity records whether serving survived. Raw transition observations
and finalized results remain in the Evidence Store. Physical continuity and
clock alignment are the first gate; repeated observations produce the directed
state-pair table, while profile publication and scheduler use remain later
work.

The transition-cost core derives the finalized observation directly from retained
raw evidence. Preparation has one stored representation, and readiness evidence
consists of the pre/post Ready process witnesses and receipt brackets rather than
an additional event history. Preparation and raw schema `v1alpha4` are local-only
changes; the recorded live evidence still uses `v1alpha3` (see Plan 10).

The independent `ModelLoadedIdleBaseline` producer is implemented for Plan
10's `P_idle(s')` input. Before its first model-loaded-idle `ProfilingRun`, it
owns one `NodeActuation` that establishes a mutable declared state; a fixed
selection creates no actuation. It then runs one `ProfilingRun` at a time until
the configured admitted-count target or attempt limit and writes a Complete
immutable aggregate containing the exact measurement state, derived
`IdleBaseline`, and accepted final-evidence references. An Insufficient request
closes only in compact status and does not fabricate an aggregate result. The
transition-cost preparation lists the namespaced baseline requests for each
selected mode and requires exactly one current Complete result whose stored
measurement state matches the target. It then freezes that result's mean and
full stored reference. The request names only the two power modes; installation
configuration supplies the unique actuation policy for each selected mode and
the logical Evidence Store used for raw, finalized, and aggregate objects.
Each baseline request also selects a required whole-core CPU quota. Its child
Run transports that selection to preparation, which freezes it in the existing
CPU quota selection as `ConfiguredCPUQuota/v1`; the quota is an execution-protocol
condition retained by Run evidence, not another baseline-state or transition-
cost compatibility dimension.

## Component contracts

These documents define reusable internal subsystem boundaries. They support the
plans but do not expand a plan's authorization:

- [MetricObserver](components/metric-observer.md) owns bounded typed power and
  thermal observation, not scientific policy or profile derivation.
- [WorkloadObserver](components/workload-observer.md) defines bounded
  measurement-window Pod-membership evidence, external-environment stability,
  and concurrent CHILL measurement detection.
- [Evidence Store](components/evidence-store.md) owns immutable evidence object
  storage and verification boundaries. Its Profiler-consumption plan remains
  separately review-gated.
- [Measurement Worker](components/measurement-worker.md) fixes the first
  CPU-ORT Job-local execution, synchronization, raw-write, and receipt contract.

## Supporting records

The following documents are background analysis, not active implementation
plans or competing architecture authorities:

- [Step 1: runtime execution fact audit](foundations/runtime-execution/01-fact-audit.md)
- [Step 2: runtime execution commonality](foundations/runtime-execution/02-commonality.md)
- [Step 3: runtime execution provenance](foundations/runtime-execution/03-provenance.md)
- [Step 4: compatibility and execution boundary](foundations/runtime-execution/04-compatibility-execution-boundary.md)

## Stable ownership rules

- `DeviceClass` describes stable hardware-class facts, not live Node or
  runtime state.
- `ModelSpec` registers logical models, immutable OCI artifact carriers, and
  stable execution-path requirements, not execution results.
- Kubernetes owns admission, placement, allocation, Job retry, and Pod
  lifecycle.
- Helm owns installation-time identities, permissions, and base configuration;
  controllers are not granted authority to create or bind their own RBAC.
- CHILL owns its desired Run decision, runtime-specific validation, evidence
  storage, and derived scheduler facts.
- The Profiler controller is a time-bounded, reconstructable control plane.
  The one-shot profiling Job is the execution plane that runs inference,
  receives typed metric observations, and persists raw evidence.
- `internal/metricobserver` remains a bounded raw-observation library. It does
  not own profiling policy or batch-energy optimization.
- SLO classification consumes an admitted profile; it does not decide whether
  Layer-0 measurement evidence is scientifically valid.

## Dependency rule

```text
operator / Profiler controller entrypoints
        -> Kubernetes adapters
        -> application orchestration
        -> plain domain leaf packages

one-shot runtime worker
        +-> runtime-specific execution adapter
        +-> durable evidence sink

bounded MetricObserver helper
        -> internal/metricobserver
        -> edge-metrics adapter

profile derivation
        -> bounded power-window integration leaf

model-loaded idle baseline controller
        -> retained idle ProfilingRun evidence + measurement derivation
        -> immutable Complete aggregate result

transition-cost derivation -> bounded power-window integration leaf

transition-cost controller
        -> model-loaded idle baseline result contract
        -> immutable Evidence Store reader
        -> native attempt Job projection + Evidence Store delivery binding

transition-cost Kubernetes observer -> API status facts + transition-cost derivation

transition-cost worker -> bounded MetricObserver + transition-cost derivation + immutable Evidence Store writer
```

Pure packages do not import `api/v1alpha1`, Kubernetes APIs, or
controller-runtime. Kubernetes adapters and execution entrypoints may import
pure packages, never the reverse. Reconciliation derives its next action from
durable objects and does not perform measurement or retain authoritative Run
state in process memory.

## Not authorized by this roadmap

- a shared `ExecutionContract` package
- a generic runtime plugin, registry, planner, or validator framework
- a compatibility controller or materialized compatibility status
- Node runtime/driver state in `DeviceClass`
- profile status or scheduler integration

Those decisions belong to the plan whose concrete input and consumer require
them.
