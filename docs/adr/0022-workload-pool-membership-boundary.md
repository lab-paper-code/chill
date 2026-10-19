# 0022 Workload Pool Membership Boundary

## Status

Accepted. Supersedes the workload-exclusivity clause of
[0015 Node Power Actuation Boundary](0015-node-power-actuation-boundary.md),
the workload-safety sentence in the Consequences of
[0017 Power-Mode Reachability Boundary](0017-power-mode-reachability-boundary.md),
and the "one campaign operates on a Node at a time" operating assumption of
[0018 Transition Ownership Boundary](0018-transition-ownership-boundary.md).

## Context

Each Workload now has its own active POOL, a Node may belong to the active POOLs
of several Workloads that reference the same model, profiling and transition-cost
measurement run only in a measurement POOL, and every other ready worker Node is
an available Node that the scheduler keeps at its lowest power mode and counts in
total energy.

The current boundary cannot express this:

- Membership is one scalar Node label, `edge.dacs.io/serving-pool`, written by
  one mover. A Node cannot be in two active POOLs, and the label is state that
  must be kept in step with the instances that actually serve.
- Measurement isolation is a `measurement-*` value of the same label, set and
  cleared by hand around `serving-operation`. Profiling never checks it.
- The NodeActuator refuses Node-wide actuation while any Pod outside configured
  namespaces runs on the Node. CHILL then cannot lower an available Node that
  hosts someone else's Pod, and a measurement fails for a Pod CHILL does not own.

## Decision

The entire POOL is every Kubernetes Node without the control-plane role label.
Scheduler configuration decides only where instances may be placed and how power
is actuated; a Node lacking it stays in the POOL with a reason.

Active POOL membership is derived, not stored. A Node is in Workload W's active
POOL while a CHILL serving instance owned by or bound to W runs on it. Moving a
Node between POOLs is the prepare and remove actions of one transition; no
membership action or membership label exists.

The measurement POOL is one Node annotation, `edge.dacs.io/measurement-pool`,
whose value is the full identity of the measurement that holds the Node,
including its Kubernetes UID, or a fresh UUID per `serving-operation` measurement. The
measurement executor (the `BatchEnergyOptimization`, `ModelLoadedIdleBaseline` and
`PowerModeTransitionCost` controllers, and `serving-operation`) acquires it before
any NodeActuation, Run, Job or measured instance for the Node, and releases it
when its own work has ended, including on failure. Acquisition refuses a Node
that runs a CHILL serving instance. Run, NodeActuation and Job creation require
the annotation to name their own parent measurement. The scheduler never plans
on an annotated Node and adds the annotated Nodes' observed consumption to every
candidate as one common term.

The annotation is the only hold marker; no lock object or fence serializes it.
Acquisition refuses a Node with CHILL serving Pods and backs off while another
owner's NodeActuation is unfinished. Serving Pod creation and the NodeActuator
read the annotation right before acting and refuse when it names another owner.
Measurement instances (the transition runner and `serving-operation` instances)
are observed but never routed or counted in an active POOL. A process that
waits on another holder retries on a fixed interval, since no controller
watches Nodes.

ponytail: a check and the action it guards are separate API calls, so an
acquisition landing between them can leave a serving Pod or a power change on a
measured Node. The recorded Pod list and NodeActuations of each measurement
expose such an overlap; a resourceVersion post-check is added when one is
observed.

A transition-cost measurement observes a loaded runner. ChillServer, which owns
serving Pod creation, creates that runner and its Service for the measurement
holding the Node, records the chosen profile on the Service, replaces the runner
with that same profile when a restart-type transition ends it, and deletes both
once the measurement is terminal. The measurement releases the Node only after
both are gone. Runner and Service carry the measurement UID, and ChillServer
touches no object without that marker.

Measured evidence selects itself. Every successfully completed profile, operation
cost, idle baseline and power-mode transition cost under its fixed location is
read, kept only when its meter matches the current declaration, and the latest
per full condition is used, including the Node power mode for operations and the
loaded state for power-mode transitions; one invalid record is reported and
skipped. No configuration lists which measurement applies.

CHILL manages only the Pods it places, marked by the `edge.dacs.io/workload-role`
label on every creation path. The NodeActuator does not inspect other Pods.
Measurements proceed beside them and record the Node's Pods with that
classification. Energy uses the whole-Node meter as measured.

Lowering an available Node to its lowest power mode is a fixed rule. Every
candidate starts from all available Nodes at their lowest mode, and the lowering
heads each such Node's action chain, so any selected action on that Node follows
it. Its time and energy are excluded from the comparison and recorded as an
assumption on each decision, so no empty-Node transition cost is measured.
Re-entering an available Node into an active POOL is a candidate transition whose
cost is compared; its power raise uses the transition cost measured with the
runner it is about to prepare, also recorded as an assumption.

## Consequences

Membership follows the actual instances, so partially completed transitions are
reported as they are. There is no label to repair after a failed move.

One annotation holder per Node enforces one measurement per Node, which ADR 0018
recorded as an assumption.

A Node without an unloaded baseline leaves total energy unevaluable, so the
scheduler waits until that baseline is measured rather than comparing a partial
POOL.

A measurement object deleted while it holds a Node leaves the annotation in
place; an operator removes it with `kubectl annotate node`. This is accepted
until it occurs in practice.

External Pods can perturb measurements and serving. That perturbation is part of
the measured environment and is visible in the recorded Pod list; CHILL does not
prevent it.
