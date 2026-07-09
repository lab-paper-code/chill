# 0008 ChillSystem Status Surface

## Status

Accepted

## Context

CHILL has internal ordering for install, startup, discovery, and cleanup, but that ordering should not become an operator-facing command model. Operators need a single status surface that answers whether the CHILL installation in `chill-system` is ready, progressing, or degraded, and why.

Rook-Ceph exposes this pattern through its root cluster CR: users inspect one resource for phase, message, health, and detailed conditions. CHILL needs the same UX shape, but with Kubernetes-native conditions as the primary status contract.

## Decision

Introduce a namespaced `ChillSystem` root status resource. The operator automatically creates one instance in the management namespace, named after the Helm release by default.

The public operational flow becomes:

```sh
kubectl -n chill-system get chillsystem
kubectl -n chill-system describe chillsystem chill
```

`ChillSystem.status` uses Kubernetes `conditions` as the durable API and keeps printer-column fields such as `phase`, `ready`, `operatorState`, `nodeDiscoveryState`, `message`, and resource counts for quick `kubectl get` output.

The first status implementation observes:

- operator Deployment readiness
- node-discovery DaemonSet readiness or disabled state
- observed Node count
- observed DeviceClass count
- observation errors such as missing RBAC

Helm does not create the `ChillSystem` custom resource directly. The operator owns the singleton creation to avoid coupling custom resource creation to same-chart CRD installation timing.

## Consequences

Operators get a single CHILL-native status object without learning the internal release-flow phase graph.

The status reconciler becomes the extension point for future modules. New components should add structured component status and conditions before adding ad hoc troubleshooting output elsewhere.

Because the operator writes this status, it cannot update status while the operator Deployment is scaled to zero. If CHILL needs offline uninstall status later, that should be handled outside the runtime status object rather than overloading `ChillSystem.status`.
