# ChillSystem Status Surface

## Status

Accepted

## Context

`ChillSystem` is the cluster-scoped root for operator-managed runtime resources. Its status summarizes the availability of those resources.

## Decision

Keep status to `observedGeneration` and standard type-keyed conditions. Enabled managed components publish:

- `NodeAgentAvailable`
- `NodeAgentActuationAvailable`, covering the configured Jetson and NVIDIA actuation operands
- `ProfilerAvailable`
- `EvidenceStoreAvailable`, including the store's bootstrap state

`Ready` aggregates these observations. A false component makes it false; otherwise an unknown component makes it unknown. It is true when every enabled component is available, including when all components are disabled.

Conditions carry reason, message, observed generation and transition time. Preserve transition time when condition status remains unchanged. Write the status subresource only when the resulting status differs.

Owned DaemonSets, Deployments and Jobs, together with mapped Pod watches, request framework-derived reconciliation. Finalization and completed-Pod cleanup may requeue while work remains.

The operator Deployment uses its own Deployment status and health probes. `ChillSystem.status` does not report operator self-liveness or mirror Node and DeviceClass inventory.

## Consequences

Operators use `Ready` for installation availability and component conditions for its cause. Kubernetes resources remain authoritative for replica counts, Pod lifecycle, Node health and detailed diagnostics.

