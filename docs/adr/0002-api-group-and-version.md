# API Group and Version

## Status

Accepted

## Decision

Use API group `edge.dacs.io` and version `v1alpha1` for CHILL CRDs.

The group and version identify manifest, RBAC, watch and stored-object contracts. Renaming them requires an explicit breaking change.

## Consequences

The alpha API permits schema exploration. Stored-contract changes follow [Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md).

Conversion webhooks require a separate API-promotion decision.

