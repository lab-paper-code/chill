# Lattepanda CPU execution-contract audit

Date: 2026-07-12 KST

## Question

Why did the first Kubernetes batch-1 result produce about 29 requests/s and
6.2 W when the historical EEP profile recorded about 183 requests/s and 17.2 W
for the same model artifact, runtime family, and execution provider?

## Controlled states

Every Kubernetes state used the same MobileNet-V2-050 artifact digest, ONNX
Runtime 1.23.2, CPUExecutionProvider, batch 1, 30-second serial inference
window, and concurrent one-second Shelly observation.

| State | CPU limit | ORT intra/inter | Throughput (req/s) | Mean latency (ms) | Wall power (W) | J/request |
|:---|:---|:---|---:|---:|---:|---:|
| C | 2 | default/default | 28.893 | 34.610 | 6.230 | 0.2156 |
| B | 2 | 2/1 | 114.168 | 8.759 | 13.570 | 0.1189 |
| A | none | 2/1 | 114.207 | 8.756 | 13.663 | 0.1196 |
| D | none | default/default | 183.655 | 5.445 | 17.627 | 0.0960 |
| E | 4 | 4/1 | 186.463 | 5.363 | 17.777 | 0.0953 |
| historical EEP | no Kubernetes limit | default/default | 182.510 | 5.474 | 17.179 | 0.0941 |

## Evidence

State C saw four CPUs through affinity but had `cpu.max=200000 100000`.
All 301 CFS periods were throttled, with about 58.98 seconds of accumulated
throttled time during a 30-second wall-clock run. Its bimodal latency
(`p50=17.214 ms`, `p95=70.450 ms`) was therefore a quota artifact.

State B aligned the ORT worker count with the two-CPU quota. Accumulated
throttled time fell to about 20 milliseconds and the distribution collapsed to
`p50=8.727 ms`, `p95=8.972 ms`. Removing the quota in state A changed neither
throughput nor latency materially, so the limit itself was not the remaining
gap once thread count and quota were aligned.

The historical remote benchmark script was retained on the Lattepanda with a
2026-04-17 modification time. Unlike the current canonical EEP script, it did
not create `SessionOptions` or set intra/inter-op thread counts. Historical EEP
profiles were therefore produced with the ONNX Runtime default thread pool,
not the later explicit 2/1 contract.

State D reproduced that historical contract without a CPU limit. Its
throughput differed from the historical EEP value by about 0.6%, mean latency
by about 0.5%, wall power by about 2.6%, and energy per request by about 2.0%.
The current host bare-metal run with the current explicit 2/1 EEP script also
matched Kubernetes states A/B at about 114.7 requests/s and 13.5 W.

State E replaced the historical implicit contract with an explicit four-CPU
quota and ORT 4/1 thread configuration. It produced 186.463 requests/s at
17.777 W. Accumulated throttled time was about 40 milliseconds over 30 seconds,
and its latency and energy per request matched state D and historical EEP
within the observed run-to-run range. An explicit 4/1 thread and four-CPU limit
can therefore represent the historical execution path without relying on
runtime defaults or an unlimited container.

## Conclusion

The original Kubernetes/EEP discrepancy was not evidence of Kubernetes or
container overhead. It was caused by comparing different CPU execution
contracts:

```text
historical EEP: ORT default pool over four visible CPUs, no CPU quota
first Kubernetes spike: ORT default pool over four visible CPUs, two-CPU quota
```

The first spike's low power did not indicate better efficiency. It represented
a heavily throttled execution state with worse energy per request. Future
profiles must record and compare CPU quota, visible/affinity CPU count, ORT
thread counts, and CFS throttling evidence as part of the measured state.

This audit does not by itself decide CPU requests, node exclusivity, or the
serving contract. It establishes that the Kubernetes runtime must make the ORT
thread count and CPU limit explicit and mutually consistent. CPU request and
co-location policy remain separate admission and scheduling decisions.

## Live derivation validation

The batch spike subsequently derived the contract from the live Lattepanda
Node rather than embedding the value four as policy:

```text
status.allocatable.cpu=4
  -> OnePodPerNodeFullCPU budget=4
  -> limits.cpu=4
  -> ORT intra/inter=4/1
```

All five batch Pods reported `cpu.max=400000 100000`, ORT 4/1, and the same
mounted derivation provenance. Accumulated throttled time was between about 7
and 35 milliseconds per 30-second run. The runtime now fails before warm-up if
the mounted contract differs from the actual cgroup limit or ORT thread
environment. CPU request remains explicitly marked `SpikeOnlyUnresolved` and
is not presented as part of this derivation.
