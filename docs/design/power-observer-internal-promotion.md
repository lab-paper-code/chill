# PowerObserver Internal Promotion

## Status

Implemented.

## Purpose

Promote only the power-observation mechanics proven by the isolated spike into
a reusable CHILL internal module. Batch characterization becomes one consumer
of that module; it does not define the module's ownership or policy.

The promoted module answers one question:

> During a bounded interval, what power readings and observation failures were
> returned by this already-resolved source?

It does not decide whether the evidence is scientifically acceptable or what
CHILL should do with it.

## Evidence supporting promotion

The spike has exercised the same observation boundary with CPU, RKNN, and
Jetson TensorRT workloads. The edge-metrics adapter has also sustained a
120-second, one-second polling probe without transport failure. Integrated
runs demonstrated:

- bounded polling independent of the normal Prometheus scrape interval
- workload/observer start coordination
- receipt timestamps and per-request latency
- successful and failed sample accounting
- retention of repeated values rather than treating them as failures
- a long but bounded wait before Jetson TensorRT measurement begins
- a real first-sample transient that must remain visible to later policy

These results support reuse of the observation mechanism. They do not yet
validate energy acceptance, baseline subtraction, or `b_sat` derivation.

## Responsibility boundary

### Internal PowerObserver owns

- validating a bounded observation request
- scheduling reads at the requested interval
- applying a timeout to each source read
- retaining every successful reading with CHILL receipt time
- retaining typed read failures with time and request latency
- returning source identity and factual observation summaries
- stopping on duration expiry or context cancellation

### A source adapter owns

- connecting to one already-resolved power source
- parsing the source-specific response into watts
- exposing stable source and metric identity
- distinguishing adapter-level parse, missing-metric, and transport errors

The first supported adapter is edge-metrics and reads
`shelly_power_total_watts`. Supporting additional sources later must not alter
the observer scheduling contract.

### The Profiler control path owns

- selecting the target Kubernetes Node
- resolving that Node to a source endpoint
- freezing the resolved source in an immutable profiling Run
- choosing duration, interval, request timeout, and synchronization strategy
- creating and owning workload/observer execution resources
- determining evidence acceptance and retry policy
- deriving baseline-adjusted power, energy per request, and `b_sat`
- persisting Run and profile status

### Other systems retain their ownership

- edge-metrics owns Shelly communication and metric exposition
- Kubernetes owns Node and Pod health
- Prometheus owns normal monitoring and retained history
- the scheduler consumes accepted profile results; it does not perform power
  observation

## Dependency direction

```text
Profiler control and Run lifecycle
        |
        | constructs request with resolved source
        v
internal/powerobserver
        |
        | calls a source interface
        v
internal/powerobserver/edgemetrics
        |
        v
edge-metrics /metrics
```

`internal/powerobserver` must not import CHILL API types, controller-runtime,
Kubernetes clients, or profiler packages. The edge-metrics adapter may depend
on HTTP and Prometheus text parsing, but not Kubernetes discovery.

The spike currently declares a nested module named
`edge.dacs.io/chill/spikes/power-observer`, while the repository module is
`github.com/lab-paper-code/chill`. Go's `internal` visibility rule prevents
that nested module from importing the repository's internal package. During
promotion, the spike's nested `go.mod` and `go.sum` must therefore be removed
and its CLI compiled as part of the repository module. This makes the desired
dependency legal without exporting the observation package publicly.

## Proposed package surface

```text
internal/powerobserver/
  observer.go       bounded polling and cancellation
  types.go          request, sample, failure, result, source identity
  errors.go         stable observer-level error categories
  observer_test.go
  edgemetrics/
    source.go        edge-metrics adapter
    source_test.go
```

The conceptual boundary is intentionally small:

```go
type Source interface {
    ReadPower(context.Context) (watts float64, err error)
    Identity() SourceIdentity
}

type Observer interface {
    Observe(context.Context, Request) (Result, error)
}
```

These signatures are illustrative. Review of the spike tests may refine Go
names, but must not broaden responsibility.

## Data contract

An observation request contains only:

- polling interval
- total bounded duration
- per-read timeout

An observation result preserves:

- source and metric identity
- observation start and end
- successful samples: watts, receipt timestamp, request latency
- failed attempts: timestamp, request latency, category, diagnostic message
- factual summary: attempts, successes, failures, duration, request-latency
  summary, and maximum successful-sample gap
- an explicit indication that edge-metrics provides no trusted source-side
  sample timestamp

The raw samples and failures are authoritative. The summary is a convenience
derived solely from those records.

## Explicit non-goals

The internal module will not initially contain:

- Kubernetes Node-to-endpoint resolution
- a sidecar CLI or file-based start-signal protocol
- Job, Pod, CRD, or controller lifecycle management
- hard-coded polling intervals or minimum sample counts
- sample deletion, smoothing, transient removal, or outlier rejection
- baseline/idle-power subtraction
- joules-per-request or `b_sat` calculation
- scientific acceptance, retry, or confidence policy
- Prometheus scrape-interval mutation
- generic multi-vendor adapter abstractions beyond what the edge-metrics
  boundary actually requires

In particular, the observed Jetson first sample must be returned unchanged.
The internal module cannot label it invalid without profiler-owned context.

## Promotion sequence

1. Copy the observer types, polling logic, edge-metrics parsing, and their unit
   tests into the proposed internal packages without behavioral redesign.
2. Remove the spike's nested Go module files so the CLI is built within the
   repository module and can legally import its internal package.
3. Make the spike CLI import the internal module. Keep Kubernetes resolution,
   flags, and file coordination in the spike.
4. Run unit and race tests for the internal packages and the spike CLI.
5. Repeat a bounded live edge-metrics probe and one integrated workload run.
6. Compare the new raw result shape and behavior with retained spike evidence.
7. Only after equivalence is established, remove duplicated observation-core
   code from `spikes/power-observer`.
8. Update ADR 0013 to record that its promotion gate was satisfied.

At every step, the spike remains runnable. Promotion is complete only when the
spike is a consumer of the internal module rather than a second implementation.

## Acceptance gates

Promotion is acceptable when:

- no package dependency points from `internal/powerobserver` toward profiler,
  controller, Kubernetes, or CHILL API packages
- the spike CLI is part of the root Go module; no module-path workaround or
  exported public package is introduced to bypass Go `internal` visibility
- request validation and cancellation behavior have deterministic unit tests
- success, timeout, missing metric, malformed value, and transport failure are
  covered by adapter/observer tests
- raw repeated values and the first sample are preserved
- the race detector passes
- a live one-second bounded probe completes with retained timestamps, request
  latency, and failure accounting
- one workload-integrated run shows that initialization is outside the
  observation window and both containers terminate cleanly
- no energy or `b_sat` value is produced by the internal module

## Deferred decisions

The following belong to the later Profiler/Run design and do not block this
promotion:

- the API object that represents an immutable profiling Run
- the stable Node-to-PowerSource resolver contract
- whether observation executes as a sidecar or a separately owned workload
- admissibility thresholds, transient handling, and repeated trials
- source credentials, TLS policy, and multi-source selection
- long-term result storage and Prometheus instrumentation

## Review question

Does this boundary promote only a reusable measurement primitive while leaving
source resolution, experiment orchestration, scientific policy, and profile
status with their proper owners?
