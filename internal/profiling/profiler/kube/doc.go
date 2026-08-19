// Package kube implements the Profiler's read-side Kubernetes adapters.
//
// It resolves trusted catalog facts, configured policies, Nodes, metric
// sources, workload-observation delivery, and the current Evidence Store
// binding. Reconciliation and owned-object writes remain in the sibling
// controller package; executable entrypoints assemble both packages.
package kube
