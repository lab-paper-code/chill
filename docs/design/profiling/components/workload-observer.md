# WorkloadObserver

[Component contracts](README.md) and [Profiling design index](../README.md)

Whole-node power interpretation needs membership throughout the runtime window.
The observer runs only in the model-loaded idle baseline measurement, whose
result the power-mode transition cost reads. Batch profiles are measured by the
session in [profile-session.md](../../serving/profile-session.md). The observer
records active Pods on the target Node and observation continuity.
Pod presence does not prove resource usage or constant load.

## Execution ownership

The measurement Job has artifact/preprocessor init containers and runtime,
MetricObserver and WorkloadObserver containers. Kubernetes owns Pod lifecycle.

Before warmup, target-Node field-selected Pod LIST/WATCH probes capability.
After warmup, runtime requests a fresh retained session. Acknowledgement requires
the initial active set and continued watch. Runtime then signals metric start
and opens the window.

| Atomic path | Meaning |
|---|---|
| `workload-observer-ready` | Preflight capability |
| `workload-window-start` | Open retained session after warmup |
| `workload-window-ready` | Membership and continuation established |
| `measurement-end` | Runtime window closed |
| `runtime-failed` | Stop waits/watch and fail |
| `workload-result.json` | Bounded atomic result after causal end fence |

After measurement-end, the same watch continues until the Run Pod reports
successful `metric-observer` termination. That helper publishes its result only
after observing measurement-end. Bookmarks and later lists do not establish
this causal progress.

The original attempt context covers preparation, runtime, watch drain and both
handoffs. Closing the window does not restart or detach the deadline.
Runtime failure and original cancellation still stop observation.

## Retained facts

Retain target Node name/UID, continuous resourceVersion bounds, coverage times,
initial/final active sets, material events, Pod identities/owners/phases/images,
Run Job/Pod self-identity and typed failures. Pending, Running and Unknown Pods
are conservatively active. Already-terminal Pods are not active co-residents.

Retain material events drained before the fence. Receipt time cannot establish
whether a source change preceded runtime end; ambiguous timing stays explicit.
Missing fence, expired history, unresumable watch, failure or overflow makes
continuity incomplete. Events are never silently truncated.

The handoff boundary checks schema, target/Run identity, bounds, chronology and
surrounding coverage. Raw composition binds source, target and window;
finalization validates once. Retained readers decode bounded immutable evidence.
No terminal Node workload snapshot is added.

Equivalent order/metadata representations need no byte-for-byte replay.
Unaccounted physical membership projects Unknown. Incomplete observation adds
no measurement STOP or scientific rejection; malformed data and wrong
target/window links still fail.

## Environment identity

Retain every external Pod without product-specific selectors. Keep the fixed
measurement role and standard labels needed to identify concurrent CHILL
measurements. The observer never changes workloads or Node state.

Owner-bound identity uses controller kind/name/UID, phase, images, selected
labels and replica multiplicity. Same-owner replacement between Runs does not
drift identity merely through Pod name/UID. Unowned Pods remain name/UID-bound.
Different environments remain separate; stable membership proves no isolation.

## Permissions and recovery

`automountServiceAccountToken=false`; only WorkloadObserver receives its dedicated
short-lived projected token and CA. Its ServiceAccount has Pod get/list/watch.
Node field selection limits requests; cross-namespace discovery still requires
cluster-wide Pod-read authority.

Kubernetes returns whole Pods; evidence projects bounded fields. Installation
may provide an API endpoint for KubeEdge without Service environment variables.
There is no Secret/ConfigMap access, Node mutation, log/exec access, status write
or workload write.

Committed raw supports reconstruction without another watch. A later snapshot
cannot recover an uncommitted window. A new execution needs a new Run identity.
The observer owns no isolation, scheduling, batch or publication policy.
