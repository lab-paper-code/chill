# CHILL Code Intelligence

## Current decision

CHILL uses separate tools for separate evidence classes. No graph or index is a
source of truth by itself.

| Need | Mechanism | Authority |
|---|---|---|
| Go definitions, references, implementations, and call hierarchy | pinned `gopls` | `gopls` result confirmed in source |
| Actual Go package dependencies | `go list` | `go list` output |
| Allowed package dependencies | pinned `go-arch-lint` | reviewed `.go-arch-lint.yml`; enforced in CI |
| Fast agent discovery | pinned CodeGraph | optional candidate discovery only |
| Desired Kubernetes topology | validated `helm template` plus pinned KubeDiagrams | rendered manifests are authoritative; diagrams are review views |

CodeGraph is optional, local, absent from CI, and never required for
contributors. KubeDiagrams is also local and absent from correctness CI. Helm
rendering remains the deployment-topology authority.

## Evidence rules

| Claim | Required verification |
|---|---|
| Go definition, reference, or implementation | `gopls`, then returned source |
| Direct function call | source or `gopls` call hierarchy; also check implementations for dynamic dispatch |
| Actual package edge | `go list` |
| Allowed package edge | `.go-arch-lint.yml` and the relevant design or ADR |
| Controller event path | exact `For`, `Owns`, `Watches`, or mapping registration plus focused tests |
| Behavioral impact or test coverage | source and tests; never an index result alone |
| Desired deployment resources | `helm template` with recorded release, namespace, values, and `--set` inputs |
| Live runtime state | an explicitly captured cluster snapshot |

Controller-runtime event delivery is reported as `framework-derived`, not as a
direct Go call from setup code to `Reconcile`.

The current six regression answers and their mandatory evidence are in
[golden-queries.md](golden-queries.md).

## Development workflow

1. Use `gopls`, source search, and direct reads for semantic navigation.
   CodeGraph may reduce initial discovery work but does not replace these
   checks.
2. Verify package direction with `go list`. When an approved boundary changes,
   update `.go-arch-lint.yml` in the same change.
3. Verify controller paths from registration source and focused tests.
4. Verify deployment claims from rendered Helm output, not template-source
   search or a code index.
5. Before merge, run the applicable checks below.

| Command | Purpose |
|---|---|
| `make code-intelligence-check` | baseline, `gopls`, architecture coverage, architecture gate, and negative fixture |
| `make gopls-check` | pinned Go semantic regression checks |
| `make arch-check` | package mapping and allowed-import enforcement |
| `make arch-graph` | ignored, on-demand package diagram; not a correctness gate |
| `make codegraph-check` | optional CodeGraph golden-query regression check |
| `make codegraph-watcher` | optional MCP watcher create/modify/rename/delete check |
| `make deployment-graph-check` | validated default and Profiler-enabled deployment graphs |
| `make code-intelligence-review` | required checks plus optional deployment-graph review |

Normal build, test, generated-artifact, Helm, and live-environment validation
remain separate from code-intelligence checks.

## Package architecture contract

- Every in-scope production package maps to exactly one reviewed component.
- Plain domain packages remain independent of APIs, Kubernetes clients,
  controller-runtime, and higher-level runtime packages.
- Adapter packages may import their core contracts; core contracts may not
  import adapters.
- `cmd/**` packages are composition roots. Their wiring permissions do not
  relax internal package rules.
- Operator and Profiler runtime packages do not import each other.
- Test files and `test/` packages are measured but are not yet enforced by the
  architecture gate.
- `chill-dashboard/` is a CHILL-internal Node web/BFF deployment unit, not a
  Go package or an invented architecture-lint component. Its API boundary is
  the fixed HTTPS owner; validate it with its native pnpm checks and browser
  smoke separately from `go list` and `.go-arch-lint.yml`.

Detailed direction is encoded in `.go-arch-lint.yml` and grounded in the
[profiling dependency rule](../profiling/README.md#dependency-rule),
[MetricObserver ADR](../../adr/0013-metric-observation-boundary.md), and
[Evidence Store boundary](../profiling/components/evidence-store.md#dependency-direction).

### Reviewed model and serving boundaries

The following reviewed production boundaries follow the native firstfloor
cutover. Source imports are authoritative; the command below refreshes the
import evidence. Component permissions live in `.go-arch-lint.yml`.

| Package / component | Actual internal/API imports |
|---|---|
| `modelcompat` / `model-compat` | None; stdlib-only pure compatibility and tensor/native-setting contracts |
| `modelcatalog` / `model-catalog` | `api/v1alpha1`, `modelcompat` |
| `serving` / `serving-core` | `evidencestore`, `firstfloor`, `metricobserver`, `modelcompat`, `profiling/derivation` |
| `serving/entrypoint` / `serving-entrypoint` | `api/v1alpha1`, `serving`, `serving/kube` |
| `firstfloor` / `firstfloor` | No CHILL package imports; bounded native executor leaf |
| `serving/firstfloor` / `serving-firstfloor` | `firstfloor`, `serving` |
| `profiling/run` / `profiling-run` | `modelcompat`, native `firstfloor` metadata types shared with the measured executor |
| `serving/kube` / `serving-kube` | API, platform component helpers, device/model/evidence adapters, metric observation, admitted profiling contracts, power-window integration, transition-cost leaf, serving core and native adapter |

```sh
go list -f '{{.ImportPath}} {{join .Imports " "}}' \
  ./internal/modelcompat ./internal/modelcatalog ./internal/serving \
  ./internal/serving/entrypoint ./internal/serving/kube ./internal/serving/firstfloor ./internal/firstfloor
```

The native executor leaf never imports Serving, profiling or Kubernetes. Serving
reuses its batching policy directly for scheduler replay; the SDK adapter joins
actual native readiness/state to admitted profiles. The Kubernetes adapter owns
Pod preparation and removal, immutable bundle materialization and existing
NodeActuation submission. The transition-cost core may consume firstfloor
metadata and plain metric observations, not Serving or its Kubernetes adapter.
Composition roots assemble these owners. No architecture-gate pass is implied
by this boundary description.

The named-input projection also imports `modelcompat`:
`go list -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/profiling/profiler/projection`
captures that actual edge. `profiler-projection → model-compat` is reviewed in
`.go-arch-lint.yml`; it reuses the tensor contract, not a Serving runtime.

## Optional CodeGraph contract

CodeGraph is retained because it consistently reduced discovery work for
focused entry-point and package-boundary questions. It repeatedly missed or
partially answered interface completeness, API-field impact, affected tests,
and Helm resource topology.

Therefore:

- use it for candidate entry points, callers, related symbols, source snippets,
  and scoped package exploration;
- do not use it to assert complete impact, test coverage, controller event
  delivery, allowed imports, or deployment resources; and
- keep mandatory source, `gopls`, `go list`, test, and Helm verification.

Installation, privacy controls, local commands, and rollback are in
[codegraph.md](codegraph.md).

## Deployment topology contract

KubeDiagrams converts the repository's validated Helm renders into portable
SVG and machine-readable DOT JSON review artifacts. It does not read the chart
directly and does not replace manifest, test, or live-cluster validation.

Two canonical profiles remain separate:

- default chart values; and
- `profiler.enabled=true` with the synthetic transition-observer Node declared
  by the review profile.

The check verifies the exact Profiler-only resource set, Profiler RBAC
relationships, `ChillSystem` ConfigMap references, and the absence of a
Helm-owned Profiler Deployment. The Operator remains responsible for that
operand. Commands, artifacts, metadata, and limitations are in
[deployment-topology.md](deployment-topology.md).

## Freshness, security, and maintenance

| Artifact | Refresh rule | Stored in Git |
|---|---|---|
| `gopls` state | live for LSP; reconnect or reload detached MCP after changes | no |
| `.go-arch-lint.yml` | same change as an approved boundary decision | yes |
| package graph image | regenerate from the current commit | no |
| `.codegraph/` index | watcher plus clean status before relying on it | never |
| rendered manifests or diagrams | regenerate for every commit and values set | no by default |

- Keep tools pinned and repository-local; do not use `latest` or `curl | sh`.
- Keep telemetry and runtime fallback downloads disabled for CodeGraph.
- Do not upload repository indexes from CI.
- A tool upgrade reruns its golden checks before merge.
- An intentional source or chart change updates
  [golden-queries.md](golden-queries.md) in the same change.
- If CodeGraph freshness or verified discovery quality regresses, remove its
  MCP entry and run `make codegraph-uninit`. Baseline development is unchanged.
- If KubeDiagrams output regresses, retain `manifest.yaml` and `resources.txt`
  as the desired-state evidence and remove only the generated graph view.

## Native runtime and scheduler deployment

`make docker-build-firstfloor` builds `build/docker/firstfloor.Dockerfile`.
It ships `/opt/chill/bin/serving-worker`, `/opt/chill/bin/serving-entrypoint` (including
`--materialize-profile` mode), `/measurement-worker`, the persistent firstfloor
runner and its sibling ORT/RKNNLite helpers. CPU defaults install NumPy 2.2.6
and ONNX Runtime 1.27.0. `make build` includes serving-worker and
serving-operation; `make test-runtime` selects the firstfloor runtime suite.
No published image digest or hardware qualification is implied.

CUDA deployment requires an operator-selected NVIDIA CUDA/cuDNN base compatible
with the exact ONNX Runtime GPU wheel and target architecture. Build with
`RUNTIME_IMAGE` set to that base and `ORT_PACKAGE=onnxruntime-gpu`; use NVIDIA's
device plugin/container runtime and the physical GPU UUID inventory. See
[ONNX Runtime CUDA requirements](https://onnxruntime.ai/docs/execution-providers/CUDA-ExecutionProvider.html#requirements).
Do not install both CPU and GPU ORT distributions in one image. RKNN deployment
requires a compatible ARM64 vendor base containing the exact Rockchip
`rknn-toolkit-lite2` wheel and `librknnrt.so`, matched to the board driver and
compiled model. Obtain these from
[Rockchip's vendor repository](https://github.com/airockchip/rknn-toolkit2).
Supply that base as `RUNTIME_IMAGE`; the copied RKNNLite helper loads the real
vendor SDK. Neither deployment falls back to synthetic inference or CPU when
its selected provider is absent.

Scheduler configuration is explicit and independent of Profiler enablement:
set `servingEntrypoint.scheduler.configMap` to an existing ConfigMap in the
serving namespace and `scheduler.key` (default `scheduler.json`) to its JSON
entry. The owner reads it directly using
`--serving-scheduler-configmap`/`--serving-scheduler-config-key`; settings updates
require update permission on that exact named ConfigMap. The chart does not
grant blanket ConfigMap update permission. `config/samples/serving-scheduler.json`
is a valid controls-off example, not a calibrated deployment. Before enabling
controls, populate physical CPU/GPU inventory, non-serving reservations, measured
profile preparation bundles, meter identities, immutable operation-cost
references and existing actuator-backed power requests from the actual cluster.
Mirror each configured power-cost measurement's namespace/name in Helm
`servingEntrypoint.scheduler.powerCostSources` to grant named read access to
the retained measurement and its immutable preparation ConfigMap.
Raw service samples remain in immutable evidence bundles, not ConfigMaps.
The owner creates/removes native Pods and their small config/credential objects;
NodeActuation remains the sole power actuator and pool membership uses the
existing Node label ownership.
