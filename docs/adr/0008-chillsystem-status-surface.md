# 0008 ChillSystem Status Surface

## Status

Accepted

## Context

CHILL has internal ordering for install, startup, discovery, and cleanup, but that ordering should not become an operator-facing command model. Operators need a single status surface that answers whether the CHILL installation is ready, progressing, or degraded, and why.

Rook-Ceph exposes this pattern through its root cluster CR: users inspect one resource for phase, message, health, and detailed conditions. CHILL needs the same UX shape, but with Kubernetes-native conditions as the primary status contract.

## Decision

Introduce a cluster-scoped `ChillSystem` root resource. The single CHILL Helm chart creates this root CR, and the operator reconciles status and children from it.

The public operational flow becomes:

```sh
kubectl get chillsystem
kubectl describe chillsystem chill
```

`ChillSystem.status` uses Kubernetes `conditions` as the durable API and keeps printer-column fields such as `phase`, `ready`, `operatorState`, `nodeDiscoveryState`, `message`, and resource counts for quick `kubectl get` output.

The first status implementation observes:

- operator Deployment readiness
- node-discovery DaemonSet readiness or disabled state
- observed Node count
- observed DeviceClass count
- observation errors such as missing RBAC

The root CR has a finalizer. Deleting the CHILL Helm release deletes the root CR, allowing the operator to remove child runtime resources, node metadata, and CHILL `DeviceClass` resources during release teardown.

## Consequences

Operators get a single CHILL-native root and status object without learning the internal release-flow phase graph.

The status reconciler becomes the extension point for future modules. New components should add structured component status and conditions before adding ad hoc troubleshooting output elsewhere.

Because the operator owns finalization, the single chart must keep the operator and root CR in the same release instead of exposing separate public release ordering.
