# 0023 Resource Ledger and Scoped Reservations

## Status

Accepted by the user on 2026-10-26: option B (the resource ledger before the
third demonstration). This records the rationale, not a replacement specification.

## Context

The second end-to-end demonstration exposed a chain of incorrect implications:
a routing-policy acknowledgment (ACK) still pending was reported as an error;
the observer removed the instance from its snapshot; the planner prepared the
same slot on the same CPU again. Two physical Pods then violated exclusive CPU
allocation. The shared safety check stopped every decision, including unrelated
MobileNet expansion: phase B success was **0.67% (243/36,112)**.
See [the incident](../postmortems/0002-demo2-duplicate-preparation.md).

Approximately 25 structural findings were grouped into five concerns: knowledge
state, writing and authority, execution ownership and admission, fault isolation,
and control dynamics. They reduced to four recurring design habits:

| Previous habit | Required replacement |
|---|---|
| The visible observation list is the physical world | Keep physical inventory separate from observation quality and age. |
| Every operation kind has its own permission rules | Reserve the complete affected Node, Workload and physical-instance scope through one admission path. |
| Any unfinished transition stops all planning | Freeze only reserved scopes and continue outside them. |
| Safety and energy share one decision tempo | Separate fast allocation/expansion from slow reduction/replacement; decided separately in [0025](0025-two-tempo-control.md). |

The first three changes must land together, in inventory → reservations → scoped
planning order. Removing the barrier first would expose concurrent operations
without common ownership. The first demonstration had already waited in 32 of
36 phase-B decisions behind a transition, so this was also a demonstration issue.

External research supplied boundary models, not a new dependency:

- [OPC UA DataValue](https://reference.opcfoundation.org/specs/OPC-10000-4/7.11)
  carries value, status and timestamps: unknown is not absent.
- [Envoy xDS ACK/NACK semantics](https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol#ack-and-nack-semantics-summary)
  distinguish sent, accepted and rejected configuration; pending is not deletion.
- [Kubernetes scheduler Reserve/Unreserve](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/#reserve)
  separates serialized planning from concurrent execution and explicit ownership.
- [controller-runtime reconciliation](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.19.0/pkg/reconcile/reconcile.go)
  revisits current state per target rather than making one slow target a global wait.

Rejected alternatives:

- Local patches only: they interrupted the immediate duplicate-prepare path but
  retained global waiting and the same class of hidden-inventory failures.
- Replace everything before demonstration three: unnecessarily included control
  policy changes requiring separate approval and delayed the critical demonstration.
- New services, stores, custom resource definitions (CRDs), or a workflow engine:
  existing Pods, finalizers, run records and operation identities already suffice.

## Decision

Use the existing observer source map as a **Pod UID-keyed physical ledger**.
Runtime and scheduler snapshots are projections, not independent inventories.
Observation failure preserves identity, allocation, ownership and last confirmed
state while freshness and identity checks separately decide dispatch eligibility.
Only confirmed executor termination and required Pod cleanup retire an entry.

Use existing action runs to reserve the affected Nodes, Workload UIDs, Pod UIDs
and physical executors. Admit, detect conflicts and release using that same scope.
Retain reservations after an uncertain remote effect, not merely until a goroutine
ends. Keep one global planner; freeze reserved scopes and evaluate independent ones.
Ready-subset routing inside an owner remains distinct from independent replanning.

The authoritative contract is [scheduler implementation §5.4](../design/serving/scheduler-implementation.md#54-자원-장부--sch-5052-ac-3739)
and [scheduler SRS](../spec/scheduler/SRS.md), SCH-50–52 / AC-37–39.
Current implementation anchors are `internal/serving/kube/observer.go`
(`restoreSources`, `publishLedger`, `verifySource`), `reservation.go`
(`reserveRun`, `releaseObservedScopes`), and `scheduler.go` (`reconcileSnapshot`).

## Consequences

Independent expansion no longer waits for an unrelated slow transition. Unknown
physical state remains occupied, rather than becoming fictitious spare capacity.
This does not waive actual shared safety failures such as exclusive CPU overlap.

The cutover exposed additional defects, subsequently addressed:

- Early capacity completion could monopolize a bootstrap Node; a worst-value-only
  comparison hid other improvements. See [0024](0024-candidate-comparison-and-bootstrap.md).
- Refresh initially erased Ready before probes completed; retain confirmed Ready
  until an actual outcome invalidates it, including during partial publication.
- A late probe/log follower could resurrect a retired source; source creation now
  belongs to Pod-lifecycle observation and late publication rechecks identity.
- Per-probe namespace-wide Pod listing caused a large performance regression;
  exact UID-label selection fixed it. See [0004](../postmortems/0004-gateway-pod-list-regression.md).

Limits remain: reservations conservatively cover whole Nodes; an existing owner's
frozen target cannot yet grow onto newly released Nodes. Bootstrap timeout does
not prevent later Node monopolization. Tick reentry and sequential native ACKs
can still add delay. Policy ConfigMap single-authority cleanup and moving cleanup
responsibility to finalizer reconciliation remain after evaluation scenario E5.

Raw rationale: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/structure-redesign-20261026.md`
and `audit-20261024/structure-20261026/{clusters,Research-K1,Research-K2,Research-K3,Research-K4,Research-K5}.md`
under that same operations root; large audit payloads are not copied here.
