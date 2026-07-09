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

For cluster operations, prefer the repo Make targets. They keep component
ordering and cleanup guards inside the repo tooling instead of exposing an
application-specific phase graph to operators.

```sh
make helm-preflight HELM_VALUES=charts/chill/values-testbed.yaml
make helm-install HELM_VALUES=charts/chill/values-testbed.yaml
```

Start runtime components after images are available:

```sh
make helm-start \
  HELM_VALUES=charts/chill/values-testbed.yaml \
  CONTROLLER_IMG=<registry>/chill/controller:<tag> \
  NODE_DISCOVERY_IMG=<registry>/chill/node-discovery:<tag>
```

Cleanup is also exposed as high-level Helm operations:

```sh
make helm-stop HELM_VALUES=charts/chill/values-testbed.yaml
make helm-uninstall HELM_VALUES=charts/chill/values-testbed.yaml
```

CRD deletion is a separate guarded action:

```sh
make helm-purge-crds CONFIRM_PURGE_CRDS=chill
```

For a real-cluster install smoke, keep the first pass inert: do not let Helm
claim CRDs and do not start pods. This catches RBAC, namespace, ConfigMap, and
Deployment rendering issues without depending on a registry image.

```sh
make helm-install-smoke HELM_VALUES=charts/chill/values-testbed.yaml
kubectl api-resources --api-group=edge.dacs.io
kubectl -n chill-system get deploy,cm,sa,role,rolebinding
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

For a controller-only runtime smoke with a node-local image, set
`controller.nodeSelector` and `controller.image.pullPolicy=Never` through Helm
instead of patching the Deployment by hand.

```sh
helm upgrade chill charts/chill \
  --namespace chill-system \
  -f charts/chill/values-testbed.yaml \
  --set crds.enabled=false \
  --set discovery.enabled=false \
  --set controller.replicaCount=1 \
  --set controller.image.repository=chill/controller \
  --set controller.image.tag=<local-tag> \
  --set controller.image.pullPolicy=Never \
  --set 'controller.nodeSelector.kubernetes\.io/hostname=<node-name>'
```

Useful diagnosis is written to node annotations:

```sh
kubectl describe node <node-name> | grep edge.dacs.io
```

## License

No public license is selected yet. This private repo is all rights reserved until the lab/project IP policy is checked. Do not make the repository public before a license is explicitly added.
