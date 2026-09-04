# 0019 Durable Stored-Contract Compatibility

## Status

Accepted

## Context

Durable CHILL state outlives the release that wrote it. Kubernetes objects
remain in the API server and execution evidence remains in its store, so a
current reader encounters forms written under an earlier contract.

CHILL is a research prototype on a single testbed. Its stored objects are
measurement working state, not records anyone else depends on, and the testbed
is cleaned deliberately. Building compatibility machinery for that state would
cost more than re-running the measurement it protects.

## Decision

Keep readers strict and single-version. Do not add dual decoders, conversion
paths, stored-version migration, or a general compatibility framework.

A stored form that the current reader cannot decode is deleted, not migrated.
An operator removes the affected objects; where a finalizer guards an external
mutation, strict validation of unrelated fields must not make the object
undeletable.

Change a stored contract at most once per work unit, and not while a
measurement campaign depends on it. Bumping a version strands every object
written before it, so a bump that no reader needs is pure cost.

A reader that meets a form it cannot decode reaches one operator-visible
terminal outcome rather than returning an ordinary error that reconciles
without bound. The `ProfilingRun` evidence path is the implemented shape: its
[reconciler](../../internal/profiling/profiler/controller/run_reconciler.go) publishes
`EvidenceVerified=False` with reason `EvidenceInvalid`, and its
[lifecycle](../../internal/profiling/profiler/lifecycle.go) then selects the terminal
`ActionComplete`.

## Consequences

Stored measurement state is disposable. Re-running a measurement is the
recovery path, and the raw evidence archive outside the cluster is the durable
scientific record.

An unbounded reconcile loop on an undecodable object is a defect, because it
stalls the campaign that owns the object. A terminal outcome is required; a
compatibility layer is not.
