# 0019 Durable Stored-Contract Compatibility

## Status

Accepted

## Context

Durable CHILL state outlives the process and release that wrote it. Kubernetes
objects remain in the API server, and execution evidence remains in its store
for later Run and campaign reconciliation. A current reader therefore encounters
forms written under earlier contracts.

On 2026-08-13, three strict-schema failures were observed on this testbed:

1. `NodeActuation` objects written before policy identity became required
   repeatedly failed with `validate projected NodeActuation: handoff policy
   identity is required without surrounding whitespace`. A restoration
   finalizer also blocked deletion, so removing the objects required manually
   stripping the finalizer.
2. `ProfilingRun` and `BatchSaturation` objects referred to retained execution
   evidence under a superseded schema. The Run controller and owning campaign
   repeatedly failed with `unsupported execution evidence schemaVersion` while
   loading that evidence.
3. `NodeActuation` objects written before observation boot identity became
   required repeatedly failed with `observation boot identity is required
   without surrounding whitespace`.

Each incompatibility was permanent for the stored object but traveled through
an ordinary reconcile error path and was retried without bound. The observed
symptoms disappeared only after the affected objects were deleted.

[Plan 9](../design/profiling/plans/09-durable-node-state-transition-control-path.md)
describes stored-schema compatibility for a proposed `NodeStateTransition`.
That plan is design only, with no approved or implemented scope, and does not
govern the existing objects or stored evidence that exhibited these failures.
A repository-wide decision is required at every durable storage boundary.

## Decision

The following are review obligations for every change to a durable CHILL object
or stored evidence contract. No repository tooling currently enforces them.

1. **Reader before writer.** Support for reading a new stored form is shipped
   and deployed before any deployed writer can persist that form. A rollout
   must not rely on all readers and writers changing atomically.
2. **No required meaning added in place.** A change that makes a previously
   absent or optional fact required, or changes a stored field's meaning or
   type, introduces a new served API version or stored-evidence version. It
   does not reuse the version whose contract it changes. Current writers may
   enforce current creation semantics without making retained objects
   undecodable by readers of their recorded version. Conversion may preserve
   existing facts but must not invent missing policy, identity, observation,
   or witness facts.
3. **Stored-version inventory before reader removal.** A change that retires a
   stored form establishes which versions and instances remain in every store
   the release reads before support is removed. Each retained instance is
   migrated, deliberately deleted or retired, or assigned its defined bounded
   compatibility outcome before the old reader is removed.
4. **Bounded incompatible-object outcome.** A reader distinguishes permanent
   incompatibility from a transient read or infrastructure failure. An object
   or evidence reference that the current reader cannot decode reaches one
   defined terminal outcome, visible to an operator, without returning an
   ordinary error that causes an unbounded reconcile loop. Consumers remain
   closed to the incompatible state.
5. **Finalizer safety.** A finalizer is not released while a mutation may have
   begun and recovery is unproven. For as long as a recovery finalizer can
   remain stored, the responsible reader retains a path that can read the
   deletion, mutation-fence, and recovery facts needed to decide and prove
   cleanup. Strict validation of unrelated current fields must not make an
   object undeletable. If recovery cannot yet be proved, the object reaches one
   operator-visible recovery-required outcome and remains fenced; it does not
   retry the same decode failure without bound.

The implemented `ProfilingRun` evidence path is the precedent for the bounded
outcome. Its [reconciler](../../internal/profiler/controller/intent_reconciler.go)
publishes `EvidenceVerified=False` with reason `EvidenceInvalid` for permanently
invalid retained evidence, and its [lifecycle](../../internal/profiler/lifecycle.go)
then selects the terminal `ActionComplete`. Focused
[projection tests](../../internal/profiler/controller/projection_test.go)
exercise that no-requeue result. This precedent supplies the required outcome
shape; it is not general compatibility enforcement.

## Consequences

Release order and stored-version inventory become part of reviewing any durable
schema change. Removing a reader is safe only after the inventory establishes
that no remaining stored instance depends on it or that every such instance has
entered its explicit bounded outcome.

Permanent incompatibility is recorded as object state instead of represented by
controller activity. Transient failures remain retryable. A resource whose
finalizer protects an external mutation requires a stable cleanup path across
the versions that may remain stored; an incompatible non-recovery field alone
cannot bypass restoration or prevent deletion forever.

This decision does not require a compatibility layer for its own sake, dual
decoders kept forever, or a general migration framework. A development testbed
may be cleaned by deliberately deleting old objects. The invariants govern how
a change is shipped and how affected objects converge; they do not require old
data to be preserved.
