# Helm CRD Upgrade Strategy

## Status

Accepted

## Decision

Keep `v1alpha1` CRDs under `charts/chill/templates/crds/` so the single CHILL Helm release manages alpha schema updates.

CRDs carry `helm.sh/resource-policy: keep`. `crds.enabled` supports installs where CRDs remain owned separately.

Before a full Helm install against a real cluster, use `make helm-crd-check`. Use `make helm-adopt-crds` only after confirming that the previous release should relinquish ownership. Adoption is an explicit operator action.

Move CRDs to the chart-level `crds/` directory only with an API-promotion decision that ends routine schema updates through Helm.

## Consequences

Helm upgrades can update alpha schemas. Keep annotations, ownership and uninstall behavior require review because CRD deletion destroys stored objects.

