# 0005 CRD Scope

## Status

Accepted

## Context

CRD scope changes require CRD recreation and stored-object migration, not an in-place update.

The first API set is:

- `DeviceClass`
- `ModelSpec`

## Decision

Generate both first-wave CRDs as cluster-scoped resources.

`DeviceClass` describes node/device infrastructure, similar in role to `StorageClass` or `RuntimeClass`.

`ModelSpec` is the only ambiguous resource. It could become tenant-owned in a multi-tenant serving platform, but CHILL starts as a single research-cluster controller where a cluster-scoped catalog is simpler and avoids namespace duplication.

## Consequences

The initial controller can reconcile cluster infrastructure through one global API surface.

If multi-tenancy becomes a real requirement, do not mutate `ModelSpec` scope in place. Introduce a new namespaced kind or a `v1beta1` API migration plan and keep the breaking scope change explicit.
