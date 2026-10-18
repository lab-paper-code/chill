# 0017 Power-Mode Reachability Boundary

## Status

Accepted

## Context

`DeviceClass.spec.powerModes` declares the named power modes and typed actuator
targets a device catalog offers. Catalog declaration does not establish that the
configured actuator can move a device into a mode.

On the Jetson platform each mode carries a GPU power-gating mask the device
establishes at boot. A transition between two modes sharing a mask completes in
place; a transition into a mask the device did not boot with does not. That
makes reachability a property of the transition rather than of the target mode,
so it cannot be characterized from the target's name.

The tool separates the two cases itself. It refuses the second kind and names
the reason, and it offers a flag that records the target and claims to restart
the device. From a container without the capability the restart does not happen
and the flag still returns success, leaving a device whose reported mode names
the target while the target's gating is unset. The reported mode name is
therefore nominal: it says which mode was requested, not that it was
established.

## Decision

A declared target is a target. Where the tool applies the mode, that is the
whole transition. Where it refuses with the reboot demand, the mode is one the
device establishes at boot, so the actuator records the target and restarts the
Node into it and the boot establishes it in full.

The restart is the actuator's own, which is what makes its absence observable. A
restart that returns is a restart that did not happen, and the transition fails
rather than reporting a mode that was only recorded. The actuator holds the host
PID namespace and `SYS_BOOT` for this and nothing else, and the operator refuses
a configuration carrying any other capability.

Applying to a Node already holding the target does nothing. That is what bounds
the restart: the Node returns holding the mode, the repeated apply stops there,
and a second restart cannot follow.

Keep the existing fail-closed behavior. A refusal the tool gives for any other
reason stays a typed terminal failure, and every apply is still followed by an
independent read-back before the state is accepted. The read-back confirms the
gating mask in effect, not only the reported name: a mode whose configured
gating mask still reads ungated is recorded but not established, so the
read-back fails, and a Node that returns from its restart in that state fails
rather than restarting again.

## Consequences

Every catalogued mode is reachable, so a campaign's power-mode axis is the
catalog rather than one mask equivalence class.

A restart ends every Pod on the Node, not only CHILL's. Node-wide workload
safety gates the transition before the mode is recorded, and what widens is the
reach of what it permits, which is the same judgment the operator makes in
declaring the target.
