# 0017 Power-Mode Reachability Boundary

## Status

Accepted

## Context

`DeviceClass.spec.powerModes` declares the named power modes and typed actuator
targets a device catalog offers. Catalog declaration does not establish that the
configured actuator can move a device into a mode, and reaching one mode does
not cost what reaching another does.

On the Jetson platform each mode carries a GPU TPC power-gating mask the device
establishes at boot. The actuator is asked for a reboot exactly when the target
mode's mask differs from the mask in effect. Reachability is therefore a
property of the transition and not of the target mode: two modes sharing a mask
are reached in place, and two modes in different masks are not.

An actuator that treats a reboot demand as a refusal leaves a catalogued mode
permanently unreachable and makes the reachable set an accident of whichever
mask the device happened to boot into.

## Decision

A declared target is a target. The actuator establishes the declared mode, and
where the device requires a reboot to establish it, that reboot is part of the
transition rather than grounds to refuse it. Take it through the actuator's
documented non-interactive path; do not answer an interactive prompt.

The rest of the actuator's contract is unchanged. Host command success is never
inferred, every apply is followed by an independent read-back before the state
is accepted, and an apply failure remains a typed terminal failure.

A transition that reboots is survivable because the actuation phase is made
durable before the host command runs and the next action is derived from that
durable record, never from what the actuator held in memory. An actuator process
that does not outlive its own command is an ordinary case here, not a fault.

## Consequences

Every catalogued mode is reachable, so a campaign's power-mode axis is the
catalog rather than one mask equivalence class.

A reboot ends every Pod on the Node, not only CHILL's. Node-wide workload safety
already gates the transition; what widens is the reach of what it permits, and
that is the same judgment the operator makes in declaring the target.

Transition cost is not one quantity. A transition that reboots pays for the boot
and a transition in place does not, so they are separate measured rows and are
not compared as though they were the same operation.

An apply whose process dies with the Node normally records nothing and the
transition resumes from its durable phase. A narrow race remains in which the
actuator records a terminal apply failure just before the Node goes down while
the transition in fact took effect. That costs a campaign step and a retry, and
it is recorded here rather than engineered against.
