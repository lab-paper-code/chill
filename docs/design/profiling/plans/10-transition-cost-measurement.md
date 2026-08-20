# Plan 10: Power-Mode Transition-Cost Measurement

> Navigation: [Profiling design index](../README.md)
>
> Research target: `docs/spec/00_measurement.md`,
> `docs/spec/spec_v0.2_plain.md`, and
> `docs/spec/05_transition_control.md`
>
> Existing transition atom: [Plan 9](09-transition-extraction.md)
>
> Observation and storage support:
> [ADR 0013](../../../adr/0013-metric-observation-boundary.md),
> [Evidence Store](../components/evidence-store.md)

## Status

Approved for implementation. This approval does not select a live target or
authorize an unbounded Node restart, image publication, or cluster mutation.

The Kubernetes-independent worker core, immutable raw-evidence write, minimal
`PowerModeTransitionCost` API, and polling Kubernetes fact observer now exist.
The controller preparation boundary resolves the selected transition-power
source and both target-mode baselines into the two ordered directions needed by
future attempt-scoped observation Jobs. One current-only immutable ConfigMap
retains that enclosing preparation and the selected off-target observer Node
before execution. Job projection and the repeatable one-attempt materialization
operation now extend through strict raw-evidence finalization into one immutable,
raw-free observation. A package-local control core now reconstructs the retained
alternating direction prefix, advances only its first missing attempt, and derives
the complete two-row table, writes one terminal result, and publishes compact
status. `cmd/profiler` registers that complete lifecycle with the existing
resolvers and Evidence Store; Helm supplies its off-target placement, operational
bounds, and least-privilege identities. Deployment and live transition
verification remain incomplete.

This plan fills the two directed specification table entries between one
selected pair of device power modes. Each `NodeActuation` opens one observation
window and derives both its elapsed time `tau` and its energy overhead `eta`
from that same window. Transition-time and transition-energy measurements are
not separate experiments.

The specification defines the research quantity. Existing API fields, source
behavior, and ADRs describe the current implementation and its constraints;
they do not redefine `tau` or `eta`.

## Question

> How does one real state transition, including a restart-required transition,
> produce an auditable `tau_(s->s')` and `eta_(s->s')` from one common
> observation window?

## First measured scope

The first implementation selects two power modes on one device type with one
exact model already loaded and serving. Each direction is measured as a
separate state pair:

~~~text
s  = (device type t, model m, source power mode p)
s' = (device type t, model m, target power mode p')
~~~

The exact Node UID, device class, model, execution path, actuator kind and
target, source mode, target mode, serving workload, request-readiness witness,
serving-survival witness, meter source, idle-baseline evidence, direction
order, accepted-count target, attempt bound, and measurement policy are frozen
before execution.

This is a valid state-pair measurement, not a claim that every transition has
the same cost. Source-state independence, target-only reduction, another
actuator kind, or model replacement requires its own measured evidence.

## Measurement contract

One successfully completed `NodeActuation` with sufficient measurement evidence
produces one accepted transition observation for its exact state pair:

~~~yaml
transitionObservation:
  nodeActuation: ...
  sourceState: ...
  targetState: ...
  transitionWindow:
    startedAt: ...
    completedAt: ...
    boundaryUncertaintyMilliseconds: ...
  tauMilliseconds: ...
  transitionWallJoules: ...
  targetIdleBaseline:
    evidence: ...
    meanWallWatts: ...
  etaJoules: ...
  servingSurvived: ...
  rawEvidence: ...
~~~

The boundaries have one meaning:

- `startedAt` is when the frozen transition operation begins execution after
  the power observer is ready;
- `completedAt` is the earliest instant after independent target-state
  read-back at which the frozen request-readiness witness succeeds; and
- `tauMilliseconds` and `transitionWallJoules` use exactly those two
  boundaries.

For the first power-mode path, the request-readiness witness is the selected
serving workload's existing Kubernetes Ready signal, backed by its existing
application readiness check. The measurement injects no inference load merely
to close the window. For an in-place transition, the witness confirms that the
same workload remains request-ready in the target mode. After a restart, it
closes the window only after the target mode is read back and the workload is
Ready again. The witness has a frozen freshness bound; a pre-transition
`Ready=True` observation cannot close a post-transition window.

Only those factual observations close the transition. No configured delay or
post-hoc power rule moves `completedAt`. Operational phase timestamps may
remain as diagnostic evidence, but none substitutes for transition completion.

## Joint derivation of tau and eta

Let `P(t)` be the wall-power trace from the restart-independent meter, and let
`P_idle(s')` be an admitted model-loaded-idle baseline for the exact target
state. The first estimator is:

~~~text
tauSeconds             = completedAt - startedAt
tauMilliseconds        = tauSeconds converted to milliseconds
transitionWallJoules   = integral of P(t) from startedAt through completedAt
etaJoules              = transitionWallJoules - P_idle(s') * tauSeconds
~~~

`eta` is signed and is never clamped. The raw wall energy, exact baseline
reference, sample trace, integration policy, and boundary uncertainty remain
available so the result can be reproduced.

`P_idle(s')` is an aggregate `profilederivation.IdleBaseline`, not one
`ProfilingRun` value. The independent `ModelLoadedIdleBaseline` producer now
runs only model-loaded-idle Runs, admits their retained evidence through
`EvaluateIdle`, derives the aggregate through `DeriveIdleBaseline`, and writes
one immutable Complete result with its accepted final-evidence references. Its
stored measurement state fixes the target Node, ModelSpec, DeviceClass,
execution path, and power mode. The execution path is a frozen experimental
compatibility condition for reusing that aggregate; it does not add a new axis
to the state-pair equation above. The transition-cost baseline source accepts
only the current independent result schema, requires its exact stored
measurement state, and freezes the stored mean and full result reference. It
does not read `BatchSaturation` results or support a compatibility reader.

The specification requires `tau`, `eta`, and the target-state idle term for its
transition-cost equation but does not prescribe this subtraction algorithm.
This equation is Plan 10's explicit measurement estimator. It must be validated
against retained transition observations rather than presented as text copied
from the specification.

## Serving-survival observation

The specification asks whether the serving process survives the transition in
addition to time and energy. The same observation therefore records the
serving process/container facts before `startedAt` and after `completedAt`.

- `servingSurvived=true` means the pre- and post-transition process facts show
  that the serving process was not replaced or restarted;
- `servingSurvived=false` is a valid measured result when that process is
  observed to terminate or restart; and
- missing or untrustworthy survival evidence makes the transition observation
  insufficient rather than silently producing `false`.

The enclosing measurement selects one serving workload and its restart witness.
Each observation retains only the resulting process, restart, and readiness
facts; it does not repeat the workload selection. The observation adds no
synthetic inference traffic to the measured device, and the compact boolean is
the table value.

## Ownership and placement

One `PowerModeTransitionCost` is one repeated measurement of two directed state
pairs. It directly owns:

- one observation Job per attempted `NodeActuation`, placed away from the
  target Node, with at most one attempt Job active; and
- the sequential `NodeActuation` objects it creates only after the
  corresponding Job Pod becomes Ready after the observer-ready marker exists.

It retains compact references to one immutable raw and one immutable finalized
evidence object per `NodeActuation`, plus one immutable final result object
containing the two directed table entries.

There is no additional CHILL API object for an individual measured transition.
The native attempt Job and `NodeActuation` share one deterministic name; the
`NodeActuation` UID identifies the execution, and its finalized evidence
identifies the corresponding scientific observation.

`NodeActuation` remains the one-way state-changing atom and the per-Node
actuator remains the sole host-state writer. It applies the target and performs
independent target read-back. It does not poll the meter, integrate energy,
observe workload readiness or survival, calculate `tau` or `eta`, or write
measurement evidence.

The off-target observer reads power and watches the frozen readiness and
survival evidence. Both the observer process and its physical meter path must
remain available while the target Node restarts. Moving the process off-target
is insufficient when its data source is an exporter that disappears with the
target Node.

### Selected meter path

The first implementation places the observer on `genesis1`, away from the
target Node, and reads the central Shelly Gateway there:

~~~text
Shelly -> ws://155.230.35.213:18765
       -> edge-metrics-exporter-shelly-gateway
       -> http://155.230.35.213:18766/prometheus?hostname=jetson-orin
       -> off-target transition observer
~~~

The observer selects exactly this Prometheus series:

~~~promql
shelly_power_total_watts{
  hostname="jetson-orin",
  device_type="jetson_orin"
}
~~~

Zero or multiple matching series is a failed meter read. An unmapped Shelly or
a series for another device never substitutes for the selected series. The
three currently connected but unmapped Shellys are therefore outside this
measurement and do not block it.

The Gateway's `hostname` filter limits the response, but does not replace
evidence validation. The configured query value must equal the frozen
`hostname` label, and the observer still requires the returned series to have
exactly both frozen labels, including `device_type`.

The same static Node declaration supplies this exact power source to profiling
Runs together with a separate local thermal endpoint. A profiling Run combines
both reads in one bounded tick; the transition observer remains power-only and
consumes the declaration's exact power fields directly.

The selected Gateway is Deployment
`monitoring/edge-metrics-exporter-shelly-gateway`, currently delivered by Helm
release `edge-metrics` revision 26 with image tag `sha-94c425c`. The enclosing
measurement record selects the metric name, both label values, filtered
endpoint, and Gateway configuration once; individual transition observations
do not repeat that selection. The Gateway is measurement infrastructure:
neither `PowerModeTransitionCost` nor its observer owns, deploys, reconfigures,
or restarts it. The existing per-Node exporter DaemonSet is not part of this
meter path.

The `PowerModeTransitionCost` controller freezes intent, sequences one
`NodeActuation` at a time, verifies its evidence, and publishes compact status.
It does not poll the meter in `Reconcile` or keep authoritative samples only in
process memory. A plain Plan 9 `NodeActuation` remains usable without a
measurement lifecycle.

## Boundary and clock authority

Width and area can share a window only when transition markers and power
samples are comparable in one declared time domain. The first implementation
selects one concrete marker-transfer and meter-timestamp path; it does not add
a generic clock-synchronization framework.

The selected Prometheus endpoint supplies no device-source timestamp. The
observer therefore records its HTTP receipt time and request latency for every
read and retains both in raw evidence. It does not synthesize or claim a Shelly
timestamp. Window-boundary uncertainty covers mapping transition markers into
that observer time domain; the per-read receipt and latency records remain
available to audit the samples used by the integration.

Raw evidence retains:

- the device-side transition-start and target-read-back facts;
- the observer-side meter samples, workload-readiness facts, and serving
  process/restart facts;
- the mapping used to place both window boundaries in the meter trace; and
- the measured upper bound on boundary-alignment error.

`boundaryMappingUncertaintyMilliseconds` freezes that Gate 0 measured and
selected marker-transfer upper bound. It must cover the frozen fact-poll
interval. `maximumBoundaryUncertaintyMilliseconds` is a separate observation
acceptance ceiling; it does not supply or replace the measured bound.

If the selected path cannot bound that error tightly enough for the frozen
sampling and integration policy, implementation stops at the feasibility gate.
It does not invent an offset, assume host clocks are identical, or widen the
error bound after seeing the result.

## Control flow

~~~text
freeze one PowerModeTransitionCost
    |
    v
repeat one directed transition at a time
    |
    +-> confirm the exact required source state
    |
    +-> create one off-target attempt observer Job
    |       |
    |       +-> observer-ready marker -> Job Pod Ready
    |
    +-> create one NodeActuation
    |       |
    |       +-> actual transition begins -------- transitionWindow.startedAt
    |       |                                     power integration begins
    |       +-> apply -> optional restart -> independent target read-back
    |       +-> fresh workload Ready ------------ transitionWindow.completedAt
    |                                             power integration ends
    |
    +-> observer writes one immutable raw object
    +-> attempt observer Job exits
    +-> controller verifies and finalizes the transition observation
    +-> continue only from the independently confirmed target state
    |
    v
accepted-count target or attempt bound reached
    |
    v
derive both directed table entries and write one immutable final result
    |
    v
publish compact PowerModeTransitionCost status
~~~

The observer starts power and workload observation before the transition so
neither boundary depends on observer startup. Only the interval from
`startedAt` through `completedAt` contributes to `tau` and `eta`; the
pre-window data proves source health and supports boundary placement.

## Evidence contract

Raw samples and observation records never enter `NodeActuation.status` or
`PowerModeTransitionCost.status`. The enclosing control and evidence record
owns the measurement selection once: state pair, execution path, actuator,
meter series, serving workload, observer placement, estimator, policy, and
target-idle baseline. Each per-transition raw object retains only the
observations made under that selection:

- source and target state read-backs;
- every bounded power sample and failed read with its observer receipt time and
  request latency;
- every process/restart fact and readiness event;
- transition-start, target-read-back, and request-readiness boundary facts; and
- boundary-mapping uncertainty.

Each finalized transition object records the state pair, transition window,
`tau`, raw wall energy, baseline, `eta`, serving-survival flag, verdict, and
reason. The final result object aggregates only verified transition objects
into the two directed table entries. Kubernetes status carries only phase,
compact outcome, accepted counts, and verified immutable references.

A worker receipt is only a hint. The controller independently reads the raw
object, strictly decodes it, validates its structure and state read-backs once,
derives the result from that value, and writes a separate finalized object.

The first stored schema is current-only. A later stored-contract change is one
explicit work unit; obsolete test objects are deleted rather than supported by
compatibility readers.

## Terminal outcomes

- Power observation does not become Ready: create no `NodeActuation` and
  retain no transition observation.
- The observed source state differs from frozen `s`: terminate with
  `SourceStateMismatch`; do not relabel the observation.
- The Node already holds `s'`: no transition occurred and no transition
  observation is accepted.
- Actuation or independent target read-back fails: retain partial raw evidence
  and publish no accepted `tau` or `eta`.
- The target is read back but the frozen request-readiness witness never
  succeeds: the transition did not complete within the measurement deadline;
  retain evidence and publish no accepted cost.
- Meter loss, excessive sample gap, unbounded clock alignment, or missing
  readiness or survival evidence makes that observation insufficient even when
  the state transition itself succeeds.
- A measured serving interruption produces `servingSurvived=false`; it does not
  invalidate otherwise complete time and energy evidence.
No outcome starts an automatic recovery transition. Returning to the source
state is another explicitly requested and independently measured
`NodeActuation`.

## Delivery plan

### Gate 0: physical measurement feasibility

1. Select one exact target Node and state pair while using the off-target
   observer on `genesis1` and central Shelly Gateway path declared above.
2. Select one concrete transition-start marker, target read-back,
   request-readiness witness, and serving-survival witness for that path.
3. With separate live-test authorization, capture one bounded trace across a
   restart-required transition.
4. Report sample cadence, failed reads, maximum gap, timestamp authority,
   marker-transfer path, and boundary-alignment uncertainty.
5. Stop if the observer or meter path disappears with the target Node, if the
   workload facts cannot distinguish Ready, restart, and unknown, or if the
   timing error exceeds the frozen integration policy.

This gate changes no CHILL API or production controller. This document alone
does not authorize the live transition in step 3.

The central Gateway Deployment and device migration are complete. Its full
Prometheus endpoint still exposes all seven mapped device series, each of the
seven hostname-filtered requests returns only that Node's series, and an
unknown hostname returns HTTP 200 with an empty body. The Prometheus target is
up, per-device request failures are isolated by the Gateway, and the existing
exporter DaemonSet image and generation are unchanged. The old per-Node
WebSocket servers have no connected devices and remain outside this path.
These checks do not prove restart-spanning continuity. Gate 0 remains
incomplete until a bounded restart-required transition trace shows that the
exact selected series continues to be collected while the target Node is
unavailable and returns.

Under Helm revision 5 and the current split-source configuration on 2026-08-20,
BatchSaturation `plan10-gate0-maxn-baseline` reached
`Complete`/`Stopped-CapabilityBoundary` with 6/6 trials `Accepted`. Its
`chill.dacs.io/batch-saturation-result.v1alpha3` result fixes the measurement
state to `jetsono` / `mobilenet-v2-050` / `jetson-agx-orin-64g` / `ort-cpu` /
`MAXN`. Three model-loaded-idle trials produced an aggregate mean of
`8.484698057297415 W`, variance `0.000047230885953752635`, 2 degrees of freedom,
and a 95% half-width of `0.029569864969649712 W`. The first idle trial's metric
observer retained 31/31 power samples with zero failed reads; thermal
observation was available for 31/31 samples, throttling was active for 0/31,
and the maximum temperature was `45.218 C`.

The model-loaded HTTP readiness witness `plan10-serving-jetsono` is `Ready`, and
Service `chill-serving-jetsono` has exactly one ready endpoint. This is not an
inference or SLO server; it witnesses only Pod readiness and process continuity.
Gate 0 remains incomplete because there is no `MODE_15W` idle baseline and no
restart-required trace. The separate reboot approval is still pending.

### Unit 1: pure observation and derivation contracts

1. Define Kubernetes-independent raw fact and result types for one transition
   observation and a result type for the two directed table entries.
2. Implement structural validation, bounded integration, target-idle
   subtraction, serving-survival reduction, directed summary construction, and
   typed terminal outcomes as pure functions.
3. Add focused tests for common boundaries, signed `eta`, source mismatch,
   target-read-back failure, stale or failed request readiness, serving
   interruption, sample gaps, and boundary uncertainty.

Stop for review. This unit adds no API, controller, Job, or Helm surface.

### Unit 2: one restart-independent observer

1. Reuse the bounded metric-observation polling core where its existing
   contract fits. Add only a central Shelly Gateway Prometheus reader that
   selects the exact metric and label pair declared above; do not add a generic
   meter-source or plugin framework.
2. Add one worker that verifies its meter and workload-observation paths,
   creates the observer-ready marker, buffers each pre-window, observes exact
   boundaries, and writes one raw evidence object per observed
   `NodeActuation`.
3. Prove offline that the worker retains samples and workload events while a
   simulated target executor disappears and returns; do not call that a live
   restart proof.

Stop for review. The observer still does not create or mutate a transition.

The worker assembly, current raw-write boundary, and concrete polling
transition-fact observer are implemented separately. The minimal
`PowerModeTransitionCost` API exists. Controller preparation now projects its
observation policy, resolves one central transition-power source and both
target-mode baselines, and prepares the two directed core contracts. Its power
request extends one sample interval beyond the fact timeout only to retain the
last possible sample; factual completion and integration boundaries do not
move. The registered controller alternates the two directions, materializes and
finalizes one attempt at a time, and stops at the accepted-count target or frozen
attempt limit. A current-only terminal result writer retains the exact two-row
table for a complete measurement, or the terminal reason and per-direction
finalized-evidence progress for an insufficient or failed measurement. Compact
status points to that terminal object; rendered configuration and RBAC do not
constitute deployment or live measurement proof.

### Unit 3: complete measurement control path

1. Add the minimal `PowerModeTransitionCost` API and generated artifacts. Its
   immutable spec contains the exact states, direction order, accepted-count
   target, attempt bound, observation policy, estimator, and evidence binding.
2. For each attempt, reconcile one off-target observation Job before creating
   its owned `NodeActuation`, with at most one attempt Job active.
3. Create at most one `NodeActuation` at a time and expose only the device-side
   facts required to locate actual transition start and independent target
   read-back.
4. Observe fresh workload readiness after target read-back and finalize one
   transition observation from the verified raw object.
5. Create the next `NodeActuation` only when the preceding one independently
   confirms the exact state required as its source. An unknown or mismatched
   state terminates the measurement without a recovery transition.
6. Retain every accepted, insufficient, invalid, and failed observation.
   Reaching the frozen attempt bound without enough accepted observations
   produces an insufficient result rather than an unbounded retry.
7. Report `tau`, `eta`, and serving survival per direction with the
   predeclared summary and uncertainty method. Energy results require at least
   five accepted observations per direction.
8. Write one final table with the two directed state-pair rows. Do not reduce
   it to target-only costs until sampled state pairs support that assumption.
9. Remove the current `NodeActuation.status.transitionCost` aggregate and
   segment fields after their diagnostic consumers are migrated. Operational
   lifecycle timestamps may remain, but finalized observation evidence becomes
   the sole scientific transition-cost authority.
10. Add only the placement, RBAC, trusted-source configuration, and Evidence
    Store binding required by this exact path.

Stop for review. The directed table is the research output of this plan.
`DeviceProfile` publication, scheduler consumption, and EWMA correction remain
later work.

### Unit 4: live vertical verification

With explicit live-test authorization:

1. create one `PowerModeTransitionCost` for the exact state pair proven
   feasible by Gate 0;
2. capture observer placement, meter continuity, every owned `NodeActuation`,
   raw evidence, finalized transition evidence, and final result evidence; and
3. independently reproduce each `startedAt`, `completedAt`, `tau`, wall
   joules, `eta`, serving-survival value, and both directed table entries from
   the immutable objects.

The live result proves only that exact device, model, state pair, actuator, and
measurement protocol. It does not establish target-only cost or another
device's result.

## Verification gates

Each implementation unit runs focused package tests. Before handoff, run the
applicable repository gates:

- `make lint` and `make test` for Go behavior;
- `make arch-check` for a new production package or dependency;
- `make manifests generate` with generated-drift inspection for API changes;
- `make helm-lint helm-template` and separate default and explicitly configured
  Profiler-enabled renders for Job, RBAC, or values changes; and
- mutation-to-red tests for shared window boundaries, integration, baseline
  subtraction, request-readiness completion, serving interruption, sample-gap
  rejection, boundary uncertainty, and observation-before-transition ordering.

Rendered configuration and offline executor-loss tests do not prove off-target
placement, physical meter continuity, restart survival, evidence persistence,
or a measured transition cost. Those claims require captured live evidence.

## Explicit exclusions

- post-hoc end-boundary inference or a configured wait;
- treating the current operational `ReadyAt` duration as
  `tau_(s->s')`;
- claiming one measured direction represents another state pair;
- a generic actuator, meter-source, readiness-witness, or clock plugin
  registry;
- an always-on telemetry service or Prometheus-history dependency;
- automatic restore, retry chains, recovery states, epochs, or unrelated
  fencing;
- digest recomputation, canonical re-encoding, or byte-equality validation;
- compatibility readers for older transition evidence; and
- `DeviceProfile` publication, scheduler integration, EWMA correction, image
  publication, or live-cluster mutation without separate authorization.

## Review questions

1. Do `startedAt` and `completedAt` express the specification's actual
   transition-to-request-ready interval for the selected state pair?
2. Does the selected workload readiness fact establish request readiness
   without adding measurement traffic or an inferred end boundary?
3. Is admitted target-state idle evidence the accepted baseline for the signed
   `eta` estimator?
4. Does Gate 0 bound restart-independent meter continuity and boundary timing
   before production APIs are added?
5. Does the repeated experiment produce the state-pair table needed by the
   research specification without prematurely publishing scheduler inputs?
