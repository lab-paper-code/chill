# CHILL

CHILL (Cost-aware Heterogeneous Inference for Latency and Load) is a Kubernetes-native research operator for energy-minimal edge inference under tail-latency SLOs.

The repo implements profiling, ChillServer for HTTPS Workload management/inference,
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

`make helm-template` and Helm install validate the rendered ChillServer scheduler
configuration with the locally built `bin/chill-server`. Fast deploy runs this
gate before uninstall. Release, namespace, values and `HELM_SET` overrides select
the rendered process arguments and its ConfigMap name, namespace and data key.
For an external scheduler ConfigMap, set `CHILL_SCHEDULER_CONFIGMAP_FILE` to a
private manifest containing only that selected ConfigMap and scheduler data key.
Keep this file outside the repository. The gate uses the startup JSON decoder
and settings validator and requires every configured field.
Install and fast-deploy preflight also read that selected live ConfigMap and
validate its actual data before any release mutation. External ConfigMap edits
remain the operator's responsibility during deployment.

Run `make test-deployment-config` for the actual binary valid-settings and
missing-`planSearchWorkers` regression. To check a selected current configuration,
run `python3 hack/deployment-config-test.py --configmap /private/selected-cm.json`
after `make build-chill-server`. This gate applies to the next deployment;
runtime7 and the currently deployed source remain unchanged.

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

The single ChillServer registers ModelSpecs and Workloads, queries desired/applied
definitions and current Pools, and accepts one raw inference sample per request:
the Workload's Preprocessor input fields as base64 `BYTES`. Bearer tokens are verified by Kubernetes TokenReview and authorized
by SubjectAccessReview for the HTTP path and verb. Managers use this API, not
direct Kubernetes writes to managed ModelSpecs or Workloads.

```sh
# CHILL_SERVER is the HTTPS endpoint covered by CHILL_SERVER_CA and its TLS certificate.
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' "$CHILL_SERVER/api/v1/workloads" \
  --data '{"name":"detector-fast","spec":{"modelRef":{"name":"detector"},"preprocessorRef":{"name":"detector-preprocessor"},"datasetRef":{"name":"detector-records"},"slo":{"percentile":99.5,"targetLatency":"100ms"}}}'
curl --cacert "$CHILL_SERVER_CA" -H "Authorization: Bearer $TOKEN" \
  "$CHILL_SERVER/api/v1/workloads/detector-fast"
```

Registration can return `201` with admission closed (`MissingDataset` without a
`datasetRef`, otherwise `MissingEvidence` until measured).
Use the queried `desired.metadata.resourceVersion` as `If-Match` for PUT or
DELETE; stale versions return `409`. Model registration, inference, updates,
observation filters, and exact deployment settings are in the
[Workload contract](docs/design/serving/workload-management.md).
The [model sample guide](config/samples/catalog/README.md) submits prepared
manifests through ChillServer.

The following is the previous serving work unit's verification checkpoint.
Current firstfloor scheduler evidence is recorded in the
[live evidence index](docs/plans/2026-10-13-scheduler-live-evidence-campaign.md).

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
release. It presents the native ChillServer APIs, not an independent control plane.
Its [design and exercised scope](docs/design/dashboard/README.md) distinguish
the current core from pending SRS producers.

Build its standalone image from the repository root:

```sh
docker build -f build/docker/dashboard.Dockerfile -t "$DASHBOARD_IMAGE" .
```

`make docker-build-all`에도 ChillServer(`chill-serving-entrypoint`), Dashboard,
load-generator 이미지가 포함되며, `.github/workflows/images.yaml`에서도 세 이미지를 빌드합니다.

Enable `dashboard.enabled` together with TLS-enabled ChillServer. Supply the Dashboard
image and a `dashboard.caSecretName` containing `ca.crt` for ChillServer.
The chart keeps the web Service private. ChillServer serves the web application
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

Enable ChillServer only with an existing TLS Secret, persistent observation storage,
and an existing scheduler ConfigMap; the chart does not create native serving capacity:

```sh
helm upgrade --install chill charts/chill \
  --namespace chill-system --create-namespace \
  --set servingEntrypoint.enabled=true \
  --set servingEntrypoint.tls.secretName=chill-serving-tls \
  --set servingEntrypoint.scheduler.configMap=serving-scheduler
```

The scheduler derives its candidates and costs from completed measurements in the
`servingEntrypoint.profileNamespaces` namespaces (an empty list defaults to the serving
namespace): the latest successful BatchEnergyOptimization per measured condition, the
serving-operation records under `serving/operation-measurements/` in the Evidence Store,
and Complete PowerModeTransitionCost objects. `GET /api/v1/scheduler/profiles` lists the
derived profiles and the measurements that contribute none; it is read-only. Completed
measurements become candidates automatically, and autonomous control is enabled per
Workload by listing its UID in the ConfigMap's `settings.autonomousWorkloads`. The Evidence Store
credentials need list permission on the bucket. ChillServer is fixed to one replica
with `Recreate`, exposes HTTPS on port `8080`, and keeps a private RWO ObservationLog
plus immutable Evidence Store windows.

### Targeted node agent actuation replacement

`nodeAgent.actuation.updateStrategy` renders the native DaemonSet strategy of
the `chill-node-agent-actuation` DaemonSet into the authoritative node agent
ConfigMap. The default is `RollingUpdate` with
`maxUnavailable: 1`. For operator-elected replacement, use `type: OnDelete`
and remove `rollingUpdate` entirely (a Helm override can set it to `null`).
The full reconciler remains active; this is not a pause.

To change the strategy on a running system, stage a new, unreferenced
ConfigMap containing the new `actuation.updateStrategy`, then switch the
`ChillSystem` `nodeAgent.configRef` to that ConfigMap. Do not modify the
operand template until the operator has reconciled `OnDelete`.

Record the original config and Pod UIDs first. With `OnDelete` observed on the
DaemonSet, change the authoritative template config and delete only the
operator-selected target actuation Pod; wait for its replacement to be ready.
Never delete or restart the excluded AGX Pod or change its Node configuration.
After the approved operation and power-mode restoration, restore the original
template config while retaining `OnDelete`, replace only the selected target Pod
again, and confirm the excluded AGX Pod UID is unchanged. Restore `RollingUpdate`
only after every eligible Pod already matches the restored template; otherwise
retain `OnDelete` until replacing the excluded AGX is separately authorized.

`make helm-uninstall` separates responsibilities: it first deletes the root
`ChillSystem` and waits for CHILL finalizers through the Kubernetes API, then
removes the Helm release with hooks disabled. The chart still includes a
Helm `pre-delete` hook as a safety net for direct `helm uninstall`.

Default image repositories:

```text
daclab/chill-operator:<chart appVersion>
daclab/chill-node-agent:<chart appVersion>
```

The node agent runs as two DaemonSets from that image. `chill-node-agent`
publishes hardware signature and online-CPU metadata on every Node.
`chill-node-agent-actuation` (`nodeAgent.actuation.enabled`) applies the
power mode the `chill.dacs.io/power-mode` Node label names on the Nodes selected
by the `nodeSelector` of each DeviceClass named in `powerActuation.policies`.

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
