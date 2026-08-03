# Transition spike method

## Status and purpose

This is the minimum experiment contract for the isolated transition-cost
spike. It reuses the profiling system's separation between declared protocol,
material physical identity, and observed environment. It does not define a
production transition API or authorize publication into `DeviceProfile`.

Every controlled run must retain its exact pre-run intent bytes and digest
before the first state mutation. Post-run reconstruction may diagnose an old
observation, but it cannot retroactively make that observation controlled.

## Frozen records

### `TransitionProtocol`: configured choices

The protocol records what the experimenter chose:

- `actuatorKind`: for example Kubernetes full-GPU Pod replacement;
- `procedure`: `BBM`, `MBB`, or an explicit overlap duration;
- `cachePrecondition`: separate states for runtime image, artifact image,
  materialized artifact bytes, and backend engine (`cold` or `warm`), including
  how each state was verified;
- `servingDuring`: `none` or a fixed external load and rate;
- `baseline`: `sourceServing`, `idle`, `standby`, or `zero`, with an independent
  baseline window and evidence identity;
- `powerObservation`: canonical and diagnostic metric identities, direct poll
  cadence, minimum phase/window samples, maximum sample gap, maximum boundary
  distance, and the handling of missing or duplicated samples;
- `window.start`: one canonical transition-command event; and
- `window.end`: one canonical target service-ready event.

The baseline is part of the protocol, not a property discovered after looking
at the sign of the result. A run may be re-evaluated against another retained
baseline only as an explicitly named sensitivity analysis.

Wall power remains the canonical energy source required by
`docs/spec/00_measurement.md` section 4.2. Internal rail power may provide
higher-resolution phase attribution, but it is diagnostic and cannot replace
the wall integral unless a run declares that explicit spec exception before
execution. A short transition must not inherit a 10-second Prometheus scrape
implicitly: its intent must either configure a sufficiently fast direct
wall-power poll or fail its energy-coverage gate.

### `TransitionStatePair`: material physical configuration

Both `from` and `to` bind:

- Node UID and DeviceClass;
- model artifact digest;
- runtime image digest, runtime version, backend/provider, and precision;
- power-mode identity; and
- CPU, memory, and accelerator resource contract.

The pair also binds `coResidencyCapacity`: the number of source/target serving
instances that the declared resource contract can place concurrently. This is
not inferred from the chosen procedure. For example, one allocatable GPU with
one full GPU requested by each Pod gives `coResidencyCapacity=1` for that
contract. It explains why a requested MBB Pod procedure is unrealizable
without claiming that all possible GPU-sharing actuators have capacity one.

The intent records both `procedureRequested` and `procedureRealized`. A
capacity-forced BBM is therefore distinguishable from an experimenter-selected
BBM.

### `TransitionEnvironment`: observed, not configured

The environment retains facts sampled at the declared boundaries and over the
window:

- co-resident Pod identities and resource contracts;
- thermal-observation availability, temperature, and throttle state;
- Node boot and runtime identities; and
- metric-source identity, timestamps, coverage, and gaps.

Missing environment evidence is reported as unavailable. It is never filled by
a protocol default.

## Retro-label decision

Before comparing a pre-contract observation with a controlled run, enumerate
every required field above and ask whether its value and evidence identity are
recoverable from retained raw evidence. The decision is binary:

- all fields recoverable: retain a post-hoc label and mark that it was not
  frozen before mutation; or
- any field unavailable: the observation cannot be one side of a one-factor
  causal delta, and that side must be rerun under the frozen protocol.

A digest created after execution does not repair a missing value or turn the
old run into a controlled run.

## Energy vector

Retain the total wall-power integral as a physical observation, but do not call
one baseline-subtracted scalar "transition cost". Report these components
separately, each with `identified`, `proxy`, or `unavailable` evidence status:

- `E_prepare >= 0`: preparation work above the independently measured idle
  baseline;
- `E_overlap >= 0`: additional source-and-target co-execution energy for an
  MBB or overlap procedure; and
- `E_forgone <= 0`: source-serving dynamic energy not spent during a BBM
  service gap. This is an opportunity difference, not preparation cost.

Aggregate wall power cannot identify a component when serving and preparation
overlap without an additional phase boundary or comparator. Such a value is a
proxy or unavailable; it is not silently assigned to `E_prepare`.

Assign high-resolution samples to declared lifecycle phases before applying a
baseline. At minimum, keep source termination grace, capacity/scheduling and
container startup, artifact materialization, runtime session creation, backend
engine construction or cache load, and post-engine readiness separate. Report
the raw sample count and maximum gap for every phase. A lower-resolution wall
series may validate only the total and long phases while a diagnostic rail
series resolves the shorter phases; that evidence asymmetry must remain
visible.

Do not collapse this vector back into one signed number. In particular,
`E_forgone` can make a source-serving-referenced delta negative while
preparation still consumed positive energy above idle.

## Availability

Availability is a different measurement family and unit:

- potential service-capacity gap upper bound in seconds when no external load
  generator is present;
- measured service-capacity gap in seconds only when a declared request path
  observes it; and
- violated or unserved requests under a declared external load.

Availability is never added to joules. An internal inference loop can identify
process-lifecycle boundaries, but without an external request path it provides
only a potential gap upper bound and cannot identify request-level outage or
violation counts.

## Minimal comparison design

Use one declared reference and change one factor at a time:

1. warm only the backend engine cache while keeping the state pair and all
   other protocol fields fixed;
2. re-evaluate retained power against an idle baseline as named sensitivity
   analysis, without pretending it was the original frozen baseline; and
3. move to a state pair/resource contract with `coResidencyCapacity >= 2` to
   enter the MBB regime and identify `E_overlap`.

The first controlled follow-up is the engine-cache toggle because the
diagnostic cold observation placed most elapsed time in first-inference engine
construction.
