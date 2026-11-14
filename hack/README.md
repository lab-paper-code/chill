# Hack Scripts

Use a repo-local kind kubeconfig for development and e2e tests:

```sh
./hack/kind-up.sh
export KUBECONFIG="$PWD/.kube/chill-kind.config"
```

The tracked `.envrc` points `KUBECONFIG` at `.kube/chill-kind.config` when direnv is enabled.

Do not use the testbed kubeconfig as the default shell context inside this repo. Access the testbed only with an explicit prefix:

```sh
KUBECONFIG=/path/to/testbed.kubeconfig kubectl get nodes
```

Before installing the Helm chart with `crds.enabled=true` on a shared cluster,
check CRD ownership:

```sh
make helm-crd-check
```

Use `make helm-adopt-crds` only for an intentional release migration.

Helm install and cleanup is implemented behind the repo Make targets:

```sh
make helm-install
make helm-uninstall
```

When `HELM_VALUES` names a values file, that file owns component image values;
Makefile-generated image defaults do not override it. An exact component image
can still be overridden explicitly through `OPERATOR_IMG`, `NODE_AGENT_IMG`,
`PROFILER_IMG`, or `QUERY_IMG` in the command environment or Make command
line.

`make helm-uninstall` treats runtime cleanup as CHILL's responsibility: it
deletes the root `ChillSystem` and waits for finalizers before calling Helm
with hooks disabled. The chart hook remains only as a safety net for direct
`helm uninstall`.

`make helm-purge-crds` is intentionally guarded and requires
`CONFIRM_PURGE_CRDS=chill`.

## Recipe-driven roll-forward deployment

`hack/fast-deploy.sh` runs `hack/deploy/main.py`:

```sh
hack/fast-deploy.sh run /absolute/private/recipe.json --dry-run
hack/fast-deploy.sh run /absolute/private/recipe.json --inspect --evidence /absolute/private/new-inspection
hack/fast-deploy.sh run /absolute/private/recipe.json --execute --evidence /absolute/private/new-deployment
```

Default and `--dry-run` print a local plan. They perform no cluster or registry
access, image build, lock acquisition or persistent evidence write. An optional
coverage projector compiles locally. `--inspect` reads the specified cluster and
renders its live values with the pinned source chart; it performs no custom phase
commands, publication or cluster mutation. Inspect and execute require a new
evidence directory. Only `--execute` permits publication and mutation.
`--source SHA` overrides the recipe source.

Failure stops execution and retains partial evidence. Review a fresh roll-forward
with a new evidence directory. There is no automatic rollback, resume or retry.
Live operation requires separate authorization and the
[operations safety rules](../docs/operations/ops-agent-guide.md).

### Recipe inputs

Set a reviewed full source commit, kubeconfig, context, namespace, release,
registry, `apiEnvironment`, positive current `expectedRevision`, positive
`minFreeBytes`, and a nonempty `images` selection. Source and separately pinned
`toolSources` are archived from Git. The selected release must match the reviewed
revision at each checkpoint and have status `deployed` or `failed`.

The private API environment supplies `d2.apiBase`, `d2.caFile` and `d2.tokenFile`.
Keep credentials out of recipe values, build arguments, argv and publications.
TLS CA and hostname validation remain enabled; redirects are prohibited.
Authentication follows actual API responses. Token expiry estimates do not
replace authentication.

Python 3, PyYAML, Git and tar are required. The selected phases also use Helm,
kubectl, curl and Docker buildx. Explicitly set `registryScheme: "http"` for an
HTTP registry. Existing tool configuration supplies credentials.

Build, Helm upgrade and readiness budgets are `buildSeconds` (1800),
`upgradeSeconds` (900) and `readinessSeconds` (480). The Helm process allowance is
the upgrade budget plus 30 seconds. `maxDiskUsedPercent` defaults to 85.
Choose disk headroom from the reviewed operation's actual needs.

### Sources, phases, and acceptance

Custom phases are `preflight`, `stage`, `quiesce`, `migrate`, `configure`, `accept`.
Each command has an argv array and positive seconds, for example:

```json
{"argv":["python3","{source}/path/to/reviewed-script.py"],"seconds":120}
```

Replace the example path with a reviewed script in the pinned source. Commands
have no shell interpolation. Substitutions are `{source}`, `{evidence}`,
`{namespace}`, `{release}` and `{tool:NAME}`. A separately versioned command selects
its `toolSources.NAME` commit with `"source": "NAME"`. Current context must match
the recipe before publication and every custom phase. The runner does not
capture token-bearing kubeconfig content.

`stage` writes declared evidence-relative `generatedValues`. The runner freezes
them before final rendering and custom quiesce. Later phases must preserve frozen
inputs and records. State-changing custom phases need explicit acceptance.
Runtime or harness activation needs reviewed stage/accept scripts, or the
built-in `runtimeDeclarations: true` path selecting all six FirstFloor roles.
The built-in path performs approved exact-image probes after publication and
before declaration activation; `runtimeInspectionSeconds` defaults to 3600.
`publicationOnly` explicitly names roles published without activation.

Before GPU probes, the runner derives targets from the selected source, checks
Node Ready and CUDA GPU allocatable capacity, and temporarily excludes affected
nodes. It disables autonomy only for Workloads owning observed GPU allocations.
Only idle or retained CHILL allocations may be removed through the normal public
remove operation, followed by allocation-release observation. Active routing,
accepted requests, ownership/reservation conflicts and external GPU users stop
deployment. Forced Pod deletion and automatic unroute are prohibited.

Temporary settings use path-scoped receipts and fresh ETag/If-Match for restoration
on success or failure. External edits survive; changed tracked paths are skipped
and recorded. Ambiguous writes and restoration conflicts stop execution.
Preparation and restoration each use the readiness budget. Inspect and dry-run
never quiesce serving.

The runner retains live user values, merges explicit source values and upgrades
with Helm `--reset-values`. `replaceValues: true` requires complete explicit values.
Review inspection's `chart-diff.txt` and pass that exact file with
`--chart-diff-approved FILE`. Staged configuration changes beyond trusted image
overlays and validated built-in probe output require separate exact
`--staged-diff-approved FILE` or recipe `stagedDiffApproved`.

Helm/chart owns CRDs. Before chart removal, both the previous release manifest
and live CRD must already contain `helm.sh/resource-policy: keep`. The runner
does not add keep, patch ownership or separately apply CRDs.

The loss guard reads measurement roots and owned native Jobs across all namespaces
at preflight and immediately before upgrade. Incomplete or unfrozen work, pending
cleanup and deleting roots stop ordinary deployment. Terminal dispatch-occupancy
measurements need no `spec.conditions`. The runner preserves blockers without
draining or deleting them. The narrow image-only exception is described below.

### Image-only measurement-preserving boundaries

A dashboard-only update must select exactly `dashboard`, retain live values,
and have no custom phases, values overrides, generated values, tool sources,
runtime declarations or measurement-affecting options. Both node-agent and
profiler workload-observer Kubernetes clients need a fixed nonempty `apiServer`
or explicit `discoverFromDefaultEndpoint: false` in captured values.

Captured manifests, hooks and both source renders must have identical resources
and fields except the dashboard container image in `<release>-dashboard`.
Missing, duplicate, malformed or undecidable objects stop execution. Hook bodies
must be unchanged. Proven dashboard-only upgrades use `--no-hooks`.

The exception preserves all-namespace raw objects, owned Jobs and blockers.
Disk, revision, authentication, backup, chart review, registry, readiness and
acceptance gates still apply. Other changes follow the
[durable stored-contract compatibility decision](../docs/adr/0019-durable-stored-contract-compatibility.md).

### Image selection and reuse

`hack/deploy/images.py` owns Dockerfiles, platform defaults, build arguments and
Helm mappings for these roles:

```text
server profiler dashboard node-agent operator query metric-observer
workload-observer load-generator evaluation-harness
firstfloor firstfloor-cuda firstfloor-l4t-r32 firstfloor-l4t-r35
firstfloor-l4t-r36 firstfloor-rknn
```

Query is a separate image. Runtime and harness roles need explicit activation;
publication alone does not establish installation or execution.

An image item may specify `role`, `platforms`, `buildArgs` or
`prebuilt: "/absolute/private/publication.json"`. Prebuilt selection verifies the
registry artifact and requested platforms and pins the digest without rebuilding.
Recorded build provenance and observed OCI labels remain evidence.

`reusePublication` names a prior publication receipt. The planner compares the
originating Dockerfile, COPY/ADD inputs, Dockerignore, actual arguments, platforms
and pinned external sources against the selected source. Unknown origin,
unavailable commits, dynamic inputs, archive attribute transformations or custom
preflight commands conservatively require rebuilding.

Broad COPY inputs are compared as a whole. Reuse does not detect floating
base-image or package updates; omit reuse for an intentional refresh.
Explicit prebuilt items take precedence. `reusePendingBuilds` names per-role
pending metadata and requires `reusePublication` plus the originating frozen
`image-plan.json`. Changed or unverifiable pending inputs stop execution.

Dry-run prints actions, reasons and input paths. Inspect and execute verify
candidate registry artifacts and platforms. Publication `sourceSHA` records
deployment source; role `sourceSHA` records known build origin. Reuse preserves
digests and labels and still performs required probes and activation.

### Readiness, diagnostics and evidence

Each selected deployed role needs at least one matching nonterminal container.
All matching containers and Pods must be Ready. Deleting and terminal Pods are
ignored. Readiness uses declared image/container identity; restart count and
status `imageID` are recorded without becoming digest-equality gates.
Optional `waitFor` names Deployment/DaemonSet rollout waits.

Coverage startup checks precede profile capture. Explicit `CoverageWarming` and
recognized startup transport errors may wait within a finite readiness deadline;
401/403, other HTTP errors, invalid snapshots and TLS verification failures stop.
Post-upgrade rollout and API readiness share one deadline.

`apiChecks` require HTTP 200 and a body strictly smaller than each `maxBytes`.
`queryService` adds direct unauthenticated 401/authenticated 200 checks through a
private port-forward. `dashboardCheck: true` checks HTTP 200 HTML.
`checkConfiguration: true` builds the source's actual offline server validator
and checks live scheduler input or explicit `schedulerConfigMapFile`.
That file is validation input; a reviewed `configure` phase or prerequisite
must apply external ConfigMap changes.

Profile IDs, counts, batch values, usable coverage and measurement comparisons
are report-only diagnostics. Keep failures and unknown values visible.
Session profile diagnostics retain `power` (idle, awake step, hold), per-batch
`inputSlopeJoules` estimates and ranges, `saturationInputRate`, and `memoryBytes`.
An unidentified estimate remains valid without a point value; its range and
status remain part of preservation diagnostics. Retained-record comparisons
also cover the memory evidence basis and immutable profile provenance.
`expectedProfileCount`, `expectedCoverageUsable` and optional `baselineDirectory`
support reporting; preserve original baseline evidence separately from current
before/after captures. `inspectionCells` and `inspectionRequests` project assessed
snapshots through selected-source production logic; changed native identities or
execution state produce unavailable diagnostics.

`READY` means operational Pods/APIs and required steps completed. Hardware,
browser interaction and scientific acceptance require their own evidence.

Execute holds `/tmp/chill-heavy.lock`; inspect does not. Evidence contains
private recipes, source records, command logs, manifests, hooks, retained objects,
publication receipts, chart/render diffs, API captures, readiness and failures.
Keep it outside the checkout. CLI watch prints phases, image decisions,
heartbeats and completion/evidence paths without dumping argv or credentials.
Ctrl-C stops the executing process group. Timing records live in
`performance.json` and `command-timings.jsonl`.

The runner records builder disk usage before and after builds. Pruning defaults
off. `pruneCompletedBuildCache: true` authorizes unused cache cleanup on the
recipe's CHILL-owned builder after successful build/push. Cleanup failures and
insufficient space stop execution. Other builders, images and volumes remain
outside this cleanup.

## Profiler runtime declarations

Align RunConfig producers and consumers when their stored contract changes.
Use immutable runtime images and obtain `profiling.executionDeclarations` from
exact-image inspection with `hack/runtime-declarations.py`.

Supply per-platform child digest references through `CPU_ARM64_IMAGE`,
`CPU_AMD64_IMAGE`, optional `CUDA_AMD64_IMAGE`, `CUDA_L4T_R32_IMAGE`,
`CUDA_L4T_R35_IMAGE`, `CUDA_L4T_R36_IMAGE` and `RKNN_IMAGE`.
Set `NAMESPACE`, `KUBECTL` and `INSPECT_TIMEOUT_SECONDS` for the reviewed target.
The generator probes CPU once per architecture, CUDA separately per configured
GPU Node, and the RKNN host boundary on its configured Node. Device UUIDs bind
CUDA declarations to the inspected Node.

```sh
# Export actual immutable per-platform image references before this live probe.
hack/runtime-declarations.py > /tmp/runtime-declarations.yaml
```

Merge reviewed declarations and observer images into the deployment values.
Each declaration's image, architecture, packages and verification claim travel
together. Validate the chosen values with:

```sh
make helm-lint helm-template HELM_VALUES=charts/chill/values-profiler.yaml
hack/runtime-declarations.py --self-check
```

## Offline observation tables

`hack/observations/fetch.sh <from-hour> <to-hour> <dir>` accepts UTC hours as
`YYYYMMDDTHH` and downloads one extra hour on each side. Set `CHILL_S3_ENDPOINT`,
`CHILL_S3_BUCKET` and existing AWS credentials. It writes `<dir>/obs`.
Run the SQL with the downloaded directory as working directory:

```sh
# From the repository root; use actual retained data and interval values.
ROOT="$PWD"
(cd "$OBSERVATION_DIR" && duckdb < "$ROOT/hack/observations/e1.sql")
go run ./hack/observations/power \
  -dir "$OBSERVATION_DIR/obs" -settings "$SETTINGS_JSON" -state "$STATE_JSON" \
  -from "$FROM" -to "$TO"
```

The power reducer derives meters from settings and actual Node state. Explicit
`-maximum-gap` requests policy re-evaluation; otherwise recorded policy applies.

`go run ./hack/kappa -mode table -scheduler scheduler.json -rows rows.json`은
오프라인 전체 격자 용량을 계산하고 rows 파일을 출력한다.
`-mode tick -scheduler scheduler.json -repeat 1`은 현재 계획기를 실행한다.
`-mode compare -scheduler scheduler.json -profiles profiles.json -records records.jsonl`은
보관된 결정과 재결정을 비교한다. `-format jsonl`, `-catalog catalog.jsonl`,
`-arrivals arrivals.jsonl`로 생략된 참조와 실제 도착을 복원할 수 있다.
`-mode guard`와 `-next N`은 보관 target을 후속 snapshot에서 신규 접수 기준으로 검사한다.
기존 전환의 소유권과 계속 실행 권한은 유지된다.
`-mode slo -scheduler scheduler.json -workloads plc-anomaly,smolvlm2-vqa`는
지정 Workload의 배치 크기 1 지연을 바탕으로 SLO 후보를 제안한다.

## Experiment controllers

`hack/evaluation/` implements the EvaluationRun/Stage Job adapter.
The controller freezes absolute rates, uint64 decimal-string seeds, Dataset
name/UID references, `settingsPatch`, measurement Node/meter scope, evaluator
image/source identity and generator `nodeName`.

`evaluation.py` dispatches prepare, factor, stage, judge, cleanup-jobs, finish
and publish from immutable controller input. Current stored contracts are
`e5-binding.v4`, `e5-judgment.v4`, `e5-summary.v5` and `failurePolicy: v2`.
Retained raw evidence and publications remain immutable.

The Dataset init container stages evaluation records. Prepare freezes generated
or explicit arrival offsets once in the workspace PVC; producers mount them
read-only and replay without regeneration. Memory and process-shard settings
come from frozen input. Schedule counts and hashes record provenance.

SLO uses exact client full-response elapsed time and the frozen offered
population. Generator quality, SLO, energy, drain, cleanup and publication are
independent outcomes. No-load or unavailable evidence cannot become PASS.
Missing power means UNAVAILABLE; partial coverage retains `observedJoules`,
while `wholePoolJoules` requires complete coverage. Meter scope uses exact
declared Node UID/meter pairs, including idle nodes.

Settings changes use GET ETag, declared patch, PUT If-Match and readback.
Apply conflicts never retry. Restoration touches only paths still equal to
applied values and preserves external edits. One revert conflict permits fresh
read and re-evaluation; another blocks Returned. Job cleanup and restoration
each attempt their owned work even when the other fails.
Cleanup deletes only exact-UID evaluator-owned Jobs and Pods. Returned records
cleanup and restoration; CHILL instances and routes may remain.

Participating-node Pods, health changes and competing generators are retained
as environmental observations. Owner UID determines evaluator-owned writers.
Wrong Workload/Node UID, unsafe mutations, credentials/path scope violations,
evidence overwrite and diagnostic 401/403 are hard failures. Finite drain or
early EOF retains partial facts after stopping owned writers.

### Measurement planning and manual soak

`hack/measure/gen.py --coverage --output-dir /tmp/coverage-plan-out --preflight`
reads coverage, catalog and inventory into a frozen external plan. It does not
create resources. `--pilot` also emits a runtime-family/heavy-model pilot.
The planner config is `hack/measure/coverage-plan.json`; protocol, runtime
declarations and active-path policy must match actual selected conditions.

Current completed requests may be reused; current in-progress requests suppress
duplicates. A failed latest request does not revive an older success.
The named Workload supplies actual SLO and model/preprocessor/Dataset identity.
BEO searches batches automatically with exactly three trials per batch.
CPU request, quota and affinity are neither inherited nor generated.
Retain operating-point settings, elapsed windows and metric padding.

Memory exclusions bind runtime image, execution path and exact worker limit.
Changed or unknown conditions require explicit operator policy review before
request generation. A path-specific rule takes precedence over a class rule.
Worker limits derive from live Node allocatable, configured memory fraction and
installed policy ceiling. Missing inputs stay unconfirmed.
Preserve raw policy evidence; do not hand-edit generated lanes.

```sh
python3 hack/measure/gen.py --coverage --self-check
python3 hack/measure/feeder.py \
  --manifest /tmp/coverage-plan-out/resume-plan.json --stages 1,2 --check-manifest
```

The feeder gets existing requests before creation: completed ones are skipped,
active ones awaited, and failed/refused ones stop the lane. `AlreadyExists`
stops a create race. Approved holds and node handback remain operator-owned.
A running feeder retains its frozen plan.

`hack/measure/capture-progress.py` captures measurement progress. See the
[operations guide](../docs/operations/ops-agent-guide.md).
`hack/measure/smoke/smoke.py NODE OUTDIR WL=PROFILE:DATASET` records preparation,
routing and cleanup in an external directory. Partial or failed reset stops
further mutations while preserving read-only observations.

For an explicitly approved manual soak, disable autonomous Workloads and prepare
two or three Ready instances owned by one Workload:

```sh
python3 hack/measure/soak_ledger.py --self-check
CHILL_EXPERIMENT_CONFIG=/absolute/environment.json \
  python3 hack/measure/soak_ledger.py --workload-uid WORKLOAD_UID \
  --instances INSTANCE_ID_1 INSTANCE_ID_2 --out /external/soak-evidence \
  --duration 1800 --interval 20
```

The soak cycles routing and release, replaces one fixed target, and leaves
prepared instances in place. Violations and incomplete replacement produce
nonzero exit status; retain operation receipts and raw snapshots.

### Offline explicit-load preview

```sh
python3 -B hack/evaluation/demo.py \
  --config hack/evaluation/demo-config.example.json --out /private/new-binding.json
```

This freezes a local load preview. EvaluationRun owns live execution.

Offline regression entry points:

```sh
make test-deploy
python3 -B hack/evaluation/demo.py --self-check
python3 -B hack/evaluation/conductor.py self-check
make test-loadgen
```
