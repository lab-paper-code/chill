# 0017 Power-Mode Reachability Boundary

## Status

Accepted

## Context

`DeviceClass.spec.powerModes` declares the named power modes and typed actuator
targets a device catalog offers. Catalog declaration does not establish that the
configured actuator can move a device into a mode.

On the Jetson platform each mode carries a GPU TPC power-gating mask the device
establishes at boot. A transition between two modes sharing a mask completes in
place; a transition into a mask the device did not boot with does not. That
makes reachability a property of the transition rather than of the target mode,
so it cannot be characterized from the target's name.

The actuator tool offers a flag that claims to reboot when a switch requires
one. It does not reboot from the actuator's environment, and it does not refuse
either. It applies the parameters it can and returns success, leaving a device
whose reported mode names the target while the target's gating parameter is
unset. The tool's own verbose query shows the disagreement; its plain query,
which is what a read-back reads, does not.

The reported mode name is therefore a nominal value. It says which mode was
requested, not that the mode was established.

## Decision

Authorize a transition only where the actuator establishes the mode in fact.
For this platform that is the set of transitions staying within the mask
equivalence class the device booted into, and it excludes the force flag, whose
success is indistinguishable at the read-back from a mode that was established.

Reaching a mode in another class requires the Node to reboot into it. The
actuator cannot reboot a Node, and granting it that is a decision this one does
not take. Until it is taken, a mode outside the booted class is unreachable and
is declared as such rather than approximated.

Keep the existing fail-closed behavior. An apply failure remains a typed
terminal failure and does not start measurement.

## Consequences

A campaign operating within the booted mask class has a two-point power-mode
axis on the tested class, and a four-point energy-versus-capacity curve is not
obtainable through this path.

Read-back compares a mode name, and a mode name does not distinguish an
established mode from a partly applied one. Any actuator path that can apply a
mode partially can therefore produce a transition that verifies and settles
without the device holding the state. Closing that gap means observing the
parameters a mode is made of, not the name the tool prints for it.
