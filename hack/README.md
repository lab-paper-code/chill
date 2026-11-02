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
First live use is only after the current cutover, with advisor-3's deploy main
as the sole live actor. This documentation/change performs no live operation.

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

The current first live use is the actual dashboard nine-role selection:
`server`, `query`, `dashboard`, and all six FirstFloor roles. It must include
FirstFloor build/publication → separate fixed approved exact-image probes →
measured declaration activation, not publication-only receipts.
`queryService: "chill-query"` adds direct unauthenticated 401/authenticated 200
checks through a private port-forward. `dashboardCheck: true` requires HTTP 200
HTML, not a browser interaction test. Neither substitutes for hardware or
measurement acceptance.
The tracked first-use recipe is
`hack/deploy/plans/dashboard-reads-20261103.json`; pass its actual approved
source using `--source`. The declaration records main's confirmed post-cutover
revision 178 and control-315 tag; a later live revision requires a new review.

Keep all evidence private: live values, manifests, retained objects, commands,
publication, renders, before/after API captures and failure records can contain
operational data. Execute shares `/tmp/chill-heavy.lock`; inspect does not.
The disk gates require `minFreeBytes` and `maxDiskUsedPercent` (default 85).
`pruneBuiltImages: true` permits only Docker image pruning scoped to the current
source SHA label; it never authorizes unrelated image/cache/data pruning.
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

### E5 #222/#223 static replay

Historical #222/#223 release names and evidence IDs below are retained; they are
not reissued by this rename. New authorizations use `evaluation-execute`.

Reuse `conductor.py factor --mode static --stage E5-3 --pre-state <static-selection.json>
--row-tag 223`; this is the existing paired pre-state prepare/ACK path, not a
campaign resume engine. Both a factor `pre-state.json` snapshot and the frozen
selection's `state.snapshot` are read by the same baseline reader. The selected
state must match its recorded fingerprint and the Run's original selection.
Prepare retains the selected maxBatch and polls fresh instances[] ACKs.
After route publication, compare the fingerprint once with only old physical IDs
rebound to actual prepare-ACK IDs; any profile/allocation/cap/wait/route/mode
difference exits 2. `static-replay-restore.json` retains that comparison and mapping.

`--row-tag 223` appends static E5-3..6 and full E5-4..7 directories; previous
rows, the failed unsuffixed static E5-3, original freeze and finish evidence remain
untouched. Tagged cleanup touches tagged rows only. No prior row is rerun.
Each tagged row's entry/exit records available filesystem bytes in
`disk-boundaries.jsonl`; below 310 GB (310,000,000,000 bytes) stops before another
row. `summarize` and `tables` select appended rows explicitly and display
**#222+#223 합성, 같은 rev104 소스**, not a fresh independent campaign.

Offline only: add `--dry-run` to that factor command, with the preserved #222
binding and Run. It never constructs CPO or contacts the cluster; it prints
restoration request bodies, original fingerprint, ACK-only ID rebinding rule and
execution order, and writes `Run/static-restore-dry-run-223.json`.

CampaignOps sequence below requires `CHILL_E5_RELEASE_223` to point to the actual
Main-approved #223 `e5-execute` release with the unchanged #222 binding/component
sources. Run each line only after the preceding one succeeds; `set -e` stops
the loops on a failed stage or invalid-generator judgment. These are existing
stage/restore/finish commands, with no automatic retry or factor experiment.

```sh
set -e; export CHILL_EXPERIMENT_CONFIG=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/inputs/environment-212.json; Run=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full; Release=${CHILL_E5_RELEASE_223:?Main-approved release required}; e5() { nice -n 19 python3 -B hack/evaluation/conductor.py "$@" --binding "$Run/binding.json" --release "$Release"; }; e5 factor --out "$Run" --mode static --stage E5-3 --pre-state "$Run/static-selection.json" --row-tag 223
for n in 3 4 5 6; do e5 stage --out "$Run" --mode static --stage "E5-$n" --row-tag 223; e5 judge --out "$Run/static-E5-$n-223"; done
e5 restore --out "$Run" --row-tag 223
for n in 4 5 6 7; do e5 stage --out "$Run" --mode full --stage "E5-$n" --row-tag 223; e5 judge --out "$Run/full-E5-$n-223"; done
e5 finish --out "$Run" --row-tag 223; e5 cleanup-jobs --out "$Run" --row-tag 223; e5 summarize --out "$Run"; nice -n 19 python3 -B hack/evaluation/conductor.py tables "$Run" | tee "$Run/tables-223.txt"
```


### Evaluation micro rows

**미니 행은 반복 검증용, 정본 표는 전체 행.** `micro=true` 결과는 별도 비교
표/`e5-summary.json.microRows`에만 표시하며 정본 prepare-idle~failure·static-baseline 및 시나리오
완주 수에는 포함하지 않는다. D1·완료-only p95·POOL 에너지/공백 오차 판정은
동일한 raw 규칙을 사용한다. 동일 길이 고정 trace가 없으면 에너지 Δ는 N/A다.
전체 길이 정본 에너지와 짧은 행 에너지를 빼서 절감으로 표시하지 않는다.

`hack/evaluation/micro-stages.example.json`의 `stages`를 기존 전체 행 뒤에 추가한다.
실제 bind 입력은 3회차의 R·payload·seed·arrival·poolShares를 그대로 사용하고,
기존 `stages`에 이 미니 행 정의만 덧붙인다
(`rate_w = alpha_w × s_w × R_w`). `CHILL_EXPERIMENT_CONFIG`의 `evaluation.stages`도
bind 입력에 `stages`가 없을 때만 적용된다. 운영 복사본은
`inputs/environment-micro.json`이며 기존 `environment-212.json`은 바꾸지 않는다.

| 행 (저장 ID) | 현재 이름 | 부하 창 | 복원 원본 |
|---|---|---|---|
| E5-5m | drop-surge-micro | α=.3 180s → α=.9 300s | full-E5-5-223/pre-state.json |
| E5-7m | failure-micro | α=.3 360s; 120s 후 serving Pod 주입 1건 | full-E5-7-223/pre-state.json |
| E5-3m | ramp-up-micro | α=.5 180s → α=.9 300s | full-E5-3-223/pre-state.json |
| E5-6m | soak-micro | α=.3 600s soak | full-E5-6-223/pre-state.json |

행별 `factor --mode full --stage E5-5m --pre-state "$Run/full-E5-5-223/pre-state.json"
--row-tag <fresh-tag>` 후 같은 stage/tag의 `stage`·`judge`를 사용한다.
micro 행은 성공한 해당 복원 receipt가 필수이며 기존 재시도/덮어쓰기 금지는 그대로다.
failure-micro는 stage의 `failureInjectionRunbook.steps`를 사용하며 `failure-select`는
양의 route/assigned 및 실제 UID가 확인된 Pod 1건의 명령만 출력한다. 실행은
운영자만 수행하고 실제 명령·시각·응답을 `failure-events.json`에 기록한다.
이 설정은 측정을 실행하지 않는다. 네트워크 없는 산식·허용 목록·복원 원본·주입
자가 검사는 `python3 -B hack/evaluation/conductor.py self-check-micro`, 표 격리는
`python3 -B hack/evaluation/conductor.py tables --self-check`다.

### Evaluation targeted full replay

`factor --mode full --stage E5-{1,3,5,6,7} --pre-state <original-row/pre-state.json>
--row-tag 3` reuses the same reset/prepare/native-mode/route ACK path (#237 adds E5-3).
It reads the original row's sibling `pre-settings.json` (or embedded settings), including
full autonomy, and restores served canonical instances with exact maxBatch,
allocation, profile, wait, routing and native modes. Only physical IDs returned
by prepare ACKs are rebound. One final fingerprint comparison is retained in
`Run/full-E5-N-3-restore.json`; mismatch exits 2 before any targeted stage.
Autonomy stays off while producers prepare; the saved full settings are enabled
at producer GO after releasing restored routing. Targeted stages require their
own accepted receipt, not completion of intervening historical rows.
Targeted E5-3 retains the original campaign's static selection; only the first
untagged full E5-3 freezes a static baseline at its offered-window boundary.
Sampler shutdown recognizes intentional SIGTERM after drain; cleanup errors remain
visible and do not trigger measurement replay.

The existing fingerprint intentionally excludes reservations and owner-only
non-serving preparations. They are **not** reconstructed. Every dry-run/restore
receipt and the rendered conditions table reports the original reservation count
and excluded owner-only count, including E5-5's original C1 reservation presence.
The preserved #222/#223 inputs currently record E5-5: 0 reservations/0 owner-only;
E5-6 and E5-7: 1 reservation/3 owner-only each. These counts are input observations,
not an assertion of reproduced reservation behavior. Existing rows are untouched;
new directories are `full-E5-{1,5,6,7}-3`. Tables label them
**3회차 = 표적 재실행, 시작 상태 복원, 소스 R1~R6+R4b+C1+C2**.
Retained static/other full rows are historical, not a new independent campaign.

Offline planning: add `--dry-run` to each full factor command. It does not
construct CPO or contact the cluster. Output is
`Run/full-E5-N-restore-dry-run-3.json`, with original pre-state/pre-settings hashes,
request bodies, exclusions, expected fingerprint and factor → stage → judge order.
Use the original `full-E5-1/pre-state.json` and
`full-E5-{5,6,7}-223/pre-state.json` inputs; no copied or fabricated start state.

failure injection is **operator-only**, as in the first actual run. During its
offered window, immediately before the recommended 180s delete and 360s
cordon/delete, run `conductor.py failure-select --out "$Run" --mode full --stage E5-7
--row-tag 3 --failure-kind delete-serving-pod` and then the same command with
`--failure-kind cordon-delete`. Selection only performs GETs and emits
`operatorCommands`/`operatorArgv`; it never executes them. Execute the emitted
commands immediately (before the recorded `expiresAtUTC`), then append
actual command/result records to `full-E5-7-3/failure-events.json`'s `events`.
Each deletion record requires `kind`, actual `atUTC`, `exitCode`, `podUID`,
`nodeUID`, `commandRef`, `responseRef`. The first command reference records
`argv` (or `deleteArgv`); the cordon-delete reference records `deleteArgv` plus
the actual cordon command/result. At recommended 600s the operator uncordons the
second selected Node and records the third `uncordon` event. Recovery evidence
comes from the preserved `scheduler` and `cluster` API rows, not operator readbacks.

The selector intersects the latest fresh scheduler state's **routing weight > 0**
with **instanceAssignments > 0 in the immediately preceding observation window**
and the current scheduler state's Ready serving instance, Pod name/UID and Node
reference. The first selection requires candidates on two distinct Nodes; the
second excludes the first Node. The record includes weight, assigned count,
Pod/Node UIDs, window bounds and captured API source paths.
Deletes use raw Kubernetes `DeleteOptions` with UID-only preconditions and no
grace-period override, matching the first actual run. Missing candidates, stale
state or read failure writes `status: INVALID`, emits no injection command and
cancels injection; do not override it manually. Judge validates the saved
selection/source/command/options evidence; INVALID or UNMEASURED remains visible
instead of a captured failure scenario. Expired selection is INVALID, not a
license to use a stale target.

CampaignOps order (five shell lines; existing frozen component release/binding
for R1~R6+R4b+C1+C2 must be supplied, never reuse the old rev104 source release).
Use a separate operator shell for the failure selection/injections while line 3
runs. Every row must finish before advancing; no automatic retries:

```sh
set -e; export CHILL_EXPERIMENT_CONFIG=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/inputs/environment-212.json; Run=/home/genesis1/.cache/chill-assets/ops/pools-deploy/campaign/v2/d-20261027/control-222-full; Binding=${CHILL_E5_BINDING_3:?Main-approved C1+C2 binding required}; Release=${CHILL_E5_RELEASE_3:?Main-approved release required}; evaluation() { nice -n 19 python3 -B hack/evaluation/conductor.py "$@" --binding "$Binding" --release "$Release"; }
for n in 1 5 6; do suffix=-223; test "$n" != 1 || suffix=; evaluation factor --out "$Run" --mode full --stage "E5-$n" --pre-state "$Run/full-E5-$n$suffix/pre-state.json" --row-tag 3; evaluation stage --out "$Run" --mode full --stage "E5-$n" --row-tag 3; evaluation judge --out "$Run/full-E5-$n-3"; done
evaluation factor --out "$Run" --mode full --stage E5-7 --pre-state "$Run/full-E5-7-223/pre-state.json" --row-tag 3; evaluation stage --out "$Run" --mode full --stage E5-7 --row-tag 3
evaluation judge --out "$Run/full-E5-7-3"
evaluation finish --out "$Run" --row-tag 3; evaluation cleanup-jobs --out "$Run" --row-tag 3; evaluation summarize --out "$Run"; nice -n 19 python3 -B hack/evaluation/conductor.py tables "$Run" | tee "$Run/tables-3.txt"
```



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

### Required pre-deployment decision + guard replay gate

**배포 전 필수:** `hack/evaluation/replay-gate.py`를 보존한 ramp-up/drop-surge/failure 및 회귀
stage 전체에 실행하고 `summary.json`의 `verdict=PASS`를 배포 증거에 첨부한다.
판정은 **기준 소스 대비**다: 첫 `--source`(또는 `--reference`)가 기준이며 후보는 stage마다 나빠진 tick·공급 증가 거부가 기준을 넘지 않으면 PASS(기록 자체의 population mismatch로 생기는 잔여 bad tick은 기준에도 같이 나타난다). 불일치 자체는 실패가 아니다. 배포 전 기준은 **현재 live 서버 소스**로 둔다.
나빠진 tick은 (1) keep κ가 수요보다 부족한 상황의 기록된 공급 증가 target을
재평가가 Keep/no target으로 바꿈, 또는 (2) guard가 공급 증가 target을
당시/다음 N tick 중 한 번이라도 거부함이다. 공동 owner는 workload 하나라도
공급이 증가하면 포함하고, origin tick당 한 번만 센다.

```sh
python3 -B hack/evaluation/replay-gate.py --source HEAD \
  --stage /ops/campaign/full-E5-3-7 --stage /ops/campaign/full-E5-5-7 \
  --stage /ops/campaign/full-E5-7-7 --stage /ops/campaign/full-E5-5-8d \
  --rows /ops/deployB/control/kappa-rows.json \
  --profiles /ops/deployB/control/profiles-gate.json \
  --scheduler /ops/deployB/control/scheduler.json \
  --out /ops/campaign/v2/replay-gate/YYYY-MM-DD
```

`--source`는 반복 가능하며 SHA/ref 또는 `tree`(현재 tracked diff와 untracked
파일 포함)다. `git archive` 소스에 현재 `hack/kappa`만 계측기로 덧입히고
제품 planner/guard는 해당 소스 그대로 빌드한다. 캐시는 소스·tree patch·계측기·
Go 버전/플랫폼 해시로 구분한다. guard가 없던 역사적 소스는
`guardAvailable=false`/`GuardNotPresentInSource`로 명시하며 현 guard를 이식하지 않는다.
입력 파일 SHA256, 빌드 provenance, 실행 시간, 모든 tick의 compare/guard JSONL과
요약 JSON/MD를 보존한다. 종료값은 PASS=0, 문턱 초과=2, 실행 오류=1이다.
출력 디렉터리는 새 경로여야 하며, 실패/오류도 배포 불가다.

`kappa -mode guard -scheduler ... -profiles ... -records records.jsonl.gz -rows ...
-catalog catalog.jsonl.gz -arrivals arrivals.jsonl.gz [-next 3] [-format jsonl]`는
admission의 `SchedulerReductionWorkloads`를
동결한 뒤 `SchedulerReductionCommit`을 당시/다음 snapshot에 호출한다.
표는 at/status/target key/거부 여부·사유/수요와 keep·target κ를 포함한다.
`-mode compare -format jsonl`는 각 decision을 재평가하고 route 정수 weight,
instance ID, profile, CPU, batch와 wait까지 비교한다.
원본 `SchedulerRecord`는 workload/cost/baseline을 catalog 참조로 바꾸고 arrivals를
제거한다. runner는 stage의 `obs/*/catalog.jsonl*`을 서버의
`DecodeSchedulerLine`으로 복원하고, `requests.jsonl*`의 실제 ingress를
`requestArrival`과 동일한 accepted/compute 규칙으로 복원한다(assigned보다 final 우선,
native supplement 제외). 창은 `[observationEnd-observationWindow, observationEnd)`다.
참조/arrival이 누락된 원본을 빈 수요로 평가하지 않고 오류로 닫는다.
compare JSONL의 recordedCounts/replayCounts 및 populationMismatch를 함께 보존한다.
재구성 요청의 final 상태는 capture 순간과 다를 수 있으므로 mismatch는 감춰서는 안 된다.
수요는 max(minimumDemand, snapshot.scaleDown.rate), 공급은 Ready positive-route
인스턴스의 built row κ 합이다(target의 준비 전 인스턴스도 예상 공급에 포함).
이는 backlog/SLO 전체 판정이나 실측 처리율이 아니며 guard의 내부 거부 predicate를
추측하지 않는다. 기록된 stage의 setup/teardown을 포함한 모든 decision을 사용한다.
다음 tick 검증은 동결 target+기록 관측의 반사실 진단이며 completion callback,
실행기 owner 취소 또는 closed-loop 클러스터 시뮬레이션을 대체하지 않는다.
끝부분은 남은 tick까지만 검증한다. 클러스터 접근은 하지 않는다.
최소 회귀 검사는 `python3 -B hack/evaluation/replay-gate.py --self-check`와
`go test ./hack/kappa`다.


## Experiment controllers

New experiment Python starts under `hack/`. `hack/evaluation/` owns the campaign
controller and its analysis helpers, `hack/evaluation/d2/` owns the D2 binding,
conductor, cgroup sampler, native-mode request and offline analyzer, and
`hack/measure/` owns measurement preparation, generation and analysis.
CampaignOps owns testbed mutations and image publication.

Set `CHILL_EXPERIMENT_CONFIG` to an absolute external JSON file before invoking
these tools. Its top-level objects are `paths`, `d2`, `evaluation` and `measure`.
Optional `samples` holds complete environment-specific registration/measurement
documents keyed by their existing repository-relative sample paths; select them
explicitly using the [sample procedures](../config/samples/README.md#environment-specific-inputs).
Evaluation binds `d2.namespace` (default `chill-system`) and the selected experiment
policies into `binding.config`; later execution/analysis consumes that binding.
The [configuration ownership table](../docs/design/serving/configuration-hardcoding.md#슬라이스-5-구현과-기본값)
lists the stage/arrival/G0/timebox keys and their unchanged defaults.
`paths.repository` identifies this checkout; `paths.ops`, `paths.campaign` and
`paths.d2State` identify external operational state and retained evidence.
Each tool reads its section for machine identities, endpoints, credential-file
paths, kubeconfig and operational inputs. Keep credential contents in their
existing external files. Store results, raw records, backups and temporary
verification output outside the checkout.

Invoke repository entrypoints with absolute paths and retain the existing
explicit CLI inputs. For example:

```sh
export CHILL_EXPERIMENT_CONFIG=/path/to/external/environment.json
python3 /path/to/checkout/hack/evaluation/d2/analyze_before.py \
  --phase-dir /path/to/retained/phase --out /path/to/external/summary.json
python3 /path/to/checkout/hack/evaluation/d2/bind_before.py \
  --scheduler /path/to/scheduler.json --capacity /path/to/capacity.json \
  --nodes /path/to/nodes.json --pods /path/to/pods.json \
  --out /path/to/external/binding.json
```

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

The runner reuses the D2 conductor's authenticated API and kubectl helpers
(`d2` endpoint, CA/token and kubectl configuration; `measure.namespace` for
serving Pods). It cycles route/unroute/release, removes and re-prepares one
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


`conduct_before.py` is an imported helper module, not a standalone command.
Evaluation and demo construct its `Conductor` without the retired D2 constructor and
provide the approved binding, output, authentication and journal context.
The shared API, publication, producer and cleanup helpers remain available.
The D2 cleanup helper, evaluation finish and demo resume commands submit one explicit scheduler
`reset` with their captured Workload UIDs and a required reason, retain the returned
step/native ACK evidence, and stop on failure without a client retry or resume
state machine. The server owns autonomy removal, unroute/drain/removal, release,
and minimum supported idle-power readback. Cleanup does not recreate original
physical modes. Subsequent input-settings restoration excludes the read-only
`staticRouting` and `staticRoutingValidatedAt` fields; static selections use
`route`/`unroute`/`release` operations and preserve unrelated UID declarations.
Request lifetime is optional: an unset value omits `--request-lifetime`, rather
than synthesizing zero or a client deadline. Socket budgets remain explicit.
Evaluation and demo `publish --experiment-id ID --store-config /private/store.json`
upload private raw evidence first through the existing PutImmutable bridge, then
the final `serving/experiments.v1alpha1/ID/summary.json`. Existing IDs cannot be
overwritten; corrections require a new ID. `native_mode_request.py` remains the
separately approved baseline-preparation path, not cleanup sequencing.
과거 게시·평가 기록은 바꾸지 않는다.
`native_mode_request.py` consumes the explicit native-mode `--release` and
`--out`. `sample_cgroups.py` consumes an external host inventory plus
`--duration`, `--phase` and `--out`. The `smoke_*.py` scripts exercise retained
records and bounded local fixtures; the component-throttle smoke writes to
`d2.smokeOutput`.

Evaluation collects CHILL facts only through authenticated APIs using the collector
service-account token at `paths.ops/.token-collector`. After drain, resource-batch
support and the next closed-minute boundary, the raw exporter supplies
`definitions`, `requests`, `management`, `scheduler`, `catalog`, `resources` and
`cluster`; the collector builds `raw-source-index.json`. Settings `meters` and
state Node references feed the unchanged power integration, without ConfigMap,
S3, host filesystem or Kubernetes evidence reads.
Bindings record `collectorTokenFile` for evidence GETs and the existing
`tokenFile` for approved operator PUT/POST actions; collector RBAC grants no
writes. The additional collector processed-GET permissions, `requestTimeout`
and `podName` require the next approved deployment, not a live RBAC patch.

The external producer-only `client` cgroup sampler retains stage-entry identity,
start readiness and post-end counter release. STOP monitoring and operator failure
commands remain unchanged. Missing counters remain unknown, not zero. Capture
receipts (`capture-identity`, `s3-hour-list-*`, `upload.log`, `collector-status`,
`*-collector.stderr`) are no longer produced or judged.

Reports identify GC as `runtime-metrics-delta` (window cumulative CPU/cycle
difference, not a gctrace cycle distribution), and failure recovery as
`scheduler`/`cluster` evidence. D1, latency and energy formulas are unchanged.
Never export raw observations or query historical dashboard data during an evaluation
measurement window. Historical publications are not rewritten.

`hack/evaluation/conductor.py check-preparation --out /path/to/external/preparation.json`
checks the retained preparation inputs. `hack/evaluation/wire.py execute` takes
the approved `--numbers`, `--release` and fresh external `--out` directory;
its archive, raw-collection and producer helpers live alongside it.

The approved numeric input is supplied externally; the retired exploratory
numeric-table CLI is removed. Original numeric inputs and published results
remain immutable. Kappa reads only current single-Workload snapshots.

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

### Short autonomous demo (not the normative evaluation study)

`hack/evaluation/demo.py` reuses `CPO.start/run/collect/finish`, the existing producer
Job template, Poisson arrival generator/seeds, authenticated API, raw archive
collector and prebuilt power reducer. It does not call the R/G/10800-second
scientific binding/judgment gates. The example
`hack/evaluation/demo-config.example.json` selects E0/A/B/C and explicit request timing.
With no `evaluationConfig`, `prepare` reads the existing environment's operational
recipe and captures scheduler state, nodes, pods, Ready serving-image digest
and gateway cap. It reuses `wire.Prepare.canonical()` to create missing
canonical Workloads from the recipe's exact model/preprocessor/dataset
references and configured SLO, preserving existing definitions/SLOs without
PUTs. Registration ACKs and applied UID/generation/SLO are retained before
binding. It reuses `wire.template()` plus existing D2 producer arguments and
explicit `requestTiming` overrides, saving `demo-evaluation-config.json` and
`binding.json`. This registration is CampaignOps-only, under Main's release.
`controlSourceSHA` is Main's helm-revision source claim, resolved in git and
kept distinct from the observed image digest; it is not inferred from image
bytes. `demoSkipped` names every bypassed scientific gate. An optional
`evaluationConfig` can still name an already assembled raw evaluation config.
Copy the example outside the repository if changing its inputs. Rates and
reporting-only `measurementSLO` are demonstration choices, not capacity claims.
Existing scheduler SLOs are captured and never changed; new canonical
definitions use configured SLOs. Lifetime/socket cover the actual applied
SLOs within the live gateway cap. No image builds occur.

CampaignOps alone executes live commands, with the existing safety monitor
running and all instances/autonomy/static routes initially empty:

```sh
DEMO=/absolute/path/to/checkout/hack/evaluation/demo.py
CONFIG=/absolute/path/to/external/demo-config.json
OUT=/absolute/path/below/paths.campaign/demo-run
RELEASE=/absolute/path/to/external/demo-release.json
python3 "$DEMO" prepare --config "$CONFIG" --out "$OUT"
# Main issues the existing CPO release JSON:
# {"executor":"CampaignOps","command":"evaluation-execute",
#  "bindingPath":"<OUT>/binding.json","sourceSHA":"<binding config approval.sourceSHA>",
#  "mainInstructionUTC":"<actual authorization UTC>"}
python3 "$DEMO" start --out "$OUT" --release "$RELEASE"
python3 "$DEMO" run --out "$OUT" --release "$RELEASE"
# Alternatively execute one configured stage at a time:
# python3 "$DEMO" stage --stage E0 --out "$OUT" --release "$RELEASE"
# then A, B, C. Every stage already collects before advancing.
# Show already collected evidence, or collect a drained, not-yet-captured stage:
python3 "$DEMO" collect --stage C --out "$OUT" --release "$RELEASE"
python3 "$DEMO" summary --out "$OUT"
python3 "$DEMO" finish --out "$OUT" --release "$RELEASE"
```

E0 runs without producers after autonomy is enabled. Before the first loaded
stage the driver requires each canonical Workload's owned Ready positive route.
Each loaded stage gets new producer Jobs; the measured gap from the previous
offered window includes drain, collection and setup. No requests are offered in
that gap, so the demo cannot measure its request failures. STOP stages retain
partial evidence; there is no implicit retry. `finish` restores original
settings/modes using the existing owned-unroute/remove/publication contract,
then deletes exact owned producer Jobs. Only after that cleanup completes,
it deletes the three captured canonical definitions using exact UID/generation
checks and `If-Match` resource versions, retaining ACKs and waiting for actual
definition disappearance.

Each `smoke-<stage>/` contains pre/post state, per-Workload `*-client.jsonl`,
producer lifecycle evidence, raw collection, `power*.json` and `summary.json`.
Top-level `summary.json` and `summary.md` combine stages. HTTP success is
status 200 / frozen offered count; p95/p99 are nearest-rank successful-client
round-trip latency, compared with configured `measurementSLO` (or captured
scheduler SLO when omitted), not server execution latency. Both criteria and
the unchanged scheduler SLO are retained in each workload summary.
Pool/active/drain energy comes from existing complete power integrals and is
shared, never attributed to workloads. Placement events compare raw scheduler
snapshots and boundary snapshots (instance creation/deletion/node/profile/
allocation, node modes and routing); event times are observed timestamps with
the preceding observation retained, not exact native creation times.
Transition failures count failed/unsent client requests overlapping those
observation brackets or raw action `startedAt..completedAt` intervals.
Incomplete scheduler/client evidence is unknown, not zero.
The scheduler's explicit JSON `results: null` is an empty Go result slice for
consumer iteration/concatenation; retained raw state keeps the original null.
Demo Jobs omit `--workload-slo` and `--gateway-timeout`, which the pinned D2
producer predates; prepare still checks actual SLO/lifetime/gateway/socket
budgets on the host. Normative evaluation Jobs retain their flags. Self-check compares
generated demo options with immutable e18ac/4fb CLI parser option sets using
AST, without running an image. On producer failure, exact Job-owned Pod logs,
stderr and identity receipts are saved under `startup-<workload>/` before
owned Job deletion, including failures before log attachment.
Final producer cleanup skips a retained creation record only when its phase
already records `stopped: true` for that exact Job name and UID. Missing or
mismatched deletion ACKs retain the existing exact-UID fail-closed behavior.
Demo stage boundaries and resume wait only for zero scheduler outstanding
requests and zero per-Workload pending requests. They retain
`demoRequestDrain.inProgressTarget/actions` and `request-drain-state.json`
without waiting for the planner to become idle. Resume additionally requires
owned Ready instances and positive routes. Normative evaluation and final owned
cleanup retain their original global-idle waits.

`tolerateArchive` is **diagnostic-only (진단 전용, 정본 evaluation 금지)** in `demo.py`.
Its top-level or stage-level list may name only `power` and
`power-drain-inclusive`; a stage list overrides the top-level list (`[]`
restores strict capture). Missing archives retain original NOT_READY evidence,
explicit `missingArchives` and null energy, never a successful energy result.
Normative `conductor.py` rejects the key anywhere in its config or binding, including
empty/false/null values, rather than silently ignoring it.

For an otherwise completed producer window stopped only by the former
planner-idle timeout, `collect --stage A` can collect without replay. It requires
all original start/drained/end-counter and terminal Complete proofs. Original
`phase.json` (including STOP), client logs and offered start/end stay unchanged;
a separate `collected-phase.json` records the original failure and the actual
late request-drain observation, with `recovery-post-*` boundaries. Only a
successful collection makes this derived phase eligible for the next stage.
Reports explicitly label recovery and the longer drain-inclusive energy
interval. After collection, use `resume --stage B` if failure restored autonomy
off, then stage B/C, summary and finish; do not replay A.

After an explicitly released stage failure, CampaignOps preserves the failed
stage directory and explicitly re-freezes changed harness source references;
there is no automatic retry or source refresh. `demo.py resume --out "$OUT"
--release "$RELEASE"` reuses the existing settings PUT to enable canonical
autonomy, adopts only owned current instances and waits for Ready positive
routes using the same `CPO.missing_ready_routes` predicate as bootstrap and the
Ready gate. It preserves original `initial.json` and `start-ack.json`, recording a
new `resume-*/resume-ack.json`. If no instances remain, it first reuses the
configured zero-load E0 through CPO.run in a fresh `smoke-E0-resume-*/` folder
(180 seconds in the example), preserving original E0 results. Then execute
`stage --stage A`, B, C, collect, summary and finish with the same out/release.

Fresh demo `finish` submits one server `reset` for the owned Workload UIDs and
retains its full ordered step/result evidence before restoring input settings
or deleting definitions. Active global routing or affected-Node operations
are refused before mutation; the client does not assemble cleanup, retry,
rollback, or resume an old unroute acknowledgment. A failed reset stops later
steps and preserves the observed partial state. A later explicit reset reads
current state and skips already-satisfied changes.

New campaign/demo bindings freeze the full `schedulerSettings`, including
per-UID `minimumDemand`. JSON summaries and immutable published stage KPIs
retain that frozen input, not current settings. Older bindings without it show
null and an explicit missing reason; their immutable records are not rewritten.

`python3 hack/evaluation/demo.py --help` and `--self-check` work without environment
configuration. The latter runs the real CPO producer stage loop against local
pipes and mocked API/kubectl, with no cluster access, image builds or full tests.

`python3 hack/evaluation/conductor.py self-check` needs `CHILL_EXPERIMENT_CONFIG` pointing at an
environment JSON (module constants) but makes no cluster or network calls.

### One-command evaluation round

`python3 -B hack/evaluation/round.py --round /absolute/private/round.json` runs the
released server-only roll-forward, initial owned reset, headroom PUT/readback, κ,
ordered factor → headroom GET → stage/first-load-tick check → judge → guards →
cleanup, comparison tables, and final finish/empty-state readback. It reuses
`conductor.CPO`, `conductor.py` commands, token lifetime checks, and table formatters; raw
latency/D1/energy judgments are not recomputed or weakened. Run only outside an
active measurement window and under an actual Main-approved release.
예정 대체: 현재 `round.py`·로컬 binding·운영자 장애 주입은 유지하되, [실험 실행 설계](../docs/design/profiling/experiment-execution.md) / [실험 SRS](../docs/spec/experiments/SRS.md)에 따른 정의·독립 실행으로 대체할 예정이다.

The private input JSON uses the fields below **plus the required `deployment`
object described after the example**. All filesystem paths are absolute;
replace illustrative paths/SHAs with approved inputs. The example deliberately
contains no guessed live deployment revision and is not ready to execute.

```json
{
  "sourceSHA": "0123456789abcdef0123456789abcdef01234567",
  "rows": ["E5-5", "E5-7", "E5-6", "E5-3"],
  "rowTag": "9",
  "restoreOriginTag": "223",
  "h": 0.25,
  "rateBasis": {"tag": "3", "binding": "/ops/campaign/run/binding-3.json"},
  "compareTags": ["3", "7"],
  "environment": "/ops/campaign/inputs/environment.json",
  "binding": "/ops/campaign/run/approved-input-binding.json",
  "release": "/ops/campaign/run/release-round-9.json",
  "run": "/ops/campaign/run",
  "evidence": "/ops/deployB/round-9",
  "publication": "/ops/prebuilt/server-publication.json",
  "minimumFreeBytes": 310000000000,
  "kappa": {"reuseRows": "/ops/deployB/prior/kappa-rows.json"},
  "takeoverAfter600Seconds": false,
  "failureInjection": true
}
```
Add `deployment` with actual reviewed `context`, `kubeconfig`, `release`,
`registry`, and positive `expectedRevision`; optional `registryScheme` is
`http` or `https` (default). Do not copy a revision from an old control record.
Namespace comes from the existing environment's `d2.namespace` (default
`chill-system`), API environment from `environment`, source from `sourceSHA`,
and free-byte floor from `minimumFreeBytes`. The old control-number input is
removed, not translated into a new invocation. The round writes a fixed
server-only prebuilt deployment recipe under each attempt and calls
`fast-deploy.sh run` with explicit source, execute and new evidence arguments.
The existing environment's `d2.kubectl` argv must explicitly pin exactly one
`--context` and one `--kubeconfig` agreeing with `deployment`. Both paired and
`--flag=value` forms are accepted; missing, duplicate or mismatched pins and
`--server`/`--cluster` overrides are rejected before evidence construction or
deployment. Round, harness and failure helpers reuse that original argv
unchanged; no alternate environment file is synthesized.


`environment.paths.ops` is the actual external ops root, **not** literal `/ops`.
`run` is the existing campaign containing original snapshots and `initial.json`.
Do **not** create `evidence` or any `full-E5-*-TAG` directories: the runner and
evaluation controller own creation. The runner writes `run/binding-round-9.json` from
the supplied binding, preserving R3 payloads, seeds, rates, traces, and κ while
freezing the selected harness package references and new server provenance.
The pre-existing approved release must name that exact generated `bindingPath`,
`executor: "CampaignOps"`, `command: "evaluation-execute"`, `mainInstructionUTC`, and
explicit `sources: {server, loadgen, power}`; legacy single `sourceSHA` releases
are rejected. It never fabricates approval. The server publication must have
the matching SHA, only `roles.server`, `runtimeBuilds: 0`, and complete build
provenance (`dockerfile`, `buildArgs`, `platforms`, `digest`,
`platformDigests`, `platformReceipts`). The round supplies the receipt as the
server image's `prebuilt` input to the recipe-driven `fast-deploy.sh run`
interface, with an explicit current revision and new private evidence.
No image is rebuilt on the host. A matching live digest skips execute only,
not local validation of the full publication receipt. When deployment runs,
Ready/API/restartCount and registry provenance gates apply.
Any chart drift stops; the round does not accept chart approval.

Optional `harnessSHA` selects a different full commit for the frozen
`ops/harness/evaluation-<sha>` tree (default `sourceSHA`). Missing trees are created
atomically from `git archive`; existing Python files are compared with that
commit before use. Bind micro schedules beforehand; rows `E5-{3,5,6,7}m` must
carry `micro: true` and `restoreStage` in the supplied binding, with their own
bound failure runbook for E5-7m. Original micro restore snapshots use tag 223.
For normal E5-3, the historical default is the unsuffixed `full-E5-3`; other
rows use `restoreOriginTag`. Override with e.g.
`"restoreOrigins": {"E5-3": "223"}` when the actual original exists there.
R3 comparisons default to unsuffixed E5-3 and E5-7 tag `3b`; explicit
`"compareOrigins": {"3": {"E5-7": "3b"}}` maps historical exceptions.

Optional `reservationNodes` freezes the actual participating baseline node names
into the round binding. CPO start/reset and round empty-state gates then wait for
reservations/transitions on those nodes only; excluded-node reservations/actions
remain in evidence without releasing their leases. Reservations without node
scope still block. Omitting this field retains the global gate.

To replay κ once, use `"kappa": {"executable": "/absolute/prebuilt/kappa"}`.
Its fixed argv is `-mode table -workloads
image-classification-tight-slo,smolvlm2-vqa,plc-anomaly -margin 29 -window 60
-windows 20 -passing 19 -workers 3 -scheduler <capture> -rows <output>`.
The result is recorded, never applied to R3 offered rates. `reuseRows` instead
records a hash/reference and zero replays. Both require positive Built rows
for all canonical workloads.

Preflight renews insufficient-lifetime/expired credentials using only the
existing `chill-dashboard-user` and `chill-evaluation-evidence-collector` service
accounts in the configured namespace. `.token`/`.token-collector` are replaced
atomically with mode 0600; originals remain in `.bak` (later backups receive a
timestamp). Tokens never enter evidence or argv. Initial reservation/action
wait is 600s; only `takeoverAfter600Seconds: true` authorizes one direct reset
after the original timeout evidence is preserved. No other recovery or retry
is implicit. failure requires `failureInjection: true`, executes only fresh
`failure-select` operator argv, and retains actual commands/results/timestamps;
STOP does not silently uncordon or reset physical state.

Each attempt retains its own STOP, command logs, tables/comparison JSON/text,
and final summary under `evidence/attempt-*`; `round-state.json` tracks completed
steps and effective row tags. Disk, restartCount/Pod identity and deployment
template guards stop during load as well as at row boundaries. Missing rows
are UNMEASURED, never zero or PASS. Final success requires instances 0,
reservations 0, autonomy off. `finish` restores initial settings, so the summary
records actual final h, including h=0; another round always PUTs h again.
The same round JSON may reuse its evidence root (including repeated `--until 0`)
with a unique attempt directory; `round-config.sha256` rejects a different round
configuration. Older roots are adopted only when their saved state has the same config.

Resume explicitly with the same JSON and `--resume-from 0..7` (also
`preflight`, `deploy`, `reset`, `headroom`, `kappa`, `rows`, `tables`, `finish`).
An existing evidence root with completed steps requires `--resume-from`; a
preflight-only STOP with no completed steps may reuse the root without it.
Row boundaries use `--resume-from 5:E5-6`; individual substeps accept
`5:E5-6:factor|headroom|stage|judge|guard|cleanup`. Incomplete prerequisites
cannot be skipped. A pre-load input correction can use
`--resume-from 5:E5-6 --next-row-tag` to increment the suffix for that row and
remaining rows, preserving previous attempts. A row with `startUTC` can
**never** be replayed by this option; a new tag is not measurement-retry
authorization. Existing factor/reset/stage evidence is never adopted as an
implicit retry. Correct changed top-level JSON in a separately released round
rather than rewriting prior round history.

Use `--until <step>` to exit successfully after that step, recording its canonical
identifier in `round-summary.json` as `stoppedAfter`. It accepts the same names
and substeps as resume; `5`/`rows` includes every configured row, and `5:E5-6`
includes that row's cleanup. The boundary cannot precede `--resume-from`.
For example, `--until 1` stops after deployment; continue with the identical
round JSON and `--resume-from 2`. Early stops do not run later steps or `finish`
and therefore do not imply final empty-state verification.

`python3 -B hack/evaluation/round.py --self-check` checks input validation, ordering,
STOP boundaries and compressed judgment projection entirely offline, without
environment configuration, Go, image builds, deployment or network requests.

