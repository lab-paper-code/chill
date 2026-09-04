package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers ProfilingRun and owned Job events. Configuration
// resolution remains an exact API read before the Job is created.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("profilingrun").
		For(&edgev1alpha1.ProfilingRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
