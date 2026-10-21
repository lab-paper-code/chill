package controller

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManager registers direct baseline events and framework-derived
// wakeups from its owned ProfilingRuns. The Node's power mode is polled with
// RequeueAfter while it settles.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("modelloadedidlebaseline").
		For(&edgev1alpha1.ModelLoadedIdleBaseline{}).
		Owns(&edgev1alpha1.ProfilingRun{}).
		Complete(r)
}
