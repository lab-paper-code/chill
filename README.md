# CHILL

CHILL (Cost-aware Heterogeneous Inference for Latency and Load) is a Kubernetes-native research operator for energy-minimal edge inference under tail-latency SLOs.

The current repo is an early scaffold. Public implementation decisions live in:

- `docs/adr/`

Detailed research notes and unpublished design context are kept outside the public repository.

## Baseline

- Go module: `github.com/lab-paper-code/chill`
- Kubernetes baseline: `1.31`
- Kubebuilder: `v4.2.0`
- API group/version: `edge.dacs.io/v1alpha1`

## Development

Use `make test` for local verification. It runs generated manifests, code generation, formatting, vet, and envtest while excluding e2e tests.

```sh
make test
```

Before adding feature logic, keep the bootstrap gates green:

```sh
make manifests generate
git diff --exit-code
test -z "$(git status --porcelain --untracked-files=normal)"
make lint
make test
make helm-lint helm-template
```

`make manifests` also syncs generated CRDs into the Helm chart.

Do not run e2e tests against the testbed kubeconfig. E2E tests require a `kind-*` context.

```sh
./hack/kind-up.sh
export KUBECONFIG="$PWD/.kube/chill-kind.config"
make test-e2e
```

If direnv is enabled, the tracked `.envrc` sets `KUBECONFIG` to the repo-local kind kubeconfig.

## Helm

CHILL uses two Helm releases:

- `charts/chill-operator`: CRDs, RBAC, and the operator Deployment.
- `charts/chill`: the cluster-scoped `ChillSystem` root CR and system config.

```sh
helm install chill-operator charts/chill-operator \
  --namespace chill-system \
  --create-namespace

helm install chill charts/chill \
  --namespace chill-system
```

The repo Make targets run the same order:

```sh
make helm-preflight
make helm-install
make helm-uninstall
```

Default image repositories:

```text
daclab/chill-operator:<chart appVersion>
daclab/chill-node-discovery:<chart appVersion>
```

Once the system is installed, CHILL publishes a cluster-scoped status object:

```sh
kubectl get chillsystem
kubectl describe chillsystem chill
```

CRD deletion is a separate guarded action:

```sh
make helm-purge-crds CONFIRM_PURGE_CRDS=chill-operator
```

For an operator-only smoke without starting pods:

```sh
make helm-install-smoke
kubectl api-resources --api-group=edge.dacs.io
kubectl -n chill-system get deploy,sa,role,rolebinding
```

Before a full Helm install that manages CRDs, check whether existing CRDs are
already owned by another Helm release.

```sh
make helm-crd-check
```

If this repo is taking over CRDs from a retired release, adopt them explicitly:

```sh
make helm-adopt-crds \
  FROM_RELEASE_NAME=<old-release> \
  FROM_RELEASE_NAMESPACE=<old-namespace>
```

Useful diagnosis is written to node annotations:

```sh
kubectl describe node <node-name> | grep edge.dacs.io
```

## License

No public license is selected yet. This private repo is all rights reserved until the lab/project IP policy is checked. Do not make the repository public before a license is explicitly added.
