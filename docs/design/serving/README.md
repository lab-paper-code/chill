# Serving Design

## Status

`internal/serving` holds the entry point's routing decision. Nothing else here
is implemented.

This document carries decisions and the observations they rest on. Anything not
stated here is not decided — including the object model, the control loops, node
roles, and whether CHILL owns the serving worker or adopts one.

## Question

> A consumer sends inference requests around the clock at a load it does not
> coordinate with us. What must CHILL be, as a Kubernetes-native system, for that
> to work while the cluster runs at the energy-minimal configuration for whatever
> load has arrived?

The scheduler this requires is specified in [`docs/spec/`](../../spec/): routing
weights in `03_routing.md`, configuration selection in
`04_configuration_selection.md`, transition control in `05_transition_control.md`.

## The consumer contract

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

CHILL speaks the Open Inference Protocol, also published as the KServe V2
predict protocol, rather than a wire form of its own. That protocol is
implemented by NVIDIA Triton, TensorFlow Serving, and ONNX Runtime Server, and a
compliant server may offer HTTP/REST, gRPC, or both.

## Why concealment forces routing

Some configuration changes cannot be made underneath live traffic. A power mode
applies to a whole node, and on a memory-constrained device a new model cannot
be resident before the old one leaves.

`03_routing.md` §7 states the consequence: routing is not only how load is
distributed, it is the precondition for other variables being changeable at all,
because the only way to change them without interrupting service is to move a
node's traffic away first.

## What this cluster's network can do

| Observation | Verified against |
|---|---|
| `kube-proxy` runs only on the three control-plane nodes | cluster |
| every `istio-system` workload runs only on the control plane, and no pod on any edge node carries an `istio-proxy` sidecar (0 of 100) | cluster |
| Gateway API CRDs are not installed; the EdgeMesh agent's RBAC covers `networking.istio.io` and not `gateway.networking.k8s.io` | cluster |
| `edgemesh-agent` runs on all ten nodes | cluster |
| EdgeMesh's load balancer implements round-robin, random, and consistent-hash selection, reads `DestinationRule` and not `VirtualService`, and applies no per-backend weight | [`pkg/loadbalancer`](https://github.com/kubeedge/edgemesh/tree/main/pkg/loadbalancer) |
| CHILL already depends on EdgeMesh: the operator labels the evidence-store Service with `service.edgemesh.kubeedge.io/service-proxy-name`, and edge measurement workers reach it on every Run | cluster |

KServe and Knative do not ship a proxy. KServe declares Istio or an
`ingressClassName` as a prerequisite and recommends Gateway API; Knative defines
its own `KIngress` abstraction and ships three interchangeable implementations.
Both assume a networking layer that can weight.

## Where a request branches

EdgeMesh's proxy is a DaemonSet. It intercepts through the iptables rules of the
node the client runs on and selects an endpoint there. No point exists through
which all requests pass.

An external consumer has no such node, so its request enters the cluster
somewhere, and that entry is the one point every external request crosses.

## The entry point

CHILL supplies the entry point, and the entry point distributes.

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

The decision is CHILL's and the transport is EdgeMesh's, which is the division
that already governs power mode.

[`internal/serving`](../../../internal/serving) carries that decision alone:
what the entry point reads from an inference request — the declared model, with
the payload left opaque — and which node's Service receives it, given the
weights. It touches no transport and holds no state.

## What serving would unlock for measurement

`μ_s`, the SLO capacity of a state, is defined against an arrival process: the
largest arrival rate whose steady-state tail latency stays inside the class
target. The current measurement workload is a closed loop with no arrivals, so
it yields throughput at saturation, which is an upper bound on `μ_s` and not
`μ_s`. Every capacity constraint in `03_routing.md` and
`04_configuration_selection.md` is stated in terms of `μ_s`.

## Not yet verified

These bear on the decisions above and have not been demonstrated on this cluster.

- **Cloud-to-edge reachability.** Edge-to-cloud is exercised on every Run. The
  reverse direction, which the entry point needs, has never been demonstrated.
- **Weight propagation delay**, which bounds how fast a routing loop can run.
- **The tunnel hop's cost**, which enters the latency budget of any request whose
  serving node differs from the entry point's node.
