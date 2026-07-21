# WorkloadObserver

> Navigation: [Profiling design index](../README.md)

## Status

Implemented. The Kubernetes-free observation contract, isolated Kubernetes
list-watch adapter, bounded executable, immutable installation-fact resolution,
read-only identity, Job-local execution, raw evidence retention, admission
projection, and read-only operator projection are live-verified.

## Purpose

CHILL measures whole-node wall power. A terminal Pod list can describe what
remains after a Run, but it cannot prove which workloads were present during
the earlier runtime measurement window.

WorkloadObserver answers one bounded question:

> Which Kubernetes Pods were active on the target Node throughout one exact
> runtime measurement window, and was that observation continuous?

It reports Kubernetes workload-membership facts. It does not infer CPU, GPU,
network, or application activity from Pod presence.

## Responsibility boundary

WorkloadObserver owns:

- establishing an initial Pod list for the frozen target Node;
- continuing from that list's Kubernetes `resourceVersion` with a Pod watch;
- aligning its coverage with the runtime measurement start and end signals;
- retaining bounded membership changes and the active set at both boundaries;
- reporting watch interruption, expired history, and overflow;
  and
- writing one atomic Job-local result for the Measurement Worker.

The Measurement Worker validates the result against the frozen intent and
includes it in raw evidence. Profiler admission decides whether coverage is
sufficient and whether observed co-residents violate the selected policy.
Kubernetes remains authoritative for Pod identity and lifecycle.

WorkloadObserver does not:

- mutate, evict, cordon, taint, reschedule, or pause workloads;
- change Node measurement or serving phase;
- read power, thermal, runtime, or application metrics;
- run inside a controller reconcile or controller-memory background loop; or
- decide whether a profile is publishable.

## Execution shape

The one-shot profiling Pod gains a third long-running container:

```text
artifact init container
        |
        v
runtime  <---- shared measurement signals ---->  MetricObserver
   |
   +----- shared measurement signals --------->  WorkloadObserver
        |
        v
Measurement Worker commits one raw evidence object
```

WorkloadObserver starts before warm-up and probes a field-selected Pod
`LIST/WATCH` for `spec.nodeName=<target>`. This readiness probe retains no
measurement evidence. After warm-up, runtime requests the workload window;
WorkloadObserver opens a new list-watch session and acknowledges only after its
initial active set and watch continuation are established. Runtime then emits
the separate measurement-start signal used by MetricObserver and begins
inference. Pods that disappeared before this second list cannot contaminate the
measurement window.

The executable uses five atomic Job-local coordination paths:

```text
workload-observer-ready  preflight list-watch capability is established
workload-window-start    warm-up is complete; open the retained observation
workload-window-ready    retained list-watch continuity is established
measurement-end          runtime has closed its measurement window
workload-result.json     validated bounded result after the closing bookmark
```

The observer continues until the runtime end signal. It then opens one bounded
catch-up watch from the last processed `resourceVersion` and closes coverage
only after receiving a Kubernetes bookmark. Events replayed before that bookmark
remain part of the observation. A missing bookmark, expired history, an
unresumable watch, or a bounded-result overflow makes coverage incomplete. Its
coverage timestamps must surround the runtime result's exact measurement
timestamps. It never fills a gap by treating a later list as proof of the
missing interval.

## Evidence contract

The bounded result retains only facts needed to reconstruct membership:

- target Node name and frozen Node UID;
- initial list `resourceVersion` and observer coverage bounds;
- active Pod identities at measurement start and end;
- material membership changes during the window;
- Pod namespace, name, UID, owner identity, phase, and requested images;
- the profiling Job and Pod identities used to exclude the Run itself;
- the last continuous `resourceVersion`;
- typed observation failures; and
- whether the complete runtime window was continuously covered.

Succeeded and Failed Pods already terminal before the window are retained only
when needed for audit and are not classified as active co-residents. A Pod that
is Pending, Running, or Unknown during any part of the window is conservatively
active. The result is size-bounded. Exceeding the bound is evidence
insufficiency, not permission to truncate silently.

Raw measurement evidence requires a new schema version. Previously retained
raw evidence remains readable but has unknown workload-window coverage. The
terminal Node workload snapshot remains a separate post-run historical fact;
it is not replaced or reinterpreted as window evidence.

## Admission projection

Admission can set the window-known fact only when:

- list-to-watch continuity is established;
- observer coverage surrounds the runtime measurement window;
- target Node, Run Pod, Job, and intent identities match;
- no watch gap, expired history, overflow, or observer failure exists; and
- the workload result passes strict evidence validation.

Any Pod membership change that violates the selected co-resident policy is a
typed rejection. Missing or discontinuous observation is `Insufficient`.

The stable background set must contribute to the material environment identity
used to compare active trials and idle baselines. Workload membership evidence
does not prove that a stable Pod had constant internal load; repeated trials,
idle-baseline uncertainty, and later measurement-isolation policy remain
separate safeguards.

## Kubernetes permission boundary

The profiling Pod keeps `automountServiceAccountToken=false`. A dedicated,
short-lived projected ServiceAccount token is mounted only into the
WorkloadObserver container. Runtime, artifact, and MetricObserver containers do
not receive the token.

Helm, not the Profiler controller, owns the dedicated ServiceAccount and its
read-only Pod `get`, `list`, and `watch` permission. `get` validates the helper's
own Pod and Job identity. The observer requests only Pods selected by the frozen
target Node, but built-in Kubernetes RBAC authorizes Pod list/watch at namespace
or cluster scope rather than by Node field. Because co-residents can live in any
namespace, this is necessarily a cluster-wide Pod-read capability. Kubernetes
returns complete Pod objects; the observer projects only the bounded identity
fields above into evidence. The credential and digest-pinned observer image must
remain isolated from all other Job containers.

Regular Kubernetes Pods may use standard in-cluster configuration. KubeEdge
edge Pods cannot assume that Kubernetes service environment variables are
present, so the Job supplies the installation-owned API endpoint when needed.
The executable reuses the projected token and CA paths through the shared CHILL
Kubernetes client configuration adapter; it does not discover or persist
credentials.

No Secret, ConfigMap, Node mutation, Pod log, exec, status write, or workload
write permission is granted.

## Recovery boundary

The observer result is part of the immutable raw object. Once committed, the
Profiler can reconstruct admission after restart without another watch. A Job
that terminates before committing complete workload evidence cannot be repaired
from later Kubernetes state and remains insufficient.

Duplicate reconciles may verify the same retained result but never recreate
measurement-window facts. Retrying measurement requires a new Run identity.

## Allowed-background policy

Every active Pod other than the profiling Run Pod must match an
installation-time allowed-background selector. Stable presence alone does not
make an unrelated workload admissible.

The Profiler resolves namespace, direct controller owner kind and name, and
exact-label selectors from its installation-owned fact source before Job
creation and freezes their canonical form in the internal Run intent. Labels
alone do not establish workload identity because another Pod can copy them. The intent
deterministically provides the policy digest and retained label keys used by the
observer. The observer records the exact Pod facts needed to reproduce matching,
while admission applies the frozen policy. CHILL does not mutate external
workloads. Additional selector operators require a later policy schema.

The stable allowed-background set also contributes to the material environment
identity used to compare active trials and idle baselines. Policy is never a
mutable finalization-time choice or a caller-supplied per-Run knob.

## Explicit non-goals

- generic Kubernetes audit collection;
- arbitrary object or event watching;
- process-level activity attribution;
- node isolation, workload eviction, or scheduling control;
- idle-baseline or repeated-trial orchestration;
- `b_sat`, SLO, `DeviceProfile`, or scheduler publication; and
- long-term workload monitoring.
