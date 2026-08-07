# Profiling campaign records

> Navigation: [Profiling design index](../README.md)

This directory contains immutable, pre-measurement inputs and concise terminal
outcomes for offline profiling campaigns. A campaign becomes frozen at the Git
commit that first contains its contract, policy, and inclusion rule. Do not
copy Run identities or measurement values here: the name prefix selects
`ProfilingRun` objects, and their status and evidence reference provide the
read-time result.

| Campaign | Status | Plan |
|---|---|---|
| [AGX Orin CPU ORT MODE_50W batch saturation v1](agx-ortcpu-mode50w-bsat-v1/README.md) | frozen before measurement; no Runs submitted | Plan 5 |
| [RKNN MobileNet V2 0.50 batch saturation v2](rknn-mobilenet-v2-050-bsat-v2/README.md) | completed candidate-only; 6.395% upper bound did not confirm the 5% rule | Plan 5 |
| [RKNN MobileNet V2 0.50 batch saturation v1](rknn-mobilenet-v2-050-bsat-v1/README.md) | completed unavailable; discovery selected batch 1, frozen confirmation budget insufficient | Plan 5 |
| [RKNN MobileNet V2 0.50 sequential v1](rknn-mobilenet-v2-050-sequential-v1/README.md) | completed; selected batch 1, stopped at batch-3 capability boundary | Plan 5a |

These records are configuration provenance, not CRDs, controller state, or a
second evidence store. The applicable plan remains authoritative for the
estimand and acceptance boundary.
