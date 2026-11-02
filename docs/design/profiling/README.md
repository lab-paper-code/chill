# Runtime, Artifact, and Profiling

Current boundary: execution → immutable raw/finalized evidence → numeric derivation. Scientific admission removed @`788bcf93`: thermal/workload observations remain, without automatic scientific accept/reject. Read only the relevant contract; source and retained evidence, not historical plans, establish implementation facts.

Quick-energy policy `quick-energy-v2` measures every batch in the frozen ordered
plan before deriving the minimum measured mean incremental work per item.
Intermediate local minima no longer stop measurement; completion is
`MinimumFound/SearchSpaceExhausted`, not a saturation or unmeasured-optimality
claim. The policy label is recorded in result detail; CRD, RunConfig, raw and
evidence schemas are unchanged. Terminal current-generation v1 campaigns and
their immutable results remain untouched/readable. Nonterminal campaigns use
the current controller policy on resume (the policy version is not frozen in
the BEO spec); an already stored incompatible result is rejected, never
overwritten. Use a new campaign identity for a new v2 sweep.

## Plans

| Plan | Boundary | Status |
|---|---|---|
| [1](plans/01-catalog-static-compatibility.md) | Read-only catalog/static compatibility | completed |
| [2](plans/02-profiling-run-execution.md) | Immutable Run/one native Job/evidence | completed |
| [3](plans/03-profile-admission-derivation.md) | Numeric derivation and serving below-resolution energy | completed |
| [4](plans/04-minimal-profiler-control-path.md) | Operator-managed Profiler and durable control | completed |
| [6](plans/06-execution-support-expansion.md) | Exact support matrix and outstanding device/live gates | active |
| [7](plans/07-batch-energy-optimization-control-path.md) | Minimum-energy objective; v2 full sweep supersedes historical local-stop scheduling | completed |
| [8](plans/08-declared-batch-size-artifacts.md) | Artifact versus campaign batch-set ownership | completed |
| [10](plans/10-transition-cost-measurement.md) | Exact directed tau/eta measurement; new estimator live gate open | completed / live gate active |
| 5 / 5a | Saturation / sequential early-stop designs | removed @`dd520844`; Plan 7 owns current objective |
| 9 | Owned NodeActuation transition extraction | removed @`f4b0b685`; [Node request/generation](../platform/node-agent.md#3-노드-전력모드-node-label과-generation) owns current protocol |

Retired Plan 5 experiments remain conclusions, not current APIs: 12 retained idle/discovery Runs selected batch 1 but a three-trial cap could not meet 5% confirmatory precision (`Unavailable/ConfirmatoryBudgetInsufficient`). A disjoint pilot-informed six-trial-cap/18-active-Run-budget campaign selected batch 1, froze four confirmatory Runs per measured batch, retained all eight, and found remaining-improvement upper bound **6.395% > 5%** (`CandidateOnly/DomainNoImprovementNotConfirmed`); no pooling or confirmed saturation claim.

## Reusable contracts

| Contract | Ownership / status |
|---|---|
| [Experiment execution](experiment-execution.md) | E5 immutable definition, profiler orchestration, harness Job and publication — design |
| [Measurement Worker](components/measurement-worker.md) | firstfloor Job-local execution, synchronization, raw-write/receipt — active |
| [Evidence Store](components/evidence-store.md) | Immutable object storage, independent verification, operator lifecycle — active |
| [MetricObserver](components/metric-observer.md) | Bounded typed power/thermal observation, no derivation policy — active |
| [WorkloadObserver](components/workload-observer.md) | Bounded Pod-membership/environment/concurrent-measurement observations — active |
| [Runtime execution](foundations/runtime-execution/README.md) | Consolidated compatibility/execution/provenance conclusions — active |

Profiling uses firstfloor with dataset `profiling` raw records and preprocessor artifacts, not a serving-entrypoint measurement command; [input contract](../serving/heterogeneous-workloads.md). Frozen RunConfig v1alpha17 binds actual model/preprocessor/dataset, batches/threads/affinity/provider; native request v1alpha2, active result v1alpha8, loaded-idle v1alpha6. RKNN declares tensor name/FP32 datatype/batch-free NHWC shape, queries `librknnrt` metadata before measurement, and fails unsupported/unavailable metadata even for idle. Sampling uses fixed-seed `random.Random` outside timing; warmup uses module `WARMUP_SAMPLE`, measured preprocessing failure fails the trial.

Evidence v1alpha15 binds actual execution/transition facts; permanent contradiction is `EvidenceInvalid`. Verified reference completes a Run independently of Job existence. Successful Jobs receive Background deletion only after final evidence/reference + frozen non-secret store binding + `jobCleanup: Pending` are recorded; deletion records `Done`. Failed Jobs receive post-outcome diagnostic TTL via `profiler.failedJobRetentionSeconds` / `--failed-job-retention-seconds` (default **86400**, inclusive **1..604800**). Transition attempts follow immutable-finalization eligibility; restart retries cleanup, but markerless old terminal objects are never swept or given a new TTL. Evidence itself is not automatically garbage-collected; old Runs without binding recover from their recorded owned Job.

Parent controllers share `ProfilingRunStatus.TerminalFailureReasons` for failed
execution/evidence interpretation. Verified evidence takes precedence; pending
Runs remain pending. Campaign retries/result publication and idle-baseline
attempt limits/publication remain separate parent policies. This does not
replace the Profiler's current-generation pre-Job failure check.

Serving configurations must be measured, not inferred. CPU request is separate from optional positive whole-core quota and physical affinity; quotas/masks distinguish measurements, inter-op threads=1, affinity alone records `ExclusivityClaimed=false`. Memory is placement-feasibility evidence, not a limit sweep. On cgroup v2 without `memory.peak`, timestamped `memory.current` samples cover active/idle windows at a scheduling target interval; retained maxima can miss spikes and are not kernel high-water/absolute bounds. GPU UUID/RKNN ABI and actual backend remain execution observations.

Measurement Jobs cap runtime and artifact/preprocessor/dataset copy-init memory at `min(executionPolicyDefaults.runtimeMemoryLimitBytes, floor(Node.status.allocatable.memory × profiler.measurementMemoryFraction))`; the chart defaults to fraction **0.8** and a **16 GiB** upper envelope. File-copy page cache is charged to the init cgroup, so the **64 MiB** metric observer limit is not a suitable copy limit. The Node UID must still match the resolved Run. Runtime and copy-init requests share `executionPolicyDefaults.runtimeMemoryRequestBytes` (**128 MiB**); metric/workload observers keep their configured **64/128 MiB** limits. Sequential copy init containers do not increase the Pod's effective memory request above runtime plus observers. This leaves node headroom instead of admitting the old 16 GiB runtime envelope on 4/8 GiB devices. Limits are recorded only in native Job/Pod specs, not RunConfig or evidence schema; recovery keeps the immutable Job's cap. Any runtime, observer, or init `OOMKilled` is retained with container name and limit in `status.runtimeTermination.logTail` through the existing failed-Job path. CPU quota, thread/affinity settings, timing and power sampling are unchanged; memory limits are safety bounds, not a memory sweep. Runs that exceed them fail rather than publish a measurement; near-limit memory reclaim can affect timing/power and is not claimed to be measurement-neutral.

## Dependency rule
Ownership: DeviceClass stable hardware; ModelSpec immutable artifact/path requirements; Kubernetes placement/allocation/lifecycle; Helm install identity/RBAC; CHILL frozen decisions/evidence/derivation. Pure leaves import no Kubernetes/CHILL API/controller-runtime; adapters depend inward. No generic ExecutionContract/plugin framework, compatibility status controller or implicit scheduler authorization.

E5 (`SCH-08`, `SCH-31`, `SCH-42`): normative binding and all consumers reject `tolerateArchive` anywhere, regardless of value; required capture is fail-closed. Only `hack/e5/demo.py` diagnostic `demo-e5-binding.v1` trials may tolerate explicitly named missing power archives, retaining missing provenance/null energy: **진단 전용, E5 금지**. Offline check: `python3 hack/e5/demo.py --self-check` (not run by this documentation change).
