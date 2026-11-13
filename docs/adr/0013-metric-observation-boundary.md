# Metric Observation Boundary

## Status

Accepted

## Context

Profiling needs bounded wall-power and thermal observations. edge-metrics owns sensor access, power-source communication, metric interpretation and enablement. CHILL owns the experiment window, immutable evidence and scientific admission.

## Decision

Use the typed observer in `internal/metricobserver` and its `edgemetrics` adapter. Resolve a requested Node through configured metric sources before creating the Job. Freeze the exact power endpoint, series labels, metric names, thermal endpoint, six thermal slots, cadence, timeout, duration and observer image in RunConfig.

Each tick uses one bounded context to read power and then thermal facts. A power failure records a failed read. Thermal failure preserves successful power and its separate failure record at the same receipt time. Preserve missing fields, counter resets and per-metric issues; the observer reports facts without interpreting temperature as a throttling threshold.

Profiler admission owns coverage and thermal usability. Derivation owns integration and optimization. Scheduler consumers use admitted profiles. Sampling does not modify exporter configuration or shared Prometheus scrape cadence.

The Job receives resolved endpoints without Kubernetes credentials for rediscovery. The observer container publishes one bounded atomic handoff; the measurement worker validates and persists raw evidence.

### Resource admission

`RequiredCapacity` derives the full schedule before readiness or any source read: the initial read, cadence including a ticker/deadline race, and the producer-end final read when requested. Failed final reads retry within the same watchdog. Each read may retain both a power sample and a failure.

Admit the unchanged schedule against the existing result-byte and memory envelopes. Profiling Jobs supply `maxMetricResultBytes` and `observerMemoryLimitBytes`. Transition Jobs supply their workspace object limit and downward-API `limits.memory`; without an explicit memory limit, Kubernetes supplies Node allocatable memory. ChillServer supplies its object ceiling and current Go soft memory limit. Memory requests are scheduling reservations.

Capacity accounting uses source identity, maximum schema-shaped records, encoded JSON size, retained structs, scalar allocations and summary scratch. Reserve encoder and returned-buffer overhead. Allocate sample and failure capacity once.

Derive diagnostic slots from the remaining result and memory budgets, including JSON escaping. Long diagnostics retain a bounded prefix, original `messageBytes` and `messageTruncated`. This applies to thermal issues as well as read failures. It bounds retained/serialized evidence; upstream error-string allocation and unrelated process memory remain outside that estimate.

Insufficient capacity rejects the original schedule before work. Do not shorten duration, cadence or evidence to fit. Unexpected runtime failures publish partial evidence and fail observer completion. WorkloadObserver Pod events retain their independent event limit.

## Consequences

The reusable observer has no dependency on Kubernetes, orchestration or scheduling. Finalization validates raw evidence once and binds Pod UID, target Node, actuation, successful Job completion and finalize time. Execution evidence retains the current schema without container terminal records or Node workload snapshots; retained readers bound bytes and decode.

Required missing evidence remains an explicit admission failure. CHILL does not enable exporter metrics on behalf of a campaign.

