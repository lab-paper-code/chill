# Jetson Xavier NX MobileNet to EfficientNet cold transition

Date: 2026-08-03

This observation records one live Kubernetes model replacement from a running
MobileNet-V2-050 TensorRT process to a cold EfficientNet-B4 TensorRT process.
It did not retain a frozen transition intent before execution. It is therefore
a post-hoc parameter-discovery observation, not an accepted `tau`, `eta`, or
E1 sample.

## Question and boundary

The immediate question was whether model replacement is small enough to
undermine the motivation for transition-aware control. The measured path is a
direct break-before-make Pod replacement on a node with one allocatable GPU.
The target uses a newly published, initially uncached artifact image and an
empty Pod-local TensorRT engine cache. The runtime image was already present.

This run does not satisfy the specification's final protocol: it has one trial,
does not drop the host page/inode cache, does not randomize model order, does
not compare loaders, and does not provide the repetitions and confidence
interval required for an energy result. The Node's power-mode identity was not
captured. No external request generator or Service measured request-level SLO
survival.

## Post-hoc parameter audit

The run predates the contract in [`../METHOD.md`](../METHOD.md). The
following values can be reconstructed from retained evidence, but their
post-hoc status is part of the result:

| Record | Reconstructed value | Attribution status |
|---|---|---|
| procedure requested | direct Pod `BBM` | not frozen before mutation |
| procedure realized | capacity-forced `BBM` | observed: target first failed scheduling on GPU capacity |
| serving during transition | no external load; internal source loop continued until termination | request-level behavior unavailable |
| target runtime image | warm | observed pre-existing runtime image |
| target artifact image | cold | observed unique new image and target image ID |
| target materialized artifact | cold | observed empty model volume populated by init container |
| target engine | cold | observed 0-byte cache becoming 79,883,571 bytes |
| analysis baseline | source-serving | chosen only during post-run analysis |
| canonical window | delete-request issue to target runtime readiness marker | reconstructed from retained epochs |
| co-residency capacity | one full-GPU serving Pod | observed Node capacity 1 and request 1 per Pod |
| power mode | unavailable | not captured |
| co-resident Pod set at both boundaries | unavailable | not captured |
| thermal/throttle state | observed over the window | available and unthrottled |

The shorthand `all-cold` would be inaccurate: the target artifact and engine
paths were cold, but the TensorRT runtime image was warm. This distinction is
one reason later runs must bind each cache independently.

### Retro-label decision: not eligible for a causal delta

The cold observation cannot be retro-labeled as the reference side of a
one-factor warm-engine comparison. Required values remain unavailable:

- source and target power-mode identity;
- co-resident Pod identities and resource contracts at both boundaries;
- the source Pod's retained full resource and image-status snapshot; and
- wall-power sampling at the sub-second/one-second resolution needed for an
  expected roughly 25-second warm transition.

The retained 10-second wall series is sufficient for the cold total and long
engine-build phase, but it cannot satisfy the future warm protocol's
short-phase coverage. A controlled cold run under the same frozen protocol is
therefore required before interpreting `warm - cold` as an engine-cache-only
delta.

## Frozen execution facts

- Node: `jetsonx`, UID `4a093de6-af5f-4df0-96b2-c6f6c561c970`
- DeviceClass label: `jetson-xavier-nx-8g`; architecture: `arm64`
- Kernel: `5.10.120-tegra`; containerd: `1.7.12`
- Accelerator allocation: one `nvidia.com/gpu` for both source and target Pods;
  the Node has one allocatable `nvidia.com/gpu`
- Runtime: ONNX Runtime 1.16.0 with TensorRT, CUDA, then CPU providers
- Runtime image observed on the target:
  `sha256:ee5cc6e44bed05a9003e367785bc26a1b8f6e8e8aafb9bbf0acc8de0edbc5fec`
- Source model: MobileNet-V2-050, 7,875,901 bytes,
  `sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2`
- Source artifact OCI index:
  `sha256:1db7b031013d0c5bdf0f4a30c8647f694baf377243bf845d3ccf2e8a64a079b6`
- Target model: EfficientNet-B4, 77,264,016 bytes,
  `sha256:f19aa28943b7a79ef31952cd6ac126109edbab16c7bc0eb2ef01ad9a5dc03b30`
- Target artifact OCI index and observed image ID:
  `sha256:33bda75cb21051bb626831980254b2afb79514e3673f862c8695c94e3360c770`
- Target batch: 1; TensorRT FP32; three warm-up calls after the first
  successful inference
- TensorRT engine cache: empty at target process start; Pod-local `emptyDir`

The artifact images were published to the internal development registry with
unique `prelim-c46d0b3` tags. The registry exposes OCI indexes, and the target
Pod status confirmed the EfficientNet index digest above.

## Event result

The transition request was the source Pod deletion request at
`2026-08-03T09:46:04.861001508Z`. The target process emitted its service-ready
event at `2026-08-03T09:50:51.029625026Z` after writing its readiness marker.
The resulting request-to-ready interval was **286.169 seconds**.

| Observable interval | Seconds | Evidence boundary |
|---|---:|---|
| replacement request to target process start | 14.139 | Kubernetes action to target runtime event |
| target artifact digest verification | 0.148 | runtime monotonic duration |
| ONNX Runtime session creation | 11.387 | runtime monotonic duration |
| first inference and lazy TensorRT engine construction | **260.334** | runtime monotonic duration; cache changed from 0 bytes to 79,883,571 bytes |
| three post-build warm-up calls | 0.128 | runtime monotonic duration |
| replacement request to service-ready | **286.169** | action epoch to target ready epoch |

The lazy engine-construction interval alone accounts for about **91%** of the
request-to-ready time. Session creation is therefore not a valid proxy for the
target's usable readiness.

### Post-hoc phase energy attribution

The direct `jetson_power_vdd_in_watts` trace contains 392 raw samples inside
the transition window: 1.370 Hz mean cadence and 1.101 seconds maximum gap.
Six stable pre-source samples of the same metric give a post-hoc
`nodeStandby` candidate of 4.997 W. The original derived field names use
`idleReferenced`; under the refined taxonomy they mean node-standby-referenced
sensitivity only. Assigning the high-resolution samples to retained runtime
epochs produces:

| Phase | Seconds | Raw rail samples | Max gap (s) | Mean rail W | Rail J | Excess over `nodeStandby` J |
|---|---:|---:|---:|---:|---:|---:|
| source termination grace through last completed source window | 4.258 | 6 | 0.739 | 10.684 | 45.488 | 24.213 |
| last source window to target process start | 9.881 | 13 | 0.890 | 6.966 | 68.829 | 19.452 |
| artifact verify and handoff to session creation | 0.150 | 0 | 0.150 | 6.633 | 0.996 | 0.246 |
| ONNX Runtime session creation | 11.386 | 15 | 0.788 | 6.355 | 72.355 | 15.458 |
| session-to-first-inference handoff | 0.020 | 0 | 0.020 | 6.165 | 0.123 | 0.023 |
| first inference and TensorRT engine construction | 260.339 | 357 | 1.101 | 7.257 | 1,889.197 | **588.281** |
| post-engine warm-up to Ready | 0.134 | 1 | 0.068 | 11.108 | 1.490 | 0.820 |

The source-grace row is source serving, not preparation. Excluding it leaves a
624.279 J node-standby-referenced rail proxy across the target-preparation
phases; the engine phase contributes 588.281 J, about 94% of that proxy. The
last-source-window-to-process row still mixes termination, capacity wait,
sandbox/image work, and runtime handoff, so it is not a purified artifact-pull
coefficient.

Wall power remains canonical. Its 10-second series has 26 raw samples inside
the long engine phase and gives 3,453.940 J total for that phase, or 937.326 J
above the post-hoc 9.667 W wall `nodeStandby` candidate. The shorter phases
contain at most one raw wall sample each and are insufficient for wall-level phase
attribution. The rail phase table is therefore diagnostic attribution, while
the 3,810.75 J wall integral remains the canonical total cross-check.

This decomposition separates the potential preparation proxy from source
grace and the unobserved service opportunity. `E_forgone` itself remains
unavailable: without a declared external request path, the retained process
epochs provide only a potential capacity-gap upper bound, not actual unserved
work.

The cold image-pull component cannot be isolated exactly from this run.
KubeEdge retained the target's scheduling and container-state times but emitted
no `Pulling` or `Pulled` event. The target was scheduled at
`09:46:11Z`, and the artifact init container first logged at
`09:46:14.966Z`; this approximately four-second interval is a composite upper
bound containing Pod sandbox creation, the cold 77 MB artifact-image fetch,
and init-container start. The artifact init container then copied and hashed
the ONNX file in 1.145 seconds. Its completion to target runtime-process start
took another 2.889 seconds. These values are useful diagnostics, not purified
image-pull or weight-loader parameters.

The target first hit `FailedScheduling` because the source held the Node's only
GPU. It scheduled after source release. With one GPU requested per Pod, this
Pod-level path cannot make the target ready before breaking the source.

## Power and thermal observation

Prometheus retained original Shelly wall-power samples at a 10-second scrape
interval. Trapezoidal integration with linear interpolation at the exact
request and service-ready boundaries produced:

- 28 raw in-window samples, plus two interpolated boundaries
- mean wall power: 13.316 W
- transition-window wall energy: **3,810.75 J**
- preceding 60-second active-source baseline: 17.283 W from six raw samples
- diagnostic difference from continuing that active-source baseline for the
  same duration: -1,135.20 J

The original analysis implicitly selected the source-serving baseline. A
post-hoc sensitivity analysis selected six stable pre-source samples from
`09:42:22.138Z` through `09:43:12.138Z` as a `nodeStandby` candidate. Their
mean was 9.667 W. Applying the same transition window changes only the
counterfactual:

| Named counterfactual | Baseline power | Counterfactual energy | Observed minus counterfactual |
|---|---:|---:|---:|
| zero | 0 W | 0 J | +3,810.75 J |
| post-hoc `nodeStandby` candidate | 9.667 W | 2,766.30 J | **+1,044.45 J** |
| post-hoc source serving | 17.283 W | 4,945.95 J | **-1,135.20 J** |

The sign reversal is produced solely by baseline selection. Neither signed
delta is promoted to transition cost because no baseline was frozen before the
run and the aggregate wall trace cannot isolate preparation from the source's
4.258-second termination grace. Under the component method,
`+1,044.45 J` is only a whole-window node-standby-referenced excess; the phase
table above replaces it with separately labeled rail proxies. `E_overlap=0` is
identified for this full-GPU BBM path, while `E_forgone` is unavailable because
no external serving load was declared. The 3,810.75 J total integral remains a
physical observation rather than accepted `eta`.

A separate approximately 0.72-second direct sampler observed the Jetson input
rail. It integrated to 2,078.48 J at 7.263 W mean over the same exact
boundaries. This is diagnostic internal-rail evidence, not a substitute for
the Shelly wall measurement.

Thermal observation was available throughout the Prometheus window. The 28
samples spanning the transition reported no active throttling, and Shelly
temperature remained between 51.6 and 52.0 degrees Celsius.

## Serving and performance boundary

The source process continued its internal one-second inference loop for 4.258
seconds after the deletion request before termination. Delete request to
target Ready was 286.169 seconds; without an external request path this is a
potential capacity-gap upper bound, not a measured request outage. The elapsed
time from the source's last completed service window to the target's first
completed service window was 282.925 seconds. The target then sustained
approximately 23 inferences per second with roughly 44--45 ms mean latency in
its first steady windows.

This establishes a capacity gap for the measured break-before-make path. It
does not prove request-level outage duration or compare source and target
performance: there was no external request path, and the two models have
different computational costs. Source-process termination is an actuator-path
fact for Kubernetes Pod replacement, not a general statement about in-place
power-mode actuators.

## Preliminary decision

This diagnostic path took about 4.8 minutes and exposed device-specific
TensorRT construction as the first controlled toggle. Without a frozen intent,
it cannot support a general transition-cost claim or establish that ordinary
transitions always cost minutes. The dominant observed component is also
cacheable.

The next comparison must first rerun this cold side under a frozen protocol,
then run the same state pair with only a verified persistent or prebuilt target
engine-cache hit changed. The full E1 campaign must add at least 20 randomized
cold-start trials for load timing, at least five energy trials with confidence
intervals, explicit power-mode identity, controlled cache and loader groups,
standard image-pull timing, and an external serving/load path.

## Retained evidence

The [`raw/2026-08-03`](raw/2026-08-03/) directory retains the exact source and
target runtime logs, target artifact and Pod facts, Node and service-script
snapshots, Kubernetes boundaries and events, direct rail samples, Prometheus
wall-power/rail/thermal samples with their original timestamps, and the
derived analysis JSON. The Markdown values above can be recomputed from those
files without consulting the live namespace.
