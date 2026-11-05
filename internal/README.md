# Internal package map

CHILL groups production packages by measured capability first and uses technical
layers only inside that capability. Start from a capability below instead of
treating every directory under `internal/` as an equal entry point.

## Capability entry points

| Capability | Start here | Boundary |
|---|---|---|
| Profiling | [`profiling/`](profiling/) | Immutable Runs, batch campaigns, numeric derivation, evidence verification, and Job execution |
| System operator | [`operator/`](operator/) | `ChillSystem` reconciliation and operand desired state |
| Node actuation | [`nodeactuator/`](nodeactuator/) | One verified Node power-state transition |
| Operator query | [`queryapi/`](queryapi/) | Read models, Kubernetes queries, and the read-only HTTP API |
| Evidence storage | [`evidencestore/`](evidencestore/) | Immutable object identity, verification, and storage adapters |
| Device facts | [`deviceclass/`](deviceclass/) and [`modelcompat/`](modelcompat/) | Stable hardware facts and pure static model/input/native-setting compatibility |
| Observation | [`metricobserver/`](metricobserver/) and [`workloadobserver/`](workloadobserver/) | Bounded metric and workload evidence collection |
| Model registration | [`modelcatalog/`](modelcatalog/) | Shared API-to-model projection and registration validation |
| Serving | [`serving/`](serving/) | Immutable applied definitions, admission, bounded queues, instance routing, physical drain and private ObservationLog |

Small support packages remain at the top level because they do not define a
separate product capability: `component` owns stable component names,
`metadata` owns shared Kubernetes metadata, `kubeconfig` owns client
configuration, and `nodeprobe` owns bounded host discovery.

## Profiling layout

```text
profiling/
├── campaign/              BatchEnergyOptimization state machine
│   ├── controller/        campaign reconciliation and owned child resources
│   └── evidence/          retained campaign result, plan and trial reconstruction
├── run/                   immutable Run contracts, Node grant reader and Kubernetes projection
├── derivation/            numeric curve, stopping, and statistical derivation
├── idlebaseline/
│   └── controller/        idle-baseline attempts and publication
├── transitioncost/        measured power-mode transition contracts
│   ├── controller/        transition-cost reconciliation
│   ├── kube/              Kubernetes transition adapters
│   └── worker/            transition measurement execution
├── powerwindow/           power-window contracts and integration
└── profiler/
    ├── controller/        ProfilingRun reconciliation, status, and owned writes
    ├── evidence/          retained evidence validation and sealed records
    │   └── kube/          Kubernetes evidence projection and loading
    ├── kube/              trusted Kubernetes fact and policy readers
    ├── projection/        model/input and execution projections
    └── worker/            one-Job runtime execution plane
```

The executable composition root is [`../cmd/profiler/`](../cmd/profiler/).
It assembles the controller and Kubernetes readers; neither package constructs
the other.

## Serving layout

```text
modelcompat/              pure tensor and runtime compatibility contracts
modelcatalog/             shared ModelSpec projection and validation
firstfloor/               CHILL-leaf native actor, batching, IPC and Container value projection
serving/                  scheduler, admission, queues and runtime contracts
├── application/          scheduler configuration, profiles and observation orchestration
├── state/                operation state, scopes and native-service contracts
├── operations/           preparation, routing, lifecycle and measurement operations
├── management/           model/workload registration and management views
├── measurement/          retained measurement evidence and occupancy services
├── observation/          private ObservationLog, JSONL/S3 storage and queries
├── live/                 live DTOs, hub, metrics and WebSocket transport
├── httpapi/              authenticated HTTPS query, management and inference
├── kube/                 Kubernetes readers/writers, authorization, measured
│                         catalog and current Node/Pod/native observations
└── firstfloor/           typed native HTTP client and correlated progress facts
```

[`../cmd/chill-server/`](../cmd/chill-server/) assembles concrete
adapters. The `serving` root imports profiling derivation, not Profiler
controllers or candidate resolvers. Subpackages such as `application` and
`kube` also consume Profiler execution and evidence adapters.
The native runtime is firstfloor with explicit ORT CPU/CUDA or RKNN execution,
per-UID/definition batch isolation and shared actor/replay batching decisions.
Reviewed allowed directions and current `go list` edges are recorded in the
[architecture contract](../docs/design/code-intelligence/README.md#package-architecture-contract).

The serving root keeps capacity judgments in `scheduler_capacity_*`, search in
`scheduler_capacity_search.go`, and shared costs in `scheduler_capacity_cost.go`
and `scheduler_cost_baseline.go`. `scheduler_replay_*` owns replay simulation,
with shared snapshot checks in `scheduler_validate.go`.
In `serving/kube`, `scheduler_application_capture.go` assembles live sources,
`scheduler_state_*` projects and reconciles Kubernetes state and effect proofs,
`scheduler_operations_launch.go` launches operations, and
`scheduler_capacity_admission.go` judges the physical budget.
`serving/state` owns operation state and scopes; `serving/operations` owns
planning and lifecycle, with preparation contracts in
`operations_prepare_contract.go`. In `serving/observation`, `observation_log_*`
separates records, query reduction, durable storage and archiving; local capture
and scanning use `observation_log_read.go`. Legacy DOM evidence and
operation-cost measurement paths remain supported.

Capacity decisions retain the live search trajectory at
`schedulerLine.decision.capacity.search.steps` in the existing ObservationLog
scheduler JSONL stream (no separate log or schema bump).
Each `keep`/`empty` start has an origin (`k: 0`, `evaluated: 0`); the unchanged
aggregate excludes initial evaluations. Later entries are accepted moves only.
Across both starts, sum of step `evaluated` plus `search.trailingEvaluated`
equals `search.evaluated`: trailing counts every unaccepted sweep, including
intermediate k=2 local optima. Both origins precede k=2 then k=3 moves; a
neighborhood without an accepted move has no entry.

Step fields are `start`, `k`, `changedNodes` (existing name/UID references),
`joules`, `feasible`, `final`, `evaluated`, `durationNanos`, and optional
`budgetCut`. Times are measured on the main goroutine around each origin
evaluation or accepting sweep; their sum is at most `search.durationNanos`.
Compare consecutive points of the same start using the actual `better` order
(feasible, final, descending exceedances, then joules/keep/key), not energy
alone. Energy cannot increase between feasible points but may increase when
improving infeasible points. `final` means final-capacity feasible, not the
selected endpoint. `terminationReason` is `noImprovement` or `budgetCut`;
errors return no persisted decision. The last recorded point alone carries
`budgetCut: true` when the search is cut, even if another start wins. Cuts
retain the prefix, including improvements accepted from partial sweeps.

`TestCapacitySearchStepRecordSize` measures the existing eight-Node,
three-Workload evaluation fixture (two power modes on the Jetson stand-ins):
15 steps added 3,021 JSON bytes per decision (3,083 bytes total search JSON).
Timing digit counts vary between runs. At one decision every 10 seconds,
this is approximately 26.1 MB/day of additional uncompressed JSON.
This is not a size cap: accepted moves are retained through the search budget,
and larger trajectories increase serialization/write work under the
ObservationLog lock at every decision (normally every 10 seconds).

## Layer vocabulary

- Capability contracts and decisions remain independent of Kubernetes where
  practical. `profiling/run` also contains a Node grant reader and Kubernetes
  value projections; a capability-root location alone does not imply purity.
- `controller` contains controller-runtime reconciliation and durable writes.
- `kube` contains Kubernetes-backed readers, repositories, or projections that
  implement a consumer-owned contract.
- Provider-specific adapters use the provider name, such as `s3`, `jetson`, or
  `edgemetrics`.
- `cmd/**` packages are composition roots. Wiring permission there does not
  relax dependency direction inside `internal/`.

The intended direction is:

```text
cmd composition root
    -> controller and external adapters
    -> application orchestration
    -> plain capability contracts
```

Do not introduce `common`, `shared`, or `util` packages as a placement shortcut.
A reusable package needs one named measured quantity or platform responsibility
and at least one concrete consumer.

## Verification

Actual imports come from `go list`; allowed imports come from
[`../.go-arch-lint.yml`](../.go-arch-lint.yml). After moving or adding a
production package, update the architecture mapping in the same change and run:

```sh
make arch-check
make gopls-check
make test
```

Controller event delivery requires its exact `For`, `Owns`, or `Watches`
registration plus focused tests. A directory diagram or direct-call search does
not establish that framework-derived path.
