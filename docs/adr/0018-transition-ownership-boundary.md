# 0018 Transition Ownership Boundary

## Status

Accepted

## Context

ADR 0015 assigns Node-wide power mutation to the per-Node `NodeActuator` and
assigns the Profiler the orchestration around one actuated `ProfilingRun`. The
current code implements that assignment literally. `reconcileActuatedRun` ensures
the Run's durable handoff, projects the Run, Job, and handoff observations, and
selects one action: wait for target readiness, create the measurement Job, hold
the verified target while the Job is active, request restoration after execution
ends, wait for restoration, report failure, or continue into the ordinary
evidence lifecycle.

The NodeActuator lifecycle behind that handoff acquires a Node Lease, checks
safety, observes the previous state, applies only when needed, independently
observes the target, settles, holds, restores when requested, independently
observes the restored state, and releases the Lease only after restoration is
proven. That lifecycle correctly keeps command completion separate from observed
convergence. It also makes a state transition a precondition and cleanup phase
inside one Run rather than an operation of its own.

That has two consequences.

First, transition cost cannot be measured. The research specification defines
transition duration τ, transition energy overhead η, and the transition-survival
flag as raw quantities measured between states, alongside the quantities
measured within a state. A Run-owned transition has no evidence boundary for
them, and two Run-level rules actively prevent observing one:
[execution evidence](../../internal/profiler/evidence/actuation.go) is invalid if
any measurement container started before the actuation became ready, so power
sampling never covers the transition interval; and
[workload safety](../../internal/nodeactuator/controller/workload_safety.go)
fails closed while a foreign Pod occupies the Node, so no inference can be in
flight during an apply. Both rules are correct for a Run — a measurement window
must not contain the transition that establishes its state, and a Node must not
be mutated under someone else's workload. They are wrong only as constraints on
measuring the transition itself, which is evidence that the transition does not
belong inside a Run.

Second, each Run pays for transitions it does not measure. A Run whose target
differs from the current Node state transitions to its target and back again, so
N Runs in one arm imply 2N transitions of a shared Node, and each Run's lifetime
spans both because its evidence stays withheld until restoration is verified.

## Decision

A state transition is an operation separate from every `ProfilingRun` that
consumes the state it establishes.

A transition is atomic: one indivisible operation with a single terminal
outcome, one owner, and no temporal overlap with any measurement window. It has
no recovery chain — a failed transition records the observed actual state and
terminates. Returning to a previous state is not a cleanup phase but another
transition with that state as its target, so every state change is the same
operation.

A `ProfilingRun` declares a state precondition and executes only once that state
is confirmed by read-back. This is a loose dependency: the Run does not cause,
own, hold, or restore the transition, and it does not repair a state that does
not match. Many Runs may execute against one established state. Run evidence
retains the observed state it measured in; it does not retain the transition.

A campaign emits a transition as one step of the same decision loop that emits
its measurements, chosen from its current projection rather than from a
precomputed step list. Because the atom is the same unit for every consumer, a
campaign that establishes a state and a measurement of transition cost assemble
the same operation in different orders.

This decision narrows and partially supersedes ADR 0015. It supersedes ADR
0015's assignment of a per-Run actuation state machine to the Profiler—creating
a Run-owned handoff, holding the target for that Run, requesting restoration
after its Job, and delaying Run completion until restoration—and the requirement
that every transition be bound to an owning Run. It preserves ADR 0015's
dedicated NodeActuator as CHILL's sole Node-wide power-state writer, its
Node-local `Observe` and `Apply` boundary, independent read-back, Lease
serialization, fail-closed workload safety, and the requirement to prove any
requested restoration.

ADR 0016 is unchanged: declared batch-size capability and execution-state
identity still determine what a Run may measure. ADR 0017 remains authoritative
for power-mode reachability.

## Consequences

Transition cost becomes measurable, because the operation is separable in time
and in ownership from the measurement that consumes its result. A Run stops
paying for transitions it does not measure, and repeated Runs share one
established state.

State is not restored automatically. A Node holds its last established state
until another transition changes it, which is what allows consecutive Runs to
share one state and what removes the 2N-transition cost.

The Node Lease covers the transition rather than the measurement that follows
it, since holding it for a measurement is the Run ownership this decision
removes. Workload safety refuses to mutate a Node under a Pod outside the
configured allowed namespaces, but a measurement Job runs in a namespace that
configuration allows, so it does not by itself prevent a transition during a
measurement. What prevents one is that a campaign emits a transition before its
sweep and none during it, and that one campaign operates on a Node at a time.
That is an operating assumption of the testbed, recorded rather than enforced.
