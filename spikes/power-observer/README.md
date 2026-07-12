# PowerObserver Probe

> TODO(internal): Replace CLI examples and open questions with the profiler-owned
> PowerSource/ObservationRequest API, status conditions, credential policy, and
> retained evidence contract once those objects are implemented.

This probe exercises the CHILL-owned power-observation contract described by
ADR 0013. The reusable polling and edge-metrics adapter live under
`internal/powerobserver`; this directory retains only the Kubernetes resolver,
probe CLI, image entry point, and spike tests. It does not write a CR status.

The candidate boundary is:

```text
Observer
  -> schedules bounded polling and preserves raw evidence
Source
  -> reads one instantaneous power value from one transport
Profiler policy (not implemented)
  -> decides evidence admissibility and derives energy quantities
```

The caller supplies a Kubernetes Node name. A `TargetResolver` finds exactly
one Ready edge-metrics exporter Pod scheduled on that Node and resolves its
Pod IP to a metrics endpoint. The first `Source` polls that endpoint and
requires exactly one `shelly_power_total_watts` series. A successful sample is
timestamped after the response is received and parsed. This timestamp is an
observer receipt timestamp, not a Shelly device timestamp.

In the integrated batch spike, resolution runs in the cloud-side orchestration
step before workload creation. Only the resolved endpoint is passed to the
edge-side observer, which receives no Kubernetes API credential. The
`-resolved-endpoint` CLI option represents this internal hand-off; it is not a
profiler-facing target identity.

Run unit tests:

```sh
go test ./internal/powerobserver/... ./spikes/power-observer/...
```

Run a bounded live probe against the current Lattepanda exporter:

```sh
go run ./spikes/power-observer/cmd/probe \
  -node-name lattepanda \
  -interval 1s \
  -duration 30s \
  -request-timeout 500ms
```

Open questions intentionally left for the Profiler/Run design:

- the stable label contract used to identify an edge-metrics exporter
- whether Pod IP is the correct endpoint form for every supported cluster
- whether observation cancellation should be represented separately from a
  completed bounded run
- the quality/acceptance policy and its owner
- which observer health values become Prometheus metrics
- how a profiling workload and observer share one experiment identity
