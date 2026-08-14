# Serving Design

## Status

Design only. No scope is approved and nothing is implemented. This document
settles ownership and boundaries so that a later plan can be scoped; it does not
authorise API, controller, RBAC, deployment, or schema change.

Whether CHILL owns the serving worker or adopts an existing one is deliberately
left open in section 7. Everything else here follows from the consumer contract
and from what this cluster's network can actually do.

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

The wire form is not ours to invent. The Open Inference Protocol, also published
as the KServe V2 predict protocol, is implemented by NVIDIA Triton, TensorFlow
Serving, and ONNX Runtime Server, and a compliant server may offer HTTP/REST,
gRPC, or both. CHILL speaks that protocol so that existing clients and
benchmarking tools work unchanged and so that a later comparison against another
serving system is made on equal terms.

**What the contract hides is what shapes the system.** A consumer must not be
able to observe which node served a request, what power mode that node was in,
what batch its request joined, or that the cluster changed configuration while
its request was in flight.

The last of those is load-bearing. Some configuration changes cannot be made
underneath live traffic: a power mode applies to a whole node, and on a
memory-constrained device a new model cannot be resident before the old one
leaves. Concealing such a change therefore requires moving a node's traffic away
first, changing it while it is empty, and returning traffic afterwards. Routing
is not only how load is distributed; it is the precondition for every other knob
being usable at all (`03_routing.md` §7).

## 2. Node roles

Every node CHILL knows about is in exactly one role at any moment.

| Role | Meaning |
|---|---|
| **Serving** | Carries consumer traffic. Its state is part of the current configuration. |
| **Profiling** | Running a `ProfilingRun`. Carries no consumer traffic. |
| **Inactive** | Powered on and Kubernetes-Ready, carrying neither. Costs `P_standby`. |

The roles are exclusive by necessity rather than by policy. A measurement is
only valid if nothing else runs on the node, and the fail-closed workload-safety
check already refuses to mutate a node occupied by a foreign workload. Consumer
traffic is a foreign workload to a measurement, and a measurement is a foreign
workload to consumer traffic.

Profiling is therefore a consumer of the same drain mechanism transitions use:
to profile a serving node, first move its traffic away.

## 3. What this cluster's network can do

The design cannot be read off the prevailing convention, because the convention
assumes a network this cluster does not have.

| Fact | Consequence |
|---|---|
| `kube-proxy` runs only on the three control-plane nodes | Edge nodes have no standard service networking |
| Every `istio-system` workload runs only on the control plane, and no pod on any edge node carries an `istio-proxy` sidecar | The service mesh does not extend to the edge |
| Gateway API CRDs are not installed, and the EdgeMesh agent has no permission for `gateway.networking.k8s.io` | `HTTPRoute` is not a usable interface here |
| `edgemesh-agent` runs on all ten nodes and reads `networking.istio.io` | EdgeMesh is the one data plane present everywhere |

EdgeMesh is also the only one that spans the two halves. CHILL already depends
on it: the operator labels the evidence-store Service with
`service.edgemesh.kubeedge.io/service-proxy-name`, and measurement workers on
edge nodes reach that Service through the LibP2P tunnel on every Run.

What EdgeMesh does not do is weight. Its load balancer implements round-robin,
random, and consistent-hash selection; it reads `DestinationRule` and not
`VirtualService`; and it treats every endpoint of a Service as equal. There is
no per-backend weight anywhere in that path.

That matters because `w`, the weight vector, is the output of `03_routing.md`
and the input to everything above it, including the drain that makes transitions
invisible to a consumer.

## 4. Where a request branches

EdgeMesh's proxy is a DaemonSet, not a central gateway. It intercepts through
the iptables rules of **the node the client runs on**, and selects an endpoint
there. There is no point through which all requests pass, so there is no place
to observe total arrival rate and no single place a weight could be applied.

An external consumer has no such node. Its request must enter the cluster
somewhere, and that entry is the one point every external request crosses.

## 5. The entry point

CHILL supplies the entry point, and the entry point distributes.

This is a departure from how serving platforms usually work, and the reason is
environmental rather than architectural: KServe and Knative do not ship a proxy
because their environments already provide one that can weight. This one does
not, and the entry point has to exist regardless for external consumers, so the
distribution decision is placed where it is already unavoidable.

### Reaching a chosen backend

Choosing a backend and reaching it pull in opposite directions. Calling a shared
Service hands the choice to EdgeMesh, which ignores weight; calling a pod
address directly does not resolve across the tunnel, which is why EdgeMesh
exists.

Both are satisfied by giving each serving node its own Service with a single
endpoint. A Service with one endpoint has no selection left to make, so
EdgeMesh's policy becomes irrelevant to the outcome and its transport still
applies.

```
entry point ── weighted choice of Service ──▶ chill-serving-<node>
                                                    │  EdgeMesh tunnel
                                                    ▼
                                              the serving pod
```

The decision is CHILL's; the transport is EdgeMesh's. That is the same division
that already governs power modes and batch size.

### Responsibilities

The entry point terminates the Open Inference Protocol for external consumers,
holds the weight table the optimizer writes, chooses a backend for each request
so that a zero weight means no request rather than few, forwards to the chosen
node's Service, and counts both arrivals and outstanding requests per backend.

The last two are why drain becomes decidable: a backend is drained when its
weight is zero and its outstanding count reaches zero. Both are the entry
point's own state rather than a statistic scraped from someone else's proxy.

It does not batch, load a model, or run inference.

## 6. What CHILL ships and what it depends on

Ownership follows meaning, not resource type. An object whose contents are
CHILL's decision is CHILL's object even when its schema belongs to someone else.

| Shipped in `chill-system` | Depended on, declared, not shipped |
|---|---|
| the entry point | EdgeMesh and its tunnel |
| serving workers | Kubernetes |
| per-node serving Services | the node power-metric exporter in `monitoring` |
| the custom resources of section 8 | the container registry |
| the optimizer and transition controllers | |

A declared dependency that is absent produces an explicit unavailable status
rather than degraded behaviour, which is the pattern `ChillSystem` already
follows for its components.

One obligation is ours alone and has no counterpart in the platforms above: a
weight that has been written must be confirmed to have taken effect. An
optimizer that believes an unexecuted decision will keep optimising against a
cluster that is not in the state it thinks. This is the same reason a
`NodeActuation` proves its target by independent read-back rather than by the
success of the command.

## 7. The serving worker — open question

The specification names CHILL's identity as the Profiler, the optimizer, and
desired/current separated actuation with a drain orchestrator
(`spec_v0.2_plain.md` §4.2). A model runtime is not among them, which argues for
adopting one.

Against that, CHILL already owns a process that loads a model and runs batched
inference — the measurement worker — and the deciding axis is narrower than
general capability:

> **Does a measured curve still describe the system that serves?**

Every layer above `03_routing.md` consumes `ε(b)` and `μ_s`. If the serving path
is not the measured path, the envelope is computed from numbers that do not
apply and the optimizer inherits that error with no signal that it happened.
Extending the measurement worker keeps the two paths identical and keeps `b*` an
exact CHILL decision, at the cost of writing a queue with deadline semantics.
Adopting a serving system inherits a mature queue and gives up both.

The decision is not taken here.

## 8. The object model

Two custom resources already exist as empty declarations and are exactly the two
artefacts this system needs.

| Resource | What it becomes | Produced by | Consumed by |
|---|---|---|---|
| `DeviceProfile` | the measured facts for one device type in one state: `ε(b)`, `P_idle`, `P_standby`, `μ_s` | the profiling path | envelope computation |
| `ClusterEnergyModel` | the offline result: the active configuration set and the load thresholds between them | envelope computation | the configuration loop |

Two more are needed and do not exist: a consumer-facing serving declaration that
names the model and SLO class and yields the endpoint that answers it, and a
desired-configuration record separated from the configuration currently
realised, per `spec_v0.2_plain.md` §4.1. That separation is not bookkeeping —
without it an optimizer reading only current state mistakes a node mid-transition
for a degraded node and reacts to its own action.

## 9. The control loops

| Loop | Period | Decides | Cost of being wrong |
|---|---|---|---|
| **Serving** | per request | which queued requests form the next batch | one request's latency |
| **Routing** | seconds | the weight table, and each node's batch size | recomputed immediately |
| **Configuration** | slow, hysteresis-gated | which configuration the cluster should be in, and whether to move there now | a transition, paid in time and energy |

The separation is the specification's own principle: variables cheap to change
and cheap to solve are solved often and on the inside; variables expensive to
change are solved rarely and on the outside (`03_routing.md` §6).

The configuration loop does not act directly. It records a desired
configuration, and the transition path realises it: drain the affected nodes by
reweighting, change their state through the existing actuation boundary, and
return them to service.

## 10. What serving unlocks for measurement

`μ_s`, the SLO capacity of a state, is defined against an arrival process: the
largest arrival rate whose steady-state tail latency stays inside the class
target. The current measurement workload is a closed loop with no arrivals, so
it yields throughput at saturation, which is an upper bound on `μ_s` and not
`μ_s` itself. Every capacity constraint in `03_routing.md` and
`04_configuration_selection.md` is stated in terms of `μ_s`.

A serving path with real arrivals makes `μ_s` measurable. Until one exists, the
cluster-capacity constraint is approximated by a quantity that ignores latency.

## 11. Costs and unverified assumptions

**The entry point is a single point.** If it stops, serving stops. Acceptable
for a research prototype and recorded rather than mitigated.

**A tunnel hop is in the latency path.** Any request whose serving node differs
from the entry point's node crosses the LibP2P tunnel. That is a property of
this architecture and belongs in the SLO class budget as a measured quantity,
not an ignored one. Placing the entry point on a control-plane node keeps it off
the devices under measurement, at the price of putting the hop in front of every
edge-served request.

**Cloud-to-edge reachability is unverified.** Edge-to-cloud is exercised on every
Run, since edge measurement workers reach the evidence store. The reverse
direction, which the entry point needs, has not been demonstrated.

**Weight resolution is bounded by what the entry point can enforce**, not by
EdgeMesh. Placing distribution at the entry point is what keeps `w` a continuous
decision rather than one quantised to replica counts.

## 12. Deferred

- More than one SLO class. The extension is mechanical (`03_routing.md` §5) but
  each dimension multiplies the configuration space.
- The model knob. Configuration is defined over node count, model, and power
  mode; a first system may hold the model fixed and still exercise the whole
  scheduler.
- The concave routing path. `03_routing.md` branches on `κ`; which branch
  applies is a measured question and the measurement does not exist yet.
- Individual-node selection within a type, which the specification delegates to
  a second-order rule.
- gRPC. The protocol is compliant with either transport, and HTTP alone is
  enough until serialisation cost is shown to matter.
- Reboot-class transitions, which remain outside the actuation contract.
