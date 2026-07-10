# 0008 ChillSystem Status Surface

## Status

Accepted

## Context

`ChillSystem` is the cluster-scoped root for the runtime resources reconciled by
CHILL. Operators need one Kubernetes-native answer to whether the desired CHILL
runtime represented by that root resource is ready.

Kubernetes workloads and Nodes already expose their own lifecycle, health,
replica, and resource status. Copying those APIs into parallel phase fields,
component records, messages, or aggregate counts would create duplicate and
eventually inconsistent representations. The CHILL operator also cannot provide
a reliable self-liveness report through a status that stops updating when the
operator itself is unavailable.

## Decision

Keep `ChillSystem.status` limited to:

- `observedGeneration`, identifying the latest reconciled `ChillSystem` spec
- a standard `Ready` condition with status, reason, message,
  `observedGeneration`, and transition time

`Ready` reports whether the runtime resources currently required by the
`ChillSystem` spec are ready. The initial implementation owns node discovery:

- node discovery disabled: `Ready=True`
- required node-discovery DaemonSet fully Ready: `Ready=True`
- DaemonSet missing, pending, progressing, or degraded: `Ready=False`
- DaemonSet observation failed: `Ready=Unknown`

The condition reason and message explain progress or failure. Separate phase,
ready mirror, top-level message, Progressing/Degraded conditions, component
records, and Node or `DeviceClass` counts are not part of the status API.

The default `kubectl get chillsystem` view exposes only `READY` from the Ready
condition and `AGE`. Detailed diagnostics remain available from
`kubectl describe chillsystem` and the authoritative Kubernetes resources.

The operator Deployment is not summarized in `ChillSystem.status`. Kubernetes
Deployment status and the operator health/readiness probes remain authoritative
for the operator itself.

The reconciler is level-based. It observes the current desired runtime and
writes the status subresource only when the resulting condition changes. Watches
on owned runtime resources request reconciliation; periodic status polling is
not required for normal operation.

Future CHILL modules extend readiness only when the `ChillSystem` spec owns their
desired lifecycle and their reconciler can report an observable condition. New
status fields are not added solely for dashboard convenience.

## Consequences

`ChillSystem` provides one compact readiness contract without re-modeling
Deployments, DaemonSets, Nodes, or domain resources. Reason and message retain
the information needed to distinguish disabled, progressing, missing, degraded,
and unobservable runtime states.

Removing duplicated fields is an intentional `v1alpha1` API change. Existing
stored status is replaced by the minimal condition on the next successful
reconciliation, and generated CRDs and printer columns follow the reduced API.

Operators use standard Kubernetes queries for component replica details, Node
health, and domain-resource inventory. CHILL status remains reserved for state
owned by CHILL reconciliation.
