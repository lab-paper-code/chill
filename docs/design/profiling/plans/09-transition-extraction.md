# Plan 9: Transition Extraction

> Navigation: [Profiling design index](../README.md)
>
> Boundary: [ADR 0018](../../../adr/0018-transition-ownership-boundary.md),
> [ADR 0015](../../../adr/0015-node-power-actuation-boundary.md)
>
> Reachability: [ADR 0017](../../../adr/0017-in-place-power-mode-reachability-boundary.md)

## Status

Partially implemented. `NodeActuation` is a one-way transition with a single
terminal outcome and no Run owner. A non-fixed Run confirms its frozen state
from the newest successful transition on its target Node, records that
transition, and only then creates its Job. Campaign assembly remains in this
plan. ADR 0018 owns the boundary.

## Question

> The specification measures transition duration τ, transition energy overhead
> η, and the transition-survival flag between states. How does one independent
> transition establish a state that many Runs can consume without placing that
> transition inside a Run's measurement window?

## Ownership boundary

Transition ownership is independent of `ProfilingRun`. The `NodeActuation`
object and the per-Node actuator perform apply and independent read-back.

`NodeActuation` is a one-way operation toward a declared target. Its
terminal outcome is the declared target state; returning to an earlier state is
another operation with that state as its target. One object therefore records
one state change with one terminal outcome, which is the property that makes it
measurable and reusable.

A `ProfilingRun` declares the state it requires. The Run controller confirms
that state by read-back before execution; a Run whose required state is not
established does not run and does not repair it. Run evidence retains the
confirming transition identity, the observed target state, and the transition
completion time; the previous state remains transition-owned evidence.

For a Run with a configured actuation policy, confirmation lists
`NodeActuation` objects in the Run namespace, filters successful terminal
`Ready` objects by exact target Node name and UID, and selects the greatest
`status.readyAt`. The selected transition's declared class, mode, and actuator
target and its target read-back must match frozen intent. The Run records its
name and UID before creating the Job. No completed transition, or a newest
successful transition for another mode, produces terminal
`RequiredStateNotConfirmed` without a Job. A Run without an actuation policy
executes directly.

This confirmation can be stale and that limitation is accepted. Nothing else
changes Node power state on this testbed, and operating procedure emits no
transition during a measurement sweep. The Run does not re-observe the host or
add an epoch, fence, claim, state-generation counter, or tenure check.

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

Until the campaign action vocabulary emits transition atoms, an operator
submits each required `NodeActuation` manually. There is no automatic Run
fallback when confirmation is absent.

## Verification

The Run boundary is verified when an unconfirmed Run reaches terminal
`RequiredStateNotConfirmed` without creating a transition or Job, a confirmed
Run records the newest successful matching transition before Job creation, and
evidence rejects a Job container that started before that transition completed.
The extraction is complete when a `BatchSaturation` sweep of N Runs in one state
performs one transition rather than 2N.
