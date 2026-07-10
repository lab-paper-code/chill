# 0011 DeviceClass Status Boundary

## Status

Accepted

## Context

CHILL discovers hardware facts on Kubernetes Nodes, assigns a device-class
label, and reconciles `DeviceClass` resources from the device catalog. A
`DeviceClass` describes the shared capabilities of one device type and provides
the selector that maps Kubernetes Nodes to that class.

Kubernetes already owns Node lifecycle, health, readiness, schedulability, and
resource observations. Controllers and users can obtain the current members of
a `DeviceClass` by applying `spec.nodeSelector` to the live Node set. Copying
member names, `NodeReady`, or derived Ready counts into `DeviceClass.status`
would create a second, eventually consistent representation of Kubernetes-owned
state without adding a CHILL-specific control meaning.

The research design will eventually require CHILL-owned runtime state, including
desired/current configuration convergence and actuation progress. Those states
are per-node execution concerns, while `DeviceClass` is a class-level capability
resource. Defining their complete API before the corresponding observer,
actuator, and scheduler exist would introduce speculative fields with no
controller able to report them truthfully.

## Decision

Keep Kubernetes Nodes authoritative for class membership and Kubernetes-native
Node state. CHILL components that need current members or health list Nodes with
`DeviceClass.spec.nodeSelector` and inspect the Node API directly.

Keep `DeviceClass` focused on class-level capabilities. Its status does not
mirror:

- member Node names
- Node readiness, conditions, taints, or schedulability
- total or Ready Node counts
- per-node model, power mode, batching, telemetry, or transition state

`DeviceClass.status` remains empty until CHILL has a class-level observed
property that cannot be obtained directly from Kubernetes and a controller that
owns and continuously reconciles that property. New status fields are added
with the controller that can reconstruct them from observable state; fields are
not reserved with placeholder or permanently `unknown` values.

If CHILL introduces per-node control, its desired and observed execution state
belongs to a separate CHILL-owned resource. That resource follows the
Kubernetes `spec`/`status` contract: user or planner intent is stored in `spec`,
and the responsible observer or actuator reports current state and convergence
through `status`. `DeviceClass` may later expose a small class-level summary of
that CHILL-owned state when an operational consumer requires it, but it does not
become the owner of per-node state.

Scheduling eligibility is derived at decision time rather than persisted as an
intrinsic `DeviceClass` or Node condition. It can depend on a target model,
profile availability, current execution state, cluster headroom, and workload,
so a single stored eligibility value would be incomplete without that context.

Each status field has one owning controller. Controllers remain level-based and
reconstruct status from current observations rather than event history. Status
is written through the status subresource only when the owning controller has a
meaningful observation to publish.

`ChillSystem.status` remains the installation-level health surface established
by ADR 0008. It reports CHILL-owned runtime readiness and does not replace
queries to authoritative Node state when making a scheduling or actuation
decision.

## Consequences

The current Node discovery and `DeviceClass` reconciliation path is sufficient
to establish the device inventory boundary without an additional inventory
status controller. Model catalog and compatibility work can consume the same
Node and `DeviceClass` sources without waiting for duplicated Ready counts.

The API stays small and truthful: Kubernetes reports Kubernetes state, while
CHILL reports only state produced by CHILL-specific control loops. Per-node
execution state can evolve independently from the stable class capability API,
and future controllers gain explicit status ownership without competing writes.

Operators who need class membership or current Kubernetes health use standard
Node queries with the class selector. CHILL-specific summaries are introduced
only when their semantics, producer, and consumer exist together.
