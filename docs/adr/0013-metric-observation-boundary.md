# 0013 Metric Observation Boundary

## Status

Accepted

## Context

CHILL profiling needs wall-power samples for energy derivation and thermal facts
to decide whether a measured trial is usable. The testbed exposes exact Shelly
power through a central Gateway and device thermal facts through local
edge-metrics exposition endpoints. Normal Prometheus retention is
useful for monitoring, but its scrape interval does not define a short
profiling experiment's observation window.

edge-metrics owns platform sensor access, Shelly communication, platform-level
metric interpretation, exposition, and per-metric enablement. CHILL owns the
bounded profiling window, immutable Run evidence, and scientific admission.
Kubernetes owns Node and Pod health, while Prometheus owns normal retained
monitoring history.

## Decision

Use a typed MetricObserver contract for bounded profiling-time observation. A
Profiler request identifies a Kubernetes Node; the Profiler resolves that Node
through static metric-source configuration before creating the measurement
Job. The selected filtered power endpoint, exact power-series labels, required
power metric name, local thermal endpoint, six typed thermal metric names,
polling interval, timeout, duration, and digest-pinned MetricObserver image are
frozen in the Run intent. The adapter interprets those frozen values and records
the same identities in its result.

Each polling tick uses one bounded request context to read the exact central
power series and then the local thermal facts. A failure of either endpoint
fails the tick; no partial sample is returned. A successful pair records one
CHILL receipt timestamp and request latency. Whole-tick failures and per-metric
thermal issues are different evidence. The observer does not rewrite
edge-metrics configuration, discard repeated values, or treat temperature as a
throttling threshold.

The first adapter supports one required wall-power slot and six fixed-semantic
thermal slots. Static metric-source configuration owns their concrete metric
names, and the selected mapping is frozen into the Run intent.

The first profiling path requires a valid wall-power value for a successful
sample. Thermal fields remain typed and optional because platforms expose
different direct or cumulative signals. Missing thermal evidence is preserved
as missing and later yields an insufficient admission verdict.

MetricObserver reports only factual observation. Profiler admission determines
window coverage and classifies unknown thermal state, observed throttling, or a
counter reset. Profile derivation owns energy integration and `b_sat`. The
scheduler consumes only admitted profile results and does not interpret thermal
signals.

The observer performs bounded experiment-local polling. It does not change the
shared Prometheus or ServiceMonitor scrape interval. A measurement Pod receives
the already-resolved power and thermal endpoints and does not receive Kubernetes
API credentials to rediscover either source.

## Consequences

The reusable core lives under `internal/metricobserver`, and the edge-metrics
adapter lives under `internal/metricobserver/edgemetrics`. Their dependency
direction remains toward plain observation contracts and away from Kubernetes,
Profiler orchestration, admission, and scheduler packages.

The one-shot profiling Job runs a `metric-observer` container beside the runtime
container. It publishes one bounded atomic handoff containing typed samples and
failures. The measurement worker validates and persists that result as raw
evidence; terminal Kubernetes facts and admission are finalized later.

Metric availability is operational configuration owned by edge-metrics. CHILL
fails closed when required evidence is absent and surfaces that reason; it does
not enable or disable exporter metrics on behalf of an experiment.
