# MetricObserver Probe

This probe exercises bounded typed observation against one edge-metrics
endpoint. The reusable polling contract and adapter live under
`internal/metricobserver`; this directory contains only the Kubernetes resolver,
standalone probe entry point, image, and spike tests.

The probe can resolve an exporter Pod from a Kubernetes Node for interactive
testing. In the Profiler path, source resolution happens before Job creation and
the observer receives only the frozen endpoint without Kubernetes credentials.

Each scrape records required Shelly wall power and the thermal facts exposed in
the same response. Timestamps are CHILL receipt timestamps.

Run the tests:

```sh
go test ./internal/metricobserver/... ./spikes/metric-observer/...
```

Run a bounded probe:

```sh
go run ./spikes/metric-observer/cmd/probe \
  -node-name lattepanda \
  -interval 1s \
  -duration 30s \
  -request-timeout 500ms
```
