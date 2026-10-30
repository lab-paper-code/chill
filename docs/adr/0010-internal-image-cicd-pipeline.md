# 0010 Internal Fast Image Deployment Loop

## Status
Accepted

## Context
Docker Hub remains the public release/distribution path. Internal images shorten testbed feedback without replacing merge/release correctness gates. Reuse Makefile image variables (`IMAGE_NAMESPACE`, `OPERATOR_IMG`, `NODE_AGENT_IMG`, `PROFILER_IMG`, `IMAGE_TAG`), Dockerfiles, Helm image values and the install-only smoke path; no second manifest or lifecycle authority.

## Decision
Use a local/manual `make fast-deploy` / `hack/fast-deploy.sh` wrapper for the selected checkout/ref. This is destructive development observation, not public publication or preservation of CHILL runtime state.

| Pipeline boundary | Contract |
|---|---|
| Registry | Current endpoint `155.230.35.213:5000/chill/{chill-operator,chill-node-agent,chill-profiler}`; registry implementation may change. Assign endpoint/namespace ownership, push credential source, cluster pull-auth, TLS/CA distribution and retention owner. |
| Identity | Immutable `sha-<git-sha>`; dirty checkout without explicit tag uses `sha-<git-sha>-dirty-<UTC timestamp>`. Mutable `dev` is human convenience only; analysis records immutable tag or digest. |
| Invocation | `make fast-deploy FAST_DEPLOY_ARGS="--components operator --observe summary"`; no second configuration surface. Default components `all`; explicit `operator`/`node-agent` selection builds `build/docker/operator.Dockerfile` / `build/docker/node-agent.Dockerfile`. Resolve and reuse required deployed images not rebuilt; fail before uninstall if unresolved. |
| Trigger | Local/manual now; `workflow_dispatch` optional later; trusted branch auto-deploy only after testbed locking. No default PR push/deployment. |
| Minimal gates | Successful image build/push, useful Helm uninstall/install errors, rollout sufficient to inspect Pods/logs/events. Full repository verification is not a precondition of every fast deployment. |
| Build speed | Build explicitly selected changed components; independent images concurrently; direct BuildKit registry push when supported, validated `ctr --plain-http` fallback. Registry cache only after basic loop is useful. |
| Target safety | Print/check `kubectl config current-context`, `kubectl cluster-info`, `helm status chill --namespace chill-system` and `kubectl get crd chillsystems.edge.dacs.io` (last two may be absent). Explicit kubeconfig or confirmed active context; interactive confirmation before deletion unless deliberate noninteractive flag. |
| Reset | Reuse `make helm-uninstall` then `make helm-install` (or equivalent) with internal `OPERATOR_IMG`, `NODE_AGENT_IMG`, `PROFILER_IMG`; same `chill` release / `chill-system` namespace. Current checkout chart/generated manifests define installed state. Developer updates generated API/CRD manifests first. |
| Helm images | Public defaults stay Docker Hub (`daclab/chill-operator`, `daclab/chill-node-agent`). Override `operator.image.*`, `nodeAgent.image.*`, `profiler.image.*` via secret-free `values-internal.yaml` or wrapper `--set`; never embed registry credentials. |
| Pull access | Prefer node-wide auth/trust. Current chart/controller does not expose `imagePullSecrets` for both operator Deployment and managed node-agent DaemonSets; namespace-scoped support is a separate enhancement. Verify push robot/CI account, node pulls, runtime TLS trust, auth and retention once. |
| Retention | Suggested development images 7–14 days; referenced `sha-*` longer; release tags retained. |
| Observation | Report commit SHA, image refs/digests, Pod image IDs, kube context, release/namespace/revision, ChillSystem status, operator Deployment rollout, node-agent DaemonSets, Pods, warning events and bounded operator/node-agent logs. `summary`: rollout/image IDs/warnings; default `full`: also all recent events and bounded logs. |
| Normal gates | `make manifests generate`, `git diff --exit-code`, `make lint`, `make test`, `make helm-lint`, `make helm-template` remain for merge/release and trusted API/CRD/RBAC/lifecycle changes. CRD authority: `make manifests` → `config/crd/bases` → `make helm-sync-crds` → `charts/chill/templates/crds`. |
| Deferred hardening | Optional workflow wrapper, locking/concurrency, selected full-CI deployment gates, registry cache/retention automation, vulnerability scanning (Trivy or equivalent), SBOM, Cosign signing, digest-based promotion and deployment records (digest/SHA/chart version/namespace). Initial scope is authenticated push, current-cluster deployment and observations. |

### Recorded testbed evidence (2026-07-10)
The HTTP registry `155.230.35.213:5000` was configured on `jetson-nano`, `jetsonx`, `jetsono`, `lattepanda`, `orangepi`, `orin-nano`, `rasp5`. Each has `/etc/containerd/certs.d/155.230.35.213:5000/hosts.toml` with server/host `http://155.230.35.213:5000` and capabilities `["pull", "resolve"]`. CRI `config_path` is `/etc/containerd/certs.d`; most use `[plugins."io.containerd.grpc.v1.cri".registry]`, while orin-nano uses v1 `[plugins.cri.registry]` (effective dump converts to v2).

Containerd was restarted on each node. Backups use `.bak.chill-fast-<timestamp>` beside edited files, including NVIDIA Jetson `/etc/containerd/config.d/*.toml` fragments.

Validation image: `155.230.35.213:5000/chill/chill-node-discovery:pull-smoke-postconfig-08bba02-20260710-225842`, digest `sha256:8957fd6a70565e4add4ece0cc37fb4f918644b84eb1397265fc4d3fbb5e4e17a`. `crictl pull` through CRI succeeded on all seven nodes. Orin-nano used a temporary arm64 `/tmp` crictl, removed afterward; its pinned Kubernetes Pod was evicted before pull for DiskPressure, so CRI pull—not Pod rollout—is its evidence.

## Consequences
Development feedback is faster and less dependent on Docker Hub, but two image paths require strict naming/tag policy: Docker Hub is external release; internal registry is development/testbed. Exhaustive pre-deploy verification is traded for live feedback only because this loop does not publish public images and normal CI/release verification remains separate.
