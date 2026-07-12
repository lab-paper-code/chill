# 0013 Power Observation Boundary

## Status

Accepted

## Context

CHILL's profiler needs wall-power observations to derive active power, energy
per request, and the energy saturation batch for a measured execution state.
The current testbed already exposes each device's Shelly measurement as
`shelly_power_total_watts` through edge-metrics and Prometheus.

The first batch-characterization run showed that the normal 10-second
Prometheus scrape interval is sufficient for monitoring but too sparse for
short profiling windows. A separate 120-second probe against the Lattepanda
edge-metrics `/metrics` endpoint completed 120 one-second polls without an HTTP,
RPC, or timeout failure. Responses took about 35 milliseconds on average and
the reported power value changed at sub-ten-second timescales. The Shelly path
does not provide a trustworthy source timestamp for each instantaneous power
reading, so the observer must timestamp receipt and retain request latency.

edge-metrics owns Shelly connectivity and metric exposition. Kubernetes and
the monitoring stack own exporter resource monitoring. CHILL needs to own the
profiling observation window and the evidence needed to decide whether its
samples are usable; it does not need to take ownership of either system.

## Decision

Introduce a CHILL power-observation contract before the profiler, profile CRD,
or scheduler consumes power-derived values. Develop and validate the contract
in an isolated spike before placing it under `internal/`.

A profiler identifies the target by Kubernetes Node name. A separate resolver
maps that Node to one Ready edge-metrics exporter endpoint; IP addresses, Pod
names, and ports do not become profiler inputs. A power observation request
then identifies polling interval, request timeout, and observation duration.
Its result preserves:

- receipt timestamp and power value for every successful sample
- request latency for every attempt
- typed failures and their timestamps
- source identity and metric identity
- a summary of attempts, successful samples, failures, spacing, and latency

The first resolver lists exporter Pods by stable labels and `spec.nodeName`,
requires exactly one Ready non-terminating Pod, and uses its Pod IP as the
spike endpoint. Zero matching Pods, matching but unready Pods, and multiple
Ready Pods are distinct resolution failures. The first source implementation
polls the resolved edge-metrics `/metrics` endpoint for
`shelly_power_total_watts`. CHILL timestamps a reading after receiving and
parsing the response. It does not represent that timestamp as a Shelly device
timestamp.

Resolution runs in the cloud-side profiling control path before the workload
is created. The resolved endpoint may be passed to an edge-side observer as an
internal execution detail. The measurement Pod does not receive Kubernetes API
credentials merely to rediscover its own source endpoint; this also avoids
assuming that a KubeEdge Pod can reach the Kubernetes API Service directly.

Do not dynamically modify the shared Prometheus or `ServiceMonitor` scrape
interval. Prometheus remains the normal monitoring and retained-history path.
During a profiling run, the PowerObserver performs bounded high-rate polling
for that experiment only. The workload duration and polling interval are
experiment inputs; later profiler policy may derive them from a required
sample count.

Keep raw observation separate from acceptance policy and scientific
derivation. The observer reports facts such as missing samples, response
latency, and maximum sample gap. A profiler may later decide whether that
evidence is admissible and may derive active power or energy per request. The
observer does not derive `b_sat` and does not write `DeviceProfile` status.

Do not duplicate exporter CPU or memory metrics as CHILL metrics. CHILL may
instrument its own request count, failures, duration, sample spacing, and last
success when the contract moves into a running component. Identical adjacent
power values are retained as observations but are not, by themselves, treated
as a source failure.

## Consequences

The reusable observation core lives under `internal/powerobserver`, and the
first source adapter lives under `internal/powerobserver/edgemetrics`. These
packages have no dependency on CHILL APIs, controllers, Kubernetes clients, or
profiler packages.

The Kubernetes resolver, probe CLI, and file-based workload coordination remain
under `spikes/power-observer`. The spike is a consumer of the internal module,
not a second observation implementation. Its former nested Go module was
removed so root tests, race detection, vet, and lint cover the probe and the
internal packages together.

Promotion was accepted after unit and race tests, amd64 and arm64 image builds,
a bounded live edge-metrics probe, and an integrated CPU workload observation.
The integrated observation retained 30 successful one-second samples with no
source failure, while the workload and observer containers both exited
successfully.

Batch characterization still determines suitable polling intervals, minimum
windows, and evidence-acceptance policy. Promotion does not make those
experiment-specific choices part of PowerObserver.
