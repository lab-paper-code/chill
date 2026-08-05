# Profiling campaign records

> Navigation: [Profiling design index](../README.md)

This directory contains immutable, pre-measurement records for offline
profiling campaigns. A campaign becomes frozen at the Git commit that first
contains or content-addresses its contract, policy, and inclusion rule. Later
result additions must append exact Run identities and must not rewrite the
frozen inputs.

| Campaign | Status | Plan |
|---|---|---|
| [RKNN MobileNet V2 0.50 batch saturation v2](rknn-mobilenet-v2-050-bsat-v2/README.md) | discovery inputs frozen; not started | Plan 5 |
| [RKNN MobileNet V2 0.50 batch saturation v1](rknn-mobilenet-v2-050-bsat-v1/README.md) | completed unavailable; discovery selected batch 1, frozen confirmation budget insufficient | Plan 5 |
| [RKNN MobileNet V2 0.50 sequential v1](rknn-mobilenet-v2-050-sequential-v1/README.md) | completed; selected batch 1, stopped at batch-3 capability boundary | Plan 5a |

These records are read-time provenance, not CRDs or controller state. The
applicable plan remains authoritative for the estimand and acceptance boundary.
