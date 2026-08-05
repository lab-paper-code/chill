# Profiling campaign records

> Navigation: [Profiling design index](../README.md)

This directory contains immutable, pre-measurement records for offline
profiling campaigns. A campaign becomes frozen at the Git commit that first
contains its contract, policy, and inclusion rule. Later result additions must
append exact Run identities and must not rewrite the frozen inputs.

| Campaign | Status | Plan |
|---|---|---|
| [RKNN MobileNet V2 0.50 sequential v1](rknn-mobilenet-v2-050-sequential-v1/README.md) | frozen; no campaign Runs submitted | Plan 5a |

These records are read-time provenance, not CRDs or controller state. The
applicable plan remains authoritative for the estimand and acceptance boundary.
