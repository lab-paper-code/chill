# Internal package map

CHILL groups production packages by measured capability first and uses technical
layers only inside that capability. Start from a capability below instead of
treating every directory under `internal/` as an equal entry point.

## Capability entry points

| Capability | Start here | Boundary |
|---|---|---|
| Profiling | [`profiling/`](profiling/) | Immutable Runs, batch campaigns, derivation, evidence admission, and Job execution |
| System operator | [`operator/`](operator/) | `ChillSystem` reconciliation and operand desired state |
| Node actuation | [`nodeactuator/`](nodeactuator/) | One verified Node power-state transition |
| Operator console | [`operatorconsole/`](operatorconsole/) | Read models, Kubernetes queries, and the read-only HTTP API |
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
│   └── controller/        campaign reconciliation and owned child resources
├── run/                   immutable Run and execution-evidence contracts
├── derivation/            pure curve, stopping, and statistical derivation
└── profiler/
    ├── admission/         verified evidence to scientific verdict
    ├── controller/        ProfilingRun reconciliation, status, and owned writes
    ├── evidence/          retained evidence validation and sealed records
    │   └── kube/          Kubernetes evidence projection and loading
    ├── kube/              trusted Kubernetes fact and policy readers
    └── worker/            one-Job runtime execution plane
```

The executable composition root is [`../cmd/profiler/`](../cmd/profiler/).
It assembles the controller and Kubernetes readers; neither package constructs
the other.

## Serving layout

```text
modelcompat/              pure tensor and runtime compatibility contracts
modelcatalog/             shared ModelSpec projection and validation
firstfloor/               leaf single-runner actor, SLO batching and framed IPC
serving/                  admission, snapshots, routes, drain, JSONL/S3 windows
├── httpapi/              authenticated HTTPS query, management and inference
├── kube/                 private uncached writer, TokenReview/SAR, measured
│                         catalog and current Node/Pod/native observations
└── firstfloor/           typed native HTTP client and correlated progress facts
```

[`../cmd/chill-server/`](../cmd/chill-server/) assembles concrete
adapters. Serving does not import Profiler controllers or candidate resolvers.
The native runtime is firstfloor with explicit ORT CPU/CUDA or RKNN execution,
per-UID/definition batch isolation and shared actor/replay batching decisions.
Reviewed allowed directions and current `go list` edges are recorded in the
[architecture contract](../docs/design/code-intelligence/README.md#package-architecture-contract).

## Layer vocabulary

- A capability root such as `profiling/run` or `nodeactuator` contains plain
  contracts and decisions when the code can remain independent of Kubernetes.
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
