# Jetson Xavier NX TensorRT smoke

Date: 2026-07-12

This observation records one live Kubernetes execution point. It is not a
batch-saturation result.

## Frozen execution facts

- Node: `jetsonx`, DeviceClass label `jetson-xavier-nx-8g`
- Kubernetes accelerator allocation: `nvidia.com/gpu: 1`
- Host: JetPack 5.1.2 family / L4T R35.4.1
- Observed power mode: `MODE_15W_6CORE`, nvpmodel ID 2
- Runtime: ONNX Runtime 1.16.0, `TensorrtExecutionProvider`
- Runtime image base: `nvcr.io/nvidia/l4t-tensorrt:r8.5.2-runtime`
- Model: EfficientNet-B4 ONNX
- Model SHA-256: `f19aa28943b7a79ef31952cd6ac126109edbab16c7bc0eb2ef01ad9a5dc03b30`
- Batch: 1; warm-up calls: 10; steady measurement window: 30 seconds
- Power source: edge-metrics adapter for the Shelly attached to `jetsonx`

## Observed result

- Job: `efficientnet-b4-jetson-trt-smoke`
- Runtime completion: succeeded; no restart
- Provider selected by ORT: `TensorrtExecutionProvider`
- Output shape checked on every call: `(1, 1000)`
- Inferences: 680
- Mean / p95 / p99 latency: 44.151 / 46.317 / 48.424 ms
- Throughput: 22.650 items/s
- Power samples: 30 successful, 0 failed
- Mean / minimum / maximum: 23.007 / 11.700 / 24.400 W
- Power request latency mean / p95: 190.09 / 195.41 ms
- Maximum power-sample gap: 1.006 seconds

The naive whole-window product is approximately 1.016 J/request
(`23.007 W * 0.044151 s`). It is retained only as a provisional diagnostic.
The first power sample was 11.7 W while the remaining 29 samples averaged
23.397 W; excluding that first sample would produce approximately 1.033
J/request. The spike therefore has direct evidence of a measurement-boundary
transient and must not silently choose either value as accepted energy.

## Lifecycle finding

ORT session construction returned after 13.410 seconds, but the first warm-up
did not finish until roughly four additional minutes later while the GPU was
active. TensorRT preparation is therefore not represented by session creation
time alone. A promoted profiler needs separate evidence for session creation,
first successful inference, remaining warm-up, and steady measurement.

The PowerObserver waited for the workload signal throughout preparation and
sampled only the steady window. Its formerly fixed 30-second signal timeout was
made a bounded configurable value; this Job uses 10 minutes.

## Claim boundary and next experiment

This run proves that the artifact/runtime/device tuple is executable through
the Kubernetes GPU resource path and that a synchronized Shelly observation
can complete. It does not prove `bSat`, repeatability, an accepted energy
value, or serving SLO behavior. The next campaign must measure multiple
supported batches with repeated trials, explicitly handle the first power
sample transient, and retain TensorRT engine identity/lifecycle per batch.

Raw evidence:

- `raw/jetson-xavier-nx-efficientnet-b4-bs1-runtime.log`
- `raw/jetson-xavier-nx-efficientnet-b4-bs1-power.log`
- `raw/jetson-xavier-nx-efficientnet-b4-bs1-pod.yaml`

## Execution-provider contract follow-up

The contract-format follow-up ran successfully with runtime image
`jetson-trt-v2`. ORT reported the registered provider chain as TensorRT,
CUDA, then CPU. The result separately recorded TensorRT as primary, CUDA as
the requested fallback, `nvidia-gpu` as the compute device, and FP32 as the
configured precision. Graph partition counts remain `NotCaptured`; provider
registration order is not used as evidence that every graph node ran in
TensorRT.

The follow-up completed 684 inferences with 43.910 ms mean latency and both
containers exited zero. Its raw evidence is retained separately:

- `raw/jetson-xavier-nx-efficientnet-b4-bs1-runtime-v2.log`
- `raw/jetson-xavier-nx-efficientnet-b4-bs1-power-v2.log`
