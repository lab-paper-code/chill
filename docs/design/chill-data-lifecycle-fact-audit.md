# CHILL Data Lifecycle Fact Audit

## Status

Current implementation fact audit. This document inventories data boundaries
and fixes the first storage ownership boundary; it does not select a storage
engine or authorize a new API.

## Purpose

CHILL must not treat every value it observes or derives as one kind of data.
Before choosing storage, this audit separates:

- Kubernetes objects and metadata that the current control plane writes;
- values that current internal packages can calculate but do not persist;
- evidence retained only by isolated spikes; and
- profile and scheduling data required by the system specification but not yet
  represented by an implemented lifecycle.

`Authoritative` below means the source that answers the current-state question.
A snapshot retained as Run evidence does not become a second current-state
authority.

## Current production data

| Data | Writer | Current reader | Authority and identity | Current retention |
|---|---|---|---|---|
| `ChillSystem.status.observedGeneration` and Conditions | CHILL Operator system reconciler | users and Kubernetes tooling | `ChillSystem` generation plus observed DaemonSet and Deployment generations and status | Stored with the `ChillSystem`; updated only when the derived status changes |
| Node hardware discovery labels and annotations | node-discovery DaemonSet | DeviceClass discovery and operators | Kubernetes Node UID/resourceVersion is the current Node identity; discovery metadata identifies source and result | Stored on the Node; CHILL-owned keys are removed by `ChillSystem` finalization |
| DeviceClass discovery result annotations and class label on Nodes | DeviceClass discovery reconciler | DeviceClass discovery and later inventory consumers | Current Node plus discovery catalog and policy | Reconciled in place; CHILL-owned keys are removed on system finalization |
| discovered `DeviceClass.spec` and ownership metadata | DeviceClass discovery reconciler | compatibility and future inventory consumers | Derived from the current Node set and catalog; owned by one `ChillSystem` | Stale discovered classes are pruned; owned classes are deleted on system finalization |
| operator-managed DaemonSet and Deployment objects | node-discovery and Profiler operand reconcilers | Kubernetes and `ChillSystem` status observer | Native object UID/generation/status | Native ownership plus explicit finalization; no historical archive |
| Helm-owned ConfigMaps, RBAC, and base workload configuration | Helm release | Operator and operand startup paths | Helm release values and Kubernetes object identity | Helm release lifecycle |
| controller and workload logs | process and container runtime | `kubectl logs` and cluster logging, if installed | Kubernetes Pod/container identity | Kubernetes/runtime or external logging policy; CHILL defines no durable log retention |
| controller-runtime metrics | CHILL processes | Prometheus only when an endpoint is enabled and scraped | metric labels and scrape target identity | No CHILL retention; metrics endpoints are disabled by default in the current chart |

The current public catalog APIs do not add another persistence layer:

- `ModelSpec` stores artifact names, formats, digests, and runtime requirements,
  but no controller currently writes status and the schema does not locate the
  artifact bytes.
- `DeviceClass.status`, `ModelSpec.status`, `DeviceProfile`, and
  `ClusterEnergyModel` are empty schemas.
- There is no `ProfilingRun` API or Profiler Run reconciler. The deployed
  Profiler process currently has health, readiness, namespace-scoped cache, and
  leader-election plumbing only.

## Calculated but not persisted by production

The following internal packages return typed values to their caller. They do
not own Kubernetes objects, files, databases, retention, or deletion:

| Package | Values produced | Current persistence owner |
|---|---|---|
| `internal/modelcompat` | compatibility verdict and typed reasons | none |
| `internal/powerobserver` | source identity, raw samples, read failures, timing, and factual summary | none |
| `internal/profilingrun` | immutable intent identity and runtime-evidence validation | none |
| `internal/profilederivation` | admission verdict, energy estimates, points, uncertainty, and `b_sat` result | none |

This is intentional library responsibility separation, but it means those
values disappear unless an application layer explicitly retains them.

## Spike-only retained evidence

The profiling spike retains one content-addressed JSON bundle containing:

- the candidate report and immutable Run intent;
- raw Job, Pod, actual Node, and co-resident Pod snapshots;
- runtime result, identities, latency samples, and execution facts; and
- the unmodified PowerObserver result.

The collector hashes the exact encoded bytes, writes a temporary file, and
renames it to a digest-derived filename. The retained example is 389,364 bytes.
This demonstrates an evidence shape and local atomic write, not a production
durable store. The repository directory has no storage availability contract,
multi-writer behavior, access control, retention, garbage collection, or
recovery path.

Other spike observations, manifests, and Markdown reports are development and
research artifacts. They are not data emitted or managed by the running CHILL
system.

## Specification-required data without an implemented lifecycle

The system specification eventually requires more than single-Run evidence:

| Data family | Examples | Unimplemented lifecycle concern |
|---|---|---|
| immutable Run record | frozen inputs, actual execution identity, raw latency and power evidence, validation outcome | durable byte store, compact index, handoff, retry, and deletion |
| admitted profile facts | per-state latency and energy points, uncertainty, idle power, `b_sat`, thermal and throttled capacity | aggregation identity, publication object, history, freshness, and invalidation |
| transition evidence | transition time, energy, serving survival, and degradation | measurement identity, aggregation, and drift update |
| observation-quality evidence | metric staleness, gaps, timestamps, and source health | raw evidence retention versus operational time-series boundary |
| current scheduler inputs | queue/load observations, current inventory, admitted profile version, and availability | current authority, freshness, and fail-closed behavior |
| scheduler decisions and actuation outcomes | selected configuration, rule/input identities, requested and observed transition | audit depth and retention; not yet an implemented scheduler concern |
| research exports | campaign tables, plots, and evaluation datasets | export/reproducibility workflow outside the online control loop |

The specification does not imply that all these families belong in one CRD,
database, bucket, or retention policy.

## Confirmed gaps

The current system has no production contract for:

- a durable evidence sink or its health;
- an immutable evidence reference containing location, digest, schema, and
  size;
- storage credentials and tenant isolation;
- confirmation that evidence is durable before a Job or Run object is removed;
- retention, pinning, garbage collection, or explicit deletion;
- reconstruction after Profiler restart or cluster-object deletion;
- a searchable long-term metadata index; or
- versioned publication and invalidation of admitted profiles.

These gaps must be closed per data family. They are not evidence that CHILL
needs a universal storage subsystem.

## Audit conclusion

The first unresolved production boundary is the immutable single-Run evidence
handoff:

```text
measurement worker
  -> durable immutable bytes
  -> verified location + digest + schema + size
  -> compact ProfilingRun reference and admission status
```

That boundary is a prerequisite for a trustworthy Profiler Run reconciler.
Profile history, scheduler audit, and a general metadata index should not be
designed into the first sink without evidence that the first Run path needs
them.

The evidence store is a CHILL-managed operand. Its Deployment, Service,
persistent storage attachment, credential reference, health, and lifecycle
belong to the CHILL system boundary. The storage engine itself is a replaceable
dependency behind the S3-compatible protocol; CHILL does not implement an
object store.

This does not make storage operation a Profiler responsibility. The Operator
manages the evidence-store operand, the measurement worker writes one immutable
Run bundle, and the Profiler controller retrieves and verifies that bundle.
The Profiler depends on a logical CHILL evidence-store contract rather than a
storage product or caller-provided endpoint.

## Current-cluster storage candidate audit

The current cluster does not provide one existing storage path that CHILL can
adopt without crossing an ownership boundary:

- Rook CephFS and RBD CSI drivers are registered on `genesis1`, `genesis2`, and
  `genesis3`, but not on the heterogeneous edge Nodes. A profiling worker pinned
  to an edge Node therefore cannot assume that it can mount a Rook PVC.
- Kubeflow's MinIO is backed by a Kubeflow-owned Rook PVC, credentials, Service,
  and lifecycle. It is exposed as ClusterIP, and current ingress policies allow
  Kubeflow/profile, Istio, same-namespace, and explicitly WebDAV traffic—not
  `chill-system`. Reuse would require CHILL to modify and depend on another
  system's authorization and storage lifecycle.
- The existing WebDAV service is NodePort-accessible, but it has its own shared
  PVC, credentials, and watcher sidecar workflow. It does not establish the
  immutable create, digest verification, or CHILL retention contract.
- The static NFS volume is bound to `edge-ai-experiment` and its mount behavior
  on the KubeEdge Nodes has not been established. It is not a CHILL-owned sink.

Direct edge-worker PVC storage and reuse of an existing shared service are
therefore not accepted assumptions. The next experiment should deploy a
CHILL-owned evidence-store operand with an S3-compatible endpoint and dedicated
credentials, isolated from the existing shared services. It tests edge
reachability, immutable put/get, digest verification, duplicate writes, partial
failure, and restart recovery. Passing that spike validates the CHILL
management boundary and the replaceable protocol adapter; it does not make the
tested storage product part of the Profiler implementation.
