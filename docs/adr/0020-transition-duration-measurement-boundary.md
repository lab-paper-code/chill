# 0020 Transition Duration Measurement Boundary

## Status

Accepted

## Context

ADR 0018 separates a transition from every Run that consumes the state it establishes and records that transition cost becomes measurable. It names transition duration τ, transition energy overhead η, and the transition-survival flag as quantities measured between states without bounding any of them.

The research specification names τ as the time one transition takes and fixes that interval in its measurement method: from the start of the transition until the new state is ready to receive requests.

A transition that changes state retains four Node-clock timestamps: the durable pre-mutation fence, the actuator's own target read-back, entry into settling, and terminal Ready. The intervals between them do not share one character. Between the fence and the read-back lie the device change, two host invocations, two control-plane round trips, and the two reconcile re-entries between them. Between the read-back and settling entry lies one further round trip. Settling is not observed at all: the actuator waits the duration the immutable spec declares.

One end-to-end number therefore adds a device property, controller-path cost, and a declared constant. Raising the declared settling duration would raise that number while no device behaved differently, which is what disqualifies it as a measured quantity.

## Decision

τ is not published as one scalar. A transition is a sequence of intervals, and each interval carries exactly one character:

- `device` — the state change the hardware performs;
- `controller path` — the durable fencing and control-plane round trips CHILL adds around it; and
- `declared` — an interval whose length the immutable spec fixes.

Intervals of different character are reported separately and are never summed. A result that reports one interval reports its character with it.

The measured boundary opens at the durable pre-mutation fence and closes at terminal Ready. Lease acquisition and previous-state observation precede the fence and are preparation rather than transition.

Current instrumentation cannot isolate the device interval. The fence round trip lies inside it, and that round trip is what distinguishes a state change from a crash before one, so it is not removable. Reporting the device interval as a device property requires measuring that round trip first; until then the interval is reported as device with controller path included and is named for what it contains.

ADR 0017 remains authoritative for which transitions restart the Node. A restarting transition and an in-place one share this decomposition.

## Consequences

The declared interval is isolated, so changing the settling duration cannot contaminate a measured one.

The specification's τ closes when the new state accepts requests. This boundary closes earlier, at established state. The serving-restoration interval — Pod rescheduling, container start, and model load — is unmeasured and belongs to a separate unit. No result under this boundary is presented as the specification's τ.

A transition into a state the Node already holds never opens the fence and yields no sample. τ samples come only from actual state changes, so repeated samples require alternating targets, which is one one-way transition in each direction.

A decomposition needs no new API. The four timestamps it reads are already durable on the transition. `NodeActuationTransitionCost` publishes an end-to-end sum and has no consumer; its field set is settled together with the unit that first measures transition cost.
