# Code Intelligence Golden Queries

This file is the current regression oracle for code discovery. It does not
create architecture policy. Update an answer and its evidence in the same
change when the underlying implementation intentionally changes.

## Q1. How does a ProfilingRun reach reconciliation?

`cmd/profiler/controller_setup.go` constructs the dependencies and calls
`IntentReconciler.SetupWithManager`. Registration in
`internal/profiler/controller/intent_setup.go` uses:

```text
For(ProfilingRun) + Owns(ConfigMap)
  -> controller-runtime event delivery
  -> IntentReconciler.Reconcile
```

Required evidence: setup source and focused reconciler tests. The event edge is
`framework-derived`, not a direct Go call.

## Q2. Which implementation freezes a Run intent?

`RunIntentFreezer` is declared in
`internal/profiler/controller/intent_reconciler.go`. Production uses
`IntentFreezer` from `intent_freezer.go`; `intent_reconciler_test.go` contains a
recording fake. `cmd/profiler/controller_setup.go` constructs and injects the
production implementation.

Required evidence: `gopls implementation` must find production and test
implementations; the composition root determines which one production uses.

## Q3. Where is the Evidence Store adapter boundary?

`internal/evidencestore` owns the product-neutral immutable-object contract.
`internal/evidencestore/s3` imports that core and owns AWS-specific behavior;
the core does not import the adapter. Production Operator packages do not
consume the S3 client adapter.

Required evidence: `go list` for actual imports and source for responsibilities.

## Q4. Where is the MetricObserver adapter boundary?

`internal/metricobserver` owns the plain observation contract.
`internal/metricobserver/edgemetrics` imports and implements that contract.
Profiler controller code consumes the adapter for metric-source fact resolution;
Profiler evidence and worker packages consume only the plain contract and
result types.

Required evidence: `go list` for actual imports and the MetricObserver ADR for
allowed direction. Package edges do not prove runtime sampling behavior.

## Q5. What changes if ProfilingRunStatus.IntentDigest changes?

The field is declared in `api/v1alpha1/profilingrun_types.go`. Current semantic
references include intent recovery and publication in `intent_freezer.go`,
frozen-intent projection in `projection.go`, controller tests, and the
ProfilingRun API integration test.

Required evidence: `gopls references`, direct source and tests, plus generated
deepcopy, CRD, and Helm CRD checks for type or JSON-shape changes. CodeGraph
impact and affected-test results are explicitly insufficient.

## Q6. What does enabling the Profiler add to the Helm render?

Compared with the default profile, `profiler.enabled=true` adds:

```text
ClusterRole/chill-profiler
ClusterRoleBinding/chill-profiler
ConfigMap/chill-profiler-config
ConfigMap/chill-profiler-facts
Role/chill-profiler
RoleBinding/chill-profiler
ServiceAccount/chill-profiler
```

It does not add a Profiler Deployment. Helm installs identity, configuration,
and permissions; the CHILL Operator manages the operand Deployment.

Required evidence: two validated Helm renders using the same release and
namespace, followed by a resource-set comparison. The optional
`make deployment-graph-check` command verifies the review view without
replacing that evidence.

## Regression commands

```bash
make code-intelligence-baseline
make gopls-check
make arch-check
make code-intelligence-check
```

The baseline command prints current file, package, import-edge, and rendered
resource counts. Treat those generated values as a snapshot, not permanent
requirements.
