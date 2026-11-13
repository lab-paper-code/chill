# Durable Stored-Contract Compatibility

## Status

Accepted

## Context

CHILL's Kubernetes objects and stored evidence outlive the release that wrote them. Measurement working state on the research testbed is disposable; scientific raw archives and published records remain preserved.

## Decision

Keep readers strict and single-version. Do not add dual decoders, conversion paths, stored-version migration or a general compatibility framework.

An operator deletes affected working objects that current readers cannot decode. A finalizer protecting an external mutation must remain usable when unrelated stored fields are invalid.

Every writer keys a stored object under its own owner ID. Measurement deletion (BatchEnergyOptimization and DispatchOccupancyMeasurement) therefore decides what to remove from native UIDs and those keys, never by decoding stored payloads, so an object the current reader rejects stays deletable.

Change a stored contract at most once per work unit, outside campaigns that depend on it. Avoid version bumps without a reader requirement.

Finalization validates raw execution evidence once. Retained execution readers bound object bytes and decode the current schema. Contract cutovers never rewrite archived scientific raw records or published results.

An undecodable object must reach one operator-visible terminal outcome. ProfilingRun publishes `EvidenceVerified=False` with reason `EvidenceInvalid`; its lifecycle selects `ActionComplete`.

## Consequences

Re-running a measurement recovers disposable working state. An unbounded reconcile loop on undecodable evidence is a defect because it blocks the owning campaign.

