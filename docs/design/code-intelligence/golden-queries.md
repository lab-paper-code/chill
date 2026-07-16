# Code Intelligence Golden Queries

## Status

Phase 0 answer key captured for commit
`d0786e5644bad214c5a89762c17bbd1b828c7c89`. The Go and Helm source paths were
clean relative to that commit; unrelated design-document changes were present.

Phase 1 passed on 2026-07-16 with `gopls` v0.23.0 running its workspace Go
commands through Go 1.26.4. Workspace diagnostics and error-level diagnostics
across all non-spike Go files were zero. Advisory hints and style suggestions
remain visible in editors but are not promoted into a new repository lint gate.

Phase 2 passed on 2026-07-16 with `go-arch-lint` v1.16.0 built by the pinned Go
1.26.4 tooling command. All 29 in-scope production packages map to 17 reviewed
components. Test files are measured separately and are not enforced; the
current tree has one test-only internal edge attached to a production package,
plus three excluded `test/` packages with four internal edges. The intentional
`power-core -> api` fixture is rejected.

This is a regression oracle for code discovery. It is not an additional
architecture authority. When the underlying code intentionally changes, update
the answer and its evidence in the same change.

## Reproduce the baseline

```bash
make code-intelligence-baseline
```

Expected snapshot on the recorded commit:

| Measure | Value |
|---|---:|
| Non-spike Go files | 179 |
| Non-spike Go packages | 32 |
| Repository-internal non-spike import edges | 67 |
| Helm template files | 24 |
| Default rendered resources | 23 |
| Profiler-enabled rendered resources | 30 |

The command also prints the exact Profiler-only rendered resources and whether
Go, module, or chart inputs differ from the recorded commit.

## Query 1: how does a ProfilingRun reach reconciliation?

### Answer

The Profiler composition root constructs the concrete dependencies and calls
`IntentReconciler.SetupWithManager` in
`cmd/profiler/controller_setup.go`. Registration is defined in
`internal/profiler/controller/intent_setup.go`:

```text
For(ProfilingRun)
  + Owns(ConfigMap)
  -> controller-runtime event delivery
  -> IntentReconciler.Reconcile
```

The transition from registered events to `Reconcile` is
`framework-derived`, not a direct Go call. The registration source and focused
reconciler tests are required evidence.

### Baseline discovery work

One symbol search and reads of the composition root, setup file, and reconciler:
four discovery operations.

### Phase 1 semantic check

`gopls definition` on the composition-root call must return
`internal/profiler/controller/intent_setup.go`. Call hierarchy for `Reconcile`
may show direct test callers and the `Freeze` callee; it is not expected to
manufacture the controller-runtime event edge.

## Query 2: which implementation freezes a Run intent?

### Answer

`RunIntentFreezer` and its `Freeze` method are declared in
`internal/profiler/controller/intent_reconciler.go`. The production
implementation is `IntentFreezer` in
`internal/profiler/controller/intent_freezer.go`. A recording fake exists in
`intent_reconciler_test.go`.

`cmd/profiler/controller_setup.go` constructs `IntentFreezer` and injects it
into `NewIntentReconciler`.

### Baseline discovery work

One interface search plus three source reads: four discovery operations.

### Phase 1 semantic check

`gopls implementation` on `RunIntentFreezer` must include both the production
`IntentFreezer` and the test fake. Correctness claims use the composition root
to distinguish which implementation is used in production.

## Query 3: where is the Evidence Store adapter boundary?

### Answer

`internal/evidencestore` owns the product-neutral immutable object contract.
`internal/evidencestore/s3` imports that core and owns AWS-specific behavior.
The core does not import its S3 adapter.

On the recorded commit, non-spike production packages consume the core but do
not import `internal/evidencestore/s3`. The only current S3-adapter consumer is
the retained spike client. In particular, the Operator's Evidence Store operand
manager does not import the S3 client adapter.

### Baseline discovery work

One `go list` import query, one import search, and one source-boundary read:
three discovery operations.

### Verification

Actual package edges come from `go list`; source confirms the responsibility of
each side. A future package graph may display this result but does not replace
the import query.

## Query 4: where is the PowerObserver adapter boundary?

### Answer

`internal/powerobserver` owns bounded observation and contains no Kubernetes or
edge-metrics dependency. `internal/powerobserver/edgemetrics` imports the core
and implements the source adapter.

`internal/profiler/controller` currently consumes the edge-metrics adapter for
power-source fact resolution. `internal/profiler/evidence` and
`internal/profiler/worker` consume only the plain PowerObserver contract and
result types.

### Baseline discovery work

One `go list` import query, one import search, and two boundary reads: four
discovery operations.

### Verification

Use `go list` for the actual direction and the PowerObserver ADR for the allowed
direction. Do not infer runtime sampling behavior from the package edge.

## Query 5: what changes if ProfilingRunStatus.IntentDigest changes?

### Answer

The API field is declared in `api/v1alpha1/profilingrun_types.go`. Semantic
references include:

- intent recovery and publication in
  `internal/profiler/controller/intent_freezer.go`;
- frozen-intent projection in
  `internal/profiler/controller/projection.go`;
- controller unit tests; and
- the ProfilingRun API integration test.

Generated deepcopy code is type-generated evidence, not a separately designed
consumer. Changing JSON shape also requires generated CRD and Helm CRD checks,
which symbol references alone do not prove.

### Baseline discovery work

One text search and at least three representative source/test reads: four
discovery operations before generated-artifact verification.

### Phase 1 semantic check

`gopls references` on the field must include the freezer, projection, and API
integration test locations.

## Query 6: what does enabling the Profiler add to the Helm render?

### Answer

The default render contains 23 resources. Setting `profiler.enabled=true`
contains 30 and adds exactly:

```text
ClusterRole/chill-profiler
ClusterRoleBinding/chill-profiler
ConfigMap/chill-profiler-config
ConfigMap/chill-profiler-facts
Role/chill-profiler
RoleBinding/chill-profiler
ServiceAccount/chill-profiler
```

The Helm render does not add a Profiler Deployment. Helm installs the identity,
configuration, and permissions; the CHILL Operator manages the Profiler operand
Deployment. A deployment diagram must preserve this ownership distinction.

### Baseline discovery work

Two Helm renders plus one resource-set comparison: three discovery operations.

### Verification

Regenerate both manifests with the same release and namespace. Template-source
search can explain a condition but is not the desired-state authority.

## Local usage

Install and validate the pinned local tool:

```bash
make gopls
make gopls-check
```

Install and run the pinned package-architecture gate, including coverage and
the intentional rejection fixture:

```bash
make arch-check
```

Render the reviewed package graph on demand to the ignored local `bin/`
directory:

```bash
make arch-graph
```

Run the complete Phase 0 through Phase 2 verification:

```bash
make code-intelligence-check
```

For an editor, use `hack/gopls-chill.sh` as the language-server executable. For
a manually configured detached MCP server, use:

```bash
./hack/gopls-chill.sh mcp
```

The wrapper selects the supported tooling Go command while the module continues
to target Go 1.22.12. It reads only the repository-local ignored binary. No
interactive installer or automatic agent-configuration edit is part of Phase
1.
