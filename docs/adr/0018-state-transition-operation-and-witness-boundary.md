# 0018 State Transition Operation and Witness Boundary

## Status

Accepted. This records an architectural boundary only. The redesign is neither
implemented nor scheduled.

## Context

ADR 0015 assigns Node-wide power mutation to the per-Node `NodeActuator` and
assigns the Profiler the orchestration around one actuated `ProfilingRun`. The
current code implements that assignment literally. `reconcileActuatedRun`
ensures the Run's durable handoff and publishes its reference, projects the Run,
Job, and handoff observations, and selects one of these actions: wait for target
readiness, create the measurement Job, hold the verified target while the Job is
active, request restoration after execution ends, wait for restoration, report
failure, or continue into the ordinary evidence lifecycle. While restoration is
pending, it publishes that the measurement outcome is withheld.

The NodeActuator lifecycle behind that handoff acquires and retains a Node
Lease, checks safety, observes the previous state, applies only when needed,
independently observes the target, settles, holds, restores when requested,
independently observes the restored state, and releases the Lease only after
restoration is proven. That lifecycle correctly keeps command completion
separate from observed convergence. It also makes a state transition a
precondition and cleanup phase inside one Run rather than an operation with its
own evidence.

That structure has two related defects.

First, the actor cannot be the only witness for a transition that stops the
actor. The `chill-node-actuator` DaemonSet runs on the Node whose state it
changes. It can apply and read back an in-place transition. During a reboot-class
transition, however, that process dies with the Node. Its later replacement
cannot by itself distinguish a reboot initiated by CHILL from an unrelated Node
crash.

Kubernetes exposes a durable, control-plane-visible witness in
`Node.status.nodeInfo.bootID`. A 2026-08-13 control-plane observation for
`jetsono` returned `2df858bf-fe42-4002-b01c-b3c6b4f8b5f8`. CHILL retains no boot
identity in transition or Run evidence. Recording the identity on both
sides of an in-place transition would prove that the boot did not change. That
has value before any reboot capability exists: a repetition of the 2026-08-13
MAXN/MODE_50W transition could replace today's inference from the absence of a
reboot demand with direct before/after boot-identity evidence.

ADR 0017 records the one observed NVPModel failure mode that requested a reboot
and CHILL's fail-closed refusal to answer that request. CHILL has never rebooted
a Node. All reboot-class reasoning in this decision follows from that failure
mode and the actor's Node-local lifetime, not from a reboot performed by CHILL.
ADR 0017 remains authoritative for the in-place reachability mechanism; this
decision does not restate it.

A reboot also changes the measurement boundary. A Node-local observer cannot
report until kubelet startup, CNI readiness, image availability, and Pod
scheduling allow it to run. A transition duration ending at that first reading
would therefore measure Kubernetes recovery, not only the device transition.
For a reboot-class transition, the transition-time instrument must be the
external wall-power meter. The same instrument measured the 2026-08-13 in-place
transition, where an external 2 Hz poller and CHILL's in-Job observer agreed to
0.03% over the same window.

Second, the transition is welded to the Run. The research specification defines
transition duration tau, transition energy overhead eta, and the
transition-survival flag as raw quantities measured between states, alongside
the quantities measured within a state. The current Run-owned lifecycle has no
separate evidence boundary for them. Each actuated Run also pays for a
transition to its target and a transition back to its previous state. If a
reboot-class edge were ever separately authorized, three Runs in one arm would
therefore imply six reboots of a shared Node. The Run lifetime would span both
reboots because its evidence remains withheld until restoration is verified.

## Decision

Treat a state transition as a first-class durable operation with its own record
and evidence. The record identifies the previous and target states, boot
identity before and after the operation, and lifecycle timestamps. Transition
evidence is separate from the evidence of every `ProfilingRun` that later uses
the established state.

A `ProfilingRun` declares a state precondition and waits until that state is
confirmed. This is a loose dependency: the Run does not cause, own, hold, or
restore the transition. Many Runs may execute against one established state. A
later move to another state is another explicit transition, not cleanup implied
by any one Run.

Transition confirmation is a control-plane responsibility. Transition
execution remains a Node-local responsibility because the NVPModel state can be
read and changed only through the Node-local host boundary. The actor publishes
its observations through durable objects; the control plane confirms the
transition from durable actor observations and witnesses that remain available
when the actor does not. These are separate roles, not one process acting and
attesting to its own survival.

Any transition whose actor cannot survive it requires an external witness.
Control-plane observation of the Node boot identity establishes whether the
boot changed and distinguishes the expected identity transition from an
unexplained disappearance. External wall-power observation supplies the device
transition window for tau and the power trace needed for transition-energy
evidence; a restarted Node-local Pod's first reading is not that boundary.

This decision does not authorize CHILL to reboot a Node. It does not choose a
reboot mechanism, grant privileges, or make the reboot-class path executable.
It only makes such a transition describable and independently witnessable. The
exact API, resource kind, schema, controller protocol, and migration sequence
belong to a future plan that does not yet exist.

This decision narrows and partially supersedes ADR 0015. It supersedes ADR
0015's assignment of a per-Run actuation state machine to the Profiler—creating
a Run-owned handoff, holding the target for that Run, requesting restoration
after its Job, and delaying Run completion until restoration. It also supersedes
the requirement that every transition handoff be universally bound to an
owning Run. It preserves ADR 0015's dedicated NodeActuator as CHILL's sole
Node-wide power-state writer, its Node-local `Observe` and `Apply` boundary,
independent read-back, Lease serialization, fail-closed workload safety, and the
requirement to prove any requested restoration.

ADR 0016 is unchanged: declared batch-size capability and execution-state
identity still determine what a Run may measure. This decision changes who owns
reaching an established state, not the artifact or batch capability within that
state. ADR 0017 is also unchanged except that a future decision about a
reboot-class edge would require the witness boundary defined here.

## Consequences

Transition cost becomes a measurable quantity with evidence separate from Run
evidence. A Run stops paying for transitions it does not measure, and repeated
Runs can share one already established state.

Any transition that can terminate its actor requires an external witness. Boot
identity and external wall-power observations have distinct jobs: the former
witnesses boot continuity or change from the control plane, while the latter
measures the device transition interval and energy.

The current Run-owned implementation remains in place until a separate plan is
approved and implemented. That plan must define durable ownership, concurrency,
failure recovery, state-precondition confirmation, evidence admission, and
migration without weakening ADR 0015's safety boundary.

The reboot capability itself is deliberately unresolved. Its execution
mechanism, security posture, authorization, recovery policy, and the blast
radius of rebooting a shared testbed Node require a separate decision and
evidence. Nothing in this ADR schedules or permits that work.
