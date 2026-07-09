# 0007 Internal Helm Release Flow

## Status

Accepted

## Context

Helm applies resources in a fixed kind/name order, not as an application-specific dependency graph. CHILL is still small, but future modules will add more runtime dependencies: operator, node discovery, device-class reconciliation, and later scheduling or power-control components.

The public operator UX should stay Helm-shaped. Exposing a separate CHILL lifecycle command set would make users think about internal phase boundaries that should remain implementation detail.

## Decision

Keep Helm charts as the resource packaging unit and keep dependency ordering inside `hack/helm-release-flow.sh`.

Split packaging into two releases:

- `chill-operator`: CRDs, operator RBAC, and the operator Deployment.
- `chill`: the cluster-scoped `ChillSystem` root CR and system config.

Public Make targets stay intent-oriented:

- `helm-preflight`
- `helm-install`
- `helm-uninstall`
- `helm-purge-crds`

The internal install direction is:

```text
preflight -> install-operator -> install-system
```

`helm-install` follows product-style Helm UX and installs the operator first,
then creates the `ChillSystem` root resource that drives runtime reconciliation.

The internal cleanup direction is:

```text
uninstall-system(ChillSystem finalizer cleanup) -> uninstall-operator -> purge-crds
```

`helm-uninstall` removes the system release first. Deleting the `ChillSystem`
root CR triggers operator finalization, which removes operator-managed
DaemonSets, node metadata, and CHILL `DeviceClass` resources before the
operator release is removed. CRDs remain by default. `helm-purge-crds` is a
separate guarded destructive action.

## Consequences

Operators get a small Helm-oriented command surface, while CHILL-specific dependency intent remains centralized in Kubernetes reconciliation and one internal script.

Future runtime modules should be modeled under the `ChillSystem` root resource first. Helm should package the desired root/config resources, not become a CHILL-specific lifecycle engine.
