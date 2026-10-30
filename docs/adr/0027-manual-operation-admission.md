# 0027 Live Manual Admission and Confirmed Reservation Release

## Status

Accepted on 2026-10-26. This refines the admission and release boundary of [0023 Resource Ledger](0023-resource-ledger.md); it does not introduce a second manual-operation reservation system.

## Context

Manual operations can complete between scheduler Ticks. Reusing the last Tick snapshot then tests a new operation against a state that never represented its actual predecessor's completed policy. After a successful PLC route, a SmolVLM2 route was rejected with `preserved distribution changed`; retry later succeeded. The operating record contains **two real refusals** caused by stale admission state, motivating a live-state fix rather than a retry instruction to operators.

A separate incident exposed the other half of ownership. When rasp5 became NodeNotReady, manual MobileNet routing failed partially with `policy target physical executor changed`. The refusal itself was correct: an unready executor must not receive policy. However, the terminated operation's reservation never became releasable while Pods remained. Subsequent route, release and reset calls all returned **409**, with six physical instances retained and no load running. The observation did not establish why the Node became unready.

Keeping unknown effects reserved is required for safety. Keeping a confirmed, terminated failure reserved forever is not. The original scoped-reservation contract lacked both live effect reconfirmation and bounded reset ownership handoff; restarting merely cleared in-memory reservations, not the design defect.

## Decision

Admit a manual change against **current state immediately before admission**, not `c.state.Snapshot` from the last Tick. Under existing Tick serialization, capture the same live observer/runtime/Node inputs used by Tick, determine the operation's entire affected scope, reserve it atomically, and recheck physical safety at execution boundaries. Do not publish a synthetic scheduler decision. Reset may reject invalid structure/scope before discovery, but its actual plan and Node admission still use fresh state.

Separate execution termination from reservation release. A fresh observation after a failed/partial manual routing operation may release its reservation while Pods remain only if it proves:

- The exact reserved Pod UID and container/physical identity, on the same Node UID.
- Pod/native readiness and a successful observation newer than operation end.
- Either the requested policy is acknowledged, or the recorded pre-operation native policy is unchanged. The latter is confirmed state, not invented success or an acknowledgment of the failed request.

Keep reservations on policy mismatch, unready state, stale observation or executor replacement. The existing release path after confirmed Pod absence and no pending power transition remains. Work execution ending or timing out alone is no proof.

Allow reset to **atomically inherit** a terminated owner's reservation only when its entire responsibility fits the admitted reset Workloads and exact Nodes in `operation.Nodes`. Reject active owners, measurement owners and any out-of-scope Node or Workload. Do not treat an expanded historical conflict snapshot as extra Node-cleanup permission, and do not release the old owner before acquiring reset.

Authoritative detail: [scheduler implementation §5.4](../design/serving/scheduler-implementation.md#54-자원-장부--sch-5052-ac-3739), [scheduler operations](../design/serving/scheduler-operations.md) and [scheduler SRS](../spec/scheduler/SRS.md), SCH-51 / AC-38. Current implementation anchors are `internal/serving/kube/measurement_sequence.go` (`ExecuteManual`), `manual_reset.go` (fresh reset planning), and `reservation.go` (`reserveRun`, `releaseObservedScopes`, `manualRoutingEffectConfirmed`).

## Consequences

A recently completed manual operation is immediately visible to the next admission. Operators need not wait for a Tick or restart to escape a stale guard. Confirmed failure effects can release ownership without deleting healthy Pods; uncertainty still blocks only the affected scope, not all planning.

Known limitation: `release` did not record `Before.Native`, so its unchanged-policy fallback cannot prove release while the Pod remains. Requested-policy proof, when available, and reset/physical cleanup remain distinct paths. This pre-existing limit is not permission to manufacture prior state or drop reservations blindly.

Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/checkpoints.md`, entries `ReservationFix27`, the revision-82 stale routing refusal, and the revision-84 live-capture admission fix. Detailed operational transcripts and transient recovery logs are intentionally not reproduced.
