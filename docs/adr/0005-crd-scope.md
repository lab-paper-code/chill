# 0005 CRD Scope

## Status

Accepted

## Context

CRD scope is a hard API boundary. Changing a CRD from cluster-scoped to namespaced, or the reverse, is not an in-place migration; it requires deleting and recreating the CRD and migrating stored objects.

The first API set is:

- `DeviceClass`
- `ModelSpec`
- `DeviceProfile`
- `ClusterEnergyModel`

## Decision

Generate all four first-wave CRDs as cluster-scoped resources.

`DeviceClass` describes node/device infrastructure, similar in role to `StorageClass` or `RuntimeClass`.

`DeviceProfile` records measured infrastructure state and transition costs.

`ClusterEnergyModel` is explicitly a cluster-level model.

`ModelSpec` is the only ambiguous resource. It could become tenant-owned in a multi-tenant serving platform, but CHILL starts as a single research-cluster controller where a cluster-scoped catalog is simpler and avoids namespace duplication.

## Consequences

The initial controller can reconcile cluster infrastructure through one global API surface.

If multi-tenancy becomes a real requirement, do not mutate `ModelSpec` scope in place. Introduce a new namespaced kind or a `v1beta1` API migration plan and keep the breaking scope change explicit.

Measurement Jobs are namespaced while `DeviceProfile` is cluster-scoped. Envtest covers API-server acceptance of namespaced Jobs with cluster-scoped owner references; a later kind e2e test must cover actual garbage collection because envtest does not run the Kubernetes garbage collector controller.
