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

`hack/fast-deploy.sh` is the existing entry point, **replaced**, not extended
with a parallel deployment implementation. It is a thin runner for
`hack/deploy/main.py`; the only interface is:

```sh
hack/fast-deploy.sh run /absolute/private/recipe.json --dry-run
hack/fast-deploy.sh run /absolute/private/recipe.json --inspect --evidence /absolute/private/new-inspection
hack/fast-deploy.sh run /absolute/private/recipe.json --execute --evidence /absolute/private/new-deployment
```

The default is local-only dry-run: no cluster/registry access, image build, lock
or persistent evidence writes. An optional coverage projector compiles locally.
`--inspect` reads the explicit cluster and renders its live
values plus the pinned source chart; it never publishes, mutates the cluster,
or executes custom phase commands. Inspect and execute require a **new**
evidence directory. `--execute` alone authorizes publication and mutation;
removing `--dry-run` does not. `--source SHA` overrides the recipe source.
There are no legacy deployment modes, reset, rollback, resume, or compatibility
flags. Failure means STOP, retain evidence, then review a fresh roll-forward.

### Ordinary control update recipe

This local recipe writer uses the actual reviewed source and current deployed
revision, not an example SHA/revision masquerading as approval. Set
`DEPLOY_SOURCE` to the approved full commit, `DEPLOY_REVISION` to the reviewed
current Helm revision, `DEPLOY_RECIPE` to a private JSON output path, and
`DEPLOY_API_ENVIRONMENT` to the existing private environment JSON. These are
operator inputs from the current cutover review, not values to guess.

```sh
python3 - <<'PY'
import json, os
from pathlib import Path
os.umask(0o077)
recipe = {
    "source": os.environ["DEPLOY_SOURCE"],
    "context": "kubernetes-admin@kubernetes",
    "kubeconfig": "/home/genesis1/.kube/config",
    "namespace": "chill-system",
    "release": "chill",
    "registry": "155.230.35.213:5000/chill",
    "registryScheme": "http",
    "apiEnvironment": os.environ["DEPLOY_API_ENVIRONMENT"],
    "expectedRevision": int(os.environ["DEPLOY_REVISION"]),
    "minFreeBytes": 310000000000,
    "images": ["server"],
    "values": [],
    "replaceValues": False,
    "generatedValues": [],
    "toolSources": {},
    "commands": {},
    "allowedLostProfileIDs": [],
    "apiChecks": []
}
with Path(os.environ["DEPLOY_RECIPE"]).open("x") as out:
    json.dump(recipe, out, indent=2)
    out.write("\n")
PY
```

The target above is the known testbed, **not** permission to deploy there.
First live use is only after the current cutover, with deployment session
`main` as the sole live actor. This change performs no live operation.

### Sources, phases, and acceptance

The fixed command phases are `preflight`, `stage`, `quiesce`, `migrate`,
`configure`, `accept`. Each optional command is
`{"argv": ["python3", "{source}/path/to/reviewed-script.py"], "seconds": 120}`;
the path must identify a real reviewed script in the selected commit, not a
generic migration inferred by the tool. There is no shell interpolation.
Supported substitutions are `{source}`, `{evidence}`, `{namespace}`,
`{release}`, and `{tool:NAME}`. For separately versioned tooling, freeze its
full Git commit in `toolSources.NAME`, select `"source": "NAME"` on the
command, and invoke its script through `{tool:NAME}`. This separates deployment
source from migration/tool source rather than executing a mutable checkout.
The current context must match the recipe context before publication and before
every custom phase. This check does not flatten or capture token-bearing
kubeconfig content.

`stage` creates explicitly listed `generatedValues` files relative to the
new evidence directory. The runner freezes those inputs before final rendering
and **before quiesce**; later phases must not rewrite frozen inputs or records.
Use these inputs for new runtime declarations and harness new-input values.
Runtime/harness activation requires explicit stage/generatedValues and accept
scripts, or the fixed `runtimeDeclarations: true` implementation for all six
FirstFloor roles together. The latter runs the existing inspector in a separate
fixed, approved exact-image probe stage after publication, before measured
declaration activation (`runtimeInspectionSeconds`, default 3600). It does not
guess probes or treat publication as readiness. Retain partial runtime,
profile and measurement diagnostics without falling back to an older successful
result; do not invent a profile-specific deployment blocker.
For future deliberately unactivated builds, `publicationOnly` must explicitly
list those selected roles. It is not allowed to replace the required activation
in this first live use. State-changing quiesce/migrate/configure commands
require explicit acceptance.

The execution gates are explicit target/revision and disk/token preflight,
private backups, chart review, publication with registry provenance checks,
stage/frozen final render and server dry-run, quiesce/migrate, configure,
Helm update, Pod readiness, and acceptance. Helm/chart is the sole CRD owner:
there is no separate SSA CRD apply/wait phase or compatibility field.
Before removing a CRD from the chart, both the previous Helm release manifest
and live CRD must already carry `helm.sh/resource-policy: keep`. This is a
data-preservation gate; the tool never patches ownership or adds keep
automatically. The default uses
live **user** values plus explicit source values with Helm `--reset-values`;
it is not `--reuse-values` and does not freeze computed `--all` defaults.
`replaceValues: true` instead supplies complete explicit values.
Unapproved chart drift stops: review inspection's `chart-diff.txt`,
then pass that exact reviewed file with `--chart-diff-approved FILE`.
An old SHA, old revision, or a different diff is not approval.
Generated-values changes have a separate gate: compare the final render against
the trusted verified-image overlay plus validated built-in runtime-probe output.
Any other configuration change requires an exact separate
`--staged-diff-approved FILE` (or recipe `stagedDiffApproved`) before quiesce.
Base source-chart approval does not approve staged configuration changes;
dynamic probe output is validated by the built-in path, not an arbitrary
override.

`images` is an explicit selection, never automatic change detection. The
source role catalog has all 16 roles:
`server`, `profiler`, `dashboard`, `node-agent`, `operator`, `query`,
`metric-observer`, `workload-observer`, `load-generator`, `evaluation-harness`,
`firstfloor`, `firstfloor-cuda`, `firstfloor-l4t-r32`, `firstfloor-l4t-r35`,
`firstfloor-l4t-r36`, `firstfloor-rknn`. Dockerfiles, variant build arguments,
platform defaults and Helm mappings come from `hack/deploy/images.py`.
In particular Query is a separate upstream image; updating server does not
update Query. Runtime/harness roles have no inferred Helm image mapping.
Per-image objects may specify `role`, `platforms`, `buildArgs`, or
`prebuilt: "/absolute/private/publication.json"` instead of a role string.
Prebuilt receipts must carry matching `sourceSHA` and role provenance:
`dockerfile`, `buildArgs`, `platforms`, `digest`, `platformDigests`, and
`platformReceipts`. Execution verifies registry manifests/platform configs and
OCI source revision before using digest-pinned images; it does not rebuild
prebuilt inputs. Credentials do not belong in recipes, argv, or build args.

Authenticated API checks and operational readiness are deployment gates;
profile IDs/counts, numeric batch values, usable coverage and measurement
comparisons are **report-only diagnostics**, not experiment PASS or reasons
alone to reject an operational deployment. Keep diagnostic `passed: false`
and errors visible. `expectedProfileCount` and `expectedCoverageUsable` are
report targets, not rejection knobs. Optional `baselineDirectory` points to
the original frozen baseline: report its 259 profiles and the 17-profile loss
separately from the current 242-profile before/after comparison; never rewrite
historical raw evidence or hide the difference behind an older success.
Retain strict current-before/current-after records separately from the original
frozen-baseline four-criterion report with its approved 17-profile loss.
Unknown coverage counts are `null` with source-incomplete diagnostics, not
guessed zeroes or deployment blockers.
There is no new allow-flag framework. Actual data/backup loss, auth/token,
security failures and CLI errors remain blocking.

Default A selects the latest cell by `measuredAt` with a deterministic tie;
the operator's existing replacement annotation has priority. Failed/partial
measurement results remain visible rather than selecting an old success.

For the first transition, `inspectionCells` names the retained, individually
assessed variant array; `inspectionRequests` is its companion native BEO
snapshot, also used for offline dry-run. `--inspect` reads fresh native BEO
annotations/conditions plus current public profiles/coverage. If native
identities or execution state changed since that snapshot, projection is
unavailable (null counts/selections), not an old-success fallback or a deploy
blocker; refresh the assessed inputs. Annotation-only changes still apply.
The local Go adapter uses
the selected application source's production selection/assessment functions
through Go's file overlay; it neither changes that archive nor copies new
declarations into historical evidence. This is a conditional projection over
the named assessment snapshot, not fresh raw revalidation or hardware/runtime
equivalence. Requests without assessed variants and source incompleteness stay visible.
Additional `apiChecks` use objects such as
`{"path": "/api/v1/scheduler/state", "maxBytes": 1048576}` and require HTTP
200 with body size strictly **less than** `maxBytes`. The existing environment's
`d2.apiBase`, `d2.caFile`, and `d2.tokenFile` supply HTTPS/CA/Bearer auth.
`READY` means operational Pods/APIs and required steps completed, not browser,
hardware, campaign or scientific-result acceptance.

The corrected runbook zero-work gate reads the five measurement CR kinds and
owned native Jobs across all namespaces at preflight and immediately before
upgrade. Terminal DOMs need no `spec.conditions`. Incomplete/unfrozen work,
pending cleanup and deleting roots stop deployment without draining/deleting
anything. This prevents measurement loss, not low-quality science.
Runtime activation also checks source-defined probe targets at preflight and
immediately before probing: Ready must be True; CUDA targets need at least one
allocatable GPU. No retry, node repair or foreign-Pod action is automatic.

The current first live use is the actual dashboard ten-role selection:
`server`, `profiler`, `query`, `dashboard`, and all six FirstFloor roles. It includes
FirstFloor build/publication → separate fixed approved exact-image probes →
measured declaration activation, not publication-only receipts.
`queryService: "chill-query"` adds direct unauthenticated 401/authenticated 200
checks through a private port-forward. `dashboardCheck: true` requires HTTP 200
HTML, not a browser interaction test. Neither substitutes for hardware or
measurement acceptance.
The tracked first-use recipe is
`hack/deploy/plans/dashboard-reads-20261103.json`; its application source is
pinned to 1b81e0d5 and may be explicitly changed with approved `--source`.
The declaration records main's confirmed post-cutover
revision 178 and control-315 tag; a later live revision requires a new review.

Keep all evidence private: live values, manifests, retained objects, commands,
publication, renders, before/after API captures and failure records can contain
operational data. Execute shares `/tmp/chill-heavy.lock`; inspect does not.
The disk gates require `minFreeBytes` and `maxDiskUsedPercent` (default 85).
Builds record `docker buildx du` before/after on their actual builders; the
runner does not prune unrelated or supposedly local images/cache/data.
Firstfloor installs apt before Python-package arguments and shares the pinned
Python dependency layer between CPU and RKNN; optional extras are installed
afterward with the same pins enforced. This uses only the existing BuildKit
layer cache: GC can still evict it, and cold vendor base pulls/installations
are unchanged. No quantitative speedup is claimed without measured builds.
`performance.json` and `command-timings.jsonl` retain measured stage/command durations. Coverage is checked
before profiles to avoid repeated profile captures while warming; this is not
evidence of a global speedup.
See [the operations guide](../docs/operations/ops-agent-guide.md) for operational
limits. Repository-local regression checks (not run by writing these docs):
`python3 -B hack/deploy/test_deploy.py` and
`python3 -B hack/deploy/test_acceptance.py`. `make test-deploy` is the
repository Make entry point and is included in `make test`.

Optional `checkConfiguration: true` runs the fixed `configuration.py` check:
build the selected source's actual `chill-server` offline validator once, then
validate the selected external scheduler ConfigMap through the real CLI flags
and live JSON, or explicit `schedulerConfigMapFile`. The dashboard first-use
recipe enables this check. Invalid actual configuration/CLI execution blocks;
it is not a scientific diagnostic. Prebuilt evaluation-round deployment does
not set it and introduces no default Go build.

Optional `waitFor` explicitly names Deployment/DaemonSet resources whose
rollout status must complete within the readiness budget. The first-use recipe
includes operator, profiler and three DaemonSets. Actual phase/command timings
can expose those waits; no performance result is inferred from configuration.


## Profiler runtime declarations

Rebuild all seven runtime platform variants and both observer images from one
committed source whenever the RunConfig contract changes; updating only the
profiler leaves measurement Jobs unable to decode its configuration. Replace
the complete declaration list and observer digests in both `values-profiler.yaml`
and `values-operating.yaml`, then deploy a profiling-only values overlay so the
live server and dashboard images remain unchanged.

`charts/chill/values-profiler.yaml` takes `profiling.executionDeclarations`
only as exact-image inspection output. After the firstfloor runtime images are
pushed, list each per-platform child digest and run the generator against the
testbed:

```sh
docker manifest inspect --insecure 155.230.35.213:5000/chill/chill-firstfloor:TAG \
  | jq -r '.manifests[] | "\(.platform.os)/\(.platform.architecture) \(.digest)"'
CPU_ARM64_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CPU_AMD64_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_AMD64_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_L4T_R32_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_L4T_R35_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
CUDA_L4T_R36_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
RKNN_IMAGE=155.230.35.213:5000/chill/chill-firstfloor@sha256:... \
  hack/runtime-declarations.py > /tmp/runtime-declarations.yaml
```

The tags are `TAG` for CPU (`linux/arm64` and `linux/amd64` children),
`TAG-cuda`, `TAG-l4t-r32`, `TAG-l4t-r35`, `TAG-l4t-r36`, and `TAG-rknn`. The generator runs
one probe Pod per image in `chill-system` on a Node of the target class, with
the measurement Job's user, groups, privilege, and `nvidia.com/gpu` request. It
builds each payload from the architecture, ONNX Runtime providers, and package
versions read inside the image, reads the GPU UUID through the CUDA driver on
every eligible GPU Node (`jetson-nano`, `jetsono`, `jetsonx`, `orin-nano`, `genesis3`), and
reads the RKNPU driver version and device-tree token on `orangepi`. `genesis2` is
temporarily omitted because GPU allocatable is 0 (`gpu-feature-discovery` CrashLoop);
restore its probe after GPU recovery. With all image variables set, this yields
14 declarations instead of 15. Every
DeviceClass receives one class-level CPU declaration; each GPU Node receives its
own CUDA declaration with `node` set, because the GPU UUID names that Node's
device, and `orangepi-5-plus-16g` adds its RKNN declaration, each when the image
variable is set. `genesis1` runs the transition observer and has no metric
source, so it is not probed.

Replace the `executionDeclarations` list in the values file with the output's
list and validate the render with
`make helm-lint helm-template HELM_VALUES=charts/chill/values-profiler.yaml`.
`hack/runtime-declarations.py --self-check` checks the payload builder offline.

## Offline observation tables

`hack/observations/` builds measurement tables from the hourly S3 observation segments: `fetch.sh <from-hour> <to-hour> <dir>` downloads them plus one hour on each side, `duckdb < hack/observations/e1.sql` in `<dir>` gives the E1 request and Tick tables, and `go run ./hack/observations/power --dir <dir>/obs --scheduler-config <scheduler ConfigMap value> --from --to` gives whole and active POOL energy with the ChillServer reducer.

`go run ./hack/kappa` prototypes the per-option capacity table with the existing scheduler replay: `-mode table` builds kappa for every option and Workload SLO, `-mode validate` replays chosen options at 0.8 to 1.2 kappa, and `-mode compare` replays recorded decisions with the kappa-sum node DP planner (`-sweep` adds the replay planner at swept loads). Results: `docs/design/serving/capacity-table-evidence.md`.

`go run ./hack/kappa -mode slo -scheduler <GET /api/v1/scheduler dump> -workloads <names>` proposes an SLO target per named Workload: per Node the lowest p99 b1 latency (execution + dispatch occupancy + `completionMarginMS`), then the target that passes at least half the Nodes and fails the two slowest, at the widest latency gap. It builds the Workload's rows at that target with p95 and p99 and prints the largest kappa per Node, the kappa sum, whether the rule holds on kappa, and each table's build time. A Workload need not be autonomous; any Workload with the same model, preprocessor and dataset selects the same options. `go test ./hack/kappa` checks the proposal on synthetic Nodes.

## Experiment controllers

`hack/evaluation/` implements the black-box EvaluationRun/Stage Job adapter.
The controller freezes absolute rates, inline uint64 decimal-string seeds,
Dataset name/UID references, public settingsPatch, measurement node/meter scope,
evaluator image/source identity and generator nodeName. There is no scientific
host input bundle, calibration or CHILL topology restore path.

`evaluation.py` executes prepare/factor/stage/judge/cleanup-jobs/finish/publish
from the controller's immutable spec/execution/config ConfigMap. Public settings
comparisons use GET/PUT `/api/v1/scheduler/settings`: GET ETag, merge the declared
settingsPatch, PUT with If-Match, and readback. Apply conflicts never retry.
Revert restores only paths still equal to our applied values; external changes
are recorded and skipped. One 409 revert conflict permits one fresh GET and
re-evaluation; a second blocks Returned. Each PUT records If-Match, status and
response ETag alongside request/response/readback and apply/revert timestamps.
Applied path values come from the PUT response's canonical settings, including
defaults materialized from null patches. Newly created objects are tracked by
member: revert removes only evaluator-added members, preserves operator-added
members, and removes an originally absent parent only when it becomes empty.

The generator Job is assembled directly. Its Dataset init container copies the
carrier's evaluation directory to `/evaluation`; Preprocessor input names
select the existing record fields. Actual record order/IDs/bytes/hash are Ready
evidence, not host inputs. Every generator Pod uses the frozen
`loadGenerator.nodeName` verbatim. Memory and shard/barrier declarations remain.

`hack/measure/` remains separate measurement tooling. Keep operational
credentials and evidence outside the checkout; never copy token contents into
publications.

`hack/measure/soak_ledger.py` is a CampaignOps-only manual ledger soak. Disable
all autonomous workloads and manually prepare 2–3 Ready instances owned by one
Workload before running:

```sh
python3 hack/measure/soak_ledger.py --self-check
CHILL_EXPERIMENT_CONFIG=/absolute/environment.json \
  python3 hack/measure/soak_ledger.py --workload-uid WORKLOAD_UID \
  --instances INSTANCE_ID_1 INSTANCE_ID_2 --out /external/soak-evidence \
  --duration 1800 --interval 20
```

The runner uses the existing authenticated public API transport with `d2`
endpoint/CA/token configuration and `measure.namespace` for serving Pods.
It cycles route/unroute/release, removes and re-prepares one
instance halfway through, and leaves the manually prepared instances in place.
The replacement target stays fixed: after its successful removal, a refused
prepare retries only preparation, never removal of another original instance.
It compares all scheduler instance Pod UIDs with the serving-label Pod list;
each mismatching UID must persist into the next cycle before it is reported.
After each completed ACK, the runner waits up to 129 seconds for a strictly
newer scheduler snapshot before another operation or invariant check; POOL
observations must also be newer than the ACK. The server wire timestamps are
`snapshot.at` and ACK `endedAt` (`observedAt`/`completedAt` are also accepted).
RFC3339Nano timestamps use the existing D2 parser for Python 3.8 compatibility.
Replacement reads the live instance's `profile.id` or retained `profileID`.
Ready flicker requires Pod Ready with no native failure/reason; otherwise the
transition is counted separately as `nativeUnavailable`, not a violation.
Removed-ID reappearance in fresh state or POOL is an immediate violation.
Operation request/ACK files, raw violation snapshots and
`summary.json` remain outside the checkout; violations do not stop the soak.
Exit status is nonzero if any violation or incomplete replacement was recorded.


Evaluation observes requests and evaluator-owned client cgroups through the
authenticated raw API and meter energy through the public power endpoint.
SLO uses HTTP receipt-to-final elapsedNanos and the complete frozen offered
population. Generator lateness is separate. Meter energy is the sum of only
declared Node UID + meter matches over the same from/to, including idle nodes;
coverage uses those meters' source identities, boundary continuity and gaps.
Scheduler membership/history or active-pool integrals are not validity gates.

Ready/GO and exact created Pod/container counters are retained. Cleanup deletes
only exact-UID evaluator-owned Jobs/Pods and restores the changed settings
paths; Returned does not mean CHILL instances/routes are empty. Historical
evidence and publications remain immutable and are not reinterpreted.
For measurement preparation, `hack/measure/gen.py --output-dir` takes a fresh
external directory under the current measurement campaign.
`hack/measure/phase-a/measurement-generalization-20261024/post-d2-25-20261025/prepare.py`
takes `--output-dir` and supports `--check` and `--refresh-timing`.
`hack/measure/feeder.py --manifest /path/to/manifest.json --stages 7
--check-manifest` checks the generated manifest offline. Positional rerun and
unstaged lane modes are removed; approved holds, terminal checks and Node handback
still gate each lane. The
`meter-cadence/analyze.py` helper takes `--input-dir` for retained telemetry
and `--output-dir` for its external tables.

`hack/measure/gen.py --coverage --output-dir /tmp/coverage-plan-out --preflight`
reads live coverage/catalog/inventory and retains the inputs and frozen
`hack/measure/coverage-plan.json` alongside BEO lane manifests; it never creates
cluster resources. Compatible unmeasured, running, problem and TargetBatchMissing
insufficient cells are considered; unknown support and cells without an eligible
Node are listed separately. The config excludes genesis1 (testbed) and genesis2
(hardware failure), so RTX2080Ti cells use genesis3.
A cell whose only Unknown support reason is `RuntimeDeclarationMissing` is
included if the first eligible Node has a matching runtime execution declaration
and the live profiler has that class/path policy. The artifact and declaration
must match the class architecture and path runtime family/backend; declarations
for another Node do not count. This recovers genesis3's CUDA amd64 cells without
admitting impossible architecture/backend combinations. `node-declared-cells.json`
records the selected Node per recovered cell; other Unknown cells stay excluded.
Coverage resolves the current runtime image for the selected class, execution
path's runtime family/backend and eligible Node from `profiling.executionDeclarations`
in `runtimeValues` (`charts/chill/values-operating.yaml`, read with PyYAML).
Target selection and request reuse call the same explicit comparison:
`runtimeImage`, `curveSchema` and `evidenceSchema`. Completed results must have
curve-plan v1alpha10 and finalized execution-evidence v1alpha16 (shared CPU
RunConfig v1alpha18). Their runtime image comes from the verified public profile
condition, not from a historical cell annotation. A current completed measurement
is reused; a current in-progress request suppresses duplicate generation; a
different runtime/version or Failed request does not suppress a new request.
Pending requests record those three fields in
`chill.dacs.io/coverage-execution-conditions`; absent/old annotations are not a
shared-execution claim. There is no old annotation reader or CPU sizing/matching.
CPU-controlled evidence is not promoted by merely ignoring its CPU fields.
No result is deleted. `inputs/current-runtime-images.json`,
`inputs/required-execution-conditions.json`, stale/current-runtime lists and
`remeasurement-cells.json` explain selection. Source SHA-256 and the deterministic
request-name hash are provenance/naming, not evidence equality gates. Historical
results remain in the captured inputs, but are not timing bounds for different conditions.
Protocol inheritance follows the dashboard's exact request → exact profile →
latest successful same-model → same-path order, but never inherits CPU request,
quota or affinity, even on the same Node. Catalog contracts resolve inputs.
Profiling and serving both use runtime-default shared CPU execution. No CPU
request, quota or affinity is inherited or generated.
Three independent sessions, one warmup iteration, all declared artifact batches
(omit `spec.batchSizes`), or the configured powers-of-two planner batch set
bounded by runtime maximum when undeclared are used.
Every model and included batch uses the same config `windowSeconds` active
window (default 60 seconds), independently repeated three times. Start with
the full planner batch set, then retain a batch only if its same-cell
measured time allows `floor(windowSeconds / secondsPerCall) >= minCallsInWindow`
(default one call). The conservative time is the slowest measurement of that
exact model/class/path/power-mode/batch, including client overhead, whose runtime image and
curve/evidence versions match the cell's required execution conditions through
the same `coverage_condition_matches` predicate as target/reuse selection.
`timings.json` retains those execution conditions per timing; old CPU-controlled
curve/evidence versions are not timing evidence for shared execution. Without a
matching timing the batch remains included unless a smaller batch was already
excluded by a matching timing. Once a matching batch fails the threshold, all
larger batches in the same cell are excluded, even with unknown or apparently
faster timings. `excludedFromBatch` records the first failure. Another model,
device class, path, power mode, runtime or evidence version never starts this monotone exclusion.
`excluded-batches.json` records “창 안 유의미 추론 불가”; if no batches survive,
`excluded-cells.json` records the cell and no BEO is emitted. A partially
filtered artifact-declared set also excludes the cell rather than violating the
server's indivisible declaration contract. There is no SLO-based shortcut,
second phase, sample-count target, dynamic window extension, or window cap.

`coverage-plan.v2` is a **single cutover** from the old class-wide integer map to
`memoryMaxBatch` rule records; the generator rejects v1 and has no dual reader.
Each record preserves the old `maxBatch` (including 0), its original model/class/
optional-path selector, observed runtime image/path/exact worker memory limit,
raw observations, source references and `basis` (`observed-failure` versus
`conservative-exclusion`). A path-specific rule takes precedence over a class
rule, as before. `null` means **조건 미상**, not a wildcard execution condition.
Only all three known, matching conditions apply the old cap as
“측정 불가(메모리)”. A changed or unknown condition instead holds the cell with
“메모리 제외 정책 재검토 필요” (`conditions-changed`/`conditions-unknown` in
`batchEstimates[].memoryPolicy`). This does not declare a new combination feasible,
clear an old value, or automatically remeasure it; an operator must explicitly
review/change the next policy before an affected request can be emitted.
`excluded-batches.json`/`excluded-cells.json` retain the whole rule and current
conditions; `memoryPolicyReviewCellCount` separates these holds from confirmed
same-condition exclusions. Existing campaigns/feeder frozen plans are untouched.

`runtimeImage` and `memoryLimitBytes` are **not supported top-level coverage-cell,
coverage query, or BEO-spec overrides**. Runtime identity is a public variant/
profile condition; the generator resolves required identity from execution
declarations. `ExecutionPolicySpec.runtimeMemoryLimitBytes` is an upper envelope,
selected from the installed `chill-profiler-facts` active-path policy (exact class
before `*`) and retained in `inputs/execution-policies.json`. The compared limit
mirrors the worker: `min(policyLimit, int(float(Node allocatable bytes) * measurementMemoryFraction))`.
Allocatable is read from the actual Node with the inventory UID checked; the
configured fraction is read from the installed profiler Deployment arguments
bound to that same facts ConfigMap. `inputs/memory-limit-inputs.json` retains
both snapshots. Positive fractional allocatable bytes are rounded up as in
Kubernetes `Quantity.Value()`, before the worker's float64 multiply/truncation.
Missing/ambiguous/invalid inputs produce a null limit and
`memoryLimitStatus: unconfirmed` (확정 불가), never a fallback to the policy ceiling.
Board RAM and rounded GiB reports are not substitutes for actual Node allocatable.
For the retained Xavier `6915992Ki`, policy ceiling 16GiB and configured fraction
0.8, this gives **5665580646 bytes**, preserving the same-condition cap rather
than incorrectly reporting a change from a 16GiB worker limit.
The internal `coverage_batch_plan(..., execution_conditions=...)` parameter makes
these read-only planning facts explicit; no memory/runtime override is sent in a BEO.

Retained Smol rules (all for `smolvlm2-500m-video-instruct`):

| Original selector | maxBatch | Evidence runtime / path / worker limit | Basis and application |
|---|---:|---|---|
| lattepanda class | 1 | `3a5a3e0b…a4b6` / CPU FP32 amd64 / exact bytes unknown (reported 5.98Gi) | c9cc020c b2 OOMKilled/137; curve efbfea1d + retained failure summary identify runtime/path; class-wide expansion remains review-only |
| raspberry-pi class | 1 | unknown / unknown / unknown | c9cc020c precaution copied from lattepanda, not Pi OOM evidence; review-only |
| xavier class | 1 | unknown / unknown / unknown | c9cc020c precaution; review-only outside specific paths below |
| orin-nano class | 1 | unknown / unknown / unknown | c9cc020c precaution; review-only outside specific CUDA path |
| RTX2080Ti class | 2 | `bc690b8c…e4edc` / CUDA amd64 / limit unknown | 89a12080 b4 warmup Softmax RUNTIME_EXCEPTION; curve d3c0089e identifies runtime/path; no proof of host OOM, review-only |
| orin-nano / CUDA VLM arm64 | 0 | `5d1170a3…4602` / same path / limit unknown | f7394f55; retained Run a74c0aba reports NvMap/FusedMatMul allocation failure, worker exit 3; image from immutable curve 905024f5; review-only until limit established |
| xavier / CPU FP32 arm64 | 0 | `50684bd2…7d73` / same path / **5665580646 bytes** | 89de4f85; curve 4eb27c55 + Run 77e2b181 raw OOMKilled/limit; applies only to this exact condition, otherwise review |
| xavier / CUDA VLM arm64 | 0 | `21609797…e631` / same path / limit unknown | 5dc8abb2; curve b7fe27fa + Run 2bcf73ad confirm `start runner: EOF`, not OOM; conservative review-only exclusion |

Full image identities, native Run names/UIDs, immutable curve keys and raw failure
classification are retained in the policy. The queried xavier CPU/orin CUDA
failed Runs had no finalized `profiling-runs/<uid>/execution.json` object.
Runtime images above were recovered from existing immutable campaign curve plans,
never reconstructed from today's runtime defaults. Original lattepanda/RTX
failure observations are retained from git and saved coverage summaries; rounded
limits and missing native/frozen worker envelopes remain unknown.

The active metric observation deadline includes the last in-flight call:
`windowSeconds + ceil(max(included batch call bounds)) + inherited metric padding`.
Each known call bound is the slowest same-condition model/class/path/power-mode/batch estimate multiplied
by config `callDurationEstimateMultiplier` (2); unknown timings use config
`unknownCallDurationSeconds` (600). Excluded batches do not inflate the deadline.
This changes observer/worker budgets, not the runtime's 60-second start window,
actual power integration window, or `durationToleranceSeconds`: tolerance only
allows measurement undershoot and must remain finite, nonnegative and below
the measurement duration. Both tolerance and metric duration are frozen in
RunConfig/curve identity; no storage schema or acceptance rule changes.
The policy is part of the shared plan hash, so changing these settings renames
every newly generated cell, not just slow cells; existing requests are skipped
only under the explicit execution-condition comparison above, never rewritten.
A running feeder keeps its frozen plan until the operator explicitly replaces it.
Idle uses the inherited fixed window or config `idleSeconds` (10 seconds).
Reports state: “셀별 정밀도(CI)는 측정 후 기록하며 목표치가 아님”.
Estimates include active windows, three shared-idle windows and their settling
time, plus the configured 20-second overhead per active or idle run. Variable
load/warmup/polling time is not included. MNV/PLC precede Smol, with power modes
grouped within each priority tier. `laneLastExecutionPaths` and the model-specific
`laneLastModelExecutionPaths` stably move CUDA VLM and Smol CUDA amd64 cells to
the end of their lane tier, without delaying MNV/PLC on the shared amd64 path.
All other cells keep their relative order. Order changes belong in the generator
config, never hand-edited lane files. Canary/holds remain operator-owned
feeder-hold policy.
Check the existing feeder format offline:
`hack/measure/feeder.py --manifest /tmp/coverage-plan-out/resume-plan.json
--stages 1,2 --check-manifest`. `gen.py --coverage --self-check` is offline and
exercises `generate_coverage` end to end with read-only API/kubectl fixtures:
current completion/reuse, pending deduplication, obsolete runtime/version,
Failed replanning and same/changed/unknown memory conditions. The same consumer
check also runs under `python3 -m unittest discover -s hack/measure/smoke -p 'test_*.py'`.
Add `--pilot` to cover each planned runtime family plus the heaviest model on its
smallest-memory planned node; repeated nodes remain ordered lanes. Documents
reuse their full-plan BEO names. Cell annotation or `coverage-` model/class/path/
mode matching is used only after execution conditions match; reused names/statuses
remain in `existing-cells.json`.
Feeder GETs existing BEOs before creation: successful
terminal requests are skipped, active ones are awaited, and failed/refused ones
stop the lane. A create-race `AlreadyExists` remains a fatal create error;
feeder never applies or overwrites an existing BEO.
Optional preflight checks one sample per model. Its audited route calls
`client.Reader` and the pure curve-plan projector, discarding transitions
(`internal/queryapi/kube/batch_energy_optimization_preflight_query.go`,
`cmd/chill-query/run.go`); Query API assembly only reads catalog/evidence. It does
not submit a BEO or execute an actuator.

`hack/measure/smoke/smoke.py NODE OUTDIR WL=PROFILE:DATASET` retains prepare,
route and cleanup-reset requests/results in an external output directory.
A failed or partial reset propagates as an error and triggers no further
mutations; read-only workload observations (`observations-WL.json`) and
`scheduler-actions.json` are still collected. The offline regression is
`python3 -m unittest discover -s hack/measure/smoke -p 'test_smoke.py'`.

Record the repository harness commit alongside the actual control-image
source and immutable image identities in operational execution metadata.
CampaignOps switches active callers after the current conductor exits and
confirms that every consumer uses repository entrypoints. Historical receipts
and raw records retain their original provenance. At the approved evaluation pilot
stage, run the existing `smokeSeconds` lifecycle through cleanup with the
repository controller, retain its logs and handback proof, and exclude that
smoke from scientific results before full evaluation.

### Offline explicit-load preview

`python3 -B hack/evaluation/demo.py --config hack/evaluation/demo-config.example.json --out /private/new-binding.json`
previews the declared absolute load and deterministic trace only; it does not
submit a run or claim measurement. Live execution belongs to EvaluationRun.

Offline regressions (no cluster writes or image builds):

```sh
python3 -B hack/evaluation/test_evaluation.py
python3 -B hack/evaluation/demo.py --self-check
python3 -B hack/evaluation/conductor.py self-check
PYTHONPATH=load-generator/src python3 -B -m unittest discover -s load-generator/tests
```

Stored contracts are binding v3, inputs/snapshot v2alpha1 and judgment v2;
old readers and local orchestration are removed rather than kept as aliases.

