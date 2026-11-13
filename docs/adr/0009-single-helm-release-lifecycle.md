# Single Helm Release Lifecycle

## Status

Accepted

## Decision

Use `charts/chill` as the single public Helm chart and release. Helm declares CRDs, operator resources, the `ChillSystem` root, and configured services, RBAC and runtime inputs.

Install with:

```sh
helm install chill charts/chill --namespace chill-system --create-namespace
```

The supported uninstall sequence is:

```sh
kubectl delete chillsystem chill --wait
helm uninstall chill --namespace chill-system --no-hooks
```

The operator reads the Helm-declared root and owns runtime reconciliation and finalization below it. Finalization removes managed operands, CHILL-owned Node metadata and CHILL-managed DeviceClass resources.

Node metadata survives individual discovery Pod replacement, eviction and exit. Only explicit system finalization removes that inventory.

The default install creates the root. `system.enabled=false` supports install-only checks and explicit migration windows.

Alpha CRDs remain templated with `helm.sh/resource-policy: keep`, following [Helm CRD Upgrade Strategy](0003-helm-crd-upgrade-strategy.md). Destructive CRD deletion remains a guarded explicit action.

`make helm-uninstall` performs root finalization followed by Helm removal. A `pre-delete` hook deletes the root as a safety net for direct `helm uninstall`.

## Consequences

Installation uses one release. Runtime cleanup must finish while the operator still runs. Runtime modules extend the existing root lifecycle and chart without adding a public release for internal ordering.

