package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers direct ProfilingRun and owned durable-transition
// events. Fact resolution remains an exact API read during preparation.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("profilingrun").
		For(&edgev1alpha1.ProfilingRun{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
