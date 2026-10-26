# 0026 Finished Measurement Jobs, Pods and Retained Evidence

## Status

Accepted by the user on 2026-10-26. New finalized measurements use evidence-first
Job cleanup. Existing successful Jobs remain; only approved finished Pods were
removed. This is not permission for a retrospective Job sweep.

## Context

The gateway regression investigation found **1,053 namespace Pods**, including
**981 Succeeded Pods**, being decoded repeatedly during instance verification.
The [selector fix](../postmortems/0004-gateway-pod-list-regression.md) removed that
hot-path cost, but completed measurement executions still accumulated without
automatic cleanup. Resource retention needed a lifecycle fix, not only a faster
reader or a larger ChillServer.

A completed measurement has two different durable facts: the execution finished,
and its immutable evidence was verified and recorded. Previously, losing the
recorded Job could turn a completed Run into `ExecutionLost`. Kubernetes TTL set
at Job creation could delete execution material before evidence finalization.

The proposed retrospective cleanup was also unsafe. Inventory found **1,012
successful Jobs** with verified evidence and stored objects, but older Runs did
not retain the Evidence Store connection binding. Their retained loader still
recovered that binding from the exact owned Job. Object existence alone did not
prove that deleting the Job preserved the reader's path to those objects.

## Decision

Once independently verified execution evidence and its reference are recorded,
a Run remains complete without a live Job. Preserve recorded Job, Pod and Node
identities for evidence validation; do not clear those identities on cleanup.

At new finalization, record the non-secret Evidence Store connection binding and
`jobCleanup: Pending` in the same Run status update as verified evidence. Only
then delete a successful measurement Job with **Background propagation** and
an exact UID precondition. Record `Done` and retry interrupted cleanup through
existing reconciliation. Retained readers use the frozen Run binding, not a
current declaration that may have changed.

Apply the same evidence-first rule to power-transition measurement attempts.
Keep Evidence Store initialization Jobs outside this measurement cleanup policy.

Failed Jobs remain for diagnosis. Configure retention through
`profiler.failedJobRetentionSeconds`, default **86400 s**, inclusive range
**1..604800 s**. Patch their TTL only after the final outcome has been recorded;
**never set TTL at measurement Job creation**. Rejected/non-accepted transition
observations follow diagnostic retention rather than successful cleanup.

Do not retrofit cleanup markers or diagnostic TTL onto existing markerless Runs
and terminal transition evidence. Preserve their legacy Job-based read path.
The user explicitly deferred deletion of the 1,012 successful legacy Jobs and
approved deletion of **Succeeded Pods only**, with UID/resourceVersion conditions.
Do not invoke measurement-object deletion APIs or alter stored evidence for this.

Authoritative lifecycle: [profiling design](../design/profiling/README.md).
Current implementation anchors:

- `internal/profiling/profiler/controller/run_reconciler.go`:
  `publishFinalizedEvidence`, `reconcileTerminalRun` and cleanup status handling.
- `internal/profiling/profiler/evidence/kube/retained_loader.go:loaderFor`:
  frozen Run binding, falling back to the exact Job for old Runs.
- `internal/profiling/transitioncost/controller/attempt_cleanup.go:cleanupJob`:
  transition-attempt Background deletion and diagnostic retention.

## Consequences

Cleanup no longer changes scientific completion, and new retained evidence reads
survive removal of execution Jobs. A transient cleanup failure is a cleanup
responsibility, not a reason to repeat a completed measurement.

The approved operation removed **976 Succeeded Pods**, reducing total Pods from
**1,049 to 73**. Its snapshot retained **1,026 Jobs**, **1,025 Runs**, and all
stored objects unchanged. The Job total differs from the preceding inventory
snapshot; these counts are not one simultaneous cluster inventory.
Failed-phase Pods were preserved: 36 owned by successful Jobs and 10 by failed
Jobs. Five representative retained-evidence reads (genesis2, genesis3, lattepanda,
orangepi and rasp5) succeeded after Pod removal.

Legacy successful Jobs remain an intentional dependency, not unfinished automatic
cleanup. A future approved Job removal requires preserving their binding/read
path first. The inventory CSV and raw per-object checks stay outside the repository.

Raw evidence: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/measurement-job-inventory-20261027.csv`
and `deployB/control-177-20261027/{pod-cleanup-summary,loader-sample-results}.json`
under the same operations root; decisions are in `intent/checkpoints.md`.
