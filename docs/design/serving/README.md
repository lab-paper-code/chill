# Serving Design

## Status

Design only. No scope is approved and nothing is implemented. This document
settles ownership and boundaries so that a later plan can be scoped; it does not
authorise API, controller, RBAC, deployment, or schema change.

The serving-runtime ownership question in section 3 is deliberately left open.
Both paths are described so the choice can be made on stated grounds.

## Question

> A consumer sends inference requests around the clock at a load it does not
> coordinate with us. What must CHILL be, as a Kubernetes-native system, for that
> to work while the cluster runs at the energy-minimal configuration for whatever
> load has arrived?

The scheduler this requires is specified in [`docs/spec/`](../../spec/): routing
weights in `03_routing.md`, configuration selection in
`04_configuration_selection.md`, and transition control in
`05_transition_control.md`. This document does not restate those rules. It
describes the system that runs them.

## 1. The consumer contract

CHILL accepts inference requests for a declared model at a declared SLO class,
and returns each result within that class's latency target, at whatever load
arrives.

| The consumer supplies | The consumer receives |
|---|---|
| model identity | the inference result |
| SLO class | delivery within that class's latency target |
| request payload | nothing about how it was served |

**What the contract hides is what shapes the system.** A consumer must not be
able to observe which node served a request, what power mode that node was in,
what batch its request joined, or that the cluster changed configuration while
its request was in flight.

The last of those is the load-bearing one. Configuration changes are not
instantaneous and some of them cannot be made underneath live traffic: a power
mode applies to a whole node, and on a memory-constrained device a new model
cannot be resident before the old one leaves. Concealing such a change from the
consumer therefore requires moving that node's traffic elsewhere first, changing
it while it is empty, and returning traffic afterwards. Routing is not only how
load is distributed; it is the precondition for every other knob being usable at
all (`03_routing.md` §7).

## 2. What the cluster looks like from inside

Every node CHILL knows about is in exactly one role at any moment.

| Role | Meaning |
|---|---|
| **Serving** | Carries consumer traffic. Its state is part of the current configuration. |
| **Profiling** | Running a `ProfilingRun`. Carries no consumer traffic. |
| **Inactive** | Powered on and Kubernetes-Ready, carrying neither. Costs `P_standby`. |

The roles are exclusive by necessity rather than by policy. A profiling
measurement is only valid if nothing else runs on the node, and the existing
fail-closed workload-safety check already refuses to mutate a node occupied by a
foreign workload. Consumer traffic is a foreign workload to a measurement, and a
measurement is a foreign workload to consumer traffic.

This makes profiling a consumer of the same drain mechanism that transitions
use: to profile a serving node, first move its traffic away.

## 3. What CHILL owns — open question

The specification names CHILL's identity as three things
(`spec_v0.2_plain.md` §4.2): the Profiler, the optimizer, and desired/current
separated actuation with a drain orchestrator. A model runtime is not among
them.

That still leaves the serving worker itself undecided, because CHILL already
owns a process that loads a model and runs batched inference — the measurement
worker. The question is whether that process grows a queue and an endpoint, or
whether an existing serving system is adopted beside it.

### Path A — extend the measurement worker

| | |
|---|---|
| CHILL writes | request intake, queue, batch-formation boundary, SLO deadline handling |
| CHILL reuses | model load, runtime session, batched execution, the existing image and its frozen runtime identity |
| Batch control | exact. `b*` is a CHILL decision applied directly |
| Measurement transfer | the profiled execution path and the serving execution path are the same code, so a measured `ε(b)` describes the serving node |
| Principal risk | a queue with deadline semantics is a real component and is easy to get subtly wrong |

### Path B — adopt an existing serving system

| | |
|---|---|
| CHILL writes | an adapter for placement, weights, and lifecycle |
| CHILL reuses | a mature queue, batching, and endpoint |
| Batch control | indirect. The adopted system's batching policy has to be steered into agreeing with `b*(λ)` from `01_batch_size.md` |
| Measurement transfer | weaker. The profiled path is the CHILL worker; the serving path is the adopted runtime. `ε(b)` measured on one describes the other only if they execute identically |
| Principal risk | the two batching policies never fully reconcile, and the measured curves stop predicting serving behaviour |

### How to decide

The axis that matters is **whether a measured curve still describes the system
that serves**. Every layer above `03_routing.md` consumes `ε(b)` and `μ_s`. If
the serving path is not the measured path, the envelope is computed from numbers
that do not apply, and the optimizer's decisions inherit that error without any
signal that it happened.

That argues for Path A unless the queue-and-deadline component proves larger
than it looks. The decision is not taken here.

## 4. The object model

Two custom resources already exist as empty declarations and are exactly the two
artefacts this system needs.

| Resource | What it becomes | Produced by | Consumed by |
|---|---|---|---|
| `DeviceProfile` | the measured facts for one device type in one state: `ε(b)`, `P_idle`, `P_standby`, `μ_s` | the profiling path | envelope computation |
| `ClusterEnergyModel` | the offline result: the active configuration set and the load thresholds between them | envelope computation | the configuration loop |

Two more are needed and do not exist.

| Resource | Role |
|---|---|
| a consumer-facing serving declaration | the request that CHILL serve a model at an SLO class, and the stable endpoint that answers it |
| a desired-configuration record | the configuration the optimizer has chosen, separated from the configuration currently realised, per `spec_v0.2_plain.md` §4.1 |

The desired/current separation is not bookkeeping. Without it an optimizer
reading only current state mistakes a node mid-transition for a degraded node
and reacts to its own action.

## 5. The control loops

Three loops at three periods, each cheap enough for its period.

| Loop | Period | Decides | Cost of being wrong |
|---|---|---|---|
| **Serving** | per request | which queued requests form the next batch | one request's latency |
| **Routing** | seconds | the weight vector across serving nodes, and each node's batch size | recomputed immediately; weights cost nothing to change |
| **Configuration** | slow, hysteresis-gated | which configuration the cluster should be in, and whether to move there now | a transition, paid in time and energy |

The separation is the specification's own principle: variables that are cheap to
change and cheap to solve are solved often and on the inside; variables that are
expensive to change are solved rarely and on the outside (`03_routing.md` §6).

The configuration loop does not act directly. It records a desired
configuration, and the transition path realises it: drain the affected nodes by
reweighting, change their state through the existing actuation boundary, and
return them to service. That path is where `05_transition_control.md`'s decision
to follow or hold the envelope is enforced.

## 6. What serving unlocks for measurement

Serving is not only the application. It is the instrument for a quantity
profiling cannot produce.

`μ_s`, the SLO capacity of a state, is defined against an arrival process: the
largest arrival rate whose steady-state tail latency stays inside the class
target. The current measurement workload is a closed loop with no arrivals, so
it yields throughput at saturation, which is an upper bound on `μ_s` and not
`μ_s` itself. Every capacity constraint in `03_routing.md` and
`04_configuration_selection.md` is stated in terms of `μ_s`.

A serving path with real arrivals makes `μ_s` measurable. Until one exists, the
cluster-capacity constraint is being approximated by a quantity that ignores
latency.

## 7. Deferred

Named so that their absence is a decision rather than an oversight.

- More than one SLO class. The extension is mechanical (`03_routing.md` §5) but
  every added dimension multiplies the configuration space.
- The model knob. Configuration is defined over node count, model, and power
  mode; a first system may hold the model fixed and still exercise the whole
  scheduler.
- The concave routing path. `03_routing.md` branches on `κ`; which branch
  applies is a measured question and the measurement does not exist yet.
- Individual-node selection within a type. The specification assigns counts per
  type and delegates which instance to a second-order rule.
- Anything about reboot-class transitions, which remain outside the actuation
  contract.
