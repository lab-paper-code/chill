# CHILL Code Intelligence and Architecture Analysis

## Status

Adoption design with the reproducible Phase 0 baseline, repository-local
Phase 1 `gopls` path, and Phase 2 package-architecture gate implemented on
2026-07-16. The
[golden-query record](golden-queries.md) fixes the current answer key and
verification commands.

The CI workflow runs `make arch-check`; editor and MCP client configuration are
not installed automatically. The Phase 3 CodeGraph local pilot is in progress
with one of three required sessions complete. It may still be rejected without
removing the earlier baselines. Phase 4 requires its own review.

## Decision

CHILL will use a layered code-intelligence stack rather than one universal
index or graph:

| Layer | Selected mechanism | Responsibility | Authority |
|---|---|---|---|
| Go semantic navigation | `gopls` | definitions, references, implementations, symbols, types, and call hierarchy | semantic navigation baseline for saved Go source |
| Go package architecture | `go list` plus `go-arch-lint` | actual import edges, allowed package direction, CI enforcement, and package diagrams | `go list` for actual edges; reviewed architecture rules for allowed edges |
| Agent exploration | CodeGraph, optional local pilot | fast source discovery and candidate impact paths | non-authoritative hint only |
| Rendered deployment topology | `helm template` plus KubeDiagrams | Kubernetes resource, reference, selector, and ownership diagrams for an exact values set | rendered manifests for desired deployment state |

The compiler, repository source, tests, and rendered manifests remain the
underlying sources of truth. A graph is a view over one of those sources; it is
not a replacement for them.

## Why the design is layered

CHILL has at least four different relationships that are easy to collapse into
one misleading picture:

1. a Go symbol refers to or calls another Go symbol;
2. one Go package imports another package;
3. controller-runtime maps Kubernetes events to reconciliation requests; and
4. rendered Kubernetes resources refer to, select, or own other resources.

No selected tool proves all four. Keeping the layers separate makes every
answer state what evidence it used and prevents an inferred control path from
being presented as a verified execution path.

## Goals

- Make definition, reference, interface-implementation, and type navigation
  reliable for the growing Go workspace.
- Turn stable package-boundary decisions already recorded in CHILL design and
  ADR documents into a merge-time check.
- Give agents a faster optional discovery path without allowing an index to
  override source inspection or semantic verification.
- Generate reproducible deployment diagrams from exact Helm render inputs.
- Keep every added mechanism local, pinned, removable, and small enough to
  operate without a separate server or database service.

## Non-goals

- A single graph that joins Go calls, controller events, Kubernetes topology,
  runtime traffic, and research evidence.
- Hosted or organization-wide code search.
- Runtime tracing, metrics, or cluster observability.
- Replacing `go test`, `go vet`, `golangci-lint`, `go list`, Helm validation,
  or live verification.
- Treating Helm template source as if it were a concrete deployment.
- Automatically inferring or approving architecture policy from current
  imports. Existing imports may contain debt; design and ADRs define intent.
- Committing local indexes or routinely committing generated diagrams.

## Current repository baseline

The following snapshot was measured on 2026-07-16 and is context, not a fixed
requirement:

| Item | Current value |
|---|---:|
| Non-spike Go files | 179 |
| Non-spike Go packages | 32 |
| Unique repository-internal import edges | 67 |
| Helm template files containing template expressions | 24 |
| Default rendered resources | 23 |
| Rendered resources with `profiler.enabled=true` | 30 |

The host currently runs Go 1.23.4 while `go.mod` declares Go 1.22.12. Phase 1
pins `gopls` v0.23.0 and its tooling command Go 1.26.4 under the ignored local
`bin/` directory. Phase 2 similarly pins `go-arch-lint` v1.16.0 and builds it
with Go 1.26.4 under `bin/`; it is not host-installed. CodeGraph,
KubeDiagrams, and Graphviz are not installed. Docker and Helm are available.
The baseline must be remeasured after relevant package or chart changes because
the counts will continue to move.

## Evidence contract

Every code-intelligence answer or diagram must identify its evidence class.

| Claim | Required verification |
|---|---|
| Go definition, references, or implementation | `gopls`, then source at the returned locations |
| Actual package dependency | `go list`; a linter or diagram may present the result |
| Allowed package dependency | reviewed `.go-arch-lint.yml` derived from current design and ADRs |
| Direct function call | `gopls` call hierarchy or source; dynamic calls require references and implementations too |
| Controller event path | exact `For`, `Owns`, `Watches`, or mapping registration plus focused tests |
| Behavioral impact or test coverage | source and tests; never an index result alone |
| Desired Kubernetes topology | `helm template` output from the recorded chart, values, release, and namespace |
| Actual runtime topology | an explicitly captured cluster snapshot; outside the default adoption scope |

### Controller-runtime exception

Calls such as `For`, `Owns`, `Watches`, and
`EnqueueRequestsFromMapFunc` register event behavior. The later event-to-
`Reconcile` transition is performed by controller-runtime and is not an
ordinary direct call edge in CHILL source.

A symbol or call graph may locate the registration and reconciler, but it must
not claim that it proved the runtime event path. Such a path is reported as
`framework-derived` and cites the registration source and its focused test.
This rule also applies to future informer callbacks, webhooks, and work queues.

## Layer 1: Go semantic navigation

### Role

`gopls` is the default semantic engine for Go. Editors use its LSP service.
Agents may additionally use its experimental MCP server when the client
supports it. The MCP surface requires `gopls` v0.20 or later, but the exact
version still follows the pinning and toolchain policy below.

The expected operations are:

- definition and reference lookup;
- workspace symbol search;
- interface implementation lookup;
- type hierarchy; and
- incoming and outgoing call hierarchy.

Call hierarchy is incomplete for dynamic dispatch. An interface call must be
checked with implementation and reference queries before describing impact.

### Workspace freshness

- LSP attached to an editor can observe that editor's current buffers.
- Detached `gopls mcp` observes saved files on disk.
- CI does not depend on the MCP surface.
- An answer must say when unsaved buffers may differ from the indexed view.

### Toolchain policy

The newest `gopls` supports only the two most recent Go command releases, even
though it can analyze source written for older Go language versions. Therefore
the Go command used by `gopls` must be aligned with its documented support
window before installation. This does not require changing the module's
`go 1.22.12` directive.

The implementation change will:

1. record an exact `gopls` version;
2. record the Go command version used to run it;
3. verify that pair against the release support policy; and
4. run workspace-load, definition, reference, and implementation checks.

If the host Go command cannot be upgraded immediately, an older `gopls`
release whose documentation includes that command version may be pinned as a
time-bounded transition. An unpinned `@latest` install is not an accepted
steady state.

### Acceptance gate

Phase 1 passes when `gopls` loads all non-spike packages without workspace
errors and answers the Phase 0 golden queries for:

- the `ProfilingRun` reconciler and its setup function;
- the interface and concrete implementation that freeze a Run intent;
- callers and implementations at the Evidence Store boundary; and
- references to a selected API status field.

Returned locations must agree with direct source inspection. A failed or stale
MCP integration does not block using LSP; it blocks only the MCP addition.

## Layer 2: Go package architecture

### Role

`go list` is the oracle for the actual Go package import graph.
`go-arch-lint` compares that graph with a reviewed desired graph, exits
non-zero on violations, and can generate a package-level diagram.

This layer enforces import direction only. It does not prove function calls,
controller events, data flow, or runtime behavior.

### Initial invariants

The first ruleset must encode only boundaries that are already explicit in
CHILL design or ADRs:

1. Plain leaf packages such as `internal/modelcompat`,
   `internal/profilingrun`, `internal/profilederivation`,
   `internal/powerobserver`, and `internal/evidencestore` do not import CHILL
   APIs, Kubernetes clients, controller-runtime, Operator packages, or
   Profiler controller packages.
2. `internal/powerobserver/edgemetrics` may depend on the PowerObserver core;
   the core may not depend on that adapter.
3. `internal/evidencestore/s3` may depend on the Evidence Store core; the core
   may not depend on the S3 adapter.
4. `internal/operator/profiler` manages the Profiler operand and does not
   import Profiler application or profiling-domain leaf packages.
5. `internal/profiler/controller` may adapt the API, Kubernetes, and
   controller-runtime to `internal/profiler`; the application package may not
   import the controller or Kubernetes adapter packages.
6. Operator and Profiler runtime packages do not import each other.
7. `cmd/**` packages remain composition roots and may assemble concrete
   implementations; permission at the composition root does not relax an
   internal package rule.

These invariants come from the existing
[profiling dependency rule](../profiling/README.md#dependency-rule),
[minimal Profiler package shape](../profiling/plans/04-minimal-profiler-control-path.md#package-and-dependency-shape),
[PowerObserver decision](../../adr/0013-power-observation-boundary.md), and
[Evidence Store boundary](../profiling/components/evidence-store.md#dependency-direction).
Proposed-but-unimplemented package shapes do not become lint components until
their design status and implementation boundary are separately approved.

The implementation must map each in-scope package to one non-overlapping
component. Broad wildcards that silently place a package in multiple
components are rejected. Packages not yet governed remain visible in a
coverage report rather than being implicitly declared valid.

### Repository integration — implemented

Phase 2 adds:

- a reviewed `.go-arch-lint.yml`;
- an exact tool version or container digest;
- `make arch-check` for local and CI enforcement;
- `make arch-graph` for an on-demand SVG or DOT artifact; and
- a package-to-component coverage check based on `go list`.

`go-arch-lint` is fixed at v1.16.0. Although that release requires Go 1.25 or
later when built from source, its repository-local installer reuses the pinned
Go 1.26.4 tooling command. It does not use a floating tag or change the module's
Go 1.22.12 target.

The initial ruleset maps all 29 in-scope production packages to 17
non-overlapping components. The current tree has no legacy exception. Advanced
DI inference is disabled because it attributes valid `cmd/profiler`
composition-root wiring to internal packages; the enforced oracle is the
saved-source import graph.

### CI placement

`make arch-check` runs after generated-artifact verification and before the
test suite. A violation blocks merge. `make arch-graph` does not run as a
correctness gate; CI may upload its output as a review artifact.

Test files and `test/` packages are measured but not enforced by this initial
gate. The companion coverage check currently reports one test-only internal
edge attached to a production package, plus three excluded `test/` packages
with four internal edges. It prints `architecture.test_files_enforced=false`
instead of implying that tests are covered.

## Layer 3: optional agent exploration index

### Role and authority

CodeGraph may be piloted as a local MCP and CLI discovery accelerator. It can
return likely entry points, related symbols, callers, callees, and source
snippets with fewer file-by-file searches.

It has no CI role and no authority to make any of these claims without the
evidence contract above:

- an execution path is complete;
- a change is safe;
- a test covers or does not cover a path;
- a package dependency is allowed; or
- a controller event reaches a reconciler.

### Local pilot result

The formal first pilot session on 2026-07-16 produced this index:

| Measure | Observed result |
|---|---:|
| Files indexed | 279 |
| Indexing time | 1.2 seconds reported by CodeGraph; 2.36 seconds for the full make target |
| Graph nodes | 3,266 |
| Graph edges | 8,138 |
| Local index size | 9.87 MiB |

The index included current untracked files and connected the production
`RunIntentFreezer.Freeze` interface call to `IntentFreezer.Freeze`, but did not
include the recording fake in that relationship. Broad natural-language setup
queries still return unrelated reconcilers. `IntentDigest` impact resolves the
same-named annotation constant rather than the API field, and the affected-test
query misses existing tests. Helm discovery does not recover the authoritative
rendered resource set.

The result supports continuing sessions 2 and 3, not baseline adoption. See the
[Phase 3 pilot record](codegraph-pilot.md) for exact results and commands.

### Isolation and configuration

The pilot must satisfy all of the following:

- pin an exact version and verify the release artifact;
- set `CODEGRAPH_TELEMETRY=0` or `DO_NOT_TRACK=1`;
- prevent runtime fallback downloads where the selected distribution supports
  that control;
- add `.codegraph/` to `.gitignore` before creating an index;
- keep the SQLite index local and never upload it as a CI artifact;
- use `install --print-config` or equivalent manual MCP configuration, with no
  automatic edits to repository or agent instruction files; and
- keep ordinary source-reading tools available.

The MCP server's own guidance may encourage trusting index results without
re-verification. That guidance is incompatible with CHILL's evidence contract
and is explicitly overridden by this document.

### Pilot gate

The pilot uses the same six golden questions in at least three separate coding
sessions. It passes only if:

1. every answer includes the mandatory source locations identified in the
   Phase 0 answer key;
2. create, modify, delete, rename, and untracked-file cases are reflected after
   the watcher debounce and a clean `codegraph status`;
3. interface implementations agree with `gopls`;
4. package edges agree with `go list`;
5. controller-runtime edges are labeled `framework-derived` and cite setup
   source rather than appearing as direct calls;
6. no high-impact false statement survives source verification; and
7. at least three of the six questions require fewer manual discovery reads or
   searches than the Phase 0 baseline.

Test-impact features remain unapproved until a separate corpus shows no missed
existing test for the golden paths. If any correctness gate fails, CodeGraph
remains absent; `gopls`, `go list`, and source search already provide the
required baseline.

## Layer 4: rendered deployment topology

### Role

KubeDiagrams generates an on-demand view of rendered Kubernetes resources and
their reference, selector, and ownership relationships. It is not a Go code
graph and it does not prove live network traffic or controller behavior.

Although the tool can ingest a Helm chart directly, CHILL will pass it the
output of the repository's own `helm template` command. This keeps the exact
release name, namespace, values, image settings, and chart validation path
visible and reproducible.

### Required render profiles

The first integration produces two separate diagrams:

1. the default chart render; and
2. the chart render with `profiler.enabled=true`.

Additional diagrams require a named values file or an exact recorded `--set`
list. Diagrams from different values sets are never merged and described as one
deployment.

### Execution and output

The initial implementation uses a pinned container image digest because Docker
is already available and the host lacks Graphviz. It does not use the hosted
web service and does not install Python or Graphviz globally.

The proposed targets are:

- `make deployment-graph-default`; and
- `make deployment-graph-profiler`.

Each target first runs the existing Helm validation, writes the rendered YAML
and Mermaid or SVG output to a temporary or caller-selected artifact directory,
and records:

- Git commit;
- chart version;
- release and namespace;
- values file hash and exact `--set` arguments;
- Helm version; and
- KubeDiagrams image digest.

Generated output is not committed by default. CI may generate and upload it for
a design-review workflow, but ordinary test CI does not depend on diagram
layout.

## Adoption sequence

### Phase 0: freeze the baseline and answer key — implemented

Before installing anything:

1. capture package and import edges with `go list`;
2. capture the two Helm render profiles and resource counts;
3. define six golden questions covering Profiler setup, interface dispatch,
   Evidence Store and PowerObserver boundaries, API impact, and rendered
   topology; and
4. record correct source locations and the number of search/read operations
   needed to answer each question in a `golden-queries.md` record beside this
   document.

Exit condition: the answer key is reproducible on the same commit and does not
depend on a generated graph. `make code-intelligence-baseline` and
[`golden-queries.md`](golden-queries.md) satisfy this condition for the current
recorded commit.

### Phase 1: establish `gopls` — implemented local baseline

Align and pin the tooling Go command and `gopls`, then validate LSP first. Add
detached or attached MCP only after semantic queries agree with LSP and source.

Exit condition: the Phase 1 acceptance gate passes. This becomes the minimum
adopted code-intelligence baseline. `make gopls-check` verifies the pinned
versions, workspace load, error-level diagnostics across non-spike Go files,
and the semantic golden queries. LSP and detached MCP client configuration
remain explicit developer choices.

### Phase 2: enforce package architecture — implemented

Encode only the initial invariants, make the component mapping complete for
that scope, and review the actual-versus-desired graph. Enable the CI gate only
after the current tree passes without broad exemptions.

Exit condition: `make arch-check` is deterministic locally and in CI, and an
intentional forbidden import causes the expected failure in a fixture or
temporary test change. The checked-in negative fixture verifies that
`power-core -> api` is rejected. `make arch-graph` writes an on-demand SVG to
the ignored `bin/` directory and is not a correctness gate.

### Phase 3: run the optional CodeGraph pilot — in progress (1/3 sessions)

Install it only in an isolated local tool location, apply the privacy and
instruction controls, and run the pilot gate. Do not add it to CI or require it
for contributors.

Exit condition: either record it as an optional local accelerator with its
known blind spots, or remove its MCP configuration and local index. Session 1
passed the provisional discovery-benefit threshold, but explicit sync does not
yet satisfy the MCP watcher condition and two separate sessions remain.

### Phase 4: add deployment graph targets

Pin the KubeDiagrams container, feed it validated rendered YAML, and verify the
default and Profiler-enabled profiles independently.

Exit condition: another developer can regenerate both diagrams from the
recorded commands and obtain the same resource relationships. Node placement
or layout does not need to be byte-identical.

## Freshness and ownership

| Artifact | Owner | Refresh rule | Stored in Git |
|---|---|---|---|
| `gopls` workspace state | developer or agent session | live for LSP; reconnect/reload for detached MCP | no |
| `.go-arch-lint.yml` | architecture reviewers | same change as an approved package-boundary decision | yes |
| package graph image | generating developer or CI | regenerate from current commit | no by default |
| `.codegraph/` index | individual developer | watcher plus explicit status check before reliance | never |
| rendered deployment YAML and diagram | generating developer or review workflow | regenerate for every commit and values set | no by default |

A generated artifact without commit and input metadata is stale by definition.

## Security and supply-chain controls

- Use local execution only; do not submit repository content to an online
  indexing or diagram service.
- Pin exact binary versions and container digests. Do not use `latest` in
  scripts or CI.
- Verify published checksums, signatures, or provenance when available.
- Do not document or automate `curl | sh` installation paths.
- Keep telemetry disabled for optional indexes.
- Review MCP tools as code-reading capabilities. They may read repository
  files, execute Go workspace-loading commands, write caches, and access module
  proxies under the same permissions as the invoking agent.
- Keep caches and generated indexes outside version control, and do not upload
  them from CI.
- A tool upgrade is a separate tooling change that reruns the relevant golden
  queries before merging.

## Failure and rollback

| Layer | Failure condition | Rollback |
|---|---|---|
| `gopls` MCP | stale or incorrect result, unstable client integration | remove MCP entry and retain normal LSP/source navigation |
| `go-arch-lint` | nondeterministic graph, unmaintainable component overlap, or unsupported imports | remove CI invocation while retaining the reviewed rule file and `go list` audit until corrected |
| CodeGraph | any pilot correctness gate failure or insufficient discovery benefit | remove MCP entry and `.codegraph/`; no baseline capability is lost |
| KubeDiagrams | incorrect relationships or unreproducible output | remove graph targets and retain `helm template` plus existing Helm validation |

Rollback never deletes architecture decisions, tests, or source evidence. It
only removes the failed view or enforcement mechanism.

## Success criteria

The adoption is successful when:

- Go symbols and interface implementations can be located against the current
  saved workspace without a repository-wide manual scan;
- a prohibited package import fails before merge;
- the package graph can be regenerated from reviewed rules and actual imports;
- deployment diagrams are tied to one exact Helm render profile;
- controller event paths are never mislabeled as direct calls; and
- removing the optional index does not change correctness, CI, or the ability
  to answer the golden questions.

## References

- [`gopls` overview and support policy](https://go.dev/gopls/)
- [`gopls` MCP support](https://go.dev/gopls/features/mcp)
- [`gopls` v0.23.0 release](https://go.dev/gopls/release/v0.23.0)
- [Go release history](https://go.dev/doc/devel/release)
- [`go-arch-lint` repository and configuration overview](https://github.com/fe3dback/go-arch-lint)
- [`go-arch-lint` v1.16.0 release](https://github.com/fe3dback/go-arch-lint/releases/tag/v1.16.0)
- [`go-arch-lint` configuration syntax](https://github.com/fe3dback/go-arch-lint/blob/v1.16.0/docs/syntax/README.md)
- [`go-arch-lint` graph command](https://github.com/fe3dback/go-arch-lint/blob/v1.16.0/docs/graph/README.md)
- [CodeGraph repository and local-index documentation](https://github.com/colbymchenry/codegraph)
- [CodeGraph v1.4.1 release](https://github.com/colbymchenry/codegraph/releases/tag/v1.4.1)
- [KubeDiagrams repository and input/output documentation](https://github.com/philippemerle/KubeDiagrams)
