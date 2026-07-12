# First fixed-batch sweep: Lattepanda ONNX Runtime CPU

Date: 2026-07-11

## Outcome

The Kubernetes Indexed Job completed all five indexes without Pod restarts on
node `lattepanda`. The immutable MobileNet-V2-050 artifact accepted dynamic
batch inputs for batches 1, 2, 4, 8, and 16 through ONNX Runtime
`CPUExecutionProvider`.

The experiment used 20 warm-up executions followed by five repetitions of 100
serial fixed-batch executions for each batch size. Results below are derived
from the structured `EXPERIMENT_RESULT_JSON` records collected from each Pod.

| Batch | Mean execution (ms) | p95 (ms) | p99 (ms) | Throughput (items/s) |
|---:|---:|---:|---:|---:|
| 1 | 35.360 | 54.071 | 58.727 | 28.281 |
| 2 | 65.838 | 86.991 | 90.187 | 30.378 |
| 4 | 129.046 | 166.294 | 171.156 | 30.997 |
| 8 | 255.282 | 285.559 | 289.125 | 31.338 |
| 16 | 493.447 | 558.204 | 563.781 | 32.425 |

These numbers establish a latency/throughput curve for this execution state.
The initial runtime collection did not itself establish an energy curve.

The Prometheus power-observer spike subsequently joined the retained
`shelly_power_total_watts{node="lattepanda"}` samples to the recorded execution
windows:

| Batch | Shelly samples | Mean wall power (W) | Provisional energy/request (J) | Coverage |
|---:|---:|---:|---:|:---|
| 1 | 2 | 6.000 | 0.212160 | insufficient |
| 2 | 3 | 6.100 | 0.200806 | insufficient |
| 4 | 6 | 6.467 | 0.208624 | sufficient |
| 8 | 13 | 6.415 | 0.204717 | sufficient |
| 16 | 25 | 6.352 | 0.195898 | sufficient |

The coverage classification uses a spike-local minimum of five in-window
samples. It is not yet a profiling policy. Batch 1 and 2 are too short relative
to the current 10-second Prometheus scrape interval, so the joined curve does
not support a `b_sat` conclusion. The values demonstrate the adapter path and
remain preliminary measurement evidence.

## Operational observations

- `parallelism: 1` kept batch Pods sequential on the target node.
- Runtime increased substantially with batch size; the complete sweep took
  about nine minutes. A future generalized protocol should express a measured
  duration or precision target rather than assuming one iteration count has a
  comparable experiment cost for every batch.
- The first collector implementation attempted to sort Pods directly by the
  dotted completion-index annotation. `kubectl --sort-by` interpreted dots in
  the annotation key as nested field separators. Collection now extracts the
  escaped annotation with JSONPath and sorts the resulting index explicitly.
