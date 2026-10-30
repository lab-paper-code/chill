# WorkloadObserver

> Navigation: [Component contracts](README.md) · [Profiling design index](../README.md)

## Status and purpose

Implemented; causal Pod-status end-fence success was live-verified by three
sequential same-RunConfig Runs (historically admitted by `wall-power-v2`).
Scientific admission was removed (`788bcf93`); current output is bounded workload
facts, not accept/reject policy. Read-only identity/raw-evidence ownership remains.

Whole-node wall power requires window membership, not merely a terminal Pod list.
The observer records which Pods were active on the target Node and whether that
observation continuously covered the exact runtime window. Presence proves no
CPU, GPU, network, application activity, or stable internal load.

## Ownership and execution

The Job contains artifact init, runtime worker, MetricObserver, WorkloadObserver.
The worker validates the workload result against RunConfig and includes it in raw
evidence; Kubernetes owns Pod identity/lifecycle. No controller-memory background
watch or reconcile-time measurement. The observer never mutates/evicts/cordons/
taints/reschedules/pauses workloads, changes Node phase, reads runtime/power/
thermal/application metrics, or decides profile publication.

Before warm-up, field-selected Pod `LIST/WATCH` for `spec.nodeName=<target>`
probes capability without retaining measurement evidence. After warm-up, runtime
requests a fresh retained list-watch session; acknowledgement requires initial
active set and watch continuation. Runtime then emits the separate metric start
signal and begins inference. Pods gone before that second list do not contaminate
the window.

| Atomic Job-local path | Meaning |
|---|---|
| `workload-observer-ready` | Preflight list-watch capability established |
| `workload-window-start` | Warm-up complete; open retained session |
| `workload-window-ready` | Retained list-watch continuity established |
| `measurement-end` | Runtime window closed |
| `runtime-failed` | Stop every wait/watch immediately and exit failed |
| `workload-result.json` | Atomic bounded result after causal Pod-status end fence |

`runtime-failed` releases the Node within seconds. After measurement-end, keep
consuming the same watch until the Run Pod reports successful termination of
`metric-observer`: that helper cannot succeed until it observes measurement-end
and atomically publishes its result. This Pod status resource-version fence is
causally post-end; neither discretionary bookmarks nor later lists prove progress.

Retain material events drained before the fence. Receipt times cannot prove
whether source transitions preceded runtime end, so ambiguous drain timing remains
explicit uncertainty, not an ignored event. Missing fence, expired history,
unresumable watch or result overflow means incomplete coverage. Coverage bounds
must surround the runtime result's exact timestamps.

## Bounded evidence

Retain target Node name/frozen UID; initial-list and last continuous
`resourceVersion`; observer coverage bounds; start/end active Pod sets; material
membership changes; namespace/name/UID, owner identity, phase and requested images;
Run Job/Pod identities for self-exclusion; typed failures; continuous-window fact.

Pending, Running and Unknown Pods during any part of the window are conservatively
active. Pods already Succeeded/Failed before the window are audit-only when needed,
not active co-residents. Overflow is incomplete evidence, never silent truncation.
Current raw schema requires the bounded workload result; terminal Node workload
snapshot stays a separate post-run historical fact.

Reconstruction requires list-watch continuity, surrounding coverage, matching Node/
Run Pod/Job/RunConfig identities, strict result validation, and explicit watch-gap,
expired-history, overflow, observer-failure and ambiguous-end-drain facts. Concurrent
CHILL measurements and material external membership changes are retained as facts;
stable external presence is not an allowlist or scientific rejection rule.

## External environment identity

CHILL maintains no external-product selectors/identities. Retain every external
Pod on the Node without classification by product, namespace, owner or label.
Profiling Jobs carry one fixed workload-role label; retain only it and standard
CHILL workload labels needed to identify another CHILL measurement. Report external
entry, exit, restart or material change without mutating or pausing the workload.

Stable external set contributes to owner-bound environment identity: controller
owner kind/name/UID, phase, images, selected labels and replica multiplicity.
Exclude Pod name/UID for owner-controlled Pods so same-owner replacement between
trials does not falsely drift identity. Owner UID/image/phase/selected-label/
replica-count changes do change identity. Unowned Pods remain name/UID-bound,
failing safe on replacement. Different identities remain individually usable,
not aggregatable as one environment. Stable membership does not prove constant
load; repeated trials, idle uncertainty and later isolation policy are separate.

## Permission and recovery boundary

`automountServiceAccountToken=false`; dedicated short-lived projected token/CA
mount only in WorkloadObserver, never runtime, artifact or MetricObserver.
Helm owns the dedicated ServiceAccount and read-only Pod `get`, `list`, `watch`.
`get` verifies own Pod/Job identity. Target-Node field selection limits requests,
not RBAC: cross-namespace co-residents require cluster-wide Pod-read authority.
Kubernetes returns whole Pods; evidence projects only the bounded fields above.
Credential and immutable observer OCI image remain isolated from other containers.

Regular Pods may use in-cluster configuration. KubeEdge may lack Service env vars,
so Job supplies installation-owned API endpoint when needed. Shared CHILL client
configuration reuses projected token/CA paths; no credential discovery/persistence.
No Secret/ConfigMap access, Node mutation, logs, exec, status write or workload write.

Committed immutable raw evidence permits restart reconstruction without another
watch. A Job ending before complete evidence commit cannot recover window facts
from later state. Duplicate reconciles only verify retained results; retry requires
a new Run identity.

Non-goals: generic audit/arbitrary object watching, process attribution, isolation/
eviction/scheduling control, idle/repeated-trial orchestration, batch optimization,
SLO/`DeviceProfile`/scheduler publication, or long-term workload monitoring.
