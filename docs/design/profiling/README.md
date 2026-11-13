# Runtime, Artifact, and Profiling

Current boundary: execution → immutable raw/finalized evidence → numeric derivation. Finalization validates raw evidence once and binds raw Pod UID, target Node, actuation, Job success/completion and finalize time. Retained execution readers bound actual bytes and decode the current schema only; raw RunConfig supplies execution authority. Mutable request/status copies, deterministic-key spelling, receipt sizes and duplicated provenance do not establish a second execution contract. Scientific admission removed @`788bcf93`: thermal/workload observations remain, without automatic scientific accept/reject. Source and retained evidence, not historical plans, establish implementation facts.

Batch discovery freezes `1, 2, 4, …` through the selected firstfloor's actual
`MaxBatchSize`, intersected with finite artifact-declared batches when present;
batch 1 is required. Public BEO inputs expose no batch list; the existing
`retryLimit` (0–10) remains for known transient infrastructure failures at the same
K. Each trial retains its own existing ProfilingRun and Kubernetes Job.
The common actor startup warms only batch 1; the first higher-K execution remains
inside that K's measured window. Three completed loaded-idle trials precede three
completed active trials per K. Active trials meet both elapsed-time and known-G
sample floors.

Discovery ends on domain exhaustion, runtime-execution memory exhaustion
(`OOMKilled` or typed ORT BFCArena allocation failure), or the existing execution
measurement-budget boundary. `Complete/BatchDiscoveryComplete`
records the completed batch prefix, exact stop and optional prefix energy; it does
not assert that every candidate succeeded or that a usable profile exists.
Generic runtime/model errors, integrity failures and unknown failures are fatal.
Known infrastructure disruptions use bounded same-K retries; exhaustion is
`Failed`. Neither a local energy minimum nor SLO impossibility stops discovery.
[Plan 7](plans/07-batch-energy-optimization-control-path.md) owns the result contract.
After approved source deployment, start new campaign identities; do not resume
old campaigns under this contract or rewrite completed raw/finalized evidence,
stored plans or results. Historical full-sweep counts remain provenance, not new
campaign completion targets.

## Plans

| Plan | Boundary | Status |
|---|---|---|
| [1](plans/01-catalog-static-compatibility.md) | Read-only catalog/static compatibility | completed |
| [2](plans/02-profiling-run-execution.md) | Immutable Run/one native Job/evidence | completed |
| [3](plans/03-profile-admission-derivation.md) | Numeric derivation and serving below-resolution energy | completed |
| [4](plans/04-minimal-profiler-control-path.md) | Operator-managed Profiler and durable control | completed |
| [6](plans/06-execution-support-expansion.md) | Exact support matrix and outstanding device/live gates | active |
| [7](plans/07-batch-energy-optimization-control-path.md) | Automatic batch discovery and completed-prefix energy result | current contract; integration/live proof separate |
| [8](plans/08-declared-batch-size-artifacts.md) | Artifact declaration bounds automatic batch domain | current contract; retained artifact probe below |
| [10](plans/10-transition-cost-measurement.md) | Exact directed tau/eta measurement; new estimator live gate open | completed / live gate active |
| 5 / 5a | Saturation / sequential early-stop designs | removed @`dd520844`; Plan 7 owns current objective |
| 9 | Owned NodeActuation transition extraction | removed @`f4b0b685`; [Node request/generation](../platform/node-agent.md#3-노드-전력모드-node-label과-generation) owns current protocol |

Retired Plan 5 experiments remain conclusions, not current APIs: 12 retained idle/discovery Runs selected batch 1 but a three-trial cap could not meet 5% confirmatory precision (`Unavailable/ConfirmatoryBudgetInsufficient`). A disjoint pilot-informed six-trial-cap/18-active-Run-budget campaign selected batch 1, froze four confirmatory Runs per measured batch, retained all eight, and found remaining-improvement upper bound **6.395% > 5%** (`CandidateOnly/DomainNoImprovementNotConfirmed`); no pooling or confirmed saturation claim.

## Reusable contracts

| Contract | Ownership / status |
|---|---|
| [Experiment execution](experiment-execution.md) | evaluation immutable definition, profiler orchestration, harness Job and publication — design |
| [Measurement Worker](components/measurement-worker.md) | firstfloor Job-local execution, synchronization, raw-write/receipt; raw load counts prove load success — active |
| [Evidence Store](components/evidence-store.md) | Immutable winner, finalize-only validation, bounded decode-only execution readers, operator lifecycle — active |
| [MetricObserver](components/metric-observer.md) | Bounded typed power/thermal observation, no derivation policy — active |
| [WorkloadObserver](components/workload-observer.md) | Bounded Pod-membership/environment/concurrent-measurement observations — active |
| [Runtime execution](foundations/runtime-execution/README.md) | Consolidated compatibility/execution/provenance conclusions — active |

Profiling uses firstfloor's serving `/infer` path with dataset `profiling` raw records and preprocessor artifacts; [input contract](../serving/heterogeneous-workloads.md). Frozen RunConfig v1alpha22 binds actual model/preprocessor/dataset, batches/provider and operating-point inputs, including positive integer `postLoadDurationSeconds`, with no CPU allocation or thread overrides. Native request v1alpha2 remains; active runtime and raw are v1alpha14, loaded-idle runtime v1alpha7. An off-node load Job executes sparse, measured-K saturation and Poisson utilization phases; the same loaded actor and observers remain alive through the frozen request-free post-load window. [Measurement Worker](components/measurement-worker.md#active-raw-v1alpha14-field-contract) owns the exact phase/request/batch/power and post-load schema. Runtime-default session thread settings and actual cgroup quota/cpuset/affinity/usage/throttling remain observed evidence. RKNN retains its tensor/host metadata checks and fixed artifact batch.

Execution evidence v1alpha18 binds actual execution/transition facts without container terminal records or Node workload snapshots. Raw load counts prove load success; Job status determines finished/failed execution. Finalization does not recheck raw workload self-consistency. Active raw v1alpha14 and idle raw v1alpha8 remain unchanged. This is one stored-contract cutover under [ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md): operators delete affected old working objects, with no compatibility reader or migration. Archived scientific raw records are not rewritten. Permanent contradiction at finalization is `EvidenceInvalid`. Verified references complete Runs independently of Job existence. Successful measurement and off-node load Jobs use their recorded Run ownership and cleanup lifecycle; failed Jobs retain the configured diagnostic TTL. Evidence is not automatically garbage-collected.

Parent controllers share the `ProfilingRunStatus` terminal-failure interpreter for
execution/evidence interpretation. Verified evidence takes precedence; pending
Runs remain pending. Expected BEO OOM/budget boundaries complete discovery without
retry; known transient infrastructure disruptions alone use its existing retry
limit. Fatal or retry-exhausted failures remain `Failed`. Idle-baseline attempt
limits/publication remain a separate parent policy. This does not
replace the Profiler's current-generation pre-Job failure check.

Finalization distinguishes immutable evidence contradictions (`EvidenceInvalid`,
fatal to a campaign) from retryable store/API failures
(`EvidenceVerified=Unknown/EvidenceVerificationFailed`). The latter retry
finalization against the recorded Job instead of consuming execution attempts.
Retained Runs with the previous `False/EvidenceVerificationFailed` condition
also reverify normally after deployment; they are not terminal failures.

Shared preparation admits the full observer `AttemptTimeout` against the selected
result and memory budgets before public preflight succeeds or a new Job is built.
Capacity rejection records current-generation `ExecutionPolicyInvalid`; an owned
BEO child then produces parent `FatalFailure`, preserves completed trials, and
uses normal Node handback. Transient dependency read errors remain reconciliation
errors. Root `Failed/ExecutionPolicyInvalid` supports existing guarded public
retirement. Recorded Job recovery retains its original executable contract.

`internal/profiling/campaign/evidence` reconstructs retained results, frozen plans
and completed active/model-loaded-idle trials. Result schema/outcome, Run ownership,
current-generation completion, actual trial mode/batch and recorded Job/Pod binding
remain checked. Frozen result/plan bodies are not re-admitted against mutable
campaign specs or repeated reference/provenance copies. The profiler execution
reader only bounds bytes and decodes; raw validation belongs to finalization.
Reads remain bounded to eight concurrent trials with input-ordered errors and
lineage; callbacks retain the caller's frozen store binding and actual byte cap.
Serving selection, runtime support, energy/memory/profile construction and query
completeness remain consumer policies. Result/plan loading precedes trial IO.

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

Runtime validation still checks real inference, tensor metadata, initialized and
executed batches, counts, physical native service samples, memory observations and
selected devices. Runtime/NumPy/RKNN package and driver version strings remain
observed provenance; declaration-version differences do not reject real execution.
Observer representation differences are accepted. Missing physical membership
continuity projects an Unknown workload environment without adding a measurement
STOP; malformed observations, wrong targets and uncovered runtime windows still fail.

Before a CurvePlan or RunConfig is recorded, the shared preparer freezes runtime memory at `min(executionPolicyDefaults.runtimeMemoryLimitBytes, floor(Node.status.allocatable.memory × profiler.measurementMemoryFraction))` in `Actor.MemoryLimitBytes`, with the selected runtime request in `Actor.MemoryRequestBytes`. The Node UID must match the resolved Run. The chart defaults remain fraction **0.8**, **16 GiB** upper limit and **128 MiB** request. Query preflight receives the same execution-policy key and memory fraction. Actual serving Pods and measurement Jobs both call `firstfloor.RuntimeContainer` with that frozen Config for image, CPU/memory/accelerator resources, performance environment and artifact/preprocessor mounts; launching does not reselect memory or image. Copy-init containers share its memory envelope; metric/workload observers retain their separate configured limits. File-copy page cache belongs to the init cgroup, not the metric observer. Serving therefore uses the measured pressure envelope rather than substituting peak-observed bytes plus overhead. Near-limit reclaim is not claimed to be measurement-neutral; an OOM fails the existing Job rather than publishing a measurement.

First measurement is profile-independent: valid public BEO and standalone inputs
need no retained profile, EvaluationRun, EvaluationStage or LoadScenario.
Profiling native uses fixed-batch `/infer` without service distributions.
Sparse no-backlog samples determine saturation K in the same Run; saturation
determines utilization arrival rates. Every phase keeps actual native service/
preceding-idle pairs and reaches both its duration and known-gap sample floor.
Only utilization phases enter incremental energy; all phases enter service
distributions. Per-phase whole-node power and saturation dispatch residuals remain
evidence. Loaded-idle retains the existing settle, empty-queue, zero-inference
and resident-state protocol. See the worker contract for the immutable inputs.

Scheduled client arrivals (`scheduledAt`) define active request latency and offered
rates. Clients FIFO-wait for frozen native slots before `dispatchedAt`; temporary
slot saturation does not invalidate a phase. Queue hold consumes existing Context
elapsed/lifetime budget. Every phase, including saturation, retains common
scheduled/sent/completed counts, load-node offering/drain UTC boundaries and
`scheduledInputs / issueElapsedSecondsMonotonic`; measurement-node power fences
remain separate. Active raw/runtime v1alpha14 and RunConfig v1alpha22 remain the
single current contract, without old-field readers.

Load-plan validation separates identity/budgets, frozen operating points, dataset
fields and native endpoints in that order. Native response validation checks
completion identity, service-pair membership and nested timing before retaining
compact proof. Poisson arrival production and FIFO dispatch have separate named
steps; both still charge the same frozen result-byte budget.

`make test-native` uses two independent active and two loaded-idle trials for its
local Identity-model integration fixture. Active phases retain the 59-known-gap
floor with 4-second elapsed floors and an 18-second post-load window (30-second
frozen active duration). Idle still settles for 60 seconds and observes for 30.
This bounded software-path fixture does not change campaign measurement defaults.

Public ProfilingRun specs carry no frozen Workload UID/SLO/N body. Owner
references remain lookup hints; the executable resolver authenticates the root,
Node grant and immutable CurvePlan. No fake owner, unadmitted root or unplanned
batch may create a Job. The idle-baseline root retains its own protocol.

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
Ownership: DeviceClass stable hardware; ModelSpec immutable artifact/path requirements; Kubernetes placement/allocation/lifecycle; Helm install identity/RBAC; CHILL frozen decisions/evidence/derivation. `modelcompat` and the `evidencestore` root have no Kubernetes imports; derivation has no direct Kubernetes imports. Its firstfloor dependency includes `RuntimeContainer`, a Kubernetes value projection, so derivation is not transitively Kubernetes-free. Firstfloor is a CHILL-leaf; `profiling/run` includes Kubernetes projections and a Node grant reader. Adapters depend inward. No generic ExecutionContract/plugin framework, compatibility status controller or implicit scheduler authorization.

Profiler execution-plan admission depends inward on the plain MetricObserver
core for schedule-derived retained/serialized byte requirements. The observer
owns record accounting; Profiler supplies the selected existing result and
container-memory limits. Kubernetes adapters only deliver that envelope.


evaluation (`SCH-08`, `SCH-31`, `SCH-42`): normative binding and all consumers reject `tolerateArchive` anywhere, regardless of value; required capture is fail-closed. Only `hack/evaluation/demo.py` diagnostic `demo-e5-binding.v2` trials may tolerate explicitly named missing power archives, retaining missing provenance/null energy: **진단 전용, evaluation 금지**. Offline check: `python3 hack/evaluation/demo.py --self-check`.
