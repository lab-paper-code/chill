# Internal package map

CHILL groups production packages by measured capability first and uses technical
layers only inside that capability. Start from a capability below.

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
| Scheduler search | [`scheduler/`](scheduler/) | Search protocol and selectable search implementations |
| Evaluation | [`evaluation/`](evaluation/) | Experiment lifecycle and execution services |

Small support packages remain at the top level: `component` owns stable component names,
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
adapters. The `serving` root imports profiling derivation. It must not import
Profiler controllers or candidate resolvers. Subpackages such as `application` and
`kube` also consume Profiler execution and evidence adapters.
The native runtime is firstfloor with explicit ORT CPU/CUDA or RKNN execution,
per-UID/definition batch isolation and shared actor/replay batching decisions.
Reviewed allowed directions are recorded in the
[architecture contract](../docs/design/code-intelligence/README.md).

The serving scheduler adapts measured capacity and cost judgments to the search
protocol in [`scheduler/protocol/`](scheduler/protocol/). Search implementations
live in [`scheduler/v1/`](scheduler/v1/) and [`scheduler/v2/`](scheduler/v2/);
`planSearchStrategy` selects one. Persisted diagnostics describe the selected
algorithm's actual search and termination.

`serving/kube` captures live inputs, reconciles state and effect proofs, launches
operations, and checks the physical budget. `serving/state` owns operation state
and scopes; `serving/operations` owns preparation and lifecycle.
`serving/observation` owns retained records, queries, storage and archiving.

## Layer vocabulary

- Capability contracts and decisions remain independent of Kubernetes where
  practical. `profiling/run` also contains a Node grant reader and Kubernetes
  value projections.
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
