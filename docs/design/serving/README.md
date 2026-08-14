# Serving Design

## Status

Design only. Nothing here is implemented and no scope is approved.

Each section below is marked for what kind of statement it makes.

- **Observed** — verified against this cluster, the specification, or published
  documentation of another system. Cited where it is not reproducible from the
  repository.
- **Decided** — settled and not reopened without a reason.
- **Open** — proposed or unresolved. Nothing downstream should be built on an
  open item as though it were settled.

## Question

> A consumer sends inference requests around the clock at a load it does not
> coordinate with us. What must CHILL be, as a Kubernetes-native system, for that
> to work while the cluster runs at the energy-minimal configuration for whatever
> load has arrived?

The scheduler this requires is specified in [`docs/spec/`](../../spec/): routing
weights in `03_routing.md`, configuration selection in
`04_configuration_selection.md`, and transition control in
`05_transition_control.md`. This document does not restate those rules.

## 1. The consumer contract — Decided

CHILL accepts inference requests for a declared model at a declared SLO class
and returns each result within that class's latency target, at whatever load
arrives.

| The consumer supplies | The consumer receives |
|---|---|
| model identity | the inference result |
| SLO class | delivery within that class's latency target |
| request payload | nothing about how it was served |

A consumer does not observe which node served a request, what power mode that
node was in, what batch its request joined, or that the cluster changed
configuration while its request was in flight.

**Wire form — Decided.** CHILL speaks the Open Inference Protocol, also
published as the KServe V2 predict protocol, rather than one of its own.

*Observed:* the protocol is implemented by NVIDIA Triton, TensorFlow Serving,
and ONNX Runtime Server, and a compliant server may offer HTTP/REST, gRPC, or
both.

*Open:* whether HTTP alone is sufficient for the first system, or gRPC is needed
from the start.

## 2. The consequence of concealment — Observed

Some configuration changes cannot be made underneath live traffic. A power mode
applies to a whole node, and on a memory-constrained device a new model cannot
be resident before the old one leaves.

`03_routing.md` §7 states the consequence: routing is not only how load is
distributed, it is the precondition for other variables being changeable at all,
because the only way to change them without interrupting service is to move a
node's traffic away first.

## 3. Node roles — Open

A node might be required to be in exactly one of these at a time.

| Role | Meaning |
|---|---|
| Serving | carries consumer traffic |
| Profiling | running a `ProfilingRun`, carrying no consumer traffic |
| Inactive | powered on and Ready, carrying neither |

*Observed:* the fail-closed workload-safety check already refuses to mutate a
node occupied by a workload outside the configured allowed namespaces, and a
measurement's admission requires that no foreign workload polluted its window.

*Open:* whether that is enough, or whether an explicit role with a controller
owning transitions between roles is needed. Nothing has been decided, and CHILL
has no notion of a node role today.

## 4. What this cluster's network can do — Observed

| Fact | Where verified |
|---|---|
| `kube-proxy` runs only on the three control-plane nodes | cluster |
| every `istio-system` workload runs only on the control plane, and no pod on any edge node carries an `istio-proxy` sidecar (0 of 100) | cluster |
| Gateway API CRDs are not installed; the EdgeMesh agent's RBAC covers `networking.istio.io` and not `gateway.networking.k8s.io` | cluster |
| `edgemesh-agent` runs on all ten nodes | cluster |
| EdgeMesh's load balancer implements round-robin, random, and consistent-hash selection, reads `DestinationRule` and not `VirtualService`, and applies no per-backend weight | [`pkg/loadbalancer`](https://github.com/kubeedge/edgemesh/tree/main/pkg/loadbalancer) |
| CHILL already depends on EdgeMesh: the operator labels the evidence-store Service with `service.edgemesh.kubeedge.io/service-proxy-name`, and edge measurement workers reach it on every Run | cluster |

*Observed, prior art:* KServe and Knative do not ship a proxy. KServe declares
Istio or an `ingressClassName` as a prerequisite and recommends Gateway API;
Knative defines its own `KIngress` abstraction and ships three interchangeable
implementations. Both assume a networking layer that can weight.

## 5. Where a request branches — Observed

EdgeMesh's proxy is a DaemonSet. It intercepts through the iptables rules of the
node the client runs on and selects an endpoint there. No point exists through
which all requests pass.

An external consumer has no such node, so its request enters the cluster
somewhere, and that entry is the one point every external request crosses.

## 6. The entry point — Decided

CHILL supplies the entry point, and the entry point distributes.

### Reaching a chosen backend — Decided

Choosing a backend and reaching it pull in opposite directions: calling a shared
Service hands the choice to EdgeMesh, which applies no weight, and calling a pod
address directly does not resolve across the tunnel.

Each serving node gets its own Service with a single endpoint. A Service with
one endpoint leaves EdgeMesh nothing to select, so its transport applies and its
policy does not.

```
entry point ── weighted choice of Service ──▶ chill-serving-<node>
                                                    │  EdgeMesh tunnel
                                                    ▼
                                              the serving pod
```

### Responsibilities — Open

Proposed, not settled: terminate the protocol for external consumers; hold a
weight table written by the optimizer; choose a backend per request such that a
zero weight yields no request rather than few; forward to the chosen node's
Service; count arrivals and outstanding requests per backend.

The last of these is what would make drain decidable — a backend is drained when
its weight is zero and its outstanding count reaches zero — but the drain
protocol itself is not designed here.

*Open:* whether the entry point batches, or whether batching belongs entirely to
the serving worker.

### Placement — Open

The entry point could run on a control-plane node or on an edge node. A
control-plane placement keeps it off the devices under measurement; an edge
placement removes the tunnel hop for requests served by that same node. Neither
has been chosen, and the latency consequence of each is unmeasured.

## 7. The serving worker — Open

`spec_v0.2_plain.md` §4.2 names CHILL's identity as the Profiler, the optimizer,
and desired/current separated actuation with a drain orchestrator. A model
runtime is not among them.

CHILL nonetheless already owns a process that loads a model and runs batched
inference — the measurement worker. The axis on which this turns:

> Does a measured curve still describe the system that serves?

Every layer above `03_routing.md` consumes `ε(b)` and `μ_s`. If the serving path
is not the measured path, the envelope is computed from numbers taken elsewhere.
Extending the measurement worker keeps the two paths identical and keeps `b*` an
exact CHILL decision, at the cost of writing a queue with deadline semantics.
Adopting a serving system inherits a mature queue and gives up both.

Not decided.

## 8. What is shipped and what is depended on — Open

A proposed division, not settled:

| Proposed as shipped in `chill-system` | Proposed as depended on and declared |
|---|---|
| the entry point | EdgeMesh and its tunnel |
| serving workers | Kubernetes |
| per-node serving Services | the node power-metric exporter in `monitoring` |
| the custom resources of section 9 | the container registry |
| the optimizer and transition controllers | |

*Observed:* `ChillSystem` already reports an explicit unavailable status for a
component that is not present, rather than degrading silently.

*Open:* whether a written weight must be confirmed to have taken effect before
the optimizer treats it as applied. `NodeActuation` proves its target by
independent read-back rather than by command success; whether routing needs the
same has not been decided.

## 9. The object model — Open

`DeviceProfile` and `ClusterEnergyModel` exist today as empty declarations
(`Spec struct{}` / `Status struct{}`).

Proposed: `DeviceProfile` carries the measured facts for one device type in one
state — `ε(b)`, `P_idle`, `P_standby`, `μ_s` — and `ClusterEnergyModel` carries
the offline envelope result, the active configuration set and the load
thresholds between them.

Also proposed, and not existing: a consumer-facing serving declaration naming
model and SLO class, and a desired-configuration record separated from the
configuration currently realised.

*Observed:* `spec_v0.2_plain.md` §4.1 states the failure that desired/current
separation prevents — an optimizer that reads only current state treats a node
mid-transition as a degraded node and manipulates further variables in response.

## 10. Control loops — Open

A proposed decomposition. The periods are not derived from any measurement and
the middle loop's period in particular depends on how fast a weight change
propagates in this cluster, which is unmeasured.

| Loop | Proposed period | Would decide |
|---|---|---|
| Serving | per request | which queued requests form the next batch |
| Routing | seconds | the weight table, and each node's batch size |
| Configuration | slow, hysteresis-gated | which configuration to be in, and whether to move now |

*Observed:* `03_routing.md` §6 gives the principle the decomposition follows —
variables cheap to change and cheap to solve are solved often and on the inside;
variables expensive to change are solved rarely and on the outside.

## 11. What serving would unlock for measurement — Observed

`μ_s`, the SLO capacity of a state, is defined against an arrival process: the
largest arrival rate whose steady-state tail latency stays inside the class
target. The current measurement workload is a closed loop with no arrivals, so
it yields throughput at saturation, which is an upper bound on `μ_s` and not
`μ_s`. Every capacity constraint in `03_routing.md` and
`04_configuration_selection.md` is stated in terms of `μ_s`.

## 12. Unresolved risks

Stated without a verdict on whether they are acceptable.

- **A single entry point.** If it stops, serving stops.
- **A tunnel hop in the latency path.** Any request whose serving node differs
  from the entry point's node crosses the LibP2P tunnel. Its cost is unmeasured.
- **Cloud-to-edge reachability is unverified.** Edge-to-cloud is exercised on
  every Run. The reverse direction, which the entry point needs, has never been
  demonstrated on this cluster.
- **Weight propagation delay is unmeasured**, and it bounds how fast the routing
  loop can usefully run.

## 13. Deferred

- More than one SLO class.
- The model knob within a configuration.
- The concave routing path; `03_routing.md` branches on `κ`, and which branch
  applies here is unmeasured.
- Individual-node selection within a type, which the specification delegates to
  a second-order rule.
- Reboot-class transitions, which remain outside the actuation contract.
