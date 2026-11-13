# DeviceClass Status Boundary

## Status

Accepted

## Context

`DeviceClass` holds shared hardware capabilities and a Node selector. Kubernetes Nodes expose membership, health, schedulability and current resources.

## Decision

Keep `DeviceClass.status` empty. Consumers apply `spec.nodeSelector` to current Nodes. Do not duplicate member names, readiness, counts, taints or per-node execution state in the class resource.

CHILL power control stores per-node intent and observation on the Node. The desired power-mode label identifies intent; read-back, boot ID, generation, state and the last transition record identify observed execution state. The dedicated actuator owns host mutation and observation under [Node Power Actuation Boundary](0015-node-power-actuation-boundary.md).

Keep model/runtime installation and compatibility outside stable class capabilities. Derive scheduling eligibility at decision time from the requested workload, measured profiles, execution state and current Kubernetes resources.

Each observed field has one owner. Status owners publish meaningful current observations through their status boundary. Installation availability follows [ChillSystem Status Surface](0008-chillsystem-status-surface.md).

## Consequences

Class inventory remains independent of runtime control. Node and workload consumers use authoritative observations without a fan-out controller maintaining duplicated membership or Ready counts.

