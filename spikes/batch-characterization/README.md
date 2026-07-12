# Batch Characterization Spike

> TODO(internal): This directory is disposable design evidence. Production
> ownership moves to ModelArtifact/DeviceProfile APIs, profiler reconciliation,
> runtime-specific contract builders, owned Run Jobs, and status derivation.

This isolated spike measures the fixed-batch execution curve for one immutable
runtime state. It is the smallest Layer-0 experiment needed before CHILL can
reason about an energy saturation batch.

The experiment deliberately keeps these values fixed:

- DeviceClass: `lattepanda-3-delta-8g`
- model artifact: MobileNet-V2-050 ONNX, identified by SHA-256
- runtime: ONNX Runtime `CPUExecutionProvider`
- Kubernetes resource limits and node placement

The adaptive cloud-side runner measures batches `1, 2, 4, ...` sequentially
and stops the coarse search at the first strict increase in energy per request.
It then applies discrete binary refinement only inside the preceding/current
batch interval. Each measured batch runs as a one-Pod Indexed Job containing
separate runtime and
PowerObserver containers. After warm-up, the containers coordinate one bounded
measurement window through an `emptyDir`; runtime and power evidence remain in
separate structured log records.

The profiling orchestrator resolves the target Node's edge-metrics exporter
before creating the workload and passes the resolved endpoint through a
ConfigMap. The edge-side PowerObserver has no Kubernetes API credentials; it
polls the resolved `shelly_power_total_watts` endpoint once per second. The
runtime container does not own power measurement. Scientific acceptance and
`bSat` derivation remain outside this integration checkpoint.

The search reduces measurement cost only. It returns `candidateBSat`; repeated
measurement, uncertainty handling, and scientific acceptance remain separate,
so `acceptedBSat` is always unset in this spike.

For the CPUExecutionProvider path, the same cloud-side step derives a CPU
execution contract from the live Node's allocatable CPU. Under the current
one-serving-Pod-per-Node assumption, the integer CPU budget becomes both the
container CPU limit and ORT intra-op thread count; inter-op is fixed to one.
The derived JSON is mounted into the runtime and copied into its result. CPU
request and node-exclusivity policy are deliberately marked unresolved and are
not hidden inside this derivation.

## Run

Build and push the runtime image:

```sh
./spikes/batch-characterization/scripts/build-push.sh
```

The build step captures the pushed OCI index digest, selects its unique
`linux/amd64` runnable child, and probes that exact child image before writing
a digest-named runtime declaration under
`results/runtime-declarations/`. The current HTTP test registry uses an
isolated containerd namespace through `RUNTIME_PROBE_RUNNER=ctr-plain-http`
(the default); a Docker daemon configured to pull the registry may use
`RUNTIME_PROBE_RUNNER=docker`. Probe or identity failure emits no new
declaration.

Deploy the sweep and wait for all indexed Pods:

```sh
./spikes/batch-characterization/scripts/run.sh
```

Run the adaptive coarse-then-binary campaign and retain every round under one
campaign evidence directory:

```sh
./spikes/batch-characterization/scripts/adaptive-run.py --maximum-batch 16
```

`run.sh` remains usable for a fixed diagnostic sweep. The adaptive runner calls
it with one selected batch at a time so that each new energy observation can
determine the next measurement.

Collect immutable log evidence and derive the current latency-only summary:

```sh
./spikes/batch-characterization/scripts/collect.sh
./spikes/batch-characterization/scripts/analyze.py \
  spikes/batch-characterization/results/latest/results.jsonl
./spikes/batch-characterization/scripts/analyze-integrated.py \
  spikes/batch-characterization/results/latest/results.jsonl \
  spikes/batch-characterization/results/latest/power-observations.jsonl
```

Join the measurement windows with raw Lattepanda Shelly samples retained by
Prometheus:

```sh
./spikes/batch-characterization/scripts/collect-power.sh
```

The observer queries `shelly_power_total_watts{node="lattepanda"}` through a
temporary local port-forward. It records the original sample timestamps and
labels, rather than querying the edge-metrics server's configuration API.
Energy per request is marked provisional whenever a batch window contains
fewer than five samples; `bSat` remains unset until every curve point meets the
coverage rule. This threshold is a spike guardrail, not a finalized profiling
policy.

Remove only this spike namespace:

```sh
./spikes/batch-characterization/scripts/cleanup.sh
```

This is not an open-loop serving or SLO experiment. It does not exercise a
request queue, dynamic batching, arrival rates, or end-to-end request latency.

## Jetson TensorRT validity boundary

The Jetson path uses the Kubernetes NVIDIA device plugin and requests one
`nvidia.com/gpu`; it does not mount GPU device files or run privileged. Its
runtime contract is bound to Xavier NX, JetPack/L4T R35.4.1, ONNX Runtime
1.16.0, TensorRT EP, and the observed `MODE_15W_6CORE` power mode. The runtime
fails if TensorRT EP is unavailable, is not the selected provider, the model
digest differs, or an inference output is not `(batch, 1000)`.

The result separates ONNX Runtime from its execution backend. It records the
primary TensorRT EP, requested CUDA fallback, ORT's registered provider chain,
GPU compute device, and FP32 precision. Provider ordering is not graph
partition evidence: until node/subgraph assignment is captured, TensorRT,
CUDA, and CPU fallback node counts remain explicitly `NotCaptured`.

Run the bounded batch-1 smoke:

```sh
kubectl apply -f spikes/batch-characterization/manifests/jetson-trt-smoke-job.yaml
kubectl wait --for=condition=complete \
  job/efficientnet-b4-jetson-trt-smoke \
  -n chill-batch-characterization --timeout=10m
```

TensorRT engine preparation is outside the steady measurement window. The
spike currently uses a Pod-local engine cache, so every fresh Pod may repeat
device-specific engine construction. This smoke proves one executable point;
it does not derive or accept `bSat`.

## RKNN validity boundary

RKNN execution has a different derived contract from the ORT CPU path. The
runner prints the same `CHILL DERIVED EXECUTION CONTRACT` block, but reports
the selected DeviceClass and accelerator interface, RKNN runtime ABI, NPU core
mask, source/runtime layouts, and artifact shape capability instead of CPU
quota and ORT thread counts.

The source ONNX layout is NCHW while the compiled RKNN runtime input is NHWC.
The runner therefore creates one reusable NHWC buffer. Every warm-up and
measurement call must return a non-empty output with shape `(batch, 1000)`;
failed calls are fatal and can never become latency samples.

Static and enumerated-dynamic RKNN artifacts are separate execution states.
`supportedBatches` is an artifact capability, not an inventory-derived global
limit. The dynamic MobileNet-V2-100 spike artifact enumerates batches 1 through
16; a static artifact declares exactly one batch.

Render a dynamic batch without editing the base Job:

```sh
./spikes/batch-characterization/scripts/render-rknn-job.py \
  spikes/batch-characterization/manifests/rknn-smoke-job.yaml \
  --name mobilenet-v2-100-rknn-dynamic-bs2 \
  --artifact-image 155.230.35.213:5000/chill/spike-mobilenet-v2-100-rknn-dynamic:v1 \
  --artifact-path /artifact/model.rknn \
  --artifact-digest sha256:5cfd87d767e1ad7da49a128c2f4bde25ed1fd52fcbd1324a0ec799e7142626c9 \
  --shape-mode enumerated-dynamic \
  --supported-batches 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16 \
  --batch 2 | kubectl apply -f -
```
