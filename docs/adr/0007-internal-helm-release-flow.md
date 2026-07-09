# 0007 Internal Helm Release Flow

## Status

Accepted

## Context

Helm applies resources in a fixed kind/name order, not as an application-specific dependency graph. CHILL is still small, but future modules will add more runtime dependencies: operator, node discovery, device-class reconciliation, and later scheduling or power-control components.

The public operator UX should stay Helm-shaped. Exposing a separate CHILL lifecycle command set would make users think about internal phase boundaries that should remain implementation detail.

## Decision

Keep the Helm chart as the resource packaging unit and keep dependency ordering inside `hack/helm-release-flow.sh`.

Public Make targets stay intent-oriented:

- `helm-preflight`
- `helm-install`
- `helm-start`
- `helm-stop`
- `helm-uninstall`
- `helm-purge-crds`

The internal install direction is:

```text
preflight -> install
```

`helm-install` follows product-style Helm UX and installs the runtime selected
by Helm values. The default chart starts the operator from the published
Docker Hub image. Site-specific values may also enable node-discovery during
install.

The internal cleanup direction is:

```text
stop(node-discovery -> operator) -> uninstall -> purge-crds
```

`helm-start` remains available for restarting a stopped release or enabling
runtime components after changing image values. `helm-stop` disables them in
reverse order.

`helm-uninstall` removes the Helm release after runtime components are disabled
and deletes the operator-created singleton `ChillSystem` status object. CRDs
remain by default. `helm-purge-crds` is a separate guarded destructive action.

## Consequences

Operators get a small Helm-oriented command surface, while CHILL-specific dependency intent remains centralized in one internal script.

Future runtime modules should extend the internal release flow first, then wire Helm values or manifests behind the public intent-oriented targets.
