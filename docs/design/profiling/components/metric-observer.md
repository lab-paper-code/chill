# MetricObserver

> Navigation: [Profiling design index](../README.md)

## Status

Approved for the first edge-metrics-backed profiling path.

## Purpose

MetricObserver records the typed metric facts required while one profiling
measurement window is active. The first Run source combines one already-resolved
central exact-series power endpoint with one local thermal endpoint. One tick
records their power and thermal-validity facts only when both reads complete
under the same bounded request context.

The adapter also exposes a power-only exact-series source intended for the
transition-cost observer. It consumes one frozen metric name and a non-empty
label map, then accepts only the series whose label set and values match that
map exactly. It sends the already-resolved endpoint, including its validated
hostname filter, unchanged; filtering the response does not weaken exact-label
validation.

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

- issuing sequential bounded reads to the resolved power and thermal endpoints
  under one request context;
- parsing the configured and frozen required wall-power metric;
- parsing the configured and frozen typed thermal metrics;
- selecting the configured power series by its exact frozen labels;
- rejecting zero or multiple exact-series matches;
- failing the tick when either endpoint read fails, without a partial sample;
- rejecting duplicate, malformed, non-finite, or semantically invalid values;
  and
- preserving a missing or invalid thermal fact without converting it into a
  healthy thermal state.

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

Power and a readable thermal endpoint are required by the first profiling
path. A missing or invalid power value, or a failed endpoint read, is a failed
source-read attempt. Thermal signals are heterogeneous, so their
fields are optional and their exact presence remains evidence. A successful
power read cannot silently turn missing thermal evidence into a healthy state.
The power-only path makes no thermal claim and does not accept a series with
missing or additional labels as a substitute.

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
edge-metrics exposition endpoints
```

The core package imports no CHILL API, Kubernetes client, controller-runtime,
Profiler, admission, or derivation package. The adapter imports the core
contract, the maintained Prometheus exposition parser, and standard HTTP
transport only.

## Explicit non-goals

- arbitrary Prometheus query or metric-map support;
- Kubernetes Node-to-endpoint discovery;
- Prometheus or edge-metrics configuration mutation;
- temperature-threshold inference;
- admission, retry, energy integration, or batch-energy optimization;
- co-resident workload observation;
- long-term monitoring; and
- scheduler state or control.

## Acceptance gates

- one bounded tick combines exact central power with local thermal facts;
- either endpoint failure or oversize response fails the tick without a partial
  sample;
- required-power failure and partial thermal failure remain distinguishable;
- bounded scheduling, timeout, cancellation, event limits, and summaries have
  deterministic tests;
- edge-metrics parsing covers configured direct signals, counters, missing
  metrics, duplicates, malformed values, and unsupported observation;
- the power-only path accepts exactly one configured metric-and-label match and
  rejects zero matches, multiple matches, and label-set drift, while preserving
  the resolved hostname-filtered endpoint in its request;
- raw evidence validation binds the exact source and RunConfig;
- admission tests distinguish unknown, throttled, counter-reset, and known
  non-throttled windows; and
- architecture, Go, generated API, and Helm checks pass.
