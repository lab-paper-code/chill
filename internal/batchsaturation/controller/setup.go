package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers direct BatchSaturation events and
// framework-derived wakeups from its owned ProfilingRuns.
func (r *BatchSaturationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("batchsaturation").
		For(&edgev1alpha1.BatchSaturation{}).
		Owns(&edgev1alpha1.ProfilingRun{}).
		Complete(r)
}
