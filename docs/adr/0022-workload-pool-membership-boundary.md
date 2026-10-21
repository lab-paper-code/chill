# 0022 Workload Pool Membership Boundary

## Status

Accepted. Supersedes the workload-exclusivity clause of
[0015 Node Power Actuation Boundary](0015-node-power-actuation-boundary.md) and
the workload-safety sentence in the Consequences of
[0017 Power-Mode Reachability Boundary](0017-power-mode-reachability-boundary.md).
Measurement POOL admission is decided by migration step M3 of the adopted
[architecture rethink](../design/architecture-rethink/README.md).

## Context

Each Workload now has its own active POOL, a Node may belong to the active POOLs
of several Workloads that reference the same model, profiling and transition-cost
measurement run only in a measurement POOL, and every other ready worker Node is
an available Node that the scheduler keeps at its lowest power mode.

The current boundary cannot express this:

- Membership is one scalar Node label, `edge.dacs.io/serving-pool`, written by
  one mover. A Node cannot be in two active POOLs, and the label is state that
  must be kept in step with the instances that actually serve.
- The NodeActuator refuses Node-wide actuation while any Pod outside configured
  namespaces runs on the Node. CHILL then cannot lower an available Node that
  hosts someone else's Pod, and a measurement fails for a Pod CHILL does not own.

## Decision

The entire POOL is every Kubernetes Node without the control-plane role label.
Scheduler configuration decides only where instances may be placed and how power
is actuated; a Node lacking it stays in the POOL with a reason.

Active POOL membership is derived, not stored. A Node is in Workload W's active
POOL while a CHILL serving instance owned by or bound to W runs on it. Moving a
Node between POOLs is the prepare and remove actions of one transition. The
`edge.dacs.io/serving-pool` label, its mover and every membership action are
removed.

The measurement POOL is the Node taint `chill.dacs.io/measurement:NoSchedule`.
Until M3 an operator adds it before a measurement and removes it afterwards;
measurement Pods tolerate it. Because CHILL creates serving Pods with
`spec.nodeName`, which bypasses kube-scheduler, ChillServer itself never places
on, lowers, or changes the power of a tainted Node, and checks the taint right
before a Workload prepare or power change. A taint denies by default, so a
forgotten removal only leaves a Node unused. M3
([measurement admission](../design/serving/measurement-admission.md)) makes
ChillServer the only writer of the same taint: it admits a measurement object
onto an empty Node with the object's UID as the taint value, refuses a Node that
runs a CHILL serving instance, uses that UID as the fencing token on power
changes, creates the transition-cost runner, and removes the taint when the
object ends or disappears.

Measured evidence selects itself. Every successfully completed profile, operation
cost, idle baseline and power-mode transition cost under its fixed location is
read, kept only when its meter matches the current declaration, and the latest
per full condition is used; one invalid record is reported and skipped. No
configuration lists which measurement applies.

CHILL manages only the Pods it places, marked by the `edge.dacs.io/workload-role`
label on every creation path. The NodeActuator does not inspect other Pods.
Measurements proceed beside them and record the Node's Pods with that
classification. Energy uses the whole-Node meter as measured.

Lowering an available Node to its lowest power mode is a fixed rule shared by
every candidate, so its energy is a common term eliminated from the comparison;
candidates compare only the Nodes whose state they change, and full-POOL energy
is reported from measurements. Re-entering an available Node is a candidate
transition whose cost is compared: the power-mode transition measured on the
empty Node plus the prepare measured at the target mode. A transition shorter
than the meter resolves has no cost, and that candidate is not evaluable.

## Consequences

Membership follows the actual instances, so partially completed transitions are
reported as they are. There is no membership label to repair after a failed move.

A Node without an unloaded baseline blocks only candidates that change that Node.

External Pods can perturb measurements and serving. That perturbation is part of
the measured environment and is visible in the recorded Pod list; CHILL does not
prevent it.
