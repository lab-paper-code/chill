# 0002 API Group and Version

## Status

Accepted

## Context

CRD group names become part of every manifest, RBAC rule, sample, controller watch, and user-facing API path. Renaming them after users or experiments exist is effectively a breaking API migration.

## Decision

Use API group `edge.dacs.io` and start at version `v1alpha1`.

All first-wave CRDs are generated under `edge.dacs.io/v1alpha1`.

## Consequences

`v1alpha1` is the schema exploration phase. Breaking schema changes are allowed while the repo is private and before artifact evaluation.

Conversion webhooks are deferred until a `v1beta1` promotion decision.
