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
and **before custom quiesce**; later phases must not rewrite frozen inputs or records.
Use these inputs for new runtime declarations and harness new-input values.
Runtime/harness activation requires explicit stage/generatedValues and accept
scripts, or the fixed `runtimeDeclarations: true` implementation for all six
FirstFloor roles together. The latter runs the existing inspector in a separate
fixed, approved exact-image probe stage after publication, before measured
declaration activation (`runtimeInspectionSeconds`, default 3600). It does not
guess probes or treat publication as readiness. Retain partial runtime,
profile and measurement diagnostics without falling back to an older successful
result; do not invent a profile-specific deployment blocker.

Before these GPU probes, the runner derives GPU nodes from the selected source
inspector, temporarily excludes them and disables autonomy only for Workloads
owning their observed GPU allocations. **Only idle/retained CHILL allocations
are removed automatically**, through the normal public remove operation and a
subsequent Kubernetes allocation-release observation. Active routing, accepted
requests, conflicting ownership/reservations and external GPU consumers stop
deployment; there is no automatic unroute, forced Pod deletion or probe bypass.
Raw state, Pods, operation responses and settings receipts remain under
`runtime-quiescence/` in the new deployment evidence directory.

Temporary settings are restored after probing, on success or failure, using the
existing path-scoped settings receipt and fresh ETag/If-Match. Unrelated user
edits survive; externally changed tracked paths are skipped and recorded rather
than overwritten. Ambiguous writes and restoration conflicts remain explicit
STOPs. Preparation and restoration each use the existing `readinessSeconds`
budget; this is not deployment rollback. Inspect/dry-run never quiesce serving.
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
`--staged-diff-approved FILE` (or recipe `stagedDiffApproved`) before custom quiesce.
Base source-chart approval does not approve staged configuration changes;
dynamic probe output is validated by the built-in path, not an arbitrary
override.

Strict dashboard-only execution preserves existing nonterminal measurements.
It requires exactly the `dashboard` image role, retained live values without
`values` overrides or `replaceValues`, and no custom phase commands,
`generatedValues`, `toolSources`, runtime declarations, or measurement-affecting
options. Both node-agent and profiler workload-observer Kubernetes clients must
have an explicit nonempty `apiServer` or explicit
`discoverFromDefaultEndpoint: false` in the captured live values. Missing or
dynamic discovery inputs cannot use the dashboard-only path: offline rendering
cannot prove the effects of Kubernetes `lookup` during upgrade.
Eligibility is provisional: the captured live manifest plus Helm hooks and source render
must contain identical resource identities and structurally identical objects,
except for the `image` of container `dashboard` in the single `apps/v1`
Deployment named `<release>-dashboard` in the selected namespace. An unchanged
image is allowed in the pre-publication render. The final verified-image render
must pass the same boundary before mutation. Missing, duplicate, malformed, or
undecidable resources stop execution. Mapping keys must be unique strings before
YAML constructs dictionaries, so implicit boolean/numeric keys cannot hide changes.
Hook identities and bodies must also remain unchanged. Proven dashboard-only
execution automatically uses Helm `--no-hooks` and records the reason in
deployment state, preventing unchanged post-upgrade jobs such as bucket setup
from rerunning. This has no recipe switch; full deployments retain hook execution.

Both all-namespace in-flight reads still retain full objects, owned Jobs and
`blockers.json`; nonterminal campaigns remain visible diagnostic evidence.
The proven dashboard-only path may proceed with those blockers, without
GPU probes, scheduler quiescence or measurement-state mutation. Server, CRD,
measurement configuration, hook and mixed changes retain the ADR 0019 STOP,
except for the separately proven retirement repair scope below. Chart approval
does not widen either scope. Noneligible recipes stop before custom
commands. Disk, authentication, revision, chart-review, backups, server dry-run,
registry provenance, readiness and acceptance gates remain active. There is no
force/ignore switch, object-UID exception, manual Helm bypass or guard removal.

### Old-contract retirement repair installation

`retirementRepairOnly: true` selects a narrowly scoped installation of exactly
the `server` and `profiler` image roles. The default is `false`. Retain live user
values with `values: []` and `replaceValues: false`; omit custom commands,
`toolSources`, generated values, runtime declarations, publication-only roles
and measurement-policy overrides. Both Kubernetes client endpoints must pass
the same static live-values guard described above.

Before publication and again after the verified-image overlay, every resource
identity and field must match the captured live manifest and hooks. The only
allowed differences are the `serving-entrypoint` container image in Deployment
`<release>-serving-entrypoint`, and the root `image` inside ConfigMap
`<release>-profiler-config`'s `data["config.yaml"]`. The profiler is
operator-managed; this scope does not introduce a profiler Deployment. Inner
YAML requires unique string mapping keys. Comparison retains YAML scalar tags
and spellings without Python numeric coercion, excluding only the root image.
Equivalent-looking non-image numeric rewrites are conservatively rejected,
including sexagesimal forms that the actual Go config reader treats as strings.
CRDs, backend settings, policy, metadata, other images and profiler config
fields retain the old deployed contract. Chart approval cannot widen this scope.
Helm hooks remain unchanged and are not rerun. Server-side dry-run covers only
these two rendered objects; it does not apply CRDs or measurement resources.

The source-reviewed diagnostic helper must directly prove every original
in-flight blocker is an eligible pre-execution metadata-decision failure.
Evidence must establish no owned run, explicit plan-not-found, no published
result and exact current Node grant/measurement-taint ownership. Existing
plans, runs, results, other in-flight work, foreign or unknown ownership and
API/store errors stop installation. The runner retains original all-namespace
objects and blocker reports, and obtains fresh proof immediately before upgrade.
Changed final rendering or proof stops before Helm upgrade. Operator-supplied
proof files, force/ignore flags and UID exceptions are not accepted.

Installation preserves the nonterminal BEO and owned Node taint while updating
the two repair images. It authorizes neither measurement execution nor direct
deletion. After installation, the repaired existing path must return through
normal handback, power readback and taint return before public DELETE can
succeed. Do not patch status, finalizers or taints manually, bypass the runner
with direct Helm, or replace the old schema, runtime, policy or stored contract.
No runtime probes, scheduler quiescence, migration or configuration phases run
for this installation. Ordinary deployment retains its in-flight STOP.

`images` explicitly selects roles to deploy; selected roles may reuse unchanged
image inputs as described below. The source role catalog has all 16 roles:
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

To build only changed inputs, set recipe
`"reusePublication": "/absolute/private/prior-deployment/publication.json"`.
This is the existing publication evidence, not a new cache database. Missing
roles build normally; omit the field to rebuild the selection. Explicit
`prebuilt` entries retain their same-source contract and take precedence.

If a build pushed successfully but stopped before its receipt was verified, name
that role's existing metadata explicitly:
`"reusePendingBuilds": {"firstfloor": "/absolute/private/prior-deployment/firstfloor-build.json"}`.
This requires `reusePublication` and the same prior directory's frozen
`image-plan.json`. The planner binds the original role, build inputs, image and
metadata; inspect/execute verify the published tag, platforms and original OCI
revision. A pending candidate becomes a receipt only after registry verification.
Invalid or changed pending inputs stop. Pending reuse also rejects custom
preflight commands, which can mutate build inputs. Retain the old evidence and
use a new execution directory.

The planner reads each role's original OCI revision from platform receipts
and uses Git diff against the requested source for its Dockerfile, local
COPY/ADD inputs, and Dockerignore files. Effective arguments, platforms and
the pinned harness power source must also match. Unknown/dynamic input forms
or unavailable Git commits rebuild conservatively with a visible reason.
Configured custom preflight commands also disable inferred reuse: they can
modify archived inputs before a build. Tracked `.gitattributes` in either
revision, or repository-local `info/attributes`, conservatively rebuild
because archive export transforms can change effective input bytes. Archive
commands ignore global/system attributes without changing external Git
configuration; Git comparisons explicitly disable textconv.
Dockerignore exclusions are not optimized: a broad `COPY internal/` watches
all of `internal/`, even packages not linked into that image. This does not
detect updates to floating external base images/packages; omit reuse when
intentionally refreshing those.

Dry-run lists `action` (`build`, `reuse`, `prebuilt`, `verify-pending`), `reason`,
and `inputPaths` without network access; candidates still require verification.
Inspect rechecks candidate registry digests/platforms/OCI revisions,
and execute repeats the same checks before publication. `image-plan.json`
freezes the decisions and prior receipts. Publication `sourceSHA` identifies
the deployment source; each role's `sourceSHA` identifies its actual image
source, which can remain older. Reused images keep their original digest and
labels; runtime probes and declaration activation are **not** skipped.
Use a previous record that actually contains the roles being considered;
the runner never scans unrelated evidence directories or invents a latest tag.

Inspect and execute automatically print a CLI watch stream: phase BEGIN/DONE
with phase/total elapsed time, image decisions, command start/exit, and
five-second heartbeats during silent waits. Human `.log`/`.txt` output is
teed live with command/channel labels; large JSON/YAML captures stay on disk.
Coverage attempts show pending/ready and remaining readiness time. Final
COMPLETE/STOP includes elapsed time and evidence location; READY is still a
separate machine-readable line. This works without a TTY or ANSI redraw and
does not print command argv/environment. All original stdout/stderr evidence
and command timings remain available. Ctrl-C in the executing CLI interrupts
that deployment and cleans up its current local process group; it is not
equivalent to Ctrl-C in a separate `tail -F` log viewer.

Large backup responses are scoped to the deployment, not hidden by increasing
limits. CRD backup fetches the complete live schemas for names declared in this
release's captured manifest/hooks, rather than every CRD on the cluster;
removed-CRD `keep` checks are unchanged. In-flight loss guards still read every
CHILL root and owned Job across **all namespaces**, with full raw responses.
Their command evidence and nested evidence paths now share one file via
hardlinks, and file-returning commands avoid a redundant `read_bytes` buffer.
This reduces local copies/disk writes; it does **not** reduce the Jobs/CR wire
payload or truncate measured evidence. Full profiles needed for numeric
before/after comparison remain full, while authentication uses its bounded
summary request.

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
Builds record `docker buildx du` before/after on their actual builders.
`"pruneCompletedBuildCache": true` authorizes reclaiming unused cache on the
recipe's CHILL-owned builder after each successful build/push, before the
post-build disk check. It defaults to false and runs
`docker buildx prune --builder <actual builder> --force`.
BuildKit protects live references; its `inuse` filter is presence-valued, so
`inuse=false` would match no unused records. Cleanup failure or insufficient
remaining headroom stops execution. Other builders, images and volumes remain
outside this cleanup; default prune also retains shared/internal/frontend cache.
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
every eligible GPU Node (`jetson-nano`, `jetsono`, `jetsonx`, `orin-nano`,
`genesis2`, `genesis3`), and reads the RKNPU driver version and device-tree token
on `orangepi`. Both RTX nodes are inspected independently; neither reuses the
other Node's GPU UUID or runtime facts. With all image variables set, this
yields 15 declarations. Every
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

The pre-7-7 cutover uses **failurePolicy v2**, binding `e5-binding.v4`,
judgment `e5-judgment.v3` and summary `e5-summary.v4`, with strict single-version
readers. Historical evidence is not rewritten. SLO, achieved-load fidelity,
offered/tail energy, injection realization, drain, cleanup and publication are
independent outcomes; a defect in one does not erase observations in another.
Lateness and gap thresholds remain configurable reporting labels. Only zero
evidenced infer-call/send/returned requests invalidates an offered-load claim.
No power samples means energy UNAVAILABLE, never a fabricated zero integral.
Partial energy retains `observedJoules`; `wholePoolJoules` requires full coverage.
Raw ambiguity quarantines the affected request IDs, not unrelated workloads.
Unknown operational fields are listed in `ignoredKeys` and never applied.
CPU/throttle telemetry is best-effort, not a GO/drain dependency. On a finite
drain watchdog or early EOF, stop exact owned writers and collect partial facts.
Wrong Workload/Node UID, unsafe mutation, credentials/path scope and evidence
overwrite remain hard boundaries. Generation/source/provenance labels are facts.
An all-no-load/unavailable collection of stages stays NOT_EVALUATED, never PASS.
Substage SLO labels use the same no-send/no-final availability boundaries.
Diagnostic HTTP 401/403 remains a fatal authorization failure, not missing data.

`evaluation.py` executes prepare/factor/stage/judge/cleanup-jobs/finish/publish
from the controller's immutable spec/execution/config ConfigMap. Public settings
comparisons use GET/PUT `/api/v1/scheduler/settings`: GET ETag, merge the declared
settingsPatch, PUT with If-Match, and readback. Apply conflicts never retry.

Python responsibility owners: `settings_patch.py` owns optimistic patch/revert;
`judgment.py` owns judge/summary/publication measurement interpretation;
`request_analysis.py` owns the unchanged request timing/fidelity analysis
formerly under `d2/analyze_before.py`. `evidence_io.py` owns strict binding JSON
and atomic file helpers without depending on producer execution. `conductor.py`
retains producer lifecycle. `input_snapshot.py` owns adapter preparation/config
and environment snapshots; `publication.py` owns evidence assembly/publication;
`evaluation.Harness` dispatches the existing steps. These are direct imports,
not re-export compatibility paths; stored contracts, source keys, cleanup and
exit codes remain unchanged. Existing recursive Docker COPY includes the new
modules; this extraction does not rebuild or activate a harness image.
Revert restores only paths still equal to our applied values; external changes
are recorded and skipped. One 409 revert conflict permits one fresh GET and
re-evaluation; a second blocks Returned. Each PUT records If-Match, status and
response ETag alongside request/response/readback and apply/revert timestamps.
Applied path values come from the PUT response's canonical settings, including
defaults materialized from null patches. Newly created objects are tracked by
member: revert removes only evaluator-added members, preserves operator-added
members, and removes an originally absent parent only when it becomes empty.
Job cleanup is attempted before settings restoration, but cannot prevent it:
cleanup-jobs and run finish independently attempt every receipt in their scope.
`cleanup-outcome.json` retains each attempt's cleanup and restore results.
Either failure keeps the step failed and blocks Returned; restoring settings
does not declare a remaining Job cleaned up. Finish retries honor `revertedAt`
and never reapply or rewrite an already restored patch.

The generator Job is assembled directly. Its Dataset init container copies the
carrier's evaluation directory to `/evaluation`; Preprocessor input names
select the existing record fields. Actual record order/IDs/bytes/hash are Ready
evidence, not host inputs. Every generator Pod uses the frozen
`loadGenerator.nodeName` verbatim. Memory and shard/barrier declarations remain.

Generated arrivals travel as compact segment/rate/seed declarations, not an argv
array of every offset. Conductor and producer use the same load-generator
`segmented_arrivals_ns` function; producer checks the regenerated SHA and count
before any inference. Explicit `traceRefs` instead travel as a file: preparation
freezes the evaluator Pod's existing workspace PVC identity, and the producer
mounts its staged trace read-only at `/evaluation/.chill-arrivals.json`.
Both file-byte SHA and logical arrival SHA/count must match. The existing Pod
get and Job creation grants suffice; no ConfigMap or new RBAC is required.

At prepare start, freeze the participating nodes' Running/Pending Pod facts in
`environment-start.json` and `snapshot.environmentStart`: namespace, name, UID,
assigned node, phase and ownerReference summary. Participating nodes are exactly
`powerMeasurement.nodes` plus `loadGenerator.nodeName`. Existing system, tenant
and ownerless Pods are environmental evidence, not a preparation rejection.

At the existing prepare/factor/stage monitoring boundaries, record new active
Pod UIDs and health changes in environment/target/node observation histories.
`environment-intrusions.json` records new competing Pods without stopping load.
Each cross-namespace Pod query uses `spec.nodeName`; terminal Pods and other
nodes are ignored. No Pod-name, age, release-label or namespace guesses are used.

New evaluator Pods are exempt only when their direct Job owner UID matches a
live Job controlled by this Run or its known Stage UIDs. No ancestor walk or
release-identity classifier is used. New serving Pods for the current scenario's
frozen Workload UIDs are normal target autoscaling: use the controller's unique
`edge.dacs.io/v1alpha1` Workload ownerReference predicate (a08fda9d). DaemonSet
and static/mirror Pods are also exempt as persistent node infrastructure.
No ReplicaSet/StatefulSet/CronJob ancestor-read grants are needed.

Nonterminal load-generator Jobs are also observations, not a refusal. The
`generator-interference.json` history distinguishes a Job declaration from
another actual Running producer on the same generator node. NotReady target
Pods/nodes are recorded as behavior under test; target selection still uses the
declared resource's nonempty Kubernetes selector and exact Workload/Node UID.

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

Current planning/collection is implemented in `hack/measure/coverage_plan.py`;
`gen.py` remains the CLI dispatcher and retains the approved historical
34-profile reconstruction and SciPy dependency. Its coverage self-checks live
in the existing `hack/measure/smoke/test_coverage.py`; the command below
continues to execute them. The historical rerun obligation above is unchanged.

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
Target selection and request reuse compare the current `curveSchema` and
`evidenceSchema`; `runtimeImage` is retained provenance, not performance identity.
Completed results must have curve-plan v1alpha11 and finalized execution-evidence
v1alpha17, sealing strict RunConfig v1alpha19. There is no old-schema reader.
A current completed measurement is reused; a current in-progress request
suppresses duplicate generation. A Failed/latest-selected request does not revive
an older successful sibling or suppress regeneration; replacement lineage remains
separate from reuse eligibility. Pending requests record these schema identities
and image provenance in `chill.dacs.io/coverage-execution-conditions`.
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
Every planned batch receives three independently restarted trials, each stopping
only after both the configured minimum elapsed window (30 seconds by default)
and the Workload-derived native service-call count are complete. The generator
reads `/api/v1/workloads` and selects the named Workload matching the exact model,
preprocessor and dataset; absent or ambiguous sources fail rather than inventing
a percentile. The controller freezes that Workload's identity and SLO. At the
existing 95% confidence convention the count is
`ceil(log(.05)/log(workload.slo.percentile/100))`: 59 for p95, 299 for p99.
The pooled maximum's minimal quantile coverage is not a precise percentile
estimate or proof of κ safety.

Historical slow calls remain timing provenance and conditional work estimates;
they never drop a batch or truncate the larger planned batches.
`minCallsInWindow` and the monotone throughput exclusion are removed. Only the
campaign's verified same-device, same-condition batch-one service evidence can
establish the SLO-impossible exception; a slow p95 or old CPU-regime result cannot.
The existing scoped memory safety/review exclusions below remain separate.

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

Metric observation follows the runtime's actual end fence through sample-limited
windows; its immutable safety timeout remains a failure bound, never a successful
stop. The generator preserves inherited metric padding above the elapsed minimum,
without call-duration multipliers or invented rates for unknown timings.
Active evidence must satisfy the full elapsed minimum regardless of the retained
`durationToleranceSeconds`; observation windows and exact images are provenance,
not performance-key identity.
The policy contributes to deterministic request naming. Existing requests are
reused only through the explicit current-schema conditions above, never rewritten.
A running feeder keeps its frozen plan until the operator explicitly replaces it.
Idle retains its inherited window or config `idleSeconds` (10 seconds).
Work estimates use `3 × Σ_batch max(windowSeconds, n_min × secondsPerCall)` where
same-condition timing exists. Unknown rates contribute only the elapsed lower
bound, explicitly not a completion estimate. Idle/settling and the existing
20-second per-Run overhead scenario are added; variable startup/warmup/polling are
not included. MNV/PLC precede Smol, with power modes
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

