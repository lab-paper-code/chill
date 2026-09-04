package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers direct BatchEnergyOptimization events and
// framework-derived wakeups from its owned transitions and ProfilingRuns.
func (r *BatchEnergyOptimizationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("batchenergyoptimization").
		For(&edgev1alpha1.BatchEnergyOptimization{}).
		Owns(&edgev1alpha1.NodeActuation{}).
		Owns(&edgev1alpha1.ProfilingRun{}).
		Complete(r)
}
