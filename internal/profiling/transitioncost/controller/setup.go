package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// setupWithManager registers the enclosing measurement and its owned
// Kubernetes records. Pod receipt reads do not require a Pod watch.
func (r *powerModeTransitionCostReconciler) setupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		Named("powermodetransitioncost").
		For(&edgev1alpha1.PowerModeTransitionCost{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Owns(&edgev1alpha1.NodeActuation{}).
		Complete(r)
}
