# CHILL Code Intelligence

## Evidence rules

| Claim | Mechanism and authority |
|---|---|
| Go definitions, references, implementations and call hierarchy | Pinned `gopls`, confirmed in returned source |
| Direct call | Source/call hierarchy, including dynamic-dispatch implementations |
| Actual package dependency | `go list` |
| Allowed dependency | Reviewed `.go-arch-lint.yml` and relevant design/decision |
| Controller event path | For/Owns/Watches/mapping registration and focused tests |
| Behavior or test coverage | Current source and tests |
| Desired deployment resources | Validated Helm render with release, namespace and exact values |
| Live runtime state | Explicit captured cluster snapshot |

Controller-runtime delivery is `framework-derived`; setup does not directly call
Reconcile. Indexes locate candidates without establishing correctness.
The [golden queries](golden-queries.md) are the regression oracle.
Update their evidence with intentional implementation changes.

## Workflow and commands

Locate the smallest entry point with source search and `gopls`. Confirm imports
with `go list`; update architecture policy in an approved boundary change.
Verify registration and behavior separately. Deployment claims need renders.

| Command | Purpose |
|---|---|
| `make gopls-check` | Pinned workspace and semantic regressions |
| `make arch-check` | Package mapping, imports and negative fixture |
| `make code-intelligence-check` | Baseline, semantic and architecture checks |
| `make arch-graph` | Ignored on-demand package view |
| `make codegraph-check` | Optional discovery regression |
| `make codegraph-watcher` | Optional watcher lifecycle check |
| `make deployment-graph-check` | Separate default/Profiler render views |
| `make code-intelligence-review` | Required checks and optional deployment review |

The Makefile pins gopls/Go. `hack/gopls-smoke.sh` hardcodes production resolver,
setup and recorded-Job recovery queries; it does not parse the Markdown oracle.
AGENTS.md requires semantic checks for covered changes. CI separately checks
generated artifacts, architecture, lint, tests and Helm, without running gopls.
Configured checks do not establish branch protection or observed acceptance.

## Package architecture contract

Each production package in `api/`, `cmd/` and `internal/` maps to one reviewed
component. Local tools/nested worktrees are excluded. Test files and `test/`
are outside architecture enforcement.

Adapters import cores, not conversely. Composition-root permissions do not
relax internal direction. Operator/Profiler runtime packages do not import
each other.

`modelcompat` and the `evidencestore` root have no Kubernetes imports.
Derivation has no direct Kubernetes imports; firstfloor carries Kubernetes
value projections. `profiling/run` includes projections and a Node grant reader.
A CHILL-leaf need not be transitively Kubernetes-free.

Scheduler direction is `serving → scheduler → {v1,v2} → protocol`.
Serving also uses protocol ports. Algorithms import neither their sibling,
selector, Serving, Kubernetes nor evidence adapters. Protocol imports no CHILL
package. Exact evaluation and physical admission remain in Serving. Each
scheduler package has its own component. `planSearchStrategy` defaults to `v2`
and also supports `v1`.

Firstfloor is the native leaf. `RuntimeContainer` projects a frozen actor
without creating resources. Serving operations own preparation/removal/power;
Kubernetes adapters supply readers/writers. Derivation owns shared dispatch math.

Evaluation service owns application operations without depending on controller
or HTTP DTOs. Controller owns pool acquisition/asynchronous execution; HTTP
converts results. Query owns read models/coverage/refresh. Composition roots
adapt Serving/catalog and campaign-evidence dependencies.

Dashboard is a Node deployment unit outside Go architecture lint. Its boundary
is fixed HTTPS ChillServer; native pnpm/browser checks remain separate.

Exact direction is in [architecture policy](../../../.go-arch-lint.yml).
See [profiling dependency rule](../profiling/README.md#dependency-rule),
[Metric Observation Boundary](../../adr/0013-metric-observation-boundary.md)
and [Evidence Store](../profiling/components/evidence-store.md).

## Optional local discovery

CodeGraph is local and optional, absent from correctness CI and contributor
requirements. Use it for candidate symbols, callers, snippets and exploration.
Complete impact, interfaces, tests, events, imports and deployment resources
still need the authorities above. See [CodeGraph](codegraph.md).

### Optional Graphify setup

Recorded project setup:

```sh
uv tool install --python 3.12 graphifyy==0.9.97
graphify install --project --platform agents
omp plugin install "$(graphify pi guard path)"
```

Restart OMP for `/skill:graphify`. Local code-only extraction and bounded view:

```sh
graphify extract . --code-only --max-workers 4
graphify cluster-only . --no-label --no-viz
graphify export html --node-limit 5000
graphify query "serving" --budget 1000
```

Keep graphs/reports in ignored `graphify-out/`. Code-only extraction needs no
API key and does not analyze document semantics. Check freshness; confirm
findings with source authorities.

## Deployment and maintenance

Keep default and Profiler-enabled renders separate. Diagrams are review views.
See [deployment topology](deployment-topology.md) for commands/isolation/limits.
Runtime pins belong to [`Makefile`](../../../Makefile) and
[`firstfloor.Dockerfile`](../../../build/docker/firstfloor.Dockerfile);
execution/image semantics belong to the
[worker contract](../profiling/components/measurement-worker.md).
These sources do not prove published images or hardware qualification.

Pin tools locally. Keep indexes ignored and CodeGraph telemetry/update checks/
runtime fallback downloads disabled. Do not upload indexes from CI. Upgrades
rerun applicable regressions. Intentional source/chart changes update the oracle.
Visualization regressions leave manifest/resource authority intact.
