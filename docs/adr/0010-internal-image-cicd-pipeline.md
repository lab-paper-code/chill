# 0010 Internal Fast Image Deployment Loop

## Status
Accepted

## Context
Docker Hub remains the public release/distribution path. Internal images shorten
testbed feedback without replacing merge/release correctness gates. Deployment
must preserve approved state and source provenance rather than reinstalling a
release or silently restoring old stored contracts after failure.

## Decision
Replace the existing `hack/fast-deploy.sh` implementation with a thin Python
runner for `hack/deploy/main.py`. There is one deployment path, not a new path
alongside an old reset implementation. The interface is
`hack/fast-deploy.sh run RECIPE.json`; Make forwards `FAST_DEPLOY_ARGS` unchanged.
The [hack README](../../hack/README.md#recipe-driven-roll-forward-deployment)
owns the full recipe, source role catalog, and executable examples; the
[operations guide](../operations/ops-agent-guide.md#recipe-기반-roll-forward-배포)
owns operational limits.

| Pipeline boundary | Contract |
|---|---|
| Modes | Default/`--dry-run`: local plan only, no network/build/lock/evidence writes. `--inspect`: explicit read-only cluster/chart render, no custom commands or publication. Only `--execute` authorizes publication/mutation. Inspect/execute require new private evidence directories. |
| Target | Required kubeconfig, context, namespace, release, registry, API environment, positive expected current revision and free-byte floor. Revision/status changes stop before mutation. No guessed target, automatic image selection, reset, rollback, resume, or legacy flag shim. |
| Sources | Resolve deployment source to a full Git commit and archive it. Separately versioned scripts use explicit `toolSources` commits. Fixed argv placeholders select those frozen trees, never mutable working copies or shell interpolation. |
| Images | Explicit selection from all 16 roles in `images.py`, reusing source Dockerfiles, Make runtime defaults, role platforms and chart mappings. Do not infer runtime/harness declaration mappings or probes. Public image defaults remain unchanged. |
| Publication | Buildx publication or per-image prebuilt receipt; verify registry manifests, actual platforms and OCI source revision. Record sourceSHA, Dockerfile, buildArgs, platforms, digest, platformDigests and platformReceipts. Apply digest-pinned images. Keep credentials out of recipes/argv/build arguments. |
| Phases | Fixed preflight/stage/quiesce/migrate/configure/accept command lists, each with bounded argv commands. Stage creates declared evidence-relative generatedValues, frozen before final render/quiesce. Runtime/harness activation requires explicit stage/accept and declarations/new-input values, or the fixed six-role FirstFloor `runtimeDeclarations` implementation using the existing inspector (default 3600-second budget). No fabricated generic migration or acceptance script. |
| Review | Preserve live user values plus explicit source values with Helm `--reset-values`, not `--reuse-values` or computed `--all` defaults; complete explicit replacement values require opt-in. Back up live values/manifests/hooks and retained resources privately. Reject chart drift unless the approved file exactly matches the current chart diff. Recheck revision and perform server-side dry-run before mutation. |
| Roll-forward | Optional explicit quiesce and source-pinned migration/configuration; then Helm update, readiness and acceptance. Helm/chart solely owns CRDs; no separate SSA apply/wait or ownership patch. Removing a CRD requires keep annotation already present on the live CRD; never add it automatically. Failure retains partial state/evidence and stops. A new reviewed deployment moves forward; no automatic retry/restore/rollback or evidence-directory reuse. |
| Acceptance | Operational Pod spec/container identity, Ready, zero restarts, matching index/platform imageID, authenticated APIs and required-step completion gate deployment READY. Profile-ID/count, batch, usable-coverage and measurement comparisons are report-only; expectedProfileCount/expectedCoverageUsable are report targets. Preserve passed:false/errors and partial results without old-success fallback. Actual data/backup loss, security/auth/token and CLI failures remain blocking. READY is not scientific, browser, hardware or campaign PASS. |
| Evidence | New private evidence, frozen inputs/source, bounded command stdout/stderr, receipts, renders/diffs, API before/after captures, readiness, performance.json/command-timings.jsonl and failure records. Coverage-before-profiles avoids repeated captures while warming, without claiming an unmeasured global speedup. Execute shares `/tmp/chill-heavy.lock`; inspect does not. Tokens are never evidence or argv. |
| Authorization | First live use follows the current cutover and is performed solely by deployment session main under explicit authorization. Its dashboard ten-role selection includes server/profiler/query/dashboard and all six FirstFloor builds/publications, separate approved exact-image probes and measured declaration activation; publicationOnly does not satisfy this use. Implementing/documenting the tool is not live authorization. No default PR push/deployment. |
| Normal gates | Repository merge/release verification remains separate. CRD authority stays `make manifests` → `config/crd/bases` → `make helm-sync-crds` → `charts/chill/templates/crds`. Runnable local regressions: `python3 -B hack/deploy/test_deploy.py` and `python3 -B hack/deploy/test_acceptance.py`. |
| Pull access | Existing node auth/trust remains an operational prerequisite; this change does not configure nodes, credentials, TLS trust or retention. No new workflow, registry service, signing or retention framework is introduced. |
| Additional checks | queryService adds direct Query 401/200 checks via private port-forward; dashboardCheck requires HTTP 200 HTML, not browser verification. Disk gates include minFreeBytes and maxDiskUsedPercent (default 85); actual builders record buildx du before/after. The runner does not prune images/cache/data. |
| Scientific diagnostics | Optional baselineDirectory reports the original frozen 259-profile baseline and 17-profile difference separately from current before-242/after diagnostics; historical raw evidence remains untouched. Default A selects the latest measuredAt cell with a deterministic tie and honors the operator's existing replacement annotation priority. No old-success fallback or new allow-flag framework. |
| Final staged review | Before publication and each custom phase, require current context to match the recipe without flattening token-bearing kubeconfig. Base chart approval is separate from exact stagedDiffApproved/--staged-diff-approved approval for final-render configuration changes beyond trusted verified-image overlays and validated built-in probe output. Approve before quiesce. |
| Records and entry points | Keep strict current-before/after diagnostics separate from the original frozen four-criterion baseline report; unknown coverage is null/source-incomplete, not a blocker. The tracked first-use recipe is hack/deploy/plans/dashboard-reads-20261103.json, with source 1b81e0d5 and confirmed revision 178/control-315. Later changes require a new review. make test-deploy is included in make test. |
| Configuration and rollout | Optional checkConfiguration builds the selected source's actual chill-server offline validator once and validates the external scheduler ConfigMap using actual flags/live JSON or explicit schedulerConfigMapFile. Dashboard first use enables it; prebuilt rounds do not, so no default Go build. Invalid real configuration/CLI execution blocks. Optional waitFor explicitly waits for Deployment/DaemonSet rollout within readiness budget, with actual timings retained. |
| Measurement loss | Corrected runbook zero-work checks read five CR kinds and owned Jobs across all namespaces at preflight and immediately before upgrade. Terminal DOMs need no spec.conditions. Incomplete/unfrozen/cleanup/deleting state blocks; no automatic drain/delete. |
| Probe executability | Preflight and the runtime stage derive actual targets from the source inspector and record Node Ready plus CUDA GPU allocatable. Missing/unready/GPU-less targets stop before probing; no retry/repair or foreign-Pod changes. Source 1b81e0d5 has 14 declarations and 8 probe targets. |

### Recorded testbed evidence (2026-07-10)
The HTTP registry `155.230.35.213:5000` was configured on `jetson-nano`, `jetsonx`, `jetsono`, `lattepanda`, `orangepi`, `orin-nano`, `rasp5`. Each has `/etc/containerd/certs.d/155.230.35.213:5000/hosts.toml` with server/host `http://155.230.35.213:5000` and capabilities `["pull", "resolve"]`. CRI `config_path` is `/etc/containerd/certs.d`; most use `[plugins."io.containerd.grpc.v1.cri".registry]`, while orin-nano uses v1 `[plugins.cri.registry]` (effective dump converts to v2).

Containerd was restarted on each node. Backups use `.bak.chill-fast-<timestamp>` beside edited files, including NVIDIA Jetson `/etc/containerd/config.d/*.toml` fragments.

Validation image: `155.230.35.213:5000/chill/chill-node-discovery:pull-smoke-postconfig-08bba02-20260710-225842`, digest `sha256:8957fd6a70565e4add4ece0cc37fb4f918644b84eb1397265fc4d3fbb5e4e17a`. `crictl pull` through CRI succeeded on all seven nodes. Orin-nano used a temporary arm64 `/tmp` crictl, removed afterward; its pinned Kubernetes Pod was evicted before pull for DiskPressure, so CRI pull—not Pod rollout—is its evidence.

## Consequences
Development feedback uses an explicit, state-preserving roll-forward contract.
Docker Hub remains external release; the internal registry remains development/
testbed publication. Failures stop with private evidence instead of hiding
partial state behind destructive reinstall or automatic rollback. Deployment
readiness is not scientific, browser or hardware acceptance, and normal
merge/release verification remains separate.
