# 0007 Internal Helm Release Flow

## Status

Superseded by [0009 Single Helm Release Lifecycle](0009-single-helm-release-lifecycle.md)

## Context

Helm applies resources in a fixed kind/name order, not as an application-specific dependency graph. CHILL is still small, but future modules will add more runtime dependencies: operator, node discovery, device-class reconciliation, and later scheduling or power-control components.

The public operator UX should stay Helm-shaped. Exposing a separate CHILL lifecycle command set would make users think about internal phase boundaries that should remain implementation detail.

## Decision

This ADR is retained only as historical context. It previously used two Helm
releases to preserve install and teardown ordering while the bootstrap surface
was changing.

The current decision is ADR 0009: use one `charts/chill` release and preserve
teardown ordering with a Helm `pre-delete` hook that deletes the root
`ChillSystem` before the operator Deployment is removed.

## Consequences

Operators get a small Helm-oriented command surface, while CHILL-specific dependency intent remains centralized in Kubernetes reconciliation and one internal script.

Future runtime modules should be modeled under the `ChillSystem` root resource first. Helm should package the desired root/config resources, not become a CHILL-specific lifecycle engine.
