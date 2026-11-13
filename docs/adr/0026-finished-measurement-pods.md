# Finished Measurement Jobs, Pods and Retained Evidence

## Status

Accepted

## Decision

Once verified execution evidence and its reference are recorded, a Run remains complete without a live Job. Preserve recorded Job, Pod and Node identities.

At finalization, store the non-secret Evidence Store connection binding and `jobCleanup: Pending` with verified evidence in one Run status update. Then delete a successful measurement Job with Background propagation and an exact UID precondition. Record `Done` and reconcile interrupted cleanup.

Retained readers use the frozen Run binding. Markerless Runs without a binding still require their exact recorded owned Job to recover it; preserve that read path.

Apply the evidence-first rule to transition-cost attempts. Evidence Store initialization Jobs have their own lifecycle.

Failed/non-accepted executions retain diagnostics. `profiler.failedJobRetentionSeconds` defaults to 86400 seconds and accepts 1 through 604800. Set their Job TTL only after recording the final outcome; never set measurement Job TTL at creation.

Do not retrofit markers or diagnostic TTL onto markerless terminal records. Removing their legacy Jobs requires an explicit operation preserving the binding/read path. Measurement cleanup does not delete measurement objects or stored evidence.

## Consequences

Cleanup failure remains cleanup work and cannot trigger repeated measurement of completed evidence. Scientific completion and physical Job retention have separate lifecycles, described in the [profiling design](../design/profiling/README.md).

