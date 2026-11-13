# Transition Ownership Boundary

## Status

Accepted

## Decision

A power-state transition is an independent operation. A ProfilingRun declares an established-state precondition and starts only after independent read-back confirms it. The Run consumes the observed state without causing, holding or restoring its transition.

Campaigns select transitions and measurements from their current projection. Multiple Runs may share one established state. Returning to a previous state requires a new desired-mode request.

The dedicated actuator owns host mutation, typed apply/read-back and the transition's terminal outcome under [Node Power Actuation Boundary](0015-node-power-actuation-boundary.md). A failed request records actual observed state and terminates. Adapter-local failure restoration retains its own bounded safety responsibility.

### Durable mutation fence

Persist pending state and `ApplyStartedAt` before host mutation. Lease acquisition is not part of this mechanism: the current state record lives on the Node. Preserve request identity, generation and bounded timing across reconciliation.

Independent target observation precedes settling and terminal Ready. Command exit alone cannot prove convergence. A reboot or intervening host-generation change that does not establish the target terminates the request, preventing another reboot for that request.

### No-op and measurement boundaries

For a Jetson target already independently observed, record successful established state without opening an apply fence or settling again. It yields no actual state-change sample. NVIDIA clock requests still apply the declared lock before acceptance because dynamic clocks may momentarily equal the requested MHz.

Actual transition-cost samples require a state change; repeated directed samples alternate targets. The transition-cost measurement owns its apply-marker, independent target read-back, fresh Ready-process witness and power window. Its scalar duration closes at the fresh Ready-process witness; declared post-Ready settling belongs to the energy observation window.

Ordinary within-state profiling must not overlap a state change. ChillServer owns measurement admission, the UID-bearing measurement taint, scope reservations and Node hand-back under [Workload Pool Membership Boundary](0022-workload-pool-membership-boundary.md). These boundaries serialize CHILL operations; direct external Node metadata writes remain outside their authority.

## Consequences

Established state persists until another transition changes it. Runs share state without an automatic return transition after every measurement.

Reboot reachability follows [Power-Mode Reachability Boundary](0017-power-mode-reachability-boundary.md). Transition duration, energy and survival evidence belong to their measurement operation, while Run evidence retains the state it measured.

