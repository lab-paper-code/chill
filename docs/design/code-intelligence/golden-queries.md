# Code Intelligence Golden Queries

This file is the current regression oracle for code discovery. It does not
create architecture policy. Update an answer and its evidence in the same
change when the underlying implementation intentionally changes.

## Q1. How does a ProfilingRun reach reconciliation?

`cmd/profiler/controller_setup.go` constructs the shared dependencies and calls
`RunReconciler.SetupWithManager`. Registration in
`internal/profiling/profiler/controller/run_setup.go` uses:

```text
For(ProfilingRun) + Owns(Job)
  -> controller-runtime event delivery
  -> RunReconciler.Reconcile
```

Required evidence: setup source and focused reconciler tests. The event edge is
`framework-derived`, not a direct Go call.

## Q2. Which implementation resolves a RunConfig?

`RunConfigurationResolver` is declared in
`internal/profiling/profiler/controller/run_reconciler.go`. Production uses
`RunConfigResolver` from `run_config_resolver.go`; `run_reconciler_test.go`
contains a recording fake. `cmd/profiler/controller_setup.go` constructs and
injects the production implementation.

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

## Q5. Where is the exact RunConfig recovered after controller restart?

`resolvedRunFromJob` in
`internal/profiling/profiler/controller/run_record.go` decodes and validates the
RunConfig, execution policy, and optional actuation policy carried by the owned
Job. It is used by the reconciler, Job recovery, and Job drift validation. There is no
separate ConfigMap execution record or intermediate resolution status phase.

Required evidence: `gopls references`, direct source, focused controller tests,
and the generated ProfilingRun CRD. CodeGraph impact and affected-test results
are explicitly insufficient.

## Q6. What does enabling the Profiler add to the Helm render?

Compared with the default profile, `profiler.enabled=true` plus the review-only
synthetic transition-observer Node adds:

```text
ClusterRole/chill-profiler
ClusterRole/chill-profiler-transition-observer
ClusterRole/chill-profiler-workload-observer
ClusterRoleBinding/chill-profiler
ClusterRoleBinding/chill-profiler-transition-observer
ClusterRoleBinding/chill-profiler-workload-observer
ConfigMap/chill-profiler-config
ConfigMap/chill-profiler-facts
Role/chill-profiler
Role/chill-profiler-transition-observer
RoleBinding/chill-profiler
RoleBinding/chill-profiler-transition-observer
ServiceAccount/chill-profiler
ServiceAccount/chill-profiler-transition-observer
ServiceAccount/chill-profiler-workload-observer
```

It does not add a Profiler Deployment. Helm installs identity, configuration,
and permissions; the CHILL Operator manages the operand Deployment.

Required evidence: two validated Helm renders using the same release and
namespace, followed by a resource-set comparison. The optional
`make deployment-graph-check` command verifies the review view without
replacing that evidence.

## Q7. How does a BatchEnergyOptimization search advance?

`cmd/profiler/controller_setup.go` reuses the Profiler's candidate, preparation,
Job-backed Run record, Evidence Store, and retained-evidence dependencies and calls the
batch-energy optimization reconciler's `SetupWithManager`. Registration in
`internal/profiling/campaign/controller/setup.go` uses:

```text
For(BatchEnergyOptimization) + Owns(NodeActuation) + Owns(ProfilingRun)
  -> controller-runtime event delivery
  -> BatchEnergyOptimizationReconciler.Reconcile
```

The reconciler lists labeled child Runs and the projection verifies the exact
controller-owner UID before pure decision logic is called. Required evidence:
setup and projection source plus the two focused Profiler envtest campaigns.
The event edge is `framework-derived`, not a direct Go call.

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
