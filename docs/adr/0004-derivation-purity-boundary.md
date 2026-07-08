# 0004 Derivation Purity Boundary

## Status

Accepted

## Context

The scheduler's core value is the measured derivation chain: request energy, batch saturation, capacity, water-filling, envelope construction, gear thresholds, and transition economics.

Those calculations must be reusable by tests, analysis scripts, and controllers without dragging Kubernetes clients into the mathematical core.

## Decision

Keep `internal/derivation` as a pure Go library with no Kubernetes imports.

Inputs and outputs must be plain Go structs. Kubernetes CRD types may be translated into derivation inputs at controller boundaries, not inside derivation code.

## Consequences

Unit tests and golden files can validate math without envtest or a cluster.

Controllers remain thin adapters around measurement state, CR status, and actuator orchestration.

Any future Kubernetes import in `internal/derivation` is a design violation unless this ADR is superseded.
