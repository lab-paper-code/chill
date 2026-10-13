# CHILL

CHILL (Cost-aware Heterogeneous Inference for Latency and Load) is a Kubernetes-native research operator for energy-minimal edge inference under tail-latency SLOs.

The repo implements profiling, an HTTPS Workload management/inference owner,
and a measured-profile scheduler with native firstfloor serving. Immutable
request SLOs drive batch selection; measured whole-POOL energy and transition
costs drive configuration selection. Public implementation decisions live in:

- `docs/adr/`

The design-document entry point is [`docs/design/README.md`](docs/design/README.md).
The production package map and layer vocabulary are in
[`internal/README.md`](internal/README.md).

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

### Operator CLI

`make build` creates the `kubectl-chill` plugin in `bin/`. Add that directory
to `PATH` to query the current kubeconfig context without changing cluster
state:

```sh
export PATH="$PWD/bin:$PATH"
kubectl chill inventory
kubectl chill inventory -o json
kubectl chill system
kubectl chill system -o json
kubectl chill runs -n chill-system
kubectl chill runs -A -o json
kubectl chill runs show <run-name> -n chill-system
kubectl chill runs show <run-name> -n chill-system -o json
```

The current commands require read permission for cluster-scoped `ChillSystem`,
`DeviceClass`, `ModelSpec`, namespaced `ProfilingRun`, and Kubernetes `Node`
resources. The Helm chart installs the unbound `chill-console-viewer`
ClusterRole; a cluster administrator decides which users or groups receive it.
The CLI does not run inside the operator Pod.

`runs show` follows the Run's immutable evidence reference through the
ClusterIP-only Console API. Enable that API with `--set console.enabled=true`.
The CLI reaches it through the Kubernetes Service proxy; Evidence Store
credentials and internal storage endpoints stay inside the Console Pod.

### Workload HTTPS API

The single owner registers ModelSpecs and Workloads, queries desired/applied
definitions and current Pools, and accepts one named-tensor inference sample
per request. Bearer tokens are verified by Kubernetes TokenReview and authorized
by SubjectAccessReview for the HTTP path and verb. Managers use this API, not
direct Kubernetes writes to managed ModelSpecs or Workloads.

```sh
# OWNER is the HTTPS endpoint covered by OWNER_CA and its TLS certificate.
curl --cacert "$OWNER_CA" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' "$OWNER/api/v1/workloads" \
  --data '{"name":"detector-fast","spec":{"modelRef":{"name":"detector"},"slo":{"percentile":99.5,"targetLatency":"100ms"}}}'
curl --cacert "$OWNER_CA" -H "Authorization: Bearer $TOKEN" \
  "$OWNER/api/v1/workloads/detector-fast"
```

Registration can return `201` with admission closed (`MissingEvidence`).
Use the queried `desired.metadata.resourceVersion` as `If-Match` for PUT or
DELETE; stale versions return `409`. Model registration, inference, updates,
observation filters, and exact deployment settings are in the
[Workload contract](docs/design/serving/workload-management.md).
The [model sample guide](config/samples/catalog/README.md) submits prepared
manifests through this owner.

The following is the previous serving work unit's verification checkpoint.
Current firstfloor scheduler evidence is recorded in the
[scheduler implementation](docs/design/serving/scheduler-implementation.md).

Local isolated kind verified HTTPS auth, lifecycle and reference protection,
private JSONL/S3 archival, and admission closed without evidence. The actual
official native Triton runner verified named multi-input numeric/bool inference,
same-UID batches, native queue timeout, and ACTIVE/IDLE profiling smoke.
The live `chill-system` testbed additionally verified native Model/Workload
registration, update conflict, actor identity, deletion/re-registration isolation,
HTTPS Dashboard relay and admission closed without evidence. A full qualifying
native campaign followed by Ready gateway inference and ARM/RKNN hardware
execution have **not** been verified.

### Dashboard

The [Dashboard module](chill-dashboard/README.md) is part of the same CHILL
release. It presents the native owner APIs, not an independent control plane.
Its [design and exercised scope](docs/design/dashboard/README.md) distinguish
the current core from pending SRS producers.

Build its standalone image from the repository root:

```sh
docker build -f build/docker/dashboard.Dockerfile -t "$DASHBOARD_IMAGE" .
```

Enable `dashboard.enabled` together with the TLS owner. Supply the Dashboard
image and a `dashboard.caSecretName` containing `ca.crt` for the native owner.
The chart keeps the web Service private. The owner serves the web application
and BFF on its existing HTTPS listener; the BFF verifies its fixed native
HTTPS origin. Native API paths retain TokenReview/SubjectAccessReview.

Use a state-preserving upgrade for an existing installation:

```sh
helm upgrade chill charts/chill --namespace chill-system \
  --reset-then-reuse-values -f /path/to/reviewed-values.yaml --wait
```

The actual testbed uses `https://155.230.35.213:31104/`. NodePort is a
test-only **live override** on `servingEntrypoint.service`; chart defaults remain
`ClusterIP`. The development certificate is self-signed; only browser navigation
needs a trust decision, not disabled BFF verification. The authorized testbed
caller can obtain a short-lived token without storing it in application config:

```sh
kubectl --context kubernetes-admin@kubernetes -n chill-system \
  create token chill-dashboard-user --duration=2h
```

## Helm

CHILL uses one Helm release:

- `charts/chill`: CRDs, operator RBAC/Deployment, the cluster-scoped
  `ChillSystem` root CR, and system config.

```sh
helm install chill charts/chill \
  --namespace chill-system \
  --create-namespace
```

The repo Make targets run the same release flow:

```sh
make helm-preflight
make helm-install
make helm-uninstall
```

Enable the owner only with an existing TLS Secret and persistent observation
storage; the chart does not create native serving capacity:

```sh
helm upgrade --install chill charts/chill \
  --namespace chill-system --create-namespace \
  --set servingEntrypoint.enabled=true \
  --set servingEntrypoint.tls.secretName=chill-serving-tls \
  --set 'servingEntrypoint.profileCampaigns[0]=chill-system/native-cpu-campaign'
```

The selected campaign must be completed and provide the complete frozen native
batch domain `1..maxBatchSize`; an empty selection admits no profiles.
The owner is fixed to one replica with `Recreate`, exposes HTTPS on port `8080`,
and keeps a private RWO journal plus immutable Evidence Store windows.

`make helm-uninstall` separates responsibilities: it first deletes the root
`ChillSystem` and waits for CHILL finalizers through the Kubernetes API, then
removes the Helm release with hooks disabled. The chart still includes a
Helm `pre-delete` hook as a safety net for direct `helm uninstall`.

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
make helm-purge-crds CONFIRM_PURGE_CRDS=chill
```

For an install smoke without starting CHILL pods:

```sh
make helm-install-smoke
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
  FROM_RELEASE_NAME=chill-operator \
  FROM_RELEASE_NAMESPACE=chill-system
```

Useful diagnosis is written to node annotations:

```sh
kubectl describe node <node-name> | grep edge.dacs.io
```

## License

No public license is selected yet. This private repo is all rights reserved until the lab/project IP policy is checked. Do not make the repository public before a license is explicitly added.
