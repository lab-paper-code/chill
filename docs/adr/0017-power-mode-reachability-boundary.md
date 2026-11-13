# Power-Mode Reachability Boundary

## Status

Accepted

## Context

Jetson power modes may set GPU power-gating masks established at boot. A declared target and a reported mode name do not establish that the configured gating is active.

## Decision

Apply a Jetson mode in place when NVPModel supports it. On its explicit reboot-required refusal, record the target and use the actuator's own host restart to establish the mode at boot. Other refusals remain terminal failures.

The Jetson operand uses host PID access and only `SYS_BOOT`. Configuration validation rejects other added capabilities. A restart call that returns is a failure.

If independent observation confirms the target is already established, applying it does nothing. After a reboot, a failed target read-back terminates the transition; do not restart again to hide the failure.

Read-back checks configured power-gating masks as well as the reported mode. A target recorded without established gating fails observation. Every successful apply requires independent read-back before acceptance.

## Consequences

Campaigns may request catalogued modes across boot-mask classes, subject to actual actuator support and observed convergence.

A host restart interrupts all Pods on that Node. CHILL's measurement admission and serving reservations control its own operations under [Workload Pool Membership Boundary](0022-workload-pool-membership-boundary.md); they do not protect arbitrary external workloads from a requested reboot.

