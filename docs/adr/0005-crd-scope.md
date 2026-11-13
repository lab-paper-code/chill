# CRD Scope

## Status

Accepted

## Decision

`DeviceClass` and `ModelSpec` are cluster-scoped resources.

`DeviceClass` describes shared node/device capabilities. `ModelSpec` is a cluster-wide model catalog for the research testbed.

## Consequences

Changing resource scope requires recreation and an explicit stored-object cutover. A future tenant-owned model catalog requires a new namespaced kind or an API-promotion decision; do not mutate the scope of stored `ModelSpec` objects in place.

