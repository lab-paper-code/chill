package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers only direct ProfilingRun and owned intent-record
// events. Fact resolution remains an exact API read during preparation.
func (r *IntentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("profilingrun-intent").
		For(&edgev1alpha1.ProfilingRun{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
