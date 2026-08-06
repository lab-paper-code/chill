# Runtime, Artifact, and Profiling Roadmap

## Status

Roadmap with the `ProfilingRun` path through durable execution-evidence
verification and single-Run admission implemented. Repeated active and idle
trials, exact-state curve derivation, and the first pre-frozen Plan 5a decision
are also implemented and live-verified for one two-member RKNN family.
The first frozen Plan 5 campaign closed with a typed budget-insufficient result;
an accepted statistical confirmation and profile publication remain incomplete.
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
profile admission and b_sat derivation
    |
    v
future enumerator / scheduler
```

Each boundary is planned and approved separately. A later plan consumes the
previous plan's output without taking over its responsibility. If live
evidence exposes a bad earlier assumption, that earlier boundary is revised
separately.

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

### 3. Profile admission and derivation — implemented first read-only path

[plans/03-profile-admission-derivation.md](plans/03-profile-admission-derivation.md)

Answers only:

> Are the execution and metric observations sufficient to admit a profile, and
> what energy curve and `b_sat` follow from that evidence?

Its first pure read-only plan is approved against the retained real Run
evidence. Persistence remains deferred until the output contract survives
adversarial and live-evidence review.

### 4. Minimal Profiler control path — first bounded path implemented

[plans/04-minimal-profiler-control-path.md](plans/04-minimal-profiler-control-path.md)

Answers only:

> Can an operator-managed CHILL Profiler own one exact ProfilingRun from a
> compatible request through durable evidence and single-Run admission?

The Operator manages a separately deployed Profiler operand and reports its
component availability. The Profiler resolves trusted facts, freezes one Run,
creates one owned Job, verifies the worker's durable raw object, finalizes
terminal Kubernetes evidence, independently reloads it, applies the named
single-Run admission policy, and publishes compact execution, evidence, and
admission status. Model-loaded-idle request, evidence, and admission dispatch
are live-verified. The scope does not orchestrate a batch
campaign, derive `b_sat`, publish a `DeviceProfile`, or connect to the
scheduler.

### 5a. Sequential batch sweep with early stopping — implemented and live-verified

[plans/05a-sequential-batch-stop.md](plans/05a-sequential-batch-stop.md)

Answers only:

> Walking batch sizes upward, when has energy per item stopped improving enough
> to keep going?

The pure family-contract, Loader-verified family, exact-state curve, and
sequential-stop boundaries are implemented. A pre-registered two-member RKNN
campaign retained, verified, admitted, and composed all 12 post-freeze Runs,
selected batch one, and stopped at the batch-three capability boundary. The
result is a decision without a confidence statement; its selected batch is the
candidate Plan 5's confirmation pass takes as input.

### 5. Batch-saturation campaign — pure derivation implemented

[plans/05-batch-saturation-campaign.md](plans/05-batch-saturation-campaign.md)

Answers only:

> Under what predeclared rule does a set of Runs establish batch saturation for
> one frozen curve family?

Approved scope with pure derivation and the first frozen campaign completed.
Plan 5a supplies the frozen family, verified family, and exact-state curve
container. The Plan 5 point estimator and retained previous interval are
implemented, and the global post-selection error allocation is derived and
regression-tested. The deterministic domain-wide discovery screen is also
implemented, as is the global-`H` confirmatory sizing rule. The final joint
contrast/covariance estimator and typed confirmatory result are implemented.
The discovery and confirmatory policies have strict canonical decoders and
content digests, the second artifact is checked against the realized sizing
allocation, and the final result binds both digests. The first concrete Plan 5
campaign retained and re-verified all 12 idle and discovery Runs for the
two-member RKNN family. Discovery selected batch one, but the pre-frozen
three-trial cap could not meet the simultaneous 5% precision rule. Its committed
second policy and final result therefore record
`Unavailable: ConfirmatoryBudgetInsufficient` and allocate no confirmatory Runs.
No statistical `b_sat` claim was made. A disjoint second campaign is frozen
with a pilot-sized six-trial cap and 18-active-Run budget; none of the first
campaign's evidence is pooled into it. Its own live discovery selected batch
one and froze four confirmatory Runs at each of batches one and two before any
of those Runs were submitted. All eight were then retained and admitted, but
the final 6.395% remaining-improvement upper bound exceeded the frozen 5% rule.
The typed outcome is `CandidateOnly: DomainNoImprovementNotConfirmed`, not an
accepted `b_sat`.
Campaign provenance is pre-registered in a committed record rather than
machine-enforced; that boundary is stated in the plan. It does not authorize a
`DeviceProfile` API, a campaign controller, an intent schema change, or scheduler
consumption.

### 6. Execution-support expansion — active

[plans/06-execution-support-expansion.md](plans/06-execution-support-expansion.md)

Answers only:

> Which exact Node, model, runtime/backend, power-mode, and workload-mode rows
> does production support, and which smallest vertical slice raises the next
> row to measurement-ready?

P0 records the L0--L4 vocabulary and exact support matrix. Raspberry Pi 5 CPU
ORT remains paused at L2 on a physical electrical blocker. Orange Pi CPU ORT
and two immutable RKNN fixed-batch rows are L4. ADR 0014 closes the
fixed-artifact curve-composition decision. Batch one was rebuilt and both
members were remeasured under the same recipe, catalog, and image generation,
so aligned L3 evidence exists. The pure Plan 5a family/stop core and first
frozen campaign then produced and re-verified all 12 post-freeze Runs, selected
batch one, and stopped at the batch-three capability boundary. That result
promotes only the two exact RKNN rows to L4; it is not a statistical `b_sat`
claim.
Runtime-specific adapters remain explicit. [ADR 0015](../../adr/0015-node-power-actuation-boundary.md)
assigns mutable power-mode ownership to a dedicated per-Node actuator operand.
The Profiler now creates the durable `NodeActuation` handoff from frozen Run
inputs, and the actuator boundary strictly projects only an exact local
Node name/UID match. The pure apply/read-back/settle/restore decision core and
exact Node-UID Lease primitive are implemented. An internal status reconciler
now drives those primitives against the durable handoff, and a fail-closed
workload-safety adapter reports exact active Pod blockers outside explicit
namespace and Pod-UID allowances. The reconciler also rejects an uninstalled
actuation-policy digest before any Lease, safety, or host port and enters the
same restoration path if policy authorization drifts after state was retained.
The opt-in command and Operator/Helm DaemonSet lifecycle are now implemented,
with exact Node and self-Pod identity supplied through the downward API, shared
Go policy canonicalization, namespaced Lease/handoff RBAC, and a deletion gate
that retains the operand while any handoff remains unfinished or unrestored.
The first Jetson NVPModel host adapter now executes only a configured argv,
parses an independent name/ID query, and keeps command completion distinct from
controller read-back. Helm declares the exact binary, library, configuration,
state, and sysfs mounts without privileged mode or added capabilities. The
Profiler now creates a non-fixed Run's measurement Job only after verified and
settled target state, advances the handoff to restoration after a terminal Job,
and withholds evidence finalization until ordered restoration is proven.
Actuated finalization retains the exact Run/handoff identities, typed
previous/target/restored observations, and lifecycle timestamps in execution
evidence v1alpha2 while fixed Runs remain on v1alpha1. The chart remains
disabled by default. The opt-in AGX Orin CPU ORT MODE_50W slice has now produced
three sequential accepted active Runs and three sequential accepted
model-loaded-idle Runs on current images. Retained reload and the production
projections proved one shared execution-state identity and one shared
owner-bound environment identity across all six, so both exact rows are L3. A
read-only derivation produced the first batch-one incremental-energy estimate,
but no multi-batch campaign or L4 claim follows from it.
Catalog presence and historical standalone smoke are not production execution
evidence.

## Campaign records

[campaigns/README.md](campaigns/README.md) indexes immutable offline campaign
inputs and post-run evidence lists. A record does not expand the scope of its
owning plan and is not controller state.

## Component contracts

These documents define reusable internal subsystem boundaries. They support the
plans but do not expand a plan's authorization:

- [MetricObserver](components/metric-observer.md) owns bounded typed power and
  thermal observation, not evidence-admission policy or profile derivation.
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
- [Step 5: execution support matrix](foundations/runtime-execution/05-support-matrix.csv)
- [Execution-support validation ledger](foundations/runtime-execution/validation-ledger.md)

## Stable ownership rules

- `DeviceClass` describes stable hardware-class facts, not live Node or
  runtime state.
- `ModelSpec` describes logical models, immutable artifacts, and stable
  execution-path requirements, not execution results.
- Kubernetes owns admission, placement, allocation, Job retry, and Pod
  lifecycle.
- Helm owns installation-time identities, permissions, and base configuration;
  controllers are not granted authority to create or bind their own RBAC.
- CHILL owns its desired Run decision, runtime-specific validation, evidence
  admission, and derived scheduler facts.
- The Profiler controller is a time-bounded, reconstructable control plane.
  The one-shot profiling Job is the execution plane that runs inference,
  receives typed metric observations, and persists raw evidence.
- `internal/metricobserver` remains a bounded raw-observation library. It does
  not own profiling policy or `b_sat`.
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
