# 0009 Single Helm Release Lifecycle

## Status

Accepted

## Context

CHILL initially used two Helm releases:

- `chill-operator`: CRDs, operator RBAC, and the operator Deployment.
- `chill`: the cluster-scoped `ChillSystem` root CR and system config.

This preserved a safe teardown order while the bootstrap surface was still
changing, but it leaked internal phase ordering into the operator workflow.
Users had to install and uninstall releases in the correct order, and manual
`helm uninstall chill-operator` could remove the operator before all runtime
cleanup intent was visible.

The intended product shape is one CHILL installation with one status root.
`ChillSystem` should remain the runtime root resource reconciled by the
operator, but users should not have to manage a separate operator release.

## Decision

Use `charts/chill` as the single public Helm chart and release. The chart
packages:

- CRDs
- operator ServiceAccount, RBAC, and Deployment
- the cluster-scoped `ChillSystem` root CR
- node-discovery ServiceAccount, RBAC, config, and discovery catalog

The public flow is:

```sh
helm install chill charts/chill --namespace chill-system --create-namespace
helm uninstall chill --namespace chill-system
```

The operator does not auto-create its own `ChillSystem` root. Helm owns the
root declaration; the operator owns runtime reconciliation below that root,
including finalization, node-discovery DaemonSets, node metadata cleanup, and
CHILL-managed `DeviceClass` resources.

`system.enabled=false` is reserved for install-only smoke tests and migration
windows. The default install creates the root `ChillSystem`.

CRDs stay templated under `charts/chill/templates/crds/` during `v1alpha1` so
Helm upgrades can update alpha schemas. CRDs keep `helm.sh/resource-policy:
keep`; destructive CRD deletion remains a guarded explicit action.

`helm uninstall` uses a `pre-delete` hook Job to delete the root
`ChillSystem` and wait for finalizer completion while the operator Deployment
is still present. This preserves the teardown ordering previously encoded by
the two-release workflow without exposing that ordering as a user-facing
install procedure.

## Consequences

Install and uninstall become one Helm command each.

Existing clusters that previously installed `chill-operator` may retain CRDs
annotated as owned by `chill-operator/chill-system`. Those CRDs must be adopted
once into `chill/chill-system` before the single chart can manage them:

```sh
make helm-adopt-crds \
  FROM_RELEASE_NAME=chill-operator \
  FROM_RELEASE_NAMESPACE=chill-system
```

Future runtime modules should be modeled under `ChillSystem` and packaged in
the single chart as desired root/config resources. Do not reintroduce a second
public Helm release for internal phase ordering.
