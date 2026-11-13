# MetricObserver

[Component contracts](README.md) and [Profiling design index](../README.md)

MetricObserver retains typed power/thermal facts for a bounded window.
Numeric derivation and scientific interpretation belong to consumers.

## Ownership

| Owner | Contract |
|---|---|
| Core | Validate bounded request, schedule reads, retain samples and issues, summarize, stop on timeout/cancellation |
| edge-metrics adapter | Read frozen exact-series power and auxiliary thermal endpoints with bounded sequential requests |
| Profiler | Resolve sources/policy, execute Jobs, store evidence and derive numbers |
| edge-metrics | Sensor access, platform interpretation and exposition |

The core has no Kubernetes, CHILL API, controller-runtime, Profiler or derivation
dependency. The adapter uses the core, maintained Prometheus parser and HTTP.

## Sources and failure

Power source identity includes metric name and exact non-empty label map.
Zero/multiple matches, additional/missing labels, duplicate/malformed/non-finite
or semantically invalid power fail the tick. A resolved endpoint and validated
hostname filter are sent unchanged; response filtering cannot relax identity.

Valid wall power is required. Missing/invalid power, request failure or oversized
response yields no sample. Thermal is auxiliary: its endpoint failure retains
power, omits thermal and records the failure. Per-metric issues remain distinct
from endpoint failure.

Thermal facts preserve current throttling signals, event counters, counter
resets, latched history and maximum temperature where exposed. Missing and
unsupported signals stay absent. Temperature/history alone do not prove
throttling. Power-only observation makes no thermal claim.

Samples retain CHILL receipt time because a trusted source timestamp is not
available for every value. Repeated values remain; no smoothing, discarding,
threshold inference or energy integration occurs here.

## End fence and capacity

Observation uses the frozen interval, per-read timeout and event/byte limits.
After producer completion, a real final power read retries at that interval
until one succeeds or the original duration watchdog expires. Failed reads
remain in `failures`; result counts include all retained samples and failures.
Capacity admits power and auxiliary thermal failures together.

For sample-count stops, start/end waits and the `ObserveUntil` watchdog derive
from producer `AttemptTimeout`. Admission covers that entire schedule before
polling or Job creation. This is a failure bound; successful elapsed/sample
floors can take longer than nominal measurement duration. Runtime failure still
interrupts both observers.

Raw validation binds source, RunConfig and actual window once at finalization.
Co-resident membership belongs to WorkloadObserver. Discovery, monitoring
configuration changes, retry policy outside the final-read watchdog,
optimization, scheduler state and control remain outside this component.
