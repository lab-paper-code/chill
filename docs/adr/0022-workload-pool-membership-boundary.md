# Workload Pool Membership Boundary

## Status

Accepted

## Decision

The resource pool includes Kubernetes worker Nodes without the control-plane role label. Scheduler configuration controls placement and actuation eligibility; unavailable configuration leaves the Node visible with a reason.

Derive a Workload's active pool from CHILL instances owned by or bound to it. One Node may serve several Workloads. Prepare and remove operations change membership through actual execution state; no separate membership label or mover is maintained.

ChillServer is the single writer of `chill.dacs.io/measurement:NoSchedule`. It admits a measurement onto a Node without conflicting CHILL serving Pods or pending power changes, reserves its scope, and sets the holder object's UID as the taint value. It reports a conflicting serving Pod explicitly.

Measurement Pods tolerate the taint. Because serving Pods use `spec.nodeName`, ChillServer also checks measurement ownership immediately before ordinary prepare and power operations. Measurement-authorized operations use their holder identity.

When the measurement ends or disappears, hand-back waits for pending effects, removes its remaining CHILL serving Pods, lowers the Node and then removes the taint. A power change inside an incompatible measurement window invalidates that measurement.

CHILL marks its Pods with `edge.dacs.io/workload-role` and manages only its own operations. The actuator does not enforce foreign-Pod exclusivity. External workloads can perturb whole-Node power and serving; a measurement must retain the evidence its specific contract owns.

Select completed evidence by full execution conditions and current meter declaration. Report and skip invalid records. Use the latest eligible evidence without a configuration-maintained list of selected measurements.

Empty-Node saving is an admitted candidate subject to common-period energy comparison, payback and minimum gain under [Current-Demand Control and a Fixed Energy Comparison Period](0025-two-tempo-control.md). Its transition costs are not unconditionally cancelled as a common term. Measurement hand-back has its own explicit lowest-mode cleanup responsibility.

## Consequences

Membership and partial transitions follow physical instances. Missing baseline or operation-cost evidence excludes affected comparisons without fabricating zero cost.

The [measurement admission design](../design/serving/measurement-admission.md) owns reservation and hand-back details. Actuator privilege and reboot reachability follow [Node Power Actuation Boundary](0015-node-power-actuation-boundary.md) and [Power-Mode Reachability Boundary](0017-power-mode-reachability-boundary.md).

