# Plan 9: Transition Extraction

> Navigation: [Profiling design index](../README.md)
>
> Boundary: [ADR 0018](../../../adr/0018-transition-ownership-boundary.md),
> [ADR 0015](../../../adr/0015-node-power-actuation-boundary.md)
>
> Reachability: [ADR 0017](../../../adr/0017-in-place-power-mode-reachability-boundary.md)

## Status

Approved for the scope below and not yet implemented. ADR 0018 owns the
boundary; this plan owns the change that realizes it.

## Question

> The specification measures transition duration τ, transition energy overhead
> η, and the transition-survival flag between states. A `ProfilingRun` owns the
> transition that establishes its own state, so no window exists in which those
> quantities can be observed. What is the smallest change that makes a
> transition separately observable and separately assemblable?

## What moves

Transition ownership leaves `ProfilingRun`. The mechanism does not move: the
`NodeActuation` object and the per-Node actuator already perform apply and
independent read-back, and neither is redesigned.

`NodeActuation` becomes a one-way operation toward a declared target. Its
restore phase is removed, because returning to an earlier state is another
operation with that state as its target. One object therefore records one state
change with one terminal outcome, which is the property that makes it
measurable and reusable.

A `ProfilingRun` declares the state it requires. Admission confirms that state
by read-back and executes; a Run whose required state is not established does
not run and does not repair it. Run evidence retains the observed state and no
longer retains transition facts.

## The atom

One transition is one indivisible operation with a single terminal outcome and
one owner. It does not overlap a measurement window. A failure records the
observed actual state and terminates without a recovery chain or a retry
sequence.

That failure rule is deliberate. Automatic recovery would reintroduce a
transition whose extent is decided by the system rather than by the assembler,
and an operation whose extent is not fixed cannot be a measurement subject.

## How a campaign assembles it

`Decide` resolves one step per reconcile from a `Snapshot` built only from the
immutable spec and owned objects, and delegates the measurement question to the
method, which returns exactly one next measurement or a terminal result. Because
the next step follows from the previous result, no precomputed step list exists.

A transition therefore joins the decision's action vocabulary rather than a
list. When the required state is not established, the decision emits one
transition atom and waits; otherwise it proceeds to measurements as before.

The observed Node state is projected from the owned transition atom, not from
reported status, so `Snapshot` keeps its property that a decision cannot depend
on previously reported progress.

One `BatchSaturation` measures one state. A two-state comparison is two objects.
A campaign therefore emits at most one transition before its sweep and none
during it, and `sequential-early-stop` is unchanged. A failed transition is a
terminal campaign outcome; the retry limit governs trials, not transitions.

## Scope boundary

The following are excluded and no part of this plan depends on them: a new
resource kind; state epochs, fences, tenure, claims, or successor protocols;
recovery state machines; stored-schema compatibility readers; reboot-class
actuation; and the transition-cost measurement itself, which is a separate unit
that consumes the atom this plan produces.

The stored contract changes once, when the extraction is complete. Objects
written under the prior contract are deleted rather than migrated, per
[ADR 0019](../../../adr/0019-durable-stored-contract-compatibility.md).

## Verification

The extraction is complete when a `BatchSaturation` sweep of N Runs in one state
performs one transition rather than 2N, when a Run whose required state is not
established reaches a terminal outcome without actuating, and when a transition
atom can be created and observed independently of any Run.
