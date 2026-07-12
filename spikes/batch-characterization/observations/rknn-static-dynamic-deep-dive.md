# RKNN static/dynamic batch-2 deep dive

## Invalidated observation

The first dynamic-shape Kubernetes smoke reused an NCHW array after RKNNLite
had converted that array to NHWC. Later calls failed input-shape validation and
returned `None`, but the runner timed the failed calls without checking the
output. Its reported latency and throughput are invalid.

The fixed runner creates a runtime-native NHWC buffer and validates every
output. Earlier RKNN measurements that did not validate output shape are not
scientific evidence of correct inference.

## Controlled comparison

Both artifacts were generated from the same MobileNet-V2-100 ONNX model with
RKNN Toolkit 2.3.2, FP16, batch 2, and ran on `NPU_CORE_0`. Kubernetes runs
used 20 warm-ups and three alternating 30-second measurements per artifact.
Every power window contained 30 successful samples and zero failures.

| Artifact | Mean latency | Mean throughput | Mean power | Mean energy/request |
|---|---:|---:|---:|---:|
| static batch 2 | 10.277 ms | 194.61 items/s | 5.252 W | 0.026989 J |
| dynamic batches 1-16, selecting 2 | 10.618 ms | 188.36 items/s | 5.258 W | 0.027914 J |

Dynamic minus static was +3.32% latency, about -3.21% throughput, +0.11%
power, and +3.43% energy/request. Repeated identical input produced identical
outputs (`max_abs_diff=0`).

The dynamic artifact was 62 MB versus 7.8 MB for static batch 2. RKNN debug
reported 53,312 KB versus 6,664 KB internal memory and 109,713 KB versus
6,984.56 KB weight memory. Per-frame memory read/write was identical at
34,514.89 KB. The remaining end-to-end overhead is therefore observed at the
dynamic artifact/runtime boundary; the available RKNNLite API does not isolate
it to one internal function.
