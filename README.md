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

The default chart installs the operator surface without enabling hardware discovery. This keeps a plain install inert until a site-specific catalog is provided.

```sh
helm template chill charts/chill --namespace chill-system
```

For the six-node lab testbed, use the testbed values file. Discovery runs in two stages: the node daemon labels hardware facts from host files, then the controller matches those labels to the device catalog and creates `DeviceClass` objects.

```sh
make docker-buildx-all \
  CONTROLLER_IMG=<registry>/chill/controller:<tag> \
  NODE_DISCOVERY_IMG=<registry>/chill/node-discovery:<tag>

kubectl label node <node-name> node-role.kubernetes.io/edge=

helm upgrade --install chill charts/chill \
  --namespace chill-system \
  --create-namespace \
  -f charts/chill/values-testbed.yaml \
  --set controller.image.repository=<registry>/chill/controller \
  --set controller.image.tag=<tag> \
  --set nodeDiscovery.image.repository=<registry>/chill/node-discovery \
  --set nodeDiscovery.image.tag=<tag>

kubectl get nodes --show-labels | grep edge.dacs.io
kubectl get deviceclasses.edge.dacs.io
```

Useful diagnosis is written to node annotations:

```sh
kubectl describe node <node-name> | grep edge.dacs.io
```

## License

No public license is selected yet. This private repo is all rights reserved until the lab/project IP policy is checked. Do not make the repository public before a license is explicitly added.
