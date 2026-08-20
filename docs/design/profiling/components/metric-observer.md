# MetricObserver

> Navigation: [Profiling design index](../README.md)

## Status

Approved for the first edge-metrics-backed profiling path.

## Purpose

MetricObserver records the typed metric facts required while one profiling
measurement window is active. The first source is one already-resolved
edge-metrics endpoint, and the first contract records wall power together with
thermal-validity facts from the same scrape.

It answers one question:

> During a bounded interval, what power and thermal facts, partial metric
> failures, and source-read failures were observed from this source?

It does not decide whether the evidence is scientifically admissible or what
CHILL should do with it.

## Responsibility boundary

MetricObserver owns:

- validating a bounded observation request;
- scheduling one source read at each requested interval;
- applying a timeout to each read;
- retaining every successful typed sample with CHILL receipt time;
- retaining per-metric issues separately from whole-request failures;
- returning source identity and factual observation summaries; and
- stopping on duration expiry or context cancellation.

The edge-metrics adapter owns:

- issuing one bounded HTTP request to one resolved `/metrics` endpoint;
- parsing `shelly_power_total_watts` as the required wall-power value;
- parsing the supported `device_thermal_*` family as typed thermal facts;
- rejecting duplicate, malformed, non-finite, or semantically invalid values;
  and
- preserving a missing or invalid thermal fact without converting it into a
  healthy thermal state.

The central Shelly Gateway adapter is separate from edge-metrics. It selects
exactly one `shelly_power_total_watts` gauge by its frozen `device_type` and
`hostname` labels from the shared `/prometheus` endpoint. It reports wall power
only and makes no thermal claim.

The Profiler owns source resolution, observation policy, Job lifecycle,
evidence admission, retry, persistence, and scientific derivation. edge-metrics
owns platform sensor access, platform interpretation, metric exposition, and
per-metric enablement. MetricObserver never changes edge-metrics configuration.

## Data contract

One successful scrape produces one timestamped sample containing:

- required wall power in watts;
- whether a supported thermal signal was observable;
- a direct current-throttling signal when exposed;
- CPU core and package thermal-event counters when exposed;
- a latched thermal-history signal when exposed;
- maximum device temperature as diagnostic context; and
- any typed issue for a required or malformed metric.

Power is required by the first profiling path. A missing or invalid power value
is a failed source-read attempt. Thermal signals are heterogeneous, so their
fields are optional and their exact presence remains evidence. A successful
power read cannot silently turn missing thermal evidence into a healthy state.

The observer records receipt timestamps because edge-metrics does not provide a
trusted source timestamp for every value. It preserves repeated values and does
not smooth, discard, threshold, or infer from them.

## Thermal admission projection

Admission, not MetricObserver, applies these rules to samples overlapping the
steady runtime window:

- unreadable or insufficiently covered thermal evidence is `Insufficient`;
- any direct active-throttling sample is `Rejected`;
- an increase in a continuously observed CPU thermal-event counter is
  `Rejected`;
- a decreasing counter is an uninterpretable reset and is `Insufficient`;
- stable, sufficiently covered direct or counter evidence establishes known
  non-throttled state; and
- temperature and latched history remain diagnostic and do not create a
  throttling verdict by themselves.

Co-resident workload evidence is a separate admission input and is not owned by
MetricObserver.

## Dependency direction

```text
Profiler control and Run lifecycle
        |
        | freezes source and observation policy
        v
internal/metricobserver
        ^
        | implements typed source contract
internal/metricobserver/edgemetrics
        |
        v
edge-metrics /metrics

internal/metricobserver
        ^
        | implements target-specific power source
internal/metricobserver/shellygateway
        |
        v
central Shelly Gateway /prometheus
```

The core package imports no CHILL API, Kubernetes client, controller-runtime,
Profiler, admission, or derivation package. Both adapters import only the core
contract for CHILL types; the central adapter delegates Prometheus text parsing
to the maintained Prometheus common library.

## Explicit non-goals

- arbitrary Prometheus query or metric-map support;
- Kubernetes Node-to-endpoint discovery;
- Prometheus or edge-metrics configuration mutation;
- temperature-threshold inference;
- admission, retry, energy integration, or `b_sat` derivation;
- co-resident workload observation;
- long-term monitoring; and
- scheduler state or control.

## Acceptance gates

- one HTTP response yields aligned power and thermal facts;
- required-power failure and partial thermal failure remain distinguishable;
- bounded scheduling, timeout, cancellation, event limits, and summaries have
  deterministic tests;
- edge-metrics parsing covers direct signals, counters, missing metrics,
  duplicates, malformed values, and unsupported observation;
- central Shelly Gateway parsing selects exactly one frozen target series;
- raw evidence validation binds the exact source and immutable Run intent;
- admission tests distinguish unknown, throttled, counter-reset, and known
  non-throttled windows; and
- architecture, Go, generated API, and Helm checks pass.
