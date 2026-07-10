# 0010 Internal Fast Image Deployment Loop

## Status

Accepted

## Context

CHILL already has a public image workflow that builds `chill-operator` and
`chill-node-discovery` images for Docker Hub. That path should remain the
external release and distribution path.

For day-to-day development, however, Docker Hub is not the fastest feedback
loop. The immediate need is to apply code changes quickly to the current
Kubernetes testbed and observe the changed behavior in live pods.

This path is not intended to replace the normal correctness gates used for
merge, release, or public image publication.

The repository already has the main integration points needed for a parallel
internal path:

- `Makefile` image variables: `IMAGE_NAMESPACE`, `OPERATOR_IMG`,
  `NODE_DISCOVERY_IMG`, `IMAGE_TAG`.
- Docker build targets for the operator and node-discovery components.
- Helm values for `operator.image.*` and `nodeDiscovery.image.*`.
- A safe install-only Helm smoke path that can install the chart without
  starting CHILL runtime pods.

The internal pipeline should reuse those surfaces instead of introducing a
separate deployment mechanism.

This ADR records a lightweight development loop, not a release-grade CI/CD
system. The current testbed registry endpoint is recorded below as an
operational choice for the fast loop; it is not a public release registry
decision.

## Goals

- Keep Docker Hub as the public release path.
- Add an internal image path for fast development against the current
  Kubernetes testbed.
- Make internal images addressable by immutable commit tags.
- Minimize pre-deploy gates so developers can observe code changes quickly.
- Keep full verification in the normal CI, merge, and release paths.
- Use a reset-style deployment so chart, API, CRD, and runtime changes in the
  current checkout are reflected together.

## Non-Goals

- Replace Docker Hub publication.
- Change the public chart default image repositories immediately.
- Auto-deploy every pull request into the shared testbed.
- Require `make lint`, `make test`, or full Helm validation before every
  internal development deployment.
- Preserve CHILL runtime state across a fast deployment.
- Solve the full supply-chain hardening problem in the first implementation.

## Proposed Design

Use a private registry namespace for development images, for example:

```text
registry.internal/chill/chill-operator
registry.internal/chill/chill-node-discovery
```

For the current Kubernetes testbed, use the existing internal registry:

```text
155.230.35.213:5000/chill/chill-operator
155.230.35.213:5000/chill/chill-node-discovery
```

The long-term registry implementation can still change while this ADR is
proposed. For the fast loop, the minimum contract is intentionally small:

- registry endpoint and namespace ownership;
- image push credential source;
- cluster pull-auth mode;
- TLS/CA distribution path for every node runtime;
- retention policy owner.

Harbor, GitLab Container Registry, or a private Docker Registry are all
acceptable only if they satisfy that contract for the CHILL testbed.

Use immutable commit tags as the deployment source of truth:

```text
sha-<git-sha>
```

When the checkout is dirty and no explicit tag is supplied, the wrapper uses a
unique snapshot tag instead of overwriting the clean commit tag:

```text
sha-<git-sha>-dirty-<UTC timestamp>
```

Convenience tags may also be published from trusted development runs:

```text
dev
```

The mutable `dev` tag is only for quick human iteration. Any deployment that
needs later analysis should record the immutable `sha-<git-sha>` tag or the
resolved image digest.

## Fast Development Flow

Keep the existing Docker Hub workflow and normal CI workflow for public image
publication and merge confidence.

The first implementation should be a local/manual wrapper, for example
`make fast-deploy` or `hack/fast-deploy.sh`. It should build, push, deploy, and
observe from the developer's selected checkout/ref.

The Make entry point accepts wrapper arguments without introducing a second
configuration surface:

```sh
make fast-deploy FAST_DEPLOY_ARGS="--components operator --observe summary"
```

The wrapper builds the same component matrix:

- `chill-operator` from `build/docker/operator.Dockerfile`
- `chill-node-discovery` from `build/docker/node-discovery.Dockerfile`

The default remains `all`, but an explicit component selection may rebuild
only `operator` or `node-discovery`. The wrapper must resolve and reuse the
currently deployed image for the component it does not rebuild; it must fail
before uninstall if that image cannot be resolved.

Recommended trigger policy for the fast loop:

- local/manual invocation: build, push, deploy, and observe the current ref.
- GitHub Actions `workflow_dispatch`: optional future wrapper around the same
  local script.
- trusted same-repository development branch auto-deploy: future option only,
  after testbed locking is in place.
- `pull_request`: no default push or deployment.

The fast loop should not wait for the full repository verification suite. The
minimal pre-deploy checks are:

- image build succeeds;
- image push succeeds;
- Helm uninstall/install commands succeed or report useful errors;
- target pods roll out far enough to inspect logs and Kubernetes events.

Full checks remain in the normal CI path:

```sh
make manifests generate
git diff --exit-code
make lint
make test
make helm-lint
make helm-template
```

Recommended speed policy:

- Build only changed components when the caller can identify them explicitly.
- Build independent component images concurrently.
- Push directly from BuildKit to the internal registry when supported, with the
  validated `ctr --plain-http` path retained as a fallback.
- Add registry-backed BuildKit cache only after the basic loop is useful.

## Deployment Flow

The development deployment should reset the current CHILL release in the current
testbed. This is intentionally destructive for CHILL-managed runtime resources:
the wrapper uninstalls the release, then installs it again with images built
from the selected checkout/ref.

The wrapper must first print and check the target:

```sh
kubectl config current-context
kubectl cluster-info
helm status chill --namespace chill-system || true
kubectl get crd chillsystems.edge.dacs.io || true
```

The wrapper must make the target explicit before it deletes anything. A local
run should require interactive confirmation unless a deliberate non-interactive
flag is set.

The reset deploy should reuse the existing Helm lifecycle targets:

```sh
OPERATOR_IMG=registry.internal/chill/chill-operator:sha-<git-sha> \
NODE_DISCOVERY_IMG=registry.internal/chill/chill-node-discovery:sha-<git-sha> \
make helm-uninstall

OPERATOR_IMG=registry.internal/chill/chill-operator:sha-<git-sha> \
NODE_DISCOVERY_IMG=registry.internal/chill/chill-node-discovery:sha-<git-sha> \
make helm-install
```

The wrapper can implement equivalent commands. The important behavior is:

- Use internal image repositories.
- Use `sha-<git-sha>` by default, with `dev` allowed for manual iteration.
- Reinstall the same `chill` release in the same `chill-system` namespace.
- Let the chart and generated manifests in the current checkout define the
  newly installed state.
- Use an explicit kubeconfig or print and require the developer to confirm the
  active context before touching the cluster.
- End by collecting rollout state, pod status, recent events, and relevant logs.

If the edit includes API or CRD changes, the developer is responsible for making
sure generated manifests are already up to date before running the fast loop.
The fast loop does not replace review-quality validation; it only applies the
current checkout to the development testbed.

## Helm Values

The chart defaults may remain Docker Hub oriented:

```yaml
operator:
  image:
    repository: daclab/chill-operator

nodeDiscovery:
  image:
    repository: daclab/chill-node-discovery
```

Internal deployments should override repositories and tags through either:

- a checked-in `values-internal.yaml` that contains no secrets, or
- wrapper-provided Helm `--set` arguments.

Do not hard-code registry credentials in Helm values.

For the first implementation, prefer node-wide registry authentication and TLS
trust configuration. That keeps the fast loop simple. The current
chart/controller contract does not expose `imagePullSecrets` for both the
operator Deployment and the operator-managed node-discovery DaemonSet. If
namespace-scoped pull secrets are required, add explicit chart values and
node-discovery config support as a separate enhancement.

## Registry and Cluster Requirements

Before relying on the fast loop, verify once:

- The registry has a robot or CI account with push permission.
- Cluster nodes can pull images from the registry.
- TLS certificates are trusted by every container runtime that will pull the
  images.
- Pull authentication is configured node-wide, or chart/controller support for
  image pull secrets has been implemented and validated.
- Registry retention policy is set before high-frequency internal image
  publication.

Suggested retention policy:

- short-lived development images: 7 to 14 days.
- `sha-*` images referenced by notes, issues, or experiment logs: longer
  retention.
- Release tags: retained.

### Current Testbed Runtime Configuration

As of 2026-07-10, the current edge nodes have been configured to pull CHILL
development images from the HTTP registry `155.230.35.213:5000`.

Configured Kubernetes edge nodes:

- `jetson-nano`
- `jetsonx`
- `jetsono`
- `lattepanda`
- `orangepi`
- `orin-nano`
- `rasp5`

Each node has:

```text
/etc/containerd/certs.d/155.230.35.213:5000/hosts.toml
```

with:

```toml
server = "http://155.230.35.213:5000"

[host."http://155.230.35.213:5000"]
  capabilities = ["pull", "resolve"]
```

The containerd CRI registry `config_path` points at
`/etc/containerd/certs.d`. Most nodes use the v2-style
`[plugins."io.containerd.grpc.v1.cri".registry]` section. `orin-nano` still
uses a v1 containerd config file, so it uses `[plugins.cri.registry]`; the
effective containerd dump converts that to the v2 CRI registry config.

The change was applied directly to the testbed nodes and containerd was
restarted on each node. Backups were left next to the edited files using the
suffix `.bak.chill-fast-<timestamp>`. Jetson nodes with NVIDIA containerd
fragments also have matching backups for `/etc/containerd/config.d/*.toml`.

Validation image:

```text
155.230.35.213:5000/chill/chill-node-discovery:pull-smoke-postconfig-08bba02-20260710-225842
sha256:8957fd6a70565e4add4ece0cc37fb4f918644b84eb1397265fc4d3fbb5e4e17a
```

`crictl pull` succeeded through the CRI image service on all seven edge nodes.
`orin-nano` does not have a permanent `crictl` binary installed; it was
validated with a temporary arm64 `crictl` copied to `/tmp` and removed after
the check. A temporary Kubernetes Pod pinned to `orin-nano` was evicted before
image pull because the node had `DiskPressure`, so CRI pull is the current
evidence for that node.

## Observation Output

The fast loop should optimize for observability, not exhaustive correctness.
Each run should report:

- commit SHA;
- image references or digests;
- observed pod image IDs;
- kube context;
- Helm release and namespace;
- Helm revision;
- `ChillSystem` status;
- operator Deployment rollout status;
- node-discovery DaemonSet status;
- pod status;
- recent warning events;
- recent bounded operator and node-discovery logs.

`summary` observation mode reports rollout state, pod image IDs, and warning
events. `full` mode additionally collects all recent events and bounded logs.
The default remains `full`; developers may select `summary` for a faster repeat
loop.

This gives developers enough information to decide whether the code change is
visible in the current Kubernetes environment.

## Normal Verification Path

The normal verification path remains unchanged and is still required for merge
or release. It is also the path to use before trusting API, CRD, RBAC, or Helm
lifecycle changes beyond local observation. The CRD/chart source-of-truth flow
remains:

```text
make manifests
  -> config/crd/bases
  -> make helm-sync-crds
  -> charts/chill/templates/crds
```

The fast loop must not introduce a second source of truth for manifests, chart
CRDs, or runtime lifecycle. It applies whatever the current checkout and chart
already contain.

## Security and Provenance

The first implementation can be limited to authenticated push, current-cluster
deployment, and observation output.

After the basic path is stable, add:

- image vulnerability scanning with Trivy or an equivalent scanner;
- SBOM generation;
- image signing with Cosign;
- deployment logs that record image digest, commit SHA, chart version, and
  target namespace.

## Implementation Phases

### Initial Adoption

1. Select and validate the internal registry.
2. Configure local image push credentials.
3. Add a local/manual fast-deploy wrapper using the existing component matrix.
4. Build and push the image for the current testbed platform.
5. Print the target kube context, Helm release, namespace, and CRD state.
6. Ask for confirmation before deleting the development release.
7. Run `make helm-uninstall`.
8. Run `make helm-install` with the internal image references.
9. Print observation output: rollout status, pod status, recent events, and
   logs.

### Future Hardening

- Add an optional GitHub Actions `workflow_dispatch` wrapper around the local
  script.
- Add shared-testbed locking or CI concurrency if remote runs are enabled.
- Add full CI gating before selected deployments.
- Add registry-backed BuildKit cache.
- Add retention automation.
- Add image vulnerability scanning.
- Add SBOM generation.
- Add image signing.
- Add digest-based release promotion between internal and public registries.

## Consequences

Development image feedback becomes faster and less dependent on Docker Hub.

The project gains two image paths with different purposes, so naming and tag
policy must stay strict. Docker Hub remains the external release path; the
internal registry is the development and testbed path.

The fast loop intentionally trades exhaustive pre-deploy verification for quick
live feedback in the current Kubernetes environment. The risk is acceptable only
because this path is for development observation, does not publish public
images, and keeps normal CI/release verification separate.
