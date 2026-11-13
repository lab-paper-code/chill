# 0013 Metric Observation Boundary

## Status

Accepted

## Context

CHILL profiling needs wall-power samples for energy derivation and thermal facts to decide whether a measured trial is usable. The testbed exposes exact Shelly power through a central Gateway and device thermal facts through local edge-metrics exposition endpoints. Normal Prometheus retention is useful for monitoring, but its scrape interval does not define a short profiling experiment's observation window.

edge-metrics owns platform sensor access, Shelly communication, platform-level metric interpretation, exposition, and per-metric enablement. CHILL owns the bounded profiling window, immutable Run evidence, and scientific admission. Kubernetes owns Node and Pod health, while Prometheus owns normal retained monitoring history.

## Decision

Use a typed MetricObserver contract for bounded profiling-time observation. A Profiler request identifies a Kubernetes Node; the Profiler resolves that Node through static metric-source configuration before creating the measurement Job. The selected filtered power endpoint, exact power-series labels, required power metric name, local thermal endpoint, six typed thermal metric names, polling interval, timeout, duration, and digest-pinned MetricObserver image are recorded in RunConfig. The adapter interprets those values and records the same identities in its result.

Each polling tick uses one bounded request context to read the exact central power series and then the local thermal facts. A power failure records a failed read. A thermal endpoint failure preserves both the successful power sample and a separate failure record at the same receipt timestamp; it never discards power. Whole-source failures and per-metric thermal issues remain different evidence. The observer does not rewrite edge-metrics configuration, discard repeated values, or treat temperature as a throttling threshold.

The first adapter supports one required wall-power slot and six fixed-semantic thermal slots. Static metric-source configuration owns their concrete metric names, and the selected mapping is recorded in RunConfig.

The first profiling path requires a valid wall-power value for a successful sample. Thermal fields remain typed and optional because platforms expose different direct or cumulative signals. Missing thermal evidence is preserved as missing and later yields an insufficient admission verdict.

MetricObserver reports only factual observation. Profiler admission determines window coverage and classifies unknown thermal state, observed throttling, or a counter reset. Profile derivation owns energy integration and batch-energy optimization. The scheduler consumes only admitted profile results and does not interpret thermal signals.

The observer performs bounded experiment-local polling. It does not change the shared Prometheus or ServiceMonitor scrape interval. A measurement Pod receives the already-resolved power and thermal endpoints and does not receive Kubernetes API credentials to rediscover either source.

### Schedule-derived resource admission

The observer owns required capacity: an immediate read plus the duration/interval
cadence (including a ticker/deadline race); producer-end observation adds one
immediate final read. Failed final reads retry at the same interval within the
same watchdog, not a second cadence. Each read can retain one sample and one
failure. There is no user-configured metric event count and no profiler-owned
copy of this calculation.

Before readiness or source reads, `RequiredCapacity` compares the full schedule
with the selected worker's existing `maxMetricResultBytes` and container
`observerMemoryLimitBytes`. Kubernetes memory requests are scheduling
reservations, not measured baseline usage; they are not subtracted from limits.
Buffer accounting is not a claim that Go RSS or arbitrary source allocations
cannot fail. Transition Jobs use their existing workspace byte limit (the
controller's `transitionMaximumObjectBytes`) and downward-API `limits.memory`.
Production transition Jobs have no explicit memory limit: the kubelet supplies
node allocatable memory in that case ([Kubernetes downward API](https://kubernetes.io/docs/concepts/workloads/pods/downward-api/#fallback-information-for-resource-limits)).
The command admits that runtime envelope before any source read or readiness,
so missing Job-time memory limits do not reject normal preparations. Their
fact-observer and combined raw-artifact overhead still belong to that Job, not
to the reusable metric buffer estimate. These limits enter the observer
constructor, never the sampling request, chart, CRD, or RunConfig as new settings.

ChillServer supplies both operation-cost and periodic scheduler power observers
with its existing `maxEvidenceBytes` object ceiling and the Go memory limit
queried by `debug.SetMemoryLimit(-1)`, the same configured `GOMEMLIMIT` already
used by its overload guard. The deployment sets that soft limit below the
container memory limit. `OperationConfig.MetricLimits` only delivers this
process envelope; sampling records gain no resource fields. This is a bound on
the metric component, not an estimate of all operation-state or hourly archive
metadata. Refresh/operation admission refuses an oversized schedule before
source reads or physical actions; it does not adjust duration or interval.


Byte admission marshals the actual source identity and schema-shaped maximum
records, including six thermal issues, scalar pointers, counters, JSON escaping
(up to six bytes per input byte), UTC timestamps, and numeric formatting room.
`unsafe.Sizeof` accounts for retained structs, scalar allocations and summary
sorting scratch; fixed-capacity sample/failure arrays avoid append growth.
The memory estimate also reserves four times the bounded encoded result for
encoder buffer growth, the returned marshal copy, and record/allocator scratch.
It does not use an unexplained bytes/event multiplier.

The adapter's HTTP response body is capped at 1 MiB, but diagnostics had **no
length cap**: transport errors include caller-owned URLs and parser errors can
include response text. Diagnostic slots are derived from the remaining result
and memory budgets after fixed records, identities and serialization costs.
Each input byte reserves up to six encoded JSON bytes and one retained byte.
Long messages keep a bounded prefix, original byte length (`messageBytes`) and
`messageTruncated: true`; diagnostic length never interrupts the power window.
The same explicit metadata applies to thermal metric issue messages. A slot
can contain zero prefix bytes if only fixed records fit: failure class, original
length and the truncation marker still remain. Typed issues are bounded to the
six metric slots and configured metric-name length. This bounds retained and
serialized evidence, not the upstream allocation of `error.Error()` strings.

Insufficient bytes reject the original schedule before work starts. Duration,
interval and sample/failure records are not reduced to fit. Unexpected runtime
capacity or schema failures return partial evidence, and the metric command publishes it
before exiting unsuccessfully; the coordinator still requires successful
observer completion. WorkloadObserver's independent material-Pod-event limit
(`maxWorkloadEvents`, delivered as its own `--max-events`) is unchanged: Pod watch
events are not the metric sampler's scheduled records.

The removal is part of the RunConfig v1alpha18 shared-CPU cutover; it
does not introduce another stored-contract version or a compatibility reader.

## Consequences

The reusable core lives under `internal/metricobserver`, and the edge-metrics adapter lives under `internal/metricobserver/edgemetrics`. Their dependency direction remains toward plain observation contracts and away from Kubernetes, Profiler orchestration, admission, and scheduler packages.

The one-shot profiling Job runs a `metric-observer` container beside the runtime container. It publishes one bounded atomic handoff containing typed samples and failures. The measurement worker validates and persists that result as raw evidence. Finalization validates raw evidence once and binds raw Pod UID, target Node, actuation, Job success/completion and finalize time. Execution evidence v1alpha18 retains no container terminal records or Node workload snapshot; retained execution readers only bound bytes and decode.

Metric availability is operational configuration owned by edge-metrics. CHILL fails closed when required evidence is absent and surfaces that reason; it does not enable or disable exporter metrics on behalf of an experiment.
