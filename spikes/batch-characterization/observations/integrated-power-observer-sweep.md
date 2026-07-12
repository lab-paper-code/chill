# Integrated PowerObserver fixed-batch sweep

Date: 2026-07-12 KST

## Execution shape

The cloud-side spike runner resolved `nodeName=lattepanda` to the Ready
edge-metrics exporter endpoint and passed that endpoint to the workload through
a ConfigMap. Each Indexed Job Pod then ran two separate containers:

```text
runtime: warm up -> signal measurement start -> fixed-batch inference for 30s
power-observer: signal ready -> wait for start -> poll edge-metrics every 1s for 30s
```

The edge-side observer received no Kubernetes API credential. Runtime and power
evidence were emitted independently and joined by Node identity and overlapping
timestamps after the Job completed.

## Result

All five Job indexes completed without a container restart. Each batch obtained
30 successful in-window Shelly samples and no source failure.

| Batch | Inferences in 30s | Power samples | Mean wall power (W) | Provisional energy/request (J) |
|---:|---:|---:|---:|---:|
| 1 | 869 | 30 | 6.383 | 0.220295 |
| 2 | 460 | 30 | 6.527 | 0.212828 |
| 4 | 246 | 30 | 6.603 | 0.201804 |
| 8 | 123 | 30 | 6.523 | 0.199571 |
| 16 | 60 | 30 | 6.547 | 0.205258 |

The observer/runtime overlap was approximately 30 seconds for every batch.
Maximum observed sample gap ranged from 1.025 to 1.073 seconds. Observer p95
request latency ranged from about 46 to 116 milliseconds.

These are integration results from one sweep, not a decision-grade energy
profile. The curve decreases through batch 8 and rises at batch 16 in this run,
but no `b_sat` is derived without repeated measurements, uncertainty treatment,
and a concrete saturation rule.

## Boundary learned from the failed first attempt

The first integration attempt placed Kubernetes resolution inside the
edge-side observer. That Pod could not construct an in-cluster client because
the KubeEdge execution environment did not provide the normal Kubernetes API
Service environment. More importantly, this placement gave the measurement
Pod credentials solely to rediscover an endpoint the profiling control path
could resolve beforehand.

The corrected boundary is:

```text
cloud-side profiling control: Node -> edge-metrics endpoint
edge-side PowerObserver: resolved endpoint -> timestamped power evidence
```

This keeps Node identity as the profiler-facing input while treating the
endpoint as an internal execution detail.
