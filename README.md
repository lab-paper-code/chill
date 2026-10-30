# CHILL

**Cost-aware Heterogeneous Inference for Latency and Load** — a Kubernetes-native
research operator that serves edge inference at minimal measured energy while
meeting per-request tail-latency SLOs.

CHILL profiles each model × device × power-mode condition on the real hardware,
turns the completed measurements into a scheduler catalog, and places inference
Pools across heterogeneous nodes (x86, Jetson, Raspberry Pi, RK3588) using
measured energy and transition costs rather than static assumptions.

## Components

| Component | Role |
|---|---|
| Operator (`cmd/chill`) | Reconciles the CRDs below, runs profiling campaigns, owns node-agent DaemonSets |
| Profiler (`cmd/profiler`, `measurement-worker`, observers) | Executes `ProfilingRun`s and writes immutable evidence (Evidence Store, S3-compatible) |
| ChillServer (`cmd/chill-server`) | Single HTTPS entry point: Model/Workload registration, inference, measured-profile scheduler, observation and evidence APIs |
| Node agent (`cmd/node-agent`) | Publishes hardware facts per Node and applies requested power modes |
| Dashboard (`chill-dashboard/`) | Operator web UI over the native ChillServer APIs |
| CLI (`kubectl-chill`) | Read-only inventory, system, and run inspection |

API group: `edge.dacs.io/v1alpha1`. Key CRDs: `ChillSystem`, `DeviceClass`,
`ModelSpec`, `Workload`, `ProfilingRun`, `BatchEnergyOptimization`, `PowerModeTransitionCost`.

## Requirements

- Kubernetes 1.31 (KubeEdge-attached edge nodes supported)
- Go 1.22+, Helm 3, Docker (for image builds)
- An S3-compatible Evidence Store and a TLS Secret for ChillServer
- Kubebuilder v4.2.0 tooling (`make manifests generate`)

## Quick start

```sh
# Install the operator, CRDs and the ChillSystem root object
helm install chill charts/chill --namespace chill-system --create-namespace

# Enable ChillServer (needs an existing TLS Secret and scheduler ConfigMap)
helm upgrade --install chill charts/chill --namespace chill-system \
  --set servingEntrypoint.enabled=true \
  --set servingEntrypoint.tls.secretName=chill-serving-tls \
  --set servingEntrypoint.scheduler.configMap=serving-scheduler

kubectl get chillsystem
```

Register a Workload and query it through the HTTPS API (bearer tokens are
checked with TokenReview/SubjectAccessReview):

```sh
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' "$CHILL_SERVER/api/v1/workloads" \
  --data '{"name":"detector-fast","spec":{"modelRef":{"name":"detector"},"preprocessorRef":{"name":"detector-preprocessor"},"datasetRef":{"name":"detector-records"},"slo":{"percentile":99.5,"targetLatency":"100ms"}}}'
```

A Workload becomes schedulable once its conditions have completed measurements;
`GET /api/v1/scheduler/profiles` shows the derived profiles and what is still
missing. See the [Workload contract](docs/design/serving/workload-management.md)
and the [sample catalog](config/samples/catalog/README.md).

For a state-preserving upgrade of an existing installation:

```sh
helm upgrade chill charts/chill --namespace chill-system \
  --reset-then-reuse-values -f /path/to/reviewed-values.yaml --wait
```

`make helm-uninstall` deletes the `ChillSystem` root (waiting for finalizers)
before removing the release. CRD purge and adoption from another release are
separate guarded targets (`make helm-purge-crds`, `make helm-adopt-crds`).

## Development

```sh
make lint                 # golangci-lint
make test                 # generate, vet, unit + envtest (no e2e)
make helm-lint helm-template
make arch-check           # package dependency rules (.go-arch-lint.yml)
make build                # bin/chill-server, bin/kubectl-chill, ...
```

E2E tests require a repository-local kind context; never point them at a shared
cluster:

```sh
./hack/kind-up.sh
export KUBECONFIG="$PWD/.kube/chill-kind.config"
make test-e2e
```

Images (`daclab/chill-operator`, `chill-node-agent`, `chill-serving-entrypoint`,
`chill-dashboard`, load-generator) are built by `make docker-build-all` and
`.github/workflows/images.yaml`.

## Documentation

| Where | What |
|---|---|
| [`docs/README.md`](docs/README.md) | Documentation map and maintenance rules |
| [`docs/spec/`](docs/spec/README.md) | Requirements and the measurement tables the project exists to fill |
| [`docs/design/`](docs/design/README.md) | Design contracts per domain (profiling, serving, dashboard, platform) |
| [`docs/adr/`](docs/adr/README.md) | Architecture decisions |
| [`docs/operations/ops-agent-guide.md`](docs/operations/ops-agent-guide.md) | Testbed operating procedures and safety rules |
| [`internal/README.md`](internal/README.md) | Package map and layer vocabulary |
| [`chill-dashboard/README.md`](chill-dashboard/README.md) | Dashboard module |

## Status

Research prototype. The scheduler, evidence pipeline, and dashboard are
exercised on a live heterogeneous testbed; interfaces may change between
measurement campaigns without compatibility shims.

## License

No public license has been selected. All rights reserved; do not make this
repository public until a license is explicitly added.
