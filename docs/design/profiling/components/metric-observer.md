# MetricObserver

> Navigation: [Component contracts](README.md) · [Profiling design index](../README.md)

## Status and purpose

Approved for the first edge-metrics-backed profiling path. Records typed facts
for a bounded measurement window, not scientific accept/reject policy.
Scientific admission was removed (`788bcf93`); execution/raw evidence and numeric
derivation remain. Thermal and co-resident facts are observations, not verdicts.

## Responsibility boundary

| Owner | Contract |
|---|---|
| MetricObserver | Validate bounded request; schedule interval reads with per-read timeout; retain every successful typed sample with CHILL receipt time; separate per-metric issues from whole-request failures; return source identity/summaries; stop on duration expiry or cancellation |
| edge-metrics adapter | Sequential bounded reads of resolved central exact-series power and auxiliary local thermal endpoints; parse frozen configured metrics; enforce exact power labels; reject zero/multiple matches, duplicate/malformed/non-finite/semantically invalid power; preserve power when thermal fails |
| Profiler | Source resolution, observation policy, Job lifecycle, persistence, retry ownership, scientific numeric derivation |
| edge-metrics | Sensor access, platform interpretation, exposition, per-metric enablement; observer never changes its configuration |

The power-only exact-series source serves transition-cost observation. It takes
one frozen metric name and non-empty label map, accepts only identical label sets
and values, and sends the already-resolved endpoint (including validated hostname
filter) unchanged. Response filtering never relaxes exact-label validation.

## Data and failure contract

A successful tick retains:

- required wall power in watts;
- supported-thermal-signal observability;
- direct current-throttling signal, when exposed;
- CPU core/package thermal-event counters, when exposed;
- latched thermal-history signal, when exposed;
- maximum device temperature as diagnostic context; and
- typed required/malformed-metric issues.

The first profiling path requires valid power. Missing or invalid power, failed
power reads, or oversize power responses fail the tick without a sample. Thermal
is auxiliary: endpoint timeout/read/parse failures retain power, omit only that
sample's thermal field, and add the thermal failure to `failures`. Partial thermal
metric issues remain distinct from endpoint failures: optional heterogeneous
thermal fields preserve their exact presence, never convert missing/invalid
evidence to healthy state. Power-only makes no thermal claim and rejects
missing/additional labels.

Receipt timestamps are used because no trusted source timestamp exists for every
value. Repeated values remain; no smoothing, discarding, thresholding, or inference.
Direct signals, counters, counter decreases/resets, temperature, and latched
history remain raw facts; temperature/history alone do not establish throttling.
Co-resident workload evidence belongs to WorkloadObserver.

Bounded scheduling, timeouts, cancellation, event limits and summaries remain
required. After producer completion, a real final power read is retried at the
configured observation interval within the remaining duration watchdog. One
success publishes the result; exhausting the watchdog without success still
fails. Failed attempts remain in `failures`; existing summary counts include
every retained sample and failure (including auxiliary thermal failures), without
new stored fields. Job event capacity allows power plus a thermal failure per
tick. Raw validation binds the exact source and RunConfig. Parsing must preserve
missing/unsupported signals and reject duplicates/malformed values.

## Dependency and exclusions

`Profiler -> internal/metricobserver <- internal/metricobserver/edgemetrics ->
edge-metrics endpoints`. Core imports no CHILL API, Kubernetes client,
controller-runtime, Profiler, admission, or derivation package. Adapter imports
only the core contract, maintained Prometheus exposition parser, and standard
HTTP transport.

No arbitrary Prometheus queries/metric maps, Node endpoint discovery, monitoring
configuration mutation, temperature-threshold inference, scientific admission,
retry policy beyond the bounded final-read watchdog, energy integration, batch
optimization, co-resident observation, long-term monitoring, scheduler state,
or control.
