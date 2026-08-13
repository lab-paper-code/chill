# CHILL Agent Instructions

These instructions apply to the whole repository. Keep this file short; the
linked design documents own detailed rationale and changing implementation
facts.

## What this repository is for

CHILL is a research prototype. Its purpose is to fill the measurement tables in
`docs/spec/`. Code earns its place by running an experiment or producing a table
or figure; if it does neither, defer it.

- Do not generalize for compatibility or failure modes that have not occurred.
  No epoch, fence, tenure, multi-version reader, or recovery state machine
  without a measurement that requires one.
- Abstraction that standardizes a repeatedly measured quantity is the welcome
  exception. It maps to a spec variable, repeats across device, model, and state,
  applies one measurement window and exclusion rule, retains raw trials, and
  feeds a table directly.
- A behavior that must itself be measured does not live inside the lifecycle of
  the thing measuring it. Extract it as an atomic operation a campaign emits as
  one step, so a new experiment becomes a new assembly instead of a new
  mechanism.
- Prefer deleting to generalizing. The testbed is cleaned deliberately.

## Start and scope

- Inspect `git status --short` before editing and preserve unrelated user work.
- Read only the relevant entry point in `docs/design/README.md`; do not load the
  entire design tree by default.
- Use `docs/design/code-intelligence/README.md` for semantic-navigation,
  package-boundary, optional indexing, and deployment-topology evidence rules.
- Treat `docs/design/code-intelligence/golden-queries.md` as a regression oracle,
  not as a substitute for current source.

## Evidence routing

Use the cheapest authoritative evidence for the claim being made.

| Claim | Required evidence |
|---|---|
| Go definition, reference, implementation, or call hierarchy | pinned `gopls`, then the returned source |
| Actual package dependency | `go list` |
| Allowed package dependency | `.go-arch-lint.yml` and the relevant design or ADR |
| Controller event path | exact `For`, `Owns`, `Watches`, or mapping registration plus focused tests |
| Behavioral impact or test coverage | source and tests |
| Desired Kubernetes resources | validated `helm template` with explicit release, namespace, values, and `--set` inputs |
| Live runtime state | an explicitly captured cluster snapshot |

- CodeGraph is optional candidate discovery only. Check index freshness before
  using it and confirm every conclusion with the authority above.
- KubeDiagrams output is a review view only. The rendered manifest and sorted
  resource set remain authoritative.
- Describe controller-runtime event delivery as `framework-derived`; do not
  invent a direct Go call from setup code to `Reconcile`.

## Efficient change workflow

1. Locate the smallest relevant surface with `rg`, `gopls`, and direct reads.
   Use CodeGraph only when the entry point or affected package is genuinely
   unclear and indexing reduces discovery work.
2. Verify the current behavior and boundary before editing.
3. Make the smallest coherent change and add focused tests.
4. Run checks selected by the changed surface; do not run graph tooling for an
   unrelated Go-only change.
5. Before handoff, run the applicable repository gates and report anything not
   run.

| Changed surface | Minimum applicable checks |
|---|---|
| Go behavior within one package | focused `go test`, then `make lint` and `make test` before handoff |
| New production package or internal import | `make arch-check` |
| Go semantic path covered by the golden queries | `make gopls-check` or `make code-intelligence-check` |
| API types or generated Kubernetes artifacts | `make manifests generate`, inspect generated drift, then relevant tests |
| Helm templates or values | `make helm-lint helm-template` and both default and Profiler renders as applicable |
| Desired deployment topology | `make deployment-graph-check`, then inspect the manifest/resource delta before the SVG |
| Full local code-intelligence review | `make code-intelligence-review` |

Normal generated-artifact, lint, test, Helm, and live-environment validation are
not replaced by code-intelligence checks.

## Architecture changes

- Every production package under `api/`, `cmd/`, or `internal/` must map to one
  reviewed component.
- If an approved dependency boundary changes, update `.go-arch-lint.yml` and
  the relevant design or ADR in the same change.
- `cmd/**` packages are composition roots; their broad wiring permission does
  not relax internal package direction.
- Tests, `test/`, `spikes/`, and `hack/` are not architecture merge gates.
  Measure or test them with their owning mechanisms instead of claiming they
  are enforced.

## Configured behavior

- Values that decide what a Run measures, or how its evidence is judged, belong
  in configuration, not in Go constants. Reproducibility comes from freezing the
  selected entry and source identity into the immutable Run record, not from
  compiling the value in.
- Do not give a policy struct fields that accept only one value. Either accept
  the range or make it a constant.
- Prefer a maintained library over a hand-rolled numeric, statistical, or
  protocol primitive. Never ship a silent approximation; return an error outside
  the range actually implemented.

## Verification boundaries

- Do not change a stored evidence or object contract while a measurement
  campaign depends on it. Change it once per work unit, and delete the stranded
  objects rather than building a reader for them
  ([ADR 0019](docs/adr/0019-durable-stored-contract-compatibility.md)).
- Do not add digest format, recomputation, or comparison as a validation gate.
- Do not add byte-for-byte equality or canonical re-encoding comparisons as a
  validation gate.
- Validate boundary inputs structurally and semantically, not by their encoding.
- Verify once at that boundary, return a type only that boundary can construct,
  and do not re-validate it downstream.
- Keep canonicalization separate from validation so a caller can take one without
  the other.

## Deployment and safety

- Keep default and `profiler.enabled=true` Helm profiles separate. Never present
  a combined diagram as one deployable state.
- A deployment graph does not prove live resources, readiness, traffic,
  reconciliation, or minimum RBAC privilege.
- Do not modify a live cluster, publish images, or change external agent
  configuration unless the user explicitly requests that action.
- Never run e2e tests against the testbed kubeconfig. Use the repository-local
  `kind-*` context described in `README.md`.
- Keep generated indexes and diagrams ignored; do not commit or upload them.

## Handoff

Report the change, the evidence used, commands run and their results, checks not
run, remaining worktree changes, and whether live-cluster behavior was verified.
