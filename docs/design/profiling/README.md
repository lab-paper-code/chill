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
| [Experiment execution](experiment-execution.md) | evaluation immutable definition, profiler orchestration, harness Job and publication — design |
| [Measurement Worker](components/measurement-worker.md) | firstfloor Job-local execution, synchronization, raw-write/receipt — active |
| [Evidence Store](components/evidence-store.md) | Immutable object storage, independent verification, operator lifecycle — active |
| [MetricObserver](components/metric-observer.md) | Bounded typed power/thermal observation, no derivation policy — active |
| [WorkloadObserver](components/workload-observer.md) | Bounded Pod-membership/environment/concurrent-measurement observations — active |
| [Runtime execution](foundations/runtime-execution/README.md) | Consolidated compatibility/execution/provenance conclusions — active |

Profiling uses firstfloor with dataset `profiling` raw records and preprocessor artifacts, not a serving-entrypoint measurement command; [input contract](../serving/heterogeneous-workloads.md). Frozen RunConfig v1alpha20 binds actual model/preprocessor/dataset, batches/provider, with no CPU allocation or thread overrides; native request v1alpha2, active result v1alpha10, loaded-idle v1alpha7. Runtime-default session thread settings and actual cgroup quota/cpuset/affinity/usage/throttling remain observed evidence. Curve-plan v1alpha12 excludes CPU allocation matching. RKNN declares tensor name/FP32 datatype/batch-free NHWC shape, queries `librknnrt` metadata before measurement, and fails unsupported/unavailable metadata even for idle. Sampling uses fixed-seed `random.Random` outside timing; warmup uses module `WARMUP_SAMPLE`, measured preprocessing failure fails the trial.

Evidence v1alpha17 binds actual execution/transition facts; active raw v1alpha10 and idle raw v1alpha8 retain runtime observations rather than CPU allocation requirements. This is one stored-contract cutover after dependent campaigns finish; old controlled records/publications are not rewritten or automatically reused. Permanent contradiction is `EvidenceInvalid`. Verified reference completes a Run independently of Job existence. Successful Jobs receive Background deletion only after final evidence/reference + frozen non-secret store binding + `jobCleanup: Pending` are recorded; deletion records `Done`. Failed Jobs receive post-outcome diagnostic TTL via `profiler.failedJobRetentionSeconds` / `--failed-job-retention-seconds` (default **86400**, inclusive **1..604800**). Transition attempts follow immutable-finalization eligibility; restart retries cleanup, but markerless old terminal objects are never swept or given a new TTL. Evidence itself is not automatically garbage-collected; old Runs without binding recover from their recorded owned Job.

Parent controllers share the `ProfilingRunStatus` terminal-failure interpreter for
execution/evidence interpretation. Verified evidence takes precedence; pending
Runs remain pending. Campaign retries/result publication and idle-baseline
attempt limits/publication remain separate parent policies. This does not
replace the Profiler's current-generation pre-Job failure check.

`internal/profiling/campaign/evidence` reconstructs retained campaign results,
frozen plans and completed active/model-loaded-idle trials. It owns campaign
state/result/plan consistency and Run ownership, protocol, current-generation
completion, evidence-reference and recorded-execution binding checks. Reads
remain bounded to eight concurrent trials with input-ordered errors and lineage.
Its object/trial callbacks keep the caller's store binding and byte limits.
This boundary depends inward on the campaign, derivation, profiler evidence,
Run, Evidence Store and API contracts; the existing profiler evidence package
stays campaign-independent. Serving catalog selection, firstfloor admissibility,
energy/memory/profile construction and query reporting completeness/derivation
remain separate consumer policies. The reader's result/plan stage precedes trial
loading so serving still rejects unsupported runtime families without trial IO.

Campaign Runs now freeze `spec.campaignTrial.batch` and `.attempt`; workload mode
in `spec.measurement` identifies active versus idle, and `measurement.batch`
remains the actual execution batch. Names provide deterministic object lookup,
not logical-batch/attempt recovery. Run generation and verified curve-state
checking share `profiling/run.IdleExecutionBatch` with the resolved backend,
never the execution-path name: ORT CPU/CUDA use idle batch 1; RKNN retains the
fixed artifact batch even when its path has a model-specific name.
This is a **저장 계약 변경** (stored-object contract cutover): use new campaign
identities between dependent measurements. Old campaign children without the
identity or with contradictory trial identity stop the parent with terminal
`Failed/InvalidCampaignRun`, including the child and cause in its condition.
Foreign/stale owner-UID collisions remain reconciliation errors; only immutable
trial-identity failures become terminal parent status.
There is no name/label fallback, migration, or dual reader. Historical
raw/finalized evidence and publications are not rewritten;
RunConfig/evidence numeric execution contracts are unchanged by this change.

Campaign child discovery uses `edge.dacs.io/batch-energy-optimization` only as a
candidate filter. Parent names of at most 63 bytes retain their existing label
value; longer admitted names use `beo-` plus the first 56 hexadecimal SHA-256
characters of the full name (60 bytes total). Child creation, progress listing
and frozen-plan recovery use the same projection. The exact parent name stays
in the controller owner reference, and its UID remains the ownership authority;
a same-name foreign UID still fails reconciliation. Existing child names,
short-name labels, admission's 200-character name limit and stored evidence
contracts are unchanged. No existing objects are rewritten or campaigns restarted.

Serving configurations must be measured, not inferred. Profiling and serving share CPU without requests, limits, explicit pinning or quota-derived thread overrides. Runtime-default applied session settings, actual affinity/cpuset/quota (including unlimited), usage and throttling are observations, not allocation identities or active/idle matching gates. Memory is placement-feasibility evidence, not a limit sweep. On cgroup v2 without `memory.peak`, timestamped `memory.current` samples cover active/idle windows at a scheduling target interval; retained maxima can miss spikes and are not kernel high-water/absolute bounds. GPU UUID/RKNN ABI and actual backend remain execution observations.

Before a CurvePlan or RunConfig is recorded, the shared preparer freezes runtime memory at `min(executionPolicyDefaults.runtimeMemoryLimitBytes, floor(Node.status.allocatable.memory × profiler.measurementMemoryFraction))` in `Actor.MemoryLimitBytes`, with the selected runtime request in `Actor.MemoryRequestBytes`. The Node UID must match the resolved Run. The chart defaults remain fraction **0.8**, **16 GiB** upper limit and **128 MiB** request. Query preflight receives the same execution-policy key and memory fraction. Actual serving Pods and measurement Jobs both call `firstfloor.RuntimeContainer` with that frozen Config for image, CPU/memory/accelerator resources, performance environment and artifact/preprocessor mounts; launching does not reselect memory or image. Copy-init containers share its memory envelope; metric/workload observers retain their separate configured limits. File-copy page cache belongs to the init cgroup, not the metric observer. Serving therefore uses the measured pressure envelope rather than substituting peak-observed bytes plus overhead. Near-limit reclaim is not claimed to be measurement-neutral; an OOM fails the existing Job rather than publishing a measurement.

First measurement is profile-independent: valid public BEO and standalone inputs
need no retained profile, EvaluationRun, EvaluationStage or LoadScenario. The
existing `empirical-cadence-fixed-batch` mode adds no wait; actual monotonic
preceding-idle/service pairs and physical TrialID are retained, with null startup
idle. No timing field, donor draw, synthetic gap or client cadence is introduced.
The pinned Workload and its SLO still determine the known-gap sample minimum;
elapsed time and sample count must both complete, across all K and three trials.
Public ProfilingRun specs carry no frozen Workload UID/SLO/N body.
An owner reference is only a lookup hint, never authorization. The executable
resolver reads that root from the API, checks actual admission and the existing
Node grant, and loads its strict immutable CurvePlan. Only a canonical child
whose logical K belongs to that plan derives the plan's frozen measurement body.
Standalone admission uses that same resolver boundary; a fake owner, absent or
unadmitted root, missing plan or unplanned K cannot create a Job. The existing
idle-baseline root is authenticated through its actual immutable idle protocol.

An already-owned Job is the authenticated immutable executable-record recovery
boundary. Restart recovery trusts that recorded RunConfig/Job identity; it is
not a new child-input admission and does not reinterpret mutable source data.

The pure profiler trace join keeps final HTTP identity authoritative, enriches
only missing native fields, and retains partial/unknown physical batches before
checking full membership and consecutive IDs. Current canonical native G is
preferred; archived actor-running/finished or legacy offsets are explicitly
labelled proxy boundaries, not relabelled canonical G. Same execution conditions
and K are preferred, otherwise only G is borrowed within the one selected
context. Evidence acquisition and physical joining happen once, then every
planned K gets its own frozen selection (or the standalone current K). A shared
base-K law is not silently copied to other batches: observed K and borrowed
status remain accurate for each child. Actual record cardinality, not equal
phase weighting, drives seed-0 resampling. Explicit frozen draws cover the
configured sample floor and cycle for longer elapsed-floor trials. Child/recovery
execution consumes its selected frozen body without source rediscovery.
Zero-load separators remain in the full semantic schedule and advance the actual
producer cursor, but are not empty pacing populations. Only offered phases get
source elements; adjacency never crosses phase or separator boundaries.
Profiling may depend inward on derivation's existing semantic-condition
normalization; Kubernetes/evidence acquisition stays in the adapter.

## Dependency rule
Ownership: DeviceClass stable hardware; ModelSpec immutable artifact/path requirements; Kubernetes placement/allocation/lifecycle; Helm install identity/RBAC; CHILL frozen decisions/evidence/derivation. Pure leaves import no Kubernetes/CHILL API/controller-runtime; adapters depend inward. No generic ExecutionContract/plugin framework, compatibility status controller or implicit scheduler authorization.

Profiler execution-plan admission depends inward on the plain MetricObserver
core for schedule-derived retained/serialized byte requirements. The observer
owns record accounting; Profiler supplies the selected existing result and
container-memory limits. Kubernetes adapters only deliver that envelope.


evaluation (`SCH-08`, `SCH-31`, `SCH-42`): normative binding and all consumers reject `tolerateArchive` anywhere, regardless of value; required capture is fail-closed. Only `hack/evaluation/demo.py` diagnostic `demo-e5-binding.v2` trials may tolerate explicitly named missing power archives, retaining missing provenance/null energy: **진단 전용, evaluation 금지**. Offline check: `python3 hack/evaluation/demo.py --self-check`.
