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

The following CHILL-internal edges were captured with `go list` for the six
packages below. They are actual imports, not call edges or runtime validation.
Component names and permissions come separately from `.go-arch-lint.yml`.

| Package / component | Actual internal/API imports |
|---|---|
| `modelcompat` / `model-compat` | None; stdlib-only pure compatibility and tensor/native-setting contracts |
| `modelcatalog` / `model-catalog` | `api/v1alpha1`, `modelcompat` |
| `serving` / `serving-core` | `evidencestore`, `modelcompat`, `profiling/derivation` |
| `serving/entrypoint` / `serving-entrypoint` | `api/v1alpha1`, `serving`, `serving/kube` |
| `serving/triton` / `serving-triton` | `modelcompat`, `serving` |
| `serving/kube` / `serving-kube` | `api/v1alpha1`, `deviceclass`, `evidencestore`, `modelcatalog`, `modelcompat`, `profiling/campaign`, `profiling/derivation`, `profiling/profiler/evidence`, `profiling/profiler/projection`, `profiling/run`, `serving`, `serving/triton` |

```sh
go list -f '{{.ImportPath}} {{join .Imports " "}}' \
  ./internal/modelcompat ./internal/modelcatalog ./internal/serving \
  ./internal/serving/entrypoint ./internal/serving/kube ./internal/serving/triton
```

Allowed directions retain a plain Serving core without API, HTTP or Kubernetes
clients. `model-catalog` may depend on API and `model-compat`; entrypoint may
depend on API, core and its concrete kube adapter; Triton may depend on core and
`model-compat`. The kube adapter may use the listed model, evidence, campaign,
derivation, evidence/projection, `profiling-run` and runtime boundaries, not
Profiler orchestration, controllers or its candidate resolver. Composition roots assemble them.
The Triton adapter also imports Kubernetes resource quantities; that does not
turn it into a Kubernetes resource writer. No architecture-gate pass is implied
by this dependency capture.

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
