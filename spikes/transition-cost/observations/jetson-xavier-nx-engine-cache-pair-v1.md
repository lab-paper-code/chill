# Jetson Xavier NX EfficientNet engine-cache paired transition

Date: 2026-08-03

This observation records one controlled cold/warm pair for the same
MobileNet-V2-050 to EfficientNet-B4 replacement. The target TensorRT engine
cache is the only configured factor that differs. It is the first attributable
transition toggle in this spike, but one pair is not a population estimate and
does not satisfy the E1 repetition or confidence-interval boundary.

## Frozen comparison

The exact pre-run intent is
[`../intents/jetsonx-mobilenet-to-efficientnet-engine-cache-pair-v1.json`](../intents/jetsonx-mobilenet-to-efficientnet-engine-cache-pair-v1.json),
with digest
`sha256:1c7ba9316ac84ed1774f8e0c248ced4187a8fb04a2509b52834cf5bc90e9538e`.
Cold ran before warm as declared.

Both runs used the same Node UID and boot ID, `MODE_15W_6CORE` power mode,
source and target image digests, model digests, resource contracts, normalized
non-experiment running-Pod set, and BBM full-GPU actuator. The pre-source
temperature means differed by 0.034 degrees Celsius, both transition windows
had thermal observation, and neither reported active thermal throttling. The
semantically normalized target Pod specs differ only in the engine-cache mode
value used by the common init command.

The cold target process verified an empty cache. The warm target process
verified two prebuilt cache files totaling 79,889,620 bytes from the same
digest-bound cache image. The pair validity checks in `paired-analysis.json`
all pass. This is a within-node, one-factor paired observation; it is not a
claim about another node, backend, model pair, or cache format.

## Sampling contract result

Wall power is canonical and was polled directly from the edge metrics exporter
with a target 0.5-second sleep. Internal rail power came from the same response
and remains diagnostic. Prometheus' nominal 10-second wall series is retained
only as a total cross-check.

| Coverage fact | Cold | Warm | Frozen gate |
|---|---:|---:|---:|
| direct wall raw samples in total window | 384 | 35 | at least 20 |
| maximum direct wall gap | 0.787 s | 0.831 s | at most 1.5 s |
| start-boundary distance | 0.184 s | 0.009 s | at most 1.0 s |
| end-boundary distance | 0.095 s | 0.315 s | at most 1.0 s |
| total-window wall coverage | pass | pass | all gates |
| Prometheus wall raw samples | 28 | 3 | cross-check only |

The Prometheus wall integral differed from the direct canonical integral by
+1.93% for cold and +2.31% for warm. This confirms total scale only; three
warm Prometheus samples cannot resolve short phases.

## Paired result

| Observable | Cold engine | Warm engine | Warm minus cold | Reduction |
|---|---:|---:|---:|---:|
| transition command to target Ready | 280.846 s | 26.165 s | -254.681 s | **90.68%** |
| first inference | 254.957 s | 0.752 s | -254.204 s | **99.70%** |
| canonical wall energy, total window | 3,700.617 J | 360.688 J | -3,339.929 J | **90.25%** |
| wall excess over independently measured idle | 831.191 J | 93.118 J | -738.074 J | **88.80%** |
| diagnostic rail energy, total window | 2,033.669 J | 190.125 J | -1,843.544 J | 90.65% |

The independently measured wall-idle baselines were 10.217 W for cold and
10.226 W for warm. Source-serving baselines were 17.697 W and 17.802 W. The
source-serving-referenced wall deltas remain negative in both runs; that sign
is the opportunity effect of BBM, not negative preparation work.

The one-factor observation supports the narrow statement that, on this exact
state pair and actuator, supplying the verified TensorRT engine cache removed
about 255 seconds and 3.34 kJ from the observed transition window. Repetitions
are still required to estimate variance or attach a confidence interval.

## Phase attribution and energy vector

Samples were assigned to lifecycle phases before baseline subtraction. The
long cold first-inference/engine phase lasted 254.959 seconds, contained 349
raw wall samples, and contributed 738.899 J above idle. This phase passed the
per-phase gate. Cold session creation lasted 10.426 seconds with 14 samples
and contributed 22.953 J above idle.

The warm engine-cache phase lasted only 0.752 seconds and contained one raw
wall sample and one raw rail sample. It therefore fails the declared minimum
of five raw samples per attributed phase. No standalone warm engine-phase
energy is claimed. This is a useful gate result: the total transition is
covered, while the sub-second phase requires a faster instrument for phase
energy attribution.

The retained component statuses are:

- `E_prepare`: proxy, not identified. Excluding source grace, the sum of only
  wall phases that pass their individual coverage gates is 795.637 J cold and
  56.187 J warm, a 739.451 J reduction. Mixed startup and uncovered microphases
  prevent promotion to a purified preparation coefficient.
- `E_overlap = 0`: identified for this realized full-GPU BBM path; source and
  target did not execute concurrently.
- `E_forgone`: unavailable. There was no external request path from which to
  measure service opportunity or unserved work.

The total wall integral remains a physical observation and the paired delta is
attributable to the cache toggle under this frozen run. Neither is collapsed
into a general signed transition-cost scalar.

## Availability boundary

No load generator was attached. Transition command to target Ready is therefore
only a potential service-capacity-gap upper bound: 280.846 seconds cold and
26.165 seconds warm. It is not measured request outage, a violated-request
count, or an SLO result.

## Retained evidence

[`raw/2026-08-03-engine-cache-pair-v1/`](raw/2026-08-03-engine-cache-pair-v1/)
retains the exact direct power traces, canonical phase boundaries, runtime and
init logs, observed source and target Pod facts, Node and power-mode snapshots,
normalized environment records, rendered target manifests, events, diagnostic
Prometheus responses, derived analysis, and checksums. The frozen intent lives
outside the raw directory so that its pre-run repository digest remains the
comparison identity.

## Next evidence step

Repeat randomized cold/warm pairs under the same intent family to estimate
variance before promoting an E1 coefficient. Separately, a resource contract
with `coResidencyCapacity >= 2` is required to enter the MBB regime and observe
`E_overlap`; this full-GPU Node cannot answer that question.
